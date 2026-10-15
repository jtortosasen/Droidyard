package gittest

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var (
	fixtureDir = flag.String("fixture", "", "write the history fixtures into this directory (hist, and big unless -fixture-big=false) and stop")
	fixtureBig = flag.Bool("fixture-big", true, "with -fixture: also write big, 100,000 commits and 40 merged branches")
)

// TestMakeFixture is not a test: with -fixture=<dir> it writes the repositories the
// level-4 runs of plan m2n open in the browser, <dir>/hist (with its bare origin
// beside it as hist-origin.git and a linked work tree as hist-linked), <dir>/big, and the
// fixtures of part 2: w-origin.git, w and w-other (Remote), s and s-*.git (Submodules).
func TestMakeFixture(t *testing.T) {
	t.Parallel()
	if *fixtureDir == "" {
		t.Skip("run with -fixture=<dir> to write the history fixtures")
	}
	History(t, filepath.Join(*fixtureDir, "hist"))
	Remote(t, *fixtureDir)
	Submodules(t, *fixtureDir)
	if *fixtureBig {
		Synthetic(t, filepath.Join(*fixtureDir, "big"), 100000, 40, 100)
	}
}

func out(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(newGit(t).run(dir, nil, args...))
}

func count(t *testing.T, dir string, args ...string) int {
	t.Helper()
	n, err := strconv.Atoi(out(t, dir, args...))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Decision (plan m2n step 1b): History makes every shape later steps assert on, so a
// fixture that drifts fails here, not as a puzzling failure in a capability test.
func TestHistoryShape(t *testing.T) {
	t.Parallel()
	r := History(t, filepath.Join(t.TempDir(), "hist"))
	d := r.Dir

	refs := strings.Fields(out(t, d, "for-each-ref", "--format=%(refname)"))
	for _, want := range []string{
		"refs/heads/main", "refs/heads/feature/login", "refs/heads/feature/search/ui/results",
		"refs/heads/bugfix/crash", "refs/heads/chore/deps", "refs/heads/chore/wt",
		"refs/remotes/origin/main", "refs/remotes/origin/release/1.0", "refs/remotes/origin/HEAD",
		"refs/tags/v0.9", "refs/tags/v1.0", "refs/tags/v1.0-final", "refs/tags/tree-tag",
	} {
		if !contains(refs, want) {
			t.Errorf("no %s in %v", want, refs)
		}
	}
	if contains(refs, "refs/heads/release/1.0") {
		t.Errorf("release/1.0 must exist on origin only")
	}

	if got := len(strings.Fields(out(t, d, "rev-list", "--parents", "-n1", r.Oids["merge"]))); got != 3 {
		t.Errorf("merge has %d fields in its rev-list line, want 3 (itself and two parents)", got)
	}
	if got := len(strings.Fields(out(t, d, "rev-list", "--parents", "-n1", r.Oids["octopus"]))); got != 5 {
		t.Errorf("octopus has %d fields in its rev-list line, want 5 (itself and four parents)", got)
	}
	// A merge carries the files its branches brought (fast-import would keep only the first
	// parent's tree).
	for oid, files := range map[string][]string{
		r.Oids["merge"]:   {"src/login.txt", "merged.txt"},
		r.Oids["octopus"]: {"src/login.txt", "src/crash.txt", "deps.txt", "src/search.txt", "octopus.txt"},
	} {
		tree := strings.Fields(out(t, d, "ls-tree", "-r", "--name-only", oid))
		for _, f := range files {
			if !contains(tree, f) {
				t.Errorf("%s lacks %s: %v", oid[:7], f, tree)
			}
		}
	}
	if got := out(t, d, "diff-tree", "-r", "-M", "--name-status", "--no-commit-id", r.Oids["rename"]+"^", r.Oids["rename"]); !strings.HasPrefix(got, "R100\tdocs/old-name.txt\tdocs/new-name.txt") {
		t.Errorf("rename commit diff = %q, want R100 docs/old-name.txt -> docs/new-name.txt", got)
	}
	if got := out(t, d, "diff-tree", "-r", "--numstat", "--root", "--no-commit-id", r.Oids["root"]); !strings.Contains(got, "-\t-\tdata/blob.bin") {
		t.Errorf("root commit numstat = %q, want data/blob.bin as binary", got)
	}

	// Tag kinds: lightweight, annotated, a tag of a tag, a tag on a tree.
	for tag, want := range map[string]string{"v0.9": "commit", "v1.0": "tag", "v1.0-final": "tag", "tree-tag": "tag"} {
		if got := out(t, d, "for-each-ref", "--format=%(objecttype)", "refs/tags/"+tag); got != want {
			t.Errorf("tag %s object type = %q, want %q", tag, got, want)
		}
	}
	if got := out(t, d, "for-each-ref", "--format=%(*objecttype)", "refs/tags/tree-tag"); got != "tree" {
		t.Errorf("tree-tag points at a %q, want a tree", got)
	}
	if got := out(t, d, "cat-file", "-p", "refs/tags/v1.0-final"); !strings.Contains(got, "\ntype tag\n") {
		t.Errorf("v1.0-final does not point at another tag:\n%s", got)
	}
	if got := out(t, d, "for-each-ref", "--format=%(*objecttype)", "refs/tags/v1.0-final"); got != "commit" {
		t.Errorf("for-each-ref peels v1.0-final to a %q, want a commit (the peel is recursive)", got)
	}

	// Main is ahead of and behind origin/main by one commit each.
	if got := out(t, d, "rev-list", "--left-right", "--count", "main...origin/main"); got != "1\t1" {
		t.Errorf("main...origin/main = %q, want 1 1", got)
	}
	if got := out(t, d, "rev-parse", "--abbrev-ref", "main@{upstream}"); got != "origin/main" {
		t.Errorf("main's upstream = %q", got)
	}
	if got := out(t, d, "rev-parse", "HEAD"); got != r.Oids["head"] {
		t.Errorf("HEAD = %s, want %s", got, r.Oids["head"])
	}
	if got := out(t, d, "status", "--porcelain"); got != "" {
		t.Errorf("the work tree is not clean: %q", got)
	}

	// A linked work tree on chore/wt.
	wt := out(t, d, "worktree", "list", "--porcelain")
	linked, err := filepath.EvalSymlinks(r.Linked)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wt, "worktree "+linked) || !strings.Contains(wt, "branch refs/heads/chore/wt") {
		t.Errorf("worktree list lacks the linked tree:\n%s", wt)
	}
	if _, err := os.Stat(filepath.Join(r.Linked, ".git")); err != nil {
		t.Errorf("linked work tree: %v", err)
	}

	// Non-ASCII author, kept as git stores it.
	if got := out(t, d, "log", "--format=%an", "--author=Zo", "-n1"); got != "Zoë Ünïcode" {
		t.Errorf("author = %q", got)
	}
	if out(t, d, "fsck", "--strict", "--no-dangling") != "" {
		t.Errorf("fsck complained")
	}
}

// Decision (plan m2n step 1b): Synthetic makes exactly the commit count asked for, with
// the merged branches and the tags, because the benchmarks quote "100,000 commits".
func TestSyntheticCounts(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ commits, branches, tags int }{{200, 3, 12}, {50, 0, 0}, {13, 4, 1}} {
		dir := filepath.Join(t.TempDir(), "syn")
		Synthetic(t, dir, c.commits, c.branches, c.tags)
		if got := count(t, dir, "rev-list", "--count", "--all"); got != c.commits {
			t.Errorf("%+v: %d commits, want %d", c, got, c.commits)
		}
		if got := count(t, dir, "rev-list", "--count", "--merges", "main"); got != c.branches {
			t.Errorf("%+v: %d merges on main, want %d", c, got, c.branches)
		}
		if got := len(strings.Fields(out(t, dir, "for-each-ref", "--format=%(refname)", "refs/heads/branch"))); got != c.branches {
			t.Errorf("%+v: %d branch refs, want %d", c, got, c.branches)
		}
		if got := len(strings.Fields(out(t, dir, "for-each-ref", "--format=%(refname)", "refs/tags"))); got != c.tags {
			t.Errorf("%+v: %d tags, want %d", c, got, c.tags)
		}
		// Every branch tip is merged into main, so the graph has real merges.
		if got := out(t, dir, "branch", "--no-merged", "main"); got != "" {
			t.Errorf("%+v: unmerged branches: %q", c, got)
		}
		if got := out(t, dir, "status", "--porcelain"); got != "" {
			t.Errorf("%+v: the work tree is not clean: %q", c, got)
		}
	}
}

// Decision: Synthetic refuses a request it cannot satisfy exactly, rather than
// returning a repository of another size.
func TestSyntheticRefusesTooFewCommits(t *testing.T) {
	t.Parallel()
	// The check is Synthetic's first act; running it needs git only for the skip.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ft := &fakeT{TB: t}
	func() {
		defer func() { _ = recover() }()
		Synthetic(ft, t.TempDir(), 9, 3, 0)
	}()
	if !ft.failed {
		t.Errorf("Synthetic(9 commits, 3 branches) did not fail")
	}
}

// fakeT records a Fatalf instead of ending the real test, by panicking out of it.
type fakeT struct {
	testing.TB
	failed bool
}

func (f *fakeT) Fatalf(string, ...any) { f.failed = true; panic("fatal") }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
