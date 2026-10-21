package device

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
)

// The records of a view's gestures (plan m2y step 3; ADR-0117 §6). A view's input travels its
// stream socket and not a call, so nothing in the core sees it; each stream turns the events
// it takes into gestures, and each gesture is one operation of the capability that replays
// it (device.tap, swipe, pressKey or scroll), recorded after the fact with the view's origin
// and target, so Activity, the trace and operation.state show it as they show a call.
//
// A record is never on the input's path. The stream hands it to the ADB's sink by a send
// that cannot block, and one goroutine takes the sink's channel to the store (Record), which
// holds the store's lock for an insert and leaves the publishing and the trace line to a
// flush of its own. The sink loses a record in one case: 256 are already waiting for the
// store's lock. It counts the loss and the drain goroutine logs it at most once a minute
// (ADR-0117 §6).
//
// A record is the view's gesture as the stream sent it, made when the source accepted the
// event: it is not proof of delivery. A down the finger rule refused still gives a completed
// tap or swipe, and a key or a wheel whose write fails afterwards stays completed. Completed
// is an up (or a key, or a wheel burst) the view sent; cancelled is its own cancel or the
// view ending with a finger down; failed is only the stream ending failed while the gesture
// was open (DEVICE_STREAM_FAILED).

// Recorder is the operation store as the sink needs it: *capability.Operations.
type Recorder interface {
	Record(capability.Recorded) (string, error)
}

// The sink's and the classifier's bounds.
const (
	gestureQueueCap = 256
	dropLogEvery    = time.Minute
	// wireUnit is the wire's full scale: 0..65535 is the frame, 0..1.
	wireUnit = 65535.0
	// tapTravel is 1 % of the frame in wire units: a gesture that stays within it of its
	// down, on each axis, and ends within maxTapHoldMs is a tap.
	tapTravel = 655
	// scrollGap is how far apart two wheel events at one point may be and still be one
	// device.scroll.
	scrollGap = 250 * time.Millisecond
	// maxGesturePoints bounds what one drag keeps while it runs: past it every other point is
	// dropped, so a drag that never lifts costs a bounded amount.
	maxGesturePoints = 4096
	// scrollBound is a scroll's per-axis bound in wire units (maxScrollNotches notches).
	scrollBound = maxScrollNotches * screen.ScrollUnit
)

// gestureSink carries records from the streams to the store without blocking them.
type gestureSink struct {
	mu      sync.Mutex
	ch      chan capability.Recorded // made on first use
	running bool                     // a drain goroutine is taking ch

	dropped atomic.Int64 // records lost since the daemon started
	pending int64        // lost and not yet logged; guarded by mu
	lastLog time.Time    // guarded by mu
}

// SetRecorder gives the ADB the store the live view's gestures are recorded in. It is called
// once, at wiring, before any call. Without one the gestures are not recorded.
func (a *ADB) SetRecorder(r Recorder) { a.recorder = r }

// DroppedGestureRecords is how many gesture records were lost to a full sink since the
// daemon started. Test-only in practice.
func (a *ADB) DroppedGestureRecords() int64 { return a.gestures.dropped.Load() }

// recordGesture queues r for the store and returns at once. It starts the drain goroutine
// when none runs; when the queue is full it drops r and counts it. It writes nothing to the
// log: a stalled log writer must not delay an input (the drain tells of drops, reportDrops).
func (a *ADB) recordGesture(r capability.Recorded) {
	if a.recorder == nil {
		return
	}
	g := &a.gestures
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ch == nil {
		g.ch = make(chan capability.Recorded, gestureQueueCap)
	}
	select {
	case g.ch <- r:
		if !g.running {
			g.running = true
			go a.drainGestures()
		}
	default:
		g.dropped.Add(1)
		g.pending++
	}
}

// takeDropReport is how many drops the log has not been told of, when it may be told: at most
// once every dropLogEvery, the first drop at once. It resets the count it returns.
func (g *gestureSink) takeDropReport(now time.Time) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending == 0 || now.Sub(g.lastLog) < dropLogEvery {
		return 0
	}
	n := g.pending
	g.lastLog, g.pending = now, 0
	return n
}

// reportDrops writes the line for the drops not yet told of, if one is due. Only the drain
// goroutine calls it, so the log writer is never on an input's path.
func (a *ADB) reportDrops() {
	if n := a.gestures.takeDropReport(time.Now()); n > 0 {
		a.logf("the sink of the live view's gesture records was full, %d dropped (the store's lock was held for long)", n)
	}
}

// drainGestures takes the sink's channel to the store, and ends when the channel is empty so
// an idle daemon holds no goroutine. The check of the channel and the clearing of running
// are one step under mu, as the send and the start of a drain are, so a record sent after
// the drain looked finds running false and starts the next. After each record, and before it
// ends, it tells the log of drops, which can only be once the store lets it go on.
func (a *ADB) drainGestures() {
	g := &a.gestures
	for {
		select {
		case r := <-g.ch:
			if _, err := a.recorder.Record(r); err != nil {
				a.logf("a gesture of the live view was not recorded: %v", err)
			}
			a.reportDrops()
		default:
			a.reportDrops()
			g.mu.Lock()
			if len(g.ch) == 0 {
				g.running = false
				g.mu.Unlock()
				return
			}
			g.mu.Unlock()
		}
	}
}

// gesturePoint is one place on a finger's path, in the wire's units.
type gesturePoint struct {
	x, y uint16
	at   time.Time
}

// scrollBurst is the wheel events at one point that are one device.scroll so far.
type scrollBurst struct {
	x, y       uint16
	h, v       int32 // the sums, in wire units
	start, end time.Time
	seq        uint64 // which burst the timer that flushes it belongs to
}

// gestures is a stream's state between its events and its records, guarded by the stream's
// mu.
type gestures struct {
	down  bool
	pts   []gesturePoint
	burst *scrollBurst
	seq   uint64
	timer *time.Timer
}

// touchGesture applies a touch event of the view and returns the record it completes, if any.
func (s *screenStream) touchGesture(ev screen.Event, now time.Time) []capability.Recorded {
	g := &s.g
	p := gesturePoint{ev.X, ev.Y, now}
	switch ev.Action {
	case screen.TouchDown:
		if g.down {
			return nil // a second down of a finger already down: the source keeps the first gesture
		}
		g.down, g.pts = true, append(g.pts[:0], p)
	case screen.TouchMove:
		if !g.down {
			return nil
		}
		if len(g.pts) >= maxGesturePoints {
			kept := g.pts[:1]
			for i := 2; i < len(g.pts); i += 2 {
				kept = append(kept, g.pts[i])
			}
			g.pts = kept
		}
		g.pts = append(g.pts, p)
	default: // up or cancel
		if !g.down {
			return nil
		}
		g.pts = append(g.pts, p)
		state := capability.StateCompleted
		if ev.Action == screen.TouchCancel {
			state = capability.StateCancelled
		}
		return []capability.Recorded{s.endGesture(state)}
	}
	return nil
}

// endGesture turns the finger's path into its record and clears it.
func (s *screenStream) endGesture(state capability.State) capability.Recorded {
	g := &s.g
	r := fingerRecord(s.device, s.origin, g.pts, state)
	g.down, g.pts = false, g.pts[:0]
	return r
}

// keyGesture is the record of a key.
func (s *screenStream) keyGesture(ev screen.Event, now time.Time) capability.Recorded {
	return capability.Recorded{
		Capability: "device.pressKey", Origin: s.origin, Started: now, Finished: now, State: capability.StateCompleted,
		Params: ScreenKeyParams{Device: s.device, Key: ev.Key}, Result: ScreenInputResult{Delivered: true},
	}
}

// scrollGesture adds a wheel event to the burst it belongs to, and returns the record of the
// burst it closes, if any. Two events are one burst when they are less than scrollGap apart,
// at one point (within tapTravel of the burst's first), turn no axis the other way, and their
// sums stay within the bound; the timer closes a burst nothing followed.
func (s *screenStream) scrollGesture(ev screen.Event, now time.Time) []capability.Recorded {
	g := &s.g
	h, v := int32(screen.ClampScroll(0, ev.H)), int32(screen.ClampScroll(0, ev.V))
	var out []capability.Recorded
	if b := g.burst; b != nil {
		if now.Sub(b.end) < scrollGap && near(b.x, ev.X) && near(b.y, ev.Y) &&
			!reverses(b.h, h) && !reverses(b.v, v) &&
			abs32(b.h+h) <= scrollBound && abs32(b.v+v) <= scrollBound {
			b.h, b.v, b.end = b.h+h, b.v+v, now
			s.armScroll(b)
			return nil
		}
		if r, ok := s.endBurst(); ok {
			out = append(out, r)
		}
	}
	g.seq++
	g.burst = &scrollBurst{x: ev.X, y: ev.Y, h: h, v: v, start: now, end: now, seq: g.seq}
	s.armScroll(g.burst)
	return out
}

// armScroll schedules the flush of a burst scrollGap after its last event.
func (s *screenStream) armScroll(b *scrollBurst) {
	g := &s.g
	seq := b.seq
	if g.timer != nil {
		g.timer.Stop()
	}
	g.timer = time.AfterFunc(scrollGap, func() {
		s.mu.Lock()
		if g.burst == nil || g.burst.seq != seq {
			s.mu.Unlock()
			return
		}
		r, ok := s.endBurst()
		s.mu.Unlock()
		if ok {
			s.record(r)
		}
	})
}

// endBurst closes the burst. A burst whose events were all zero scrolled nothing, which no
// device.scroll can say (not both dx and dy zero), so it has no record; no other burst sums
// to zero, since scrollGesture closes a burst where an axis would reverse.
func (s *screenStream) endBurst() (capability.Recorded, bool) {
	g := &s.g
	b := g.burst
	g.burst = nil
	if g.timer != nil {
		g.timer.Stop()
	}
	if b == nil || (b.h == 0 && b.v == 0) {
		return capability.Recorded{}, false
	}
	x, y := float64(b.x)/wireUnit, float64(b.y)/wireUnit
	dx, dy := float64(b.h)/screen.ScrollUnit, float64(b.v)/screen.ScrollUnit
	return capability.Recorded{
		Capability: "device.scroll", Origin: s.origin, Started: b.start, Finished: b.end, State: capability.StateCompleted,
		Params: ScreenScrollParams{Device: s.device, X: &x, Y: &y, DX: &dx, DY: &dy}, Result: ScreenInputResult{Delivered: true},
	}, true
}

// finishGestures closes what the stream still has open when it ends: the burst of wheel
// events, completed, and a finger down, cancelled, or failed when the source failed under
// it. The stream's mu is held.
func (s *screenStream) finishGestures(now time.Time, failed bool) []capability.Recorded {
	var out []capability.Recorded
	if r, ok := s.endBurst(); ok {
		out = append(out, r)
	}
	if s.g.down {
		s.g.pts = append(s.g.pts, gesturePoint{s.lastX, s.lastY, now})
		if failed {
			out = append(out, s.endGesture(capability.StateFailed))
		} else {
			out = append(out, s.endGesture(capability.StateCancelled))
		}
	}
	return out
}

// reverses is whether adding amount to an axis's running sum turns the wheel the other way.
// A burst is closed there, so each piece has a net of one sign and none can sum to nothing:
// every wheel event the view sent is in a record.
func reverses(sum, amount int32) bool { return sum != 0 && amount != 0 && (sum > 0) != (amount > 0) }

func near(a, b uint16) bool { return abs32(int32(a)-int32(b)) <= tapTravel }

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// fingerRecord is the record of a finger's path as the view sent it, from its down to its
// lift (not proof that the device got it, see the top of this file): a device.tap
// when it stayed within tapTravel of the down on each axis and lasted at most maxTapHoldMs,
// a device.swipe otherwise. state is completed (an up), cancelled (a cancel, or the view's
// end) or failed (the source failed under it). The operation's times are the gesture's own;
// a swipe's points are resampled by time to at most maxSwipePoints and a gesture longer than
// maxSwipeSpanMs has its times scaled into it, so the params are always ones the capability
// takes.
func fingerRecord(device string, origin capability.Origin, pts []gesturePoint, state capability.State) capability.Recorded {
	first, last := pts[0], pts[len(pts)-1]
	r := capability.Recorded{Origin: origin, Started: first.at, Finished: last.at, State: state}
	switch state {
	case capability.StateCompleted:
		r.Result = ScreenInputResult{Delivered: true}
	case capability.StateFailed:
		r.Err = capability.NewError(ErrStreamFailed, "the live view failed while the gesture was being sent")
	}
	held := last.at.Sub(first.at)
	if held < 0 {
		held = 0
	}
	still := true
	for _, p := range pts {
		still = still && near(first.x, p.x) && near(first.y, p.y)
	}
	if still && held <= maxTapHoldMs*time.Millisecond {
		x, y, ms := float64(first.x)/wireUnit, float64(first.y)/wireUnit, int(held.Milliseconds())
		r.Capability, r.Params = "device.tap", ScreenTapParams{Device: device, X: &x, Y: &y, DurationMs: &ms}
		return r
	}
	r.Capability = "device.swipe"
	path := swipePath(pts)
	r.Params = ScreenSwipeParams{Device: device, Path: path}
	return r
}

// swipePath is the points of a drag as a device.swipe's path: at most maxSwipePoints, taken
// by time (the last point at or before each of that many evenly spaced instants, the first
// and last always kept), their times in milliseconds from the first point, scaled into
// maxSwipeSpanMs when the drag was longer, and strictly increasing.
func swipePath(pts []gesturePoint) []SwipePoint {
	first, last := pts[0].at, pts[len(pts)-1].at
	span := last.Sub(first)
	idx := []int{0}
	if len(pts) <= maxSwipePoints {
		for i := 1; i < len(pts); i++ {
			idx = append(idx, i)
		}
	} else {
		j := 0
		for k := 1; k < maxSwipePoints; k++ {
			at := first.Add(span * time.Duration(k) / (maxSwipePoints - 1))
			for j+1 < len(pts) && !pts[j+1].at.After(at) {
				j++
			}
			if k == maxSwipePoints-1 {
				j = len(pts) - 1
			}
			if j != idx[len(idx)-1] {
				idx = append(idx, j)
			}
		}
	}
	if len(idx) < minSwipePoints {
		idx = append(idx, len(pts)-1)
	}
	ts := make([]int, len(idx))
	for i, k := range idx {
		t := pts[k].at.Sub(first)
		if span > maxSwipeSpanMs*time.Millisecond {
			t = time.Duration(float64(t) * float64(maxSwipeSpanMs*time.Millisecond) / float64(span))
		}
		ts[i] = int(math.Round(float64(t) / float64(time.Millisecond)))
	}
	ts[0] = 0
	for i := 1; i < len(ts); i++ {
		ts[i] = max(ts[i], ts[i-1]+1)
	}
	for i := len(ts) - 1; i >= 0 && ts[i] > maxSwipeSpanMs-(len(ts)-1-i); i-- {
		ts[i] = maxSwipeSpanMs - (len(ts) - 1 - i)
	}
	path := make([]SwipePoint, len(idx))
	for i, k := range idx {
		x, y, t := float64(pts[k].x)/wireUnit, float64(pts[k].y)/wireUnit, ts[i]
		path[i] = SwipePoint{X: &x, Y: &y, T: &t}
	}
	return path
}
