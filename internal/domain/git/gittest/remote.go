package gittest

// The fixtures of plan m2n part 2 (step P1 and the level-5 runs K4 describes): a work tree
// with a remote and a colleague who pushes to it, and a superproject with submodules.
// All remotes are local paths: nothing here touches the network, and nothing but the
// repositories it creates. Each work tree has a `post-checkout` and a
// `reference-transaction` hook that append a line to a marker file, and an empty
// credential.helper (both macOS gits ship `osxkeychain` at system scope), so a test or a
// run can tell whether a git ran a hook and cannot reach the owner's keychain.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Remotes is the repository Remote built.
type Remotes struct {
	// Origin is the bare remote `w-origin.git`; W the work tree `w`, a clone of it; Other
	// `w-other`, a second clone that pushes, standing in for a colleague.
	Origin, W, Other string
	// Markers is the file W's hooks append to: one line per hook run, "<hook> <cwd>".
	Markers string
	// Oids are the remote's branches as built: "main", "feature/x" and "y".
	Oids map[string]string
	g    *git
}

// hooks are the two hooks a trusted work tree has, each writing a line to marker.
var hooks = map[string]string{
	"post-checkout":         "echo \"post-checkout $PWD\" >> %s\n",
	"reference-transaction": "echo \"reference-transaction $1 $PWD\" >> %s\ncat >/dev/null\n",
}

// installHooks writes the marker-writing hooks into gitDir/hooks.
func (g *git) installHooks(gitDir, marker string) {
	g.t.Helper()
	if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o755); err != nil {
		g.t.Fatal(err)
	}
	for name, body := range hooks {
		script := "#!/bin/sh\n" + fmt.Sprintf(body, "'"+marker+"'")
		if err := os.WriteFile(filepath.Join(gitDir, "hooks", name), []byte(script), 0o755); err != nil {
			g.t.Fatal(err)
		}
	}
}

// writeFile writes rel under dir.
func writeFile(t testing.TB, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// save records everything in dir as one commit.
func (g *git) save(dir, message string) string {
	g.t.Helper()
	g.run(dir, nil, "add", "-A")
	g.run(dir, nil, "commit", "-q", "-m", message)
	return strings.TrimSpace(g.run(dir, nil, "rev-parse", "HEAD"))
}

// Remote builds, under dir (which it creates), `w-origin.git` (bare; branches main,
// feature/x and y), `w` and `w-other` (clones of it, each with only a local main: the
// other two branches are remote-tracking ones). The hooks are in `w`, and its
// credential.helper is empty. The test or run makes the colleague push with PushOther.
func Remote(t testing.TB, dir string) *Remotes {
	t.Helper()
	dir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	g := newGit(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Remotes{
		Origin: filepath.Join(dir, "w-origin.git"), W: filepath.Join(dir, "w"), Other: filepath.Join(dir, "w-other"),
		Markers: filepath.Join(dir, ".w-hook-markers"), Oids: map[string]string{}, g: g,
	}
	seed := filepath.Join(t.TempDir(), "seed")
	g.run(dir, nil, "init", "-q", "-b", "main", seed)
	writeFile(t, seed, "README.md", "# remote fixture\n")
	writeFile(t, seed, "shared.txt", "base\n")
	g.save(seed, "Initial")
	writeFile(t, seed, "a.txt", "a\n")
	r.Oids["main"] = g.save(seed, "Second")
	g.run(seed, nil, "branch", "-q", "feature/x")
	g.run(seed, nil, "branch", "-q", "y")
	g.run(seed, nil, "checkout", "-q", "feature/x")
	writeFile(t, seed, "x.txt", "x\n")
	writeFile(t, seed, "shared.txt", "feature x\n")
	r.Oids["feature/x"] = g.save(seed, "Feature x")
	g.run(seed, nil, "checkout", "-q", "y")
	writeFile(t, seed, "y.txt", "y\n")
	r.Oids["y"] = g.save(seed, "Branch y")

	g.run(dir, nil, "init", "-q", "--bare", "-b", "main", r.Origin)
	g.run(seed, nil, "push", "-q", r.Origin, "main", "feature/x", "y")
	for _, clone := range []string{r.W, r.Other} {
		g.run(dir, nil, "clone", "-q", r.Origin, clone)
		g.run(clone, nil, "config", "credential.helper", "")
	}
	g.installHooks(filepath.Join(r.W, ".git"), r.Markers)
	return r
}

// PushOther makes the colleague commit a new file on `w-other`'s main and push it to the
// remote, and returns the new commit. The file is named after n so that two pushes differ.
func (r *Remotes) PushOther(t testing.TB, n int) string {
	t.Helper()
	writeFile(t, r.Other, fmt.Sprintf("other-%d.txt", n), fmt.Sprintf("pushed %d\n", n))
	oid := r.g.save(r.Other, fmt.Sprintf("Pushed by the colleague %d", n))
	r.g.run(r.Other, nil, "push", "-q", "origin", "main")
	return oid
}

// Subs is the repository Submodules built.
type Subs struct {
	// Super is the work tree `s`; Origin its bare remote `s-origin.git`; Lib, Nested and
	// Extra the bare remotes of the submodules `lib`, `lib/nested` and `extra`.
	Super, Origin, Lib, Nested, Extra string
	// Markers is the file the hooks of `s` and of the two initialised submodules append to.
	Markers string
	// Oids: "nested1" and "nested2" (the commits of `lib/nested`), "lib1" and "lib2" (of
	// `lib`; lib1 records nested1, lib2 records nested2), "extra" and "super" (the
	// superproject's tip, which records lib2 and extra).
	Oids map[string]string
}

// Submodules builds, under dir (which it creates), the superproject `s` with the
// submodules `lib` and `lib/nested` initialised and checked out at the recorded commits
// and `extra` left uninitialised, cloned from the bare remotes `s-origin.git`,
// `s-lib.git`, `s-nested.git` and `s-extra.git`. The submodules' URLs are the remotes'
// absolute local paths; git's default refuses a local path for a submodule
// (protocol.file.allow=user), so the fixture is made with it allowed on the command line
// and `s`'s own configuration does not allow it (write_spike_test.go says what that
// means for a daemon's update). The hooks are in `s` and in the two initialised
// submodules' git directories; the credential.helper of each is empty.
func Submodules(t testing.TB, dir string) *Subs {
	t.Helper()
	dir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	g := newGit(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Subs{
		Super: filepath.Join(dir, "s"), Origin: filepath.Join(dir, "s-origin.git"), Lib: filepath.Join(dir, "s-lib.git"),
		Nested: filepath.Join(dir, "s-nested.git"), Extra: filepath.Join(dir, "s-extra.git"),
		Markers: filepath.Join(dir, ".s-hook-markers"), Oids: map[string]string{},
	}
	build := t.TempDir()
	allow := []string{"-c", "protocol.file.allow=always"}
	sub := func(d string, args ...string) string { return g.run(d, nil, append(slicesClone(allow), args...)...) }
	bare := func(work, to string) { g.run(build, nil, "clone", "-q", "--bare", work, to) }

	nested := filepath.Join(build, "nested")
	g.run(build, nil, "init", "-q", "-b", "main", nested)
	writeFile(t, nested, "n.txt", "1\n")
	s.Oids["nested1"] = g.save(nested, "nested 1")
	writeFile(t, nested, "n.txt", "2\n")
	s.Oids["nested2"] = g.save(nested, "nested 2")
	bare(nested, s.Nested)

	lib := filepath.Join(build, "lib")
	g.run(build, nil, "init", "-q", "-b", "main", lib)
	writeFile(t, lib, "lib.txt", "1\n")
	sub(lib, "submodule", "add", "-q", s.Nested, "nested")
	g.run(filepath.Join(lib, "nested"), nil, "checkout", "-q", s.Oids["nested1"])
	s.Oids["lib1"] = g.save(lib, "lib 1")
	g.run(filepath.Join(lib, "nested"), nil, "checkout", "-q", s.Oids["nested2"])
	writeFile(t, lib, "lib.txt", "2\n")
	s.Oids["lib2"] = g.save(lib, "lib 2")
	bare(lib, s.Lib)

	extra := filepath.Join(build, "extra")
	g.run(build, nil, "init", "-q", "-b", "main", extra)
	writeFile(t, extra, "extra.txt", "1\n")
	s.Oids["extra"] = g.save(extra, "extra 1")
	bare(extra, s.Extra)

	super := filepath.Join(build, "super")
	g.run(build, nil, "init", "-q", "-b", "main", super)
	writeFile(t, super, "README.md", "# submodule fixture\n")
	g.save(super, "Initial")
	sub(super, "submodule", "add", "-q", s.Lib, "lib")
	sub(super, "submodule", "add", "-q", s.Extra, "extra")
	s.Oids["super"] = g.save(super, "Add the submodules")
	bare(super, s.Origin)

	g.run(dir, nil, "clone", "-q", s.Origin, s.Super)
	sub(s.Super, "submodule", "update", "-q", "--init", "--recursive", "lib")
	gitDirs := []string{
		filepath.Join(s.Super, ".git"),
		filepath.Join(s.Super, ".git", "modules", "lib"),
		filepath.Join(s.Super, ".git", "modules", "lib", "modules", "nested"),
	}
	for _, gd := range gitDirs {
		g.run(s.Super, nil, "config", "--file", filepath.Join(gd, "config"), "credential.helper", "")
		g.installHooks(gd, s.Markers)
	}
	return s
}

func slicesClone(s []string) []string { return append([]string(nil), s...) }
