package git

// git.updateSubmodules (plan m2n part 2, step P5; ADR-0071 §1–§3, ADR-0073): brings
// submodules to the commits their repository records, initialising them first, as a user's
// own `git submodule update --init --recursive` does. A network command (it may clone), so
// it runs under writeRunner's network invocation and the five-minute limit, and its whole
// process group, the child gits it starts included, is ended at every end of the call.
//
// What may reach git's argv. A path only if a fresh git.submodules walk lists it (the
// listing is bounded by the workspace and by the repository's own .gitmodules and gitlinks),
// so `..`, a plain directory, a name that starts with `-` and a path that no longer is a
// submodule are REQUEST_INVALID_PARAMS before any write git starts. The paths go after `--`,
// with the global --literal-pathspecs (updateArgs): `submodule update` does not accept
// --end-of-options (plan m2n P1 item 9).
//
// Where git runs, and why paths are always named. A path is given to git as the workspace's
// own relative spelling, from the workspace root, because git resolves it from the current
// directory: with no path at all, git updates every submodule of the repository, those
// outside a subdirectory workspace included (observed). So "all" is the listed submodules
// that are not nested in another, named. A nested submodule is no path of its parent
// repository's index: git answers "pathspec did not match", so a request for one runs in its
// parent's directory, with the path relative to it. A path whose listed ancestor is also
// asked for is left to that ancestor's --recursive.

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// UpdateSubmodulesParams is git.updateSubmodules' parameters.
type UpdateSubmodulesParams struct {
	Paths []string `json:"paths,omitempty" jsonschema_description:"Submodule paths as git.submodules lists them, a nested one starting with its parent's. Absent: every one listed."`
}

// UpdateSubmodulesResult is git.updateSubmodules' result.
type UpdateSubmodulesResult struct {
	Paths []string `json:"paths" jsonschema:"required" jsonschema_description:"The submodules the update was asked to bring to their recorded commits: the paths given, or every one listed when it started; nested ones not initialised before are initialised too, unnamed. Empty in a result returned again after delivery."`
}

func registerUpdateSubmodules(reg *capability.Registry, opts Options) {
	reg.MustRegister(opts.write(capability.Descriptor{
		Name:        "git.updateSubmodules",
		Since:       sinceWrites,
		Description: "Updates the project's git submodules for the UI, all or the given paths: each is initialised if need be and checked out at the commit the repository records, nested ones included. Only on a trusted project; no hook runs and git never prompts, so a credential that needs one fails with a next step.",
		Params:      UpdateSubmodulesParams{},
		Result:      UpdateSubmodulesResult{},
		// The codes the handler returns (ADR-0071 §2, §4). WORKSPACE_NOT_OPEN comes from
		// Start. WORKSPACE_UNTRUSTED is the trust hook's. REQUEST_INVALID_PARAMS is a list
		// of paths that is empty, repeats one or names what git.submodules does not list.
		// GIT_FAILED is git's own error, a credential it cannot ask for, a lock another git
		// holds, a submodule that cannot be read, and the five-minute limit.
		Errors: []capability.ErrorCode{
			CodeNotFound, CodeNotARepository, CodeFailed,
			capability.ErrWorkspaceUntrusted, capability.ErrInvalidParams,
		},
		// A retained result drops the list once delivered (ADR-0020 §Retention), as
		// git.submodules' does: the caller read it, and it can run to 1,000 paths.
		Slim: func(result any) any {
			if r, ok := result.(UpdateSubmodulesResult); ok {
				r.Paths = []string{}
				return r
			}
			return result
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return opts.updateSubmodules(ctx, call)
		},
	}))
}

// updateSubmodules serves git.updateSubmodules. Every refusal comes before the first write
// git; after it, the call stops at the first git that fails and rolls nothing back.
func (o Options) updateSubmodules(ctx context.Context, call *capability.Call) (any, error) {
	p := call.Params.(UpdateSubmodulesParams)
	if p.Paths != nil && len(p.Paths) == 0 {
		return nil, capability.NewError(capability.ErrInvalidParams, "paths is empty: it names no submodule").
			WithNextStep("pass the paths git.submodules lists, or omit paths to update them all")
	}
	if len(p.Paths) > maxSubmodules {
		return nil, capability.Errorf(capability.ErrInvalidParams, "paths names %d submodules; git.submodules lists at most %d", len(p.Paths), maxSubmodules).
			WithNextStep("name fewer, or omit paths to update every submodule")
	}
	seen := make(map[string]bool, len(p.Paths))
	for i, sub := range p.Paths {
		if seen[sub] {
			return nil, capability.Errorf(capability.ErrInvalidParams, "paths[%d] repeats %q", i, truncate(sub, 80)).
				WithNextStep("name each submodule once")
		}
		seen[sub] = true
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

	// The gate has admitted the root, so the repository's own configuration is the user's;
	// the listing is the walk of git.submodules, read now, not the UI's last one.
	listed, err := o.listSubmodules(ctx, r, root)
	if err != nil {
		return nil, err
	}
	known := make([]string, len(listed.Submodules))
	for i, s := range listed.Submodules {
		known[i] = s.Path
	}
	asked := p.Paths
	if asked == nil {
		asked = slices.Clone(known)
	}
	for _, sub := range asked {
		if !slices.Contains(known, sub) {
			return nil, notASubmodule(sub)
		}
	}

	timeout := o.timeoutFor(true)
	deadline := time.Now().Add(timeout)
	for i, run := range updateRuns(root, known, asked) {
		left := time.Until(deadline)
		if left <= 0 {
			return nil, mapWriteError(run.dir, ErrTimeout, timeout, run.command())
		}
		res, err := w.run(ctx, run.dir, true, left, updateArgs(run.paths))
		if err != nil {
			return nil, partial(i, mapWriteError(run.dir, err, timeout, run.command()))
		}
		if res.ExitCode != 0 {
			return nil, partial(i, writeFailure(run.dir, res))
		}
	}
	return UpdateSubmodulesResult{Paths: asked}, nil
}

// updateRun is one `submodule update`: the directory git runs in and the paths it is given,
// relative to that directory.
type updateRun struct {
	dir   string
	paths []string
}

// command is the git command that does this run's update, as a terminal would run it in dir:
// the timeout's nextStep names it, so the user updates exactly these submodules and not
// every submodule of dir's repository. Past a few paths the rest are only counted.
func (r updateRun) command() string {
	const named = 5
	quoted := make([]string, 0, named)
	for _, p := range r.paths[:min(len(r.paths), named)] {
		quoted = append(quoted, "'"+strings.ReplaceAll(truncate(p, 200), "'", `'\''`)+"'")
	}
	cmd := "submodule update --init --recursive -- " + strings.Join(quoted, " ")
	if len(r.paths) > named {
		cmd += fmt.Sprintf(" (and %d more)", len(r.paths)-named)
	}
	return cmd
}

// updateRuns plans the gits of an update of asked, a subset of known (the walk's paths,
// parents before the nested ones, relative to root): one run at the workspace root for the
// asked submodules that no listed submodule holds, then one per parent for the nested ones,
// in the listing's order. A path whose ancestor is asked is left to that ancestor's
// --recursive. Each run's paths are chunked so no argv grows with the list.
func updateRuns(root string, known, asked []string) []updateRun {
	order := map[string][]string{} // parent ("" for the root) -> paths relative to it
	var parents []string
	for _, sub := range known {
		if !slices.Contains(asked, sub) {
			continue
		}
		parent := ""
		for _, k := range known {
			if len(k) > len(parent) && strings.HasPrefix(sub, k+"/") {
				parent = k // the longest listed ancestor: the repository that holds sub
			}
		}
		if holdsAsked(known, asked, sub) {
			continue
		}
		if _, seen := order[parent]; !seen {
			parents = append(parents, parent)
		}
		order[parent] = append(order[parent], strings.TrimPrefix(sub, parent+"/"))
	}
	// The workspace's own repository first: its update may initialise the parents of the rest.
	slices.SortStableFunc(parents, func(a, b string) int {
		switch {
		case a == b:
			return 0
		case a == "":
			return -1
		case b == "":
			return 1
		}
		return 0
	})
	var runs []updateRun
	for _, parent := range parents {
		dir := root
		if parent != "" {
			dir = filepath.Join(root, filepath.FromSlash(parent))
		}
		rest := order[parent]
		for len(rest) > 0 {
			n, size := 0, 0
			for n < len(rest) && (n == 0 || size+len(rest[n])+1 <= pathspecChunk) {
				size += len(rest[n]) + 1
				n++
			}
			runs = append(runs, updateRun{dir: dir, paths: rest[:n]})
			rest = rest[n:]
		}
	}
	return runs
}

// holdsAsked says whether a listed ancestor of sub is itself asked for.
func holdsAsked(known, asked []string, sub string) bool {
	for sup := path.Dir(sub); sup != "." && sup != "/"; sup = path.Dir(sup) {
		if slices.Contains(known, sup) && slices.Contains(asked, sup) {
			return true
		}
	}
	return false
}

// partial adds to a failure of a later git what an earlier one means: it was applied and is
// not undone.
func partial(done int, err error) error {
	if done == 0 {
		return err
	}
	ce, ok := err.(*capability.Error)
	if !ok { // a cancel passes through for the operation layer to end as cancelled
		return err
	}
	const note = "the submodules updated before this one stay updated"
	if ce.NextStep == "" {
		return ce.WithNextStep(note + "; fix the cause, then update again")
	}
	return ce.WithNextStep(ce.NextStep + "; " + note)
}

// notASubmodule is the refusal of a path that git.submodules does not list.
func notASubmodule(sub string) *capability.Error {
	return capability.Errorf(capability.ErrInvalidParams, "%q is not a submodule of this project", truncate(sub, 80)).
		WithNextStep("pass paths as git.submodules lists them, or omit paths to update every submodule")
}
