package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/policy"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// The control line and the stream's input (plan m2y step 2; ADR-0117 §3 to §5) over a source
// whose control socket is a recorder: no adb, no device, no child process. The source is the
// real screenSource with its real line, Inject and Control, built without the process that
// would give it a socket (lineFix); the streams are the real screenStreams that
// device.watchScreen makes (openStream).

// enableControl makes sure the stream input is on for the test. It is on by default in every
// build (watchscreen.go, controlOn); step 2's tests keep the call.
func enableControl(t *testing.T) {
	t.Helper()
	old := controlOn
	controlOn = true
	t.Cleanup(func() { controlOn = old })
}

// wireRec stands for the control socket: it records each message written to it.
type wireRec struct {
	mu      sync.Mutex
	msgs    [][]byte
	entered chan struct{} // when set, receives once for each write that began
	gate    chan struct{} // when set, a write waits until it is closed
	err     error         // when set, a write fails with it
}

func (r *wireRec) write(b []byte) (int, error) {
	if r.entered != nil {
		r.entered <- struct{}{}
	}
	if r.gate != nil {
		<-r.gate
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return 0, r.err
	}
	r.msgs = append(r.msgs, append([]byte(nil), b...))
	return len(b), nil
}

func (r *wireRec) all() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.msgs...)
}

// kind names a message of scrcpy's control socket as the tests read it.
func msgKind(b []byte) string {
	switch {
	case len(b) == 32 && b[0] == 2:
		return [...]string{"down", "up", "move", "cancel"}[b[1]]
	case len(b) == 28:
		return "key"
	case len(b) == 21 && b[0] == 3:
		return "scroll"
	}
	return fmt.Sprintf("? % x", b)
}

func (r *wireRec) kinds() []string {
	var out []string
	for _, m := range r.all() {
		out = append(out, msgKind(m))
	}
	return out
}

func (r *wireRec) waitN(t *testing.T, n int) {
	t.Helper()
	testtime.Eventually(t, testtime.Bound(10*time.Second), fmt.Sprintf("%d messages on the control socket", n),
		func() bool { return len(r.all()) >= n })
}

// settle waits until the line's writer has written everything queued before the call.
func settle(l *controlLine) { l.flush(testtime.Bound(10 * time.Second)) }

func pt(action screen.TouchAction, x, y uint16) screen.Event {
	return screen.Event{Kind: screen.EventTouch, Action: action, X: x, Y: y}
}

// frameOf is the wire's frame of a touch.
func frameOf(action screen.TouchAction, x, y uint16) []byte {
	return []byte{1, byte(action), byte(x >> 8), byte(x), byte(y >> 8), byte(y)}
}

// lineFix is a live-view source over a recorder, an ADB for its streams, and a way to open
// them. The source's frame is the recording's first config: 590x1280.
type lineFix struct {
	a        *ADB
	bus      *event.Bus
	src      *screenSource
	rec      *wireRec
	size     screen.Size
	released atomic.Int32
	n        int
}

func newLineFix(t *testing.T) *lineFix {
	t.Helper()
	enableControl(t)
	_, pkts, _ := ssFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	s := &screenSource{set: &screenSources{}, ctx: ctx, cancel: cancel, hub: screen.NewHub(), done: make(chan struct{})}
	s.hub.Push(pkts[0]) // the config: the frame size
	rec := &wireRec{}
	s.line = newControlLine(s.hub.Size, rec.write, s.endOnWriteError, s.hub.Done(), s.hub.Reason)
	s.line.running.Store(true)
	go s.line.run()
	bus := event.New(event.Options{})
	a := &ADB{}
	a.SetBus(bus)
	f := &lineFix{a: a, bus: bus, src: s, rec: rec, size: s.hub.Size()}
	if f.size != (screen.Size{Width: 590, Height: 1280}) {
		t.Fatalf("the recording's frame is %v", f.size)
	}
	t.Cleanup(func() {
		c, cancelAll := ssCtx()
		defer cancelAll()
		a.screenViews.StopAll(c)
		s.line.shutdown()
		cancel()
	})
	return f
}

// view is a stream opened on the fixture's source and attached, as a socket would.
type view struct {
	res    WatchScreenResult
	sub    *event.Subscription
	sess   *event.Session
	st     *screenStream
	h      ScreenHandle
	subID  string
	device string
}

func (f *lineFix) openWith(t *testing.T, who capability.Origin, kind viewKind, src screen.Source) *view {
	t.Helper()
	f.n++
	sess := event.NewSession(f.bus, fmt.Sprintf("s%d", f.n))
	t.Cleanup(sess.Close)
	device := fmt.Sprintf("D%d", f.n)
	out, err := f.a.openStream(context.Background(), &capability.Call{Origin: who, OperationID: fmt.Sprintf("op%d", f.n)},
		sess, device, kind, src, &sourceRef{drop: func() func() { f.released.Add(1); return func() {} }})
	if err != nil {
		t.Fatalf("openStream: %v", err)
	}
	res := out.(WatchScreenResult)
	sub, ok := sess.Subscription(res.SubscriptionID)
	if !ok {
		t.Fatalf("no subscription %s", res.SubscriptionID)
	}
	f.a.screenViews.mu.Lock()
	st := f.a.screenViews.byID[res.StreamID]
	f.a.screenViews.mu.Unlock()
	var h ScreenHandle
	if who.Client == capability.ClientPeer {
		h, err = f.a.AttachPeerScreen(who.Name, res.StreamID)
	} else {
		h, err = f.a.AttachScreen(res.StreamID)
	}
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	return &view{res: res, sub: sub, sess: sess, st: st, h: h, subID: res.SubscriptionID, device: device}
}

var localUI = capability.Origin{Client: capability.ClientUI}

// open opens a view of the daemon's own UI on an emulator.
func (f *lineFix) open(t *testing.T) *view { return f.openWith(t, localUI, viewKind{}, f.src) }

// want is the scrcpy bytes the daemon writes for ev against the fixture's frame.
func (f *lineFix) want(t *testing.T, ev screen.Event) []byte {
	t.Helper()
	b, err := screen.EncodeEvent(ev, f.size)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Decision (ADR-0117 §3, §4): a stream's well-formed frames reach the control socket as the
// scrcpy messages EncodeEvent builds for them, against the frame's size, in the order they
// were sent, and the stream's own cancel is a cancel. A key is its down and up pair, a
// scroll is one message.
func TestInputReachesTheControlLine(t *testing.T) {
	f := newLineFix(t)
	v := f.open(t)
	events := []screen.Event{
		pt(screen.TouchDown, 1000, 2000), pt(screen.TouchMove, 30000, 40000), pt(screen.TouchUp, 30000, 40000),
		{Kind: screen.EventKey, Key: "home"},
		{Kind: screen.EventScroll, X: 100, Y: 200, H: 0, V: 256},
		pt(screen.TouchDown, 5, 6), pt(screen.TouchCancel, 5, 6),
	}
	frames := [][]byte{
		frameOf(screen.TouchDown, 1000, 2000), frameOf(screen.TouchMove, 30000, 40000), frameOf(screen.TouchUp, 30000, 40000),
		{2, 2}, {3, 0, 100, 0, 200, 0, 0, 1, 0}, frameOf(screen.TouchDown, 5, 6), frameOf(screen.TouchCancel, 5, 6),
	}
	for i, fr := range frames {
		if err := v.h.Input(fr); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	f.rec.waitN(t, len(events))
	for i, ev := range events {
		if got, want := f.rec.all()[i], f.want(t, ev); string(got) != string(want) {
			t.Errorf("message %d is % x, want % x", i, got, want)
		}
	}
}

// Decision (ADR-0117 §1): an ended stream accepts nothing, so no input reaches the device
// after the view ended. A down sent after the end would take the free finger and be written
// if the stream did not check; it is refused and the recorder sees nothing.
func TestInputAfterTheStreamEndedSendsNothing(t *testing.T) {
	f := newLineFix(t)
	v := f.open(t)
	v.sess.Unsubscribe(v.subID) // the view ends
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the stream to end", func() bool {
		v.st.mu.Lock()
		defer v.st.mu.Unlock()
		return v.st.ended
	})
	for _, fr := range [][]byte{frameOf(screen.TouchDown, 10, 10), {2, 1}} {
		if err := v.h.Input(fr); !errors.Is(err, errInputEnded) {
			t.Errorf("an input after the end: %v, want errInputEnded", err)
		}
	}
	settle(f.src.line)
	if got := f.rec.all(); len(got) != 0 {
		t.Errorf("%d messages written for an ended stream: %v", len(got), f.rec.kinds())
	}
}

// spySource records the order of what a stream says to its source and when it lets go of it.
type spySource struct {
	*screenSource
	mu  sync.Mutex
	log []string
}

func (s *spySource) note(x string) { s.mu.Lock(); s.log = append(s.log, x); s.mu.Unlock() }
func (s *spySource) Control(h screen.Holder, ev screen.Event) error {
	if ev.Kind == screen.EventRelease {
		s.note("release-event")
	}
	return s.screenSource.Control(h, ev)
}

// Decision (ADR-0117 §5): every end of a view lifts the finger it holds with ACTION_CANCEL
// at the last point and the current frame size, never ACTION_UP, and does it before the
// viewer is released: only the client's own up writes UP. The ends are the socket closing,
// the subscription ending, the policy sweep, the lease ending and the daemon stopping. The
// lease case sets the stream's session by hand because only a paired peer's stream has one,
// and a peer's stream takes no input until step 5; the end path is the same function. Seen
// failing with the daemon's lift written as ACTION_UP.
func TestEveryEndCancelsAHeldFinger(t *testing.T) {
	type end struct {
		name string
		kind viewKind
		do   func(f *lineFix, v *view)
	}
	for _, tc := range []end{
		{"the socket closes", viewKind{}, func(f *lineFix, v *view) { v.h.Close() }},
		{"the subscription ends", viewKind{}, func(f *lineFix, v *view) { v.sess.Unsubscribe(v.subID) }},
		{"the policy sweep", viewKind{physical: true}, func(f *lineFix, v *view) { f.a.SweepViews() }},
		{"the lease ends", viewKind{}, func(f *lineFix, v *view) {
			v.st.session = "link1"
			f.a.screenViews.endLease("link1", v.device)
		}},
		{"the daemon stops", viewKind{}, func(f *lineFix, v *view) {
			c, cancel := ssCtx()
			defer cancel()
			f.a.screenViews.StopAll(c)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLineFix(t)
			spy := &spySource{screenSource: f.src}
			v := f.openWith(t, localUI, tc.kind, spy)
			for _, fr := range [][]byte{frameOf(screen.TouchDown, 100, 200), frameOf(screen.TouchMove, 31000, 42000)} {
				if err := v.h.Input(fr); err != nil {
					t.Fatal(err)
				}
			}
			f.rec.waitN(t, 2)
			tc.do(f, v)
			f.rec.waitN(t, 3)
			msgs := f.rec.all()
			if got, want := msgs[len(msgs)-1], f.want(t, pt(screen.TouchCancel, 31000, 42000)); string(got) != string(want) {
				t.Errorf("the last message is % x, want the cancel at the last point % x", got, want)
			}
			for _, k := range f.rec.kinds() {
				if k == "up" {
					t.Errorf("the daemon wrote an ACTION_UP: %v", f.rec.kinds())
				}
			}
			testtime.Eventually(t, testtime.Bound(10*time.Second), "the viewer's release", func() bool { return f.released.Load() == 1 })
			spy.mu.Lock()
			defer spy.mu.Unlock()
			if len(spy.log) != 1 {
				t.Errorf("the stream told its source %v, want one release", spy.log)
			}
		})
	}
	t.Run("a stream with no finger writes nothing", func(t *testing.T) {
		f := newLineFix(t)
		v := f.open(t)
		v.h.Close()
		testtime.Eventually(t, testtime.Bound(10*time.Second), "the viewer's release", func() bool { return f.released.Load() == 1 })
		settle(f.src.line)
		if got := f.rec.kinds(); len(got) != 0 {
			t.Errorf("an end with no finger wrote %v", got)
		}
	})
	t.Run("after its own up", func(t *testing.T) {
		f := newLineFix(t)
		v := f.open(t)
		for _, fr := range [][]byte{frameOf(screen.TouchDown, 1, 2), frameOf(screen.TouchUp, 1, 2)} {
			v.h.Input(fr)
		}
		f.rec.waitN(t, 2)
		v.h.Close()
		testtime.Eventually(t, testtime.Bound(10*time.Second), "the viewer's release", func() bool { return f.released.Load() == 1 })
		settle(f.src.line)
		if got := f.rec.kinds(); fmt.Sprint(got) != "[down up]" {
			t.Errorf("a gesture the client finished, then the end: %v, want [down up]", got)
		}
	})
}

// Decision (ADR-0117 §4): a gesture of a call and a drag from a view never interleave on the
// one pointer id. A local Inject swipe holds the finger for its whole second; a view's down,
// moves and up begun during it are dropped whole (a local holder never takes the finger from a
// local one), and the swipe's own messages are written intact. The drag is not queued for
// later: after the swipe ends, the drag's rest finds no finger. Seen failing with the finger
// state ignored by the line (a view's down always taking the finger).
func TestInjectAndTunnelNeverInterleave(t *testing.T) {
	f := newLineFix(t)
	v := f.open(t)
	swipe := screen.Swipe{Points: []screen.Point{{X: 0.2, Y: 0.8}, {X: 0.2, Y: 0.5, T: 500 * time.Millisecond}, {X: 0.2, Y: 0.2, T: time.Second}}}
	wantSwipe, err := screen.EncodeControl(swipe, f.size)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- f.src.Inject(context.Background(), screen.NewHolder(false), swipe) }()
	f.rec.waitN(t, 1) // the swipe's down is on the device
	for _, fr := range [][]byte{
		frameOf(screen.TouchDown, 40000, 40000), frameOf(screen.TouchMove, 40001, 40001), frameOf(screen.TouchMove, 40002, 40002), frameOf(screen.TouchUp, 40002, 40002),
	} {
		if err := v.h.Input(fr); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("Inject: %v", err)
	}
	settle(f.src.line)
	got := f.rec.all()
	if len(got) != len(wantSwipe) {
		t.Fatalf("%d messages written, want the swipe's %d: %v", len(got), len(wantSwipe), f.rec.kinds())
	}
	for i, m := range wantSwipe {
		if string(got[i]) != string(m.Data) {
			t.Errorf("message %d is % x, want the swipe's % x", i, got[i], m.Data)
		}
	}
}

// Decision (ADR-0117 §4): a gesture of a call waits for a view's gesture to end, bounded by
// the call's context, then writes whole. The drag's down is written first; the swipe, begun
// during it, writes nothing until the drag's up, and not at all if its context ends first.
func TestInjectWaitsForAViewsGesture(t *testing.T) {
	f := newLineFix(t)
	v := f.open(t)
	v.h.Input(frameOf(screen.TouchDown, 10, 10))
	f.rec.waitN(t, 1)
	tap := screen.Tap{X: 0.5, Y: 0.5}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := f.src.Inject(ctx, screen.NewHolder(false), tap); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("an Inject behind a held finger: %v, want the context's end", err)
	}
	if got := f.rec.kinds(); fmt.Sprint(got) != "[down]" {
		t.Fatalf("the waiting Inject wrote %v", got)
	}
	done := make(chan error, 1)
	go func() { done <- f.src.Inject(context.Background(), screen.NewHolder(false), tap) }()
	time.Sleep(100 * time.Millisecond)
	v.h.Input(frameOf(screen.TouchUp, 10, 10))
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(testtime.Bound(10 * time.Second)):
		t.Fatal("the Inject did not run after the view's gesture ended")
	}
	if got := f.rec.kinds(); fmt.Sprint(got) != "[down up down up]" {
		t.Errorf("wrote %v, want the drag then the tap", got)
	}
}

// Decision (ADR-0117 §4, ADR-0094 §5, ADR-0104 §2): the host's own view takes the finger from
// a peer's gesture, with an ACTION_CANCEL for the peer's gesture at its last point written
// first; the rest of the peer's gesture is dropped. The reverse writes nothing for the peer:
// a peer never takes the finger from a local holder, and its refused gesture is dropped
// whole. Between two local holders the first keeps it.
func TestHostUIPreemptsAPeerFinger(t *testing.T) {
	t.Run("a local view takes the finger", func(t *testing.T) {
		f := newLineFix(t)
		local := f.open(t)
		peer := screen.NewHolder(true)
		for _, ev := range []screen.Event{pt(screen.TouchDown, 100, 100), pt(screen.TouchMove, 200, 300)} {
			if err := f.src.Control(peer, ev); err != nil {
				t.Fatal(err)
			}
		}
		f.rec.waitN(t, 2)
		local.h.Input(frameOf(screen.TouchDown, 9000, 9000))
		f.rec.waitN(t, 4)
		// The peer's rest is dropped; the local gesture goes on.
		f.src.Control(peer, pt(screen.TouchMove, 300, 300))
		f.src.Control(peer, pt(screen.TouchUp, 300, 300))
		local.h.Input(frameOf(screen.TouchUp, 9000, 9000))
		f.rec.waitN(t, 5)
		settle(f.src.line)
		got := f.rec.all()
		wants := []screen.Event{pt(screen.TouchDown, 100, 100), pt(screen.TouchMove, 200, 300), pt(screen.TouchCancel, 200, 300),
			pt(screen.TouchDown, 9000, 9000), pt(screen.TouchUp, 9000, 9000)}
		if len(got) != len(wants) {
			t.Fatalf("wrote %v, want down move cancel down up", f.rec.kinds())
		}
		for i, ev := range wants {
			if string(got[i]) != string(f.want(t, ev)) {
				t.Errorf("message %d is %s, want % x", i, msgKind(got[i]), f.want(t, ev))
			}
		}
	})
	t.Run("a peer never takes it from a local holder", func(t *testing.T) {
		f := newLineFix(t)
		local := f.open(t)
		peer := screen.NewHolder(true)
		local.h.Input(frameOf(screen.TouchDown, 9000, 9000))
		f.rec.waitN(t, 1)
		for _, ev := range []screen.Event{pt(screen.TouchDown, 1, 1), pt(screen.TouchMove, 2, 2), pt(screen.TouchUp, 2, 2)} {
			f.src.Control(peer, ev)
		}
		settle(f.src.line)
		if got := f.rec.kinds(); fmt.Sprint(got) != "[down]" {
			t.Errorf("a peer's gesture on a local finger wrote %v", got)
		}
		local.h.Input(frameOf(screen.TouchUp, 9000, 9000))
		f.src.Control(peer, pt(screen.TouchMove, 3, 3)) // the rest of the dropped gesture, now with a free finger
		settle(f.src.line)
		if got := f.rec.kinds(); fmt.Sprint(got) != "[down up]" {
			t.Errorf("the dropped gesture's move after the finger was free: %v, want it dropped too", got)
		}
	})
	t.Run("between two local holders the first keeps it", func(t *testing.T) {
		f := newLineFix(t)
		first, second := f.open(t), f.open(t)
		first.h.Input(frameOf(screen.TouchDown, 1, 1))
		f.rec.waitN(t, 1)
		second.h.Input(frameOf(screen.TouchDown, 2, 2))
		second.h.Input(frameOf(screen.TouchUp, 2, 2))
		first.h.Input(frameOf(screen.TouchUp, 1, 1))
		settle(f.src.line)
		if got := f.rec.kinds(); fmt.Sprint(got) != "[down up]" {
			t.Errorf("wrote %v, want the first holder's gesture alone", got)
		}
	})
}

// Decision (ADR-0117 §4): a gesture of a local call takes the finger from a peer's, as a local
// view does: a cancel for the peer's gesture, then the call's own, whole. Seen failing with
// the preemption removed from the Inject's acquire: the call waited behind the peer's finger.
func TestLocalInjectTakesAPeerFinger(t *testing.T) {
	f := newLineFix(t)
	peer := screen.NewHolder(true)
	f.src.Control(peer, pt(screen.TouchDown, 100, 100))
	f.rec.waitN(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), testtime.Bound(10*time.Second))
	defer cancel()
	if err := f.src.Inject(ctx, screen.NewHolder(false), screen.Tap{X: 0.5, Y: 0.5}); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if got := f.rec.kinds(); fmt.Sprint(got) != "[down cancel down up]" {
		t.Errorf("wrote %v, want the peer's down, its cancel, then the call's tap", got)
	}
	// And the reverse: a peer's call waits behind a local view's finger, never taking it.
	local := f.open(t)
	local.h.Input(frameOf(screen.TouchDown, 9000, 9000))
	f.rec.waitN(t, 5)
	short, stop := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer stop()
	if err := f.src.Inject(short, screen.NewHolder(true), screen.Tap{X: 0.5, Y: 0.5}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a peer's call behind a local finger: %v, want it to wait for its context", err)
	}
	if got := f.rec.kinds(); len(got) != 5 {
		t.Errorf("the peer's call wrote behind a local finger: %v", got)
	}
}

// Decision (ADR-0117 §4, plan m2y Q2): a lent device.swipe of a paired peer is classed by its
// call's origin, so the host's own view takes the finger from it mid-gesture instead of
// being dropped for up to five seconds: a cancel for the peer's gesture, then the local down,
// and the call ends delivered with the partial-delivery warning (the device has part of it).
// Seen failing with every Inject classed local (injectScreen passing a local holder): the
// view's down was dropped and no cancel was written.
func TestHostUIPreemptsAPeerInject(t *testing.T) {
	f := newLineFix(t)
	f.a.screenLookup = func(string) screen.Source { return f.src }
	local := f.open(t)
	long := screen.Swipe{Points: []screen.Point{{X: 0.5, Y: 0.8}, {X: 0.5, Y: 0.5, T: 2500 * time.Millisecond}, {X: 0.5, Y: 0.2, T: 5 * time.Second}}}
	type out struct {
		res any
		err error
	}
	done := make(chan out, 1)
	go func() {
		r, err := f.a.injectScreen(context.Background(), capability.Origin{Client: capability.ClientPeer, Name: "anna"}, "x", long)
		done <- out{r, err}
	}()
	f.rec.waitN(t, 1) // the peer's down
	local.h.Input(frameOf(screen.TouchDown, 9000, 9000))
	select {
	case o := <-done:
		if o.err != nil {
			t.Fatalf("the preempted call failed: %v", o.err)
		}
		r := o.res.(ScreenInputResult)
		if !r.Delivered || len(r.Warnings) != 1 {
			t.Errorf("the preempted call returned %+v, want delivered with the partial-delivery warning", r)
		}
	case <-time.After(testtime.Bound(10 * time.Second)):
		t.Fatal("the peer's swipe went on after the host's view took the finger")
	}
	f.rec.waitN(t, 3)
	local.h.Input(frameOf(screen.TouchUp, 9000, 9000))
	f.rec.waitN(t, 4)
	if got := f.rec.kinds(); fmt.Sprint(got) != "[down cancel down up]" {
		t.Fatalf("wrote %v, want the peer's down, its cancel, then the local gesture", got)
	}
	// And the cancel is at the peer's last point, for the current frame.
	down := f.rec.all()[0]
	if c, ok := screen.CancelOf(down, f.size); !ok || string(f.rec.all()[1]) != string(c) {
		t.Errorf("the cancel is % x, want % x", f.rec.all()[1], c)
	}
}

// Decision (ADR-0117 §3): a stalled control socket costs a burst of moves one message. With
// the socket blocked on the down, 100 moves queue as one (a newer move replaces the older
// of the same holder), and the up behind them is never dropped; when the socket frees, the
// device gets the down, the last move, then the up. Seen failing with the coalescing
// removed: 100 moves were written.
func TestMovesCoalesceBehindAStalledWrite(t *testing.T) {
	f := newLineFix(t)
	f.rec.entered, f.rec.gate = make(chan struct{}, 8), make(chan struct{})
	v := f.open(t)
	v.h.Input(frameOf(screen.TouchDown, 10, 10))
	<-f.rec.entered // the writer is inside the down's write
	for i := 1; i <= 100; i++ {
		if err := v.h.Input(frameOf(screen.TouchMove, uint16(100*i), uint16(100*i))); err != nil {
			t.Fatal(err)
		}
	}
	v.h.Input(frameOf(screen.TouchUp, 10000, 10000))
	close(f.rec.gate)
	f.rec.waitN(t, 3)
	settle(f.src.line)
	got := f.rec.all()
	if len(got) != 3 {
		t.Fatalf("wrote %v, want down move up", f.rec.kinds())
	}
	if string(got[1]) != string(f.want(t, pt(screen.TouchMove, 10000, 10000))) {
		t.Errorf("the move written is % x, want the newest, at 10000", got[1])
	}
	if msgKind(got[0]) != "down" || msgKind(got[2]) != "up" {
		t.Errorf("the gesture's ends were not kept: %v", f.rec.kinds())
	}
}

// Decision (ADR-0117 §3): scrolls sum in the queue, clamped to 16 notches, and a key is never
// merged into anything. Behind a stalled write, three scrolls of one notch are one message of
// three notches.
func TestScrollsSumBehindAStalledWrite(t *testing.T) {
	f := newLineFix(t)
	f.rec.entered, f.rec.gate = make(chan struct{}, 8), make(chan struct{})
	v := f.open(t)
	v.h.Input([]byte{2, 1}) // a key: the writer blocks in it
	<-f.rec.entered
	for i := 0; i < 3; i++ {
		v.h.Input([]byte{3, 0, 5, 0, 6, 0, 0, 1, 0})
	}
	close(f.rec.gate)
	f.rec.waitN(t, 2)
	settle(f.src.line)
	got := f.rec.all()
	if len(got) != 2 {
		t.Fatalf("wrote %v, want a key and one scroll", f.rec.kinds())
	}
	if want := f.want(t, screen.Event{Kind: screen.EventScroll, X: 5, Y: 6, V: 768}); string(got[1]) != string(want) {
		t.Errorf("the scroll is % x, want three notches % x", got[1], want)
	}
}

// Decision (ADR-0117 §3, §2): a queue of 256 refuses the 257th event, and only the sender
// hears it (its socket is closed, 1008, by the adapter): the source goes on. The closed
// sender's queued events are dropped with its stream (an ended stream accepts nothing), so
// after it closes only the write already in flight follows, and the other viewer of the same
// device is written at once. Seen failing with the bound removed: the 257th was accepted.
func TestFullQueueClosesOnlyTheSender(t *testing.T) {
	f := newLineFix(t)
	f.rec.entered, f.rec.gate = make(chan struct{}, 16), make(chan struct{})
	a, b := f.open(t), f.open(t)
	a.h.Input([]byte{2, 1})
	<-f.rec.entered // the writer is inside a write; the queue is empty
	for i := 0; i < controlQueueCap; i++ {
		if err := a.h.Input([]byte{2, 2}); err != nil {
			t.Fatalf("event %d of a queue of %d: %v", i+1, controlQueueCap, err)
		}
	}
	if err := a.h.Input([]byte{2, 2}); !errors.Is(err, screen.ErrControlQueueFull) {
		t.Fatalf("event %d: %v, want ErrControlQueueFull", controlQueueCap+1, err)
	}
	if err := b.h.Input([]byte{2, 3}); !errors.Is(err, screen.ErrControlQueueFull) {
		t.Fatalf("the other viewer's input into a full queue: %v, want ErrControlQueueFull", err)
	}
	select {
	case <-f.src.Done():
		t.Fatal("a full queue ended the source")
	default:
	}
	a.h.Close() // the adapter closes the sender's socket: its 256 queued keys go with it
	testtime.Eventually(t, testtime.Bound(10*time.Second), "a's stream to end", func() bool {
		a.st.mu.Lock()
		defer a.st.mu.Unlock()
		return a.st.ended
	})
	if err := b.h.Input([]byte{2, 3}); err != nil {
		t.Fatalf("the other viewer's input after the sender closed: %v", err)
	}
	close(f.rec.gate)
	f.rec.waitN(t, 2)
	settle(f.src.line)
	if got := f.rec.kinds(); fmt.Sprint(got) != "[key key]" {
		t.Errorf("wrote %d messages, want the one in flight and the other viewer's: %v", len(got), got)
	}
	if got := f.rec.all(); string(got[1]) != string(f.want(t, screen.Event{Kind: screen.EventKey, Key: "appSwitch"})) {
		t.Errorf("the second message is % x, want the other viewer's appSwitch key", got[1])
	}
}

// Decision (ADR-0117 §2): an ended stream accepts nothing, and what it had queued is dropped
// with it: the keys, scrolls, and a whole click (down and up) that wait behind a stalled
// write are never written after the view was revoked or its socket closed; only the write
// already in flight is. Seen failing with the stream's release sent only when it held a
// finger: the keys and the click were written after the end.
func TestQueuedInputOfAnEndedStreamIsDropped(t *testing.T) {
	ends := map[string]func(f *lineFix, v *view, pol *flipPolicy){
		"the policy sweep": func(f *lineFix, v *view, pol *flipPolicy) {
			pol.admit.Store(false)
			f.a.SweepViews()
		},
		"the socket closes": func(f *lineFix, v *view, pol *flipPolicy) { v.h.Close() },
	}
	frames := map[string][][]byte{
		"keys":        {{2, 2}, {2, 2}, {2, 2}},
		"a scroll":    {{3, 0, 5, 0, 6, 0, 0, 1, 0}},
		"down and up": {frameOf(screen.TouchDown, 10, 10), frameOf(screen.TouchUp, 10, 10)},
	}
	for endName, end := range ends {
		for name, fr := range frames {
			t.Run(endName+"/"+name, func(t *testing.T) {
				f := newLineFix(t)
				pol := &flipPolicy{}
				pol.admit.Store(true)
				f.a.policy = pol
				f.rec.entered, f.rec.gate = make(chan struct{}, 16), make(chan struct{})
				v := f.openWith(t, localUI, viewKind{physical: true}, f.src)
				v.h.Input([]byte{2, 1})
				<-f.rec.entered // the writer is stalled inside this key
				for _, frame := range fr {
					if err := v.h.Input(frame); err != nil {
						t.Fatal(err)
					}
				}
				end(f, v, pol)
				testtime.Eventually(t, testtime.Bound(10*time.Second), "the stream to end", func() bool {
					v.st.mu.Lock()
					defer v.st.mu.Unlock()
					return v.st.ended
				})
				close(f.rec.gate)
				settle(f.src.line)
				if got := f.rec.kinds(); fmt.Sprint(got) != "[key]" {
					t.Errorf("written after the end: %v, want only the key that was in flight", got)
				}
			})
		}
	}
}

// Decision (ADR-0117 §3): at most 1,000 messages in any second per socket; the 1,001st is
// refused (the adapter closes the socket 1008). Moves without a down are ignored, but they
// are still messages and still count.
func TestInputRateClosesTheSocket(t *testing.T) {
	f := newLineFix(t)
	v := f.open(t)
	for i := 0; i < maxInputPerSecond; i++ {
		if err := v.h.Input(frameOf(screen.TouchMove, 1, 1)); err != nil {
			t.Fatalf("message %d: %v", i+1, err)
		}
	}
	if err := v.h.Input(frameOf(screen.TouchMove, 1, 1)); !errors.Is(err, errInputRate) {
		t.Fatalf("message %d inside the second: %v, want errInputRate", maxInputPerSecond+1, err)
	}
	if got := f.rec.all(); len(got) != 0 {
		t.Errorf("%d messages written for moves with no down", len(got))
	}
}

// Decision (ADR-0117 §5, plan m2y step 1's S5): when the frame size changes while a finger is
// held, the daemon writes ACTION_CANCEL carrying the NEW size, because scrcpy drops a touch
// whose size is not the current video size and a lift for the old size would leave the app's
// finger down; the client's own up afterwards finds no finger and writes nothing. It happens
// at the size change (the stream's config packet), not at the next event, and also when the
// next event is what finds the new size. Seen failing with the cancel written for the old
// size (liftLocked passing the finger's own size).
func TestSizeChangeWhileHeldCancelsWithTheNewSize(t *testing.T) {
	_, rotated := rotatedConfigs(t)
	for _, tc := range []struct {
		name   string
		notify bool
	}{{"at the stream's size change", true}, {"found by the next event", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLineFix(t)
			v := f.open(t)
			v.h.Input(frameOf(screen.TouchDown, 65535, 65535))
			v.h.Input(frameOf(screen.TouchMove, 60000, 60000))
			f.rec.waitN(t, 2)
			last := f.rec.all()[1]
			f.src.hub.Push(rotated) // the encoder restarted with the frame turned
			newSize := f.src.hub.Size()
			if newSize == f.size {
				t.Fatalf("the rotated config has the same size %v", newSize)
			}
			if tc.notify {
				f.src.line.sizeChanged()
			} else {
				v.h.Input(frameOf(screen.TouchMove, 60001, 60001))
			}
			f.rec.waitN(t, 3)
			want, _ := screen.CancelOf(last, newSize)
			if got := f.rec.all()[2]; string(got) != string(want) {
				t.Errorf("the lift is % x, want the cancel for the new frame % x", got, want)
			}
			v.h.Input(frameOf(screen.TouchUp, 60001, 60001))
			settle(f.src.line)
			if got := f.rec.kinds(); fmt.Sprint(got) != "[down move cancel]" {
				t.Errorf("wrote %v, want down move cancel (the client's up finds no finger)", got)
			}
		})
	}
}

// rotatedConfigs reads the recording of a rotation and returns its first config and the
// second, whose frame is the first's turned.
func rotatedConfigs(t *testing.T) (first, second screen.Packet) {
	t.Helper()
	data, _ := ssRotation(t)
	var configs []screen.Packet
	if err := screen.Demux(bytes.NewReader(data), func(p screen.Packet) error {
		if p.Config {
			configs = append(configs, p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(configs) != 2 {
		t.Fatalf("%d configs in the rotation", len(configs))
	}
	return configs[0], configs[1]
}

// flipPolicy is a policy whose answer a test changes.
type flipPolicy struct{ admit atomic.Bool }

func (p *flipPolicy) PhysicalAdmitted(string) policy.Admission {
	return policy.Admission{Admitted: p.admit.Load(), Cause: "flipped"}
}

// Decision (ADR-0117 §2): a view of the daemon's own client on a physical device or a remote
// id ends, within one sweep, as stopped deviceGone with the policy as the cause when the
// policy stops admitting it, and its input is refused from then on; a view the policy still
// admits goes on, an emulator's view is never ended by it, and a paired peer's view is the
// lease sweep's, not this one's. Seen failing with the admitted check removed: the first
// sweep, with the policy still admitting, ended the view.
func TestViewEndsWhenThePolicyStopsAdmittingIt(t *testing.T) {
	f := newLineFix(t)
	pol := &flipPolicy{}
	pol.admit.Store(true)
	f.a.policy = pol
	physical := f.openWith(t, localUI, viewKind{physical: true}, f.src)
	remote := f.openWith(t, localUI, viewKind{remote: true}, f.src)
	emulator := f.open(t)
	peer := f.openWith(t, capability.Origin{Client: capability.ClientPeer, Name: "anna", ID: "link1"}, viewKind{physical: true}, f.src)

	f.a.SweepViews()
	for name, v := range map[string]*view{"physical": physical, "remote": remote, "emulator": emulator, "peer": peer} {
		v.st.mu.Lock()
		ended := v.st.ended
		v.st.mu.Unlock()
		if ended {
			t.Errorf("the %s view ended while the policy admitted it", name)
		}
	}

	pol.admit.Store(false)
	f.a.SweepViews()
	for name, v := range map[string]*view{"physical": physical, "remote": remote} {
		e := screenStoppedOf(t, v.sub)
		if e.Reason != StopDeviceGone || e.Cause != viewPolicyCause {
			t.Errorf("the %s view stopped %+v, want deviceGone because of the policy", name, e)
		}
		// A remote id's stream takes no input at all in this step, which is refused before
		// the end is looked at; the physical one's is refused because it ended.
		err := v.h.Input(frameOf(screen.TouchDown, 1, 1))
		if err == nil || (name == "physical" && !errors.Is(err, errInputEnded)) {
			t.Errorf("the %s view took input after it ended: %v", name, err)
		}
	}
	for name, v := range map[string]*view{"emulator": emulator, "peer": peer} {
		v.st.mu.Lock()
		ended := v.st.ended
		v.st.mu.Unlock()
		if ended {
			t.Errorf("the %s view was ended by the policy sweep", name)
		}
	}
}

// stoppedOf reads the stopped event a stream published.
func screenStoppedOf(t *testing.T, sub *event.Subscription) ScreenStoppedEvent {
	t.Helper()
	e := next(t, sub)
	if e.Topic != TopicScreen || e.Type != EventStopped {
		t.Fatalf("event %s/%s, want %s/%s", e.Topic, e.Type, TopicScreen, EventStopped)
	}
	var se ScreenStoppedEvent
	if err := json.Unmarshal(e.Payload, &se); err != nil {
		t.Fatal(err)
	}
	return se
}

// Decision (ADR-0117 §1, plan m2y steps 2 and 5): a remote device's view takes input only when
// its source says its host granted it (the relayed source's grant, peerinput_test.go), so a
// remote id over a source that grants nothing says control false and its Input is refused,
// whatever the build. Nothing is queued for it.
func TestControlFalseForARemoteID(t *testing.T) {
	f := newLineFix(t)
	v := f.openWith(t, localUI, viewKind{remote: true}, f.src)
	if v.res.Control {
		t.Error("a remote id's result says control true")
	}
	if err := v.h.Input(frameOf(screen.TouchDown, 1, 1)); !errors.Is(err, errInputOff) {
		t.Errorf("a remote view's input: %v, want errInputOff", err)
	}
	settle(f.src.line)
	if got := f.rec.all(); len(got) != 0 {
		t.Errorf("%d messages written for a remote view", len(got))
	}
}

// Decision (ADR-0117 §3): a local view of an emulator says control true when the build takes
// input, and its handle takes well-formed frames.
func TestControlTrueForALocalView(t *testing.T) {
	f := newLineFix(t)
	v := f.open(t)
	if !v.res.Control {
		t.Error("a local view's result says control false")
	}
}

// Decision (ADR-0079 §3, ADR-0117 §4): an Inject whose write fails ends the source as failed,
// as before, and the line refuses to write again; the finger is not left held.
func TestControlWriteFailureEndsTheSource(t *testing.T) {
	f := newLineFix(t)
	v := f.open(t)
	f.rec.err = errors.New("broken pipe")
	v.h.Input(frameOf(screen.TouchDown, 1, 1))
	select {
	case <-f.src.Done():
	case <-time.After(testtime.Bound(10 * time.Second)):
		t.Fatal("a failed write did not end the source")
	}
	var end *ScreenEnd
	if !errors.As(f.src.Reason(), &end) || end.Reason != StopFailed {
		t.Errorf("reason %v, want failed", f.src.Reason())
	}
	if err := v.h.Input(frameOf(screen.TouchMove, 2, 2)); err == nil {
		t.Error("input after the source failed was accepted")
	}
}
