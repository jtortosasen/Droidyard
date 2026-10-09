package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/process"
)

// device.watchLogs (ADR-0041; plan m2a step 4) over the re-executed fake adb: the stream's
// child is a process the supervisor records, so only this fixture reaches the paths the
// tests below pin. Every wait is generous: under -race each fake spawn costs about a
// second (followRaceOverhead).

const (
	wlBase = "1791337000"
	// wlWait bounds a wait for an event or a process to end.
	wlWait = 30 * time.Second
)

// watchFixture is the follow fixture with a bus and one session that carries events.
type watchFixture struct {
	*followFixture
	bus  *event.Bus
	sess *event.Session
}

func newWatchFixture(t *testing.T, files map[string]string) *watchFixture {
	t.Helper()
	f := newFollowFixture(t, files)
	bus := event.New(event.Options{})
	f.adb.SetBus(bus)
	w := &watchFixture{followFixture: f, bus: bus, sess: event.NewSession(bus, "s1")}
	t.Cleanup(func() {
		w.sess.Close()
		ctx, cancel := context.WithTimeout(context.Background(), wlWait)
		defer cancel()
		f.adb.streams.StopAll(ctx)
	})
	return w
}

// wlLog is a device log of main-buffer lines, "<base>.<usec>" apart by one microsecond
// from usec0, with the messages msg(i).
func wlLog(base string, usec0, n int, uid int, msg func(i int) string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%s\n", followLine(fmt.Sprintf("%s.%06d", base, usec0+i), uid, "App", msg(i)))
	}
	return b.String()
}

const wlHeader = "--------- beginning of main\n"

// setLog replaces the fake device's log atomically: the fake re-reads the file at every
// turn and must never see half of it.
func (w *watchFixture) setLog(t *testing.T, content string) {
	t.Helper()
	name := filepath.Join(w.dir, uiSerial+".log")
	if err := os.WriteFile(name+".tmp", []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(name+".tmp", name); err != nil {
		t.Fatal(err)
	}
}

// startOn is start for a given session.
func (w *watchFixture) startOn(t *testing.T, sess *event.Session, params string) (capability.Outcome, *event.Subscription) {
	t.Helper()
	id, err := w.ops.Start(context.Background(), capability.Request{
		Capability: "device.watchLogs", Params: []byte(params), Client: capability.ClientUI, Session: sess,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	out := w.wait(t, id)
	if out.State != capability.StateCompleted {
		return out, nil
	}
	res, ok := out.Result.(WatchLogsResult)
	if !ok {
		t.Fatalf("result is %T, want WatchLogsResult", out.Result)
	}
	sub, ok := sess.Subscription(res.SubscriptionID)
	if !ok {
		t.Fatalf("the session does not hold subscription %s", res.SubscriptionID)
	}
	return out, sub
}

// start opens a stream on the fixture's session and requires it to run.
func (w *watchFixture) start(t *testing.T, params string) (WatchLogsResult, *event.Subscription) {
	t.Helper()
	out, sub := w.startOn(t, w.sess, params)
	if out.State != capability.StateCompleted {
		t.Fatalf("device.watchLogs %s with error %+v", out.State, out.Error)
	}
	return out.Result.(WatchLogsResult), sub
}

func (w *watchFixture) params(extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return fmt.Sprintf(`{"device":%q%s}`, uiSerial, extra)
}

// next waits for the subscription's next envelope.
func next(t *testing.T, sub *event.Subscription) event.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wlWait)
	defer cancel()
	e, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("waiting for an event: %v", err)
	}
	return e
}

func linesOf(t *testing.T, e event.Event) LinesEvent {
	t.Helper()
	if e.Topic != TopicLogcat || e.Type != EventLines {
		t.Fatalf("event %s/%s, want %s/%s", e.Topic, e.Type, TopicLogcat, EventLines)
	}
	var le LinesEvent
	if err := json.Unmarshal(e.Payload, &le); err != nil {
		t.Fatal(err)
	}
	return le
}

func stoppedOf(t *testing.T, e event.Event) StoppedEvent {
	t.Helper()
	if e.Topic != TopicLogcat || e.Type != EventStopped {
		t.Fatalf("event %s/%s, want %s/%s", e.Topic, e.Type, TopicLogcat, EventStopped)
	}
	var se StoppedEvent
	if err := json.Unmarshal(e.Payload, &se); err != nil {
		t.Fatal(err)
	}
	return se
}

// collect reads lines events until the messages delivered and the lines dropped add up
// to want, and returns every event.
func collect(t *testing.T, sub *event.Subscription, want int) []LinesEvent {
	t.Helper()
	var events []LinesEvent
	for got := 0; got < want; {
		le := linesOf(t, next(t, sub))
		events = append(events, le)
		got += len(le.Lines) + le.Dropped
	}
	return events
}

// messagesOf joins the messages of the events' lines, in order.
func messagesOf(events []LinesEvent) string {
	var m []string
	for _, e := range events {
		for _, l := range e.Lines {
			m = append(m, l.Message)
		}
	}
	return strings.Join(m, ",")
}

// waitRecords polls until processes.json holds n records, and returns them.
func (w *watchFixture) waitRecords(t *testing.T, n int) []process.Record {
	t.Helper()
	for deadline := time.Now().Add(wlWait); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if recs := w.records(t); len(recs) == n {
			return recs
		}
	}
	t.Fatalf("processes.json never held %d records (holds %+v)", n, w.records(t))
	return nil
}

// waitIdle requires the stream set to empty and the records and children to be gone: a
// stream leaves no child and no pid record (ADR-0041 §6).
func (w *watchFixture) waitIdle(t *testing.T, pids ...int) {
	t.Helper()
	for deadline := time.Now().Add(wlWait); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if w.adb.streams.Live() == 0 {
			break
		}
	}
	if n := w.adb.streams.Live(); n != 0 {
		t.Errorf("%d streams are still in the set", n)
	}
	if recs := w.records(t); len(recs) != 0 {
		t.Errorf("processes.json holds %+v, want none", recs)
	}
	for _, pid := range pids {
		if !gone(pid) {
			t.Errorf("child pid %d is still running", pid)
		}
	}
}

// Decision (ADR-0041 §4–§6): lines that come due after the call completed are published
// within one batch interval, in events of at most 500 lines, in order, each marked
// untrusted, to the call's subscription; a run of lines is batched, not sent one by one;
// and the child outlives the call that started it, since the operation's context ends
// when the call completes. Seen failing with the batcher removed (no event ever
// published, then with one event per line: no event carries more than one) and with the
// child on the operation's context (the call completes, the child is killed, the lines
// appended afterwards never arrive).
func TestWatchLogsStreamsBatches(t *testing.T) {
	w := newWatchFixture(t, map[string]string{
		uiSerial + ".log": wlHeader + wlLog(wlBase, 0, 3, 10146, func(i int) string { return fmt.Sprintf("early-%d", i) }),
	})
	res, sub := w.start(t, w.params(`"tail":10`))
	if res.Device != uiSerial || res.SubscriptionID == "" || res.UID != nil {
		t.Fatalf("result %+v, want the device, a subscription and no uid", res)
	}
	// The history first: the three lines already in the log.
	if first := collect(t, sub, 3); len(first) == 0 {
		t.Fatal("no history was published")
	}

	// The call completed long ago: these lines come due on the running child.
	const late = 1200
	appended := time.Now()
	w.setLog(t, wlHeader+
		wlLog(wlBase, 0, 3, 10146, func(i int) string { return fmt.Sprintf("early-%d", i) })+
		wlLog("1791337100", 0, late, 10146, func(i int) string { return fmt.Sprintf("late-%d", i) }))
	var events []event.Event
	got := 0
	for got < late {
		e := next(t, sub)
		if len(events) == 0 {
			if took := time.Since(appended); took > streamInterval+time.Second {
				t.Errorf("the first batch took %s after the lines came due, want about one interval (%s)", took, streamInterval)
			}
		}
		le := linesOf(t, e)
		if len(le.Lines) > streamBatchLines {
			t.Errorf("an event carries %d lines, want at most %d", len(le.Lines), streamBatchLines)
		}
		if !le.Untrusted || le.Device != uiSerial || le.Dropped != 0 {
			t.Errorf("event %+v, want untrusted true, device %s and nothing dropped", le, uiSerial)
		}
		if e.Workspace != nil || e.Operation == "" {
			t.Errorf("event envelope workspace %v operation %q, want global and the call's operation", e.Workspace, e.Operation)
		}
		for _, l := range le.Lines {
			if want := fmt.Sprintf("late-%d", got); l.Message != want {
				t.Fatalf("line %d is %q, want %q: batches must keep the log's order", got, l.Message, want)
			}
			if l.Tag != "App" || l.Priority != "info" || l.PID != 6406 {
				t.Errorf("line %+v is not parsed as the log line it was", l)
			}
			got++
		}
		events = append(events, e)
	}
	biggest := 0
	for _, e := range events {
		if n := len(linesOf(t, e).Lines); n > biggest {
			biggest = n
		}
	}
	if biggest < 100 {
		t.Errorf("the biggest event carries %d lines out of %d: lines were published one by one, not batched", biggest, late)
	}
}

// Decision (ADR-0041 §5): nothing is dropped silently, and two mechanisms report it apart.
// A backlog past the pending buffer's 10,000 lines drops its oldest and the next event
// says how many in `dropped`; a subscription that does not drain loses events to the bus's
// own events.gap marker and `dropped` stays zero. Seen failing with the drop uncounted
// (delivered plus dropped fell short of the log) and with the pending buffer unbounded
// (nothing dropped, so the first assertion fails).
func TestWatchLogsCountsDrops(t *testing.T) {
	t.Run("a backlog past the pending cap is counted in dropped", func(t *testing.T) {
		const n = 15000
		w := newWatchFixture(t, map[string]string{
			uiSerial + ".date.out": "1791337500.000000\n",
			uiSerial + ".log":      wlHeader + wlLog(wlBase, 0, n, 10146, func(i int) string { return fmt.Sprintf("m%d", i) }),
		})
		_, sub := w.start(t, w.params(`"since":"2026-10-07T00:00:00Z"`))
		events := collect(t, sub, n)
		delivered, dropped, last := 0, 0, ""
		for _, e := range events {
			delivered += len(e.Lines)
			dropped += e.Dropped
			if len(e.Lines) > 0 {
				last = e.Lines[len(e.Lines)-1].Message
			}
		}
		if dropped == 0 {
			t.Errorf("a backlog of %d lines dropped none: the pending buffer is unbounded", n)
		}
		if delivered+dropped != n {
			t.Errorf("delivered %d + dropped %d = %d, want %d: a line was lost without being counted", delivered, dropped, delivered+dropped, n)
		}
		if want := fmt.Sprintf("m%d", n-1); last != want {
			t.Errorf("the newest line delivered is %q, want %q: the oldest are what a full buffer drops", last, want)
		}
	})

	t.Run("a line in no known format is counted, and the lines around it arrive", func(t *testing.T) {
		w := newWatchFixture(t, map[string]string{
			uiSerial + ".date.out": "1791337500.000000\n",
			uiSerial + ".log": wlHeader + wlLog(wlBase, 0, 3, 10146, func(i int) string { return fmt.Sprintf("a%d", i) }) +
				"not a logcat line\n" + wlLog(wlBase, 3, 3, 10146, func(i int) string { return fmt.Sprintf("b%d", i) }),
		})
		_, sub := w.start(t, w.params(`"since":"2026-10-07T00:00:00Z"`))
		events := collect(t, sub, 7)
		dropped := 0
		for _, e := range events {
			dropped += e.Dropped
		}
		if got := messagesOf(events); got != "a0,a1,a2,b0,b1,b2" || dropped != 1 {
			t.Errorf("lines %q, dropped %d; want a0,a1,a2,b0,b1,b2 and 1 dropped", got, dropped)
		}
	})

	t.Run("a subscription that does not drain gets events.gap", func(t *testing.T) {
		const n = 3000 // six events of 500, a queue of two
		w := newWatchFixture(t, map[string]string{
			uiSerial + ".date.out": "1791337500.000000\n",
			uiSerial + ".log":      wlHeader + wlLog(wlBase, 0, n, 10146, func(i int) string { return fmt.Sprintf("m%d", i) }),
		})
		w.sess.SetQueue(2)
		_, sub := w.start(t, w.params(`"since":"2026-10-07T00:00:00Z"`))
		// Nothing reads until the stream has published everything: six events took six seqs.
		for deadline := time.Now().Add(wlWait); w.bus.Position().Seq < 6 && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		}
		if seq := w.bus.Position().Seq; seq < 6 {
			t.Fatalf("the stream published %d events, want at least 6", seq)
		}
		var topics []string
		dropped := 0
		for {
			e, ok := sub.TryNext()
			if !ok {
				break
			}
			topics = append(topics, e.Topic)
			if e.Topic == TopicLogcat {
				dropped += linesOf(t, e).Dropped
			}
		}
		if len(topics) != 3 || topics[0] != TopicLogcat || topics[1] != TopicLogcat || topics[2] != event.TopicGap {
			t.Errorf("the subscription holds %v, want two events and the bus's events.gap", topics)
		}
		if dropped != 0 {
			t.Errorf("dropped = %d: the backlog was under the pending cap, so only the gap reports the loss", dropped)
		}
	})
}

// Decision (ADR-0041 §6): a stream's child never outlives its stream and every end path
// leaves no child, no pid record and nothing in the stream set: events.unsubscribe, the
// session's close, a second stream of the same session and device (the first publishes
// stopped: replaced, the second runs on), a logcat that exits by itself (deviceGone when
// device.list no longer holds the serial, else exited, the cause naming the status), and
// an operation cancelled while the child starts. Seen failing with the end hook not
// killing the child (a live pid after unsubscribe and after the session's close) and with
// the cancelled start leaving a stream in the set.
func TestWatchLogsEndsItsChild(t *testing.T) {
	files := map[string]string{
		uiSerial + ".log": wlHeader + wlLog(wlBase, 0, 2, 10146, func(i int) string { return fmt.Sprintf("m%d", i) }),
	}

	t.Run("events.unsubscribe", func(t *testing.T) {
		w := newWatchFixture(t, files)
		res, _ := w.start(t, w.params(""))
		rec := w.waitRecords(t, 1)[0]
		w.sess.Unsubscribe(res.SubscriptionID)
		w.waitIdle(t, rec.PID)
	})

	t.Run("the session's close", func(t *testing.T) {
		w := newWatchFixture(t, files)
		w.start(t, w.params(""))
		rec := w.waitRecords(t, 1)[0]
		w.sess.Close()
		w.waitIdle(t, rec.PID)
	})

	t.Run("a second stream of the same session and device", func(t *testing.T) {
		w := newWatchFixture(t, files)
		_, first := w.start(t, w.params(""))
		rec1 := w.waitRecords(t, 1)[0]
		res2, _ := w.start(t, w.params(""))
		// The first stream says it was replaced, after its lines, and stays open and empty.
		var stopped StoppedEvent
		for {
			e := next(t, first)
			if e.Type == EventStopped {
				stopped = stoppedOf(t, e)
				break
			}
		}
		if stopped.Reason != StopReplaced || stopped.Device != uiSerial {
			t.Errorf("stopped %+v, want replaced for %s", stopped, uiSerial)
		}
		for deadline := time.Now().Add(wlWait); !gone(rec1.PID) && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		}
		if !gone(rec1.PID) {
			t.Errorf("the replaced stream's child %d is still running", rec1.PID)
		}
		recs := w.waitRecords(t, 1)
		if recs[0].PID == rec1.PID || gone(recs[0].PID) {
			t.Errorf("records %+v: want the second stream's child alive and distinct from %d", recs, rec1.PID)
		}
		if e, ok := first.TryNext(); ok {
			t.Errorf("the replaced subscription received %s/%s after stopped", e.Topic, e.Type)
		}
		if _, still := w.sess.Subscription(res2.SubscriptionID); !still {
			t.Error("the replacing subscription is gone")
		}
		if n := w.sess.Len(); n != 2 {
			t.Errorf("the session holds %d subscriptions, want both: stopped leaves the first open and empty", n)
		}
		w.sess.Close()
		w.waitIdle(t, rec1.PID, recs[0].PID)
	})

	for _, c := range []struct {
		name   string
		listed bool
		reason string
	}{
		{"a logcat that exits while the serial is gone", false, StopDeviceGone},
		{"a logcat that exits while the serial is listed", true, StopExited},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWatchFixture(t, files)
			_, sub := w.start(t, w.params(""))
			rec := w.waitRecords(t, 1)[0]
			if !c.listed {
				devicetestWrite(t, w.dir, "devices.out", listHead)
			}
			devicetestWrite(t, w.dir, uiSerial+".follow.exitcode", "7")
			var stopped StoppedEvent
			for {
				if e := next(t, sub); e.Type == EventStopped {
					stopped = stoppedOf(t, e)
					break
				}
			}
			if stopped.Reason != c.reason || stopped.Device != uiSerial {
				t.Errorf("stopped %+v, want %s", stopped, c.reason)
			}
			if stopped.Cause != "adb logcat exited with status 7" {
				t.Errorf("cause %q, want the exit status and no device text", stopped.Cause)
			}
			w.waitIdle(t, rec.PID)
			// The subscription stays open and empty until the session ends.
			if n := w.sess.Len(); n != 1 {
				t.Errorf("the session holds %d subscriptions, want the stopped stream's, still open", n)
			}
		})
	}

	t.Run("the operation cancelled while the child starts", func(t *testing.T) {
		w := newWatchFixture(t, files)
		entered, release := make(chan struct{}), make(chan struct{})
		real := recordedStarter(w.sup)
		var once sync.Once
		w.adb.SetStreamStarter(func(cmd *exec.Cmd, kind string) (Stream, error) {
			once.Do(func() { close(entered) })
			<-release
			return real(cmd, kind)
		})
		id, err := w.ops.Start(context.Background(), capability.Request{
			Capability: "device.watchLogs", Params: []byte(w.params("")), Client: capability.ClientUI, Session: w.sess,
		})
		if err != nil {
			t.Fatal(err)
		}
		<-entered
		if err := w.ops.Cancel(id); err != nil {
			t.Fatal(err)
		}
		close(release)
		if out := w.wait(t, id); out.State != capability.StateCancelled {
			t.Fatalf("state %s with error %+v, want cancelled", out.State, out.Error)
		}
		w.waitIdle(t)
		if n := w.sess.Len(); n != 0 {
			t.Errorf("the cancelled call left %d subscriptions", n)
		}
		if pid, ok := readFollowPid(t, w.dir, uiSerial); ok && !gone(pid) {
			t.Errorf("a child (pid %d) survived the cancelled start", pid)
		}
	})
}

// devicetestWrite replaces a scenario file atomically, so the fake never reads it half
// written.
func devicetestWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p+".tmp", []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p+".tmp", p); err != nil {
		t.Fatal(err)
	}
}

// Decision (ADR-0010; ADR-0041 §7): device.watchLogs needs a session that carries events.
// A call without one is SESSION_REQUIRED before any operation exists and no adb command
// runs; the CLI, HTTP and MCP halves of the same rule are TestWatchLogsNeedsAWebSocketSession
// in cmd/droidyard. Seen failing with the descriptor stateless (the call ran and opened
// no subscription anywhere).
func TestWatchLogsWithoutASessionIsRefused(t *testing.T) {
	w := newWatchFixture(t, nil)
	_, err := w.ops.Start(context.Background(), capability.Request{
		Capability: "device.watchLogs", Params: []byte(w.params("")), Client: capability.ClientCLI,
	})
	var e *capability.Error
	if !errors.As(err, &e) || e.Code != capability.ErrSessionRequired {
		t.Fatalf("Start: %v, want SESSION_REQUIRED", err)
	}
	if calls := devicetest.Calls(t, w.dir); len(calls) != 0 {
		t.Errorf("adb was invoked %v for a call that needs a session", calls)
	}
}

// Decision (ADR-0041 §2): `applicationId` filters on the host by logcat's uid column, the
// uid resolved once at start and returned. A device without the column, and an application
// pm does not list, each fail DEVICE_LOGS_FAILED and start no child. Seen failing with the
// filter dropped (the other app's and the system's lines arrive) and with the not-installed
// case falling back to every line.
func TestWatchLogsApplicationFilter(t *testing.T) {
	log := wlHeader +
		followLine(wlBase+".000001", 10146, "Mine", "m1") + "\n" +
		followLine(wlBase+".000002", 10200, "Other", "o1") + "\n" +
		followLine(wlBase+".000003", 0, "Sys", "s1") + "\n" +
		followLine(wlBase+".000004", 10146, "Mine", "m2") + "\n"

	t.Run("uid-filtered", func(t *testing.T) {
		w := newWatchFixture(t, map[string]string{
			uiSerial + ".log":      log,
			uiSerial + ".pm.out":   "package:" + appID + " uid:10146\npackage:" + appID + ".debug uid:10147\n",
			uiSerial + ".date.out": "1791337500.000000\n",
		})
		res, sub := w.start(t, w.params(`"applicationId":"`+appID+`","tail":100`))
		if res.UID == nil || *res.UID != 10146 {
			t.Fatalf("uid = %v, want 10146", res.UID)
		}
		if got := messagesOf(collect(t, sub, 2)); got != "m1,m2" {
			t.Errorf("lines %q, want only the application's m1,m2", got)
		}
	})

	t.Run("under the filter, a line without the uid column is counted, not hidden", func(t *testing.T) {
		w := newWatchFixture(t, map[string]string{
			// The uid-less line is older than the five the start probe reads (-t 5), which
			// have the column: the probe passes, as on a device whose column comes and goes.
			uiSerial + ".log": wlHeader + wlBase + ".000000 6406 6406 I NoUID: n1\n" +
				wlLog(wlBase, 1, 5, 10146, func(i int) string { return fmt.Sprintf("m%d", i) }),
			uiSerial + ".pm.out":   "package:" + appID + " uid:10146\n",
			uiSerial + ".date.out": "1791337500.000000\n",
		})
		_, sub := w.start(t, w.params(`"applicationId":"`+appID+`","tail":100`))
		events := collect(t, sub, 6)
		dropped := 0
		for _, e := range events {
			dropped += e.Dropped
		}
		if got := messagesOf(events); got != "m0,m1,m2,m3,m4" || dropped != 1 {
			t.Errorf("lines %q, dropped %d; want m0..m4 and the uid-less line counted as 1 dropped", got, dropped)
		}
	})

	t.Run("a device without the uid column", func(t *testing.T) {
		w := newWatchFixture(t, map[string]string{
			uiSerial + ".log": log, uiSerial + ".nouid": "", uiSerial + ".pm.out": "package:" + appID + " uid:10146\n",
		})
		out, _ := w.startOn(t, w.sess, w.params(`"applicationId":"`+appID+`"`))
		if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != ErrLogsFailed {
			t.Fatalf("outcome %s %+v, want DEVICE_LOGS_FAILED", out.State, out.Error)
		}
		w.waitIdle(t)
	})

	t.Run("an application pm does not list", func(t *testing.T) {
		w := newWatchFixture(t, map[string]string{uiSerial + ".log": log, uiSerial + ".pm.out": "package:com.other.app uid:10300\n"})
		out, _ := w.startOn(t, w.sess, w.params(`"applicationId":"`+appID+`"`))
		if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != ErrLogsFailed {
			t.Fatalf("outcome %s %+v, want DEVICE_LOGS_FAILED", out.State, out.Error)
		}
		if !strings.Contains(out.Error.NextStep, "Install it, then start the stream again") {
			t.Errorf("next step %q does not say to install the application and start again", out.Error.NextStep)
		}
		w.waitIdle(t)
		if n := w.sess.Len(); n != 0 {
			t.Errorf("the failed call left %d subscriptions", n)
		}
	})
}

// Decision (ADR-0041 §2): `tail` N starts the stream with the newest N lines (logcat -T
// N, digits only), `since` with the lines at or after an RFC 3339 device-clock time
// (logcat -T <epoch>); the default is 1,000 lines; naming both, or a tail out of range, is
// REQUEST_INVALID_PARAMS before the device is touched. Seen failing with the count read
// as an epoch (every line, or none) and with since sent as tail.
func TestWatchLogsTailAndSince(t *testing.T) {
	log := wlHeader + wlLog(wlBase, 0, 10, 10146, func(i int) string { return fmt.Sprintf("m%d", i) })
	files := map[string]string{uiSerial + ".log": log}
	calls := func(w *watchFixture) string { return strings.Join(devicetest.Calls(t, w.dir), "\n") }

	t.Run("tail gives the newest N", func(t *testing.T) {
		w := newWatchFixture(t, files)
		_, sub := w.start(t, w.params(`"tail":3`))
		if got := messagesOf(collect(t, sub, 3)); got != "m7,m8,m9" {
			t.Errorf("lines %q, want the newest three m7,m8,m9", got)
		}
		if !strings.Contains(calls(w), "-b main,system,crash -T 3") {
			t.Errorf("calls %q lack logcat -T 3 over main,system,crash", calls(w))
		}
	})

	t.Run("the default is 1000", func(t *testing.T) {
		w := newWatchFixture(t, files)
		w.start(t, w.params(""))
		// The child logs its arguments once it runs, a moment after the call completed.
		for deadline := time.Now().Add(wlWait); !strings.Contains(calls(w), " -T 1000") && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		}
		if !strings.Contains(calls(w), " -T 1000") {
			t.Errorf("calls %q lack logcat -T 1000", calls(w))
		}
	})

	t.Run("since is an epoch", func(t *testing.T) {
		w := newWatchFixture(t, files)
		// m6 is 1791337000.000006 on the device clock.
		_, sub := w.start(t, w.params(`"since":"2026-10-07T01:36:40.000006Z"`))
		if got := messagesOf(collect(t, sub, 4)); got != "m6,m7,m8,m9" {
			t.Errorf("lines %q, want those at or after the instant: m6,m7,m8,m9", got)
		}
		if !strings.Contains(calls(w), " -T 1791337000.000006") {
			t.Errorf("calls %q lack logcat -T 1791337000.000006", calls(w))
		}
	})

	for name, params := range map[string]string{
		"both":            `"tail":5,"since":"2026-10-07T01:36:40Z"`,
		"a negative tail": `"tail":-1`,
		"a tail too big":  `"tail":5001`,
		"a bad since":     `"since":"yesterday"`,
		"a bad app":       `"applicationId":"nodots"`,
	} {
		t.Run("invalid: "+name, func(t *testing.T) {
			w := newWatchFixture(t, nil)
			out, _ := w.startOn(t, w.sess, w.params(params))
			if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != capability.ErrInvalidParams {
				t.Fatalf("outcome %s %+v, want REQUEST_INVALID_PARAMS", out.State, out.Error)
			}
			if c := devicetest.Calls(t, w.dir); len(c) != 0 {
				t.Errorf("adb was invoked %v for a malformed call", c)
			}
		})
	}
}
