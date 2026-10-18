package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// occupationWorld is a repository with two linked worktrees and sessions connected at its
// main tree, driven through workspace.enter itself.
type occupationWorld struct {
	h          *watchHarness
	main, a, b string
	mainID     string
	clockMu    sync.Mutex
	clock      time.Time
	sessions   *capability.Sessions
}

func newOccupationWorld(t *testing.T, conflict string) *occupationWorld {
	t.Helper()
	w := &occupationWorld{h: newWatchHarness(t, nil), clock: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)}
	base := canonicalBase(t)
	w.main = repoWithCommit(t, base)
	w.a, w.b = filepath.Join(base, "a"), filepath.Join(base, "b")
	gitHere(t, w.main, "worktree", "add", "-q", "-b", "task/a", w.a)
	gitHere(t, w.main, "worktree", "add", "-q", "-b", "task/b", w.b)
	w.mainID = w.h.open(t, w.main).ID
	w.sessions = w.h.ops.Sessions()
	w.sessions.UseClock(func() time.Time {
		w.clockMu.Lock()
		defer w.clockMu.Unlock()
		return w.clock
	})
	w.sessions.UseOccupation(func() (string, time.Duration) { return conflict, time.Hour })
	return w
}

func (w *occupationWorld) advance(d time.Duration) {
	w.clockMu.Lock()
	w.clock = w.clock.Add(d)
	w.clockMu.Unlock()
}

// connect registers a session of chat named name, bound at the main tree.
func (w *occupationWorld) connect(id, chat, name string) {
	w.sessions.Connect(id, chat)
	w.sessions.Named(id, name)
	w.sessions.Opened(id, w.main, w.mainID, w.main)
}

// enter is session id's workspace.enter of root: the error it failed with, if any.
func (w *occupationWorld) enter(t *testing.T, id, root string) *capability.Error {
	t.Helper()
	params, _ := json.Marshal(EnterParams{Root: root})
	out, err := w.h.ops.Invoke(context.Background(), capability.Request{Capability: CapEnter, Params: params,
		Client: capability.ClientMCP, Origin: capability.Origin{ID: id}})
	if err != nil {
		t.Fatalf("%s enters %s: %v", id, root, err)
	}
	if out.State == capability.StateCompleted {
		return nil
	}
	if out.Error == nil {
		t.Fatalf("%s enters %s: %s without an error", id, root, out.State)
	}
	return out.Error
}

// TestEnterOccupiesTheWorktree is ADR-0094 §1 to §6 at workspace.enter: a session that
// enters a linked worktree holds it and another chat's enter there fails
// WORKSPACE_OCCUPIED, naming "another chat" with the holder's client name and since when,
// and a way out, the binding unchanged; entering the main tree frees it; an idle holder
// is taken over; allow binds without holding; and workspace.describe lists the held
// worktree with occupiedBy to the other chat and not to the holder. Seen failing with the
// refusal removed from enter: the second enter completed and bound.
func TestEnterOccupiesTheWorktree(t *testing.T) {
	t.Parallel()
	w := newOccupationWorld(t, capability.OccupationRefuse)
	w.connect("a", "chat-a", "claude-code")
	w.connect("b", "chat-b", "claude-code")

	// The main tree is never held: both enter it again and again, none is refused.
	for i := 0; i < 3; i++ {
		for _, id := range []string{"a", "b"} {
			if e := w.enter(t, id, w.main); e != nil {
				t.Fatalf("%s enters the main tree (%d): %+v", id, i, e)
			}
		}
	}

	if e := w.enter(t, "a", w.a); e != nil {
		t.Fatalf("a enters its free worktree: %+v", e)
	}
	e := w.enter(t, "b", w.a)
	if e == nil || e.Code != capability.ErrWorkspaceOccupied {
		t.Fatalf("b enters what a holds: %+v; want WORKSPACE_OCCUPIED", e)
	}
	for _, want := range []string{"another chat (claude-code, since 2026-10-18T09:00:00Z)", w.a} {
		if !strings.Contains(e.Message, want) {
			t.Errorf("message %q lacks %q", e.Message, want)
		}
	}
	for _, want := range []string{"git worktree add", "workspace_enter", "release " + w.a} {
		if !strings.Contains(e.NextStep, want) {
			t.Errorf("nextStep %q lacks %q", e.NextStep, want)
		}
	}
	if strings.Contains(e.Message+e.NextStep, "chat-a") || strings.Contains(e.Message+e.NextStep, "\"a\"") {
		t.Errorf("the refusal carries a chat or session id: %+v", e)
	}
	if root, _ := w.sessions.Binding("b"); root != w.main {
		t.Errorf("b is bound to %s after a refusal; want the main tree", root)
	}

	// describe: b is told, a is not; the main tree is never listed as held.
	described := func(id string) []WorktreeInfo {
		out, err := w.h.ops.Invoke(context.Background(), capability.Request{Capability: CapDescribe, Workspace: w.main,
			Client: capability.ClientMCP, Origin: capability.Origin{ID: id}})
		if err != nil || out.State != capability.StateCompleted {
			t.Fatalf("describe as %s: %v %+v", id, err, out)
		}
		return out.Result.(Described).Worktrees
	}
	for _, wi := range described("b") {
		switch {
		case wi.Root == w.a && (wi.OccupiedBy == nil || wi.OccupiedBy.Name != "claude-code" || wi.OccupiedBy.Since.IsZero() || wi.OccupiedBy.Stale):
			t.Errorf("b is told %+v of %s; want a's name and when, not stale", wi.OccupiedBy, w.a)
		case wi.Root == w.b && wi.OccupiedBy != nil:
			t.Errorf("the free worktree %s is listed as held: %+v", w.b, wi.OccupiedBy)
		}
	}
	for _, wi := range described("a") {
		if wi.OccupiedBy != nil {
			t.Errorf("a is told it occupies %s: %+v", wi.Root, wi.OccupiedBy)
		}
	}
	raw, _ := json.Marshal(described(""))
	if !strings.Contains(string(raw), `"occupiedBy":{"name":"claude-code","since":"2026-10-18T09:00:00Z"}`) {
		t.Errorf("the UI's describe wrote %s; want a's occupiedBy on the wire", raw)
	}

	// The holder goes to the main tree: it is free, and b enters it.
	if e := w.enter(t, "a", w.main); e != nil {
		t.Fatalf("a enters the main tree: %+v", e)
	}
	if e := w.enter(t, "b", w.a); e != nil {
		t.Fatalf("b enters what a freed: %+v", e)
	}

	// An idle holder is taken over (b, holding a's old worktree, made no request for two hours).
	w.advance(2 * time.Hour)
	w.sessions.Seen("a") // a is kept fresh, b is not
	w.sessions.Sweep()
	for _, wi := range described("a") {
		if wi.Root == w.a && (wi.OccupiedBy == nil || !wi.OccupiedBy.Stale) {
			t.Errorf("a is told %+v of %s; want b stale", wi.OccupiedBy, w.a)
		}
	}
	if e := w.enter(t, "a", w.a); e != nil {
		t.Errorf("a enters an idle holder's worktree: %+v", e)
	}
}

// TestEnterUnderAllowBindsWithoutHolding holds ADR-0094 §5: under allow another chat
// enters a held worktree and is bound there without holding it, and the first still holds
// it, so describe still names the first holder to the second.
func TestEnterUnderAllowBindsWithoutHolding(t *testing.T) {
	t.Parallel()
	w := newOccupationWorld(t, capability.OccupationAllow)
	w.connect("a", "chat-a", "claude-code")
	w.connect("b", "chat-b", "cursor")
	if e := w.enter(t, "a", w.a); e != nil {
		t.Fatal(e)
	}
	if e := w.enter(t, "b", w.a); e != nil {
		t.Fatalf("allow: b was refused: %+v", e)
	}
	if root, _ := w.sessions.Binding("b"); root != w.a {
		t.Errorf("allow: b is bound to %s; want %s", root, w.a)
	}
	if h, ok := w.sessions.HolderOf(w.a, "b"); !ok || h.Name != "claude-code" {
		t.Errorf("allow: %s is held by %+v, %v; want a still", w.a, h, ok)
	}
}

// TestEnterCannotDecideInsideItsOwnWindow holds ADR-0094 §1 through workspace.enter
// itself: while one session's enter stands between the registry's decision and its effect
// (the occupy hook, with the lock held), a second session's enter of the same worktree is
// started from the hook. It can only wait for the lock, so when the first has held the
// worktree it is refused WORKSPACE_OCCUPIED, and exactly one session holds. Two cases: a
// free worktree, and one held by an idle session that both could take over. Several rounds
// each. It fails when the lock is released across that window: the second enter then
// completes inside it and both hold.
func TestEnterCannotDecideInsideItsOwnWindow(t *testing.T) {
	t.Parallel()
	w := newOccupationWorld(t, capability.OccupationRefuse)
	t.Cleanup(func() { w.sessions.SetOccupyHook(nil) })
	const rounds = 3
	for _, takeover := range []bool{false, true} {
		for i := 0; i < rounds; i++ {
			name := fmt.Sprintf("free round %d", i)
			if takeover {
				name = fmt.Sprintf("takeover round %d", i)
			}
			p, q := fmt.Sprintf("p%v%d", takeover, i), fmt.Sprintf("q%v%d", takeover, i)
			if takeover {
				idle := "idle" + p
				w.connect(idle, "chat-"+idle, "agent")
				if e := w.enter(t, idle, w.a); e != nil {
					t.Fatalf("%s: the idle holder was refused: %+v", name, e)
				}
				w.advance(2 * time.Hour)
				w.sessions.Sweep()
			}
			w.connect(p, "chat-"+p, "agent")
			w.connect(q, "chat-"+q, "agent")

			var armed atomic.Bool
			armed.Store(true)
			done := make(chan *capability.Error, 1)
			var insideWindow atomic.Bool
			w.sessions.SetOccupyHook(func() {
				if !armed.CompareAndSwap(true, false) {
					return
				}
				go func() { done <- w.enter(t, q, w.a) }()
				// q reaches Occupy within a few ms and blocks on the lock; waiting longer than
				// its path to Occupy (workspace.open of an open worktree, about 20 ms) is what
				// lets a split lock show.
				select {
				case e := <-done:
					insideWindow.Store(true)
					done <- e
				case <-time.After(250 * time.Millisecond):
				}
			})
			pErr := w.enter(t, p, w.a)
			var qErr *capability.Error
			select {
			case qErr = <-done:
			case <-time.After(10 * time.Second):
				t.Fatalf("%s: q's enter never returned", name)
			}
			w.sessions.SetOccupyHook(nil)

			if insideWindow.Load() {
				t.Errorf("%s: q's enter completed while p's stood between its decision and its hold", name)
			}
			if pErr != nil || qErr == nil || qErr.Code != capability.ErrWorkspaceOccupied {
				t.Errorf("%s: p's enter = %+v, q's = %+v; want p entering and q refused WORKSPACE_OCCUPIED", name, pErr, qErr)
			}
			// p holds it: q is told so, and p is told nothing (it is the holder).
			if _, ok := w.sessions.HolderOf(w.a, q); !ok {
				t.Errorf("%s: q is told nobody holds the worktree", name)
			}
			if _, ok := w.sessions.HolderOf(w.a, p); ok {
				t.Errorf("%s: p is told another session holds the worktree", name)
			}
			for _, id := range []string{p, q} {
				w.sessions.End(id)
			}
		}
	}
}
