package device

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// The key-frame request (plan m2y step 6; ADR-0118 §2) on the control line and on a real
// screenSource's Hub: a lagging viewer makes the source write scrcpy's reset-video, never
// more than one every keyFrameRequestEvery, never in the way of a finger.

var resetVideo = []byte{17}

// Decision (ADR-0118 §2): the source writes at most one reset-video every 2 s, however many
// lags the Hub reports: two lags 1 s apart are one message, a lag 2 s after the first is a
// second one. Without the limiter every lag (and a stalled viewer lags on every packet) would
// flood the encoder, whose answer takes up to 2 s, with requests that restart its wait.
func TestLagAsksForOneKeyFrame(t *testing.T) {
	f := newLineFix(t)
	l := f.src.line
	clock := time.Unix(2_000_000, 0)
	l.now = func() time.Time { return clock }

	l.requestKeyFrame()
	clock = clock.Add(time.Second)
	l.requestKeyFrame()
	settle(l)
	if got := f.rec.all(); len(got) != 1 || !bytes.Equal(got[0], resetVideo) {
		t.Fatalf("two lags 1 s apart wrote % x, want one reset-video (11)", got)
	}

	clock = clock.Add(time.Second) // 2 s after the first
	l.requestKeyFrame()
	settle(l)
	if got := f.rec.all(); len(got) != 2 || !bytes.Equal(got[1], resetVideo) {
		t.Fatalf("a lag 2 s after the first wrote % x in all, want a second reset-video", got)
	}
}

// Decision (ADR-0118 §2): the request has no finger and takes none: it is written in its place
// among a view's events and a gesture in progress carries on around it (it is not a lift, and
// the finger's holder is still the finger's holder). A request that lifted or replaced a move
// would cancel the user's drag at every lag.
func TestKeyFrameRequestLeavesAFingerAlone(t *testing.T) {
	f := newLineFix(t)
	v := f.open(t)
	v.h.Input(frameOf(screen.TouchDown, 100, 100))
	f.src.line.requestKeyFrame()
	v.h.Input(frameOf(screen.TouchMove, 200, 200))
	v.h.Input(frameOf(screen.TouchUp, 200, 200))
	f.rec.waitN(t, 4)
	settle(f.src.line)
	got := f.rec.all()
	if len(got) != 4 || !bytes.Equal(got[1], resetVideo) {
		t.Fatalf("wrote %v, want down, the reset-video, move, up in the order they came", f.rec.kinds())
	}
	if msgKind(got[0]) != "down" || msgKind(got[2]) != "move" || msgKind(got[3]) != "up" {
		t.Errorf("the gesture was disturbed by the request: %v", f.rec.kinds())
	}
}

// Decision (ADR-0118 §2): a full queue drops the request instead of blocking the Hub's owner,
// and a dropped request does not start the 2 s interval, so the next lag asks again.
func TestKeyFrameRequestIsDroppedByAFullQueue(t *testing.T) {
	f := newLineFix(t)
	l := f.src.line
	f.rec.entered, f.rec.gate = make(chan struct{}, 8), make(chan struct{})
	if err := l.Control(screen.NewHolder(false), pt(screen.TouchDown, 10, 10)); err != nil {
		t.Fatal(err)
	}
	<-f.rec.entered // the writer is inside the down's write: nothing leaves the queue
	l.qmu.Lock()
	for len(l.queue) < controlQueueCap {
		l.queue = append(l.queue, lineItem{}) // events the writer will skip once released
	}
	l.qmu.Unlock()
	done := make(chan struct{})
	go func() { l.requestKeyFrame(); close(done) }()
	select {
	case <-done:
	case <-time.After(testtime.Bound(5 * time.Second)):
		t.Fatal("requestKeyFrame blocked on a full queue")
	}
	l.qmu.Lock()
	n := len(l.queue)
	l.qmu.Unlock()
	if n != controlQueueCap {
		t.Fatalf("the queue holds %d items, want it still at its bound %d (the request dropped)", n, controlQueueCap)
	}
	if !l.lastReset.IsZero() {
		t.Error("a dropped request started the interval")
	}
	close(f.rec.gate)
}

// Decision (ADR-0118 §2): the production source wires its Hub's lag to its control line: a
// viewer that falls MaxViewerLag behind, on the real Hub with its real clock, makes the
// source write one reset-video, and a viewer that keeps up writes none. The source is
// newScreenSource's, so a wiring that is missing there fails here.
func TestAScreenSourceAsksForAKeyFrameWhenAViewerLags(t *testing.T) {
	_, pkts, _ := ssFixture(t)
	s := (&ADB{}).newScreenSource(&screenSources{}, &target{})
	rec := &wireRec{}
	s.line.write = rec.write
	s.line.running.Store(true)
	go s.line.run()
	t.Cleanup(func() { s.line.shutdown(); s.cancel() })

	steady, behind := s.hub.Join(), s.hub.Join()
	defer steady.Close()
	defer behind.Close()
	s.hub.Push(pkts[0])
	s.hub.Push(pkts[1]) // the key frame
	ctx, cancel := context.WithTimeout(context.Background(), testtime.Bound(5*time.Second))
	defer cancel()
	for _, v := range []screen.Viewer{steady, behind} { // the age counts from a first read
		if _, err := v.Next(ctx); err != nil {
			t.Fatal(err)
		}
	}
	s.hub.Push(screen.Packet{PTS: 1, Data: []byte("delta")})
	if _, err := steady.Next(ctx); err != nil {
		t.Fatal(err)
	}
	settle(s.line)
	if n := len(rec.all()); n != 0 {
		t.Fatalf("%d messages written before any lag", n)
	}

	time.Sleep(screen.MaxViewerLag + 100*time.Millisecond)
	s.hub.Push(screen.Packet{PTS: 2, Data: []byte("delta")})
	rec.waitN(t, 1)
	settle(s.line)
	if got := rec.all(); len(got) != 1 || !bytes.Equal(got[0], resetVideo) {
		t.Fatalf("a lagging viewer made the source write % x, want one reset-video (11)", got)
	}
}

// Decision (ADR-0118 §1, the close verifier's B2): a relayed source's Hub applies no age bound,
// because its viewer could only wait for a key frame its host's encoder will not send on a
// static screen. A viewer of the real relayed source that reads MaxViewerLag late keeps its
// packets and is not rejoined; newRelayedSource's own Hub is tested, so a constructor that
// leaves the bound on fails here.
func TestARelayedSourceDoesNotResetALateViewer(t *testing.T) {
	_, pkts, _ := ssFixture(t)
	s := (&ADB{}).newRelayedSource(&relayedScreens{}, &remoteTarget{remote: "far", id: "far/X1", serial: "X1"})
	t.Cleanup(s.cancel)
	v := s.hub.Join()
	defer v.Close()
	s.hub.Push(pkts[0])
	s.hub.Push(pkts[1])
	ctx, cancel := context.WithTimeout(context.Background(), testtime.Bound(5*time.Second))
	defer cancel()
	if _, err := v.Next(ctx); err != nil {
		t.Fatal(err)
	}
	s.hub.Push(screen.Packet{PTS: 1, Data: []byte("delta")})
	time.Sleep(screen.MaxViewerLag + 100*time.Millisecond)
	s.hub.Push(screen.Packet{PTS: 2, Data: []byte("delta2")})
	for _, want := range []int64{1, 2} {
		p, err := v.Next(ctx)
		if err != nil || p.PTS != want {
			t.Fatalf("a viewer reading late got pts %d, %v; want pts %d kept", p.PTS, err, want)
		}
	}
	if n := s.hub.Rejoins(); n != 0 {
		t.Fatalf("%d rejoins on a relayed source, want none", n)
	}
}
