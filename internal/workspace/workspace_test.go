package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/store"
)

// syncBuffer is a log the close goroutine writes while the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// harness is the shipped workspace registry and operation store, in process, with
// test-only workspace-scoped fixtures. Every fixture returns nil, never an error, so
// an operation that ends cancelled was cancelled by the store, not by its handler:
//
//	fixture.honours  sends its operation id on started, then returns nil once its
//	                 context is cancelled
//	fixture.ignores  sends its operation id on started, then returns nil once release
//	                 is closed, whatever its context
//	fixture.scoped   returns nil at once, recording the Call.Workspace it saw
type harness struct {
	ws      *Registry
	ops     *capability.Operations
	st      *store.Store
	log     *syncBuffer
	started chan string
	release chan struct{}
	ran     atomic.Int32
	saw     atomic.Value // string
	// stopped is when the stop-sidecars hook last ran: the end of a close's work.
	stopped atomic.Value // time.Time
}

func newHarness(t *testing.T, drain time.Duration) *harness {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return newHarnessOn(t, st, drain)
}

func newHarnessOn(t *testing.T, st *store.Store, drain time.Duration) *harness {
	t.Helper()
	h := &harness{st: st, log: &syncBuffer{}, started: make(chan string, 8), release: make(chan struct{})}
	reg := capability.NewRegistry()
	h.ops = capability.NewOperations(reg)
	h.ops.RegisterCapabilities()
	h.ws = New(Options{Store: st, Ops: h.ops, Drain: drain, Log: h.log,
		StopSidecars: func(string) { h.stopped.Store(time.Now()) }})
	Register(reg, h.ws)
	type none struct{}
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.honours", Kind: capability.Command, Scope: capability.Workspace,
		Description: "Runs until cancelled.", Params: none{}, Result: none{},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			h.started <- call.OperationID
			<-ctx.Done()
			return none{}, nil
		},
	})
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.ignores", Kind: capability.Command, Scope: capability.Workspace,
		Description: "Runs until released.", Params: none{}, Result: none{},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			h.started <- call.OperationID
			<-h.release
			return none{}, nil
		},
	})
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.scoped", Kind: capability.Query, Scope: capability.Workspace,
		Description: "Reads a workspace.", Params: none{}, Result: none{},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			h.ran.Add(1)
			h.saw.Store(call.Workspace)
			return none{}, nil
		},
	})
	t.Cleanup(func() {
		select {
		case <-h.release:
		default:
			close(h.release)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.ops.Shutdown(ctx)
	})
	return h
}

func (h *harness) invoke(t *testing.T, capName, target string) capability.Outcome {
	t.Helper()
	out, err := h.ops.Invoke(context.Background(), capability.Request{Capability: capName, Workspace: target})
	if err != nil {
		t.Fatalf("%s %s: rejected at Start: %v", capName, target, err)
	}
	return out
}

func (h *harness) open(t *testing.T, target string) Info {
	t.Helper()
	out := h.invoke(t, CapOpen, target)
	if out.State != capability.StateCompleted {
		t.Fatalf("open %s: %s %v", target, out.State, out.Error)
	}
	return out.Result.(Info)
}

// start starts a fixture in the workspace and returns its operation id once its
// handler runs.
func (h *harness) start(t *testing.T, fixture, target string) string {
	t.Helper()
	id, err := h.ops.Start(context.Background(), capability.Request{Capability: fixture, Workspace: target})
	if err != nil {
		t.Fatalf("%s in %s: %v", fixture, target, err)
	}
	select {
	case got := <-h.started:
		if got != id {
			t.Fatalf("started %s, want %s", got, id)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not start", fixture)
	}
	return id
}

func (h *harness) snapshot(t *testing.T, id string) capability.Outcome {
	t.Helper()
	out, err := h.ops.Wait(context.Background(), id, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *harness) waitEnd(t *testing.T, id string) capability.Outcome {
	t.Helper()
	out, err := h.ops.Wait(context.Background(), id, time.Now().Add(5*time.Second))
	if err != nil || !out.State.Terminal() {
		t.Fatalf("operation %s did not end within 5 s: %+v, %v", id, out, err)
	}
	return out
}

func codeOf(err error) capability.ErrorCode {
	if e, ok := err.(*capability.Error); ok {
		return e.Code
	}
	return ""
}

// project is a temporary project directory, by its canonical root.
func project(t *testing.T) string {
	t.Helper()
	return platform.Current().Canonical(t.TempDir())
}

func schemaVersionOf(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v.SchemaVersion
}

// TestOpenRegistersARoot: open answers ready and untrusted (ADR-0015 §5: nothing grants
// trust in 1c), names the workspace by the id rule, persists workspace.json and the
// index at schemaVersion 1, is a no-op on an open workspace, keeps createdAt across a
// close, and after a restart opens by an indexed id (ADR-0021 §Layout;
// workspace-state.md §Identity and reuse).
func TestOpenRegistersARoot(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	root := project(t)
	info := h.open(t, root)
	wantID, _ := platform.Current().WorkspaceID(root)
	if info.ID != wantID || info.Root != root || info.Name != filepath.Base(root) || info.State != Ready || info.Trusted || info.CreatedAt.IsZero() || info.DegradedBy != nil {
		t.Fatalf("open = %+v; want ready, untrusted, id %s", info, wantID)
	}
	for _, f := range []string{filepath.Join(h.st.Root(), "ws", wantID, "workspace.json"), filepath.Join(h.st.Root(), "workspaces.json")} {
		if v := schemaVersionOf(t, f); v != 1 {
			t.Errorf("%s: schemaVersion %d", f, v)
		}
	}
	if again := h.open(t, root); again != info {
		t.Errorf("a second open = %+v; want the same state %+v", again, info)
	}
	if out := h.invoke(t, CapClose, root); out.State != capability.StateCompleted {
		t.Fatalf("close: %+v", out)
	}
	if reopened := h.open(t, root); !reopened.CreatedAt.Equal(info.CreatedAt) {
		t.Errorf("reopening changed createdAt: %v → %v", info.CreatedAt, reopened.CreatedAt)
	}

	// A new daemon on the same state: the id resolves through the index.
	restarted := newHarnessOn(t, mustStore(t, h.st.Root()), 200*time.Millisecond)
	byID := restarted.open(t, wantID)
	if byID.Root != root || !byID.CreatedAt.Equal(info.CreatedAt) {
		t.Errorf("open by id after a restart = %+v", byID)
	}
}

func mustStore(t *testing.T, root string) *store.Store {
	t.Helper()
	st, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// TestOpenRunsNoProjectCode: a root whose gradlew would leave a marker is opened and
// the marker never appears (ADR-0015 §5; workspace-state.md §Lifecycle).
func TestOpenRunsNoProjectCode(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	root := project(t)
	marker := filepath.Join(root, "ran")
	script := "#!/bin/sh\ntouch '" + marker + "'\n"
	for _, f := range []string{"gradlew", "settings.gradle.kts"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.open(t, root)
	h.invoke(t, CapDescribe, root)
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("opening ran project code: %v", err)
	}
}

// TestOpenOfAnInvalidTargetFails: a missing path, a regular file, an unreadable
// directory and an id no index holds each create an operation that ends failed with
// WORKSPACE_ROOT_INVALID, details.target as given, and ADR-0026's nextStep; never a
// Start rejection, never REQUEST_INVALID_PARAMS.
func TestOpenOfAnInvalidTargetFails(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	os.WriteFile(file, []byte("x"), 0o600)
	locked := filepath.Join(dir, "locked")
	os.Mkdir(locked, 0o700)
	os.Chmod(locked, 0)
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	targets := map[string]string{
		"missing path":         filepath.Join(dir, "missing"),
		"regular file":         file,
		"id no index holds":    "nothing-0123456789ab",
		"relative path, an id": "some/dir",
	}
	if os.Getuid() != 0 {
		targets["unreadable directory"] = locked
	}
	for name, target := range targets {
		out := h.invoke(t, CapOpen, target)
		e := out.Error
		if out.OperationID == "" || out.State != capability.StateFailed || e == nil || e.Code != ErrRootInvalid ||
			e.Details["target"] != target || e.NextStep != "call `workspace.open` with the absolute path of a readable project directory" {
			t.Errorf("%s: %+v (%+v)", name, out, e)
		}
	}
}

// TestNewerWorkspaceFileDegrades: a workspace.json of schemaVersion 2 is never
// overwritten; the workspace opens degraded, and open and describe carry
// WORKSPACE_STATE_NEWER in degradedBy (ADR-0026; ADR-0021 §Format).
func TestNewerWorkspaceFileDegrades(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	root := project(t)
	id, _ := platform.Current().WorkspaceID(root)
	file := filepath.Join(h.st.Root(), "ws", id, "workspace.json")
	os.MkdirAll(filepath.Dir(file), 0o700)
	newer := []byte(`{"schemaVersion":2,"root":"` + root + `","future":1}`)
	os.WriteFile(file, newer, 0o600)
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	os.Chtimes(file, old, old)

	wantNext := "upgrade droidyard, or delete `" + filepath.Join(h.st.Root(), "ws", id) + "/` to reset this workspace"
	opened := h.open(t, root)
	described := h.invoke(t, CapDescribe, root)
	for name, info := range map[string]any{"open": opened, "describe": described.Result} {
		i, ok := info.(Info)
		if !ok || i.State != Degraded || i.DegradedBy == nil || i.DegradedBy.Code != ErrStateNewer || i.DegradedBy.NextStep != wantNext {
			t.Errorf("%s: %+v", name, info)
		}
	}
	raw, _ := os.ReadFile(file)
	fi, _ := os.Stat(file)
	if !bytes.Equal(raw, newer) || !fi.ModTime().Equal(old) {
		t.Errorf("the newer workspace.json changed: %q, mtime %v", raw, fi.ModTime())
	}
}

// TestStartAdmitsOnlyAnOpenWorkspace: a workspace-scoped call on a target that is not
// open is rejected at Start with WORKSPACE_NOT_OPEN, nextStep naming workspace.open,
// no operation and no handler run (ADR-0020 §Core primitives); once it is open, the
// call runs with the canonical id, whatever spelling named it.
func TestStartAdmitsOnlyAnOpenWorkspace(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	dir, err := os.MkdirTemp("/tmp", "wsadmit")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	id, _ := platform.Current().WorkspaceID(dir)
	for _, target := range []string{dir, id} {
		opID, err := h.ops.Start(context.Background(), capability.Request{Capability: "fixture.scoped", Workspace: target})
		e, _ := err.(*capability.Error)
		if opID != "" || codeOf(err) != capability.ErrWorkspaceNotOpen || !strings.Contains(e.NextStep, "workspace.open") {
			t.Errorf("%s: Start = %q, %v", target, opID, err)
		}
	}
	if h.ran.Load() != 0 || h.ops.Running() != 0 {
		t.Fatalf("a rejected call ran (%d) or left an operation (%d)", h.ran.Load(), h.ops.Running())
	}

	h.open(t, dir)
	for _, spelling := range []string{dir, platform.Current().Canonical(dir), id} {
		out := h.invoke(t, "fixture.scoped", spelling)
		if out.State != capability.StateCompleted || h.saw.Load() != id {
			t.Errorf("%s: %+v, the handler saw workspace %v; want %s", spelling, out, h.saw.Load(), id)
		}
	}
}

// TestCloseWithNothingRunningIsImmediate: no operation to drain, no wait.
func TestCloseWithNothingRunningIsImmediate(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	root := project(t)
	h.open(t, root)
	began := time.Now()
	out := h.invoke(t, CapClose, root)
	if took := time.Since(began); out.State != capability.StateCompleted || took >= 50*time.Millisecond {
		t.Errorf("close: %s in %s; want completed in under 50 ms", out.State, took)
	}
	if s := h.ws.State(root); s != Closed {
		t.Errorf("state %s after close", s)
	}
	if _, err := h.ops.Start(context.Background(), capability.Request{Capability: CapDescribe, Workspace: root}); codeOf(err) != capability.ErrWorkspaceNotOpen {
		t.Errorf("describe after close: %v; want WORKSPACE_NOT_OPEN", err)
	}
	if out := h.invoke(t, CapClose, root); out.State != capability.StateCompleted {
		t.Errorf("close of a closed workspace: %+v", out)
	}
	if out := h.invoke(t, CapClose, project(t)); out.State != capability.StateCompleted {
		t.Errorf("close of a root never opened: %+v", out)
	}
}

// TestCloseCancelsItsWorkspacesOperations: close returns only after an operation that
// honours cancellation has ended cancelled, and leaves another workspace's running
// (ADR-0027).
func TestCloseCancelsItsWorkspacesOperations(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	a, b := project(t), project(t)
	h.open(t, a)
	h.open(t, b)
	inA := h.start(t, "fixture.honours", a)
	inB := h.start(t, "fixture.honours", b)

	if out := h.invoke(t, CapClose, a); out.State != capability.StateCompleted {
		t.Fatalf("close: %+v", out)
	}
	got := h.snapshot(t, inA)
	if got.State != capability.StateCancelled || got.Error == nil || got.Error.Code != capability.ErrOperationCancelled {
		t.Errorf("when close returned, its workspace's operation was %+v; want cancelled", got)
	}
	if other := h.snapshot(t, inB); other.State != capability.StateRunning {
		t.Errorf("another workspace's operation: %s; want running", other.State)
	}
}

// TestCloseBoundsItsDrain: an operation that ignores cancellation holds close for the
// drain bound only; close completes with the workspace closed and the operation
// logged, still running; released, it ends cancelled although its handler returned
// nil. operation.cancel on the close itself does not stop it: it ends completed
// (ADR-0027; plan m1c, R15).
func TestCloseBoundsItsDrain(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	root := project(t)
	h.open(t, root)
	stuck := h.start(t, "fixture.ignores", root)

	began := time.Now()
	closeID, err := h.ops.Start(context.Background(), capability.Request{Capability: CapClose, Workspace: root})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, h.ws, root, Closing)
	if _, err := h.ops.Invoke(context.Background(), capability.Request{Capability: capability.OperationCancel,
		Params: json.RawMessage(`{"operationId":"` + closeID + `"}`)}); err != nil {
		t.Fatal(err)
	}
	closed := h.waitEnd(t, closeID)
	took := time.Since(began)
	if closed.State != capability.StateCompleted || took < 200*time.Millisecond || took > time.Second {
		t.Fatalf("close: %s after %s; want completed between 200 ms and 1 s", closed.State, took)
	}
	if s := h.ws.State(root); s != Closed {
		t.Errorf("state %s when close returned", s)
	}
	if stopped, _ := h.stopped.Load().(time.Time); stopped.Sub(began) < 200*time.Millisecond {
		t.Errorf("the stop-sidecars hook ran %s after close began, before the drain ended", stopped.Sub(began))
	}
	if !strings.Contains(h.log.String(), stuck) {
		t.Errorf("no log line names %s: %q", stuck, h.log.String())
	}
	if s := h.snapshot(t, stuck); s.State != capability.StateRunning {
		t.Errorf("the ignoring operation is %s when close returned; want still running", s.State)
	}
	close(h.release)
	if end := h.waitEnd(t, stuck); end.State != capability.StateCancelled || end.Error.Code != capability.ErrOperationCancelled {
		t.Errorf("released, the operation ended %+v; want cancelled", end)
	}
}

// closing starts a close of root behind an operation that ignores cancellation, and
// returns once the workspace is closing, with a channel for the close's end.
func (h *harness) closing(t *testing.T, root string) chan ended {
	t.Helper()
	h.start(t, "fixture.ignores", root)
	first := h.run(CapClose, root)
	waitState(t, h.ws, root, Closing)
	return first
}

type ended struct {
	out capability.Outcome
	at  time.Time
}

// recv bounds every wait on a channel, so a reversed decision fails the test rather
// than hanging it.
func recv[T any](t *testing.T, ch <-chan T, what string) T {
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

func (h *harness) run(capName, target string) chan ended {
	ch := make(chan ended, 1)
	go func() {
		out, _ := h.ops.Invoke(context.Background(), capability.Request{Capability: capName, Workspace: target})
		ch <- ended{out, time.Now()}
	}()
	return ch
}

// TestOpenDuringCloseWaitsThenOpens: while a close drains, other workspace-scoped
// calls are rejected at Start, and an open waits for closed, then opens (ADR-0027).
func TestOpenDuringCloseWaitsThenOpens(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	root := project(t)
	h.open(t, root)
	first := h.closing(t, root)
	if _, err := h.ops.Start(context.Background(), capability.Request{Capability: "fixture.scoped", Workspace: root}); codeOf(err) != capability.ErrWorkspaceNotOpen {
		t.Errorf("a call during closing: %v; want WORKSPACE_NOT_OPEN", err)
	}
	reopen := h.run(CapOpen, root)
	time.Sleep(100 * time.Millisecond)
	select {
	case e := <-reopen:
		t.Fatalf("open returned during the drain: %+v", e.out)
	default:
	}
	c, o := recv(t, first, "close"), recv(t, reopen, "open")
	if c.out.State != capability.StateCompleted {
		t.Errorf("close: %s", c.out.State)
	}
	stopped := h.stopped.Load().(time.Time)
	if o.out.State != capability.StateCompleted || o.out.Result.(Info).State != Ready || o.at.Before(stopped) {
		t.Errorf("open: %+v at %v; want ready, after the close finished at %v", o.out, o.at, stopped)
	}
}

// TestSecondCloseWaitsAndCompletes: a close during a close waits for the first and
// completes as a no-op (ADR-0027).
func TestSecondCloseWaitsAndCompletes(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	root := project(t)
	h.open(t, root)
	first := h.closing(t, root)
	second := h.run(CapClose, root)
	time.Sleep(100 * time.Millisecond)
	select {
	case e := <-second:
		t.Fatalf("the second close returned during the drain: %+v", e.out)
	default:
	}
	c1, c2 := recv(t, first, "first close"), recv(t, second, "second close")
	stopped := h.stopped.Load().(time.Time)
	if c1.out.State != capability.StateCompleted || c2.out.State != capability.StateCompleted || c2.at.Before(stopped) {
		t.Errorf("closes: %s, %s at %v; want both completed, the second after the first finished at %v", c1.out.State, c2.out.State, c2.at, stopped)
	}
	if took := c2.at.Sub(c1.at); took > 50*time.Millisecond {
		t.Errorf("the second close drained again (%s after the first): it was not a no-op", took)
	}
	if s := h.ws.State(root); s != Closed {
		t.Errorf("state %s", s)
	}
}

func waitState(t *testing.T, ws *Registry, target string, want State) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ws.State(target) != want; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("state %s, want %s", ws.State(target), want)
		}
	}
}

// heldAdmit is the test hook between Start's open check and its recording: it calls
// the real Admit, whose record it holds until release.
type heldAdmit struct {
	real          *Registry
	held, release chan struct{}
}

func (h heldAdmit) Admit(target string, record func(id string)) bool {
	return h.real.Admit(target, func(id string) {
		close(h.held)
		<-h.release
		record(id)
	})
}

// TestStartsCheckAndRecordShareCloseLock: an operation admitted by Start's open check
// is always one the close cancels, because the check, the recording and close's entry
// into closing share one lock (plan m1c, B1). Start is held after its check and before
// its recording; close cannot enter closing meanwhile; released, the operation is
// recorded, close cancels it, and close returns after it has ended cancelled.
func TestStartsCheckAndRecordShareCloseLock(t *testing.T) {
	h := newHarness(t, 2*time.Second)
	root := project(t)
	h.open(t, root)
	hook := heldAdmit{real: h.ws, held: make(chan struct{}), release: make(chan struct{})}
	h.ops.UseWorkspaces(hook)

	started := make(chan string, 1)
	go func() {
		id, err := h.ops.Start(context.Background(), capability.Request{Capability: "fixture.honours", Workspace: root})
		if err != nil {
			t.Errorf("Start: %v", err)
		}
		started <- id
	}()
	recv(t, hook.held, "Start at the hook")
	closed := make(chan time.Time, 1)
	go func() {
		h.ops.Invoke(context.Background(), capability.Request{Capability: CapClose, Workspace: root})
		closed <- time.Now()
	}()
	for until := time.Now().Add(200 * time.Millisecond); time.Now().Before(until); time.Sleep(5 * time.Millisecond) {
		if s := h.ws.State(root); s == Closing || s == Closed {
			t.Fatalf("close entered %s while Start held the admission lock", s)
		}
		select {
		case <-closed:
			t.Fatal("close returned while Start held the admission lock")
		default:
		}
	}
	close(hook.release)
	id := recv(t, started, "Start")
	at := recv(t, closed, "close")
	out := h.snapshot(t, id)
	if out.State != capability.StateCancelled || out.Error == nil || out.Error.Code != capability.ErrOperationCancelled {
		t.Errorf("the admitted operation, when close returned at %v: %+v; want cancelled", at, out)
	}
}

// TestNewerIndexIsNeitherReadNorWritten: with a workspaces.json of schemaVersion 2,
// open by root works and skips the index, open by an id this daemon has not opened
// fails WORKSPACE_ROOT_INVALID, the file is untouched and recorded, and no call gets
// REQUEST_INVALID_PARAMS (plan m1c, R16; ADR-0021 §Format).
func TestNewerIndexIsNeitherReadNorWritten(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	file := filepath.Join(h.st.Root(), "workspaces.json")
	newer := []byte(`{"schemaVersion":2,"workspaces":{"x-0123456789ab":{"root":"/nowhere"}}}`)
	os.WriteFile(file, newer, 0o600)

	root := project(t)
	byRoot := h.invoke(t, CapOpen, root)
	byID := h.invoke(t, CapOpen, "x-0123456789ab")
	if byRoot.State != capability.StateCompleted || byRoot.Result.(Info).State != Ready {
		t.Errorf("open by root: %+v %+v", byRoot, byRoot.Error)
	}
	if byID.State != capability.StateFailed || byID.Error.Code != ErrRootInvalid {
		t.Errorf("open by an id only the newer index holds: %+v %+v", byID, byID.Error)
	}
	if raw, _ := os.ReadFile(file); !bytes.Equal(raw, newer) {
		t.Errorf("the newer index changed: %q", raw)
	}
	found := false
	for _, p := range h.st.Problems() {
		found = found || (p.File == "workspaces.json" && p.Problem == store.ProblemNewerVersion)
	}
	if !found {
		t.Errorf("problems: %+v", h.st.Problems())
	}
	id, _ := platform.Current().WorkspaceID(root)
	if again := h.invoke(t, CapOpen, id); again.State != capability.StateCompleted {
		t.Errorf("open by the id of a workspace this daemon opened: %+v", again.Error)
	}
}

// TestProductionDrainIs10s: the bound wired when none is given is ADR-0027's.
func TestProductionDrainIs10s(t *testing.T) {
	if d := New(Options{}).Drain(); d != 10*time.Second {
		t.Errorf("drain %s", d)
	}
}
