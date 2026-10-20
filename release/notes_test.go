package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestNotes drives notes.sh in a synthetic repository: a first release, then a merged
// branch and a commit made on main, then a second release.
func TestNotes(t *testing.T) {
	script, err := filepath.Abs("notes.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	env := append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GITHUB_REPOSITORY=o/r")
	run := func(name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		return string(out)
	}
	run("git", "init", "-q", "-b", "main")
	run("git", "commit", "-q", "--allow-empty", "-m", "the start")
	run("git", "tag", "v0.1.0")
	run("git", "checkout", "-q", "-b", "m2x")
	run("git", "commit", "-q", "--allow-empty", "-m", "m2x step 1: not listed")
	run("git", "checkout", "-q", "main")
	run("git", "merge", "-q", "--no-ff", "m2x", "-m", "Merge m2x: a feature")
	run("git", "commit", "-q", "--allow-empty", "-m", "register: a row")
	run("git", "tag", "v0.2.0")

	first := run(script, "v0.1.0")
	if !strings.HasPrefix(first, "The first release.\n") || strings.Contains(first, "the start") {
		t.Errorf("v0.1.0 notes:\n%s", first)
	}
	second := run(script, "v0.2.0")
	for _, want := range []string{
		"Changes since v0.1.0:\n\n- register: a row\n- m2x: a feature\n\n",
		"Full diff: https://github.com/o/r/compare/v0.1.0...v0.2.0\n",
		"curl -fsSL https://raw.githubusercontent.com/o/r/main/install.sh | sh\n",
	} {
		if !strings.Contains(second, want) {
			t.Errorf("v0.2.0 notes lack %q:\n%s", want, second)
		}
	}
	if strings.Contains(second, "step 1") || strings.Contains(second, "Merge ") {
		t.Errorf("v0.2.0 notes list a branch's own commit or a raw merge subject:\n%s", second)
	}

	cmd := exec.Command(script, "v9.9.9")
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "no tag v9.9.9") {
		t.Errorf("an unknown tag: err %v, output %q", err, out)
	}
}
