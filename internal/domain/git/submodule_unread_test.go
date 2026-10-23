package git

// m3b step 2, verifier round (ADR-0125 §3, §4): the trusted fallback's bound, what the pass
// says of a gitlink it did not read, an unread nested gitlink that is no change of its parent,
// and the watch's dedup.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/git/gittest"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// Decision (ADR-0125 §4): a trusted `none` status that is slow has the pass's bound, not the
// runner's, and what follows it is read with `dirty`. lib/nested's own config holds a clean
// filter that sleeps, and its file is stat-dirty: git's child of a child in the `none` status
// runs it and hangs; lib's own `dirty` status starts nothing in nested. The bounds are
// shortened (pass and `none` 2 s scaled, runner 30 s): the tick lasts the `none` bound, the
// `dirty` status and the pass deadline, and lib is read (its files listed) while lib/nested,
// whose own status runs the filter, is unread. The old retry after the runner's bound makes the
// tick as long as that; a `none` in the fallen-back pass makes lib itself hang.
func TestTrustedFallbackBoundsTheTick(t *testing.T) {
	t.Parallel()
	f := newPassFx(t)
	bound := testtime.Bound(2 * time.Second)
	f.passTimeout = bound
	f.readTimeout = 15 * bound
	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
	f.at(s.Super)
	lib := filepath.Join(f.root, "lib")
	nested := filepath.Join(lib, "nested")
	gitDir := filepath.Join(s.Super, ".git", "modules", "lib", "modules", "nested")
	slow := f.script("slow.sh", "exec sleep 120")
	f.append(filepath.Join(gitDir, "config"), "[filter \"slow\"]\n\tclean = "+slow+"\n")
	f.write(gitDir, "info/attributes", "n.txt filter=slow\n")
	f.touch(nested, "n.txt")
	f.write(lib, "dirty.txt", "d\n")

	start := time.Now()
	st := f.tick()
	elapsed := time.Since(start)
	if elapsed > 8*bound {
		t.Errorf("the tick took %s; want about the none bound %s, a dirty status and the pass deadline %s", elapsed.Round(time.Millisecond), bound, bound)
	}
	if got := filesOf(st, "lib"); !equal(got, []string{"lib/dirty.txt untracked"}) {
		t.Errorf("lib's files %v; want lib/dirty.txt (the fallen-back pass reads lib with dirty)", got)
	}
	if e, ok := entryOf(st, "lib/nested"); !ok || e.Submodule == nil || e.Submodule.Unread == "" {
		t.Errorf("lib/nested = %+v (present %v); want unread (its own status ran out of the pass's time)", e, ok)
	}
	if !st.SubmodulesUnread {
		t.Errorf("submodulesUnread not set after the pass deadline")
	}
}

// Decision (ADR-0125 §3): a gitlink the pass does not read says so when its contents are in
// question, with a sentence. An embedded repository (added to the index, no .gitmodules entry)
// holding an untracked file, on a trusted root where git flagged it: "not listed in
// .gitmodules", in a project whose .gitmodules names others and in one with no .gitmodules at
// all. A listed submodule whose .git is a symlink on a trusted root: no git runs there and it
// says so. A gitlink with no directory of its own says nothing.
func TestUnreadGitlinkSaysWhyItWasNotRead(t *testing.T) {
	t.Parallel()
	embed := func(f *readFx, super string) {
		f.g(super, "init", "-q", "-b", "main", "emb")
		f.write(filepath.Join(super, "emb"), "e.txt", "e\n")
		f.commit(filepath.Join(super, "emb"))
		f.g(super, "add", "emb")
		f.write(filepath.Join(super, "emb"), "untracked.txt", "u\n")
	}
	t.Run("not in a .gitmodules that names others", func(t *testing.T) {
		t.Parallel()
		f := newPassFx(t)
		s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
		f.at(s.Super)
		embed(f, s.Super)
		st := f.tick()
		if e, ok := entryOf(st, "emb"); !ok || e.Submodule == nil || e.Submodule.Unread != "not listed in .gitmodules" {
			t.Errorf("emb = %+v (present %v); want unread, not listed in .gitmodules\nall: %v", e, ok, describeEntries(st))
		}
		if got := filesOf(st, "emb"); len(got) != 0 {
			t.Errorf("emb's files %v", got)
		}
	})
	t.Run("no .gitmodules at all", func(t *testing.T) {
		t.Parallel()
		f := newPassFx(t)
		f.write(f.root, "a.txt", "a\n")
		f.commit(f.root)
		embed(f, f.root)
		dirs := f.gitDirs()
		st := f.tick()
		if e, ok := entryOf(st, "emb"); !ok || e.Submodule == nil || e.Submodule.Unread != "not listed in .gitmodules" {
			t.Errorf("emb = %+v (present %v); want unread, not listed in .gitmodules\nall: %v", e, ok, describeEntries(st))
		}
		for _, d := range *dirs {
			if under(d, filepath.Join(f.root, "emb")) {
				t.Errorf("a git started in %q", d)
			}
		}
	})
	t.Run("a listed submodule whose .git is a symlink", func(t *testing.T) {
		t.Parallel()
		f := newPassFx(t)
		s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
		f.at(s.Super)
		lib := filepath.Join(f.root, "lib")
		f.write(lib, "untracked.txt", "u\n")
		if err := os.Remove(filepath.Join(lib, ".git")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(s.Super, ".git", "modules", "lib"), filepath.Join(lib, ".git")); err != nil {
			t.Fatal(err)
		}
		st := f.tick()
		if e, ok := entryOf(st, "lib"); !ok || e.Submodule == nil || e.Submodule.Unread != "its .git is a symbolic link" {
			t.Errorf("lib = %+v (present %v); want unread, its .git is a symbolic link\nall: %v", e, ok, describeEntries(st))
		}
		if got := filesOf(st, "lib"); len(got) != 0 {
			t.Errorf("lib's files %v", got)
		}
	})
	t.Run("an uninitialised gitlink", func(t *testing.T) {
		t.Parallel()
		f := newPassFx(t)
		f.write(f.root, "a.txt", "a\n")
		f.commit(f.root)
		oid := strings.TrimSpace(f.g(f.root, "rev-parse", "HEAD"))
		f.gitlinkAt(f.root, oid, "ghost")
		for _, trusted := range []bool{true, false} {
			f.asTrust(trusted)
			st := f.tick()
			if e, ok := entryOf(st, "ghost"); ok && e.Submodule != nil && e.Submodule.Unread != "" {
				t.Errorf("trusted %v: ghost is unread (%s); it has no contents in question", trusted, e.Submodule.Unread)
			}
		}
	})
}

// Decision (ADR-0125 §3): a nested gitlink that could not be read is no change of its parent.
// lib is clean; lib/nested's own config holds a filter key that cannot be switched off, on an
// untrusted root, so lib/nested is unread, and lib carries no modification for it.
func TestUnreadNestedIsNoChangeOfItsParent(t *testing.T) {
	t.Parallel()
	f := newPassFx(t)
	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
	f.at(s.Super)
	f.untrusted()
	f.append(filepath.Join(s.Super, ".git", "modules", "lib", "modules", "nested", "config"), "[filter \"\"]\n\tclean = x\n")
	st := f.tick()
	if e, ok := entryOf(st, "lib/nested"); !ok || e.Submodule == nil || e.Submodule.Unread == "" {
		t.Errorf("lib/nested = %+v (present %v); want unread\nall: %v", e, ok, describeEntries(st))
	}
	if e, ok := entryOf(st, "lib"); ok && e.Submodule != nil && (e.Submodule.Modified || e.Submodule.Untracked || e.Submodule.Commits) {
		t.Errorf("lib = %+v; want no change of its own (its nested submodule was not read)", e.Submodule)
	}
}

// Decision (ADR-0066 §1): the watch publishes when the status differs on the wire; a status
// that fell back to dirty and one that did not, otherwise equal, are the same.
func TestWatchDedupIgnoresTheFallback(t *testing.T) {
	t.Parallel()
	a := Status{Entries: []Entry{{Path: "x", Mark: MarkModified}}, fellBack: true}
	b := Status{Entries: []Entry{{Path: "x", Mark: MarkModified}}}
	if !sameOnTheWire(a, b) {
		t.Errorf("a fallback flipping is a change")
	}
	b.Entries[0].Mark = MarkAdded
	if sameOnTheWire(a, b) {
		t.Errorf("different entries are the same")
	}
}

// TestNoneBoundOnlyWithSubmodules: a trusted project with no .gitmodules keeps the runner's
// bound, so a status slower than the pass's bound is not cut and asked again with `dirty`.
func TestNoneBoundOnlyWithSubmodules(t *testing.T) {
	t.Parallel()
	f := newPassFx(t)
	bound := testtime.Bound(500 * time.Millisecond)
	f.passTimeout = bound
	f.readTimeout = 20 * bound
	f.register()
	f.write(f.root, "a.txt", "a\n")
	f.commit(f.root)
	slow := f.script("slow.sh", "sleep "+strconv.FormatFloat((2*bound).Seconds(), 'f', 2, 64)+"; cat")
	f.appendConfig(f.root, "[filter \"slow\"]\n\tclean = "+slow+"\n")
	f.write(f.root, ".git/info/attributes", "a.txt filter=slow\n")
	f.touch(f.root, "a.txt")
	st := f.tick()
	if st.fellBack {
		t.Errorf("a trusted status with no submodules fell back; want the runner's bound, one status")
	}
	if len(st.Entries) != 0 {
		t.Errorf("entries %+v; want none (the filter passes the content through)", st.Entries)
	}
}
