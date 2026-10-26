package sdk

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sha1hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestLicenceHash pins sdklib's hash (ADR-0130 §3; the spike's §(a)): sha1 of the UTF-8
// text after TrimStringAdapter's four steps. The raw text and a plainly trimmed one hash
// to values no Android tool writes, so the test fails if either is used.
func TestLicenceHash(t *testing.T) {
	// Leading newline and indented title; a blank line (two newlines) that must stay;
	// runs of spaces inside a line; lines that end in a single newline (joined by a
	// space); a tab-and-space indent after a newline; three newlines that stay; trailing
	// spaces and a final newline.
	raw := "\n   Title line\n\n  indented  para   with  gaps\nsecond line\n\t tabbed\n\n\nend  \n"
	// Step 1 removes spaces and tabs that follow whitespace, step 2 turns each lone
	// newline into a space, step 3 collapses spaces, step 4 trims.
	normalised := "Title line\n\nindented para with gaps second line tabbed\n\n\nend"

	got := licenceHash(raw)
	if want := sha1hex(normalised); got != want {
		t.Errorf("licenceHash = %s, want sha1 of the normalised text %s", got, want)
	}
	if got == sha1hex(raw) {
		t.Error("the hash is the raw text's sha1")
	}
	if got == sha1hex(strings.TrimSpace(raw)) {
		t.Error("the hash is the plainly trimmed text's sha1")
	}
	if trimStringAdapter(raw) != normalised {
		t.Errorf("trimStringAdapter = %q, want %q", trimStringAdapter(raw), normalised)
	}
}

// TestLicenceHashSteps pins each of the four steps on its own, so a step reversed is
// caught where it is reversed.
func TestLicenceHashSteps(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"spaces after a newline go", "a\n   b", "a b"},
		{"a tab after a space goes", "a \t b", "a b"},
		{"a lone newline is a space", "a\nb", "a b"},
		{"a blank line stays", "a\n\nb", "a\n\nb"},
		{"CRLF: the lone LF becomes a space", "a\r\nb", "a\r b"},
		{"runs of spaces collapse", "a    b", "a b"},
		{"trim removes control characters at the ends", "\x01 \n a \t\x1f", "a"},
		{"inner non-breaking space is kept", "a  b", "a  b"},
	} {
		if got := trimStringAdapter(c.in); got != c.want {
			t.Errorf("%s: %q -> %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestLicenceAcceptedByHash pins what accepted means: a regular file licenses/<id> holding
// a line equal to the current text's hash. An older text's hash, no file, and a symlink to
// a file that has the hash are not.
func TestLicenceAcceptedByHash(t *testing.T) {
	current := licenceHash(fxLicenceText)
	older := licenceHash(fxOtherLicenceText)
	root := t.TempDir()
	dir := filepath.Join(root, "licenses")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(id, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, id), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("current", "\n"+older+"\n"+current+"\n")                         // a line equal among others, as the owner's file has
	write("older-only", "\n"+older)                                        // only an older text
	write("crlf", current+"\r\n")                                          // Windows line ends
	write("target", "\n"+current)                                          // a file that has the hash...
	os.Symlink(filepath.Join(dir, "target"), filepath.Join(dir, "linked")) // ...and a link to it
	write("partial", current[:30])                                         // a prefix of the hash is not a line equal to it

	for _, c := range []struct {
		id   string
		want bool
	}{
		{"current", true}, {"crlf", true}, {"older-only", false}, {"absent", false},
		{"linked", false}, {"partial", false}, {"target", true},
	} {
		if got := licenseAccepted(root, c.id, current); got != c.want {
			t.Errorf("licence %s accepted = %v, want %v", c.id, got, c.want)
		}
	}
	// A licence id that is not a valid id never reaches a path.
	if licenseAccepted(root, "../licenses/current", current) {
		t.Error("an id with a path in it was read")
	}
	// A file over the bound is not read.
	write("huge", strings.Repeat("x\n", maxLicenseFile)+current)
	if licenseAccepted(root, "huge", current) {
		t.Error("a file over the bound was read")
	}
}
