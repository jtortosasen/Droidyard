package device

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// device.uiEvents: the baseline of a wait without a cursor, the settle's view of events beyond
// the page, and the call's time bound (ADR-0111 Decision 2, 7 and 8; plan m2w step 6).

// burstInto adds n scroll events 100 ms apart to the fake reader after delay and reports when
// the last was added; done is released when the burst ends.
func burstInto(r *devicetest.FakeReader, delay time.Duration, n int) (lastAt *atomic.Int64, done *sync.WaitGroup) {
	lastAt, done = new(atomic.Int64), new(sync.WaitGroup)
	done.Add(1)
	go func() {
		defer done.Done()
		time.Sleep(delay)
		for i := 0; i < n; i++ {
			lastAt.Store(time.Now().UnixMilli())
			r.AddEvents(nowEvent("scroll"))
			time.Sleep(100 * time.Millisecond)
		}
	}()
	return lastAt, done
}

// Decision (ADR-0111 Decision 2 and 7): the reader folds a burst into the newest entry that no
// answer has returned, so a wait without a cursor must take its baseline from an entry an
// answer has returned (sealed); else the folds into the baseline entry are never seen. The
// fake folds as the reader does. The ring holds a click and, newest, a scroll; a scroll burst
// that began before the call continues for 0.5 s. `change` must end as changed, and `settle`
// must not settle while the burst goes on. Seen failing with the baseline taken from the answer
// to `events 0 1` alone (it seals only the oldest entry): change timed out after 3 s and
// settle ended 400 ms in, before the burst did.
func TestUIEventsWaitWithoutSinceSeesFoldsIntoTheBaseline(t *testing.T) {
	setup := func(t *testing.T) (*uiFixture, *devicetest.FakeReader) {
		f, r, _ := primedEvents(t)
		r.SetEventsFold(true)
		r.AddEvents(devicetest.FakeEvent{TimeMs: time.Now().UnixMilli(), Type: "click", Window: 3, Package: "com.example", Class: "android.widget.Button"}, nowEvent("scroll"))
		return f, r
	}
	t.Run("change", func(t *testing.T) {
		f, r := setup(t)
		_, done := burstInto(r, 150*time.Millisecond, 5)
		start := time.Now()
		res, err := f.eventsDirect(t, context.Background(), UIEventsParams{Device: uiSerial, WaitMs: 3000})
		done.Wait()
		if err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); res.Outcome != "changed" || d > 1500*time.Millisecond {
			t.Errorf("outcome %q after %v; want changed soon after the burst began", res.Outcome, d)
		}
	})
	t.Run("settle", func(t *testing.T) {
		f, r := setup(t)
		lastAt, done := burstInto(r, 150*time.Millisecond, 6)
		res, err := f.eventsDirect(t, context.Background(), UIEventsParams{Device: uiSerial, WaitMs: 5000, Until: "settle", QuietMs: 400})
		returned := time.Now().UnixMilli()
		done.Wait()
		if err != nil {
			t.Fatal(err)
		}
		if quiet := returned - lastAt.Load(); res.Outcome != "settled" || quiet < 400 {
			t.Errorf("outcome %q, %d ms after the burst's last event; want settled at least 400 ms after it", res.Outcome, quiet)
		}
	})
}

// Decision (ADR-0111 Decision 7: quiet is counted from the newest event after the cursor, even
// when the page ends before it): with max 2 the page holds the two oldest of five scrolls
// spread over 400 ms, and the newest is found by asking for the ring's last entry. quietMs 300
// must pass after the fifth. Seen failing with that request skipped (the quiet was counted from
// the second scroll, and the call settled before the fifth plus 300 ms).
func TestUIEventsSettleCountsFromTheNewestBeyondThePage(t *testing.T) {
	f, r, cur := primedEvents(t)
	lastAt, done := burstInto(r, 0, 5)
	res, err := f.eventsDirect(t, context.Background(), UIEventsParams{Device: uiSerial, Since: cur, WaitMs: 5000, Until: "settle", QuietMs: 300, Max: 2})
	returned := time.Now().UnixMilli()
	done.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if quiet := returned - lastAt.Load(); res.Outcome != "settled" || quiet < 300 || len(res.Events) != 2 || !res.More {
		t.Errorf("outcome %q, %d ms after the fifth, %d events, more %v; want settled at least 300 ms after it with the page of 2 and more", res.Outcome, quiet, len(res.Events), res.More)
	}
}

// Decision (ADR-0111 Decision 8: the worst case is the wait plus two recoveries, each the lock
// wait, the ping and the start bound): a reader that accepts every events request and never
// answers cannot hold the call past that, whatever one exchange may take on its own. The
// numbers are shrunk (wait 0.3 s, lock 0.1, ping 0.1, start 0.3: a bound of 1.3 s) and one
// exchange may take 10 s. Seen failing with the exchange and the recovery uncapped by the
// call's end: the call ran past 10 s.
func TestUIEventsCallEndsByItsTimeBound(t *testing.T) {
	f, r, cur := primedEvents(t)
	poll, lock, ping, start := eventsPollTimeout, eventsLockWait, readerPingCheck, readerStartBound
	eventsPollTimeout, eventsLockWait, readerPingCheck, readerStartBound = 10*time.Second, 100*time.Millisecond, 100*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() {
		eventsPollTimeout, eventsLockWait, readerPingCheck, readerStartBound = poll, lock, ping, start
	})
	r.SetEventsHold(true)
	bound := 300*time.Millisecond + eventsRecoveries*(eventsLockWait+readerPingCheck+readerStartBound)
	began := time.Now()
	_, err := f.eventsDirect(t, context.Background(), UIEventsParams{Device: uiSerial, Since: cur, WaitMs: 300})
	mustErrorCode(t, err, ErrUIDumpFailed)
	if d := time.Since(began); d > bound+time.Duration(testtime.Scale())*700*time.Millisecond {
		t.Errorf("a reader that never answers held the call for %v; its bound is %v", d, bound)
	}
}
