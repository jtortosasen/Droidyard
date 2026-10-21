package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// TestOperationWatchNeedsASession: operation.watch is streaming, so through plain HTTP and
// the core's MCP client kind it is SESSION_REQUIRED at Start, naming GET /v1/ws, with no
// operation (ADR-0043 §1; ADR-0010). A CLI connection carries streams (ADR-0122), so
// the call succeeds there. The MCP adapter lists no operation_watch tool (TestMCPTranscript
// counts the tools).
func TestOperationWatchNeedsASession(t *testing.T) {
	t.Parallel()
	p, _ := startHTTP(t, "127.0.0.1:0")
	port := portOf(t, p.d.HTTPAddr())
	want := func(what string, env capability.ResponseEnvelope) {
		t.Helper()
		if env.Error == nil || env.Error.Code != capability.ErrSessionRequired || env.Operation != nil || env.Result != nil ||
			!strings.Contains(env.Error.NextStep, "GET /v1/ws") {
			t.Errorf("%s: %+v (error %+v); want SESSION_REQUIRED naming GET /v1/ws, no operation, no result", what, env, env.Error)
		}
	}

	resp := callRequest(port, p.token, `{"id":"w","capability":"operation.watch"}`).send(t, p.d.HTTPAddr())
	if resp.StatusCode != 200 {
		t.Errorf("POST /v1/call operation.watch: %d; want 200 with an envelope", resp.StatusCode)
	}
	want("POST /v1/call operation.watch", envelopeOf(t, resp))

	f := p.follow(t, "operation.watch")
	f.opened(t)
	f.kill(t)

	out, err := p.ops.Invoke(context.Background(), capability.Request{Capability: "operation.watch", Client: capability.ClientMCP})
	var e *capability.Error
	if !errors.As(err, &e) || e.Code != capability.ErrSessionRequired || out.OperationID != "" {
		t.Errorf("operation.watch as client mcp: (%+v, %v); want SESSION_REQUIRED", out, err)
	}
	// The refused calls left none, and the CLI's was released with its connection.
	waitSubscriptions(t, p, 0)
}

// TestWSOperationWatchSeesAnMCPCall is the promise of ADR-0043: a WebSocket session that
// watches is told, as it happens, of an operation another client makes through the shared
// daemon. An MCP session calls system_health; the WebSocket receives running and then
// completed for it, naming client mcp, under the operation id the daemon's trace line
// carries, and nothing the call sent or returned.
func TestWSOperationWatchSeesAnMCPCall(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	var mu sync.Mutex
	var trace []string
	p.ops.UseTrace(func(format string, v ...any) {
		mu.Lock()
		defer mu.Unlock()
		trace = append(trace, fmt.Sprintf(format, v...))
	})

	w := wsConnect(t, p)
	w.send("watch", "operation.watch", "", `{}`, nil)
	f := w.must("the operation.watch response")
	if f.ID != "watch" || f.Error != nil {
		t.Fatalf("operation.watch answered %+v (error %+v)", f, f.Error)
	}
	var res struct {
		SubscriptionID string `json:"subscriptionId"`
		Operations     []struct {
			OperationID string `json:"operationId"`
			Capability  string `json:"capability"`
			State       string `json:"state"`
			Client      string `json:"client"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(f.Result, &res); err != nil || res.SubscriptionID == "" {
		t.Fatalf("the result %s: %v", f.Result, err)
	}
	// The watch is itself a recorded operation, running when its snapshot was taken.
	if len(res.Operations) != 1 || res.Operations[0].Capability != "operation.watch" || res.Operations[0].State != "running" || res.Operations[0].Client != "ui" {
		t.Errorf("the snapshot is %+v; want the watch itself, running, client ui", res.Operations)
	}

	c := startShim(t, p.state, "mcp-client")
	c.initialize("watch-test")
	c.callTool(1, "system_health", `{}`)
	if v := response(t, c.recv(), 1); v == nil {
		t.Fatal("system_health answered nothing")
	}

	type seen struct {
		op      string
		payload capability.OperationState
	}
	var health []seen
	for len(health) < 2 {
		fr := w.must("the events of the MCP call")
		if fr.Event == nil || fr.SubscriptionID != res.SubscriptionID {
			continue // the watch's own terminal event, or the response
		}
		if fr.Event.Topic != capability.StateTopic || fr.Event.Type != capability.StateType {
			t.Fatalf("the watch received %s/%s", fr.Event.Topic, fr.Event.Type)
		}
		var pl capability.OperationState
		if err := json.Unmarshal(fr.Event.Payload, &pl); err != nil {
			t.Fatal(err)
		}
		if pl.Capability == "system.health" {
			health = append(health, seen{fr.Event.Operation, pl})
		}
	}
	a, b := health[0], health[1]
	if a.payload.State != capability.StateRunning || b.payload.State != capability.StateCompleted || a.op != b.op || a.op == "" ||
		a.payload.Client != capability.ClientMCP || b.payload.Client != capability.ClientMCP || b.payload.DurationMs == nil {
		t.Errorf("the watch received %+v then %+v; want running then completed for one operation, client mcp, with durationMs", a, b)
	}
	for deadline := 0; ; deadline++ {
		mu.Lock()
		joined := strings.Join(trace, "\n")
		mu.Unlock()
		if strings.Contains(joined, "op "+a.op+" client=mcp capability=system.health") {
			break
		}
		if deadline > 2000 {
			t.Fatalf("the daemon's trace has no line for %s:\n%s", a.op, joined)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
