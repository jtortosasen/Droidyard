package capability

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Operations.Record (plan m2y step 3; ADR-0117 §6): a gesture of the live view recorded after
// it happened, as the operation of the capability that replays it.

type recParams struct {
	Device string `json:"device"`
}

// recStore has a capability whose handler, authorization and resource must never run for a
// record: the view's authorization was the one its device.watchScreen passed.
func recStore(t *testing.T) *Operations {
	t.Helper()
	reg := NewRegistry()
	reg.MustRegister(Descriptor{
		Name: "rec.swipe", Kind: Command, Scope: Global, Description: "A recorded gesture.",
		Params: recParams{}, Result: traceResult{}, Errors: []ErrorCode{ErrOperationQueueTimeout},
		Target:    func(p any) *OpTarget { return &OpTarget{Device: p.(recParams).Device} },
		Resource:  func(*Call) string { panic("a record takes no resource") },
		Authorize: func(context.Context, *Call) (any, error) { panic("a record runs no authorization") },
		Handler:   func(context.Context, *Call) (any, error) { panic("a record runs no handler") },
	})
	reg.MustRegister(Descriptor{
		Name: "rec.scoped", Kind: Command, Scope: Workspace, Description: "Needs a workspace.",
		Params: recParams{}, Result: traceResult{},
		Handler: func(context.Context, *Call) (any, error) { return traceResult{}, nil },
	})
	ops := NewOperations(reg)
	ops.RegisterCapabilities()
	return ops
}

func (r *stateRecorder) awaitStates(t *testing.T, id string, n int) []State {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if s := r.states(id); len(s) >= n {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation %s has %v published after 5 s; want %d events", id, r.states(id), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// Decision (ADR-0117 §6): a record is the operation a call would have been, already
// terminal. It is findable by operation.wait with its state and result, never listed as
// active, shows its declared target and its real duration, and publishes the two events
// every operation does, running and then its terminal state, in that order; and none of the
// hooks of a call (authorization, resource, handler) runs for it. A cancelled record carries
// OPERATION_CANCELLED, a failed one its own code. Seen failing with the start event not
// queued: only the terminal event was published.
func TestRecordInsertsATerminalOperation(t *testing.T) {
	ops := recStore(t)
	rec := &stateRecorder{ops: ops}
	ops.UseStatePublisher(rec)
	lines := &traceRecorder{ops: ops}
	ops.UseTrace(lines.logf)
	at := time.Now().Add(-time.Minute)
	origin := Origin{ID: "ws-1", Name: "browser", Client: ClientUI}

	for _, c := range []struct {
		state State
		err   *Error
		code  ErrorCode
	}{
		{StateCompleted, nil, ""},
		{StateCancelled, nil, ErrOperationCancelled},
		{StateFailed, NewError("REC_BROKEN", "it broke"), "REC_BROKEN"},
	} {
		id, err := ops.Record(Recorded{
			Capability: "rec.swipe", Origin: origin, Params: recParams{Device: "emulator-5554"},
			Started: at, Finished: at.Add(1500 * time.Millisecond), State: c.state, Result: traceResult{Secret: "r"}, Err: c.err,
		})
		if err != nil {
			t.Fatalf("Record %s: %v", c.state, err)
		}
		out, err := ops.Wait(context.Background(), id, time.Now())
		if err != nil || out.State != c.state || out.Capability != "rec.swipe" {
			t.Fatalf("Wait: %+v, %v; want %s", out, err, c.state)
		}
		if c.state == StateCompleted && out.Result != (traceResult{Secret: "r"}) {
			t.Errorf("the completed record's result is %#v", out.Result)
		}
		if c.code == "" && out.Error != nil || c.code != "" && (out.Error == nil || out.Error.Code != c.code) {
			t.Errorf("%s: error %+v, want code %q", c.state, out.Error, c.code)
		}
		for _, a := range ops.Active() {
			if a.OperationID == id {
				t.Errorf("the %s record is listed as active", c.state)
			}
		}
		states := rec.awaitStates(t, id, 2)
		if len(states) != 2 || states[0] != StateRunning || states[1] != c.state {
			t.Errorf("%s: published %v, want running then the terminal state", c.state, states)
		}
		var last OperationState
		for _, e := range rec.all() {
			if e.operation == id {
				last = e.payload.(OperationState)
			}
		}
		if last.Target == nil || last.Target.Device != "emulator-5554" || last.Client != ClientUI ||
			last.Session == nil || last.Session.ID != "ws-1" || last.DurationMs == nil || *last.DurationMs != 1500 || last.Code != c.code && c.state == StateFailed {
			t.Errorf("%s: terminal event %+v, want the target, client, session and 1500 ms", c.state, last)
		}
	}
	if got := ops.Running(); got != 0 {
		t.Errorf("%d operations running after three records", got)
	}
}

// Decision (ADR-0117 §6): a record that cannot be one of a call is refused and leaves
// nothing: an unknown capability, a workspace-scoped one, a state that is not terminal, a
// failed record with no error.
func TestRecordRefusesWhatIsNotARecord(t *testing.T) {
	ops := recStore(t)
	for name, r := range map[string]Recorded{
		"unknown":        {Capability: "rec.nothing", State: StateCompleted},
		"workspace":      {Capability: "rec.scoped", State: StateCompleted},
		"running":        {Capability: "rec.swipe", State: StateRunning},
		"queued":         {Capability: "rec.swipe", State: StateQueued},
		"no state":       {Capability: "rec.swipe"},
		"failed, no err": {Capability: "rec.swipe", State: StateFailed},
	} {
		if id, err := ops.Record(r); err == nil || id != "" {
			t.Errorf("%s: Record = %q, %v; want an error", name, id, err)
		}
	}
	if n := len(ops.terminal); n != 0 {
		t.Errorf("%d operations after refused records", n)
	}
}

// Decision (ADR-0099 §5, ADR-0101): a record made for a paired peer's call names the peer as
// any operation of that peer does: the event says client peer, the trace line carries
// peer=<name> after the duration, and only a peer of that name reaches the record with
// operation.wait, as for the peer's own calls. Seen failing with the owner not recorded: the
// line lacked peer= and any peer reached it.
func TestRecordedOperationFromAPeerNamesIt(t *testing.T) {
	ops := recStore(t)
	rec := &stateRecorder{ops: ops}
	ops.UseStatePublisher(rec)
	lines := &traceRecorder{ops: ops}
	ops.UseTrace(lines.logf)
	id, err := ops.Record(Recorded{Capability: "rec.swipe", Origin: Origin{ID: "link-1", Name: "anna", Client: ClientPeer},
		Params: recParams{Device: "R58M001"}, State: StateCompleted})
	if err != nil {
		t.Fatal(err)
	}
	lines.await(t, 1)
	if line := lines.snapshot()[0]; !strings.HasPrefix(line, "droidyard: op "+id+" client=peer capability=rec.swipe workspace=- state=completed ms=") ||
		!strings.HasSuffix(line, " peer=anna") {
		t.Errorf("the trace line is %q; want client=peer and a closing peer=anna", line)
	}
	if states := rec.awaitStates(t, id, 2); states[1] != StateCompleted {
		t.Errorf("published %v", states)
	}
	if e := rec.all()[0].payload.(OperationState); e.Client != ClientPeer {
		t.Errorf("the start event says client %q", e.Client)
	}
	if _, err := ops.WaitAs(context.Background(), Origin{Name: "anna", Client: ClientPeer}, id, time.Now()); err != nil {
		t.Errorf("anna cannot reach her own record: %v", err)
	}
	if _, err := ops.WaitAs(context.Background(), Origin{Name: "bob", Client: ClientPeer}, id, time.Now()); err == nil {
		t.Error("bob reached anna's record")
	}
}

// Decision (ADR-0117 §6, "an input never waits on its record"): Record queues and returns,
// and the publishing and the trace line are a flush on a goroutine of its own, so a trace
// writer and a state publisher that are stalled delay the flush and never a Record: a
// thousand records go in within a second while both are blocked, and once they are released
// every one has been published (running and terminal) and traced.
// Seen failing with Record flushing before it returns: the first record blocked on the
// publisher.
func TestRecordNeverWaitsOnTheFlush(t *testing.T) {
	ops := recStore(t)
	gate := make(chan struct{})
	var nPub, nTrace atomic.Int64
	pub := &stateRecorder{ops: ops, gate: func(published) { <-gate; nPub.Add(1) }}
	ops.UseStatePublisher(pub)
	ops.UseTrace(func(string, ...any) { <-gate; nTrace.Add(1) })
	const n = 1000
	began := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < n; i++ {
			if _, err := ops.Record(Recorded{Capability: "rec.swipe", Origin: Origin{Client: ClientUI}, Params: recParams{Device: "d"}, State: StateCompleted}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		close(gate)
		t.Fatal("Record waited on a stalled publisher or trace writer")
	}
	if took := time.Since(began); took > time.Second {
		t.Errorf("%d records took %v with the flush stalled; want under a second", n, took)
	}
	if nPub.Load() != 0 || nTrace.Load() != 0 {
		t.Errorf("something was published (%d) or traced (%d) while both were blocked", nPub.Load(), nTrace.Load())
	}
	close(gate)
	deadline := time.Now().Add(20 * time.Second)
	for nPub.Load() < 2*n || nTrace.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("after release: %d events and %d lines, want %d and %d", nPub.Load(), nTrace.Load(), 2*n, n)
		}
		time.Sleep(time.Millisecond)
	}
}
