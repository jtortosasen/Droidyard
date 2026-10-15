package git

// git.checkout and git.createBranch (plan m2n part 2, step P3; ADR-0071 §1, §5, §6, plan
// Q6). Real git over the fixtures of gittest.Remote and gittest.Submodules, started through
// the real supervisor's recorded spawner (wfx, write_test.go). Each test says which decision
// it holds and, where the plan asks, what it was seen failing under.

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/git/gittest"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// branches is a call's result, failing the test on an error of the call.
func (f *wfx) branches(name string, params any) BranchResult {
	f.t.Helper()
	res, err := f.call(name, "ws", params)
	if err != nil {
		f.t.Fatalf("%s: %s (%s; cause %q; nextStep %q)", name, err.Code, err.Message, err.Cause, err.NextStep)
	}
	return res.(BranchResult)
}

// head is the checked-out branch of the fixture's work tree.
func (f *wfx) head() string {
	f.t.Helper()
	return strings.TrimSpace(f.e.g(f.r.W, "symbolic-ref", "--short", "HEAD"))
}

// wantDone asserts a one-root result of a done outcome for ".".
func wantDone(t *testing.T, res BranchResult) {
	t.Helper()
	if len(res.Outcomes) != 1 || res.Outcomes[0].Root != "." || res.Outcomes[0].Status != "done" || res.Outcomes[0].Error != nil {
		t.Fatalf("outcomes %+v; want one done outcome for \".\"", res.Outcomes)
	}
}

// Decision (ADR-0071 §1): git.checkout of a local branch switches to it, and only that: the
// work tree takes the branch's files, no other ref is created or moved, and no hook runs
// (the fixture's post-checkout and reference-transaction write markers). The control is the
// state before: HEAD is main and y.txt is absent.
func TestCheckoutLocal(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newWfx(t)
	f.e.g(f.r.W, "branch", "-q", "loc", "origin/y")
	if f.head() != "main" || exists(filepath.Join(f.r.W, "y.txt")) {
		t.Fatal("control: the fixture is not on main without y.txt")
	}
	refs := f.e.g(f.r.W, "for-each-ref", "--format=%(refname) %(objectname)")
	hookMarks(t, f.r.Markers, true)

	wantDone(t, f.branches("git.checkout", CheckoutParams{Name: "loc"}))
	if got := f.head(); got != "loc" {
		t.Errorf("HEAD is %q, want loc", got)
	}
	if !exists(filepath.Join(f.r.W, "y.txt")) {
		t.Error("the branch's file y.txt is not in the work tree")
	}
	if got := f.e.g(f.r.W, "for-each-ref", "--format=%(refname) %(objectname)"); got != refs {
		t.Errorf("a ref changed:\nbefore:\n%safter:\n%s", refs, got)
	}
	if marks := hookMarks(t, f.r.Markers, false); marks != "" {
		t.Errorf("a hook ran: %q", marks)
	}
	if n := f.writeStarts.Load(); n != 1 {
		t.Errorf("%d write gits for one checkout, want 1", n)
	}
}

// Decision (ADR-0071 §1): a name that is only a remote-tracking branch of the remote asked
// for becomes a local branch at that commit tracking it (upstream origin/y), and is checked
// out. The control: no local y before, and plain `git switch y` (git's own guess) would have
// made one too, which is why the call asks for it with `remote` and --no-guess otherwise.
func TestCheckoutRemoteCreatesTracking(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newWfx(t)
	if f.e.revOr(f.r.W, "refs/heads/y") != "" {
		t.Fatal("control: a local y exists before the checkout")
	}
	hookMarks(t, f.r.Markers, true)

	wantDone(t, f.branches("git.checkout", CheckoutParams{Name: "y", Remote: "origin"}))
	if got := f.head(); got != "y" {
		t.Errorf("HEAD is %q, want y", got)
	}
	if got, want := f.e.rev(f.r.W, "refs/heads/y"), f.r.Oids["y"]; got != want {
		t.Errorf("the new y is at %s, want origin/y's %s", got, want)
	}
	cfg := func(k string) string { return strings.TrimSpace(f.e.g(f.r.W, "config", "--get", k)) }
	if cfg("branch.y.remote") != "origin" || cfg("branch.y.merge") != "refs/heads/y" {
		t.Errorf("y tracks %q %q, want origin refs/heads/y", cfg("branch.y.remote"), cfg("branch.y.merge"))
	}
	if marks := hookMarks(t, f.r.Markers, false); marks != "" {
		t.Errorf("a hook ran: %q", marks)
	}

	// A folder in the name: feature/x.
	wantDone(t, f.branches("git.checkout", CheckoutParams{Name: "feature/x", Remote: "origin"}))
	if got := f.head(); got != "feature/x" {
		t.Errorf("HEAD is %q, want feature/x", got)
	}
}

// Decision (ADR-0071 §1): with `remote` and a local branch of that name, the local branch
// is switched to as it is and nothing is created or moved: it stays at its own commit, not
// the remote-tracking one's, and gets no upstream. Seen failing with the local check
// removed (the call always taking the tracking path): git refused to create y.
func TestCheckoutExistingLocalWithRemote(t *testing.T) {
	t.Parallel()
	f := newWfx(t)
	f.e.g(f.r.W, "branch", "-q", "y", "main")
	own := f.e.rev(f.r.W, "refs/heads/y")
	if own == f.r.Oids["y"] {
		t.Fatal("control: the local y is already at origin/y")
	}
	refs := f.e.g(f.r.W, "for-each-ref", "--format=%(refname) %(objectname)")

	wantDone(t, f.branches("git.checkout", CheckoutParams{Name: "y", Remote: "origin"}))
	if f.head() != "y" {
		t.Errorf("HEAD is %q, want y", f.head())
	}
	if got := f.e.g(f.r.W, "for-each-ref", "--format=%(refname) %(objectname)"); got != refs {
		t.Errorf("a ref changed:\nbefore:\n%safter:\n%s", refs, got)
	}
	if out, err := f.e.gErr(f.r.W, "config", "--get", "branch.y.remote"); err == nil {
		t.Errorf("the local y was given an upstream: %q", out)
	}
}

// Decision (ADR-0071 §5): a checkout git refuses over local changes is a failed outcome of
// its root, GIT_FAILED with git's own words naming the file in the way, and changes
// nothing: HEAD, the refs and the edited file are as they were. The call itself succeeds.
// The control is the same checkout once the file is restored.
func TestCheckoutRefusedOverLocalChanges(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newWfx(t)
	f.e.g(f.r.W, "branch", "-q", "fx", "origin/feature/x")
	f.e.write(f.r.W, "shared.txt", "my uncommitted edit\n")
	before := treeState(t, f.e, f.r.W)

	res := f.branches("git.checkout", CheckoutParams{Name: "fx"})
	if len(res.Outcomes) != 1 || res.Outcomes[0].Status != "failed" || res.Outcomes[0].Error == nil {
		t.Fatalf("outcomes %+v; want one failed outcome", res.Outcomes)
	}
	oe := res.Outcomes[0].Error
	if oe.Code != string(CodeFailed) || !strings.Contains(oe.Cause, "shared.txt") {
		t.Errorf("error %+v; want GIT_FAILED with a cause naming shared.txt", oe)
	}
	if strings.Contains(oe.Cause, "\n") == false {
		t.Errorf("cause %q is one line: the files in the way come on the lines after the first", oe.Cause)
	}
	if after := treeState(t, f.e, f.r.W); after != before {
		t.Errorf("the refused checkout changed the repository:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if left := locks(t, f.r.W); len(left) > 0 {
		t.Errorf("locks left: %v", left)
	}

	f.e.g(f.r.W, "checkout", "-q", "--", "shared.txt")
	wantDone(t, f.branches("git.checkout", CheckoutParams{Name: "fx"}))
}

// Decision (ADR-0071 §3, §6): a checkout leaves submodules alone, they are roots of their
// own. The superproject's branch records an older commit of `lib`; with submodule.recurse
// on, as a user's configuration may have it, plain git moves lib's HEAD, and the call does
// not. Seen failing with --no-recurse-submodules removed from switchArgs: lib moved to lib1.
func TestCheckoutNoRecurse(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newWfxBare(t) // the Remote of newWfx would go unused: the workspace is `s`
	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "subs"))
	f.roots["ws"] = platform.Current().Canonical(s.Super)
	f.register()
	f.e.g(s.Super, "config", "submodule.recurse", "true")
	lib := filepath.Join(s.Super, "lib")
	f.e.g(s.Super, "branch", "-q", "old")
	f.e.g(s.Super, "switch", "-q", "old")
	f.e.g(s.Super, "update-index", "--add", "--cacheinfo", "160000,"+s.Oids["lib1"]+",lib")
	f.e.g(s.Super, "commit", "-q", "-m", "record lib1")
	f.e.g(s.Super, "switch", "-q", "main")
	if got := f.e.rev(lib, "HEAD"); got != s.Oids["lib2"] {
		t.Fatalf("control: lib is at %s, want lib2 %s", got, s.Oids["lib2"])
	}

	// The control: plain git, with the recursion the user configured, moves lib.
	f.e.g(s.Super, "switch", "-q", "old")
	if got := f.e.rev(lib, "HEAD"); got != s.Oids["lib1"] {
		t.Fatalf("control: plain `git switch old` left lib at %s, want lib1 %s: the test cannot fail", got, s.Oids["lib1"])
	}
	f.e.g(s.Super, "switch", "-q", "main")
	f.e.g(lib, "checkout", "-q", s.Oids["lib2"])

	wantDone(t, f.branches("git.checkout", CheckoutParams{Name: "old"}))
	if got := strings.TrimSpace(f.e.g(s.Super, "symbolic-ref", "--short", "HEAD")); got != "old" {
		t.Errorf("the superproject is on %q, want old", got)
	}
	if got := f.e.rev(lib, "HEAD"); got != s.Oids["lib2"] {
		t.Errorf("lib's HEAD moved to %s, want it untouched at %s", got, s.Oids["lib2"])
	}
}

// Decision (ADR-0071 §3): no hook runs for a branch operation on a trusted root, though
// the repository has them: all three calls below move HEAD or add a ref, which post-checkout
// and reference-transaction would see. The control: the same operation by plain git writes
// the markers, so the empty marker file is the invocation's doing (core.hooksPath).
func TestCheckoutRunsNoHook(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newWfx(t)
	f.e.g(f.r.W, "switch", "-q", "-c", "ctl", "origin/y")
	if hookMarks(t, f.r.Markers, true) == "" {
		t.Fatal("control: plain git ran no hook in the fixture, so the test cannot fail")
	}
	f.e.g(f.r.W, "switch", "-q", "main")
	hookMarks(t, f.r.Markers, true)

	f.branches("git.checkout", CheckoutParams{Name: "y", Remote: "origin"})
	f.branches("git.checkout", CheckoutParams{Name: "main"})
	f.branches("git.createBranch", CreateBranchParams{Name: "made", Checkout: true, From: f.r.Oids["y"]})
	f.branches("git.createBranch", CreateBranchParams{Name: "made2", Checkout: false, From: f.r.Oids["y"]})
	if marks := hookMarks(t, f.r.Markers, false); marks != "" {
		t.Errorf("a hook ran: %q", marks)
	}
}

// Decision (ADR-0071 §1, plan Q6 (1)): git.createBranch makes the branch at `from`, with no
// upstream: with checkout false HEAD stays where it is, with true it moves; a name that
// exists is REQUEST_INVALID_PARAMS and no write git starts. The commit is a remote's, so a
// branch made from the current HEAD would not tell `from` from HEAD.
func TestCreateBranch(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newWfx(t)
	from := f.r.Oids["feature/x"]
	if f.e.rev(f.r.W, "HEAD") == from {
		t.Fatal("control: HEAD is already at from")
	}

	wantDone(t, f.branches("git.createBranch", CreateBranchParams{Name: "plain", From: from}))
	if got := f.e.rev(f.r.W, "refs/heads/plain"); got != from {
		t.Errorf("plain is at %s, want %s", got, from)
	}
	if f.head() != "main" {
		t.Errorf("HEAD moved to %q with checkout false", f.head())
	}

	wantDone(t, f.branches("git.createBranch", CreateBranchParams{Name: "moved", Checkout: true, From: from, Roots: []string{"."}}))
	if got := f.e.rev(f.r.W, "refs/heads/moved"); got != from {
		t.Errorf("moved is at %s, want %s", got, from)
	}
	if f.head() != "moved" || f.e.rev(f.r.W, "HEAD") != from {
		t.Errorf("HEAD is %q at %s, want moved at %s", f.head(), f.e.rev(f.r.W, "HEAD"), from)
	}
	for _, b := range []string{"plain", "moved"} {
		if out, err := f.e.gErr(f.r.W, "config", "--get", "branch."+b+".remote"); err == nil {
			t.Errorf("%s was given an upstream: %q", b, out)
		}
	}

	// A name that exists, for both values of checkout.
	starts := f.writeStarts.Load()
	for _, checkout := range []bool{false, true} {
		for _, name := range []string{"plain", "main"} {
			_, err := f.call("git.createBranch", "ws", CreateBranchParams{Name: name, Checkout: checkout, From: from})
			wantErrCode(t, err, capability.ErrInvalidParams)
			if err.NextStep == "" {
				t.Errorf("%s: refused without a nextStep", name)
			}
		}
	}
	if n := f.writeStarts.Load(); n != starts {
		t.Errorf("%d write gits started for names that exist", n-starts)
	}
	if got := f.e.rev(f.r.W, "refs/heads/plain"); got != from {
		t.Errorf("plain moved to %s", got)
	}
}

// Decision (ADR-0071 §5, plan Q6 (1)): `from` is a full object id of a commit of this
// repository, checked before the write: a short id, a ref name, HEAD, upper case, a tree's
// id, a blob's id, a tag object's id and an id the repository does not hold are each
// REQUEST_INVALID_PARAMS, and no write git starts. The control is a commit's full id.
// Seen failing with the cat-file check removed (firstNotCommit): the tree, the blob, the tag
// and the unknown id reached the write and failed there as outcomes.
func TestCreateBranchFromRefused(t *testing.T) {
	t.Parallel()
	f := newWfx(t)
	good := f.r.Oids["y"]
	tree := f.e.rev(f.r.W, "HEAD^{tree}")
	blob := f.e.rev(f.r.W, "HEAD:README.md")
	f.e.g(f.r.W, "tag", "-a", "-m", "annotated", "v1", "main")
	tag := f.e.rev(f.r.W, "refs/tags/v1")
	if tag == f.e.rev(f.r.W, "refs/tags/v1^{commit}") {
		t.Fatal("control: the annotated tag has the commit's id")
	}
	for name, from := range map[string]string{
		"a short id": good[:7], "a ref name": "main", "HEAD": "HEAD", "empty": "", "upper case": strings.ToUpper(good),
		"a tree": tree, "a blob": blob, "a tag object": tag, "an unknown id": strings.Repeat("ab", 20),
		"an option": "--orphan", "an id and more": good + " main", "an id and a newline": good + "\n",
		"a 41 character id": good + "a",
	} {
		for _, checkout := range []bool{false, true} {
			_, err := f.call("git.createBranch", "ws", CreateBranchParams{Name: "never", Checkout: checkout, From: from})
			if err == nil || err.Code != capability.ErrInvalidParams {
				t.Errorf("from %s (checkout %v): %+v, want REQUEST_INVALID_PARAMS", name, checkout, err)
			} else if err.NextStep == "" {
				t.Errorf("from %s: refused without a nextStep", name)
			}
		}
	}
	if n := f.writeStarts.Load(); n != 0 {
		t.Errorf("%d write gits started for refused starts", n)
	}
	if f.e.revOr(f.r.W, "refs/heads/never") != "" {
		t.Error("a branch was created")
	}
	wantDone(t, f.branches("git.createBranch", CreateBranchParams{Name: "ok", From: good}))
}

// The names the branch capabilities refuse, in two families, by who refuses them
// (TestValidName holds the split: validName refuses the first and accepts the second).
//
// namesRefusedInGo are refused by validName alone, before any git is looked for: an option,
// an expansion, a pseudo-ref, another ref's prefix, a control character, a name too long.
// TestBranchNamesRefusedBeforeAnyGit runs them all, through both capabilities, with no
// repository at all.
//
// namesRefusedByGit pass validName and are refused after it: by `git check-ref-format
// --branch` or because they start with a remote's name and a slash. Only git can say so,
// and TestBranchNamesRefused asks it, in a repository with an origin.
var (
	namesRefusedInGo = []string{
		"-b", "--orphan", "-x", "--end-of-options", "-", "@{-1}", "@{u}", "a@{1}", "HEAD", "@", "a\tb",
		"NUL\x00name", "a\x00b", "new\nline", "a\nb", "cr\rname", "del\x7fname", strings.Repeat("a", 256), "",
		"refs/heads/x", "refs/tags/x", "heads/x", "remotes/origin/x", "tags/v1", "FETCH_HEAD", "ORIG_HEAD", "MERGE_HEAD",
	}
	namesRefusedByGit = []string{
		"a..b", "a b", "x.lock", "feature.lock", "a/x.lock",
		"a~1", "a^", "a:b", "a?b", "a*b", "a[b", "a\\b", "/a", "a/", "a//b", ".a", "a/.b", "a.",
		"origin/x", "origin/feature/x", "origin/main",
	}
)

// Decision (ADR-0071 §5; plan m2n P1 fact 9 and its verifier): no name reaches a write
// git's argv unvalidated. validName refuses what is an option, an expansion, a pseudo-ref
// or another ref (namesRefusedInGo, in TestBranchNamesRefusedBeforeAnyGit); `git remote`
// refuses a remote's name and a slash; `check-ref-format --branch` refuses what git's own
// rules do (namesRefusedByGit, here). Each shape, through both capabilities, is
// REQUEST_INVALID_PARAMS with a nextStep, and no write git starts (counting starter). The
// control: a name of the same family that is valid is accepted. A handful of the names
// validName refuses stay here too, so the whole path is seen once with a repository: HEAD,
// refs/heads/x and FETCH_HEAD, which git's own checks accept, would reach the write if
// validName accepted everything (seen failing so: a ref named refs/heads/refs/heads/x was
// created, HEAD was refused by the write).
func TestBranchNamesRefused(t *testing.T) {
	t.Parallel()
	f := newWfx(t)
	// A previous branch, so that @{-1} means something to check-ref-format.
	f.e.g(f.r.W, "switch", "-q", "-c", "previous")
	f.e.g(f.r.W, "switch", "-q", "main")
	from := f.r.Oids["y"]
	bad := append(slices.Clone(namesRefusedByGit), "-b", "HEAD", "@{-1}", "refs/heads/x", "FETCH_HEAD")
	for _, name := range bad {
		if _, err := f.call("git.checkout", "ws", CheckoutParams{Name: name}); err == nil || err.Code != capability.ErrInvalidParams {
			t.Errorf("checkout %q: %+v, want REQUEST_INVALID_PARAMS", name, err)
		} else if err.NextStep == "" {
			t.Errorf("checkout %q: refused without a nextStep", name)
		}
		if _, err := f.call("git.checkout", "ws", CheckoutParams{Name: name, Remote: "origin"}); err == nil || err.Code != capability.ErrInvalidParams {
			t.Errorf("checkout %q from origin: %+v, want REQUEST_INVALID_PARAMS", name, err)
		}
		for _, checkout := range []bool{false, true} {
			if _, err := f.call("git.createBranch", "ws", CreateBranchParams{Name: name, Checkout: checkout, From: from}); err == nil || err.Code != capability.ErrInvalidParams {
				t.Errorf("createBranch %q (checkout %v): %+v, want REQUEST_INVALID_PARAMS", name, checkout, err)
			} else if err.NextStep == "" {
				t.Errorf("createBranch %q: refused without a nextStep", name)
			}
		}
	}
	if n := f.writeStarts.Load(); n != 0 {
		t.Errorf("%d write gits started for refused names, want none", n)
	}
	for _, ref := range []string{"refs/heads/refs/heads/x", "refs/heads/HEAD", "refs/heads/-b", "refs/heads/FETCH_HEAD"} {
		if f.e.revOr(f.r.W, ref) != "" {
			t.Errorf("%s was created", ref)
		}
	}
	// The controls: names of the same families that are fine.
	for _, name := range []string{"feature/login", "a@b", "my-refs/x", "originx", "headline", "release.1"} {
		wantDone(t, f.branches("git.createBranch", CreateBranchParams{Name: name, From: from}))
	}
}

// Decision (ADR-0071 §5): validName is the Go check that comes first; the names it alone is
// responsible for (namesRefusedInGo: every one of them, which TestValidName holds apart from
// git's own rules) are refused without git being asked, through git.checkout (with and
// without a remote) and git.createBranch (with and without a checkout): a Registry whose git
// is not found would answer GIT_NOT_FOUND to anything that reached a git, and no repository
// is there to answer. Seen failing with validName made to accept everything: the names
// below that git's own rules accept were not refused here.
func TestBranchNamesRefusedBeforeAnyGit(t *testing.T) {
	t.Parallel()
	f := newWfxBare(t)
	f.look = func(string) (string, error) { return "", os.ErrNotExist }
	f.register()
	refused := func(what, name string, err *capability.Error) {
		t.Helper()
		if err == nil || err.Code != capability.ErrInvalidParams {
			t.Errorf("%s %q: %+v, want REQUEST_INVALID_PARAMS before any git is looked for", what, name, err)
		} else if err.NextStep == "" {
			t.Errorf("%s %q: refused without a nextStep", what, name)
		}
	}
	for _, name := range namesRefusedInGo {
		_, err := f.call("git.checkout", "ws", CheckoutParams{Name: name})
		refused("checkout", name, err)
		_, err = f.call("git.checkout", "ws", CheckoutParams{Name: name, Remote: "origin"})
		refused("checkout from origin", name, err)
		for _, checkout := range []bool{false, true} {
			_, err = f.call("git.createBranch", "ws", CreateBranchParams{Name: name, Checkout: checkout, From: strings.Repeat("a", 40)})
			refused("createBranch", name, err)
		}
	}
	if w, r := f.writeStarts.Load(), f.readStarts.Load(); w != 0 || r != 0 {
		t.Errorf("%d write gits and %d reads started for names validName refuses, want none", w, r)
	}
	// The control: a name validName accepts does reach the lookup of git.
	_, err := f.call("git.checkout", "ws", CheckoutParams{Name: "main"})
	wantErrCode(t, err, CodeNotFound)
}

// Decision (ADR-0071 §1): a checkout of a branch the repository lacks is REQUEST_INVALID_PARAMS
// with a nextStep and no write git: neither a local branch nor, with a remote, a
// remote-tracking one; a remote that is not the repository's; a tag or a commit, which are
// not branches. The control: a branch that exists is checked out.
func TestCheckoutMissingBranch(t *testing.T) {
	t.Parallel()
	f := newWfx(t)
	f.e.g(f.r.W, "tag", "-a", "-m", "t", "v1", "main")
	for name, p := range map[string]CheckoutParams{
		"nowhere":                    {Name: "nowhere"},
		"nowhere on origin":          {Name: "nowhere", Remote: "origin"},
		"y, no remote given":         {Name: "y"},
		"y on a remote it lacks":     {Name: "y", Remote: "upstream"},
		"y on a remote's url":        {Name: "y", Remote: f.r.Origin},
		"a tag":                      {Name: "v1"},
		"a tag on origin":            {Name: "v1", Remote: "origin"},
		"a commit id":                {Name: f.r.Oids["y"]},
		"a remote-tracking spelling": {Name: "origin/y"},
		"the remote's HEAD":          {Name: "HEAD", Remote: "origin"},
	} {
		_, err := f.call("git.checkout", "ws", p)
		if err == nil || err.Code != capability.ErrInvalidParams {
			t.Errorf("%s: %+v, want REQUEST_INVALID_PARAMS", name, err)
		} else if err.NextStep == "" {
			t.Errorf("%s: refused without a nextStep", name)
		}
	}
	if n := f.writeStarts.Load(); n != 0 {
		t.Errorf("%d write gits started for branches that do not exist", n)
	}
	if f.head() != "main" {
		t.Errorf("HEAD is %q", f.head())
	}
	wantDone(t, f.branches("git.checkout", CheckoutParams{Name: "y", Remote: "origin"}))
}

// Decision (ADR-0071 §1, §4, plan P-1 B2): a checkout started while a fetch holds the
// repository is queued, names the fetch in blockedBy, starts no git, and runs once the
// fetch is cancelled; and it is not failed by ADR-0020's 20 s queue bound (the writes are
// LongRunning: TestWritesAreLongRunning). The fetch is held on a gate script.
func TestCheckoutQueuedBehindFetch(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newWfx(t)
	f.e.g(f.r.W, "branch", "-q", "loc", "origin/y")
	logA, gate := filepath.Join(t.TempDir(), "a.log"), filepath.Join(t.TempDir(), "a.gate")
	f.e.g(f.r.W, "config", "remote.origin.uploadpack", gateScript(f.e, logA, gate))

	fetch := f.start("git.fetch", "ws", FetchParams{Remote: "origin"})
	waitLines(t, logA, 1)
	checkout := f.start("git.checkout", "ws", CheckoutParams{Name: "loc"})
	create := f.start("git.createBranch", "ws", CreateBranchParams{Name: "behind", Checkout: true, From: f.r.Oids["y"]})
	for name, id := range map[string]string{"checkout": checkout, "createBranch": create} {
		out := f.snap(id)
		if out.State != capability.StateQueued || out.BlockedBy == nil || out.BlockedBy.OperationID == "" {
			t.Fatalf("%s: %s, blockedBy %+v; want queued behind a write", name, out.State, out.BlockedBy)
		}
	}
	if out := f.snap(checkout); out.BlockedBy.OperationID != fetch {
		t.Errorf("the checkout names %s as the one it waits for, want the fetch %s", out.BlockedBy.OperationID, fetch)
	}
	if n := f.writeStarts.Load(); n != 1 {
		t.Errorf("%d write gits started with the fetch holding the repository, want only the fetch", n)
	}
	if f.head() != "main" {
		t.Errorf("HEAD moved to %q while queued", f.head())
	}

	if err := f.ops.Cancel(fetch); err != nil {
		t.Fatal(err)
	}
	if out := f.until(fetch, 15*time.Second); out.State != capability.StateCancelled {
		t.Errorf("the fetch is %s, want cancelled", out.State)
	}
	for name, id := range map[string]string{"checkout": checkout, "createBranch": create} {
		if out := f.until(id, 30*time.Second); out.State != capability.StateCompleted {
			t.Errorf("%s: %s, %+v; want completed after the fetch ended", name, out.State, out.Error)
		}
	}
	if f.head() != "behind" {
		t.Errorf("HEAD is %q after both ran, want behind (the checkout, then the new branch)", f.head())
	}
}

// slowCheckout sets the fixture up for a checkout held mid-write by a slow smudge filter
// (a trusted repository's own configuration): branch slowb has a file that the filter
// rewrites, and the filter records its pid and sleeps. It returns the pid file.
func (f *wfx) slowCheckout() string {
	f.t.Helper()
	started := filepath.Join(f.t.TempDir(), "smudge.pid")
	f.e.g(f.r.W, "switch", "-q", "-c", "slowb")
	f.e.write(f.r.W, ".gitattributes", "slow.txt filter=slow\n")
	f.e.write(f.r.W, "slow.txt", "slow\n")
	f.e.commit(f.r.W)
	f.e.g(f.r.W, "switch", "-q", "main")
	f.e.g(f.r.W, "config", "filter.slow.smudge", f.e.script("smudge.sh", "echo $$ > '"+started+"'\nexec sleep 60"))
	return started
}

// Decision (ADR-0071 §3, plan P1 item 11, P1 verifier): a checkout ended mid-write leaves
// no lock file (SIGTERM first, so git cleans its locks; SIGKILL alone leaves index.lock)
// and its group is gone; HEAD stays where it was; and its outcome says so with the nextStep
// to finish or undo it in a terminal. A cancel after the write began is the root's outcome
// (OPERATION_CANCELLED, the call completes, as ADR-0053 §4 does for an item), not a bare
// cancelled operation. The control, in the same test: SIGKILL alone to the same hold leaves
// a lock. Seen failing with SIGKILL sent at once (stopGroup without its SIGTERM): the
// cancelled checkout left index.lock.
func TestWriteCancelLeavesNoLockCheckout(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newWfx(t)
	started := f.slowCheckout()
	before := f.e.rev(f.r.W, "HEAD")
	id := f.start("git.checkout", "ws", CheckoutParams{Name: "slowb"})
	pid := pidIn(t, started)
	time.Sleep(200 * time.Millisecond)
	held := locks(t, f.r.W)
	pgid := mustPgid(pid)
	if !groupAlive(pgid) {
		t.Fatal("the group is not alive before the cancel")
	}

	if err := f.ops.Cancel(id); err != nil {
		t.Fatal(err)
	}
	out := f.until(id, 15*time.Second)
	if out.State != capability.StateCompleted {
		t.Fatalf("outcome %s, error %+v; want completed with the root's cancelled outcome", out.State, out.Error)
	}
	res := decodeBranchResult(t, out)
	if len(res.Outcomes) != 1 || res.Outcomes[0].Status != "failed" || res.Outcomes[0].Error == nil || res.Outcomes[0].Error.Code != string(capability.ErrOperationCancelled) {
		t.Fatalf("outcomes %+v; want one failed OPERATION_CANCELLED outcome", res.Outcomes)
	}
	if ns := res.Outcomes[0].Error.NextStep; !strings.Contains(ns, "git status") || !strings.Contains(ns, "terminal") || !strings.Contains(ns, f.roots["ws"]) {
		t.Errorf("nextStep %q does not send the user to `git status` in a terminal in the root", ns)
	}
	if groupAlive(pgid) {
		t.Errorf("group %d is still alive", pgid)
	}
	if left := locks(t, f.r.W); len(left) > 0 {
		t.Errorf("the cancelled checkout left locks %v (held while it ran: %v)", left, held)
	}
	if f.head() != "main" || f.e.rev(f.r.W, "HEAD") != before {
		t.Errorf("HEAD moved to %q", f.head())
	}
	if recs := f.records(); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after the cancel", recs)
	}

	// The control: the same hold ended by SIGKILL alone.
	f2 := newWfx(t)
	started2 := f2.slowCheckout()
	id2 := f2.start("git.checkout", "ws", CheckoutParams{Name: "slowb"})
	pid2 := pidIn(t, started2)
	time.Sleep(200 * time.Millisecond)
	syscall.Kill(-mustPgid(pid2), syscall.SIGKILL)
	f2.until(id2, 15*time.Second)
	if left := locks(t, f2.r.W); len(left) == 0 {
		t.Logf("UNABLE TO DISCRIMINATE: SIGKILL alone left no lock either, so the absence above proves nothing about the signal order")
	} else {
		t.Logf("control: SIGKILL alone left %v", left)
	}
}

// Decision (ADR-0071 §3): a checkout that runs past its limit is stopped, whole group, no
// lock left, and is a failed outcome GIT_FAILED naming the limit, with the same nextStep as
// a cancel. The limit is shortened for the test.
func TestCheckoutTimeout(t *testing.T) {
	// The smudge has to start inside the limit: 700 ms did not, once, with two package runs
	// beside the pre-commit hook's; 5 s does, and the smudge sleeps 60.
	t.Parallel()
	f := newWfx(t)
	f.timeout = 5 * time.Second
	f.register()
	started := f.slowCheckout()
	res := f.branches("git.checkout", CheckoutParams{Name: "slowb"})
	if len(res.Outcomes) != 1 || res.Outcomes[0].Status != "failed" || res.Outcomes[0].Error == nil {
		t.Fatalf("outcomes %+v; want one failed outcome", res.Outcomes)
	}
	oe := res.Outcomes[0].Error
	if oe.Code != string(CodeFailed) || !strings.Contains(oe.Message, "did not finish") || !strings.Contains(oe.NextStep, "git status") {
		t.Errorf("error %+v; want GIT_FAILED naming the limit, with the git status nextStep", oe)
	}
	pid := pidIn(t, started)
	if syscall.Kill(pid, 0) != syscall.ESRCH || groupAlive(f.groups()[0]) {
		t.Errorf("the group %v (smudge %d) outlived the timed-out call", f.groups(), pid)
	}
	if left := locks(t, f.r.W); len(left) > 0 {
		t.Errorf("locks left: %v", left)
	}
	if f.head() != "main" {
		t.Errorf("HEAD is %q", f.head())
	}
}

func decodeBranchResult(t *testing.T, out capability.Outcome) BranchResult {
	t.Helper()
	res, ok := out.Result.(BranchResult)
	if !ok {
		t.Fatalf("result is %T, want BranchResult", out.Result)
	}
	return res
}

// Decision (ADR-0071 §1, §5; plan P1 fact 8): in a partial clone a branch whose blobs are
// missing cannot be checked out from here (the daemon never fetches objects by itself): a
// failed outcome with git's line, nothing changed, and the nextStep to fetch the missing
// objects once in a terminal.
func TestCheckoutPartialCloneNextStep(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newWfx(t)
	f.e.g(f.r.Origin, "config", "uploadpack.allowFilter", "true")
	f.e.g(f.r.Origin, "config", "uploadpack.allowAnySHA1InWant", "true")
	pc := filepath.Join(t.TempDir(), "pc")
	f.e.g(filepath.Dir(pc), "clone", "-q", "--filter=blob:none", "file://"+f.r.Origin, pc)
	f.e.g(pc, "branch", "-q", "fx", "origin/feature/x")
	f.roots["ws"] = platform.Current().Canonical(pc)
	f.register()
	before := treeState(t, f.e, pc)

	res := f.branches("git.checkout", CheckoutParams{Name: "fx"})
	if len(res.Outcomes) != 1 || res.Outcomes[0].Status != "failed" || res.Outcomes[0].Error == nil {
		t.Fatalf("outcomes %+v; want one failed outcome", res.Outcomes)
	}
	oe := res.Outcomes[0].Error
	if !strings.Contains(oe.NextStep, "terminal") || !strings.Contains(oe.NextStep, "partial clone") || !strings.Contains(oe.NextStep, f.roots["ws"]) {
		t.Errorf("nextStep %q does not send the user to fetch the missing objects in a terminal", oe.NextStep)
	}
	if after := treeState(t, f.e, pc); after != before {
		t.Errorf("the refused checkout changed the clone:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// A refusal that is not the partial clone's has no such nextStep.
	f2 := newWfx(t)
	f2.e.g(f2.r.W, "branch", "-q", "fx", "origin/feature/x")
	f2.e.write(f2.r.W, "shared.txt", "edit\n")
	r2 := f2.branches("git.checkout", CheckoutParams{Name: "fx"})
	if len(r2.Outcomes) != 1 || r2.Outcomes[0].Error == nil || r2.Outcomes[0].Error.NextStep != "" {
		t.Errorf("a refusal over local changes: %+v; want a failed outcome with no nextStep", r2.Outcomes)
	}
}

// Decision (ADR-0071 §6, plan P3, P8): a root that is no initialised submodule of the
// project is REQUEST_INVALID_PARAMS before any write git, whatever else roots holds: a path
// the repository records no gitlink at (this fixture has no submodule), a parent or absolute
// or dotted spelling, a root named twice. fromIn names no root of the call, or the root's
// own: also refused. (That a gitlink is accepted is TestSyncOrder's.) The control: roots
// ["."] and an absent roots are the same call. It was TestSubmoduleRootsBeforeP8, when
// every submodule path was refused.
func TestBranchRootsRefused(t *testing.T) {
	t.Parallel()
	f := newWfx(t)
	from := f.r.Oids["y"]
	f.e.g(f.r.W, "branch", "-q", "loc", "origin/y")
	for name, roots := range map[string][]string{
		"no gitlink there":  {"lib"},
		"the root and one":  {".", "lib"},
		"a nested path":     {"lib/nested"},
		"a parent path":     {".."},
		"an absolute path":  {f.roots["ws"]},
		"a repeated root":   {".", "."},
		"an empty path":     {""},
		"a dotted spelling": {"./"},
	} {
		if _, err := f.call("git.checkout", "ws", CheckoutParams{Name: "loc", Roots: roots}); err == nil || err.Code != capability.ErrInvalidParams || err.NextStep == "" {
			t.Errorf("checkout with %s: %+v, want REQUEST_INVALID_PARAMS with a nextStep", name, err)
		}
		if _, err := f.call("git.createBranch", "ws", CreateBranchParams{Name: "n", From: from, Roots: roots}); err == nil || err.Code != capability.ErrInvalidParams || err.NextStep == "" {
			t.Errorf("createBranch with %s: %+v, want REQUEST_INVALID_PARAMS with a nextStep", name, err)
		}
	}
	for name, fromIn := range map[string]map[string]string{
		"a submodule's start": {"lib": from},
		"the root's own":      {".": from},
	} {
		if _, err := f.call("git.createBranch", "ws", CreateBranchParams{Name: "n", From: from, FromIn: fromIn}); err == nil || err.Code != capability.ErrInvalidParams {
			t.Errorf("createBranch with fromIn %s: %+v, want REQUEST_INVALID_PARAMS", name, err)
		}
		if _, err := f.call("git.createBranch", "ws", CreateBranchParams{Name: "n", From: from, Roots: []string{"."}, FromIn: fromIn}); err == nil || err.Code != capability.ErrInvalidParams {
			t.Errorf("createBranch with roots [.] and fromIn %s: %+v, want REQUEST_INVALID_PARAMS", name, err)
		}
	}
	if n := f.writeStarts.Load(); n != 0 {
		t.Errorf("%d write gits started for refused roots", n)
	}
	wantDone(t, f.branches("git.checkout", CheckoutParams{Name: "loc", Roots: []string{"."}}))
	wantDone(t, f.branches("git.checkout", CheckoutParams{Name: "main"}))
}

// Decision (ADR-0071 §2): both writes are gated on the root's trust before the handler runs:
// WORKSPACE_UNTRUSTED with the trust nextStep and no git of any kind. Two gates: the hook
// refusing, and none at all (the golden registry).
func TestBranchWritesUntrustedStartNothing(t *testing.T) {
	t.Parallel()
	for _, hook := range []func(ctx context.Context, call *capability.Call) (any, error){
		func(ctx context.Context, call *capability.Call) (any, error) {
			return nil, policy.UntrustedError("/the/root")
		},
		nil,
	} {
		f := newWfxShared(t) // no git starts, so the fixture is only a directory
		f.authorize = hook
		f.register()
		for name, params := range map[string]any{
			"git.checkout":     CheckoutParams{Name: "main"},
			"git.createBranch": CreateBranchParams{Name: "n", From: f.r.Oids["y"], Checkout: true},
		} {
			out := f.until(f.start(name, "ws", params), 10*time.Second)
			if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != capability.ErrWorkspaceUntrusted {
				t.Fatalf("%s: %s, error %+v; want failed with WORKSPACE_UNTRUSTED", name, out.State, out.Error)
			}
			if !strings.Contains(out.Error.NextStep, "droidyard trust") {
				t.Errorf("%s: nextStep %q does not say how to trust the project", name, out.Error.NextStep)
			}
		}
		if w, r := f.writeStarts.Load(), f.readStarts.Load(); w != 0 || r != 0 {
			t.Errorf("starts: %d write gits and %d reads on an untrusted root, want none", w, r)
		}
	}
}

// Decision (ADR-0071 §1): a root that is no repository is GIT_NOT_A_REPOSITORY from the
// remotes' listing, before any write; no git is GIT_NOT_FOUND.
func TestBranchWritesNotARepositoryAndNoGit(t *testing.T) {
	t.Parallel()
	f := newWfxBare(t) // "ws" is an empty directory: no repository
	_, err := f.call("git.checkout", "ws", CheckoutParams{Name: "main"})
	wantErrCode(t, err, CodeNotARepository)
	_, err = f.call("git.createBranch", "ws", CreateBranchParams{Name: "n", From: strings.Repeat("a", 40)})
	wantErrCode(t, err, CodeNotARepository)
	f.look = func(string) (string, error) { return "", os.ErrNotExist }
	f.register()
	_, err = f.call("git.checkout", "ws", CheckoutParams{Name: "main"})
	wantErrCode(t, err, CodeNotFound)
	if n := f.writeStarts.Load(); n != 0 {
		t.Errorf("%d write gits started", n)
	}
}
