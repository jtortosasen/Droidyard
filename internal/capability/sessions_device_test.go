package capability

import (
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Device occupation (ADR-0094 §1 to §6): which session holds which serial, decided and
// applied in one critical section of the registry, and freed with the session, with its
// return to the main tree and with the serial.

// devicesOfLast is the serials the last event of id says it holds.
func devicesOfLast(rec *sessionRecorder, id string) []string {
	return lastState(rec, id).Devices
}

// TestOccupyDeviceHoldsOneAndRefusesTheOther holds ADR-0094 §2 and §5: the first session
// to act on a free serial holds it and its row says so; another live session of another
// chat acting on it is refused, told who holds it and since when, and holds nothing; the
// holder acting again stays the holder. A registry that held for whoever came last, or
// refused its own holder, fails the second and third steps.
func TestOccupyDeviceHoldsOneAndRefusesTheOther(t *testing.T) {
	s, rec, _ := occupied(OccupationRefuse)
	connectAt(s, "a", "chat-a", "agent-a")
	connectAt(s, "b", "chat-b", "agent-b")

	if _, refused := s.OccupyDevice("a", "emulator-5554"); refused {
		t.Fatal("a free serial was refused")
	}
	if got := devicesOfLast(rec, "a"); !slices.Equal(got, []string{"emulator-5554"}) {
		t.Errorf("a's row carries devices %v; want the serial it acted on", got)
	}
	for _, row := range s.listed() {
		if row.ID == "a" && !slices.Equal(row.Devices, []string{"emulator-5554"}) {
			t.Errorf("a's session.watch snapshot row carries devices %v; want the serial it acted on", row.Devices)
		}
	}
	held, refused := s.OccupyDevice("b", "emulator-5554")
	if !refused {
		t.Fatal("another chat's acting call on a held serial was not refused")
	}
	if held.Name != "agent-a" || held.Since.IsZero() {
		t.Errorf("the refusal describes the holder as %+v; want its name and since", held)
	}
	if got := devicesOfLast(rec, "b"); len(got) != 0 {
		t.Errorf("b, refused, holds %v", got)
	}
	if _, refused := s.OccupyDevice("a", "emulator-5554"); refused {
		t.Error("the holder was refused its own serial")
	}
	if _, refused := s.OccupyDevice("b", "emulator-5556"); refused {
		t.Error("a second, free serial was refused")
	}
	if h, ok := s.DeviceHolder("emulator-5554", "b"); !ok || h.Name != "agent-a" {
		t.Errorf("DeviceHolder for b = %+v, %v; want agent-a", h, ok)
	}
	if _, ok := s.DeviceHolder("emulator-5554", "a"); ok {
		t.Error("the holder is told it is occupied by itself")
	}
	if _, ok := s.DeviceHolder("emulator-5554", ""); !ok {
		t.Error("the UI or the CLI (no session) is not told of the holder")
	}
}

// TestOccupyDeviceIsPerChat holds ADR-0094 §1 for devices: two live sessions of one chat are
// one holder's worth, so neither is refused the serial the other holds, and the second does
// not hold it; a session of no chat is its own chat. Without the chat rule a chat with two
// connections locked itself out of its own emulator.
func TestOccupyDeviceIsPerChat(t *testing.T) {
	s, rec, _ := occupied(OccupationRefuse)
	connectAt(s, "a1", "chat-a", "claude-code")
	connectAt(s, "a2", "chat-a", "claude-code")
	connectAt(s, "n1", "", "agent")
	connectAt(s, "n2", "", "agent")

	s.OccupyDevice("a1", "emulator-5554")
	if _, refused := s.OccupyDevice("a2", "emulator-5554"); refused {
		t.Error("a session of the holder's own chat was refused")
	}
	if got := devicesOfLast(rec, "a2"); len(got) != 0 {
		t.Errorf("the second session of the chat holds %v; want only the first to", got)
	}
	if _, ok := s.DeviceHolder("emulator-5554", "a2"); ok {
		t.Error("the holder's own chat is told it is occupied")
	}
	s.OccupyDevice("n1", "emulator-5556")
	if _, refused := s.OccupyDevice("n2", "emulator-5556"); !refused {
		t.Error("two sessions of no chat were taken for one")
	}
}

// TestOccupyDeviceIdleHolderIsTakenOver holds ADR-0094 §4: a holder that has been idle is
// taken over by another session's acting call, whose row then carries the serial and the
// old holder's loses it; a request of the holder ends its idleness, and it holds again
// against the next.
func TestOccupyDeviceIdleHolderIsTakenOver(t *testing.T) {
	s, rec, clk := occupied(OccupationRefuse)
	connectAt(s, "a", "chat-a", "agent")
	connectAt(s, "b", "chat-b", "agent")
	s.OccupyDevice("a", "emulator-5554")

	clk.advance(10 * time.Minute)
	s.Sweep()
	if h, _ := s.DeviceHolder("emulator-5554", "b"); !h.Stale {
		t.Error("an idle holder is not reported idle")
	}
	if _, refused := s.OccupyDevice("b", "emulator-5554"); refused {
		t.Fatal("an idle holder's serial was refused")
	}
	if got := devicesOfLast(rec, "b"); !slices.Equal(got, []string{"emulator-5554"}) {
		t.Errorf("b took over but its row carries %v", got)
	}
	if got := devicesOfLast(rec, "a"); len(got) != 0 {
		t.Errorf("a, taken over, still carries %v", got)
	}

	s.Seen("a")
	s.Seen("b") // b's request ended its idleness too: the sweep had marked both
	if _, refused := s.OccupyDevice("a", "emulator-5554"); !refused {
		t.Error("a woke and was not refused the serial b now holds")
	}
}

// TestOccupyDeviceAllowProceedsAndKeepsTheHolder holds ADR-0094 §5 under conflict allow: the
// other session's call goes on, it does not take the serial, and the holder stays; ask is
// refuse for a device (§6).
func TestOccupyDeviceAllowProceedsAndKeepsTheHolder(t *testing.T) {
	s, rec, _ := occupied(OccupationAllow)
	connectAt(s, "a", "chat-a", "agent")
	connectAt(s, "b", "chat-b", "agent")
	s.OccupyDevice("a", "emulator-5554")
	if _, refused := s.OccupyDevice("b", "emulator-5554"); refused {
		t.Error("allow refused")
	}
	if got := devicesOfLast(rec, "a"); !slices.Equal(got, []string{"emulator-5554"}) {
		t.Errorf("allow moved the hold off a: %v", got)
	}
	if got := devicesOfLast(rec, "b"); len(got) != 0 {
		t.Errorf("allow made b a holder: %v", got)
	}

	ask, _, _ := occupied(OccupationAsk)
	connectAt(ask, "a", "chat-a", "agent")
	connectAt(ask, "b", "chat-b", "agent")
	ask.OccupyDevice("a", "emulator-5554")
	if _, refused := ask.OccupyDevice("b", "emulator-5554"); !refused {
		t.Error("ask was allowed where Droidyard cannot ask")
	}
}

// TestDeviceOccupationEnds holds ADR-0094 §3: a hold ends with the session, when its binding
// moves to the main tree (also when it is already there: the skill's "task done"), by
// ReleaseDevice, and when a listing no longer holds the serial; but not when the binding
// moves to another worktree, and not for a hold taken after the listing was read. A
// registry that released the devices at every move, or kept them at the main tree, fails
// here.
func TestDeviceOccupationEnds(t *testing.T) {
	s, rec, _ := occupied(OccupationRefuse)
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		connectAt(s, id, "chat-"+id, "agent")
	}

	s.OccupyDevice("a", "emulator-5554")
	s.OccupyDevice("a", "emulator-5556")
	s.Occupy("a", "ws-wt", "/wt1", true) // another worktree: keeps the devices
	if got := devicesOfLast(rec, "a"); !slices.Equal(got, []string{"emulator-5554", "emulator-5556"}) {
		t.Errorf("entering a linked worktree changed a's devices to %v", got)
	}
	s.Occupy("a", "ws-main", "/main", false) // the main tree: task done
	if got := devicesOfLast(rec, "a"); len(got) != 0 {
		t.Errorf("a back at the main tree still carries %v", got)
	}
	if _, refused := s.OccupyDevice("b", "emulator-5554"); refused {
		t.Error("the serial was still held after the holder returned to the main tree")
	}

	// Already at the main tree: workspace_enter of it again releases.
	s.Occupy("b", "ws-main", "/main", false)
	if got := devicesOfLast(rec, "b"); len(got) != 0 {
		t.Errorf("b entering the main tree it is in kept %v", got)
	}

	// The session's end.
	s.OccupyDevice("c", "emulator-5558")
	s.End("c")
	s.mu.Lock()
	_, leaked := s.devHolders["emulator-5558"]
	s.mu.Unlock()
	if leaked {
		t.Error("an ended session left its serial in the registry")
	}
	if _, refused := s.OccupyDevice("d", "emulator-5558"); refused {
		t.Error("an ended session's serial is still held")
	}

	// ReleaseDevice, which moves no binding.
	s.OccupyDevice("e", "emulator-5560")
	s.ReleaseDevice("emulator-5560")
	if got := devicesOfLast(rec, "e"); len(got) != 0 {
		t.Errorf("ReleaseDevice left e carrying %v", got)
	}
	if r, ok := s.Binding("e"); !ok || r != "/main" {
		t.Errorf("ReleaseDevice moved e's binding to %q", r)
	}
	s.ReleaseDevice("emulator-9999") // a free serial is already released

	// A serial gone from a listing is freed; one still in it is kept.
	s.OccupyDevice("e", "emulator-5562")
	s.OccupyDevice("e", "emulator-5564")
	s.ForgetDevicesAbsent([]string{"emulator-5564"}, s.DeviceMark())
	if got := devicesOfLast(rec, "e"); !slices.Equal(got, []string{"emulator-5564"}) {
		t.Errorf("after a listing without emulator-5562 e carries %v; want only emulator-5564", got)
	}

	// A hold taken after the listing was read is not on its word.
	mark := s.DeviceMark()
	s.OccupyDevice("e", "emulator-5566")
	s.ForgetDevicesAbsent(nil, mark)
	if got := devicesOfLast(rec, "e"); !slices.Contains(got, "emulator-5566") {
		t.Errorf("a hold taken after the listing was read was released by it: %v", got)
	}
}

// TestTakeAndAdoptDevice holds ADR-0094 §2 for emulator.start: a fresh boot takes the serial
// whoever held it, and an adopted emulator is held only when nobody holds it. The takeover
// publishes the old holder's row.
func TestTakeAndAdoptDevice(t *testing.T) {
	s, rec, _ := occupied(OccupationRefuse)
	connectAt(s, "a", "chat-a", "agent")
	connectAt(s, "b", "chat-b", "agent")
	s.OccupyDevice("b", "emulator-5554")

	s.AdoptDevice("a", "emulator-5554")
	if got := devicesOfLast(rec, "a"); len(got) != 0 {
		t.Errorf("an adopt took a held serial: %v", got)
	}
	s.AdoptDevice("a", "emulator-5556")
	if got := devicesOfLast(rec, "a"); !slices.Equal(got, []string{"emulator-5556"}) {
		t.Errorf("an adopt of a free serial did not hold: %v", got)
	}
	s.TakeDevice("a", "emulator-5554")
	if got := devicesOfLast(rec, "a"); !slices.Equal(got, []string{"emulator-5554", "emulator-5556"}) {
		t.Errorf("a boot did not take the serial: %v", got)
	}
	if got := devicesOfLast(rec, "b"); len(got) != 0 {
		t.Errorf("the session a boot took the serial from still carries %v", got)
	}
	s.TakeDevice("nobody", "emulator-5558")
	s.AdoptDevice("nobody", "emulator-5558")
	if _, ok := s.DeviceHolder("emulator-5558", ""); ok {
		t.Error("a session that is not registered holds a device")
	}
}

// TestOccupyDeviceDecidesAndHoldsUnderOneLock holds ADR-0094 §1 for devices: many sessions
// acting on one free serial at once, one holds and the rest are refused, for a free serial
// and for one held by an idle session. Run under -race.
func TestOccupyDeviceDecidesAndHoldsUnderOneLock(t *testing.T) {
	const n = 32
	ids := make([]string, n)
	for i := range ids {
		ids[i] = "s" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	for _, takeover := range []bool{false, true} {
		s, rec, clk := occupied(OccupationRefuse)
		if takeover {
			connectAt(s, "idle", "chat-idle", "agent")
			s.OccupyDevice("idle", "emulator-5554")
			clk.advance(10 * time.Minute)
			s.Sweep()
		}
		for _, id := range ids {
			connectAt(s, id, "chat-"+id, "agent")
		}
		var won atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if _, refused := s.OccupyDevice(id, "emulator-5554"); !refused {
					won.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		holding := 0
		for _, id := range ids {
			if len(devicesOfLast(rec, id)) > 0 {
				holding++
			}
		}
		if won.Load() != 1 || holding != 1 {
			t.Errorf("takeover=%v: %d calls went on and %d rows hold; want exactly one of each", takeover, won.Load(), holding)
		}
	}
}

// TestOccupyDeviceCannotDecideInsideItsOwnWindow holds ADR-0094 §1 deterministically, as
// TestOccupyCannotDecideInsideItsOwnWindow does for worktrees: while one OccupyDevice stands
// between its decision and its effect, a second one on the same serial is started from the
// hook and must not complete there; it completes after the first has held the serial, and
// is refused. It fails when the lock is released across that window. Both a free serial and
// one held by an idle session, which both could take over.
func TestOccupyDeviceCannotDecideInsideItsOwnWindow(t *testing.T) {
	for _, takeover := range []bool{false, true} {
		name := map[bool]string{false: "free", true: "idle holder"}[takeover]
		s, rec, clk := occupied(OccupationRefuse)
		if takeover {
			connectAt(s, "idle", "chat-idle", "agent")
			s.OccupyDevice("idle", "emulator-5554")
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
				_, refused := s.OccupyDevice("b", "emulator-5554")
				bRefused.Store(refused)
				close(done)
			}()
			select {
			case <-done:
				insideWindow.Store(true)
			case <-time.After(150 * time.Millisecond):
			}
		})
		_, aRefused := s.OccupyDevice("a", "emulator-5554")
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: b's OccupyDevice never returned", name)
		}
		s.SetOccupyHook(nil)

		if insideWindow.Load() {
			t.Errorf("%s: b's OccupyDevice completed while a stood between its claim and its hold", name)
		}
		if aRefused || !bRefused.Load() {
			t.Errorf("%s: a refused = %v, b refused = %v; want a holding and b refused", name, aRefused, bRefused.Load())
		}
		if len(devicesOfLast(rec, "a")) != 1 || len(devicesOfLast(rec, "b")) != 0 {
			t.Errorf("%s: the last events leave a carrying %v and b %v; want only a", name, devicesOfLast(rec, "a"), devicesOfLast(rec, "b"))
		}
	}
}
