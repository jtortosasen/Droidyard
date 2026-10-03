package capability

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type testSession struct{ id string }

func (s testSession) ID() string { return s.id }

// TestStatefulCapabilitiesNeedASession holds the decision of ADR-0010 as amended
// (plan m1d, D6): a Stateful or Streaming capability called without a session that can
// carry it is rejected in prepare with SESSION_REQUIRED, before any operation is
// recorded and before the handler could run, whichever way it is called (Start,
// Invoke) and whichever client; a Stateless one is unaffected, and the same call with
// a session reaches its handler with that session.
func TestStatefulCapabilitiesNeedASession(t *testing.T) {
	var ran atomic.Int32
	var mu sync.Mutex
	var seen Session
	reg := NewRegistry()
	for name, st := range map[string]Statefulness{"fixture.stateless": Stateless, "fixture.stateful": Stateful, "fixture.streaming": Streaming} {
		reg.MustRegister(Descriptor{
			Name: name, Kind: Command, Scope: Global, Statefulness: st, Description: "Records its session.",
			Params: struct{}{}, Result: struct{}{},
			Handler: func(_ context.Context, call *Call) (any, error) {
				ran.Add(1)
				mu.Lock()
				seen = call.Session
				mu.Unlock()
				return struct{}{}, nil
			},
		})
	}
	ops := NewOperations(reg)
	ops.RegisterCapabilities()
	lastSeen := func() Session {
		mu.Lock()
		defer mu.Unlock()
		return seen
	}
	records := func() int {
		ops.mu.Lock()
		defer ops.mu.Unlock()
		return len(ops.ops)
	}

	for _, name := range []string{"fixture.stateful", "fixture.streaming"} {
		for _, client := range []ClientKind{ClientCLI, ClientMCP, ClientUI} {
			req := Request{Capability: name, Client: client}
			id, err := ops.Start(context.Background(), req)
			var e *Error
			if !errors.As(err, &e) || e.Code != ErrSessionRequired || id != "" {
				t.Fatalf("Start %s as %s without a session: (%q, %v); want SESSION_REQUIRED and no id", name, client, id, err)
			}
			if !strings.Contains(e.NextStep, "GET /v1/ws") {
				t.Errorf("SESSION_REQUIRED's nextStep is %q; want it to name GET /v1/ws", e.NextStep)
			}
			if _, err := ops.Invoke(context.Background(), req); !errors.As(err, &e) || e.Code != ErrSessionRequired {
				t.Fatalf("Invoke %s as %s without a session: %v; want SESSION_REQUIRED", name, client, err)
			}
		}
	}
	if n := records(); n != 0 || ran.Load() != 0 {
		t.Fatalf("after rejected calls the store holds %d operations and %d handlers ran; want none: the check runs before anything is recorded", n, ran.Load())
	}

	// Stateless: no session needed, none passed on.
	if out, err := ops.Invoke(context.Background(), Request{Capability: "fixture.stateless", Client: ClientCLI}); err != nil || out.State != StateCompleted {
		t.Fatalf("a stateless call without a session: (%+v, %v); want completed", out, err)
	}
	if s := lastSeen(); s != nil {
		t.Errorf("a call with no session reached its handler with %v", s)
	}

	// With a session: runs, and the handler is handed that session.
	want := testSession{id: "ses-1"}
	for _, name := range []string{"fixture.stateful", "fixture.streaming"} {
		out, err := ops.Invoke(context.Background(), Request{Capability: name, Client: ClientUI, Session: want})
		if err != nil || out.State != StateCompleted {
			t.Fatalf("%s with a session: (%+v, %v); want completed", name, out, err)
		}
		if got := lastSeen(); got != Session(want) {
			t.Errorf("%s's handler saw session %v; want %v", name, got, want)
		}
	}
	// Waiting on an operation made with a session does not need one: the session
	// belongs to the call that started it, not to a later operation.wait, which is
	// Stateless and goes through prepare like any call.
	id, err := ops.Start(context.Background(), Request{Capability: "fixture.stateful", Session: want})
	if err != nil {
		t.Fatal(err)
	}
	out, err := ops.Invoke(context.Background(), Request{Capability: "operation.wait", Params: json.RawMessage(`{"operationId":"` + id + `"}`)})
	if err != nil || out.State != StateCompleted {
		t.Fatalf("operation.wait on an operation started with a session, called without one: (%+v, %v); want completed", out, err)
	}
}

// nilSession is a Session whose pointer is nil: an interface holding it is not == nil.
type nilSession struct{}

func (*nilSession) ID() string { return "" }

// TestTypedNilSessionIsNoSession: a Session interface holding a nil pointer carries
// nothing, so a stateful capability rejects it as it rejects nil, before anything is
// recorded (ADR-0010 as amended; plan m1d, D6). Without the guard it would pass
// `== nil` and reach the handler.
func TestTypedNilSessionIsNoSession(t *testing.T) {
	var ran atomic.Int32
	reg := NewRegistry()
	reg.MustRegister(Descriptor{
		Name: "fixture.stateful", Kind: Command, Scope: Global, Statefulness: Stateful, Description: "Counts runs.",
		Params: struct{}{}, Result: struct{}{},
		Handler: func(context.Context, *Call) (any, error) { ran.Add(1); return struct{}{}, nil },
	})
	ops := NewOperations(reg)
	var typedNil *nilSession
	req := Request{Capability: "fixture.stateful", Client: ClientUI, Session: typedNil}
	id, err := ops.Start(context.Background(), req)
	var e *Error
	if !errors.As(err, &e) || e.Code != ErrSessionRequired || id != "" {
		t.Fatalf("Start with a typed-nil session: (%q, %v); want SESSION_REQUIRED and no id", id, err)
	}
	if _, err := ops.Invoke(context.Background(), req); !errors.As(err, &e) || e.Code != ErrSessionRequired {
		t.Fatalf("Invoke with a typed-nil session: %v; want SESSION_REQUIRED", err)
	}
	ops.mu.Lock()
	n := len(ops.ops)
	ops.mu.Unlock()
	if n != 0 || ran.Load() != 0 {
		t.Fatalf("a typed-nil session left %d operations and ran %d handlers; want none", n, ran.Load())
	}
}
