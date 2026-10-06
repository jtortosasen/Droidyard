package droidyard.sidecar

import java.io.FileDescriptor
import java.io.FileOutputStream
import java.io.OutputStream
import java.nio.charset.StandardCharsets.UTF_8

/**
 * JSON-RPC 2.0 framing (plan m1g D6): one message per line, the protocol on stdout and
 * only the protocol on stdout. [Protocol] writes every message to the original standard
 * output; at start the entry point points `System.out` at stderr, so a stray `println`
 * — ours, TAPI's or a plugin's — cannot corrupt the stream. A line over [MAX_LINE] is a
 * protocol error the client detects and turns into `BUILD_SIDECAR_CRASHED`; the sidecar
 * never emits one (D6 caps every console line, failure message and failure tree).
 */
class Protocol(stream: OutputStream = FileOutputStream(FileDescriptor.out)) {
    private val out = stream
    private val lock = Any()

    /** Writes one message as a line. Serialised: builds emit from several threads. */
    fun send(message: Json) {
        val bytes = (Encoder.encode(message) + "\n").toByteArray(UTF_8)
        synchronized(lock) {
            out.write(bytes)
            out.flush()
        }
    }

    fun result(id: Json, result: Json) = send(
        obj("jsonrpc" to str("2.0"), "id" to id, "result" to result)
    )

    /** An error response. [id] is JSON null when the request id could not be read. */
    fun error(id: Json?, code: Int, message: String, data: Json? = null) = send(
        obj(
            "jsonrpc" to str("2.0"), "id" to (id ?: Json.Null),
            "error" to obj("code" to num(code), "message" to str(message), "data" to data)
        )
    )

    /** A notification: no id, never answered. */
    fun notify(method: String, params: Json) = send(
        obj("jsonrpc" to str("2.0"), "method" to str(method), "params" to params)
    )

    companion object {
        /** The client's cap on one protocol line (D6). */
        const val MAX_LINE = 1 shl 20

        const val PARSE_ERROR = -32700
        const val INVALID_REQUEST = -32600
        const val METHOD_NOT_FOUND = -32601
        const val INVALID_PARAMS = -32602
        const val INTERNAL_ERROR = -32603

        /** Application errors carry this code, with the message in `data.message`. */
        const val APPLICATION_ERROR = -32000
    }
}

/**
 * A failure a method reports to the client. The core turns it into the capability error
 * its own vocabulary names; the sidecar never invents a capability error code.
 */
class RpcException(val rpcCode: Int, val rpcMessage: String, val data: Json? = null) :
    Exception(rpcMessage) {
    companion object {
        /** A method-level failure (no connection, a bad parameter value, Gradle's own). */
        fun application(message: String, cause: String? = null): RpcException =
            RpcException(
                Protocol.APPLICATION_ERROR, message,
                if (cause == null) null else obj("message" to str(cause))
            )
    }
}

/** Truncates a string to [bytes] UTF-8 bytes, dropping the remainder (D6's 8 KiB cap). */
fun capUtf8(s: String, bytes: Int): String {
    val encoded = s.toByteArray(UTF_8)
    if (encoded.size <= bytes) return s
    var end = bytes
    while (end > 0 && (encoded[end].toInt() and 0xC0) == 0x80) end-- // back to a UTF-8 boundary
    return String(encoded, 0, end, UTF_8)
}

/** The UTF-8 byte length of [s] once encoded as a JSON string literal (D6's accounting). */
fun encodedLength(s: String): Int = Encoder.encode(Json.Str(s)).toByteArray(UTF_8).size

/** The UTF-8 byte length of [value] once encoded as JSON (D6's accounting). */
fun encodedLength(value: Json): Int = Encoder.encode(value).toByteArray(UTF_8).size

/**
 * A shared budget of encoded bytes (plan m1g D6, step 1 fix round). The terminal
 * `event.build.finished` notification draws `failures` and `failureProblems` from one
 * allowance, so the line stays well under the 1 MiB notification cap by construction,
 * whatever Gradle reports.
 */
class EncodeBudget(limit: Int) {
    var remaining: Int = limit
        private set

    /** Whether [bytes] more still fit. */
    fun fits(bytes: Int): Boolean = remaining >= bytes

    /** Charges [bytes] against the budget, clamping at zero. */
    fun spend(bytes: Int) {
        remaining = maxOf(0, remaining - bytes)
    }

    /** Charges [s]'s encoded length against the budget. */
    fun spend(s: String) = spend(encodedLength(s))
}
