package device

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
)

// device.watchLogs on a remote device (plan m2t step 5; ADR-0100 §3, ADR-0041, ADR-0042): the
// call opens the caller's subscription here and a subscription on the host, whose events
// are republished to this daemon's with `device` the remote id. No logcat runs here. The
// stream ends the way a local one does, with a `stopped` event and the subscription left
// open and empty: the host's own `stopped` (its reasons), or the link lost (`deviceGone`).
// The caller's unsubscribe, or the end of its session, ends the host's subscription, which
// ends its logcat.

// The relayed stream's bounds; variables so a test is quick.
var (
	// relayLogsStart bounds device.watchLogs on the host, whose own start runs adb a few
	// times.
	relayLogsStart = 30 * time.Second
	// relayLogsCleanup bounds the host call that ends the subscription.
	relayLogsCleanup = 5 * time.Second
)

// watchLogsErrors are the codes device.watchLogs declares; a host's other code is not one
// this daemon can read (ADR-0100 §4).
var watchLogsErrors = append(append([]capability.ErrorCode{}, LentErrors...),
	ErrLogsFailed, capability.ErrInvalidParams, capability.ErrSessionRequired)

// relayedLogSet holds the live relayed streams by the client session and remote device that
// started them, so that a session's second stream of a remote device ends its first, as the
// local rule is (ADR-0041: one stream per session and device). The host sees only this
// daemon's link session, which holds several streams of a device for distinct sessions of
// this daemon (peerStreamsMax), so the rule for a session is kept here. The zero value is
// ready.
type relayedLogSet struct {
	mu sync.Mutex
	by map[streamKey]*relayedLogs
}

// replace makes s the stream of its session and device and returns the one it replaces.
func (rs *relayedLogSet) replace(s *relayedLogs) *relayedLogs {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.by == nil {
		rs.by = map[streamKey]*relayedLogs{}
	}
	old := rs.by[s.key]
	rs.by[s.key] = s
	return old
}

// remove takes s out of the set if it is still its session's stream.
func (rs *relayedLogSet) remove(s *relayedLogs) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.by[s.key] == s {
		delete(rs.by, s.key)
	}
}

// relayedLogs is one remote watchLogs call's stream.
type relayedLogs struct {
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

// watchLogsRemote serves device.watchLogs on a remote device.
func (a *ADB) watchLogsRemote(ctx context.Context, call *capability.Call, rt *remoteTarget) (any, error) {
	p := call.Params.(WatchLogsParams)
	if err := p.validate(); err != nil {
		return nil, err
	}
	sess, err := event.SessionOf(call)
	if err != nil {
		return nil, err
	}
	if a.bus == nil {
		return nil, capability.NewError(capability.ErrSessionRequired, "device.watchLogs needs the daemon's event bus").
			WithNextStep("watch over the WebSocket session, GET /v1/ws")
	}
	// Whatever comes of this call involves another machine's answers.
	yes := true
	call.Untrusted = &yes
	params, err := hostParams(call.Params, rt.serial)
	if err != nil {
		return nil, capability.AsError(err)
	}
	s := &relayedLogs{adb: a, rt: rt, bus: a.bus, operation: call.OperationID,
		key:   streamKey{session: sess.ID(), serial: rt.id},
		ready: make(chan struct{}), quit: make(chan struct{})}
	id, sub, serr := sess.Open([]string{TopicLogcat}, s.unsubscribed)
	if serr != nil {
		return nil, serr
	}
	s.sub = sub
	res, err := s.start(ctx, params)
	if err != nil {
		close(s.ready)
		s.finish()
		sess.Unsubscribe(id)
		return nil, err
	}
	close(s.ready)
	res.SubscriptionID, res.Device = id, rt.id
	// The same session streaming the same device again replaces the stream it had.
	if old := a.relayedLogs.replace(s); old != nil {
		old.replaced()
	}
	select {
	case <-s.quit: // ended while it started: finish has already come and gone
		a.relayedLogs.remove(s)
	default:
	}
	// The last act: a cancel that landed during the start ends the stream, so nothing
	// outlives a cancelled operation.
	if ctx.Err() != nil {
		s.finish()
		sess.Unsubscribe(id)
		return nil, ctx.Err()
	}
	return res, nil
}

// start runs device.watchLogs on the host, routing its subscription's events to this
// stream before the next frame is read, and returns the host's answer in this daemon's
// contract. The link's connection is taken before the call, so one lost in between counts
// as lost.
func (s *relayedLogs) start(ctx context.Context, params json.RawMessage) (WatchLogsResult, error) {
	rt := s.rt
	down := rt.link.Down()
	cctx, cancel := context.WithTimeout(ctx, relayLogsStart)
	defer cancel()
	resp, stop, err := rt.link.CallRouted(cctx, capability.RequestEnvelope{Capability: "device.watchLogs", Params: params}, s.onEvent)
	if err != nil {
		return WatchLogsResult{}, linkError(ctx, rt, err)
	}
	s.mu.Lock()
	s.stop = stop
	s.mu.Unlock()
	if resp.Error != nil {
		return WatchLogsResult{}, s.adb.hostError(resp.Error, watchLogsErrors, rt)
	}
	if resp.Operation == nil || resp.Operation.State != capability.StateCompleted || resp.Result == nil {
		return WatchLogsResult{}, incompatible(rt, "it answered neither a result nor an error")
	}
	var r WatchLogsResult
	if json.Unmarshal(resp.Result, &r) != nil {
		return WatchLogsResult{}, incompatible(rt, "its result is not one this version reads")
	}
	// From here the host holds a subscription for us: finish ends it, whatever follows.
	if len(r.SubscriptionID) <= maxRelayedID {
		s.mu.Lock()
		s.hostSub = r.SubscriptionID
		s.mu.Unlock()
	}
	if r.SubscriptionID == "" || len(r.SubscriptionID) > maxRelayedID {
		return WatchLogsResult{}, incompatible(rt, "it named a subscription this version cannot use")
	}
	out := WatchLogsResult{}
	if r.UID != nil && *r.UID >= 0 {
		out.UID = r.UID
	}
	go s.watchLink(down)
	return out, nil
}

// unsubscribed is the end hook of the caller's subscription (its events.unsubscribe or its
// session's end). It never blocks: the host is told on a goroutine of its own, once the
// start is over.
func (s *relayedLogs) unsubscribed() {
	s.mu.Lock()
	s.ended = true
	s.mu.Unlock()
	go func() {
		<-s.ready
		s.finish()
	}()
}

// finish leaves nothing of the stream on either side: the link watch ended, the events no
// longer routed, and the host asked to end its subscription, which ends its logcat. It runs
// once.
func (s *relayedLogs) finish() {
	s.once.Do(func() {
		close(s.quit)
		s.adb.relayedLogs.remove(s)
		s.mu.Lock()
		stop, hostSub := s.stop, s.hostSub
		s.mu.Unlock()
		if stop != nil {
			stop()
		}
		if hostSub == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), relayLogsCleanup)
		defer cancel()
		p, _ := json.Marshal(event.UnsubscribeParams{SubscriptionID: hostSub})
		s.rt.link.Call(ctx, capability.RequestEnvelope{Capability: event.CapUnsubscribe, Params: p})
	})
}

// replaced ends the stream because its session started another of the same device: stopped
// replaced is published to its subscription, which stays open, and the host's stream is
// ended. It never blocks.
func (s *relayedLogs) replaced() {
	s.publishStopped(StopReplaced, "another stream of this device was started by the same session")
	go s.finish()
}

// watchLink ends the stream with deviceGone when the link it lives on is lost (ADR-0042: the
// device is not there to read).
func (s *relayedLogs) watchLink(down <-chan struct{}) {
	select {
	case <-s.quit:
	case <-down:
		s.publishStopped(StopDeviceGone, "the link to the remote "+s.rt.remote+" was lost")
	}
}

// onEvent is the host's events for the stream's subscription, called by the link's reader:
// lines are republished with the remote id, `stopped` with this daemon's own words. It never
// blocks.
func (s *relayedLogs) onEvent(raw json.RawMessage) {
	var e struct {
		Topic   string          `json:"topic"`
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Topic != TopicLogcat {
		return
	}
	switch e.Type {
	case EventLines:
		var p LinesEvent
		if json.Unmarshal(e.Payload, &p) == nil {
			s.publishLines(p)
		}
	case EventStopped:
		var p StoppedEvent
		if json.Unmarshal(e.Payload, &p) == nil {
			s.publishStopped(s.hostReason(p.Reason))
		}
	}
}

// publishLines republishes a batch from the host with the lines bounded as a local stream
// bounds them; a line that is not in the stream's format, or past the batch, is counted as
// dropped, as a local stream counts one it does not deliver.
func (s *relayedLogs) publishLines(p LinesEvent) {
	dropped := max(p.Dropped, 0)
	lines := make([]StreamLine, 0, min(len(p.Lines), streamBatchLines))
	for i, l := range p.Lines {
		if len(lines) == streamBatchLines {
			dropped += len(p.Lines) - i
			break
		}
		if _, ok := priorityRank[l.Priority]; !ok || len(l.Time) > 64 {
			dropped++
			continue
		}
		l.Tag, l.Message = truncateUTF8(l.Tag, logsLineCap), truncateUTF8(l.Message, logsLineCap)
		l.Package = truncateUTF8(l.Package, 256)
		lines = append(lines, l)
	}
	s.mu.Lock()
	skip := s.ended || s.stopped
	s.mu.Unlock()
	if skip {
		return
	}
	s.bus.PublishTo(s.sub, TopicLogcat, EventLines, "", s.operation,
		LinesEvent{Device: s.rt.id, Lines: lines, Dropped: dropped, Untrusted: true})
}

// hostReason is the stopped event of a host's reason, in this daemon's words: its three
// reasons, anything else as exited. The host's cause is not repeated.
func (s *relayedLogs) hostReason(reason string) (string, string) {
	remote := s.rt.remote
	switch reason {
	case StopDeviceGone:
		return StopDeviceGone, "the remote " + remote + " reports that the device is gone"
	case StopReplaced:
		return StopReplaced, "the remote " + remote + " ended this stream to make room for another of this daemon's"
	}
	return StopExited, "the log stream on the remote " + remote + " ended"
}

// publishStopped publishes the stream's end, once: after it nothing more reaches the
// subscription, which stays open until the caller ends it.
func (s *relayedLogs) publishStopped(reason, cause string) {
	s.mu.Lock()
	skip := s.ended || s.stopped
	s.stopped = true
	s.mu.Unlock()
	if skip {
		return
	}
	s.bus.PublishTo(s.sub, TopicLogcat, EventStopped, "", s.operation,
		StoppedEvent{Device: s.rt.id, Reason: reason, Cause: cause})
}
