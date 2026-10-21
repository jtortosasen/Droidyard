package device

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// A view's input over the real screen source (plan m2y step 2): the source's control socket
// is the fixture listener's, which records the bytes it receives.

func waitControl(t *testing.T, f *ssFix, n int) []byte {
	t.Helper()
	testtime.Eventually(t, testtime.Bound(10*time.Second), fmt.Sprintf("%d control bytes", n), func() bool { return len(f.ln.controlBytes()) >= n })
	return f.ln.controlBytes()
}

// Decision (ADR-0117 §3): the frames of a real stream's socket arrive at the server's
// control socket as the scrcpy messages of the wire's three kinds, against the frame's
// size, byte for byte: a drag's down, move and up, a key's pair and a wheel notch.
func TestInputReachesTheControlSocket(t *testing.T) {
	enableControl(t)
	w := newWSFix(t)
	res, _ := w.watch(t)
	if !res.Control {
		t.Fatal("the result says control false")
	}
	h, err := w.adb.AttachScreen(res.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	size := screen.Size{Width: res.Width, Height: res.Height}
	var want []byte
	for _, in := range []struct {
		frame []byte
		ev    screen.Event
	}{
		{frameOf(screen.TouchDown, 20000, 30000), pt(screen.TouchDown, 20000, 30000)},
		{frameOf(screen.TouchMove, 21000, 31000), pt(screen.TouchMove, 21000, 31000)},
		{frameOf(screen.TouchUp, 21000, 31000), pt(screen.TouchUp, 21000, 31000)},
		{[]byte{2, 4}, screen.Event{Kind: screen.EventKey, Key: "enter"}},
		{[]byte{3, 0, 7, 0, 8, 0, 0, 0xff, 0}, screen.Event{Kind: screen.EventScroll, X: 7, Y: 8, V: -256}},
	} {
		if err := h.Input(in.frame); err != nil {
			t.Fatal(err)
		}
		b, err := screen.EncodeEvent(in.ev, size)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, b...)
	}
	if got := waitControl(t, w.ssFix, len(want)); string(got) != string(want) {
		t.Errorf("the control socket received % x, want % x", got, want)
	}
	h.Close()
	w.requireGone(t, res.StreamID)
}

// Decision (ADR-0117 §5): the last viewer of a source ending with a finger down still gets its
// cancel to the device: the source writes what its streams queued before it closes its
// sockets, so a view closed at the moment the source goes away does not leave the app's
// finger down. The control socket receives the down, then the cancel at the last point and
// never an up. Seen failing with the flush in the source's teardown removed.
func TestLastViewerEndFlushesItsCancel(t *testing.T) {
	enableControl(t)
	w := newWSFix(t)
	res, _ := w.watch(t)
	h, err := w.adb.AttachScreen(res.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	size := screen.Size{Width: res.Width, Height: res.Height}
	h.Input(frameOf(screen.TouchDown, 100, 200))
	h.Input(frameOf(screen.TouchMove, 300, 400))
	down, _ := screen.EncodeEvent(pt(screen.TouchDown, 100, 200), size)
	move, _ := screen.EncodeEvent(pt(screen.TouchMove, 300, 400), size)
	cancel, _ := screen.EncodeEvent(pt(screen.TouchCancel, 300, 400), size)
	waitControl(t, w.ssFix, len(down)+len(move))
	// The writer is held busy while the view closes, so the cancel is still queued when the
	// source goes away; it is let go once the teardown has had time to reach its sockets.
	src := w.adb.screens.running(uiSerial)
	src.line.wmu.Lock()
	h.Close() // the only viewer: the source goes with it
	time.Sleep(300 * time.Millisecond)
	src.line.wmu.Unlock()
	w.requireGone(t, res.StreamID)
	want := string(down) + string(move) + string(cancel)
	if got := waitControl(t, w.ssFix, len(want)); string(got) != want {
		t.Errorf("the control socket received % x, want the down, the move and the cancel % x", got, want)
	}
}

// Decision (ADR-0117 §5, step 1's S5): a rotation while a finger is held lifts it with a
// cancel for the new frame, on the real source: the demux tells the line when the stream
// announces a size, with no event from the client. The recording turns the frame from
// 590x1280 to 1280x590 once its tail is released. The client's later up writes nothing.
// Seen failing with the demux not telling the line (sizeChanged removed): no cancel came
// until the client's next event.
func TestRotationCancelsAHeldFingerOnTheRealSource(t *testing.T) {
	enableControl(t)
	data, keyEnd := ssRotation(t)
	f := newSSFixture(t, screenListenerOpts{head: data[:keyEnd], tail: data[keyEnd:], gateTail: true}, nil)
	w := newWSFixFrom(t, f)
	res, _ := w.watch(t)
	if res.Width != 590 || res.Height != 1280 {
		t.Fatalf("the stream starts at %dx%d", res.Width, res.Height)
	}
	h, err := w.adb.AttachScreen(res.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	old := screen.Size{Width: 590, Height: 1280}
	down, _ := screen.EncodeEvent(pt(screen.TouchDown, 65535, 65535), old)
	move, _ := screen.EncodeEvent(pt(screen.TouchMove, 60000, 60000), old)
	h.Input(frameOf(screen.TouchDown, 65535, 65535))
	h.Input(frameOf(screen.TouchMove, 60000, 60000))
	waitControl(t, f, len(down)+len(move))
	f.ln.Release() // the encoder restarts with the frame turned: 1280x590
	cancel, _ := screen.CancelOf(move, screen.Size{Width: 1280, Height: 590})
	want := string(down) + string(move) + string(cancel)
	if got := waitControl(t, f, len(want)); string(got) != want {
		t.Fatalf("the control socket received % x, want the down, the move and the new frame's cancel % x", got, want)
	}
	h.Input(frameOf(screen.TouchUp, 60000, 60000))
	time.Sleep(200 * time.Millisecond)
	if got := f.ln.controlBytes(); string(got) != want {
		t.Errorf("the client's up after the cancel wrote % x", got[len(want):])
	}
	h.Close()
	w.requireGone(t, res.StreamID)
}

// eofWatch records, from the fake server's side of the control socket, what had reached it
// and whether the child still ran when the socket ended: the server on the device reads the
// control socket in order and acts on the end of it, so a child killed before that point
// never injects the cancel that was written.
type eofWatch struct {
	ln       atomic.Pointer[screenListener]
	src      atomic.Pointer[screenSource]
	exits    bool   // the server exits on its own when its control socket ends
	stopFile string // what makes the fake child exit; guarded by mu
	mu       sync.Mutex
	seen     bool
	alive    bool
	bytes    []byte
}

func (e *eofWatch) onEOF() {
	src, ln := e.src.Load(), e.ln.Load()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seen, e.alive, e.bytes = true, true, ln.controlBytes()
	select {
	case <-src.childDone:
		e.alive = false
	default:
	}
	if e.exits {
		// Atomically: the fake reads the file while it polls, and an empty read is status 0.
		os.WriteFile(e.stopFile+".tmp", []byte("3"), 0o600)
		os.Rename(e.stopFile+".tmp", e.stopFile)
	}
}

func (e *eofWatch) result(t *testing.T) (alive bool, got []byte) {
	t.Helper()
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the control socket's end", func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.seen
	})
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.alive, e.bytes
}

// lastViewerHolding opens the only view of a real source, holds a finger (a down and a move)
// and returns what the test needs to close it.
func lastViewerHolding(t *testing.T, e *eofWatch, bound time.Duration) (w *wsFix, h ScreenHandle, src *screenSource, res WatchScreenResult, want string) {
	t.Helper()
	enableControl(t)
	old := screenChildExitBound
	screenChildExitBound = bound
	t.Cleanup(func() { screenChildExitBound = old })
	data, _, _ := ssFixture(t)
	f := newSSFixture(t, screenListenerOpts{head: data, onControlEOF: e.onEOF}, nil)
	e.ln.Store(f.ln)
	e.mu.Lock()
	e.stopFile = filepath.Join(f.dir, uiSerial+".scrcpy.stop")
	e.mu.Unlock()
	w = newWSFixFrom(t, f)
	res, _ = w.watch(t)
	src = w.adb.screens.running(uiSerial)
	e.src.Store(src)
	h, err := w.adb.AttachScreen(res.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	size := screen.Size{Width: res.Width, Height: res.Height}
	h.Input(frameOf(screen.TouchDown, 100, 200))
	h.Input(frameOf(screen.TouchMove, 300, 400))
	down, _ := screen.EncodeEvent(pt(screen.TouchDown, 100, 200), size)
	move, _ := screen.EncodeEvent(pt(screen.TouchMove, 300, 400), size)
	cancel, _ := screen.EncodeEvent(pt(screen.TouchCancel, 300, 400), size)
	waitControl(t, f, len(down)+len(move))
	return w, h, src, res, string(down) + string(move) + string(cancel)
}

// Decision (ADR-0117 §5): the last viewer's cancel must reach the app, not only the socket.
// The server on the device reads the control socket in order and ends on its end, so the
// child (the `adb shell` that runs it) is not killed when the source ends: the cancel is
// written, the control socket is closed, and the child gets screenChildExitBound to exit on
// its own; only then is it killed. Here the server exits when its control socket ends, as
// scrcpy's does: the child must still be running at that point (the control socket has
// delivered the cancel and then its end), and must have exited by itself, status 3, not been
// killed. Seen failing with the child killed by the source's context when the last viewer
// released (the old order): the child was gone at the socket's end.
func TestLastViewersCancelIsReadBeforeTheChildIsKilled(t *testing.T) {
	e := &eofWatch{exits: true}
	w, h, src, res, want := lastViewerHolding(t, e, 5*time.Second)
	h.Close()
	alive, got := e.result(t)
	if !alive {
		t.Error("the child was already killed when the control socket ended")
	}
	if string(got) != want {
		t.Errorf("the server had received % x at the socket's end, want the down, the move and the cancel % x", got, want)
	}
	select {
	case <-src.done:
	case <-time.After(testtime.Bound(30 * time.Second)):
		t.Fatal("the source did not leave")
	}
	if src.childStatus != 3 {
		t.Errorf("the child's status is %d, want 3: it exited by itself", src.childStatus)
	}
	w.requireGone(t, res.StreamID)
}

// Decision (ADR-0117 §5): the wait for the server's own exit is bounded. A child that never
// exits is killed once screenChildExitBound passed, the source leaves, and nothing is left of
// it (the record, the forward). Seen failing with the wait unbounded: the source never left.
func TestAChildThatDoesNotExitIsKilledAfterTheBound(t *testing.T) {
	e := &eofWatch{}
	w, h, src, res, _ := lastViewerHolding(t, e, 400*time.Millisecond)
	began := time.Now()
	h.Close()
	select {
	case <-src.done:
	case <-time.After(testtime.Bound(10 * time.Second)):
		t.Fatal("a child that never exits kept the source")
	}
	if d := time.Since(began); d < 400*time.Millisecond {
		t.Errorf("the source left after %v, before the bound", d)
	}
	if alive, _ := e.result(t); !alive {
		t.Error("the child was killed before the control socket's end")
	}
	if src.childStatus != -1 {
		t.Errorf("the child's status is %d, want -1: killed", src.childStatus)
	}
	w.requireGone(t, res.StreamID)
}

// Decision (ADR-0117 §5): a view that held no finger writes nothing, so its source's end is
// not slowed by the wait: the last viewer closing leaves at once, the child killed as before.
func TestAViewWithNoFingerDoesNotWaitForTheChild(t *testing.T) {
	enableControl(t)
	old := screenChildExitBound
	screenChildExitBound = 30 * time.Second
	t.Cleanup(func() { screenChildExitBound = old })
	w := newWSFix(t)
	res, _ := w.watch(t)
	src := w.adb.screens.running(uiSerial)
	h, err := w.adb.AttachScreen(res.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
	select {
	case <-src.done:
	case <-time.After(testtime.Bound(10 * time.Second)):
		t.Fatal("a view with no finger waited for the child")
	}
	w.requireGone(t, res.StreamID)
}
