package capability

import (
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Occupation (ADR-0094 §1 to §4): which session holds a linked worktree, decided and bound
// in one critical section of the registry.

// occupationClock is a clock a test moves.
type occupationClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *occupationClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *occupationClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// occupied is a registry whose configuration is conflict and a two-minute idle threshold,
// on a clock the test moves.
func occupied(conflict string) (*Sessions, *sessionRecorder, *occupationClock) {
	s, rec := newWatchedSessions()
	clk := &occupationClock{t: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)}
	s.UseClock(clk.now)
	s.UseOccupation(func() (string, time.Duration) { return conflict, 2 * time.Minute })
	return s, rec, clk
}

// connectAt connects id (of chat) named name, bound at the main tree "/main".
func connectAt(s *Sessions, id, chat, name string) {
	s.Connect(id, chat)
	s.Named(id, name)
	s.Opened(id, "/main", "ws-main", "/main")
}

func lastState(rec *sessionRecorder, id string) SessionState {
	var last SessionState
	for _, e := range rec.events() {
		if e.ID == id {
			last = e
		}
	}
	return last
}

// TestOccupyHoldsLinkedWorktreesOnly holds ADR-0094 §1 and §3: a linked worktree is held
// by the one session that binds there, its row says so (occupies is the workspace id) and
// nothing else's does; the main tree is never held, however often it is entered; moving
// away, to the main tree too, releases what was held. The decision it guards: the main
// tree stays everyone's, and a release needs no tool, only the move. Seen failing with the
// linked test removed (every root held): the main-tree rows carried occupies and the
// second session entering the main tree was refused.
func TestOccupyHoldsLinkedWorktreesOnly(t *testing.T) {
	s, rec, _ := occupied(OccupationRefuse)
	connectAt(s, "a", "chat-a", "claude-code")
	connectAt(s, "b", "chat-b", "claude-code")

	for i := 0; i < 3; i++ {
		for _, id := range []string{"a", "b"} {
			if _, refused := s.Occupy(id, "ws-main", "/main", false); refused {
				t.Fatalf("%s entering the main tree was refused", id)
			}
		}
	}
	if _, ok := s.HolderOf("/main", ""); ok {
		t.Error("the main tree is held")
	}
	for _, e := range rec.events() {
		if e.Occupies != "" {
			t.Errorf("an event carries occupies %q before any linked worktree was entered: %+v", e.Occupies, e)
		}
	}

	if _, refused := s.Occupy("a", "ws-wt1", "/wt1", true); refused {
		t.Fatal("a free linked worktree was refused")
	}
	if got := lastState(rec, "a"); got.Occupies != "ws-wt1" || got.Workspace != "ws-wt1" {
		t.Errorf("a's state after entering is %+v; want it bound to and occupying ws-wt1", got)
	}
	if root, _ := s.Binding("a"); root != "/wt1" {
		t.Errorf("a is bound to %q; want /wt1", root)
	}
	if h, ok := s.HolderOf("/wt1", "b"); !ok || h.Name != "claude-code" || h.Since.IsZero() || h.Stale {
		t.Errorf("b is told %+v, %v of /wt1; want a's name and when, not stale", h, ok)
	}
	if _, ok := s.HolderOf("/wt1", "a"); ok {
		t.Error("a is told of its own hold")
	}

	// Moving to the main tree frees it, and the row loses occupies.
	s.Occupy("a", "ws-main", "/main", false)
	if got := lastState(rec, "a"); got.Occupies != "" || got.Workspace != "ws-main" {
		t.Errorf("a's state after the main tree is %+v; want no occupies", got)
	}
	if _, ok := s.HolderOf("/wt1", ""); ok {
		t.Error("/wt1 is held after its holder went to the main tree")
	}
}

// TestOccupyRefusesAnotherLiveChat holds ADR-0094 §5 and §6: a second chat entering a
// worktree a live, non-idle session holds is refused with the holder, and nothing changes
// (its binding, the holder's row, the chat's remembered binding); refuse and ask refuse
// alike, because Droidyard cannot ask there; allow binds the second without holding it.
// Seen failing with the check removed: the second session entered and took the hold.
func TestOccupyRefusesAnotherLiveChat(t *testing.T) {
	for _, conflict := range []string{OccupationRefuse, OccupationAsk, OccupationAllow} {
		s, rec, _ := occupied(conflict)
		connectAt(s, "a", "chat-a", "claude-code")
		connectAt(s, "b", "chat-b", "cursor")
		s.Occupy("a", "ws-wt1", "/wt1", true)
		before := len(rec.events())

		holder, refused := s.Occupy("b", "ws-wt1", "/wt1", true)
		if conflict == OccupationAllow {
			if refused {
				t.Errorf("allow: b was refused")
			}
			if root, _ := s.Binding("b"); root != "/wt1" {
				t.Errorf("allow: b is bound to %q; want /wt1", root)
			}
			if h, ok := s.HolderOf("/wt1", "b"); !ok || h.Name != "claude-code" {
				t.Errorf("allow: /wt1 is held by %+v, %v; want a still", h, ok)
			}
			if got := lastState(rec, "b"); got.Occupies != "" {
				t.Errorf("allow: b's state is %+v; want it not occupying", got)
			}
			if got := lastState(rec, "a"); got.Occupies != "ws-wt1" {
				t.Errorf("allow: a's state is %+v; want it still occupying", got)
			}
			continue
		}
		if !refused || holder.Name != "claude-code" || holder.Since.IsZero() {
			t.Errorf("%s: Occupy = %+v, %v; want refused, naming a", conflict, holder, refused)
		}
		if root, _ := s.Binding("b"); root != "/main" {
			t.Errorf("%s: b is bound to %q after a refusal; want /main", conflict, root)
		}
		if n := len(rec.events()); n != before {
			t.Errorf("%s: a refusal published %d events", conflict, n-before)
		}
		if mem, _ := s.Remembered("chat-b"); mem == "/wt1" {
			t.Errorf("%s: a refusal was remembered as the chat's binding", conflict)
		}
	}
}

// TestOccupyIsPerChatNotPerSession holds ADR-0094 §1: two live sessions of one chat share
// what either holds. The second enters what the first holds without being refused, is bound
// there but does not hold it, and is not told it is occupied; the first still holds it for
// another chat. Sessions with no chat are all distinct. Seen failing with the chat
// comparison removed: the resumed session of the chat was refused its own worktree.
func TestOccupyIsPerChatNotPerSession(t *testing.T) {
	s, _, _ := occupied(OccupationRefuse)
	connectAt(s, "x1", "chat-x", "claude-code")
	connectAt(s, "x2", "chat-x", "claude-code")
	connectAt(s, "c1", "", "cursor")
	connectAt(s, "c2", "", "cursor")
	connectAt(s, "y", "chat-y", "claude-code")

	s.Occupy("x1", "ws-wt1", "/wt1", true)
	if _, refused := s.Occupy("x2", "ws-wt1", "/wt1", true); refused {
		t.Fatal("the second session of a chat was refused what the first holds")
	}
	if root, _ := s.Binding("x2"); root != "/wt1" {
		t.Errorf("x2 is bound to %q; want /wt1", root)
	}
	if _, ok := s.HolderOf("/wt1", "x2"); ok {
		t.Error("x2 is told its own chat occupies /wt1")
	}
	if _, refused := s.Occupy("y", "ws-wt1", "/wt1", true); !refused {
		t.Error("another chat entered what x1 holds")
	}

	s.Occupy("c1", "ws-wt2", "/wt2", true)
	if _, refused := s.Occupy("c2", "ws-wt2", "/wt2", true); !refused {
		t.Error("two sessions with no chat shared a worktree: no chat id tells them apart, so they are different chats")
	}
}

// TestIdleHolderIsMarkedAndTakenOver holds ADR-0094 §4: a session with no request for the
// configured time is marked stale by the sweep (its row says so, and the worktree it holds
// is listed as stale to others); the next session to enter takes the worktree over, and the
// idle one's row loses occupies; a request of the idle session clears stale. Seen failing
// with the stale check removed: the second session was refused an idle holder's worktree.
func TestIdleHolderIsMarkedAndTakenOver(t *testing.T) {
	s, rec, clk := occupied(OccupationRefuse)
	connectAt(s, "a", "chat-a", "claude-code")
	connectAt(s, "b", "chat-b", "claude-code")
	s.Occupy("a", "ws-wt1", "/wt1", true)

	clk.advance(time.Minute)
	s.Sweep()
	if lastState(rec, "a").Stale {
		t.Fatal("a is stale one minute into a two-minute threshold")
	}
	clk.advance(2 * time.Minute)
	s.Seen("b") // b keeps calling; a does not
	s.Sweep()
	if got := lastState(rec, "a"); !got.Stale || got.Occupies != "ws-wt1" {
		t.Errorf("a's state is %+v; want stale and still occupying", got)
	}
	if got := lastState(rec, "b"); got.Stale {
		t.Errorf("b's state is %+v; b made a request", got)
	}
	if h, ok := s.HolderOf("/wt1", "b"); !ok || !h.Stale {
		t.Errorf("b is told %+v, %v of /wt1; want the holder stale", h, ok)
	}
	list := s.listed()
	if !list[0].Stale || list[0].Occupies != "ws-wt1" || list[1].Stale {
		t.Errorf("the snapshot is %+v; want a stale and occupying, b not stale", list)
	}

	if _, refused := s.Occupy("b", "ws-wt1", "/wt1", true); refused {
		t.Fatal("b was refused an idle holder's worktree")
	}
	if got := lastState(rec, "a"); got.Occupies != "" || !got.Stale {
		t.Errorf("a's state after the takeover is %+v; want it without occupies", got)
	}
	if got := lastState(rec, "b"); got.Occupies != "ws-wt1" {
		t.Errorf("b's state after the takeover is %+v; want occupying", got)
	}

	// A request ends the idleness, and publishes it; it brings nothing back.
	s.Seen("a")
	if got := lastState(rec, "a"); got.Stale || got.Occupies != "" {
		t.Errorf("a's state after a request is %+v; want not stale, still without occupies", got)
	}
	if h, ok := s.HolderOf("/wt1", "a"); !ok || h.Stale {
		t.Errorf("a is told %+v, %v of /wt1; want b, not stale", h, ok)
	}
}

// TestOccupationEndsWithTheSessionAndHelloHolds holds ADR-0094 §2 and §3: a session bound
// by its hello in a free linked worktree holds it, and one bound in a worktree another
// session holds is bound, not holding (a hello is never refused); a session's end frees
// what it held, and nothing is published after the end.
func TestOccupationEndsWithTheSessionAndHelloHolds(t *testing.T) {
	s, rec, _ := occupied(OccupationRefuse)
	s.Connect("a", "chat-a")
	s.Opened("a", "/main", "ws-wt1", "/wt1")
	if got := lastState(rec, "a"); got.Occupies != "ws-wt1" {
		t.Errorf("a's state after a hello in a free worktree is %+v; want occupying", got)
	}
	s.Connect("b", "chat-b")
	s.Opened("b", "/main", "ws-wt1", "/wt1")
	if got := lastState(rec, "b"); got.Occupies != "" || got.Workspace != "ws-wt1" {
		t.Errorf("b's state after a hello in a held worktree is %+v; want bound, not occupying", got)
	}
	s.Connect("m", "chat-m")
	s.Opened("m", "/main", "ws-main", "/main")
	if got := lastState(rec, "m"); got.Occupies != "" {
		t.Errorf("a hello at the main tree occupies: %+v", got)
	}

	s.End("a")
	if _, ok := s.HolderOf("/wt1", ""); ok {
		t.Error("/wt1 is still held after its holder ended")
	}
	s.Connect("c", "chat-c")
	if _, refused := s.Occupy("c", "ws-wt1", "/wt1", true); refused {
		t.Error("c was refused a worktree whose holder ended")
	}
}

// TestOccupyDecidesAndBindsUnderOneLock holds ADR-0094 §1 against the race: many sessions
// enter one free worktree at once, and exactly one is not refused; many take over one idle
// holder's worktree at once, and exactly one holds it afterwards. Run with -race. Seen
// failing with the decision taken outside the lock (the claim and the acquire in two
// critical sections): several sessions held the same worktree.
func TestOccupyDecidesAndBindsUnderOneLock(t *testing.T) {
	const n = 32
	race := func(s *Sessions, root string, ids []string) int {
		var won atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if _, refused := s.Occupy(id, "ws", root, true); !refused {
					won.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		return int(won.Load())
	}
	ids := make([]string, n)
	for i := range ids {
		ids[i] = "s" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}

	s, rec, _ := occupied(OccupationRefuse)
	for _, id := range ids {
		connectAt(s, id, "chat-"+id, "agent")
	}
	if won := race(s, "/wt1", ids); won != 1 {
		t.Errorf("%d sessions entered one free worktree; want exactly one", won)
	}

	// An idle holder, taken over by all the others at once.
	s2, rec2, clk2 := occupied(OccupationRefuse)
	connectAt(s2, "idle", "chat-idle", "agent")
	s2.Occupy("idle", "ws", "/wt2", true)
	clk2.advance(10 * time.Minute)
	s2.Sweep()
	for _, id := range ids {
		connectAt(s2, id, "chat-"+id, "agent")
	}
	if won := race(s2, "/wt2", ids); won != 1 {
		t.Errorf("%d sessions took over one idle holder's worktree; want exactly one", won)
	}
	for name, r := range map[string]*sessionRecorder{"free": rec, "takeover": rec2} {
		holding := map[string]string{}
		for _, e := range r.events() {
			if e.Ended {
				continue
			}
			holding[e.ID] = e.Occupies
		}
		var holders []string
		for id, ws := range holding {
			if ws != "" {
				holders = append(holders, id)
			}
		}
		slices.Sort(holders)
		if len(holders) != 1 {
			t.Errorf("%s: the last events leave %v holding; want exactly one", name, holders)
		}
	}
}

// TestOccupyCannotDecideInsideItsOwnWindow holds ADR-0094 §1 deterministically: while one
// Occupy stands between its decision (the claim) and its effect (the hold), a second Occupy
// of the same worktree is started from the occupy hook, and it must not complete there. It
// can only wait for the registry's lock, so it completes after the first has held the
// worktree, and is refused. Both cases are run: a free worktree, and one held by an idle
// session, which both sessions could take over. It fails when the lock is released across
// that window: the hook then runs unlocked, the second Occupy completes first, and both
// are allowed and both hold.
func TestOccupyCannotDecideInsideItsOwnWindow(t *testing.T) {
	for _, takeover := range []bool{false, true} {
		name := map[bool]string{false: "free", true: "idle holder"}[takeover]
		s, rec, clk := occupied(OccupationRefuse)
		if takeover {
			connectAt(s, "idle", "chat-idle", "agent")
			s.Occupy("idle", "ws", "/wt1", true)
			clk.advance(10 * time.Minute)
			s.Sweep()
		}
		connectAt(s, "a", "chat-a", "agent")
		connectAt(s, "b", "chat-b", "agent")

		var armed atomic.Bool
		armed.Store(true)
		done := make(chan struct{})
		var bRefused, insideWindow atomic.Bool
		s.SetOccupyHook(func() {
			if !armed.CompareAndSwap(true, false) {
				return
			}
			go func() {
				_, refused := s.Occupy("b", "ws", "/wt1", true)
				bRefused.Store(refused)
				close(done)
			}()
			select {
			case <-done:
				insideWindow.Store(true) // b decided and bound while a stood between its claim and its hold
			case <-time.After(150 * time.Millisecond):
			}
		})
		_, aRefused := s.Occupy("a", "ws", "/wt1", true)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: b's Occupy never returned", name)
		}
		s.SetOccupyHook(nil)

		if insideWindow.Load() {
			t.Errorf("%s: b's Occupy completed while a stood between its claim and its hold", name)
		}
		if aRefused || !bRefused.Load() {
			t.Errorf("%s: a refused = %v, b refused = %v; want a holding and b refused", name, aRefused, bRefused.Load())
		}
		holding := map[string]string{}
		for _, e := range rec.events() {
			if !e.Ended {
				holding[e.ID] = e.Occupies
			}
		}
		if holding["a"] == "" || holding["b"] != "" {
			t.Errorf("%s: the last events leave a occupying %q and b %q; want only a", name, holding["a"], holding["b"])
		}
	}
}

// TestHoldPassesToAnotherSessionOfTheChat holds ADR-0094 §1 for a chat with two live
// connections: the second entered what the first holds and is bound, not holding; when the
// first ends, or moves away (to the main tree too), the hold passes to the second, whose
// row says so, and another chat's enter is still refused. Without the handover nobody held
// the worktree once the first was gone and the other chat walked in. A session of the chat
// bound elsewhere is handed nothing.
func TestHoldPassesToAnotherSessionOfTheChat(t *testing.T) {
	for _, how := range []string{"ends", "moves to the main tree", "moves to another worktree"} {
		s, rec, _ := occupied(OccupationRefuse)
		connectAt(s, "x1", "chat-x", "claude-code")
		connectAt(s, "x2", "chat-x", "claude-code")
		connectAt(s, "x3", "chat-x", "claude-code") // the chat's third connection stays at the main tree
		connectAt(s, "y", "chat-y", "claude-code")
		s.Occupy("x1", "ws-wt1", "/wt1", true)
		if _, refused := s.Occupy("x2", "ws-wt1", "/wt1", true); refused {
			t.Fatalf("%s: x2 was refused what its own chat holds", how)
		}
		if lastState(rec, "x2").Occupies != "" {
			t.Fatalf("%s: x2 holds while x1 does", how)
		}

		switch how {
		case "ends":
			s.End("x1")
		case "moves to the main tree":
			s.Occupy("x1", "ws-main", "/main", false)
		default:
			s.Occupy("x1", "ws-wt2", "/wt2", true)
		}
		if got := lastState(rec, "x2"); got.Occupies != "ws-wt1" {
			t.Errorf("%s: x2's state is %+v; want it occupying ws-wt1 now", how, got)
		}
		if got := lastState(rec, "x3"); got.Occupies != "" {
			t.Errorf("%s: x3, bound at the main tree, was handed the hold: %+v", how, got)
		}
		if _, refused := s.Occupy("y", "ws-wt1", "/wt1", true); !refused {
			t.Errorf("%s: another chat entered the worktree once its holder passed it to a sibling", how)
		}
		if h, ok := s.HolderOf("/wt1", "y"); !ok || h.Name != "claude-code" {
			t.Errorf("%s: y is told %+v, %v of /wt1; want the chat still holding it", how, h, ok)
		}
		if how == "moves to another worktree" && lastState(rec, "x1").Occupies != "ws-wt2" {
			t.Errorf("%s: x1 does not hold the worktree it moved to: %+v", how, lastState(rec, "x1"))
		}
	}
}
