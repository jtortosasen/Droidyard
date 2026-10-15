package git

// P3's fix round (verifier): an empty roots, the outcomes of a cancel between roots, an exact
// branch lookup, a new branch refused over local changes. Same fixtures and harness as
// checkout_test.go.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// Decision (ADR-0071 §6; the descriptions say absent roots mean ["."]): an empty list is not
// absent, it names no repository, and is REQUEST_INVALID_PARAMS before any git. It is sent as
// raw JSON, because the struct's omitempty would drop an empty slice. Seen failing with the
// empty-list check removed: the call ran as ["."].
func TestEmptyRootsRefused(t *testing.T) {
	t.Parallel()
	f := newWfx(t)
	f.e.g(f.r.W, "branch", "-q", "loc", "origin/y")
	for name, params := range map[string]string{
		"git.checkout":     `{"name":"loc","roots":[]}`,
		"git.createBranch": `{"name":"n","checkout":false,"from":"` + f.r.Oids["y"] + `","roots":[]}`,
	} {
		id, err := f.ops.Start(context.Background(), capability.Request{Capability: name, Workspace: "ws", Params: json.RawMessage(params)})
		if err != nil {
			t.Fatalf("%s: Start: %v", name, err)
		}
		out := f.until(id, 10*time.Second)
		if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != capability.ErrInvalidParams || out.Error.NextStep == "" {
			t.Errorf("%s with roots []: %s, %+v; want failed REQUEST_INVALID_PARAMS with a nextStep", name, out.State, out.Error)
		}
	}
	if n := f.writeStarts.Load(); n != 0 {
		t.Errorf("%d write gits started", n)
	}
	// The control: roots absent is ["."].
	wantDone(t, f.branches("git.checkout", CheckoutParams{Name: "loc"}))
}

// Decision (ADR-0053 §4's item pattern, ADR-0071 §6): the outcomes a call builds when it
// stops. `skipped` is only "an earlier root failed"; after a cancel the roots git did not
// reach are failed OPERATION_CANCELLED. Several roots are real only from step P8, but the
// shape is published now, so apply is driven with a fake write: a cancel between roots, a
// cancel while the second root's git runs, a refusal followed by a cancel (the rest stay
// skipped), a timeout, and a cancel before the first root (the call's own cancel). Seen
// failing with the cancelled roots marked skipped.
func TestApplyOutcomesOfACancelAndOfAFailure(t *testing.T) {
	t.Parallel()
	steps := []branchStep{
		{root: ".", dir: "/r", switches: true},
		{root: "lib", dir: "/r/lib", switches: true},
		{root: "lib/nested", dir: "/r/lib/nested", switches: true},
	}
	done := func() (result, error) { return result{}, nil }
	// run drives apply with write(i, cancel) answering the i-th root's git (from 1).
	run := func(ctx context.Context, cancel context.CancelFunc, write func(i int, cancel context.CancelFunc) (result, error)) (BranchResult, error) {
		i := 0
		b := &branchCall{exec: func(ctx context.Context, dir string, timeout time.Duration, args []string) (result, error) {
			i++
			return write(i, cancel)
		}}
		res, err := b.apply(ctx, steps, "git refused the checkout")
		if err != nil {
			return BranchResult{}, err
		}
		return res.(BranchResult), nil
	}
	summary := func(r BranchResult) string {
		var parts []string
		for _, o := range r.Outcomes {
			s := o.Root + "=" + o.Status
			if o.Error != nil {
				s += "/" + o.Error.Code
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, " ")
	}
	wantSummary := func(name string, r BranchResult, want string) {
		t.Helper()
		if got := summary(r); got != want {
			t.Errorf("%s: outcomes %q, want %q", name, got, want)
		}
	}

	// A cancel lands after the first root is done: the other two are failed, cancelled, not skipped.
	ctx, cancel := context.WithCancel(context.Background())
	res, err := run(ctx, cancel, func(i int, cancel context.CancelFunc) (result, error) {
		cancel()
		return done()
	})
	if err != nil {
		t.Fatal(err)
	}
	wantSummary("cancel between roots", res, ".=done lib=failed/OPERATION_CANCELLED lib/nested=failed/OPERATION_CANCELLED")
	if res.Outcomes[1].Error.NextStep != "" {
		t.Errorf("a root git never reached has a nextStep %q: there is nothing to finish or undo there", res.Outcomes[1].Error.NextStep)
	}

	// A cancel ends the second root's git: that root says so with the terminal nextStep; the third was not reached.
	ctx, cancel = context.WithCancel(context.Background())
	res, err = run(ctx, cancel, func(i int, cancel context.CancelFunc) (result, error) {
		if i == 2 {
			cancel()
			return result{ExitCode: -1}, context.Canceled
		}
		return done()
	})
	if err != nil {
		t.Fatal(err)
	}
	wantSummary("cancel while a root writes", res, ".=done lib=failed/OPERATION_CANCELLED lib/nested=failed/OPERATION_CANCELLED")
	if ns := res.Outcomes[1].Error.NextStep; !strings.Contains(ns, "git status") || !strings.Contains(ns, "/r/lib") {
		t.Errorf("nextStep %q does not send the user to git status in the root that was being written", ns)
	}

	// A refusal stops the call: the rest are skipped, also when a cancel follows it.
	ctx, cancel = context.WithCancel(context.Background())
	res, err = run(ctx, cancel, func(i int, cancel context.CancelFunc) (result, error) {
		if i == 2 {
			cancel()
			return result{ExitCode: 1, Stderr: []byte("error: Your local changes would be overwritten:\n\tx.txt\n")}, nil
		}
		return done()
	})
	if err != nil {
		t.Fatal(err)
	}
	wantSummary("refusal then cancel", res, ".=done lib=failed/GIT_FAILED lib/nested=skipped")

	// A timeout is a failure of its root, GIT_FAILED with the fixed text as the cause: the rest are skipped.
	res, err = run(context.Background(), func() {}, func(i int, cancel context.CancelFunc) (result, error) {
		if i == 1 {
			return result{ExitCode: -1}, ErrTimeout
		}
		return done()
	})
	if err != nil {
		t.Fatal(err)
	}
	wantSummary("timeout", res, ".=failed/GIT_FAILED lib=skipped lib/nested=skipped")
	if res.Outcomes[0].Error.Cause != ErrTimeout.Error() || !strings.Contains(res.Outcomes[0].Error.NextStep, "git status") {
		t.Errorf("timeout error %+v", res.Outcomes[0].Error)
	}

	// A cancel before any root was tried is the call's own cancel, not an outcome.
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := run(ctx, cancel, func(i int, cancel context.CancelFunc) (result, error) { return done() }); err != context.Canceled {
		t.Errorf("a cancel before the first root: %v, want context.Canceled", err)
	}
}

// Decision (ADR-0071 §6, ADR-0053 §2): once a root has been written the call has outcomes to
// give, so an error that is not git's refusal (git or the repository gone, git not started)
// is that root's failed outcome with its own code and message, the rest skipped, and the call
// completes; before the first write the same error is the call's own. Driven through the
// exec seam. Seen failing with the old `return nil, err`: the call lost the done root.
func TestApplyErrorsAfterAWrite(t *testing.T) {
	t.Parallel()
	steps := []branchStep{{root: ".", dir: "/r"}, {root: "lib", dir: "/r/lib"}, {root: "lib/nested", dir: "/r/lib/nested"}}
	apply := func(failAt int, res result, err error) (BranchResult, error) {
		i := 0
		b := &branchCall{exec: func(context.Context, string, time.Duration, []string) (result, error) {
			i++
			if i == failAt {
				return res, err
			}
			return result{}, nil
		}}
		out, aerr := b.apply(context.Background(), steps, "git refused the checkout")
		if aerr != nil {
			return BranchResult{}, aerr
		}
		return out.(BranchResult), nil
	}
	notARepo := result{ExitCode: 128, Stderr: []byte("fatal: not a git repository (or any of the parent directories): .git\n")}
	for name, c := range map[string]struct {
		res  result
		err  error
		code capability.ErrorCode
	}{
		"the repository is gone": {notARepo, nil, CodeNotARepository},
		"git is gone":            {result{ExitCode: -1}, ErrNotFound, CodeNotFound},
		"git could not start":    {result{ExitCode: -1}, ErrNoStarter, CodeFailed},
	} {
		// After a write: lib's outcome, lib/nested skipped, the call completed.
		got, err := apply(2, c.res, c.err)
		if err != nil {
			t.Errorf("%s, after a write: the call failed with %v, want its outcomes", name, err)
			continue
		}
		var rows []string
		for _, o := range got.Outcomes {
			rows = append(rows, o.Root+"="+o.Status)
		}
		if strings.Join(rows, " ") != ".=done lib=failed lib/nested=skipped" {
			t.Errorf("%s: outcomes %v", name, rows)
			continue
		}
		if e := got.Outcomes[1].Error; e == nil || e.Code != string(c.code) || e.Message == "" {
			t.Errorf("%s: error %+v, want code %s with a message", name, e, c.code)
		}
		// Before the first write: the call's own error, with the same code.
		_, err = apply(1, c.res, c.err)
		if ce := capability.AsError(err); err == nil || ce.Code != c.code {
			t.Errorf("%s, before a write: %v, want the call's error %s", name, err, c.code)
		}
	}
}

// Decision (ADR-0071 §1): a branch exists only as refs/heads/<name>. A tag spelled
// refs/heads/ghost lives at refs/tags/refs/heads/ghost, where `rev-parse --verify
// refs/heads/ghost` also finds it; the lookup must not (show-ref --verify is exact). So a
// new branch `ghost` is made, and a checkout of `ghost` finds no branch. Seen failing with
// rev-parse in refExists: the new branch was refused as existing.
func TestBranchLookupIsExact(t *testing.T) {
	t.Parallel()
	f := newWfx(t)
	f.e.g(f.r.W, "tag", "refs/heads/ghost", "main")
	if f.e.revOr(f.r.W, "refs/heads/ghost") == "" {
		t.Fatal("control: rev-parse does not find the tag under refs/heads/ghost, so the test cannot fail")
	}
	_, err := f.call("git.checkout", "ws", CheckoutParams{Name: "ghost"})
	if err == nil || err.Code != capability.ErrInvalidParams {
		t.Errorf("checkout of ghost: %+v, want REQUEST_INVALID_PARAMS (no such branch)", err)
	}
	wantDone(t, f.branches("git.createBranch", CreateBranchParams{Name: "ghost", From: f.r.Oids["y"]}))
	if got := strings.TrimSpace(f.e.g(f.r.W, "for-each-ref", "--format=%(objectname)", "refs/heads/ghost")); got != f.r.Oids["y"] {
		t.Errorf("refs/heads/ghost is %s, want the new branch at %s", got, f.r.Oids["y"])
	}
}

// Decision (ADR-0071 §5): a new branch that is checked out, refused by git over conflicting
// local changes, is a failed outcome, GIT_FAILED with the cause naming the file; the work tree
// and refs are as they were and no branch is left behind (`switch --create` creates the ref
// only once the switch can go ahead: plan P1 fact 7). The control is the same call once the
// file is restored.
func TestCreateBranchWithCheckoutRefusedOverLocalChanges(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newWfx(t)
	from := f.r.Oids["feature/x"]
	f.e.write(f.r.W, "shared.txt", "my uncommitted edit\n")
	before := treeState(t, f.e, f.r.W)

	res := f.branches("git.createBranch", CreateBranchParams{Name: "wanted", Checkout: true, From: from})
	if len(res.Outcomes) != 1 || res.Outcomes[0].Status != "failed" || res.Outcomes[0].Error == nil {
		t.Fatalf("outcomes %+v; want one failed outcome", res.Outcomes)
	}
	oe := res.Outcomes[0].Error
	if oe.Code != string(CodeFailed) || !strings.Contains(oe.Cause, "shared.txt") {
		t.Errorf("error %+v; want GIT_FAILED with a cause naming shared.txt", oe)
	}
	if f.e.revOr(f.r.W, "refs/heads/wanted") != "" {
		t.Error("the refused call left the branch behind")
	}
	if after := treeState(t, f.e, f.r.W); after != before {
		t.Errorf("the refused call changed the repository:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if left := locks(t, f.r.W); len(left) > 0 {
		t.Errorf("locks left: %v", left)
	}

	f.e.g(f.r.W, "checkout", "-q", "--", "shared.txt")
	wantDone(t, f.branches("git.createBranch", CreateBranchParams{Name: "wanted", Checkout: true, From: from}))
}

// Decision (ADR-0071 §5): the refusal of a name that starts with a remote's name says what to
// do for a creation and for a checkout alike: choose another name, and a branch already named
// so is renamed in a terminal.
func TestRemotePrefixRefusalSaysWhatToDo(t *testing.T) {
	t.Parallel()
	f := newWfx(t)
	for _, call := range []func() *capability.Error{
		func() *capability.Error {
			_, e := f.call("git.checkout", "ws", CheckoutParams{Name: "origin/x"})
			return e
		},
		func() *capability.Error {
			_, e := f.call("git.createBranch", "ws", CreateBranchParams{Name: "origin/x", From: f.r.Oids["y"]})
			return e
		},
	} {
		err := call()
		if err == nil || err.Code != capability.ErrInvalidParams || !strings.Contains(err.NextStep, "origin/") || !strings.Contains(err.NextStep, "renamed in a terminal") {
			t.Errorf("remote-prefix refusal: %+v", err)
		}
	}
}
