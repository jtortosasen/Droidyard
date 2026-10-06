package droidyard.sidecar

import org.gradle.tooling.GradleConnector
import org.gradle.tooling.GradleConnectionException
import org.gradle.tooling.LongRunningOperation
import org.gradle.tooling.ProjectConnection
import org.gradle.tooling.model.GradleProject
import org.gradle.tooling.model.GradleTask
import org.gradle.tooling.model.build.BuildEnvironment
import org.gradle.tooling.CancellationTokenSource
import java.io.BufferedReader
import java.io.File
import java.io.InputStreamReader
import java.nio.charset.StandardCharsets.UTF_8
import java.util.Collections
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.system.exitProcess

/** The protocol version core and sidecar agree on; a mismatch is refused by the core. */
const val PROTOCOL_VERSION = 1

/** The sidecar's own version, reported by `hello` (the core reports it in health). */
const val SIDECAR_VERSION = "1.0.0"

/**
 * One root's Gradle connection and its lock. Every TAPI call for a root is serialised on
 * [lock] (plan m1g R5, `gradle.md` §Concurrency): a model fetch during a build would make
 * Gradle start a second daemon, because a busy daemon is not reused.
 *
 * [javaHome] is the JVM each operation runs on (D6, step 1 fix round), never the Gradle
 * installation; [env] is the set of environment overrides, or null when there are none.
 */
class Root(
    val path: String,
    val connection: ProjectConnection,
    val javaHome: String?,
    val env: Map<String, String>?,
    val lock: Any,
    @Volatile var capabilities: Json = obj(),
)

/**
 * Applies [javaHome] and the environment [overrides] to an operation (plan m1g D6, step 1
 * fix round): `setJavaHome` selects the JVM of every TAPI call, never the Gradle
 * installation, and the overrides are merged over the sidecar's inherited environment so
 * that `{}` or an absent `env` means "inherit", never an empty environment.
 */
fun applyRootOptions(op: LongRunningOperation, javaHome: String?, overrides: Map<String, String>?) {
    if (!javaHome.isNullOrEmpty()) op.setJavaHome(File(javaHome))
    val env = mergedEnvironment(overrides)
    if (env != null) op.setEnvironmentVariables(env)
}

/** The overrides merged over [System.getenv]; null when there are none to apply. */
fun mergedEnvironment(overrides: Map<String, String>?): Map<String, String>? {
    if (overrides.isNullOrEmpty()) return null
    val merged = LinkedHashMap<String, String>(System.getenv())
    merged.putAll(overrides)
    return merged
}

/**
 * The sidecar's session: reads JSON-RPC lines, dispatches them, and owns the roots, the
 * builds and the shutdown path. The reader is a thread of its own so that a long TAPI
 * call cannot hold off the stdin-EOF that must end the process (plan m1g, `finish`).
 */
class Session(private val proto: Protocol) {
    private val roots = ConcurrentHashMap<String, Root>()
    private val locks = ConcurrentHashMap<String, Any>()
    private val builds = ConcurrentHashMap<String, Build>()
    private val cancels: MutableList<CancellationTokenSource> =
        Collections.synchronizedList(ArrayList())
    // One dispatcher thread, so requests are answered in the order they arrive (a
    // `connection.open` cannot race a `project.model` behind it). A build runs on its
    // own thread, so `build.cancel` is still served while a build runs.
    private val pool = Executors.newSingleThreadExecutor { r -> Thread(r, "rpc").apply { isDaemon = true } }

    /**
     * build.cancel, ping and shutdown: a second single thread, so they never queue
     * behind a model fetch on [pool] and a blocking TAPI cancel callback never stops the
     * reader from seeing EOF (step 1 re-check, N-a).
     */
    private val control = Executors.newSingleThreadExecutor { r -> Thread(r, "control").apply { isDaemon = true } }

    /**
     * Cancels that arrived before their build.run was dispatched (the run still queued
     * on [pool]): the build is cancelled before it starts, so it ends `cancelled` with one
     * `finished` (step 1 re-check, R1). Guarded by [startLock]; bounded, oldest dropped,
     * since a late cancel of a finished build also lands here.
     */
    private val cancelledBeforeStart = LinkedHashSet<String>()
    private val startLock = Any()
    private val stopping = AtomicBoolean(false)
    private val testStdout = System.getProperty("droidyard.sidecar.testStdout") != null

    /** Reads request lines until stdin ends, then exits (D6: EOF is the kill switch). */
    fun serve() {
        if (testStdout) System.out.println("sidecar: started, protocol $PROTOCOL_VERSION")
        val reader = BufferedReader(InputStreamReader(System.`in`, UTF_8), 1 shl 16)
        while (true) {
            val line = try {
                reader.readLine() ?: break
            } catch (e: Exception) {
                break
            }
            if (line.isBlank()) continue
            handleLine(line)
        }
        onEof()
    }

    private fun handleLine(line: String) {
        val request = try {
            Decoder.parse(line)
        } catch (e: JsonDecodeException) {
            proto.error(null, Protocol.PARSE_ERROR, "parse error: ${e.message}")
            return
        }
        if (request !is Json.Obj) {
            proto.error(null, Protocol.INVALID_REQUEST, "request is not a JSON object")
            return
        }
        val method = request.optString("method")
        if (method == null) {
            proto.error(request.field("id"), Protocol.INVALID_REQUEST, "request has no method")
            return
        }
        val hasId = request.field("id") != null
        val id = request.field("id") ?: Json.Null
        val params = request.field("params") ?: Json.Obj(LinkedHashMap())
        // build.cancel, ping and shutdown are answered on the reader thread (plan m1g
        // step 1 fix round, N3): they must not queue behind a connection.open or a
        // project.model blocked on a root's lock, or a cancel could not reach a running
        // build. Every other method keeps the single dispatcher, so requests are answered
        // in the order they arrive.
        if (method == "build.cancel" || method == "ping" || method == "shutdown") {
            control.execute { dispatch(hasId, id, method, params) }
        } else {
            pool.execute { dispatch(hasId, id, method, params) }
        }
    }

    private fun dispatch(hasId: Boolean, id: Json, method: String, params: Json) {
        if (testStdout) System.out.println("sidecar: serving $method")
        try {
            when (method) {
                "hello" -> reply(hasId, id, hello())
                "ping" -> reply(hasId, id, obj())
                "connection.open" -> reply(hasId, id, openConnection(params))
                "connection.close" -> { closeConnection(params); reply(hasId, id, obj()) }
                "project.model" -> reply(hasId, id, projectModel(params))
                "build.run" -> reply(hasId, id, startBuild(params))
                "build.cancel" -> { cancelBuild(params); reply(hasId, id, obj()) }
                "shutdown" -> { reply(hasId, id, obj()); shutdown() }
                // An unknown method that arrived as a notification (no id) gets no
                // response (plan m1g step 1 fix round, N9; JSON-RPC 2.0).
                else -> if (hasId) {
                    proto.error(id, Protocol.METHOD_NOT_FOUND, "Method not found: ${capUtf8(method, 8 shl 10)}")
                }
            }
        } catch (e: RpcException) {
            if (hasId) proto.error(id, e.rpcCode, e.rpcMessage, e.data)
        } catch (e: Throwable) {
            if (hasId) proto.error(id, Protocol.INTERNAL_ERROR, capUtf8(e.message ?: e.javaClass.simpleName, 8 shl 10))
        }
    }

    private fun reply(hasId: Boolean, id: Json, result: Json) {
        if (hasId) proto.result(id, result)
    }

    // --- methods ------------------------------------------------------------------

    private fun hello(): Json {
        val cls = GradleConnector::class.java
        val tapi = cls.`package`?.implementationVersion ?: "unknown"
        return obj(
            "protocolVersion" to num(PROTOCOL_VERSION),
            "sidecarVersion" to str(SIDECAR_VERSION),
            "tapiVersion" to str(tapi),
            "java" to obj(
                "home" to str(System.getProperty("java.home") ?: ""),
                "version" to str(System.getProperty("java.version") ?: ""),
            ),
        )
    }

    private fun openConnection(request: Json): Json {
        val rootPath = request.optString("root")
            ?: throw RpcException.application("connection.open needs a root")
        val javaHome = request.optString("javaHome")
        val env = request.optEnv("env")
        val dir = File(rootPath)
        if (!dir.isDirectory) throw RpcException.application("root is not a directory: $rootPath")
        val key = try { dir.canonicalPath } catch (e: Exception) { dir.absolutePath }
        val lock = locks.computeIfAbsent(key) { Any() }
        synchronized(lock) {
            if (roots.containsKey(key)) throw RpcException.application("root is already open: $key")
            val connector = GradleConnector.newConnector().forProjectDirectory(dir)
            // D19: a project with a wrapper uses it; one without falls back to the
            // Tooling API's own distribution ("connector"), which is what the source
            // field reports.
            val wrapper = File(dir, "gradle/wrapper/gradle-wrapper.properties").isFile
            val source = if (wrapper) "wrapper" else "connector"
            if (wrapper) connector.useBuildDistribution()
            // javaHome is NOT the Gradle installation (D6, step 1 fix round): it is the JVM
            // of each TAPI call, applied here and on every later operation. useInstallation
            // is deliberately absent.
            val connection = try {
                connector.connect()
            } catch (e: GradleConnectionException) {
                throw RpcException.application("Gradle connection failed", e.message)
            }
            val cts = GradleConnector.newCancellationTokenSource()
            cancels.add(cts)
            try {
                val builder = connection.model(BuildEnvironment::class.java)
                    .withCancellationToken(cts.token())
                applyRootOptions(builder, javaHome, env)
                val environment = builder.get()
                val version = environment.gradle.gradleVersion
                val daemonJavaHome = try {
                    environment.java?.javaHome?.absolutePath
                } catch (e: Exception) {
                    null
                }
                val capabilities = capabilitiesFor(version)
                roots[key] = Root(key, connection, javaHome, env, lock, capabilities)
                return obj(
                    "gradleVersion" to str(version),
                    "source" to str(source),
                    "daemonJavaHome" to daemonJavaHome?.let { str(it) },
                    "capabilities" to capabilities,
                )
            } catch (e: Throwable) {
                try { connection.close() } catch (ignored: Exception) {}
                throw RpcException.application("BuildEnvironment failed", e.message)
            } finally {
                cancels.remove(cts)
                cts.cancel()
            }
        }
    }

    private fun closeConnection(request: Json) {
        val rootPath = request.optString("root")
            ?: throw RpcException.application("connection.close needs a root")
        val key = canonicalOrAbsolute(rootPath)
        val root = roots.remove(key) ?: return
        synchronized(root.lock) {
            try { root.connection.close() } catch (e: Exception) { /* already gone */ }
        }
    }

    private fun rootOf(request: Json, method: String): Root {
        val rootPath = request.optString("root")
            ?: throw RpcException.application("$method needs a root")
        return roots[canonicalOrAbsolute(rootPath)]
            ?: throw RpcException.application("no connection.open for root: $rootPath")
    }

    /**
     * `project.model` returns the generic TAPI model (`GradleProject`), not the probe
     * (plan m1g D25): modules, tasks and directories, which is all `project.*` and
     * `build.tasks` need before staging 3.
     */
    private fun projectModel(request: Json): Json {
        val root = rootOf(request, "project.model")
        val cts = GradleConnector.newCancellationTokenSource()
        cancels.add(cts)
        try {
            synchronized(root.lock) {
                val builder = root.connection.model(GradleProject::class.java)
                    .withCancellationToken(cts.token())
                applyRootOptions(builder, root.javaHome, root.env)
                val project = builder.get()
                val rootDir = File(root.path)
                val projects = ArrayList<Json>()
                collectProjects(rootDir, project, projects)
                return obj("root" to str(root.path), "projects" to arr(projects))
            }
        } catch (e: GradleConnectionException) {
            throw RpcException.application("project.model failed", e.message)
        } finally {
            cancels.remove(cts)
            cts.cancel()
        }
    }

    private fun collectProjects(rootDir: File, project: GradleProject, out: MutableList<Json>) {
        val tasks = ArrayList<Json>()
        val set: Collection<GradleTask>? = project.tasks
        set?.forEach { t ->
            tasks.add(
                obj(
                    "name" to str(t.name ?: ""),
                    "path" to str(t.path ?: ""),
                    "group" to str(t.group ?: ""),
                    "description" to str(t.description ?: ""),
                    "public" to bool(t.isPublic),
                )
            )
        }
        val dir = try { project.projectDirectory } catch (e: Exception) { null }
        val dirText = when {
            dir == null -> "."
            else -> relativeTo(rootDir, dir)
        }
        val buildDir = try { project.buildDirectory?.absolutePath } catch (e: Exception) { null }
        out.add(
            obj(
                "path" to str(project.path ?: ":"),
                "name" to str(project.name ?: ""),
                "dir" to str(dirText),
                "buildDir" to buildDir?.let { str(it) },
                "tasks" to arr(tasks),
            )
        )
        project.children?.forEach { child -> collectProjects(rootDir, child, out) }
    }

    private fun relativeTo(root: File, dir: File): String {
        return try {
            val rel = root.toPath().toAbsolutePath().normalize()
                .relativize(dir.toPath().toAbsolutePath().normalize()).toString()
            if (rel.isEmpty()) "." else rel
        } catch (e: Exception) {
            dir.absolutePath
        }
    }

    private fun startBuild(request: Json): Json {
        val root = rootOf(request, "build.run")
        val buildId = request.optString("buildId")
            ?: throw RpcException.application("build.run needs a buildId")
        val tasks = request.optStrings("tasks")?.filter { it.isNotEmpty() }
        if (tasks.isNullOrEmpty()) throw RpcException.application("build.run needs at least one task")
        if (builds.containsKey(buildId)) throw RpcException.application("build is already running: $buildId")
        val build = Build(buildId, root, proto, tasks) { done -> buildFinished(done) }
        synchronized(startLock) {
            builds[buildId] = build
            if (cancelledBeforeStart.remove(buildId)) build.cancel()
        }
        // A failed Thread.start must not leave the build registered (plan m1g step 1 fix
        // round, N4): the caller gets an error and no notification is ever sent.
        val failure = build.start()
        if (failure != null) {
            builds.remove(buildId)
            throw RpcException.application("could not start the build", failure.message)
        }
        return obj("accepted" to bool(true))
    }

    private fun cancelBuild(request: Json) {
        val buildId = request.optString("buildId")
            ?: throw RpcException.application("build.cancel needs a buildId")
        synchronized(startLock) {
            val build = builds[buildId]
            if (build != null) {
                build.cancel()
                return
            }
            // Not started yet (its build.run still queued) or already finished: remember
            // it, so a run that is still coming starts cancelled.
            cancelledBeforeStart.add(buildId)
            if (cancelledBeforeStart.size > 256) cancelledBeforeStart.remove(cancelledBeforeStart.first())
        }
    }

    /** Called by a build when it is done, whatever its outcome. */
    fun buildFinished(buildId: String) {
        builds.remove(buildId)
    }

    // --- lifecycle ----------------------------------------------------------------

    private fun shutdown() {
        if (!stopping.compareAndSet(false, true)) return
        armHalt()
        // The close can block (a Gradle daemon that does not answer); it runs on its own
        // thread behind the halt guard, so shutdown never waits on it (N1/N2).
        Thread {
            cancelAll()
            for (root in roots.values) {
                try { root.connection.close() } catch (e: Exception) {}
            }
            exitProcess(0)
        }.start()
    }

    /** stdin EOF (D6, `process-lifecycle.md` layer 1): cancel everything and exit. */
    private fun onEof() {
        if (!stopping.compareAndSet(false, true)) return
        armHalt()
        cancelAll()
        // Brief grace so a notification already being written is not cut mid-line, then
        // exit well inside the five seconds the protocol promises.
        Thread.sleep(100)
        exitProcess(0)
    }

    /**
     * Starts a daemon thread that halts the JVM after four seconds (plan m1g step 1 fix
     * round, N1/N2): the graceful exit must never be held hostage by a blocked TAPI call,
     * a Gradle daemon that will not answer, or a shutdown hook.
     */
    private fun armHalt() {
        Thread {
            try { Thread.sleep(4000) } catch (e: InterruptedException) {}
            Runtime.getRuntime().halt(0)
        }.apply { isDaemon = true; name = "halt" }.start()
    }

    private fun cancelAll() {
        synchronized(cancels) {
            for (cts in cancels) {
                try { cts.cancel() } catch (e: Exception) {}
            }
        }
        for (build in builds.values) build.cancel()
    }

    private fun canonicalOrAbsolute(path: String): String {
        val f = File(path)
        return try { f.canonicalPath } catch (e: Exception) { f.absolutePath }
    }

    companion object {
        /**
         * `GradleCapabilities` from the Gradle version (`gradle.md` §Version detection
         * and toolchain profile, item 2; ADR-0018 rule 1). A flag is true from the
         * release its API arrived in; the core drops the profile to `degraded` when a
         * gated call later throws.
         */
        fun capabilitiesFor(version: String): Json {
            val parts = version.split('.', '-', limit = 3)
            val major = parts.getOrNull(0)?.toIntOrNull() ?: 0
            val minor = parts.getOrNull(1)?.toIntOrNull() ?: 0
            fun at(ma: Int, mi: Int) = major > ma || (major == ma && minor >= mi)
            return obj(
                "problemEvents" to bool(at(8, 12)),
                "problemLocations" to bool(at(8, 13)),
                "detailedFailure" to bool(at(8, 12)),
                "problemSummaries" to bool(at(8, 12)),
                "buildPhaseEvents" to bool(at(7, 6)),
                "testSpecFilters" to bool(at(7, 6)),
                "versionInfo" to bool(at(9, 4)),
                "taskProvenance" to bool(at(8, 0)),
            )
        }
    }
}
