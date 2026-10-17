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
	// CapChooseRoot is workspace.chooseRoot (ADR-0045 §4); CapWatch is workspace.watch
	// (watch.go).
	CapChooseRoot = "workspace.chooseRoot"
)

// since is the API version that introduced the workspace capabilities (plan m1c, R2).
const since = "0.2.0"

// None is the empty parameter object: the workspace is the envelope's, never a
// parameter (ADR-0020 §Scope and workspace addressing).
type None struct{}

// CloseResult is workspace.close's result: nothing (plan m1c, R6).
type CloseResult struct{}

// sinceWatch is the API version that introduced workspace.watch and workspace.chooseRoot
// (plan m2c D17: one version for the milestone).
const sinceWatch = "0.16.0"

// Register adds workspace.open, workspace.describe, workspace.close, workspace.watch and
// workspace.chooseRoot.
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
		Errors:      []capability.ErrorCode{ErrRootInvalid, ErrStateNewer, ErrStateUnreadable, capability.ErrProjectNoGradleRoot},
		Annotations: capability.Annotations{
			Destructive: &notDestructive,
			Reason:      "registers a root and runs no project code",
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			info, err := ws.Open(call.Workspace)
			if err != nil {
				return nil, err
			}
			return withGradle(info), nil
		},
	})

	reg.MustRegister(capability.Descriptor{
		Name:        CapDescribe,
		Kind:        capability.Query,
		Scope:       capability.Workspace,
		Since:       since,
		Description: "Describes the open workspace: its id, root, Gradle root, state, whether it is trusted, and, when degraded, why.",
		Params:      None{},
		Result:      Info{},
		Errors:      []capability.ErrorCode{ErrStateNewer, capability.ErrProjectNoGradleRoot},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			info, err := ws.Describe(call.Workspace)
			if err != nil {
				return nil, err
			}
			// Resolved again here, not in Registry.Describe, which every root lookup calls.
			info.MainRoot = ws.mainRoot(info.Root)
			return withGradle(info), nil
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

	reg.MustRegister(capability.Descriptor{
		Name:         CapWatch,
		Kind:         capability.Command,
		Scope:        capability.Global,
		Statefulness: capability.Streaming,
		Since:        sinceWatch,
		Description:  "Streams every workspace's state transitions to the calling WebSocket session on topic workspace.state, with its name, root and trust. The result lists the workspaces this installation has known, the newest opened first and at most 50, with the live state of those open now, taken after the subscription opened: merge it with the events by id, and call this again after an events.gap.",
		Params:       None{},
		Result:       WatchResult{},
		Events: &capability.Events{
			Topic: StateTopic,
			Types: map[string]any{EventState: StateEvent{}},
		},
		Errors: []capability.ErrorCode{capability.ErrSessionRequired, ErrStateUnreadable},
		Annotations: capability.Annotations{
			Destructive: &notDestructive,
			Reason:      "adds a subscription to the calling session; changes no workspace",
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return ws.watch(ctx, call)
		},
	})

	reg.MustRegister(capability.Descriptor{
		Name:         CapChooseRoot,
		Kind:         capability.Query,
		Scope:        capability.Global,
		Audience:     capability.AudienceHuman,
		LongRunning:  true,
		Since:        sinceWatch,
		Availability: []capability.Requirement{capability.RequiresFolderDialog},
		Description:  "Raises the operating system's folder dialog on the daemon's machine and returns the project root of the folder the user picks, or dismissed when they cancel or leave it for five minutes. One dialog at a time, on macOS only, and a person's choice: the core refuses it from an agent session.",
		Params:       None{},
		Result:       ChooseRootResult{},
		Errors:       []capability.ErrorCode{ErrDialogFailed},
		Resource:     func(*capability.Call) string { return dialogKey },
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return ws.chooseRoot(ctx)
		},
	})
}
