package assistant

import (
	"strings"
	"unicode/utf8"
)

// The reader of a hook command line, kept to recognise the entries an earlier init wrote
// (hookentry.go): a hand-written wrapper does not split into words and is the user's.

// wordRune is one character of a shell word; quoted means quoted or escaped, so it is
// never a glob character.
type wordRune struct {
	r      rune
	quoted bool
}

type word []wordRune

func (w word) String() string {
	var b strings.Builder
	for _, c := range w {
		b.WriteRune(c.r)
	}
	return b.String()
}

// splitWords splits a command into words as `sh` would, for single and double quotes
// and backslashes. It refuses (ok false) whatever it does not model: an operator, a
// redirection, a substitution, an expansion, a comment, a glob bracket, a control
// character, an unterminated quote.
func splitWords(s string) (words []word, ok bool) {
	var cur word
	started := false
	flush := func() {
		if started {
			words = append(words, cur)
		}
		cur, started = nil, false
	}
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n <= 1 {
			return nil, false
		}
		i += n
		if (r < 0x20 && r != '\t') || r == 0x7f {
			return nil, false
		}
		switch r {
		case ' ', '\t':
			flush()
		case ';', '&', '|', '<', '>', '(', ')', '`', '$', '{', '}', '[':
			return nil, false
		case '~', '#':
			if !started {
				return nil, false
			}
			cur = append(cur, wordRune{r: r})
		case '\'':
			end := strings.IndexByte(s[i:], '\'')
			if end < 0 {
				return nil, false
			}
			for _, q := range s[i : i+end] {
				if (q < 0x20 && q != '\t') || q == 0x7f {
					return nil, false
				}
				cur = append(cur, wordRune{q, true})
			}
			i += end + 1
			started = true
		case '"':
			started = true
			closed := false
			for i < len(s) {
				q, n := utf8.DecodeRuneInString(s[i:])
				if q == utf8.RuneError && n <= 1 {
					return nil, false
				}
				i += n
				if (q < 0x20 && q != '\t') || q == 0x7f {
					return nil, false
				}
				if q == '"' {
					closed = true
					break
				}
				if q == '$' || q == '`' {
					return nil, false
				}
				if q == '\\' && i < len(s) {
					nq, nn := utf8.DecodeRuneInString(s[i:])
					if nq == '$' || nq == '`' || nq == '"' || nq == '\\' {
						q = nq
						i += nn
					}
				}
				cur = append(cur, wordRune{q, true})
			}
			if !closed {
				return nil, false
			}
		case '\\':
			if i >= len(s) {
				return nil, false
			}
			q, n := utf8.DecodeRuneInString(s[i:])
			i += n
			if (q < 0x20 && q != '\t') || q == 0x7f {
				return nil, false
			}
			cur = append(cur, wordRune{q, true})
			started = true
		default:
			cur = append(cur, wordRune{r: r})
			started = true
		}
	}
	flush()
	return words, true
}

// shellArg quotes a string as one POSIX shell word, leaving a plain path as it is. A
// single quote is closed, escaped and reopened, so any path survives.
func shellArg(s string) string {
	plain := s != ""
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_@%+=:,./-", c)) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
