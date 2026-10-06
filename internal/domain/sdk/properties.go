package sdk

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ParseProperties parses Java .properties syntax, as java.util.Properties.load does:
// `#` and `!` start a comment, `=`, `:` or whitespace separates key from value, `\`
// escapes the next character (\t \n \r \f, a literal \ \= \: \ # \!, a space, and
// \uXXXX), and a line ending in an unescaped `\` continues on the next line with its
// leading whitespace removed. The last value for a key wins.
//
// This is the syntax a project's local.properties is written in, which AGP reads for
// sdk.dir (sdk.md §Project SDK; plan m1h step 4). Bytes are decoded as UTF-8, not
// ISO-8859-1: a non-ASCII byte is taken as UTF-8, which is what Android Studio writes.
func ParseProperties(r io.Reader) (map[string]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	props := map[string]string{}
	for _, line := range logicalLines(string(data)) {
		key, value := splitProperty(line)
		props[key] = value
	}
	return props, nil
}

// ReadLocalProperties reads <root>/local.properties in Java properties syntax. A file
// that does not exist is an empty map and no error: a project need not have one.
func ReadLocalProperties(root string) (map[string]string, error) {
	f, err := os.Open(filepath.Join(root, "local.properties"))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	defer f.Close()
	return ParseProperties(f)
}

// logicalLines splits properties text into logical lines: physical lines joined across
// a trailing unescaped backslash, each with its leading whitespace removed; comments and
// blank lines are dropped.
func logicalLines(text string) []string {
	var out []string
	var cur strings.Builder
	continued := false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		line = strings.TrimLeft(line, " \t\f")
		if !continued {
			if line == "" || line[0] == '#' || line[0] == '!' {
				continue
			}
		}
		if endsWithOddBackslashes(line) {
			cur.WriteString(line[:len(line)-1])
			continued = true
			continue
		}
		cur.WriteString(line)
		out = append(out, cur.String())
		cur.Reset()
		continued = false
	}
	// A final line ending in a backslash: Java treats the continuation as empty.
	if continued {
		out = append(out, cur.String())
	}
	return out
}

// endsWithOddBackslashes reports whether line ends in an unescaped backslash, i.e. one
// that continues the line rather than being an escaped literal.
func endsWithOddBackslashes(line string) bool {
	n := 0
	for i := len(line) - 1; i >= 0 && line[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}

// splitProperty splits one logical line into its unescaped key and value, as
// Properties.load0 does.
func splitProperty(line string) (key, value string) {
	limit := len(line)
	keyLen, valueStart := 0, limit
	hasSep := false
	precedingBackslash := false
	for keyLen < limit {
		c := line[keyLen]
		if (c == '=' || c == ':') && !precedingBackslash {
			valueStart = keyLen + 1
			hasSep = true
			break
		} else if (c == ' ' || c == '\t' || c == '\f') && !precedingBackslash {
			valueStart = keyLen + 1
			break
		}
		if c == '\\' {
			precedingBackslash = !precedingBackslash
		} else {
			precedingBackslash = false
		}
		keyLen++
	}
	for valueStart < limit {
		c := line[valueStart]
		if c != ' ' && c != '\t' && c != '\f' {
			if !hasSep && (c == '=' || c == ':') {
				hasSep = true
			} else {
				break
			}
		}
		valueStart++
	}
	return unescape(line[:keyLen]), unescape(line[valueStart:])
}

// unescape applies Properties.loadConvert's escapes: \t \n \r \f, \uXXXX, and any other
// escaped character as itself. Without a backslash the string is returned unchanged.
func unescape(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			break
		}
		switch s[i] {
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 'f':
			b.WriteByte('\f')
		case 'u':
			if i+4 < len(s) {
				if n, err := strconv.ParseUint(s[i+1:i+5], 16, 32); err == nil {
					b.WriteRune(rune(n))
					i += 4
					continue
				}
			}
			b.WriteByte('u')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
