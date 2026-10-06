package droidyard.sidecar

import org.gradle.tooling.BuildCancelledException
import org.gradle.tooling.BuildLauncher
import org.gradle.tooling.Failure
import org.gradle.tooling.GradleConnectionException
import org.gradle.tooling.GradleConnector
import org.gradle.tooling.exceptions.UnsupportedOperationConfigurationException
import org.gradle.tooling.model.UnsupportedMethodException
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

/** At most this many failure-carried problems in `event.build.finished` (D6). */
const val MAX_FAILURE_PROBLEMS = 500

/** At most this many solutions kept per problem record (D6: keeps a notification bounded). */
const val MAX_PROBLEM_SOLUTIONS = 10

/**
 * The encoded-byte budget the terminal notification shares across `failures` and
 * `failureProblems` (D6, step 1 fix round), well under the 1 MiB notification cap.
 */
const val MAX_FINISHED_TEXT = 512 * 1024

/** Per-node / per-problem JSON overhead charged against the budget (a safe upper bound). */
private const val NODE_OVERHEAD = 200
private const val PROBLEM_OVERHEAD = 400

/** The smallest node worth starting; below it a parent reports `truncatedCauses` (D6). */
private const val MIN_NODE_COST = NODE_OVERHEAD + 64

/** A batch carries at most this many lines, or this many encoded bytes, or is flushed at 100 ms. */
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

    /** Starts the build thread; returns the failure when the thread could not start (N4). */
    fun start(): Throwable? {
        val thread = Thread({ synchronized(root.lock) { run() } }, "build-$buildId")
        thread.isDaemon = true
        return try {
            thread.start()
            null
        } catch (e: Throwable) {
            // A thread that cannot be created is usually an OutOfMemoryError, an Error.
            e
        }
    }

    private fun run() {
        val out = LineStream(buildId, "stdout", proto)
        val err = LineStream(buildId, "stderr", proto)
        val listener = BuildListener(buildId, proto)
        var outcome = "success"
        var failures: List<Failure> = emptyList()
        try {
            try {
                runBuild(out, err, listener)
            } catch (e: BuildCancelledException) {
                outcome = "cancelled"
            } catch (e: GradleConnectionException) {
                outcome = if (cancelled) "cancelled" else "failed"
                failures = try { e.failures ?: emptyList() } catch (x: Exception) { emptyList() }
            } catch (e: Throwable) {
                outcome = "failed"
                failures = listOf(SimpleFailure(e.message ?: e.javaClass.simpleName, e.toString()))
            }
        } finally {
            out.close()
            err.close()
            listener.flushProgress()
        }
        // Exactly one event.build.finished per accepted build (N4), even when building the
        // full message throws; the minimal fallback still names the build and its outcome.
        try {
            val message = try {
                finished(outcome, failures, listener)
            } catch (e: Throwable) {
                fallbackFinished(e)
            }
            proto.notify("event.build.finished", message)
        } finally {
            onDone(buildId)
        }
    }

    /**
     * Runs the build, degrading rather than failing when TAPI refuses the Problems
     * subscription or detailed failure (plan m1g step 1 fix round, B3): the refusal is a
     * capability gap, recorded in `missingCapabilities`, and the build is retried once
     * without either. `UnsupportedOperationConfigurationException` carries no failure tree,
     * so without this the whole build would fail on a Gradle older than its capabilities
     * claimed.
     */
    private fun runBuild(out: LineStream, err: LineStream, listener: BuildListener) {
        try {
            launcher(out, err, listener, withProblems = true, detailedFailure = true).run()
        } catch (e: UnsupportedOperationConfigurationException) {
            listener.missing.add("problemEvents")
            listener.missing.add("detailedFailure")
            launcher(out, err, listener, withProblems = false, detailedFailure = false).run()
        }
    }

    private fun launcher(
        out: LineStream,
        err: LineStream,
        listener: BuildListener,
        withProblems: Boolean,
        detailedFailure: Boolean,
    ): BuildLauncher {
        val launcher = root.connection.newBuild()
            .forTasks(*tasks.toTypedArray())
            .setColorOutput(false)
            .setStandardOutput(out)
            .setStandardError(err)
            .withCancellationToken(cancellation.token())
        applyRootOptions(launcher, root.javaHome, root.env)
        // Subscription-time gating (ADR-0018 rule 1): only ask for the Problems stream
        // where the client decodes its shape.
        if (withProblems && root.capabilities.optBool("problemEvents") == true) {
            launcher.addProgressListener(listener, OperationType.PROBLEMS)
        }
        if (detailedFailure && root.capabilities.optBool("detailedFailure") == true) {
            launcher.withDetailedFailure()
        }
        launcher.addProgressListener(listener, OperationType.TASK)
        return launcher
    }

    private fun finished(outcome: String, failures: List<Failure>, listener: BuildListener): Json {
        val budget = EncodeBudget(MAX_FINISHED_TEXT)
        val tree = FailureWriter.write(failures, budget)
        val problems = failureProblems(failures, budget, listener.missing)
        return obj(
            "buildId" to str(buildId),
            "outcome" to str(outcome),
            "failures" to arr(tree),
            "failureProblems" to arr(problems.problems),
            "failureProblemsDropped" to (if (problems.dropped > 0) num(problems.dropped) else null),
            "missingCapabilities" to arr(listener.missing.toList().map { str(it) }),
        )
    }

    /** A minimal, always-buildable terminal notification (N4). */
    private fun fallbackFinished(e: Throwable): Json = obj(
        "buildId" to str(buildId),
        "outcome" to str("failed"),
        "failures" to arr(FailureWriter.fallback("sidecar error: " + (e.message ?: e.javaClass.simpleName))),
        "failureProblems" to arr(emptyList()),
        "missingCapabilities" to arr(emptyList()),
    )

    /** A `Failure` for something that is not a Gradle failure (an internal error). */
    private class SimpleFailure(private val text: String, private val detail: String) : Failure {
        override fun getMessage(): String = text
        override fun getDescription(): String = detail
        override fun getOwnDescription(): String = detail
        override fun getCauses(): List<Failure> = emptyList()
        override fun getProblems(): List<Problem> = emptyList()
    }

    /** The failure-carried problems, capped and drawn from the shared budget (D6). */
    private class Problems(val problems: List<Json>, val dropped: Int)

    private fun failureProblems(
        failures: List<Failure>,
        budget: EncodeBudget,
        missing: MutableSet<String>,
    ): Problems {
        val out = ArrayList<Json>()
        val seen = HashSet<String>()
        var dropped = 0
        fun walk(f: Failure, depth: Int) {
            if (depth > 20) return
            try {
                for (p in f.problems ?: emptyList()) {
                    val j = problemJson(p, missing)
                    if (!seen.add(Encoder.encode(j))) continue
                    val cost = PROBLEM_OVERHEAD + encodedLength(j)
                    if (out.size >= MAX_FAILURE_PROBLEMS || !budget.fits(cost)) {
                        dropped++
                        continue
                    }
                    budget.spend(cost)
                    out.add(j)
                }
            } catch (e: Exception) { /* a failure without problems */ }
            try { f.causes?.forEach { walk(it, depth + 1) } } catch (e: Exception) {}
        }
        failures.forEach { walk(it, 0) }
        return Problems(out, dropped)
    }
}

/**
 * Serialises a failure tree, capped at [MAX_FAILURE_NODES] nodes and drawn from the
 * shared [EncodeBudget] (D6): when either limit is reached a parent stops recursing and
 * carries `truncatedCauses: N` — the count of causes left out — instead of N more nodes.
 */
object FailureWriter {
    fun write(failures: List<Failure>, budget: EncodeBudget): List<Json> {
        val nodesLeft = AtomicInteger(MAX_FAILURE_NODES)
        val out = ArrayList<Json>(failures.size)
        for ((i, f) in failures.withIndex()) {
            if (out.isNotEmpty() && (nodesLeft.get() <= 0 || !budget.fits(MIN_NODE_COST))) break
            out.add(node(f, "f$i", budget, nodesLeft))
        }
        return out
    }

    /** A minimal failure list for the fallback notification (N4). */
    fun fallback(text: String): List<Json> {
        val message = capUtf8(text, MAX_FAILURE_TEXT)
        return listOf(
            obj(
                "id" to str(stableId(message, "")),
                "message" to str(message),
                "description" to str(""),
                "causes" to arr(emptyList()),
            )
        )
    }

    private fun node(f: Failure, path: String, budget: EncodeBudget, nodesLeft: AtomicInteger): Json {
        val message = capUtf8(f.message ?: "", MAX_FAILURE_TEXT)
        val description = capUtf8(f.description ?: "", MAX_FAILURE_TEXT)
        nodesLeft.decrementAndGet()
        budget.spend(NODE_OVERHEAD)
        budget.spend(message)
        budget.spend(description)
        val causes = try { f.causes } catch (e: Exception) { null }
        val children = ArrayList<Json>()
        var truncated = 0
        if (!causes.isNullOrEmpty()) {
            for ((i, c) in causes.withIndex()) {
                if (nodesLeft.get() <= 0 || !budget.fits(MIN_NODE_COST)) {
                    truncated = causes.size - i
                    break
                }
                children.add(node(c, "$path.$i", budget, nodesLeft))
            }
        }
        return obj(
            "id" to str(stableId(message, description)),
            "message" to str(message),
            "description" to str(description),
            "causes" to arr(children),
            "truncatedCauses" to (if (truncated > 0) num(truncated) else null),
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

/**
 * Maps a Gradle Problems record to the shape `build.errors` merges (gradle.md §Error
 * model). Every string is cut at [MAX_FAILURE_TEXT] (D6). A getter that TAPI does not
 * support records the capability gap in [missing]: a location getter as `problemLocations`,
 * any other as `problemEvents` (step 1 fix round, B3).
 */
fun problemJson(p: Problem, missing: MutableSet<String>? = null): Json {
    val definition = problemGetter(missing, "problemEvents") { p.definition }
    val severity = problemGetter(missing, "problemEvents") { severityName(definition?.severity) }
    val problemId = problemGetter(missing, "problemEvents") { definition?.id?.name }
    val problemDisplay = problemGetter(missing, "problemEvents") { definition?.id?.displayName }
    val documentation = problemGetter(missing, "problemEvents") { definition?.documentationLink?.url }
    val label = problemGetter(missing, "problemEvents") { p.contextualLabel?.contextualLabel }
    val details = problemGetter(missing, "problemEvents") { p.details?.details }
    val locations = problemGetter(missing, "problemLocations") { p.originLocations }
    val loc = locations?.firstOrNull()
    val file = (loc as? FileLocation)?.path
    val line = (loc as? LineInFileLocation)?.line
    val column = (loc as? LineInFileLocation)?.column
    val solutions = problemGetter(missing, "problemEvents") { p.solutions?.map { it.solution } ?: emptyList() }
        ?: emptyList()
    val taskPath = problemGetter(missing, "problemLocations") {
        (locations?.firstOrNull { it is org.gradle.tooling.events.problems.TaskPathLocation }
            as? org.gradle.tooling.events.problems.TaskPathLocation)?.buildTreePath
    }
    return obj(
        "severity" to severity?.let { str(capUtf8(it, MAX_FAILURE_TEXT)) },
        "problemId" to problemId?.let { str(capUtf8(it, MAX_FAILURE_TEXT)) },
        "problemDisplayName" to problemDisplay?.let { str(capUtf8(it, MAX_FAILURE_TEXT)) },
        "contextualLabel" to label?.let { str(capUtf8(it, MAX_FAILURE_TEXT)) },
        "details" to details?.let { str(capUtf8(it, MAX_FAILURE_TEXT)) },
        "file" to file?.let { str(capUtf8(it, MAX_FAILURE_TEXT)) },
        "line" to line?.let { num(it) },
        "column" to column?.let { num(it) },
        "taskPath" to taskPath?.let { str(capUtf8(it, MAX_FAILURE_TEXT)) },
        "solutions" to arr(solutions.take(MAX_PROBLEM_SOLUTIONS).map { str(capUtf8(it, MAX_FAILURE_TEXT)) }),
        "documentationLink" to documentation?.let { str(capUtf8(it, MAX_FAILURE_TEXT)) },
    )
}

/** A problem getter, recording a TAPI gap in [missing]; any other failure degrades to null. */
private inline fun <T> problemGetter(missing: MutableSet<String>?, key: String, block: () -> T?): T? =
    try {
        block()
    } catch (e: UnsupportedMethodException) {
        missing?.add(key)
        null
    } catch (e: Exception) {
        null
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
                "path" to str(capUtf8(path, MAX_FAILURE_TEXT)),
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
            obj("buildId" to str(buildId), "problem" to problemJson(problem, missing)),
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
                "activeTask" to str(capUtf8(activeTask, MAX_FAILURE_TEXT)),
            ),
        )
    }

    private fun taskPath(event: TaskStartEvent): String {
        val descriptor = event.descriptor
        return if (descriptor is TaskOperationDescriptor) descriptor.taskPath else descriptor?.name ?: ""
    }

    // `taskSummary`'s vocabulary (gradle.md §The build model) names a plain success
    // `executed`; the task-level outcome is `success`. Without this the `executed` count
    // never moves (step 1 fix round, N5).
    private fun summaryKey(outcome: String): String = if (outcome == "success") "executed" else outcome

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
    private var overflowed = false
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
                        if (take < chunk) {
                            dropped += (chunk - take).toLong()
                            overflowed = true
                        }
                    } else {
                        dropped += chunk.toLong()
                        overflowed = true
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
        // When the line was cut at the 64 KiB buffer, back the cut off to a UTF-8
        // boundary so the emitted text never splits a sequence (D6, step 1 fix round);
        // the backed-off bytes join the dropped count.
        if (overflowed) {
            val boundary = utf8Boundary(buffer, length)
            if (boundary < length) {
                dropped += (length - boundary).toLong()
                length = boundary
            }
        }
        overflowed = false
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
        // Counted in the encoded line's UTF-8 bytes (D6, step 1 fix round): what the
        // notification actually carries, escapes included, so the batch stays bounded.
        val bytes = encodedLength(line)
        if (batch.isNotEmpty() &&
            (batch.size >= MAX_BATCH_LINES || batchBytes + bytes > MAX_BATCH_BYTES)
        ) {
            flushBatch()
        }
        batch.add(line)
        batchBytes += bytes
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

        /**
         * The largest length ≤ [len] at which [b] ends on a UTF-8 code-point boundary
         * (D6, step 1 fix round): a complete last sequence is kept, an incomplete one is
         * dropped whole. The `MAX_CONSOLE_LINE` cut must never split a sequence.
         */
        fun utf8Boundary(b: ByteArray, len: Int): Int {
            if (len <= 0) return 0
            var i = len - 1
            while (i >= 0 && (b[i].toInt() and 0xC0) == 0x80) i-- // skip continuation bytes
            if (i < 0) return len // no lead byte at all: leave the bytes as they are
            val lead = b[i].toInt() and 0xFF
            val expected = when {
                lead and 0x80 == 0x00 -> 1
                lead and 0xE0 == 0xC0 -> 2
                lead and 0xF0 == 0xE0 -> 3
                lead and 0xF8 == 0xF0 -> 4
                else -> 1
            }
            return if (len - i >= expected) len else i
        }
    }
}
