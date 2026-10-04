package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// lifecycle is what a lifecycle subscription received, as "state" strings, and the
// workspace each carried.
func lifecycle(t *testing.T, sub *event.Subscription) (states []string, workspaces []string) {
	t.Helper()
	for {
		e, ok := sub.TryNext()
		if !ok {
			return
		}
		var p struct {
			State string `json:"state"`
		}
		if e.Topic != "workspace.lifecycle" || json.Unmarshal(e.Payload, &p) != nil || p.State != e.Type || e.Workspace == nil {
			t.Fatalf("an event on a lifecycle subscription: %+v", e)
		}
		states, workspaces = append(states, e.Type), append(workspaces, *e.Workspace)
	}
}

// TestEventsThroughASession: on the shipped wiring, a session that can carry events
// (here the event layer's own, the one the WebSocket will set) subscribes, receives
// the workspace's transitions from a real workspace.open and workspace.close, and
// stops receiving once it unsubscribes; another session's subscription is untouched
// by its unsubscribe; the session's end leaves the bus with none of its subscriptions
// (plan m1d, step 6; ADR-0010 as amended; capability-api.md §Subscriptions).
func TestEventsThroughASession(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	a, b := event.NewSession(p.bus, "ses-a"), event.NewSession(p.bus, "ses-b")
	run := func(s *event.Session, name, workspace, params string) (capability.Outcome, error) {
		return p.ops.Invoke(context.Background(), capability.Request{
			Capability: name, Workspace: workspace, Params: json.RawMessage(params), Client: capability.ClientUI, Session: s,
		})
	}
	subscribe := func(s *event.Session) (string, *event.Subscription) {
		t.Helper()
		out, err := run(s, "events.subscribe", "", `{"topics":["workspace.lifecycle"]}`)
		if err != nil || out.State != capability.StateCompleted {
			t.Fatalf("events.subscribe: (%+v, %v); want completed", out, err)
		}
		id := out.Result.(event.SubscribeResult).SubscriptionID
		sub, ok := s.Subscription(id)
		if !ok {
			t.Fatalf("the session does not hold subscription %s", id)
		}
		return id, sub
	}
	idA, subA := subscribe(a)
	_, subB := subscribe(b)

	// b ends a's subscription: nothing happens to it.
	if out, err := run(b, "events.unsubscribe", "", `{"subscriptionId":"`+idA+`"}`); err != nil || out.State != capability.StateCompleted {
		t.Fatalf("events.unsubscribe of another session's id: (%+v, %v); want completed", out, err)
	}

	root, _ := gradleProject(t)
	id, _ := platform.Current().WorkspaceID(root)
	if out, err := run(a, "workspace.open", root, `{}`); err != nil || out.State != capability.StateCompleted {
		t.Fatalf("workspace.open: (%+v, %v)", out, err)
	}
	for name, sub := range map[string]*event.Subscription{"a": subA, "b": subB} {
		states, workspaces := lifecycle(t, sub)
		if strings.Join(states, ",") != "opening,ready" || workspaces[0] != id || workspaces[1] != id {
			t.Errorf("session %s received %v for %v; want opening, ready for %s", name, states, workspaces, id)
		}
	}

	// a unsubscribes: it receives nothing more; b still does.
	if out, err := run(a, "events.unsubscribe", "", `{"subscriptionId":"`+idA+`"}`); err != nil || out.State != capability.StateCompleted || out.Result != (event.UnsubscribeResult{}) {
		t.Fatalf("events.unsubscribe: (%+v, %v); want completed with {}", out, err)
	}
	if out, err := run(a, "workspace.close", root, `{}`); err != nil || out.State != capability.StateCompleted {
		t.Fatalf("workspace.close: (%+v, %v)", out, err)
	}
	if states, _ := lifecycle(t, subA); len(states) != 0 {
		t.Errorf("a received %v after it unsubscribed", states)
	}
	if states, _ := lifecycle(t, subB); strings.Join(states, ",") != "closing,closed" {
		t.Errorf("b received %v; want closing, closed", states)
	}

	// A call that names a topic nobody can deliver is rejected, with no operation.
	for _, params := range []string{`{"topics":["nope.nope"]}`, `{"topics":[]}`} {
		id, err := p.ops.Start(context.Background(), capability.Request{Capability: "events.subscribe", Params: json.RawMessage(params), Client: capability.ClientUI, Session: a})
		var e *capability.Error
		if !errors.As(err, &e) || e.Code != capability.ErrInvalidParams || id != "" {
			t.Errorf("events.subscribe %s: (%q, %v); want REQUEST_INVALID_PARAMS and no operation", params, id, err)
		}
	}

	a.Close()
	b.Close()
	if n := p.bus.Subscriptions(); n != 0 {
		t.Errorf("after both sessions ended the bus holds %d subscriptions; want 0", n)
	}
}

// TestEventsNeedASessionThatCanCarryThem: through every adapter the daemon ships, a
// call to events.subscribe or events.unsubscribe is rejected with SESSION_REQUIRED at
// Start, naming GET /v1/ws, with no operation: plain HTTP, a CLI call, and the core's
// MCP client kind (the MCP adapter lists no events tool at all; see
// TestMCPTranscript). ADR-0010 as amended; plan m1d, D6.
func TestEventsNeedASessionThatCanCarryThem(t *testing.T) {
	t.Parallel()
	p, _ := startHTTP(t, "127.0.0.1:0")
	port := portOf(t, p.d.HTTPAddr())
	subscribeParams := `{"topics":["workspace.lifecycle"]}`
	unsubscribeParams := `{"subscriptionId":"sub-0000000000000000"}`
	want := func(what string, env capability.ResponseEnvelope) {
		t.Helper()
		if env.Error == nil || env.Error.Code != capability.ErrSessionRequired || env.Operation != nil || env.Result != nil ||
			!strings.Contains(env.Error.NextStep, "GET /v1/ws") {
			t.Errorf("%s: %+v (error %+v); want SESSION_REQUIRED naming GET /v1/ws, no operation, no result", what, env, env.Error)
		}
	}

	for _, c := range []struct{ name, params string }{{"events.subscribe", subscribeParams}, {"events.unsubscribe", unsubscribeParams}} {
		resp := callRequest(port, p.token, `{"id":"e","capability":"`+c.name+`","params":`+c.params+`}`).send(t, p.d.HTTPAddr())
		if resp.StatusCode != 200 {
			t.Errorf("POST /v1/call %s: %d; want 200 with an envelope", c.name, resp.StatusCode)
		}
		want("POST /v1/call "+c.name, envelopeOf(t, resp))

		env, code := p.call(t, c.name, "--params", c.params)
		if code == 0 {
			t.Errorf("droidyard call %s exited 0", c.name)
		}
		want("droidyard call "+c.name, env)

		out, err := p.ops.Invoke(context.Background(), capability.Request{Capability: c.name, Params: json.RawMessage(c.params), Client: capability.ClientMCP})
		var e *capability.Error
		if !errors.As(err, &e) || e.Code != capability.ErrSessionRequired || out.OperationID != "" {
			t.Errorf("%s as client mcp: (%+v, %v); want SESSION_REQUIRED", c.name, out, err)
		}
	}
	if n := p.bus.Subscriptions(); n != 0 {
		t.Errorf("the rejected calls left %d subscriptions on the bus", n)
	}
}
