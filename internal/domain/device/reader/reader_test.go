package reader

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Decision (ADR-0029: the dex is a pinned build artefact): the embedded bytes are the
// ones the pinned digest names, so a rebuilt dex without its digest, or a digest edited
// without its dex, fails here.
func TestEmbeddedDexMatchesThePinnedDigest(t *testing.T) {
	if got := Digest(); got != SHA256 {
		t.Fatalf("embedded reader.dex has SHA-256 %s, reader.go pins %s", got, SHA256)
	}
	if len(dex) < 4 || string(dex[:4]) != "dex\n" {
		t.Errorf("embedded file is not a dex (%d bytes)", len(dex))
	}
}

// Decision (reproducible build): build.sh, run on the checked-in source, gives the
// embedded bytes. Level 2 where the SDK is absent: the test then skips, and says so.
func TestBuildScriptReproducesTheDex(t *testing.T) {
	sdk := os.Getenv("ANDROID_HOME")
	if sdk == "" {
		sdk = os.Getenv("ANDROID_SDK_ROOT")
	}
	if sdk == "" {
		home, _ := os.UserHomeDir()
		sdk = filepath.Join(home, "Library", "Android", "sdk")
	}
	if _, err := os.Stat(filepath.Join(sdk, "build-tools", "37.0.0", "d8")); err != nil {
		t.Skipf("level 2 only: no Android SDK build-tools 37.0.0 at %s", sdk)
	}
	if _, err := exec.LookPath("javac"); err != nil {
		t.Skip("level 2 only: no javac")
	}
	// The pinned bytes are javac 17.0.11's (build.sh); another javac emits other bytes.
	if v, _ := exec.Command("javac", "-version").CombinedOutput(); strings.TrimSpace(string(v)) != "javac 17.0.11" {
		t.Skipf("level 2 only: host %s, reader.dex was built with javac 17.0.11", strings.TrimSpace(string(v)))
	}
	out := filepath.Join(t.TempDir(), "reader.dex")
	cmd := exec.Command("sh", "build.sh")
	cmd.Env = append(os.Environ(), "OUT="+out, "ANDROID_HOME="+sdk)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build.sh: %v\n%s", err, b)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, dex) {
		t.Errorf("build.sh output differs from the embedded reader.dex (%d against %d bytes): rebuild, and update SHA256", len(got), len(dex))
	}
}
