package workspace

import (
	"context"
	"path/filepath"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// CapEnter is workspace.enter (ADR-0082 §3).
const CapEnter = "workspace.enter"

// sinceEnter is the API version that introduced workspace.enter (plan m2q: one version
// for the milestone).
const sinceEnter = "0.28.0"

// ErrNotAWorktree is WORKSPACE_NOT_A_WORKTREE (ADR-0082 §3).
const ErrNotAWorktree = capability.ErrWorkspaceNotAWorktree

// enterNextStep is what a root that cannot be entered is told to pass.
const enterNextStep = "call workspace_enter with the absolute path of the project's root or of a git worktree of it"

// EnterParams are workspace.enter's parameters.
type EnterParams struct {
	Root string `json:"root" jsonschema:"required" jsonschema_description:"Absolute path of the project's main tree or of one of its git worktrees."`
}

// enter rebinds the calling MCP session (ADR-0082 §3). root must be the session's project
// root or a linked worktree of it; any other root, a genuine worktree of another
// repository included, is refused and the binding stays. The workspace is opened if it is
// closed and no trust is granted (ADR-0015 §5): trust comes from the project's own
// (platform.LinkedWorktree; policy.Trusted).
func (r *Registry) enter(ctx context.Context, call *capability.Call) (any, error) {
	p := call.Params.(EnterParams)
	if r.opts.Ops == nil {
		return nil, capability.NewError(ErrNotAWorktree, "this registry has no sessions to move")
	}
	if !filepath.IsAbs(p.Root) {
		return nil, rootInvalid(p.Root, "root must be an absolute path").WithNextStep(enterNextStep)
	}
	sessions := r.opts.Ops.Sessions()
	project := sessions.Project(call.Origin.ID)
	if project == "" {
		return nil, capability.Errorf(ErrNotAWorktree, "%q is not a workspace this session can enter: it is bound to no project", p.Root).
			WithNextStep("start the session in the project's directory, or pass --root to droidyard mcp")
	}
	env := r.opts.Env
	root, project := env.Canonical(p.Root), env.Canonical(project)
	if root != project {
		if main, ok := platform.LinkedWorktree(env, root); !ok || main != project {
			return nil, capability.Errorf(ErrNotAWorktree, "%q is neither this session's project (%s) nor a git worktree of it", p.Root, project).
				WithNextStep("pass the project's root or one of its git worktrees (`git worktree list` names them)").
				WithDetails(map[string]any{"project": project})
		}
	}
	if err := readableDir(root); err != nil {
		return nil, rootInvalid(p.Root, err.Error()).WithNextStep(enterNextStep)
	}
	info, err := r.Open(root)
	if err != nil {
		return nil, err
	}
	sessions.Bound(call.Origin.ID, info.ID, info.Root)
	return withGradle(info), nil
}
