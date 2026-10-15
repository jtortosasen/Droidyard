package git

// git.pull (plan m2n part 3, step R3; ADR-0074 §1, §2): the workspace repository's current
// branch fast-forwarded to its upstream, for the toolbar's Pull. A fast-forward moves the
// branch and the work tree to a descendant or git refuses before it writes, as a checkout
// does, so it cannot stop half-way with conflicts the UI cannot resolve. It is a network
// write on the write runner (write.go) with git.fetch's invocation, key and limits.

import (
	"context"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// PullParams is git.pull's parameters: none.
type PullParams struct{}

// PullResult is git.pull's result.
type PullResult struct {
	Branch string `json:"branch" jsonschema:"required" jsonschema_description:"The local branch that was pulled, without refs/heads/."`
	Before string `json:"before" jsonschema:"required" jsonschema_description:"The full object id the branch pointed at before the pull."`
	After  string `json:"after" jsonschema:"required" jsonschema_description:"The full object id the branch points at after it. Equal to before: it was already up to date."`
}

func registerPull(reg *capability.Registry, opts Options) {
	reg.MustRegister(opts.write(capability.Descriptor{
		Name:        "git.pull",
		Since:       sinceWrites,
		Description: "Fast-forwards the current branch to its upstream for the UI: fetches, then moves the branch and the work tree forward, or refuses (a detached HEAD, no upstream, a diverged branch, local changes in the way); nothing is merged, rebased or stashed. Only on a trusted project; git never prompts here, so a credential that needs one fails with a next step.",
		Params:      PullParams{},
		Result:      PullResult{},
		// The codes the handler returns (ADR-0071 §2, §4; ADR-0074 §2). WORKSPACE_NOT_OPEN
		// comes from Start. WORKSPACE_UNTRUSTED is the trust hook's. GIT_FAILED is a detached
		// HEAD, a branch with no commit yet or no upstream, git's own refusal (it cannot fast-forward, local
		// changes in the way), a credential it cannot ask for, a ref lock another git holds
		// and the five-minute limit.
		Errors: []capability.ErrorCode{
			CodeNotFound, CodeNotARepository, CodeFailed,
			capability.ErrWorkspaceUntrusted,
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return opts.pull(ctx, call)
		},
	}))
}

// pull serves git.pull. Everything git can say before the first write is asked first
// (the branch HEAD is on, its upstream, where the branch is), so a refusal writes nothing.
func (o Options) pull(ctx context.Context, call *capability.Call) (any, error) {
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

	// The gate has admitted the root, so the repository's own configuration is the user's.
	ref, err := headBranchRef(ctx, r, root)
	if err != nil {
		return nil, err
	}
	if ref == "" {
		return nil, capability.NewError(CodeFailed, "git cannot pull: HEAD is detached").
			WithCause("HEAD is not on a branch").
			WithNextStep("check a branch out first; a pull brings a branch up to its upstream")
	}
	branch := strings.TrimPrefix(ref, "refs/heads/")
	// Where the branch is first: an unborn branch has no upstream to read either, and its
	// refusal says why.
	before, err := pullHead(ctx, r, root)
	if err != nil {
		return nil, err
	}
	upstream, err := upstreamOf(ctx, r, root, ref)
	if err != nil {
		return nil, err
	}
	if upstream == "" {
		return nil, capability.Errorf(CodeFailed, "git cannot pull: the branch %q has no upstream", truncate(branch, 80)).
			WithCause("no upstream branch is configured for " + truncate(branch, 80)).
			WithNextStep("set one in a terminal in " + root + " with `git branch --set-upstream-to=<remote>/<branch>`, then retry")
	}

	timeout := o.timeoutFor(true)
	res, err := w.run(ctx, root, true, timeout, pullArgs())
	if err != nil {
		return nil, mapWriteError(root, err, timeout, "pull --ff-only")
	}
	if res.ExitCode != 0 {
		return nil, pullFailure(root, res)
	}
	after, err := pullHead(ctx, r, root)
	if err != nil {
		return nil, err
	}
	return PullResult{Branch: branch, Before: before, After: after}, nil
}

// pullArgs is the pull: the upstream of the current branch, fast-forward only. The flags
// and the three -c settings say the same twice, so that the repository's configuration (a
// rebase, a stash, a submodule recursion the user set for their own pulls) cannot make
// this one rebase, stash or recurse. --quiet drops the per-ref progress lines a fetch
// writes, as fetchArgs does; `fatal:` and `error:` lines stay. --no-prune: its fetch never
// prunes, as git.fetch never does (ADR-0071 §1); on the command line, because a
// repository's remote.<name>.prune and remote.<name>.pruneTags outrank fetch.prune, and
// pruneTags would delete the user's own unpushed tags.
func pullArgs() []string {
	return writeCmd("-c", "pull.rebase=false", "-c", "merge.autoStash=false", "-c", "submodule.recurse=false",
		"pull", "--quiet", "--ff-only", "--no-rebase", "--no-recurse-submodules", "--no-autostash", "--no-prune")
}

// pullFailure is writeFailure with, for a branch git cannot fast-forward, the way on:
// merging or rebasing is the user's, in a terminal (ADR-0074 §2).
func pullFailure(root string, res result) error {
	err := writeFailure(root, res)
	if ce := capability.AsError(err); ce.Code == CodeFailed && ce.NextStep == "" &&
		(strings.Contains(ce.Cause, "fast-forward") || strings.Contains(ce.Cause, "diverging branches")) {
		ce.WithNextStep("the branch has commits its upstream lacks: merge or rebase it in a terminal in " + root + ", then retry")
	}
	return err
}

// headBranchRef is the full ref of the branch HEAD is on, an unborn one included; "" for a
// detached HEAD.
func headBranchRef(ctx context.Context, r *runner, root string) (string, error) {
	res, err := r.run(ctx, root, true, nil, hist("symbolic-ref", "--quiet", "HEAD")...)
	if err != nil {
		return "", mapError(root, err)
	}
	switch res.ExitCode {
	case 0:
		return strings.TrimSpace(strings.ToValidUTF8(string(res.Stdout), "�")), nil
	case 1:
		return "", nil
	}
	return "", mapError(root, gitError(res))
}

// upstreamOf is the full name of the ref the branch ref tracks, or "" when none is
// configured. ref is a full refs/heads/ name git itself gave, so no option can be read
// from it and a ref name holds no pattern character.
func upstreamOf(ctx context.Context, r *runner, root, ref string) (string, error) {
	res, err := r.run(ctx, root, true, nil, hist("for-each-ref", "--format=%(upstream)", ref)...)
	if err != nil {
		return "", mapError(root, err)
	}
	if res.ExitCode != 0 {
		return "", mapError(root, gitError(res))
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// pullHead is HEAD's object id, read through history.go's headOid: a branch with nothing
// saved on it yet cannot be pulled into, and says so.
func pullHead(ctx context.Context, r *runner, root string) (string, error) {
	oid, err := headOid(ctx, r, root, true)
	if err != nil {
		return "", mapError(root, err)
	}
	if !isOid(oid) {
		return "", capability.NewError(CodeFailed, "git cannot pull: the branch has no first save yet").
			WithCause("HEAD does not point at a saved state").
			WithNextStep("save a first state or check out another branch, then retry")
	}
	return oid, nil
}
