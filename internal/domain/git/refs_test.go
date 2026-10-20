package git

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/git/gittest"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// refsPage calls git.refs as the UI would.
func (f *readFx) refsPage() (RefsResult, *capability.Error) {
	f.t.Helper()
	d, ok := f.reg.Lookup("git.refs")
	if !ok {
		f.t.Fatal("git.refs is not registered")
	}
	res, err := d.Handler(context.Background(), &capability.Call{Workspace: "ws", Params: RefsParams{}})
	if err != nil {
		return RefsResult{}, capability.AsError(err)
	}
	return res.(RefsResult), nil
}

// mustRefs is refsPage that fails the test on an error.
func (f *readFx) mustRefs() RefsResult {
	f.t.Helper()
	res, err := f.refsPage()
	if err != nil {
		f.t.Fatalf("git.refs: %s (%s; cause %q)", err.Code, err.Message, err.Cause)
	}
	return res
}

// lines is the non-empty lines of plain git's output.
func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// createRefs makes refs of the given full names, all pointing at oid, in one git.
func (e *env) createRefs(dir, oid string, refs []string) {
	e.t.Helper()
	var b strings.Builder
	for _, r := range refs {
		b.WriteString("create " + r + " " + oid + "\n")
	}
	e.gStdin(dir, b.String(), "update-ref", "--stdin")
}

// createPackedRefs makes refs as createRefs does, but by appending them to packed-refs, the
// file git itself writes (the header's "sorted" is dropped, as TestRefsAwkwardNames does,
// so git sorts them on reading). For the tens of thousands a cap test needs: as loose
// files they cost seconds to write and to read, and git.refs reading them took most of
// its 10 s limit once other tests shared the disk. Refs git lists are the same.
func (e *env) createPackedRefs(dir, oid string, refs []string) {
	e.t.Helper()
	e.g(dir, "pack-refs", "--all")
	packed := filepath.Join(dir, ".git", "packed-refs")
	raw, err := os.ReadFile(packed)
	if err != nil && !os.IsNotExist(err) {
		e.t.Fatal(err)
	}
	var b strings.Builder
	if len(raw) == 0 {
		b.WriteString("# pack-refs with: peeled fully-peeled \n")
	} else {
		b.WriteString(strings.Replace(string(raw), " sorted", "", 1))
	}
	for _, r := range refs {
		b.WriteString(oid + " " + r + "\n")
	}
	if err := os.WriteFile(packed, []byte(b.String()), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// Decision (Q1 §1, §2): git.refs lists what git lists, whole and as plain git names it:
// HEAD with the current branch's upstream and its ahead and behind (counted by git, here
// one commit each way), local branches with their folders, remote-tracking branches split
// into remote and name, tags peeled to a commit however many tags deep (a tag on a tree
// has none), the work trees with the one that holds the workspace marked, and the tip set
// git.log starts from. A detached HEAD has a commit and no branch, upstream or counts.
func TestRefsOfARealRepository(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	r := gittest.History(t, filepath.Join(t.TempDir(), "hist"))
	f.at(r.Dir)

	res := f.mustRefs()

	// HEAD: main, one ahead and one behind origin/main.
	counts := strings.Fields(f.g(r.Dir, "rev-list", "--left-right", "--count", "main...origin/main"))
	if !equal(counts, []string{"1", "1"}) {
		t.Fatalf("fixture: git counts main...origin/main as %v, want 1 1", counts)
	}
	wantHead := RefsHead{Oid: r.Oids["head"], Branch: "main", Upstream: "origin/main", Ahead: 1, Behind: 1}
	if res.Head != wantHead {
		t.Errorf("head = %+v; want %+v", res.Head, wantHead)
	}

	// Local branches: git's names (folders and all) in git's order; only main has an upstream.
	names := lines(f.g(r.Dir, "for-each-ref", "--format=%(refname:strip=2) %(objectname)", "refs/heads"))
	var gotBranches []string
	for _, b := range res.Branches {
		gotBranches = append(gotBranches, b.Name+" "+b.Oid)
		if (b.Upstream != "") != (b.Name == "main") || (b.Name == "main" && b.Upstream != "origin/main") {
			t.Errorf("branch %s has upstream %q", b.Name, b.Upstream)
		}
	}
	if !equal(gotBranches, names) || len(names) != 6 {
		t.Errorf("branches:\n got %v\nwant %v (six, folders included)", gotBranches, names)
	}
	if !slices.Contains(gotBranches, "feature/search/ui/results "+f.oidsOf(r.Dir, "feature/search/ui/results")[0]) {
		t.Errorf("the three-folder-deep branch is missing: %v", gotBranches)
	}

	// Remote-tracking: origin's, split, the branch only origin has included, and the
	// default-branch pointer as git lists it.
	var wantRemotes, gotRemotes []string
	for _, l := range lines(f.g(r.Dir, "for-each-ref", "--format=%(refname:strip=2) %(objectname)", "refs/remotes")) {
		rest, _ := strings.CutPrefix(l, "origin/")
		wantRemotes = append(wantRemotes, "origin "+rest)
	}
	for _, x := range res.Remotes {
		gotRemotes = append(gotRemotes, x.Remote+" "+x.Name+" "+x.Oid)
	}
	if !equal(gotRemotes, wantRemotes) || len(wantRemotes) != 8 {
		t.Errorf("remotes:\n got %v\nwant %v", gotRemotes, wantRemotes)
	}
	if !slices.Contains(gotRemotes, "origin release/1.0 "+f.oidsOf(r.Dir, "origin/release/1.0")[0]) {
		t.Errorf("the branch only origin has is missing: %v", gotRemotes)
	}

	// Tags: annotated, lightweight, a tag of a tag (all a commit), a tag on a tree (none).
	wantTags := map[string]string{"v1.0": r.Oids["merge"], "v0.9": r.Oids["second"], "v1.0-final": r.Oids["merge"], "tree-tag": ""}
	if len(res.Tags) != len(wantTags) {
		t.Errorf("%d tags, want %d: %+v", len(res.Tags), len(wantTags), res.Tags)
	}
	for _, tg := range res.Tags {
		if want, ok := wantTags[tg.Name]; !ok || tg.Oid != want {
			t.Errorf("tag %s = %q; want %q", tg.Name, tg.Oid, want)
		}
	}
	if b, _ := json.Marshal(RefsTag{Name: "tree-tag"}); string(b) != `{"name":"tree-tag"}` {
		t.Errorf("a tag with no commit as JSON = %s", b)
	}
	// Newest first, by git's own creation date.
	dates := map[string]int64{}
	for _, l := range lines(f.g(r.Dir, "for-each-ref", "--format=%(refname:strip=2) %(creatordate:unix)", "refs/tags")) {
		name, d, _ := strings.Cut(l, " ")
		dates[name], _ = strconv.ParseInt(d, 10, 64)
	}
	for i := 1; i < len(res.Tags); i++ {
		a, b := res.Tags[i-1], res.Tags[i]
		if dates[a.Name] < dates[b.Name] || (dates[a.Name] == dates[b.Name] && a.Name > b.Name) {
			t.Errorf("tags %s then %s are not newest first, by name within a date", a.Name, b.Name)
		}
	}

	// Work trees: the main one first and current, the linked one on chore/wt.
	want := []RefsWorktree{
		{Path: f.platform.Canonical(r.Dir), Branch: "main", Oid: r.Oids["head"], Current: true},
		{Path: f.platform.Canonical(r.Linked), Branch: "chore/wt", Oid: f.oidsOf(r.Dir, "chore/wt")[0]},
	}
	if len(res.Worktrees) != 2 {
		t.Fatalf("worktrees = %+v; want two", res.Worktrees)
	}
	for i, w := range res.Worktrees {
		w.Path = f.platform.Canonical(w.Path)
		if w != want[i] {
			t.Errorf("worktree %d = %+v; want %+v", i, w, want[i])
		}
	}

	// The tips are the shared function's, HEAD first.
	tp, truncated, err := tips(context.Background(), f.r, r.Dir, false)
	if err != nil || truncated {
		t.Fatalf("tips: %v truncated %v", err, truncated)
	}
	if !equal(res.Tips, tp) || res.TipsTruncated || res.Tips[0] != r.Oids["head"] {
		t.Errorf("tips = %v truncated %v; want %v", res.Tips, res.TipsTruncated, tp)
	}
	if res.BranchesTruncated || res.RemotesTruncated || res.TagsTruncated || res.WorktreesTruncated {
		t.Errorf("a list says it is truncated: %+v", res)
	}

	// The same repository from its linked work tree: it is the current one, on its branch.
	f.at(r.Linked)
	lres := f.mustRefs()
	if lres.Head.Branch != "chore/wt" || lres.Head.Upstream != "" || lres.Head.Ahead != 0 || lres.Head.Behind != 0 {
		t.Errorf("head from the linked work tree = %+v", lres.Head)
	}
	if len(lres.Worktrees) != 2 || lres.Worktrees[0].Current || !lres.Worktrees[1].Current {
		t.Errorf("worktrees from the linked work tree = %+v; want only the linked one current", lres.Worktrees)
	}

	// A workspace below the repository's top lists the whole repository's refs.
	f.at(filepath.Join(r.Dir, "src"))
	sres := f.mustRefs()
	if len(sres.Branches) != 6 || len(sres.Tags) != 4 || !sres.Worktrees[0].Current || sres.Head.Ahead != 1 {
		t.Errorf("from a subfolder: %d branches, %d tags, worktrees %+v, head %+v", len(sres.Branches), len(sres.Tags), sres.Worktrees, sres.Head)
	}

	// A detached HEAD: a commit, no branch, no upstream, no counts.
	f.at(r.Dir)
	f.g(r.Dir, "checkout", "-q", "--detach", r.Oids["root"])
	dres := f.mustRefs()
	if dres.Head != (RefsHead{Oid: r.Oids["root"], Detached: true}) {
		t.Errorf("detached head = %+v; want the root commit and nothing else", dres.Head)
	}
	if dres.Tips[0] != r.Oids["root"] || !dres.Worktrees[0].Current || dres.Worktrees[0].Branch != "" || dres.Worktrees[0].Oid != r.Oids["root"] {
		t.Errorf("detached: first tip %s, worktree %+v", dres.Tips[0], dres.Worktrees[0])
	}
}

// Decision (Q1 §1: "ahead and behind for the current branch only, against its upstream"):
// an upstream that is a local branch counts too, and the upstream of a branch the
// repository no longer has is named and counts zero, not an error.
func TestRefsUpstreamKinds(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	r := gittest.History(t, filepath.Join(t.TempDir(), "hist"))
	f.at(r.Dir)

	f.g(r.Dir, "checkout", "-q", "-b", "topic", r.Oids["root"])
	f.g(r.Dir, "branch", "-q", "--set-upstream-to=main", "topic")
	want := strings.Fields(f.g(r.Dir, "rev-list", "--left-right", "--count", "topic...main"))
	res := f.mustRefs()
	got := []string{strconv.Itoa(res.Head.Ahead), strconv.Itoa(res.Head.Behind)}
	if res.Head.Upstream != "main" || !equal(got, want) || res.Head.Ahead != 0 || res.Head.Behind == 0 || res.Head.UpstreamGone {
		t.Errorf("a local upstream: head = %+v; git counts %v", res.Head, want)
	}

	f.g(r.Dir, "config", "branch.topic.remote", "origin")
	f.g(r.Dir, "config", "branch.topic.merge", "refs/heads/gone")
	res = f.mustRefs()
	if res.Head.Upstream != "origin/gone" || res.Head.Ahead != 0 || res.Head.Behind != 0 {
		t.Errorf("a vanished upstream: head = %+v; want it named and 0, 0", res.Head)
	}
	// upstreamGone tells a vanished upstream from one that is merely level with the branch
	// (also 0, 0): HEAD's says so, and so does the branch's own entry; a branch whose
	// upstream is listed does not.
	if !res.Head.UpstreamGone {
		t.Errorf("a vanished upstream: head.upstreamGone is false")
	}
	for _, b := range res.Branches {
		if gone := b.Name == "topic"; b.UpstreamGone != gone {
			t.Errorf("branch %s: upstreamGone = %v, want %v", b.Name, b.UpstreamGone, gone)
		}
	}
	if b, _ := json.Marshal(RefsBranch{Name: "x", Oid: "a", Upstream: "origin/x"}); strings.Contains(string(b), "upstreamGone") {
		t.Errorf("a branch whose upstream is there carries upstreamGone: %s", b)
	}
}

// Decision (Q1 §1): a repository with no commit has a branch HEAD would be on, no commit,
// no refs and no tips, as lists ([] and not null), and no error.
func TestRefsUnborn(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	res := f.mustRefs()
	b, _ := json.Marshal(res)
	want := `{"head":{"branch":"main","detached":false,"upstreamGone":false,"ahead":0,"behind":0},"branches":[],"branchesTruncated":false,"remotes":[],"remotesTruncated":false,"tags":[],"tagsTruncated":false,"worktrees":[{"path":"` + f.platform.Canonical(f.root) + `","branch":"main","current":true}],"worktreesTruncated":false,"tips":[],"tipsTruncated":false}`
	if string(b) != want {
		t.Errorf("an unborn repository:\n got %s\nwant %s", b, want)
	}
}

// Decision (ADR-0066 §4): a root in no repository is GIT_NOT_A_REPOSITORY; no git on the
// machine is GIT_NOT_FOUND with a next step, before any process starts.
func TestRefsNotARepository(t *testing.T) {
	t.Parallel()
	f, r := newHistoryFx(t)
	f.at(t.TempDir())
	_, err := f.refsPage()
	wantCode(t, err, CodeNotARepository)

	f.at(r.Dir)
	before := f.starts.Load()
	f.look = func(string) (string, error) { return "", exec.ErrNotFound }
	_, err = f.refsPage()
	wantCode(t, err, CodeNotFound)
	if err.NextStep == "" || f.starts.Load() != before {
		t.Errorf("GIT_NOT_FOUND: next step %q, %d starts", err.NextStep, f.starts.Load()-before)
	}
}

// Decision (Q1 §2, ADR-0069 §2: "one function builds the tip set for git.log and
// git.refs"): on a repository with more refs than the cap and annotated tags among them,
// git.refs' tips are exactly git.log's first-page tips, and exactly the set an
// independent reading of the refs gives: HEAD, branches, remote-tracking branches, then
// tags by newest date, every tag peeled to its commit, 5,000 at most. The independent
// oracle is what fails when both capabilities stop peeling together; the comparison of the
// two is what fails when only one of them is changed.
func TestRefsTipsEqualLogTips(t *testing.T) {
	t.Parallel()
	needGit(t)
	f := newReadFxOn(t, sharedSynthetic(t)) // every tenth tag annotated
	dir := f.root

	refs := f.mustRefs()
	log := f.mustLog(LogParams{Limit: ip(10)})
	// Not fatal: when the tips stop being peeled (fewer distinct ids than refs) this guard
	// fires first, and the independent comparison below must report as well.
	if !log.TipsTruncated || len(log.Tips) != maxTips {
		t.Errorf("git.log's first page has %d tips, truncated %v; want the cap of %d reached: the fixture is short of tips, or the tip set lost entries (peeling?)", len(log.Tips), log.TipsTruncated, maxTips)
	}
	if !refs.TipsTruncated || len(refs.Tips) != maxTips {
		t.Errorf("git.refs: %d tips, truncated %v; want %d, true", len(refs.Tips), refs.TipsTruncated, maxTips)
	}
	if !equal(refs.Tips, log.Tips) {
		t.Errorf("git.refs' tips differ from git.log's first page's (%d and %d)", len(refs.Tips), len(log.Tips))
	}

	var want []string
	want = append(want, strings.TrimSpace(f.g(dir, "rev-parse", "HEAD")))
	for _, ns := range []string{"refs/heads", "refs/remotes"} {
		for _, l := range lines(f.g(dir, "for-each-ref", "--format=%(objectname)", ns)) {
			want = append(want, l)
		}
	}
	type tg struct {
		when int64
		name string
		oid  string
	}
	var tags []tg
	annotated := 0
	for _, l := range lines(f.g(dir, "for-each-ref", "--format=%(creatordate:unix) %(refname) %(objecttype) %(objectname) %(*objectname)", "refs/tags")) {
		p := strings.Fields(l)
		when, _ := strconv.ParseInt(p[0], 10, 64)
		oid := p[3]
		if p[2] == "tag" {
			oid = p[4]
			annotated++
		}
		tags = append(tags, tg{when, p[1], oid})
	}
	if annotated < 500 {
		t.Fatalf("fixture: %d annotated tags, want hundreds", annotated)
	}
	sort.SliceStable(tags, func(i, j int) bool {
		if tags[i].when != tags[j].when {
			return tags[i].when > tags[j].when
		}
		return tags[i].name < tags[j].name
	})
	for _, x := range tags {
		want = append(want, x.oid)
	}
	want = dedupe(want)[:maxTips]
	if !equal(refs.Tips, want) {
		t.Errorf("git.refs' tips are not the independently built set (peeled tags, newest first, capped)")
	}
}

// Decision (Q1 §1): ref names are project content that can hold anything, returned as they
// are and never on argv. Unicode and a deep folder come back whole; a name git allows but
// that looks like an option (--output=..., -x) is only a name: it is listed, no file is
// written, and no ref name nor object id appears on any git's command line. A name with
// bytes that are not UTF-8 comes back as valid UTF-8 (one U+FFFD per run), so the JSON the
// browser parses is never an error.
func TestRefsAwkwardNames(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	r := gittest.History(t, filepath.Join(t.TempDir(), "hist"))
	f.at(r.Dir)
	deep := "feature/ünï-ツ/ふかい/zz-marker-deep/a/b/c/d/e"
	f.env.createRefs(r.Dir, r.Oids["head"], []string{
		"refs/heads/" + deep, "refs/heads/--output=pwned", "refs/heads/-x",
		"refs/tags/zz-marker-tag/ünï", "refs/remotes/origin/ünï/zz-marker-remote", "refs/remotes/we-ird/zz-marker-b",
	})
	// An invalid UTF-8 name cannot be a loose ref on every file system, so it is packed.
	f.g(r.Dir, "pack-refs", "--all")
	packed := filepath.Join(r.Dir, ".git", "packed-refs")
	raw, err := os.ReadFile(packed)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), " sorted", "", 1) + r.Oids["head"] + " refs/heads/zz-marker-bad\xff\xfename\n"
	if err := os.WriteFile(packed, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	// A git that does not read the invalid-UTF-8 ref skips that assertion only: the argv
	// and option checks do not depend on it.
	badReadable := true
	if out := f.g(r.Dir, "for-each-ref", "refs/heads/zz-marker-bad*"); !strings.Contains(out, "zz-marker-bad") {
		badReadable = false
		t.Logf("this git does not read a ref name of invalid UTF-8: %q; the U+FFFD assertion is skipped", out)
	}

	var mu sync.Mutex
	var argvs [][]string
	f.onStart = func(cmd *exec.Cmd) {
		mu.Lock()
		argvs = append(argvs, slices.Clone(cmd.Args))
		mu.Unlock()
	}
	res := f.mustRefs()

	names := map[string]bool{}
	for _, b := range res.Branches {
		names[b.Name] = true
	}
	wantNames := []string{deep, "--output=pwned", "-x"}
	if badReadable {
		wantNames = append(wantNames, "zz-marker-bad�name")
	}
	for _, want := range wantNames {
		if !names[want] {
			t.Errorf("branch %q is not listed", want)
		}
	}
	var remotes []string
	for _, x := range res.Remotes {
		remotes = append(remotes, x.Remote+"|"+x.Name)
	}
	for _, want := range []string{"origin|ünï/zz-marker-remote", "we-ird|zz-marker-b"} {
		if !slices.Contains(remotes, want) {
			t.Errorf("remote-tracking branch %q is not listed: %v", want, remotes)
		}
	}
	if !slices.ContainsFunc(res.Tags, func(x RefsTag) bool { return x.Name == "zz-marker-tag/ünï" && x.Oid == r.Oids["head"] }) {
		t.Errorf("the Unicode tag is not listed: %+v", res.Tags)
	}
	b, _ := json.Marshal(res)
	if !utf8.Valid(b) || !json.Valid(b) {
		t.Errorf("the result is not valid JSON of valid UTF-8")
	}
	if exists(filepath.Join(r.Dir, "pwned")) {
		t.Errorf("a branch named --output=pwned wrote a file: a ref name reached an option")
	}

	hexOid := regexp.MustCompile(`[0-9a-f]{40}`)
	if len(argvs) < 5 {
		t.Fatalf("only %d gits ran; the search below proves nothing", len(argvs))
	}
	for _, argv := range argvs {
		for _, a := range argv {
			if strings.Contains(a, "zz-marker") || strings.Contains(a, "pwned") || hexOid.MatchString(a) {
				t.Errorf("a ref name or object id on a git's argv: %q", argv)
			}
		}
	}
}

// Decision (review of step 5: a remote's name can hold a slash): a remote-tracking ref is
// split at the end of the longest configured remote name that prefixes it, not at the first
// slash, so the refs of a remote team/origin are not credited to a remote team; a ref no
// configured remote claims falls back to the first slash. Splitting at the first slash
// would make team/origin's branch x the branch origin/x of team.
func TestRefsRemoteNames(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	f.write(f.root, "a.txt", "a\n")
	f.commit(f.root)
	head := strings.TrimSpace(f.g(f.root, "rev-parse", "HEAD"))
	f.g(f.root, "remote", "add", "team", "https://example.invalid/team.git")
	// git refuses to add a remote named inside another's; the configuration allows it.
	f.g(f.root, "config", "remote.team/origin.url", "https://example.invalid/origin.git")
	f.env.createRefs(f.root, head, []string{
		"refs/remotes/team/main", "refs/remotes/team/dev/y",
		"refs/remotes/team/origin/x", "refs/remotes/team/origin/feat/z",
		"refs/remotes/stray/q/r",
	})

	var got []string
	for _, x := range f.mustRefs().Remotes {
		got = append(got, x.Remote+"|"+x.Name)
	}
	want := []string{"stray|q/r", "team/origin|feat/z", "team/origin|x", "team|dev/y", "team|main"}
	sort.Strings(got)
	if !equal(got, want) {
		t.Errorf("remote-tracking branches:\n got %v\nwant %v", got, want)
	}
}

// Decision (review of step 5, ADR-0069 §2: HEAD and the refs are read once and the lists
// and the tips are built from the same output): one git.refs call runs one rev-parse of
// HEAD and one for-each-ref. A second read of either could disagree with the first when a
// ref moves in between.
func TestRefsReadsOnce(t *testing.T) {
	t.Parallel()
	f, _ := newHistoryFx(t)
	var mu sync.Mutex
	var heads, eachRefs int
	f.onStart = func(cmd *exec.Cmd) {
		mu.Lock()
		defer mu.Unlock()
		if slices.Contains(cmd.Args, "for-each-ref") {
			eachRefs++
		}
		if slices.Contains(cmd.Args, "HEAD^{commit}") {
			heads++
		}
	}
	f.mustRefs()
	if heads != 1 || eachRefs != 1 {
		t.Errorf("one git.refs ran rev-parse HEAD %d times and for-each-ref %d times; want once each", heads, eachRefs)
	}
}

// fillRefs gives dir n more branches and n more remote-tracking branches, all at HEAD.
func (e *env) fillRefs(dir string, n int) {
	e.t.Helper()
	head := strings.TrimSpace(e.g(dir, "rev-parse", "HEAD"))
	var heads, remotes []string
	for i := 0; i < n; i++ {
		heads = append(heads, fmt.Sprintf("refs/heads/b%05d", i))
		remotes = append(remotes, fmt.Sprintf("refs/remotes/origin/r%05d", i))
	}
	e.createPackedRefs(dir, head, heads)
	e.createPackedRefs(dir, head, remotes)
}

// Decision (Q1 §1, ADR-0069 §2): each list holds at most 5,000, with its own truncated
// flag, and exactly 5,000 is not truncated (that boundary: TestParseRefsCaps, on synthetic
// output; this test is git's real output over the cap). What drops out is the tail: the alphabetical
// end of the branches and of the remote-tracking branches, the oldest tags (the order
// git.log's tips drop them in). The flags are independent: one over the cap does not mark
// the others.
func TestRefsCaps(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	dir := filepath.Join(t.TempDir(), "syn")
	gittest.Synthetic(t, dir, 6000, 0, 5001)
	f.at(dir)
	// main and origin/main exist already: 5,000 more of each make 5,001.
	f.env.fillRefs(dir, 5000)

	res := f.mustRefs()
	if len(res.Branches) != maxRefList || !res.BranchesTruncated || len(res.Remotes) != maxRefList || !res.RemotesTruncated || len(res.Tags) != maxRefList || !res.TagsTruncated || res.WorktreesTruncated {
		t.Fatalf("%d branches (%v), %d remotes (%v), %d tags (%v), worktrees truncated %v; want 5000 each, the three lists truncated",
			len(res.Branches), res.BranchesTruncated, len(res.Remotes), res.RemotesTruncated, len(res.Tags), res.TagsTruncated, res.WorktreesTruncated)
	}
	heads := lines(f.g(dir, "for-each-ref", "--format=%(refname:strip=2)", "refs/heads"))
	for i, b := range res.Branches {
		if b.Name != heads[i] {
			t.Fatalf("branch %d = %q; want %q (the first 5000 by name)", i, b.Name, heads[i])
		}
	}
	remotes := lines(f.g(dir, "for-each-ref", "--format=%(refname:strip=3)", "refs/remotes"))
	for i, x := range res.Remotes {
		if x.Remote != "origin" || x.Name != remotes[i] {
			t.Fatalf("remote %d = %+v; want origin %q", i, x, remotes[i])
		}
	}
	oldest := lines(f.g(dir, "for-each-ref", "--sort=creatordate", "--format=%(refname:strip=2)", "refs/tags"))[0]
	for _, tg := range res.Tags {
		if tg.Name == oldest {
			t.Errorf("the oldest tag %s is among the 5000 kept", oldest)
		}
	}
	if res.Tags[0].Name != lines(f.g(dir, "for-each-ref", "--sort=-creatordate", "--format=%(refname:strip=2)", "refs/tags"))[0] {
		t.Errorf("the newest tag is not first: %s", res.Tags[0].Name)
	}

	// That exactly 5,000 in a list is whole and not truncated is a boundary of Go code, held
	// by TestParseRefsCaps on synthetic output; here is git's own output meeting it.
}

// Decision (ADR-0066 §2, ADR-0069 §Consequences): when git's own output passes the 4 MiB
// cap, for-each-ref has been cut in ref-name order, so the tags (last) are marked
// truncated, the lists before them are whole and not marked, the cut line is never half
// read, and the tips say so too.
//
// The claim is the cap and its marks, not git's speed: writing 30,000 long refs takes git
// seconds when the machine is loaded, and the product's 10 s limit (kept as it is) would end
// the call before the output passed the cap. The fixture's limit is a hang guard, 60 s
// scaled; the cap still has to be met, since every assertion below is about the cut output.
func TestRefsOutputCap(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	f.readTimeout = testtime.Bound(60 * time.Second)
	f.register()
	f.write(f.root, "a.txt", "a\n")
	f.commit(f.root)
	head := strings.TrimSpace(f.g(f.root, "rev-parse", "HEAD"))
	long := strings.Repeat("n", 100)
	var tags []string
	for i := 0; i < 30000; i++ {
		tags = append(tags, fmt.Sprintf("refs/tags/%s%05d", long, i))
	}
	f.env.createPackedRefs(f.root, head, tags)

	res := f.mustRefs()
	if !res.TagsTruncated || len(res.Tags) != maxRefList || res.BranchesTruncated || res.RemotesTruncated || !res.TipsTruncated {
		t.Errorf("%d tags (truncated %v), branches truncated %v, remotes truncated %v, tips truncated %v; want the tags and the tips marked",
			len(res.Tags), res.TagsTruncated, res.BranchesTruncated, res.RemotesTruncated, res.TipsTruncated)
	}
	if len(res.Branches) != 1 || res.Branches[0].Name != "main" || res.Head.Oid != head {
		t.Errorf("the lists before the cut are not whole: %+v, head %+v", res.Branches, res.Head)
	}
	for _, tg := range res.Tags {
		if len(tg.Name) != len(long)+5 || tg.Oid != head {
			t.Fatalf("a cut or damaged tag: %q %q", tg.Name, tg.Oid)
		}
	}
}

// Decision (Q1 §1): the work-tree list is read from git's -z porcelain, whole entries only:
// a bare repository's entry is not a work tree, an all-zero HEAD is no commit, and an
// entry the output was cut in (never terminated) is not read. The cap is the lists' cap.
func TestParseWorktrees(t *testing.T) {
	t.Parallel()
	const z = "0000000000000000000000000000000000000000"
	oid := strings.Repeat("a", 40)
	out := "worktree /r\x00HEAD " + oid + "\x00branch refs/heads/main\x00\x00" +
		"worktree /r.git\x00bare\x00\x00" +
		"worktree /w ü\xff\x00HEAD " + z + "\x00branch refs/heads/new\x00\x00" +
		"worktree /d\x00HEAD " + oid + "\x00detached\x00locked why\x00\x00" +
		"worktree /cut\x00HEAD " + oid
	got := parseWorktrees([]byte(out), func(p string) bool { return p == "/d" })
	want := []RefsWorktree{
		{Path: "/r", Branch: "main", Oid: oid},
		{Path: "/w ü�", Branch: "new"},
		{Path: "/d", Oid: oid, Current: true},
	}
	if len(got) != len(want) {
		t.Fatalf("worktrees = %+v; want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("worktree %d = %+v; want %+v", i, got[i], want[i])
		}
	}

	var many strings.Builder
	for i := 0; i < maxRefList+1; i++ {
		many.WriteString("worktree /w" + strconv.Itoa(i) + "\x00HEAD " + oid + "\x00detached\x00\x00")
	}
	capped, truncated := capList(parseWorktrees([]byte(many.String()), func(string) bool { return false }), false)
	if len(capped) != maxRefList || !truncated || capped[maxRefList-1].Path != "/w"+strconv.Itoa(maxRefList-1) {
		t.Errorf("5001 work trees: %d kept, truncated %v", len(capped), truncated)
	}
	capped, truncated = capList(capped, false)
	if len(capped) != maxRefList || truncated {
		t.Errorf("5000 work trees: %d kept, truncated %v; want all", len(capped), truncated)
	}
}

// Decision (ADR-0066 §3, ADR-0069 §3, Q1 §3): through the capability, on an untrusted
// root, what the repository configures to make git run something or show another history
// does not happen, and the counts are the repository's own. Controls prove each attack
// works against plain git: a signature program, an fsmonitor hook and an external diff are
// run by it, a replace ref or a graft changes the ahead and behind it counts.
func TestRefsFence(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (*readFx, string) {
		f := newReadFx(t)
		e := f.env
		dir, head := e.historyRepo("")
		f.at(dir)
		f.trusted = func(string) (bool, *capability.Error) { return false, nil }
		// main is one commit ahead of side, its upstream.
		e.g(dir, "branch", "-f", "side", "HEAD~1")
		e.g(dir, "branch", "-q", "--set-upstream-to=side", "main")
		return f, head
	}
	wantCounts := func(t *testing.T, f *readFx, head string) {
		t.Helper()
		res := f.mustRefs()
		if res.Head.Oid != head || res.Head.Upstream != "side" || res.Head.Ahead != 1 || res.Head.Behind != 0 {
			t.Errorf("head = %+v; want %s, side, 1 ahead, 0 behind", res.Head, head)
		}
	}
	plainCounts := func(f *readFx) string {
		out := f.plainRun(f.root, nil, "-c", "advice.graftFileDeprecated=false", "rev-list", "--left-right", "--count", "main...side")
		return strings.Join(strings.Fields(out), " ")
	}

	t.Run("programs the repository configures", func(t *testing.T) {
		t.Parallel()
		f, head := setup(t)
		e := f.env
		gpg, fsm, ext := filepath.Join(t.TempDir(), "gpg"), filepath.Join(t.TempDir(), "fsm"), filepath.Join(t.TempDir(), "ext")
		e.signedHead(f.root, "PGP SIGNATURE")
		head = strings.TrimSpace(e.g(f.root, "rev-parse", "HEAD"))
		e.g(f.root, "branch", "-f", "side", "HEAD~1")
		e.appendConfig(f.root, "[log]\n\tshowSignature = true\n[gpg]\n\tprogram = "+e.script("gpg.sh", "touch '"+gpg+"'\ncat")+"\n"+
			"[core]\n\tfsmonitor = "+e.script("fsm.sh", "touch '"+fsm+"'")+"\n"+
			"[diff]\n\texternal = "+e.script("ext.sh", "touch '"+ext+"'")+"\n")
		e.write(f.root, "a.txt", "dirty\n") // a status or diff has work to do

		e.g(f.root, "log", "-1")
		e.g(f.root, "status", "--porcelain")
		e.g(f.root, "diff")
		for name, mark := range map[string]string{"gpg.program": gpg, "core.fsmonitor": fsm, "diff.external": ext} {
			if !exists(mark) {
				t.Fatalf("control: plain git did not run %s; the check below proves nothing", name)
			}
			os.Remove(mark)
		}
		wantCounts(t, f, head)
		for name, mark := range map[string]string{"gpg.program": gpg, "core.fsmonitor": fsm, "diff.external": ext} {
			if exists(mark) {
				t.Errorf("git.refs ran the repository's %s", name)
			}
		}
	})

	t.Run("a replace ref giving HEAD another history", func(t *testing.T) {
		t.Parallel()
		f, head := setup(t)
		e := f.env
		e.g(f.root, "update-ref", "refs/replace/"+head, e.forgedCommit(f.root, ""))
		if got := plainCounts(f); got != "1 1" {
			t.Fatalf("control: plain git counts %q, want 1 1 (the replacement has no parent)", got)
		}
		wantCounts(t, f, head)
	})

	t.Run("a graft giving HEAD no parent", func(t *testing.T) {
		t.Parallel()
		f, head := setup(t)
		f.write(f.root, ".git/info/grafts", head+"\n")
		if got := plainCounts(f); got != "1 1" {
			t.Fatalf("control: plain git counts %q, want 1 1 (the graft cuts the history)", got)
		}
		wantCounts(t, f, head)
	})
}

// Decision (ADR-0069 §1, ADR-0060 §2, ADR-0020 §Retention): git.refs is a stateless
// workspace query for the human UI (audience human: the core refuses it over MCP), that
// needs git, whose codes are listed (REQUEST_INVALID_PARAMS since plan m2n P6 gave it the
// optional root), and that takes no parameter but that root. Reversing the audience
// would list it as an agent tool.
func TestRefsDescriptor(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	d, ok := f.reg.Lookup("git.refs")
	if !ok {
		t.Fatal("git.refs is not registered")
	}
	if d.Kind != capability.Query || d.Scope != capability.Workspace || d.Statefulness != capability.Stateless || d.Audience != capability.AudienceHuman || d.Since != "0.24.0" {
		t.Errorf("kind %v scope %v statefulness %v audience %v since %s", d.Kind, d.Scope, d.Statefulness, d.Audience, d.Since)
	}
	if len(d.Availability) != 1 || d.Availability[0] != capability.RequiresGit {
		t.Errorf("availability %v; want git", d.Availability)
	}
	for _, code := range []capability.ErrorCode{CodeNotFound, CodeNotARepository, CodeFailed, capability.ErrWorkspaceUntrusted, capability.ErrInvalidParams} {
		if !slices.Contains(d.Errors, code) {
			t.Errorf("the descriptor does not list %s, which the handler returns", code)
		}
	}
	if len(d.Errors) != 5 {
		t.Errorf("errors %v; want the five the handler returns", d.Errors)
	}
}

// Decision (ADR-0020 §Retention): a result returned again after delivery holds none of
// the lists and no tips, so retention never keeps the repository's refs; HEAD and the
// flags stay, and the lists as [] not null.
func TestRefsSlim(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	d, _ := f.reg.Lookup("git.refs")
	full := RefsResult{
		Head:               RefsHead{Oid: "a", Branch: "main", Upstream: "origin/main", Ahead: 2, Behind: 3},
		Branches:           []RefsBranch{{Name: "main", Oid: "a"}},
		BranchesTruncated:  true,
		Remotes:            []RefsRemote{{Remote: "origin", Name: "main", Oid: "a"}},
		Tags:               []RefsTag{{Name: "v1", Oid: "a"}},
		TagsTruncated:      true,
		Worktrees:          []RefsWorktree{{Path: "/r", Current: true}},
		WorktreesTruncated: true,
		Tips:               []string{"a", "b"},
		TipsTruncated:      true,
	}
	got, ok := d.Slim(full).(RefsResult)
	if !ok {
		t.Fatalf("Slim returned %T", d.Slim(full))
	}
	if len(got.Branches) != 0 || len(got.Remotes) != 0 || len(got.Tags) != 0 || len(got.Worktrees) != 0 || len(got.Tips) != 0 {
		t.Errorf("slim keeps a list: %+v", got)
	}
	if got.Head != full.Head || !got.BranchesTruncated || got.RemotesTruncated || !got.TagsTruncated || !got.WorktreesTruncated || !got.TipsTruncated {
		t.Errorf("slim = %+v; want HEAD and every flag kept", got)
	}
	want := `{"head":{"oid":"a","branch":"main","detached":false,"upstream":"origin/main","upstreamGone":false,"ahead":2,"behind":3},"branches":[],"branchesTruncated":true,"remotes":[],"remotesTruncated":false,"tags":[],"tagsTruncated":true,"worktrees":[],"worktreesTruncated":true,"tips":[],"tipsTruncated":true}`
	if b, _ := json.Marshal(got); string(b) != want {
		t.Errorf("slim as JSON = %s", b)
	}
	if len(full.Branches) != 1 || len(full.Tips) != 2 {
		t.Errorf("Slim changed the result it was given")
	}
}
