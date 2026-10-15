package git

import (
	"context"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/git/gittest"
)

// oidsOf resolves names to oids with plain git, in order.
func (e *env) oidsOf(dir string, names ...string) []string {
	e.t.Helper()
	return strings.Fields(e.g(dir, append([]string{"rev-parse"}, names...)...))
}

// dedupe keeps the first of each oid.
func dedupe(oids []string) []string {
	var out []string
	for _, o := range oids {
		if !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	return out
}

// Decision (Q1 §2): the tip set is HEAD, then local branches, then remote-tracking
// branches, then tags by newest date; every tag peeled to its commit however many tags
// deep, a tag on a tree dropped, each commit once at its first place. One function builds
// it, so git.log and git.refs agree.
func TestTipsOrderPeelingAndDedupe(t *testing.T) {
	t.Parallel()
	e := needGit(t)
	r := sharedHistory(t)
	got, truncated, err := tips(context.Background(), e.r, r.Dir, false)
	if err != nil || truncated {
		t.Fatalf("tips: %v truncated %v", err, truncated)
	}

	// Heads in ref-name order, then remote-tracking branches in ref-name order, then the
	// tags: v1.0 (annotated, the newest), v0.9 (a lightweight tag, dated by its commit);
	// v1.0-final is a tag of v1.0 and a duplicate, tree-tag points at a tree.
	want := dedupe(append(append(append([]string{r.Oids["head"]},
		e.oidsOf(r.Dir, "bugfix/crash", "chore/deps", "chore/wt", "feature/login", "feature/search/ui/results", "main")...),
		e.oidsOf(r.Dir, "origin/HEAD", "origin/bugfix/crash", "origin/chore/deps", "origin/chore/wt", "origin/feature/login",
			"origin/feature/search/ui/results", "origin/main", "origin/release/1.0")...),
		r.Oids["merge"], r.Oids["second"]))
	if !equal(got, want) {
		t.Errorf("tips:\n got %v\nwant %v", got, want)
	}
	if got[0] != r.Oids["head"] {
		t.Errorf("HEAD is not first: %v", got)
	}
	if slices.Contains(got, r.Oids["tree-tag"]) {
		t.Errorf("a tag on a tree is among the tips")
	}
	if n := len(dedupe(got)); n != len(got) {
		t.Errorf("%d tips, %d distinct", len(got), n)
	}
	// Both ends of the main branch: ahead of and behind origin.
	if !slices.Contains(got, r.Oids["origin-main"]) {
		t.Errorf("origin/main is not a tip")
	}
	// The release branch exists on origin only.
	if rel := e.oidsOf(r.Dir, "origin/release/1.0")[0]; !slices.Contains(got, rel) {
		t.Errorf("origin/release/1.0 is not a tip")
	}
}

// Decision (Q1 §2): at most 5,000 tips, HEAD and the branches kept first, and
// tipsTruncated says when any was dropped; one fewer and it does not.
func TestTipsCapped(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		tags      int
		truncated bool
	}{{4980, false}, {5100, true}} {
		t.Run(strconv.Itoa(c.tags)+" tags", func(t *testing.T) {
			t.Parallel()
			e := needGit(t)
			var dir string
			if c.tags == 5100 { // the one the other cap tests read too: shared, and only read here
				dir = sharedSynthetic(t)
			} else {
				dir = filepath.Join(t.TempDir(), "syn")
				gittest.Synthetic(t, dir, 6000, 0, c.tags)
			}
			got, truncated, err := tips(context.Background(), e.r, dir, false)
			if err != nil {
				t.Fatal(err)
			}
			if truncated != c.truncated {
				t.Errorf("truncated = %v, want %v (%d tips)", truncated, c.truncated, len(got))
			}
			if c.truncated && len(got) != maxTips {
				t.Errorf("%d tips, want the cap %d", len(got), maxTips)
			}
			if !c.truncated && len(got) <= 4980 {
				t.Errorf("only %d tips for %d distinct tagged commits", len(got), c.tags)
			}
			head := e.oidsOf(dir, "HEAD", "origin/main")
			if got[0] != head[0] || got[1] != head[1] {
				t.Errorf("HEAD and the branches are not first: %v", got[:2])
			}
			if n := len(dedupe(got)); n != len(got) {
				t.Errorf("%d tips, %d distinct", len(got), n)
			}
		})
	}
}

// Decision: a repository with no commit has no tips and no error.
func TestTipsOfAnUnbornRepository(t *testing.T) {
	t.Parallel()
	e := needGit(t)
	dir := e.repo()
	got, truncated, err := tips(context.Background(), e.r, dir, false)
	if err != nil || truncated || len(got) != 0 {
		t.Errorf("tips = %v truncated %v err %v, want none", got, truncated, err)
	}
}

// Decision (Q1 §2, §5): the builders' commands give what the later steps parse: one
// log record of six NUL-terminated fields per commit, children before their parents, a
// page being a slice of the whole; a commit's details of nine fields; its files against
// the first parent (a root commit against the empty tree), with git's own counts.
func TestBuildersAgainstARealRepository(t *testing.T) {
	t.Parallel()
	e := needGit(t)
	r := sharedHistory(t)
	ctx := context.Background()
	run := func(stdin []byte, args []string) string {
		t.Helper()
		res, err := e.r.run(ctx, r.Dir, false, stdin, args...)
		if err != nil || res.ExitCode != 0 || res.Truncated {
			t.Fatalf("%v: err %v exit %d: %s", args, err, res.ExitCode, res.Stderr)
		}
		return string(res.Stdout)
	}
	tp, _, err := tips(ctx, e.r, r.Dir, false)
	if err != nil {
		t.Fatal(err)
	}
	stdin := []byte(strings.Join(tp, "\n") + "\n")

	records := func(out string, fields int) [][]string {
		t.Helper()
		parts := strings.Split(out, "\x00")
		if parts[len(parts)-1] != "" {
			t.Fatalf("the output does not end in a NUL: %q", parts[len(parts)-1])
		}
		parts = parts[:len(parts)-1]
		if len(parts)%fields != 0 {
			t.Fatalf("%d fields, not a multiple of %d", len(parts), fields)
		}
		var out2 [][]string
		for i := 0; i < len(parts); i += fields {
			out2 = append(out2, parts[i:i+fields])
		}
		return out2
	}
	all := records(run(stdin, logArgs(0, 1000)), logFields)
	if want, _ := strconv.Atoi(strings.TrimSpace(e.g(r.Dir, append([]string{"rev-list", "--count"}, tp...)...))); len(all) != want {
		t.Errorf("%d commits in the walk, git counts %d", len(all), want)
	}
	pos := map[string]int{}
	for i, rec := range all {
		pos[rec[0]] = i
		if len(rec[0]) != 40 || rec[4] == "" || rec[5] == "" {
			t.Errorf("record %q is malformed", rec)
		}
	}
	for i, rec := range all {
		for _, p := range strings.Fields(rec[1]) {
			if pos[p] <= i {
				t.Errorf("commit %d (%s) comes after its parent %s", i, rec[0][:7], p[:7])
			}
		}
	}
	if got := all[pos[r.Oids["octopus"]]][1]; len(strings.Fields(got)) != 4 {
		t.Errorf("octopus parents = %q, want four", got)
	}
	var zoe string
	for _, rec := range all {
		if strings.HasPrefix(rec[2], "Zo") {
			zoe = rec[2]
		}
	}
	if zoe != "Zoë Ünïcode" {
		t.Errorf("author = %q, want the raw name", zoe)
	}
	var joined [][]string
	for skip := 0; skip < len(all); skip += 7 {
		joined = append(joined, records(run(stdin, logArgs(skip, 7)), logFields)...)
	}
	if len(joined) != len(all) {
		t.Fatalf("pages of 7 hold %d commits, one page %d", len(joined), len(all))
	}
	for i := range all {
		if all[i][0] != joined[i][0] {
			t.Fatalf("pages differ from the whole at commit %d", i)
		}
	}

	firstParent := func(oid string) string { return strings.Fields(all[pos[oid]][1])[0] }
	details, files := showArgs(r.Oids["merge"], firstParent(r.Oids["merge"]))
	d := records(run(nil, details), showFields)
	if len(d) != 1 || d[0][0] != r.Oids["merge"] || d[0][8] != "Merge feature/login" {
		t.Errorf("details = %q", d)
	}
	// A merge's files are against its first parent (Q1 §5): what the merge brought in, the
	// merged branch's file included; src/app.txt, which differs only between the merge and
	// the merged branch (the second-parent diff), is not there.
	if got := run(nil, files); !strings.Contains(got, "\x00merged.txt\x00") || !strings.Contains(got, "\x00src/login.txt\x00") || strings.Contains(got, "src/app.txt") {
		t.Errorf("merge files = %q, want merged.txt and src/login.txt, and no src/app.txt", got)
	}
	// A root commit is against the empty tree.
	_, files = showArgs(r.Oids["root"], "")
	if got := run(nil, files); !strings.Contains(got, "README.md") || !strings.Contains(got, "-\t-\t") {
		t.Errorf("root files = %q, want every file, the binary one with - - counts", got)
	}
	_, files = showArgs(r.Oids["rename"], firstParent(r.Oids["rename"]))
	if got := run(nil, files); !strings.Contains(got, "R100") || !strings.Contains(got, "docs/new-name.txt") {
		t.Errorf("rename files = %q, want R100", got)
	}

	wt := strings.Split(run(nil, worktreeArgs()), "\x00\x00")
	if len(wt) != 3 || wt[2] != "" { // two entries, each ended by an empty field, then the end
		t.Errorf("worktree list = %q, want two entries", wt)
	}
}

// Decision (Q1 §2): HEAD is a tip first even when detached on a commit no ref points to.
func TestTipsOfADetachedHead(t *testing.T) {
	t.Parallel()
	e := needGit(t)
	r := gittest.History(t, filepath.Join(t.TempDir(), "hist"))
	e.g(r.Dir, "checkout", "-q", "--detach", r.Oids["root"])
	got, truncated, err := tips(context.Background(), e.r, r.Dir, false)
	if err != nil || truncated {
		t.Fatalf("tips: %v truncated %v", err, truncated)
	}
	if got[0] != r.Oids["root"] {
		t.Errorf("first tip = %s, want the detached HEAD %s", got[0], r.Oids["root"])
	}
	if n := len(dedupe(got)); n != len(got) || !slices.Contains(got, r.Oids["merge"]) || !slices.Contains(got, e.oidsOf(r.Dir, "main")[0]) {
		t.Errorf("tips lose the branches or repeat: %v", got)
	}
}

// Decision (Q1 §2, "git.show lists only the workspace's files"): the "." pathspec, from
// the root git runs in, keeps a subdirectory workspace to its own files.
func TestShowFilesFromASubdirectory(t *testing.T) {
	t.Parallel()
	e := needGit(t)
	r := sharedHistory(t)
	_, files := showArgs(r.Oids["octopus"], r.Oids["merge"])
	list := func(root string) string {
		t.Helper()
		res, err := e.r.run(context.Background(), root, false, nil, files...)
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("%s: err %v exit %d: %s", root, err, res.ExitCode, res.Stderr)
		}
		return string(res.Stdout)
	}
	top := list(r.Dir)
	for _, f := range []string{"octopus.txt", "deps.txt", "src/crash.txt", "src/search.txt"} {
		if !strings.Contains(top, "\x00"+f+"\x00") {
			t.Errorf("the top workspace lacks %s: %q", f, top)
		}
	}
	sub := list(filepath.Join(r.Dir, "src"))
	for _, f := range []string{"src/crash.txt", "src/search.txt"} {
		if !strings.Contains(sub, "\x00"+f+"\x00") {
			t.Errorf("the src workspace lacks %s (paths are the repository's, not the workspace's): %q", f, sub)
		}
	}
	for _, f := range []string{"octopus.txt", "deps.txt"} {
		if strings.Contains(sub, f) {
			t.Errorf("the src workspace lists %s, a file outside it: %q", f, sub)
		}
	}
}
