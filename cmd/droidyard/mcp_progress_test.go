package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// progressFixture is fixture.progress, test-only and long-running: it reports each of
// its reports of total once the test releases it, and with hold waits for one more
// release between its last report and its return.
type progressFixture struct {
	release  chan struct{}
	reported chan int // each report's done, once Progress has returned
	started  chan string
}

type progressParams struct {
	Reports []int `json:"reports"`
	Total   int   `json:"total"`
	Hold    bool  `json:"hold,omitempty"`
}

func (f *progressFixture) run(ctx context.Context, call *capability.Call) (any, error) {
	p := call.Params.(progressParams)
	f.started <- call.OperationID
	wait := func() error {
		select {
		case <-f.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for _, r := range p.Reports {
		if err := wait(); err != nil {
			return nil, err
		}
		call.Progress(r, p.Total, "")
		f.reported <- r
	}
	if p.Hold {
		if err := wait(); err != nil {
			return nil, err
		}
	}
	return struct{}{}, nil
}

func startProgress(t *testing.T) (*inProcess, *progressFixture) {
	t.Helper()
	f := &progressFixture{release: make(chan struct{}), reported: make(chan int, 16), started: make(chan string, 4)}
	p := startInProcessWith(t, func(reg *capability.Registry) {
		reg.MustRegister(capability.Descriptor{
			Name: "fixture.progress", Kind: capability.Command, Scope: capability.Global, LongRunning: true,
			Description: "Reports progress when released, then completes.", Params: progressParams{}, Result: struct{}{},
			Handler: f.run,
		})
	})
	return p, f
}

// next releases the fixture's next step and, for a report, waits until it was made.
func (f *progressFixture) next(t *testing.T, report int) {
	t.Helper()
	select {
	case f.release <- struct{}{}:
	case <-time.After(10 * time.Second):
		t.Fatal("the fixture never took its release")
	}
	if report == 0 {
		return
	}
	select {
	case got := <-f.reported:
		if got != report {
			t.Fatalf("the fixture reported %d; want %d", got, report)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the fixture never reported %d", report)
	}
}

func (f *progressFixture) waitStarted(t *testing.T) string {
	t.Helper()
	select {
	case id := <-f.started:
		return id
	case <-time.After(10 * time.Second):
		t.Fatal("fixture.progress never started")
	}
	return ""
}

func (s *shim) callProgress(id int, token, args string) {
	meta := ""
	if token != "" {
		meta = `,"_meta":{"progressToken":"` + token + `"}`
	}
	s.send(`{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"tools/call","params":{"name":"fixture_progress","arguments":` + args + meta + `}}`)
}

// progressLine is a line the shim prints: a notifications/progress, or a response.
type progressLine struct {
	ID     *int   `json:"id"`
	Method string `json:"method"`
	Params struct {
		ProgressToken any     `json:"progressToken"`
		Progress      float64 `json:"progress"`
		Total         float64 `json:"total"`
	} `json:"params"`
	Result struct {
		StructuredContent capability.OperationView `json:"structuredContent"`
		IsError           bool                     `json:"isError"`
	} `json:"result"`
}

// untilAnswer reads s's lines, waiting at most within for each, until the response to
// id; it returns the notifications/progress before it, in order, and the response.
func untilAnswer(t *testing.T, s *shim, id int, within time.Duration) ([]progressLine, progressLine) {
	t.Helper()
	var notes []progressLine
	for {
		var raw []byte
		select {
		case line, ok := <-s.lines:
			if !ok {
				t.Fatalf("%s: the shim exited; stderr: %s", s.name, s.stderr)
			}
			raw = line
		case <-time.After(within):
			t.Fatalf("%s: no answer to %d within %s", s.name, id, within)
		}
		t.Logf("%s ← %s", s.name, raw)
		var l progressLine
		if err := json.Unmarshal(raw, &l); err != nil {
			t.Fatalf("%s: not JSON-RPC: %s", s.name, raw)
		}
		switch {
		case l.ID != nil && *l.ID == id:
			return notes, l
		case l.Method == "notifications/progress":
			notes = append(notes, l)
		default:
			t.Fatalf("%s: unexpected line %s", s.name, raw)
		}
	}
}

// progressOf is each notification's progress, failing on one for another token or
// with another total.
func progressOf(t *testing.T, what string, notes []progressLine, token string, total float64) []int {
	t.Helper()
	var got []int
	for _, n := range notes {
		if n.Params.ProgressToken != token || n.Params.Total != total {
			t.Errorf("%s: a notification for token %v, total %v; want %q, %v", what, n.Params.ProgressToken, n.Params.Total, token, total)
		}
		got = append(got, int(n.Params.Progress))
	}
	return got
}

func wantProgress(t *testing.T, what string, got []int, want ...int) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: progress %v before the answer; want %v", what, got, want)
	}
}

// TestMCPProgress: a call that carries a progressToken receives its operation's
// progress as notifications/progress while it waits, strictly increasing, the last
// report included, all before its answer and none after; without a token none; each
// session only its own (ADR-0020 §Adapter projections; mcp-features.md §7; plan m1d,
// step 3). Re-executed shims on an in-process daemon; run -count=20 under -race.
func TestMCPProgress(t *testing.T) {
	t.Parallel()
	t.Run("a: reports before the answer, none after", func(t *testing.T) {
		p, f := startProgress(t)
		// The adapter is held between Start and its subscription while report 1 is
		// made: subscribing from the position taken before Start replays it.
		held, unhold := make(chan struct{}), make(chan struct{})
		var once sync.Once
		p.mcp.SetSubscribeHook(func() {
			once.Do(func() { close(held) })
			<-unhold
		})
		t.Cleanup(func() { p.mcp.SetSubscribeHook(nil) })
		c := startShim(t, p.state, "a", "--wait", "60s")
		c.initialize("progress")
		c.callProgress(1, "t-a", `{"reports":[1,2,3],"total":3}`)
		select {
		case <-held:
		case <-time.After(10 * time.Second):
			t.Fatal("the call never reached the subscribe hook")
		}
		f.next(t, 1)
		close(unhold)

		var got []int
		for _, report := range []int{2, 3} {
			l := c.recv()
			var n progressLine
			json.Unmarshal(l, &n)
			if n.Method != "notifications/progress" {
				t.Fatalf("before report %d the shim printed %s; want progress", report, l)
			}
			got = append(got, progressOf(t, "a", []progressLine{n}, "t-a", 3)...)
			f.next(t, report)
		}
		notes, answer := untilAnswer(t, c, 1, 10*time.Second)
		got = append(got, progressOf(t, "a", notes, "t-a", 3)...)
		wantProgress(t, "a", got, 1, 2, 3)
		if v := answer.Result.StructuredContent; answer.Result.IsError || v.State != capability.StateCompleted {
			t.Errorf("the answer is %+v; want the completed view", answer.Result)
		}
		c.silent(300 * time.Millisecond)
	})

	t.Run("a′: every report still queued when the wait ends", func(t *testing.T) {
		p, f := startProgress(t)
		var holds atomic.Int32
		p.mcp.SetForwardHook(func(waitEnded <-chan struct{}) {
			holds.Add(1)
			<-waitEnded
		})
		t.Cleanup(func() { p.mcp.SetForwardHook(nil) })
		c := startShim(t, p.state, "a′", "--wait", "60s")
		c.initialize("progress")
		c.callProgress(1, "t-a", `{"reports":[1,2,3],"total":3}`)
		f.waitStarted(t)
		f.next(t, 1)
		f.next(t, 2)
		f.next(t, 3)
		notes, _ := untilAnswer(t, c, 1, 10*time.Second)
		wantProgress(t, "a′", progressOf(t, "a′", notes, "t-a", 3), 1, 2, 3)
		if holds.Load() != 1 {
			t.Errorf("the forwarder was held %d times; want once", holds.Load())
		}
		c.silent(300 * time.Millisecond)
	})

	t.Run("a″: the last seq before the end is on another topic", func(t *testing.T) {
		p, f := startProgress(t)
		c := startShim(t, p.state, "a″", "--wait", "60s")
		c.initialize("progress")
		c.callProgress(1, "t-a", `{"reports":[1,2,3],"total":3,"hold":true}`)
		f.waitStarted(t)
		f.next(t, 1)
		f.next(t, 2)
		f.next(t, 3)
		before := p.bus.Position()
		root := platform.Current().Canonical(t.TempDir())
		if out, err := p.ops.Invoke(context.Background(), capability.Request{Capability: "workspace.open", Workspace: root, Client: capability.ClientCLI}); err != nil || out.State != capability.StateCompleted {
			t.Fatalf("workspace.open: %+v, %v", out, err)
		}
		if after := p.bus.Position(); after.Seq < before.Seq+2 {
			t.Fatalf("workspace.open moved the bus from %d to %d; want its opening and ready after report 3", before.Seq, after.Seq)
		}
		released := time.Now()
		f.next(t, 0)
		notes, answer := untilAnswer(t, c, 1, 5*time.Second)
		if d := time.Since(released); d > time.Second {
			t.Errorf("the answer came %s after the release; want within 1 s", d)
		}
		wantProgress(t, "a″", progressOf(t, "a″", notes, "t-a", 3), 1, 2, 3)
		if answer.Result.StructuredContent.State != capability.StateCompleted {
			t.Errorf("the answer is %+v; want the completed view", answer.Result)
		}
	})

	t.Run("b: no token, no notification", func(t *testing.T) {
		p, f := startProgress(t)
		c := startShim(t, p.state, "b", "--wait", "60s")
		c.initialize("progress")
		c.callProgress(1, "", `{"reports":[1,2,3],"total":3}`)
		f.waitStarted(t)
		close(f.release)
		notes, _ := untilAnswer(t, c, 1, 10*time.Second)
		if len(notes) != 0 {
			t.Errorf("a call without a token received %d progress notifications", len(notes))
		}
		c.silent(300 * time.Millisecond)
	})

	t.Run("c: operation_wait forwards the progress made during its wait", func(t *testing.T) {
		p, f := startProgress(t)
		c := startShim(t, p.state, "c", "--wait", "200ms")
		c.initialize("progress")
		c.callProgress(1, "", `{"reports":[1,2,3],"total":3}`)
		_, running := untilAnswer(t, c, 1, 10*time.Second)
		v := running.Result.StructuredContent
		if v.State != capability.StateRunning || v.OperationID == "" {
			t.Fatalf("after 200 ms the call answered %+v; want the running view", running.Result)
		}
		waiting := make(chan struct{}, 1)
		p.mcp.SetSubscribeHook(func() { waiting <- struct{}{} })
		t.Cleanup(func() { p.mcp.SetSubscribeHook(nil) })
		c.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"operation_wait","arguments":{"operationId":"` + v.OperationID + `"},"_meta":{"progressToken":"t-w"}}}`)
		select {
		case <-waiting:
		case <-time.After(10 * time.Second):
			t.Fatal("operation_wait never reached the subscribe hook")
		}
		close(f.release)
		notes, answer := untilAnswer(t, c, 2, 10*time.Second)
		wantProgress(t, "c", progressOf(t, "c", notes, "t-w", 3), 1, 2, 3)
		if answer.Result.StructuredContent.State != capability.StateCompleted {
			t.Errorf("operation_wait answered %+v; want the completed view", answer.Result)
		}
		c.silent(300 * time.Millisecond)
	})

	t.Run("d: progress only increases", func(t *testing.T) {
		p, f := startProgress(t)
		c := startShim(t, p.state, "d", "--wait", "60s")
		c.initialize("progress")
		c.callProgress(1, "t-d", `{"reports":[2,2,3],"total":3}`)
		f.waitStarted(t)
		close(f.release)
		notes, _ := untilAnswer(t, c, 1, 10*time.Second)
		wantProgress(t, "d", progressOf(t, "d", notes, "t-d", 3), 2, 3)
	})

	t.Run("e: each session only its own", func(t *testing.T) {
		p, f := startProgress(t)
		a := startShim(t, p.state, "a", "--wait", "60s")
		b := startShim(t, p.state, "b", "--wait", "60s")
		a.initialize("a")
		b.initialize("b")
		a.callProgress(1, "t-a", `{"reports":[1,2,3],"total":3}`)
		b.callProgress(1, "t-b", `{"reports":[10,20,30],"total":30}`)
		f.waitStarted(t)
		f.waitStarted(t)
		close(f.release)
		for _, c := range []struct {
			s     *shim
			token string
			total float64
			want  []int
		}{{a, "t-a", 3, []int{1, 2, 3}}, {b, "t-b", 30, []int{10, 20, 30}}} {
			notes, _ := untilAnswer(t, c.s, 1, 10*time.Second)
			what := fmt.Sprintf("e, session %s", c.s.name)
			wantProgress(t, what, progressOf(t, what, notes, c.token, c.total), c.want...)
		}
	})
}
