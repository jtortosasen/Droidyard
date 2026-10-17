package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/system"
)

// TestVersionLine pins the display of the build beside the API version (ADR-0068 §2).
func TestVersionLine(t *testing.T) {
	for build, want := range map[string]string{
		"v1.2.3": "droidyard v1.2.3 (API 9.9)",
		"dev":    "droidyard dev (API 9.9)",
	} {
		if got := versionLine(build, "9.9"); got != want {
			t.Errorf("versionLine(%q) = %q, want %q", build, got, want)
		}
	}
}

// TestVersionLicenses expects `version --licenses` to print the embedded scrcpy licence,
// and bare `version` to stay one line (install.sh parses it).
func TestVersionLicenses(t *testing.T) {
	var b bytes.Buffer
	if code := versionCommand(&b, []string{"--licenses"}); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	for _, want := range []string{"Apache License", "Version 2.0"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("--licenses output lacks %q", want)
		}
	}
	b.Reset()
	versionCommand(&b, nil)
	if strings.Count(b.String(), "\n") != 1 {
		t.Errorf("bare version printed %q, want one line", b.String())
	}
}

// TestVersionCommand runs the binary's `version` and expects the build and the API
// version, so a command that prints only the API version fails.
func TestVersionCommand(t *testing.T) {
	cmd := exec.Command(os.Args[0], "version")
	cmd.Env = append(os.Environ(), reexec+"=1", "DROIDYARD_STATE_DIR="+t.TempDir())
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	want := "droidyard dev (API " + system.Version + ")\n"
	if string(out) != want {
		t.Errorf("version printed %q, want %q", out, want)
	}
}
