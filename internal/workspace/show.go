package workspace

import (
	"context"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The names of workspace.show and workspace.forget (ADR-0084, ADR-0086).
const (
	CapShow   = "workspace.show"
	CapForget = "workspace.forget"
)

// The types of workspace.state events beside EventState. Both carry the workspace in the
// frame and an empty payload.
const (
	// EventShow asks every open tab to select the workspace, opening it if it is closed
	// (ADR-0084 §1).
	EventShow = "show"
	// EventForgotten says the workspace left the recent list (ADR-0086 §1).
	EventForgotten = "forgotten"
)

// sinceShow is the API version that introduced workspace.show and workspace.forget (plan
// m2q: one version for the milestone).
const sinceShow = "0.28.0"

// ShowEvent is the payload of a workspace.state event of type show: empty, the workspace
// is the frame's.
type ShowEvent struct{}

// ForgottenEvent is the payload of a workspace.state event of type forgotten: empty, the
// workspace is the frame's.
type ForgottenEvent struct{}

// ShowParams are workspace.show's parameters.
type ShowParams struct {
	Workspace string `json:"workspace,omitempty" jsonschema_description:"The id of the workspace the tabs select; absent, they keep what they show and only the count is returned."`
}

// ShowResult is workspace.show's result.
type ShowResult struct {
	Tabs int `json:"tabs" jsonschema:"required" jsonschema_description:"How many workspace.watch subscriptions were told, which is how many open tabs there are; 0 when none is."`
}

// ForgetParams are workspace.forget's parameters.
type ForgetParams struct {
	Workspace string `json:"workspace" jsonschema:"required" jsonschema_description:"The id of a workspace that is not open."`
}

// ForgetResult is workspace.forget's result: nothing.
type ForgetResult struct{}

// show is workspace.show: every workspace.watch subscription is offered a show event, and
// the count of them is the answer (ADR-0084 §1). Nothing is stored: a tab that connects
// later is not told.
func (r *Registry) show(_ context.Context, call *capability.Call) (any, error) {
	p := call.Params.(ShowParams)
	if r.opts.States == nil {
		return ShowResult{}, nil
	}
	return ShowResult{Tabs: r.opts.States.Broadcast(StateTopic, EventShow, p.Workspace, "", ShowEvent{})}, nil
}

// forget is workspace.forget (ADR-0086): the index entry of a workspace that is not open
// is removed and ws/<wsid>/ stays, so opening the root again lists its history. An open
// one is refused, and an id the index does not hold answers as done, publishing nothing.
func (r *Registry) forget(_ context.Context, call *capability.Call) (any, error) {
	id := call.Params.(ForgetParams).Workspace
	removed, err := r.dropFromIndex(id)
	if err != nil {
		return nil, err
	}
	if removed && r.opts.States != nil {
		// Under pubMu and only while it is still closed: an open stores its state before it
		// takes pubMu to publish, so forgotten either precedes its opening event, which
		// lists the row again, or is not sent (a tab never drops a workspace that opened).
		r.pubMu.Lock()
		if s := r.State(id); s == "" || s == Closed {
			r.opts.States.Broadcast(StateTopic, EventForgotten, id, "", ForgottenEvent{})
		}
		r.pubMu.Unlock()
	}
	return ForgetResult{}, nil
}

// dropFromIndex deletes id's entry from workspaces.json and reports whether there was one. The state is read under indexMu,
// which an open takes (recordOpen) only after it has left closed, so an open that has
// begun is never forgotten under. An index of a newer version is not written (plan m1c,
// R16): the entry leaves this daemon's memory only.
func (r *Registry) dropFromIndex(id string) (bool, error) {
	r.indexMu.Lock()
	defer r.indexMu.Unlock()
	if state := r.State(id); state != "" && state != Closed {
		return false, capability.Errorf(capability.ErrWorkspaceOpen, "workspace %q is %s, so it is not removed from the recent projects", id, state).
			WithNextStep("close the workspace first, then remove it again")
	}
	if err := r.loadIndex(); err != nil {
		return false, stateUnreadable("workspaces.json", err)
	}
	if _, ok := r.idx.Workspaces[id]; !ok {
		return false, nil
	}
	delete(r.idx.Workspaces, id)
	if !r.indexNewer {
		if err := r.opts.Store.Write(indexFile, r.idx); err != nil {
			return false, stateUnreadable("workspaces.json", err)
		}
	}
	return true, nil
}
