package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jtortosasen/droidyard/adapters/cli"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/system"
)

// opWatch is a WebSocket subscribed to operation.state: it reads the daemon's recorded
// operations, in order, to see whose session each carries.
type opWatch struct {
	t   *testing.T
	ws  *wsClient
	sub string
}

func watchOperations(t *testing.T, state string) *opWatch {
	t.Helper()
	ws := wsToDaemon(t, state)
	ws.send("watch", "operation.watch", "", `{}`, nil)
	_, f := ws.collect("watch")
	var res struct {
		SubscriptionID string `json:"subscriptionId"`
	}
	if err := json.Unmarshal(f.Result, &res); err != nil || res.SubscriptionID == "" {
		t.Fatalf("operation.watch answered %s: %v", f.Result, err)
	}
	return &opWatch{t: t, ws: ws, sub: res.SubscriptionID}
}

// completed is the next completed operation of the capability, whatever else is recorded
// meanwhile.
func (o *opWatch) completed(cap string) capability.OperationState {
	o.t.Helper()
	for {
		fr := o.ws.must("a completed " + cap)
		if fr.Event == nil || fr.SubscriptionID != o.sub {
			continue
		}
		var pl capability.OperationState
		if err := json.Unmarshal(fr.Event.Payload, &pl); err != nil {
			o.t.Fatal(err)
		}
		if pl.Capability == cap && pl.State == capability.StateCompleted {
			return pl
		}
	}
}

// claudeAfterChat is a Claude Code PostToolUse for command run in cwd by the chat.
func claudeAfterChat(t *testing.T, command, cwd, chat string) string {
	t.Helper()
	return cursorFixture(t, "claude-postToolUse", map[string]any{"cwd": cwd, "session_id": chat, "tool_input": map[string]any{"command": command}})
}

// TestHookJoinsItsSession is ADR-0095 §1 and §2 end to end: a real `serve`, scripted MCP
// shims, the real `droidyard hook` and `droidyard trash`. A hook or a trash that names a
// chat is joined to the live MCP session of that chat, which gains attribution and
// freshness and nothing else.
//
//   - a Claude Code PostToolUse `./gradlew tasks` whose session_id is the chat of shim
//     agent-a is recorded under agent-a's session ({id, name}); one with another
//     session_id carries no MCP session's ref;
//   - agent-a idle (the daemon's clock moved past staleAfterMinutes), a joined hook event
//     clears its `stale`;
//   - a CLI connection joined to agent-a calling workspace.enter fails
//     REQUEST_NOT_ALLOWED_HERE and agent-a's binding stays where it was;
//   - a Cursor event whose conversation_id is C, beside a Cursor-like shim with no chat,
//     is joined to no one (a smoke row: see the comment at the row);
//   - `droidyard trash` with CLAUDE_CODE_SESSION_ID the chat's id records its file.delete
//     under agent-a; with CURSOR_AGENT=1 it ignores that variable and is joined to no one.
//
// Seen failing: with the hook's hello not carrying the chat, the first row's record
// carried no session of agent-a; with Seen not called for a joined request, agent-a stayed
// stale; with the connection's Origin.ID set to the joined session and the Binding
// check's client test removed, the CLI's workspace.enter moved agent-a to wt1; with
// `trash` carrying no chat, its file.delete carried no session of agent-a; with the
// CURSOR_AGENT test removed, the second delete was joined to agent-a.
func TestHookJoinsItsSession(t *testing.T) {
	t.Parallel()
	w := newOccupationWorld(t)
	const chat = "chat-x"
	a := w.agent(t, "agent-a", chat, w.main)
	w.agent(t, "cursor-like", "", w.main)
	aRow, _ := w.row(t, "agent-a")
	cRow, _ := w.row(t, "cursor-like")
	ops := watchOperations(t, w.state)

	hook := func(event string, args ...string) {
		t.Helper()
		res := runHook(t, w.state, event, nil, append([]string{"--root", w.main}, args...)...)
		if res.code != 0 || res.stderr != "" {
			t.Fatalf("the hook exited %d with stderr %q; want 0 and none", res.code, res.stderr)
		}
	}
	claude := func(chat string) {
		t.Helper()
		hook(claudeAfterChat(t, "./gradlew tasks", w.main, chat), "--client", "claude")
	}

	// A hook event of the chat is recorded under the chat's session.
	claude(chat)
	if ref := ops.completed("agent.recordCommand").Session; ref == nil || ref.ID != aRow.ID || ref.Name != "agent-a" {
		t.Errorf("the record of chat %s carries the session %+v; want agent-a's {id %s, name agent-a}", chat, ref, aRow.ID)
	}
	// An event of another chat is joined to no MCP session: as before, the hook's own
	// connection, with no name.
	claude("chat-y")
	if ref := ops.completed("agent.recordCommand").Session; ref != nil && (ref.ID == aRow.ID || ref.ID == cRow.ID || ref.Name != "") {
		t.Errorf("the record of an unknown chat carries the session %+v; want no MCP session's", ref)
	}

	// A smoke row, not a proven one: "Cursor stays unjoined" rests on the Cursor shim reading
	// only CLAUDE_CODE_SESSION_ID (spike C2 (d)), so its session has no chat to match; this row
	// would fail only if the daemon joined by something other than the chat.
	hook(cursorFixture(t, "cursor-4-postToolUse", map[string]any{"conversation_id": "conv-c",
		"tool_input": map[string]any{"command": "./gradlew tasks", "cwd": ""}}), "--client", "cursor")
	if ref := ops.completed("agent.recordCommand").Session; ref != nil && (ref.ID == aRow.ID || ref.ID == cRow.ID || ref.Name != "") {
		t.Errorf("the record of a Cursor event carries the session %+v; want none of the shims'", ref)
	}

	// A connection joined to the session moves no binding: workspace.enter is for the MCP
	// session alone.
	conn, r, _, err := daemon.Dial(sockFor(w.state), daemon.Hello{Protocol: daemon.Protocol, Version: system.Version,
		Client: capability.ClientCLI, Chat: chat, Root: w.main})
	if err != nil {
		t.Fatalf("dialling as a joined CLI connection: %v", err)
	}
	env, err := runCall(conn, r, cli.Call{Capability: "workspace.enter", Params: json.RawMessage(`{"root":"` + w.wt1 + `"}`)})
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	if env.Error == nil || env.Error.Code != capability.ErrNotAllowedHere {
		t.Errorf("workspace.enter from a joined CLI connection: %+v; want REQUEST_NOT_ALLOWED_HERE", env.Error)
	}
	if root, _ := describedWith(t, a, 10); root != w.main {
		t.Errorf("agent-a describes %s after the joined connection's enter; want the main tree: a joined call moves no binding", root)
	}
	if row, _ := w.row(t, "agent-a"); row.Occupies != "" {
		t.Errorf("agent-a occupies %q; want nothing", row.Occupies)
	}

	// `droidyard trash`: Claude Code's shell carries the chat in CLAUDE_CODE_SESSION_ID.
	trash := func(file string, env ...string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(w.main, file), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		res := runTrashEnv(t, w.main, w.state, env, "--root", w.main, "--", file)
		if res.code != 0 {
			t.Fatalf("droidyard trash %s exited %d: %s%s", file, res.code, res.stdout, res.stderr)
		}
	}
	trash("del-1.txt", chatEnv+"="+chat)
	if ref := ops.completed("file.delete").Session; ref == nil || ref.ID != aRow.ID || ref.Name != "agent-a" {
		t.Errorf("the delete of `droidyard trash` with CLAUDE_CODE_SESSION_ID=%s carries the session %+v; want agent-a's", chat, ref)
	}
	// A cursor-agent started from a Claude Code shell inherits that variable: CURSOR_AGENT=1
	// says the chat it names is not this shell's.
	trash("del-2.txt", chatEnv+"="+chat, "CURSOR_AGENT=1")
	if ref := ops.completed("file.delete").Session; ref != nil && (ref.ID == aRow.ID || ref.Name != "") {
		t.Errorf("the delete of a cursor-agent shell carries the session %+v; want no MCP session's", ref)
	}

	// Freshness: agent-a goes idle, and a joined hook event ends it. The shim made no call.
	w.configure(t, `{"staleAfterMinutes":1}`)
	w.advanceTo(120)
	w.waitRow(t, "agent-a", "stale", func(r capability.SessionListed) bool { return r.Stale })
	claude(chat)
	w.waitRow(t, "agent-a", "fresh again after a joined hook", func(r capability.SessionListed) bool { return !r.Stale })
	// The idleness of a session the hook did not name is left alone.
	if r, _ := w.row(t, "cursor-like"); !r.Stale {
		t.Errorf("cursor-like's row is %+v; want it still idle: only the joined session is seen", r)
	}
}
