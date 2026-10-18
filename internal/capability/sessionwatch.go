package capability

import (
	"context"
	"time"
)

// session.watch and its stream (ADR-0083 §1).
const (
	SessionWatch = "session.watch"
	// SessionStateTopic is where session.state events are broadcast: the stream of
	// session.watch, ring-less, which events.subscribe does not serve.
	SessionStateTopic = "session.state"
	SessionStateType  = "state"
)

// sinceSessionWatch is the API version that introduced session.watch.
const sinceSessionWatch = "0.28.0"

// SessionState is the payload of a session.state event: the session's state as it stands
// after the change, or just its end. It carries nothing a call asked or answered.
type SessionState struct {
	ID        string `json:"id" jsonschema:"required" jsonschema_description:"The daemon's id for the MCP session. It grants nothing."`
	Name      string `json:"name,omitempty" jsonschema_description:"What the client called itself, bounded to 64 characters; absent until the client has said."`
	Workspace string `json:"workspace,omitempty" jsonschema_description:"The id of the workspace the session is bound to now; absent before it is bound."`
	Occupies  string `json:"occupies,omitempty" jsonschema_description:"The id of the workspace of the linked git worktree the session holds, so that no other chat enters there (ADR-0094); absent when it holds none."`
	Stale     bool   `json:"stale,omitempty" jsonschema_description:"True while the session has made no request for the configured time (120 minutes by default): another chat may then take over what it holds. Absent otherwise."`
	Ended     bool   `json:"ended" jsonschema:"required" jsonschema_description:"True on the session's last event, which carries nothing else: the session is gone."`
}

// SessionListed is one row of session.watch's snapshot.
type SessionListed struct {
	ID          string    `json:"id" jsonschema:"required" jsonschema_description:"The daemon's id for the MCP session, as on session.state. It grants nothing."`
	Name        string    `json:"name,omitempty" jsonschema_description:"What the client called itself, bounded to 64 characters; absent until the client has said."`
	Workspace   string    `json:"workspace,omitempty" jsonschema_description:"The id of the workspace the session is bound to now; absent before it is bound."`
	Occupies    string    `json:"occupies,omitempty" jsonschema_description:"The id of the workspace of the linked git worktree the session holds, as on session.state; absent when it holds none."`
	Stale       bool      `json:"stale,omitempty" jsonschema_description:"True while the session is idle, as on session.state; absent otherwise."`
	ConnectedAt time.Time `json:"connectedAt" jsonschema:"required"`
}

// SessionWatchParams are session.watch's parameters: none.
type SessionWatchParams struct{}

// SessionWatchResult is session.watch's result.
type SessionWatchResult struct {
	SubscriptionID string          `json:"subscriptionId" jsonschema:"required" jsonschema_description:"The subscription the session.state events arrive on, over the WebSocket session that made the call, until events.unsubscribe or the session's end."`
	Sessions       []SessionListed `json:"sessions" jsonschema:"required" jsonschema_description:"The MCP sessions connected now, the oldest first, taken after the subscription opened: merge it with the events by id."`
}

// registerSessionWatch adds session.watch. Like workspace.watch it takes the
// subscription first and the snapshot second, so a session that connects, changes or ends
// between the two reaches the watcher as an event, and one that did so before is in the
// snapshot (ADR-0083 §1; ADR-0045 §3).
func (o *Operations) registerSessionWatch() {
	notDestructive := false
	o.reg.MustRegister(Descriptor{
		Name:         SessionWatch,
		Kind:         Command,
		Scope:        Global,
		Statefulness: Streaming,
		Since:        sinceSessionWatch,
		Description:  "Streams the agents connected over MCP to the calling WebSocket session on topic session.state, from the moment each connects: on connecting, on its name, on every binding and on its end. The result lists the sessions connected now, oldest first, taken after the subscription opened: merge it with the events by id, and call this again after an events.gap.",
		Params:       SessionWatchParams{},
		Result:       SessionWatchResult{},
		Events: &Events{
			Topic: SessionStateTopic,
			Types: map[string]any{SessionStateType: SessionState{}},
		},
		Errors: []ErrorCode{ErrSessionRequired},
		Annotations: Annotations{
			Destructive: &notDestructive,
			Reason:      "adds a subscription to the calling WebSocket session; changes no MCP session",
		},
		Handler: func(ctx context.Context, call *Call) (any, error) {
			sess, ok := call.Session.(Follower)
			if !ok {
				return nil, NewError(ErrSessionRequired, "events need a session that can carry them").
					WithNextStep("subscribe over the WebSocket session, GET /v1/ws")
			}
			id, err := sess.Follow([]string{SessionStateTopic})
			if err != nil {
				return nil, err
			}
			if hook := o.sessions.watchHook.Load(); hook != nil {
				(*hook)()
			}
			list := o.sessions.listed()
			if err := ctx.Err(); err != nil {
				sess.Unsubscribe(id)
				return nil, err
			}
			return SessionWatchResult{SubscriptionID: id, Sessions: list}, nil
		},
	})
}
