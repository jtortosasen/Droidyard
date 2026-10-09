package capability

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The operation.state events (ADR-0043): one event per transition of a recorded
// operation, published after the store's lock is released and in transition order.

// stateRecorder is the injected StatePublisher. It calls back into the store before it
// does anything else, so an event published while the store's mutex is held deadlocks the
// test (the scenario runs under a watchdog). gate, when set, runs before an event is
// recorded and may block: the recorded order is the order the publisher finished in.
type stateRecorder struct {
	ops  *Operations
	gate func(e published)

	mu  sync.Mutex
	got []published
}

func (r *stateRecorder) Broadcast(topic, typ, workspace, operation string, payload any) {
	r.ops.Active()
	e := published{topic, typ, workspace, operation, payload}
	if r.gate != nil {
		r.gate(e)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, e)
}

func (r *stateRecorder) all() []published {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}

// states is the states the operation id was published in, in order.
func (r *stateRecorder) states(id string) []State {
	var out []State
	for _, e := range r.all() {
		if e.operation == id {
			out = append(out, e.payload.(OperationState).State)
		}
	}
	return out
}

// index is the position of the event for (id, state) in the recorded order, -1 if absent.
func (r *stateRecorder) index(id string, s State) int {
	for i, e := range r.all() {
		if e.operation == id && e.payload.(OperationState).State == s {
			return i
		}
	}
	return -1
}

// await waits until the operation's terminal event has been published: Wait returns once
// the operation is terminal, a moment before its event leaves the store.
func (r *stateRecorder) await(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if s := r.states(id); len(s) > 0 && s[len(s)-1].Terminal() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation %s has no terminal event after 5 s; published %v", id, r.states(id))
		}
		time.Sleep(time.Millisecond)
	}
}

func stateStore(t *testing.T) (*Operations, *stateRecorder, chan struct{}) {
	t.Helper()
	ops, _, started := traceStore(t)
	rec := &stateRecorder{ops: ops}
	ops.UseStatePublisher(rec)
	return ops, rec, started
}

// watchdog fails the test if f has not returned within d: the way a publisher called under
// the store's lock shows, since the publisher calls back into the store.
func watchdog(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("no progress in %s: an event was published while the store's lock was held", d)
	}
}

// TestOperationStateEvents is the decision of ADR-0043 §2 and §4: whatever path ends an
// operation, it is published once when recorded and once at its terminal state, a queued
// one with a `running` event between when the lock is granted, in transition order;
// a failed one carries its code; nothing the caller sent or the handler returned reaches
// a payload; operation.wait and operation.cancel are not operations and publish nothing.
// The recorder calls back into the store, so each event leaves with the store's lock
// released.
func TestOperationStateEvents(t *testing.T) {
	ops, rec, started := stateStore(t)
	ctx := context.Background()

	watchdog(t, 20*time.Second, func() {
		start := func(client ClientKind, capability, workspace string) string {
			t.Helper()
			params := json.RawMessage(`{"secret":"` + sentinelParam + `"}`)
			id, err := ops.Start(ctx, Request{Capability: capability, Workspace: workspace, Params: params, Client: client})
			if err != nil {
				t.Fatalf("Start %s: %v", capability, err)
			}
			return id
		}
		wait := func(id string, state State) {
			t.Helper()
			out, err := ops.Wait(ctx, id, time.Time{})
			if err != nil || out.State != state {
				t.Fatalf("operation %s: %+v, %v; want %s", id, out, err, state)
			}
			rec.await(t, id)
		}
		want := func(id string, states ...State) {
			t.Helper()
			if got := rec.states(id); !slices.Equal(got, states) {
				t.Errorf("operation %s published %v; want %v", id, got, states)
			}
		}
		var all []string

		for _, client := range []ClientKind{ClientMCP, ""} {
			// completed, global and in a workspace
			id := start(client, "trace.ok", "")
			wait(id, StateCompleted)
			want(id, StateRunning, StateCompleted)
			all = append(all, id)
			scoped := start(client, "trace.scoped", "/some/root")
			wait(scoped, StateCompleted)
			want(scoped, StateRunning, StateCompleted)
			all = append(all, scoped)
			if i := rec.index(scoped, StateRunning); rec.all()[i].workspace != "ws-canon" {
				t.Errorf("a workspace operation's event names workspace %q; want ws-canon", rec.all()[i].workspace)
			}
			if i := rec.index(id, StateRunning); rec.all()[i].workspace != "" {
				t.Errorf("a global operation's event names workspace %q; want none", rec.all()[i].workspace)
			}

			// failed: its code
			id = start(client, "trace.fail", "")
			wait(id, StateFailed)
			want(id, StateRunning, StateFailed)
			all = append(all, id)

			// cancelled while running
			id = start(client, "trace.block", "")
			<-started
			if err := ops.Cancel(id); err != nil {
				t.Fatal(err)
			}
			wait(id, StateCancelled)
			want(id, StateRunning, StateCancelled)
			all = append(all, id)

			// cancelled while queued: queued with blockedBy, then cancelled, never running
			holder := start(client, "trace.held", "")
			queued := start(client, "trace.held", "")
			if err := ops.Cancel(queued); err != nil {
				t.Fatal(err)
			}
			wait(queued, StateCancelled)
			want(queued, StateQueued, StateCancelled)
			all = append(all, holder, queued)

			// a queue timeout: queued, then failed with its code
			late := start(client, "trace.held", "")
			wait(late, StateFailed)
			want(late, StateQueued, StateFailed)
			all = append(all, late)

			// granted: a queued operation runs when its holder ends, and the granted event
			// precedes the holder's terminal one (the grant happens in the holder's own
			// critical section)
			next := start(client, "trace.held", "")
			if err := ops.Cancel(holder); err != nil {
				t.Fatal(err)
			}
			wait(holder, StateCancelled)
			want(holder, StateRunning, StateCancelled)
			want(next, StateQueued, StateRunning)
			if g, e := rec.index(next, StateRunning), rec.index(holder, StateCancelled); g < 0 || e < 0 || g > e {
				t.Errorf("the granted event of %s is at %d and the holder's end at %d; the grant is published first", next, g, e)
			}
			if err := ops.Cancel(next); err != nil {
				t.Fatal(err)
			}
			wait(next, StateCancelled)
			want(next, StateQueued, StateRunning, StateCancelled)
			all = append(all, next)

			// cancelled by a workspace close
			id = start(client, "trace.scopedblock", "/some/root")
			<-started
			if got := ops.CancelWorkspace("ws-canon"); len(got) != 1 || got[0] != id {
				t.Fatalf("CancelWorkspace cancelled %v; want [%s]", got, id)
			}
			wait(id, StateCancelled)
			want(id, StateRunning, StateCancelled)
			all = append(all, id)

			// cancelled by a shutdown, running and queued
			id = start(client, "trace.block", "")
			<-started
			h := start(client, "trace.held", "")
			q := start(client, "trace.held", "")
			if err := ops.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			wait(id, StateCancelled)
			wait(h, StateCancelled)
			wait(q, StateCancelled)
			want(id, StateRunning, StateCancelled)
			want(q, StateQueued, StateCancelled)
			all = append(all, id, h, q)

			// operation.wait and operation.cancel are not operations: no event of their own
			args := json.RawMessage(`{"operationId":"` + id + `"}`)
			for _, name := range []string{"operation.wait", "operation.cancel"} {
				if _, err := ops.Invoke(ctx, Request{Capability: name, Params: args, Client: client}); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
			}
		}
		time.Sleep(50 * time.Millisecond) // a second terminal event for the same operation would be late

		// Nothing else: every published event belongs to an operation the test made.
		seen := map[string]bool{}
		for _, id := range all {
			seen[id] = true
		}
		allowedKeys := []string{"capability", "client", "state", "startedAt", "durationMs", "code", "blockedBy"}
		for _, e := range rec.all() {
			p := e.payload.(OperationState)
			if e.topic != StateTopic || e.typ != StateType {
				t.Errorf("event %+v is not operation.state/state", e)
			}
			if !seen[e.operation] {
				t.Errorf("an event for an operation the test did not make, or for operation.wait/cancel: %+v", e)
			}
			raw, _ := json.Marshal(p)
			for _, s := range []string{sentinelParam, sentinelResult, sentinelMessage, sentinelCause, sentinelDetail} {
				if strings.Contains(string(raw), s) {
					t.Errorf("the payload carries request or device data %q: %s", s, raw)
				}
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			for k := range fields {
				if !slices.Contains(allowedKeys, k) {
					t.Errorf("the payload has a field %q outside ADR-0043 §2: %s", k, raw)
				}
			}
			switch p.State {
			case StateQueued:
				if p.BlockedBy == nil || p.BlockedBy.OperationID == "" || p.DurationMs != nil || p.Code != "" {
					t.Errorf("a queued event is {blockedBy} only: %s", raw)
				}
			case StateRunning:
				if p.BlockedBy != nil || p.DurationMs != nil || p.Code != "" {
					t.Errorf("a running event carries no blockedBy, durationMs or code: %s", raw)
				}
			default:
				if p.DurationMs == nil || p.BlockedBy != nil {
					t.Errorf("a terminal event carries durationMs and no blockedBy: %s", raw)
				}
				wantCode := ""
				if p.State == StateFailed {
					wantCode = "TRACE_BROKEN"
					if p.Capability == "trace.held" {
						wantCode = string(ErrOperationQueueTimeout)
					}
				}
				if string(p.Code) != wantCode {
					t.Errorf("a %s event of %s has code %q; want %q", p.State, p.Capability, p.Code, wantCode)
				}
			}
			if _, has := fields["client"]; has != (p.Client != "") {
				t.Errorf("the client field is present=%v for client %q; omitted only when none was recorded", has, p.Client)
			}
		}
		clients := map[ClientKind]bool{}
		for _, e := range rec.all() {
			clients[e.payload.(OperationState).Client] = true
		}
		if !clients[ClientMCP] || !clients[""] || len(clients) != 2 {
			t.Errorf("the events carry clients %v; want mcp and none", clients)
		}
	})
}

// TestStateEventsKeepTransitionOrder is the decision of ADR-0043 §3, "published after the
// store's lock is released, in transition order": the flush is one step at a time. A
// queued operation's start event is held inside the publisher while its holder ends and
// the lock is granted; the grant and the holder's end must wait behind the start event,
// not overtake it.
func TestStateEventsKeepTransitionOrder(t *testing.T) {
	ops, rec, _ := stateStore(t)
	ctx := context.Background()

	holder, err := ops.Start(ctx, Request{Capability: "trace.held", Client: ClientCLI})
	if err != nil {
		t.Fatal(err)
	}
	rec.await2(t, holder, StateRunning)

	gateReached, release := make(chan struct{}), make(chan struct{})
	var reached, released sync.Once
	releaseGate := func() { released.Do(func() { close(release) }) }
	t.Cleanup(releaseGate) // a failure path must not leave the publisher held
	rec.gate = func(e published) {
		if p := e.payload.(OperationState); e.operation != holder && p.State == StateQueued {
			reached.Do(func() { close(gateReached) })
			<-release
		}
	}
	var queued string
	var startErr error
	startReturned := make(chan struct{})
	go func() {
		defer close(startReturned)
		queued, startErr = ops.Start(ctx, Request{Capability: "trace.held", Client: ClientCLI})
	}()
	select {
	case <-gateReached:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued operation's start event never reached the publisher")
	}
	// The holder ends, and the grant to the queued operation happens, while the queued
	// operation's start event is still inside the publisher.
	// Cancel flushes too, so it waits behind the held publisher: it runs on its own.
	cancelled := make(chan error, 1)
	go func() { cancelled <- ops.Cancel(holder) }()
	if _, err := ops.Wait(ctx, holder, time.Time{}); err != nil {
		t.Fatal(err)
	}
	// Whatever else is published meanwhile must be nothing: the holder's end and the
	// grant are queued behind the start event. A run without the serialisation shows them
	// here.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) && len(rec.states(holder)) < 2 {
		time.Sleep(time.Millisecond)
	}
	releaseGate()
	<-startReturned
	if err := <-cancelled; err != nil {
		t.Fatal(err)
	}
	if startErr != nil {
		t.Fatal(startErr)
	}
	rec.await(t, holder)
	rec.await2(t, queued, StateRunning)

	q, g, e := rec.index(queued, StateQueued), rec.index(queued, StateRunning), rec.index(holder, StateCancelled)
	if q < 0 || g < 0 || e < 0 || !(q < g && g < e) {
		t.Errorf("published order: queued start at %d, grant at %d, holder end at %d; want the transition order", q, g, e)
	}
	if err := ops.Cancel(queued); err != nil {
		t.Fatal(err)
	}
	rec.await(t, queued)
}

// await2 waits until the operation's event in state s has been published.
func (r *stateRecorder) await2(t *testing.T, id string, s State) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.index(id, s) < 0 {
		if time.Now().After(deadline) {
			t.Fatalf("operation %s has no %s event after 5 s; published %v", id, s, r.states(id))
		}
		time.Sleep(time.Millisecond)
	}
}

// TestStateEventsCarryTimes: startedAt is the record time on the store's clock and
// durationMs the time from the record to the terminal state on it, so a slow operation
// reads as slow.
func TestStateEventsCarryTimes(t *testing.T) {
	f := newFixture(t)
	rec := &stateRecorder{ops: f.ops}
	f.ops.UseStatePublisher(rec)
	id := f.startBlocking(t)
	f.clock.advance(1500 * time.Millisecond)
	if err := f.ops.Cancel(id); err != nil {
		t.Fatal(err)
	}
	rec.await(t, id)
	events := rec.all()
	if len(events) != 2 {
		t.Fatalf("published %d events; want 2", len(events))
	}
	start, end := events[0].payload.(OperationState), events[1].payload.(OperationState)
	if !start.StartedAt.Equal(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)) || !end.StartedAt.Equal(start.StartedAt) {
		t.Errorf("startedAt %v and %v; want the record time 12:00:00 on both", start.StartedAt, end.StartedAt)
	}
	if end.DurationMs == nil || *end.DurationMs != 1500 {
		t.Errorf("durationMs %v; want 1500", end.DurationMs)
	}
}

// TestActiveListsOnlyAnnouncedOperations is the decision of ADR-0043 §1 on the snapshot's
// window: an operation between its record and its start event is not listed, because
// its place (running or queued) is not decided yet, so a snapshot never shows `running`
// one about to be queued. The Resource hook runs in that window. A terminal operation is
// never listed, and a queued one is listed as queued with blockedBy.
func TestActiveListsOnlyAnnouncedOperations(t *testing.T) {
	reg := NewRegistry()
	var ops *Operations
	var inWindow []string
	var mu sync.Mutex
	reg.MustRegister(Descriptor{
		Name: "active.held", Kind: Command, Scope: Global, Description: "Holds one key until cancelled.",
		Params: traceParams{}, Result: traceResult{}, Errors: []ErrorCode{ErrOperationQueueTimeout},
		Resource: func(call *Call) string {
			// Between the record and the start event.
			for _, a := range ops.Active() {
				if a.OperationID == call.OperationID {
					mu.Lock()
					inWindow = append(inWindow, a.OperationID+" "+string(a.State))
					mu.Unlock()
				}
			}
			return "k"
		},
		Handler: func(ctx context.Context, call *Call) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	ops = NewOperations(reg)
	ops.UseStatePublisher(&stateRecorder{ops: ops})
	ctx := context.Background()

	holder, err := ops.Start(ctx, Request{Capability: "active.held", Client: ClientUI})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := ops.Start(ctx, Request{Capability: "active.held", Client: ClientMCP})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := slices.Clone(inWindow)
	mu.Unlock()
	if len(got) != 0 {
		t.Errorf("Active listed operations between their record and their start event: %v", got)
	}

	byID := map[string]ActiveOperation{}
	for _, a := range ops.Active() {
		byID[a.OperationID] = a
	}
	h, q := byID[holder], byID[queued]
	if h.State != StateRunning || h.Client != ClientUI || h.Capability != "active.held" || h.BlockedBy != nil || h.StartedAt.IsZero() {
		t.Errorf("the holder is listed as %+v; want running, client ui, no blockedBy", h)
	}
	if q.State != StateQueued || q.Client != ClientMCP || q.BlockedBy == nil || q.BlockedBy.OperationID != holder {
		t.Errorf("the queued operation is listed as %+v; want queued, client mcp, blockedBy the holder", q)
	}
	if err := ops.Cancel(queued); err != nil {
		t.Fatal(err)
	}
	if err := ops.Cancel(holder); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{queued, holder} {
		if _, err := ops.Wait(ctx, id, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	if left := ops.Active(); len(left) != 0 || left == nil {
		t.Errorf("Active after both ended is %v; want an empty, non-nil list", left)
	}
}
