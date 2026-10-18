package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/workspace"
)

// The environment a re-executed daemon reads the session registry's clock and sweep from:
// the first names a file holding the seconds to add to the real time, the second the
// interval of the idle sweep (ADR-0094 §4).
const (
	sessionClockEnv = "DROIDYARD_TEST_SESSION_CLOCK"
	sessionSweepEnv = "DROIDYARD_TEST_SESSION_SWEEP"
)

// occupationWorld is a real `serve` with a trusted repository `main` and two linked
// worktrees `wt1` and `wt2`, whose configuration the test rewrites and whose session
// clock it moves.
type occupationWorld struct {
	state          string
	main, wt1, wt2 string
	clock          string
	srv            *process
	idOf           func(root string) string
}

func newOccupationWorld(t *testing.T) *occupationWorld {
	t.Helper()
	w := &occupationWorld{state: shortDir(t)}
	cfg, _ := fakeSidecar(t, gradleRootModel)
	base := platform.Current().Canonical(t.TempDir())
	w.main, w.wt1, w.wt2 = filepath.Join(base, "main"), filepath.Join(base, "wt1"), filepath.Join(base, "wt2")
	if err := os.MkdirAll(w.main, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.main, "settings.gradle.kts"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, w.main, "init", "-q", "-b", "main")
	gitIn(t, w.main, "add", "-A")
	gitIn(t, w.main, "commit", "-q", "-m", "c")
	gitIn(t, w.main, "worktree", "add", "-q", "../wt1", "-b", "task1")
	gitIn(t, w.main, "worktree", "add", "-q", "../wt2", "-b", "task2")
	w.idOf = func(root string) string {
		id, _ := platform.Current().WorkspaceID(root)
		return id
	}
	w.clock = filepath.Join(base, "clock")
	w.advanceTo(0)
	w.configure(t, "")
	w.srv = droidyardEnv(t, w.state, "", []string{gradleFakeEnv + "=" + cfg, gradleJVMEnv + "=" + fakeJDK(t),
		sessionClockEnv + "=" + w.clock, sessionSweepEnv + "=50ms"}, "serve")
	w.srv.expect(t, "listening")
	return w
}

// configure writes config.json: main trusted, and occupation as the section's JSON ("" for
// none, the defaults). The daemon re-reads it by size and mtime.
func (w *occupationWorld) configure(t *testing.T, occupation string) {
	t.Helper()
	body := fmt.Sprintf(`{"schemaVersion":1,"trust":{"roots":[%q],"prefixes":[]}`, w.main)
	if occupation != "" {
		body += `,"occupation":` + occupation
	}
	body += "}"
	dir := filepath.Join(w.state, "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// advanceTo sets the daemon's session clock to the real time plus secs.
func (w *occupationWorld) advanceTo(secs int) {
	if err := os.WriteFile(w.clock, []byte(strconv.Itoa(secs)), 0o600); err != nil {
		panic(err)
	}
}

// agent is a connected, initialised MCP session named name, of chat, bound at root.
func (w *occupationWorld) agent(t *testing.T, name, chat, root string) *shim {
	t.Helper()
	var env []string
	if chat != "" {
		env = []string{chatEnv + "=" + chat}
	}
	s := startShimEnv(t, w.state, root, name, env, []string{"mcp", "--no-autostart", "--root", root})
	s.initialize(name)
	w.waitRow(t, name, "its row", func(capability.SessionListed) bool { return true })
	return s
}

// rows is the sessions the daemon lists now.
func (w *occupationWorld) rows(t *testing.T) []capability.SessionListed {
	t.Helper()
	_, rows, _ := watchSessions(t, wsToDaemon(t, w.state), "occ")
	return rows
}

// row is the row named name, if one is listed. Names are unique in this test.
func (w *occupationWorld) row(t *testing.T, name string) (capability.SessionListed, bool) {
	t.Helper()
	for _, r := range w.rows(t) {
		if r.Name == name {
			return r, true
		}
	}
	return capability.SessionListed{}, false
}

// waitRow waits for the row named name to exist and satisfy ok.
func (w *occupationWorld) waitRow(t *testing.T, name, what string, ok func(capability.SessionListed) bool) capability.SessionListed {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if r, found := w.row(t, name); found && ok(r) {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %s never came; the rows are %+v", name, what, w.rows(t))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// waitGone waits for the session named name to have no row.
func (w *occupationWorld) waitGone(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, found := w.row(t, name); !found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s still has a row after its shim ended", name)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// kill ends the shim hard and waits for the daemon to drop the session.
func (w *occupationWorld) kill(t *testing.T, s *shim) {
	t.Helper()
	s.cmd.Process.Kill()
	w.waitGone(t, s.name)
}

// occupies waits for the row named name to hold the workspace of root, or none for "".
func (w *occupationWorld) occupies(t *testing.T, name, root string) capability.SessionListed {
	t.Helper()
	want := ""
	if root != "" {
		want = w.idOf(root)
	}
	return w.waitRow(t, name, "occupies "+want, func(r capability.SessionListed) bool { return r.Occupies == want })
}

// enter is workspace_enter of root by s.
func (w *occupationWorld) enter(t *testing.T, s *shim, id int, root string) boundCall {
	t.Helper()
	return s.toolWith(id, "workspace_enter", `{"root":"`+root+`"}`)
}

// describedWith is workspace_describe as the shim sees it: the root it acts on and the worktrees.
func describedWith(t *testing.T, s *shim, id int) (string, []workspace.WorktreeInfo) {
	t.Helper()
	s.callTool(id, "workspace_describe", `{}`)
	line := s.recv()
	var msg struct {
		Result struct {
			IsError           bool            `json:"isError"`
			StructuredContent json.RawMessage `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line, &msg); err != nil || msg.Result.IsError {
		t.Fatalf("%s: workspace_describe: %s (%v)", s.name, line, err)
	}
	var d workspace.Described
	if err := json.Unmarshal(msg.Result.StructuredContent, &d); err != nil {
		t.Fatalf("%s: workspace_describe's result: %s (%v)", s.name, line, err)
	}
	return d.Root, d.Worktrees
}

// occupiedBy is the occupiedBy of root among worktrees, nil when it has none.
func occupiedBy(worktrees []workspace.WorktreeInfo, root string) *workspace.OccupiedBy {
	for _, wi := range worktrees {
		if wi.Root == root {
			return wi.OccupiedBy
		}
	}
	return nil
}

// TestWorktreeOccupation is ADR-0094 §1 to §7 for worktrees, end to end: a real `serve`,
// real `git worktree add` worktrees, a trusted `main`, and scripted MCP shims whose
// session.watch rows are read back. The stages, each on the one daemon:
//
//   - a hello bound in a free linked worktree holds it, and one bound in a worktree
//     another session holds is bound, not holding;
//   - A enters wt1 and holds it (its row's occupies is wt1's id); B entering wt1 fails
//     WORKSPACE_OCCUPIED naming A and B still describes main; B's describe lists wt1 with
//     occupiedBy.name A, and A's does not; entering the main tree, again and again, holds
//     nothing;
//   - A enters wt2 and frees wt1, B enters it; A's shim killed ends A and frees wt2, C
//     enters it;
//   - with staleAfterMinutes 1 and the clock moved, the holder's row is stale; a session
//     entering its worktree takes it, the idle row loses occupies, and its next call
//     clears stale;
//   - allow binds without holding, and ask refuses as refuse does;
//   - two live sessions of one chat share what either holds;
//   - a chat that ended in wt1 and finds another chat holding it is bound at main with the
//     memory kept, and is back in wt1 once the other moves away.
//
// Seen failing: with Occupy's refusal removed, B's enter completed and B described wt1;
// with the linked-worktree test removed (every root held), A's and B's repeated enters of
// main refused the second; with the restore unchanged (Forget on any failed enter), the
// chat's next session was bound at main after the other chat moved away.
func TestWorktreeOccupation(t *testing.T) {
	t.Parallel()
	w := newOccupationWorld(t)
	watcher := wsToDaemon(t, w.state)
	sub, _, _ := watchSessions(t, watcher, "events")

	// A hello bound in a linked worktree.
	h1 := w.agent(t, "hello-1", "chat-h1", w.wt2)
	w.occupies(t, "hello-1", w.wt2)
	h2 := w.agent(t, "hello-2", "chat-h2", w.wt2)
	if r := w.occupies(t, "hello-2", ""); r.Workspace != w.idOf(w.wt2) {
		t.Errorf("hello-2 is bound to %s; want wt2 (%s)", r.Workspace, w.idOf(w.wt2))
	}
	if root, _ := describedWith(t, h2, 10); root != w.wt2 {
		t.Errorf("hello-2 describes %s; want wt2", root)
	}
	w.kill(t, h1)
	w.kill(t, h2)

	// A and B at the main tree.
	a := w.agent(t, "agent-a", "chat-a", w.main)
	b := w.agent(t, "agent-b", "chat-b", w.main)
	for i := 0; i < 3; i++ {
		for j, s := range []*shim{a, b} {
			if c := w.enter(t, s, 20+10*i+j, w.main); c.isError {
				t.Fatalf("%s entering the main tree (%d): %s", s.name, i, c.raw)
			}
		}
	}
	for _, name := range []string{"agent-a", "agent-b"} {
		if r, _ := w.row(t, name); r.Occupies != "" {
			t.Errorf("%s occupies %q after entering the main tree; want nothing: the main tree is never held", name, r.Occupies)
		}
	}

	// A holds wt1; B is refused, naming A, and stays at main.
	if c := w.enter(t, a, 100, w.wt1); c.isError {
		t.Fatalf("a enters its free worktree: %s", c.raw)
	}
	w.occupies(t, "agent-a", w.wt1)
	c := w.enter(t, b, 101, w.wt1)
	if !c.isError || c.err.Code != capability.ErrWorkspaceOccupied {
		t.Fatalf("b enters what a holds: %s; want WORKSPACE_OCCUPIED", c.raw)
	}
	if !strings.Contains(c.err.Message, "another chat (agent-a, since ") || !strings.Contains(c.err.NextStep, "workspace_enter") ||
		!strings.Contains(c.err.NextStep, "release "+w.wt1) {
		t.Errorf("the refusal is %+v; want the holder's name and the way out", c.err)
	}
	for _, secret := range []string{"chat-a", "chat-b"} {
		if strings.Contains(string(c.raw), secret) {
			t.Errorf("the refusal carries a chat id: %s", c.raw)
		}
	}
	root, worktrees := describedWith(t, b, 102)
	if root != w.main {
		t.Errorf("b describes %s after its refusal; want the main tree", root)
	}
	if by := occupiedBy(worktrees, w.wt1); by == nil || by.Name != "agent-a" || by.Since.IsZero() || by.Stale {
		t.Errorf("b's describe lists wt1 occupiedBy %+v; want agent-a, with when, not stale", by)
	}
	if by := occupiedBy(worktrees, w.wt2); by != nil {
		t.Errorf("b's describe lists the free wt2 occupiedBy %+v", by)
	}
	if _, own := describedWith(t, a, 103); occupiedBy(own, w.wt1) != nil {
		t.Errorf("a's describe says its own worktree is occupied: %+v", occupiedBy(own, w.wt1))
	}
	if r, _ := w.row(t, "agent-b"); r.Occupies != "" || r.Workspace != w.idOf(w.main) {
		t.Errorf("b's row after the refusal is %+v; want bound to main, occupying nothing", r)
	}

	// A moves on to wt2: wt1 is free for B. A is killed: wt2 is free for C.
	if c := w.enter(t, a, 110, w.wt2); c.isError {
		t.Fatalf("a enters wt2: %s", c.raw)
	}
	w.occupies(t, "agent-a", w.wt2)
	if c := w.enter(t, b, 111, w.wt1); c.isError {
		t.Fatalf("b enters wt1 after a left it: %s", c.raw)
	}
	w.occupies(t, "agent-b", w.wt1)
	aID := w.occupies(t, "agent-a", w.wt2).ID
	cc := w.agent(t, "agent-c", "chat-c", w.main)
	if c := w.enter(t, cc, 120, w.wt2); !c.isError || c.err.Code != capability.ErrWorkspaceOccupied {
		t.Fatalf("c enters what a holds: %s; want WORKSPACE_OCCUPIED", c.raw)
	}
	w.kill(t, a)
	for ended := false; !ended; {
		st, _ := nextSession(t, watcher, sub)
		ended = st.ID == aID && st.Ended
	}
	if c := w.enter(t, cc, 121, w.wt2); c.isError {
		t.Fatalf("c enters wt2 after a ended: %s", c.raw)
	}
	w.occupies(t, "agent-c", w.wt2)

	// Idleness: one minute is the threshold, the clock moves two. Every session is then
	// idle; B, which holds wt1, is taken over by C, and B's next call clears its idleness.
	w.configure(t, `{"staleAfterMinutes":1}`)
	w.advanceTo(120)
	w.waitRow(t, "agent-b", "stale", func(r capability.SessionListed) bool { return r.Stale && r.Occupies == w.idOf(w.wt1) })
	if _, wts := describedWith(t, cc, 130); occupiedBy(wts, w.wt1) == nil || !occupiedBy(wts, w.wt1).Stale {
		t.Errorf("c is told wt1 is occupied by %+v; want its holder stale", occupiedBy(wts, w.wt1))
	}
	if c := w.enter(t, cc, 131, w.wt1); c.isError {
		t.Fatalf("c enters an idle holder's worktree: %s", c.raw)
	}
	w.occupies(t, "agent-c", w.wt1)
	if r := w.occupies(t, "agent-b", ""); !r.Stale {
		t.Errorf("b's row is %+v after the takeover; want it still stale, without occupies", r)
	}
	if root, _ := describedWith(t, b, 132); root != w.wt1 {
		t.Errorf("b describes %s after being taken over; want it still bound to wt1", root)
	}
	w.waitRow(t, "agent-b", "stale cleared", func(r capability.SessionListed) bool { return !r.Stale })
	w.configure(t, "")

	// allow binds without holding; ask refuses as refuse does.
	w.configure(t, `{"conflict":"allow"}`)
	d := w.agent(t, "agent-d", "chat-d", w.main)
	if c := w.enter(t, d, 140, w.wt1); c.isError {
		t.Fatalf("allow: d is refused wt1: %s", c.raw)
	}
	if root, _ := describedWith(t, d, 141); root != w.wt1 {
		t.Errorf("allow: d describes %s; want wt1", root)
	}
	w.occupies(t, "agent-d", "")
	w.occupies(t, "agent-c", w.wt1)
	w.configure(t, `{"conflict":"ask"}`)
	e := w.agent(t, "agent-e", "chat-e", w.main)
	if c := w.enter(t, e, 150, w.wt1); !c.isError || c.err.Code != capability.ErrWorkspaceOccupied {
		t.Errorf("ask: e entering wt1: %s; want WORKSPACE_OCCUPIED as under refuse", c.raw)
	}
	w.configure(t, "")
	for _, s := range []*shim{b, cc, d, e} {
		w.kill(t, s)
	}

	// Two live sessions of one chat share what either holds.
	x1 := w.agent(t, "x-one", "chat-x", w.main)
	x2 := w.agent(t, "x-two", "chat-x", w.main)
	if c := w.enter(t, x1, 160, w.wt1); c.isError {
		t.Fatalf("x1 enters wt1: %s", c.raw)
	}
	w.occupies(t, "x-one", w.wt1)
	if c := w.enter(t, x2, 161, w.wt1); c.isError {
		t.Fatalf("x2 is refused what its own chat holds: %s", c.raw)
	}
	w.occupies(t, "x-two", "")
	if _, wts := describedWith(t, x2, 162); occupiedBy(wts, w.wt1) != nil {
		t.Errorf("x2 is told its own chat occupies wt1: %+v", occupiedBy(wts, w.wt1))
	}
	// The first connection ends: the chat still holds wt1, now through the second, and
	// another chat is still refused.
	y := w.agent(t, "y-one", "chat-y", w.main)
	w.kill(t, x1)
	w.occupies(t, "x-two", w.wt1)
	if c := w.enter(t, y, 163, w.wt1); !c.isError || c.err.Code != capability.ErrWorkspaceOccupied {
		t.Errorf("y enters wt1 once x1 ended: %s; want WORKSPACE_OCCUPIED, the chat holds it through x2", c.raw)
	}
	// The same when the first connection moves to the main tree instead.
	x3 := w.agent(t, "x-three", "chat-x", w.wt1)
	w.occupies(t, "x-three", "")
	if c := w.enter(t, x2, 164, w.main); c.isError {
		t.Fatalf("x2 enters main: %s", c.raw)
	}
	w.occupies(t, "x-three", w.wt1)
	if c := w.enter(t, y, 165, w.wt1); !c.isError || c.err.Code != capability.ErrWorkspaceOccupied {
		t.Errorf("y enters wt1 once x2 moved to main: %s; want WORKSPACE_OCCUPIED, the chat holds it through x3", c.raw)
	}
	w.kill(t, x2)
	w.kill(t, x3)
	w.kill(t, y)

	// A resumed chat: chat-r ends in wt1, chat-s enters it, chat-r's next session is bound
	// at main (and keeps its memory); once chat-s moves away, the session after it is back.
	r1 := w.agent(t, "r-one", "chat-r", w.main)
	if c := w.enter(t, r1, 170, w.wt1); c.isError {
		t.Fatalf("r1 enters wt1: %s", c.raw)
	}
	w.kill(t, r1)
	s1 := w.agent(t, "s-one", "chat-s", w.main)
	if c := w.enter(t, s1, 171, w.wt1); c.isError {
		t.Fatalf("s1 enters wt1 once r1 ended: %s", c.raw)
	}
	r2 := w.agent(t, "r-two", "chat-r", w.main)
	if root, _ := describedWith(t, r2, 172); root != w.main {
		t.Errorf("the resumed chat is bound at %s while another chat holds wt1; want the main tree", root)
	}
	w.occupies(t, "r-two", "")
	if c := w.enter(t, s1, 173, w.main); c.isError {
		t.Fatalf("s1 enters main: %s", c.raw)
	}
	w.occupies(t, "s-one", "")
	r3 := w.agent(t, "r-three", "chat-r", w.main)
	if root, _ := describedWith(t, r3, 174); root != w.wt1 {
		t.Errorf("the next session of the chat is bound at %s; want wt1, its memory kept", root)
	}
	w.occupies(t, "r-three", w.wt1)

	if code := droidyard(t, w.state, "stop").wait(t); code != 0 {
		t.Errorf("stop exited %d", code)
	}
	w.srv.wait(t)
}
