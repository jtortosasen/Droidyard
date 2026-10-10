package capability

import (
	"encoding/json"
	"time"

	"github.com/invopop/jsonschema"
)

// State is where an operation stands (ADR-0020 §States). A queued operation waits
// behind another for a per-resource lock and names it in blockedBy.
type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

// Terminal reports whether the operation has ended.
func (s State) Terminal() bool {
	return s == StateCompleted || s == StateFailed || s == StateCancelled
}

// RequestEnvelope is one call on the wire (docs/architecture/capability-api.md
// §Request envelope). Workspace is an envelope field, never a parameter.
type RequestEnvelope struct {
	ID         string          `json:"id"`
	Capability string          `json:"capability"`
	Workspace  string          `json:"workspace,omitempty"`
	Params     json.RawMessage `json:"params,omitempty"`
	// WaitMs is how long to wait before returning a still-running operation.
	// Omitted: wait to completion.
	WaitMs *int64 `json:"waitMs,omitempty"`
}

// ResponseEnvelope answers one request (docs/architecture/capability-api.md
// §Response envelope). Result is present iff the operation completed, Error iff it
// failed or was cancelled, never both. A request rejected before it started has an
// Error and no Operation.
type ResponseEnvelope struct {
	ID        string           `json:"id"`
	Operation *OperationStatus `json:"operation,omitempty"`
	Result    json.RawMessage  `json:"result,omitempty"`
	Error     *Error           `json:"error,omitempty"`
	Meta      *Meta            `json:"meta,omitempty"`
}

// OperationStatus is the operation part of a response envelope.
type OperationStatus struct {
	ID        string     `json:"id"`
	State     State      `json:"state"`
	Progress  *Progress  `json:"progress,omitempty"`
	BlockedBy *BlockedBy `json:"blockedBy,omitempty"`
}

// Meta is what a response says about itself rather than about the result.
type Meta struct {
	DurationMs int64 `json:"durationMs,omitempty"`
	Untrusted  bool  `json:"untrusted,omitempty"`
}

// Meta returns the envelope's meta for this outcome: untrusted when it carries
// device- or app-derived data, a marked failure included, and none otherwise
// (ADR-0015 §1; plan m1f, D5).
func (o Outcome) Meta() *Meta {
	if o.Untrusted {
		return &Meta{Untrusted: true}
	}
	return nil
}

// OperationView is the MCP projection of an operation: what a long-running tool and
// operation_wait return (ADR-0020 §Adapter projections). It is one object with no
// top-level union: a completed operation sets Result.
type OperationView struct {
	OperationID string     `json:"operationId" jsonschema:"required"`
	Capability  string     `json:"capability" jsonschema:"required"`
	State       State      `json:"state" jsonschema:"required"`
	Progress    *Progress  `json:"progress,omitempty"`
	BlockedBy   *BlockedBy `json:"blockedBy,omitempty"`
	Result      Object     `json:"result,omitempty"`
}

// Progress reports how far a long-running call has got.
type Progress struct {
	Phase   string `json:"phase,omitempty"`
	Done    int    `json:"done"`
	Total   int    `json:"total,omitempty"`
	Message string `json:"message,omitempty"`
}

// BlockedBy names what a queued operation is waiting behind.
type BlockedBy struct {
	OperationID string `json:"operationId,omitempty"`
	Capability  string `json:"capability,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// The operation.state stream (ADR-0043): one event per transition of a recorded
// operation, published to every operation.watch subscription.
const (
	StateTopic = "operation.state"
	StateType  = "state"
)

// OperationState is the payload of an operation.state event. The envelope's operation
// and workspace name the operation. It carries who asked and where the operation
// stands, never what was asked or answered: no parameter, result, message or cause (the
// trace line's rule; ADR-0043 §2).
type OperationState struct {
	Capability string `json:"capability" jsonschema:"required"`
	// Client is the kind of session that made the call; omitted for a call recorded
	// without one.
	Client    ClientKind `json:"client,omitempty" jsonschema:"enum=cli,enum=mcp,enum=ui"`
	State     State      `json:"state" jsonschema:"required,enum=queued,enum=running,enum=completed,enum=failed,enum=cancelled"`
	StartedAt time.Time  `json:"startedAt" jsonschema:"required"`
	// DurationMs is set on the terminal event only.
	DurationMs *int64 `json:"durationMs,omitempty" jsonschema_description:"Set on the terminal event: milliseconds from the operation's record to its end."`
	// Session is the session that made the call (ADR-0048); omitted for a call recorded
	// without one.
	Session *SessionRef `json:"session,omitempty" jsonschema_description:"The session that made the call; omitted for a call recorded without one."`
	// Code is set on a failed operation's terminal event only.
	Code      ErrorCode  `json:"code,omitempty" jsonschema_description:"Set on a failed operation's terminal event."`
	BlockedBy *BlockedBy `json:"blockedBy,omitempty" jsonschema_description:"Set while the operation is queued."`
}

// SessionRef names the session that made a recorded call, on operation.state events and
// operation.watch rows (ADR-0048). It is an opaque identity for the UI's agents panel: no
// capability takes a session id, so it grants nothing, and it carries no device or
// project text.
type SessionRef struct {
	ID string `json:"id" jsonschema:"required" jsonschema_description:"The daemon session's opaque id."`
	// Name is what the client calls itself: for MCP, clientInfo.name from initialize,
	// at most 64 runes with control and format characters removed; absent before it.
	Name string `json:"name,omitempty" jsonschema_description:"What the client calls itself: for MCP, clientInfo.name from initialize (at most 64 characters, control and format characters removed); absent before it."`
	// Workspace is the session's bound workspace id, if that workspace was open when the
	// call was recorded. It is taken once: under the store's lock it could not be, since
	// admission takes the store's lock inside the workspace's.
	Workspace string `json:"workspace,omitempty" jsonschema_description:"The session's bound workspace id, if that workspace was open when the call was recorded."`
}

// copy returns a copy of the reference, so no event shares memory with the record; nil
// stays nil.
func (r *SessionRef) copy() *SessionRef {
	if r == nil {
		return nil
	}
	c := *r
	return &c
}

// ActiveOperation is one row of operation.watch's snapshot: an operation not yet
// terminal.
type ActiveOperation struct {
	OperationID string      `json:"operationId" jsonschema:"required"`
	Capability  string      `json:"capability" jsonschema:"required"`
	Workspace   string      `json:"workspace,omitempty" jsonschema_description:"The workspace id; omitted for a global operation."`
	Client      ClientKind  `json:"client,omitempty" jsonschema:"enum=cli,enum=mcp,enum=ui"`
	State       State       `json:"state" jsonschema:"required,enum=queued,enum=running"`
	StartedAt   time.Time   `json:"startedAt" jsonschema:"required"`
	BlockedBy   *BlockedBy  `json:"blockedBy,omitempty" jsonschema_description:"Set while the operation is queued."`
	Session     *SessionRef `json:"session,omitempty" jsonschema_description:"The session that made the call; omitted for a call recorded without one."`
}

// Object is a JSON object whose shape is named elsewhere: for operation_wait, by the
// capability the view names. It reflects as {"type":"object"}, ADR-0020's "open
// object"; a bare json.RawMessage would reflect as the empty schema {}.
type Object json.RawMessage

// JSONSchema is the hook invopop/jsonschema honours in place of reflection.
func (Object) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object"}
}

// MarshalJSON writes the object as it is.
func (o Object) MarshalJSON() ([]byte, error) {
	if o == nil {
		return []byte("null"), nil
	}
	return o, nil
}

// UnmarshalJSON keeps a copy of the raw object.
func (o *Object) UnmarshalJSON(b []byte) error {
	*o = append((*o)[:0], b...)
	return nil
}
