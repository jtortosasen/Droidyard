package sdk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseProperties pins the Java .properties syntax the reader must keep, as
// java.util.Properties.load: comments, both separators, whitespace separators, the
// escapes, continuation lines, and the last-value-wins rule. Reversing any of these (for
// example taking `:` as part of the key, or not stripping a continuation's leading
// whitespace) fails a case.
func TestParseProperties(t *testing.T) {
	in := `sdk.dir=/Users/x/Library/Android/sdk
# a comment
! also a comment
key:colon
space value
escaped\:key=with\:colon
trailing\ space=value\ with\ spaces
multi=one\
   two\
   three
windows=C\:\\Users\\x
unicode=\u0041\u00e9
backslash=a\\b
empty=
novalue
dup=first
dup=second
`
	got, err := ParseProperties(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"sdk.dir":        "/Users/x/Library/Android/sdk",
		"key":            "colon",
		"space":          "value",
		"escaped:key":    "with:colon",
		"trailing space": "value with spaces",
		"multi":          "onetwothree",
		"windows":        `C:\Users\x`,
		"unicode":        "A\u00e9",
		"backslash":      `a\b`,
		"empty":          "",
		"novalue":        "",
		"dup":            "second",
	}
	if len(got) != len(want) {
		t.Errorf("parsed %d keys (%v), want %d", len(got), got, len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%q = %q, want %q", k, got[k], v)
		}
	}
}

// TestReadLocalProperties pins the reader part B uses: an escaped-colon sdk.dir (a
// Windows-written file, which is legal Java properties), and a project with no
// local.properties being empty rather than an error.
func TestReadLocalProperties(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "local.properties"), []byte("sdk.dir=C\\:\\\\Android\\\\sdk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLocalProperties(root)
	if err != nil {
		t.Fatal(err)
	}
	if got["sdk.dir"] != `C:\Android\sdk` {
		t.Errorf("sdk.dir = %q, want C:\\Android\\sdk", got["sdk.dir"])
	}

	empty, err := ReadLocalProperties(t.TempDir())
	if err != nil || len(empty) != 0 {
		t.Errorf("a missing local.properties = %v, %v; want an empty map and no error", empty, err)
	}
}
