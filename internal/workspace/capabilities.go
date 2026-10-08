package workspace

import (
	"context"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The capabilities' names.
const (
	CapOpen     = "workspace.open"
	CapDescribe = "workspace.describe"
	CapClose    = "workspace.close"
)

// since is the API version that introduced the workspace capabilities (plan m1c, R2).
const since = "0.2.0"

// None is the empty parameter object: the workspace is the envelope's, never a
// parameter (ADR-0020 §Scope and workspace addressing).
type None struct{}

// CloseResult is workspace.close's result: nothing (plan m1c, R6).
type CloseResult struct{}

// Register adds workspace.open, workspace.describe and workspace.close.
func Register(reg *capability.Registry, ws *Registry) {
	notDestructive := false
	reg.MustRegister(capability.Descriptor{
		Name:        CapOpen,
		Kind:        capability.Command,
		Scope:       capability.Workspace,
		Idempotent:  true,
		RawTarget:   true,
		Since:       since,
		Description: "Opens the workspace for a project root, or returns its state if it is already open. An MCP session opens its bound root itself, so call this only to reopen it after a close.",
		Params:      None{},
		Result:      Info{},
		Errors:      []capability.ErrorCode{ErrRootInvalid, ErrStateNewer, ErrStateUnreadable},
		Annotations: capability.Annotations{
			Destructive: &notDestructive,
			Reason:      "registers a root and runs no project code",
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			info, err := ws.Open(call.Workspace)
			if err != nil {
				return nil, err
			}
			return info, nil
		},
	})

	reg.MustRegister(capability.Descriptor{
		Name:        CapDescribe,
		Kind:        capability.Query,
		Scope:       capability.Workspace,
		Since:       since,
		Description: "Describes the open workspace: its id, root, state, whether it is trusted, and, when degraded, why.",
		Params:      None{},
		Result:      Info{},
		Errors:      []capability.ErrorCode{ErrStateNewer},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			info, err := ws.Describe(call.Workspace)
			if err != nil {
				return nil, err
			}
			return info, nil
		},
	})

	reg.MustRegister(capability.Descriptor{
		Name:        CapClose,
		Kind:        capability.Command,
		Scope:       capability.Workspace,
		Idempotent:  true,
		RawTarget:   true,
		Since:       since,
		Description: "Closes the workspace for every session: cancels its running operations, waits up to 10 s for them, and stops its processes. Closing a workspace that is not open does nothing.",
		Params:      None{},
		Result:      CloseResult{},
		// Destructive by the command default, kept on purpose: it stops in-flight
		// work, possibly work the UI or another agent started
		// (docs/interfaces/mcp.md §Annotations).
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			// Not cancellable once begun (ADR-0027): ctx is not consulted.
			return CloseResult{}, ws.Close(call.Workspace)
		},
	})
}
