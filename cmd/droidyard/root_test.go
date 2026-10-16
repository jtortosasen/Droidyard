package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jtortosasen/droidyard/internal/platform"
)

// buildAt makes dir/rel a Gradle build (a settings.gradle.kts in it).
func buildAt(t *testing.T, dir, rel string) {
	t.Helper()
	d := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "settings.gradle.kts"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestProjectOf pins ADR-0075 §6: the upward walk is unchanged, and the top of a
// repository is a project only when its Gradle root resolves (one candidate, or a valid
// key). The home directory is never one, so a user who runs `droidyard` in ~ is not
// offered the one build somewhere under it.
func TestProjectOf(t *testing.T) {
	// HOME is set, so the test cannot run in parallel.
	home := platform.Current().Canonical(t.TempDir())
	t.Setenv("HOME", home)
	mk := func() string { return platform.Current().Canonical(t.TempDir()) }

	top := mk()
	buildAt(t, top, "dev")
	two := mk()
	buildAt(t, two, "a")
	buildAt(t, two, "b")
	keyed := mk()
	buildAt(t, keyed, "a")
	buildAt(t, keyed, "b")
	if err := os.MkdirAll(filepath.Join(keyed, ".droidyard"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keyed, ".droidyard", "project.json"), []byte(`{"schemaVersion":1,"gradleRoot":"b"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	buildAt(t, home, "x/y") // exactly one build, two levels below HOME

	for _, c := range []struct {
		name string
		cwd  string
		want string // "" is not found
	}{
		{"the top of a repository with one build below", top, top},
		{"inside that build: the walk finds it", filepath.Join(top, "dev"), filepath.Join(top, "dev")},
		{"below that build", filepath.Join(top, "dev", "sub"), filepath.Join(top, "dev")},
		{"two builds and no choice", two, ""},
		{"two builds and a valid key", keyed, keyed},
		{"the home directory holding one build two levels down", home, ""},
		{"the filesystem root", string(filepath.Separator), ""},
	} {
		if c.cwd == filepath.Join(top, "dev", "sub") {
			os.MkdirAll(c.cwd, 0o755)
		}
		got, ok := projectOf(c.cwd)
		if (c.want == "") == ok || got != c.want {
			t.Errorf("%s: projectOf(%s) = %q, %v; want %q", c.name, c.cwd, got, ok, c.want)
		}
	}
}
