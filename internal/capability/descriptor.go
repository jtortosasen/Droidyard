// Package capability defines the contract every capability in the core implements:
// the descriptor, the registry, the invocation model and the typed error.
//
// It is the single source of truth for dispatch, JSON Schema generation, MCP tool
// definitions and the CLI (ADR-0016). A capability that is not registered here does
// not exist.
package capability

import "context"

// Kind describes a capability's side effects on the environment.
type Kind uint8

const (
	// Query has no side effects. It may still be slow.
	Query Kind = iota
	// Command mutates state or triggers work.
	Command
)

func (k Kind) String() string {
	switch k {
	case Query:
		return "query"
	case Command:
		return "command"
	default:
		return "unknown"
	}
}

// Scope says whether a call targets a workspace, and therefore whether the request
// envelope must carry one (ADR-0020).
type Scope uint8

const (
	// Global capabilities read or write no workspace state.
	Global Scope = iota
	// Workspace capabilities require a workspace in the envelope, and reject one
	// otherwise.
	Workspace
)

func (s Scope) String() string {
	switch s {
	case Global:
		return "global"
	case Workspace:
		return "workspace"
	default:
		return "unknown"
	}
}

// Statefulness says whether a capability needs a session to keep state between calls
// (ADR-0010). Operations belong to the daemon, not to a session, so a long-running
// call is not by itself stateful.
type Statefulness uint8

const (
	Stateless Statefulness = iota
	Stateful
	Streaming
)

func (s Statefulness) String() string {
	switch s {
	case Stateless:
		return "stateless"
	case Stateful:
		return "stateful"
	case Streaming:
		return "streaming"
	default:
		return "unknown"
	}
}

// Audience says who may call a capability. Human capabilities are never exposed over
// MCP, and the transport rejects them rather than merely hiding them (ADR-0015 §6).
type Audience uint8

const (
	AudienceAll Audience = iota
	AudienceHuman
)

func (a Audience) String() string {
	switch a {
	case AudienceAll:
		return "all"
	case AudienceHuman:
		return "human"
	default:
		return "unknown"
	}
}

// Requirement names an external tool a capability needs. Availability is reported
// through system.capabilities, and an unavailable requirement produces a structured
// error rather than a capability that silently disappears.
type Requirement string

const (
	RequiresADB      Requirement = "adb"
	RequiresEmulator Requirement = "emulator"
	RequiresSDK      Requirement = "sdk"
	RequiresGradle   Requirement = "gradle"
	RequiresGit      Requirement = "git"
)

// Annotations are hints for a client's approval interface. They are never
// enforcement: the authorization boundary is separate and re-checked immediately
// before a handler runs (ADR-0015).
//
// Read-only and idempotent are derived from Kind and Idempotent and cannot be
// overridden. Destructive and open-world can, and an override must carry a reason.
type Annotations struct {
	// Destructive reports that the call may destroy data or work it cannot restore,
	// including another session's. Nil means "derived".
	Destructive *bool
	// OpenWorld reports that the call reaches beyond the core's own state and the
	// workspace's files. Nil means "derived".
	OpenWorld *bool
	// Reason explains an override. Required when either pointer is set.
	Reason string
}

// Handler runs a capability. It must honour ctx cancellation and must be safe for
// concurrent invocation.
type Handler func(ctx context.Context, call *Call) (any, error)

// Call carries everything a handler needs beyond its decoded parameters.
type Call struct {
	// OperationID is the id Start returned, set before the handler runs. A domain
	// that keeps its own records uses it as theirs: a build's id is its operation id
	// (ADR-0020 §Operation capabilities). Empty for operation.wait and
	// operation.cancel, which are not operations.
	OperationID string
	// Workspace is the canonical workspace id. Empty for global capabilities; the
	// target as the envelope gave it for a RawTarget capability.
	Workspace string
	// Params is the decoded parameter struct.
	Params any
	// Session is the request's session; nil for a Stateless capability's call made
	// without one. A Stateful or Streaming capability always has one.
	Session Session
	// Progress reports progress for a long-running call. Never nil.
	Progress func(done, total int, message string)
	// Untrusted marks result fields that carry device- or app-derived data, by
	// field name, so the adapter can signal them per result (ADR-0015 §1).
	Untrusted []string
}

// Descriptor is one capability. It is registered explicitly by its domain package at
// startup, never discovered by reflection, so the set of capabilities is greppable.
type Descriptor struct {
	// Name is "<domain>.<verb>", matching ^[a-z]+\.[a-z][A-Za-z0-9]*$: never an
	// underscore, so the MCP mapping (dot to underscore) is reversible.
	Name string

	Kind         Kind
	Scope        Scope
	Statefulness Statefulness

	// LongRunning says the call may outlive an adapter's wait budget (ADR-0020). It
	// does not make the call stateful.
	LongRunning bool

	// Idempotent says repeating the call is safe and equivalent.
	Idempotent bool

	// AcceptsIdempotencyKey says the call dedupes retries on the envelope's key.
	AcceptsIdempotencyKey bool

	// RawTarget marks workspace.open and workspace.close: Start admits the call
	// whether or not its workspace is open, and the handler receives the envelope's
	// target as given, to judge it itself (ADR-0026: validation is the handler's).
	// Such a call is not an operation of the workspace, so a close never cancels or
	// waits on an open or a close (ADR-0027). Not published.
	RawTarget bool

	Audience Audience

	// Params and Result are struct instances; the JSON Schema is generated from them.
	Params any
	Result any

	// Errors lists every code the handler returns itself; a projecting capability
	// passes the waited operation's code through. Part of the published schema.
	Errors []ErrorCode

	Annotations Annotations

	// Availability lists the external tools the capability needs.
	Availability []Requirement

	// Since is the API version that introduced the capability.
	Since string
	// Deprecated is set once the capability is on its way out.
	Deprecated *Deprecation

	Description string

	Handler Handler

	// unrecorded marks operation.wait and operation.cancel, whose calls are not
	// operations (ADR-0020 §Core primitives). Unexported, so only this package can
	// register such a capability.
	unrecorded bool
}

// Recorded reports whether a call to the capability becomes an operation. Every
// capability's does except operation.wait's and operation.cancel's: callers run those
// through Invoke, never Start.
func (d Descriptor) Recorded() bool { return !d.unrecorded }

// Deprecation marks a capability as deprecated without removing it.
type Deprecation struct {
	Since       string
	Replacement string
}
