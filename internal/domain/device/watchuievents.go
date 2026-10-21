package device

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
)

// device.watchUiEvents (ADR-0112): the screen's events as a stream for the calling session
// (WebSocket or CLI), for the web UI's panel. The call reads where the reader's ring ends, opens a
// session subscription with an end hook and starts a poller that asks the reader for the
// events after its own cursor and publishes them with Bus.PublishTo, outside the replay ring,
// as device.watchLogs does (ADR-0041). The poller is device.uiEvents's own reach to the
// reader (uievents.go): it takes no lock and recovers a reader that does not answer. It is
// never an MCP tool (ADR-0010). It is lendable (ADR-0115 Decision 1): a peer's stream counts as
// activity for its lease and ends with it, and a link session keeps at most peerStreamsMax of
// them per device; on a remote id it is relayed (remoteuievents.go).

// TopicUIEvents is the topic device.watchUiEvents' events carry. Like device.logcat it is not
// in events.subscribe's enum: only the subscription the call opened receives it.
const TopicUIEvents = "device.uiEvents"

// EventUIEvents is the type of an event carrying the screen's events; EventStopped (the log
// stream's) is the other.
const EventUIEvents = "events"

var (
	// uiStreamEvery is the pause between two polls of a stream (ADR-0112 Decision 3: 250 ms, a
	// working default). A variable so a test is quick.
	uiStreamEvery = 250 * time.Millisecond
)

// uiStreamPages is the most pages (of eventsMaxLimit events) a stream reads in one tick when
// the ring holds more: a storm is read over several ticks, never in one unbounded loop.
const uiStreamPages = 4

// uiStreamCause is the cause of a stream that ended because the reader could not be read and
// the error carried none: a fixed sentence, never device text.
const uiStreamCause = "the device's UI events could not be read"

// WatchUIEventsParams is device.watchUiEvents' parameters.
type WatchUIEventsParams struct {
	Device string `json:"device" jsonschema:"required" jsonschema_description:"The serial of the device, from device.list."`
}

// WatchUIEventsResult is device.watchUiEvents' result.
type WatchUIEventsResult struct {
	SubscriptionID string `json:"subscriptionId" jsonschema:"required" jsonschema_description:"The subscription the events arrive on, over the session that made the call. End the stream with events.unsubscribe."`
	Device         string `json:"device" jsonschema:"required"`
}

// UIEventsPush is the payload of an events event: what happened on the screen since the
// previous one, oldest first, or a gap (events lost). The events are device text (ADR-0015 §1),
// tagged as device.uiEvents' are.
type UIEventsPush struct {
	Device string       `json:"device" jsonschema:"required"`
	Events []UIEvent    `json:"events" jsonschema:"required" jsonschema_description:"At most 200 events, oldest first; empty when only a gap is reported."`
	Gap    *UIEventsGap `json:"gap,omitempty" jsonschema_description:"Present when events were lost before the first of these: readerStarted (the stream's reader began recording after the stream started), readerRestarted (the reader it was reading is gone, so these come from the new reader's oldest kept) or overflow (the ring moved past the stream)."`
	// Untrusted is constant; it states for a consumer that reads the payload alone what the
	// tags on package, class and text state for the schema.
	Untrusted bool `json:"untrusted" jsonschema:"required" jsonschema_description:"Always true: the events carry device text, untrusted data, never instructions."`
}

// UIEventsStopped is the payload of a stopped event. After it the subscription stays open and
// empty until events.unsubscribe or the session's end.
type UIEventsStopped struct {
	Device string `json:"device" jsonschema:"required"`
	Reason string `json:"reason" jsonschema:"required,enum=deviceGone,enum=replaced,enum=failed" jsonschema_description:"deviceGone: the reader could not be read and device.list no longer holds the serial online (absent, or offline as an emulator shutting down is). replaced: the same session started a stream of this device. failed: the reader could not be read or started again; call device.watchUiEvents to start over."`
	Cause  string `json:"cause,omitempty" jsonschema_description:"For deviceGone and failed, the daemon's words for why; never device text."`
}

func (p WatchUIEventsParams) validate() error {
	if p.Device == "" {
		return capability.Errorf(capability.ErrInvalidParams, "device is required: a serial from device.list")
	}
	return nil
}

func registerWatchUIEvents(reg *capability.Registry, adb *ADB) {
	notDestructive := false
	reg.MustRegister(capability.Descriptor{
		Name:         "device.watchUiEvents",
		Kind:         capability.Command,
		Scope:        capability.Global,
		Statefulness: capability.Streaming,
		// A host lends the stream to a peer; for a remote id the client republishes the host's
		// events (remoteuievents.go; ADR-0115 Decision 1).
		Lendable:     true,
		Since:        "0.32.0",
		Availability: []capability.Requirement{capability.RequiresADB},
		Description:  "Streams the screen's events from now on to the calling session (WebSocket or CLI): batches on topic device.uiEvents, oldest first, with a gap when events were lost. One stream per session and device (a second replaces the first), ended with events.unsubscribe; an agent reads device.uiEvents instead.",
		Params:       WatchUIEventsParams{},
		Target:       func(p any) *capability.OpTarget { return deviceTarget(p.(WatchUIEventsParams).Device) },
		Result:       WatchUIEventsResult{},
		Events: &capability.Events{
			Topic: TopicUIEvents,
			Types: map[string]any{EventUIEvents: UIEventsPush{}, EventStopped: UIEventsStopped{}},
		},
		Errors: slices.Clone(watchUIEventsErrors),
		Annotations: capability.Annotations{
			Destructive: &notDestructive,
			Reason:      "adds a subscription to the calling session and reads the reader's event ring; changes nothing on the device",
		},
		Authorize: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(WatchUIEventsParams)
			// Before the hook, so a malformed parameter runs no adb command at all.
			if err := p.validate(); err != nil {
				return nil, err
			}
			rt, remote, err := adb.authorizeRemote(call)
			if err != nil {
				return nil, err
			}
			if remote {
				return rt, nil
			}
			return adb.authorize(ctx, call.Origin, p.Device, "device.watchUiEvents")
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			if rt, ok := call.Authorized.(*remoteTarget); ok {
				return adb.watchUIEventsRemote(ctx, call, rt)
			}
			return adb.watchUIEvents(ctx, call)
		},
	})
}

// uiEnd is why a stream ended, decided once by whoever gets there first.
type uiEnd int

const (
	uiEndNone         uiEnd = iota
	uiEndSubscription       // events.unsubscribe, or the session's close: nothing to publish
	uiEndReplaced           // the session started a stream of the same device: stopped is published
	uiEndShutdown           // StopAll
	uiEndCancelled          // the call was cancelled while starting
	uiEndFailed             // the poller gave up: stopped is published
	uiEndLease              // the host ended the peer's lease of the device (ADR-0104 §4): stopped is published
)

// uiEventStreams is every live stream of the daemon. A stream is in it from the call that
// created it until its poller has returned, so StopAll can wait for one whose end hook already
// fired. The zero value is ready.
type uiEventStreams struct {
	mu       sync.Mutex
	stopping bool
	all      map[*uiEventStream]struct{}
	byKey    map[streamKey]*uiEventStream
	peers    map[streamKey][]*uiEventStream // a peer session's streams of a device, oldest first
}

var errUIStreamsStopping = errors.New("the daemon is stopping")

// add puts s in the set, refusing once StopAll has begun, and returns the stream it replaces,
// if any: for a local session the stream of the same session and device; for a paired peer's
// link session, which keeps up to peerStreamsMax streams of a device (its client has one
// session for all its own, ADR-0106), its oldest once the cap is reached.
func (st *uiEventStreams) add(s *uiEventStream) (old *uiEventStream, err error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.stopping {
		return nil, errUIStreamsStopping
	}
	if st.all == nil {
		st.all, st.byKey, st.peers = map[*uiEventStream]struct{}{}, map[streamKey]*uiEventStream{}, map[streamKey][]*uiEventStream{}
	}
	st.all[s] = struct{}{}
	if s.peer {
		list := st.peers[s.key]
		if len(list) >= peerStreamsMax {
			old, list = list[0], list[1:]
		}
		st.peers[s.key] = append(list, s)
		return old, nil
	}
	old = st.byKey[s.key]
	st.byKey[s.key] = s
	return old, nil
}

// remove takes s out of the set: the last thing a stream does.
func (st *uiEventStreams) remove(s *uiEventStream) {
	if s.activity != nil {
		defer s.activity() // after the set's lock is released
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.all, s)
	if st.byKey[s.key] == s {
		delete(st.byKey, s.key)
	}
	if list := st.peers[s.key]; s.peer {
		if i := slices.Index(list, s); i >= 0 {
			list = slices.Delete(list, i, i+1)
		}
		if len(list) == 0 {
			delete(st.peers, s.key)
		} else {
			st.peers[s.key] = list
		}
	}
}

// endLease ends every stream the link session holds of serial, as stopped deviceGone with the
// cause of the host's end of the lease (ADR-0104 §4).
func (st *uiEventStreams) endLease(session, serial string) {
	st.mu.Lock()
	list := slices.Clone(st.peers[streamKey{session: session, serial: serial}])
	st.mu.Unlock()
	for _, s := range list {
		s.end(uiEndLease)
	}
}

// Live is how many streams are in the set: zero once every poller has returned.
func (st *uiEventStreams) Live() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.all)
}

// StopAll ends every stream and waits until each has left the set. New streams are refused
// from here on. It returns early only when ctx ends; ADB.Close calls it with the other
// streams (ADR-0041 §6).
func (st *uiEventStreams) StopAll(ctx context.Context) {
	st.mu.Lock()
	st.stopping = true
	list := make([]*uiEventStream, 0, len(st.all))
	for s := range st.all {
		list = append(list, s)
	}
	st.mu.Unlock()
	for _, s := range list {
		s.end(uiEndShutdown)
	}
	for _, s := range list {
		select {
		case <-s.done:
		case <-ctx.Done():
			return
		}
	}
}

// LiveUIEventStreams is how many device.watchUiEvents streams are running. Test-only in
// practice.
func (a *ADB) LiveUIEventStreams() int { return a.uiStreams.Live() }

// uiEventStream is one device.watchUiEvents stream: the cursor it has read to and the poller
// that reads on from it. Only the poller publishes, so the events precede the stopped event
// and nothing follows it.
type uiEventStream struct {
	adb       *ADB
	set       *uiEventStreams
	bus       *event.Bus
	sub       *event.Subscription
	key       streamKey
	t         *target
	operation string
	peer      bool // opened by a paired peer's session: bounded by peerStreamsMax, not replaced
	// activity ends the stream's count as activity of its peer's lease (ADR-0104 §3); set
	// once, before the poller runs, and called when the stream leaves the set.
	activity func()

	// ctx is the poller's own context: the operation's ends when the call completes. cancel is
	// the end.
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed after the stream left the set

	// inst and base are the reader instance the stream reads and the sequence number it has
	// read to; only the poller touches them after the stream started.
	inst string
	base uint64
	// gap is what the first read reported (the reader was started for it), published first.
	gap *UIEventsGap

	mu     sync.Mutex
	reason uiEnd
}

// end records why the stream ends, the first reason wins, and stops the poller. It never
// blocks, so a subscription's end hook may call it. It returns the reason that stuck.
func (s *uiEventStream) end(r uiEnd) uiEnd {
	s.mu.Lock()
	if s.reason == uiEndNone {
		s.reason = r
	}
	got := s.reason
	s.mu.Unlock()
	s.cancel()
	return got
}

// endedFor is why the stream was ended, uiEndNone while it has not been.
func (s *uiEventStream) endedFor() uiEnd {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reason
}

// run polls until the stream ends, publishes what ended it when someone is to be told, and
// leaves the set.
func (s *uiEventStream) run() {
	defer close(s.done)
	defer s.set.remove(s)
	defer s.cancel()
	if s.gap != nil {
		s.publish(nil, s.gap)
	}
	reason, cause := s.poll()
	if reason != "" {
		// A replace can land while the failure is being classified (readerStopOf lists devices):
		// the replaced subscription is told so, as when the replace comes first.
		switch s.end(uiEndFailed) {
		case uiEndFailed:
			s.publishStopped(reason, cause)
		case uiEndReplaced:
			s.publishStopped(StopReplaced, "")
		case uiEndLease:
			s.publishStopped(StopDeviceGone, leaseEndedCause)
		}
		return
	}
	// Ended from outside: only a replaced stream, or one whose lease ended, has someone to tell.
	switch s.endedFor() {
	case uiEndReplaced:
		s.publishStopped(StopReplaced, "")
	case uiEndLease:
		s.publishStopped(StopDeviceGone, leaseEndedCause)
	}
}

// poll reads every uiStreamEvery until the stream's context ends, which returns "", or the
// reader cannot be read, which returns the stopped reason and its cause.
func (s *uiEventStream) poll() (reason, cause string) {
	tick := time.NewTicker(uiStreamEvery)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return "", ""
		case <-tick.C:
		}
		if reason, cause = s.step(); reason != "" || s.ctx.Err() != nil {
			return reason, cause
		}
	}
}

// step reads what the ring holds after the cursor, up to uiStreamPages pages, and publishes
// it. A reader that does not answer is restarted once by the patient reach, under the read
// lock; one that still does not ends the stream: it is never asked again. A lock another
// caller holds ends the tick quietly.
func (s *uiEventStream) step() (reason, cause string) {
	// The poller is patient (ADR-0114): a reader another call holds the lock of is an empty
	// tick, never a failure; it fails only when restarting a dead reader, with the lock, fails.
	r := &eventsReach{a: s.adb, t: s.t, c: s.adb.reader(s.t.serial), end: patientEnd(), patient: true}
	fail := func(err error) (string, string) {
		if s.ctx.Err() != nil || errors.Is(err, errReaderBusy) {
			return "", ""
		}
		return readerStopOf(s.ctx, s.adb, s.t, err)
	}
	for page := 0; page < uiStreamPages; page++ {
		ans, err := r.poll(s.ctx, s.base, eventsMaxLimit)
		if err != nil {
			return fail(err)
		}
		// An answer from another instance, or a cursor ahead of the ring, is a reader that is
		// not the one the cursor names: read the new ring from its first entry (ADR-0111
		// Decision 6).
		// The stream's cursor moves only once the new ring is read: a busy tick in between
		// leaves it on the old instance, so the next tick sees the change and reports the gap.
		restarted := false
		inst, base := s.inst, s.base
		for changes := 0; ans.Instance != inst || base > ans.NextSeq-1; changes++ {
			if changes >= eventsInstanceMax {
				return fail(eventsFailed("the reader behind the device's forward keeps changing"))
			}
			restarted = true
			inst, base = ans.Instance, 0
			if ans, err = r.poll(s.ctx, 0, eventsMaxLimit); err != nil {
				return fail(err)
			}
		}
		s.inst, s.base = inst, base
		var gap *UIEventsGap
		switch {
		case r.started:
			gap = &UIEventsGap{Reason: gapStarted}
		case restarted:
			gap = &UIEventsGap{Reason: gapRestarted}
		case ans.OldestSeq > s.base+1:
			gap = &UIEventsGap{Reason: gapOverflow, Dropped: int(ans.OldestSeq - 1 - s.base)}
		}
		events := make([]UIEvent, 0, len(ans.Events))
		last := ans.NextSeq - 1
		for _, ev := range ans.Events {
			events = append(events, uiEventOf(ev))
			last = ev.Seq
		}
		s.base = last
		if len(events) > 0 || gap != nil {
			s.publish(events, gap)
		}
		if last >= ans.NextSeq-1 {
			break
		}
	}
	return "", ""
}

func (s *uiEventStream) publish(events []UIEvent, gap *UIEventsGap) {
	if events == nil {
		events = []UIEvent{}
	}
	s.bus.PublishTo(s.sub, TopicUIEvents, EventUIEvents, "", s.operation,
		UIEventsPush{Device: s.t.serial, Events: events, Gap: gap, Untrusted: true})
}

func (s *uiEventStream) publishStopped(reason, cause string) {
	s.bus.PublishTo(s.sub, TopicUIEvents, EventStopped, "", s.operation,
		UIEventsStopped{Device: s.t.serial, Reason: reason, Cause: cause})
}

// readerStopOf is the reason and cause of a stream or a recording whose reader cannot be read:
// deviceGone when device.list no longer holds the serial online, else failed. The cause is the
// error's own, which names no device text (events.go), or a fixed sentence.
func readerStopOf(ctx context.Context, adb *ADB, t *target, err error) (reason, cause string) {
	cause = uiStreamCause
	var ce *capability.Error
	if errors.As(err, &ce) && ce.Cause != "" {
		cause = ce.Cause
	}
	reason = StopFailed
	lctx, cancel := context.WithTimeout(ctx, streamListBound)
	defer cancel()
	if devices, lerr := adb.listAll(lctx, t.adb); lerr == nil {
		gone := true
		for _, d := range devices {
			if d.Device == t.serial && d.State == StateDevice {
				gone = false
				break
			}
		}
		if gone {
			reason = StopDeviceGone
		}
	}
	return reason, cause
}

// watchUIEvents serves device.watchUiEvents: it reads where the reader's ring ends (starting
// the reader when the daemon has none, which the stream's first event reports), opens the
// session's subscription with its end hook and starts the poller.
func (a *ADB) watchUIEvents(ctx context.Context, call *capability.Call) (any, error) {
	p := call.Params.(WatchUIEventsParams)
	if err := p.validate(); err != nil {
		return nil, err
	}
	sess, err := event.SessionOf(call)
	if err != nil {
		return nil, err
	}
	// The bus is the daemon's; without it (the golden registry's descriptors) a stream cannot run.
	if a.bus == nil {
		return nil, capability.NewError(capability.ErrSessionRequired, "device.watchUiEvents needs the daemon's event bus").
			WithNextStep("watch over the WebSocket session, GET /v1/ws")
	}
	t := call.Authorized.(*target)
	if err := a.ready(ctx, t); err != nil {
		return nil, err
	}
	// The newest event is read, and so sealed (uievents.go): the stream reads what comes
	// after it. Its errors are device.uiEvents's.
	first, err := a.uiEvents(ctx, t, t.serial, eventsQuery{max: 1, quietMs: eventsQuietDef})
	if err != nil {
		return nil, err
	}
	inst, base, ok := parseEventsCursor(first.Cursor)
	if !ok {
		return nil, eventsFailed("the reader's cursor could not be read")
	}

	s := &uiEventStream{
		adb: a, set: &a.uiStreams, bus: a.bus,
		key: streamKey{session: sess.ID(), serial: t.serial}, t: t, operation: call.OperationID,
		peer: call.Origin.Client == capability.ClientPeer,
		done: make(chan struct{}), inst: inst, base: base, gap: first.Gap,
	}
	// The poller's own context: the operation's ends when this call completes.
	s.ctx, s.cancel = context.WithCancel(context.WithoutCancel(ctx))
	id, sub, serr := sess.Open([]string{TopicUIEvents}, func() { s.end(uiEndSubscription) })
	if serr != nil {
		s.cancel()
		return nil, serr
	}
	s.sub = sub
	a.started()
	old, err := a.uiStreams.add(s)
	if err != nil {
		sess.Unsubscribe(id)
		return nil, capability.NewError(ErrUIDumpFailed, "the device's UI events could not be streamed").
			WithCause(err.Error()).
			WithNextStep("Start the daemon again, then retry.")
	}
	// An open stream is activity of the peer's lease until it leaves the set (ADR-0104 §3).
	s.activity = a.streamActivity(call)
	// A lease that ended while the stream was starting found it in no table: it ends here
	// (ADR-0104 §4), and one that ends after this check finds it in the table.
	if a.leaseLost(call, t.serial) {
		s.end(uiEndLease)
	}
	go s.run()
	// The same session streaming the same device again replaces the stream it had.
	if old != nil {
		old.end(uiEndReplaced)
	}
	// The last act: a cancel that landed while the stream started ends it, so nothing
	// outlives a cancelled operation.
	if ctx.Err() != nil {
		s.end(uiEndCancelled)
		<-s.done
		sess.Unsubscribe(id)
		return nil, ctx.Err()
	}
	return WatchUIEventsResult{SubscriptionID: id, Device: t.serial}, nil
}
