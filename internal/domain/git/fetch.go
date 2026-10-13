package git

import (
	"context"
	"slices"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// sinceWrites is the API version that introduces the Git writes (plan m2n part 2, P2).
const sinceWrites = "0.25.0"

// FetchParams is git.fetch's parameters.
type FetchParams struct {
	Remote string `json:"remote,omitempty" jsonschema_description:"The name of one remote of the repository (git remote lists them). Absent: every remote."`
}

// FetchResult is git.fetch's result.
type FetchResult struct {
	Remotes []string `json:"remotes" jsonschema:"required" jsonschema_description:"The names of the remotes fetched: the one asked for, or every remote the repository has except those set to skip fetch --all."`
}

func registerFetch(reg *capability.Registry, opts Options) {
	reg.MustRegister(opts.write(capability.Descriptor{
		Name:        "git.fetch",
		Since:       sinceWrites,
		Description: "Fetches the repository's remotes, or one of them, for the UI: new commits arrive as remote-tracking refs, no branch moves, nothing is pruned and submodules are left alone. Only on a trusted project; git never prompts in a terminal here, so a credential that needs one fails with a next step to run git fetch once in a terminal.",
		Params:      FetchParams{},
		Result:      FetchResult{},
		// The codes the handler returns (ADR-0071 §2, §4). WORKSPACE_NOT_OPEN comes from
		// Start. WORKSPACE_UNTRUSTED is the trust hook's. REQUEST_INVALID_PARAMS is a remote
		// that is not one of the repository's. GIT_FAILED is git's own error, a credential
		// it cannot ask for, a ref lock another git holds, and the five-minute limit.
		Errors: []capability.ErrorCode{
			CodeNotFound, CodeNotARepository, CodeFailed,
			capability.ErrWorkspaceUntrusted, capability.ErrInvalidParams,
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return opts.fetch(ctx, call)
		},
	}))
}

// fetch serves git.fetch. The remote is checked against `git remote`'s names before the
// fetch starts, because validName alone admits a URL or a path, which git would fetch
// from: the name reaches argv only as one of the repository's own (ADR-0071 §5).
func (o Options) fetch(ctx context.Context, call *capability.Call) (any, error) {
	p := call.Params.(FetchParams)
	if p.Remote != "" && !validName(p.Remote) {
		return nil, notARemote(p.Remote)
	}
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
	res, err := r.run(ctx, root, true, nil, hist("remote")...)
	if err != nil {
		return nil, mapError(root, err)
	}
	if res.ExitCode != 0 {
		return nil, mapError(root, gitError(res))
	}
	var names []string
	for _, n := range strings.Split(string(res.Stdout), "\n") {
		if n != "" {
			names = append(names, n)
		}
	}
	if res.Truncated {
		// A name cut by the cap would be fetched or refused wrongly.
		return nil, capability.NewError(CodeFailed, "the repository has more remotes than git.fetch can list")
	}
	remotes := names
	if p.Remote != "" {
		if !slices.Contains(names, p.Remote) {
			return nil, notARemote(p.Remote)
		}
		remotes = []string{p.Remote}
	} else {
		// `fetch --all` leaves out the remotes that ask to be skipped, so the result does too.
		skipped, cerr := o.skippedByAll(ctx, r, root)
		if cerr != nil {
			return nil, cerr
		}
		remotes = slices.DeleteFunc(slices.Clone(names), func(n string) bool { return skipped[n] })
	}

	timeout := o.timeoutFor(true)
	res, err = w.run(ctx, root, true, timeout, fetchArgs(p.Remote))
	if err != nil {
		return nil, mapWriteError(root, err, timeout, "fetch")
	}
	if res.ExitCode != 0 {
		return nil, writeFailure(root, res)
	}
	if remotes == nil {
		remotes = []string{}
	}
	return FetchResult{Remotes: remotes}, nil
}

// skippedByAll lists the remotes `git fetch --all` skips: those with remote.<name>.skipFetchAll
// (or its older name, skipDefaultUpdate) true. The configuration is read with a fixed argv;
// the names come from its keys, whole, since a remote's name may hold dots.
func (o Options) skippedByAll(ctx context.Context, r *runner, root string) (map[string]bool, *capability.Error) {
	res, err := r.run(ctx, root, true, nil, hist("config", "-z", "--type=bool", "--get-regexp", `^remote\..*\.(skipfetchall|skipdefaultupdate)$`)...)
	if err != nil {
		return nil, capability.AsError(mapError(root, err))
	}
	switch res.ExitCode {
	case 1: // no such key
		return nil, nil
	case 0:
	default:
		return nil, capability.AsError(mapError(root, gitError(res)))
	}
	if res.Truncated {
		return nil, capability.NewError(CodeFailed, "the repository has more remote settings than git.fetch can list")
	}
	skipped := map[string]bool{}
	for _, rec := range strings.Split(string(res.Stdout), "\x00") {
		key, value, _ := strings.Cut(rec, "\n")
		name, ok := strings.CutPrefix(key, "remote.")
		if !ok || value != "true" {
			continue
		}
		for _, suffix := range []string{".skipfetchall", ".skipdefaultupdate"} {
			if n, ok := strings.CutSuffix(name, suffix); ok {
				skipped[n] = true
			}
		}
	}
	return skipped, nil
}

// notARemote is the refusal of a remote name the repository does not have.
func notARemote(name string) *capability.Error {
	return capability.Errorf(capability.ErrInvalidParams, "%q is not the name of a remote of this repository", truncate(name, 80)).
		WithNextStep("pass one of the names `git remote` lists, or omit remote to fetch them all")
}
