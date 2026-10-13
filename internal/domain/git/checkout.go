package git

// git.checkout and git.createBranch (plan m2n part 2, step P3; ADR-0071 §1, §5, §6, plan
// Q6 (1)): the two writes that move or add a branch, for the History tab. Everything git
// can say before the first write is asked before it, in every root, and fails the whole
// call with nothing applied; only what git says while writing fails a root's outcome.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// rootSelf is the repository of the workspace itself, the one root a call can name until
// submodules are roots (plan m2n P8).
const rootSelf = "."

// Outcome statuses (ADR-0053 §2's per-item pattern, ADR-0071 §6).
const (
	outcomeDone    = "done"
	outcomeFailed  = "failed"
	outcomeSkipped = "skipped"
)

// CheckoutParams is git.checkout's parameters.
type CheckoutParams struct {
	Name   string   `json:"name" jsonschema:"required" jsonschema_description:"The branch to check out, without refs/heads/: a local one, or with remote its name on that remote."`
	Remote string   `json:"remote,omitempty" jsonschema_description:"A remote of the repository. When name is no local branch, one is created tracking this remote's."`
	Roots  []string `json:"roots,omitempty" jsonschema_description:"The repositories to apply it to, in order: \".\" is the project's own. Only \".\" for now. Absent: [\".\"]."`
}

// CreateBranchParams is git.createBranch's parameters.
type CreateBranchParams struct {
	Name     string            `json:"name" jsonschema:"required" jsonschema_description:"The new branch's name, without refs/heads/. A name that exists, or reads as another ref, is refused."`
	Checkout bool              `json:"checkout" jsonschema:"required" jsonschema_description:"True: check the new branch out too. False: HEAD stays where it is."`
	From     string            `json:"from" jsonschema:"required" jsonschema_description:"The start: a full object id (40 or 64 hex characters) of a commit of the repository, not a ref name."`
	Roots    []string          `json:"roots,omitempty" jsonschema_description:"The repositories to apply it to, in order: \".\" is the project's own. Only \".\" for now. Absent: [\".\"]."`
	FromIn   map[string]string `json:"fromIn,omitempty" jsonschema_description:"Per submodule path, the full object id of its start commit. Submodule roots are not accepted yet."`
}

// OutcomeError is why a root's write failed.
type OutcomeError struct {
	Code     string `json:"code" jsonschema:"required,enum=GIT_FAILED,enum=OPERATION_CANCELLED" jsonschema_description:"GIT_FAILED: git refused or the time limit passed. OPERATION_CANCELLED: the call was cancelled while git was writing, or before it reached this root."`
	Message  string `json:"message" jsonschema:"required" jsonschema_description:"What happened."`
	Cause    string `json:"cause,omitempty" jsonschema_description:"Git's own words, from its first fatal: or error: line, at most 20 lines and 4 KiB (a refused checkout names the files in the way), or the time limit's fixed text. Show it as text."`
	NextStep string `json:"nextStep,omitempty" jsonschema_description:"What to do next, when there is something to do."`
}

// BranchOutcome is what happened in one root.
type BranchOutcome struct {
	Root   string        `json:"root" jsonschema:"required" jsonschema_description:"The root as asked: \".\" or a submodule path."`
	Status string        `json:"status" jsonschema:"required,enum=done,enum=failed,enum=skipped" jsonschema_description:"done: applied. failed: git refused it, it was ended, or the call was cancelled before it ran; see error. skipped: not tried, because an earlier root failed."`
	Error  *OutcomeError `json:"error,omitempty" jsonschema_description:"Present when the status is failed."`
}

// BranchResult is the result of git.checkout and git.createBranch.
type BranchResult struct {
	Outcomes []BranchOutcome `json:"outcomes" jsonschema:"required" jsonschema_description:"One outcome per root, in the order applied. The call stops at the first failed root and the rest are skipped; after a cancel they are failed OPERATION_CANCELLED. Nothing applied is rolled back. A refusal decided before any git writes fails the whole call and applies nothing."`
}

func registerCheckout(reg *capability.Registry, opts Options) {
	// The codes both handlers return (ADR-0071 §2, §4, §5). WORKSPACE_UNTRUSTED is the trust
	// hook's. REQUEST_INVALID_PARAMS is everything refused before a write: a name, an oid, a
	// remote, a root, a branch the repository lacks or already has. A write git's own refusal
	// is not an error of the call but a failed outcome, so GIT_FAILED here is only a git that
	// cannot be run or listed.
	errs := []capability.ErrorCode{
		CodeNotFound, CodeNotARepository, CodeFailed,
		capability.ErrWorkspaceUntrusted, capability.ErrInvalidParams,
	}
	reg.MustRegister(opts.write(capability.Descriptor{
		Name:        "git.checkout",
		Since:       sinceWrites,
		Description: "Checks a branch out for the UI, or creates a local branch tracking a remote-tracking one and checks that out; nothing is forced or kept aside, and a checkout git refuses over local changes fails its root with git's words naming the files. Only on a trusted project; the result has one outcome per root.",
		Params:      CheckoutParams{},
		Result:      BranchResult{},
		Errors:      errs,
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return opts.checkout(ctx, call)
		},
	}))
	reg.MustRegister(opts.write(capability.Descriptor{
		Name:        "git.createBranch",
		Since:       sinceWrites,
		Description: "Creates a local branch at a commit for the UI, with no upstream, and optionally checks it out; a name that exists is refused, and a checkout git refuses over local changes fails its root with git's words. Only on a trusted project; the result has one outcome per root.",
		Params:      CreateBranchParams{},
		Result:      BranchResult{},
		Errors:      errs,
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return opts.createBranch(ctx, call)
		},
	}))
}

// checkout serves git.checkout. Order, every step before any write git: the shapes that
// need no git (roots, the name, the remote), the repository's remotes, the name against
// them and against git's own rules, the remote, then which ref the checkout is of.
func (o Options) checkout(ctx context.Context, call *capability.Call) (any, error) {
	p := call.Params.(CheckoutParams)
	if cerr := branchRoots(p.Roots, nil); cerr != nil {
		return nil, cerr
	}
	if !validName(p.Name) {
		return nil, notABranchName(p.Name)
	}
	if p.Remote != "" && !validName(p.Remote) {
		return nil, notARemote(p.Remote)
	}
	b, cerr := o.branchCall(call)
	if cerr != nil {
		return nil, cerr
	}
	if cerr := b.checkName(ctx, p.Name); cerr != nil {
		return nil, cerr
	}
	if p.Remote != "" && !slices.Contains(b.remotes, p.Remote) {
		return nil, notARemote(p.Remote)
	}

	local, err := b.refExists(ctx, "refs/heads/"+p.Name)
	if err != nil {
		return nil, err
	}
	args := switchArgs(p.Name)
	if !local {
		if p.Remote == "" {
			return nil, noSuchBranch(p.Name, "")
		}
		tracking := "refs/remotes/" + p.Remote + "/" + p.Name
		found, err := b.refExists(ctx, tracking)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, noSuchBranch(p.Name, p.Remote)
		}
		// The full ref, so a local branch that merely looks like origin/<name> is not the start.
		args = trackArgs(p.Name, tracking)
	}
	return b.apply(ctx, []branchStep{{root: rootSelf, dir: b.root, args: args, switches: true}}, "git refused the checkout")
}

// createBranch serves git.createBranch, in checkout's order: the shapes, the name, then the
// name being free and from being a commit.
func (o Options) createBranch(ctx context.Context, call *capability.Call) (any, error) {
	p := call.Params.(CreateBranchParams)
	if cerr := branchRoots(p.Roots, p.FromIn); cerr != nil {
		return nil, cerr
	}
	if !validName(p.Name) {
		return nil, notABranchName(p.Name)
	}
	if !isOid(p.From) {
		return nil, notACommit()
	}
	b, cerr := o.branchCall(call)
	if cerr != nil {
		return nil, cerr
	}
	if cerr := b.checkName(ctx, p.Name); cerr != nil {
		return nil, cerr
	}
	exists, err := b.refExists(ctx, "refs/heads/"+p.Name)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, capability.Errorf(capability.ErrInvalidParams, "a branch named %q already exists", truncate(p.Name, 80)).
			WithNextStep("choose another name, or check the existing branch out")
	}
	if i, cerr := o.firstNotCommit(ctx, b.r, workspaceRoot(b.root), true, []string{p.From}); cerr != nil {
		return nil, cerr
	} else if i >= 0 {
		return nil, notACommit()
	}
	return b.apply(ctx, []branchStep{{root: rootSelf, dir: b.root, args: branchArgs(p.Name, p.From, p.Checkout), switches: p.Checkout}}, "git refused to create the branch")
}

// branchRoots is the roots check, which needs no git: each root once, and only the
// project's own until submodules are roots (plan m2n P8). fromIn gives a start per
// submodule root, so with none admitted any entry names a root the call does not apply to.
func branchRoots(roots []string, fromIn map[string]string) *capability.Error {
	// Absent roots mean ["."]; an empty list names no root at all.
	if roots != nil && len(roots) == 0 {
		return capability.NewError(capability.ErrInvalidParams, "roots is empty: it names no repository").
			WithNextStep(`pass roots ["."], or omit roots`)
	}
	for i, root := range roots {
		switch {
		case root != rootSelf:
			return capability.Errorf(capability.ErrInvalidParams, "roots[%d] is %q: only %q is accepted for now", i, truncate(root, 80), rootSelf).
				WithNextStep(`pass roots ["."], or omit roots`)
		case slices.Contains(roots[:i], root):
			return capability.Errorf(capability.ErrInvalidParams, "roots[%d] repeats %q", i, root).
				WithNextStep("name each root once")
		}
	}
	for path := range fromIn {
		return capability.Errorf(capability.ErrInvalidParams, "fromIn names %q, which is not one of roots", truncate(path, 80)).
			WithNextStep("omit fromIn: only the project's own repository is a root for now")
	}
	return nil
}

// branchCall is what the two handlers read the repository with, for a root the gate has
// admitted (the repository's own configuration is the user's).
type branchCall struct {
	o       Options
	root    string
	r       *runner
	w       *writeRunner
	remotes []string
	// exec runs one root's write git: the write runner's run, replaced in the test of apply.
	exec func(ctx context.Context, dir string, timeout time.Duration, args []string) (result, error)
}

// branchCall resolves the root and both runners and lists the remotes, the first reads of
// the call.
func (o Options) branchCall(call *capability.Call) (*branchCall, error) {
	root, cerr := o.root(call)
	if cerr != nil {
		return nil, cerr
	}
	r, cerr := o.runner()
	if cerr != nil {
		return nil, cerr
	}
	w, cerr := o.writeRunner()
	if cerr != nil {
		return nil, cerr
	}
	return &branchCall{o: o, root: root, r: r, w: w, exec: func(ctx context.Context, dir string, timeout time.Duration, args []string) (result, error) {
		return w.run(ctx, dir, false, timeout, args)
	}}, nil
}

// checkName asks the repository about a name validName passed: it does not start with the
// name of a remote and a slash (such a branch would read as that remote's, and git resolves
// origin/x to the remote-tracking branch before it), and git's own rules accept it as a
// branch name. The remotes are read here, once.
func (b *branchCall) checkName(ctx context.Context, name string) error {
	remotes, err := listRemotes(ctx, b.r, b.root)
	if err != nil {
		return err
	}
	b.remotes = remotes
	for _, remote := range remotes {
		if strings.HasPrefix(name, remote+"/") {
			return capability.Errorf(capability.ErrInvalidParams, "%q starts with the name of the remote %q and a slash, so it would read as one of that remote's branches", truncate(name, 80), truncate(remote, 80)).
				WithNextStep("choose a name that does not start with " + truncate(remote, 80) + "/; a branch already named so cannot be used from here until it is renamed in a terminal")
		}
	}
	res, err := b.r.run(ctx, b.root, true, nil, refFormatArgs(name)...)
	if err != nil {
		return mapError(b.root, err)
	}
	// --branch prints the name it understood, which differs from name when git expanded it.
	if res.ExitCode != 0 || strings.TrimSuffix(string(res.Stdout), "\n") != name {
		return notABranchName(name)
	}
	return nil
}

// refExists says whether the full ref name exists, and only that ref (after --end-of-options,
// so never read as an option).
func (b *branchCall) refExists(ctx context.Context, ref string) (bool, error) {
	// show-ref --verify is exact: rev-parse --verify would also look under refs/tags/<ref>.
	res, err := b.r.run(ctx, b.root, true, nil, hist("show-ref", "--verify", "--quiet", "--end-of-options", ref)...)
	if err != nil {
		return false, mapError(b.root, err)
	}
	switch res.ExitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	}
	return false, mapError(b.root, gitError(res))
}

// branchStep is one root's write.
type branchStep struct {
	// root is the root as the result names it; dir the directory git runs in.
	root, dir string
	args      []string
	// switches: the write moves HEAD and rewrites the work tree, which a cancel can leave
	// half done.
	switches bool
}

// apply runs the steps in order and stops at the first failed one, the rest skipped,
// nothing rolled back (ADR-0071 §6). A cancel before the first write is the call's own
// cancel; one after it is the root's failed outcome, since the write may have begun.
func (b *branchCall) apply(ctx context.Context, steps []branchStep, msg string) (any, error) {
	timeout := b.o.timeoutFor(false)
	out := BranchResult{Outcomes: make([]BranchOutcome, 0, len(steps))}
	// What stopped the call: a root that failed (the rest are skipped), or a cancel (the rest
	// are failed OPERATION_CANCELLED, ADR-0053 §4's item pattern).
	const (
		running = iota
		failedRoot
		cancelled
	)
	stop := running
	for _, s := range steps {
		switch {
		case stop == failedRoot:
			out.Outcomes = append(out.Outcomes, BranchOutcome{Root: s.root, Status: outcomeSkipped})
			continue
		case stop == cancelled || ctx.Err() != nil:
			if len(out.Outcomes) == 0 {
				return nil, ctx.Err()
			}
			stop = cancelled
			out.Outcomes = append(out.Outcomes, BranchOutcome{Root: s.root, Status: outcomeFailed, Error: &OutcomeError{
				Code:    string(capability.ErrOperationCancelled),
				Message: "the operation was cancelled before git reached this root",
			}})
			continue
		}
		res, err := b.exec(ctx, s.dir, timeout, s.args)
		switch {
		case err == nil && res.ExitCode == 0:
			out.Outcomes = append(out.Outcomes, BranchOutcome{Root: s.root, Status: outcomeDone})
		case err == nil:
			e := writeFailure(s.dir, res)
			if ce := capability.AsError(e); ce.Code != CodeFailed {
				return nil, ce
			}
			stop = failedRoot
			out.Outcomes = append(out.Outcomes, BranchOutcome{Root: s.root, Status: outcomeFailed, Error: b.refused(s, res, msg)})
		case errors.Is(err, ErrTimeout):
			stop = failedRoot
			out.Outcomes = append(out.Outcomes, BranchOutcome{Root: s.root, Status: outcomeFailed, Error: &OutcomeError{
				Code:     string(CodeFailed),
				Message:  fmt.Sprintf("git did not finish in %s and was stopped", timeout),
				Cause:    ErrTimeout.Error(),
				NextStep: endedMidWrite(s),
			}})
		case ctx.Err() != nil:
			stop = cancelled
			out.Outcomes = append(out.Outcomes, BranchOutcome{Root: s.root, Status: outcomeFailed, Error: &OutcomeError{
				Code:     string(capability.ErrOperationCancelled),
				Message:  "the operation was cancelled while git was writing",
				NextStep: endedMidWrite(s),
			}})
		default:
			// git did not start, or could not be reaped: nothing was written by this call.
			return nil, mapError(s.dir, err)
		}
	}
	return out, nil
}

// refused is a failed outcome for a git that exited non-zero: its cause in its own words.
// A partial clone whose missing files git may not fetch from here gets the terminal
// nextStep (plan m2n P1 fact 8).
func (b *branchCall) refused(s branchStep, res result, msg string) *OutcomeError {
	e := &OutcomeError{
		Code:    string(CodeFailed),
		Message: msg,
		Cause:   writeCause(res.Stderr),
	}
	if bytesContain(res.Stderr, "promisor remote") || bytesContain(res.Stderr, "lazy fetching disabled") {
		e.NextStep = "this repository is a partial clone and files this branch needs are not here: fetch the missing objects once in a terminal in " + s.dir + " (switching to the branch there does it), then retry"
	}
	return e
}

func bytesContain(b []byte, s string) bool { return strings.Contains(string(b), s) }

// endedMidWrite is the nextStep of a write ended before it finished: HEAD stays where it
// was with a work tree that may be half switched, and no lock file is left (plan m2n P1
// fact 11); only the user's git can finish or undo it.
func endedMidWrite(s branchStep) string {
	if s.switches {
		return "run `git status` in a terminal in " + s.dir + "; finish or undo the checkout there"
	}
	return "run `git branch` in a terminal in " + s.dir + " to see whether the branch was created"
}

// notABranchName is the refusal of a name validName or git's own rules reject.
func notABranchName(name string) *capability.Error {
	return capability.Errorf(capability.ErrInvalidParams, "%q is not a branch name this can use", truncate(name, 80)).
		WithNextStep("use a plain name such as feature/login: no spaces, \"..\", control characters or leading \"-\", not HEAD, not ending in .lock or \"/\", and not starting with refs/, heads/, remotes/ or tags/")
}

// notACommit is the refusal of a from that is not the full id of a commit of the repository.
func notACommit() *capability.Error {
	return capability.NewError(capability.ErrInvalidParams, "from is not the full object id of a commit of this repository").
		WithNextStep("pass the 40 or 64 hex characters of a commit the history shows, not a ref name, a short id or another kind of object")
}

// noSuchBranch is the refusal of a checkout of a branch the repository lacks.
func noSuchBranch(name, remote string) *capability.Error {
	msg := fmt.Sprintf("there is no local branch %q", truncate(name, 80))
	if remote != "" {
		msg = fmt.Sprintf("there is no local branch %q and no remote-tracking branch %q", truncate(name, 80), truncate(remote+"/"+name, 160))
	}
	return capability.NewError(capability.ErrInvalidParams, msg).
		WithNextStep("pass the name of a branch the Branches list shows; fetch first if the remote's branch is new")
}
