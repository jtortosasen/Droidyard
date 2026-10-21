package capability

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
)

// The compact text renderings (ADR-0036 Decision 2; mcp.md §Results). A descriptor may
// declare one (Descriptor.Text); the MCP adapter then sends it as the text block, in place
// of the result's compact JSON, and the structured content stays the full result.
//
// A rendering has many lines, where compact JSON had one, so a string that came from a
// device or an app could otherwise forge a line of ours (ADR-0015 §1). Text is the one
// way a renderer writes: it has no method that takes a raw string, only ones that emit a
// quoted string (Q, Pre) or a bare word that is safe or else quoted (W, List), so a
// renderer cannot write device text unquoted, whatever it passes in. The tests that hold
// this are TestEveryTaggedStringIsQuoted (internal/schema), which injects a forged line
// into every string under an untrusted-tagged field of each rendered result type, and the
// adapter's own check on what it sends.

// Quote is s as a JSON string: line breaks, quotes and every control and format character
// escaped, and nothing but the escapes added (< > & are left as they are). JSON leaves the
// C1 controls (U+0080–U+009F, U+0085 among them, which some line splitters read as a break
// and U+009B, which a terminal reads as CSI), DEL and the format characters (U+202E and the
// other bidi controls, zero-width ones, U+FEFF) alone, so Quote escapes them as \uXXXX,
// a pair of escapes above U+FFFF. It is the single quoting function of every rendering.
func Quote(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(s) != nil {
		return `""` // a string always encodes; kept so that Quote cannot fail open
	}
	out := strings.TrimSuffix(buf.String(), "\n")
	if strings.IndexFunc(out, escapedByQuote) < 0 {
		return out
	}
	var b strings.Builder
	for _, r := range out {
		if !escapedByQuote(r) {
			b.WriteRune(r)
			continue
		}
		if r > 0xFFFF {
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&b, `\u%04x\u%04x`, hi, lo)
			continue
		}
		fmt.Fprintf(&b, `\u%04x`, r)
	}
	return b.String()
}

// escapedByQuote is a rune JSON's encoder writes raw that Quote escapes: a control
// character (C0 ones are already escaped by then) or a format character.
func escapedByQuote(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}

// wordSafe reports whether s can stand in a rendering as it is: one or more of letters,
// digits and . _ - : / + @ # = , (no space, quote, bracket, backtick or control
// character), and not starting like our fixed first line, so that a word at the start of
// a line cannot read as it.
func wordSafe(s string) bool {
	if s == "" || len(s) > 256 || strings.HasPrefix(strings.ToLower(s), "untrusted") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("._-:/+@#=,>", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// word is s bare when wordSafe, else Quote(s).
func word(s string) string {
	if wordSafe(s) {
		return s
	}
	return Quote(s)
}

// Text builds a rendering line by line. Items on a line are separated by one space.
type Text struct {
	b    strings.Builder
	line bool // the current line has an item, so the next one needs a space
}

func (t *Text) item(s string) *Text {
	if t.line {
		t.b.WriteByte(' ')
	}
	t.b.WriteString(s)
	t.line = true
	return t
}

// W writes each s as a word: bare when it is made of safe characters (a keyword, a ref, a
// number, a timestamp, an enum value), else quoted.
func (t *Text) W(words ...string) *Text {
	for _, w := range words {
		t.item(word(w))
	}
	return t
}

// Q writes s quoted, always.
func (t *Text) Q(s string) *Text { return t.item(Quote(s)) }

// Pre writes prefix directly followed by s quoted, as one item: Pre("#", id) is #"id",
// Pre("desc=", d) is desc="d". The prefix is written as a word, so it can only be a short
// literal of the safe characters; one that is not is quoted whole.
func (t *Text) Pre(prefix, s string) *Text {
	if !wordSafe(prefix) {
		return t.item(Quote(prefix + s))
	}
	return t.item(prefix + Quote(s))
}

// Int writes n.
func (t *Text) Int(n int64) *Text { return t.item(strconv.FormatInt(n, 10)) }

// Ints writes ns joined by commas, one item: 188,442.
func (t *Text) Ints(ns ...int) *Text {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return t.item(strings.Join(parts, ","))
}

// Box writes a rectangle as [left,top,right,bottom], one item.
func (t *Text) Box(b [4]int) *Text {
	return t.item("[" + strconv.Itoa(b[0]) + "," + strconv.Itoa(b[1]) + "," + strconv.Itoa(b[2]) + "," + strconv.Itoa(b[3]) + "]")
}

// List writes items as words joined by commas, one item: clickable,focused. Nothing is
// written for an empty list.
func (t *Text) List(items []string) *Text {
	if len(items) == 0 {
		return t
	}
	parts := make([]string, len(items))
	for i, s := range items {
		parts[i] = word(s)
	}
	return t.item(strings.Join(parts, ","))
}

// Indent starts a new line indented by n spaces. It is called at the start of a line.
func (t *Text) Indent(n int) *Text {
	t.Nl()
	if n > 0 {
		t.b.WriteString(strings.Repeat(" ", n))
	}
	return t
}

// Started reports whether the current line has an item.
func (t *Text) Started() bool { return t.line }

// Nl ends the current line, if it has any item.
func (t *Text) Nl() *Text {
	if t.line {
		t.b.WriteByte('\n')
		t.line = false
	}
	return t
}

// String is the text so far, without a trailing line break.
func (t *Text) String() string { return strings.TrimRight(t.b.String(), "\n") }

// TextOf adapts a renderer of one result type to Descriptor.Text. The adapter returns ""
// ("no rendering": the caller falls back to the compact JSON) for a result of another type,
// so a renderer never sees one it was not written for. A result is accepted as a value or
// as a pointer.
func TextOf[T any](render func(T) string) func(any) string {
	return func(v any) string {
		switch r := v.(type) {
		case T:
			return render(r)
		case *T:
			if r != nil {
				return render(*r)
			}
		}
		return ""
	}
}
