package capability

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The per-resource lock, the queued state and the authorization re-check (plan m1e,
// step 5), on test-only capabilities. Each test says which decision it holds.

type lockParams struct {
	Key   string `json:"key,omitempty"`
	Label string `json:"label"`
}

type lockFixture struct {
	ops *Operations

	mu      sync.Mutex
	gates   map[string]chan struct{} // label -> closed to let that handler finish
	order   []string                 // labels in the order their handlers began
	started chan string              // a label per handler start

	active, maxActive atomic.Int32
	ran               atomic.Int32 // handler runs of lock.hold
	authed            atomic.Int32 // hook runs
	deny              atomic.Bool  // the hook denies
	hookGate          chan struct{}
	hookIn            chan string // the hook announces itself, then waits on hookGate when set
	handed            atomic.Value
	keyHeldInHook     atomic.Value // the operation blocked behind the key while the hook ran
}

func (f *lockFixture) gate(label string) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.gates[label]
	if !ok {
		g = make(chan struct{})
		f.gates[label] = g
	}
	return g
}

func (f *lockFixture) open(label string) { close(f.gate(label)) }

func newLockFixture(t *testing.T) *lockFixture {
	t.Helper()
	f := &lockFixture{gates: map[string]chan struct{}{}, started: make(chan string, 256)}
	reg := NewRegistry()
	hold := func(name string, long bool, authorize func(context.Context, *Call) (any, error)) Descriptor {
		var errs []ErrorCode
		if !long {
			errs = []ErrorCode{ErrOperationQueueTimeout}
		}
		return Descriptor{
			Name: name, Kind: Command, Scope: Global, LongRunning: long, Description: "Holds its key until its gate opens.",
			Params: lockParams{}, Result: result{},
			Resource:  func(c *Call) string { return c.Params.(lockParams).Key },
			Authorize: authorize,
			Errors:    errs,
			Handler: func(ctx context.Context, call *Call) (any, error) {
				p := call.Params.(lockParams)
				f.ran.Add(1)
				n := f.active.Add(1)
				for {
					m := f.maxActive.Load()
					if n <= m || f.maxActive.CompareAndSwap(m, n) {
						break
					}
				}
				f.mu.Lock()
				f.order = append(f.order, p.Label)
				f.mu.Unlock()
				if call.Authorized != nil {
					f.handed.Store(call.Authorized)
				}
				f.started <- p.Label
				defer f.active.Add(-1)
				select {
				case <-f.gate(p.Label):
					return result{Count: 1}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			},
		}
	}
	reg.MustRegister(hold("lock.hold", false, nil))
	reg.MustRegister(hold("lock.long", true, nil))
	reg.MustRegister(hold("lock.authorized", false, func(ctx context.Context, call *Call) (any, error) {
		f.authed.Add(1)
		if f.hookIn != nil {
			f.hookIn <- call.Params.(lockParams).Label
			<-f.hookGate
		}
		if f.deny.Load() {
			return nil, NewError("FIXTURE_DENIED", "the policy denies it")
		}
		return "read-by-the-hook", nil
	}))
	f.ops = NewOperations(reg)
	f.ops.RegisterCapabilities()
	return f
}

func (f *lockFixture) start(t *testing.T, capName, key, label string) string {
	t.Helper()
	id, err := f.ops.Start(context.Background(), Request{Capability: capName, Params: []byte(`{"key":"` + key + `","label":"` + label + `"}`)})
	if err != nil {
		t.Fatalf("Start %s: %v", label, err)
	}
	return id
}

func (f *lockFixture) snap(t *testing.T, id string) Outcome {
	t.Helper()
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *lockFixture) waitState(t *testing.T, id string, want State) Outcome {
	t.Helper()
	var out Outcome
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if out = f.snap(t, id); out.State == want {
			return out
		}
	}
	t.Fatalf("operation %s is %s; want %s", id, out.State, want)
	return out
}

func (f *lockFixture) label(t *testing.T) string {
	t.Helper()
	select {
	case l := <-f.started:
		return l
	case <-time.After(5 * time.Second):
		t.Fatal("no handler started")
		return ""
	}
}

// TestSameKeyQueuesAndDifferentKeysRunTogether: the scheduler takes the lock before the
// handler, so a second call on the key is already queued, naming the first, while the
// first's handler runs, and it runs once the first ends. Taken inside the handler,
// the second would report running. Different keys do not contend.
func TestSameKeyQueuesAndDifferentKeysRunTogether(t *testing.T) {
	f := newLockFixture(t)
	a := f.start(t, "lock.hold", "dev-1", "a")
	if got := f.label(t); got != "a" {
		t.Fatalf("started %q; want a", got)
	}
	b := f.start(t, "lock.hold", "dev-1", "b")
	out := f.snap(t, b)
	if out.State != StateQueued || out.BlockedBy == nil || out.BlockedBy.OperationID != a || out.BlockedBy.Capability != "lock.hold" {
		t.Fatalf("second call on the key: state %s, blockedBy %+v; want queued behind %s (lock.hold)", out.State, out.BlockedBy, a)
	}
	if f.ran.Load() != 1 {
		t.Errorf("handlers run = %d with the second queued; want 1", f.ran.Load())
	}
	// A different key is not behind anything.
	c := f.start(t, "lock.hold", "dev-2", "c")
	if got := f.label(t); got != "c" {
		t.Fatalf("started %q; want c, a call on another key, running beside a", got)
	}
	if out := f.snap(t, c); out.State != StateRunning || out.BlockedBy != nil {
		t.Errorf("call on another key: state %s, blockedBy %+v; want running, none", out.State, out.BlockedBy)
	}
	f.open("a")
	if got := f.label(t); got != "b" {
		t.Fatalf("after a ended, started %q; want b", got)
	}
	if out := f.snap(t, b); out.State != StateRunning || out.BlockedBy != nil {
		t.Errorf("b after a ended: state %s, blockedBy %+v; want running, none", out.State, out.BlockedBy)
	}
	f.open("b")
	f.open("c")
	f.waitState(t, b, StateCompleted)
	f.waitState(t, c, StateCompleted)
}

// TestBlockedByFollowsTheHolder: the third call waits behind the first, and once the
// first ends and the second holds the key, it is blocked by the second.
func TestBlockedByFollowsTheHolder(t *testing.T) {
	f := newLockFixture(t)
	a := f.start(t, "lock.hold", "k", "a")
	f.label(t)
	b := f.start(t, "lock.hold", "k", "b")
	c := f.start(t, "lock.hold", "k", "c")
	if by := f.snap(t, c).BlockedBy; by == nil || by.OperationID != a {
		t.Fatalf("c blockedBy %+v; want %s", by, a)
	}
	f.open("a")
	f.label(t)
	if out := f.snap(t, c); out.State != StateQueued || out.BlockedBy == nil || out.BlockedBy.OperationID != b {
		t.Errorf("c after a ended: %s, blockedBy %+v; want queued behind %s", out.State, out.BlockedBy, b)
	}
	f.open("b")
	f.label(t)
	f.open("c")
	f.waitState(t, c, StateCompleted)
}

// TestManyContendersRunOneAtATimeInFIFOOrder: the promise of the lock. Fifty calls on
// one key, started in order: at no moment are two handlers running, they begin in
// start order, and none is lost.
func TestManyContendersRunOneAtATimeInFIFOOrder(t *testing.T) {
	f := newLockFixture(t)
	f.ops.queueBound = time.Minute
	const n = 50
	ids := make([]string, n)
	want := make([]string, n)
	for i := range ids {
		want[i] = string(rune('A'+i/26)) + string(rune('a'+i%26))
		ids[i] = f.start(t, "lock.hold", "one", want[i])
	}
	for i := 0; i < n; i++ {
		if got := f.label(t); got != want[i] {
			t.Fatalf("handler %d to start is %q; want %q (FIFO)", i, got, want[i])
		}
		if i+1 < n && f.snap(t, ids[i+1]).State != StateQueued {
			t.Fatalf("%s is not queued while %s holds the key", want[i+1], want[i])
		}
		f.open(want[i])
	}
	for _, id := range ids {
		f.waitState(t, id, StateCompleted)
	}
	if m := f.maxActive.Load(); m != 1 {
		t.Errorf("at most %d handlers ran at once; want strictly 1", m)
	}
	if f.ran.Load() != n {
		t.Errorf("%d handlers ran; want %d, none lost", f.ran.Load(), n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range want {
		if f.order[i] != want[i] {
			t.Fatalf("handler order %v; want %v", f.order, want)
		}
	}
}

// TestQueueBoundFailsAShortCallThatNeverRan: a call not flagged long-running that is
// still queued at the bound fails OPERATION_QUEUE_TIMEOUT with blockedBy and waitedMs,
// and its handler never ran (ADR-0020 §Queue bound); the holder is unaffected.
func TestQueueBoundFailsAShortCallThatNeverRan(t *testing.T) {
	f := newLockFixture(t)
	f.ops.queueBound = 60 * time.Millisecond
	a := f.start(t, "lock.hold", "k", "a")
	f.label(t)
	b := f.start(t, "lock.hold", "k", "b")
	out := f.waitState(t, b, StateFailed)
	if out.Error == nil || out.Error.Code != ErrOperationQueueTimeout {
		t.Fatalf("queued call error %+v; want %s", out.Error, ErrOperationQueueTimeout)
	}
	by, _ := out.Error.Details["blockedBy"].(map[string]any)
	if by["operationId"] != a || by["capability"] != "lock.hold" {
		t.Errorf("details.blockedBy = %v; want %s / lock.hold", by, a)
	}
	if ms, _ := out.Error.Details["waitedMs"].(int64); ms < 60 {
		t.Errorf("details.waitedMs = %v; want at least the 60 ms bound", out.Error.Details["waitedMs"])
	}
	if f.ran.Load() != 1 {
		t.Errorf("handlers run = %d; want 1: the timed-out call never ran", f.ran.Load())
	}
	if out.BlockedBy != nil {
		t.Errorf("a failed operation still carries blockedBy %+v", out.BlockedBy)
	}
	if got := f.snap(t, a).State; got != StateRunning {
		t.Errorf("the holder is %s after the other timed out; want running", got)
	}
	// Its place is gone: a later call queues behind a alone and is not run by the dead one's turn.
	f.open("a")
	c := f.start(t, "lock.hold", "k", "c")
	f.label(t)
	f.open("c")
	f.waitState(t, c, StateCompleted)
	if f.ran.Load() != 2 {
		t.Errorf("handlers run = %d; want 2", f.ran.Load())
	}
}

// TestLongRunningHasNoQueueBound: the bound is for calls not flagged long-running.
func TestLongRunningHasNoQueueBound(t *testing.T) {
	f := newLockFixture(t)
	f.ops.queueBound = 30 * time.Millisecond
	f.start(t, "lock.hold", "k", "a")
	f.label(t)
	b := f.start(t, "lock.long", "k", "b")
	time.Sleep(250 * time.Millisecond) // eight bounds
	if out := f.snap(t, b); out.State != StateQueued {
		t.Fatalf("a long-running call is %s after eight bounds (%+v); want queued", out.State, out.Error)
	}
	f.open("a")
	if got := f.label(t); got != "b" {
		t.Fatalf("started %q; want b", got)
	}
	f.open("b")
	f.waitState(t, b, StateCompleted)
}

// TestCancelOfAQueuedCallIsImmediateAndFreesItsPlace: Cancel returns with the
// operation already cancelled, with no handler run and OPERATION_CANCELLED; the call
// behind it moves up and names the holder, and runs next.
func TestCancelOfAQueuedCallIsImmediateAndFreesItsPlace(t *testing.T) {
	f := newLockFixture(t)
	f.ops.queueBound = time.Minute
	a := f.start(t, "lock.hold", "k", "a")
	f.label(t)
	b := f.start(t, "lock.hold", "k", "b")
	c := f.start(t, "lock.hold", "k", "c")
	if err := f.ops.Cancel(b); err != nil {
		t.Fatal(err)
	}
	out := f.snap(t, b) // no waiting: the state when Cancel returned
	if out.State != StateCancelled || out.Error == nil || out.Error.Code != ErrOperationCancelled || out.BlockedBy != nil {
		t.Fatalf("queued call right after Cancel: %s, error %+v, blockedBy %+v; want cancelled at once", out.State, out.Error, out.BlockedBy)
	}
	if out := f.snap(t, c); out.State != StateQueued || out.BlockedBy == nil || out.BlockedBy.OperationID != a {
		t.Errorf("c: %s, blockedBy %+v; want queued behind %s", out.State, out.BlockedBy, a)
	}
	f.open("a")
	if got := f.label(t); got != "c" {
		t.Fatalf("after a ended, started %q; want c (b was cancelled)", got)
	}
	f.open("c")
	f.waitState(t, c, StateCompleted)
	if f.ran.Load() != 2 {
		t.Errorf("handlers run = %d; want 2: the cancelled call never ran", f.ran.Load())
	}
}

// TestAuthorizationIsRecheckedWithTheKeyHeldBeforeTheHandler: for a call that really
// queued, the hook does not run while it waits, runs once the key is handed over and
// with the key held (a third call is queued behind it), and before the handler. A
// denial fails the operation with the hook's error and the handler never runs.
func TestAuthorizationIsRecheckedWithTheKeyHeldBeforeTheHandler(t *testing.T) {
	f := newLockFixture(t)
	f.ops.queueBound = time.Minute
	f.hookIn, f.hookGate = make(chan string, 1), make(chan struct{})
	f.deny.Store(true)
	h := f.start(t, "lock.hold", "k", "h")
	f.label(t)
	a := f.start(t, "lock.authorized", "k", "a")
	if out := f.snap(t, a); out.State != StateQueued {
		t.Fatalf("hooked call behind a holder is %s; want queued", out.State)
	}
	select {
	case got := <-f.hookIn:
		t.Fatalf("the hook ran for %q while its call was still queued: it must run only with the key held", got)
	case <-time.After(50 * time.Millisecond):
	}
	if f.authed.Load() != 0 {
		t.Fatalf("hook runs = %d while queued; want 0", f.authed.Load())
	}
	f.open("h")
	select {
	case got := <-f.hookIn:
		if got != "a" {
			t.Fatalf("hook ran for %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the authorization hook never ran after the key was handed over")
	}
	_ = h
	if f.ran.Load() != 1 {
		t.Errorf("handlers run = %d while the hook runs; want only the holder's: the hook comes before the handler", f.ran.Load())
	}
	b := f.start(t, "lock.hold", "k", "b")
	if out := f.snap(t, b); out.State != StateQueued || out.BlockedBy == nil || out.BlockedBy.OperationID != a {
		t.Errorf("while the hook runs, b is %s (%+v); want queued behind the hook's operation: the key is held", out.State, out.BlockedBy)
	}
	close(f.hookGate)
	out := f.waitState(t, a, StateFailed)
	if out.Error == nil || out.Error.Code != "FIXTURE_DENIED" {
		t.Fatalf("denied call error %+v; want FIXTURE_DENIED", out.Error)
	}
	if got := f.label(t); got != "b" {
		t.Fatalf("started %q; want b, which takes the key after the denial", got)
	}
	f.open("b")
	f.waitState(t, b, StateCompleted)
	if f.ran.Load() != 2 {
		t.Errorf("handlers run = %d; want the holder's and b's, not a's", f.ran.Load())
	}
}

// TestKeyIsHandedOverBeforeACompletedCallIsVisible: the key passes to the next caller
// in the same critical section as the terminal transition, so a caller that sees A
// completed and starts B on the key never finds B queued behind a finished holder.
func TestKeyIsHandedOverBeforeACompletedCallIsVisible(t *testing.T) {
	f := newLockFixture(t)
	for i := 0; i < 200; i++ {
		label := "a" + strconv.Itoa(i)
		a := f.start(t, "lock.hold", "k", label)
		f.label(t)
		f.open(label)
		f.waitState(t, a, StateCompleted)
		l2 := "b" + label
		b := f.start(t, "lock.hold", "k", l2)
		if out := f.snap(t, b); out.State == StateQueued {
			t.Fatalf("iteration %d: B is queued (behind %+v) right after A was seen completed", i, out.BlockedBy)
		}
		f.label(t)
		f.open(l2)
		f.waitState(t, b, StateCompleted)
	}
}

// TestCancelledAtGrantRunsNeitherHookNorHandler: a call whose context is already
// cancelled when it is granted the key ends cancelled; the hook and the handler do not
// run, and the key is released. Driven through execute directly, since the instant
// between grant and run cannot be held open from outside.
func TestCancelledAtGrantRunsNeitherHookNorHandler(t *testing.T) {
	f := newLockFixture(t)
	d, _ := f.ops.reg.Lookup("lock.authorized")
	ctx, cancel := context.WithCancel(context.Background())
	op := &operation{id: "op-x", capability: d.Name, state: StateRunning, cancel: cancel, done: make(chan struct{}), cancelRequested: true}
	f.ops.ops[op.id] = op
	f.ops.locks["k"] = &lock{holder: op}
	cancel()
	f.ops.execute(ctx, op, d, &Call{Params: lockParams{Key: "k", Label: "x"}}, "k")
	if op.state != StateCancelled || op.err == nil || op.err.Code != ErrOperationCancelled {
		t.Errorf("state %s, error %+v; want cancelled", op.state, op.err)
	}
	if f.authed.Load() != 0 || f.ran.Load() != 0 {
		t.Errorf("hook runs %d, handler runs %d; want neither", f.authed.Load(), f.ran.Load())
	}
	if _, held := f.ops.locks["k"]; held {
		t.Error("the key is still held after the call ended")
	}
}

// TestCancelWorkspaceEndsAQueuedCallAtOnce: a workspace close ends a queued call
// cancelled immediately, with no handler run, and the queue behind it stays intact.
func TestCancelWorkspaceEndsAQueuedCallAtOnce(t *testing.T) {
	f := newLockFixture(t)
	f.ops.queueBound = time.Minute
	a := f.start(t, "lock.hold", "k", "a")
	f.label(t)
	b := f.start(t, "lock.hold", "k", "b")
	c := f.start(t, "lock.hold", "k", "c")
	f.ops.mu.Lock()
	f.ops.ops[b].workspace = "ws-1" // as a workspace-scoped call is recorded
	f.ops.mu.Unlock()
	f.ops.CancelWorkspace("ws-1")
	out := f.snap(t, b) // no waiting
	if out.State != StateCancelled || out.Error == nil || out.Error.Code != ErrOperationCancelled || out.BlockedBy != nil {
		t.Fatalf("queued call after CancelWorkspace: %s, error %+v, blockedBy %+v; want cancelled at once", out.State, out.Error, out.BlockedBy)
	}
	if f.ran.Load() != 1 {
		t.Errorf("handlers run = %d; want 1: the cancelled call never ran", f.ran.Load())
	}
	if out := f.snap(t, c); out.State != StateQueued || out.BlockedBy == nil || out.BlockedBy.OperationID != a {
		t.Errorf("c: %s, blockedBy %+v; want queued behind %s", out.State, out.BlockedBy, a)
	}
	f.open("a")
	if got := f.label(t); got != "c" {
		t.Fatalf("after a ended, started %q; want c", got)
	}
	f.open("c")
	f.waitState(t, c, StateCompleted)
}

// TestDenialOnACapabilityWithNoKey: the re-check does not depend on a lock. And an
// approval hands what the hook read to the handler.
func TestDenialOnACapabilityWithNoKey(t *testing.T) {
	f := newLockFixture(t)
	f.deny.Store(true)
	a := f.start(t, "lock.authorized", "", "a")
	out := f.waitState(t, a, StateFailed)
	if out.Error == nil || out.Error.Code != "FIXTURE_DENIED" || f.ran.Load() != 0 || f.authed.Load() != 1 {
		t.Fatalf("keyless denial: error %+v, handler runs %d, hook runs %d; want FIXTURE_DENIED, 0, 1", out.Error, f.ran.Load(), f.authed.Load())
	}

	f.deny.Store(false)
	b := f.start(t, "lock.authorized", "", "b")
	f.label(t)
	if got := f.handed.Load(); got != "read-by-the-hook" {
		t.Errorf("the handler saw Call.Authorized = %v; want what the hook returned", got)
	}
	f.open("b")
	f.waitState(t, b, StateCompleted)
}

// TestShutdownEndsQueuedCalls: a queued operation is an operation Shutdown must end,
// or it would wait on it forever.
func TestShutdownEndsQueuedCalls(t *testing.T) {
	f := newLockFixture(t)
	f.ops.queueBound = time.Minute
	f.start(t, "lock.hold", "k", "a")
	f.label(t)
	b := f.start(t, "lock.hold", "k", "b")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.ops.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown with a queued call: %v", err)
	}
	if got := f.snap(t, b).State; got != StateCancelled {
		t.Errorf("queued call after Shutdown is %s; want cancelled", got)
	}
	if f.ran.Load() != 1 {
		t.Errorf("handlers run = %d; want 1: the queued call never ran", f.ran.Load())
	}
}

// TestQueueBoundRunsFromTheEnqueue: the bound counts from when the call was queued,
// not from when its goroutine began waiting; a waiter already past the bound fails at
// once, and waitedMs reports the real queue time.
func TestQueueBoundRunsFromTheEnqueue(t *testing.T) {
	f := newLockFixture(t)
	f.ops.queueBound = 5 * time.Second
	f.start(t, "lock.hold", "k", "a")
	f.label(t)
	d, _ := f.ops.reg.Lookup("lock.hold")
	op := &operation{id: "op-q", capability: d.Name, state: StateRunning, cancel: func() {}, done: make(chan struct{})}
	f.ops.mu.Lock()
	f.ops.ops[op.id] = op
	f.ops.mu.Unlock()
	f.ops.mu.Lock()
	w := f.ops.enqueueLocked(op, "k")
	w.since = time.Now().Add(-6 * time.Second)
	f.ops.mu.Unlock()
	start := time.Now()
	if f.ops.awaitLock(context.Background(), op, d, w) {
		t.Fatal("awaitLock granted a lock that is held")
	}
	if time.Since(start) > time.Second {
		t.Errorf("awaitLock took %s; a waiter 6 s into a 5 s bound must fail at once", time.Since(start))
	}
	if op.state != StateFailed || op.err == nil || op.err.Code != ErrOperationQueueTimeout {
		t.Fatalf("state %s, error %+v; want failed OPERATION_QUEUE_TIMEOUT", op.state, op.err)
	}
	if ms, _ := op.err.Details["waitedMs"].(int64); ms < 6000 {
		t.Errorf("waitedMs = %v; want at least 6000", op.err.Details["waitedMs"])
	}
}
