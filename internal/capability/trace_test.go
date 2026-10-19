package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// The capability trace (plan m2a, Q3; ADR-0020 §Core primitives, "one record type gives
// one trace format"): one line per operation, written once when it reaches a terminal
// state, naming who asked and never what was asked or answered.

const (
	sentinelParam   = "PARAM-SENTINEL"
	sentinelResult  = "RESULT-SENTINEL"
	sentinelMessage = "MESSAGE-SENTINEL"
	sentinelCause   = "CAUSE-SENTINEL"
	sentinelDetail  = "DETAIL-SENTINEL"
)

type traceParams struct {
	Secret string `json:"secret,omitempty"`
}
type traceResult struct {
	Secret string `json:"secret"`
}

// traceRecorder is the injected logger. It calls back into the store before it takes its
// own lock, so a line written while the store's mutex is held deadlocks the test.
type traceRecorder struct {
	ops   *Operations
	mu    sync.Mutex
	lines []string
}

func (r *traceRecorder) logf(format string, v ...any) {
	r.ops.Running()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, v...))
}

func (r *traceRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

// await waits for n lines: Wait returns once an operation is terminal, a moment before
// its line is written (the line is written after the store's lock is released).
func (r *traceRecorder) await(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(r.snapshot()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d trace lines after 5 s; want %d: %q", len(r.snapshot()), n, r.snapshot())
		}
		time.Sleep(time.Millisecond)
	}
}

func traceStore(t *testing.T) (*Operations, *traceRecorder, chan struct{}) {
	t.Helper()
	started := make(chan struct{}, 16)
	reg := NewRegistry()
	block := func(ctx context.Context, call *Call) (any, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	reg.MustRegister(Descriptor{
		Name: "trace.ok", Kind: Command, Scope: Global, Description: "Succeeds.",
		Params: traceParams{}, Result: traceResult{},
		Handler: func(context.Context, *Call) (any, error) { return traceResult{Secret: sentinelResult}, nil },
	})
	reg.MustRegister(Descriptor{
		Name: "trace.scoped", Kind: Command, Scope: Workspace, Description: "Succeeds in a workspace.",
		Params: traceParams{}, Result: traceResult{},
		Handler: func(context.Context, *Call) (any, error) { return traceResult{Secret: sentinelResult}, nil },
	})
	reg.MustRegister(Descriptor{
		Name: "trace.fail", Kind: Command, Scope: Global, Description: "Fails.",
		Params: traceParams{}, Result: traceResult{},
		Handler: func(context.Context, *Call) (any, error) {
			return nil, Errorf("TRACE_BROKEN", "%s", sentinelMessage).WithCause(sentinelCause).
				WithDetails(map[string]any{"d": sentinelDetail})
		},
	})
	reg.MustRegister(Descriptor{
		Name: "trace.block", Kind: Command, Scope: Global, Description: "Blocks until cancelled.",
		Params: traceParams{}, Result: traceResult{}, Handler: block,
	})
	reg.MustRegister(Descriptor{
		Name: "trace.scopedblock", Kind: Command, Scope: Workspace, Description: "Blocks in a workspace until cancelled.",
		Params: traceParams{}, Result: traceResult{}, Handler: block,
	})
	reg.MustRegister(Descriptor{
		Name: "trace.held", Kind: Command, Scope: Global, Description: "Holds one key until cancelled.",
		Params: traceParams{}, Result: traceResult{}, Errors: []ErrorCode{ErrOperationQueueTimeout},
		Resource: func(*Call) string { return "k" },
		Handler: func(ctx context.Context, call *Call) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	ops := NewOperations(reg)
	ops.RegisterCapabilities()
	ops.UseWorkspaces(admitAs("ws-canon"))
	ops.queueBound = 100 * time.Millisecond
	rec := &traceRecorder{ops: ops}
	ops.UseTrace(rec.logf)
	return ops, rec, started
}

// TestEveryOperationIsTracedOnce is the decision of Q3: whatever path ends an operation
// (completed, failed, cancelled while running or while queued, a workspace close, a
// shutdown, a queue timeout), whatever client made the call, exactly one line of the
// documented shape is written, naming the client kind, and nothing the caller sent or
// the handler returned reaches it. operation.wait and operation.cancel are not operations
// and write none. The recorder calls back into the store, so the line is written with
// the store's lock released.
func TestEveryOperationIsTracedOnce(t *testing.T) {
	ops, rec, started := traceStore(t)
	ctx := context.Background()
	type want struct{ re *regexp.Regexp }
	wants := map[string]want{}
	expect := func(id string, client ClientKind, capability, ws, state, code string) {
		s := fmt.Sprintf(`^droidyard: op %s client=%s capability=%s workspace=%s state=%s ms=\d+`, id, client, regexp.QuoteMeta(capability), ws, state)
		if code != "" {
			s += " code=" + code
		}
		wants[id] = want{regexp.MustCompile(s + "$")}
		// The line is written by the call that ended the operation, not by whichever
		// call ends another one later.
		rec.await(t, len(wants))
	}
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
	}

	for _, client := range []ClientKind{ClientCLI, ClientMCP, ClientUI} {
		// completed, global and in a workspace
		id := start(client, "trace.ok", "")
		wait(id, StateCompleted)
		expect(id, client, "trace.ok", "-", "completed", "")
		id = start(client, "trace.scoped", "/some/root")
		wait(id, StateCompleted)
		expect(id, client, "trace.scoped", "ws-canon", "completed", "")

		// Invoke is Start and Wait: still one line
		out, err := ops.Invoke(ctx, Request{Capability: "trace.ok", Client: client})
		if err != nil || out.State != StateCompleted {
			t.Fatalf("Invoke: %+v, %v", out, err)
		}
		expect(out.OperationID, client, "trace.ok", "-", "completed", "")

		// failed: its code, and nothing of its message, cause or details
		id = start(client, "trace.fail", "")
		wait(id, StateFailed)
		expect(id, client, "trace.fail", "-", "failed", "TRACE_BROKEN")

		// cancelled while running
		id = start(client, "trace.block", "")
		<-started
		if err := ops.Cancel(id); err != nil {
			t.Fatal(err)
		}
		wait(id, StateCancelled)
		expect(id, client, "trace.block", "-", "cancelled", "")

		// cancelled while queued, then the holder it queued behind
		holder := start(client, "trace.held", "")
		queued := start(client, "trace.held", "")
		if err := ops.Cancel(queued); err != nil {
			t.Fatal(err)
		}
		wait(queued, StateCancelled)
		expect(queued, client, "trace.held", "-", "cancelled", "")

		// a queue timeout: failed, with its code
		late := start(client, "trace.held", "")
		wait(late, StateFailed)
		expect(late, client, "trace.held", "-", "failed", string(ErrOperationQueueTimeout))
		if err := ops.Cancel(holder); err != nil {
			t.Fatal(err)
		}
		wait(holder, StateCancelled)
		expect(holder, client, "trace.held", "-", "cancelled", "")

		// cancelled by a workspace close
		id = start(client, "trace.scopedblock", "/some/root")
		<-started
		if got := ops.CancelWorkspace("ws-canon"); len(got) != 1 || got[0] != id {
			t.Fatalf("CancelWorkspace cancelled %v; want [%s]", got, id)
		}
		wait(id, StateCancelled)
		expect(id, client, "trace.scopedblock", "ws-canon", "cancelled", "")

		// cancelled by a shutdown
		id = start(client, "trace.block", "")
		<-started
		if err := ops.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		wait(id, StateCancelled)
		expect(id, client, "trace.block", "-", "cancelled", "")

		// operation.wait and operation.cancel are not operations: no line of their own
		args := json.RawMessage(`{"operationId":"` + id + `"}`)
		for _, name := range []string{"operation.wait", "operation.cancel"} {
			if _, err := ops.Invoke(ctx, Request{Capability: name, Params: args, Client: ClientCLI}); err != nil { // the CLI reaches any operation (ADR-0101)
				t.Fatalf("%s: %v", name, err)
			}
		}
	}

	rec.await(t, len(wants))
	time.Sleep(100 * time.Millisecond) // a second line for the same operation would be late
	lines := rec.snapshot()
	if len(lines) != len(wants) {
		t.Fatalf("%d trace lines for %d operations:\n%s", len(lines), len(wants), strings.Join(lines, "\n"))
	}
	seen := map[string]bool{}
	for _, line := range lines {
		for _, s := range []string{sentinelParam, sentinelResult, sentinelMessage, sentinelCause, sentinelDetail} {
			if strings.Contains(line, s) {
				t.Errorf("the trace line carries request or device data %q: %s", s, line)
			}
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			t.Fatalf("malformed trace line %q", line)
		}
		id := fields[2]
		w, ok := wants[id]
		if !ok {
			t.Errorf("a trace line for an operation the test did not make, or for operation.wait/cancel: %s", line)
			continue
		}
		if seen[id] {
			t.Errorf("operation %s traced twice: %s", id, line)
		}
		seen[id] = true
		if !w.re.MatchString(line) {
			t.Errorf("trace line %q does not match %s", line, w.re)
		}
	}
	if len(seen) != len(wants) {
		t.Errorf("%d operations traced; want %d", len(seen), len(wants))
	}
}

// TestTraceMsIsTheOperationsDuration: ms is the time from the operation's record to its
// terminal state, on the store's clock, so a slow operation reads as slow.
func TestTraceMsIsTheOperationsDuration(t *testing.T) {
	f := newFixture(t)
	rec := &traceRecorder{ops: f.ops}
	f.ops.UseTrace(rec.logf)
	id := f.startBlocking(t)
	f.clock.advance(1500 * time.Millisecond)
	if err := f.ops.Cancel(id); err != nil {
		t.Fatal(err)
	}
	rec.await(t, 1)
	want := "droidyard: op " + id + " client=- capability=fixture.block workspace=- state=cancelled ms=1500"
	if got := rec.snapshot()[0]; got != want {
		t.Errorf("trace line %q; want %q (a call with no client kind shows it as -)", got, want)
	}
}
