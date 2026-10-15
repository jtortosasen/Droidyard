package git

// git.checkout and git.createBranch (plan m2n part 2, step P3; ADR-0071 §1, §5, §6, plan
// Q6 (1)): the two writes that move or add a branch, for the History tab. Everything git
// can say before the first write is asked before it, in every root, and fails the whole
// call with nothing applied; only what git says while writing fails a root's outcome.
//
// Synchronised branches (step P8; ADR-0071 §6): `roots` names the project and any initialised
// submodules (each resolved as a read's `root` is, roots.go). They are applied "." first and
// then depth-first by path whatever order was given; every root is fully checked before the
// first write; the first failed root stops the call and the rest are skipped; nothing is rolled
// back. The call holds one repository key throughout (write.go's resource: the workspace's
// common directory), since it is one operation.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// rootSelf is the repository of the workspace itself, the first root of every call.
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
	Roots  []string `json:"roots,omitempty" jsonschema_description:"\".\" is the project's own, the rest submodule paths. Applied \".\" first, then depth-first. Absent: [\".\"]."`
}

// CreateBranchParams is git.createBranch's parameters.
type CreateBranchParams struct {
	Name     string            `json:"name" jsonschema:"required" jsonschema_description:"The new branch's name, without refs/heads/. A name that exists, or reads as another ref, is refused."`
	Checkout bool              `json:"checkout" jsonschema:"required" jsonschema_description:"True: check the new branch out too. False: HEAD stays where it is."`
	From     string            `json:"from" jsonschema:"required" jsonschema_description:"The start: a full object id (40 or 64 hex characters) of a commit of the repository, not a ref name."`
	Roots    []string          `json:"roots,omitempty" jsonschema_description:"\".\" is the project's own, the rest submodule paths. Applied \".\" first, then depth-first. Absent: [\".\"]."`
	FromIn   map[string]string `json:"fromIn,omitempty" jsonschema_description:"Per submodule path in roots, the full object id of its start commit there. A root without an entry refuses the call."`
}

// OutcomeError is why a root's write failed.
type OutcomeError struct {
	Code     string `json:"code" jsonschema:"required,enum=GIT_FAILED,enum=GIT_NOT_FOUND,enum=GIT_NOT_A_REPOSITORY,enum=OPERATION_CANCELLED" jsonschema_description:"GIT_FAILED: git refused or the time limit passed. GIT_NOT_FOUND, GIT_NOT_A_REPOSITORY: git or this root's repository went away after an earlier root was written. OPERATION_CANCELLED: the call was cancelled while git was writing, or before it reached this root."`
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
// need no git (roots, the name, the remote), then, for each root in the order it will be
// applied, that the root is the project or an initialised submodule, its remotes, the name
// against them and against git's own rules, the remote, and which ref the checkout is of.
// The first root that fails any of it fails the whole call with nothing applied (ADR-0071
// §6).
func (o Options) checkout(ctx context.Context, call *capability.Call) (any, error) {
	p := call.Params.(CheckoutParams)
	rels, cerr := branchRoots(p.Roots, nil)
	if cerr != nil {
		return nil, cerr
	}
	if !validName(p.Name) {
		return nil, notABranchName(p.Name)
	}
	if p.Remote != "" && !validName(p.Remote) {
		return nil, notARemote(p.Remote)
	}
	b, err := o.branchCall(call)
	if err != nil {
		return nil, err
	}
	steps := make([]branchStep, 0, len(rels))
	for _, rel := range rels {
		g, err := o.resolveRoot(ctx, b.r, b.root, true, rel)
		if err != nil {
			return nil, err
		}
		remotes, err := b.checkName(ctx, g, p.Name)
		if err != nil {
			return nil, err
		}
		if p.Remote != "" && !slices.Contains(remotes, p.Remote) {
			return nil, at(notARemote(p.Remote), g)
		}
		local, err := b.refExists(ctx, g, "refs/heads/"+p.Name)
		if err != nil {
			return nil, err
		}
		args := switchArgs(p.Name)
		if !local {
			if p.Remote == "" {
				return nil, at(noSuchBranch(p.Name, ""), g)
			}
			tracking := "refs/remotes/" + p.Remote + "/" + p.Name
			found, err := b.refExists(ctx, g, tracking)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, at(noSuchBranch(p.Name, p.Remote), g)
			}
			// The full ref, so a local branch that merely looks like origin/<name> is not the start.
			args = trackArgs(p.Name, tracking)
		}
		steps = append(steps, branchStep{root: displayRoot(rel), dir: g.dir, args: args, switches: true})
	}
	return b.apply(ctx, steps, "git refused the checkout")
}

// createBranch serves git.createBranch, in checkout's order: the shapes (every root's
// start among them), then, per root, the name, the name being free and the start being a
// commit of that root.
func (o Options) createBranch(ctx context.Context, call *capability.Call) (any, error) {
	p := call.Params.(CreateBranchParams)
	rels, cerr := branchRoots(p.Roots, p.FromIn)
	if cerr != nil {
		return nil, cerr
	}
	if !validName(p.Name) {
		return nil, notABranchName(p.Name)
	}
	if !isOid(p.From) {
		return nil, notACommit()
	}
	// A start for every root, before any git: "." starts at from, a submodule at its own
	// entry of fromIn, since an oid names a commit of one repository only (plan Q6 (1)).
	starts := make([]string, len(rels))
	for i, rel := range rels {
		starts[i] = p.From
		if rel == "" {
			continue
		}
		oid, ok := p.FromIn[rel]
		switch {
		case !ok:
			return nil, capability.Errorf(capability.ErrInvalidParams, "fromIn has no start for the root %q", truncate(rel, 80)).
				WithNextStep("give every submodule in roots its start commit in fromIn")
		case !isOid(oid):
			return nil, notACommitIn(startField(rel))
		}
		starts[i] = oid
	}
	b, err := o.branchCall(call)
	if err != nil {
		return nil, err
	}
	steps := make([]branchStep, 0, len(rels))
	for i, rel := range rels {
		g, err := o.resolveRoot(ctx, b.r, b.root, true, rel)
		if err != nil {
			return nil, err
		}
		if _, err := b.checkName(ctx, g, p.Name); err != nil {
			return nil, err
		}
		exists, err := b.refExists(ctx, g, "refs/heads/"+p.Name)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, at(capability.Errorf(capability.ErrInvalidParams, "a branch named %q already exists", truncate(p.Name, 80)).
				WithNextStep("choose another name, or check the existing branch out"), g)
		}
		if bad, cerr := o.firstNotCommit(ctx, b.r, g, true, []string{starts[i]}); cerr != nil {
			return nil, cerr
		} else if bad >= 0 {
			return nil, at(notACommitIn(startField(rel)), g)
		}
		steps = append(steps, branchStep{root: displayRoot(rel), dir: g.dir, args: branchArgs(p.Name, starts[i], p.Checkout), switches: p.Checkout})
	}
	return b.apply(ctx, steps, "git refused to create the branch")
}

// branchRoots is the roots check, which needs no git, and the order the roots are applied
// in. It returns the workspace-relative path of each root ("" for "."): "." first, then
// the submodules depth-first by path (a parent before its children and each directory's
// children together, as a walk of the tree visits them), whatever order was given. Each
// root is named once and lexically clean (parseRoot); that each is a submodule is the
// caller's resolveRoot. fromIn gives a start per submodule root, so an entry for a path
// that is not a root, or for ".", which has from, is refused.
func branchRoots(roots []string, fromIn map[string]string) ([]string, *capability.Error) {
	// Absent roots mean ["."]; an empty list names no root at all.
	if roots == nil {
		roots = []string{rootSelf}
	}
	if len(roots) == 0 {
		return nil, capability.NewError(capability.ErrInvalidParams, "roots is empty: it names no repository").
			WithNextStep(`pass roots ["."], or omit roots`)
	}
	rels := make([]string, 0, len(roots))
	for i, root := range roots {
		if root == "" {
			return nil, capability.Errorf(capability.ErrInvalidParams, "roots[%d] is empty", i).
				WithNextStep(`pass "." for the project's own repository, or the path of a submodule`)
		}
		rel, cerr := parseRoot(root)
		if cerr != nil {
			return nil, cerr
		}
		if slices.Contains(roots[:i], root) {
			return nil, capability.Errorf(capability.ErrInvalidParams, "roots[%d] repeats %q", i, root).
				WithNextStep("name each root once")
		}
		rels = append(rels, rel)
	}
	for path := range fromIn {
		switch {
		case path == rootSelf:
			return nil, capability.NewError(capability.ErrInvalidParams, `fromIn names ".", which starts at from`).
				WithNextStep("omit the entry for the project's own repository: from is its start")
		case !slices.Contains(roots, path):
			return nil, capability.Errorf(capability.ErrInvalidParams, "fromIn names %q, which is not one of roots", truncate(path, 80)).
				WithNextStep("give a start only to the submodules in roots, spelled as there")
		}
	}
	slices.SortFunc(rels, compareRoots)
	return rels, nil
}

// compareRoots orders root paths as a depth-first walk visits them: component by
// component, the workspace ("") before everything and a directory before what is below it.
// (A plain string comparison puts "lib-x" between "lib" and "lib/nested".)
func compareRoots(a, b string) int {
	if a == "" || b == "" {
		switch {
		case a == b:
			return 0
		case a == "":
			return -1
		}
		return 1
	}
	return slices.Compare(strings.Split(a, "/"), strings.Split(b, "/"))
}

// displayRoot is a root as the result names it: "." for the workspace's own repository.
func displayRoot(rel string) string {
	if rel == "" {
		return rootSelf
	}
	return rel
}

// at says, in a refusal about a submodule root, which one; the workspace's own is the
// default and not named.
func at(e *capability.Error, g gitRoot) *capability.Error {
	if g.rel != "" {
		e.Message += " (in the submodule " + truncate(g.rel, 120) + ")"
	}
	return e
}

// branchCall is what the two handlers read the repository with, for a root the gate has
// admitted (the repository's own configuration is the user's).
type branchCall struct {
	o    Options
	root string
	r    *runner
	w    *writeRunner
	// exec runs one root's write git: the write runner's run, replaced in the test of apply.
	exec func(ctx context.Context, dir string, timeout time.Duration, args []string) (result, error)
}

// branchCall resolves the workspace root and both runners, the first reads of the call.
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

// checkName asks the repository g about a name validName passed: it does not start with
// the name of a remote and a slash (such a branch would read as that remote's, and git
// resolves origin/x to the remote-tracking branch before it), and git's own rules accept it
// as a branch name. It returns the remotes of g, read here.
func (b *branchCall) checkName(ctx context.Context, g gitRoot, name string) ([]string, error) {
	remotes, err := listRemotes(ctx, b.r, g.dir)
	if err != nil {
		return nil, err
	}
	for _, remote := range remotes {
		if strings.HasPrefix(name, remote+"/") {
			return nil, at(capability.Errorf(capability.ErrInvalidParams, "%q starts with the name of the remote %q and a slash, so it would read as one of that remote's branches", truncate(name, 80), truncate(remote, 80)).
				WithNextStep("choose a name that does not start with "+truncate(remote, 80)+"/; a branch already named so cannot be used from here until it is renamed in a terminal"), g)
		}
	}
	res, err := b.r.run(ctx, g.dir, true, nil, refFormatArgs(name)...)
	if err != nil {
		return nil, g.mapError(err)
	}
	// --branch prints the name it understood, which differs from name when git expanded it.
	if res.ExitCode != 0 || strings.TrimSuffix(string(res.Stdout), "\n") != name {
		return nil, notABranchName(name)
	}
	return remotes, nil
}

// refExists says whether the full ref name exists in g, and only that ref (after
// --end-of-options, so never read as an option).
func (b *branchCall) refExists(ctx context.Context, g gitRoot, ref string) (bool, error) {
	// show-ref --verify is exact: rev-parse --verify would also look under refs/tags/<ref>.
	res, err := b.r.run(ctx, g.dir, true, nil, hist("show-ref", "--verify", "--quiet", "--end-of-options", ref)...)
	if err != nil {
		return false, g.mapError(err)
	}
	switch res.ExitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	}
	return false, g.mapError(gitError(res))
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
				if len(out.Outcomes) == 0 {
					return nil, ce
				}
				stop = failedRoot
				out.Outcomes = append(out.Outcomes, BranchOutcome{Root: s.root, Status: outcomeFailed, Error: outcomeErrorOf(ce)})
				continue
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
			// git did not start, or could not be reaped: nothing was written in this root. Before
			// the first write that is the call's own error; after it, earlier roots are done and
			// the call has an outcome to give, so the error is this root's.
			ce := capability.AsError(mapError(s.dir, err))
			if len(out.Outcomes) == 0 {
				return nil, ce
			}
			stop = failedRoot
			out.Outcomes = append(out.Outcomes, BranchOutcome{Root: s.root, Status: outcomeFailed, Error: outcomeErrorOf(ce)})
		}
	}
	return out, nil
}

// outcomeErrorOf is an error of the contract as a failed root's: the code, message, cause and
// nextStep it already has. The codes that can reach it are GIT_FAILED, GIT_NOT_FOUND and
// GIT_NOT_A_REPOSITORY (OutcomeError.Code).
func outcomeErrorOf(ce *capability.Error) *OutcomeError {
	return &OutcomeError{Code: string(ce.Code), Message: ce.Message, Cause: ce.Cause, NextStep: ce.NextStep}
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
func notACommit() *capability.Error { return notACommitIn("from") }

// startField is the parameter that holds a root's start: from for the project, fromIn[<path>]
// for a submodule.
func startField(rel string) string {
	if rel == "" {
		return "from"
	}
	return fmt.Sprintf("fromIn[%q]", truncate(rel, 80))
}

// notACommitIn is notACommit for the parameter field names.
func notACommitIn(field string) *capability.Error {
	return capability.Errorf(capability.ErrInvalidParams, "%s is not the full object id of a commit of this repository", field).
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
