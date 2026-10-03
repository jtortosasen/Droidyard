package capability

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"time"
)

// ClientKind is what a session's hello says it is (ADR-0019 §7), or ui for an HTTP
// request (ADR-0015 §6). The core reads it for one rule: human-audience capabilities
// are refused to MCP (ADR-0015 §6).
type ClientKind string

const (
	ClientCLI ClientKind = "cli"
	ClientMCP ClientKind = "mcp"
	ClientUI  ClientKind = "ui"
)

// Retention of terminal operations (ADR-0020 §Retention): 15 minutes, and at least
// the last 200.
const (
	retainFor = 15 * time.Minute
	retainMin = 200
)

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
}

type operation struct {
	id         string
	capability string
	// workspace is the canonical id of the workspace the operation runs in; empty for
	// a global call and for workspace.open and workspace.close.
	workspace string

	// Guarded by Operations.mu.
	state           State
	result          any
	err             *Error
	progress        *Progress
	cancelRequested bool
	// forced: cancelled by a workspace close, so the operation ends cancelled
	// whatever its handler returns (ADR-0027).
	forced   bool
	finished time.Time

	cancel context.CancelFunc
	done   chan struct{}
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

// Publisher puts an event on the daemon's bus (core-runtime.md §Events). The event
// layer implements it; capability does not import it, so whatever publishes (the
// workspace registry, the store) is given one. Publish never blocks on a subscriber.
type Publisher interface {
	// Publish stamps one event with the bus's epoch, the next seq and the time, and
	// delivers it before it returns. workspace is the workspace id, "" for a global
	// event; operation the operation id, or "".
	Publish(topic, typ, workspace, operation string, payload any)
}

// Progress is published on operation.progress, type progress, with the operation id,
// the operation's workspace and a Progress payload (core-runtime.md §Invocation:
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

	mu       sync.Mutex
	ops      map[string]*operation
	terminal []string // ids of terminal operations, oldest first
	now      func() time.Time
}

// NewOperations returns an empty store that dispatches from reg.
func NewOperations(reg *Registry) *Operations {
	return &Operations{reg: reg, ops: make(map[string]*operation), now: time.Now}
}

// UseWorkspaces gives the store the registry that admits workspace-scoped calls.
// Without one, no workspace is open.
func (o *Operations) UseWorkspaces(ws Workspaces) { o.ws = ws }

// UsePublisher gives the store the bus its operations' progress is published on.
// Without one, progress is only kept on the operation.
func (o *Operations) UsePublisher(p Publisher) { o.pub = p }

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
	return o.start(ctx, d, params, req.Workspace)
}

// start records the operation and runs its handler. A workspace-scoped call is
// admitted first: its target must be an open workspace, and the call runs with the
// workspace's canonical id (ADR-0020 §Scope and workspace addressing).
func (o *Operations) start(ctx context.Context, d Descriptor, params any, target string) (string, error) {
	id := newID()
	// The handler's context carries the caller's values but not its cancellation:
	// only Cancel ends it.
	hctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	op := &operation{id: id, capability: d.Name, state: StateRunning, cancel: cancel, done: make(chan struct{})}
	call := &Call{
		OperationID: id,
		Workspace:   target,
		Params:      params,
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

	go o.run(hctx, op, d.Handler, call)
	return id, nil
}

func (o *Operations) run(ctx context.Context, op *operation, h Handler, call *Call) {
	res, err := h(ctx, call)

	o.mu.Lock()
	defer o.mu.Unlock()
	switch {
	case op.forced:
		op.state = StateCancelled
		op.err = NewError(ErrOperationCancelled, "the operation was cancelled: its workspace was closed")
	case err == nil:
		op.state, op.result = StateCompleted, res
	case op.cancelRequested:
		op.state = StateCancelled
		op.err = NewError(ErrOperationCancelled, "the operation was cancelled")
	default:
		op.state, op.err = StateFailed, AsError(err)
	}
	op.finished = o.now()
	op.cancel()
	o.terminal = append(o.terminal, op.id)
	close(op.done)
	o.prune()
}

// Wait returns the operation once it is terminal or the deadline passes, whichever
// comes first. A zero deadline waits with none; a deadline in the past is a status
// snapshot. Waiting has no side effects: when ctx ends, Wait returns ctx's error and
// the operation runs on.
func (o *Operations) Wait(ctx context.Context, id string, deadline time.Time) (Outcome, error) {
	op, err := o.lookup(id)
	if err != nil {
		return Outcome{}, err
	}

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
// does not wait. Cancelling a terminal operation is a no-op.
func (o *Operations) Cancel(id string) error {
	op, err := o.lookup(id)
	if err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if op.state.Terminal() {
		return nil
	}
	op.cancelRequested = true
	op.cancel()
	return nil
}

// CancelWorkspace cancels every running operation of the workspace id, as a close
// does (ADR-0027): each ends cancelled with OPERATION_CANCELLED when its handler
// returns, whatever it returns, unlike Cancel, under which a handler that returns a
// result still completes. It does not wait, and returns the ids it cancelled.
func (o *Operations) CancelWorkspace(id string) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var ids []string
	for _, op := range o.ops {
		if op.workspace == id && !op.state.Terminal() {
			op.forced, op.cancelRequested = true, true
			op.cancel()
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

// Shutdown cancels every running operation, as Cancel does, and waits until they
// have all ended or ctx ends (docs/architecture/process-lifecycle.md §Shutdown).
func (o *Operations) Shutdown(ctx context.Context) error {
	o.mu.Lock()
	var running []*operation
	for _, op := range o.ops {
		if !op.state.Terminal() {
			op.cancelRequested = true
			op.cancel()
			running = append(running, op)
		}
	}
	o.mu.Unlock()
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
	if d.Recorded() {
		id, err := o.start(ctx, d, params, req.Workspace)
		if err != nil {
			return Outcome{}, err
		}
		return o.Wait(ctx, id, time.Time{})
	}

	res, err := d.Handler(ctx, &Call{Params: params, Progress: func(int, int, string) {}})
	if err != nil {
		return Outcome{}, err
	}
	if out, ok := res.(Outcome); ok {
		return out, nil
	}
	return Outcome{Capability: d.Name, State: StateCompleted, Result: res}, nil
}

// prepare runs every check that rejects a call before it becomes an operation.
func (o *Operations) prepare(req Request) (Descriptor, any, error) {
	d, ok := o.reg.Lookup(req.Capability)
	if !ok {
		return Descriptor{}, nil, Errorf(ErrCapabilityNotFound, "no capability named %q", req.Capability).
			WithNextStep("call system.capabilities to list what exists")
	}
	if d.Audience == AudienceHuman && req.Client == ClientMCP {
		return Descriptor{}, nil, Errorf(ErrNotAllowedHere, "%s is for humans and cannot be called over MCP", d.Name).
			WithNextStep("ask the user to run it from the UI or the CLI")
	}
	switch {
	case d.Scope == Workspace && req.Workspace == "":
		return Descriptor{}, nil, Errorf(ErrMissingWorkspace, "%s is workspace-scoped and the request names no workspace", d.Name)
	case d.Scope == Global && req.Workspace != "":
		return Descriptor{}, nil, Errorf(ErrUnexpectedWorkspace, "%s is global and takes no workspace", d.Name)
	}
	params, err := decodeParams(d.Params, req.Params)
	if err != nil {
		return Descriptor{}, nil, NewError(ErrInvalidParams, "the parameters do not match the capability's input schema").
			WithCause(err.Error())
	}
	return d, params, nil
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
		return nil, Errorf(ErrOperationNotFound, "no operation %q: it never existed or is past retention", id)
	}
	return op, nil
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
	out := Outcome{
		OperationID: op.id,
		Capability:  op.capability,
		State:       op.state,
		Result:      op.result,
		Error:       op.err,
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
