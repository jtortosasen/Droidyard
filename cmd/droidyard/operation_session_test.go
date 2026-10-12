package main

import (
	"encoding/json"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// TestOperationStateCarriesTheSession is the promise of ADR-0048: the operation.state
// events and operation.watch's rows name the session that made a call, so the UI can tell
// two agents of the same client kind apart. Two real MCP shims, named "agent-a" and
// "agent-b" in initialize and bound to two project roots, a WebSocket session, a plain
// POST /v1/call and a CLI call, all through one daemon. Each shim's workspace.describe and
// system.health events carry its own session id, its own name and the workspace of its
// bound root; its bound workspace.open, which runs before initialize, carries the id and
// no name; the WebSocket's carry its id and no name; the plain HTTP call carries no
// session at all. A name is bounded and cleaned by the store (TestSessionNameIsBounded).
func TestOperationStateCarriesTheSession(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	rootA, _ := projectWith(t, "settings.gradle")
	rootB, _ := projectWith(t, "settings.gradle.kts")
	idOf := func(root string) string {
		id, _ := platform.Current().WorkspaceID(root)
		return id
	}

	w := wsConnect(t, p)
	w.send("watch", "operation.watch", "", `{}`, nil)
	f := w.must("the operation.watch response")
	var res struct {
		SubscriptionID string `json:"subscriptionId"`
		Operations     []struct {
			OperationID string                 `json:"operationId"`
			Capability  string                 `json:"capability"`
			Session     *capability.SessionRef `json:"session"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(f.Result, &res); err != nil || res.SubscriptionID == "" {
		t.Fatalf("operation.watch answered %s: %v", f.Result, err)
	}
	// The watch's own row: the WebSocket's session, with an id and no name or workspace.
	var wsSession *capability.SessionRef
	for _, op := range res.Operations {
		if op.Capability == "operation.watch" {
			wsSession = op.Session
		}
	}
	if wsSession == nil || wsSession.ID == "" || wsSession.Name != "" || wsSession.Workspace != "" {
		t.Fatalf("the WebSocket's own row carries %+v; want an id and no name or workspace", wsSession)
	}

	a := startShimIn(t, p.state, rootA, "a", []string{"mcp", "--no-autostart"})
	a.initialize("agent-a")
	b := startShimIn(t, p.state, rootB, "b", []string{"mcp", "--no-autostart"})
	b.initialize("agent-b")
	for i, s := range []*shim{a, b} {
		s.callTool(10+i, "workspace_describe", `{}`)
		if v := response(t, s.recv(), 10+i); v == nil {
			t.Fatalf("%s: workspace_describe answered nothing", s.name)
		}
		s.callTool(20+i, "system_health", `{}`)
		if v := response(t, s.recv(), 20+i); v == nil {
			t.Fatalf("%s: system_health answered nothing", s.name)
		}
	}
	resp := callRequest(portOf(t, p.d.HTTPAddr()), p.token, `{"id":"h","capability":"system.health"}`).send(t, p.d.HTTPAddr())
	httpOp := envelopeOf(t, resp).Operation
	if httpOp == nil {
		t.Fatal("POST /v1/call system.health recorded no operation")
	}
	cliEnv, code := p.call(t, "system.health")
	if code != 0 || cliEnv.Operation == nil {
		t.Fatalf("droidyard call system.health: exit %d, %+v", code, cliEnv)
	}
	w.send("own", "system.health", "", `{}`, nil)

	var events []sessionEvent
	for !sessionEventsDone(events, httpOp.ID, cliEnv.Operation.ID) {
		fr := w.must("the operation.state events")
		if fr.Event == nil || fr.SubscriptionID != res.SubscriptionID {
			continue
		}
		var pl capability.OperationState
		if err := json.Unmarshal(fr.Event.Payload, &pl); err != nil {
			t.Fatal(err)
		}
		events = append(events, sessionEvent{fr.Event.Operation, pl})
	}

	// Group the sessions seen by their id; every event of one id must agree.
	byClient := map[capability.ClientKind]map[string]bool{}
	var aID, bID string
	for _, e := range events {
		s := e.payload.Session
		switch {
		case e.op == httpOp.ID:
			if s != nil {
				t.Errorf("POST /v1/call's %s event carries the session %+v; want none", e.payload.State, s)
			}
			continue
		case s == nil:
			t.Errorf("%s %s (client %q) carries no session; want one", e.payload.Capability, e.payload.State, e.payload.Client)
			continue
		}
		if byClient[e.payload.Client] == nil {
			byClient[e.payload.Client] = map[string]bool{}
		}
		byClient[e.payload.Client][s.ID] = true
		switch {
		case s.Name == "agent-a":
			if aID == "" {
				aID = s.ID
			}
			if s.ID != aID {
				t.Errorf("agent-a is session %s and %s", aID, s.ID)
			}
			if s.Workspace != idOf(rootA) {
				t.Errorf("agent-a's %s event names workspace %q; want its bound root's %q", e.payload.Capability, s.Workspace, idOf(rootA))
			}
		case s.Name == "agent-b":
			if bID == "" {
				bID = s.ID
			}
			if s.ID != bID {
				t.Errorf("agent-b is session %s and %s", bID, s.ID)
			}
			if s.Workspace != idOf(rootB) {
				t.Errorf("agent-b's %s event names workspace %q; want its bound root's %q", e.payload.Capability, s.Workspace, idOf(rootB))
			}
		case e.payload.Client == capability.ClientMCP:
			// The bound open, made before initialize: the session's id, no name.
			if e.payload.Capability != "workspace.open" {
				t.Errorf("an MCP %s event has no session name; only the bound workspace.open runs before initialize", e.payload.Capability)
			}
		case e.payload.Client == capability.ClientUI:
			if s.ID != wsSession.ID || s.Name != "" {
				t.Errorf("a WebSocket %s event carries %+v; want id %s and no name", e.payload.Capability, s, wsSession.ID)
			}
		}
	}
	if aID == "" || bID == "" || aID == bID || aID == wsSession.ID || bID == wsSession.ID {
		t.Fatalf("session ids: agent-a %q, agent-b %q, WebSocket %q; want three distinct ones", aID, bID, wsSession.ID)
	}
	if len(byClient[capability.ClientMCP]) != 2 {
		t.Errorf("the MCP events came from sessions %v; want exactly agent-a's and agent-b's", byClient[capability.ClientMCP])
	}
	// The bound opens: each carries its shim's id, no name, and no workspace, which was not
	// open when the call was recorded.
	opens := map[string]bool{}
	for _, e := range events {
		if e.payload.Capability == "workspace.open" && e.payload.Client == capability.ClientMCP {
			if s := e.payload.Session; s == nil || s.Name != "" || s.Workspace != "" || (s.ID != aID && s.ID != bID) {
				t.Errorf("a bound workspace.open event carries %+v; want agent-a's or agent-b's id, no name and no workspace", s)
			} else {
				opens[s.ID] = true
			}
		}
	}
	if !opens[aID] || !opens[bID] {
		t.Errorf("the bound opens seen were of sessions %v; want both shims'", opens)
	}
	// The CLI call: a session of its own, with no name.
	for _, e := range events {
		if e.op == cliEnv.Operation.ID {
			if s := e.payload.Session; s == nil || s.ID == "" || s.Name != "" || s.ID == aID || s.ID == bID || s.ID == wsSession.ID {
				t.Errorf("the CLI call's event carries %+v; want a session id of its own and no name", s)
			}
		}
	}
}

// sessionEvent is one operation.state event the WebSocket watch received.
type sessionEvent struct {
	op      string
	payload capability.OperationState
}

// sessionEventsDone reports whether every call of the scenario has completed: the two
// workspace.describe calls, the five system.health calls (two shims, the WebSocket, the plain HTTP
// call and the CLI), the last two named by operation id.
func sessionEventsDone(events []sessionEvent, httpOp, cliOp string) bool {
	completed := map[string]bool{}
	byCap := map[string]int{}
	for _, e := range events {
		if e.payload.State == capability.StateCompleted {
			completed[e.op] = true
			byCap[e.payload.Capability]++
		}
	}
	return completed[httpOp] && completed[cliOp] && byCap["workspace.describe"] >= 2 && byCap["system.health"] >= 5
}
