package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/adapters/cli"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/agent"
	"github.com/jtortosasen/droidyard/internal/domain/system"
)

// editEvent is one of the events plan m2s step 1 captured (internal/assistant/testdata/hooks,
// home path and email removed) for an edit of path by chat, run in cwd.
func editEvent(t *testing.T, fixture, path, chat, cwd string) string {
	t.Helper()
	set := map[string]any{"tool_input": map[string]any{"file_path": path, "content": "x"}}
	if strings.HasPrefix(fixture, "cursor") {
		set["conversation_id"] = chat
	} else {
		set["session_id"], set["cwd"] = chat, cwd
	}
	return cursorFixture(t, fixture, set)
}

// bashEvent is a Claude Code PreToolUse for a shell command of chat.
func bashEvent(command, cwd, chat string) string {
	b, _ := json.Marshal(map[string]any{
		"session_id": chat, "transcript_path": "/nonexistent/t.jsonl", "cwd": cwd, "permission_mode": "default",
		"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": command}, "tool_use_id": "toolu_1",
	})
	return string(b)
}

// completedEdit is the next completed operation of the capability, with the workspace and the
// operation id of its event.
func (o *opWatch) completedEdit(cap string) (capability.OperationState, string, string) {
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
			ws := ""
			if fr.Event.Workspace != nil {
				ws = *fr.Event.Workspace
			}
			return pl, ws, fr.Event.Operation
		}
	}
}

// recordOf is the result of the completed operation id, as Activity reads it: operation.wait.
func recordOf(t *testing.T, state, id string) agent.Edit {
	t.Helper()
	conn, r, _, err := daemon.Dial(sockFor(state), daemon.Hello{Protocol: daemon.Protocol, Version: system.Version, Client: capability.ClientCLI})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	params, _ := json.Marshal(map[string]any{"operationId": id, "timeoutMs": 0})
	env, err := runCall(conn, r, cli.Call{Capability: "operation.wait", Params: params})
	if err != nil || env.Error != nil {
		t.Fatalf("operation.wait %s: %v %+v", id, err, env.Error)
	}
	var rec agent.Edit
	if err := json.Unmarshal(env.Result, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// TestHookEdits is ADR-0094 §5, ADR-0095 §3 and plan m2s step 4 end to end: a real `serve`
// whose file watches tick once an hour, scripted MCP shims, and the real `droidyard hook`
// fed step 1's captured Claude Code and cursor-agent events. Agent A, of chat-x, holds wt1;
// agent Y, of chat-y, is another chat's.
//
//   - Before an edit: Claude Code's Write and Edit by chat-y into wt1 are denied, the reason
//     naming A and `git worktree add`; so is cursor-agent's Write, with the way out in
//     user_message; cursor-agent's Read of the same path passes (the verdict is for its
//     Write); chat-x's own edit passes; conflict ask is Claude Code's permissionDecision
//     "ask" and, for cursor-agent, the deny; a caller that names no chat gets Claude Code's
//     pass carrying the warning as additionalContext; with no daemon the edit passes
//     within 2 s;
//   - a routed `rm` in wt1 by chat-y is the occupation deny, not the trash; by chat-x it is
//     the trash as before;
//   - After an edit: a Write that created a file in wt2 is an agent.recordEdit under wt2,
//     relative, created as the event says, under Y's session; an Edit in /tmp is under
//     --root, outside, the absolute path; cursor-agent's Write says neither created nor
//     updated;
//   - a file.watch on wt2 whose tick is an hour gets the change within 500 ms of the record.
//
// Seen failing: with the hook passing edits before they are written, the first row printed
// no deny; with the nudge removed, no change came within 500 ms; with the record not made, no
// agent.recordEdit was seen; with the rm's check given no paths, the rm was answered with the
// trash.
func TestHookEdits(t *testing.T) {
	t.Parallel()
	w := newOccupationWorld(t, watchTickEnv+"=1h")
	a := w.agent(t, "agent-a", "chat-x", w.main)
	y := w.agent(t, "agent-y", "chat-y", w.main)
	_ = y
	if c := w.enter(t, a, 10, w.wt1); c.isError {
		t.Fatalf("a enters wt1: %s", c.raw)
	}
	w.occupies(t, "agent-a", w.wt1)
	yRow, _ := w.row(t, "agent-y")
	ops := watchOperations(t, w.state)

	run := func(client, event string) hookResult {
		t.Helper()
		res := runHook(t, w.state, event, nil, "--client", client, "--root", w.main)
		if res.code != 0 || res.stderr != "" {
			t.Fatalf("the %s hook exited %d with stderr %q; want 0 and none", client, res.code, res.stderr)
		}
		return res
	}
	inWT1 := filepath.Join(w.wt1, "app", "Main.kt")

	// Before an edit.
	for _, tool := range []string{"claude-edit-1-pre-Write", "claude-edit-4-pre-Edit"} {
		reason := reasonOf(t, "claude", run("claude", editEvent(t, tool, inWT1, "chat-y", w.main)).stdout)
		if !strings.Contains(reason, "agent-a") || !strings.Contains(reason, "git worktree add") || !strings.Contains(reason, w.wt1) {
			t.Errorf("%s: the deny is %q; want it to name agent-a, wt1 and `git worktree add`", tool, reason)
		}
	}
	cursorDeny := run("cursor", editEvent(t, "cursor-edit-2-pre-Write", inWT1, "chat-y", ""))
	if reason := reasonOf(t, "cursor", cursorDeny.stdout); !strings.Contains(reason, "agent-a") || !strings.Contains(reason, "git worktree add") {
		t.Errorf("cursor's deny user_message is %q; want the holder and the way out in the one text its model reads", reason)
	}
	if got := run("cursor", editEvent(t, "cursor-edit-1-pre-Read", inWT1, "chat-y", "")).stdout; got != passOf("cursor") {
		t.Errorf("cursor's Read of a path it may not write answered %q; want the pass-through: the verdict is for its Write", got)
	}
	if got := run("claude", editEvent(t, "claude-edit-1-pre-Write", inWT1, "chat-x", w.main)).stdout; got != passOf("claude") {
		t.Errorf("the holder's own edit answered %q; want the pass-through", got)
	}
	if got := run("claude", editEvent(t, "claude-edit-1-pre-Write", filepath.Join(w.wt2, "free.kt"), "chat-y", w.main)).stdout; got != passOf("claude") {
		t.Errorf("an edit in a free worktree answered %q; want the pass-through", got)
	}
	var warn struct {
		HookSpecificOutput map[string]string `json:"hookSpecificOutput"`
	}
	noChat := run("claude", editEvent(t, "claude-edit-1-pre-Write", inWT1, "", w.main)).stdout
	if err := json.Unmarshal([]byte(noChat), &warn); err != nil || warn.HookSpecificOutput["permissionDecision"] != "" ||
		!strings.Contains(warn.HookSpecificOutput["additionalContext"], "cannot tell") {
		t.Errorf("an edit that names no chat answered %q (%v); want additionalContext alone, saying the holder cannot be told from the caller", noChat, err)
	}

	// A Claude Code chat whose hooks name a session no MCP connection has (a /clear gave them
	// a new one) may be the holder's own: warned, never refused. A Cursor conversation never is.
	clear := run("claude", editEvent(t, "claude-edit-1-pre-Write", inWT1, "chat-after-clear", w.main)).stdout
	if err := json.Unmarshal([]byte(clear), &warn); err != nil || !strings.Contains(warn.HookSpecificOutput["additionalContext"], "cannot tell") {
		t.Errorf("an edit of a chat with no live session answered %q (%v); want the warning", clear, err)
	}
	if reason := reasonOf(t, "cursor", run("cursor", editEvent(t, "cursor-edit-2-pre-Write", inWT1, "conv-without-mcp", "")).stdout); !strings.Contains(reason, "agent-a") {
		t.Errorf("a Cursor conversation with no MCP session: the deny is %q; want the holder named", reason)
	}

	w.configure(t, `{"conflict":"ask"}`)
	var ask struct {
		HookSpecificOutput map[string]string `json:"hookSpecificOutput"`
	}
	out := run("claude", editEvent(t, "claude-edit-1-pre-Write", inWT1, "chat-y", w.main)).stdout
	if err := json.Unmarshal([]byte(out), &ask); err != nil || ask.HookSpecificOutput["permissionDecision"] != "ask" || !strings.Contains(ask.HookSpecificOutput["permissionDecisionReason"], "agent-a") {
		t.Errorf("conflict ask answered %q (%v); want Claude Code's permissionDecision ask, naming agent-a", out, err)
	}
	if reason := reasonOf(t, "cursor", run("cursor", editEvent(t, "cursor-edit-2-pre-Write", inWT1, "chat-y", "")).stdout); !strings.Contains(reason, "agent-a") {
		t.Errorf("conflict ask for cursor answered a deny whose text is %q; want the holder named", reason)
	}
	// A routed rm is never the client's ask: an approved ask would run the plain rm and lose
	// the trash. It is the deny, naming the holder.
	if err := os.WriteFile(filepath.Join(w.wt1, "old.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	askRm := run("claude", bashEvent("rm old.txt", w.wt1, "chat-y")).stdout
	if strings.Contains(askRm, `"ask"`) {
		t.Errorf("an rm under conflict ask answered %q; want a deny, never the client's ask", askRm)
	}
	if reason := reasonOf(t, "claude", askRm); !strings.Contains(reason, "agent-a") || strings.Contains(reason, " trash ") {
		t.Errorf("an rm under conflict ask: the deny is %q; want the occupation message", reason)
	}
	w.configure(t, "")

	none := runHook(t, shortDir(t), editEvent(t, "claude-edit-1-pre-Write", inWT1, "chat-y", w.main), nil, "--client", "claude", "--root", w.main)
	if none.code != 0 || none.stdout != passOf("claude") || none.elapsed >= 2*time.Second {
		t.Errorf("with no daemon the edit hook answered %q (exit %d) in %v; want the pass-through within 2 s", none.stdout, none.code, none.elapsed)
	}

	// A routed rm.
	if err := os.WriteFile(filepath.Join(w.wt1, "old.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	reason := reasonOf(t, "claude", run("claude", bashEvent("rm old.txt", w.wt1, "chat-y")).stdout)
	if !strings.Contains(reason, "agent-a") || !strings.Contains(reason, "git worktree add") || strings.Contains(reason, " trash ") {
		t.Errorf("an rm by another chat in wt1: the deny is %q; want the occupation message, not the trash", reason)
	}
	if reason := reasonOf(t, "claude", run("claude", bashEvent("rm old.txt", w.wt1, "chat-x")).stdout); !strings.Contains(reason, " trash ") {
		t.Errorf("an rm by the holder: the deny is %q; want the trash replacement as before", reason)
	}
	// A writer that cannot be told from the holder is warned, and the warning goes before the
	// trash replacement, which still answers.
	if reason := reasonOf(t, "claude", run("claude", bashEvent("rm old.txt", w.wt1, "")).stdout); !strings.Contains(reason, "droidyard warning") || !strings.Contains(reason, " trash ") {
		t.Errorf("an rm that names no chat: the deny is %q; want the warning and the trash replacement", reason)
	}

	// After an edit.
	if err := os.MkdirAll(filepath.Join(w.wt2, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(w.wt2, "notes", "new.txt")
	if err := os.WriteFile(created, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("claude", editEvent(t, "claude-edit-2-post-Write-create", created, "chat-y", w.main))
	pl, ws, id := ops.completedEdit("agent.recordEdit")
	if rec := recordOf(t, w.state, id); ws != w.idOf(w.wt2) || rec.Client != "claude" || rec.Outside || len(rec.Files) != 1 || rec.Files[0].Path != "notes/new.txt" || !rec.Files[0].Created {
		t.Errorf("the record of a created file in wt2 is %+v in workspace %s; want notes/new.txt created, inside, in wt2's workspace %s", rec, ws, w.idOf(w.wt2))
	}
	if pl.Session == nil || pl.Session.ID != yRow.ID || pl.Session.Name != "agent-y" {
		t.Errorf("the record carries the session %+v; want agent-y's (%s)", pl.Session, yRow.ID)
	}

	run("claude", editEvent(t, "claude-edit-3-post-Write-update", created, "chat-y", w.main))
	if _, _, id := ops.completedEdit("agent.recordEdit"); recordOf(t, w.state, id).Files[0].Created {
		t.Error("a Write whose response says update is recorded as created")
	}
	run("claude", editEvent(t, "claude-edit-5-post-Edit", created, "chat-y", w.main))
	if _, _, id := ops.completedEdit("agent.recordEdit"); recordOf(t, w.state, id).Files[0].Created {
		t.Error("an Edit is recorded as created")
	}

	outside := "/tmp/droidyard-hook-edit-" + filepath.Base(w.state) + ".txt"
	run("claude", editEvent(t, "claude-edit-5-post-Edit", outside, "chat-y", w.main))
	_, ws, id = ops.completedEdit("agent.recordEdit")
	if rec := recordOf(t, w.state, id); ws != w.idOf(w.main) || !rec.Outside || len(rec.Files) != 1 || rec.Files[0].Path != outside {
		t.Errorf("the record of an edit in /tmp is %+v in workspace %s; want it outside, the absolute path %s, under --root's workspace %s", rec, ws, outside, w.idOf(w.main))
	}

	run("cursor", editEvent(t, "cursor-edit-3-post-Write", created, "chat-q", ""))
	_, ws, id = ops.completedEdit("agent.recordEdit")
	if rec := recordOf(t, w.state, id); ws != w.idOf(w.wt2) || rec.Client != "cursor" || rec.Files[0].Path != "notes/new.txt" || rec.Files[0].Created {
		t.Errorf("the record of cursor's Write is %+v in workspace %s; want notes/new.txt, client cursor, created unsaid", rec, ws)
	}

	// The file watch walks on the record, not on its hour-long tick.
	watch := wsToDaemon(t, w.state)
	watch.watchOn(t, w.wt2, ".")
	fresh := filepath.Join(w.wt2, "notes", "fresh.txt")
	if err := os.WriteFile(fresh, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if f, ok := watch.next(300 * time.Millisecond); ok {
		t.Fatalf("a change reached the watch before any record, on an hour-long tick: %+v", f)
	}
	run("claude", editEvent(t, "claude-edit-2-post-Write-create", fresh, "chat-y", w.main))
	start := time.Now()
	f, ok := watch.next(500 * time.Millisecond)
	if !ok || f.Event == nil || f.Event.Topic != "file.changes" || !strings.Contains(string(f.Event.Payload), "notes/fresh.txt") {
		t.Fatalf("no file.changes for notes/fresh.txt within 500 ms of the record: %+v (%v)", f, ok)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("the change took %v after the record", time.Since(start))
	}
}
