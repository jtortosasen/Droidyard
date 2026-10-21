package capability

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ClientKind is what a session's hello says it is (ADR-0019 §7), ui for an HTTP request
// (ADR-0015 §6), or peer for a paired Droidyard on the peer link (ADR-0098 §5). The core
// reads it for three rules: human-audience capabilities are refused to MCP (ADR-0015 §6),
// only a Lendable capability is served to a peer (ADR-0098 §3), and an MCP session or a
// peer reaches only its own operations (ADR-0101).
type ClientKind string

const (
	ClientCLI  ClientKind = "cli"
	ClientMCP  ClientKind = "mcp"
	ClientUI   ClientKind = "ui"
	ClientPeer ClientKind = "peer"
)

// Session is a session that can carry the state and the stream of a stateful or
// streaming capability (ADR-0010 §Decision as amended; plan m1d, D6): in 1d, only a
// WebSocket connection. A CLI call, an MCP session and a plain HTTP request are not
// one, so their requests carry none. The event layer implements it; capability does
// not import it, and a handler asserts the session it needs.
type Session interface {
	// ID names the session, for logs and tests.
	ID() string
}

// Follower is a session that can open a subscription of its own to a capability's
// stream, the topic of which events.subscribe does not serve (workspace.watch,
// ADR-0045 §3). The event layer implements it, as it does Session; capability and the
// domains do not import it, and a handler asserts the session it needs.
type Follower interface {
	Session
	// Follow opens a subscription that belongs to the session, for topics, and returns
	// its id; it fails with SESSION_REQUIRED once the session has ended.
	Follow(topics []string) (string, error)
	// Unsubscribe ends one of the session's own subscriptions.
	Unsubscribe(id string)
}

// Retention of terminal operations (ADR-0020 §Retention): 15 minutes, and at least
// the last 200.
const (
	retainFor = 15 * time.Minute
	retainMin = 200
)

// queueBound is how long a call not flagged long-running waits queued for its
// resource before it fails OPERATION_QUEUE_TIMEOUT (ADR-0020 §Core primitives: 20 s,
// a working default).
const queueBound = 20 * time.Second

// Request is one call as the core sees it, whichever adapter carried it.
type Request struct {
	Capability string
	// Workspace is required iff the capability is workspace-scoped, and rejected
	// otherwise (ADR-0020 §Scope).
	Workspace string
	// Params is the raw parameter object; empty means {}.
	Params json.RawMessage
	// Client is the calling session's kind.
	Client ClientKind
	// Session is the session the call came in on, when it can carry events; nil
	// otherwise. A Stateful or Streaming capability is rejected without one, before
	// any operation exists (ADR-0010; plan m1d, D6).
	Session Session
	// Origin names the session the call came from, for the operation.state events
	// (ADR-0048); the zero Origin (no ID) is a call without one.
	Origin Origin
}

// Origin is who made a call, as an adapter knows it: the daemon's id for the session, the
// name the client gave itself, and the root the session is bound to. It is recorded with
// the operation and appears on operation.state as SessionRef (ADR-0048). It is not a
// Session: it cannot carry events, and no capability takes it.
type Origin struct {
	// ID is the daemon session's id; empty means the call has no session.
	ID string
	// Name is what the client calls itself (for MCP, clientInfo.name); empty before it
	// is known. For a peer it is the name the host's configuration gives it, never one the
	// peer sends (ADR-0098 §5). The store bounds it (SessionName).
	Name string
	// Root is the session's bound root; empty for an unbound session.
	Root string
	// Client is the kind of the connection the call came in on. Invoke and Start set it
	// from Request.Client, so the core never reads a kind the adapter did not state. A
	// capability that holds something for a session (a device, ADR-0094) acts for a
	// connection whose Client is mcp and whose ID is the session, never for one that was
	// only joined to it.
	Client ClientKind
	// Chat is the chat id the connection's hello named, when it did (ADR-0088); empty
	// otherwise.
	Chat string
	// Joined is the id of the MCP session a non-MCP connection was joined to by its chat
	// (ADR-0095 §1); empty when none. ID stays the connection's own, so a joined call
	// moves no binding: Joined only attributes the call to the session (the operation's
	// session ref) and keeps the session from going idle.
	Joined string
}

// SessionNameMax is the most runes a SessionRef's name keeps (ADR-0048 §1).
const SessionNameMax = 64

// stripInvisible removes invalid UTF-8 and the control (Cc) and format (Cf) characters,
// which include the bidirectional overrides, from a string a client chose.
func stripInvisible(s string) string {
	s = strings.ToValidUTF8(s, "")
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

// SessionName bounds a client-chosen name for the events: invalid UTF-8, control (Cc)
// and format (Cf) characters are removed, surrounding white space is trimmed, and at
// most SessionNameMax runes are kept.
func SessionName(name string) string {
	name = stripInvisible(name)
	name = strings.TrimSpace(name)
	if r := []rune(name); len(r) > SessionNameMax {
		name = strings.TrimSpace(string(r[:SessionNameMax]))
	}
	return name
}

// Outcome is an operation as Wait reports it. Result is set iff State is completed,
// Error iff it is failed or cancelled (ADR-0020 §States).
type Outcome struct {
	OperationID string
	Capability  string
	State       State
	Result      any
	Error       *Error
	Progress    *Progress
	// Untrusted marks an outcome that carries device- or app-derived data. A
	// completed operation is marked with the descriptor's derived flag, unless the
	// handler set its own through Call; a failed one only with the handler's own mark,
	// never the derived flag, since an error from a capability whose result is tagged
	// need not quote device text (R5; plan m1f, D5).
	Untrusted bool
	// BlockedBy names the operation a queued one waits behind; nil in any other state.
	BlockedBy *BlockedBy
}

type operation struct {
	id         string
	capability string
	// workspace is the canonical id of the workspace the operation runs in; empty for
	// a global call and for workspace.open and workspace.close.
	workspace string
	// client is the kind of the session that made the call, and started when the
	// operation was recorded: both only for the trace line.
	client  ClientKind
	started time.Time
	// session is who made the call, as the events name it; nil without one. Fixed when
	// the operation is recorded.
	session *SessionRef
	// target is what the call acts on, as its descriptor declares it from the validated
	// parameters; nil when it declares none. Fixed when the operation is recorded
	// (ADR-0076).
	target *OpTarget
	// owner is who may wait on and cancel the operation besides the UI and the CLI
	// (ADR-0101). Fixed when the operation is recorded, so it holds after the session that
	// started the operation has gone.
	owner opOwner

	// Guarded by Operations.mu.
	// announced is set where the operation's start event is queued, so Active never
	// lists an operation whose place (running or queued) is not decided yet.
	announced       bool
	state           State
	result          any
	err             *Error
	progress        *Progress
	blockedBy       *BlockedBy
	waiter          *waiter // set while queued
	untrusted       bool
	slim            func(any) any // the descriptor's Slim, applied after first delivery
	cancelRequested bool
	// forced: cancelled by a workspace close, so the operation ends cancelled
	// whatever its handler returns (ADR-0027).
	forced   bool
	finished time.Time

	cancel context.CancelFunc
	done   chan struct{}
}

// opOwner is what the core remembers of the session that started an operation, for the
// check operation.wait and operation.cancel make (ADR-0101). The chat is read from the
// session registry when the operation starts, not when it is asked about: the session may
// have ended by then, and a session of the same chat that connects later reaches it.
type opOwner struct {
	session string // the starting session's id; empty without one
	chat    string // an MCP session's chat id (ADR-0088); empty without one
	peer    string // a peer's configured name
}

// Workspaces admits workspace-scoped calls. The workspace registry implements it;
// capability does not import it, so the store is given one (UseWorkspaces).
type Workspaces interface {
	// Admit resolves target, a workspace id or an absolute root, to its canonical
	// id and, if that workspace is open (ready or degraded), calls record with the
	// id and returns true. The check and record run under the lock that a close
	// takes to enter closing, so every admitted call is recorded before a close
	// collects the workspace's operations to cancel them (ADR-0027). Otherwise it
	// returns false and does not call record.
	Admit(target string, record func(id string)) bool
}

// Publisher puts an event on the daemon's bus (overview.md §Events). The event
// layer implements it; capability does not import it, so whatever publishes (the
// workspace registry, the store) is given one. Publish never blocks on a subscriber.
type Publisher interface {
	// Publish stamps one event with the bus's epoch, the next seq and the time, and
	// delivers it before it returns. workspace is the workspace id, "" for a global
	// event; operation the operation id, or "".
	Publish(topic, typ, workspace, operation string, payload any)
}

// StatePublisher puts an operation.state event on the daemon's bus (ADR-0043). The event
// layer implements it; capability does not import it. Broadcast takes a seq but never
// enters the replay ring, and offers the event to every subscription holding the topic.
// It never blocks on a subscriber, and returns how many subscriptions it offered the event.
type StatePublisher interface {
	Broadcast(topic, typ, workspace, operation string, payload any) int
}

// Progress is published on operation.progress, type progress, with the operation id,
// the operation's workspace and a Progress payload (overview.md §Events:
// "Progress goes to the event bus, tagged with the operation id"; plan m1d, D4).
const (
	ProgressTopic = "operation.progress"
	ProgressType  = "progress"
)

// Operations is the operation store: every recorded call, keyed by a daemon-wide id.
// An operation's lifetime belongs to the store, never to a caller's context: a
// caller that stops waiting leaves the operation running (ADR-0020 §Core primitives).
type Operations struct {
	reg *Registry
	ws  Workspaces
	pub Publisher
	// spub receives the operation.state events (UseStatePublisher).
	spub StatePublisher

	mu       sync.Mutex
	ops      map[string]*operation
	terminal []string // ids of terminal operations, oldest first
	now      func() time.Time

	// traces holds the trace lines of operations that reached a terminal state and
	// are not yet written; traceLog writes them (see flush). Guarded by mu.
	traces   []string
	traceLog func(format string, v ...any)

	// events holds the operation.state events not yet published, in transition order,
	// appended under mu where the transition happens. flush publishes them after mu is
	// released. Guarded by mu.
	events []stateEvent
	// flushMu makes a flush (take the queues, write, publish) one step at a time, so two
	// callers cannot publish out of the order the queue holds. It is never held together
	// with mu in the other order: flush takes mu while holding flushMu, and nothing
	// holding mu takes flushMu.
	flushMu sync.Mutex

	// locks holds the per-resource locks (Descriptor.Resource), by key. An entry
	// exists while its key is held or awaited. Guarded by mu.
	locks map[string]*lock
	// queueBound is the queued-call bound; a field so a test can shorten it.
	queueBound time.Duration

	// sessions are the MCP sessions in progress, which a Binding capability's caller must
	// be one of (Descriptor.Binding).
	sessions *Sessions

	// disconnects are the hooks Disconnected runs, in the order they were registered. Guarded
	// by hookMu.
	hookMu      sync.Mutex
	disconnects []func(originID string)
}

// stateEvent is one queued operation.state event.
type stateEvent struct {
	workspace, operation string
	payload              OperationState
}

// lock is one resource key's holder and the operations queued behind it, oldest first.
type lock struct {
	holder  *operation
	waiters []*waiter
}

// waiter is one queued operation. granted is set, and ch closed, by the release that
// hands it the lock. Guarded by Operations.mu.
type waiter struct {
	op      *operation
	key     string
	ch      chan struct{}
	granted bool
	since   time.Time
}

// NewOperations returns an empty store that dispatches from reg.
func NewOperations(reg *Registry) *Operations {
	return &Operations{reg: reg, ops: make(map[string]*operation), now: time.Now,
		locks: make(map[string]*lock), queueBound: queueBound, sessions: NewSessions()}
}

// Sessions is the registry of MCP sessions: the adapter registers each connection in it,
// and a Binding capability moves the calling session's binding through it.
func (o *Operations) Sessions() *Sessions { return o.sessions }

// OnDisconnect registers f to run whenever Disconnected is told that a native connection
// ended (ADR-0108 §3): what a connection owns beyond its operations is released there, as a
// WebSocket's subscriptions are released with its session. Hooks are registered at wiring.
func (o *Operations) OnDisconnect(f func(originID string)) {
	o.hookMu.Lock()
	defer o.hookMu.Unlock()
	o.disconnects = append(o.disconnects, f)
}

// Disconnected tells the core that the native connection with this origin id has ended,
// however it ended. It cancels no operation (ADR-0020 §Adapter projections: a disconnect
// abandons a wait and never an operation); it runs the hooks OnDisconnect registered, in
// the caller's goroutine. An empty id is ignored.
func (o *Operations) Disconnected(originID string) {
	if originID == "" {
		return
	}
	o.hookMu.Lock()
	hooks := slices.Clone(o.disconnects)
	o.hookMu.Unlock()
	for _, f := range hooks {
		f(originID)
	}
}

// UseWorkspaces gives the store the registry that admits workspace-scoped calls.
// Without one, no workspace is open.
func (o *Operations) UseWorkspaces(ws Workspaces) { o.ws = ws }

// UsePublisher gives the store the bus its operations' progress is published on.
// Without one, progress is only kept on the operation.
func (o *Operations) UsePublisher(p Publisher) { o.pub = p }

// UseStatePublisher gives the store the bus its operations' state transitions are
// published on (ADR-0043). Without one, none is published.
func (o *Operations) UseStatePublisher(p StatePublisher) {
	o.mu.Lock()
	o.spub = p
	o.mu.Unlock()
	// The same bus carries session.state (ADR-0083 §1).
	o.sessions.usePublisher(p)
}

// UseTrace sets where the trace lines go (flush); the default is the standard
// logger, which the daemon points at its log. A test injects a recorder.
func (o *Operations) UseTrace(logf func(format string, v ...any)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.traceLog = logf
}

// Start validates a call, records it as an operation and runs its handler, returning
// the operation id without waiting. A call rejected here gets a typed error and
// creates no operation (ADR-0020 §Core primitives).
func (o *Operations) Start(ctx context.Context, req Request) (string, error) {
	d, params, err := o.prepare(req)
	if err != nil {
		return "", err
	}
	if !d.Recorded() {
		return "", fmt.Errorf("capability %s is not an operation: call it through Invoke", d.Name)
	}
	return o.start(ctx, d, params, req.Workspace, req.Session, req.Client, req.Origin.stamped(req.Client))
}

// stamped is the origin with Client set to the request's client: the core, not the
// adapter, says who is calling, so the Lendable gate and a wait's reach read one kind
// (ADR-0101).
func (g Origin) stamped(client ClientKind) Origin {
	g.Client = client
	return g
}

// ownerOf is the owner an operation of this client and origin records (ADR-0101): an MCP
// session's chat as the registry holds it now, a peer's configured name.
func (o *Operations) ownerOf(client ClientKind, origin Origin) opOwner {
	session, chat, peer := OwnerStamp(client, origin, o.sessions.Chat)
	return opOwner{session: session, chat: chat, peer: peer}
}

// OwnerStamp is the owner fields ownerOf records, for a job the daemon owns beyond an
// operation (a UI recording, ADR-0113): the starting session's id, an MCP session's chat as
// chatOf (the session registry's) holds it now, and a peer's configured name. The job
// stores them and asks OwnerReaches before it lets a caller stop it.
func OwnerStamp(client ClientKind, origin Origin, chatOf func(session string) string) (session, chat, peer string) {
	session = origin.ID
	switch client {
	case ClientMCP:
		if chatOf != nil {
			chat = chatOf(origin.ID)
		}
	case ClientPeer:
		peer = SessionName(origin.Name)
	}
	return session, chat, peer
}

// reaches reports whether the caller may wait on or cancel op (ADR-0101): the UI and the
// CLI any operation (Activity, and resuming after a reconnect); an MCP session its own
// session's or its chat's; a peer the operations of its own name. Any other kind reaches
// none. It reads only what the record fixed when it was made, so it needs no lock of the
// store's.
func (o *Operations) reaches(op *operation, who Origin) bool {
	return OwnerReaches(op.client, op.owner.session, op.owner.chat, op.owner.peer, who, o.sessions.Chat)
}

// OwnerReaches is ADR-0101's rule over a stored owner (the client kind that started it and
// OwnerStamp's fields): the UI and the CLI reach any; an MCP session what an MCP session
// started, its own session's or its chat's (chatOf is the registry's chat of a session id);
// a peer what a peer of its own configured name started. Any other kind reaches none.
func OwnerReaches(client ClientKind, session, chat, peer string, who Origin, chatOf func(session string) string) bool {
	switch who.Client {
	case ClientUI, ClientCLI, "":
		return true
	case ClientMCP:
		if client != ClientMCP {
			return false
		}
		if who.ID != "" && who.ID == session {
			return true
		}
		if chatOf == nil {
			return false
		}
		c := chatOf(who.ID)
		return c != "" && c == chat
	case ClientPeer:
		name := SessionName(who.Name)
		return client == ClientPeer && name != "" && name == peer
	}
	return false
}

// sessionRef is the SessionRef an origin becomes when its operation is recorded: nil
// without an id; the name bounded; the workspace the bound root's canonical id, only
// while that workspace is open (ADR-0048 §1). Admit does the lookup and records nothing
// here.
func (o *Operations) sessionRef(origin Origin) *SessionRef {
	if origin.ID == "" {
		return nil
	}
	id := origin.ID
	if origin.Joined != "" {
		id = origin.Joined
	}
	ref := &SessionRef{ID: id, Name: SessionName(origin.Name)}
	if origin.Root != "" && o.ws != nil {
		o.ws.Admit(origin.Root, func(id string) { ref.Workspace = id })
	}
	return ref
}

// start records the operation and runs its handler. A workspace-scoped call is
// admitted first: its target must be an open workspace, and the call runs with the
// workspace's canonical id (ADR-0020 §Scope and workspace addressing).
func (o *Operations) start(ctx context.Context, d Descriptor, params any, target string, session Session, client ClientKind, origin Origin) (string, error) {
	id := newID()
	// The handler's context carries the caller's values but not its cancellation:
	// only Cancel ends it.
	hctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	var tgt *OpTarget
	if d.Target != nil {
		tgt = boundTarget(d.Target(params))
	}
	op := &operation{id: id, capability: d.Name, client: client, session: o.sessionRef(origin), target: tgt, owner: o.ownerOf(client, origin), started: o.now(), state: StateRunning, cancel: cancel, done: make(chan struct{})}
	call := &Call{
		OperationID: id,
		Workspace:   target,
		Params:      params,
		Session:     session,
		Origin:      origin,
		Progress: func(done, total int, message string) {
			p := Progress{Done: done, Total: total, Message: message}
			o.mu.Lock()
			op.progress = &p
			workspace := op.workspace
			o.mu.Unlock()
			// Published before it returns, so a report made before the handler
			// returned is on the bus before the operation ends.
			if o.pub != nil {
				o.pub.Publish(ProgressTopic, ProgressType, workspace, id, p)
			}
		},
	}
	record := func(workspace string) {
		op.workspace = workspace
		call.Workspace = workspace
		o.mu.Lock()
		o.ops[id] = op
		o.mu.Unlock()
	}

	if d.Scope == Workspace && !d.RawTarget {
		if o.ws == nil || !o.ws.Admit(target, record) {
			cancel()
			return "", Errorf(ErrWorkspaceNotOpen, "workspace %q is not open", target).
				WithNextStep("call workspace.open on it, then retry")
		}
	} else {
		record("")
		call.Workspace = target
	}

	// The lock is taken, or the place in its queue, before Start returns and in
	// start order, so the order is the callers' and the state is already `queued`
	// when the caller first looks. The start event is queued in the same critical
	// section that decides the place (begin), so no transition of the operation can
	// precede it, and it is published before the handler's goroutine exists.
	var key string
	if d.Resource != nil {
		key = d.Resource(call)
	}
	w := o.begin(op, key)
	o.flush()

	go o.run(hctx, op, d, call, key, w)
	return id, nil
}

// begin decides the operation's place — it takes the lock on key, or queues behind its
// holder, or has no key — and queues its start event (ADR-0043 §2), all under one hold of
// o.mu. It returns the operation's waiter when it is queued.
func (o *Operations) begin(op *operation, key string) *waiter {
	o.mu.Lock()
	defer o.mu.Unlock()
	var w *waiter
	if key != "" {
		w = o.enqueueLocked(op, key)
	}
	op.announced = true
	o.queueStateLocked(op)
	return w
}

// enqueueLocked takes the lock on key for op, or, if another operation holds it, queues
// op behind that one and returns its waiter. Called with o.mu held.
func (o *Operations) enqueueLocked(op *operation, key string) *waiter {
	l := o.locks[key]
	if l == nil {
		l = &lock{}
		o.locks[key] = l
	}
	if l.holder == nil {
		l.holder = op
		return nil
	}
	w := &waiter{op: op, key: key, ch: make(chan struct{}), since: time.Now()}
	l.waiters = append(l.waiters, w)
	op.state, op.waiter = StateQueued, w
	op.blockedBy = &BlockedBy{OperationID: l.holder.id, Capability: l.holder.capability}
	return w
}

// releaseLocked hands the lock on key to the oldest waiter, if any, and tells the rest
// who holds it now. Called with o.mu held.
func (o *Operations) releaseLocked(key string) {
	l := o.locks[key]
	if len(l.waiters) == 0 {
		delete(o.locks, key)
		return
	}
	next := l.waiters[0]
	l.waiters = l.waiters[1:]
	l.holder = next.op
	next.granted = true
	next.op.state, next.op.waiter, next.op.blockedBy = StateRunning, nil, nil
	o.queueStateLocked(next.op)
	for _, w := range l.waiters {
		w.op.blockedBy = &BlockedBy{OperationID: next.op.id, Capability: next.op.capability}
	}
	close(next.ch)
}

// awaitLock blocks while op is queued and reports whether it now holds the lock. It
// returns false when op ended queued: cancelled, or past the queue bound.
func (o *Operations) awaitLock(ctx context.Context, op *operation, d Descriptor, w *waiter) bool {
	var expire <-chan time.Time
	if !d.LongRunning {
		// Timed from the enqueue, not from now: a goroutine the scheduler was slow to
		// start must not stretch the real queue time past the bound.
		t := time.NewTimer(max(o.queueBound-time.Since(w.since), 0))
		defer t.Stop()
		expire = t.C
	}
	timedOut := false
	select {
	case <-w.ch:
		return true
	case <-ctx.Done():
	case <-expire:
		timedOut = true
	}

	defer o.flush() // runs after the unlock below
	o.mu.Lock()
	defer o.mu.Unlock()
	if w.granted {
		// The lock came in the same instant: the operation runs.
		return true
	}
	if op.state.Terminal() {
		return false // Cancel got there first
	}
	if timedOut {
		by := op.blockedBy
		o.dequeueLocked(op)
		op.state = StateFailed
		op.err = Errorf(ErrOperationQueueTimeout, "%s waited %s for %s and never ran", op.capability, o.queueBound, by.Capability).
			WithDetails(map[string]any{
				"blockedBy": map[string]any{"operationId": by.OperationID, "capability": by.Capability},
				"waitedMs":  time.Since(w.since).Milliseconds(),
			}).
			WithNextStep("call operation.wait on " + by.OperationID + ", then retry: the call never ran")
		o.endLocked(op)
		return false
	}
	// Not reached today: every cancel of a queued operation (Cancel, CancelWorkspace,
	// Shutdown) ends it under o.mu, so it is terminal by here. Kept so a future path
	// that cancels the context without ending the operation cannot strand it queued.
	o.endQueuedLocked(op)
	return false
}

// dequeueLocked removes op from its lock's queue. Called with o.mu held.
func (o *Operations) dequeueLocked(op *operation) {
	w := op.waiter
	l := o.locks[w.key]
	for i, x := range l.waiters {
		if x == w {
			l.waiters = append(l.waiters[:i:i], l.waiters[i+1:]...)
			break
		}
	}
	op.waiter, op.blockedBy = nil, nil
}

// endQueuedLocked ends a queued operation cancelled, at once, never having run.
// Called with o.mu held, after cancelRequested (and forced, for a close) is set.
func (o *Operations) endQueuedLocked(op *operation) {
	o.dequeueLocked(op)
	op.state = StateCancelled
	if op.forced {
		op.err = NewError(ErrOperationCancelled, "the operation was cancelled: its workspace was closed")
	} else {
		op.err = NewError(ErrOperationCancelled, "the operation was cancelled")
	}
	o.endLocked(op)
}

// endLocked records that op has reached its terminal state. Called with o.mu held.
func (o *Operations) endLocked(op *operation) {
	op.blockedBy = nil
	op.finished = o.now()
	op.cancel()
	o.terminal = append(o.terminal, op.id)
	o.traces = append(o.traces, traceLine(op))
	o.queueStateLocked(op)
	close(op.done)
	o.prune()
}

// traceLine is the operation's trace: one line, written once, when it reaches a terminal
// state (docs/architecture/overview.md §Cross-cutting concerns). It names the operation
// and who asked, never what was asked or answered: no parameter, result, message, cause or
// detail, and a code only for a failed operation. A call recorded without a client kind
// shows it as "-". Called with o.mu held.
func traceLine(op *operation) string {
	client, workspace := string(op.client), op.workspace
	if client == "" {
		client = "-"
	}
	if workspace == "" {
		workspace = "-"
	}
	line := fmt.Sprintf("droidyard: op %s client=%s capability=%s workspace=%s state=%s ms=%d",
		op.id, client, op.capability, workspace, op.state, op.finished.Sub(op.started).Milliseconds())
	if op.client == ClientPeer && op.owner.peer != "" {
		// The peer's configured name (lowercase letters, digits, hyphens), so a host reads
		// which of its peers did what (ADR-0099 §5).
		line += " peer=" + op.owner.peer
	}
	if op.state == StateFailed && op.err != nil {
		line += " code=" + string(op.err.Code)
	}
	return line
}

// queueStateLocked queues the operation.state event for op's current state: at the start
// (running, or queued with blockedBy), when a queued operation is granted (running), and
// at its terminal state (durationMs, and code when failed). The payload is built from the
// record's own fields and never from a parameter but the declared target (ADR-0076), result, message or cause
// (ADR-0043 §2).
// Called with o.mu held; flush publishes it.
func (o *Operations) queueStateLocked(op *operation) {
	if o.spub == nil {
		return
	}
	p := OperationState{Capability: op.capability, Client: op.client, State: op.state, StartedAt: op.started.UTC(), Session: op.session.copy(), Target: op.target.copy()}
	if op.state.Terminal() {
		ms := op.finished.Sub(op.started).Milliseconds()
		p.DurationMs = &ms
		if op.state == StateFailed && op.err != nil {
			p.Code = op.err.Code
		}
	}
	if op.blockedBy != nil {
		b := *op.blockedBy
		p.BlockedBy = &b
	}
	o.events = append(o.events, stateEvent{workspace: op.workspace, operation: op.id, payload: p})
}

// Active lists the operations not yet terminal whose start event is already queued, in
// no particular order: operation.watch's snapshot (ADR-0043 §1). An operation between its
// record and its start event is not listed, so a snapshot never shows `running` one that
// is about to be queued; its start event reaches the watcher instead.
func (o *Operations) Active() []ActiveOperation {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := []ActiveOperation{}
	for _, op := range o.ops {
		if !op.announced || op.state.Terminal() {
			continue
		}
		a := ActiveOperation{OperationID: op.id, Capability: op.capability, Workspace: op.workspace,
			Client: op.client, State: op.state, StartedAt: op.started.UTC(), Session: op.session.copy(), Target: op.target.copy()}
		if op.blockedBy != nil {
			b := *op.blockedBy
			a.BlockedBy = &b
		}
		out = append(out, a)
	}
	return out
}

// flush writes the trace lines and publishes the operation.state events that the
// transitions queued, after o.mu is released: a log writer or a publisher that is slow, or
// that calls back into the store, must not hold the store (plan m2a, Q3; ADR-0043 §3).
// Every path that can end or start an operation calls it once it has unlocked, so a line
// or an event is written by whichever call gets there first and never twice. flushMu is
// held from taking the queues to the last publish, so a later caller cannot publish
// before an earlier one has: the events leave in the order the transitions happened. A
// publisher may call back only into methods that do not flush (Active, Wait, Running):
// one that calls Start, Cancel, CancelWorkspace or Shutdown waits on flushMu forever.
func (o *Operations) flush() {
	o.flushMu.Lock()
	o.mu.Lock()
	lines, logf := o.traces, o.traceLog
	events, spub := o.events, o.spub
	o.traces, o.events = nil, nil
	o.mu.Unlock()
	if spub != nil {
		for _, e := range events {
			spub.Broadcast(StateTopic, StateType, e.workspace, e.operation, e.payload)
		}
	}
	// The trace lines are written after flushMu is released: they carry no order, and a
	// stalled log writer (a paused terminal on a foreground daemon's stderr) must not hold
	// every later Start behind it.
	o.flushMu.Unlock()
	if len(lines) > 0 {
		if logf == nil {
			logf = log.Printf
		}
		for _, l := range lines {
			logf("%s", l)
		}
	}
}

// run waits for the operation's lock, if it has one, then runs it. The lock is handed
// on inside execute, in the same critical section as the terminal transition.
func (o *Operations) run(ctx context.Context, op *operation, d Descriptor, call *Call, key string, w *waiter) {
	if key != "" && w != nil && !o.awaitLock(ctx, op, d, w) {
		return
	}
	o.execute(ctx, op, d, call, key)
}

func (o *Operations) execute(ctx context.Context, op *operation, d Descriptor, call *Call, key string) {
	var res any
	var err error
	if ctx.Err() != nil {
		// Cancelled at the instant the lock was granted: neither the hook nor the
		// handler runs.
		err = ctx.Err()
	} else {
		// The authorization re-check, immediately before the handler (ADR-0020
		// §Core primitives): a denial fails the operation and the handler never runs.
		if d.Authorize != nil {
			call.Authorized, err = d.Authorize(ctx, call)
		}
		if err == nil {
			res, err = d.Handler(ctx, call)
		}
	}

	defer o.flush() // runs after the unlock below
	o.mu.Lock()
	defer o.mu.Unlock()
	switch {
	case op.forced:
		op.state = StateCancelled
		op.err = NewError(ErrOperationCancelled, "the operation was cancelled: its workspace was closed")
	case err == nil:
		op.state, op.result = StateCompleted, res
		op.slim = d.Slim
		op.untrusted = d.untrusted
		if call.Untrusted != nil {
			op.untrusted = *call.Untrusted
		}
	case op.cancelRequested:
		op.state = StateCancelled
		op.err = NewError(ErrOperationCancelled, "the operation was cancelled")
	default:
		op.state, op.err = StateFailed, AsError(err)
		// The descriptor's derived flag never marks an error: only the handler's own
		// mark does, when it set Call.Untrusted to true before returning its error
		// (R5; plan m1f, D5). The note goes on the message here, once, so every
		// adapter renders it without repeating the rule.
		if call.Untrusted != nil && *call.Untrusted {
			op.untrusted = true
			// A copy: the handler's *Error may be shared, and the note is appended
			// once per failed call, not once per return of the same error value.
			e := *op.err
			e.Message += UntrustedErrorNote
			op.err = &e
		}
	}
	// The key goes to the next caller in the same critical section that makes this
	// operation terminal, so a caller that sees it end never finds the key still held.
	if key != "" {
		o.releaseLocked(key)
	}
	o.endLocked(op)
}

// Wait returns the operation once it is terminal or the deadline passes, whichever
// comes first. A zero deadline waits with none; a deadline in the past is a status
// snapshot. Waiting changes no operation's state, though a snapshot slims a retained
// result (ADR-0020, 2026-10-04). When ctx ends, Wait returns ctx's error and the
// operation runs on.
//
// Wait is for the session that started the operation, which holds its id: it checks no
// owner. A call that came with an id from elsewhere goes through WaitAs.
func (o *Operations) Wait(ctx context.Context, id string, deadline time.Time) (Outcome, error) {
	op, err := o.lookup(id)
	if err != nil {
		return Outcome{}, err
	}
	return o.wait(ctx, op, deadline)
}

// WaitAs is Wait for a caller that named the id: an operation the caller does not reach
// (ADR-0101) is OPERATION_NOT_FOUND, as an unknown one is.
func (o *Operations) WaitAs(ctx context.Context, who Origin, id string, deadline time.Time) (Outcome, error) {
	op, err := o.lookupFor(id, who)
	if err != nil {
		return Outcome{}, err
	}
	return o.wait(ctx, op, deadline)
}

func (o *Operations) wait(ctx context.Context, op *operation, deadline time.Time) (Outcome, error) {
	var expired <-chan time.Time
	if !deadline.IsZero() {
		d := deadline.Sub(o.now())
		if d <= 0 {
			return o.snapshot(op), nil
		}
		t := time.NewTimer(d)
		defer t.Stop()
		expired = t.C
	}
	select {
	case <-op.done:
	case <-expired:
	case <-ctx.Done():
		return Outcome{}, ctx.Err()
	}
	return o.snapshot(op), nil
}

// Cancel asks a running operation to stop by cancelling its handler's context. It
// does not wait. A queued operation is cancelled at once and never runs. Cancelling a
// terminal operation is a no-op. Like Wait, it checks no owner; CancelAs does.
func (o *Operations) Cancel(id string) error {
	op, err := o.lookup(id)
	if err != nil {
		return err
	}
	o.cancelOp(op)
	return nil
}

// CancelAs is Cancel for a caller that named the id: an operation the caller does not
// reach (ADR-0101) is OPERATION_NOT_FOUND, as an unknown one is.
func (o *Operations) CancelAs(who Origin, id string) error {
	op, err := o.lookupFor(id, who)
	if err != nil {
		return err
	}
	o.cancelOp(op)
	return nil
}

func (o *Operations) cancelOp(op *operation) {
	defer o.flush() // runs after the unlock below
	o.mu.Lock()
	defer o.mu.Unlock()
	if op.state.Terminal() {
		return
	}
	op.cancelRequested = true
	op.cancel()
	if op.state == StateQueued {
		o.endQueuedLocked(op)
	}
}

// CancelWorkspace cancels every operation of the workspace id, running or queued,, as a close
// does (ADR-0027): each ends cancelled with OPERATION_CANCELLED when its handler
// returns, whatever it returns, unlike Cancel, under which a handler that returns a
// result still completes. It does not wait, and returns the ids it cancelled.
func (o *Operations) CancelWorkspace(id string) []string {
	defer o.flush() // runs after the unlock below
	o.mu.Lock()
	defer o.mu.Unlock()
	var ids []string
	for _, op := range o.ops {
		if op.workspace == id && !op.state.Terminal() {
			op.forced, op.cancelRequested = true, true
			op.cancel()
			if op.state == StateQueued {
				o.endQueuedLocked(op)
			}
			ids = append(ids, op.id)
		}
	}
	return ids
}

// Running counts the operations not yet in a terminal state.
func (o *Operations) Running() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, op := range o.ops {
		if !op.state.Terminal() {
			n++
		}
	}
	return n
}

// Shutdown cancels every operation, running or queued, as Cancel does, and waits until they
// have all ended or ctx ends (docs/architecture/process-lifecycle.md §Shutdown).
func (o *Operations) Shutdown(ctx context.Context) error {
	o.mu.Lock()
	var running []*operation
	for _, op := range o.ops {
		if !op.state.Terminal() {
			op.cancelRequested = true
			op.cancel()
			if op.state == StateQueued {
				o.endQueuedLocked(op)
			}
			running = append(running, op)
		}
	}
	o.mu.Unlock()
	o.flush()
	for _, op := range running {
		select {
		case <-op.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Invoke is Start followed by Wait with no deadline: a helper for tests and internal
// callers, not a second path (ADR-0020). It is also how operation.wait and
// operation.cancel run: validated like any call, then run in the caller's goroutine
// with no record. For operation.wait the outcome is the waited operation's; for
// operation.cancel it carries no operation id, because the call is not an operation.
func (o *Operations) Invoke(ctx context.Context, req Request) (Outcome, error) {
	d, params, err := o.prepare(req)
	if err != nil {
		return Outcome{}, err
	}
	origin := req.Origin.stamped(req.Client)
	if d.Recorded() {
		id, err := o.start(ctx, d, params, req.Workspace, req.Session, req.Client, origin)
		if err != nil {
			return Outcome{}, err
		}
		return o.Wait(ctx, id, time.Time{})
	}

	// The origin reaches the handler of a call that is not an operation: operation.wait and
	// operation.cancel compare it with the operation's owner (ADR-0101).
	call := &Call{Params: params, Session: req.Session, Origin: origin, Progress: func(int, int, string) {}}
	res, err := d.Handler(ctx, call)
	if err != nil {
		return Outcome{}, err
	}
	if out, ok := res.(Outcome); ok {
		return out, nil
	}
	// A non-Outcome result is marked like a recorded one: the descriptor's derived
	// flag unless the handler set its own. Only operation.cancel reaches here today.
	untrusted := d.untrusted
	if call.Untrusted != nil {
		untrusted = *call.Untrusted
	}
	return Outcome{Capability: d.Name, State: StateCompleted, Result: res, Untrusted: untrusted}, nil
}

// prepare runs every check that rejects a call before it becomes an operation.
func (o *Operations) prepare(req Request) (Descriptor, any, error) {
	d, ok := o.reg.Lookup(req.Capability)
	if !ok {
		return Descriptor{}, nil, Errorf(ErrCapabilityNotFound, "no capability named %q", req.Capability).
			WithNextStep("call system.capabilities to list what exists")
	}
	if req.Client == ClientPeer && !d.Lendable {
		// Before anything is recorded, for every adapter, as the audience check below is for
		// MCP: a peer reaches the capabilities a host lends and no other (ADR-0098 §3).
		return Descriptor{}, nil, Errorf(ErrNotAllowedHere, "%s is not lent to peers", d.Name).
			WithNextStep("a peer can call only the capabilities a host lends; this one runs on the host's own channels")
	}
	if d.Audience == AudienceHuman && req.Client == ClientMCP {
		return Descriptor{}, nil, Errorf(ErrNotAllowedHere, "%s is for humans and cannot be called over MCP", d.Name).
			WithNextStep("ask the user to run it from the UI or the CLI")
	}
	if d.Binding && (req.Client != ClientMCP || !o.sessions.Registered(req.Origin.ID)) {
		// Before anything is recorded, for every adapter: the CLI, the WebSocket and plain
		// HTTP have no MCP session whose binding could move (ADR-0082 §4), and a CLI
		// connection joined to one (ADR-0095 §1) is not it.
		return Descriptor{}, nil, Errorf(ErrNotAllowedHere, "%s moves the calling MCP session's binding, and only an MCP session can call it", d.Name).
			WithNextStep("call it from the agent's MCP session; the UI and the CLI cannot move a session")
	}
	if d.Statefulness != Stateless && noSession(req.Session) {
		// Before anything is recorded, for every adapter: a stateful or streaming
		// capability is rejected rather than silently degraded (ADR-0010).
		return Descriptor{}, nil, Errorf(ErrSessionRequired, "%s is %s: it needs a session that can carry its state, and this connection is not one", d.Name, d.Statefulness).
			WithNextStep("subscribe over the WebSocket session, GET /v1/ws")
	}
	switch {
	case d.Scope == Workspace && req.Workspace == "":
		return Descriptor{}, nil, Errorf(ErrMissingWorkspace, "%s is workspace-scoped and the request names no workspace", d.Name)
	case d.Scope == Global && req.Workspace != "":
		return Descriptor{}, nil, Errorf(ErrUnexpectedWorkspace, "%s is global and takes no workspace", d.Name)
	}
	params, err := decodeParams(d.Params, req.Params)
	if err != nil {
		return Descriptor{}, nil, invalidParamsError(d, err)
	}
	return d, params, nil
}

// invalidParamsError turns a strict-decode failure into REQUEST_INVALID_PARAMS that names
// the field and the shape the schema expects (plan m1n step 8, main-session notes 3 and
// 4): a caller that sends a string where an object belongs, or an unknown field such as a
// workspace passed as a parameter, reads what to send instead of Go's decoder text alone.
// Everything else — a missing required field, a value that fails its pattern, trailing
// data — keeps the code and the decoder's own words as the cause.
func invalidParamsError(d Descriptor, err error) *Error {
	if field := unknownField(err); field != "" {
		return Errorf(ErrInvalidParams, "%s has no parameter %q", d.Name, field).
			WithCause(err.Error()).
			WithNextStep(unknownParamHint(d, field))
	}
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) {
		name := jsonFieldName(d.Params, ute.Field)
		shape := jsonShape(ute.Type)
		return Errorf(ErrInvalidParams, "parameter %q of %s must be %s, not %s", name, d.Name, shape, jsonTypeWord(ute.Value)).
			WithCause(err.Error()).
			WithNextStep("send " + strconv.Quote(name) + " as " + shape)
	}
	return NewError(ErrInvalidParams, "the parameters do not match the capability's input schema").
		WithCause(err.Error())
}

// unknownField is the field a json "unknown field" decode error names, "" when err is not
// that kind of error.
func unknownField(err error) string {
	const prefix = "json: unknown field "
	s := err.Error()
	if !strings.HasPrefix(s, prefix) {
		return ""
	}
	name, uerr := strconv.Unquote(strings.TrimSpace(strings.TrimPrefix(s, prefix)))
	if uerr != nil {
		return ""
	}
	return name
}

// unknownParamHint tells a caller what to send instead of the unknown field: the
// parameters the capability takes, or — for one that takes none — where the value
// belongs. workspace.open is the case that prompted it: its workspace is named by the
// request's workspace field, not by a parameter (plan m1n step 8, main-session note 4).
func unknownParamHint(d Descriptor, field string) string {
	if names := paramNames(d.Params); len(names) > 0 {
		return "remove " + strconv.Quote(field) + ", or pass one of " + d.Name + "'s parameters: " + strings.Join(names, ", ")
	}
	hint := "remove " + strconv.Quote(field) + ": " + d.Name + " takes no parameters"
	if d.Scope == Workspace {
		hint += ", and the workspace is named by the request's workspace field (the CLI's --workspace), not by a parameter"
	}
	return hint + "."
}

// jsonTypeWord names a JSON value's type as Go's json decoder reports it, in prose.
func jsonTypeWord(v string) string {
	switch v {
	case "string":
		return "a string"
	case "number":
		return "a number"
	case "bool":
		return "a boolean"
	case "object":
		return "an object"
	case "array":
		return "an array"
	}
	return v
}

// jsonShape names the JSON shape a parameter must have, read from the Go type the schema
// is generated from, so REQUEST_INVALID_PARAMS can say what to send (plan m1n step 8,
// note 3). A struct is an object listing its fields, an optional one (json `omitempty`)
// suffixed "?".
func jsonShape(t reflect.Type) string {
	if t == nil {
		return "the schema's type"
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		var names []string
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue // unexported, so not in the schema
			}
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			if strings.Contains(opts, "omitempty") {
				name += "?"
			}
			names = append(names, name)
		}
		if len(names) == 0 {
			return "an empty object"
		}
		return "an object {" + strings.Join(names, ", ") + "}"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Map:
		return "an object"
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Interface:
		return "a value"
	}
	return "the schema's type"
}

// jsonFieldName maps the Go field path a json.UnmarshalTypeError reports to the JSON name
// the caller wrote, for a top-level field whose tag differs from its Go name.
func jsonFieldName(proto any, path string) string {
	seg := path
	if i := strings.IndexByte(seg, '.'); i >= 0 {
		seg = seg[:i]
	}
	t := reflect.TypeOf(proto)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return seg
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Name != seg {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			name = f.Name
		}
		return name
	}
	return seg
}

// paramNames lists the JSON names of proto's fields, in declaration order: the parameters
// the capability takes, for a hint that names them.
func paramNames(proto any) []string {
	t := reflect.TypeOf(proto)
	if t == nil {
		return nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var names []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		names = append(names, name)
	}
	return names
}

// noSession reports whether s carries no session: nil, or an interface holding a nil
// pointer, which a naive `== nil` would take for one.
func noSession(s Session) bool {
	if s == nil {
		return true
	}
	v := reflect.ValueOf(s)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return v.IsNil()
	}
	return false
}

// decodeParams decodes raw into a fresh value of proto's type, strictly: an unknown
// field, trailing data or a missing required field (tag `jsonschema:"required"`, as
// the published schema says) is an error.
func decodeParams(proto any, raw json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	t := reflect.TypeOf(proto)
	v := reflect.New(t)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v.Interface()); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after the parameter object")
	}

	if t.Kind() == reflect.Struct {
		var present map[string]json.RawMessage
		if err := json.Unmarshal(raw, &present); err != nil {
			return nil, err
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !hasTagOption(f.Tag.Get("jsonschema"), "required") {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" {
				name = f.Name
			}
			if _, ok := present[name]; !ok {
				return nil, fmt.Errorf("missing required parameter %q", name)
			}
		}
	}
	return v.Elem().Interface(), nil
}

func hasTagOption(tag, option string) bool {
	for _, o := range strings.Split(tag, ",") {
		if o == option {
			return true
		}
	}
	return false
}

func (o *Operations) lookup(id string) (*operation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.prune()
	op, ok := o.ops[id]
	if !ok {
		return nil, notFound(id)
	}
	return op, nil
}

// lookupFor is lookup for a caller that named the id. An operation it does not reach is
// answered with the very error an unknown id gets, text included, so the answer tells it
// nothing about another session's operations (ADR-0101).
func (o *Operations) lookupFor(id string, who Origin) (*operation, error) {
	op, err := o.lookup(id)
	if err != nil {
		return nil, err
	}
	if !o.reaches(op, who) {
		return nil, notFound(id)
	}
	return op, nil
}

func notFound(id string) *Error {
	return Errorf(ErrOperationNotFound, "no operation %q: it never existed or is past retention", id)
}

// prune drops terminal operations older than retainFor, keeping at least the last
// retainMin. Called with o.mu held.
func (o *Operations) prune() {
	now := o.now()
	for len(o.terminal) > retainMin {
		op := o.ops[o.terminal[0]]
		if now.Sub(op.finished) < retainFor {
			return
		}
		delete(o.ops, op.id)
		o.terminal = o.terminal[1:]
	}
}

func (o *Operations) snapshot(op *operation) Outcome {
	o.mu.Lock()
	defer o.mu.Unlock()
	result := op.result
	if op.state == StateCompleted && op.slim != nil {
		// The first delivery carries the full result; what the operation keeps from
		// here on is the slim one. Done under o.mu, so two waiters never both get it.
		op.result, op.slim = op.slim(op.result), nil
	}
	out := Outcome{
		OperationID: op.id,
		Capability:  op.capability,
		State:       op.state,
		Result:      result,
		Error:       op.err,
		Untrusted:   op.untrusted,
	}
	if op.blockedBy != nil {
		b := *op.blockedBy
		out.BlockedBy = &b
	}
	if op.progress != nil {
		p := *op.progress
		out.Progress = &p
	}
	return out
}

// newID returns a random id, so a restarted daemon never reuses one a client still
// holds.
func newID() string {
	var b [8]byte
	rand.Read(b[:]) // never returns an error (crypto/rand, Go 1.24)
	return "op-" + hex.EncodeToString(b[:])
}
