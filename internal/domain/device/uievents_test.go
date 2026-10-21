package device

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/hosting"
)

// device.uiEvents over the fake reader (ADR-0111; plan m2w step 6). The ring itself is Java and
// is exercised at level 5; these tests pin what the daemon does with it: the cursor, the gaps,
// the wait and the recovery of a reader that does not answer.

// fakeInstance is the instance the fake reader answers until a test changes it.
const fakeInstance = "0123456789abcdef"

// eventsCall runs device.uiEvents through the scheduler and waits.
func (f *uiFixture) eventsCall(t *testing.T, params string) capability.Outcome {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: "device.uiEvents", Params: []byte(params)})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return f.wait(t, id)
}

func mustEvents(t *testing.T, out capability.Outcome) UIEventsResult {
	t.Helper()
	if out.State != capability.StateCompleted {
		t.Fatalf("operation %s with error %+v", out.State, out.Error)
	}
	r, ok := out.Result.(UIEventsResult)
	if !ok {
		t.Fatalf("result is %T, want UIEventsResult", out.Result)
	}
	return r
}

// eventsDirect runs the call without the scheduler, for the tests that time it or cancel it.
func (f *uiFixture) eventsDirect(t *testing.T, ctx context.Context, p UIEventsParams) (UIEventsResult, error) {
	q, err := p.query()
	if err != nil {
		return UIEventsResult{}, err
	}
	return f.adb.uiEvents(ctx, readerTarget(t, f.adb, uiSerial), uiSerial, q)
}

// primedEvents is a fixture whose reader is running (a first call started it) and the cursor
// that call returned, so a test starts from a known ring end with no gap left to report.
func primedEvents(t *testing.T) (*uiFixture, *devicetest.FakeReader, string) {
	t.Helper()
	f, r := actingReader(t)
	res := mustEvents(t, f.eventsCall(t, `{"device":"`+uiSerial+`"}`))
	return f, r, res.Cursor
}

// clickEvents are n click events, each with its own window id from first on, so a test reads
// which ones it got.
func clickEvents(first, n int) []devicetest.FakeEvent {
	out := make([]devicetest.FakeEvent, n)
	for i := range out {
		out[i] = devicetest.FakeEvent{TimeMs: 1760000000000 + int64(first+i)*10, Type: "click", Window: first + i, Package: "com.example", Class: "android.widget.Button"}
	}
	return out
}

// windowsOf are the window ids of a result's events: in these tests, the events' identity.
func windowsOf(res UIEventsResult) []int {
	out := make([]int, len(res.Events))
	for i, e := range res.Events {
		out[i] = *e.Window
	}
	return out
}

func nowEvent(typ string) devicetest.FakeEvent {
	return devicetest.FakeEvent{TimeMs: time.Now().UnixMilli(), Type: typ, Window: 5, Package: "com.example", Class: "android.widget.ListView"}
}

// shortRecovery makes the reader's ping check and the recovery's lock wait short.
func shortRecovery(t *testing.T) {
	t.Helper()
	ping, lock := readerPingCheck, eventsLockWait
	readerPingCheck, eventsLockWait = 200*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { readerPingCheck, eventsLockWait = ping, lock })
}

// Decision (ADR-0111 Decision 5 and 6; plan m2w A3): across calls the cursor neither repeats
// nor skips an event. 120 events are read in three calls of at most 50, then a fourth finds
// nothing and the cursor stays, then three more events continue exactly where it stopped; no
// call has a gap. Seen failing with the cursor one event short (the first event of each page
// came back twice) and with it one past (the first of each page was skipped).
func TestUIEventsCursorNeitherRepeatsNorSkips(t *testing.T) {
	f, r, cur := primedEvents(t)
	r.AddEvents(clickEvents(1, 120)...)
	var got []int
	for i, want := range []struct {
		n    int
		more bool
	}{{50, true}, {50, true}, {20, false}, {0, false}} {
		res := mustEvents(t, f.eventsCall(t, `{"device":"`+uiSerial+`","since":"`+cur+`"}`))
		if len(res.Events) != want.n || res.More != want.more || res.Gap != nil {
			t.Fatalf("call %d: %d events, more %v, gap %+v; want %d, %v, none", i+1, len(res.Events), res.More, res.Gap, want.n, want.more)
		}
		got = append(got, windowsOf(res)...)
		cur = res.Cursor
	}
	if cur != fakeInstance+".120" {
		t.Errorf("the cursor after the last page is %q, want %s.120", cur, fakeInstance)
	}
	r.AddEvents(clickEvents(121, 3)...)
	res := mustEvents(t, f.eventsCall(t, `{"device":"`+uiSerial+`","since":"`+cur+`"}`))
	got = append(got, windowsOf(res)...)
	want := make([]int, 123)
	for i := range want {
		want[i] = i + 1
	}
	if !slices.Equal(got, want) {
		t.Errorf("the events read over five calls are %v, want 1 to 123 once each", got)
	}
}

// Decision (ADR-0111 Decision 6, max): more than max pending gives the oldest max, `more`, and
// the cursor after the last one returned; asking again continues from it. Seen failing with
// the cursor at the ring's end (the second call then found nothing: the 20 others were lost).
func TestUIEventsMoreBeyondMax(t *testing.T) {
	f, r, cur := primedEvents(t)
	r.AddEvents(clickEvents(1, 30)...)
	res := mustEvents(t, f.eventsCall(t, `{"device":"`+uiSerial+`","since":"`+cur+`","max":10}`))
	if !slices.Equal(windowsOf(res), []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}) || !res.More || res.Cursor != fakeInstance+".10" {
		t.Fatalf("first page %v more %v cursor %q", windowsOf(res), res.More, res.Cursor)
	}
	res = mustEvents(t, f.eventsCall(t, `{"device":"`+uiSerial+`","since":"`+res.Cursor+`","max":100}`))
	if len(res.Events) != 20 || res.More || *res.Events[0].Window != 11 {
		t.Errorf("second page: %d events from %d, more %v; want the other 20 from 11", len(res.Events), *res.Events[0].Window, res.More)
	}
}

// Decision (ADR-0111 Decision 6): a cursor of another instance than the reader's is a
// readerRestarted gap, and the new ring is read from its first entry, not from the old
// cursor's number (the old cursor was 5; the new reader's three events are numbered 1 to 3).
// Seen failing with the instance ignored: the call failed REQUEST_INVALID_PARAMS (the old
// cursor 5 is ahead of a ring of three) instead of reporting the gap.
func TestUIEventsGapOnAnotherInstance(t *testing.T) {
	f, r, cur := primedEvents(t)
	r.AddEvents(clickEvents(1, 5)...)
	res := mustEvents(t, f.eventsCall(t, `{"device":"`+uiSerial+`","since":"`+cur+`"}`))
	if res.Cursor != fakeInstance+".5" {
		t.Fatalf("cursor %q", res.Cursor)
	}
	r.RestartEvents("fedcba9876543210")
	r.AddEvents(clickEvents(101, 3)...)
	res2 := mustEvents(t, f.eventsCall(t, `{"device":"`+uiSerial+`","since":"`+res.Cursor+`"}`))
	if res2.Gap == nil || res2.Gap.Reason != "readerRestarted" || res2.Gap.Dropped != 0 {
		t.Errorf("gap %+v, want readerRestarted", res2.Gap)
	}
	if !slices.Equal(windowsOf(res2), []int{101, 102, 103}) || res2.Cursor != "fedcba9876543210.3" {
		t.Errorf("events %v cursor %q; want the new reader's three from its first, cursor on its instance", windowsOf(res2), res2.Cursor)
	}
	// The next call from that cursor has no gap.
	if res3 := mustEvents(t, f.eventsCall(t, `{"device":"`+uiSerial+`","since":"`+res2.Cursor+`"}`)); res3.Gap != nil || len(res3.Events) != 0 {
		t.Errorf("the call after the gap: %+v", res3)
	}
}

// Decision (ADR-0111 Decision 6): a cursor older than the ring is an overflow gap that counts
// the events lost between the cursor and the oldest one kept, and returns from the oldest.
// The cursor was 10, the ring then dropped to 61: 50 events (11 to 60) were lost. Seen
// failing with dropped counted from the ring's start (60) rather than from the cursor.
func TestUIEventsOverflowCountsTheDropped(t *testing.T) {
	f, r, cur := primedEvents(t)
	r.AddEvents(clickEvents(1, 100)...)
	res := mustEvents(t, f.eventsCall(t, `{"device":"`+uiSerial+`","since":"`+cur+`","max":10}`))
	if res.Cursor != fakeInstance+".10" {
		t.Fatalf("cursor %q", res.Cursor)
	}
	r.DropEventsBefore(61)
	res = mustEvents(t, f.eventsCall(t, `{"device":"`+uiSerial+`","since":"`+res.Cursor+`"}`))
	if res.Gap == nil || res.Gap.Reason != "overflow" || res.Gap.Dropped != 50 {
		t.Fatalf("gap %+v, want overflow with 50 dropped", res.Gap)
	}
	if len(res.Events) != 40 || *res.Events[0].Window != 61 || res.More {
		t.Errorf("%d events from %d, more %v; want the 40 kept, from 61", len(res.Events), *res.Events[0].Window, res.More)
	}
}

// Decision (ADR-0111 Decision 6): a call that starts the reader (the daemon had none for the
// device) reports readerStarted, with a cursor or without; the next call does not. Seen
// failing with the gap never set for a start (the first call then had none).
func TestUIEventsGapWhenThisCallStartsTheReader(t *testing.T) {
	for _, since := range []string{"", "aaaaaaaaaaaaaaaa.7"} {
		f, r := actingReader(t)
		r.AddEvents(clickEvents(1, 2)...)
		params := `{"device":"` + uiSerial + `"`
		if since != "" {
			params += `,"since":"` + since + `"`
		}
		res := mustEvents(t, f.eventsCall(t, params+"}"))
		if res.Gap == nil || res.Gap.Reason != "readerStarted" {
			t.Errorf("since %q: gap %+v, want readerStarted", since, res.Gap)
		}
		if n := countServeStarts(f.calls(t)); n != 1 {
			t.Errorf("since %q: %d serve starts, want 1", since, n)
		}
		again := mustEvents(t, f.eventsCall(t, `{"device":"`+uiSerial+`"}`))
		if again.Gap != nil {
			t.Errorf("since %q: the second call has gap %+v", since, again.Gap)
		}
	}
}

// Decision (ADR-0111 Decision 5): without since the newest max events come back, oldest first,
// with the cursor after them and no `more`. Seen failing with the oldest max returned (events
// 1 to 10).
func TestUIEventsWithoutSinceIsTheNewest(t *testing.T) {
	f, r, _ := primedEvents(t)
	r.AddEvents(clickEvents(1, 120)...)
	res := mustEvents(t, f.eventsCall(t, `{"device":"`+uiSerial+`","max":10}`))
	if !slices.Equal(windowsOf(res), []int{111, 112, 113, 114, 115, 116, 117, 118, 119, 120}) || res.More || res.Cursor != fakeInstance+".120" || res.Gap != nil {
		t.Errorf("events %v more %v cursor %q gap %+v", windowsOf(res), res.More, res.Cursor, res.Gap)
	}
}

// Decision (plan m2w step 6: the result's shape): a window id is any int32 and absent only for
// the reader's -1; a count shows only above 1; times are RFC 3339 UTC to the millisecond of
// the device clock; a toast keeps its text and a notification has none; the outcome is marked
// untrusted for the package, class and text. Seen failing with window 0 taken for "none" (the
// window of a notification event, which is 0, was dropped).
func TestUIEventsResultShape(t *testing.T) {
	f, r, cur := primedEvents(t)
	r.AddEvents(
		devicetest.FakeEvent{TimeMs: 1760000000123, Type: "toast", Window: -1, Package: "com.example", Class: "android.widget.Toast", Text: "Saved ñ ✓"},
		devicetest.FakeEvent{TimeMs: 1760000000200, Type: "windows", Window: -3, Changes: 3},
		devicetest.FakeEvent{TimeMs: 1760000000300, Type: "notification", Window: 0, Package: "com.example", Class: "android.app.Notification"},
		devicetest.FakeEvent{TimeMs: 1760000000400, Type: "scroll", Count: 12, Window: 7, Package: "com.example"},
	)
	out := f.eventsCall(t, `{"device":"`+uiSerial+`","since":"`+cur+`"}`)
	res := mustEvents(t, out)
	if !out.Untrusted {
		t.Error("the outcome is not marked untrusted although events carry a package, a class and a toast's text")
	}
	if len(res.Events) != 4 {
		t.Fatalf("%d events", len(res.Events))
	}
	toast, windows, notification, scroll := res.Events[0], res.Events[1], res.Events[2], res.Events[3]
	if toast.Time != "2025-10-09T08:53:20.123Z" || toast.Text != "Saved ñ ✓" || toast.Window != nil || toast.Count != 0 {
		t.Errorf("toast %+v", toast)
	}
	if windows.Window == nil || *windows.Window != -3 || windows.Changes != 3 || windows.Package != "" {
		t.Errorf("windows %+v", windows)
	}
	if notification.Window == nil || *notification.Window != 0 || notification.Text != "" {
		t.Errorf("notification %+v", notification)
	}
	if scroll.Count != 12 || scroll.Window == nil || *scroll.Window != 7 {
		t.Errorf("scroll %+v", scroll)
	}
}

// Decision (ADR-0111 Decision 7): `change` ends at the first event after the cursor, and the
// poll is every 100 ms. An event added 300 ms into a 3 s wait ends the call within 150 ms of
// itself (250 ms here: the 100 ms poll plus a loaded machine's margin), with the outcome
// changed and that event. Seen failing with the wait running to its end (3 s, outcome
// timeout).
func TestUIEventsWaitChange(t *testing.T) {
	f, r, cur := primedEvents(t)
	var addedAt atomic.Int64
	go func() {
		time.Sleep(300 * time.Millisecond)
		addedAt.Store(time.Now().UnixMilli())
		r.AddEvents(nowEvent("windowState"))
	}()
	res, err := f.eventsDirect(t, context.Background(), UIEventsParams{Device: uiSerial, Since: cur, WaitMs: 3000})
	if err != nil {
		t.Fatal(err)
	}
	if late := time.Now().UnixMilli() - addedAt.Load(); res.Outcome != "changed" || len(res.Events) != 1 || late > 250 {
		t.Errorf("outcome %q with %d events, returned %d ms after the event; want changed, 1, within 250", res.Outcome, len(res.Events), late)
	}
}

// Decision (ADR-0111 Decision 7: without a cursor `change` means after the call began): the
// events already in the ring are history, so a no-cursor wait on a quiet ring times out
// (and still returns the history), and an event that comes during it ends it as changed. Seen
// failing with the history counted as a change (changed at once).
func TestUIEventsWaitChangeWithoutSinceIgnoresHistory(t *testing.T) {
	f, r, _ := primedEvents(t)
	r.AddEvents(clickEvents(1, 5)...)
	start := time.Now()
	res, err := f.eventsDirect(t, context.Background(), UIEventsParams{Device: uiSerial, WaitMs: 400})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "timeout" || len(res.Events) != 5 || time.Since(start) < 350*time.Millisecond {
		t.Errorf("outcome %q with %d events after %v; want timeout with the 5 of history after the wait", res.Outcome, len(res.Events), time.Since(start))
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		r.AddEvents(nowEvent("click"))
	}()
	res, err = f.eventsDirect(t, context.Background(), UIEventsParams{Device: uiSerial, WaitMs: 3000})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "changed" || len(res.Events) != 6 || *res.Events[5].Window != 5 {
		t.Errorf("outcome %q with %d events; want changed with the history and the new event", res.Outcome, len(res.Events))
	}
}

// Decision (ADR-0111 Decision 7): `settle` ends once no event has come for quietMs, counted
// from the newest event, not from the first. Five scrolls 80 ms apart (the last about 320 ms
// in) with quietMs 400: the call returns settled with all five, and not before 400 ms after
// the last. Seen failing with settle ending at the first event (one event, settled early).
func TestUIEventsWaitSettle(t *testing.T) {
	f, r, cur := primedEvents(t)
	var lastAt atomic.Int64
	go func() {
		for i := 0; i < 5; i++ {
			lastAt.Store(time.Now().UnixMilli())
			r.AddEvents(nowEvent("scroll"))
			time.Sleep(80 * time.Millisecond)
		}
	}()
	res, err := f.eventsDirect(t, context.Background(), UIEventsParams{Device: uiSerial, Since: cur, WaitMs: 5000, Until: "settle", QuietMs: 400})
	if err != nil {
		t.Fatal(err)
	}
	if quiet := time.Now().UnixMilli() - lastAt.Load(); res.Outcome != "settled" || len(res.Events) != 5 || quiet < 400 {
		t.Errorf("outcome %q with %d events, %d ms after the last; want settled with 5, at least 400", res.Outcome, len(res.Events), quiet)
	}
}

// Decision (ADR-0111 Decision 7: the quiet is counted from the later of the newest event and
// the call's start): a screen with no event settles after quietMs, not at the wait's end
// (outcome settled, not timeout), and a screen whose only event is old does too, after
// quietMs from the call's start and not at once. Seen failing with settle requiring an event
// (the first case timed out at 3 s) and with the quiet counted from the old event alone (the
// second case settled before 250 ms).
func TestUIEventsSettleOnAQuietScreen(t *testing.T) {
	f, r, cur := primedEvents(t)
	start := time.Now()
	res, err := f.eventsDirect(t, context.Background(), UIEventsParams{Device: uiSerial, Since: cur, WaitMs: 3000, Until: "settle", QuietMs: 300})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); res.Outcome != "settled" || len(res.Events) != 0 || d < 250*time.Millisecond || d > 1500*time.Millisecond {
		t.Errorf("quiet screen: outcome %q after %v with %d events; want settled after about 300 ms", res.Outcome, d, len(res.Events))
	}
	r.AddEvents(devicetest.FakeEvent{TimeMs: time.Now().UnixMilli() - 10000, Type: "click", Window: 9, Package: "com.example"})
	start = time.Now()
	res, err = f.eventsDirect(t, context.Background(), UIEventsParams{Device: uiSerial, Since: cur, WaitMs: 3000, Until: "settle", QuietMs: 300})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); res.Outcome != "settled" || len(res.Events) != 1 || d < 250*time.Millisecond || d > 1500*time.Millisecond {
		t.Errorf("old event: outcome %q after %v with %d events; want settled with the event after about 300 ms", res.Outcome, d, len(res.Events))
	}
}

// Decision (ADR-0111 Decision 7): a wait that nothing ends is a timeout after waitMs, with the
// cursor where it was. Seen failing with the outcome left empty.
func TestUIEventsWaitTimeout(t *testing.T) {
	f, _, cur := primedEvents(t)
	start := time.Now()
	res, err := f.eventsDirect(t, context.Background(), UIEventsParams{Device: uiSerial, Since: cur, WaitMs: 400})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); res.Outcome != "timeout" || len(res.Events) != 0 || res.Cursor != cur || d < 380*time.Millisecond || d > 1500*time.Millisecond {
		t.Errorf("outcome %q after %v, cursor %q (was %q); want timeout after about 400 ms with the cursor kept", res.Outcome, d, res.Cursor, cur)
	}
	// Without waitMs there is no outcome at all.
	if res, err := f.eventsDirect(t, context.Background(), UIEventsParams{Device: uiSerial, Since: cur}); err != nil || res.Outcome != "" {
		t.Errorf("no wait: outcome %q, %v; want none", res.Outcome, err)
	}
}

// Decision (ADR-0111 Decision 7: a cancel ends the wait at once): cancelling a call asleep
// between polls, and one whose poll is in flight (a reader that takes seconds to answer),
// both return the context's error within a second, not a failure of the reader or the full
// wait. Seen failing with the wait loop not watching the context at all (the pause, the check
// before each poll and the exchange): the first case ran its 10 s, the second 3 s and then
// failed DEVICE_UI_DUMP_FAILED.
func TestUIEventsCancelEndsTheWait(t *testing.T) {
	f, r, cur := primedEvents(t)
	for _, name := range []string{"asleep between polls", "poll in flight"} {
		if name == "poll in flight" {
			r.SetDelay(3 * time.Second)
		}
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(300 * time.Millisecond); cancel() }()
		start := time.Now()
		_, err := f.eventsDirect(t, ctx, UIEventsParams{Device: uiSerial, Since: cur, WaitMs: 10000})
		if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
			t.Errorf("%s: %v after %v; want context.Canceled within a second", name, err, time.Since(start))
		}
		cancel()
	}
}

// Decision (ADR-0111 Decision 8: the reader that answers `ping` is never restarted, ADR-0034
// §1; one that does not is restarted once, and the result reports it): the reader ends its
// connection behind a live forward (an idle exit or a crash). Case 1 (it still answers ping):
// the poll goes on, the reader is not restarted (one serve start) and there is no gap. Case 2
// (ping unanswered, a new instance behind the forward): one restart, a second serve start,
// the lock free afterwards, and the gap is readerRestarted with the new reader's events from
// its first. Seen failing with the restart skipped (case 2 failed DEVICE_UI_DUMP_FAILED) and
// with a restart whenever a poll failed (case 1 had two serve starts).
func TestUIEventsDeadReaderBehindALiveForward(t *testing.T) {
	shortRecovery(t)
	t.Run("a reader that answers ping is not restarted", func(t *testing.T) {
		f, r, cur := primedEvents(t)
		r.AddEvents(clickEvents(1, 2)...)
		r.SetEventsGone(1)
		res := mustEvents(t, f.eventsCall(t, `{"device":"`+uiSerial+`","since":"`+cur+`"}`))
		if len(res.Events) != 2 || res.Gap != nil {
			t.Errorf("%d events, gap %+v; want the 2 events and no gap", len(res.Events), res.Gap)
		}
		if n := countServeStarts(f.calls(t)); n != 1 {
			t.Errorf("%d serve starts; a reader that answers ping must not be restarted", n)
		}
	})
	t.Run("a reader that does not is restarted once", func(t *testing.T) {
		f, r, cur := primedEvents(t)
		r.AddEvents(clickEvents(1, 2)...)
		mustEvents(t, f.eventsCall(t, `{"device":"`+uiSerial+`","since":"`+cur+`"}`))
		r.SetEventsGone(1)
		r.SetMutePings(2) // the recovery's ping and the restart's own check
		r.RestartEvents("fedcba9876543210")
		r.AddEvents(clickEvents(101, 2)...)
		res := mustEvents(t, f.eventsCall(t, `{"device":"`+uiSerial+`","since":"`+fakeInstance+`.2"}`))
		if res.Gap == nil || res.Gap.Reason != "readerRestarted" || !slices.Equal(windowsOf(res), []int{101, 102}) || res.Cursor != "fedcba9876543210.2" {
			t.Errorf("gap %+v events %v cursor %q; want readerRestarted with the new reader's two", res.Gap, windowsOf(res), res.Cursor)
		}
		if n := countServeStarts(f.calls(t)); n != 2 {
			t.Errorf("%d serve starts, want the first and one restart", n)
		}
		c := f.adb.reader(uiSerial)
		lctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if err := c.acquire(lctx); err != nil {
			t.Errorf("the read lock is still held after the call: %v", err)
		} else {
			c.release()
		}
	})
}

// Decision (ADR-0111 Decision 8: no read lock for a poll; the recovery waits for it at most
// 2 s): a reader that answers is polled while another read holds the lock, at once; a reader
// that does not, with the lock held for good, still ends the call by its budget (two lock
// waits), as DEVICE_UI_DUMP_FAILED whose cause says the reader was held. Seen failing with the
// poll taking the lock (the first case waited for the lock) and with the lock wait bounded by
// the call's context alone (the second case never ended).
func TestUIEventsLockWaitIsBounded(t *testing.T) {
	shortRecovery(t)
	f, r, cur := primedEvents(t)
	r.AddEvents(clickEvents(1, 2)...)
	c := f.adb.reader(uiSerial)
	if err := c.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.release()

	start := time.Now()
	res, err := f.eventsDirect(t, context.Background(), UIEventsParams{Device: uiSerial, Since: cur})
	if err != nil || len(res.Events) != 2 || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("a poll behind a held lock: %d events, %v, after %v; want the events at once", len(res.Events), err, time.Since(start))
	}

	r.SetEventsAnswer(devicetest.ReaderGone)
	start = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = f.eventsDirect(t, ctx, UIEventsParams{Device: uiSerial, Since: cur})
	ce := mustErrorCode(t, err, ErrUIDumpFailed)
	if d := time.Since(start); d > 3*time.Second || !strings.Contains(ce.Cause, "held the reader") {
		t.Errorf("a dead reader with the lock held: %v after %v, cause %q; want it to fail within the lock waits, naming the held reader", err, d, ce.Cause)
	}
}

// Decision (ADR-0114 Decision 1: a call keeps ADR-0111 Decision 8 unchanged; only the stream's
// poller and the recorder are patient): a device.uiEvents call whose reader does not answer,
// with the read lock held by another caller for good, uses both of its recoveries, each waiting
// the lock wait, and fails DEVICE_UI_DUMP_FAILED naming the held reader. It does not return at
// the first busy lock with a retry-able "busy" of its own. Seen failing with the call made
// patient (it returned the busy error raw, at once, not a DEVICE_UI_DUMP_FAILED).
func TestUIEventsCallKeepsTwoRecoveries(t *testing.T) {
	shortRecovery(t)
	f, r, cur := primedEvents(t)
	r.SetEventsAnswer(devicetest.ReaderGone)
	holdReader(t, f)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := f.eventsDirect(t, ctx, UIEventsParams{Device: uiSerial, Since: cur})
	ce := mustErrorCode(t, err, ErrUIDumpFailed)
	if errors.Is(err, errReaderBusy) {
		t.Errorf("the call returned the patient reach's busy error: %v", err)
	}
	if d := time.Since(start); d < 2*eventsLockWait-50*time.Millisecond || !strings.Contains(ce.Cause, "held the reader") {
		t.Errorf("failed after %v with cause %q; want two lock waits (%v) and the held reader named", d, ce.Cause, 2*eventsLockWait)
	}
}

// Decision (ADR-0015 §1: nothing the device sent is quoted in an error): a reader that answers
// the events request with a line of its own, or with a malformed answer, fails
// DEVICE_UI_DUMP_FAILED after the recoveries are used, and the cause is a fixed sentence with
// none of the device's text; the reader is not restarted for it (it answers ping). This holds
// the causes of events.go through the new caller; it was not seen failing, because the
// causes are built before this code sees an answer.
func TestUIEventsFailsWithAFixedCause(t *testing.T) {
	shortRecovery(t)
	for name, answer := range map[string]string{
		"a line of its own": "droidyard-reader: " + leak,
		"a malformed entry": eventsHeader(1, 2) + "1\t1760000000100\tclick\t1\t7\t0\t" + b64(leak) + "\t!\t\n",
	} {
		f, r, cur := primedEvents(t)
		r.SetEventsAnswer(answer)
		out := f.eventsCall(t, `{"device":"`+uiSerial+`","since":"`+cur+`"}`)
		e := mustFail(t, out, ErrUIDumpFailed)
		if strings.Contains(e.Message+e.Cause+e.NextStep, leak) || e.Cause == "" {
			t.Errorf("%s: error %+v; want a fixed cause without the device's text", name, e)
		}
		if n := countServeStarts(f.calls(t)); n != 1 {
			t.Errorf("%s: %d serve starts; a reader that answers ping is not restarted", name, n)
		}
	}
}

// Decision (ADR-0111 Decision 8: no read resource, so a wait never queues a read or an
// action): during a 3 s wait, a uiTree and a tapElement on the same device finish in under
// 500 ms each. Seen failing with Resource set to dumpKey (both queued behind the wait).
func TestUIEventsWaitDoesNotBlockReads(t *testing.T) {
	quickAct(t)
	doc := tappable(tapLike("Like"), tapLikes("Likes: 0"))
	files, r := withPersistentReaderR(t, nil, doc)
	f := newUIFixture(t, files)
	cur := mustEvents(t, f.eventsCall(t, `{"device":"`+uiSerial+`"}`)).Cursor

	done := make(chan capability.Outcome, 1)
	go func() {
		done <- f.eventsCall(t, `{"device":"`+uiSerial+`","since":"`+cur+`","waitMs":3000}`)
	}()
	time.Sleep(300 * time.Millisecond) // the wait is under way
	start := time.Now()
	mustComplete(t, f.tree(t, uiSerial))
	treeTook := time.Since(start)
	start = time.Now()
	mustTap(t, f.tapCall(t, `{"device":"`+uiSerial+`","target":{"id":"like"}}`))
	tapTook := time.Since(start)
	if treeTook > 500*time.Millisecond || tapTook > 500*time.Millisecond {
		t.Errorf("during a wait, uiTree took %v and tapElement %v; want each under 500 ms", treeTook, tapTook)
	}
	select {
	case out := <-done:
		t.Fatalf("the wait ended before the reads did: %+v", out)
	default:
	}
	r.AddEvents(nowEvent("click"))
	if res := mustEvents(t, <-done); res.Outcome != "changed" {
		t.Errorf("the wait ended with %q, want changed", res.Outcome)
	}
}

// Decision (ADR-0111 Decision 5: a malformed cursor is REQUEST_INVALID_PARAMS; ranges as the
// schema states): each of these is refused before the reader is asked anything or started.
// A cursor of the right shape ahead of the reader's newest event is refused too, once the
// reader has been asked (it cannot be a cursor this reader gave). Seen failing with the
// cursor's digits parsed leniently (a trailing space was accepted).
func TestUIEventsRefusesBadParameters(t *testing.T) {
	f, r := actingReader(t)
	for _, params := range []string{
		`"since":"garbage"`, `"since":"0123456789abcdef"`, `"since":"0123456789abcdef."`,
		`"since":"0123456789ABCDEF.1"`, `"since":"0123456789abcdef.-1"`, `"since":"0123456789abcdef.1 "`,
		`"since":"0123456789abcdef.99999999999999999999"`, `"since":"0123456789abcde.1"`,
		`"waitMs":20001`, `"waitMs":-1`, `"quietMs":99`, `"quietMs":5001`, `"max":201`, `"max":-1`, `"until":"later"`,
	} {
		mustFail(t, f.eventsCall(t, `{"device":"`+uiSerial+`",`+params+`}`), capability.ErrInvalidParams)
	}
	if len(r.EventsRequests()) != 0 || countServeStarts(f.calls(t)) != 0 {
		t.Errorf("the reader was asked %q and started %d times for refused parameters", r.EventsRequests(), countServeStarts(f.calls(t)))
	}
	mustFail(t, f.eventsCall(t, `{"device":"`+uiSerial+`","since":"`+fakeInstance+`.999"}`), capability.ErrInvalidParams)
}

// Decision (ADR-0111 Decision 9 and ADR-0015 §4): a physical device the policy does not admit is
// DEVICE_NOT_ALLOWED and nothing is asked of it. Seen failing with the authorization skipped
// (DEVICE_NOT_FOUND from the readiness check instead).
func TestUIEventsPhysicalNotAllowed(t *testing.T) {
	f := newDevicePolicyFixture(t, nil, "")
	out := f.run(t, "device.uiEvents", `{"device":"`+physSerial+`"}`)
	mustFail(t, out, ErrNotAllowed)
	for _, c := range devicetest.Calls(t, f.dir) {
		if strings.HasPrefix(c, "-s "+physSerial) {
			t.Errorf("a command reached the refused device: %q", c)
		}
	}
}

// Decision (ADR-0115 Decision 1): a remote device id is relayed to its host, as device.uiTree's
// is (the link is down: DEVICE_NOT_READY, unreachable), with no adb command here, and a
// malformed cursor is refused on this side before any host is asked. Seen failing with the
// descriptor not wrapped in adb.plainThen (the local DEVICE_NOT_FOUND), and with the
// parameters checked only by the local handler (a bad cursor on a remote id reached the host).
func TestUIEventsRemoteIDRelayed(t *testing.T) {
	f := newUIFixture(t, nil)
	f.adb.remotes = oneRemote{&hosting.Link{}}
	f.adb.policy = admitting{"c/X1"}
	if ev := mustFail(t, f.eventsCall(t, `{"device":"c/X1"}`), ErrNotReady); ev.Details["state"] != "unreachable" {
		t.Fatalf("uiEvents on the remote id: %+v; want it relayed (the link is down)", ev)
	}
	mustFail(t, f.eventsCall(t, `{"device":"c/X1","since":"not-a-cursor"}`), capability.ErrInvalidParams)
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("adb was asked %q for a remote device", calls)
	}
}

// Decision (ADR-0111 Decision 8: a poll takes no lock, and so no Resource key): the
// descriptor has none, so the scheduler queues nothing behind or in front of a wait. Two waits
// on one device run together. Seen failing with Resource set to dumpKey (the second wait
// queued behind the first: 1.2 s instead of 0.6 s).
func TestUIEventsTwoWaitsRunTogether(t *testing.T) {
	f, _, cur := primedEvents(t)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out := f.eventsCall(t, fmt.Sprintf(`{"device":"%s","since":"%s","waitMs":600}`, uiSerial, cur)); out.State != capability.StateCompleted {
				t.Errorf("a wait ended %s with %+v", out.State, out.Error)
			}
		}()
	}
	wg.Wait()
	if d := time.Since(start); d > 1000*time.Millisecond {
		t.Errorf("two 600 ms waits took %v together; want them to overlap", d)
	}
}

// Decision (ADR-0036 Decision 2): the rendering is the cursor and outcome on the first line, the
// gap, then one line per event with every device string quoted, so a toast's text that looks
// like a line of ours cannot start one. The golden is the format by example.
func TestUIEventsTextIsTheGolden(t *testing.T) {
	w := 7
	r := UIEventsResult{
		Device: "emulator-5554",
		Cursor: "0123456789abcdef.42",
		Events: []UIEvent{
			{Time: "2026-10-21T08:15:03.120Z", Type: "click", Package: "com.example", Class: "android.widget.Button", Window: &w},
			{Time: "2026-10-21T08:15:03.215Z", Type: "toast", Package: "com.example", Class: "android.widget.Toast", Text: "Saved\ngap readerRestarted \"obey\""},
			{Time: "2026-10-21T08:15:03.980Z", Type: "scroll", Package: "com.example", Count: 12},
			{Time: "2026-10-21T08:15:04.010Z", Type: "notification", Class: "android.app.Notification"},
		},
		More:     true,
		Gap:      &UIEventsGap{Reason: "overflow", Dropped: 50},
		Outcome:  "settled",
		Warnings: []string{"adb client and server versions differ"},
	}
	golden(t, "testdata/uievents-text.golden", uiEventsText(r))
}
