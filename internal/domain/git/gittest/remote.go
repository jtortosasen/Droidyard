package gittest

// The fixtures of plan m2n part 2 (step P1 and the level-5 runs K4 describes): a work tree
// with a remote and a colleague who pushes to it, and a superproject with submodules.
// All remotes are local paths: nothing here touches the network, and nothing but the
// repositories it creates. Each work tree has a `post-checkout` and a
// `reference-transaction` hook that append a line to a marker file, and an empty
// credential.helper (both macOS gits ship `osxkeychain` at system scope), so a test or a
// run can tell whether a git ran a hook and cannot reach the owner's keychain.

import (
	"bufio"
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
	// The remote is written straight into the bare repository by one `git fast-import`: the
	// same four commits a work tree and a push would make, in a fraction of the processes
	// (each git a test starts costs far more than its work on a host that scans every
	// process). main is Initial then Second; feature/x and y both fork from Second.
	g.run(dir, nil, "init", "-q", "--bare", "-b", "main", r.Origin)
	marks := filepath.Join(t.TempDir(), "marks")
	var second, featureX, y int
	g.fastImport(r.Origin, marks, func(w *bufio.Writer) {
		s := newStream(w)
		const who = "Fixture|fixture@example.com"
		initial := s.commit("refs/heads/main", 0, nil, who, "Initial\n",
			file{path: "README.md", content: []byte("# remote fixture\n")},
			file{path: "shared.txt", content: []byte("base\n")})
		second = s.commit("refs/heads/main", initial, nil, who, "Second\n", file{path: "a.txt", content: []byte("a\n")})
		featureX = s.commit("refs/heads/feature/x", second, nil, who, "Feature x\n",
			file{path: "x.txt", content: []byte("x\n")},
			file{path: "shared.txt", content: []byte("feature x\n")})
		y = s.commit("refs/heads/y", second, nil, who, "Branch y\n", file{path: "y.txt", content: []byte("y\n")})
	})
	m := readMarks(t, marks)
	r.Oids["main"], r.Oids["feature/x"], r.Oids["y"] = m[second], m[featureX], m[y]
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
	allow := []string{"-c", "protocol.file.allow=always"}
	sub := func(d string, args ...string) string { return g.run(d, nil, append(slicesClone(allow), args...)...) }

	// The four remotes are written straight into bare repositories, each by one `git
	// fast-import` (a superproject's gitlinks are `M 160000 <oid> <path>`, which need no
	// checkout of the submodule): the commits `submodule add` and a few saves would make, in a
	// fraction of the processes. The history is what the doc comment says: nested has two
	// commits; lib has two, lib1 recording nested1 and lib2 nested2; extra has one; the
	// superproject has Initial and then the commit that adds lib (at lib2) and extra.
	const who = "Fixture|fixture@example.com"
	marks := t.TempDir()
	bare := func(to string, write func(s *stream)) map[int]string {
		g.run(dir, nil, "init", "-q", "--bare", "-b", "main", to)
		mf := filepath.Join(marks, filepath.Base(to)+".marks")
		g.fastImport(to, mf, func(w *bufio.Writer) { write(newStream(w)) })
		return readMarks(t, mf)
	}
	modules := func(urls ...[2]string) []byte {
		var b strings.Builder
		for _, u := range urls {
			fmt.Fprintf(&b, "[submodule %q]\n\tpath = %s\n\turl = %s\n", u[0], u[0], u[1])
		}
		return []byte(b.String())
	}
	gitlink := func(path, oid string) file { return file{path: path, gitlink: oid} }

	var nested1, nested2 int
	m := bare(s.Nested, func(st *stream) {
		nested1 = st.commit("refs/heads/main", 0, nil, who, "nested 1\n", file{path: "n.txt", content: []byte("1\n")})
		nested2 = st.commit("refs/heads/main", nested1, nil, who, "nested 2\n", file{path: "n.txt", content: []byte("2\n")})
	})
	s.Oids["nested1"], s.Oids["nested2"] = m[nested1], m[nested2]

	var lib1, lib2 int
	m = bare(s.Lib, func(st *stream) {
		gm := file{path: ".gitmodules", content: modules([2]string{"nested", s.Nested})}
		lib1 = st.commit("refs/heads/main", 0, nil, who, "lib 1\n", file{path: "lib.txt", content: []byte("1\n")}, gm, gitlink("nested", s.Oids["nested1"]))
		lib2 = st.commit("refs/heads/main", lib1, nil, who, "lib 2\n", file{path: "lib.txt", content: []byte("2\n")}, gitlink("nested", s.Oids["nested2"]))
	})
	s.Oids["lib1"], s.Oids["lib2"] = m[lib1], m[lib2]

	var extra int
	m = bare(s.Extra, func(st *stream) {
		extra = st.commit("refs/heads/main", 0, nil, who, "extra 1\n", file{path: "extra.txt", content: []byte("1\n")})
	})
	s.Oids["extra"] = m[extra]

	var superTip int
	m = bare(s.Origin, func(st *stream) {
		initial := st.commit("refs/heads/main", 0, nil, who, "Initial\n", file{path: "README.md", content: []byte("# submodule fixture\n")})
		superTip = st.commit("refs/heads/main", initial, nil, who, "Add the submodules\n",
			file{path: ".gitmodules", content: modules([2]string{"lib", s.Lib}, [2]string{"extra", s.Extra})},
			gitlink("lib", s.Oids["lib2"]), gitlink("extra", s.Oids["extra"]))
	})
	s.Oids["super"] = m[superTip]

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
