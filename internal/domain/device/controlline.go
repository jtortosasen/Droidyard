package device

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
)

// The control line of a screen source (plan m2y step 2; ADR-0117 §3 to §5): everything that
// reaches the device's control socket goes through it, and it keeps who holds the device's
// one finger.
//
// A view's events (Control) are queued, 256 at most, and written by one goroutine in the
// order they came. A queued move is replaced by a newer move of the same holder and a queued
// scroll is summed with the next, so a stalled socket costs a burst of moves one message;
// a down, an up and a cancel are never dropped for it. An Inject (a call's gesture) does not
// queue: it takes the finger (acquire), writes its messages on their schedule and gives the
// finger back, so a drag from a view and a swipe from a call never interleave on the one
// pointer id.
//
// The finger. A holder has it from its down to its lift. A local holder (the daemon's own
// views and calls) takes it from a peer holder, with an ACTION_CANCEL for the peer's gesture
// written first; a peer never takes it from a local one (ADR-0094 §5); between two local
// holders the first keeps it. A touch of a holder that does not have the finger is dropped,
// a down that was refused included: the rest of that gesture is dropped with it, because its
// moves and its up find no finger. Every lift the line writes on its own is a cancel, never
// an up, and carries the frame size now (scrcpy drops a touch whose size is not the current
// video size, a lift included, so a lift for the old size would leave the app's finger
// down); the client's own up is the only one that writes ACTION_UP.
//
// The same line serves a client daemon's view of a remote device (remotescreen.go), whose
// messages are the wire's frames for the host, which owns the frame size and its own finger:
// encode and cancelOf are what differ.
//
// A key-frame request (requestKeyFrame, ADR-0118 §2) is the line's own message, not a view's:
// the Hub tells the source that a viewer lagged and the source asks the encoder for a key frame
// with scrcpy's reset-video. It queues like a key (it has no finger and takes none, and it is
// written in its place among the events), at most once every keyFrameRequestEvery, and a full
// queue drops it: the GOP still bounds the wait, and nothing may block the Hub.
//
// All writes and the finger's state are under one lock, wmu, so a message is whole and the
// state a write depends on cannot change under it. A write can wait on a stalled socket
// for screenWriteBound with wmu held; Control never takes wmu, so it never blocks on it.

// controlQueueCap is the most events a source's queue holds (ADR-0117 §3).
const controlQueueCap = 256

// controlPeerQueueCap is the most of those the peer holders' events may take together (plan
// m2y step 5): a paired peer that floods the queue is refused at half of it, so the host's own
// views always find room and only the peer's socket is closed.
const controlPeerQueueCap = controlQueueCap / 2

var (
	// errFingerLost ends an Inject whose finger was taken from it: by a local view's down
	// while the gesture was a peer's, or by a change of the frame's size.
	errFingerLost = errors.New("device: the finger was taken from the gesture")
)

// keyFrameRequestEvery is the least time between two reset-video messages of one source
// (ADR-0118 §2): the encoder answers within 0.1 s to 2 s, and a request before the answer
// would only restart the wait.
const keyFrameRequestEvery = 2 * time.Second

// lineItem is one entry of the queue: a view's event, a check of the frame size, a request for
// a key frame, or a marker the flush waits for.
type lineItem struct {
	h     screen.Holder
	ev    screen.Event
	size  bool          // a check of the frame size
	reset bool          // a request for a key frame
	flush chan struct{} // a marker, closed when the writer reaches it
}

// isEvent is whether the item is a view's event, the only kind a holder owns.
func (it lineItem) isEvent() bool { return it.flush == nil && !it.size && !it.reset }

// finger is who has the device's one finger and where it is.
type finger struct {
	held   bool
	holder screen.Holder
	inject bool          // an Inject's gesture, not a view's
	size   screen.Size   // the frame size its down was written for
	last   []byte        // the last touch message written for it
	taken  chan struct{} // an Inject's: closed when the finger is taken from it
}

type controlLine struct {
	size  func() screen.Size
	write func([]byte) (int, error) // one message to the control socket, under its deadline
	fail  func()                    // a write failed: the source cannot go on
	done  <-chan struct{}           // closed when the source ended
	why   func() error              // the source's end reason

	// What a message is made of: scrcpy's messages for the device's frame (the default), or the
	// wire's frames for a host (the relayed source). cancelOf is the cancel of the finger whose
	// last message was given.
	encode   func(screen.Event, screen.Size) ([]byte, error)
	cancelOf func([]byte, screen.Size) ([]byte, bool)

	qmu        sync.Mutex // guards queue and sizeQueued; never held across a write
	queue      []lineItem
	sizeQueued bool
	wake       chan struct{}
	stop       chan struct{}
	stopOnce   sync.Once
	running    atomic.Bool
	lastWrite  atomic.Int64 // when a message was last written, Unix nanoseconds; zero before the first

	now       func() time.Time // the clock of the key-frame limiter; a test replaces it
	resetMu   sync.Mutex       // guards lastReset
	lastReset time.Time        // when a key frame was last requested; zero before the first

	keyMu sync.Mutex // an Inject's key messages are one pair

	wmu   sync.Mutex // guards dead, f and freed, and serialises the writes
	dead  bool       // a write failed
	f     finger
	freed chan struct{} // closed and replaced when the finger is given back
}

func newControlLine(size func() screen.Size, write func([]byte) (int, error), fail func(), done <-chan struct{}, why func() error) *controlLine {
	return &controlLine{
		size: size, write: write, fail: fail, done: done, why: why,
		encode: screen.EncodeEvent, cancelOf: screen.CancelOf, now: time.Now,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), freed: make(chan struct{}),
	}
}

func (l *controlLine) signal() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// Control queues ev for the device as h and returns at once (screen.Source.Control).
func (l *controlLine) Control(h screen.Holder, ev screen.Event) error {
	select {
	case <-l.done:
		return l.why()
	default:
	}
	l.qmu.Lock()
	defer l.qmu.Unlock()
	if ev.Kind == screen.EventRelease {
		// The stream is gone: what it still has queued is dropped, and its release (at the
		// point it gave) lifts its finger if it has one. A release is never refused for a
		// full queue; there is one per stream.
		l.queue = slices.DeleteFunc(l.queue, func(it lineItem) bool { return it.h == h && it.isEvent() })
		l.queue = append(l.queue, lineItem{h: h, ev: ev})
		l.signal()
		return nil
	}
	for i := len(l.queue) - 1; i >= 0; i-- {
		it := &l.queue[i]
		if !it.isEvent() || it.h != h {
			continue
		}
		switch {
		case isMove(ev) && isMove(it.ev):
			it.ev = ev
			return nil
		case ev.Kind == screen.EventScroll && it.ev.Kind == screen.EventScroll:
			it.ev.X, it.ev.Y = ev.X, ev.Y
			it.ev.H, it.ev.V = screen.ClampScroll(it.ev.H, ev.H), screen.ClampScroll(it.ev.V, ev.V)
			return nil
		}
		break // the holder's newest queued event is not one this one replaces
	}
	if len(l.queue) >= controlQueueCap || (h.Peer() && l.peerQueuedLocked() >= controlPeerQueueCap) {
		return screen.ErrControlQueueFull
	}
	l.queue = append(l.queue, lineItem{h: h, ev: ev})
	l.signal()
	return nil
}

// peerQueuedLocked is how many events of peer holders the queue holds. qmu is held.
func (l *controlLine) peerQueuedLocked() int {
	n := 0
	for _, it := range l.queue {
		if it.isEvent() && it.h.Peer() {
			n++
		}
	}
	return n
}

func isMove(ev screen.Event) bool {
	return ev.Kind == screen.EventTouch && ev.Action == screen.TouchMove
}

// sizeChanged tells the writer the stream announced a frame size, so a finger held for the
// old one is lifted with a cancel at once, not at the next event. At most one check waits.
func (l *controlLine) sizeChanged() {
	l.qmu.Lock()
	if !l.sizeQueued {
		l.sizeQueued = true
		l.queue = append(l.queue, lineItem{size: true})
	}
	l.qmu.Unlock()
	l.signal()
}

// requestKeyFrame asks the device's encoder for a key frame, as a lagging viewer needs one to
// rejoin (ADR-0118 §2): one reset-video message on the line, at most one every
// keyFrameRequestEvery, dropped when the queue is full. It never blocks, so the Hub's owner may
// call it from the goroutine that found the lag.
func (l *controlLine) requestKeyFrame() {
	select {
	case <-l.done:
		return
	default:
	}
	l.resetMu.Lock()
	defer l.resetMu.Unlock()
	now := l.now()
	if !l.lastReset.IsZero() && now.Sub(l.lastReset) < keyFrameRequestEvery {
		return
	}
	l.qmu.Lock()
	if len(l.queue) >= controlQueueCap {
		l.qmu.Unlock()
		return // dropped: a request that did not queue does not start the interval
	}
	l.queue = append(l.queue, lineItem{reset: true})
	l.qmu.Unlock()
	l.lastReset = now
	l.signal()
}

func (l *controlLine) pop() (lineItem, bool) {
	l.qmu.Lock()
	defer l.qmu.Unlock()
	if len(l.queue) == 0 {
		return lineItem{}, false
	}
	it := l.queue[0]
	n := copy(l.queue, l.queue[1:])
	l.queue[n] = lineItem{}
	l.queue = l.queue[:n]
	if it.size {
		l.sizeQueued = false
	}
	return it, true
}

// run is the writer: it takes the queue in order until shutdown.
func (l *controlLine) run() {
	for {
		select {
		case <-l.stop:
			return
		default:
		}
		it, ok := l.pop()
		if !ok {
			select {
			case <-l.wake:
			case <-l.stop:
				return
			}
			continue
		}
		l.handle(it)
	}
}

// shutdown stops the writer; what is still queued is not written.
func (l *controlLine) shutdown() { l.stopOnce.Do(func() { close(l.stop) }) }

// flush waits, for at most d, until the writer has reached everything queued before the
// call: the end of a source's last viewer, whose streams queued the cancels of their
// fingers, waits for them to be written before the socket closes.
func (l *controlLine) flush(d time.Duration) {
	if !l.running.Load() {
		return
	}
	ch := make(chan struct{})
	l.qmu.Lock()
	l.queue = append(l.queue, lineItem{flush: ch})
	l.qmu.Unlock()
	l.signal()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	}
}

// wroteWithin says whether a message was written to the control socket less than d ago. The
// device's server reads the socket in order but not at once, so a source that ends soon after
// a write (a cancel above all) must let the server read it before the child is killed.
func (l *controlLine) wroteWithin(d time.Duration) bool {
	at := l.lastWrite.Load()
	return at != 0 && time.Since(time.Unix(0, at)) < d
}

// stopsWriting is whether the source ended in a way that leaves the socket unusable: a clean
// end (the last viewer's) still writes what is queued, so the cancels get out.
func (l *controlLine) stopsWriting() bool {
	r := l.why()
	return r != nil && !errors.Is(r, screen.ErrClosed)
}

func (l *controlLine) handle(it lineItem) {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	if it.flush != nil {
		close(it.flush)
		return
	}
	if l.dead || l.stopsWriting() {
		return
	}
	if it.reset {
		// No finger and no frame size: the encoder is asked and nothing else changes.
		l.writeLocked(screen.EncodeResetVideo())
		return
	}
	size := l.size()
	if size == (screen.Size{}) {
		return
	}
	// A finger held for another frame size is lifted before anything else is written.
	if l.f.held && l.f.size != size {
		l.liftLocked(size)
	}
	if it.size {
		return
	}
	switch it.ev.Kind {
	case screen.EventTouch:
		l.touchLocked(it.h, it.ev, size)
	case screen.EventKey, screen.EventScroll:
		// A peer's key or wheel never reaches the device while the host's own user has the
		// finger (ADR-0094 §5), as a peer's touch never takes it.
		if it.h.Peer() && l.f.held && !l.f.holder.Peer() {
			return
		}
		if b, err := l.encode(it.ev, size); err == nil {
			l.writeLocked(b)
		}
	case screen.EventRelease:
		l.releaseLocked(it.h, it.ev, size)
	}
}

// touchLocked applies a view's touch to the finger.
func (l *controlLine) touchLocked(h screen.Holder, ev screen.Event, size screen.Size) {
	f := &l.f
	if ev.Action == screen.TouchDown {
		if f.held {
			// Its own second down, a peer's down on anyone's finger, a local down on a local
			// holder's finger: the first holder keeps it. Only a local down on a peer's
			// finger takes it, with a cancel for the peer's gesture.
			if f.holder == h || h.Peer() || !f.holder.Peer() || !l.liftLocked(size) {
				return
			}
		}
		b, err := l.encode(ev, size)
		if err != nil || !l.writeLocked(b) {
			return
		}
		l.f = finger{held: true, holder: h, size: size, last: b}
		return
	}
	// A move, an up or a cancel without the finger is the rest of a gesture that was dropped.
	if !f.held || f.holder != h || f.inject {
		return
	}
	b, err := l.encode(ev, size)
	if err != nil || !l.writeLocked(b) {
		return
	}
	f.last = b
	if ev.Action != screen.TouchMove {
		l.freeLocked()
	}
}

// releaseLocked lifts the finger of a stream that ended, with a cancel at the point it gave.
func (l *controlLine) releaseLocked(h screen.Holder, ev screen.Event, size screen.Size) {
	if !l.f.held || l.f.holder != h || l.f.inject {
		return
	}
	b, err := l.encode(screen.Event{Kind: screen.EventTouch, Action: screen.TouchCancel, X: ev.X, Y: ev.Y}, size)
	if err == nil {
		l.writeLocked(b)
	}
	l.freeLocked()
}

// liftLocked writes the cancel of the finger's gesture at its last point, for the frame size
// now, and gives the finger back. False when the write failed.
func (l *controlLine) liftLocked(size screen.Size) bool {
	if b, ok := l.cancelOf(l.f.last, size); ok {
		if !l.writeLocked(b) {
			return false
		}
	}
	l.freeLocked()
	return true
}

// freeLocked gives the finger back and wakes whoever waits for it.
func (l *controlLine) freeLocked() {
	if l.f.taken != nil {
		close(l.f.taken)
	}
	l.f = finger{}
	close(l.freed)
	l.freed = make(chan struct{})
}

// writeLocked writes one message of a view's and says whether it was written. A failure ends
// the source (a write cut midway leaves half a message on the socket, ADR-0079 §3), unless it
// already ended.
func (l *controlLine) writeLocked(b []byte) bool {
	if _, err := l.rawWriteLocked(b); err != nil {
		select {
		case <-l.done:
		default:
			l.fail()
		}
		return false
	}
	return true
}

// rawWriteLocked writes one message and remembers that the line failed. An Inject ends the
// source itself, after it has told its caller what the write said (screensource.go).
func (l *controlLine) rawWriteLocked(b []byte) (int, error) {
	n, err := l.write(b)
	if err != nil {
		l.dead = true
	} else {
		l.lastWrite.Store(time.Now().UnixNano())
	}
	return n, err
}

// acquire takes the finger for an Inject's gesture, waiting for a holder it does not take
// it from (a view's gesture, or a call's), until ctx ends or the source does. It returns the
// frame size the finger holds, which the gesture is encoded for, and a channel that closes if
// the finger is taken from it.
func (l *controlLine) acquire(ctx context.Context, h screen.Holder) (screen.Size, <-chan struct{}, error) {
	for {
		l.wmu.Lock()
		if l.dead {
			l.wmu.Unlock()
			return screen.Size{}, nil, l.endErr()
		}
		size := l.size()
		if !l.f.held || (!h.Peer() && l.f.holder.Peer() && l.liftLocked(size)) {
			taken := make(chan struct{})
			l.f = finger{held: true, holder: h, inject: true, size: size, taken: taken}
			l.wmu.Unlock()
			return size, taken, nil
		}
		wait := l.freed
		l.wmu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return screen.Size{}, nil, ctx.Err()
		case <-l.done:
			return screen.Size{}, nil, l.why()
		}
	}
}

// endErr is why the line cannot be used: the source's end reason, or the failed write's.
func (l *controlLine) endErr() error {
	if r := l.why(); r != nil {
		return r
	}
	return errors.New("device: the control socket could not be written")
}

// injectWrite writes one message of an Inject's gesture while h still has the finger.
func (l *controlLine) injectWrite(h screen.Holder, b []byte) (int, error) {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	if !l.f.held || l.f.holder != h {
		return 0, errFingerLost
	}
	n, err := l.rawWriteLocked(b)
	if err == nil && len(b) == 32 {
		l.f.last = b
	}
	return n, err
}

// injectKeyWrite writes one message of an Inject's key press. A key has no finger.
func (l *controlLine) injectKeyWrite(b []byte) (int, error) {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	if l.dead {
		return 0, l.endErr()
	}
	return l.rawWriteLocked(b)
}

// release gives the finger back when an Inject's gesture is over (or was cut).
func (l *controlLine) release(h screen.Holder) {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	if l.f.held && l.f.holder == h {
		l.freeLocked()
	}
}
