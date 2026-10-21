package device

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/event"
)

// stubViewer is a screen.Viewer with nothing to read.
type stubViewer struct{ closed atomic.Bool }

func (v *stubViewer) Next(ctx context.Context) (screen.Packet, error) {
	<-ctx.Done()
	return screen.Packet{}, ctx.Err()
}
func (v *stubViewer) Close() { v.closed.Store(true) }

// leaseClock is a clock a test moves.
type leaseClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *leaseClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *leaseClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// peerCall is a call as the link session link of a peer: what the streams are opened for.
func peerCall(link string) *capability.Call {
	return &capability.Call{Origin: capability.Origin{Client: capability.ClientPeer, ID: link}}
}

// streamFixture is the host of leaseFixture with its stream tables used directly, the way
// peerstreams_test.go does for the Logcat cap: a live view's stream and a Logcat stream
// need no device here, only the table and the lease's accounts.
type streamFixture struct {
	*hostFixture
	bus *event.Bus
	clk *leaseClock
}

func newStreamFixture(t *testing.T) *streamFixture {
	t.Helper()
	f := leaseFixture(t, "refuse")
	clk := &leaseClock{t: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)}
	f.ops.Sessions().UseClock(clk.now)
	return &streamFixture{hostFixture: f, bus: event.New(event.Options{}), clk: clk}
}

// screen adds a live view of serial for the link session to the table, counted as the
// lease's activity as watchScreen counts it.
func (f *streamFixture) screen(t *testing.T, id, link, serial string) *screenStream {
	t.Helper()
	_, sub, err := event.NewSession(f.bus, link).Open([]string{TopicScreen}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	st := &screenStream{set: &f.adb.screenViews, bus: f.bus, id: id, device: serial, session: link, src: &fakeScreen{}, viewer: &stubViewer{}, release: func() {},
		quit: make(chan struct{}), released: make(chan struct{}), sub: sub}
	if err := f.adb.screenViews.add(st); err != nil {
		t.Fatal(err)
	}
	st.activity = f.adb.streamActivity(peerCall(link))
	return st
}

// logs adds a Logcat stream of serial for the link session to the table, counted as
// watchLogs counts it.
func (f *streamFixture) logs(t *testing.T, link, serial string) *logStream {
	t.Helper()
	l := &logStream{key: streamKey{session: link, serial: serial}, peer: true, cancel: func() {}, ending: make(chan struct{})}
	if _, err := f.adb.streams.add(l); err != nil {
		t.Fatal(err)
	}
	l.activity = f.adb.streamActivity(peerCall(link))
	return l
}

func (st *screenStream) hasEnded() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.ended
}

// Decision (ADR-0104 §4): an ended lease stops the holder's streams on the device, and only
// those: the live views and the Logcat streams of the link session that held it, on that
// serial; a live view stops as `deviceGone` with the cause of the host's end, and the
// streams of another link session or of another device go on. Seen failing with the match on
// the serial alone (the other link's live view was stopped too).
func TestLeaseEndStopsTheHoldersStreams(t *testing.T) {
	f := newStreamFixture(t)
	annaView := f.screen(t, "a1", "peer-anna", leaseDevice)
	annaOtherDevice := f.screen(t, "a2", "peer-anna", "R58M001")
	bobView := f.screen(t, "b1", "peer-bob", leaseDevice)
	annaLogs := []*logStream{f.logs(t, "peer-anna", leaseDevice), f.logs(t, "peer-anna", leaseDevice)}
	annaOtherLogs := f.logs(t, "peer-anna", "R58M001")
	bobLogs := f.logs(t, "peer-bob", leaseDevice)

	f.adb.endLease("peer-anna", leaseDevice)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e, err := annaView.sub.Next(ctx)
	if err != nil {
		t.Fatalf("waiting for the holder's live view to stop: %v", err)
	}
	var stopped ScreenStoppedEvent
	if err := json.Unmarshal(e.Payload, &stopped); err != nil || e.Type != EventStopped ||
		stopped.Reason != StopDeviceGone || stopped.Cause != "the host ended the lease" || stopped.Device != leaseDevice {
		t.Errorf("the holder's live view got %s %s (%v); want stopped deviceGone, the host ended the lease, %s", e.Type, e.Payload, err, leaseDevice)
	}
	if annaOtherDevice.hasEnded() || bobView.hasEnded() {
		t.Errorf("a live view on another device (ended %v) or of another link (ended %v) was stopped with the lease", annaOtherDevice.hasEnded(), bobView.hasEnded())
	}
	for i, l := range annaLogs {
		if got := l.endReason(); got != endLease {
			t.Errorf("the holder's log stream %d ended with reason %d; want endLease (%d)", i, got, endLease)
		}
	}
	if got := annaOtherLogs.endReason(); got != endNone {
		t.Errorf("a log stream on another device ended with reason %d", got)
	}
	if got := bobLogs.endReason(); got != endNone {
		t.Errorf("another link's log stream ended with reason %d", got)
	}
}

// Decision (ADR-0104 §3): a peer's open stream, a live view or a Logcat, is activity of its
// lease for as long as it is open, and the stream's end starts the idle time. A lease with
// only a Logcat stream open is not taken over after any time; 15 minutes after the last
// stream left the table it is. Seen failing with the streams not counted (the lease of a peer
// that only watched was taken over at the first call after 15 minutes).
func TestStreamsAreActivityOfTheLease(t *testing.T) {
	f := newStreamFixture(t)
	s := f.ops.Sessions()
	if _, refused := s.OccupyDevice("peer-bob", leaseDevice); refused {
		t.Fatal("bob was refused a free device")
	}
	view := f.screen(t, "b1", "peer-bob", leaseDevice)
	logs := f.logs(t, "peer-bob", leaseDevice)

	f.clk.advance(2 * time.Hour)
	if _, refused := s.OccupyDevice("peer-anna", leaseDevice); !refused {
		t.Fatal("bob's lease, with a live view and a log stream open for two hours, was taken over")
	}
	view.end(StopDetached, "")
	f.clk.advance(2 * time.Hour)
	if _, refused := s.OccupyDevice("peer-anna", leaseDevice); !refused {
		t.Fatal("bob's lease, with a log stream still open, was taken over")
	}
	f.adb.streams.remove(logs) // the stream leaves the table: its child is reaped
	f.clk.advance(capability.PeerLeaseIdle - time.Second)
	if _, refused := s.OccupyDevice("peer-anna", leaseDevice); !refused {
		t.Fatal("the idle time did not start when the last stream ended")
	}
	f.clk.advance(2 * time.Second)
	if _, refused := s.OccupyDevice("peer-anna", leaseDevice); refused {
		t.Fatal("a lease whose last stream ended 15 minutes ago was not taken over")
	}
}
