package git

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/git/gittest"
)

func ip(n int) *int { return &n }

// logPage calls git.log as the UI would.
func (f *readFx) logPage(p LogParams) (LogResult, *capability.Error) {
	f.t.Helper()
	d, ok := f.reg.Lookup("git.log")
	if !ok {
		f.t.Fatal("git.log is not registered")
	}
	res, err := d.Handler(context.Background(), &capability.Call{Workspace: "ws", Params: p})
	if err != nil {
		return LogResult{}, capability.AsError(err)
	}
	return res.(LogResult), nil
}

// mustLog is logPage that fails the test on an error.
func (f *readFx) mustLog(p LogParams) LogResult {
	f.t.Helper()
	res, err := f.logPage(p)
	if err != nil {
		f.t.Fatalf("git.log %+v: %s (%s; cause %q)", p, err.Code, err.Message, err.Cause)
	}
	return res
}

func oidsIn(commits []LogCommit) []string {
	out := make([]string, 0, len(commits))
	for _, c := range commits {
		out = append(out, c.Oid)
	}
	return out
}

// walkOf is the history plain git gives for the tips: the oracle for the page's order.
func (e *env) walkOf(dir string, tips []string) []string {
	e.t.Helper()
	return strings.Fields(e.g(dir, append([]string{"rev-list", "--date-order"}, tips...)...))
}

// rawCommit writes a commit object whose author, committer and message are exactly the
// bytes given (git commit would refuse or tidy some of them), on top of parent ("" for
// a root) with the empty tree, and returns its oid. when is its time.
func (e *env) rawCommit(dir, parent, name, email, message string, when int) string {
	e.t.Helper()
	tree := strings.TrimSpace(e.gStdin(dir, "", "hash-object", "-t", "tree", "-w", "--stdin"))
	var raw strings.Builder
	raw.WriteString("tree " + tree + "\n")
	if parent != "" {
		raw.WriteString("parent " + parent + "\n")
	}
	ident := name + " <" + email + "> " + strconv.Itoa(when) + " +0000"
	raw.WriteString("author " + ident + "\ncommitter " + ident + "\n\n" + message + "\n")
	return strings.TrimSpace(e.gStdin(dir, raw.String(), "hash-object", "-t", "commit", "-w", "--stdin"))
}

// Decision (Q1 §2, §4): with no from, the page starts at the tips (HEAD first, a tag on
// a tree not among them), returns them, and walks the repository's history exactly as git
// orders it with date order: every parent present, every child before its parents. A
// commit's fields are the raw ones git records, a root commit has no parents (an empty
// list, not null), and a merge keeps its parents in order.
func TestLogFirstPage(t *testing.T) {
	f := newReadFx(t)
	r := gittest.History(t, filepath.Join(t.TempDir(), "hist"))
	f.at(r.Dir)

	res := f.mustLog(LogParams{})
	want, truncated, err := tips(context.Background(), f.r, r.Dir, false)
	if err != nil || truncated {
		t.Fatalf("tips: %v truncated %v", err, truncated)
	}
	if !equal(res.Tips, want) || res.TipsTruncated {
		t.Errorf("tips = %v truncated %v; want %v", res.Tips, res.TipsTruncated, want)
	}
	if res.Tips[0] != r.Oids["head"] {
		t.Errorf("HEAD is not the first tip: %v", res.Tips)
	}
	if slices.Contains(res.Tips, r.Oids["tree-tag"]) {
		t.Errorf("a tag on a tree is among the tips")
	}
	if res.More {
		t.Errorf("more is true for a history of %d commits and a page of 500", len(res.Commits))
	}

	got := oidsIn(res.Commits)
	if oracle := f.walkOf(r.Dir, res.Tips); !equal(got, oracle) {
		t.Errorf("the page is not git's date-order walk:\n got %v\nwant %v", got, oracle)
	}
	pos := map[string]int{}
	for i, oid := range got {
		pos[oid] = i
	}
	byOid := map[string]LogCommit{}
	for i, c := range res.Commits {
		byOid[c.Oid] = c
		for _, p := range c.Parents {
			j, ok := pos[p]
			if !ok {
				t.Errorf("parent %s of commit %d is not in the page", p[:7], i)
			} else if j <= i {
				t.Errorf("commit %d comes after its parent %s", i, p[:7])
			}
		}
	}

	root := byOid[r.Oids["root"]]
	if root.Parents == nil || len(root.Parents) != 0 {
		t.Errorf("root parents = %#v; want an empty list", root.Parents)
	}
	if b, _ := json.Marshal(root); !strings.Contains(string(b), `"parents":[]`) {
		t.Errorf("root commit as JSON = %s; want parents as []", b)
	}
	octopus := f.g(r.Dir, "rev-list", "--parents", "-n1", r.Oids["octopus"])
	if parents := strings.Fields(octopus)[1:]; !equal(byOid[r.Oids["octopus"]].Parents, parents) || len(parents) != 4 {
		t.Errorf("octopus parents = %v; want git's four, in order %v", byOid[r.Oids["octopus"]].Parents, parents)
	}
	if m := byOid[r.Oids["merge"]]; m.Subject != "Merge feature/login" || len(m.Parents) != 2 {
		t.Errorf("merge = %+v", m)
	}
	head := byOid[r.Oids["head"]]
	if ct := strings.TrimSpace(f.g(r.Dir, "log", "-1", "--format=%ct", r.Oids["head"])); strconv.FormatInt(head.CommitTime, 10) != ct || head.Subject != "Ours, ahead" || head.AuthorName != "Ann Author" || head.AuthorEmail != "ann@example.com" {
		t.Errorf("head = %+v; want committer time %s", head, ct)
	}
	var zoe LogCommit
	for _, c := range res.Commits {
		if strings.HasPrefix(c.AuthorName, "Zo") {
			zoe = c
		}
	}
	if zoe.AuthorName != "Zoë Ünïcode" || zoe.AuthorEmail != strings.TrimSpace(f.g(r.Dir, "log", "-1", "--format=%ae", zoe.Oid)) {
		t.Errorf("non-ASCII author = %+v", zoe)
	}
}

// Decision (Q1 §2, "git.log walks the whole repository, even when the workspace is a
// subdirectory"): the project folder does not narrow the history, because simplifying
// it by path would rewrite the parents the graph is drawn from.
func TestLogWalksTheWholeRepositoryFromASubfolder(t *testing.T) {
	f := newReadFx(t)
	r := gittest.History(t, filepath.Join(t.TempDir(), "hist"))
	f.at(r.Dir)
	whole := f.mustLog(LogParams{})
	f.at(filepath.Join(r.Dir, "docs"))
	sub := f.mustLog(LogParams{})
	if !equal(oidsIn(sub.Commits), oidsIn(whole.Commits)) || !equal(sub.Tips, whole.Tips) || len(whole.Commits) < 10 {
		t.Errorf("from docs/: %d commits, %d tips; want the repository's %d and %d", len(sub.Commits), len(sub.Tips), len(whole.Commits), len(whole.Tips))
	}
}

// Decision (Q1 §2): the pages of a walk are one walk. Pages of 7, each passing the first
// page's tips back with a skip of what was received, join into the page of everything,
// `more` is false on exactly the last page, and a commit made, a branch moved and a tag
// added between two pages change nothing, because the tips are the caller's and are not
// resolved again.
func TestLogPagesJoin(t *testing.T) {
	f := newReadFx(t)
	r := gittest.History(t, filepath.Join(t.TempDir(), "hist"))
	f.at(r.Dir)

	whole := f.mustLog(LogParams{})
	all := oidsIn(whole.Commits)
	n := len(all)
	if n <= 14 {
		t.Fatalf("the fixture has %d commits; the test needs several pages", n)
	}

	first := f.mustLog(LogParams{Limit: ip(7)})
	if !first.More || len(first.Commits) != 7 || !equal(first.Tips, whole.Tips) {
		t.Fatalf("first page: %d commits, more %v", len(first.Commits), first.More)
	}
	joined := oidsIn(first.Commits)

	// Refs move between the pages.
	f.write(r.Dir, "later.txt", "made between two pages\n")
	f.commit(r.Dir)
	f.g(r.Dir, "branch", "new-branch", r.Oids["root"])
	f.g(r.Dir, "tag", "new-tag", r.Oids["second"])
	if moved := f.mustLog(LogParams{Limit: ip(7)}); moved.Commits[0].Oid == all[0] || len(moved.Tips) == len(first.Tips) {
		t.Fatalf("control: the new commit changed nothing for a first page; the check below proves nothing")
	}

	for pages := 0; ; pages++ {
		if pages > n {
			t.Fatalf("more is still true after %d pages", pages)
		}
		page := f.mustLog(LogParams{From: first.Tips, Skip: ip(len(joined)), Limit: ip(7)})
		if len(page.Tips) != 0 || page.TipsTruncated {
			t.Errorf("a page with from returned tips %v (truncated %v)", page.Tips, page.TipsTruncated)
		}
		joined = append(joined, oidsIn(page.Commits)...)
		if !page.More {
			break
		}
		if len(page.Commits) != 7 {
			t.Fatalf("a page with more holds %d commits, want 7", len(page.Commits))
		}
	}
	if !equal(joined, all) {
		t.Errorf("pages of 7 hold %d commits and differ from the %d of one page:\n got %v\nwant %v", len(joined), n, joined, all)
	}

	// The boundary: a page of exactly the rest has no more; one short of it has.
	if p := f.mustLog(LogParams{From: first.Tips, Limit: ip(n)}); p.More || len(p.Commits) != n {
		t.Errorf("a page of exactly %d: %d commits, more %v", n, len(p.Commits), p.More)
	}
	if p := f.mustLog(LogParams{From: first.Tips, Limit: ip(n - 1)}); !p.More || len(p.Commits) != n-1 {
		t.Errorf("a page of %d: %d commits, more %v", n-1, len(p.Commits), p.More)
	}
	if p := f.mustLog(LogParams{From: first.Tips, Skip: ip(n + 50)}); p.More || len(p.Commits) != 0 || p.Commits == nil {
		t.Errorf("a skip past the end: %+v; want an empty page", p)
	}
}

// Decision (Q1 §2): at most 5,000 tips. 5,001 tags on distinct commits give 5,000 tips,
// tipsTruncated, with HEAD and the branch among them, and those tips are acceptable as
// the next page's from (the cap on from is the cap on tips).
func TestLogTipsCapped(t *testing.T) {
	f := newReadFx(t)
	dir := filepath.Join(t.TempDir(), "syn")
	gittest.Synthetic(t, dir, 6000, 0, 5001)
	f.at(dir)

	res := f.mustLog(LogParams{Limit: ip(100)})
	if len(res.Tips) != maxTips || !res.TipsTruncated || len(dedupe(res.Tips)) != maxTips {
		t.Errorf("%d tips (%d distinct), truncated %v; want %d, true", len(res.Tips), len(dedupe(res.Tips)), res.TipsTruncated, maxTips)
	}
	heads := f.oidsOf(dir, "HEAD", "origin/main")
	for _, h := range heads {
		if !slices.Contains(res.Tips, h) {
			t.Errorf("tip %s is not among the %d returned", h[:7], len(res.Tips))
		}
	}
	page2, err := f.logPage(LogParams{From: res.Tips, Skip: ip(100), Limit: ip(100)})
	if err != nil {
		t.Fatalf("page 2 with the 5,000 tips as from: %s (%s)", err.Code, err.Message)
	}
	if len(page2.Commits) != 100 {
		t.Errorf("page 2 holds %d commits, want 100", len(page2.Commits))
	}
}

// Decision (Q1 §2, ADR-0069 §2: "tips reach git on stdin"): no object id is on any git's
// argv, whether the tips were resolved by the call or passed in as from, and the log
// reads them from stdin.
func TestLogTipsOnStdin(t *testing.T) {
	f := newReadFx(t)
	r := gittest.History(t, filepath.Join(t.TempDir(), "hist"))
	f.at(r.Dir)
	var mu sync.Mutex
	var argvs [][]string
	f.onStart = func(cmd *exec.Cmd) {
		mu.Lock()
		argvs = append(argvs, slices.Clone(cmd.Args))
		mu.Unlock()
	}
	first := f.mustLog(LogParams{Limit: ip(5)})
	f.mustLog(LogParams{From: first.Tips, Skip: ip(5), Limit: ip(5)})

	hexOid := regexp.MustCompile(`[0-9a-f]{40}`)
	logs, checks := 0, 0
	for _, argv := range argvs {
		for _, a := range argv {
			if hexOid.MatchString(a) {
				t.Errorf("an object id on a git's argv: %q", argv)
			}
		}
		switch {
		case slices.Contains(argv, "log"):
			logs++
			if !slices.Contains(argv, "--stdin") {
				t.Errorf("a log without --stdin: %q", argv)
			}
		case slices.Contains(argv, "cat-file"):
			checks++
		}
	}
	// The control: the oids did reach git, so the empty argv search above means stdin.
	if logs != 2 || checks != 1 || len(first.Tips) < 5 || len(first.Commits) != 5 {
		t.Errorf("%d logs and %d existence checks (want 2 and 1) over %d tips; the search for oids on argv proves nothing", logs, checks, len(first.Tips))
	}
}

// Decision (Q1 §1: "from validated in Go before any git starts"; ADR-0069 §4): a from
// that is not a list of at most 5,000 full lowercase hex object ids, and a page out of
// bounds, are REQUEST_INVALID_PARAMS with no git started: a short id, a ref name, an
// option, a revision expression, a line break (a second oid on stdin), 5,001 ids.
func TestLogFromRefused(t *testing.T) {
	f := newReadFx(t)
	r := gittest.History(t, filepath.Join(t.TempDir(), "hist"))
	f.at(r.Dir)
	good := r.Oids["head"]
	tooMany := slices.Repeat([]string{good}, maxFrom+1)
	for name, p := range map[string]LogParams{
		"a short id":         {From: []string{good[:7]}},
		"an id one short":    {From: []string{good[:39]}},
		"a ref name":         {From: []string{"main"}},
		"HEAD":               {From: []string{"HEAD"}},
		"an option":          {From: []string{"-x"}},
		"an option with =":   {From: []string{"--output=/tmp/x"}},
		"a revision":         {From: []string{good + "^"}},
		"upper case":         {From: []string{strings.ToUpper(good)}},
		"a line break":       {From: []string{good + "\n" + good}},
		"a trailing newline": {From: []string{good + "\n"}},
		"empty":              {From: []string{""}},
		"one bad among good": {From: []string{good, "main", good}},
		"5,001 ids":          {From: tooMany},
		"a negative skip":    {Skip: ip(-1)},
		"a skip git rejects": {Skip: ip(1 << 31)},
		"a zero limit":       {Limit: ip(0)},
		"a limit of 1001":    {Limit: ip(maxLogLimit + 1)},
	} {
		_, err := f.logPage(p)
		if err == nil || err.Code != capability.ErrInvalidParams || err.NextStep == "" {
			t.Errorf("%s: %v; want REQUEST_INVALID_PARAMS with a next step", name, err)
		}
	}
	if n := f.starts.Load(); n != 0 {
		t.Errorf("%d gits started for parameters that were refused; want 0", n)
	}
	// The control: the counter counts, and the same call with good ids starts git.
	if _, err := f.logPage(LogParams{From: slices.Repeat([]string{good}, maxFrom)}); err != nil || f.starts.Load() == 0 {
		t.Errorf("control: %d ids of a real commit gave %v after %d starts; the zero above proves nothing", maxFrom, err, f.starts.Load())
	}
	if p := f.mustLog(LogParams{Limit: ip(maxLogLimit)}); len(p.Commits) == 0 {
		t.Errorf("a limit of %d returned nothing", maxLogLimit)
	}
}

// Decision (ADR-0069 §4): a from that names an object the repository does not hold, or
// one that is not a commit (a tree, a blob, an annotated tag), is REQUEST_INVALID_PARAMS,
// not git's error and not a page: the tips the caller passes back are commits.
func TestLogFromNotACommit(t *testing.T) {
	f := newReadFx(t)
	r := gittest.History(t, filepath.Join(t.TempDir(), "hist"))
	f.at(r.Dir)
	tag := f.oidsOf(r.Dir, "refs/tags/v1.0")[0]
	if kind := strings.TrimSpace(f.g(r.Dir, "cat-file", "-t", tag)); kind != "tag" {
		t.Fatalf("fixture: v1.0 is a %s", kind)
	}
	tree := f.oidsOf(r.Dir, "HEAD^{tree}")[0]
	blob := f.oidsOf(r.Dir, "HEAD:README.md")[0]
	for name, oid := range map[string]string{
		"a tree": tree, "a blob": blob, "an annotated tag": tag,
		"an unknown id": strings.Repeat("0", 40), "a 64 digit id": strings.Repeat("a", 64),
	} {
		_, err := f.logPage(LogParams{From: []string{r.Oids["head"], oid}})
		if err == nil || err.Code != capability.ErrInvalidParams || !strings.Contains(err.Message, "from[1]") {
			t.Errorf("%s: %v; want REQUEST_INVALID_PARAMS naming from[1]", name, err)
		}
	}
	if res, err := f.logPage(LogParams{From: []string{r.Oids["head"], r.Oids["root"]}}); err != nil || len(res.Commits) == 0 {
		t.Errorf("control: two commits gave %v, %d commits", err, len(res.Commits))
	}
}

// Decision (Q1 §1, ADR-0069 §4): the 4 MiB output cap ends a page at a whole record.
// Sixty commits of 100 KiB subjects pass the cap: the page holds whole commits (every
// subject intact) and says more, and the next skip, counting the records returned,
// continues without a gap or a repeat until the last commit. A single commit larger than
// the cap cannot be paged past, and is GIT_FAILED rather than a page that never moves.
func TestLogOutputCap(t *testing.T) {
	f := newReadFx(t)
	dir := f.root
	const commits, size = 60, 100 << 10
	subject := strings.Repeat("s", size)
	var oids []string
	parent := ""
	for i := range commits {
		parent = f.rawCommit(dir, parent, "A", "a@example.com", subject, 1700000000+i)
		oids = append(oids, parent)
	}
	f.g(dir, "update-ref", "refs/heads/main", parent)
	slices.Reverse(oids) // newest first

	first := f.mustLog(LogParams{Limit: ip(maxLogLimit)})
	if !first.More || len(first.Commits) == 0 || len(first.Commits)*size > stdoutLimit || (len(first.Commits)+1)*size < stdoutLimit-size {
		t.Fatalf("the capped page: %d commits, more %v; want the ones that fit in %d bytes and more", len(first.Commits), first.More, stdoutLimit)
	}
	var got []string
	for skip := 0; ; {
		page := f.mustLog(LogParams{From: first.Tips, Skip: ip(skip), Limit: ip(maxLogLimit)})
		for _, c := range page.Commits {
			if len(c.Subject) != size {
				t.Fatalf("commit %s: a subject of %d bytes, want it whole (%d)", c.Oid[:7], len(c.Subject), size)
			}
		}
		got = append(got, oidsIn(page.Commits)...)
		skip += len(page.Commits)
		if !page.More {
			break
		}
		if len(page.Commits) == 0 {
			t.Fatal("a page with more holds no commit: the walk cannot move")
		}
	}
	if !equal(got, oids) {
		t.Errorf("the capped pages hold %d commits and differ from the history's %d", len(got), len(oids))
	}

	// A commit that alone passes the cap.
	big := f.rawCommit(dir, "", "A", "a@example.com", strings.Repeat("b", stdoutLimit+1024), 1700001000)
	f.g(dir, "update-ref", "refs/heads/main", big)
	_, err := f.logPage(LogParams{From: []string{big}})
	wantCode(t, err, CodeFailed)
}

// Decision (Q1 §2): a repository with no commit has no tips and an empty page, as lists
// ([] and not null), and no error.
func TestLogUnborn(t *testing.T) {
	f := newReadFx(t)
	res, err := f.logPage(LogParams{})
	if err != nil {
		t.Fatalf("an unborn repository: %s (%s)", err.Code, err.Message)
	}
	b, _ := json.Marshal(res)
	if string(b) != `{"commits":[],"tips":[],"tipsTruncated":false,"more":false}` {
		t.Errorf("result = %s; want an empty page", b)
	}
}

// Decision (ADR-0066 §4): a root in no repository is GIT_NOT_A_REPOSITORY, whether the
// call resolves the tips or is given them; no git on the machine is GIT_NOT_FOUND with
// a next step, before any process starts.
func TestLogNotARepository(t *testing.T) {
	f := newReadFx(t)
	r := gittest.History(t, filepath.Join(t.TempDir(), "hist"))
	f.at(t.TempDir())
	_, err := f.logPage(LogParams{})
	wantCode(t, err, CodeNotARepository)
	_, err = f.logPage(LogParams{From: []string{r.Oids["head"]}})
	wantCode(t, err, CodeNotARepository)

	f.at(r.Dir)
	before := f.starts.Load()
	f.look = func(string) (string, error) { return "", exec.ErrNotFound }
	_, err = f.logPage(LogParams{})
	wantCode(t, err, CodeNotFound)
	if err.NextStep == "" || f.starts.Load() != before {
		t.Errorf("GIT_NOT_FOUND: next step %q, %d starts", err.NextStep, f.starts.Load()-before)
	}
}

// Decision (ADR-0069 §6; Q1 §1): commit text is project content that can hold anything.
// What the result carries is valid UTF-8 (invalid bytes become one U+FFFD per run, so the
// JSON the browser parses is the commit's text and never an error), bidi controls arrive
// untouched for the UI to isolate, and a 10 KiB subject is whole.
func TestLogAwkwardText(t *testing.T) {
	f := newReadFx(t)
	dir := f.root
	e := f.env
	const bidi = "‮⁦ evil ⁩‬"
	long := strings.Repeat("0123456789", 1024)
	c1 := e.rawCommit(dir, "", "Bidi "+bidi, "bidi@example.com", "subject "+bidi, 1700000001)
	c2 := e.rawCommit(dir, c1, "Caf\xe9 \xff\xfe man", "bad\x80@example.com", "sub\xc3 broken \x80\x80 end", 1700000002)
	c3 := e.rawCommit(dir, c2, "Long", "long@example.com", long, 1700000003)
	f.g(dir, "update-ref", "refs/heads/main", c3)

	res := f.mustLog(LogParams{})
	if len(res.Commits) != 3 {
		t.Fatalf("%d commits, want 3", len(res.Commits))
	}
	for _, c := range res.Commits {
		for _, s := range []string{c.AuthorName, c.AuthorEmail, c.Subject} {
			if !utf8.ValidString(s) {
				t.Errorf("commit %s holds invalid UTF-8: %q", c.Oid[:7], s)
			}
		}
	}
	if c := res.Commits[2]; c.AuthorName != "Bidi "+bidi || c.Subject != "subject "+bidi {
		t.Errorf("bidi controls were altered: %q, %q", c.AuthorName, c.Subject)
	}
	if c := res.Commits[1]; c.AuthorName != "Caf� � man" || c.AuthorEmail != "bad�@example.com" || c.Subject != "sub� broken � end" {
		t.Errorf("invalid bytes: %q, %q, %q; want one U+FFFD per run", c.AuthorName, c.AuthorEmail, c.Subject)
	}
	if c := res.Commits[0]; c.Subject != long || len(c.Subject) != 10<<10 {
		t.Errorf("the 10 KiB subject has %d bytes", len(c.Subject))
	}
}

// Decision (ADR-0066 §3, Q1 §3): through the capability, on an untrusted root, a
// repository that asks git to verify a commit's signature with a program of its own
// (log.showSignature and gpg.program) does not get it run, with the tips resolved by the
// call and with the tips passed in. The control is plain git, which runs the program.
func TestLogRunsNoSignatureProgram(t *testing.T) {
	f := newReadFx(t)
	e := f.env
	dir, _ := e.historyRepo("")
	mark := filepath.Join(t.TempDir(), "mark")
	script := e.script("gpg.sh", "touch '"+mark+"'\ncat")
	head := e.signedHead(dir, "PGP SIGNATURE")
	e.appendConfig(dir, "[log]\n\tshowSignature = true\n[gpg]\n\tprogram = "+script+"\n")
	f.at(dir)
	f.trusted = func(string) (bool, *capability.Error) { return false, nil }

	e.g(dir, "log", "-1")
	if !exists(mark) {
		t.Fatal("control: plain git did not run the signature program; the check below proves nothing")
	}
	os.Remove(mark)

	res := f.mustLog(LogParams{})
	if len(res.Commits) != 3 || res.Commits[0].Oid != head || res.Commits[0].Subject != "signed" {
		t.Fatalf("the page = %+v; want the signed head and its two parents", res.Commits)
	}
	f.mustLog(LogParams{From: res.Tips})
	if exists(mark) {
		t.Errorf("git.log ran the repository's gpg.program")
	}
}

// Decision (ADR-0069 §1, ADR-0060 §2, ADR-0020 §Retention): git.log is a stateless
// workspace query for the human UI (audience human: the core refuses it over MCP), that
// needs git, whose codes are listed, and a retained result drops the page and the tips
// once delivered but keeps the flags the caller acts on. Reversing the audience would
// list it as an agent tool.
func TestLogDescriptor(t *testing.T) {
	f := newReadFx(t)
	d, ok := f.reg.Lookup("git.log")
	if !ok {
		t.Fatal("git.log is not registered")
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
}

// Decision (ADR-0020 §Retention; the retention test of git.read): a result returned
// again after delivery holds no commits and no tips, so retention never keeps a page of
// history; more and tipsTruncated stay, and as lists ([] not null).
func TestLogSlim(t *testing.T) {
	f := newReadFx(t)
	d, _ := f.reg.Lookup("git.log")
	full := LogResult{
		Commits:       []LogCommit{{Oid: "a", Parents: []string{}, Subject: "s"}},
		Tips:          []string{"a", "b"},
		TipsTruncated: true,
		More:          true,
	}
	got, ok := d.Slim(full).(LogResult)
	if !ok {
		t.Fatalf("Slim returned %T", d.Slim(full))
	}
	if len(got.Commits) != 0 || len(got.Tips) != 0 || !got.More || !got.TipsTruncated {
		t.Errorf("slim = %+v; want the page and the tips dropped and the flags kept", got)
	}
	if b, _ := json.Marshal(got); string(b) != `{"commits":[],"tips":[],"tipsTruncated":true,"more":true}` {
		t.Errorf("slim as JSON = %s", b)
	}
	if len(full.Commits) != 1 || len(full.Tips) != 2 {
		t.Errorf("Slim changed the result it was given")
	}
}
