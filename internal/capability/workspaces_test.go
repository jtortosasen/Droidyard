package capability

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The store's side of the per-root lifetime (ADR-0137 §3; plan m3j step 5): a call of
// scope workspace is admitted by Workspaces.Enter at Start, and the registry hears of each
// operation's end, outside the store's lock.

// enterWS is a Workspaces that names every project "ws-<project>" with the root
// "/root/<project>" and the Gradle root "/gradle/<project>" (two projects spelled alike
// below share the Gradle root "/gradle/shared"), and refuses "refused". It keeps the
// project string Enter was given.
type enterWS struct {
	ops      *Operations
	entered  atomic.Int32
	ended    chan string
	mu       sync.Mutex
	projects []string
}

func (w *enterWS) Enter(project string, record func(Entered)) error {
	w.entered.Add(1)
	w.mu.Lock()
	w.projects = append(w.projects, project)
	w.mu.Unlock()
	if project == "refused" {
		return NewError(ErrInvalidParams, "the project cannot be used").WithNextStep("pass an absolute path")
	}
	gradle := "/gradle/" + project
	if project == "top" || project == "top/dev" {
		gradle = "/gradle/shared"
	}
	record(Entered{ID: "ws-" + project, Root: "/root/" + project, GradleRoot: gradle})
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
	saw     atomic.Value // Call: the id, root and Gradle root the last scoped.ok ran with
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
		f.saw.Store(*call)
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
	// The lock a build takes: the Gradle root the project resolved to (plan m3j step 6).
	scoped("scoped.gradle", func(ctx context.Context, call *Call) (any, error) {
		f.started <- call.Workspace
		<-ctx.Done()
		return nil, ctx.Err()
	}, func(call *Call) string { return "gradle.root:" + call.GradleRoot })
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
// admitted by Enter, which is given the call's `project` and names the id, the root and the
// Gradle root the handler runs with; `project` is taken off the parameters, so the
// handler's own strict decoding never meets it; one Enter refuses fails at Start with
// Enter's own error, recorded as no operation and run by no handler; a global call never
// asks Enter.
func TestStartEntersAScopedCallAndRecordsNothingWhenRefused(t *testing.T) {
	f := newEnterFixture(t)

	out, err := f.ops.Invoke(context.Background(), Request{Capability: "scoped.ok", Params: withProject(nil, "a")})
	saw, _ := f.saw.Load().(Call)
	if err != nil || out.State != StateCompleted || saw.Workspace != "ws-a" || saw.Root != "/root/a" || saw.GradleRoot != "/gradle/a" {
		t.Fatalf("a scoped call: %v %+v, the handler saw %q, %q, %q; want completed on ws-a, /root/a, /gradle/a", err, out, saw.Workspace, saw.Root, saw.GradleRoot)
	}
	if f.ws.projects[0] != "a" {
		t.Errorf("Enter was given %q; want the project as the caller sent it", f.ws.projects[0])
	}
	recvWithin(t, f.ws.ended, "the end of the scoped call")

	before := f.ws.entered.Load()
	id, err := f.ops.Start(context.Background(), Request{Capability: "scoped.ok", Params: withProject(nil, "refused")})
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

	if out, _ := f.ops.Invoke(ctx, Request{Capability: "scoped.ok", Params: withProject(nil, "c")}); out.State != StateCompleted {
		t.Fatalf("completed: %+v", out)
	}
	expect("a completed operation", "ws-c")
	if out, _ := f.ops.Invoke(ctx, Request{Capability: "scoped.fail", Params: withProject(nil, "f")}); out.State != StateFailed {
		t.Fatalf("failed: %+v", out)
	}
	expect("a failed operation", "ws-f")
	if _, err := f.ops.Invoke(ctx, Request{Capability: "global.ok"}); err != nil {
		t.Fatal(err)
	}

	running, err := f.ops.Start(ctx, Request{Capability: "scoped.block", Params: withProject(nil, "r")})
	if err != nil {
		t.Fatal(err)
	}
	recvWithin(t, f.started, "the blocking operation")
	f.ops.Cancel(running)
	expect("a cancelled running operation", "ws-r")

	holder, _ := f.ops.Start(ctx, Request{Capability: "scoped.keyed", Params: withProject(nil, "h")})
	recvWithin(t, f.started, "the key's holder")
	queued, err := f.ops.Start(ctx, Request{Capability: "scoped.keyed", Params: withProject(nil, "q")})
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
	holder, _ := f.ops.Start(ctx, Request{Capability: "scoped.keyed", Params: withProject(nil, "x")})
	recvWithin(t, f.started, "the key's holder")
	queued, _ := f.ops.Start(ctx, Request{Capability: "scoped.keyed", Params: withProject(nil, "x")})
	other, _ := f.ops.Start(ctx, Request{Capability: "scoped.block", Params: withProject(nil, "y")})
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

// TestTwoSpellingsOfOneProjectQueueOnItsGradleRoot is the core's half of ADR-0137 §3's lock
// key: the key is built from the Gradle root Enter resolved (Call.GradleRoot), not from the
// path the caller passed or the workspace's id. Two spellings of one project, its top and
// its dev/, are two workspaces with one Gradle root, so the second call queues behind the
// first, named in blockedBy, while a call on another root does not. Seen failing with the
// key built from Call.Workspace: the second call ran at once.
func TestTwoSpellingsOfOneProjectQueueOnItsGradleRoot(t *testing.T) {
	f := newEnterFixture(t)
	ctx := context.Background()
	first, err := f.ops.Start(ctx, Request{Capability: "scoped.gradle", Params: withProject(nil, "top")})
	if err != nil {
		t.Fatal(err)
	}
	recvWithin(t, f.started, "the first build")
	second, err := f.ops.Start(ctx, Request{Capability: "scoped.gradle", Params: withProject(nil, "top/dev")})
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.ops.Start(ctx, Request{Capability: "scoped.gradle", Params: withProject(nil, "elsewhere")})
	if err != nil {
		t.Fatal(err)
	}
	recvWithin(t, f.started, "a build of another Gradle root, which does not queue")

	out, err := f.ops.Wait(ctx, second, time.Now())
	if err != nil || out.State != StateQueued || out.BlockedBy == nil || out.BlockedBy.OperationID != first {
		t.Fatalf("the second spelling: %+v, %v; want queued, blocked by %s", out, err, first)
	}
	if out, _ := f.ops.Wait(ctx, other, time.Now()); out.State != StateRunning {
		t.Errorf("a build of another Gradle root is %s; want running", out.State)
	}
	f.ops.Cancel(first)
	recvWithin(t, f.started, "the second spelling, once the first ended")
	f.ops.Cancel(second)
	f.ops.Cancel(other)
	for _, id := range []string{first, second, other} {
		f.ops.Wait(ctx, id, time.Now().Add(5*time.Second))
	}
}
