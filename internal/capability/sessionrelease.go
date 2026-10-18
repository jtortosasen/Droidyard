package capability

import "context"

// SessionRelease is the owner's release of an occupation (ADR-0094 §3).
const SessionRelease = "session.release"

// sinceSessionRelease is the API version that introduced session.release: the version the
// occupation (ADR-0094) is published in.
const sinceSessionRelease = "0.29.0"

// SessionReleaseParams are session.release's parameters: exactly one of the two.
type SessionReleaseParams struct {
	Workspace string `json:"workspace,omitempty" jsonschema_description:"The workspace id of a held linked worktree, as session.watch's occupies. Give this or device."`
	Device    string `json:"device,omitempty" jsonschema_description:"The serial of a held device, as session.watch's devices. Give this or workspace."`
}

// SessionReleaseResult is session.release's result: nothing. A worktree or device nobody
// held answers the same.
type SessionReleaseResult struct{}

// registerSessionRelease adds session.release: a person's action, so an agent session is
// refused at the MCP transport (ADR-0015 §6). It ends one occupation and moves no session:
// the holder stays bound where it was, and only its row changes (ADR-0094 §3).
func (o *Operations) registerSessionRelease() {
	notDestructive := false
	o.reg.MustRegister(Descriptor{
		Name:        SessionRelease,
		Kind:        Command,
		Scope:       Global,
		Audience:    AudienceHuman,
		Idempotent:  true,
		Since:       sinceSessionRelease,
		Description: "Ends the occupation of one linked worktree or one device, whoever holds it; the holder stays bound where it was, and one nobody holds answers as done. Give exactly one of workspace and device; a person's action, refused from an agent session.",
		Params:      SessionReleaseParams{},
		Result:      SessionReleaseResult{},
		Errors:      []ErrorCode{ErrInvalidParams},
		Annotations: Annotations{
			Destructive: &notDestructive,
			Reason:      "removes an in-memory mark and changes no binding, file or device",
		},
		// The device it frees is the target (ADR-0076 §2), so the devices panel's mirror of
		// the operation is that device's; releasing a worktree has none.
		Target: func(params any) *OpTarget {
			if d := params.(SessionReleaseParams).Device; d != "" {
				return &OpTarget{Device: d}
			}
			return nil
		},
		Handler: func(ctx context.Context, call *Call) (any, error) {
			p := call.Params.(SessionReleaseParams)
			if (p.Workspace == "") == (p.Device == "") {
				return nil, NewError(ErrInvalidParams, "give exactly one of workspace and device").
					WithNextStep("pass the workspace id of a held worktree, or the serial of a held device")
			}
			if p.Workspace != "" {
				o.sessions.ReleaseWorkspace(p.Workspace)
			} else {
				o.sessions.ReleaseDevice(p.Device)
			}
			return SessionReleaseResult{}, nil
		},
	})
}
