package droidyard.sidecar

/**
 * The sidecar's JSON codec (plan m1g D7): a minimal hand-written encoder and decoder,
 * with no JSON library on the classpath. It is small on purpose — the sidecar's only
 * job on the wire is one JSON object per line, and pulling a JSON library in would add
 * an artifact whose behaviour we do not control.
 *
 * The encoder escapes what JSON requires, emits non-ASCII as raw UTF-8, and writes an
 * unpaired surrogate as `�`. The decoder decodes `\uXXXX`, combining a valid
 * surrogate pair and treating an unpaired one as U+FFFD, so a decoder never produces a
 * string Kotlin cannot re-encode. Both are round-trip tested against the Go client
 * (sidecar/sidecar_test.go), which is the consumer that has to agree with them.
 */
sealed class Json {
    object Null : Json()
    data class Bool(val value: Boolean) : Json()

    /** A number kept verbatim: ids, timestamps and counts must not lose precision. */
    data class Num(val raw: String) : Json()
    data class Str(val value: String) : Json()
    data class Arr(val items: List<Json>) : Json()

    /** An object preserving the field order the encoder emits. */
    data class Obj(val fields: Map<String, Json>) : Json()
}

/** A JSON object from pairs, skipping a null value (an absent optional field). */
fun obj(vararg pairs: Pair<String, Json?>): Json.Obj =
    Json.Obj(LinkedHashMap<String, Json>().apply {
        for ((k, v) in pairs) if (v != null) put(k, v)
    })

fun arr(items: List<Json>): Json.Arr = Json.Arr(items)

fun str(value: String): Json.Str = Json.Str(value)
fun num(value: Long): Json.Num = Json.Num(value.toString())
fun num(value: Int): Json.Num = Json.Num(value.toString())
fun bool(value: Boolean): Json.Bool = Json.Bool(value)

// --- accessors used by the dispatcher -------------------------------------------------

fun Json.field(name: String): Json? = (this as? Json.Obj)?.fields?.get(name)
fun Json.objFields(): Map<String, Json> = (this as Json.Obj).fields
fun Json.asString(): String = (this as Json.Str).value
fun Json.asLong(): Long = (this as Json.Num).raw.toLong()
fun Json.asBool(): Boolean = (this as Json.Bool).value

/** A string field, or null when absent or not a string. */
fun Json.optString(name: String): String? = field(name)?.let { if (it is Json.Str) it.value else null }

/** A string array field, or null when absent or not an array of strings. */
fun Json.optStrings(name: String): List<String>? = field(name)?.let { f ->
    if (f !is Json.Arr) null else f.items.map { if (it is Json.Str) it.value else null }
        .takeIf { list -> list.none { it == null } }?.map { it!! }
}

/** A string→string map field, or null when absent. */
fun Json.optEnv(name: String): Map<String, String>? = field(name)?.let { f ->
    if (f !is Json.Obj) null else {
        val m = LinkedHashMap<String, String>()
        for ((k, v) in f.fields) if (v is Json.Str) m[k] = v.value
        m
    }
}

fun Json.optBool(name: String): Boolean? = field(name)?.let { if (it is Json.Bool) it.value else null }

// --- the encoder ----------------------------------------------------------------------

object Encoder {
    fun encode(value: Json): String {
        val sb = StringBuilder(256)
        write(sb, value)
        return sb.toString()
    }

    private fun write(sb: StringBuilder, value: Json) {
        when (value) {
            is Json.Null -> sb.append("null")
            is Json.Bool -> sb.append(if (value.value) "true" else "false")
            is Json.Num -> sb.append(value.raw)
            is Json.Str -> writeString(sb, value.value)
            is Json.Arr -> {
                sb.append('[')
                value.items.forEachIndexed { i, item ->
                    if (i > 0) sb.append(',')
                    write(sb, item)
                }
                sb.append(']')
            }
            is Json.Obj -> {
                sb.append('{')
                var first = true
                for ((k, v) in value.fields) {
                    if (!first) sb.append(',')
                    first = false
                    writeString(sb, k)
                    sb.append(':')
                    write(sb, v)
                }
                sb.append('}')
            }
        }
    }

    /** Writes a JSON string literal; an unpaired surrogate becomes U+FFFD (D7). */
    private fun writeString(sb: StringBuilder, s: String) {
        sb.append('"')
        var i = 0
        while (i < s.length) {
            val c = s[i]
            when {
                c == '"' -> sb.append("\\\"")
                c == '\\' -> sb.append("\\\\")
                c == '\n' -> sb.append("\\n")
                c == '\r' -> sb.append("\\r")
                c == '\t' -> sb.append("\\t")
                c == '\b' -> sb.append("\\b")
                c == '\u000C' -> sb.append("\\f")
                c < ' ' -> {
                    sb.append("\\u")
                    val hex = c.code.toString(16)
                    repeat(4 - hex.length) { sb.append('0') }
                    sb.append(hex)
                }
                c.isHighSurrogate() ->
                    if (i + 1 < s.length && s[i + 1].isLowSurrogate()) {
                        sb.append(c).append(s[i + 1])
                        i++
                    } else {
                        sb.append("\\ufffd")
                    }
                c.isLowSurrogate() -> sb.append("\\ufffd")
                else -> sb.append(c)
            }
            i++
        }
        sb.append('"')
    }
}

// --- the decoder ----------------------------------------------------------------------

class JsonDecodeException(message: String) : Exception(message)

object Decoder {
    /** Parses exactly one JSON value; trailing content is an error. */
    fun parse(text: String): Json {
        val p = Parser(text)
        val v = p.value(0)
        p.skipWhitespace()
        if (!p.atEnd()) throw JsonDecodeException("unexpected trailing content at ${p.offset}")
        return v
    }

    private class Parser(private val s: String) {
        var offset = 0
            private set

        fun atEnd() = offset >= s.length

        fun skipWhitespace() {
            while (offset < s.length && (s[offset] == ' ' || s[offset] == '\t' ||
                        s[offset] == '\n' || s[offset] == '\r')
            ) offset++
        }

        fun value(depth: Int): Json {
            if (depth > 100) throw JsonDecodeException("nesting deeper than 100")
            skipWhitespace()
            if (atEnd()) throw JsonDecodeException("unexpected end of input")
            return when (val c = s[offset]) {
                '{' -> objectValue(depth)
                '[' -> arrayValue(depth)
                '"' -> Json.Str(string())
                't' -> { expect("true"); Json.Bool(true) }
                'f' -> { expect("false"); Json.Bool(false) }
                'n' -> { expect("null"); Json.Null }
                else ->
                    if (c == '-' || c in '0'..'9') number()
                    else throw JsonDecodeException("unexpected character '$c' at $offset")
            }
        }

        private fun objectValue(depth: Int): Json {
            expect("{")
            val fields = LinkedHashMap<String, Json>()
            skipWhitespace()
            if (peek() == '}') { offset++; return Json.Obj(fields) }
            while (true) {
                skipWhitespace()
                val key = string()
                skipWhitespace()
                expect(":")
                fields[key] = value(depth + 1)
                skipWhitespace()
                when (peek()) {
                    ',' -> offset++
                    '}' -> { offset++; return Json.Obj(fields) }
                    else -> throw JsonDecodeException("expected ',' or '}' at $offset")
                }
            }
        }

        private fun arrayValue(depth: Int): Json {
            expect("[")
            val items = ArrayList<Json>()
            skipWhitespace()
            if (peek() == ']') { offset++; return Json.Arr(items) }
            while (true) {
                items.add(value(depth + 1))
                skipWhitespace()
                when (peek()) {
                    ',' -> offset++
                    ']' -> { offset++; return Json.Arr(items) }
                    else -> throw JsonDecodeException("expected ',' or ']' at $offset")
                }
            }
        }

        private fun number(): Json {
            val start = offset
            if (peek() == '-') offset++
            while (!atEnd() && s[offset] in '0'..'9') offset++
            if (!atEnd() && s[offset] == '.') {
                offset++
                while (!atEnd() && s[offset] in '0'..'9') offset++
            }
            if (!atEnd() && (s[offset] == 'e' || s[offset] == 'E')) {
                offset++
                if (!atEnd() && (s[offset] == '+' || s[offset] == '-')) offset++
                while (!atEnd() && s[offset] in '0'..'9') offset++
            }
            val raw = s.substring(start, offset)
            if (raw.isEmpty() || raw == "-") throw JsonDecodeException("bad number at $start")
            return Json.Num(raw)
        }

        private fun string(): String {
            expect("\"")
            val sb = StringBuilder()
            while (true) {
                if (atEnd()) throw JsonDecodeException("unterminated string")
                val c = s[offset]
                when {
                    c == '"' -> { offset++; return sb.toString() }
                    c == '\\' -> {
                        offset++
                        if (atEnd()) throw JsonDecodeException("unterminated escape")
                        when (val e = s[offset]) {
                            '"' -> { sb.append('"'); offset++ }
                            '\\' -> { sb.append('\\'); offset++ }
                            '/' -> { sb.append('/'); offset++ }
                            'b' -> { sb.append('\b'); offset++ }
                            'f' -> { sb.append('\u000C'); offset++ }
                            'n' -> { sb.append('\n'); offset++ }
                            'r' -> { sb.append('\r'); offset++ }
                            't' -> { sb.append('\t'); offset++ }
                            'u' -> {
                                offset++
                                val unit = hex4()
                                if (unit in 0xD800..0xDBFF && offset + 1 < s.length &&
                                    s[offset] == '\\' && s[offset + 1] == 'u'
                                ) {
                                    val save = offset
                                    offset += 2
                                    val low = hex4()
                                    if (low in 0xDC00..0xDFFF) {
                                        sb.append(unit.toChar()).append(low.toChar())
                                    } else {
                                        // Not a pair: the high surrogate is a lone one, and the
                                        // second escape is an ordinary code unit. Rewind so the
                                        // loop reads it again.
                                        sb.append('�')
                                        offset = save
                                    }
                                } else if (unit in 0xD800..0xDFFF) {
                                    sb.append('�') // lone surrogate -> U+FFFD (D7)
                                } else {
                                    sb.append(unit.toChar())
                                }
                            }
                            else -> throw JsonDecodeException("bad escape '\\$e' at $offset")
                        }
                    }
                    c < ' ' -> throw JsonDecodeException("raw control character at $offset")
                    else -> { sb.append(c); offset++ }
                }
            }
        }

        private fun hex4(): Int {
            if (offset + 4 > s.length) throw JsonDecodeException("truncated \\u escape")
            var v = 0
            repeat(4) {
                val d = s[offset++].digitToIntOrNull(16)
                    ?: throw JsonDecodeException("bad \\u escape")
                v = v * 16 + d
            }
            return v
        }

        private fun peek(): Char =
            if (atEnd()) throw JsonDecodeException("unexpected end of input") else s[offset]

        private fun expect(literal: String) {
            if (!s.startsWith(literal, offset)) {
                throw JsonDecodeException("expected '$literal' at $offset")
            }
            offset += literal.length
        }
    }
}
