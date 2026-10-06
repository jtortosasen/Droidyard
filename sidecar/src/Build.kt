package droidyard.sidecar

import org.gradle.tooling.BuildCancelledException
import org.gradle.tooling.Failure
import org.gradle.tooling.GradleConnectionException
import org.gradle.tooling.GradleConnector
import org.gradle.tooling.events.ProgressListener
import org.gradle.tooling.events.OperationType
import org.gradle.tooling.events.ProgressEvent
import org.gradle.tooling.events.task.TaskExecutionResult
import org.gradle.tooling.events.task.TaskFailureResult
import org.gradle.tooling.events.task.TaskFinishEvent
import org.gradle.tooling.events.task.TaskOperationDescriptor
import org.gradle.tooling.events.task.TaskSkippedResult
import org.gradle.tooling.events.task.TaskStartEvent
import org.gradle.tooling.events.task.TaskSuccessResult
import org.gradle.tooling.events.problems.FileLocation
import org.gradle.tooling.events.problems.LineInFileLocation
import org.gradle.tooling.events.problems.Location
import org.gradle.tooling.events.problems.Problem
import org.gradle.tooling.events.problems.Severity
import org.gradle.tooling.events.problems.SingleProblemEvent
import java.io.OutputStream
import java.nio.charset.StandardCharsets.UTF_8
import java.security.MessageDigest
import java.util.Collections
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledExecutorService
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger

/** One console line is cut here and a marker follows (plan m1g D6). */
const val MAX_CONSOLE_LINE = 64 * 1024

/** A failure message or description is cut here (D6). */
const val MAX_FAILURE_TEXT = 8 * 1024

/** A failure tree has at most this many nodes (D6). */
const val MAX_FAILURE_NODES = 200

/** A batch carries at most this many lines, or this many bytes, or is flushed at 100 ms. */
const val MAX_BATCH_LINES = 200
const val MAX_BATCH_BYTES = 64 * 1024
const val BATCH_INTERVAL_MS = 100L

/**
 * One build. It runs on its own thread while holding the root's lock, so every TAPI call
 * for that root stays serialised (R5). Console output is coalesced into
 * `event.build.output` batches; task outcomes, progress and problems become their own
 * notifications; the terminal `event.build.finished` carries the outcome, the failure
 * tree and the problems failures carry.
 */
class Build(
    private val buildId: String,
    private val root: Root,
    private val proto: Protocol,
    private val tasks: List<String>,
    private val onDone: (String) -> Unit,
) {
    private val cancellation = GradleConnector.newCancellationTokenSource()

    @Volatile
    private var cancelled = false

    fun cancel() {
        cancelled = true
        try { cancellation.cancel() } catch (e: Exception) { /* already cancelled */ }
    }

    fun start() {
        val thread = Thread({ synchronized(root.lock) { run() } }, "build-$buildId")
        thread.isDaemon = true
        thread.start()
    }

    private fun run() {
        val out = LineStream(buildId, "stdout", proto)
        val err = LineStream(buildId, "stderr", proto)
        val listener = BuildListener(buildId, proto)
        var outcome = "success"
        var failures: List<Failure> = emptyList()
        try {
            val launcher = root.connection.newBuild()
                .forTasks(*tasks.toTypedArray())
                .setColorOutput(false)
                .setStandardOutput(out)
                .setStandardError(err)
                .withCancellationToken(cancellation.token())
            root.env?.let { launcher.setEnvironmentVariables(it) }
            // Subscription-time gating (ADR-0018 rule 1): only ask for the Problems
            // stream where the client decodes its shape.
            if (root.capabilities.optBool("problemEvents") == true) {
                try {
                    launcher.addProgressListener(listener, OperationType.PROBLEMS)
                } catch (e: Exception) {
                    listener.missing.add("problemEvents")
                }
            }
            if (root.capabilities.optBool("detailedFailure") == true) {
                try {
                    launcher.withDetailedFailure()
                } catch (e: Exception) {
                    listener.missing.add("detailedFailure")
                }
            }
            launcher.addProgressListener(listener, OperationType.TASK)
            launcher.run()
        } catch (e: BuildCancelledException) {
            outcome = "cancelled"
        } catch (e: GradleConnectionException) {
            outcome = if (cancelled) "cancelled" else "failed"
            failures = try { e.failures ?: emptyList() } catch (x: Exception) { emptyList() }
        } catch (e: Throwable) {
            outcome = "failed"
            failures = listOf(SimpleFailure(e.message ?: e.javaClass.simpleName, e.toString()))
        } finally {
            out.close()
            err.close()
            listener.flushProgress()
        }
        proto.notify(
            "event.build.finished",
            obj(
                "buildId" to str(buildId),
                "outcome" to str(outcome),
                "failures" to arr(FailureWriter.write(failures)),
                "failureProblems" to arr(failureProblems(failures)),
                "missingCapabilities" to arr(listener.missing.toList().map { str(it) }),
            ),
        )
        onDone(buildId)
    }

    /** A `Failure` for something that is not a Gradle failure (an internal error). */
    private class SimpleFailure(private val text: String, private val detail: String) : Failure {
        override fun getMessage(): String = text
        override fun getDescription(): String = detail
        override fun getOwnDescription(): String = detail
        override fun getCauses(): List<Failure> = emptyList()
        override fun getProblems(): List<Problem> = emptyList()
    }

    private fun failureProblems(failures: List<Failure>): List<Json> {
        val out = ArrayList<Json>()
        val seen = HashSet<String>()
        fun walk(f: Failure, depth: Int) {
            if (depth > 20) return
            try {
                for (p in f.problems ?: emptyList()) {
                    val j = problemJson(p)
                    if (seen.add(Encoder.encode(j))) out.add(j)
                }
            } catch (e: Exception) { /* a failure without problems */ }
            try { f.causes?.forEach { walk(it, depth + 1) } } catch (e: Exception) {}
        }
        failures.forEach { walk(it, 0) }
        return out
    }
}

/** Serialises a failure tree, capped at [MAX_FAILURE_NODES] nodes (D6). */
object FailureWriter {
    fun write(failures: List<Failure>): List<Json> {
        val budget = AtomicInteger(MAX_FAILURE_NODES)
        return failures.mapIndexed { i, f -> node(f, "f$i", budget) }
    }

    private fun node(f: Failure, path: String, budget: AtomicInteger): Json {
        val message = capUtf8(f.message ?: "", MAX_FAILURE_TEXT)
        val description = capUtf8(f.description ?: "", MAX_FAILURE_TEXT)
        if (budget.decrementAndGet() < 0) {
            return obj(
                "id" to str(stableId(message, description)),
                "message" to str(message),
                "description" to str(""),
                "causes" to arr(emptyList()),
                "truncated" to bool(true),
            )
        }
        val causes = try { f.causes } catch (e: Exception) { null }
        val children = ArrayList<Json>()
        causes?.forEachIndexed { i, c -> children.add(node(c, "$path.$i", budget)) }
        return obj(
            "id" to str(stableId(message, description)),
            "message" to str(message),
            "description" to str(description),
            "causes" to arr(children),
        )
    }

    /** A stable id: the same failure text always hashes to the same id. */
    private fun stableId(message: String, description: String): String {
        val md = MessageDigest.getInstance("SHA-256")
        val bytes = md.digest((message + "\n" + description).toByteArray(UTF_8))
        val sb = StringBuilder(16)
        for (i in 0 until 8) sb.append("%02x".format(bytes[i]))
        return sb.toString()
    }
}

/** Maps a Gradle Problems record to the shape `build.errors` merges (gradle.md §Error model). */
fun problemJson(p: Problem): Json {
    val definition = try { p.definition } catch (e: Exception) { null }
    val severity = try { severityName(definition?.severity) } catch (e: Exception) { null }
    val problemId = try { definition?.id?.name } catch (e: Exception) { null }
    val problemDisplay = try { definition?.id?.displayName } catch (e: Exception) { null }
    val documentation = try { definition?.documentationLink?.url } catch (e: Exception) { null }
    val label = try { p.contextualLabel?.contextualLabel } catch (e: Exception) { null }
    val details = try { p.details?.details } catch (e: Exception) { null }
    val locations = try { p.originLocations } catch (e: Exception) { null }
    val loc = locations?.firstOrNull()
    val file = (loc as? FileLocation)?.path
    val line = (loc as? LineInFileLocation)?.line
    val column = (loc as? LineInFileLocation)?.column
    val solutions = try { p.solutions?.map { it.solution } ?: emptyList() } catch (e: Exception) { emptyList() }
    val taskPath = try { (locations?.firstOrNull { it is org.gradle.tooling.events.problems.TaskPathLocation } as? org.gradle.tooling.events.problems.TaskPathLocation)?.buildTreePath } catch (e: Exception) { null }
    return obj(
        "severity" to severity?.let { str(it) },
        "problemId" to problemId?.let { str(it) },
        "problemDisplayName" to problemDisplay?.let { str(it) },
        "contextualLabel" to label?.let { str(it) },
        "details" to details?.let { str(capUtf8(it, MAX_FAILURE_TEXT)) },
        "file" to file?.let { str(it) },
        "line" to line?.let { num(it) },
        "column" to column?.let { num(it) },
        "taskPath" to taskPath?.let { str(it) },
        "solutions" to arr(solutions.map { str(it) }),
        "documentationLink" to documentation?.let { str(it) },
    )
}

private fun severityName(s: Severity?): String? = when (s) {
    null -> null
    Severity.ERROR -> "error"
    Severity.WARNING -> "warning"
    Severity.ADVICE -> "advice"
    else -> "unknown"
}

/**
 * The progress listener: task outcomes, progress and Problems events. Everything is
 * wrapped: TAPI lets a listener exception fail the build, and a malformed event must
 * never do that (gradle.md §Version detection: an unknown shape degrades a field, never
 * the build).
 */
class BuildListener(
    private val buildId: String,
    private val proto: Protocol,
) : ProgressListener {
    val missing = Collections.synchronizedSet(LinkedHashSet<String>())
    private val counts = java.util.concurrent.ConcurrentHashMap<String, Int>().apply {
        for (k in listOf("executed", "upToDate", "fromCache", "noSource", "skipped", "failed")) put(k, 0)
    }

    @Volatile
    private var activeTask: String = ""

    override fun statusChanged(event: ProgressEvent) {
        try {
            when (event) {
                is TaskStartEvent -> {
                    activeTask = taskPath(event)
                    emitProgress()
                }
                is TaskFinishEvent -> onTaskFinish(event)
                is SingleProblemEvent -> onProblem(event)
                else -> {}
            }
        } catch (e: Exception) {
            // A listener must never fail a build: record the capability gap and move on.
            missing.add("listener:" + e.javaClass.simpleName)
        }
    }

    private fun onTaskFinish(event: TaskFinishEvent) {
        val descriptor = event.descriptor
        val path = if (descriptor is TaskOperationDescriptor) descriptor.taskPath else descriptor?.name ?: ""
        val (outcome, reason) = outcomeOf(event.result)
        counts.computeIfPresent(summaryKey(outcome)) { _, v -> v + 1 }
        val duration = try { event.result.endTime - event.result.startTime } catch (e: Exception) { 0L }
        proto.notify(
            "event.build.taskFinished",
            obj(
                "buildId" to str(buildId),
                "path" to str(path),
                "outcome" to str(outcome),
                "durationMs" to num(duration),
                "reason" to reason?.let { str(capUtf8(it, MAX_FAILURE_TEXT)) },
            ),
        )
        activeTask = ""
        emitProgress()
    }

    private fun onProblem(event: SingleProblemEvent) {
        val problem = try { event.problem } catch (e: Exception) { null } ?: return
        proto.notify(
            "event.build.problem",
            obj("buildId" to str(buildId), "problem" to problemJson(problem)),
        )
    }

    fun flushProgress() = emitProgress()

    private fun emitProgress() {
        proto.notify(
            "event.build.progress",
            obj(
                "buildId" to str(buildId),
                "counts" to obj(
                    "executed" to num(counts["executed"] ?: 0),
                    "upToDate" to num(counts["upToDate"] ?: 0),
                    "fromCache" to num(counts["fromCache"] ?: 0),
                    "noSource" to num(counts["noSource"] ?: 0),
                    "skipped" to num(counts["skipped"] ?: 0),
                    "failed" to num(counts["failed"] ?: 0),
                ),
                "activeTask" to str(activeTask),
            ),
        )
    }

    private fun taskPath(event: TaskStartEvent): String {
        val descriptor = event.descriptor
        return if (descriptor is TaskOperationDescriptor) descriptor.taskPath else descriptor?.name ?: ""
    }

    private fun summaryKey(outcome: String): String = outcome

    private fun outcomeOf(result: org.gradle.tooling.events.OperationResult?): Pair<String, String?> = when (result) {
        is TaskFailureResult ->
            "failed" to result.failures.firstOrNull()?.message
        is TaskSuccessResult -> when {
            result.isFromCache -> "fromCache" to null
            result.isUpToDate -> "upToDate" to null
            else -> "success" to null
        }
        is TaskSkippedResult -> {
            val message = result.skipMessage
            if (message != null && message.uppercase().contains("NO-SOURCE")) "noSource" to message
            else "skipped" to message
        }
        is TaskExecutionResult -> "success" to null
        else -> "success" to null
    }
}

/**
 * A build's console stream: splits bytes into lines, coalesces them into batches of at
 * most [MAX_BATCH_LINES] lines or [MAX_BATCH_BYTES] bytes, flushes every
 * [BATCH_INTERVAL_MS], and cuts a single line at [MAX_CONSOLE_LINE] with a marker that
 * counts the dropped bytes (D6). Batches are `event.build.output`.
 */
class LineStream(
    private val buildId: String,
    private val stream: String,
    private val proto: Protocol,
) : OutputStream() {
    private val lock = Any()
    private val buffer = ByteArray(MAX_CONSOLE_LINE)
    private var length = 0
    private var dropped = 0L
    private val batch = ArrayList<String>()
    private var batchBytes = 0
    private val timer = SCHEDULER.scheduleAtFixedRate({ flushTimer() }, BATCH_INTERVAL_MS, BATCH_INTERVAL_MS, TimeUnit.MILLISECONDS)
    @Volatile private var closed = false

    override fun write(b: Int) = write(byteArrayOf(b.toByte()), 0, 1)

    override fun write(b: ByteArray, off: Int, len: Int) {
        synchronized(lock) {
            if (closed) return
            var i = off
            val end = off + len
            while (i < end) {
                var j = i
                while (j < end && b[j] != NEWLINE) j++
                val chunk = j - i
                if (chunk > 0) {
                    if (length < buffer.size) {
                        val take = minOf(chunk, buffer.size - length)
                        System.arraycopy(b, i, buffer, length, take)
                        length += take
                        if (take < chunk) dropped += (chunk - take).toLong()
                    } else {
                        dropped += chunk.toLong()
                    }
                }
                i = j
                if (i < end) {
                    emitLine()
                    i++
                }
            }
        }
    }

    override fun flush() {
        synchronized(lock) { flushBatch() }
    }

    override fun close() {
        synchronized(lock) {
            if (closed) return
            if (length > 0 || dropped > 0) emitLine()
            flushBatch()
            closed = true
        }
        timer.cancel(false)
    }

    private fun emitLine() {
        val text = String(buffer, 0, length, UTF_8)
        length = 0
        if (dropped > 0) {
            val lost = dropped
            dropped = 0
            add(text)
            add("[droidyard: console line truncated at $MAX_CONSOLE_LINE bytes, $lost bytes dropped]")
        } else {
            add(text)
        }
    }

    private fun add(line: String) {
        if (batch.isNotEmpty() &&
            (batch.size >= MAX_BATCH_LINES || batchBytes + line.length > MAX_BATCH_BYTES)
        ) {
            flushBatch()
        }
        batch.add(line)
        batchBytes += line.length
    }

    private fun flushBatch() {
        if (batch.isEmpty()) return
        val lines = batch.toList()
        batch.clear()
        batchBytes = 0
        proto.notify(
            "event.build.output",
            obj(
                "buildId" to str(buildId),
                "stream" to str(stream),
                "lines" to arr(lines.map { str(it) }),
            ),
        )
    }

    private fun flushTimer() {
        try {
            synchronized(lock) {
                if (!closed) flushBatch()
            }
        } catch (e: Exception) {
            // A scheduled task that throws is cancelled; keep the flusher alive by
            // swallowing here.
        }
    }

    companion object {
        private val NEWLINE = '\n'.code.toByte()
        private val SCHEDULER: ScheduledExecutorService =
            Executors.newScheduledThreadPool(1) { r -> Thread(r, "console-flush").apply { isDaemon = true } }
    }
}
