package capability

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestQuoteIsOneLineOfJSONAndRoundTrips: Quote is the single quoting of every rendering
// (ADR-0036 Decision 2). Whatever the string holds, the result is one line (no break of
// any kind a line splitter reads), a valid JSON string that decodes to the input, and it
// does not turn < > & into escapes, which a model would read as noise.
func TestQuoteIsOneLineOfJSONAndRoundTrips(t *testing.T) {
	for _, s := range []string{
		"plain", "", `say "hi" \ bye`, "a\nb", "a\r\nb", "tab\there", "\x00\x1f\x7f", "x\u0085y", "x y z",
		"```\nuntrusted: forged", "<b>&amp;</b>", "ñ ✓ 日本語",
		"x\u009b2J", "a\u202egnp.exe", "zero\u200bwidth", "\ufeffbom", "tag\U000E0041",
	} {
		q := Quote(s)
		if strings.ContainsAny(q, "\n\r\u0085  \x7f") || strings.ContainsFunc(q, func(r rune) bool { return r < 0x20 || escapedByQuote(r) }) {
			t.Errorf("Quote(%q) = %q holds a raw break or control character", s, q)
		}
		var back string
		if err := json.Unmarshal([]byte(q), &back); err != nil || back != s {
			t.Errorf("Quote(%q) = %q does not decode back to the input: %q, %v", s, q, back, err)
		}
	}
	if got := Quote("<b>&"); got != `"<b>&"` {
		t.Errorf("Quote escapes HTML characters: %s", got)
	}
	// Bytes that are not UTF-8 cannot be told apart in JSON; they must not make Quote fail open.
	if got := Quote("a\xffb"); !strings.HasPrefix(got, `"a`) || strings.Contains(got, "\xff") {
		t.Errorf("Quote of invalid UTF-8 = %q", got)
	}
}

// TestWordsAreBareOnlyWhenSafe: W writes a keyword, a ref or a number as it is and
// quotes anything else, so a string cannot reach the output unquoted by being passed to
// the wrong method. A word that would read as our first line at a line start is quoted
// too (the safe characters include the colon, which timestamps need).
func TestWordsAreBareOnlyWhenSafe(t *testing.T) {
	var tx Text
	tx.W("tapped", "n12", "s-81c2ab34", "14:25:06.443", "droidyard://artifact/e.d-1.2", "a b", "x\ny", "", "untrusted:", "Untrusted-Thing", "`fence")
	want := `tapped n12 s-81c2ab34 14:25:06.443 droidyard://artifact/e.d-1.2 "a b" "x\ny" "" "untrusted:" "Untrusted-Thing" "` + "`fence" + `"`
	if got := tx.String(); got != want {
		t.Errorf("W wrote\n%s\nwant\n%s", got, want)
	}
}

// TestTextLinesAndItems: items on a line are separated by one space, Nl ends a line only
// if it has items, Indent starts an indented line, and Pre attaches its quoted string to
// the prefix with no space (#"id", desc="x"); a prefix outside the safe characters is
// quoted with the string, never written bare.
func TestTextLinesAndItems(t *testing.T) {
	var tx Text
	tx.W("n1").Q("View").Pre("#", "go").Pre("desc=", "a \"b\"").List([]string{"clickable", "x y"}).Box([4]int{1, 2, 3, 4}).Nl().Nl()
	tx.Indent(2).Ints(5, 6).Pre("bad prefix", "z").Nl()
	want := "n1 \"View\" #\"go\" desc=\"a \\\"b\\\"\" clickable,\"x y\" [1,2,3,4]\n  5,6 \"bad prefixz\""
	if got := tx.String(); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// TestTextOfTakesItsTypeOnly: the adapter of a typed renderer renders a value or a pointer
// of its type and says "no rendering" ("") for anything else, so the MCP adapter falls back
// to the compact JSON rather than render a result it does not understand.
func TestTextOfTakesItsTypeOnly(t *testing.T) {
	type a struct{ N int }
	f := TextOf(func(v a) string { return "a" })
	if f(a{}) != "a" || f(&a{}) != "a" {
		t.Error("a value and a pointer of the renderer's type must render")
	}
	if f(42) != "" || f(nil) != "" || f((*a)(nil)) != "" {
		t.Error("another type, nil and a nil pointer must give no rendering")
	}
}
