package capability

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The lease of a lent device (ADR-0104): a paired peer's link is a session of the registry,
// and its hold of a device is ADR-0094's occupation with different rules for who is refused
// and when it is idle.

const leaseSerial = "R58M001"

// leased is occupied with the two peers anna and bob connected, and the MCP session m.
func leased(conflict string) (*Sessions, *sessionRecorder, *occupationClock) {
	s, rec, clk := occupied(conflict)
	s.ConnectPeer("anna", "anna")
	s.ConnectPeer("bob", "bob")
	connectAt(s, "m", "chat-m", "host-agent")
	return s, rec, clk
}

// TestPeerLeaseRefusesWhateverTheConflictSays is ADR-0104 §2: with occupation.conflict allow,
// where two MCP sessions both go on, a peer holding a device refuses the other peer and the
// host's agent, and a peer finding the host's agent holding is refused too; the refusal
// names the peer, by its configured name. Seen failing with the configuration honoured for
// peers (the condition `conflict != allow` alone): the agent's call went on.
func TestPeerLeaseRefusesWhateverTheConflictSays(t *testing.T) {
	for _, conflict := range []string{OccupationRefuse, OccupationAllow} {
		s, _, _ := leased(conflict)
		if _, refused := s.OccupyDevice("anna", leaseSerial); refused {
			t.Fatalf("%s: a free device was refused to the first peer", conflict)
		}
		held, refused := s.OccupyDevice("bob", leaseSerial)
		if !refused || !held.Peer || held.Name != "anna" || !strings.HasPrefix(held.Who(), "peer anna (since ") {
			t.Errorf("%s: the other peer got (%+v, refused %v); want refused, told it is peer anna", conflict, held, refused)
		}
		if held, refused := s.OccupyDevice("m", leaseSerial); !refused || !held.Peer {
			t.Errorf("%s: the host's agent got (%+v, refused %v) on a device a peer holds; want refused", conflict, held, refused)
		}
		if h, ok := s.DeviceHolder(leaseSerial, "bob"); !ok || h.Name != "anna" || !h.Peer {
			t.Errorf("%s: DeviceHolder for bob = %+v, %v; want peer anna", conflict, h, ok)
		}
		if _, ok := s.DeviceHolder(leaseSerial, "anna"); ok {
			t.Errorf("%s: the holder is told it is occupied by itself", conflict)
		}

		// The reverse: the agent holds first, and the peer is refused.
		s, _, _ = leased(conflict)
		if _, refused := s.OccupyDevice("m", leaseSerial); refused {
			t.Fatalf("%s: a free device was refused to the agent", conflict)
		}
		held, refused = s.OccupyDevice("anna", leaseSerial)
		if !refused || held.Peer || held.Name != "host-agent" || !strings.HasPrefix(held.Who(), "another chat (host-agent") {
			t.Errorf("%s: a peer on the agent's device got (%+v, refused %v); want refused, told another chat holds it", conflict, held, refused)
		}
	}

	// The control that makes the rows above mean something: two MCP sessions of two chats
	// under allow still both go on.
	s, _, _ := leased(OccupationAllow)
	connectAt(s, "n", "chat-n", "other-agent")
	s.OccupyDevice("m", leaseSerial)
	if _, refused := s.OccupyDevice("n", leaseSerial); refused {
		t.Error("allow no longer lets a second MCP session through")
	}
}

// TestPeerLeaseIdleIsAConstantAndStreamsAreActivity is ADR-0104 §3: a peer's lease is idle
// after 15 minutes with no call and no open stream, whatever occupation.staleAfterMinutes
// says (the helper's is two minutes), decided at the moment of the next call and not by the
// sweep; an open stream keeps it, and the stream's end starts the idle time. Seen failing
// with the configured threshold used for peers (the lease was taken at three minutes), and
// with the streams not counted (the lease of a peer that only watched was taken over).
func TestPeerLeaseIdleIsAConstantAndStreamsAreActivity(t *testing.T) {
	s, _, clk := leased(OccupationRefuse)
	s.OccupyDevice("anna", leaseSerial)

	clk.advance(3 * time.Minute)
	s.Sweep()
	if _, refused := s.OccupyDevice("bob", leaseSerial); !refused {
		t.Fatal("a peer's lease was taken over after 3 minutes: the configured two-minute threshold is for MCP sessions")
	}
	if h, _ := s.DeviceHolder(leaseSerial, "bob"); h.Stale {
		t.Error("a lease unused for 3 minutes is reported idle")
	}
	clk.advance(PeerLeaseIdle - 3*time.Minute - time.Second)
	if _, refused := s.OccupyDevice("bob", leaseSerial); !refused {
		t.Fatal("the lease was taken over a second before it was idle")
	}
	// A call of the holder starts the time again.
	s.Seen("anna")
	clk.advance(PeerLeaseIdle - time.Second)
	if _, refused := s.OccupyDevice("bob", leaseSerial); !refused {
		t.Fatal("a call of the holder did not restart its idle time")
	}
	clk.advance(2 * time.Second)
	if h, ok := s.DeviceHolder(leaseSerial, "bob"); !ok || !h.Stale {
		t.Errorf("DeviceHolder after the idle time = %+v, %v; want it reported idle without a sweep", h, ok)
	}
	if _, refused := s.OccupyDevice("bob", leaseSerial); refused {
		t.Fatal("an idle lease was not taken over by the next caller")
	}
	if _, ok := s.DeviceHolder(leaseSerial, "anna"); !ok {
		t.Error("the old holder is not told bob holds the device")
	}

	// An open stream is activity for however long it lasts.
	s, _, clk = leased(OccupationRefuse)
	s.OccupyDevice("anna", leaseSerial)
	s.StreamOpened("anna")
	clk.advance(20 * time.Minute)
	s.Sweep()
	if _, refused := s.OccupyDevice("bob", leaseSerial); !refused {
		t.Fatal("a lease with a stream open for 20 minutes was taken over")
	}
	s.StreamClosed("anna")
	clk.advance(PeerLeaseIdle - time.Second)
	if _, refused := s.OccupyDevice("bob", leaseSerial); !refused {
		t.Fatal("the idle time did not start at the stream's end")
	}
	clk.advance(2 * time.Second)
	if _, refused := s.OccupyDevice("bob", leaseSerial); refused {
		t.Fatal("a lease whose stream ended 15 minutes ago was not taken over")
	}
}

// TestPeerLeaseEnds is ADR-0104 §4: a link's end frees what it held; the owner's release of a
// device a peer holds tells the lease-end function, once, after the lock is released (the
// function reads the registry), so the holder's streams stop; a release of a device an MCP
// session holds, or a conditional release of a hold that has since changed, tells no one; a
// device leaving a listing does not (its streams end by their own reason); and a remote
// device's id, which no local listing has, is not released by one.
func TestPeerLeaseEnds(t *testing.T) {
	s, _, _ := leased(OccupationRefuse)
	type end struct{ session, serial string }
	var mu sync.Mutex
	var ends []end
	s.UseLeaseEnd(func(session, serial string) {
		// Reading the registry from here proves the lock is not held.
		s.DeviceHolder(serial, "")
		mu.Lock()
		ends = append(ends, end{session, serial})
		mu.Unlock()
	})
	got := func() []end { mu.Lock(); defer mu.Unlock(); return slices.Clone(ends) }

	s.OccupyDevice("anna", leaseSerial)
	if holds := s.PeerHolds(); len(holds) != 1 || holds[0] != (PeerHold{Session: "anna", Peer: "anna", Serial: leaseSerial}) {
		t.Errorf("PeerHolds = %+v; want anna's hold of %s", holds, leaseSerial)
	}
	s.ReleaseDeviceOf("bob", leaseSerial) // not the holder
	if len(got()) != 0 {
		t.Fatalf("a conditional release of someone else's hold ended the lease: %v", got())
	}
	if _, ok := s.DeviceHolder(leaseSerial, ""); !ok {
		t.Fatal("a conditional release of someone else's hold freed the device")
	}
	s.ReleaseDevice(leaseSerial)
	if e := got(); len(e) != 1 || e[0] != (end{"anna", leaseSerial}) {
		t.Errorf("the lease-end function was told %v; want anna's, once", e)
	}
	if _, ok := s.DeviceHolder(leaseSerial, ""); ok {
		t.Error("the device is held after the owner's release")
	}
	s.ReleaseDevice(leaseSerial) // nobody holds it
	if len(got()) != 1 {
		t.Errorf("releasing a free device told the function: %v", got())
	}

	s.OccupyDevice("m", leaseSerial)
	s.ReleaseDevice(leaseSerial)
	if len(got()) != 1 {
		t.Errorf("an MCP session's hold ended through the lease-end function: %v", got())
	}

	// A peer's hold is not ended by a listing without the serial or by a remote id's absence,
	// but is by its link.
	s.OccupyDevice("anna", leaseSerial)
	s.OccupyDevice("m", "qa/"+leaseSerial)
	s.OccupyDevice("m", "other/"+leaseSerial) // a slash, but no configured remote of that name
	s.ForgetDevicesAbsent(nil, s.DeviceMark(), func(serial string) bool { return strings.HasPrefix(serial, "qa/") })
	if _, ok := s.DeviceHolder("other/"+leaseSerial, ""); ok {
		t.Error("a serial with a slash that names no configured remote was kept by a listing")
	}
	if _, ok := s.DeviceHolder("qa/"+leaseSerial, ""); !ok {
		t.Error("a listing without a remote device's id released its hold: no local listing has it")
	}
	if _, ok := s.DeviceHolder(leaseSerial, ""); ok {
		t.Error("a listing without the serial left it held")
	}
	if len(got()) != 1 {
		t.Errorf("the device leaving told the lease-end function: %v", got())
	}
	s.OccupyDevice("anna", leaseSerial)
	s.End("anna")
	if _, ok := s.DeviceHolder(leaseSerial, ""); ok {
		t.Error("a peer's hold survived the end of its link")
	}
	if _, refused := s.OccupyDevice("bob", leaseSerial); refused {
		t.Error("the device was refused after the holder's link ended")
	}
}

// TestSamePeerSecondLinkTakesTheLeaseOver is the rule for a peer that reconnects before the
// host has noticed its old link is dead: its new link takes the hold over (it is the same
// peer), where another peer is refused, and the old link is no longer told it is the holder.
// Seen failing with the second link refused, naming the peer to itself.
func TestSamePeerSecondLinkTakesTheLeaseOver(t *testing.T) {
	s, _, _ := leased(OccupationRefuse)
	s.ConnectPeer("anna2", "anna")
	s.OccupyDevice("anna", leaseSerial)
	if _, refused := s.OccupyDevice("anna2", leaseSerial); refused {
		t.Fatal("a peer's new link was refused the device its old link holds")
	}
	if _, refused := s.OccupyDevice("bob", leaseSerial); !refused {
		t.Fatal("another peer was not refused")
	}
	if h, ok := s.DeviceHolder(leaseSerial, "anna"); ok {
		t.Errorf("the old link is told %+v holds the device: it is the same peer", h)
	}
}

// TestPeerLeaseDecidesUnderOneLock is ADR-0104's "one holder per lent device across peers
// and host agents, decided under the registry's lock": many peers call at once on one free
// device and exactly one is not refused; with host agents among them under allow (where two
// agents alone would both go on), a peer that wins is the only call that goes on, and a call
// that goes on beside an agent's is only another agent's. Run under -race.
func TestPeerLeaseDecidesUnderOneLock(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		s, _, _ := occupied(OccupationAllow)
		peers := map[string]bool{}
		var ids []string
		for i := range 16 {
			id := "p" + string(rune('a'+i))
			s.ConnectPeer(id, "peer-"+string(rune('a'+i)))
			peers[id] = true
			ids = append(ids, id)
			if mixed {
				a := "m" + string(rune('a'+i))
				connectAt(s, a, "chat-"+a, "agent")
				ids = append(ids, a)
			}
		}
		var mu sync.Mutex
		var went []string
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if _, refused := s.OccupyDevice(id, leaseSerial); !refused {
					mu.Lock()
					went = append(went, id)
					mu.Unlock()
				}
			}()
		}
		close(start)
		wg.Wait()
		peersThrough := 0
		for _, id := range went {
			if peers[id] {
				peersThrough++
			}
		}
		switch {
		case !mixed && len(went) != 1:
			t.Errorf("%d of 16 peers went on at once; want exactly one", len(went))
		case mixed && peersThrough > 1:
			t.Errorf("%d peers went on beside each other", peersThrough)
		case mixed && peersThrough == 1 && len(went) != 1:
			t.Errorf("a peer holds the device and %d calls went on: %v; want only the peer", len(went), went)
		case mixed && len(went) == 0:
			t.Error("no call went on")
		}
	}
}
