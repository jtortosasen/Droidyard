package device

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/hosting"
)

// The live view of a remote device (plan m2t step 3; ADR-0100 §3, §5): a screen.Source whose
// packets are the host's stream, relayed. device.watchScreen on the host issues a stream
// and a subscription; this source attaches the stream's video socket on the host's peer
// port, feeds what it reads to a Hub of this daemon's own (so the client's viewers get
// ADR-0078 §5's join and rejoin as for a local device), and reads the host's `state` and
// `stopped` events for the seam. A remote device's input has two ways in. A call (device.tap,
// swipe, pressKey, scroll) is relayed like any plain call and finds the host's running source
// (ADR-0100 §5). A view's own input (ADR-0117) travels this source's video socket as the
// wire's frames, when the host's result granted `control`: the source's control line queues
// them (the one writer, 256 entries, moves coalesced), keeps the client's own finger between
// its views, and writes the cancel of a finger down when a view ends. The host injects them
// for the stream this daemon opened, its lease being the authorization.
//
// There is one source per remote device, shared by every viewer of this daemon and
// reference-counted like screenSources. Its life is one goroutine, run, shaped like the
// scrcpy source's: start, wait to be ended, tear down, leave the set. It ends with its last
// viewer (the host's stream is released and its subscription ended), with the host's
// `stopped` (the same reason), with the link (deviceGone), on a video message that is not a
// packet (failed), and with the daemon.

// The relayed source's bounds; variables so a test is quick.
var (
	// relayScreenStart bounds device.watchScreen on the host, whose own start is bounded by
	// its push, forward, connects and first packet.
	relayScreenStart = 60 * time.Second
	// relayScreenGrace is how long the host's `stopped` event or the link's loss gets to say
	// why the video socket ended before the source says it failed.
	relayScreenGrace = 2 * time.Second
	// relayScreenCleanup bounds the host call that ends the subscription.
	relayScreenCleanup = 5 * time.Second
)

// maxRelayedID bounds the stream and subscription ids a host names: its own are 22 bytes.
const maxRelayedID = 128

// errRelayedInput is what Inject answers (a remote device's gesture is a capability call on the
// host), and what Control answers when the host did not grant input (an older host's result
// lacks `control`).
var errRelayedInput = errors.New("device: a remote device's input is a call on its host")

// watchScreenErrors are the codes device.watchScreen declares; a host's other code is not
// one this daemon can read (ADR-0100 §4).
var watchScreenErrors = append(append([]capability.ErrorCode{}, LentErrors...),
	ErrStreamFailed, capability.ErrInvalidParams, capability.ErrSessionRequired)

// relayedScreens is every relayed source of the daemon, at most one per remote device id.
type relayedScreens struct {
	mu       sync.Mutex // guards stopping, by, all and each source's refs and ending
	stopping bool
	by       map[string]*relayedSource
	all      map[*relayedSource]struct{}
}

// relayedSource is one remote device's live-view source. It implements screen.Source.
type relayedSource struct {
	set *relayedScreens
	adb *ADB
	rt  *remoteTarget

	refs   int  // holders of the source, an acquire still starting included; set.mu
	ending bool // end was called; set.mu

	ctx    context.Context // the source's own: the callers' contexts never reach the host
	cancel context.CancelFunc
	hub    *screen.Hub
	secure secureState // the host's FLAG_SECURE state, from its events

	ready     chan struct{} // closed when the start finished, failed or not
	startFail error         // set before ready closes
	done      chan struct{} // closed when the source left the set and the host was released

	// Written by run's goroutine before ready closes or the goroutines below start.
	down      <-chan struct{} // closed when the link connection the stream lives on is lost
	marked    <-chan struct{} // closed when a mark frame was written to the host: the view is renewed (ADR-0123 §2)
	sub       string          // the host's subscription, "" until the host named one
	stopRoute func()          // ends the link's routing of that subscription's events
	conn      *hosting.ScreenConn
	size      screen.Size // the size the host announced at start
	control   bool        // the host's grant: its result said control true
	wg        sync.WaitGroup

	// line is the one writer of the view's input to conn, with the queue and the client's
	// finger (controlline.go). It runs only when the host granted control.
	line *controlLine
}

var _ screen.Source = (*relayedSource)(nil)

// acquire returns the remote device's source, starting it when there is none, and the
// release the caller must call exactly when it is done (it is idempotent). It is
// screenSources.acquire for a remote id: later callers wait on the start in progress, a
// failed start fails every waiter, a caller whose ctx ends leaves, and a new start waits for
// an ending source's teardown.
func (rs *relayedScreens) acquire(ctx context.Context, a *ADB, rt *remoteTarget) (*relayedSource, func(), error) {
	s, ref, err := rs.acquireRef(ctx, a, rt)
	if err != nil {
		return nil, nil, err
	}
	return s, ref.Release, nil
}

// acquireRef is acquire whose reference can be dropped in two parts (sourceRef.Drop), as a
// live view's end needs (ADR-0123 §3).
func (rs *relayedScreens) acquireRef(ctx context.Context, a *ADB, rt *remoteTarget) (*relayedSource, *sourceRef, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		rs.mu.Lock()
		if rs.stopping {
			rs.mu.Unlock()
			return nil, nil, streamFailed("the daemon is stopping")
		}
		s := rs.by[rt.id]
		if s != nil && s.ending {
			rs.mu.Unlock()
			select {
			case <-s.done:
				continue
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		if s == nil {
			s = a.newRelayedSource(rs, rt)
			if rs.by == nil {
				rs.by, rs.all = map[string]*relayedSource{}, map[*relayedSource]struct{}{}
			}
			rs.by[rt.id], rs.all[s] = s, struct{}{}
			go s.run()
		}
		s.refs++
		rs.mu.Unlock()

		select {
		case <-s.ready:
		case <-ctx.Done():
			s.unref()
			return nil, nil, ctx.Err()
		}
		if s.startFail != nil {
			return nil, nil, s.startFail
		}
		return s, &sourceRef{drop: s.dropRef}, nil
	}
}

// streamFailed is DEVICE_STREAM_FAILED with the daemon's own cause.
func streamFailed(cause string) *capability.Error {
	return capability.NewError(ErrStreamFailed, "the device screen could not be streamed").
		WithCause(cause).
		WithNextStep("Retry. If it keeps failing, check that the device is online and unlocked; device.list says whether it is.")
}

// StopAll ends every source and waits until each has left the set, which is when its video
// socket is closed and the host asked to end its subscription. New acquires are refused from
// here on. It returns early only when ctx ends.
func (rs *relayedScreens) StopAll(ctx context.Context) {
	rs.mu.Lock()
	rs.stopping = true
	list := make([]*relayedSource, 0, len(rs.all))
	for s := range rs.all {
		list = append(list, s)
	}
	rs.mu.Unlock()
	for _, s := range list {
		s.end(&ScreenEnd{Reason: StopExited, Cause: "the daemon is stopping"})
	}
	for _, s := range list {
		select {
		case <-s.done:
		case <-ctx.Done():
			return
		}
	}
}

// Live is how many sources are in the set. Test-only in practice.
func (rs *relayedScreens) Live() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return len(rs.all)
}

func (a *ADB) newRelayedSource(rs *relayedScreens, rt *remoteTarget) *relayedSource {
	ctx, cancel := context.WithCancel(context.Background())
	s := &relayedSource{
		set: rs, adb: a, rt: rt,
		ctx: ctx, cancel: cancel, hub: screen.NewHubWithoutAgeBound(), // the host's Hub and the peer window bound the age (ADR-0121)
		ready: make(chan struct{}), done: make(chan struct{}),
	}
	// The line writes the wire for the host, which owns the device's frame size and finger:
	// the size here is a constant, so the line never lifts a finger for a size change (the host
	// does), and a write that failed is the socket's end, which read hears and reports (a
	// close code it knows, or failed after relayScreenGrace), so it does not end the source
	// itself.
	s.line = newControlLine(func() screen.Size { return screen.Size{Width: 1, Height: 1} }, s.sendInput, func() {}, s.hub.Done(), s.hub.Reason)
	s.line.encode = func(ev screen.Event, _ screen.Size) ([]byte, error) { return screen.EncodeWire(ev) }
	s.line.cancelOf = wireCancelOf
	return s
}

// wireCancelOf is the cancel frame for the finger whose last frame of the wire was last: the
// same point.
func wireCancelOf(last []byte, _ screen.Size) ([]byte, bool) {
	if len(last) != 6 || last[0] != 1 {
		return nil, false
	}
	return []byte{1, byte(screen.TouchCancel), last[2], last[3], last[4], last[5]}, true
}

// sendInput writes one frame of the view's input to the host, bounded like a write to the
// device's control socket.
func (s *relayedSource) sendInput(b []byte) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), screenWriteBound)
	defer cancel()
	if err := s.conn.Send(ctx, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

// unref drops one holder. The last one ends the source and waits until it is gone, so a
// caller that released the last reference knows the host was released when it returns.
func (s *relayedSource) unref() { s.dropRef()() }

// dropRef is unref in two parts (ADR-0123 §3): the reference is dropped, and the end claimed
// when it is the last, before it returns; the wait for the teardown is the function it
// returns.
func (s *relayedSource) dropRef() (wait func()) {
	s.set.mu.Lock()
	s.refs--
	last := s.refs == 0
	claimed := last && !s.ending
	if claimed {
		s.ending = true
	}
	s.set.mu.Unlock()
	if claimed {
		s.close(nil)
	}
	if last {
		return func() { <-s.done }
	}
	return func() {}
}

// end decides why the source ended, once: the first reason wins. A nil reason is the end of
// the last viewer, which nobody is left to read. It closes the Hub, so every viewer's Next
// returns the reason, and cancels the source's context, which wakes run. It never blocks, so
// the link's reader may call it.
func (s *relayedSource) end(reason error) {
	s.set.mu.Lock()
	if s.ending {
		s.set.mu.Unlock()
		return
	}
	s.ending = true
	s.set.mu.Unlock()
	s.close(reason)
}

func (s *relayedSource) close(reason error) {
	s.hub.Close(reason)
	s.cancel()
}

// run is the source's life: start, wait to be ended, tear down, leave the set.
func (s *relayedSource) run() {
	if err := s.start(); err != nil {
		s.end(nil)
		s.teardown()
		s.startFail = err
		s.finish()
		close(s.ready)
		return
	}
	close(s.ready)
	s.wg.Add(2)
	go s.read()
	go s.watchLink()
	if s.control {
		s.wg.Add(1)
		s.line.running.Store(true)
		go func() {
			defer s.wg.Done()
			s.line.run()
		}()
	}
	<-s.ctx.Done()
	s.teardown()
	s.finish()
}

// finish takes the source out of the set and releases whoever waits for its end.
func (s *relayedSource) finish() {
	rs := s.set
	rs.mu.Lock()
	if rs.by[s.rt.id] == s {
		delete(rs.by, s.rt.id)
	}
	delete(rs.all, s)
	rs.mu.Unlock()
	close(s.done)
}

// start runs device.watchScreen on the host, attaches the stream's video socket and
// returns why it could not, in this daemon's contract (ADR-0100 §4). The host's answer is
// untrusted as a whole: its result is read into this daemon's own type, its ids are
// bounded, and its error is mapped as a plain relay's is.
func (s *relayedSource) start() error {
	rt := s.rt
	// Before the call: a connection lost between the call and the attach counts as lost.
	s.down = rt.link.Down()
	s.marked = rt.link.Marked() // before the call too: a mark written after it is seen, one before it is in the host's start
	params, err := json.Marshal(WatchScreenParams{Device: rt.serial})
	if err != nil {
		return capability.AsError(err)
	}
	ctx, cancel := context.WithTimeout(s.ctx, relayScreenStart)
	defer cancel()
	resp, stop, err := rt.link.CallRouted(ctx, capability.RequestEnvelope{Capability: "device.watchScreen", Params: params}, s.onEvent)
	if err != nil {
		if s.ctx.Err() != nil {
			return s.endedInStart()
		}
		return linkError(s.ctx, rt, err)
	}
	s.stopRoute = stop
	if resp.Error != nil {
		return s.adb.hostError(resp.Error, watchScreenErrors, rt)
	}
	if resp.Operation == nil || resp.Operation.State != capability.StateCompleted || resp.Result == nil {
		return incompatible(rt, "it answered neither a result nor an error")
	}
	var r WatchScreenResult
	if json.Unmarshal(resp.Result, &r) != nil {
		return incompatible(rt, "its result is not one this version reads")
	}
	// The host's grant is the view's own rule (ADR-0117 §1); an older host's result lacks the
	// field, which reads as false, and nothing else about the view changes.
	s.control = r.Control
	// From here the host holds a subscription for us: teardown ends it, whatever follows.
	if len(r.SubscriptionID) <= maxRelayedID {
		s.sub = r.SubscriptionID
	}
	if r.StreamID == "" || len(r.StreamID) > maxRelayedID || r.SubscriptionID == "" || len(r.SubscriptionID) > maxRelayedID {
		return incompatible(rt, "it named a stream this version cannot attach")
	}
	if r.Width > 0 && r.Height > 0 && r.Width <= screen.MaxDimension && r.Height <= screen.MaxDimension {
		s.size = screen.Size{Width: r.Width, Height: r.Height}
	}
	conn, err := rt.link.DialScreen(ctx, r.StreamID)
	if err != nil {
		if s.ctx.Err() != nil {
			return s.endedInStart()
		}
		return streamFailed("the video socket to the remote " + rt.remote + " could not be opened")
	}
	s.conn = conn
	return nil
}

// endedInStart is why a start that was ended under way failed: the reason the source was
// ended with (the host's `stopped` or a lost link that came before the stream was up) as
// the stream failing, or the context's error for a source nobody waits for any more.
func (s *relayedSource) endedInStart() error {
	var end *ScreenEnd
	if errors.As(s.hub.Reason(), &end) {
		return streamFailed(end.Cause)
	}
	return s.ctx.Err()
}

// onEvent is the host's events for the stream's subscription, called by the link's reader:
// the secure state feeds the seam, and `stopped` ends the source with the same reason. The
// host's words are not repeated: the cause is this daemon's own.
func (s *relayedSource) onEvent(raw json.RawMessage) {
	var e struct {
		Topic   string          `json:"topic"`
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Topic != TopicScreen {
		return
	}
	switch e.Type {
	case EventState:
		var p ScreenStateEvent
		if json.Unmarshal(e.Payload, &p) == nil {
			s.secure.set(p.Secure)
		}
	case EventStopped:
		var p ScreenStoppedEvent
		if json.Unmarshal(e.Payload, &p) == nil {
			s.end(s.hostEnd(p.Reason))
		}
	}
}

// hostEnd is how the source ends when the host's stream stopped for reason: the same three
// reasons, anything else (detached included, which the host says only of a socket this
// source did not close) as failed.
func (s *relayedSource) hostEnd(reason string) *ScreenEnd {
	remote := s.rt.remote
	switch reason {
	case StopDeviceGone:
		return &ScreenEnd{Reason: StopDeviceGone, Cause: "the remote " + remote + " reports that the device is gone"}
	case StopExited:
		return &ScreenEnd{Reason: StopExited, Cause: "the screen server on the remote " + remote + " ended"}
	}
	return &ScreenEnd{Reason: StopFailed, Cause: "the remote " + remote + " ended the stream"}
}

// read feeds the video socket's packets to the Hub until it ends.
func (s *relayedSource) read() {
	defer s.wg.Done()
	for {
		pkts, err := s.conn.Next(s.ctx)
		if err != nil {
			s.readEnded(err)
			return
		}
		for _, p := range pkts {
			s.hub.Push(p)
			probePacket("relayed", p)
		}
	}
}

// readEnded ends the source for a video socket that stopped. A message that is not a packet
// is a protocol failure at once. A socket that closed or broke is the host's end, which its
// `stopped` event or the link's loss says within relayScreenGrace; with neither, it failed.
func (s *relayedSource) readEnded(err error) {
	if s.ctx.Err() != nil {
		return // ended by something else, which is closing the socket
	}
	if errors.Is(err, hosting.ErrScreenFrame) {
		s.end(&ScreenEnd{Reason: StopFailed, Cause: "the video from the remote " + s.rt.remote + " was not in the expected format"})
		return
	}
	// The host closing the socket because this daemon is no longer paired is the end of a
	// device it no longer lends, at once: nothing else is going to say why (ADR-0098 §3).
	if hosting.ScreenRevoked(err) {
		s.end(&ScreenEnd{Reason: StopDeviceGone, Cause: "the remote " + s.rt.remote + " revoked the pairing"})
		return
	}
	t := time.NewTimer(relayScreenGrace)
	defer t.Stop()
	select {
	case <-s.ctx.Done():
	case <-t.C:
		s.end(&ScreenEnd{Reason: StopFailed, Cause: "the video socket to the remote " + s.rt.remote + " closed"})
	}
}

// watchLink ends the source when the link it lives on is lost (deviceGone), or when the
// remote's fast mark was written to the host (renewed, ADR-0123 §2): the view is ended so it
// can start again at the new quality; nothing else of the remote is touched. A loss is never
// reported as renewed: when the link is down by the time the mark channel is seen closed,
// the end is the loss.
func (s *relayedSource) watchLink() {
	defer s.wg.Done()
	select {
	case <-s.ctx.Done():
	case <-s.down:
		s.end(&ScreenEnd{Reason: StopDeviceGone, Cause: "the link to the remote " + s.rt.remote + " was lost"})
	case <-s.marked:
		select {
		case <-s.down:
			s.end(&ScreenEnd{Reason: StopDeviceGone, Cause: "the link to the remote " + s.rt.remote + " was lost"})
		default:
			s.end(&ScreenEnd{Reason: StopRenewed, Cause: "the remote " + s.rt.remote + "'s fast mark changed"})
		}
	}
}

// teardown leaves nothing of the source on either side: the video socket closed (the host
// sees it end and releases the stream), the goroutines joined, the events no longer routed,
// and the host asked to end the subscription. It runs on run's goroutine, once.
func (s *relayedSource) teardown() {
	s.cancel()
	// Nothing queued is flushed: the read's context, cancelled by the end, has already closed
	// the socket. A finger the last view held is lifted by the host, whose release of the
	// stream writes ACTION_CANCEL when this socket closes (ADR-0117 §5).
	s.line.shutdown()
	if s.conn != nil {
		s.conn.Close()
	}
	s.wg.Wait()
	if s.stopRoute != nil {
		s.stopRoute()
	}
	if s.sub == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), relayScreenCleanup)
	defer cancel()
	p, _ := json.Marshal(event.UnsubscribeParams{SubscriptionID: s.sub})
	s.rt.link.Call(ctx, capability.RequestEnvelope{Capability: event.CapUnsubscribe, Params: p})
}

// Join implements screen.Source.
func (s *relayedSource) Join() screen.Viewer { return s.hub.Join() }

// Size implements screen.Source: the newest size the stream's SPS announced, else the size
// the host gave at start.
func (s *relayedSource) Size() screen.Size {
	if z := s.hub.Size(); z != (screen.Size{}) {
		return z
	}
	return s.size
}

// Done implements screen.Source.
func (s *relayedSource) Done() <-chan struct{} { return s.hub.Done() }

// Reason implements screen.Source: the *ScreenEnd that ended the stream, or screen.ErrClosed
// after the last release.
func (s *relayedSource) Reason() error { return s.hub.Reason() }

// Inject implements screen.Source: a remote device's input is the host's capability call
// (ADR-0100 §3), so this one is never the way in.
func (s *relayedSource) Inject(context.Context, screen.Holder, screen.Input) error {
	return errRelayedInput
}

// Control implements screen.Source: a remote view's event is queued for the peer's screen
// socket as the wire's frame (ADR-0117), when the host granted control; otherwise it is refused.
func (s *relayedSource) Control(h screen.Holder, ev screen.Event) error {
	if !s.control {
		return errRelayedInput
	}
	return s.line.Control(h, ev)
}

// ControlGranted says whether the host's result granted input to this view (controlGranter).
func (s *relayedSource) ControlGranted() bool { return s.control }

// WatchSecure implements screen.Source: the host's FLAG_SECURE state, from its events.
func (s *relayedSource) WatchSecure(fn func(secure bool)) (stop func()) { return s.secure.watch(fn) }
