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

// Scope says whether a call targets a project, and therefore whether its parameters must
// carry `project` (ADR-0137 §3).
type Scope uint8

const (
	// Global capabilities read or write no project state, and take no `project`.
	Global Scope = iota
	// Workspace capabilities take `project` (ProjectParam), the path of the project they
	// act on: the schema package adds the property, and the core takes it off the
	// parameters before it decodes the rest.
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

// Occupies is how a capability holds a device for the calling session
// (Descriptor.Occupies).
type Occupies string

const (
	// OccupiesAct: an acting call (install, uninstall, launch, tap or type through an
	// element, emulator stop or wipe) holds a free device and may be refused a held one.
	OccupiesAct Occupies = "act"
	// OccupiesBoot: a call that boots the device it holds, taking it over; never refused.
	OccupiesBoot Occupies = "boot"
)

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
	// Workspace is the canonical id of the project's workspace; empty for a global
	// capability. Root and GradleRoot are what the call's `project` resolved to.
	Workspace string
	// Root is the project root `project` resolved to (ADR-0137 §3; plan m3j Q2), canonical:
	// what trust, the workspace id and every published path are judged on, never the path
	// the caller passed. Empty for a global capability.
	Root string
	// GradleRoot is the canonical Gradle root inside Root, resolved at Start with it: the
	// folder the build lock is keyed on, so two spellings of one project queue. Empty for a
	// global capability.
	GradleRoot string
	// Params is the decoded parameter struct.
	Params any
	// Session is the request's session; nil for a Stateless capability's call made
	// without one. A Stateful or Streaming capability always has one.
	Session Session
	// Progress reports progress for a long-running call. Never nil.
	Progress func(done, total int, message string)
	// Untrusted is the handler's own mark for this call, set before it returns
	// (artifact.read: an artifact carries its producer's mark, ADR-0015 §1). On a
	// completed call, nil leaves the descriptor's derived flag in force and non-nil
	// sets the result's mark. On a failed call the derived flag never applies: true
	// marks the error, whose details quote device text, and the capability layer
	// appends UntrustedErrorNote to its message (plan m1f, D5). A cancelled call is
	// never marked.
	Untrusted *bool
	// Authorized is what the descriptor's Authorize hook returned: what it read, for
	// the handler to use instead of reading it again. Nil without a hook.
	Authorized any
	// Origin is the session the call came from, as the adapter named it (Request.Origin).
	// A capability that holds something for a session (a device) reads its id from it; the
	// zero Origin is a call without a session.
	Origin Origin
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

	// ProjectOptional says a workspace-scoped capability takes `project` only for some of
	// its calls (ADR-0137 §4: device.install, where `project` goes with `build` and an `apk`
	// install names none). The schema does not require `project`, and the core resolves and
	// enters a project only when the call names one; a call without it runs with no
	// workspace on its Call, so the capability's own validation must refuse the calls that
	// need one. It is for a workspace-scoped descriptor and for device.install alone.
	ProjectOptional bool

	// LongRunning says the call may outlive an adapter's wait budget (ADR-0020). It
	// does not make the call stateful.
	LongRunning bool

	// Occupies says the capability acts on a device the calling MCP session then holds
	// (ADR-0094 §2): OccupiesAct for an acting call, which holds a free device and is
	// refused one another live session holds; OccupiesBoot for a call that boots the
	// device it then holds. The device package makes the check in the call's preamble (its
	// Authorize hook), so the descriptor lists the code in Errors for an act and not for a
	// boot, which is never refused. Not published: the schema shows the code.
	Occupies Occupies

	// Idempotent says repeating the call is safe and equivalent.
	Idempotent bool

	// AcceptsIdempotencyKey says the call dedupes retries on the envelope's key.
	AcceptsIdempotencyKey bool

	Audience Audience

	// Lendable says a host serves the capability to a paired peer (ADR-0098 §3; ADR-0099
	// §2). A peer's call to any other capability is REQUEST_NOT_ALLOWED_HERE, before
	// anything is recorded. A host-global capability only: a peer names no project of the
	// host's, and Register refuses the flag on any other. Not published: the peer protocol
	// is internal to the two daemons.
	Lendable bool

	// Resource names the per-resource lock a call holds while it runs, as a function
	// of the call: its decoded params (Call.Params) and, for a workspace-scoped
	// capability, the canonical Gradle root (Call.GradleRoot) — the key a build takes is
	// "gradle.root:<root>", so two spellings of one project queue (plan m3j step 6). "" takes none. One key per
	// call, so acquisition cannot deadlock. Calls on one key run one at a time, in the
	// order they were started; a call behind another is queued and names it in
	// blockedBy (ADR-0020 §Core primitives). The scheduler takes the lock, not the
	// handler (overview.md §Concurrency). A domain may also serialise short device
	// I/O inside one bounded read with a lock of its own (the UI reader's per-device
	// lock, plan m1f R2): the scheduler's key is per call, while such a lock is per
	// read and is waited inside the read's own deadline, not a second Resource key.
	// Not published.
	Resource func(call *Call) string

	// Authorize is the authorization re-check, run by the scheduler immediately before
	// the handler: holding the Resource key when there is one, at once when there is
	// none. An error fails the operation with that error and the handler never runs;
	// the value returned reaches the handler as Call.Authorized
	// (ADR-0020 §Core primitives; ADR-0015 §2). Not published.
	Authorize func(ctx context.Context, call *Call) (any, error)

	// Target names what a call acts on, as a function of its decoded parameters
	// (Call.Params), for the operation.state events and operation.watch rows (ADR-0076).
	// It reads only the caller's own parameters, never device or project text, and may
	// return nil. When the operation is recorded each field is cleaned of control and
	// format characters and cut to OpTargetMax bytes.
	// Nil means the capability has none. Not published as such: the schema carries the
	// event field.
	Target func(params any) *OpTarget

	// Params and Result are struct instances; the JSON Schema is generated from them.
	Params any
	Result any

	// Errors lists every code the handler returns itself; a projecting capability
	// passes the waited operation's code through. Part of the published schema.
	Errors []ErrorCode

	// Events declares the events a streaming capability publishes to the subscription
	// its own call opened, so the published schema types them (ADR-0041 §4; ADR-0016).
	// Nil for a capability that publishes none.
	Events *Events

	Annotations Annotations

	// Availability lists the external tools the capability needs.
	Availability []Requirement

	// Since is the API version that introduced the capability.
	Since string
	// Deprecated is set once the capability is on its way out.
	Deprecated *Deprecation

	Description string

	Handler Handler

	// Slim, when set, replaces a completed result with a small one once the result
	// has been delivered to its first caller, so a retained operation does not hold a
	// large payload for the retention period (ADR-0020 §Retention; plan m1e, step 7).
	// Not published: it shapes what operation.wait returns later, never the schema.
	Slim func(result any) any

	// Text, when set, renders a completed result as compact text (ADR-0036 Decision 2;
	// mcp.md §Results). The MCP adapter sends it as the text block, for the tool and for
	// operation.wait, and the structured content stays the full result. "" means no
	// rendering for this value: the adapter falls back to the result's compact JSON. A
	// renderer builds its text with Text, which quotes every string it is given that is
	// not a safe word, and writes no string from a field tagged untrusted any other way.
	// Not published.
	Text func(result any) string

	// untrusted is derived by Register from the tags on Result's fields, never
	// written by hand (ADR-0015 §1).
	untrusted bool

	// unrecorded marks operation.wait and operation.cancel, whose calls are not
	// operations (ADR-0020 §Core primitives). Unexported, so only this package can
	// register such a capability.
	unrecorded bool
}

// Untrusted reports whether the capability's result has a field tagged untrusted,
// as Register derived it (ADR-0015 §1). A registered descriptor carries it; one that
// never went through Register reports false.
func (d Descriptor) Untrusted() bool { return d.untrusted }

// Recorded reports whether a call to the capability becomes an operation. Every
// capability's does except operation.wait's and operation.cancel's: callers run those
// through Invoke, never Start.
func (d Descriptor) Recorded() bool { return !d.unrecorded }

// Events is the stream a capability owns: the topic its events are published on and, per
// event type, the payload's Go type (a struct value). The schema package publishes it as
// `events: {topic, types: {<type>: <schema>}}`, inlined and with the untrusted marks the
// payload's tags give (ADR-0015 §1). The topic is not subscribable through
// events.subscribe: only the subscription the capability's call opened receives it.
type Events struct {
	Topic string
	Types map[string]any
}

// Deprecation marks a capability as deprecated without removing it.
type Deprecation struct {
	Since       string
	Replacement string
}

// ProjectParam is the name of the parameter every workspace-scoped capability takes (ADR-0137
// §3): the absolute path of the project's folder, or of a folder inside it. The schema
// package adds the property to the input schema of each such descriptor, so no parameter
// struct declares it, and Register refuses one that does; the core takes it off the
// parameters before it decodes the rest, so no handler sees it.
const ProjectParam = "project"
