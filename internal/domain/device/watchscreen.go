package device

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/event"
)

// device.watchScreen (plan m2p step 4; ADR-0078): the live view of a device for the
// calling WebSocket session. The call joins the device's shared source (screensource.go)
// as a viewer and returns a stream id; the video itself travels on GET /v1/screen, whose
// handler holds only AttachScreen: a screen.Viewer, which can read packets and close, and
// nothing that reaches the device (constraint 2). The events on the session carry the
// focused window's secure state (secure.go; ADR-0080) and the stream's end.

// TopicScreen is the topic device.watchScreen's events carry. Like device.logcat it is not
// in events.subscribe's enum: only the subscription the call opened receives it.
const TopicScreen = "device.screen"

// StopDetached joins StopDeviceGone, StopExited and StopFailed as a reason a stream ended
// (ADR-0078 §2): the stream socket closed, or no socket attached in time.
const StopDetached = "detached"

// screenAttachBound is how long a stream id waits for its socket (ADR-0078 §3); a variable
// so a test is quick.
var screenAttachBound = 10 * time.Second

// What AttachScreen refuses with. The HTTP adapter's close codes 4404 and 4409 follow them.
var (
	// ErrScreenStreamUnknown is an id that was never issued, or whose stream has ended.
	ErrScreenStreamUnknown = errors.New("device: no such screen stream")
	// ErrScreenStreamAttached is an id whose socket is already attached.
	ErrScreenStreamAttached = errors.New("device: the screen stream is already attached")
)

// WatchScreenParams is device.watchScreen's parameters.
type WatchScreenParams struct {
	Device string `json:"device" jsonschema:"required" jsonschema_description:"The serial of the device, from device.list."`
}

// WatchScreenResult is device.watchScreen's result.
type WatchScreenResult struct {
	SubscriptionID string `json:"subscriptionId" jsonschema:"required" jsonschema_description:"The subscription the stream's events arrive on, over the WebSocket session that made the call. Ending it (events.unsubscribe) ends the stream."`
	Device         string `json:"device" jsonschema:"required"`
	StreamID       string `json:"streamId" jsonschema:"required" jsonschema_description:"The id to send, with the daemon's token, as the first frame of GET /v1/screen within 10 s. It works once, for as long as the subscription lives."`
	Width          int    `json:"width" jsonschema:"required" jsonschema_description:"The frame width in pixels when the stream started; a rotation announces a new size in the stream itself."`
	Height         int    `json:"height" jsonschema:"required" jsonschema_description:"The frame height in pixels when the stream started."`
}

// ScreenStoppedEvent is the payload of a stopped event on topic device.screen. After it
// the subscription stays open and empty until events.unsubscribe or the session's end.
type ScreenStoppedEvent struct {
	Device string `json:"device" jsonschema:"required"`
	Reason string `json:"reason" jsonschema:"required,enum=deviceGone,enum=exited,enum=failed,enum=detached" jsonschema_description:"deviceGone: the stream ended and device.list no longer holds the serial online (absent, or offline as an emulator shutting down is). exited: the server on the device ended by itself, or the daemon is stopping. failed: the video stream broke the protocol. detached: the stream socket closed, or none attached within 10 s."`
	Cause  string `json:"cause,omitempty" jsonschema_description:"The daemon's words for why, never device text."`
}

// EventState is the type of a state event on topic device.screen (ADR-0080 §3).
const EventState = "state"

// ScreenStateEvent is the payload of a state event on topic device.screen: whether the
// focused window is secure (FLAG_SECURE), which Android shows black. It is published once
// the first check has answered and on each change; a failed check publishes nothing.
type ScreenStateEvent struct {
	Device string `json:"device" jsonschema:"required"`
	Secure bool   `json:"secure" jsonschema:"required" jsonschema_description:"True while the focused window has FLAG_SECURE: the screen is shown black (the platform's rule), and a screenshot of it is black or fails."`
}

func (p WatchScreenParams) validate() error {
	if p.Device == "" {
		return capability.Errorf(capability.ErrInvalidParams, "device is required: a serial from device.list")
	}
	return nil
}

func registerWatchScreen(reg *capability.Registry, adb *ADB) {
	notDestructive := false
	reg.MustRegister(capability.Descriptor{
		Name:         "device.watchScreen",
		Kind:         capability.Command,
		Scope:        capability.Global,
		Statefulness: capability.Streaming,
		Since:        "0.27.0",
		Availability: []capability.Requirement{capability.RequiresADB},
		Description:  "Starts the live view of a device for the calling WebSocket session: H.264 video on GET /v1/screen (its first frame is {\"auth\": <token>, \"stream\": <streamId>}) and the stream's end as a stopped event on topic device.screen. Every viewer of a device shares one screen server; end the stream with events.unsubscribe.",
		Params:       WatchScreenParams{},
		Target:       func(p any) *capability.OpTarget { return deviceTarget(p.(WatchScreenParams).Device) },
		Result:       WatchScreenResult{},
		Events: &capability.Events{
			Topic: TopicScreen,
			Types: map[string]any{EventState: ScreenStateEvent{}, EventStopped: ScreenStoppedEvent{}},
		},
		Errors: append(append([]capability.ErrorCode{}, PreambleErrors...), ErrStreamFailed, capability.ErrInvalidParams, capability.ErrSessionRequired),
		Annotations: capability.Annotations{
			Destructive: &notDestructive,
			Reason:      "adds a subscription to the calling session and starts a read-only server on the device; changes nothing the device keeps",
		},
		// The stream id is a capability (ADR-0078 §3): the caller's own response carries it,
		// but the retained operation must not, or any session that learns the operation id
		// (operation.state shows it to every watcher) could read the id with operation.wait.
		Slim: func(result any) any {
			if r, ok := result.(WatchScreenResult); ok {
				r.StreamID = ""
				return r
			}
			return result
		},
		Authorize: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(WatchScreenParams)
			// Before the hook, so a malformed parameter runs no adb command at all.
			if err := p.validate(); err != nil {
				return nil, err
			}
			return adb.authorize(ctx, p.Device, "device.watchScreen")
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return adb.watchScreen(ctx, call)
		},
	})
}

// screenStreams is the daemon-wide table of live-view streams (ADR-0078 §3): each maps an
// id to the viewer of the call that made it. The zero value is ready.
type screenStreams struct {
	mu       sync.Mutex
	stopping bool
	byID     map[string]*screenStream
	all      map[*screenStream]struct{} // until released, so StopAll can wait for it
}

// screenStream is one device.watchScreen call's stream. It ends once, whichever of the
// subscription, the socket, the unattached timer, the source or the daemon gets there
// first, and then releases its viewer exactly once.
type screenStream struct {
	set       *screenStreams
	bus       *event.Bus
	id        string
	device    string
	operation string
	src       *screenSource
	viewer    screen.Viewer
	release   func() // the source reference, idempotent

	sub *event.Subscription // set before the stream is in the set

	mu         sync.Mutex
	attached   bool
	ended      bool
	timer      *time.Timer
	stopSecure func()        // ends the secure-state watch (secure.go); set once the stream is in the table
	quit       chan struct{} // closed when the stream ended
	released   chan struct{} // closed when the viewer's source reference was dropped
}

// end ends the stream: the id becomes unknown, the viewer is closed, stopped is published
// when reason is not empty, and the source reference is dropped on a goroutine of its own
// (the last one waits for the server's teardown, which a subscription's end hook must not).
// Only the first end counts. An end by the subscription passes no reason: its subscriber is
// gone, so there is nobody to tell.
func (s *screenStream) end(reason, cause string) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	if s.timer != nil {
		s.timer.Stop()
	}
	close(s.quit)
	stopSecure := s.stopSecure
	s.mu.Unlock()

	// Before stopped: when it returns no state event is in flight, so none follows the end.
	if stopSecure != nil {
		stopSecure()
	}
	s.set.forget(s)
	s.viewer.Close()
	if reason != "" {
		s.bus.PublishTo(s.sub, TopicScreen, EventStopped, "", s.operation,
			ScreenStoppedEvent{Device: s.device, Reason: reason, Cause: cause})
	}
	go func() {
		defer close(s.released)
		defer s.set.remove(s)
		s.release()
	}()
}

// secureChanged publishes the focused window's secure state to this stream's subscription.
// It runs under the source's secure lock, which end waits for through stopSecure.
func (s *screenStream) secureChanged(secure bool) {
	s.mu.Lock()
	ended := s.ended
	s.mu.Unlock()
	if ended {
		return
	}
	s.bus.PublishTo(s.sub, TopicScreen, EventState, "", s.operation,
		ScreenStateEvent{Device: s.device, Secure: secure})
}

// watch ends the stream with the source's reason when the source ends first.
func (s *screenStream) watch() {
	select {
	case <-s.src.Done():
	case <-s.quit:
		return
	}
	reason, cause := StopExited, "the screen stream ended"
	var end *ScreenEnd
	if errors.As(s.src.Reason(), &end) {
		reason, cause = end.Reason, end.Cause
	}
	s.end(reason, cause)
}

// unattached is the 10 s timer: a stream nobody attached to is released.
func (s *screenStream) unattached() {
	s.mu.Lock()
	attached := s.attached
	s.mu.Unlock()
	if !attached {
		s.end(StopDetached, "no stream socket attached within "+screenAttachBound.String())
	}
}

// forget makes the id unknown.
func (ss *screenStreams) forget(s *screenStream) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.byID[s.id] == s {
		delete(ss.byID, s.id)
	}
}

// remove takes s out of the set: the last thing a stream does, once its source reference
// is dropped.
func (ss *screenStreams) remove(s *screenStream) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	delete(ss.all, s)
}

var (
	errScreenStreamsStopping = errors.New("the daemon is stopping")
	errScreenStreamEnded     = errors.New("the stream ended before it was in the table")
)

// add puts s in the table, refusing once StopAll has begun, and a stream that already
// ended (its subscription's session closed between Open and here): it would never leave.
func (ss *screenStreams) add(s *screenStream) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.stopping {
		return errScreenStreamsStopping
	}
	s.mu.Lock()
	ended := s.ended
	s.mu.Unlock()
	if ended {
		return errScreenStreamEnded
	}
	if ss.byID == nil {
		ss.byID, ss.all = map[string]*screenStream{}, map[*screenStream]struct{}{}
	}
	ss.byID[s.id], ss.all[s] = s, struct{}{}
	return nil
}

// Live is how many streams are in the table, one being released included: zero once every
// viewer's source reference is dropped. Test-only in practice.
func (ss *screenStreams) Live() int {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return len(ss.all)
}

// StopAll ends every stream and waits until each has dropped its source reference, so the
// sources' own StopAll that follows finds them held by nobody. New streams are refused
// from here on. It returns early only when ctx ends.
func (ss *screenStreams) StopAll(ctx context.Context) {
	ss.mu.Lock()
	ss.stopping = true
	list := make([]*screenStream, 0, len(ss.all))
	for s := range ss.all {
		list = append(list, s)
	}
	ss.mu.Unlock()
	for _, s := range list {
		s.end(StopExited, "the daemon is stopping")
	}
	for _, s := range list {
		select {
		case <-s.released:
		case <-ctx.Done():
			return
		}
	}
}

// AttachScreen is the only thing of the live view the HTTP adapter holds (ADR-0078 §4): it
// claims the stream id's viewer for one socket, once. The viewer reads packets and closes;
// closing it ends the stream as detached. Nothing it offers reaches the device.
func (a *ADB) AttachScreen(id string) (screen.Viewer, error) {
	ss := &a.screenViews
	ss.mu.Lock()
	s := ss.byID[id]
	ss.mu.Unlock()
	if s == nil {
		return nil, ErrScreenStreamUnknown
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return nil, ErrScreenStreamUnknown
	}
	if s.attached {
		return nil, ErrScreenStreamAttached
	}
	s.attached = true
	if s.timer != nil {
		s.timer.Stop()
	}
	return &screenHandle{s}, nil
}

// LiveScreenStreams is how many live-view streams are held. Test-only in practice.
func (a *ADB) LiveScreenStreams() int { return a.screenViews.Live() }

// LiveScreenSources is how many live-view sources are running. Test-only in practice.
func (a *ADB) LiveScreenSources() int { return a.screens.Live() }

// screenHandle is the viewer AttachScreen hands out. It is a screen.Viewer and nothing
// more: no Inject, no source.
type screenHandle struct{ s *screenStream }

func (h *screenHandle) Next(ctx context.Context) (screen.Packet, error) { return h.s.viewer.Next(ctx) }

// Close is the stream socket closing.
func (h *screenHandle) Close() { h.s.end(StopDetached, "") }

// newStreamID is 128 random bits in base64url.
func newStreamID() string {
	var b [16]byte
	rand.Read(b[:]) // never returns an error (crypto/rand, Go 1.24)
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// watchScreen serves device.watchScreen: it joins the device's source, opens the session's
// subscription with its end hook, and returns once the stream is in the table.
func (a *ADB) watchScreen(ctx context.Context, call *capability.Call) (any, error) {
	p := call.Params.(WatchScreenParams)
	if err := p.validate(); err != nil {
		return nil, err
	}
	sess, err := event.SessionOf(call)
	if err != nil {
		return nil, err
	}
	if a.bus == nil {
		return nil, capability.NewError(capability.ErrSessionRequired, "device.watchScreen needs the daemon's event bus").
			WithNextStep("watch over the WebSocket session, GET /v1/ws")
	}
	t := call.Authorized.(*target)
	if err := a.ready(ctx, t); err != nil {
		return nil, err
	}
	src, release, err := a.screens.acquire(ctx, a, t)
	if err != nil {
		return nil, err
	}

	s := &screenStream{
		set: &a.screenViews, bus: a.bus, id: newStreamID(), device: t.serial, operation: call.OperationID,
		src: src, viewer: src.Join(), release: release,
		quit: make(chan struct{}), released: make(chan struct{}),
	}
	subID, sub, err := sess.Open([]string{TopicScreen}, func() { s.end("", "") })
	if err != nil {
		s.viewer.Close()
		release()
		return nil, err
	}
	s.sub = sub
	if err := a.screenViews.add(s); err != nil {
		sess.Unsubscribe(subID) // the hook ends the stream, which drops the reference
		if errors.Is(err, errScreenStreamEnded) {
			return nil, capability.Errorf(capability.ErrSessionRequired, "the session has ended")
		}
		return nil, capability.NewError(ErrStreamFailed, "the device screen could not be streamed").
			WithCause(err.Error()).
			WithNextStep("Start the daemon again, then retry.")
	}
	stop := src.watchSecure(s.secureChanged)
	s.mu.Lock()
	s.stopSecure = stop
	ended := s.ended
	if !ended {
		s.timer = time.AfterFunc(screenAttachBound, s.unattached)
	}
	s.mu.Unlock()
	if ended { // ended between the table and the watch: nothing else will stop it
		stop()
	}
	go s.watch()

	// A cancel that landed while the stream started ends it: nothing outlives a cancelled call.
	if ctx.Err() != nil {
		sess.Unsubscribe(subID)
		return nil, ctx.Err()
	}
	size := src.Size()
	return WatchScreenResult{SubscriptionID: subID, Device: t.serial, StreamID: s.id, Width: size.Width, Height: size.Height}, nil
}
