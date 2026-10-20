package git

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/git/gittest"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// submodulesPage calls git.submodules as the UI would.
func (f *readFx) submodulesPage() (SubmodulesResult, *capability.Error) {
	f.t.Helper()
	d, ok := f.reg.Lookup("git.submodules")
	if !ok {
		f.t.Fatal("git.submodules is not registered")
	}
	res, err := d.Handler(context.Background(), &capability.Call{Workspace: "ws", Params: SubmodulesParams{}})
	if err != nil {
		return SubmodulesResult{}, capability.AsError(err)
	}
	return res.(SubmodulesResult), nil
}

func (f *readFx) mustSubmodules() SubmodulesResult {
	f.t.Helper()
	res, err := f.submodulesPage()
	if err != nil {
		f.t.Fatalf("git.submodules: %s (%s; cause %q)", err.Code, err.Message, err.Cause)
	}
	return res
}

func (f *readFx) untrusted() {
	f.trusted = func(string) (bool, *capability.Error) { return false, nil }
}

// gitDirs records the directory of every git the walk starts.
func (f *readFx) gitDirs() *[]string {
	var dirs []string
	f.onStart = func(cmd *exec.Cmd) { dirs = append(dirs, cmd.Dir) }
	return &dirs
}

// rows reduces a result to "path state recorded checkedOut" lines, in order.
func rows(res SubmodulesResult) []string {
	var out []string
	for _, s := range res.Submodules {
		out = append(out, fmt.Sprintf("%s %s %s %s", s.Path, s.State, short(s.Recorded), short(s.CheckedOut)))
	}
	return out
}

func short(oid string) string {
	if len(oid) > 7 {
		return oid[:7]
	}
	return oid
}

// gitlinkAt records a gitlink to oid at path in dir's index, with no directory behind it.
func (e *env) gitlinkAt(dir, oid, p string) {
	e.t.Helper()
	e.g(dir, "update-index", "--add", "--cacheinfo", "160000,"+oid+","+p)
}

// gitmodules writes dir/.gitmodules naming the paths, one section each.
func (e *env) gitmodules(dir string, paths ...string) {
	e.t.Helper()
	var b strings.Builder
	for i, p := range paths {
		fmt.Fprintf(&b, "[submodule \"m%d\"]\n\tpath = %s\n\turl = ./x\n", i, p)
	}
	e.write(dir, ".gitmodules", b.String())
}

// Decision (ADR-0071 §1, plan m2n P4): a submodule is uninitialised, clean, outdated
// (clean at another commit than the repository records) or dirty (its working tree
// differs from its HEAD, whatever its commit: dirty wins), each in its own directory and
// each nested one by the same walk, listed depth-first with workspace-relative paths. The
// states agree with git's own `submodule status` (uninitialised "-", outdated "+"), which
// never reports dirty; that is `git status` in the submodule.
func TestSubmodulesStates(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
	f.at(s.Super)
	lib := filepath.Join(s.Super, "lib")
	nested := filepath.Join(lib, "nested")

	// gitMarks is git's own answer: the first character of each line of `submodule
	// status` (" " at the recorded commit, "-" uninitialised, "+" another commit), by path.
	gitMarks := func() map[string]string {
		marks := map[string]string{}
		for _, l := range strings.Split(f.g(s.Super, "submodule", "status", "--recursive"), "\n") {
			if l != "" {
				marks[strings.Fields(l[1:])[1]] = l[:1]
			}
		}
		return marks
	}

	res := f.mustSubmodules()
	want := []string{
		"extra uninitialised " + short(s.Oids["extra"]) + " ",
		"lib clean " + short(s.Oids["lib2"]) + " " + short(s.Oids["lib2"]),
		"lib/nested clean " + short(s.Oids["nested2"]) + " " + short(s.Oids["nested2"]),
	}
	if !equal(rows(res), want) || res.Truncated {
		t.Errorf("clean:\n got %v truncated %v\nwant %v", rows(res), res.Truncated, want)
	}
	if got := gitMarks(); got["extra"] != "-" || got["lib"] != " " || got["lib/nested"] != " " {
		t.Errorf("git's submodule status = %q; want extra -, lib and lib/nested clean", got)
	}

	// Outdated: lib/nested checked out at the commit before the recorded one.
	f.g(nested, "checkout", "-q", s.Oids["nested1"])
	res = f.mustSubmodules()
	want[2] = "lib/nested outdated " + short(s.Oids["nested2"]) + " " + short(s.Oids["nested1"])
	if !equal(rows(res), want) {
		t.Errorf("outdated:\n got %v\nwant %v", rows(res), want)
	}
	if got := gitMarks(); got["lib/nested"] != "+" || got["lib"] != " " {
		t.Errorf("git's submodule status = %q; want lib/nested marked +", got)
	}

	// Dirty: an untracked file in lib. lib's own status ignores its submodule, so lib is
	// dirty by the file alone, and lib/nested stays what it was.
	f.write(lib, "new.txt", "x\n")
	res = f.mustSubmodules()
	want[1] = "lib dirty " + short(s.Oids["lib2"]) + " " + short(s.Oids["lib2"])
	if !equal(rows(res), want) {
		t.Errorf("dirty:\n got %v\nwant %v", rows(res), want)
	}
	if got := f.g(lib, "status", "--porcelain", "--ignore-submodules=all"); got == "" {
		t.Errorf("git status in lib is empty; the fixture is not dirty")
	}

	// Dirty wins over outdated.
	f.write(nested, "n.txt", "edited\n")
	res = f.mustSubmodules()
	want[2] = "lib/nested dirty " + short(s.Oids["nested2"]) + " " + short(s.Oids["nested1"])
	if !equal(rows(res), want) {
		t.Errorf("dirty and outdated:\n got %v\nwant %v", rows(res), want)
	}

	// The same on an untrusted root: the fence changes what is run, not what is said.
	f.untrusted()
	if got := f.mustSubmodules(); !equal(rows(got), want) {
		t.Errorf("untrusted:\n got %v\nwant %v", rows(got), want)
	}
}

// Decision (ADR-0071 §1): a repository with no submodule, and what is not one: a path
// `.gitmodules` names that the index records as a file, and a gitlink `.gitmodules` does
// not name (git's own `submodule` commands refuse it too), are not listed. The list is
// [], never null, and the call starts no git beyond the top's.
func TestSubmodulesNone(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	dir := f.root
	f.write(dir, "a.txt", "a\n")
	f.commit(dir)
	res := f.mustSubmodules()
	if res.Submodules == nil || len(res.Submodules) != 0 || res.Truncated {
		t.Errorf("no .gitmodules: %+v", res)
	}
	if b, _ := json.Marshal(res); string(b) != `{"submodules":[],"truncated":false}` {
		t.Errorf("as JSON = %s", b)
	}

	oid := strings.TrimSpace(f.g(dir, "rev-parse", "HEAD"))
	f.gitmodules(dir, "a.txt", "ghost") // a file, and a path nothing records
	f.gitlinkAt(dir, oid, "unnamed")    // a gitlink .gitmodules does not name
	if res := f.mustSubmodules(); len(res.Submodules) != 0 {
		t.Errorf("listed what is no submodule: %v", rows(res))
	}
}

// Decision (ADR-0071 §7; plan m2n P4): no git runs in a directory outside the workspace
// (the containment is of the directory git runs in, not of its git directory). A gitlink
// whose directory is a symlink out of the root, or behind one, or whose `.git` is a symlink,
// is left out and no git starts in the place it points at; so is one whose `.gitmodules`
// entry names a directory above the root of a subdirectory workspace (the repository's other
// folders), and a `.gitmodules` that is itself a symlink is not read. The outside places hold
// real repositories, so a walk that went there would start a git there.
func TestSubmodulesContainment(t *testing.T) {
	t.Parallel()
	t.Run("a symlink out of the root", func(t *testing.T) {
		t.Parallel()
		f := newReadFx(t)
		s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
		f.at(s.Super)
		outside := f.platform.Canonical(t.TempDir())
		f.g(outside, "init", "-q", "-b", "main", "lib")
		f.write(filepath.Join(outside, "lib"), "x", "x\n")
		f.commit(filepath.Join(outside, "lib"))
		if err := os.RemoveAll(filepath.Join(s.Super, "lib")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "lib"), filepath.Join(s.Super, "lib")); err != nil {
			t.Fatal(err)
		}
		dirs := f.gitDirs()
		res := f.mustSubmodules()
		if want := []string{"extra uninitialised " + short(s.Oids["extra"]) + " "}; !equal(rows(res), want) {
			t.Errorf("rows = %v; want only extra (lib is behind a symlink, and so is its nested one)", rows(res))
		}
		for _, d := range *dirs {
			if strings.HasPrefix(d, outside) || !strings.HasPrefix(d, f.root) {
				t.Errorf("a git started in %q, outside the workspace %q", d, f.root)
			}
		}
	})

	t.Run("a symlink on the way to the directory", func(t *testing.T) {
		t.Parallel()
		// mods is a symlink out of the root and mods/m a real repository behind it: the
		// directory itself is not a symlink, so only resolving the whole path finds out.
		f := newReadFx(t)
		f.write(f.root, "a.txt", "a\n")
		f.commit(f.root)
		outside := f.platform.Canonical(t.TempDir())
		f.g(outside, "init", "-q", "-b", "main", "m")
		f.write(filepath.Join(outside, "m"), "x", "x\n")
		f.commit(filepath.Join(outside, "m"))
		if err := os.Symlink(outside, filepath.Join(f.root, "mods")); err != nil {
			t.Fatal(err)
		}
		f.gitlinkAt(f.root, strings.TrimSpace(f.g(filepath.Join(outside, "m"), "rev-parse", "HEAD")), "mods/m")
		f.gitmodules(f.root, "mods/m")
		dirs := f.gitDirs()
		if res := f.mustSubmodules(); len(res.Submodules) != 0 {
			t.Errorf("listed %v behind a symlink", rows(res))
		}
		for _, d := range *dirs {
			if strings.HasPrefix(d, outside) {
				t.Errorf("a git started in %q, outside the workspace", d)
			}
		}
	})

	t.Run("a .git that is a symlink", func(t *testing.T) {
		t.Parallel()
		// The submodule's directory is the workspace's, but git never makes a symlink of a
		// submodule's .git and this one points at a repository outside: no git runs in it,
		// and it is left out.
		f := newReadFx(t)
		f.write(f.root, "a.txt", "a\n")
		f.commit(f.root)
		outside := f.platform.Canonical(t.TempDir())
		f.g(outside, "init", "-q", "-b", "main", "r")
		f.write(filepath.Join(outside, "r"), "x", "x\n")
		f.commit(filepath.Join(outside, "r"))
		if err := os.Mkdir(filepath.Join(f.root, "m"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "r", ".git"), filepath.Join(f.root, "m", ".git")); err != nil {
			t.Fatal(err)
		}
		f.gitlinkAt(f.root, strings.TrimSpace(f.g(filepath.Join(outside, "r"), "rev-parse", "HEAD")), "m")
		f.gitmodules(f.root, "m")
		dirs := f.gitDirs()
		if res := f.mustSubmodules(); len(res.Submodules) != 0 {
			t.Errorf("listed %v with a symlinked .git", rows(res))
		}
		for _, d := range *dirs {
			if d != f.root {
				t.Errorf("a git started in %q; only the workspace's own are expected", d)
			}
		}
	})

	t.Run("a symlink inside a submodule's path", func(t *testing.T) {
		t.Parallel()
		// lib/nested is reached through lib: with lib a real directory and nested a
		// symlink out, only nested is left out.
		f := newReadFx(t)
		s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
		f.at(s.Super)
		outside := f.platform.Canonical(t.TempDir())
		f.g(outside, "init", "-q", "-b", "main", "nested")
		f.write(filepath.Join(outside, "nested"), "x", "x\n")
		f.commit(filepath.Join(outside, "nested"))
		nested := filepath.Join(s.Super, "lib", "nested")
		if err := os.RemoveAll(nested); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "nested"), nested); err != nil {
			t.Fatal(err)
		}
		dirs := f.gitDirs()
		res := f.mustSubmodules()
		if got := rows(res); len(got) != 2 || !strings.HasPrefix(got[0], "extra ") || !strings.HasPrefix(got[1], "lib ") {
			t.Errorf("rows = %v; want extra and lib", got)
		}
		for _, d := range *dirs {
			if strings.HasPrefix(d, outside) {
				t.Errorf("a git started in %q, outside the workspace", d)
			}
		}
	})

	t.Run("outside a subdirectory workspace", func(t *testing.T) {
		t.Parallel()
		f := newReadFx(t)
		top := f.platform.Canonical(f.root)
		// app/mod is a submodule of the workspace; other/mod2 is the repository's, beside
		// the workspace; app/link is a gitlink whose directory is a symlink to other/mod2.
		for _, d := range []string{"app/mod", "other/mod2"} {
			f.g(top, "init", "-q", "-b", "main", d)
			f.write(filepath.Join(top, d), "x", d+"\n")
			f.commit(filepath.Join(top, d))
		}
		f.write(top, "app/a.txt", "a\n")
		f.commit(top)
		if err := os.Symlink(filepath.Join(top, "other", "mod2"), filepath.Join(top, "app", "link")); err != nil {
			t.Fatal(err)
		}
		mod := strings.TrimSpace(f.g(filepath.Join(top, "app/mod"), "rev-parse", "HEAD"))
		mod2 := strings.TrimSpace(f.g(filepath.Join(top, "other/mod2"), "rev-parse", "HEAD"))
		f.gitlinkAt(top, mod, "app/mod")
		f.gitlinkAt(top, mod2, "other/mod2")
		f.gitlinkAt(top, mod2, "app/link")
		f.gitmodules(top, "app/mod", "other/mod2", "app/link")
		f.at(filepath.Join(top, "app"))
		dirs := f.gitDirs()
		res := f.mustSubmodules()
		if want := []string{"mod clean " + short(mod) + " " + short(mod)}; !equal(rows(res), want) {
			t.Errorf("rows = %v; want %v (relative to the workspace)", rows(res), want)
		}
		for _, d := range *dirs {
			if d != f.root && !strings.HasPrefix(d, f.root+string(filepath.Separator)) {
				t.Errorf("a git started in %q, outside the workspace %q", d, f.root)
			}
		}
	})

	t.Run("a symlinked .gitmodules", func(t *testing.T) {
		t.Parallel()
		f := newReadFx(t)
		f.write(f.root, "a.txt", "a\n")
		f.commit(f.root)
		oid := strings.TrimSpace(f.g(f.root, "rev-parse", "HEAD"))
		f.gitlinkAt(f.root, oid, "m")
		outside := t.TempDir()
		f.gitmodules(outside, "m")
		if err := os.Symlink(filepath.Join(outside, ".gitmodules"), filepath.Join(f.root, ".gitmodules")); err != nil {
			t.Fatal(err)
		}
		if res := f.mustSubmodules(); len(res.Submodules) != 0 {
			t.Errorf("read a symlinked .gitmodules: %v", rows(res))
		}
	})
}

// Decision (ADR-0071 §1): the list stops at 1000 whole entries and says so; 1000 exactly
// is not truncated (the 1001st is the proof there is more). The gitlinks have no
// directory behind them, so each is uninitialised and no git runs for it.
func TestSubmodulesTruncated(t *testing.T) {
	t.Parallel()
	build := func(t *testing.T, n int) *readFx {
		f := newReadFx(t)
		f.write(f.root, "a.txt", "a\n")
		f.commit(f.root)
		oid := strings.TrimSpace(f.g(f.root, "rev-parse", "HEAD"))
		var idx strings.Builder
		var paths []string
		for i := 0; i < n; i++ {
			p := fmt.Sprintf("sub/m%04d", i)
			paths = append(paths, p)
			fmt.Fprintf(&idx, "160000 %s\t%s\n", oid, p)
		}
		f.gStdin(f.root, idx.String(), "update-index", "--add", "--index-info")
		f.gitmodules(f.root, paths...)
		return f
	}
	f := build(t, 1001)
	res := f.mustSubmodules()
	if len(res.Submodules) != 1000 || !res.Truncated {
		t.Fatalf("1001 gitlinks: %d listed, truncated %v; want 1000 and truncated", len(res.Submodules), res.Truncated)
	}
	if res.Submodules[0].Path != "sub/m0000" || res.Submodules[999].Path != "sub/m0999" {
		t.Errorf("the list is %s .. %s; want the first 1000 by path", res.Submodules[0].Path, res.Submodules[999].Path)
	}
	f = build(t, 1000)
	if res := f.mustSubmodules(); len(res.Submodules) != 1000 || res.Truncated {
		t.Errorf("1000 gitlinks: %d listed, truncated %v; want all and not truncated", len(res.Submodules), res.Truncated)
	}
}

// Decision (ADR-0071 §1): past git's 4 MiB output cap the list stops at whole entries
// with truncated: the .gitmodules read here is cut in the middle of a record, which is
// dropped, and the gitlinks that fell before the cut are listed.
func TestSubmodulesOutputCap(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	f.write(f.root, "a.txt", "a\n")
	f.commit(f.root)
	oid := strings.TrimSpace(f.g(f.root, "rev-parse", "HEAD"))
	long := strings.Repeat("x", 120)
	var paths []string
	for i := 0; i < 40000; i++ {
		paths = append(paths, fmt.Sprintf("d%06d/%s", i, long))
	}
	f.gitmodules(f.root, paths...)
	f.gitlinkAt(f.root, oid, paths[0])
	f.gitlinkAt(f.root, oid, paths[1])
	res := f.mustSubmodules()
	if !res.Truncated || len(res.Submodules) != 2 || res.Submodules[1].Path != paths[1] {
		t.Errorf("%d listed, truncated %v; want the 2 whole entries and truncated", len(res.Submodules), res.Truncated)
	}
}

// Decision (plan m2n P4): paths from .gitmodules and from ls-files are matched literally
// (--literal-pathspecs). A path that begins with pathspec magic (":(top)zzz") would be a
// pattern otherwise, naming nothing, and the gitlink would be lost; a glob character
// ("a*c") is only a character, and "-x" is a path, not an option. A gitlink the
// .gitmodules does not name ("abc", which "a*c" would match as a glob) is not listed.
func TestSubmodulesOddPaths(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	f.write(f.root, "a.txt", "a\n")
	f.commit(f.root)
	oid := strings.TrimSpace(f.g(f.root, "rev-parse", "HEAD"))
	odd := []string{":(top)zzz", "a*c", "-x"}
	for _, p := range odd {
		f.gitlinkAt(f.root, oid, p)
		if err := os.MkdirAll(filepath.Join(f.root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.gitlinkAt(f.root, oid, "abc")
	f.gitmodules(f.root, odd...)
	res := f.mustSubmodules()
	var got []string
	for _, s := range res.Submodules {
		got = append(got, s.Path)
		if s.State != StateUninitialised || s.Recorded != oid {
			t.Errorf("%q: %+v", s.Path, s)
		}
	}
	if want := []string{"-x", ":(top)zzz", "a*c"}; !equal(got, want) {
		t.Errorf("listed %q; want %q", got, want)
	}
}

// Decision: a path that is not a plain relative one is never a candidate: absolute,
// parent-directory, unclean and `.git` paths are dropped before any git sees them.
func TestSubmodulesPlainPaths(t *testing.T) {
	t.Parallel()
	for p, want := range map[string]bool{
		"lib": true, "a/b": true, "-x": true, ":(top)z": true, "a b": true,
		"": false, ".": false, "..": false, "../x": false, "/abs": false, "a/../b": false,
		"a//b": false, "a/./b": false, "lib/": false, ".git": false, "a/.GIT/b": false, ".git/modules/x": false,
	} {
		if got := plainRelative(p); got != want {
			t.Errorf("plainRelative(%q) = %v; want %v", p, got, want)
		}
	}
}

// Decision (ADR-0020 §Retention): a result returned again after delivery holds no list,
// as [] not null, and keeps the flag.
func TestSubmodulesSlim(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	d, _ := f.reg.Lookup("git.submodules")
	full := SubmodulesResult{Submodules: []Submodule{{Path: "lib", Recorded: "a", CheckedOut: "b", State: StateOutdated}}, Truncated: true}
	got, ok := d.Slim(full).(SubmodulesResult)
	if !ok {
		t.Fatalf("Slim returned %T", d.Slim(full))
	}
	if b, _ := json.Marshal(got); string(b) != `{"submodules":[],"truncated":true}` {
		t.Errorf("slim as JSON = %s", b)
	}
	if len(full.Submodules) != 1 {
		t.Errorf("Slim changed the result it was given")
	}
}

// Decision (ADR-0066 §3, plan m2n P4): on an untrusted root each submodule's git runs in
// its own directory, where that repository's own filter drivers are read and switched
// off. The attacks are in the submodules' own configuration (their git directories under
// .git/modules, which is where a real submodule keeps it): a clean filter on a tracked
// file whose mtime moved (status must hash it), core.fsmonitor, and a signature program
// (nothing the walk runs verifies a signature, so that one is a belt). A file in each is
// dirty. No marker may appear. The control is plain git, asked the way that runs them,
// and a walk that reads the drivers in the superproject only would run them here: the
// superproject has no such driver.
//
// The claim is the fence, not git's speed: the fixture's limit is a hang guard, 60 s
// scaled, as TestHistoryFence's is (a loaded machine took a submodule's status past the
// product's 10 s).
func TestSubmodulesFence(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	f.readTimeout = testtime.Bound(60 * time.Second)
	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
	f.at(s.Super)
	lib := filepath.Join(s.Super, "lib")
	nested := filepath.Join(lib, "nested")
	marks := t.TempDir()
	mark := func(name string) string { return filepath.Join(marks, name) }
	for _, sm := range []struct{ dir, gitDir, file, name string }{
		{lib, filepath.Join(s.Super, ".git", "modules", "lib"), "lib.txt", "lib"},
		{nested, filepath.Join(s.Super, ".git", "modules", "lib", "modules", "nested"), "n.txt", "nested"},
	} {
		clean := f.script("clean-"+sm.name+".sh", "touch '"+mark("filter-"+sm.name)+"'\ncat")
		fsm := f.script("fsmonitor-"+sm.name+".sh", "touch '"+mark("fsmonitor-"+sm.name)+"'")
		gpg := f.script("gpg-"+sm.name+".sh", "touch '"+mark("gpg-"+sm.name)+"'")
		f.append(filepath.Join(sm.gitDir, "config"),
			"[filter \"evil\"]\n\tclean = "+clean+"\n\trequired = true\n"+
				"[core]\n\tfsmonitor = "+fsm+"\n"+
				"[log]\n\tshowSignature = true\n[gpg]\n\tprogram = "+gpg+"\n")
		f.write(sm.gitDir, "info/attributes", sm.file+" filter=evil\n")
		f.touch(sm.dir, sm.file)
		f.write(sm.dir, "dirty.txt", "dirty\n")
	}
	clear := func() {
		ms, _ := filepath.Glob(mark("*"))
		for _, m := range ms {
			os.Remove(m)
		}
	}
	marked := func() []string {
		ms, _ := filepath.Glob(mark("*"))
		for i := range ms {
			ms[i] = filepath.Base(ms[i])
		}
		return ms
	}

	// The control: plain git runs the attacks, so "no marker" below can fail.
	clear()
	f.gErr(lib, "status", "--porcelain")
	f.gErr(nested, "status", "--porcelain")
	if got := marked(); len(got) < 4 {
		t.Fatalf("control: plain git status ran %v; want the filter and the fsmonitor of both submodules", got)
	}

	// Untrusted: nothing runs, and the rows are still true.
	clear()
	f.touch(lib, "lib.txt")
	f.touch(nested, "n.txt")
	f.untrusted()
	res := f.mustSubmodules()
	if got := marked(); len(got) != 0 {
		t.Errorf("untrusted: a submodule's own configuration ran something: %v", got)
	}
	want := []string{
		"extra uninitialised " + short(s.Oids["extra"]) + " ",
		"lib dirty " + short(s.Oids["lib2"]) + " " + short(s.Oids["lib2"]),
		"lib/nested dirty " + short(s.Oids["nested2"]) + " " + short(s.Oids["nested2"]),
	}
	if !equal(rows(res), want) {
		t.Errorf("untrusted rows:\n got %v\nwant %v", rows(res), want)
	}

	// Trusted: the user's own git, filters included (control that the walk itself does
	// not switch them off for everyone).
	f.trusted = func(string) (bool, *capability.Error) { return true, nil }
	clear()
	f.touch(lib, "lib.txt")
	f.mustSubmodules()
	if _, err := os.Stat(mark("filter-lib")); err != nil {
		t.Errorf("trusted: the submodule's own filter did not run: the fence is on for everyone, or the test cannot fail")
	}
}

// append appends text to the file p.
func (e *env) append(p, text string) {
	e.t.Helper()
	fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	defer fh.Close()
	if _, err := fh.WriteString(text); err != nil {
		e.t.Fatal(err)
	}
}

// Recorded, not asserted tightly (plan m2n P4, ADR-0071 §7 Unverified row): git.submodules
// on 20 initialised submodules, untrusted, so every git is preceded by the driver read.
// The walk is bounded by the submodules; the time and the number of gits are logged, and
// the test fails only on a wrong answer or on minutes.
func TestSubmodulesTwenty(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	f.write(f.root, "a.txt", "a\n")
	f.commit(f.root)
	var paths []string
	for i := 0; i < 20; i++ {
		p := fmt.Sprintf("mods/m%02d", i)
		paths = append(paths, p)
		sub := filepath.Join(f.root, p)
		f.g(f.root, "init", "-q", "-b", "main", p)
		f.write(sub, "x.txt", p+"\n")
		f.commit(sub)
		f.gitlinkAt(f.root, strings.TrimSpace(f.g(sub, "rev-parse", "HEAD")), p)
	}
	f.gitmodules(f.root, paths...)
	f.untrusted()
	var argvs [][]string
	f.onStart = func(cmd *exec.Cmd) { argvs = append(argvs, cmd.Args) }
	start := time.Now()
	res := f.mustSubmodules()
	elapsed := time.Since(start)
	if len(res.Submodules) != 20 {
		t.Fatalf("%d listed, want 20", len(res.Submodules))
	}
	for _, s := range res.Submodules {
		if s.State != StateClean {
			t.Errorf("%s is %s", s.Path, s.State)
		}
	}
	if elapsed > 2*time.Minute {
		t.Errorf("20 submodules took %s", elapsed)
	}
	t.Logf("git.submodules, 20 initialised submodules, untrusted: %s, %d gits (%.1f per submodule)", elapsed.Round(time.Millisecond), len(argvs), float64(len(argvs))/20)
}

// Decision (plan m2n P4, verifier round): one submodule that fails fails the whole call, but
// the cause names it. A .gitmodules of its own with a bad line is GIT_FAILED whose cause
// names the submodule (and a nested one by its full path); a filter key in its own config
// that cannot be written as an override is WORKSPACE_UNTRUSTED whose message names the
// submodule and does not say the root's configuration is at fault.
func TestSubmodulesErrorNamesTheSubmodule(t *testing.T) {
	t.Parallel()
	t.Run("a malformed .gitmodules of a submodule", func(t *testing.T) {
		t.Parallel()
		f := newReadFx(t)
		s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
		f.at(s.Super)
		f.write(filepath.Join(s.Super, "lib"), ".gitmodules", "[submodule \"x\"]\n\tpath = x\nthis is not a config line\n")
		_, err := f.submodulesPage()
		wantCode(t, err, CodeFailed)
		if !strings.Contains(err.Cause, "submodule lib:") || !strings.Contains(err.Message, `"lib"`) {
			t.Errorf("message %q, cause %q; want both to name lib", err.Message, err.Cause)
		}
	})
	t.Run("a malformed .gitmodules of a nested submodule", func(t *testing.T) {
		t.Parallel()
		f := newReadFx(t)
		s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
		f.at(s.Super)
		f.write(filepath.Join(s.Super, "lib", "nested"), ".gitmodules", "not a config line\n")
		_, err := f.submodulesPage()
		wantCode(t, err, CodeFailed)
		if !strings.Contains(err.Cause, "submodule lib/nested:") || strings.Contains(err.Cause, "submodule lib:") {
			t.Errorf("cause %q; want it to name lib/nested only", err.Cause)
		}
	})
	t.Run("a filter key of a submodule that cannot be overridden, untrusted", func(t *testing.T) {
		t.Parallel()
		f := newReadFx(t)
		s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
		f.at(s.Super)
		f.append(filepath.Join(s.Super, ".git", "modules", "lib", "config"), "[filter \"\"]\n\tclean = x\n")
		f.untrusted()
		_, err := f.submodulesPage()
		wantCode(t, err, capability.ErrWorkspaceUntrusted)
		if !strings.Contains(err.Message, `submodule "lib"`) || !strings.Contains(err.Cause, "submodule lib:") || err.NextStep == "" {
			t.Errorf("message %q, cause %q, nextStep %q; want the submodule named and the trust nextStep", err.Message, err.Cause, err.NextStep)
		}
		// The same key in the superproject's own config keeps the root's message.
		g := newReadFx(t)
		s2 := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
		g.at(s2.Super)
		g.append(filepath.Join(s2.Super, ".git", "config"), "[filter \"\"]\n\tclean = x\n")
		g.untrusted()
		_, err = g.submodulesPage()
		wantCode(t, err, capability.ErrWorkspaceUntrusted)
		if strings.Contains(err.Message, "in its submodule") {
			t.Errorf("the root's own refusal names a submodule: %q", err.Message)
		}
	})
}

// Decision (plan m2n P4, verifier round): a stale .gitmodules entry that names a huge
// ordinary directory fills ls-files' 4 MiB cap in the chunk it shares with the real
// submodules, and the gitlinks after it would be dropped. A chunk that passes the cap is asked
// again one candidate at a time, so both gitlinks are listed and the list is not truncated
// (a candidate alone cut can only lose entries below it, never a gitlink).
func TestSubmodulesHugeCandidateDirectory(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	f.write(f.root, "a.txt", "a\n")
	f.commit(f.root)
	oid := strings.TrimSpace(f.g(f.root, "rev-parse", "HEAD"))
	blob := strings.TrimSpace(f.g(f.root, "rev-parse", "HEAD:a.txt"))
	long := strings.Repeat("x", 120)
	var idx strings.Builder
	for i := 0; i < 30000; i++ {
		fmt.Fprintf(&idx, "100644 %s\t%s\n", blob, fmt.Sprintf("big/f%05d-%s", i, long))
	}
	f.gStdin(f.root, idx.String(), "update-index", "--add", "--index-info")
	f.gitlinkAt(f.root, oid, "sub1")
	f.gitlinkAt(f.root, oid, "sub2")
	f.gitmodules(f.root, "big", "sub1", "sub2")
	if n := len(f.g(f.root, "ls-files", "--stage", "-z", "--", "big")); n <= stdoutLimit {
		t.Fatalf("the fixture's directory lists %d bytes; it must pass the cap (%d) for the test to mean anything", n, stdoutLimit)
	}
	res := f.mustSubmodules()
	var got []string
	for _, s := range res.Submodules {
		got = append(got, s.Path)
	}
	if !equal(got, []string{"sub1", "sub2"}) || res.Truncated {
		t.Errorf("listed %v truncated %v; want sub1 and sub2, not truncated", got, res.Truncated)
	}
}
