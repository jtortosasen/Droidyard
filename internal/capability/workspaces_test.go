package capability

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// The store's side of the per-root lifetime (ADR-0137 §3; plan m3j step 5): a call of
// scope workspace is admitted by Workspaces.Enter at Start, and the registry hears of each
// operation's end, outside the store's lock.

// enterWS is a Workspaces that names every target "ws-<target>" and refuses "refused".
type enterWS struct {
	ops     *Operations
	entered atomic.Int32
	ended   chan string
}

func (w *enterWS) Admit(string, func(string)) bool { return false }

func (w *enterWS) Enter(target string, record func(id string)) error {
	w.entered.Add(1)
	if target == "refused" {
		return NewError(ErrInvalidParams, "the project cannot be used").WithNextStep("pass an absolute path")
	}
	record("ws-" + target)
	return nil
}

// Ended calls back into the store before it reports, as the registry takes its own locks
// while the store's must be free: a store that called it with its mutex held would
// deadlock here.
func (w *enterWS) Ended(id string) {
	w.ops.Running()
	w.ended <- id
}

type enterFixture struct {
	ops     *Operations
	ws      *enterWS
	saw     atomic.Value // string: Call.Workspace of the last scoped.ok
	started chan string
}

func newEnterFixture(t *testing.T) *enterFixture {
	t.Helper()
	f := &enterFixture{started: make(chan string, 16)}
	reg := NewRegistry()
	type none struct{}
	scoped := func(name string, h Handler, resource func(*Call) string) {
		reg.MustRegister(Descriptor{Name: name, Kind: Command, Scope: Workspace, Description: "A scoped fixture.",
			Params: none{}, Result: none{}, Handler: h, Resource: resource, LongRunning: resource != nil})
	}
	scoped("scoped.ok", func(ctx context.Context, call *Call) (any, error) {
		f.saw.Store(call.Workspace)
		return none{}, nil
	}, nil)
	scoped("scoped.fail", func(ctx context.Context, call *Call) (any, error) {
		return nil, NewError("FIXTURE_FAILED", "failed")
	}, nil)
	scoped("scoped.block", func(ctx context.Context, call *Call) (any, error) {
		f.started <- call.Workspace
		<-ctx.Done()
		return nil, ctx.Err()
	}, nil)
	scoped("scoped.keyed", func(ctx context.Context, call *Call) (any, error) {
		f.started <- call.Workspace
		<-ctx.Done()
		return nil, ctx.Err()
	}, func(*Call) string { return "k" })
	reg.MustRegister(Descriptor{Name: "global.ok", Kind: Command, Scope: Global, Description: "A global fixture.",
		Params: none{}, Result: none{}, Handler: func(context.Context, *Call) (any, error) { return none{}, nil }})
	f.ops = NewOperations(reg)
	f.ops.RegisterCapabilities()
	f.ws = &enterWS{ops: f.ops, ended: make(chan string, 16)}
	f.ops.UseWorkspaces(f.ws)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		f.ops.Shutdown(ctx)
	})
	return f
}

func recvWithin[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: nothing within 5 s", what)
	}
	var zero T
	return zero
}

// TestStartEntersAScopedCallAndRecordsNothingWhenRefused: a call of scope workspace is
// admitted by Enter, which names the id the handler runs with; one Enter refuses fails at
// Start with Enter's own error, recorded as no operation and run by no handler; a global
// call never asks Enter.
func TestStartEntersAScopedCallAndRecordsNothingWhenRefused(t *testing.T) {
	f := newEnterFixture(t)

	out, err := f.ops.Invoke(context.Background(), Request{Capability: "scoped.ok", Workspace: "a"})
	if err != nil || out.State != StateCompleted || f.saw.Load() != "ws-a" {
		t.Fatalf("a scoped call: %v %+v, the handler saw workspace %v; want completed on ws-a", err, out, f.saw.Load())
	}
	recvWithin(t, f.ws.ended, "the end of the scoped call")

	before := f.ws.entered.Load()
	id, err := f.ops.Start(context.Background(), Request{Capability: "scoped.ok", Workspace: "refused"})
	var e *Error
	if !errors.As(err, &e) || e.Code != ErrInvalidParams || e.NextStep == "" || id != "" {
		t.Fatalf("a refused call: %q, %v; want Enter's REQUEST_INVALID_PARAMS with its nextStep", id, err)
	}
	if n := f.ops.Running() + len(f.ops.Active()); n != 0 {
		t.Errorf("a refused call left %d operations", n)
	}
	if _, err := f.ops.Invoke(context.Background(), Request{Capability: "global.ok"}); err != nil {
		t.Fatal(err)
	}
	if got := f.ws.entered.Load() - before; got != 1 {
		t.Errorf("Enter was asked %d times by a refused scoped call and a global one; want once", got)
	}
}

// TestEndedIsToldOncePerOperationOutsideTheStoreLock: the registry hears of an operation's
// end once, with its workspace, however it ended (completed, failed, cancelled while
// running, cancelled while queued), and never for a global operation. Ended calls back
// into the store, so the report is made with the store's lock released. Seen failing
// with Ended called inside endLocked: the test deadlocked on the store's mutex.
func TestEndedIsToldOncePerOperationOutsideTheStoreLock(t *testing.T) {
	f := newEnterFixture(t)
	ctx := context.Background()
	expect := func(what, want string) {
		t.Helper()
		if got := recvWithin(t, f.ws.ended, what); got != want {
			t.Fatalf("%s: Ended(%q); want %q", what, got, want)
		}
	}

	if out, _ := f.ops.Invoke(ctx, Request{Capability: "scoped.ok", Workspace: "c"}); out.State != StateCompleted {
		t.Fatalf("completed: %+v", out)
	}
	expect("a completed operation", "ws-c")
	if out, _ := f.ops.Invoke(ctx, Request{Capability: "scoped.fail", Workspace: "f"}); out.State != StateFailed {
		t.Fatalf("failed: %+v", out)
	}
	expect("a failed operation", "ws-f")
	if _, err := f.ops.Invoke(ctx, Request{Capability: "global.ok"}); err != nil {
		t.Fatal(err)
	}

	running, err := f.ops.Start(ctx, Request{Capability: "scoped.block", Workspace: "r"})
	if err != nil {
		t.Fatal(err)
	}
	recvWithin(t, f.started, "the blocking operation")
	f.ops.Cancel(running)
	expect("a cancelled running operation", "ws-r")

	holder, _ := f.ops.Start(ctx, Request{Capability: "scoped.keyed", Workspace: "h"})
	recvWithin(t, f.started, "the key's holder")
	queued, err := f.ops.Start(ctx, Request{Capability: "scoped.keyed", Workspace: "q"})
	if err != nil {
		t.Fatal(err)
	}
	f.ops.Cancel(queued)
	expect("a cancelled queued operation", "ws-q")
	f.ops.Cancel(holder)
	expect("the key's holder", "ws-h")

	select {
	case id := <-f.ws.ended:
		t.Errorf("Ended(%q) again, or for a global operation; want exactly one report per scoped operation", id)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestActiveInCountsWhatIsNotTerminal: ActiveIn counts a workspace's running and queued
// operations, not another workspace's and not terminal ones: what the idle release asks
// before it lets go of a root. Seen failing with queued operations left out.
func TestActiveInCountsWhatIsNotTerminal(t *testing.T) {
	f := newEnterFixture(t)
	ctx := context.Background()
	if n := f.ops.ActiveIn("ws-x"); n != 0 {
		t.Fatalf("ActiveIn of an untouched workspace = %d", n)
	}
	holder, _ := f.ops.Start(ctx, Request{Capability: "scoped.keyed", Workspace: "x"})
	recvWithin(t, f.started, "the key's holder")
	queued, _ := f.ops.Start(ctx, Request{Capability: "scoped.keyed", Workspace: "x"})
	other, _ := f.ops.Start(ctx, Request{Capability: "scoped.block", Workspace: "y"})
	recvWithin(t, f.started, "the other workspace's operation")

	if x, y := f.ops.ActiveIn("ws-x"), f.ops.ActiveIn("ws-y"); x != 2 || y != 1 {
		t.Fatalf("ActiveIn(ws-x) = %d, ActiveIn(ws-y) = %d; want 2 (one running, one queued) and 1", x, y)
	}
	f.ops.Cancel(holder)
	f.ops.Cancel(queued)
	f.ops.Cancel(other)
	for _, id := range []string{holder, queued, other} {
		if _, err := f.ops.Wait(ctx, id, time.Now().Add(5*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if x, y := f.ops.ActiveIn("ws-x"), f.ops.ActiveIn("ws-y"); x != 0 || y != 0 {
		t.Errorf("ActiveIn after every operation ended = %d, %d; want 0, 0", x, y)
	}
}
