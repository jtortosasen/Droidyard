package device

import (
	"context"
	"encoding/json"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
)

// The screen's events of a remote device (plan m2x step 3; ADR-0115 Decision 1, ADR-0100 §3):
// device.uiEvents is relayed plain, with the host's cursor passed through unchanged, and
// device.watchUiEvents as device.watchLogs is (remotelogs.go): the call opens the caller's
// subscription here and one on the host, whose events are republished with `device` the
// remote id. No reader runs here. What the host sends is another machine's: it is bounded and
// checked against the published types before it reaches the caller.

var (
	// relayUIStart bounds device.watchUiEvents on the host, whose own start may start the
	// reader.
	relayUIStart = 30 * time.Second
	// relayUICleanup bounds the host call that ends the subscription.
	relayUICleanup = 5 * time.Second
)

// watchUIEventsErrors are the codes device.watchUiEvents declares; a host's other code is not
// one this daemon can read (ADR-0100 §4).
var watchUIEventsErrors = append(append([]capability.ErrorCode{}, LentErrors...),
	ErrUIDumpFailed, capability.ErrInvalidParams, capability.ErrSessionRequired)

// uiEventsRelayed completes a relayed device.uiEvents: the cursor must be the shape the host's
// reader writes (it is only ever passed back to a host, opaque), the events and the gap are
// bounded as the local read bounds them, and the host's warnings are bounded.
func uiEventsRelayed(rt *remoteTarget, r UIEventsResult) (any, error) {
	if _, _, ok := parseEventsCursor(r.Cursor); !ok {
		return nil, incompatible(rt, "it answered a cursor this version cannot read")
	}
	events, ok := checkedUIEvents(r.Events)
	if !ok {
		return nil, incompatible(rt, "it answered events this version cannot read")
	}
	gap, ok := checkedUIGap(r.Gap)
	if !ok {
		return nil, incompatible(rt, "it answered a gap this version cannot read")
	}
	switch r.Outcome {
	case "", outcomeChanged, outcomeSettled, outcomeTimeout:
	default:
		return nil, incompatible(rt, "it answered an outcome this version cannot read")
	}
	return UIEventsResult{
		Device: rt.id, Cursor: r.Cursor, Events: events, More: r.More, Gap: gap, Outcome: r.Outcome,
		Warnings: remoteWarnings(rt, r.Warnings),
	}, nil
}

// checkedUIEvents is a host's events as this daemon passes them on: at most the most a read
// may return, each of a published type with a time, names and text cut as the reader cuts
// them, and text on a toast only. ok is false for a list the published contract cannot hold.
func checkedUIEvents(in []UIEvent) (out []UIEvent, ok bool) {
	if len(in) > eventsMaxLimit {
		return nil, false
	}
	out = make([]UIEvent, 0, len(in))
	for _, e := range in {
		if !eventTypes[e.Type] || len(e.Time) > 64 {
			return nil, false
		}
		if _, err := time.Parse(time.RFC3339Nano, e.Time); err != nil {
			return nil, false
		}
		e.Package, e.Class = cutRunes(e.Package, eventsNameCap), cutRunes(e.Class, eventsNameCap)
		if e.Type == "toast" {
			e.Text = cutRunes(e.Text, eventsTextCap)
		} else {
			e.Text = ""
		}
		e.Count, e.Changes = max(e.Count, 0), max(e.Changes, 0)
		out = append(out, e)
	}
	return out, true
}

// checkedUIGap is a host's gap, nil for none; ok is false for a reason that is not published.
func checkedUIGap(g *UIEventsGap) (out *UIEventsGap, ok bool) {
	if g == nil {
		return nil, true
	}
	switch g.Reason {
	case gapStarted, gapRestarted, gapOverflow:
		return &UIEventsGap{Reason: g.Reason, Dropped: max(g.Dropped, 0)}, true
	}
	return nil, false
}

// cutRunes is s cut at n characters.
func cutRunes(s string, n int) string {
	s = truncateUTF8(s, 4*n) // a character is at most four bytes: the conversion below is bounded
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// relayedUISet holds the live relayed UI events streams by the client session and remote
// device that started them, so that a session's second stream of a remote device ends its
// first (ADR-0112 Decision 1), as relayedLogSet does for Logcat. The host sees only this
// daemon's link session, which holds up to peerStreamsMax streams of a device for distinct
// sessions of this daemon. The zero value is ready.
type relayedUISet struct {
	mu sync.Mutex
	by map[streamKey]*relayedUI
}

// replace makes s the stream of its session and device and returns the one it replaces.
func (rs *relayedUISet) replace(s *relayedUI) *relayedUI {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.by == nil {
		rs.by = map[streamKey]*relayedUI{}
	}
	old := rs.by[s.key]
	rs.by[s.key] = s
	return old
}

// remove takes s out of the set if it is still its session's stream.
func (rs *relayedUISet) remove(s *relayedUI) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.by[s.key] == s {
		delete(rs.by, s.key)
	}
}

// relayedUI is one remote watchUiEvents call's stream.
type relayedUI struct {
	adb       *ADB
	rt        *remoteTarget
	bus       *event.Bus
	sub       *event.Subscription // the caller's, on this daemon
	operation string
	key       streamKey // the caller's session and the remote id

	ready chan struct{} // closed when the start is over, failed or not
	quit  chan struct{} // closed by finish: the link watch ends
	once  sync.Once     // finish

	mu      sync.Mutex
	hostSub string // the host's subscription, "" until it named one
	stop    func() // ends the link's routing of that subscription's events
	ended   bool   // the caller's subscription ended: nothing more is published to it
	stopped bool   // a stopped event was published: no other follows
}

// watchUIEventsRemote serves device.watchUiEvents on a remote device.
func (a *ADB) watchUIEventsRemote(ctx context.Context, call *capability.Call, rt *remoteTarget) (any, error) {
	p := call.Params.(WatchUIEventsParams)
	if err := p.validate(); err != nil {
		return nil, err
	}
	sess, err := event.SessionOf(call)
	if err != nil {
		return nil, err
	}
	if a.bus == nil {
		return nil, capability.NewError(capability.ErrSessionRequired, "device.watchUiEvents needs the daemon's event bus").
			WithNextStep("watch over the WebSocket session, GET /v1/ws")
	}
	// Whatever comes of this call involves another machine's answers.
	yes := true
	call.Untrusted = &yes
	params, err := hostParams(call.Params, rt.serial)
	if err != nil {
		return nil, capability.AsError(err)
	}
	s := &relayedUI{adb: a, rt: rt, bus: a.bus, operation: call.OperationID,
		key:   streamKey{session: sess.ID(), serial: rt.id},
		ready: make(chan struct{}), quit: make(chan struct{})}
	id, sub, serr := sess.Open([]string{TopicUIEvents}, s.unsubscribed)
	if serr != nil {
		return nil, serr
	}
	s.sub = sub
	if err := s.start(ctx, params); err != nil {
		close(s.ready)
		s.finish()
		sess.Unsubscribe(id)
		return nil, err
	}
	close(s.ready)
	// The same session streaming the same device again replaces the stream it had.
	if old := a.relayedUIEvents.replace(s); old != nil {
		old.replaced()
	}
	select {
	case <-s.quit: // ended while it started: finish has already come and gone
		a.relayedUIEvents.remove(s)
	default:
	}
	// The last act: a cancel that landed during the start ends the stream, so nothing
	// outlives a cancelled operation.
	if ctx.Err() != nil {
		s.finish()
		sess.Unsubscribe(id)
		return nil, ctx.Err()
	}
	return WatchUIEventsResult{SubscriptionID: id, Device: rt.id}, nil
}

// start runs device.watchUiEvents on the host, routing its subscription's events to this
// stream before the next frame is read. The link's connection is taken before the call, so one
// lost in between counts as lost.
func (s *relayedUI) start(ctx context.Context, params json.RawMessage) error {
	rt := s.rt
	down := rt.link.Down()
	cctx, cancel := context.WithTimeout(ctx, relayUIStart)
	defer cancel()
	resp, stop, err := rt.link.CallRouted(cctx, capability.RequestEnvelope{Capability: "device.watchUiEvents", Params: params}, s.onEvent)
	if err != nil {
		return linkError(ctx, rt, err)
	}
	s.mu.Lock()
	s.stop = stop
	s.mu.Unlock()
	if resp.Error != nil {
		return s.adb.hostError(resp.Error, watchUIEventsErrors, rt)
	}
	if resp.Operation == nil || resp.Operation.State != capability.StateCompleted || resp.Result == nil {
		return incompatible(rt, "it answered neither a result nor an error")
	}
	var r WatchUIEventsResult
	if json.Unmarshal(resp.Result, &r) != nil {
		return incompatible(rt, "its result is not one this version reads")
	}
	// From here the host holds a subscription for us: finish ends it, whatever follows.
	if len(r.SubscriptionID) <= maxRelayedID {
		s.mu.Lock()
		s.hostSub = r.SubscriptionID
		s.mu.Unlock()
	}
	if r.SubscriptionID == "" || len(r.SubscriptionID) > maxRelayedID {
		return incompatible(rt, "it named a subscription this version cannot use")
	}
	go s.watchLink(down)
	return nil
}

// unsubscribed is the end hook of the caller's subscription (its events.unsubscribe or its
// session's end). It never blocks: the host is told on a goroutine of its own, once the
// start is over.
func (s *relayedUI) unsubscribed() {
	s.mu.Lock()
	s.ended = true
	s.mu.Unlock()
	go func() {
		<-s.ready
		s.finish()
	}()
}

// finish leaves nothing of the stream on either side: the link watch ended, the events no
// longer routed, and the host asked to end its subscription, which ends its poller. It runs
// once.
func (s *relayedUI) finish() {
	s.once.Do(func() {
		close(s.quit)
		s.adb.relayedUIEvents.remove(s)
		s.mu.Lock()
		stop, hostSub := s.stop, s.hostSub
		s.mu.Unlock()
		if stop != nil {
			stop()
		}
		if hostSub == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), relayUICleanup)
		defer cancel()
		p, _ := json.Marshal(event.UnsubscribeParams{SubscriptionID: hostSub})
		s.rt.link.Call(ctx, capability.RequestEnvelope{Capability: event.CapUnsubscribe, Params: p})
	})
}

// replaced ends the stream because its session started another of the same device: stopped
// replaced is published to its subscription, which stays open, and the host's stream is
// ended. It never blocks.
func (s *relayedUI) replaced() {
	s.publishStopped(StopReplaced, "")
	go s.finish()
}

// watchLink ends the stream with deviceGone when the link it lives on is lost (ADR-0042: the
// device is not there to read).
func (s *relayedUI) watchLink(down <-chan struct{}) {
	select {
	case <-s.quit:
	case <-down:
		s.publishStopped(StopDeviceGone, "the link to the remote "+s.rt.remote+" was lost")
	}
}

// onEvent is the host's events for the stream's subscription, called by the link's reader:
// `events` are republished with the remote id, `stopped` with this daemon's own words. It
// never blocks.
func (s *relayedUI) onEvent(raw json.RawMessage) {
	var e struct {
		Topic   string          `json:"topic"`
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Topic != TopicUIEvents {
		return
	}
	switch e.Type {
	case EventUIEvents:
		var p UIEventsPush
		if json.Unmarshal(e.Payload, &p) == nil {
			s.publishEvents(p)
		}
	case EventStopped:
		var p UIEventsStopped
		if json.Unmarshal(e.Payload, &p) == nil {
			s.publishStopped(s.hostReason(p.Reason))
		}
	}
}

// publishEvents republishes a batch from the host, checked as a local read is. A batch the
// published contract cannot hold ends the stream as failed, in this daemon's words.
func (s *relayedUI) publishEvents(p UIEventsPush) {
	events, ok := checkedUIEvents(p.Events)
	gap, gapOK := checkedUIGap(p.Gap)
	if !ok || !gapOK {
		s.publishStopped(StopFailed, "the remote "+s.rt.remote+" sent events this daemon cannot read")
		go s.finish()
		return
	}
	s.mu.Lock()
	skip := s.ended || s.stopped
	s.mu.Unlock()
	if skip {
		return
	}
	s.bus.PublishTo(s.sub, TopicUIEvents, EventUIEvents, "", s.operation,
		UIEventsPush{Device: s.rt.id, Events: events, Gap: gap, Untrusted: true})
}

// hostReason is the stopped event of a host's reason, in this daemon's words: its three
// reasons, anything else as failed. The host's cause is not repeated.
func (s *relayedUI) hostReason(reason string) (string, string) {
	remote := s.rt.remote
	switch reason {
	case StopDeviceGone:
		return StopDeviceGone, "the remote " + remote + " reports that the device is gone"
	case StopReplaced:
		return StopReplaced, "the remote " + remote + " ended this stream to make room for another of this daemon's"
	}
	return StopFailed, "the remote " + remote + " could not read the device's UI events"
}

// publishStopped publishes the stream's end, once: after it nothing more reaches the
// subscription, which stays open until the caller ends it.
func (s *relayedUI) publishStopped(reason, cause string) {
	s.mu.Lock()
	skip := s.ended || s.stopped
	s.stopped = true
	s.mu.Unlock()
	if skip {
		return
	}
	s.bus.PublishTo(s.sub, TopicUIEvents, EventStopped, "", s.operation,
		UIEventsStopped{Device: s.rt.id, Reason: reason, Cause: cause})
}
