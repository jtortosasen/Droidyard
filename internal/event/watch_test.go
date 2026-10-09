package event

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// operation.watch (ADR-0043): a subscription to every operation's state events, and the
// operations not yet terminal, taken after the subscription is open.

type watchFixture struct {
	bus *Bus
	ops *capability.Operations

	mu     sync.Mutex
	window []string // operations Active listed between their record and their start event
}

func newWatchFixture(t *testing.T) *watchFixture {
	t.Helper()
	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	f := &watchFixture{bus: New(Options{}), ops: ops}
	Register(reg, ops.Active)
	ops.UseStatePublisher(f.bus)
	quickKey := func(call *capability.Call) string {
		// Runs between the operation's record and its start event.
		for _, a := range ops.Active() {
			if a.OperationID == call.OperationID {
				f.mu.Lock()
				f.window = append(f.window, a.OperationID+" "+string(a.State))
				f.mu.Unlock()
			}
		}
		return "k"
	}
	reg.MustRegister(capability.Descriptor{
		Name: "fx.quick", Kind: capability.Command, Scope: capability.Global, Description: "Returns at once.",
		Params: struct{}{}, Result: struct{}{}, Resource: quickKey,
		Errors:  []capability.ErrorCode{capability.ErrOperationQueueTimeout},
		Handler: func(context.Context, *capability.Call) (any, error) { return struct{}{}, nil },
	})
	reg.MustRegister(capability.Descriptor{
		Name: "fx.hold", Kind: capability.Command, Scope: capability.Global, Description: "Holds one key until cancelled.",
		Params: struct{}{}, Result: struct{}{}, Resource: func(*capability.Call) string { return "hold" }, LongRunning: true,
		Handler: func(ctx context.Context, _ *capability.Call) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	reg.MustRegister(capability.Descriptor{
		Name: "fx.free", Kind: capability.Command, Scope: capability.Global, Description: "Returns at once, no lock.",
		Params: struct{}{}, Result: struct{}{},
		Handler: func(context.Context, *capability.Call) (any, error) { return struct{}{}, nil },
	})
	return f
}

// watch calls operation.watch as sess, through the store's real Start and Wait.
func (f *watchFixture) watch(t *testing.T, sess *Session) (WatchResult, *Subscription) {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: CapOperationWatch, Client: capability.ClientUI, Session: sess})
	if err != nil {
		t.Fatalf("operation.watch: %v", err)
	}
	out, err := f.ops.Wait(context.Background(), id, time.Time{})
	if err != nil || out.State != capability.StateCompleted {
		t.Fatalf("operation.watch: %+v, %v", out, err)
	}
	res, ok := out.Result.(WatchResult)
	if !ok {
		t.Fatalf("operation.watch answered %T", out.Result)
	}
	sub, ok := sess.Subscription(res.SubscriptionID)
	if !ok {
		t.Fatalf("the session has no subscription %s", res.SubscriptionID)
	}
	return res, sub
}

// started starts a fixture operation and returns its id.
func (f *watchFixture) started(t *testing.T, name string) string {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: name, Client: capability.ClientCLI})
	if err != nil {
		t.Fatalf("Start %s: %v", name, err)
	}
	return id
}

func (f *watchFixture) waitFor(t *testing.T, id string) capability.Outcome {
	t.Helper()
	out, err := f.ops.Wait(context.Background(), id, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// stateOf is one operation.state event as a watcher decodes it.
type stateOf struct {
	seq       int64
	operation string
	payload   capability.OperationState
}

// drainState reads what sub has queued, failing on anything but operation.state events.
func drainState(t *testing.T, sub *Subscription) []stateOf {
	t.Helper()
	var out []stateOf
	for {
		e, ok := sub.TryNext()
		if !ok {
			return out
		}
		if e.Topic != capability.StateTopic || e.Type != capability.StateType {
			t.Fatalf("the watch received %s/%s (%+v); want operation.state/state only", e.Topic, e.Type, e)
		}
		var p capability.OperationState
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("payload %s: %v", e.Payload, err)
		}
		out = append(out, stateOf{e.Seq, e.Operation, p})
	}
}

// awaitState reads sub until an event of (id, state) arrives or a deadline passes, and
// returns every event read.
func awaitState(t *testing.T, sub *Subscription, id string, state capability.State) []stateOf {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got []stateOf
	for {
		e, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("no %s event for %s after 5 s; read %+v", state, id, got)
		}
		var p capability.OperationState
		if json.Unmarshal(e.Payload, &p) != nil || e.Topic != capability.StateTopic {
			t.Fatalf("the watch received %+v", e)
		}
		got = append(got, stateOf{e.Seq, e.Operation, p})
		if e.Operation == id && p.State == state {
			return got
		}
	}
}

func statesOf(events []stateOf, id string) []capability.State {
	var out []capability.State
	for _, e := range events {
		if e.operation == id {
			out = append(out, e.payload.State)
		}
	}
	return out
}

func inSnapshot(r WatchResult, id string) (capability.ActiveOperation, bool) {
	for _, a := range r.Operations {
		if a.OperationID == id {
			return a, true
		}
	}
	return capability.ActiveOperation{}, false
}

// TestOperationWatchMissesNothing is the decision of ADR-0043 §1, "the snapshot is taken
// after the subscription is open": an operation that starts, or starts and ends, in the
// window between the two is not lost. The hook runs in that window, so the order is
// decided by the test and not by chance; with the snapshot taken before the subscription,
// both operations are missing from everything the watcher is given.
func TestOperationWatchMissesNothing(t *testing.T) {
	t.Run("an operation that starts in the window and runs on is in the snapshot", func(t *testing.T) {
		f := newWatchFixture(t)
		var inWindow string
		f.bus.SetWatchHook(func(string) { inWindow = f.started(t, "fx.hold") })
		res, sub := f.watch(t, NewSession(f.bus, "ses-a"))
		f.bus.SetWatchHook(nil)
		a, ok := inSnapshot(res, inWindow)
		if !ok || a.State != capability.StateRunning || a.Capability != "fx.hold" || a.Client != capability.ClientCLI {
			t.Errorf("the snapshot %+v lacks the running operation %s that started in the window (found %+v)", res.Operations, inWindow, a)
		}
		// Its start event is the subscription's too: a duplicate the client merges by id.
		if got := statesOf(drainState(t, sub), inWindow); len(got) == 0 || got[0] != capability.StateRunning {
			t.Errorf("the watch received %v for the operation that started in the window; want its running event", got)
		}
		if err := f.ops.Cancel(inWindow); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("an operation that starts and ends in the window reaches the watcher as events", func(t *testing.T) {
		f := newWatchFixture(t)
		var inWindow string
		f.bus.SetWatchHook(func(string) {
			inWindow = f.started(t, "fx.free")
			f.waitFor(t, inWindow)
		})
		res, sub := f.watch(t, NewSession(f.bus, "ses-a"))
		f.bus.SetWatchHook(nil)
		if a, ok := inSnapshot(res, inWindow); ok {
			t.Errorf("the snapshot lists %+v, which ended before it was taken", a)
		}
		got := statesOf(awaitState(t, sub, inWindow, capability.StateCompleted), inWindow)
		if !slices.Equal(got, []capability.State{capability.StateRunning, capability.StateCompleted}) {
			t.Errorf("the watch received %v for the operation that started and ended in the window; want running, completed", got)
		}
	})

	t.Run("under load every watch sees every operation it can", func(t *testing.T) {
		const quick, watches = 200, 50
		f := newWatchFixture(t)
		ctx := context.Background()

		// Three held operations that outlive every watch: one running, two queued. Every
		// snapshot must list them, the queued ones as queued.
		h1 := f.started(t, "fx.hold")
		for f.waitStatus(h1) != capability.StateRunning {
			time.Sleep(time.Millisecond)
		}
		h2, h3 := f.started(t, "fx.hold"), f.started(t, "fx.hold")

		witness := NewSession(f.bus, "witness")
		witness.SetQueue(1 << 16)
		_, wsub, err := witness.Open([]string{capability.StateTopic}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var hookMu sync.Mutex
		openedAt := map[string]int64{} // session id -> the bus's seq when its subscription was open
		f.bus.SetWatchHook(func(id string) {
			p := f.bus.Position().Seq
			hookMu.Lock()
			openedAt[id] = p
			hookMu.Unlock()
		})
		t.Cleanup(func() { f.bus.SetWatchHook(nil) })

		var (
			wg       sync.WaitGroup
			idsMu    sync.Mutex
			allIDs   []string
			results  = make([]WatchResult, watches)
			subs     = make([]*Subscription, watches)
			sessions = make([]*Session, watches)
		)
		begin := make(chan struct{})
		for i := 0; i < quick; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-begin
				id, err := f.ops.Start(ctx, capability.Request{Capability: "fx.quick", Client: capability.ClientCLI})
				if err != nil {
					t.Errorf("Start fx.quick: %v", err)
					return
				}
				idsMu.Lock()
				allIDs = append(allIDs, id)
				idsMu.Unlock()
			}()
		}
		for i := 0; i < watches; i++ {
			sessions[i] = NewSession(f.bus, fmt.Sprintf("ses-%d", i))
			sessions[i].SetQueue(1 << 16)
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-begin
				id, err := f.ops.Start(ctx, capability.Request{Capability: CapOperationWatch, Client: capability.ClientUI, Session: sessions[i]})
				if err != nil {
					t.Errorf("operation.watch: %v", err)
					return
				}
				idsMu.Lock()
				allIDs = append(allIDs, id)
				idsMu.Unlock()
				out, err := f.ops.Wait(ctx, id, time.Time{})
				if err != nil || out.State != capability.StateCompleted {
					t.Errorf("operation.watch: %+v, %v", out, err)
					return
				}
				results[i] = out.Result.(WatchResult)
				subs[i], _ = sessions[i].Subscription(results[i].SubscriptionID)
			}()
		}
		close(begin)
		wg.Wait()
		if t.Failed() {
			t.FailNow()
		}
		for _, id := range allIDs {
			f.waitFor(t, id)
		}
		for _, id := range []string{h2, h3, h1} {
			if err := f.ops.Cancel(id); err != nil {
				t.Fatal(err)
			}
		}
		for _, id := range []string{h1, h2, h3} {
			f.waitFor(t, id)
		}
		allIDs = append(allIDs, h1, h2, h3)

		// The witness holds every operation's terminal event and its seq.
		terminal := map[string]int64{}
		deadline := time.Now().Add(10 * time.Second)
		for len(terminal) < len(allIDs) {
			if time.Now().After(deadline) {
				t.Fatalf("%d terminal events of %d operations after 10 s", len(terminal), len(allIDs))
			}
			for _, e := range drainState(t, wsub) {
				if e.payload.State.Terminal() {
					terminal[e.operation] = e.seq
				}
			}
			time.Sleep(time.Millisecond)
		}

		for i := 0; i < watches; i++ {
			res, events := results[i], drainState(t, subs[i])
			seen := map[string]bool{}
			for _, a := range res.Operations {
				seen[a.OperationID] = true
				switch {
				case a.StartedAt.IsZero() || a.Capability == "":
					t.Errorf("watch %d: a snapshot row %+v lacks its capability or startedAt", i, a)
				case a.State == capability.StateQueued && (a.BlockedBy == nil || a.BlockedBy.OperationID == ""):
					t.Errorf("watch %d: %s is queued with no blockedBy: %+v", i, a.OperationID, a)
				case a.State == capability.StateRunning && a.BlockedBy != nil:
					t.Errorf("watch %d: %s is running with blockedBy: %+v", i, a.OperationID, a)
				case a.State != capability.StateQueued && a.State != capability.StateRunning:
					t.Errorf("watch %d: the snapshot lists %s as %s", i, a.OperationID, a.State)
				}
			}
			byOp := map[string][]capability.State{}
			last := int64(0)
			for _, e := range events {
				seen[e.operation] = true
				byOp[e.operation] = append(byOp[e.operation], e.payload.State)
				if e.seq <= last {
					t.Errorf("watch %d: event seq %d after %d", i, e.seq, last)
				}
				last = e.seq
			}
			for id, states := range byOp {
				if !isTransitionOrder(states) {
					t.Errorf("watch %d: operation %s received %v; want queued?, running?, one terminal, in that order", i, id, states)
				}
			}
			// The held operations started before every watch and ended after: the snapshot
			// has them, running or queued.
			if a, ok := inSnapshot(res, h1); !ok || a.State != capability.StateRunning {
				t.Errorf("watch %d: the snapshot has %+v for the running holder %s", i, a, h1)
			}
			for _, h := range []string{h2, h3} {
				if a, ok := inSnapshot(res, h); !ok || a.State != capability.StateQueued || a.BlockedBy == nil || a.BlockedBy.OperationID != h1 {
					t.Errorf("watch %d: the snapshot has %+v for %s, queued behind %s", i, a, h, h1)
				}
			}
			// Every operation that ended after the subscription was open is seen, in the
			// snapshot or by its events: the watches' own operations included.
			hookMu.Lock()
			opened, ok := openedAt[sessions[i].ID()]
			hookMu.Unlock()
			if !ok {
				t.Fatalf("watch %d: the hook never ran", i)
			}
			for id, seq := range terminal {
				if seq > opened && !seen[id] {
					t.Errorf("watch %d (open at seq %d): operation %s ended at seq %d and is neither in its snapshot nor in its events", i, opened, id, seq)
				}
			}
		}
		f.mu.Lock()
		window := slices.Clone(f.window)
		f.mu.Unlock()
		if len(window) != 0 {
			t.Errorf("Active listed %d operations between their record and their start event, such as %v", len(window), window[:min(len(window), 3)])
		}
	})
}

// isTransitionOrder reports whether states is a prefix-free path of one operation's
// life: an optional queued, an optional running, then exactly one terminal state, last.
func isTransitionOrder(states []capability.State) bool {
	rank := func(s capability.State) int {
		switch s {
		case capability.StateQueued:
			return 0
		case capability.StateRunning:
			return 1
		}
		return 2
	}
	for i := 1; i < len(states); i++ {
		if rank(states[i]) <= rank(states[i-1]) {
			return false
		}
	}
	return true
}

func (f *watchFixture) waitStatus(id string) capability.State {
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(-time.Second))
	if err != nil {
		return ""
	}
	return out.State
}

// TestOperationWatchNeedsASession: without a session that can carry events, the call is
// refused at Start with SESSION_REQUIRED and no operation exists (ADR-0010; ADR-0043 §1).
func TestOperationWatchNeedsASession(t *testing.T) {
	f := newWatchFixture(t)
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: CapOperationWatch, Client: capability.ClientCLI})
	var e *capability.Error
	if id != "" || !asError(err, &e) || e.Code != capability.ErrSessionRequired {
		t.Fatalf("operation.watch without a session: (%q, %v); want SESSION_REQUIRED and no operation", id, err)
	}
	if n := f.ops.Running(); n != 0 || f.bus.Subscriptions() != 0 {
		t.Errorf("%d operations and %d subscriptions after the refusal; want none", n, f.bus.Subscriptions())
	}
}

func asError(err error, target **capability.Error) bool {
	e, ok := err.(*capability.Error)
	*target = e
	return ok
}

// TestStateEventsNeverEnterTheReplayRing is the decision of ADR-0043 §3: an operation.state
// event takes a seq and is offered to every operation.watch subscription, but it is not
// in the replay ring (a busy activity feed cannot flush what other subscribers resume
// from) and events.subscribe cannot name its topic.
func TestStateEventsNeverEnterTheReplayRing(t *testing.T) {
	f := newWatchFixture(t)
	_, sub := f.watch(t, NewSession(f.bus, "ses-a"))
	before := f.bus.Position().Seq
	id := f.started(t, "fx.free")
	f.waitFor(t, id)
	awaitState(t, sub, id, capability.StateCompleted)
	if after := f.bus.Position().Seq; after < before+2 {
		t.Errorf("the bus is at seq %d after an operation's two events, from %d; each takes a seq", after, before)
	}
	f.bus.mu.Lock()
	ring := len(f.bus.ring)
	f.bus.mu.Unlock()
	if ring != 0 {
		t.Errorf("the replay ring holds %d events; operation.state must never enter it", ring)
	}
	var topic Topic
	if err := json.Unmarshal([]byte(`"`+capability.StateTopic+`"`), &topic); err == nil {
		t.Errorf("events.subscribe accepts topic %s; it is offered to operation.watch subscriptions only", capability.StateTopic)
	}
}
