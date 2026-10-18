package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// enterWorld is a real `serve`, the fake sidecar, and the roots the enter tests move
// between: a trusted repository `main` with a linked worktree `wt`, a second trusted
// repository `other` with its own worktree `otherWt`, a repository `sib` that is no
// worktree of anything, and a directory `planted` whose `.git` is a directory.
type enterWorld struct {
	state, events            string
	main, wt, other, otherWt string
	sib, planted             string
	srv                      *process
}

func newEnterWorld(t *testing.T) *enterWorld {
	t.Helper()
	w := &enterWorld{state: shortDir(t)}
	var cfg string
	cfg, w.events = fakeSidecar(t, gradleRootModel)
	base := platform.Current().Canonical(t.TempDir())
	w.main, w.wt = filepath.Join(base, "main"), filepath.Join(base, "wt")
	w.other, w.otherWt = filepath.Join(base, "other"), filepath.Join(base, "otherwt")
	w.sib, w.planted = filepath.Join(base, "sib"), filepath.Join(base, "planted")
	for _, d := range []string{w.main, w.other, w.sib} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "settings.gradle.kts"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, d, "init", "-q", "-b", "main")
		gitIn(t, d, "add", "-A")
		gitIn(t, d, "commit", "-q", "-m", "c")
	}
	gitIn(t, w.main, "worktree", "add", "-q", "../wt", "-b", "task")
	gitIn(t, w.other, "worktree", "add", "-q", "../otherwt", "-b", "task")
	if err := os.MkdirAll(filepath.Join(w.planted, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	w.srv = droidyardEnv(t, w.state, "", []string{gradleFakeEnv + "=" + cfg, gradleJVMEnv + "=" + fakeJDK(t)}, "serve")
	w.srv.expect(t, "listening")
	for _, root := range []string{w.main, w.other} {
		if code := droidyard(t, w.state, "trust", root).wait(t); code != 0 {
			t.Fatalf("droidyard trust %s exited %d", root, code)
		}
	}
	return w
}

// shimAt starts an MCP shim bound at root, named for the chat when chat is not "", and
// initialised.
func (w *enterWorld) shimAt(t *testing.T, name, root, chat string) *shim {
	t.Helper()
	var env []string
	if chat != "" {
		env = []string{chatEnv + "=" + chat}
	}
	s := startShimEnv(t, w.state, root, name, env, []string{"mcp", "--no-autostart", "--root", root})
	s.initialize(name)
	return s
}

// described is where the shim's calls act now.
func described(t *testing.T, s *shim, id int) string {
	t.Helper()
	c := s.workspaceTool(id, "workspace_describe")
	if c.isError {
		t.Fatalf("%s: workspace_describe failed: %s", s.name, c.raw)
	}
	return c.info.Root
}

func (w *enterWorld) stop(t *testing.T) {
	t.Helper()
	if code := droidyard(t, w.state, "stop").wait(t); code != 0 {
		t.Errorf("stop exited %d", code)
	}
	w.srv.wait(t)
}

// TestSessionEntersAWorktree is ADR-0082 §3 to §5 end to end, on a real `serve`, real
// `git worktree add` worktrees, the fake sidecar and scripted MCP sessions. Session A,
// bound at the trusted `main`, enters `wt`: its describe, its build (the sidecar opens
// `wt`) and the session of its operations are `wt`'s, while session B, bound at `main`
// meanwhile, still describes `main`; A enters `main` again. A root that is no worktree of
// A's project fails WORKSPACE_NOT_A_WORKTREE and leaves the binding where it was: a
// genuine worktree of another trusted repository, a sibling repository, `/`, a `.git`
// directory planted in a plain directory. A session bound at `wt` by its hello may enter
// `main`: its project is `main`. The CLI and the WebSocket cannot call it: they are
// refused with REQUEST_NOT_ALLOWED_HERE and no operation of workspace.enter exists.
//
// Seen failing: with the binding kept on the adapter Server instead of per session, B
// described `wt` after A's enter; with the comparison to the session's project removed
// (any genuine worktree accepted), `otherWt` was entered; with Start's Binding check
// removed, the CLI's call reached the handler and failed WORKSPACE_NOT_A_WORKTREE, and
// the failed workspace.enter was recorded as an operation.
func TestSessionEntersAWorktree(t *testing.T) {
	t.Parallel()
	w := newEnterWorld(t)
	idOf := func(root string) string {
		id, _ := platform.Current().WorkspaceID(root)
		return id
	}

	// Every operation the daemon records, from here on.
	ws := wsToDaemon(t, w.state)
	ws.send("watch", "operation.watch", "", `{}`, nil)
	_, f := ws.collect("watch")
	var sub struct {
		SubscriptionID string `json:"subscriptionId"`
	}
	if err := json.Unmarshal(f.Result, &sub); err != nil || sub.SubscriptionID == "" {
		t.Fatalf("operation.watch answered %s: %v", f.Result, err)
	}
	var recorded []capability.OperationState
	record := func(fr wsFrame) {
		if fr.Event == nil || fr.SubscriptionID != sub.SubscriptionID {
			return
		}
		var pl capability.OperationState
		if err := json.Unmarshal(fr.Event.Payload, &pl); err != nil {
			t.Fatal(err)
		}
		recorded = append(recorded, pl)
	}
	// readUntil reads operation.state events until done holds for what has been recorded.
	readUntil := func(what string, done func() bool) {
		t.Helper()
		for !done() {
			record(ws.must(what))
		}
	}
	has := func(match func(capability.OperationState) bool) bool {
		for _, op := range recorded {
			if match(op) {
				return true
			}
		}
		return false
	}

	a := w.shimAt(t, "a", w.main, "")
	if got := described(t, a, 1); got != w.main {
		t.Fatalf("A starts at %s; want %s", got, w.main)
	}
	// B is bound at main before A moves, and stays there.
	b := w.shimAt(t, "b", w.main, "")
	entered := a.toolWith(2, "workspace_enter", `{"root":"`+w.wt+`"}`)
	if entered.isError || entered.info.Root != w.wt || entered.info.MainRoot != w.main || !entered.info.Trusted || entered.info.ID != idOf(w.wt) {
		t.Fatalf("workspace_enter wt = %s; want wt's workspace, trusted, with mainRoot %s", entered.raw, w.main)
	}
	if got := described(t, a, 3); got != w.wt {
		t.Errorf("A's workspace_describe after entering is %s; want %s", got, w.wt)
	}
	if c := a.toolWith(4, "build_tasks", `{}`); c.isError {
		t.Fatalf("A's build_tasks in the worktree failed: %s", c.raw)
	}
	if lines := eventLines(t, w.events); !contains(lines, "opened "+w.wt) || contains(lines, "opened "+w.main) {
		t.Errorf("the sidecar's log is %q; want A's build to have opened %s and never %s", lines, w.wt, w.main)
	}

	if got := described(t, b, 1); got != w.main {
		t.Errorf("B, bound at %s, describes %s after A entered %s; want its own binding", w.main, got, w.wt)
	}
	if got := described(t, a, 5); got != w.wt {
		t.Errorf("A describes %s after B described; want %s", got, w.wt)
	}

	// The session of A's operations follows the move: its build.tasks names wt's workspace.
	readUntil("A's build.tasks", func() bool {
		return has(func(op capability.OperationState) bool {
			return op.Capability == "build.tasks" && op.State == capability.StateCompleted
		})
	})
	for _, op := range recorded {
		if op.Capability == "build.tasks" && (op.Session == nil || op.Session.Workspace != idOf(w.wt)) {
			t.Errorf("A's build.tasks %s carries session %+v; want workspace %s", op.State, op.Session, idOf(w.wt))
		}
	}

	// Back to the main tree.
	if back := a.toolWith(6, "workspace_enter", `{"root":"`+w.main+`"}`); back.isError || back.info.Root != w.main {
		t.Fatalf("workspace_enter main = %s; want the main tree", back.raw)
	}
	if got := described(t, a, 7); got != w.main {
		t.Errorf("A describes %s after entering the main tree; want %s", got, w.main)
	}

	// Roots that are no worktree of A's project: refused, and the binding stays.
	if c := a.toolWith(8, "workspace_enter", `{"root":"`+w.wt+`"}`); c.isError {
		t.Fatalf("A could not re-enter wt: %s", c.raw)
	}
	id := 10
	for name, root := range map[string]string{
		"a genuine worktree of another trusted repository": w.otherWt,
		"the other repository's main tree":                 w.other,
		"a sibling repository":                             w.sib,
		"the filesystem root":                              "/",
		"a planted .git directory":                         w.planted,
	} {
		c := a.toolWith(id, "workspace_enter", `{"root":"`+root+`"}`)
		id++
		if !c.isError || c.err.Code != capability.ErrWorkspaceNotAWorktree {
			t.Errorf("entering %s: %s; want WORKSPACE_NOT_A_WORKTREE", name, c.raw)
		}
		if got := described(t, a, id); got != w.wt {
			t.Errorf("after refusing %s A describes %s; want it still at %s", name, got, w.wt)
		}
		id++
	}
	if c := a.toolWith(id, "workspace_enter", `{"root":"wt"}`); !c.isError || c.err.Code != "WORKSPACE_ROOT_INVALID" {
		t.Errorf("entering a relative path: %s; want WORKSPACE_ROOT_INVALID", c.raw)
	}
	id++

	// A session bound at a worktree by its hello belongs to the same project.
	c := w.shimAt(t, "c", w.wt, "")
	if got := described(t, c, 1); got != w.wt {
		t.Errorf("C starts at %s; want its hello's %s", got, w.wt)
	}
	if back := c.toolWith(2, "workspace_enter", `{"root":"`+w.main+`"}`); back.isError || back.info.Root != w.main {
		t.Errorf("C, bound at wt, entering its project's main tree: %s; want allowed", back.raw)
	}

	// The CLI and the WebSocket have no session whose binding could move.
	env, code := callIn(t, w.state, w.main, "workspace.enter", "--params", `{"root":"`+w.wt+`"}`)
	if code == 0 || env.Error == nil || env.Error.Code != capability.ErrNotAllowedHere {
		t.Errorf("droidyard call workspace.enter = (%+v, exit %d); want REQUEST_NOT_ALLOWED_HERE", env.Error, code)
	}
	ws.send("e", "workspace.enter", "", `{"root":"`+w.wt+`"}`, nil)
	events, f := ws.collect("e")
	for _, fr := range events {
		record(fr)
	}
	if f.Error == nil || f.Error.Code != capability.ErrNotAllowedHere {
		t.Errorf("the WebSocket's workspace.enter = %+v; want REQUEST_NOT_ALLOWED_HERE", f.Error)
	}
	// A marker call, so every event of the refused calls has been read by now.
	ws.send("marker", "system.health", "", `{}`, nil)
	readUntil("the marker", func() bool {
		return has(func(op capability.OperationState) bool {
			return op.Capability == "system.health" && op.State == capability.StateCompleted
		})
	})
	for _, op := range recorded {
		if op.Capability == "workspace.enter" && op.Client != capability.ClientMCP {
			t.Errorf("a refused %s call was recorded as an operation: %+v", op.Client, op)
		}
	}
	w.stop(t)
}

// TestResumedChatRecoversItsBinding is ADR-0088 end to end. The shim names its chat from
// CLAUDE_CODE_SESSION_ID. A session of chat X enters `wt` and ends; a new session of chat X,
// hello at `main`, describes `wt` as its very first call, with no workspace_enter sent.
// Held to ADR-0088 §3's conditions: a chat whose worktree is gone is bound at `main` and its
// memory dropped, so a worktree made later at the same path is not entered by it; a chat
// remembered under one project is not moved when its new session starts in another project;
// a session with no variable, or one the pattern refuses, is bound as before; and a chat
// that moves back to `main` is remembered there.
//
// Seen failing: with the memory not written (Sessions.Bound remembering nothing) the
// resumed session described `main`; with the project test removed (any genuine worktree
// accepted) the session started in `other` was moved into `wt`; with the dropped entry kept
// (Forget removed) the chat recreated at the same path was entered again.
func TestResumedChatRecoversItsBinding(t *testing.T) {
	t.Parallel()
	w := newEnterWorld(t)

	// A chat resumed: the new connection starts where the old one last was.
	first := w.shimAt(t, "first", w.main, "chat-resume")
	if c := first.toolWith(1, "workspace_enter", `{"root":"`+w.wt+`"}`); c.isError {
		t.Fatalf("first could not enter wt: %s", c.raw)
	}
	if code := first.exit(); code != 0 {
		t.Fatalf("first exited %d", code)
	}
	resumed := w.shimAt(t, "resumed", w.main, "chat-resume")
	if got := described(t, resumed, 1); got != w.wt {
		t.Errorf("the resumed chat's first call describes %s; want its worktree %s", got, w.wt)
	}
	other := w.shimAt(t, "another", w.main, "chat-other")
	if got := described(t, other, 1); got != w.main {
		t.Errorf("a different chat starts at %s; want the main tree %s", got, w.main)
	}
	resumed.exit()
	other.exit()

	// A chat that went back to the main tree is remembered there.
	back := w.shimAt(t, "back", w.main, "chat-back")
	back.toolWith(1, "workspace_enter", `{"root":"`+w.wt+`"}`)
	back.toolWith(2, "workspace_enter", `{"root":"`+w.main+`"}`)
	back.exit()
	if got := described(t, w.shimAt(t, "back2", w.wt, "chat-back"), 1); got != w.main {
		t.Errorf("a chat whose last move was to the main tree starts at %s; want %s", got, w.main)
	}

	// A chat remembered under one project does not follow a session of another.
	foreign := w.shimAt(t, "foreign", w.main, "chat-foreign")
	foreign.toolWith(1, "workspace_enter", `{"root":"`+w.wt+`"}`)
	foreign.exit()
	elsewhere := w.shimAt(t, "elsewhere", w.other, "chat-foreign")
	if got := described(t, elsewhere, 1); got != w.other {
		t.Errorf("chat-foreign, started in %s, describes %s; want its own project, never %s", w.other, got, w.wt)
	}
	elsewhere.exit()

	// No variable, or a value the pattern refuses: bound as the hello says.
	plain := w.shimAt(t, "plain", w.main, "")
	if got := described(t, plain, 1); got != w.main {
		t.Errorf("a session with no chat describes %s; want %s", got, w.main)
	}
	bad := w.shimAt(t, "bad", w.main, "not a chat id!")
	bad.toolWith(1, "workspace_enter", `{"root":"`+w.wt+`"}`)
	bad.exit()
	if got := described(t, w.shimAt(t, "bad2", w.main, "not a chat id!"), 1); got != w.main {
		t.Errorf("a chat id the pattern refuses was remembered: describes %s; want %s", got, w.main)
	}

	// A worktree that is gone: the chat is bound at the main tree and its memory is
	// dropped, so the same path made again later is not entered on its own.
	gone := w.shimAt(t, "gone", w.main, "chat-gone")
	gone.toolWith(1, "workspace_enter", `{"root":"`+w.wt+`"}`)
	gone.exit()
	if err := os.RemoveAll(w.wt); err != nil {
		t.Fatal(err)
	}
	gitIn(t, w.main, "worktree", "prune")
	afterRemoval := w.shimAt(t, "after-removal", w.main, "chat-gone")
	if got := described(t, afterRemoval, 1); got != w.main {
		t.Errorf("chat-gone, its worktree removed, describes %s; want %s", got, w.main)
	}
	afterRemoval.exit()
	gitIn(t, w.main, "worktree", "add", "-q", "../wt", "-b", "task2")
	again := w.shimAt(t, "again", w.main, "chat-gone")
	if got := described(t, again, 1); got != w.main {
		t.Errorf("chat-gone, its worktree made again at the same path, describes %s; want %s: the entry was dropped", got, w.main)
	}
	w.stop(t)
}
