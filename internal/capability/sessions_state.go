package capability

import "time"

// The session registry's observable state. session.watch, the capability that streamed it,
// left with ADR-0137 §1, so no consumer follows the topic any more; the registry still
// broadcasts each change and still lists its rows, because the occupation tests read the
// device half (and, until the worktree half goes, the other) through them. Nothing here is a
// published contract: no descriptor names the topic or these types.
const (
	// SessionStateTopic is the topic a session's changes are broadcast on.
	SessionStateTopic = "session.state"
	SessionStateType  = "state"
)

// SessionState is the payload of a broadcast: the session's state as it stands after the
// change, or just its end.
type SessionState struct {
	ID        string   `json:"id"`
	Name      string   `json:"name,omitempty"`
	Workspace string   `json:"workspace,omitempty"`
	Occupies  string   `json:"occupies,omitempty"`
	Devices   []string `json:"devices,omitempty"`
	Stale     bool     `json:"stale,omitempty"`
	Ended     bool     `json:"ended"`
}

// SessionListed is one row of the registry's listing.
type SessionListed struct {
	ID          string    `json:"id"`
	Name        string    `json:"name,omitempty"`
	Workspace   string    `json:"workspace,omitempty"`
	Occupies    string    `json:"occupies,omitempty"`
	Devices     []string  `json:"devices,omitempty"`
	Stale       bool      `json:"stale,omitempty"`
	ConnectedAt time.Time `json:"connectedAt"`
}
