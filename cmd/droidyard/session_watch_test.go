package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// watchSessions calls session.watch on w and returns the subscription, the rows and the
// raw result.
func watchSessions(t *testing.T, w *wsClient, id string) (string, []capability.SessionListed, string) {
	t.Helper()
	w.send(id, "session.watch", "", "", nil)
	_, f := w.collect(id)
	if f.Error != nil {
		t.Fatalf("session.watch: %+v", f.Error)
	}
	var res capability.SessionWatchResult
	if err := json.Unmarshal(f.Result, &res); err != nil || res.SubscriptionID == "" {
		t.Fatalf("session.watch result %s: %v", f.Result, err)
	}
	return res.SubscriptionID, res.Sessions, string(f.Result)
}

// nextSession is the next session.state event on sub, with its raw payload. Frames of
// other subscriptions or of no event are skipped.
func nextSession(t *testing.T, w *wsClient, sub string) (capability.SessionState, string) {
	t.Helper()
	for {
		f := w.must("a session.state event")
		if f.Event == nil || f.SubscriptionID != sub {
			continue
		}
		if f.Event.Topic != capability.SessionStateTopic || f.Event.Type != capability.SessionStateType || f.Event.Workspace != nil {
			t.Fatalf("the event is %s/%s workspace %v; want a global session.state/state", f.Event.Topic, f.Event.Type, f.Event.Workspace)
		}
		var st capability.SessionState
		if err := json.Unmarshal(f.Event.Payload, &st); err != nil {
			t.Fatal(err)
		}
		return st, string(f.Event.Payload)
	}
}

// discover is the first message of a client of protocol 2026-07-28 (Claude Code 2.1.302
// opens with it and sends no initialize): its identity rides in _meta.
func (s *shim) discover(clientName string) {
	s.t.Helper()
	s.send(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"` + clientName + `","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`)
	response(s.t, s.recv(), 1)
}

// TestSessionWatch is ADR-0083 §1 end to end, on a real `serve`, real shims and a real
// `git worktree add`. A watcher is told of an MCP session from the moment it connects: its
// connect (the id alone, though the shim has sent nothing), its hello's binding, the name
// from initialize, the name from server/discover (the handshake Claude Code 2.1.302 uses),
// a workspace_enter, and its end when the shim is killed. A second session.watch lists the
// sessions connected, oldest first, with the same state; a killed session is no row. A CLI
// call is no session, and the chat id the shims carry is in no frame.
//
// Seen failing: with Connect not publishing, the first event was the hello's binding and
// the row-less connect never arrived; with Named not called from the server/discover hook,
// the discover session stayed without a name; with Bound not publishing, workspace_enter
// moved nothing the watcher saw; with End not publishing, the killed shim was never ended.
func TestSessionWatch(t *testing.T) {
	t.Parallel()
	w := newEnterWorld(t)
	idOf := func(root string) string {
		id, _ := platform.Current().WorkspaceID(root)
		return id
	}
	const chat = "chat-secret-watch"
	var frames []string // everything the watcher is told, to look for the chat id in
	next := func(ws *wsClient, sub string) capability.SessionState {
		t.Helper()
		st, raw := nextSession(t, ws, sub)
		frames = append(frames, raw)
		return st
	}

	ws := wsToDaemon(t, w.state)
	sub, rows, raw := watchSessions(t, ws, "w1")
	if len(rows) != 0 || !strings.Contains(raw, `"sessions":[]`) {
		t.Fatalf("with no session connected session.watch lists %s; want an empty array", raw)
	}

	// A connects and sends nothing: it is an event, and a row, at once.
	a := startShimEnv(t, w.state, w.main, "a", []string{chatEnv + "=" + chat}, []string{"mcp", "--no-autostart", "--root", w.main})
	connect := next(ws, sub)
	aID := connect.ID
	if aID == "" || connect.Name != "" || connect.Workspace != "" || connect.Ended {
		t.Fatalf("the first event is %+v; want the connect: the id alone", connect)
	}
	if st := next(ws, sub); st.ID != aID || st.Workspace != idOf(w.main) || st.Name != "" || st.Ended {
		t.Fatalf("the second event is %+v; want %s bound to main (%s)", st, aID, idOf(w.main))
	}
	watcher2 := wsToDaemon(t, w.state)
	_, rows, raw = watchSessions(t, watcher2, "w2")
	if len(rows) != 1 || rows[0].ID != aID || rows[0].Name != "" || rows[0].Workspace != idOf(w.main) || rows[0].ConnectedAt.IsZero() {
		t.Fatalf("a second session.watch lists %s; want A, unnamed, bound to main", raw)
	}
	frames = append(frames, raw)

	// initialize names it; the binding stays.
	a.initialize("agent-a")
	if st := next(ws, sub); st.ID != aID || st.Name != "agent-a" || st.Workspace != idOf(w.main) || st.Ended {
		t.Errorf("after initialize the event is %+v; want the name agent-a, still bound to main", st)
	}

	// workspace_enter moves it.
	if c := a.toolWith(1, "workspace_enter", `{"root":"`+w.wt+`"}`); c.isError {
		t.Fatalf("workspace_enter: %s", c.raw)
	}
	if st := next(ws, sub); st.ID != aID || st.Name != "agent-a" || st.Workspace != idOf(w.wt) || st.Ended {
		t.Errorf("after workspace_enter the event is %+v; want agent-a bound to the worktree (%s)", st, idOf(w.wt))
	}

	// A CLI call is one-shot and no session; B, a client of protocol 2026-07-28, opens
	// with server/discover and is named by it.
	if env, code := callIn(t, w.state, w.main, "system.health"); code != 0 {
		t.Fatalf("system.health exited %d: %+v", code, env.Error)
	}
	b := startShimEnv(t, w.state, w.main, "b", nil, []string{"mcp", "--no-autostart", "--root", w.main})
	connect = next(ws, sub)
	bID := connect.ID
	if bID == "" || bID == aID || connect.Name != "" || connect.Workspace != "" {
		t.Fatalf("the next new session's connect is %+v; want a second session (not the CLI call), the id alone", connect)
	}
	if st := next(ws, sub); st.ID != bID || st.Workspace != idOf(w.main) {
		t.Fatalf("B's binding event is %+v", st)
	}
	b.discover("claude-code")
	if st := next(ws, sub); st.ID != bID || st.Name != "claude-code" || st.Workspace != idOf(w.main) {
		t.Errorf("after server/discover the event is %+v; want the name claude-code", st)
	}
	_, rows, raw = watchSessions(t, watcher2, "w3")
	if len(rows) != 2 || rows[0].ID != aID || rows[1].ID != bID || rows[0].Name != "agent-a" || rows[0].Workspace != idOf(w.wt) || rows[1].Name != "claude-code" {
		t.Errorf("session.watch lists %s; want A then B (oldest first) with their names and workspaces", raw)
	}
	frames = append(frames, raw)

	// A is killed: its end, and no row.
	a.cmd.Process.Kill()
	if st := next(ws, sub); st.ID != aID || !st.Ended || st.Name != "" || st.Workspace != "" {
		t.Errorf("after the kill the event is %+v; want A's end alone", st)
	}
	_, rows, raw = watchSessions(t, watcher2, "w4")
	if len(rows) != 1 || rows[0].ID != bID {
		t.Errorf("session.watch lists %s after A was killed; want only B", raw)
	}
	b.exit()
	if st := next(ws, sub); st.ID != bID || !st.Ended {
		t.Errorf("after B's exit the event is %+v; want B's end", st)
	}

	for _, f := range frames {
		if strings.Contains(f, chat) {
			t.Errorf("a frame carries the chat id (ADR-0088 §4): %s", f)
		}
	}
	w.stop(t)
}

// TestSessionWatchLosesNothingBetweenSubscriptionAndSnapshot is the seam of ADR-0083 §1,
// as workspace.watch's is ADR-0045 §3's: a session that connects, and one that ends, after
// the subscription is open and before the snapshot is taken reach the watcher as events,
// and the snapshot shows the world after both. The sessions are registered in the core's
// registry directly: the window cannot be hit through a shim.
//
// Seen failing: with the snapshot taken before the subscription opens (the hook then runs
// between them), the connect was in neither the snapshot nor an event.
func TestSessionWatchLosesNothingBetweenSubscriptionAndSnapshot(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	sessions := p.ops.Sessions()
	sessions.Connect("old", "")
	sessions.SetWatchHook(func() {
		sessions.Connect("new", "")
		sessions.End("old")
	})
	t.Cleanup(func() { sessions.SetWatchHook(nil) })

	w := wsConnect(t, p)
	sub, rows, raw := watchSessions(t, w, "w")
	sessions.SetWatchHook(nil)
	if len(rows) != 1 || rows[0].ID != "new" {
		t.Errorf("the snapshot is %s; want only the session that connected in the window", raw)
	}
	var connected, ended bool
	for !(connected && ended) {
		st, _ := nextSession(t, w, sub)
		switch {
		case st.ID == "new" && !st.Ended:
			connected = true
		case st.ID == "old" && st.Ended:
			ended = true
		default:
			t.Fatalf("unexpected event %+v", st)
		}
	}
}

// TestSessionWatchOffMCP: session.watch is streaming, so ADR-0010 keeps it off MCP (no
// session_watch tool), and it is refused outside a session that can carry events: the CLI
// and plain HTTP get SESSION_REQUIRED naming GET /v1/ws, with no subscription left.
func TestSessionWatchOffMCP(t *testing.T) {
	t.Parallel()
	p, _ := startHTTP(t, "127.0.0.1:0")
	c := startShim(t, p.state, "mcp-client")
	c.initialize("watch-off-mcp")
	names, _ := toolNames(t, c, 1)
	for _, n := range names {
		if strings.Contains(n, "session") {
			t.Errorf("tools/list has %s; want no session tool", n)
		}
	}
	if len(names) == 0 {
		t.Fatal("tools/list is empty")
	}
	env, code := p.call(t, "session.watch")
	if code == 0 || env.Error == nil || env.Error.Code != capability.ErrSessionRequired || !strings.Contains(env.Error.NextStep, "GET /v1/ws") {
		t.Errorf("droidyard call session.watch = (%+v, exit %d); want SESSION_REQUIRED naming GET /v1/ws", env.Error, code)
	}
	if n := p.bus.Subscriptions(); n != 0 {
		t.Errorf("the refused call left %d subscriptions", n)
	}
}
