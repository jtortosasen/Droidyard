package device

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// The input of a paired peer's view, on the host (plan m2y step 5; ADR-0117 §1, §4, ADR-0094
// §5), and of this daemon's view of a remote device, on the client, over the fixtures of
// step 2 (controlline_test.go): the real streams and the real control line, a recorder for
// the control socket.

var annaOrigin = capability.Origin{Client: capability.ClientPeer, Name: "anna", ID: "link1"}

// openPeer opens a view as the paired peer anna's device.watchScreen made it.
func (f *lineFix) openPeer(t *testing.T) *view {
	t.Helper()
	return f.openWith(t, annaOrigin, viewKind{physical: true}, f.src)
}

// Decision (ADR-0117 §1, plan m2y step 5): a paired peer's view of a device the host lends it
// takes input, authorized by the device.watchScreen call that opened it: its result says
// control true, a well-formed frame reaches the control socket as scrcpy's message, a frame
// that is not the wire is still the client's error (1003 at the adapter), and the gesture is
// recorded as the peer's (client peer, its name) so the host's Activity and trace say who
// did it. Seen failing with the peer's stream refused as before (owner != "" gave errInputOff):
// the result said control false and nothing reached the socket.
func TestPeerViewTakesInputAndRecordsItAsThePeers(t *testing.T) {
	f := newLineFix(t)
	log := &gestureLog{}
	f.a.SetRecorder(log)
	v := f.openPeer(t)
	if !v.res.Control {
		t.Fatal("a peer's result says control false")
	}
	if err := v.h.Input(frameOf(screen.TouchDown, 100, 200)); err != nil {
		t.Fatalf("a peer's touch: %v", err)
	}
	if err := v.h.Input(frameOf(screen.TouchUp, 100, 200)); err != nil {
		t.Fatalf("a peer's lift: %v", err)
	}
	f.rec.waitN(t, 2)
	if got := f.rec.kinds(); fmt.Sprint(got) != "[down up]" {
		t.Errorf("wrote %v, want down up", got)
	}
	if got, want := f.rec.all()[0], f.want(t, pt(screen.TouchDown, 100, 200)); !bytes.Equal(got, want) {
		t.Errorf("the touch is % x, want % x", got, want)
	}
	recs := log.wait(t, 1)
	if r := recs[0]; r.Capability != "device.tap" || r.Origin != annaOrigin || r.State != capability.StateCompleted {
		t.Errorf("the record is %s of %+v (%s); want a completed device.tap of the peer anna", r.Capability, r.Origin, r.State)
	}
	if err := v.h.Input([]byte{9}); !errors.Is(err, screen.ErrBadControl) {
		t.Errorf("a malformed frame: %v, want ErrBadControl", err)
	}
}

// Decision (ADR-0117 §4, ADR-0094 §5): a peer's view is a peer holder, so the host's own view
// takes the finger from it with a cancel written first, and the rest of the peer's gesture is
// dropped. Seen failing with the stream's holder made local for a peer: the host's down was
// dropped behind the peer's finger and no cancel was written.
func TestHostViewPreemptsAPeerView(t *testing.T) {
	f := newLineFix(t)
	peer := f.openPeer(t)
	local := f.open(t)
	peer.h.Input(frameOf(screen.TouchDown, 100, 100))
	f.rec.waitN(t, 1)
	local.h.Input(frameOf(screen.TouchDown, 9000, 9000))
	f.rec.waitN(t, 3)
	peer.h.Input(frameOf(screen.TouchMove, 300, 300))
	peer.h.Input(frameOf(screen.TouchUp, 300, 300))
	local.h.Input(frameOf(screen.TouchUp, 9000, 9000))
	settle(f.src.line)
	if got := f.rec.kinds(); fmt.Sprint(got) != "[down cancel down up]" {
		t.Errorf("wrote %v, want the peer's down, its cancel, the host's down and up", got)
	}
}

// Decision (plan m2y step 5, rule (a), against ADR-0094 §5): of a source's 256 queue entries
// the peer holders' events together take at most 128. With the socket stalled, a peer's 129th
// event is refused (its socket alone is closed, 1008, by the adapter) while the host's own
// view still has the other 128 entries, and is refused only when all 256 are taken; the
// source goes on and the host's view stays. Seen failing with one shared bound of 256: the
// peer's 129th event was accepted, and the host's view was refused after 128 of its own.
func TestPeerCannotFillTheQueueTheHostsViewsNeed(t *testing.T) {
	f := newLineFix(t)
	f.rec.entered, f.rec.gate = make(chan struct{}, 16), make(chan struct{})
	open := sync.OnceFunc(func() { close(f.rec.gate) })
	defer open() // a failure before the writer is let go must not leave it blocked
	peer := f.openPeer(t)
	local := f.open(t)
	local.h.Input([]byte{2, 1})
	<-f.rec.entered // the writer is inside a write; the queue is empty
	for i := 0; i < controlPeerQueueCap; i++ {
		if err := peer.h.Input([]byte{2, 2}); err != nil {
			t.Fatalf("the peer's event %d of %d: %v", i+1, controlPeerQueueCap, err)
		}
	}
	if err := peer.h.Input([]byte{2, 2}); !errors.Is(err, screen.ErrControlQueueFull) {
		t.Fatalf("the peer's event %d: %v, want ErrControlQueueFull", controlPeerQueueCap+1, err)
	}
	for i := 0; i < controlQueueCap-controlPeerQueueCap; i++ {
		if err := local.h.Input([]byte{2, 3}); err != nil {
			t.Fatalf("the host's event %d behind a peer's full share: %v", i+1, err)
		}
	}
	if err := local.h.Input([]byte{2, 3}); !errors.Is(err, screen.ErrControlQueueFull) {
		t.Errorf("the host's event into all 256 taken: %v, want ErrControlQueueFull", err)
	}
	select {
	case <-f.src.Done():
		t.Fatal("a peer's flood ended the source")
	default:
	}
	// The adapter closes only the peer's socket; the host's view is still accepted once the
	// peer's queued events go with its stream.
	peer.h.Close()
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the peer's stream to end", func() bool {
		peer.st.mu.Lock()
		defer peer.st.mu.Unlock()
		return peer.st.ended
	})
	if err := local.h.Input([]byte{2, 3}); err != nil {
		t.Errorf("the host's input after the peer's socket closed: %v", err)
	}
	local.st.mu.Lock()
	ended := local.st.ended
	local.st.mu.Unlock()
	if ended {
		t.Error("the host's own stream ended with the peer's")
	}
	open()
}

// Decision (plan m2y step 5, rule (b), ADR-0094 §5): a peer's key or wheel is dropped while a
// local holder holds the finger, as a peer's touch is; a local key during a local drag is not
// (the finger gates only the peer), and a peer's key goes through when the peer itself holds
// the finger or no one does. Seen failing with the check removed: the peer's key and wheel
// were written into the host's drag.
func TestPeerKeyAndScrollAreDroppedWhileTheHostsUserHoldsTheFinger(t *testing.T) {
	f := newLineFix(t)
	peer := f.openPeer(t)
	local := f.open(t)
	local.h.Input(frameOf(screen.TouchDown, 9000, 9000))
	f.rec.waitN(t, 1)
	if err := peer.h.Input([]byte{2, 2}); err != nil {
		t.Fatal(err)
	}
	if err := peer.h.Input(scrollFrame(5, 6, 0, 256)); err != nil {
		t.Fatal(err)
	}
	local.h.Input([]byte{2, 1}) // the host's own key, in its own drag
	settle(f.src.line)
	if got := f.rec.kinds(); fmt.Sprint(got) != "[down key]" {
		t.Fatalf("wrote %v, want the host's down and its own key only", got)
	}
	if got, want := f.rec.all()[1], f.want(t, screen.Event{Kind: screen.EventKey, Key: "back"}); !bytes.Equal(got, want) {
		t.Errorf("the key written is % x, want the host's back key % x", got, want)
	}
	local.h.Input(frameOf(screen.TouchUp, 9000, 9000))
	peer.h.Input([]byte{2, 2})
	peer.h.Input(frameOf(screen.TouchDown, 1, 1))
	peer.h.Input(scrollFrame(5, 6, 0, 256))
	settle(f.src.line)
	if got := f.rec.kinds(); fmt.Sprint(got) != "[down key up key down scroll]" {
		t.Errorf("wrote %v, want the peer's key and wheel through once the finger is free or its own", got)
	}
}

// Decision (ADR-0117 §2, §5): a peer's view ends in every way the host ends it (here the
// lease sweep's endLease), and then writes a cancel for its finger and never an up, and
// accepts nothing. Seen failing with the release skipped for a peer's stream: the finger
// stayed down on the device.
func TestPeerViewEndedByTheLeaseCancelsItsFinger(t *testing.T) {
	f := newLineFix(t)
	v := f.openPeer(t)
	v.h.Input(frameOf(screen.TouchDown, 100, 100))
	f.rec.waitN(t, 1)
	f.a.screenViews.endLease(v.sess.ID(), v.device)
	f.rec.waitN(t, 2)
	if err := v.h.Input(frameOf(screen.TouchUp, 100, 100)); !errors.Is(err, errInputEnded) {
		t.Errorf("an input after the lease ended: %v, want errInputEnded", err)
	}
	settle(f.src.line)
	if got := f.rec.kinds(); fmt.Sprint(got) != "[down cancel]" {
		t.Errorf("wrote %v, want the down and its cancel, never an up", got)
	}
}

// Decision (R6, ADR-0098 §3): the host closing the video socket with the revocation code
// (4403, the peer is no longer paired) is `stopped deviceGone` at once, by the daemon's own
// cause; any other close (the stream's own 4410, a broken socket) still waits its grace for
// the host's `stopped` or the link's loss and then fails. Seen failing with the code not
// read: the 4403 row waited the grace and ended `failed`.
func TestRelayedViewReadsARevokedPairingAsDeviceGone(t *testing.T) {
	old := relayScreenGrace
	relayScreenGrace = 300 * time.Millisecond
	t.Cleanup(func() { relayScreenGrace = old })
	for name, c := range map[string]struct {
		err    error
		reason string
		prompt bool
	}{
		"revoked":      {websocket.CloseError{Code: capability.PeerCloseRevoked}, StopDeviceGone, true},
		"stream ended": {websocket.CloseError{Code: 4410}, StopFailed, false},
		"broken":       {errors.New("broken pipe"), StopFailed, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newLineFix(t)
			rs := f.a.newRelayedSource(&relayedScreens{}, &remoteTarget{remote: "far", id: "far/X1", serial: "X1"})
			start := time.Now()
			rs.readEnded(c.err)
			took := time.Since(start)
			var end *ScreenEnd
			if !errors.As(rs.hub.Reason(), &end) || end.Reason != c.reason {
				t.Fatalf("reason %v, want %s", rs.hub.Reason(), c.reason)
			}
			if c.prompt && (took > relayScreenGrace/2 || !strings.Contains(end.Cause, "revoked the pairing")) {
				t.Errorf("the revocation took %v (cause %q); want it at once, saying the pairing was revoked", took, end.Cause)
			}
			if !c.prompt && took < relayScreenGrace {
				t.Errorf("a close that is not a revocation ended after %v, before its grace", took)
			}
		})
	}
}

// Decision (ADR-0117 §5): the client line's lift of a finger on its own (the line's cancelOf,
// used wherever the line lifts a finger it still holds) is the wire's cancel at the finger's
// last point, never an up, and refuses what is not a touch frame. Seen failing with the up
// built: the frame's action byte was 1.
func TestWireCancelOfIsTheCancelAtTheLastPoint(t *testing.T) {
	for _, action := range []screen.TouchAction{screen.TouchDown, screen.TouchMove} {
		got, ok := wireCancelOf(frameOf(action, 0x1234, 0xfedc), screen.Size{})
		if !ok || !bytes.Equal(got, frameOf(screen.TouchCancel, 0x1234, 0xfedc)) {
			t.Errorf("after a %d frame: % x (%v), want the cancel at its point", action, got, ok)
		}
	}
	for _, last := range [][]byte{nil, {2, 2}, {1, 0, 0}, scrollFrame(1, 2, 3, 4)} {
		if got, ok := wireCancelOf(last, screen.Size{}); ok {
			t.Errorf("a cancel % x for the non-touch frame % x", got, last)
		}
	}
}

// relayedFix is a view of a remote device: a relayed source whose control line writes to a
// recorder in place of the host's socket, as the real source's would after start.
type relayedFix struct {
	*lineFix
	rs  *relayedSource
	rec *wireRec
}

func newRelayedFix(t *testing.T, granted bool) *relayedFix {
	t.Helper()
	f := newLineFix(t)
	rs := f.a.newRelayedSource(&relayedScreens{}, &remoteTarget{remote: "far", id: "far/X1", serial: "X1"})
	rs.size = screen.Size{Width: 590, Height: 1280}
	rs.control = granted
	rec := &wireRec{}
	rs.line.write = rec.write
	rs.line.running.Store(true)
	go rs.line.run()
	t.Cleanup(rs.line.shutdown)
	return &relayedFix{lineFix: f, rs: rs, rec: rec}
}

func (r *relayedFix) open(t *testing.T) *view {
	t.Helper()
	return r.openWith(t, localUI, viewKind{remote: true}, r.rs)
}

func (r *relayedFix) frames() [][]byte { return r.rec.all() }

// Decision (ADR-0117 §1, plan m2y step 5): a remote device's view takes input exactly when
// its host granted it: a host whose result lacks control (an older one) gives a view whose
// result says control false, whose input is refused and of which nothing is sent; one that
// granted it gives control true and the client's frame reaches the host's socket as the same
// wire frame. Seen failing with control true for every remote id: the older host's view said
// true and its first frame was queued for a host that does not read it.
func TestRelayedViewTakesInputOnlyWhenTheHostGrantedIt(t *testing.T) {
	t.Run("an older host", func(t *testing.T) {
		r := newRelayedFix(t, false)
		v := r.open(t)
		if v.res.Control {
			t.Error("the result says control true without the host's grant")
		}
		if err := v.h.Input(frameOf(screen.TouchDown, 1, 1)); !errors.Is(err, errInputOff) {
			t.Errorf("input to a view without control: %v, want errInputOff", err)
		}
		if err := r.rs.Control(screen.NewHolder(false), pt(screen.TouchDown, 1, 1)); !errors.Is(err, errRelayedInput) {
			t.Errorf("Control without the grant: %v, want errRelayedInput", err)
		}
		settle(r.rs.line)
		if got := r.frames(); len(got) != 0 {
			t.Errorf("%d frames sent to a host that granted nothing", len(got))
		}
	})
	t.Run("a host that granted it", func(t *testing.T) {
		r := newRelayedFix(t, true)
		v := r.open(t)
		if !v.res.Control {
			t.Fatal("the result says control false with the host's grant")
		}
		for _, fr := range [][]byte{frameOf(screen.TouchDown, 1000, 2000), frameOf(screen.TouchUp, 1000, 2000), {2, 4}, scrollFrame(7, 8, -256, 512)} {
			if err := v.h.Input(fr); err != nil {
				t.Fatalf("frame % x: %v", fr, err)
			}
		}
		testtime.Eventually(t, testtime.Bound(10*time.Second), "four frames", func() bool { return len(r.frames()) >= 4 })
		want := [][]byte{frameOf(screen.TouchDown, 1000, 2000), frameOf(screen.TouchUp, 1000, 2000), {2, 4}, scrollFrame(7, 8, -256, 512)}
		for i, w := range want {
			if got := r.frames()[i]; !bytes.Equal(got, w) {
				t.Errorf("frame %d sent to the host is % x, want % x", i, got, w)
			}
		}
	})
}

// Decision (ADR-0117 §4, plan m2y step 5): the client keeps one finger among its own views of
// the remote device, since they share the one stream to the host (which sees one peer
// holder): the first view's gesture is sent whole and a second view's down is dropped with the
// rest of its gesture; once the finger is free the second view's gesture goes. Seen failing
// with no finger on the client: the second view's down and up were sent into the first's drag.
func TestRelayedViewKeepsTheClientsFinger(t *testing.T) {
	r := newRelayedFix(t, true)
	a, b := r.open(t), r.open(t)
	a.h.Input(frameOf(screen.TouchDown, 1, 1))
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the first view's down", func() bool { return len(r.frames()) == 1 })
	b.h.Input(frameOf(screen.TouchDown, 2, 2))
	b.h.Input(frameOf(screen.TouchMove, 3, 3))
	b.h.Input(frameOf(screen.TouchUp, 3, 3))
	a.h.Input(frameOf(screen.TouchUp, 1, 1))
	settle(r.rs.line)
	b.h.Input(frameOf(screen.TouchDown, 4, 4))
	b.h.Input(frameOf(screen.TouchUp, 4, 4))
	settle(r.rs.line)
	want := [][]byte{frameOf(screen.TouchDown, 1, 1), frameOf(screen.TouchUp, 1, 1), frameOf(screen.TouchDown, 4, 4), frameOf(screen.TouchUp, 4, 4)}
	got := r.frames()
	if len(got) != len(want) {
		t.Fatalf("sent %d frames, want %d: % x", len(got), len(want), got)
	}
	for i, w := range want {
		if !bytes.Equal(got[i], w) {
			t.Errorf("frame %d is % x, want % x", i, got[i], w)
		}
	}
}

// Decision (ADR-0117 §5, plan m2y step 5): the end of a client's view with a finger down
// writes the cancel frame at the finger's last point and never the up, so the host's device is
// never told a click or a drop the user began was completed. Seen failing with the cancel
// frame built as an up (action 1): the host would have ended the drag as a drop.
func TestRelayedViewEndWritesACancelFrameNotAnUp(t *testing.T) {
	r := newRelayedFix(t, true)
	v := r.open(t)
	v.h.Input(frameOf(screen.TouchDown, 10, 20))
	v.h.Input(frameOf(screen.TouchMove, 3000, 4000))
	// What the view ended with still queued is dropped (ADR-0117 §2), so the two are written
	// before it ends.
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the down and the move", func() bool { return len(r.frames()) >= 2 })
	v.sess.Unsubscribe(v.subID)
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the stream to end", func() bool {
		v.st.mu.Lock()
		defer v.st.mu.Unlock()
		return v.st.ended
	})
	settle(r.rs.line)
	want := [][]byte{frameOf(screen.TouchDown, 10, 20), frameOf(screen.TouchMove, 3000, 4000), frameOf(screen.TouchCancel, 3000, 4000)}
	got := r.frames()
	if len(got) != len(want) {
		t.Fatalf("sent %d frames, want the down, the move and a cancel: % x", len(got), got)
	}
	for i, w := range want {
		if !bytes.Equal(got[i], w) {
			t.Errorf("frame %d is % x, want % x", i, got[i], w)
		}
	}
}
