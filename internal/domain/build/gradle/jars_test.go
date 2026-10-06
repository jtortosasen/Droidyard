package gradle

import (
	"bytes"
	"os"
	"testing"

	"github.com/jtortosasen/droidyard/sidecar"
)

// Decision (D5; step 5 review B2): a cached jar is checked by its bytes, so one altered
// in place with the same size is rewritten before the sidecar runs it. Seen failing
// with the size-only check.
func TestCachedJarAlteredInPlaceIsRewritten(t *testing.T) {
	dir := t.TempDir()
	jars, err := cacheJars(dir)
	if err != nil {
		t.Fatal(err)
	}
	garbage := bytes.Repeat([]byte{0xAA}, len(sidecar.Jar()))
	if err := os.WriteFile(jars.Class, garbage, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := cacheJars(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(jars.Class)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, sidecar.Jar()) {
		t.Fatal("a same-size altered jar was left in the cache")
	}
}

// Decision (step 5 review N5): a relative PATH entry is never searched, so a daemon
// started inside a project never runs that project's own java.
func TestLookPathJavaSkipsRelativeEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/bin", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/bin/java", []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if got := lookPathJava("bin"); got != "" {
		t.Fatalf("relative PATH entry searched: %q", got)
	}
	if got := lookPathJava(dir + "/bin"); got != dir+"/bin/java" {
		t.Fatalf("absolute entry: %q", got)
	}
}
