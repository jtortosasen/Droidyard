package git

// Synchronised branches (plan m2n part 2, step P8; ADR-0071 §6, plan Q6 (1)): git.checkout
// and git.createBranch over `roots` that name the project and its submodules. Real git over
// gittest.Submodules (the superproject `s`, its submodules `lib` and `lib/nested`
// initialised and `extra` not), through the real supervisor's recorded spawner. Each test
// says which decision it holds and what it was seen failing under.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/git/gittest"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// sfx is the write fixture over the superproject.
type sfx struct {
	*wfx
	s *gittest.Subs
	// ws is the canonical root the daemon runs its gits below.
	ws string
}

// newSfx registers the git capabilities over the superproject `s`, with the branch
// feature/x at each initialised root's HEAD (so a checkout of it moves HEAD from main in the
// superproject and from the detached recorded commit in each submodule).
func newSfx(t *testing.T) *sfx {
	t.Helper()
	f := newWfx(t)
	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "subs"))
	f.roots["ws"] = platform.Current().Canonical(s.Super)
	f.register()
	x := &sfx{wfx: f, s: s, ws: f.roots["ws"]}
	for _, rel := range []string{".", "lib", "lib/nested"} {
		x.e.g(x.dir(rel), "branch", "-q", "feature/x")
	}
	return x
}

// dir is the work tree of a root, as the test writes it ("." is the superproject).
func (x *sfx) dir(rel string) string { return filepath.Join(x.s.Super, filepath.FromSlash(rel)) }

// on is the branch a root has checked out, or "(detached)".
func (x *sfx) on(rel string) string {
	out, err := x.e.gErr(x.dir(rel), "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return "(detached)"
	}
	return strings.TrimSpace(out)
}

// heads is the checked-out branch of the three roots, in the order ".", "lib", "lib/nested".
func (x *sfx) heads() []string { return []string{x.on("."), x.on("lib"), x.on("lib/nested")} }

// outcomeRows reads a result as "root:status" rows, in the order given.
func outcomeRows(res BranchResult) []string {
	var rows []string
	for _, o := range res.Outcomes {
		rows = append(rows, o.Root+":"+o.Status)
	}
	return rows
}

// wantRefused asserts a whole-call refusal: REQUEST_INVALID_PARAMS with a nextStep, which
// names the submodule when want is not empty.
func wantRefused(t *testing.T, label string, err *capability.Error, want string) {
	t.Helper()
	switch {
	case err == nil:
		t.Errorf("%s: the call was not refused", label)
	case err.Code != capability.ErrInvalidParams || err.NextStep == "":
		t.Errorf("%s: %s %q (nextStep %q), want REQUEST_INVALID_PARAMS with a nextStep", label, err.Code, err.Message, err.NextStep)
	case want != "" && !strings.Contains(err.Message+err.Cause, want):
		t.Errorf("%s: %q (cause %q) does not name %q", label, err.Message, err.Cause, want)
	}
}

// Decision (ADR-0071 §6): the roots are applied "." first and then the submodules depth-first
// by path, whatever order they were given, and the result lists them in that order. The
// call gives them shuffled, deepest first; the order the write gits started in (the
// directories they ran in) is the order applied, and every root is on the branch after. The
// control is the state before: no root is on feature/x. Seen failing with the sort in
// branchRoots removed: the outcomes came back lib/nested, ., lib.
func TestSyncOrder(t *testing.T) {
	t.Log(gitVersion(t))
	x := newSfx(t)
	if h := x.heads(); h[0] == "feature/x" || h[1] == "feature/x" || h[2] == "feature/x" {
		t.Fatalf("control: heads %v already on feature/x", h)
	}

	res := x.branches("git.checkout", CheckoutParams{Name: "feature/x", Roots: []string{"lib/nested", ".", "lib"}})
	if got, want := outcomeRows(res), []string{".:done", "lib:done", "lib/nested:done"}; !slices.Equal(got, want) {
		t.Errorf("outcomes %v, want %v", got, want)
	}
	if got, want := x.heads(), []string{"feature/x", "feature/x", "feature/x"}; !slices.Equal(got, want) {
		t.Errorf("heads %v, want %v", got, want)
	}
	if got, want := x.writeDirs(), []string{x.ws, filepath.Join(x.ws, "lib"), filepath.Join(x.ws, "lib", "nested")}; !slices.Equal(got, want) {
		t.Errorf("the write gits ran in %v, want %v", got, want)
	}
}

// Decision (ADR-0071 §6): depth-first is a walk of the tree, so a directory comes before
// what is below it and before its siblings' names that sort after it as strings ("lib-x"
// follows "lib/nested", though '-' sorts before '/'); the workspace's own repository is
// first. Pure: the roots are only ordered here, none resolved. Seen failing with plain
// string order: "lib-x" came before "lib/nested".
func TestBranchRootsOrder(t *testing.T) {
	rels, cerr := branchRoots([]string{"lib-x", "lib/nested/deep", "z", ".", "lib/nested", "lib", "a"}, nil)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if want := []string{"", "a", "lib", "lib/nested", "lib/nested/deep", "lib-x", "z"}; !slices.Equal(rels, want) {
		t.Errorf("order %q, want %q", rels, want)
	}
	// Roots without the project's own are applied as given, sorted: "." is not added.
	if rels, cerr := branchRoots([]string{"lib/nested", "lib"}, nil); cerr != nil || !slices.Equal(rels, []string{"lib", "lib/nested"}) {
		t.Errorf("without \".\": %q, %v", rels, cerr)
	}
}

// Decision (ADR-0071 §6): the first failed root stops the call and the rest are skipped;
// nothing is rolled back. lib has local changes to a file that feature/x changes: its switch
// is refused with git's words; "." was switched before it and stays on the branch; lib is
// where it was with its edit intact; lib/nested is skipped and no git ran in it. The
// control is the same call once the edit is undone: all three done. Seen failing with the
// loop continuing after a failed root (stop never set): lib/nested was switched.
func TestSyncStopsAtFirstFailure(t *testing.T) {
	t.Log(gitVersion(t))
	x := newSfx(t)
	lib := x.dir("lib")
	x.e.g(lib, "switch", "-q", "feature/x")
	x.e.write(lib, "lib.txt", "changed on the branch\n")
	x.e.commit(lib)
	x.e.g(lib, "switch", "-q", "--detach", x.s.Oids["lib2"])
	x.e.write(lib, "lib.txt", "my uncommitted edit\n")
	before := libState(x, lib)

	res := x.branches("git.checkout", CheckoutParams{Name: "feature/x", Roots: []string{".", "lib", "lib/nested"}})
	if got, want := outcomeRows(res), []string{".:done", "lib:failed", "lib/nested:skipped"}; !slices.Equal(got, want) {
		t.Fatalf("outcomes %v, want %v", got, want)
	}
	oe := res.Outcomes[1].Error
	if oe == nil || oe.Code != string(CodeFailed) || !strings.Contains(oe.Cause, "lib.txt") {
		t.Errorf("lib's error %+v; want GIT_FAILED with git's cause naming lib.txt", oe)
	}
	if res.Outcomes[0].Error != nil || res.Outcomes[2].Error != nil {
		t.Errorf("errors on the done or skipped roots: %+v", res.Outcomes)
	}
	if got, want := x.heads(), []string{"feature/x", "(detached)", "(detached)"}; !slices.Equal(got, want) {
		t.Errorf("heads %v, want %v: the superproject stays on the new branch, nothing is rolled back", got, want)
	}
	if after := libState(x, lib); after != before {
		t.Errorf("the refused switch changed lib:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if got, want := x.writeDirs(), []string{x.ws, filepath.Join(x.ws, "lib")}; !slices.Equal(got, want) {
		t.Errorf("the write gits ran in %v, want %v: none in lib/nested", got, want)
	}

	x.e.g(lib, "checkout", "-q", "--", "lib.txt")
	if got := outcomeRows(x.branches("git.checkout", CheckoutParams{Name: "feature/x", Roots: []string{".", "lib", "lib/nested"}})); !slices.Equal(got, []string{".:done", "lib:done", "lib/nested:done"}) {
		t.Errorf("the control, once the edit is undone: %v", got)
	}
}

// libState is what "lib is as it was" is compared on: its HEAD, its status and its file.
func libState(x *sfx, dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "lib.txt"))
	return x.e.rev(dir, "HEAD") + "\n" + x.e.g(dir, "status", "--porcelain") + string(b)
}

// Decision (ADR-0071 §1, plan Q6 (1)): a new branch starts, in each root, at that root's own
// commit: "." at from, a submodule at its entry of fromIn. Each start is an older commit
// than the root's HEAD, so a branch made at HEAD would differ; without checkout no HEAD
// moves, with it each root's does. Seen failing with every root started at from: lib and
// lib/nested refused it as no commit of theirs.
func TestSyncCreateBranchPerRootFrom(t *testing.T) {
	t.Log(gitVersion(t))
	x := newSfx(t)
	from := x.e.rev(x.dir("."), "HEAD~1")
	starts := map[string]string{".": from, "lib": x.s.Oids["lib1"], "lib/nested": x.s.Oids["nested1"]}
	for rel, oid := range starts {
		if x.e.rev(x.dir(rel), "HEAD") == oid {
			t.Fatalf("control: %s's HEAD is already its start", rel)
		}
	}
	roots := []string{"lib/nested", ".", "lib"}
	fromIn := map[string]string{"lib": starts["lib"], "lib/nested": starts["lib/nested"]}
	headsBefore := x.heads()

	res := x.branches("git.createBranch", CreateBranchParams{Name: "sync/new", Checkout: false, From: from, Roots: roots, FromIn: fromIn})
	if got, want := outcomeRows(res), []string{".:done", "lib:done", "lib/nested:done"}; !slices.Equal(got, want) {
		t.Fatalf("outcomes %v, want %v", got, want)
	}
	for rel, oid := range starts {
		if got := x.e.rev(x.dir(rel), "sync/new"); got != oid {
			t.Errorf("%s: sync/new is at %s, want %s", rel, got, oid)
		}
	}
	if got := x.heads(); !slices.Equal(got, headsBefore) {
		t.Errorf("heads %v, want %v: checkout was false", got, headsBefore)
	}

	res = x.branches("git.createBranch", CreateBranchParams{Name: "sync/switched", Checkout: true, From: from, Roots: roots, FromIn: fromIn})
	if got, want := outcomeRows(res), []string{".:done", "lib:done", "lib/nested:done"}; !slices.Equal(got, want) {
		t.Fatalf("outcomes %v, want %v", got, want)
	}
	if got, want := x.heads(), []string{"sync/switched", "sync/switched", "sync/switched"}; !slices.Equal(got, want) {
		t.Errorf("heads %v, want %v", got, want)
	}
	for rel, oid := range starts {
		if got := x.e.rev(x.dir(rel), "HEAD"); got != oid {
			t.Errorf("%s: HEAD is %s, want its start %s", rel, got, oid)
		}
	}
}

// Decision (ADR-0071 §1, §6, plan Q6 (1)): everything readable is checked in every root
// before the first write, so these leave no root changed and start no write git: a submodule
// root with no fromIn entry; an entry that is another repository's commit, a short id or not
// an id; an entry for a path that is no root, or for "."; a name that one submodule already
// has. The refusal of the last names the submodule. The control: the same call, right, is
// done in all three roots. Seen failing with the checks of a submodule done after "." was
// written: the superproject had the branch after the refusal.
func TestSyncCreateBranchRefused(t *testing.T) {
	t.Log(gitVersion(t))
	x := newSfx(t)
	from := x.e.rev(x.dir("."), "HEAD~1")
	roots := []string{".", "lib", "lib/nested"}
	good := map[string]string{"lib": x.s.Oids["lib1"], "lib/nested": x.s.Oids["nested1"]}
	with := func(over map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range good {
			m[k] = v
		}
		for k, v := range over {
			if v == "-" {
				delete(m, k)
			} else {
				m[k] = v
			}
		}
		return m
	}
	// A branch only lib/nested has: the last root of the order, so the roots before it have
	// passed their checks when it is refused.
	x.e.g(x.dir("lib/nested"), "branch", "-q", "taken")
	cases := []struct {
		label, name string
		fromIn      map[string]string
		roots       []string
		want        string
	}{
		{"no entry for lib", "n1", with(map[string]string{"lib": "-"}), roots, "lib"},
		{"another repository's commit", "n1", with(map[string]string{"lib": x.s.Oids["nested1"]}), roots, `fromIn["lib"]`},
		{"a short id", "n1", with(map[string]string{"lib/nested": x.s.Oids["nested1"][:12]}), roots, `fromIn["lib/nested"]`},
		{"not an id", "n1", with(map[string]string{"lib/nested": "main"}), roots, `fromIn["lib/nested"]`},
		{"a tree's id", "n1", with(map[string]string{"lib": x.e.rev(x.dir("lib"), "HEAD^{tree}")}), roots, "lib"},
		{"an entry for a path not in roots", "n1", with(map[string]string{"extra": from}), roots, "extra"},
		{"an entry for the project", "n1", with(map[string]string{".": from}), roots, ""},
		{"a name a submodule has", "taken", good, roots, "lib/nested"},
	}
	for _, c := range cases {
		_, err := x.call("git.createBranch", "ws", CreateBranchParams{Name: c.name, Checkout: true, From: from, Roots: c.roots, FromIn: c.fromIn})
		wantRefused(t, c.label, err, c.want)
	}
	if n := x.writeStarts.Load(); n != 0 {
		t.Errorf("%d write gits started for refused calls", n)
	}
	for _, rel := range []string{".", "lib", "lib/nested"} {
		if x.e.revOr(x.dir(rel), "n1") != "" {
			t.Errorf("%s has the branch n1 after refused calls", rel)
		}
	}
	if got, want := x.heads(), []string{"main", "(detached)", "(detached)"}; !slices.Equal(got, want) {
		t.Errorf("heads %v, want %v", got, want)
	}
	res := x.branches("git.createBranch", CreateBranchParams{Name: "n1", Checkout: true, From: from, Roots: roots, FromIn: good})
	if got, want := outcomeRows(res), []string{".:done", "lib:done", "lib/nested:done"}; !slices.Equal(got, want) {
		t.Errorf("the control: %v, want %v", got, want)
	}
}

// Decision (ADR-0071 §6, ADR-0069 §2): a root that is not a gitlink of the project's
// repository, or not initialised, refuses the whole call, "." included: the project is
// listed before it and no write git has started. An uninitialised submodule, a plain
// directory, a path that is no directory and spellings that leave the project are each
// REQUEST_INVALID_PARAMS for both capabilities. The control: the valid roots alone are done.
// Seen failing with the roots resolved while applying: "." was on feature/x after the refusal.
func TestSyncRootRefused(t *testing.T) {
	t.Log(gitVersion(t))
	x := newSfx(t)
	x.e.write(x.dir("."), "plain/file.txt", "a directory that is no repository\n")
	for label, bad := range map[string]string{
		"an uninitialised submodule": "extra",
		"a plain directory":          "plain",
		"a path that is not there":   "nowhere",
		"a file":                     "README.md",
		"a path out of the project":  "../s",
		"an absolute path":           x.ws,
		"a dotted spelling":          "lib/",
		"inside the git directory":   "lib/.git",
		"a nested path of a plain":   "plain/lib",
	} {
		roots := []string{".", "lib", bad}
		_, err := x.call("git.checkout", "ws", CheckoutParams{Name: "feature/x", Roots: roots})
		wantRefused(t, "checkout, "+label, err, "")
		_, err = x.call("git.createBranch", "ws", CreateBranchParams{Name: "n", From: x.s.Oids["super"], Roots: roots, FromIn: map[string]string{"lib": x.s.Oids["lib1"], bad: x.s.Oids["lib1"]}})
		wantRefused(t, "createBranch, "+label, err, "")
	}
	if n := x.writeStarts.Load(); n != 0 {
		t.Errorf("%d write gits started for refused roots", n)
	}
	if got, want := x.heads(), []string{"main", "(detached)", "(detached)"}; !slices.Equal(got, want) {
		t.Errorf("heads %v, want %v: a refused call applies nothing", got, want)
	}
	if got := outcomeRows(x.branches("git.checkout", CheckoutParams{Name: "feature/x", Roots: []string{".", "lib"}})); !slices.Equal(got, []string{".:done", "lib:done"}) {
		t.Errorf("the control: %v", got)
	}
}

// Decision (ADR-0071 §6, plan P-1 S4): a checkout of a branch that one root lacks is
// REQUEST_INVALID_PARAMS for the whole call, which names the root; nothing is applied and
// no write git starts, though "." and lib/nested have the branch. The control: once lib has
// it, the same call is done in all three. Seen failing with the presence checked only in
// ".": the superproject was switched and lib answered a failed outcome.
func TestSyncBranchMissingInARoot(t *testing.T) {
	t.Log(gitVersion(t))
	x := newSfx(t)
	x.e.g(x.dir("lib"), "branch", "-q", "-D", "feature/x")
	roots := []string{".", "lib", "lib/nested"}

	_, err := x.call("git.checkout", "ws", CheckoutParams{Name: "feature/x", Roots: roots})
	wantRefused(t, "checkout", err, "lib")
	if err != nil && !strings.Contains(err.Message, "in the submodule lib") {
		t.Errorf("message %q does not say which submodule lacks the branch", err.Message)
	}
	// A remote does not help where the remote-tracking branch is missing too.
	_, err = x.call("git.checkout", "ws", CheckoutParams{Name: "feature/x", Remote: "origin", Roots: roots})
	wantRefused(t, "checkout with a remote", err, "lib")
	if n := x.writeStarts.Load(); n != 0 {
		t.Errorf("%d write gits started", n)
	}
	if got, want := x.heads(), []string{"main", "(detached)", "(detached)"}; !slices.Equal(got, want) {
		t.Errorf("heads %v, want %v: nothing is applied", got, want)
	}

	x.e.g(x.dir("lib"), "branch", "-q", "feature/x")
	if got := outcomeRows(x.branches("git.checkout", CheckoutParams{Name: "feature/x", Roots: roots})); !slices.Equal(got, []string{".:done", "lib:done", "lib/nested:done"}) {
		t.Errorf("the control: %v", got)
	}
}

// Decision (ADR-0071 §1, §6): a remote-tracking branch is made a local one in each root
// that lacks it, from that root's own remote; a root without the remote, or without the
// branch on it, refuses the whole call before any write. feature/y is pushed from the
// superproject and lib only, so it is remote-tracking there and absent in lib/nested.
func TestSyncRemoteTrackingPerRoot(t *testing.T) {
	t.Log(gitVersion(t))
	x := newSfx(t)
	for _, rel := range []string{".", "lib"} {
		x.e.g(x.dir(rel), "branch", "-q", "feature/y")
		x.e.g(x.dir(rel), "push", "-q", "origin", "feature/y")
		x.e.g(x.dir(rel), "branch", "-q", "-D", "feature/y")
	}
	_, err := x.call("git.checkout", "ws", CheckoutParams{Name: "feature/y", Remote: "origin", Roots: []string{".", "lib", "lib/nested"}})
	wantRefused(t, "a root without the branch", err, "lib/nested")
	if n := x.writeStarts.Load(); n != 0 {
		t.Errorf("%d write gits started", n)
	}

	res := x.branches("git.checkout", CheckoutParams{Name: "feature/y", Remote: "origin", Roots: []string{"lib", "."}})
	if got, want := outcomeRows(res), []string{".:done", "lib:done"}; !slices.Equal(got, want) {
		t.Fatalf("outcomes %v, want %v", got, want)
	}
	for _, rel := range []string{".", "lib"} {
		if up := strings.TrimSpace(x.e.g(x.dir(rel), "rev-parse", "--abbrev-ref", "feature/y@{upstream}")); up != "origin/feature/y" {
			t.Errorf("%s: feature/y tracks %q, want origin/feature/y", rel, up)
		}
		if x.on(rel) != "feature/y" {
			t.Errorf("%s is on %q", rel, x.on(rel))
		}
	}
}

// Decision (ADR-0071 §4, §6): one repository key for the whole call. A fetch started while
// the checkout is in lib (the second root, after "." was switched) is queued behind the
// checkout, names it in blockedBy and starts no git; it runs once the checkout has ended,
// after both roots. lib's switch is held by a smudge filter on a gate. Seen failing with no
// key on the writes (Resource empty): the fetch started at once.
func TestSyncOneKey(t *testing.T) {
	t.Log(gitVersion(t))
	x := newSfx(t)
	lib := x.dir("lib")
	x.e.g(lib, "switch", "-q", "feature/x")
	x.e.write(lib, ".gitattributes", "slow.txt filter=slow\n")
	x.e.write(lib, "slow.txt", "slow\n")
	x.e.commit(lib)
	x.e.g(lib, "switch", "-q", "--detach", x.s.Oids["lib2"])
	started, gate := filepath.Join(t.TempDir(), "smudge.pid"), filepath.Join(t.TempDir(), "smudge.gate")
	t.Cleanup(func() { os.WriteFile(gate, nil, 0o644) })
	x.e.g(lib, "config", "filter.slow.smudge", x.e.script("smudge.sh", "echo $$ > '"+started+"'\nwhile [ ! -f '"+gate+"' ]; do sleep 0.05; done\ncat"))

	checkout := x.start("git.checkout", "ws", CheckoutParams{Name: "feature/x", Roots: []string{".", "lib"}})
	waitFile(t, started, 30*time.Second)
	fetch := x.start("git.fetch", "ws", FetchParams{})
	out := x.snap(fetch)
	if out.State != capability.StateQueued || out.BlockedBy == nil || out.BlockedBy.OperationID != checkout {
		t.Fatalf("fetch: %s, blockedBy %+v; want queued behind the checkout %s", out.State, out.BlockedBy, checkout)
	}
	libWs := filepath.Join(x.ws, "lib")
	if got, want := x.writeDirs(), []string{x.ws, libWs}; !slices.Equal(got, want) {
		t.Errorf("the write gits started in %v with the checkout in lib, want only %v", got, want)
	}
	if x.on(".") != "feature/x" {
		t.Errorf("the superproject is on %q while lib is held, want feature/x", x.on("."))
	}

	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	co := x.until(checkout, 60*time.Second)
	if co.State != capability.StateCompleted {
		t.Fatalf("checkout: %s, %+v", co.State, co.Error)
	}
	if got, want := outcomeRows(decodeBranchResult(t, co)), []string{".:done", "lib:done"}; !slices.Equal(got, want) {
		t.Errorf("outcomes %v, want %v", got, want)
	}
	if fo := x.until(fetch, 60*time.Second); fo.State != capability.StateCompleted {
		t.Errorf("fetch: %s, %+v; want completed after the checkout", fo.State, fo.Error)
	}
	if got, want := x.writeDirs(), []string{x.ws, libWs, x.ws}; !slices.Equal(got, want) {
		t.Errorf("the write gits ran in %v, want the fetch last: %v", got, want)
	}
}
