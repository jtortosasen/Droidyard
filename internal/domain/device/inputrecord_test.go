package device

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// The records of a view's gestures (plan m2y step 3; ADR-0117 §6) over the control-line
// fixture of step 2: the real streams and source, a recorder for the control socket, and, in
// place of the operation store, a recorder of what the stream records.

// gestureLog is the Recorder the ADB records into: it keeps each Recorded, and can hold
// Record until gate is closed, as a store whose lock is held for long would.
type gestureLog struct {
	mu      sync.Mutex
	got     []capability.Recorded
	gate    chan struct{}
	entered chan struct{} // receives once for each Record that began, when set
}

func (g *gestureLog) Record(r capability.Recorded) (string, error) {
	if g.entered != nil {
		g.entered <- struct{}{}
	}
	if g.gate != nil {
		<-g.gate
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.got = append(g.got, r)
	return fmt.Sprintf("op-%d", len(g.got)), nil
}

func (g *gestureLog) all() []capability.Recorded {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]capability.Recorded(nil), g.got...)
}

// wait returns the first n records, waiting for them.
func (g *gestureLog) wait(t *testing.T, n int) []capability.Recorded {
	t.Helper()
	testtime.Eventually(t, testtime.Bound(10*time.Second), fmt.Sprintf("%d gesture records", n), func() bool { return len(g.all()) >= n })
	return g.all()
}

// fakeClock is the stream's clock, moved by the test.
type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

// recView is a view on a fixture whose gestures go to a gestureLog and whose clock is the
// test's.
type recView struct {
	*view
	f     *lineFix
	log   *gestureLog
	clock *fakeClock
}

func openRecorded(t *testing.T) *recView {
	t.Helper()
	f := newLineFix(t)
	log := &gestureLog{}
	f.a.SetRecorder(log)
	v := f.open(t)
	clock := &fakeClock{at: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}
	v.st.now = clock.now
	return &recView{view: v, f: f, log: log, clock: clock}
}

func (v *recView) send(t *testing.T, frame []byte) {
	t.Helper()
	if err := v.h.Input(frame); err != nil {
		t.Fatalf("Input % x: %v", frame, err)
	}
}

func (v *recView) touch(t *testing.T, action screen.TouchAction, x, y uint16) {
	v.send(t, frameOf(action, x, y))
}

func scrollFrame(x, y uint16, h, v int16) []byte {
	return []byte{3, byte(x >> 8), byte(x), byte(y >> 8), byte(y), byte(uint16(h) >> 8), byte(h), byte(uint16(v) >> 8), byte(v)}
}

// validParams runs the descriptor's own validation of a record's params: the same input()
// device.tap, swipe, pressKey and scroll run on a call's, so a record is one a call could have
// been. It returns the input the capability would send.
func validParams(t *testing.T, r capability.Recorded) screen.Input {
	t.Helper()
	var in screen.Input
	var err error
	switch p := r.Params.(type) {
	case ScreenTapParams:
		in, err = p.input()
	case ScreenSwipeParams:
		in, err = p.input()
	case ScreenKeyParams:
		in, err = p.input()
	case ScreenScrollParams:
		in, err = p.input()
	default:
		t.Fatalf("%s: params of type %T", r.Capability, r.Params)
	}
	if err != nil {
		t.Errorf("%s: the record's params fail the capability's own validation: %v", r.Capability, err)
	}
	return in
}

// drag sends a down at (x0, 20000), n moves spread evenly over total and an up (or a cancel),
// moving across the screen.
func (v *recView) drag(t *testing.T, n int, total time.Duration, end screen.TouchAction) {
	t.Helper()
	const x0, x1 = 10000, 50000
	v.touch(t, screen.TouchDown, x0, 20000)
	step := total / time.Duration(n+1)
	var x uint16 = x0
	for i := 1; i <= n; i++ {
		v.clock.advance(step)
		x = uint16(x0 + (x1-x0)*i/(n+1))
		v.touch(t, screen.TouchMove, x, 20000+uint16(i))
	}
	v.clock.advance(total - step*time.Duration(n))
	v.touch(t, end, x, 20000+uint16(n))
}

func swipeOf(t *testing.T, r capability.Recorded) []SwipePoint {
	t.Helper()
	p, ok := r.Params.(ScreenSwipeParams)
	if !ok {
		t.Fatalf("%s: params %T, want a swipe's", r.Capability, r.Params)
	}
	return p.Path
}

// Decision (ADR-0117 §6, plan m2y Q5): each gesture is one operation of the capability that
// replays it, with the gesture's own state, and params that a call to that capability would
// have been allowed to make. A down and an up that stay within 1 % of the frame and last at
// most 2 s are a device.tap (the hold its durationMs); any other is a device.swipe of at most
// 64 points resampled by time, a gesture over 5 s having its times scaled into 5 s while the
// operation keeps its real duration; a cancel is the same shape in state cancelled; a key is
// device.pressKey; the wheel events less than 250 ms apart at one point are one device.scroll,
// split where their sum passes the bound; a gesture whose write failed (the source ending
// failed under it) is failed with DEVICE_STREAM_FAILED. Seen failing with the drag's
// resampling removed: the 3 s and 7 s drags carried 300 and 700 points and failed the
// swipe's own validation (path of 2 to 64 points).
func TestGestureRecords(t *testing.T) {
	type want struct {
		capability string
		state      capability.State
		ms         time.Duration
	}
	cases := []struct {
		name string
		do   func(t *testing.T, v *recView)
		want []want
		// check looks at the single record further.
		check func(t *testing.T, r capability.Recorded, in screen.Input)
	}{
		{"a tap", func(t *testing.T, v *recView) {
			v.touch(t, screen.TouchDown, 16384, 32768)
			v.clock.advance(60 * time.Millisecond)
			v.touch(t, screen.TouchUp, 16384, 32768)
		}, []want{{"device.tap", capability.StateCompleted, 60 * time.Millisecond}}, func(t *testing.T, r capability.Recorded, in screen.Input) {
			p := r.Params.(ScreenTapParams)
			if *p.DurationMs != 60 || *p.X < 0.2499 || *p.X > 0.2501 || *p.Y < 0.4999 || *p.Y > 0.5001 || p.Device != "D1" {
				t.Errorf("tap params %+v, want the down point, 60 ms, device D1", p)
			}
			if _, ok := r.Result.(ScreenInputResult); !ok {
				t.Errorf("result %#v", r.Result)
			}
		}},
		{"a second down is the first gesture's", func(t *testing.T, v *recView) {
			v.touch(t, screen.TouchDown, 16384, 32768)
			v.clock.advance(20 * time.Millisecond)
			v.touch(t, screen.TouchDown, 50000, 50000) // the source keeps the first finger
			v.clock.advance(40 * time.Millisecond)
			v.touch(t, screen.TouchUp, 16384, 32768)
		}, []want{{"device.tap", capability.StateCompleted, 60 * time.Millisecond}}, nil},
		{"a long press", func(t *testing.T, v *recView) {
			v.touch(t, screen.TouchDown, 30000, 30000)
			v.clock.advance(1900 * time.Millisecond)
			v.touch(t, screen.TouchMove, 30010, 30010) // a tremor within 1 %
			v.clock.advance(100 * time.Millisecond)
			v.touch(t, screen.TouchUp, 30010, 30010)
		}, []want{{"device.tap", capability.StateCompleted, 2 * time.Second}}, func(t *testing.T, r capability.Recorded, in screen.Input) {
			if got := *r.Params.(ScreenTapParams).DurationMs; got != 2000 {
				t.Errorf("durationMs %d, want the 2,000 ms hold", got)
			}
		}},
		{"a hold past 2 s is not a tap", func(t *testing.T, v *recView) {
			v.touch(t, screen.TouchDown, 30000, 30000)
			v.clock.advance(2100 * time.Millisecond)
			v.touch(t, screen.TouchUp, 30000, 30000)
		}, []want{{"device.swipe", capability.StateCompleted, 2100 * time.Millisecond}}, func(t *testing.T, r capability.Recorded, in screen.Input) {
			path := swipeOf(t, r)
			if len(path) != 2 || *path[0].T != 0 || *path[1].T != 2100 {
				t.Errorf("a held touch is a swipe of its down and its up: %d points", len(path))
			}
		}},
		{"a travel past 1 % is not a tap", func(t *testing.T, v *recView) {
			v.touch(t, screen.TouchDown, 30000, 30000)
			v.clock.advance(50 * time.Millisecond)
			v.touch(t, screen.TouchUp, 30000+700, 30000)
		}, []want{{"device.swipe", capability.StateCompleted, 50 * time.Millisecond}}, nil},
		{"a 3 s drag", func(t *testing.T, v *recView) { v.drag(t, 299, 3*time.Second, screen.TouchUp) },
			[]want{{"device.swipe", capability.StateCompleted, 3 * time.Second}}, func(t *testing.T, r capability.Recorded, in screen.Input) {
				path := swipeOf(t, r)
				if len(path) != 64 || *path[0].T != 0 || *path[len(path)-1].T != 3000 {
					t.Errorf("a 3 s drag of 301 points: %d points, last t %d; want 64, 3000", len(path), *path[len(path)-1].T)
				}
				if x := *path[len(path)-1].X; x < 0.76 || x > 0.77 {
					t.Errorf("the last point is at x %v, want the drag's end near 50000/65535", x)
				}
			}},
		{"a 7 s drag", func(t *testing.T, v *recView) { v.drag(t, 699, 7*time.Second, screen.TouchUp) },
			[]want{{"device.swipe", capability.StateCompleted, 7 * time.Second}}, func(t *testing.T, r capability.Recorded, in screen.Input) {
				path := swipeOf(t, r)
				if len(path) != 64 || *path[0].T != 0 || *path[len(path)-1].T != 5000 {
					t.Errorf("a 7 s drag: %d points, last t %d; want 64 scaled into 5000", len(path), *path[len(path)-1].T)
				}
				// Scaled, not cut: the point half way along the screen was reached half way
				// through the drag, so it sits half way through the 5 s.
				mid := path[0]
				for _, p := range path {
					if d := *p.X - 30000.0/65535; d*d < (*mid.X-30000.0/65535)*(*mid.X-30000.0/65535) {
						mid = p
					}
				}
				if *mid.T < 2300 || *mid.T > 2700 {
					t.Errorf("the half-way point is at t %d, want about 2500: the times are scaled into 5 s", *mid.T)
				}
			}},
		{"a cancelled drag", func(t *testing.T, v *recView) { v.drag(t, 20, 400*time.Millisecond, screen.TouchCancel) },
			[]want{{"device.swipe", capability.StateCancelled, 400 * time.Millisecond}}, nil},
		{"a cancelled tap", func(t *testing.T, v *recView) {
			v.touch(t, screen.TouchDown, 100, 100)
			v.clock.advance(30 * time.Millisecond)
			v.touch(t, screen.TouchCancel, 100, 100)
		}, []want{{"device.tap", capability.StateCancelled, 30 * time.Millisecond}}, nil},
		{"the view ending mid-drag", func(t *testing.T, v *recView) {
			v.touch(t, screen.TouchDown, 100, 100)
			v.clock.advance(300 * time.Millisecond)
			v.touch(t, screen.TouchMove, 30000, 100)
			v.clock.advance(100 * time.Millisecond)
			v.h.Close()
		}, []want{{"device.swipe", capability.StateCancelled, 400 * time.Millisecond}}, nil},
		{"a key", func(t *testing.T, v *recView) { v.send(t, []byte{2, 4}) },
			[]want{{"device.pressKey", capability.StateCompleted, 0}}, func(t *testing.T, r capability.Recorded, in screen.Input) {
				if k := r.Params.(ScreenKeyParams).Key; k != "enter" {
					t.Errorf("key %q, want enter", k)
				}
			}},
		{"a wheel burst", func(t *testing.T, v *recView) {
			for i := 0; i < 5; i++ {
				v.send(t, scrollFrame(20000, 30000, -256, 256)) // a notch left, a notch up
				v.clock.advance(40 * time.Millisecond)
			}
		}, []want{{"device.scroll", capability.StateCompleted, 160 * time.Millisecond}}, func(t *testing.T, r capability.Recorded, in screen.Input) {
			p := r.Params.(ScreenScrollParams)
			if *p.DX != -5 || *p.DY != 5 || p.Device != "D1" {
				t.Errorf("a burst of five notches: dx %v dy %v, want -5 and 5", *p.DX, *p.DY)
			}
		}},
		{"a wheel burst past the bound is split", func(t *testing.T, v *recView) {
			for i := 0; i < 20; i++ {
				v.send(t, scrollFrame(20000, 30000, 0, 256))
				v.clock.advance(time.Millisecond)
			}
		}, []want{{"device.scroll", capability.StateCompleted, 15 * time.Millisecond}, {"device.scroll", capability.StateCompleted, 3 * time.Millisecond}}, nil},
		{"wheel events 250 ms apart are two", func(t *testing.T, v *recView) {
			v.send(t, scrollFrame(20000, 30000, 0, 256))
			v.clock.advance(250 * time.Millisecond)
			v.send(t, scrollFrame(20000, 30000, 0, 256))
		}, []want{{"device.scroll", capability.StateCompleted, 0}, {"device.scroll", capability.StateCompleted, 0}}, nil},
		{"wheel events at two points are two", func(t *testing.T, v *recView) {
			v.send(t, scrollFrame(5000, 30000, 0, 256))
			v.clock.advance(10 * time.Millisecond)
			v.send(t, scrollFrame(45000, 30000, 0, 256))
		}, []want{{"device.scroll", capability.StateCompleted, 0}, {"device.scroll", capability.StateCompleted, 0}}, nil},
		{"a failed write", func(t *testing.T, v *recView) {
			v.f.rec.err = errors.New("broken pipe")
			v.touch(t, screen.TouchDown, 100, 100)
			select {
			case <-v.f.src.Done():
			case <-time.After(testtime.Bound(10 * time.Second)):
				t.Fatal("a failed write did not end the source")
			}
		}, []want{{"device.tap", capability.StateFailed, 0}}, func(t *testing.T, r capability.Recorded, in screen.Input) {
			if r.Err == nil || r.Err.Code != ErrStreamFailed {
				t.Errorf("error %+v, want %s", r.Err, ErrStreamFailed)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := openRecorded(t)
			c.do(t, v)
			got := v.log.wait(t, len(c.want))
			time.Sleep(2 * scrollGap) // nothing more comes: no burst is left open, no record is doubled
			got = v.log.all()
			if len(got) != len(c.want) {
				t.Fatalf("%d records, want %d: %+v", len(got), len(c.want), got)
			}
			for i, w := range c.want {
				r := got[i]
				if r.Capability != w.capability || r.State != w.state || r.Finished.Sub(r.Started) != w.ms {
					t.Errorf("record %d: %s %s over %v, want %s %s over %v", i, r.Capability, r.State, r.Finished.Sub(r.Started), w.capability, w.state, w.ms)
				}
				if r.Origin.Client != capability.ClientUI {
					t.Errorf("record %d is under origin %+v, want the device.watchScreen call's (ui)", i, r.Origin)
				}
				in := validParams(t, r)
				if len(c.want) == 1 && c.check != nil {
					c.check(t, r, in)
				}
			}
		})
	}
}

// Decision (ADR-0117 §6, one loss only): a wheel turned one way and then the other, at one
// point and within 250 ms, is two device.scroll records of opposite signs, never one that
// sums to nothing and is not recorded: both messages reached the device, so both are in
// Activity. Seen failing with the burst closed only by the gap, the point and the bound:
// the two events summed to zero and no record was made.
func TestWheelReversalIsTwoRecords(t *testing.T) {
	v := openRecorded(t)
	v.send(t, scrollFrame(20000, 30000, 0, 256))
	v.clock.advance(10 * time.Millisecond)
	v.send(t, scrollFrame(20000, 30000, 0, -256))
	got := v.log.wait(t, 2)
	time.Sleep(2 * scrollGap)
	got = v.log.all()
	if len(got) != 2 {
		t.Fatalf("%d records for an up and a down of the wheel, want 2: %+v", len(got), got)
	}
	up, down := got[0].Params.(ScreenScrollParams), got[1].Params.(ScreenScrollParams)
	if *up.DY != 1 || *down.DY != -1 || *up.DX != 0 || *down.DX != 0 {
		t.Errorf("the pieces are dy %v and %v (dx %v, %v), want 1 and -1", *up.DY, *down.DY, *up.DX, *down.DX)
	}
	for _, r := range got {
		validParams(t, r)
	}
}

// realStore is the operation store with the four input capabilities registered, a state
// publisher and a trace writer that block until gate is closed, and counts of what they
// published and wrote.
type realStore struct {
	ops    *capability.Operations
	gate   chan struct{}
	mu     sync.Mutex
	lines  []string
	states map[capability.State]int
}

type statesTo struct{ s *realStore }

func (p statesTo) Broadcast(topic, typ, workspace, operation string, payload any) int {
	if p.s.gate != nil {
		<-p.s.gate
	}
	if st, ok := payload.(capability.OperationState); ok {
		p.s.mu.Lock()
		p.s.states[st.State]++
		p.s.mu.Unlock()
	}
	return 1
}

func newRealStore(t *testing.T, a *ADB, blocked bool) *realStore {
	t.Helper()
	reg := capability.NewRegistry()
	registerScreenInput(reg, a)
	s := &realStore{ops: capability.NewOperations(reg), states: map[capability.State]int{}}
	if blocked {
		s.gate = make(chan struct{})
	}
	s.ops.UseStatePublisher(statesTo{s})
	s.ops.UseTrace(func(format string, v ...any) {
		if s.gate != nil {
			<-s.gate
		}
		s.mu.Lock()
		s.lines = append(s.lines, fmt.Sprintf(format, v...))
		s.mu.Unlock()
	})
	a.SetRecorder(s.ops)
	return s
}

func (s *realStore) release() {
	if s.gate != nil {
		close(s.gate)
	}
}

func (s *realStore) snapshot() (lines []string, completed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...), s.states[capability.StateCompleted]
}

// Decision (ADR-0117 §6, "an input never waits on its record"): a trace writer and a state
// publisher that are stalled delay no input. 300 taps (600 events) reach the control socket
// within a second while neither will return, and once they do, every gesture is in the
// store, published and traced: none was dropped for the stall. Seen failing with the record
// flushed synchronously in Record: its goroutine waited on the publisher, the sink filled
// at 256 and the rest of the gestures were dropped, so the store held a fraction of the 300.
func TestRecordsNeverBlockInput(t *testing.T) {
	f := newLineFix(t)
	v := f.open(t)
	s := newRealStore(t, f.a, true)
	const taps = 300
	began := time.Now()
	for i := 0; i < taps; i++ {
		for _, fr := range [][]byte{frameOf(screen.TouchDown, 100, 100), frameOf(screen.TouchUp, 100, 100)} {
			for {
				err := v.h.Input(fr)
				if errors.Is(err, screen.ErrControlQueueFull) {
					time.Sleep(time.Millisecond)
					continue
				}
				if err != nil {
					t.Fatalf("Input: %v", err)
				}
				break
			}
		}
	}
	f.rec.waitN(t, 2*taps)
	if took := time.Since(began); took > testtime.Bound(time.Second) {
		t.Errorf("600 inputs took %v to reach the control socket with the store's flush stalled; want under a second", took)
	}
	s.release()
	testtime.Eventually(t, testtime.Bound(20*time.Second), "every gesture in the store, published and traced", func() bool {
		lines, completed := s.snapshot()
		return completed >= taps && len(lines) >= taps
	})
	if n := f.a.DroppedGestureRecords(); n != 0 {
		t.Errorf("%d gesture records dropped while only the flush was stalled", n)
	}
}

// Decision (ADR-0043 §2, ADR-0117 §6): a recorded gesture is traced as any operation is: the
// line names the client of the view's call and the capability that replays the gesture, its
// state and duration, and nothing of where the finger went. Seen failing with the origin not
// carried: the line said client=-.
func TestRecordedOperationIsTraced(t *testing.T) {
	f := newLineFix(t)
	v := f.open(t)
	s := newRealStore(t, f.a, false)
	for _, fr := range [][]byte{frameOf(screen.TouchDown, 1234, 4321), frameOf(screen.TouchMove, 50000, 4321), frameOf(screen.TouchUp, 50000, 4321)} {
		if err := v.h.Input(fr); err != nil {
			t.Fatal(err)
		}
	}
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the trace line", func() bool { lines, _ := s.snapshot(); return len(lines) >= 1 })
	lines, _ := s.snapshot()
	if len(lines) != 1 || !regexp.MustCompile(`^droidyard: op op-[0-9a-f]+ client=ui capability=device\.swipe workspace=- state=completed ms=\d+$`).MatchString(lines[0]) {
		t.Errorf("trace lines %q, want one for a completed device.swipe of client ui and no parameter", lines)
	}
}

// logSink is the daemon's log as a test sees it: a writer that can be held shut, and counts
// the lines written.
type logSink struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	gate chan struct{} // when set, a write waits until it is closed
}

func (l *logSink) Write(b []byte) (int, error) {
	if l.gate != nil {
		<-l.gate
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(b)
}

func (l *logSink) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// Decision (ADR-0117 §6): the sink loses a record in one case, 256 waiting for the store's
// lock, and counts it. With Record held shut, 256 records wait and ten more are dropped: the
// count is ten, nothing is logged while the drain waits in the store (the log is written by
// the drain, not the sender), and once the store opens the 257 accepted are all recorded and
// the log tells of the ten in one line. Seen failing with the count removed (zero).
func TestDroppedGestureRecordsAreCountedAndLoggedOnce(t *testing.T) {
	out := &logSink{}
	a := &ADB{}
	a.SetLog(out)
	log := &gestureLog{gate: make(chan struct{}), entered: make(chan struct{}, 4*gestureQueueCap)}
	a.SetRecorder(log)
	rec := capability.Recorded{Capability: "device.pressKey"}
	a.recordGesture(rec)
	<-log.entered // the drain goroutine holds one, inside Record
	for i := 0; i < gestureQueueCap+10; i++ {
		a.recordGesture(rec)
	}
	if n := a.DroppedGestureRecords(); n != 10 {
		t.Errorf("%d records counted as dropped, want 10", n)
	}
	if s := out.text(); s != "" {
		t.Errorf("the sender wrote to the log: %q", s)
	}
	close(log.gate)
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the accepted records recorded", func() bool { return len(log.all()) == gestureQueueCap+1 })
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the drain to tell the log", func() bool { return out.text() != "" })
	if s := out.text(); strings.Count(s, "\n") != 1 || !strings.Contains(s, "10 dropped") {
		t.Errorf("log %q, want one line telling of 10 drops", s)
	}
	// An idle sink holds no goroutine, and the next record starts the drain again.
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the drain to end", func() bool {
		a.gestures.mu.Lock()
		defer a.gestures.mu.Unlock()
		return !a.gestures.running
	})
	a.recordGesture(rec)
	testtime.Eventually(t, testtime.Bound(10*time.Second), "a record after the drain ended", func() bool { return len(log.all()) == gestureQueueCap+2 })
}

// Decision (ADR-0117 §6): the log hears of drops at most once a minute, the first at once, and
// what is dropped in between is told with the next line, not lost. Seen failing with the
// limit removed: the second report, ten seconds later, was not zero.
func TestDropReportsAreAtMostOncePerMinute(t *testing.T) {
	var g gestureSink
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	g.pending = 3
	if n := g.takeDropReport(t0); n != 3 {
		t.Errorf("first report %d, want 3 at once", n)
	}
	g.pending = 5
	if n := g.takeDropReport(t0.Add(10 * time.Second)); n != 0 {
		t.Errorf("a report 10 s later: %d, want none", n)
	}
	if n := g.takeDropReport(t0.Add(61 * time.Second)); n != 5 {
		t.Errorf("a report after a minute: %d, want the 5 held back", n)
	}
	if n := g.takeDropReport(t0.Add(130 * time.Second)); n != 0 {
		t.Errorf("a report with nothing dropped: %d", n)
	}
}

// Decision (ADR-0117 §6, "a stalled trace writer or publisher delays no input", and the log
// likewise): with the store shut and the daemon's log writer stalled, an input that overflows
// the sink returns at once. 300 taps go in within the bound while the log is held shut; once
// both are released the drops are in the log. Seen failing with the line written by the
// sender: the first dropped record's input waited on the log writer.
func TestStalledLogWriterDelaysNoInput(t *testing.T) {
	f := newLineFix(t)
	out := &logSink{gate: make(chan struct{})}
	f.a.SetLog(out)
	log := &gestureLog{gate: make(chan struct{})}
	f.a.SetRecorder(log)
	v := f.open(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 300; i++ {
			for _, fr := range [][]byte{frameOf(screen.TouchDown, 100, 100), frameOf(screen.TouchUp, 100, 100)} {
				for {
					err := v.h.Input(fr)
					if errors.Is(err, screen.ErrControlQueueFull) {
						time.Sleep(time.Millisecond)
						continue
					}
					if err != nil {
						t.Errorf("Input: %v", err)
						return
					}
					break
				}
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(testtime.Bound(10 * time.Second)):
		close(out.gate)
		close(log.gate)
		t.Fatal("an input waited on a stalled log writer")
	}
	if n := f.a.DroppedGestureRecords(); n == 0 {
		t.Error("no record was dropped: the test did not overflow the sink")
	}
	close(log.gate)
	close(out.gate)
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the drops in the log", func() bool { return strings.Contains(out.text(), "dropped") })
}

// Decision (plan m2y step 3; ADR-0117 §4): a scroll a call injects (device.scroll) takes no
// finger, like a key: while a view's finger is down it is written at once, as one scroll
// message, and does not wait for the lift. Seen failing with the scroll taking the finger
// like a tap: the Inject waited for the view's gesture until its context ended.
func TestInjectedScrollTakesNoFinger(t *testing.T) {
	f := newLineFix(t)
	v := f.open(t)
	if err := v.h.Input(frameOf(screen.TouchDown, 100, 100)); err != nil {
		t.Fatal(err)
	}
	f.rec.waitN(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), testtime.Bound(2*time.Second))
	defer cancel()
	if err := f.src.Inject(ctx, screen.NewHolder(false), screen.Scroll{X: 0.5, Y: 0.5, DY: 3}); err != nil {
		t.Fatalf("Inject(Scroll) while a view holds the finger: %v", err)
	}
	if got := f.rec.kinds(); len(got) != 2 || got[0] != "down" || got[1] != "scroll" {
		t.Errorf("the control socket got %v, want the view's down and one scroll", got)
	}
}

// Decision (plan m2y step 3): a daemon with no store to record in takes the input all the
// same, and records nothing, with no goroutine started.
func TestNoRecorderRecordsNothing(t *testing.T) {
	var a ADB
	a.recordGesture(capability.Recorded{})
	if a.gestures.running || a.DroppedGestureRecords() != 0 {
		t.Error("a sink without a recorder started or dropped")
	}
}
