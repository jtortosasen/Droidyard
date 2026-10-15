package gittest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Decision (plan m2n part 2, step P1b): Remote makes the shape the write tests and the
// level-5 runs assert on: main, feature/x and y on the remote, only a local main in the
// clones, the hooks and the empty credential.helper in `w`, and a colleague who pushes.
func TestRemoteShape(t *testing.T) {
	t.Parallel()
	r := Remote(t, filepath.Join(t.TempDir(), "fx"))
	heads := strings.Fields(out(t, r.Origin, "for-each-ref", "--format=%(refname)", "refs/heads"))
	if want := []string{"refs/heads/feature/x", "refs/heads/main", "refs/heads/y"}; strings.Join(heads, " ") != strings.Join(want, " ") {
		t.Errorf("the remote's branches = %v, want %v", heads, want)
	}
	for name, oid := range r.Oids {
		if got := out(t, r.Origin, "rev-parse", "refs/heads/"+name); got != oid {
			t.Errorf("remote %s = %s, want %s", name, got, oid)
		}
	}
	for _, clone := range []string{r.W, r.Other} {
		if got := out(t, clone, "for-each-ref", "--format=%(refname)", "refs/heads"); got != "refs/heads/main" {
			t.Errorf("%s: local branches = %q, want main only", filepath.Base(clone), got)
		}
		if got := out(t, clone, "for-each-ref", "--format=%(refname)", "refs/remotes/origin"); !strings.Contains(got, "origin/feature/x") || !strings.Contains(got, "origin/y") {
			t.Errorf("%s: remote-tracking branches = %q", filepath.Base(clone), got)
		}
		if got := out(t, clone, "config", "--list", "--local"); !strings.HasSuffix(got, "credential.helper=") {
			t.Errorf("%s: no empty credential.helper in\n%s", filepath.Base(clone), got)
		}
	}
	for _, h := range []string{"post-checkout", "reference-transaction"} {
		if fi, err := os.Stat(filepath.Join(r.W, ".git", "hooks", h)); err != nil || fi.Mode()&0o111 == 0 {
			t.Errorf("w: hook %s: %v", h, err)
		}
	}
	if _, err := os.Stat(r.Markers); err == nil {
		t.Errorf("a hook ran while the fixture was built: %s", r.Markers)
	}
	// The hooks work: plain git runs them (the write tests rely on this control).
	newGit(t).run(r.W, nil, "branch", "-q", "scratch")
	if b, _ := os.ReadFile(r.Markers); !strings.Contains(string(b), "reference-transaction") {
		t.Errorf("plain git left markers %q, want the reference-transaction hook", b)
	}
	newGit(t).run(r.W, nil, "switch", "-q", "scratch")
	if b, _ := os.ReadFile(r.Markers); !strings.Contains(string(b), "post-checkout") {
		t.Errorf("plain git left markers %q, want the post-checkout hook", b)
	}
	pushed := r.PushOther(t, 1)
	if got := out(t, r.Origin, "rev-parse", "main"); got != pushed {
		t.Errorf("after PushOther the remote's main = %s, want %s", got, pushed)
	}
	if got := out(t, r.W, "rev-parse", "origin/main"); got != r.Oids["main"] {
		t.Errorf("w saw the push without fetching")
	}
}

// Decision (plan m2n part 2, step P1b): Submodules makes `lib` and `lib/nested`
// initialised at the recorded commits, `extra` uninitialised, and the hooks in the three
// git directories a daemon's update can touch.
func TestSubmodulesShape(t *testing.T) {
	t.Parallel()
	s := Submodules(t, filepath.Join(t.TempDir(), "fx"))
	status := out(t, s.Super, "submodule", "status", "--recursive")
	lines := strings.Split(status, "\n")
	if len(lines) != 3 {
		t.Fatalf("submodule status = %q, want lib, lib/nested and extra", status)
	}
	// submodule status: "-" uninitialised, " " at the recorded commit; `out` trimmed the first.
	want := map[string]string{"lib": s.Oids["lib2"], "lib/nested": s.Oids["nested2"], "extra": "-" + s.Oids["extra"]}
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) < 2 || want[f[1]] != f[0] {
			t.Errorf("submodule status line %q, want %v", l, want)
		}
	}
	if got := out(t, s.Super, "ls-tree", "HEAD", "lib"); !strings.Contains(got, s.Oids["lib2"]) {
		t.Errorf("super records %q, want lib2", got)
	}
	if got := out(t, filepath.Join(s.Super, "lib"), "ls-tree", "HEAD", "nested"); !strings.Contains(got, s.Oids["nested2"]) {
		t.Errorf("lib records %q, want nested2", got)
	}
	if got := out(t, s.Super, "config", "--list", "--local"); strings.Contains(got, "protocol.file.allow") {
		t.Errorf("s admits the file transport in its own configuration:\n%s", got)
	}
	for _, gd := range []string{
		filepath.Join(s.Super, ".git"),
		filepath.Join(s.Super, ".git", "modules", "lib"),
		filepath.Join(s.Super, ".git", "modules", "lib", "modules", "nested"),
	} {
		for _, h := range []string{"post-checkout", "reference-transaction"} {
			if _, err := os.Stat(filepath.Join(gd, "hooks", h)); err != nil {
				t.Errorf("hook %s: %v", h, err)
			}
		}
	}
	if _, err := os.Stat(s.Markers); err == nil {
		t.Errorf("a hook ran while the fixture was built")
	}
}
