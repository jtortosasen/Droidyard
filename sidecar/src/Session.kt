package droidyard.sidecar

import droidyard.probe.AndroidModel
import droidyard.probe.ProbeModel
import droidyard.probeaction.ProbeAction
import droidyard.probeaction.ProbeResult
import droidyard.probeaction.ProjectFacts
import org.gradle.tooling.Failure
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
const val PROTOCOL_VERSION = 2

/**
 * The launch argument naming the probe's init script (plan m1h D7): the daemon writes it
 * into the cache and passes `-Ddroidyard.probe.init=<path>` on the sidecar's command line,
 * so the init script and the jar beside it are the daemon's, never the project's.
 */
private const val PROBE_INIT_PROPERTY = "droidyard.probe.init"

/**
 * Test-only: extra Tooling API arguments for the probed fetch alone (plan m1h step 2, the
 * 1g `testStdout` pattern). Only the opt-in tests launch the sidecar with it; in production
 * it is unset, so it changes nothing.
 */
private const val TEST_PROBE_ARGS_PROPERTY = "droidyard.sidecar.testProbeArgs"

/** Test-only: another init script, so a broken one can be exercised (plan m1h step 2). */
private const val TEST_PROBE_INIT_PROPERTY = "droidyard.sidecar.testProbeInit"

/**
 * A frame in a class of this package makes a failure the probe's (plan m1h D5). The
 * trailing dot is load-bearing: the `BuildAction` lives in `droidyard.probeaction`, which
 * must never match, or every configuration failure — always under the action's frames —
 * would be retried as the probe's.
 */
private const val PROBE_FRAME_PREFIX = "droidyard.probe."

/** The failure chain's bounds (plan m1h D5; m1g D6's 8 KiB and count). */
private const val MAX_CAUSE_CHARS = 8 * 1024
private const val MAX_CAUSE_MESSAGES = 20

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
     * `project.model` returns the generic TAPI model (`GradleProject`) and, with
     * `probe: true`, the probe's facts from one `BuildAction` (plan m1g D25; m1h D5, D7).
     * The reply keeps `projects[]` and adds `plugins`, per project `android?`, and
     * `probe {status, notes[]}`.
     *
     * `probe: false` answers the generic model with `probe.status: skipped` and never runs
     * the probe. A probed fetch that fails is retried once without the probe **only when
     * the failure chain names the probe** (D5): the retry answers the generic model with
     * `probe.status: failed`. The project's own configuration failure is not the probe's,
     * so it fails the fetch with the chain's messages and no retry; a cancelled fetch is
     * never retried.
     */
    private fun projectModel(request: Json): Json {
        val root = rootOf(request, "project.model")
        val probe = request.optBool("probe") == true
        val cts = GradleConnector.newCancellationTokenSource()
        cancels.add(cts)
        try {
            synchronized(root.lock) {
                return if (probe) {
                    probedModel(root, cts)
                } else {
                    genericModel(root, cts, "skipped", emptyList())
                }
            }
        } catch (e: GradleConnectionException) {
            throw modelFailed(e)
        } finally {
            cancels.remove(cts)
            cts.cancel()
        }
    }

    /** The generic model with the probe's status and notes attached (m1h D7). */
    private fun genericModel(
        root: Root,
        cts: CancellationTokenSource,
        status: String,
        notes: List<String>,
    ): Json {
        val projects = plainProjects(root, cts)
        return obj(
            "root" to str(root.path),
            "projects" to arr(projects),
            "probe" to obj(
                "status" to str(status),
                "notes" to arr(notes.map { str(it) }),
            ),
        )
    }

    /** One plain `GradleProject` fetch, in Gradle's project order. */
    private fun plainProjects(root: Root, cts: CancellationTokenSource): ArrayList<Json> {
        val builder = root.connection.model(GradleProject::class.java)
            .withCancellationToken(cts.token())
        applyRootOptions(builder, root.javaHome, root.env)
        val project = builder.get()
        val projects = ArrayList<Json>()
        collectProjects(File(root.path), project, projects, null)
        return projects
    }

    /**
     * The probed fetch (D5): one `BuildAction` with the probe's init script, which returns
     * the generic model and the probe's facts in the same configuration pass.
     */
    private fun probedModel(root: Root, cts: CancellationTokenSource): Json {
        val init = probeInitPath()
        val executer = root.connection.action(ProbeAction())
        executer.withCancellationToken(cts.token())
        executer.withArguments(probeArguments(init))
        applyRootOptions(executer, root.javaHome, root.env)
        val result: ProbeResult = try {
            executer.run()
        } catch (e: GradleConnectionException) {
            // A cancelled fetch is never retried: nothing about it is the probe's fault.
            if (cts.token().isCancellationRequested) throw modelFailed(e)
            // Only a failure that names the probe is retried without it. The project's own
            // configuration failure fails the fetch here, one pass, as before.
            if (!probeIsToBlame(e, init)) throw modelFailed(e)
            return failedProbeRetry(root, cts, e)
        }
        return probedReply(root, result)
    }

    /**
     * The one retry without the probe (D5): a broken probe must not cost the generic model
     * the sidecar always gave. One stderr line records it, so a test can tell a retry from
     * a single pass; a second failure is the fetch's.
     */
    private fun failedProbeRetry(
        root: Root,
        cts: CancellationTokenSource,
        failure: GradleConnectionException,
    ): Json {
        val first = firstFailureMessage(failure) ?: "the probe failed"
        System.err.println(
            "droidyard-sidecar: project.model: the probe failed, retrying without it: "
                    + capUtf8(first.replace('\n', ' '), 512),
        )
        return genericModel(root, cts, "failed", listOf(first))
    }

    /** The probe's init script: the test override first, else the launch argument (D7). */
    private fun probeInitPath(): String {
        val override = System.getProperty(TEST_PROBE_INIT_PROPERTY)
        if (!override.isNullOrEmpty()) return override
        val path = System.getProperty(PROBE_INIT_PROPERTY)
        if (path.isNullOrEmpty()) {
            throw RpcException.application("the probe's init script path is missing (-D$PROBE_INIT_PROPERTY)")
        }
        return path
    }

    /** The probed fetch's arguments: the init script, plus the test hook's extra ones. */
    private fun probeArguments(init: String): List<String> {
        val args = ArrayList<String>()
        args.add("--init-script")
        args.add(init)
        val extra = System.getProperty(TEST_PROBE_ARGS_PROPERTY)
        if (!extra.isNullOrBlank()) {
            for (arg in extra.split(' ')) if (arg.isNotEmpty()) args.add(arg)
        }
        return args
    }

    private fun probedReply(root: Root, result: ProbeResult): Json {
        val facts = HashMap<String, ProbeModel?>()
        for (factsOf in result.facts) facts[factsOf.path ?: ""] = factsOf.model

        val notes = ArrayList<String>()
        var degraded = false
        result.notes?.forEach { note ->
            if (!note.isNullOrBlank()) {
                notes.add(note)
                degraded = true
            }
        }
        for (factsOf in result.facts) {
            val model = factsOf.model ?: continue
            model.notes?.forEach { note ->
                if (!note.isNullOrBlank()) {
                    notes.add(note)
                    degraded = true
                }
            }
            val android = model.android
            // "Every field of every Android project was read" (D6): an Android project is
            // one with an android block, and a null field is its read having failed.
            if (android != null && (android.compileTarget == null || android.buildTools == null
                        || android.sdkDir == null || android.variants == null)
            ) {
                degraded = true
            }
        }

        // AGP and KGP are build-wide: the first project in path order that reports one; a
        // different value elsewhere is a note (D6).
        val ordered = result.facts.sortedBy { it.path ?: "" }
        val agp = firstVersion(ordered) { it.model?.agp }
        val kgp = firstVersion(ordered) { it.model?.kgp }
        for (factsOf in ordered) {
            val model = factsOf.model ?: continue
            if (model.agp != null && model.agp != agp) {
                notes.add("${factsOf.path}: agp: ${model.agp} (build-wide: $agp)")
            }
            if (model.kgp != null && model.kgp != kgp) {
                notes.add("${factsOf.path}: kgp: ${model.kgp} (build-wide: $kgp)")
            }
        }

        val projects = ArrayList<Json>()
        collectProjects(File(root.path), result.root, projects, facts)

        return obj(
            "root" to str(root.path),
            "projects" to arr(projects),
            "plugins" to obj("agp" to str(agp), "kgp" to str(kgp)),
            "probe" to obj(
                "status" to str(if (degraded) "partial" else "ok"),
                "notes" to arr(notes.map { str(it) }),
            ),
        )
    }

    /** The first project in path order that reports a build-wide version, else "unknown". */
    private fun firstVersion(facts: List<ProjectFacts>, value: (ProjectFacts) -> String?): String {
        for (factsOf in facts) {
            val v = value(factsOf)
            if (!v.isNullOrEmpty()) return v
        }
        return "unknown"
    }

    private fun collectProjects(
        rootDir: File,
        project: GradleProject,
        out: MutableList<Json>,
        facts: Map<String, ProbeModel?>?,
    ) {
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
        val android = if (facts == null) null else androidJson(facts[project.path ?: ":"]?.android)
        out.add(
            obj(
                "path" to str(project.path ?: ":"),
                "name" to str(project.name ?: ""),
                "dir" to str(dirText),
                "buildDir" to buildDir?.let { str(it) },
                "tasks" to arr(tasks),
                "android" to android,
            )
        )
        project.children?.forEach { child -> collectProjects(rootDir, child, out, facts) }
    }

    /** An Android project's probe facts, or null for a project that is not Android (D7). */
    private fun androidJson(android: AndroidModel?): Json? {
        if (android == null) return null
        val variants = android.variants?.map { v ->
            obj(
                "name" to str(v.name ?: ""),
                "buildType" to str(v.buildType ?: ""),
                "flavors" to arr(
                    (v.flavors ?: emptyList()).map { flavor ->
                        obj("dimension" to str(flavor.dimension ?: ""), "name" to str(flavor.name ?: ""))
                    }
                ),
            )
        }
        return obj(
            "compileTarget" to android.compileTarget?.let { str(it) },
            "buildTools" to android.buildTools?.let { str(it) },
            "sdkDir" to android.sdkDir?.let { str(it) },
            "variants" to variants?.let { arr(it) },
        )
    }

    /**
     * A failed fetch's error (D5): `PROJECT_MODEL_FAILED` with the failure chain's
     * messages as its cause, deduplicated, each cut at 8 KiB, at most 20, so the core's
     * cause holds the nested Gradle and AGP text, not only the Tooling API's wrapper.
     */
    private fun modelFailed(e: Throwable): RpcException {
        val cause = failureMessages(e).joinToString("\n")
        return RpcException.application(
            "project.model failed",
            cause.ifEmpty { e.message ?: e.javaClass.simpleName },
        )
    }

    /** Whether a failure is the probe's: a frame in its package, or a message naming it. */
    private fun probeIsToBlame(e: Throwable, init: String): Boolean {
        for (t in throwableChain(e)) {
            for (frame in t.stackTrace) {
                if (frame.className.startsWith(PROBE_FRAME_PREFIX)) return true
            }
            if (namesPath(t.message, init)) return true
        }
        if (e is GradleConnectionException) {
            for (f in flattenFailures(e.failures)) {
                if (namesPath(f.message, init) || namesPath(f.description, init)) return true
            }
        }
        return false
    }

    private fun namesPath(message: String?, init: String): Boolean =
        message != null && init.isNotEmpty() && message.contains(init)

    /** The chain's messages, most specific first, deduplicated and bounded (D5). */
    private fun failureMessages(e: Throwable): List<String> {
        val seen = LinkedHashSet<String>()
        if (e is GradleConnectionException) {
            for (f in flattenFailures(e.failures)) {
                addMessage(seen, f.message)
                addMessage(seen, f.description)
            }
        }
        for (t in throwableChain(e)) addMessage(seen, t.message)
        return seen.toList().take(MAX_CAUSE_MESSAGES)
    }

    private fun firstFailureMessage(e: Throwable): String? = failureMessages(e).firstOrNull()

    private fun addMessage(into: MutableSet<String>, message: String?) {
        if (message.isNullOrBlank()) return
        val cut = capUtf8(message.trim(), MAX_CAUSE_CHARS)
        if (cut.isNotEmpty()) into.add(cut)
    }

    private fun throwableChain(e: Throwable): List<Throwable> {
        val out = ArrayList<Throwable>()
        var t: Throwable? = e
        var guard = 0
        while (t != null && guard < 32) {
            out.add(t)
            if (t.cause === t) break
            t = t.cause
            guard++
        }
        return out
    }

    private fun flattenFailures(failures: List<Failure>?): List<Failure> {
        val out = ArrayList<Failure>()
        walkFailures(failures, out)
        return out
    }

    private fun walkFailures(failures: List<Failure>?, out: MutableList<Failure>) {
        if (failures == null) return
        for (f in failures) {
            if (out.size >= 200) return
            out.add(f)
            walkFailures(f.causes, out)
        }
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
