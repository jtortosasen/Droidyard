package capability

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is the store's clock under test control, so retention is tested without
// sleeping for 15 minutes.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type blockParams struct {
	Label string `json:"label,omitempty"`
}

// fixture is a store with test-only capabilities, never shipped:
//
//	fixture.block   sends its OperationID on started, then blocks until release or
//	                cancellation; honours ctx
//	fixture.noop    returns result{Count: 7} at once
//	fixture.fail    returns a typed error
//	fixture.scoped  workspace-scoped
//	fixture.human   human audience
type fixture struct {
	ops     *Operations
	clock   *fakeClock
	started chan string
	release chan struct{}
	ran     atomic.Int32 // handler runs of every fixture but fixture.block
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		clock:   &fakeClock{t: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)},
		started: make(chan string, 1),
		release: make(chan struct{}),
	}
	reg := NewRegistry()
	counted := func(res any, err error) Handler {
		return func(ctx context.Context, call *Call) (any, error) {
			f.ran.Add(1)
			return res, err
		}
	}
	reg.MustRegister(Descriptor{
		Name: "fixture.block", Kind: Command, Scope: Global, Description: "Blocks until released.",
		Params: blockParams{}, Result: result{},
		Handler: func(ctx context.Context, call *Call) (any, error) {
			f.started <- call.OperationID
			select {
			case <-f.release:
				return result{Count: 1}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	reg.MustRegister(Descriptor{
		Name: "fixture.noop", Kind: Query, Scope: Global, Description: "Returns at once.",
		Params: params{}, Result: result{}, Handler: counted(result{Count: 7}, nil),
	})
	reg.MustRegister(Descriptor{
		Name: "fixture.fail", Kind: Command, Scope: Global, Description: "Fails.",
		Params: params{}, Result: result{}, Handler: counted(nil, Errorf("FIXTURE_BROKEN", "it broke")),
	})
	reg.MustRegister(Descriptor{
		Name: "fixture.scoped", Kind: Query, Scope: Workspace, Description: "Reads a workspace.",
		Params: params{}, Result: result{}, Handler: counted(result{}, nil),
	})
	reg.MustRegister(Descriptor{
		Name: "fixture.human", Kind: Command, Scope: Global, Audience: AudienceHuman, Description: "Changes policy.",
		Params: params{}, Result: result{}, Handler: counted(result{}, nil),
	})
	f.ops = NewOperations(reg)
	f.ops.now = f.clock.now
	f.ops.RegisterCapabilities()
	return f
}

func (f *fixture) size() int {
	f.ops.mu.Lock()
	defer f.ops.mu.Unlock()
	return len(f.ops.ops)
}

// startBlocking starts fixture.block and returns its id once the handler runs. The
// context given to Start is cancelled as soon as Start returns: an operation's
// lifetime belongs to the store, not to the caller that started it.
func (f *fixture) startBlocking(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	id, err := f.ops.Start(ctx, Request{Capability: "fixture.block"})
	cancel()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := <-f.started; got != id {
		t.Fatalf("the handler saw OperationID %q; Start returned %q", got, id)
	}
	return id
}

func codeOf(err error) ErrorCode {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// TestStartRejectsWithATypedErrorAndNoOperation: Start rejects synchronously, with a
// typed error and no operation (ADR-0020 §Core primitives), and the handler never runs.
func TestStartRejectsWithATypedErrorAndNoOperation(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		want ErrorCode
	}{
		{"unknown capability", Request{Capability: "fixture.nope"}, ErrCapabilityNotFound},
		{"unknown parameter field", Request{Capability: "fixture.noop", Params: json.RawMessage(`{"Name":"x","bogus":1}`)}, ErrInvalidParams},
		{"parameters that are not an object", Request{Capability: "fixture.noop", Params: json.RawMessage(`[1]`)}, ErrInvalidParams},
		{"trailing data after the parameters", Request{Capability: "fixture.noop", Params: json.RawMessage(`{} {}`)}, ErrInvalidParams},
		{"missing required parameter", Request{Capability: "operation.wait", Params: json.RawMessage(`{"timeoutMs":0}`)}, ErrInvalidParams},
		{"superfluous workspace", Request{Capability: "fixture.noop", Workspace: "/home/me/app"}, ErrUnexpectedWorkspace},
		{"missing workspace", Request{Capability: "fixture.scoped"}, ErrMissingWorkspace},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			id, err := f.ops.Start(context.Background(), c.req)
			if got := codeOf(err); got != c.want {
				t.Fatalf("Start returned (%q, %v); want the typed error %s", id, err, c.want)
			}
			if id != "" {
				t.Errorf("a rejected call returned an operation id %q", id)
			}
			if n := f.size(); n != 0 {
				t.Errorf("a rejected call left %d operation(s) in the store; want none", n)
			}
			if n := f.ran.Load(); n != 0 {
				t.Errorf("a rejected call ran its handler %d time(s)", n)
			}
		})
	}
}

// TestHumanAudienceIsRefusedToMCP: the core rejects a human-audience capability from
// an MCP session by its client kind, as enforcement, not omission (ADR-0015 §6), and
// accepts it from the CLI.
func TestHumanAudienceIsRefusedToMCP(t *testing.T) {
	f := newFixture(t)
	id, err := f.ops.Start(context.Background(), Request{Capability: "fixture.human", Client: ClientMCP})
	if codeOf(err) != ErrNotAllowedHere || id != "" {
		t.Fatalf("an MCP call to a human-audience capability got (%q, %v); want %s and no operation", id, err, ErrNotAllowedHere)
	}
	if f.size() != 0 || f.ran.Load() != 0 {
		t.Fatalf("the refused call left %d operation(s) and ran its handler %d time(s)", f.size(), f.ran.Load())
	}

	out, err := f.ops.Invoke(context.Background(), Request{Capability: "fixture.human", Client: ClientCLI})
	if err != nil || out.State != StateCompleted {
		t.Fatalf("a CLI call to a human-audience capability got (%+v, %v); want it completed", out, err)
	}
}

// TestHandlerSeesItsOperationID: Start sets Call.OperationID to the id it returns
// before the handler runs, so a domain can use it as its own record id (ADR-0020
// §Operation capabilities: the build id is the operation id). startBlocking asserts it.
func TestHandlerSeesItsOperationID(t *testing.T) {
	f := newFixture(t)
	id := f.startBlocking(t)
	if id == "" {
		t.Fatal("Start returned an empty id")
	}
	close(f.release)
}

// TestWaitPastDeadlineIsASnapshot: a deadline in the past is a status snapshot, and
// waiting has no side effects (ADR-0020 §Core primitives). Neither a snapshot, nor an
// abandoned wait, nor the end of the context that started it touches the operation,
// which runs on to completion.
func TestWaitPastDeadlineIsASnapshot(t *testing.T) {
	f := newFixture(t)
	id := f.startBlocking(t)

	out, err := f.ops.Wait(context.Background(), id, f.clock.now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if out.State != StateRunning || out.Result != nil || out.Error != nil || out.OperationID != id {
		t.Fatalf("a snapshot of a running operation returned %+v; want running, no result, no error", out)
	}

	// An abandoned wait: the caller's context ends while it waits.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	if _, err := f.ops.Wait(ctx, id, time.Time{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("an abandoned wait returned %v; want context.Canceled", err)
	}

	if again, _ := f.ops.Wait(context.Background(), id, f.clock.now()); again.State != StateRunning {
		t.Fatalf("after a snapshot and an abandoned wait the operation is %s; want still running", again.State)
	}
	close(f.release)
	done, err := f.ops.Wait(context.Background(), id, time.Time{})
	if err != nil || done.State != StateCompleted {
		t.Fatalf("the operation ended (%+v, %v); want completed: waiting must not have touched it", done, err)
	}
	if r, ok := done.Result.(result); !ok || r.Count != 1 || done.Error != nil {
		t.Fatalf("a completed operation carried result %#v and error %v; want the handler's result and no error", done.Result, done.Error)
	}
}

// TestCancelEndsCancelledWithNoResult: Cancel cancels the handler's context, and the
// operation ends cancelled carrying OPERATION_CANCELLED and no result (ADR-0020
// §States). A second cancel, on a terminal operation, is a no-op.
func TestCancelEndsCancelledWithNoResult(t *testing.T) {
	f := newFixture(t)
	id := f.startBlocking(t)

	if err := f.ops.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	out, err := f.ops.Wait(context.Background(), id, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != StateCancelled {
		t.Fatalf("a cancelled operation ended %s; want cancelled", out.State)
	}
	if out.Error == nil || out.Error.Code != ErrOperationCancelled {
		t.Errorf("a cancelled operation carried error %v; want %s", out.Error, ErrOperationCancelled)
	}
	if out.Result != nil {
		t.Errorf("a cancelled operation carried a result %#v; want none", out.Result)
	}
	if err := f.ops.Cancel(id); err != nil {
		t.Errorf("cancelling a terminal operation returned %v; want a no-op", err)
	}
	if again, _ := f.ops.Wait(context.Background(), id, time.Time{}); again.State != StateCancelled {
		t.Errorf("a second cancel changed the operation to %s", again.State)
	}
}

// TestHandlerErrorEndsFailed: an operation that could not be performed ends failed
// with the handler's typed error and no result (ADR-0020 §States).
func TestHandlerErrorEndsFailed(t *testing.T) {
	f := newFixture(t)
	out, err := f.ops.Invoke(context.Background(), Request{Capability: "fixture.fail"})
	if err != nil {
		t.Fatalf("Invoke returned %v; a handler error belongs in the outcome, not to Invoke", err)
	}
	if out.State != StateFailed || out.Error == nil || out.Error.Code != "FIXTURE_BROKEN" || out.Result != nil {
		t.Fatalf("a failing handler ended %+v (error %v); want failed, FIXTURE_BROKEN, no result", out, out.Error)
	}
}

// TestRetention: terminal operations stay waitable for 15 minutes and at least the
// last 200 (ADR-0020 §Retention); past both, Wait and Cancel get OPERATION_NOT_FOUND.
func TestRetention(t *testing.T) {
	f := newFixture(t)
	invoke := func() string {
		t.Helper()
		out, err := f.ops.Invoke(context.Background(), Request{Capability: "fixture.noop"})
		if err != nil || out.State != StateCompleted {
			t.Fatalf("Invoke: (%+v, %v)", out, err)
		}
		return out.OperationID
	}
	waitable := func(id string) bool {
		t.Helper()
		_, err := f.ops.Wait(context.Background(), id, time.Time{})
		if err != nil && codeOf(err) != ErrOperationNotFound {
			t.Fatalf("Wait(%s): %v", id, err)
		}
		return err == nil
	}

	first := invoke()
	f.clock.advance(14*time.Minute + 59*time.Second)
	if !waitable(first) {
		t.Fatal("a terminal operation was dropped before 15 minutes")
	}

	// Past 15 minutes, but among the last 200: kept.
	f.clock.advance(time.Hour)
	if !waitable(first) {
		t.Fatal("a terminal operation among the last 200 was dropped")
	}

	// 200 newer ones push it out of the last 200; it is past 15 minutes, so it goes.
	var newer []string
	for range retainMin {
		newer = append(newer, invoke())
	}
	if waitable(first) {
		t.Fatal("a terminal operation older than 15 minutes and outside the last 200 is still waitable")
	}
	if err := f.ops.Cancel(first); codeOf(err) != ErrOperationNotFound {
		t.Errorf("Cancel on an expired operation returned %v; want %s", err, ErrOperationNotFound)
	}

	// The last 200 stay however old they are.
	f.clock.advance(24 * time.Hour)
	for _, id := range newer {
		if !waitable(id) {
			t.Fatalf("operation %s, one of the last 200, was dropped", id)
		}
	}

	// The 15-minute window, with the floor out of the way: 250 operations finish at
	// once, so 50 of them are outside the last 200 and only their age decides.
	var outside []string
	for range 50 {
		outside = append(outside, invoke())
	}
	var last []string
	for range retainMin {
		last = append(last, invoke())
	}
	if waitable(newer[0]) {
		t.Fatal("an operation outside the last 200 and older than 15 minutes was kept")
	}
	f.clock.advance(15*time.Minute - time.Second)
	for _, id := range outside {
		if !waitable(id) {
			t.Fatalf("operation %s, 14m59s old and outside the last 200, was dropped", id)
		}
	}
	f.clock.advance(2 * time.Second)
	for _, id := range outside {
		if waitable(id) {
			t.Fatalf("operation %s, 15m01s old and outside the last 200, is still waitable", id)
		}
	}
	for _, id := range last {
		if !waitable(id) {
			t.Fatalf("operation %s, one of the last 200, was dropped", id)
		}
	}
}

// TestOperationCallsCreateNoRecord: operation.wait and operation.cancel are not
// operations (ADR-0020 §Core primitives, "operation.* calls themselves are not
// recorded"), so calling them leaves the store's size unchanged, and Start refuses them.
func TestOperationCallsCreateNoRecord(t *testing.T) {
	f := newFixture(t)
	id := f.startBlocking(t)
	before := f.size()

	wait := Request{Capability: "operation.wait", Params: json.RawMessage(`{"operationId":"` + id + `","timeoutMs":0}`)}
	for range 20 {
		out, err := f.ops.Invoke(context.Background(), wait)
		if err != nil || out.OperationID != id || out.State != StateRunning {
			t.Fatalf("operation.wait returned (%+v, %v); want the waited operation, running", out, err)
		}
	}
	if got := f.size(); got != before {
		t.Fatalf("20 calls to operation.wait changed the store from %d to %d operations", before, got)
	}

	if _, err := f.ops.Start(context.Background(), wait); err == nil {
		t.Error("Start accepted operation.wait; it must not record it")
	}
	if got := f.size(); got != before {
		t.Fatalf("Start on operation.wait changed the store from %d to %d operations", before, got)
	}

	cancel := Request{Capability: "operation.cancel", Params: json.RawMessage(`{"operationId":"` + id + `"}`)}
	out, err := f.ops.Invoke(context.Background(), cancel)
	if err != nil || out.OperationID != "" || out.State != StateCompleted {
		t.Fatalf("operation.cancel returned (%+v, %v); want completed, with no operation of its own", out, err)
	}
	if got := f.size(); got != before {
		t.Fatalf("operation.cancel changed the store from %d to %d operations", before, got)
	}
}

// TestOperationWaitReturnsTheWaitedOperation: operation.wait answers with the waited
// operation, its state and its result or error (plan Readings, operation.wait (a));
// an unknown id is OPERATION_NOT_FOUND for both operation capabilities.
func TestOperationWaitReturnsTheWaitedOperation(t *testing.T) {
	f := newFixture(t)
	id := f.startBlocking(t)
	close(f.release)

	out, err := f.ops.Invoke(context.Background(), Request{Capability: "operation.wait", Params: json.RawMessage(`{"operationId":"` + id + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if out.OperationID != id || out.Capability != "fixture.block" || out.State != StateCompleted {
		t.Fatalf("operation.wait returned %+v; want fixture.block's operation %s, completed", out, id)
	}
	if r, ok := out.Result.(result); !ok || r.Count != 1 {
		t.Fatalf("operation.wait carried result %#v; want the waited operation's", out.Result)
	}

	for _, name := range []string{"operation.wait", "operation.cancel"} {
		_, err := f.ops.Invoke(context.Background(), Request{Capability: name, Params: json.RawMessage(`{"operationId":"op-nope"}`)})
		if codeOf(err) != ErrOperationNotFound {
			t.Errorf("%s on an unknown id returned %v; want %s", name, err, ErrOperationNotFound)
		}
	}
}

// TestOperationViewResultIsAnOpenObject: the view's result is written as the raw
// object it holds and omitted when empty, so a view with no result carries no key.
func TestOperationViewResultIsAnOpenObject(t *testing.T) {
	v := OperationView{OperationID: "op-1", Capability: "x.y", State: StateRunning}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != `{"operationId":"op-1","capability":"x.y","state":"running"}` {
		t.Errorf("a running view marshalled as %s; want no result key", got)
	}

	v.State, v.Result = StateCompleted, Object(`{"count":1}`)
	raw, _ = json.Marshal(v)
	var back OperationView
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if string(back.Result) != `{"count":1}` {
		t.Errorf("the result round-tripped as %s", back.Result)
	}
}
