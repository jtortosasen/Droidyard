package device

import (
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// uiStream adds a UI events stream of serial for the link session to the table, counted as
// the lease's activity as watchUIEvents counts it.
func (f *streamFixture) uiStream(t *testing.T, link, serial string) *uiEventStream {
	t.Helper()
	s := &uiEventStream{key: streamKey{session: link, serial: serial}, peer: true, cancel: func() {}}
	if _, err := f.adb.uiStreams.add(s); err != nil {
		t.Fatal(err)
	}
	s.activity = f.adb.streamActivity(peerCall(link))
	return s
}

// Decision (ADR-0104 §3, §4; ADR-0115 Decision 1): a peer's UI events stream is activity of
// its lease while it is in the table, and an ended lease stops the streams of the link session
// that held it, on that device, as deviceGone with the lease's cause: not those of another
// link session or of another device. Seen failing with uiStreams left out of endLease (the
// stream kept its reason) and with the stream not counted (the lease taken over after two
// hours of a stream open).
func TestLeaseEndStopsPeerUIEventsStreamsInTheTable(t *testing.T) {
	f := newStreamFixture(t)
	s := f.ops.Sessions()
	if _, refused := s.OccupyDevice("peer-bob", leaseDevice); refused {
		t.Fatal("bob was refused a free device")
	}
	mine := f.uiStream(t, "peer-bob", leaseDevice)
	otherDevice := f.uiStream(t, "peer-bob", "R58M001")
	otherLink := f.uiStream(t, "peer-anna", leaseDevice)

	f.clk.advance(2 * time.Hour)
	if _, refused := s.OccupyDevice("peer-anna", leaseDevice); !refused {
		t.Fatal("bob's lease, with a UI events stream open for two hours, was taken over")
	}

	f.adb.endLease("peer-bob", leaseDevice)
	if got := mine.endedFor(); got != uiEndLease {
		t.Errorf("the holder's stream ended with reason %d; want uiEndLease (%d)", got, uiEndLease)
	}
	if got := otherDevice.endedFor(); got != uiEndNone {
		t.Errorf("a stream on another device ended with reason %d", got)
	}
	if got := otherLink.endedFor(); got != uiEndNone {
		t.Errorf("another link's stream ended with reason %d", got)
	}

	// The other link's stream is still open, so its lease is never idle; bob's stream leaving
	// the table starts bob's own idle time, which a stream of anna's does not hold.
	f.adb.uiStreams.remove(mine)
	f.adb.uiStreams.remove(otherDevice)
	f.clk.advance(capability.PeerLeaseIdle - time.Second)
	if _, refused := s.OccupyDevice("peer-anna", leaseDevice); !refused {
		t.Fatal("the idle time did not start when the last stream ended")
	}
	f.clk.advance(2 * time.Second)
	if _, refused := s.OccupyDevice("peer-anna", leaseDevice); refused {
		t.Fatal("a lease whose last stream ended 15 minutes ago was not taken over")
	}
}
