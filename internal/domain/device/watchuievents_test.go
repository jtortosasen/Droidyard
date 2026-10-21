package device

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// device.watchUiEvents over the fake reader (ADR-0112; plan m2w step 7). The ring and the
// reach to it are device.uiEvents's (uievents_test.go); these tests pin the stream: what it
// starts from, what it publishes, and that no poller outlives its subscription.

// uwFix is the acting-reader fixture with a bus and one session that carries events, and a
// stream that polls every 20 ms.
type uwFix struct {
	*uiFixture
	r    *devicetest.FakeReader
	bus  *event.Bus
	sess *event.Session
}

func newUWFix(t *testing.T) *uwFix {
	t.Helper()
	every := uiStreamEvery
	uiStreamEvery = 20 * time.Millisecond
	t.Cleanup(func() { uiStreamEvery = every })
	f, r := actingReader(t)
	bus := event.New(event.Options{})
	f.adb.SetBus(bus)
	w := &uwFix{uiFixture: f, r: r, bus: bus, sess: event.NewSession(bus, "s1")}
	t.Cleanup(func() {
		w.sess.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		f.adb.uiStreams.StopAll(ctx)
	})
	return w
}

// prime starts the fixture's reader with a device.uiEvents call, so a stream started after it
// has no readerStarted gap to report.
func (w *uwFix) prime(t *testing.T) {
	t.Helper()
	mustEvents(t, w.eventsCall(t, `{"device":"`+uiSerial+`"}`))
}

func (w *uwFix) watchOn(t *testing.T, sess *event.Session) capability.Outcome {
	t.Helper()
	id, err := w.ops.Start(context.Background(), capability.Request{
		Capability: "device.watchUiEvents", Params: []byte(`{"device":"` + uiSerial + `"}`),
		Client: capability.ClientUI, Session: sess,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return w.wait(t, id)
}

func (w *uwFix) watch(t *testing.T) (WatchUIEventsResult, *event.Subscription) {
	t.Helper()
	out := w.watchOn(t, w.sess)
	if out.State != capability.StateCompleted {
		t.Fatalf("device.watchUiEvents %s with error %+v", out.State, out.Error)
	}
	res := out.Result.(WatchUIEventsResult)
	sub, ok := w.sess.Subscription(res.SubscriptionID)
	if !ok {
		t.Fatalf("the session does not hold subscription %s", res.SubscriptionID)
	}
	return res, sub
}

// push waits for the subscription's next event and requires an events event.
func uiPush(t *testing.T, sub *event.Subscription) UIEventsPush {
	t.Helper()
	e := next(t, sub)
	if e.Topic != TopicUIEvents || e.Type != EventUIEvents {
		t.Fatalf("event %s/%s, want %s/%s", e.Topic, e.Type, TopicUIEvents, EventUIEvents)
	}
	var p UIEventsPush
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if !p.Untrusted || p.Device != uiSerial {
		t.Errorf("payload %+v: want untrusted true for %s", p, uiSerial)
	}
	return p
}

func uiStopped(t *testing.T, sub *event.Subscription) UIEventsStopped {
	t.Helper()
	e := next(t, sub)
	if e.Topic != TopicUIEvents || e.Type != EventStopped {
		t.Fatalf("event %s/%s, want %s/%s", e.Topic, e.Type, TopicUIEvents, EventStopped)
	}
	var se UIEventsStopped
	if err := json.Unmarshal(e.Payload, &se); err != nil {
		t.Fatal(err)
	}
	return se
}

// quiet requires that nothing arrives on sub for d.
func quiet(t *testing.T, sub *event.Subscription, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	if e, err := sub.Next(ctx); err == nil {
		t.Fatalf("an event arrived where none was due: %s/%s %s", e.Topic, e.Type, e.Payload)
	}
}

func windowsOfPush(p UIEventsPush) []int {
	out := make([]int, len(p.Events))
	for i, e := range p.Events {
		out[i] = *e.Window
	}
	return out
}

// Decision (ADR-0112 Decision 3: the poller asks after its own cursor and publishes only when
// there is something): a stream shows what happens after it started, not the ring's history,
// each event once and in order, in as many events as the polls found something, and nothing
// while the screen is quiet. Seen failing with the cursor taken as 0 (the three events from
// before the call arrived first) and with an empty events event published on every tick (the
// quiet check).
func TestWatchUIEventsPublishesWhatComesAfterTheStart(t *testing.T) {
	w := newUWFix(t)
	w.r.AddEvents(clickEvents(1, 3)...) // history: not the stream's
	w.prime(t)
	_, sub := w.watch(t)
	quiet(t, sub, 150*time.Millisecond)

	w.r.AddEvents(clickEvents(10, 2)...)
	if got := windowsOfPush(uiPush(t, sub)); !slices.Equal(got, []int{10, 11}) {
		t.Fatalf("first push %v, want the two events added after the start", got)
	}
	quiet(t, sub, 150*time.Millisecond)
	w.r.AddEvents(clickEvents(12, 1)...)
	p := uiPush(t, sub)
	if got := windowsOfPush(p); !slices.Equal(got, []int{12}) || p.Gap != nil {
		t.Errorf("second push %v gap %+v, want 12 only and no gap", got, p.Gap)
	}
}

// Decision (ADR-0111 Decision 6 for the stream): a gap is never silent. A reader that started
// while the stream was being made says readerStarted in the stream's first event, empty.
// Seen failing with the gap dropped from the first event.
func TestWatchUIEventsFirstEventSaysTheReaderWasStarted(t *testing.T) {
	w := newUWFix(t)
	_, sub := w.watch(t) // no earlier call: this one starts the reader
	p := uiPush(t, sub)
	if p.Gap == nil || p.Gap.Reason != gapStarted || len(p.Events) != 0 {
		t.Errorf("first push %+v, want an empty one with a readerStarted gap", p)
	}
}

// manualStream is a stream value whose step the test runs itself, so that the ring can be
// changed between two reads with no poller in between. It reads from base on instance inst.
func (w *uwFix) manualStream(t *testing.T, inst string, base uint64) (*uiEventStream, *event.Subscription) {
	t.Helper()
	id, _, err := w.sess.Open([]string{TopicUIEvents}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := w.sess.Subscription(id)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &uiEventStream{
		adb: w.adb, set: &w.adb.uiStreams, bus: w.bus, sub: sub, t: readerTarget(t, w.adb, uiSerial),
		ctx: ctx, cancel: cancel, done: make(chan struct{}), inst: inst, base: base,
	}, sub
}

// Decision (ADR-0111 Decision 6 for the stream): a reader that restarted behind the stream
// says readerRestarted, and the stream goes on with the new reader's events from its first,
// then without a gap; a ring that moved past the stream says overflow with the count of what
// it lost; more than a page waiting is read as several pages in the same tick. Driven step by
// step, so the ring changes between two reads. Seen failing with the instance check removed
// (the restarted reader's events were read after the old cursor and reported as an ordinary
// push) and with the overflow count at zero.
func TestWatchUIEventsGaps(t *testing.T) {
	t.Run("the reader restarted", func(t *testing.T) {
		w := newUWFix(t)
		w.r.AddEvents(clickEvents(1, 5)...)
		w.prime(t)
		s, sub := w.manualStream(t, fakeInstance, 5)
		w.r.RestartEvents("fedcba9876543210")
		w.r.AddEvents(clickEvents(20, 2)...)
		if reason, _ := s.step(); reason != "" {
			t.Fatalf("step stopped the stream: %s", reason)
		}
		p := uiPush(t, sub)
		if p.Gap == nil || p.Gap.Reason != gapRestarted || !slices.Equal(windowsOfPush(p), []int{20, 21}) {
			t.Fatalf("push %+v, want readerRestarted with the new reader's two events", p)
		}
		w.r.AddEvents(clickEvents(22, 1)...)
		s.step()
		if p := uiPush(t, sub); p.Gap != nil || !slices.Equal(windowsOfPush(p), []int{22}) {
			t.Errorf("push %+v, want the next event of the new reader and no gap", p)
		}
	})
	t.Run("the ring moved past the stream", func(t *testing.T) {
		w := newUWFix(t)
		w.r.AddEvents(clickEvents(1, 10)...)
		w.prime(t)
		s, sub := w.manualStream(t, fakeInstance, 3)
		w.r.DropEventsBefore(6) // entries 4 and 5 are gone before the stream read them
		s.step()
		p := uiPush(t, sub)
		if p.Gap == nil || p.Gap.Reason != gapOverflow || p.Gap.Dropped != 2 || !slices.Equal(windowsOfPush(p), []int{6, 7, 8, 9, 10}) {
			t.Errorf("push %+v, want overflow dropping 2 and the events from 6 to 10", p)
		}
	})
	t.Run("a storm is read in pages", func(t *testing.T) {
		w := newUWFix(t)
		w.prime(t)
		s, sub := w.manualStream(t, fakeInstance, 0)
		w.r.AddEvents(clickEvents(1, 250)...)
		s.step()
		if p := uiPush(t, sub); len(p.Events) != eventsMaxLimit {
			t.Fatalf("first page has %d events, want %d", len(p.Events), eventsMaxLimit)
		}
		if p := uiPush(t, sub); len(p.Events) != 50 || p.Gap != nil {
			t.Errorf("second page %d events gap %+v, want the other 50 in the same tick", len(p.Events), p.Gap)
		}
	})
}

// Decision (ADR-0112 Decision 3: the stream ends with its subscription; Rests on: no poller
// outlives its subscription): after events.unsubscribe the stream leaves the set and the
// reader receives no further `events` request. Seen failing with the end hook not stopping
// the poller (the requests kept coming).
func TestWatchUIEventsUnsubscribeEndsThePoller(t *testing.T) {
	w := newUWFix(t)
	w.prime(t)
	res, _ := w.watch(t)
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the poller to ask the reader", func() bool { return len(w.r.EventsRequests()) > 2 })
	w.sess.Unsubscribe(res.SubscriptionID)
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the stream to leave the set", func() bool { return w.adb.LiveUIEventStreams() == 0 })
	n := len(w.r.EventsRequests())
	time.Sleep(10 * uiStreamEvery)
	if m := len(w.r.EventsRequests()); m != n {
		t.Errorf("the reader got %d more events requests after the unsubscribe", m-n)
	}
}

// Decision (ADR-0112 Decision 1: one stream per session and device, a second replaces the
// first): the first stream's subscription gets stopped replaced and then nothing, the second
// delivers, and one stream is left in the set. A second session's stream is not replaced.
// Seen failing with the replacement not made (the first kept delivering).
func TestWatchUIEventsReplacesTheSessionsOwnStream(t *testing.T) {
	w := newUWFix(t)
	w.prime(t)
	_, first := w.watch(t)
	other := event.NewSession(w.bus, "s2")
	t.Cleanup(other.Close)
	out := w.watchOn(t, other)
	if out.State != capability.StateCompleted {
		t.Fatalf("the other session's stream: %s %+v", out.State, out.Error)
	}
	if n := w.adb.LiveUIEventStreams(); n != 2 {
		t.Fatalf("%d streams, want one for each session", n)
	}
	_, second := w.watch(t)
	if se := uiStopped(t, first); se.Reason != StopReplaced || se.Device != uiSerial {
		t.Errorf("stopped %+v, want replaced for %s", se, uiSerial)
	}
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the replaced stream to leave the set", func() bool { return w.adb.LiveUIEventStreams() == 2 })
	w.r.AddEvents(clickEvents(30, 1)...)
	if got := windowsOfPush(uiPush(t, second)); !slices.Equal(got, []int{30}) {
		t.Errorf("the second stream delivered %v, want 30", got)
	}
	quiet(t, first, 100*time.Millisecond)
}

// Decision (ADR-0111 Decision 8 for the stream: a reader that answers ping is not restarted):
// one `events` request that gets no answer from a reader that is alive is recovered by the
// reach, and the stream goes on without a stopped event or a serve start.
func TestWatchUIEventsRecoversAReaderThatAnswersPing(t *testing.T) {
	shortRecovery(t)
	w := newUWFix(t)
	w.prime(t)
	_, sub := w.watch(t)
	w.r.SetEventsGone(1)
	w.r.AddEvents(clickEvents(40, 1)...)
	if got := windowsOfPush(uiPush(t, sub)); !slices.Equal(got, []int{40}) {
		t.Fatalf("pushed %v, want 40 after the recovery", got)
	}
	if n := countServeStarts(w.calls(t)); n != 1 {
		t.Errorf("%d serve starts; a reader that answers ping must not be restarted", n)
	}
}

// Decision (ADR-0112 Decision 3: a restart that fails ends the stream, never a loop): a
// reader that is gone and cannot be started again gets exactly one start attempt, the
// stream publishes stopped failed with a cause, leaves the set, and the daemon runs no adb
// command afterwards. Seen failing with the poller retrying on every tick (the start commands
// kept coming).
func TestWatchUIEventsStopsFailedWhenTheReaderCannotStart(t *testing.T) {
	shortRecovery(t)
	w := newUWFix(t)
	w.prime(t)
	_, sub := w.watch(t)
	before := countServeStarts(w.calls(t))
	devicetest.Write(t, w.dir, uiSerial+".start.exit", []byte("1"))
	w.r.Close() // the reader is gone behind a live forward

	se := uiStopped(t, sub)
	if se.Reason != StopFailed || se.Cause == "" || se.Device != uiSerial {
		t.Fatalf("stopped %+v, want failed with a cause", se)
	}
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the stream to leave the set", func() bool { return w.adb.LiveUIEventStreams() == 0 })
	calls := len(w.calls(t))
	if n := countServeStarts(w.calls(t)) - before; n != 1 {
		t.Errorf("%d start attempts, want one", n)
	}
	time.Sleep(10 * uiStreamEvery)
	if n := len(w.calls(t)); n != calls {
		t.Errorf("%d adb commands after the stream stopped", n-calls)
	}
	quiet(t, sub, 100*time.Millisecond)
}

// Decision (ADR-0114, with ADR-0111 Decision 6): a tick that sees a new reader instance and
// then meets a held lock before it has read the new ring keeps its cursor on the old instance,
// so the next tick still reports readerRestarted. Seen failing with the cursor moved before the
// new ring was read (the next tick published the new reader's events with no gap).
func TestWatchUIEventsBusyTickKeepsTheRestartGap(t *testing.T) {
	shortRecovery(t)
	eventsLockWait = 5 * time.Millisecond
	w := newUWFix(t)
	w.r.AddEvents(clickEvents(1, 3)...)
	w.prime(t)
	s, sub := w.manualStream(t, fakeInstance, 3)
	w.r.RestartEvents("fedcba9876543210")
	w.r.AddEvents(clickEvents(20, 2)...)
	release := holdReader(t, w.uiFixture)
	w.r.SetEventsGoneAfter(1, 1) // the new instance answers, then the read of its ring fails
	if reason, cause := s.step(); reason != "" {
		t.Fatalf("a busy tick ended the stream: %s (%s)", reason, cause)
	}
	quiet(t, sub, 50*time.Millisecond)
	release()
	if reason, cause := s.step(); reason != "" {
		t.Fatalf("step: %s (%s)", reason, cause)
	}
	p := uiPush(t, sub)
	if p.Gap == nil || p.Gap.Reason != gapRestarted || !slices.Equal(windowsOfPush(p), []int{20, 21}) {
		t.Fatalf("push %+v, want readerRestarted with the new reader's two events", p)
	}
}

// holdReader takes the device's read lock, as a read chain or an act does, and returns the
// function that frees it (once; a second call does nothing).
func holdReader(t *testing.T, f *uiFixture) (release func()) {
	t.Helper()
	c := f.adb.reader(uiSerial)
	if err := c.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release = func() { once.Do(c.release) }
	t.Cleanup(release)
	return release
}

// Decision (ADR-0114 Decision 1: a patient reach treats a lock held by another caller as an
// empty tick, no recovery spent and no failure): the read lock is held across 50 ticks while the
// reader's `events` answers keep failing (a reader that died; the holder is the one recovering
// it). The stream publishes nothing, does not stop and starts nothing itself; when the holder
// has restarted the reader and frees the lock, the stream goes on with the new reader's events
// (readerRestarted, never lost silently) and then without a gap. Seen failing with the old
// rule (the poller as a device.uiEvents call: two lock waits, then `stopped failed` although
// nothing had failed).
func TestWatchUIEventsWaitsOutAHeldLock(t *testing.T) {
	shortRecovery(t)
	eventsLockWait = 5 * time.Millisecond
	w := newUWFix(t)
	w.prime(t)
	_, sub := w.watch(t)

	release := holdReader(t, w.uiFixture)
	w.r.SetEventsGone(1 << 30) // every events request ends with nothing written
	before := len(w.r.EventsRequests())
	testtime.Eventually(t, testtime.Bound(10*time.Second), "50 ticks behind the held lock", func() bool {
		if w.adb.LiveUIEventStreams() == 0 {
			se := uiStopped(t, sub)
			t.Fatalf("the stream ended behind a held lock: stopped %s (%s)", se.Reason, se.Cause)
		}
		return len(w.r.EventsRequests()) >= before+50
	})
	quiet(t, sub, 100*time.Millisecond)
	if n := w.adb.LiveUIEventStreams(); n != 1 {
		t.Fatalf("%d streams live behind a held lock, want the one", n)
	}
	if n := countServeStarts(w.calls(t)); n != 1 {
		t.Errorf("%d serve starts; the poller restarts only a reader whose lock it holds", n)
	}

	// The holder restarts the reader: a new instance with two events, then the lock is freed.
	w.r.RestartEvents("fedcba9876543210")
	w.r.AddEvents(clickEvents(50, 2)...)
	w.r.SetEventsGone(0)
	release()
	p := uiPush(t, sub)
	if p.Gap == nil || p.Gap.Reason != gapRestarted || !slices.Equal(windowsOfPush(p), []int{50, 51}) {
		t.Fatalf("push %+v, want readerRestarted with the new reader's two events", p)
	}
	w.r.AddEvents(clickEvents(52, 1)...)
	if p := uiPush(t, sub); p.Gap != nil || !slices.Equal(windowsOfPush(p), []int{52}) {
		t.Errorf("push %+v, want 52 only and no gap: the stream goes on", p)
	}
	if n := countServeStarts(w.calls(t)); n != 1 {
		t.Errorf("%d serve starts, want the first only", n)
	}
}

// Decision (ADR-0114 Decision 2: with the lock the poller still restarts a dead reader once and
// fails only when that restart fails): the reader is gone and cannot be started again, and the
// lock is held for 30 ticks. Nothing is published and no start is attempted while it is held (a
// held lock is not the failure); once the lock is free the stream makes exactly one attempt,
// publishes `stopped failed` with a cause, and asks no more. Seen failing with the old rule (the
// stream stopped while the lock was held, without a start attempt) and, for the single attempt,
// by the existing TestWatchUIEventsStopsFailedWhenTheReaderCannotStart.
func TestWatchUIEventsFailsOnlyWhenItsRestartFails(t *testing.T) {
	shortRecovery(t)
	eventsLockWait = 5 * time.Millisecond
	w := newUWFix(t)
	w.prime(t)
	_, sub := w.watch(t)
	before := countServeStarts(w.calls(t))

	release := holdReader(t, w.uiFixture)
	devicetest.Write(t, w.dir, uiSerial+".start.exit", []byte("1"))
	w.r.Close() // the reader is gone behind a live forward, and its restart fails
	quiet(t, sub, 30*uiStreamEvery)
	if n := countServeStarts(w.calls(t)) - before; n != 0 {
		t.Fatalf("%d start attempts while another caller held the lock, want none", n)
	}
	if n := w.adb.LiveUIEventStreams(); n != 1 {
		t.Fatalf("%d streams live behind a held lock, want the one", n)
	}

	release()
	se := uiStopped(t, sub)
	if se.Reason != StopFailed || se.Cause == "" {
		t.Fatalf("stopped %+v, want failed with a cause", se)
	}
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the stream to leave the set", func() bool { return w.adb.LiveUIEventStreams() == 0 })
	if n := countServeStarts(w.calls(t)) - before; n != 1 {
		t.Errorf("%d start attempts, want one", n)
	}
}

// Decision (ADR-0112 Decision 2: deviceGone): the same failure with the device no longer
// listed online is stopped deviceGone. Seen failing with the reason always failed.
func TestWatchUIEventsStopsDeviceGone(t *testing.T) {
	shortRecovery(t)
	w := newUWFix(t)
	w.prime(t)
	_, sub := w.watch(t)
	devicetest.Write(t, w.dir, "devices.out", []byte(listHead))
	devicetest.Write(t, w.dir, uiSerial+".start.exit", []byte("1"))
	w.r.Close()
	if se := uiStopped(t, sub); se.Reason != StopDeviceGone {
		t.Errorf("stopped %+v, want deviceGone", se)
	}
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the stream to leave the set", func() bool { return w.adb.LiveUIEventStreams() == 0 })
}

// Decision (ADR-0041 §6 for the stream; ADB.Close ends the streams): StopAll ends every
// stream and waits until each left the set, publishes nothing to a daemon that is stopping,
// and refuses a new stream. Seen failing with the stream missing from the set Close ends
// (it stayed live).
func TestWatchUIEventsStopAll(t *testing.T) {
	w := newUWFix(t)
	w.prime(t)
	_, sub := w.watch(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w.adb.Close(ctx)
	if n := w.adb.LiveUIEventStreams(); n != 0 {
		t.Errorf("%d streams are live after Close", n)
	}
	quiet(t, sub, 100*time.Millisecond)
	out := w.watchOn(t, w.sess)
	if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != ErrUIDumpFailed {
		t.Errorf("a stream after Close: %s %+v, want DEVICE_UI_DUMP_FAILED", out.State, out.Error)
	}
}

// startOn starts a call on a scheduler, with the session it is given, and waits.
func startOn(t *testing.T, f *uiFixture, sess *event.Session, params string) capability.Outcome {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "device.watchUiEvents", Params: []byte(params), Client: capability.ClientUI, Session: sess,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return f.wait(t, id)
}

// Decision (ADR-0112 Decision 1: streaming, so a session is needed; the descriptor declares
// SESSION_REQUIRED and REQUEST_INVALID_PARAMS): a call with no session is refused by the
// scheduler, one with an empty device runs no adb command, and a daemon with no event bus
// answers SESSION_REQUIRED before the reader is asked anything. Seen failing with the bus
// check removed (a nil-pointer panic in Open).
func TestWatchUIEventsRefusals(t *testing.T) {
	w := newUWFix(t)
	_, err := w.ops.Start(context.Background(), capability.Request{Capability: "device.watchUiEvents", Params: []byte(`{"device":"` + uiSerial + `"}`)})
	if ce := capability.AsError(err); ce == nil || ce.Code != capability.ErrSessionRequired {
		t.Fatalf("a call without a session: %v, want SESSION_REQUIRED", err)
	}

	before := len(w.calls(t))
	mustFail(t, startOn(t, w.uiFixture, w.sess, `{"device":""}`), capability.ErrInvalidParams)
	if n := len(w.calls(t)); n != before {
		t.Errorf("a malformed call ran %d adb commands", n-before)
	}

	// An ADB with no bus: the golden registry's.
	f, r := actingReader(t)
	mustFail(t, startOn(t, f, event.NewSession(event.New(event.Options{}), "s9"), `{"device":"`+uiSerial+`"}`), capability.ErrSessionRequired)
	if len(r.EventsRequests()) != 0 || countServeStarts(f.calls(t)) != 0 {
		t.Error("the reader was touched by a stream that cannot run")
	}
	if w.adb.LiveUIEventStreams() != 0 {
		t.Error("a refused call left a stream")
	}
}

// Decision (ADR-0015 §4; ADR-0115 Decision 1: lendable): a physical device the policy does not
// admit is DEVICE_NOT_ALLOWED with nothing asked of it, and a remote id is relayed to its host
// (the link is down: DEVICE_NOT_READY, unreachable) with no adb command here. Seen failing with
// the Authorize hook not trying the remote id first (the local DEVICE_NOT_FOUND).
func TestWatchUIEventsIsGatedAsUIEventsIs(t *testing.T) {
	sess := event.NewSession(event.New(event.Options{}), "s9")
	f := newDevicePolicyFixture(t, nil, "")
	id, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "device.watchUiEvents", Params: []byte(`{"device":"` + physSerial + `"}`), Client: capability.ClientUI, Session: sess,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	mustFail(t, out, ErrNotAllowed)
	for _, c := range devicetest.Calls(t, f.dir) {
		if strings.HasPrefix(c, "-s "+physSerial) {
			t.Errorf("a command reached the refused device: %q", c)
		}
	}

	g := newUIFixture(t, nil)
	g.adb.remotes = oneRemote{&hosting.Link{}}
	g.adb.policy = admitting{"c/X1"}
	g.adb.SetBus(event.New(event.Options{}))
	if got := mustFail(t, startOn(t, g, sess, `{"device":"c/X1"}`), ErrNotReady); got.Details["state"] != "unreachable" {
		t.Fatalf("watchUiEvents on the remote id: %+v; want it relayed (the link is down)", got)
	}
	if calls := g.calls(t); len(calls) != 0 {
		t.Errorf("adb was asked %q for a remote device", calls)
	}
}
