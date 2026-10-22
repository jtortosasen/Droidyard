package git

// m3b step 2 (ADR-0125 §2-§4): the watch's second status, in the submodules that may hold
// changes. These tests run real git on synthetic repositories and call Options.statusOf, the
// watch's tick, so the project's status and the pass are what is read. The deadline of the
// pass is a hang guard (testtime.Bound) in every test but the bounds one, as
// TestSubmodulesFence's readTimeout is: the claim is the answer, not git's speed.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/git/gittest"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// newPassFx is a git fixture whose reads and submodule pass have hang-guard limits.
func newPassFx(t *testing.T) *readFx {
	t.Helper()
	f := newReadFx(t)
	f.readTimeout = testtime.Bound(60 * time.Second)
	f.passTimeout = testtime.Bound(60 * time.Second)
	f.register()
	return f
}

// tick is the watch's tick over the fixture's root: git.watch's status, pass included.
func (f *readFx) tick() Status {
	f.t.Helper()
	st, err := f.opts.statusOf(context.Background(), f.root)
	if err != nil {
		f.t.Fatalf("a tick (trusted %v): %v", f.trustedNow(), err)
	}
	return st
}

func (f *readFx) trustedNow() bool {
	ok, _ := f.trusted(f.root)
	return ok
}

// asTrust runs the check once with the root trusted and once not.
func (f *readFx) asTrust(trusted bool) {
	f.trusted = func(string) (bool, *capability.Error) { return trusted, nil }
}

// initSub initialises the submodule at name of super (a clone from its local remote).
func (e *env) initSub(super, name string) {
	e.t.Helper()
	e.g(super, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--init", name)
}

// addSub makes a repository with one commit and adds it to dir as the submodule at name,
// staged and initialised.
func (e *env) addSub(dir, name string) {
	e.t.Helper()
	src := e.repo()
	e.write(src, "f.txt", name+"\n")
	e.commit(src)
	e.g(dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", src, name)
}

// gitRecord is plain git's record for path in dir's status: its XY and <sub> field, "" when
// there is none.
func (e *env) gitRecord(dir, ignore, path string) (xy, field string) {
	e.t.Helper()
	out := e.g(dir, "status", "--porcelain=v2", "-z", "--ignore-submodules="+ignore, "--untracked-files=all")
	for _, rec := range strings.Split(out, "\x00") {
		f := strings.Fields(rec)
		if len(f) > 2 && (f[0] == "1" || f[0] == "u") && f[len(f)-1] == path {
			return f[1], f[2]
		}
	}
	return "", ""
}

// gitBits is the commits, modified and untracked bits of plain git's record, with commits as
// the watch means it (against HEAD: git's C, or a staged new commit, X not a dot).
func (e *env) gitBits(dir, ignore, path string) (b SubmoduleChange, present bool) {
	e.t.Helper()
	xy, field := e.gitRecord(dir, ignore, path)
	if field == "" {
		return SubmoduleChange{}, false
	}
	return SubmoduleChange{Commits: field[1] == 'C' || xy[0] != '.', Modified: field[2] == 'M', Untracked: field[3] == 'U'}, true
}

// filesOf are the entries of the status whose root is root and that are no gitlink, as
// "path mark" lines.
func filesOf(st Status, root string) []string {
	var out []string
	for _, e := range st.Entries {
		if e.Root == root && e.Submodule == nil {
			out = append(out, e.Path+" "+string(e.Mark))
		}
	}
	return out
}

// plainFiles are what `git -C dir status` says of dir's own files, as "<prefix>path mark".
func (e *env) plainFiles(dir, prefix string) []string {
	e.t.Helper()
	var out []string
	recs := strings.Split(e.g(dir, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=all"), "\x00")
	for _, rec := range recs {
		if len(rec) < 4 {
			continue
		}
		mark := "modified"
		if rec[:2] == "??" {
			mark = "untracked"
		}
		out = append(out, prefix+rec[3:]+" "+mark)
	}
	return out
}

// under: the directory is dir or below it.
func under(d, dir string) bool {
	return d == dir || strings.HasPrefix(d, dir+string(filepath.Separator))
}

// Decision (ADR-0125 §2, plan m3b Q3): on a trusted root the pass reads only the gitlinks git
// flagged M or U. With lib dirty and extra initialised and clean, no git of the daemon starts
// in extra; on an untrusted root, where git flags nothing, both are read.
func TestSubmodulePassOnlyFlaggedWhenTrusted(t *testing.T) {
	t.Parallel()
	f := newPassFx(t)
	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
	f.initSub(s.Super, "extra")
	f.at(s.Super)
	lib, extra := filepath.Join(f.root, "lib"), filepath.Join(f.root, "extra")
	f.write(lib, "new.txt", "new\n")

	dirs := f.gitDirs()
	st := f.tick()
	if got := filesOf(st, "lib"); !equal(got, []string{"lib/new.txt untracked"}) {
		t.Errorf("trusted: lib's files %v; want lib/new.txt", got)
	}
	var inLib, inExtra bool
	for _, d := range *dirs {
		inLib = inLib || under(d, lib)
		inExtra = inExtra || under(d, extra)
	}
	if !inLib || inExtra {
		t.Errorf("trusted: gits in lib %v, in extra %v; want lib only (extra is clean, git flagged only lib)", inLib, inExtra)
	}

	f.untrusted()
	dirs = f.gitDirs()
	st = f.tick()
	inExtra = false
	for _, d := range *dirs {
		inExtra = inExtra || under(d, extra)
	}
	if !inExtra {
		t.Errorf("untrusted: no git started in extra; git flags nothing there, so every initialised submodule is read")
	}
	if got := filesOf(st, "lib"); !equal(got, []string{"lib/new.txt untracked"}) {
		t.Errorf("untrusted: lib's files %v; want lib/new.txt", got)
	}
	if e, ok := entryOf(st, "extra"); ok {
		t.Errorf("untrusted: a clean extra has an entry %+v", e)
	}
}

// Decision (ADR-0125 §3, plan m3b Q2/Q3): a gitlink's three bits are git's own S<c><m><u>
// field, and the files inside it are `git -C <submodule> status`'s, prefixed and with the
// submodule as root; on an untrusted root, where git flags nothing, the daemon computes the
// bits by git's rule (is_submodule_modified): an untracked file or a nested gitlink's U is
// untracked; any other change, a nested record other than exactly S..U included, is modified.
// Each nested case sits under a parent with no change of its own, so the parent's bits come
// from the child alone: lib (clean) holds lib/nested with an untracked file only (lib must be
// exactly U); extra has a new commit and a nested child with a new commit and an untracked
// file (SC.U; extra must be C, M and U); m1 has a modified and an untracked file of its own;
// m2 a new commit only; m3 nothing.
func TestSubmoduleContentMatchesGit(t *testing.T) {
	t.Parallel()
	for _, trusted := range []bool{true, false} {
		t.Run(fmt.Sprintf("trusted %v", trusted), func(t *testing.T) {
			t.Parallel()
			f := newPassFx(t)
			s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
			f.initSub(s.Super, "extra")
			f.addSub(filepath.Join(s.Super, "extra"), "child")
			f.g(filepath.Join(s.Super, "extra"), "commit", "-q", "-m", "child")
			for _, m := range []string{"m1", "m2", "m3"} {
				f.addSub(s.Super, m)
			}
			f.g(s.Super, "commit", "-q", "-m", "subs") // the index only: extra's new commit stays unstaged
			f.at(s.Super)
			f.asTrust(trusted)
			super := f.root
			sub := func(p string) string { return filepath.Join(super, p) }

			f.write(sub("lib/nested"), "u.txt", "u\n")
			f.g(sub("extra/child"), "commit", "-q", "--allow-empty", "-m", "x")
			f.write(sub("extra/child"), "u.txt", "u\n")
			f.write(sub("m1"), "f.txt", "changed\n")
			f.write(sub("m1"), "new.txt", "new\n")
			f.g(sub("m2"), "commit", "-q", "--allow-empty", "-m", "x")

			st := f.tick()
			// Each gitlink of the project's status, and the nested ones of lib's and extra's.
			for _, c := range []struct{ dir, path, abs, want string }{
				{super, "lib", "lib", "S..U"},
				{super, "extra", "extra", "SCMU"},
				{super, "m1", "m1", "S.MU"},
				{super, "m2", "m2", "SC.."},
				{super, "m3", "m3", ""},
				{sub("lib"), "nested", "lib/nested", "S..U"},
				{sub("extra"), "child", "extra/child", "SC.U"},
			} {
				want, present := f.gitBits(c.dir, "none", c.path)
				if bits := bits(&want); present && bits != c.want {
					t.Fatalf("the fixture: git's field for %s is %s; the test expects %s", c.abs, bits, c.want)
				}
				if !present && c.want != "" {
					t.Fatalf("the fixture: git has no record for %s", c.abs)
				}
				got, ok := entryOf(st, c.abs)
				if ok != present {
					t.Errorf("%s: entry present %v; git's record present %v\nall: %v", c.abs, ok, present, describeEntries(st))
					continue
				}
				if !ok {
					continue
				}
				if got.Submodule == nil || *got.Submodule != withCheckedOut(want, got.Submodule.CheckedOut) {
					t.Errorf("%s: %+v; git's bits are %s", c.abs, got.Submodule, bits(&want))
				}
				// checkedOut says the contents were read: on a trusted root only a gitlink git
				// flagged M or U is (m2 has new commits only, so git flagged nothing there).
				wantHead := ""
				if !trusted || want.Modified || want.Untracked {
					wantHead = strings.TrimSpace(f.g(sub(c.abs), "rev-parse", "HEAD"))
				}
				if got.Submodule != nil && got.Submodule.CheckedOut != wantHead {
					t.Errorf("%s: checkedOut %q; want %q", c.abs, got.Submodule.CheckedOut, wantHead)
				}
				wantRoot := filepath.ToSlash(filepath.Dir(c.abs))
				if wantRoot == "." {
					wantRoot = ""
				}
				if got.Root != wantRoot {
					t.Errorf("%s: root %q; want %q", c.abs, got.Root, wantRoot)
				}
			}
			// The files inside each: git's own, prefixed, with the submodule as root.
			for _, d := range []string{"lib", "lib/nested", "extra", "extra/child", "m1", "m2", "m3"} {
				got, want := filesOf(st, d), f.plainFiles(sub(d), d+"/")
				if !sameSet(got, want) {
					t.Errorf("%s: files %v; git -C %s status says %v", d, got, d, want)
				}
			}
			// A clean submodule has no files and no gitlink entry; nothing else is invented.
			for _, e := range st.Entries {
				if strings.HasPrefix(e.Path, "m3") {
					t.Errorf("a clean m3 has an entry %+v", e)
				}
				if e.Submodule != nil && e.Submodule.Unread != "" {
					t.Errorf("%s is unread: %s", e.Path, e.Submodule.Unread)
				}
			}
			if st.SubmodulesUnread {
				t.Errorf("submodulesUnread set")
			}
		})
	}
}

func withCheckedOut(b SubmoduleChange, oid string) SubmoduleChange {
	b.CheckedOut = oid
	return b
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		seen[x]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// Decision (ADR-0125 §2; the shape of TestRootSubdirectoryWorkspace): a workspace that is a
// subfolder of its repository, the `.gitmodules` at the top, lists its submodules' changes,
// by the paths below the workspace; a submodule of the repository outside the workspace is
// not read.
func TestSubmodulePassSubdirectoryWorkspace(t *testing.T) {
	t.Parallel()
	for _, trusted := range []bool{true, false} {
		t.Run(fmt.Sprintf("trusted %v", trusted), func(t *testing.T) {
			t.Parallel()
			f := newPassFx(t)
			top := f.root
			f.write(top, "app/a.txt", "a\n")
			f.write(top, "other/b.txt", "b\n")
			f.commit(top)
			for _, d := range []string{"app/sub", "other/sub2"} {
				f.g(top, "init", "-q", "-b", "main", d)
				f.write(filepath.Join(top, d), "s.txt", d+"\n")
				f.commit(filepath.Join(top, d))
			}
			f.gitmodules(top, "app/sub", "other/sub2")
			f.commit(top) // records both as gitlinks, and the .gitmodules
			f.write(filepath.Join(top, "app/sub"), "dirty.txt", "d\n")
			f.write(filepath.Join(top, "other/sub2"), "dirty.txt", "d\n")
			f.at(filepath.Join(top, "app"))
			f.asTrust(trusted)
			dirs := f.gitDirs()
			st := f.tick()
			if got := filesOf(st, "sub"); !equal(got, []string{"sub/dirty.txt untracked"}) {
				t.Errorf("files of sub %v\nall: %v", got, describeEntries(st))
			}
			if e, ok := entryOf(st, "sub"); !ok || e.Submodule == nil || !e.Submodule.Untracked {
				t.Errorf("sub's entry %+v (present %v); want untracked", e, ok)
			}
			for _, d := range *dirs {
				if !under(d, f.root) {
					t.Errorf("a git started in %q, outside the workspace %q", d, f.root)
				}
			}
			for _, e := range st.Entries {
				if strings.Contains(e.Path, "sub2") || strings.HasPrefix(e.Path, "..") {
					t.Errorf("an entry outside the workspace: %+v", e)
				}
			}
		})
	}
}

// Decision (ADR-0125 §2; ADR-0066 §3): on an untrusted root each submodule's status runs
// with the filter drivers read and switched off in that submodule's own configuration.
// Drivers, an fsmonitor script and a signature program configured only in lib's and in
// lib/nested's own config (the superproject has none) run nothing, and both are listed. The
// control is plain git, which runs them; a pass that read the drivers of the superproject only
// would run them here.
func TestSubmodulePassFence(t *testing.T) {
	t.Parallel()
	f := newPassFx(t)
	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
	f.at(s.Super)
	lib := filepath.Join(f.root, "lib")
	nested := filepath.Join(lib, "nested")
	marks := t.TempDir()
	mark := func(name string) string { return filepath.Join(marks, name) }
	for _, sm := range []struct{ dir, gitDir, file, name string }{
		{lib, filepath.Join(s.Super, ".git", "modules", "lib"), "lib.txt", "lib"},
		{nested, filepath.Join(s.Super, ".git", "modules", "lib", "modules", "nested"), "n.txt", "nested"},
	} {
		clean := f.script("clean-"+sm.name+".sh", "touch '"+mark("filter-"+sm.name)+"'\ncat")
		fsm := f.script("fsmonitor-"+sm.name+".sh", "touch '"+mark("fsmonitor-"+sm.name)+"'")
		f.append(filepath.Join(sm.gitDir, "config"),
			"[filter \"evil\"]\n\tclean = "+clean+"\n\trequired = true\n[core]\n\tfsmonitor = "+fsm+"\n")
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

	// The control: plain git runs the filters and the fsmonitors, so "no marker" can fail.
	clear()
	f.gErr(lib, "status", "--porcelain")
	f.gErr(nested, "status", "--porcelain")
	if got := marked(); len(got) < 4 {
		t.Fatalf("control: plain git status ran %v; want the filter and the fsmonitor of both submodules", got)
	}

	clear()
	f.touch(lib, "lib.txt")
	f.touch(nested, "n.txt")
	f.untrusted()
	st := f.tick()
	if got := marked(); len(got) != 0 {
		t.Errorf("untrusted: a submodule's own configuration ran something: %v", got)
	}
	want := []string{"lib/dirty.txt untracked"}
	if got := filesOf(st, "lib"); !equal(got, want) {
		t.Errorf("untrusted: lib's files %v; want %v", got, want)
	}
	if got := filesOf(st, "lib/nested"); !equal(got, []string{"lib/nested/dirty.txt untracked"}) {
		t.Errorf("untrusted: lib/nested's files %v", got)
	}
}

// Decision (ADR-0125 §2; ADR-0071 §7): the containment checks of git.submodules rerun every
// tick, so no git runs in a submodule directory that is a symlink out of the workspace, nor in
// one whose .git is a symlink, and a .git that names no repository is read by no status. The
// outside places hold real repositories with untracked files, so a pass that went there would
// list them.
func TestSubmodulePassContainment(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (f *readFx, super, lib string) {
		f = newPassFx(t)
		s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
		f.at(s.Super)
		f.untrusted()
		return f, s.Super, filepath.Join(f.root, "lib")
	}
	noFiles := func(t *testing.T, st Status) {
		t.Helper()
		for _, e := range st.Entries {
			if e.Root != "" {
				t.Errorf("an entry inside a submodule: %+v", e)
			}
		}
	}
	t.Run("a directory that is a symlink out of the root", func(t *testing.T) {
		t.Parallel()
		f, _, lib := setup(t)
		outside := f.platform.Canonical(t.TempDir())
		f.g(outside, "init", "-q", "-b", "main", "lib")
		f.write(filepath.Join(outside, "lib"), "x", "x\n")
		f.commit(filepath.Join(outside, "lib"))
		f.write(filepath.Join(outside, "lib"), "untracked.txt", "u\n")
		if err := os.RemoveAll(lib); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "lib"), lib); err != nil {
			t.Fatal(err)
		}
		dirs := f.gitDirs()
		noFiles(t, f.tick())
		for _, d := range *dirs {
			if under(d, outside) || under(d, lib) {
				t.Errorf("a git started in %q, in or behind the symlink", d)
			}
		}
	})
	t.Run("a symlink on the way to the directory", func(t *testing.T) {
		t.Parallel()
		// mods is a symlink out of the root and mods/m a real repository behind it: the
		// directory itself is not a symlink, so only resolving the whole path finds out.
		f := newPassFx(t)
		f.write(f.root, "a.txt", "a\n")
		f.commit(f.root)
		outside := f.platform.Canonical(t.TempDir())
		f.g(outside, "init", "-q", "-b", "main", "m")
		f.write(filepath.Join(outside, "m"), "x", "x\n")
		f.commit(filepath.Join(outside, "m"))
		f.write(filepath.Join(outside, "m"), "untracked.txt", "u\n")
		if err := os.Symlink(outside, filepath.Join(f.root, "mods")); err != nil {
			t.Fatal(err)
		}
		f.gitlinkAt(f.root, strings.TrimSpace(f.g(filepath.Join(outside, "m"), "rev-parse", "HEAD")), "mods/m")
		f.gitmodules(f.root, "mods/m")
		f.untrusted()
		dirs := f.gitDirs()
		noFiles(t, f.tick())
		for _, d := range *dirs {
			if under(d, outside) {
				t.Errorf("a git started in %q, outside the workspace", d)
			}
		}
	})
	t.Run("a .git that is a symlink", func(t *testing.T) {
		t.Parallel()
		f, super, lib := setup(t)
		f.write(lib, "untracked.txt", "u\n")
		if err := os.Remove(filepath.Join(lib, ".git")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(super, ".git", "modules", "lib"), filepath.Join(lib, ".git")); err != nil {
			t.Fatal(err)
		}
		dirs := f.gitDirs()
		noFiles(t, f.tick())
		for _, d := range *dirs {
			if under(d, lib) {
				t.Errorf("a git started in %q, whose .git is a symlink", d)
			}
		}
	})
	t.Run("a .git that names no repository", func(t *testing.T) {
		t.Parallel()
		// The git that asks whether lib is a repository of its own is the only one that runs in
		// it: no status starts, nothing is listed. On a trusted root that git answers "not a
		// repository" and lib is uninitialised; on an untrusted one the driver read before it
		// fails, which marks lib unread with git's words.
		for _, trusted := range []bool{true, false} {
			f, _, lib := setup(t)
			f.asTrust(trusted)
			f.write(lib, "untracked.txt", "u\n")
			f.write(lib, ".git", "gitdir: "+filepath.Join(t.TempDir(), "nowhere")+"\n")
			var statuses []string
			f.onStart = func(cmd *exec.Cmd) {
				for _, a := range cmd.Args {
					if a == "status" && under(cmd.Dir, lib) {
						statuses = append(statuses, cmd.Dir)
					}
				}
			}
			st := f.tick()
			noFiles(t, st)
			if len(statuses) != 0 {
				t.Errorf("trusted %v: a status started in %v", trusted, statuses)
			}
			if e, ok := entryOf(st, "lib"); trusted && ok && e.Submodule != nil && e.Submodule.Unread != "" {
				t.Errorf("trusted: lib is unread (%s); a directory that is no repository is not a failure", e.Submodule.Unread)
			}
		}
	})
}

// Decision (ADR-0125 §4, plan m3b Q3): a git that fails in a submodule marks that gitlink
// unread, with a cause, and nothing else: the project's entries and the other submodules'
// files are published and the watch goes on. lib's own config holds a filter key that cannot
// be switched off (`filter.""`) on an untrusted root; extra is dirty and listed.
func TestSubmoduleFailureDegradesOnlyIt(t *testing.T) {
	t.Parallel()
	f := newWatchFxWith(t, 20*time.Millisecond, func(o *Options) {
		o.readTimeout = testtime.Bound(60 * time.Second)
		o.passTimeout = testtime.Bound(60 * time.Second)
	})
	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
	f.root = platform.Current().Canonical(s.Super)
	f.initSub(s.Super, "extra")
	f.write(s.Super, "top.txt", "top\n")
	f.write(filepath.Join(s.Super, "extra"), "e.txt", "e\n")
	f.write(filepath.Join(s.Super, "lib"), "l.txt", "l\n")
	f.append(filepath.Join(s.Super, ".git", "modules", "lib", "config"), "[filter \"\"]\n\tclean = x\n")
	f.trusted.Store(false)

	res, sub := f.start()
	st := Status{Entries: res.Entries}
	if e, ok := entryOf(st, "lib"); !ok || e.Submodule == nil || e.Submodule.Unread == "" || !strings.Contains(e.Submodule.Unread, "filter driver") || !strings.Contains(e.Submodule.Unread, `""`) {
		t.Errorf("lib = %+v (present %v); want unread, naming the driver", e, ok)
	}
	if got := filesOf(st, "lib"); len(got) != 0 {
		t.Errorf("lib is unread but its files are listed: %v", got)
	}
	if got := filesOf(st, "extra"); !equal(got, []string{"extra/e.txt untracked"}) {
		t.Errorf("extra's files %v; want extra/e.txt", got)
	}
	if !f.hasEntry(st, "top.txt", MarkUntracked) {
		t.Errorf("the project's own entries %v; want top.txt", describeEntries(st))
	}
	if res.SubmodulesUnread {
		t.Errorf("submodulesUnread set; a gitlink carries the failure")
	}
	// The watch goes on: the next change in extra is published, with lib still unread.
	f.write(filepath.Join(s.Super, "extra"), "more.txt", "m\n")
	ev := statusEvent(t, next(t, sub))
	if got := filesOf(ev, "extra"); !equal(got, []string{"extra/e.txt untracked", "extra/more.txt untracked"}) {
		t.Errorf("the next event's extra files %v", got)
	}
	if f.ws.Live() != 1 {
		t.Errorf("%d pollers live; want the watch still running", f.ws.Live())
	}
}

// Decision (ADR-0125 §4): a failure of the git of lib's nested listing, here lib's own
// malformed .gitmodules, marks lib unread and lists none of its files, though lib's own status
// succeeded; the project's other entries stay.
func TestSubmoduleNestedListingFailureDegradesIt(t *testing.T) {
	t.Parallel()
	f := newPassFx(t)
	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
	f.at(s.Super)
	f.untrusted()
	lib := filepath.Join(f.root, "lib")
	f.write(lib, "l.txt", "l\n")
	f.write(lib, ".gitmodules", "[submodule \"x\"]\n\tpath = x\nthis is not a config line\n")
	f.write(f.root, "top.txt", "top\n")
	st := f.tick()
	e, ok := entryOf(st, "lib")
	if !ok || e.Submodule == nil || !strings.Contains(e.Submodule.Unread, "its submodules could not be listed") {
		t.Errorf("lib = %+v (present %v); want unread because its submodules could not be listed\nall: %v", e, ok, describeEntries(st))
	}
	for _, x := range st.Entries {
		if x.Root != "" {
			t.Errorf("an entry inside an unread lib: %+v", x)
		}
	}
	if _, ok := entryOf(st, "top.txt"); !ok {
		t.Errorf("the project's own entry is missing: %v", describeEntries(st))
	}
}

// Decision (ADR-0125 §4 and the trusted fallback of step 1): a trusted status that fell back
// to `dirty` because git's child in lib failed (its index is garbage) is read as on an
// untrusted root, so the submodule that failed comes out unread and the other submodule is
// still read, and the watch has a status to publish.
func TestSubmodulePassAfterTheFallback(t *testing.T) {
	t.Parallel()
	f := newPassFx(t)
	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
	f.initSub(s.Super, "extra")
	f.at(s.Super)
	f.g(filepath.Join(s.Super, "lib"), "commit", "-q", "--allow-empty", "-m", "x")
	f.write(filepath.Join(s.Super, "extra"), "e.txt", "e\n")
	if err := os.WriteFile(filepath.Join(s.Super, ".git", "modules", "lib", "index"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := f.tick()
	if e, ok := entryOf(st, "lib"); !ok || e.Submodule == nil || e.Submodule.Unread == "" || !e.Submodule.Commits {
		t.Errorf("lib = %+v (present %v); want its new commit and unread", e, ok)
	}
	if got := filesOf(st, "extra"); !equal(got, []string{"extra/e.txt untracked"}) {
		t.Errorf("extra's files %v; want extra/e.txt (read as on an untrusted root)", got)
	}
	if e, ok := entryOf(st, "extra"); !ok || e.Submodule == nil || !e.Submodule.Untracked {
		t.Errorf("extra = %+v (present %v); want untracked", e, ok)
	}
}

// Decision (ADR-0125 §4): a failure to list the project's own submodules (here a malformed
// top .gitmodules) sets submodulesUnread, since no gitlink can say it, and the project's own
// entries are published all the same; nothing stops. It also settles whether the project's
// status survives a malformed .gitmodules, on both trusts.
func TestSubmodulesUnreadWhenTheListFails(t *testing.T) {
	t.Parallel()
	for _, trusted := range []bool{true, false} {
		t.Run(fmt.Sprintf("trusted %v", trusted), func(t *testing.T) {
			t.Parallel()
			f := newPassFx(t)
			s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
			f.at(s.Super)
			f.asTrust(trusted)
			f.write(filepath.Join(s.Super, "lib"), "l.txt", "l\n")
			f.write(s.Super, ".gitmodules", "[submodule \"x\"]\n\tpath = x\nthis is not a config line\n")
			f.write(s.Super, "top.txt", "top\n")
			st := f.tick()
			if !st.SubmodulesUnread {
				t.Errorf("submodulesUnread not set; entries %v", describeEntries(st))
			}
			if _, ok := entryOf(st, "top.txt"); !ok {
				t.Errorf("the project's own entries are lost: %v", describeEntries(st))
			}
		})
	}
}

// Decision (ADR-0125 §4, plan m3b Q3): the pass reads at most a number of submodules per tick,
// to a depth of eight, under one deadline; what the bounds leave unread is said by
// submodulesUnread, and by `unread` on the gitlinks the status has an entry for; none of them
// stops the watch. The limits are shortened here (the cap to 2; the deadline to nothing).
func TestSubmodulePassBounds(t *testing.T) {
	t.Parallel()
	t.Run("the third of three dirty submodules", func(t *testing.T) {
		t.Parallel()
		for _, trusted := range []bool{true, false} {
			t.Run(fmt.Sprintf("trusted %v", trusted), func(t *testing.T) {
				t.Parallel()
				f := newPassFx(t)
				f.passCap = 2
				f.register()
				for _, m := range []string{"m1", "m2", "m3"} {
					f.addSub(f.root, m)
				}
				f.commit(f.root)
				f.at(f.root)
				f.asTrust(trusted)
				for _, m := range []string{"m1", "m2", "m3"} {
					f.write(filepath.Join(f.root, m), "dirty.txt", "d\n")
				}
				st := f.tick()
				if !st.SubmodulesUnread {
					t.Errorf("submodulesUnread not set")
				}
				for _, m := range []string{"m1", "m2"} {
					if got := filesOf(st, m); !equal(got, []string{m + "/dirty.txt untracked"}) {
						t.Errorf("%s: files %v; want its dirty.txt", m, got)
					}
				}
				if got := filesOf(st, "m3"); len(got) != 0 {
					t.Errorf("m3 is past the cap but its files are listed: %v", got)
				}
				e, ok := entryOf(st, "m3")
				if trusted {
					// git flagged it, so the status has an entry to carry the word.
					if !ok || e.Submodule == nil || e.Submodule.Unread == "" {
						t.Errorf("trusted: m3 = %+v (present %v); want unread", e, ok)
					}
				} else if ok {
					// Nothing flagged it and there is no entry to make: submodulesUnread alone says so.
					t.Errorf("untrusted: m3 has an entry %+v", e)
				}
				for _, m := range []string{"m1", "m2"} {
					if e, _ := entryOf(st, m); e.Submodule == nil || e.Submodule.Unread != "" {
						t.Errorf("%s = %+v; want read", m, e)
					}
				}
			})
		}
	})

	t.Run("the ninth level", func(t *testing.T) {
		t.Parallel()
		for _, trusted := range []bool{true, false} {
			t.Run(fmt.Sprintf("trusted %v", trusted), func(t *testing.T) {
				t.Parallel()
				f := newPassFx(t)
				// A chain of nine nested submodules: s/sub/sub/... Each repository is made with the
				// next one inside it, from the deepest up.
				var src string
				for i := 9; i >= 1; i-- {
					r := f.repo()
					f.write(r, "f.txt", fmt.Sprintf("level %d\n", i))
					f.commit(r)
					if src != "" {
						f.g(r, "-c", "protocol.file.allow=always", "submodule", "add", "-q", src, "sub")
						f.g(r, "commit", "-q", "-m", "sub")
					}
					src = r
				}
				f.g(f.root, "-c", "protocol.file.allow=always", "submodule", "add", "-q", src, "sub")
				f.g(f.root, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--init", "--recursive")
				f.commit(f.root)
				f.at(f.root)
				f.asTrust(trusted)
				dir, rels := f.root, []string{}
				for i := 1; i <= 9; i++ {
					dir = filepath.Join(dir, "sub")
					rels = append(rels, strings.TrimPrefix(dir, f.root+string(filepath.Separator)))
					if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
						t.Fatalf("level %d was not initialised: %v", i, err)
					}
					f.write(dir, "u.txt", "u\n")
				}
				st := f.tick()
				for i, rel := range rels {
					got := filesOf(st, rel)
					if i < 8 && !equal(got, []string{rel + "/u.txt untracked"}) {
						t.Errorf("level %d (%s): files %v; want its u.txt", i+1, rel, got)
					}
					if i == 8 && len(got) != 0 {
						t.Errorf("level 9 (%s) was read: %v", rel, got)
					}
				}
				// What the pass does not read says so where its contents are in question: on a
				// trusted root git flagged level 9, on an untrusted one nothing says it changed.
				if e, ok := entryOf(st, rels[8]); trusted && (!ok || e.Submodule == nil || e.Submodule.Unread != "nested deeper than eight levels") {
					t.Errorf("trusted: level 9 = %+v (present %v); want unread, nested deeper than eight levels", e, ok)
				}
			})
		}
	})

	t.Run("a deadline that has passed", func(t *testing.T) {
		t.Parallel()
		f := newPassFx(t)
		s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
		f.passTimeout = time.Nanosecond
		f.at(s.Super)
		f.write(filepath.Join(f.root, "lib"), "dirty.txt", "d\n")
		f.write(f.root, "top.txt", "top\n")
		for _, trusted := range []bool{true, false} {
			f.asTrust(trusted)
			st := f.tick() // fatal on an error: the deadline stops nothing
			if !st.SubmodulesUnread {
				t.Errorf("trusted %v: submodulesUnread not set", trusted)
			}
			if _, ok := entryOf(st, "top.txt"); !ok {
				t.Errorf("trusted %v: the project's own entries are lost: %v", trusted, describeEntries(st))
			}
			if got := filesOf(st, "lib"); len(got) != 0 {
				t.Errorf("trusted %v: lib's files %v after the deadline", trusted, got)
			}
			// The trusted tick is a fallen-back one: the `none` status has the same bound, which is
			// gone, so git flagged nothing and lib has no entry to carry `unread`.
		}
	})
}

// Decision (ADR-0125 §2; package comment): a repository without a `.gitmodules` pays nothing
// for the pass: a tick starts the same gits the project's status alone does. The control: one
// empty `.gitmodules` makes the pass list, which starts more (on an untrusted root, where the
// pass runs even when git flagged nothing).
func TestWatchWithoutGitmodulesRunsAsBefore(t *testing.T) {
	t.Parallel()
	for _, trusted := range []bool{true, false} {
		t.Run(fmt.Sprintf("trusted %v", trusted), func(t *testing.T) {
			t.Parallel()
			f := newPassFx(t)
			f.write(f.root, "a.txt", "a\n")
			f.commit(f.root)
			f.write(f.root, "a.txt", "b\n")
			f.asTrust(trusted)
			var argvs []string
			f.onStart = func(cmd *exec.Cmd) { argvs = append(argvs, strings.Join(cmd.Args[1:], " ")) }
			r, cerr := f.opts.runner()
			if cerr != nil {
				t.Fatal(cerr)
			}
			if _, err := r.status(context.Background(), f.root, trusted); err != nil {
				t.Fatal(err)
			}
			alone := len(argvs)
			argvs = nil
			f.tick()
			if len(argvs) != alone {
				t.Errorf("a tick started %d gits, the project's status alone %d:\n%s", len(argvs), alone, strings.Join(argvs, "\n"))
			}
			if !trusted {
				f.write(f.root, ".gitmodules", "")
				argvs = nil
				f.tick()
				if len(argvs) <= alone {
					t.Errorf("control: a .gitmodules did not make the pass list (%d gits, %d alone)", len(argvs), alone)
				}
			}
		})
	}
}

// Decision (ADR-0125 §2): a file written in a submodule during a watch arrives in the next
// event, with the submodule as its root, on a trusted root (where git flags the submodule)
// and on an untrusted one (where the daemon reads it).
func TestWatchPublishesASubmoduleEdit(t *testing.T) {
	t.Parallel()
	for _, trusted := range []bool{true, false} {
		t.Run(fmt.Sprintf("trusted %v", trusted), func(t *testing.T) {
			t.Parallel()
			f := newWatchFxWith(t, 20*time.Millisecond, func(o *Options) {
				o.readTimeout = testtime.Bound(60 * time.Second)
				o.passTimeout = testtime.Bound(60 * time.Second)
			})
			s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
			f.root = platform.Current().Canonical(s.Super)
			f.trusted.Store(trusted)
			res, sub := f.start()
			if len(res.Entries) != 0 {
				t.Fatalf("the baseline %v; want a clean tree", res.Entries)
			}
			f.write(filepath.Join(s.Super, "lib"), "edit.txt", "e\n")
			ev := statusEvent(t, next(t, sub))
			if got := filesOf(ev, "lib"); !equal(got, []string{"lib/edit.txt untracked"}) {
				t.Errorf("the event's lib files %v\nall: %v", got, describeEntries(ev))
			}
			if e, ok := entryOf(ev, "lib"); !ok || e.Submodule == nil || !e.Submodule.Untracked {
				t.Errorf("the event's lib = %+v (present %v); want untracked", e, ok)
			}
		})
	}
}

// Recorded, not asserted (plan m3b step 2): one tick of the watch over 20 initialised
// submodules, one of them dirty, on a trusted and an untrusted root, with the number of gits
// started. The test fails only on a wrong answer or on minutes. Run it with each git:
// `go test -run TestSubmodulePassTwenty -v ./internal/domain/git` with the git wanted first on
// PATH.
func TestSubmodulePassTwenty(t *testing.T) {
	t.Parallel()
	f := newPassFx(t)
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
	f.commit(f.root)
	f.write(filepath.Join(f.root, "mods/m07"), "dirty.txt", "d\n")
	version := strings.TrimSpace(f.g(f.root, "--version"))

	for _, trusted := range []bool{true, false} {
		f.asTrust(trusted)
		var gits int
		f.onStart = func(cmd *exec.Cmd) { gits++ }
		start := time.Now()
		st := f.tick()
		elapsed := time.Since(start)
		if got := filesOf(st, "mods/m07"); !equal(got, []string{"mods/m07/dirty.txt untracked"}) {
			t.Errorf("trusted %v: m07's files %v", trusted, got)
		}
		if len(st.Entries) != 2 || st.SubmodulesUnread {
			t.Errorf("trusted %v: entries %v, submodulesUnread %v; want m07 and its file only", trusted, describeEntries(st), st.SubmodulesUnread)
		}
		if elapsed > 2*time.Minute {
			t.Errorf("trusted %v: the tick took %s", trusted, elapsed)
		}
		t.Logf("%s: one tick over 20 initialised submodules, one dirty, trusted %v: %s, %d gits started by the daemon", version, trusted, elapsed.Round(time.Millisecond), gits)
	}
}
