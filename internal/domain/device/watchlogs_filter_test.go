package device

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// The line filter of device.watchLogs (ADR-0135 §2-§4, §6; ADR-0041 §5; plan m3i step 5).
// The tests that attack the concurrency drive a logStream by hand (bareStream): the
// pipeline is the real one (onLine, the batcher's judgePending and publishBatch, drain)
// with a real bus and subscription, but no child, so a matcher can be held on a channel
// and the interleaving is the test's, not a clock's.

// wlLineP is a log line of a given pid, in the uid format.
func wlLineP(usec int, uid, pid int, tag, msg string) string {
	return fmt.Sprintf("%s.%06d %d %d %d I %s: %s", wlBase, usec, uid, pid, pid, tag, msg)
}

// sink reads a subscription as the client would, so the bus's queue never overflows, and
// keeps what the lines events carried.
type sink struct {
	mu        sync.Mutex
	messages  []string
	dropped   int
	delivered int
}

func newSink(t *testing.T, sub *event.Subscription) *sink {
	t.Helper()
	k := &sink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-done
	})
	go func() {
		defer close(done)
		for {
			e, err := sub.Next(ctx)
			if err != nil {
				return
			}
			if e.Type != EventLines {
				continue
			}
			var le LinesEvent
			if err := json.Unmarshal(e.Payload, &le); err != nil {
				continue
			}
			k.mu.Lock()
			for _, l := range le.Lines {
				k.messages = append(k.messages, l.Message)
			}
			k.delivered += len(le.Lines)
			k.dropped += le.Dropped
			k.mu.Unlock()
		}
	}()
	return k
}

func (k *sink) snapshot() (messages []string, dropped int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.messages), k.dropped
}

// accounted waits until the lines delivered and dropped add up to want, and returns them.
func (k *sink) accounted(t *testing.T, want int) (messages []string, dropped int) {
	t.Helper()
	testtime.Eventually(t, testtime.Bound(30*time.Second), fmt.Sprintf("%d lines delivered or dropped", want), func() bool {
		k.mu.Lock()
		defer k.mu.Unlock()
		return k.delivered+k.dropped >= want
	})
	return k.snapshot()
}

// bareStream is a logStream with no child. matcher may be nil (no filter).
func bareStream(t *testing.T, matcher func(*StreamLine) bool) (*logStream, *sink) {
	t.Helper()
	bus := event.New(event.Options{})
	sess := event.NewSession(bus, "s1")
	t.Cleanup(sess.Close)
	_, sub, err := sess.Open([]string{TopicLogcat}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &logStream{bus: bus, sub: sub, device: uiSerial, operation: "op", matcher: matcher,
		ctx: ctx, cancel: cancel, rctx: ctx, rcancel: cancel}
	return s, newSink(t, sub)
}

// push writes message m<i> as the reader would. The uid is the system's, so a line has no
// application id and asks for no package read.
func pushLine(t *testing.T, s *logStream, i int) {
	t.Helper()
	if err := s.onLine([]byte(wlLineP(i, 1000, 600, "T", "m"+strconv.Itoa(i)))); err != nil {
		t.Fatal(err)
	}
}

func msgIndex(msg string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(msg, "m"))
	return n
}

// Decision (plan m3i R2-B1b, ADR-0135 §2): the line filter runs on the batcher, never on
// the reader and never under a lock, so a slow pattern cannot hold up logcat's reader, the
// event publisher, or the lock they share; and every line written is delivered, rejected or
// counted in dropped, whether it is judged in a tick, in the drain at the stop, or lost
// unjudged to the cap (ADR-0041 §5). The matcher here is held on a channel, which stands for
// a pattern that takes as long as the test needs.
//
// Seen failing with the filter run under s.mu (the TryLock finds it held and onLine blocks
// until the matcher is released), with the drain of today's loop that judges nothing (the
// lines still unjudged at the stop are neither delivered nor counted), and with a verdict
// written back by position instead of by seq (a line evicted between the copy and the
// write-back gives its verdict to the line that took its place).
func TestWatchLogsFilterConservesLines(t *testing.T) {
	// The hang guard of every wait on the pipeline: it is never the measurement.
	guard := testtime.Bound(10 * time.Second)

	t.Run("a matcher held on a channel holds up neither the reader nor the publisher", func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var enterOnce, releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(unblock)
		s, sk := bareStream(t, func(*StreamLine) bool {
			enterOnce.Do(func() { close(entered) })
			<-release
			return true
		})
		for i := 0; i < 3; i++ {
			pushLine(t, s, i)
		}
		judged := make(chan struct{})
		go func() {
			defer close(judged)
			s.judgePending() // the batcher's tick, held in the matcher
		}()
		select {
		case <-entered:
		case <-time.After(guard):
			t.Fatal("the matcher was never called")
		}

		// Neither lock is held while a pattern runs.
		if !s.mu.TryLock() {
			t.Fatal("s.mu is held while the matcher runs: the reader would wait for the pattern")
		}
		s.mu.Unlock()
		if !s.pubMu.TryLock() {
			t.Fatal("pubMu is held while the matcher runs: the publisher would wait for the pattern")
		}
		s.pubMu.Unlock()

		// The reader still returns, and so does a publish; the lines wait for their verdict.
		read := make(chan struct{})
		go func() {
			defer close(read)
			for i := 3; i < 10; i++ {
				pushLine(t, s, i)
			}
		}()
		select {
		case <-read:
		case <-time.After(guard):
			t.Fatal("the reader did not return while the matcher was held")
		}
		if s.publishBatch() {
			t.Error("a line was published before its verdict")
		}

		unblock()
		<-judged
		if !s.publishBatch() {
			t.Fatal("nothing was published once every line was judged")
		}
		got, dropped := sk.accounted(t, 10)
		if want := "m0,m1,m2,m3,m4,m5,m6,m7,m8,m9"; strings.Join(got, ",") != want || dropped != 0 {
			t.Errorf("delivered %v, dropped %d; want %s in order and none dropped", got, dropped, want)
		}
	})

	t.Run("a stop with lines not judged yet loses none", func(t *testing.T) {
		// The matcher rejects the odd messages and counts what it rejected. No tick ran:
		// everything written is still unjudged when the stop's drain begins.
		var rejected atomic.Int32
		s, sk := bareStream(t, func(l *StreamLine) bool {
			if msgIndex(l.Message)%2 == 1 {
				rejected.Add(1)
				return false
			}
			return true
		})
		const written = 40
		for i := 0; i < written; i++ {
			pushLine(t, s, i)
		}
		s.drain()
		got, dropped := sk.accounted(t, written-int(rejected.Load()))
		if len(got) != written/2 || rejected.Load() != written/2 || dropped != 0 {
			t.Errorf("delivered %d, rejected %d, dropped %d of %d written; want 20, 20 and 0", len(got), rejected.Load(), dropped, written)
		}
		for i, m := range got {
			if m != "m"+strconv.Itoa(2*i) {
				t.Fatalf("delivered[%d] = %s; want the even messages in order", i, m)
			}
		}
		if n := len(s.pending.buf) - s.pending.head; n != 0 {
			t.Errorf("%d lines are still pending after the drain", n)
		}
	})

	t.Run("the drain's budget leaves the rest counted, not lost", func(t *testing.T) {
		old := streamDrainBudget
		streamDrainBudget = 0 // spent at the first line: one is judged, the rest cannot be
		t.Cleanup(func() { streamDrainBudget = old })
		var judgedLines atomic.Int32
		s, sk := bareStream(t, func(*StreamLine) bool {
			judgedLines.Add(1)
			return true
		})
		const written = 40
		for i := 0; i < written; i++ {
			pushLine(t, s, i)
		}
		s.drain()
		got, dropped := sk.accounted(t, written)
		if judgedLines.Load() != 1 || len(got) != 1 || dropped != written-1 {
			t.Errorf("judged %d, delivered %d, dropped %d; want 1, 1 and %d: delivered + rejected + dropped must equal the %d written", judgedLines.Load(), len(got), dropped, written-1, written)
		}
	})

	t.Run("a line evicted between the copy and the write-back is counted once", func(t *testing.T) {
		// The cap is full of unjudged lines; the batcher copies the oldest 256 out and
		// holds in the matcher; 301 more lines arrive and evict the 301 oldest, the 256
		// copied among them. The verdicts then written back belong to lines that are gone.
		entered, release := make(chan struct{}), make(chan struct{})
		var enterOnce, releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(unblock)
		s, sk := bareStream(t, func(l *StreamLine) bool {
			enterOnce.Do(func() { close(entered) })
			<-release
			return msgIndex(l.Message)%2 == 0 // keeps the even messages
		})
		const evicted = 301
		total := streamPendingCap + evicted
		for i := 0; i < streamPendingCap; i++ {
			pushLine(t, s, i)
		}
		judged := make(chan struct{})
		go func() {
			defer close(judged)
			s.judgePending()
		}()
		select {
		case <-entered:
		case <-time.After(guard):
			t.Fatal("the matcher was never called")
		}
		pushed := make(chan struct{})
		go func() {
			defer close(pushed)
			for i := streamPendingCap; i < total; i++ {
				pushLine(t, s, i)
			}
		}()
		select {
		case <-pushed:
		case <-time.After(guard):
			t.Fatal("the reader did not return while the matcher was held")
		}
		unblock()
		<-judged
		s.drain()

		// Lines m301..m10300 remain: the even ones are delivered, the odd rejected; the 301
		// evicted unjudged are dropped. A verdict written by position would hand m0..m255's
		// to m301..m556 and deliver an odd message.
		wantKept := 0
		for i := evicted; i < total; i++ {
			if i%2 == 0 {
				wantKept++
			}
		}
		got, dropped := sk.accounted(t, wantKept+evicted)
		rejected := (total - evicted) - wantKept
		if dropped != evicted || len(got) != wantKept {
			t.Fatalf("delivered %d, dropped %d, rejected %d of %d written; want %d, %d, %d", len(got), dropped, rejected, total, wantKept, evicted, rejected)
		}
		if len(got)+rejected+dropped != total {
			t.Errorf("delivered %d + rejected %d + dropped %d = %d, want the %d written", len(got), rejected, dropped, len(got)+rejected+dropped, total)
		}
		for _, m := range got {
			if i := msgIndex(m); i%2 != 0 || i < evicted {
				t.Fatalf("delivered %s: an odd message, or one that was evicted", m)
			}
		}
	})
}

// Decision (plan m3i step 5, ADR-0041 §5): a rejected line is never counted, even when the
// full buffer evicts it, and a line the filter has not judged is counted when it is evicted.
// The queue's verdicts are what take, push, setVerdict and discardUnjudged agree on.
// TestEvictedRejectedLinesAreNotDropped: a line the cap evicts counts in dropped unless the
// filter had rejected it (ADR-0135 §3; plan m3i close review: a mutant counting every
// evicted line survived the stream tests).
func TestEvictedRejectedLinesAreNotDropped(t *testing.T) {
	for _, c := range []struct {
		v       verdict
		evicted bool
		want    bool
	}{
		{verdictRejected, true, false},
		{verdictKept, true, true},
		{verdictNone, true, true},
		{verdictKept, false, false},
	} {
		if got := lostOnEviction(queuedLine{verdict: c.v}, c.evicted); got != c.want {
			t.Errorf("lostOnEviction(verdict %d, evicted %v) = %v; want %v", c.v, c.evicted, got, c.want)
		}
	}
	// Through push: a full queue of rejected lines evicts without a loss.
	var q lineQueue
	lost := 0
	for i := 1; i <= streamPendingCap+10; i++ {
		if gone, evicted := q.push(queuedLine{seq: uint64(i), verdict: verdictRejected}); lostOnEviction(gone, evicted) {
			lost++
		}
	}
	if lost != 0 {
		t.Errorf("%d rejected lines evicted counted as lost; want 0", lost)
	}
}

func TestLineQueueVerdicts(t *testing.T) {
	line := func(seq uint64, v verdict) queuedLine {
		return queuedLine{line: StreamLine{Message: "m" + strconv.FormatUint(seq, 10)}, seq: seq, verdict: v}
	}
	never := func(*queuedLine) bool { return false }
	pkg := func(*queuedLine) string { return "" }

	t.Run("take skips the rejected uncounted and stops at an unjudged line", func(t *testing.T) {
		var q lineQueue
		for i, v := range []verdict{verdictKept, verdictRejected, verdictRejected, verdictKept, verdictNone, verdictKept} {
			q.push(line(uint64(i+1), v))
		}
		out := q.take(500, 1<<20, never, pkg)
		if len(out) != 2 || out[0].Message != "m1" || out[1].Message != "m4" {
			t.Fatalf("take = %v; want the kept lines m1 and m4, the rejected ones gone", out)
		}
		if got := q.take(500, 1<<20, never, pkg); got != nil {
			t.Errorf("take at an unjudged line = %v; want nothing", got)
		}
		if !q.setVerdict(5, verdictRejected) {
			t.Fatal("setVerdict did not find a queued line")
		}
		out = q.take(500, 1<<20, never, pkg)
		if len(out) != 1 || out[0].Message != "m6" {
			t.Errorf("take after the verdict = %v; want m6 alone", out)
		}
	})

	t.Run("the 500 counts kept lines, not rejected ones", func(t *testing.T) {
		var q lineQueue
		seq := uint64(0)
		for i := 0; i < 600; i++ {
			seq++
			v := verdictRejected
			if i%2 == 0 {
				v = verdictKept
			}
			q.push(line(seq, v))
		}
		if out := q.take(500, 1<<20, never, pkg); len(out) != 300 {
			t.Errorf("take returned %d lines; want the 300 kept of the 600 queued", len(out))
		}
	})

	t.Run("a full queue evicts its oldest and says what it was", func(t *testing.T) {
		var q lineQueue
		q.push(line(1, verdictRejected))
		for i := 2; i <= streamPendingCap; i++ {
			q.push(line(uint64(i), verdictNone))
		}
		gone, ok := q.push(line(streamPendingCap+1, verdictNone))
		if !ok || gone.seq != 1 || gone.verdict != verdictRejected {
			t.Errorf("push at the cap evicted %+v (%v); want the rejected seq 1", gone, ok)
		}
		gone, ok = q.push(line(streamPendingCap+2, verdictNone))
		if !ok || gone.seq != 2 || gone.verdict != verdictNone {
			t.Errorf("the next push evicted %+v (%v); want the unjudged seq 2", gone, ok)
		}
		if q.setVerdict(1, verdictKept) || q.setVerdict(2, verdictKept) {
			t.Error("setVerdict accepted a verdict for an evicted line")
		}
		if !q.setVerdict(3, verdictKept) || q.oldest().verdict != verdictKept {
			t.Error("setVerdict lost the oldest queued line")
		}
	})

	t.Run("discardUnjudged removes only the unjudged and counts them", func(t *testing.T) {
		var q lineQueue
		for i, v := range []verdict{verdictKept, verdictRejected, verdictNone, verdictNone} {
			q.push(line(uint64(i+1), v))
		}
		if n := q.discardUnjudged(); n != 2 {
			t.Errorf("discardUnjudged = %d; want 2", n)
		}
		if out := q.take(500, 1<<20, never, pkg); len(out) != 1 || out[0].Message != "m1" {
			t.Errorf("take after the discard = %v; want m1", out)
		}
	})
}

// Decision (ADR-0135 §3, §6): device.watchLogs takes the device.logs line filter and runs it
// on the host: an excluded tag and a line no `match` selects never reach the client, are not
// counted in `dropped`, and no caller text reaches adb's argv (the child's is the one it had
// before the filter). The last line is a sentinel that passes, so that once it arrives every
// line before it has been judged. Seen failing with the filter off (the SurfaceFlinger line
// and the quiet one arrive).
func TestWatchLogsLineFilter(t *testing.T) {
	log := wlHeader +
		wlLineP(1, 1000, 600, "SurfaceFlinger", "boom frame") + "\n" + // match, but its tag is excluded
		wlLineP(2, 10146, 6406, "App", "boom one") + "\n" +
		wlLineP(3, 10146, 6406, "App", "quiet") + "\n" + // matches nothing
		wlLineP(4, 10146, 6406, "Other", "boom two") + "\n" +
		wlLineP(5, 10146, 6406, "App", "boom end") + "\n"
	w := newWatchFixture(t, map[string]string{uiSerial + ".log": log})
	_, sub := w.start(t, w.params(`"tail":100,"excludeTags":["SurfaceFlinger"],"match":["boom"]`))
	var events []LinesEvent
	for last := false; !last; {
		le := linesOf(t, next(t, sub))
		events = append(events, le)
		for _, l := range le.Lines {
			last = last || l.Message == "boom end"
		}
	}
	dropped := 0
	for _, e := range events {
		dropped += e.Dropped
	}
	if got := messagesOf(events); got != "boom one,boom two,boom end" || dropped != 0 {
		t.Errorf("lines %q, dropped %d; want boom one,boom two,boom end and nothing dropped: a rejected line is not counted", got, dropped)
	}
	calls := strings.Join(devicetest.Calls(t, w.dir), "\n")
	if !strings.Contains(calls, "-b main,system,crash -T 100") {
		t.Errorf("calls %q lack the unchanged logcat -b main,system,crash -T 100", calls)
	}
	for _, text := range []string{"SurfaceFlinger", "boom"} {
		if strings.Contains(calls, text) {
			t.Errorf("calls %q hold the caller's %q: caller text must never reach adb", calls, text)
		}
	}
}

// onlyStream is the one live stream of the fixture's device.
func onlyStream(t *testing.T, w *watchFixture) *logStream {
	t.Helper()
	w.adb.streams.mu.Lock()
	defer w.adb.streams.mu.Unlock()
	for s := range w.adb.streams.all {
		return s
	}
	t.Fatal("no stream is live")
	return nil
}

// Decision (ADR-0135 §2, ADR-0041 §5): a filter that cannot keep up costs lines, never the
// reader: with the batcher allowed no time at all (it judges one line a tick) and a backlog
// past the pending cap, the lines lost unjudged are counted in `dropped`, and when the
// stream ends the drain judges what is pending and delivers it, so delivered + dropped
// equals the lines written and the stream still ends with its stopped event. Seen failing
// with the eviction uncounted (the sum falls short) and with a drain that judges nothing
// (almost nothing is delivered).
func TestWatchLogsFilterBudget(t *testing.T) {
	old := streamFilterBudget
	streamFilterBudget = 0
	t.Cleanup(func() { streamFilterBudget = old })

	const n = 15000
	w := newWatchFixture(t, map[string]string{
		uiSerial + ".date.out": "1791337500.000000\n",
		uiSerial + ".log":      wlHeader + wlLog(wlBase, 0, n, 1000, func(i int) string { return fmt.Sprintf("m%d", i) }),
	})
	_, sub := w.start(t, w.params(`"since":"2026-10-07T00:00:00Z","match":["^App: m"]`))
	s := onlyStream(t, w)
	testtime.Eventually(t, testtime.Bound(60*time.Second), "the reader to read every line", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.seq == n
	})

	// The same session streams the device again: the first ends, drains, and says so.
	w.start(t, w.params(`"tail":1`))
	delivered, dropped := 0, 0
	var stopped StoppedEvent
	for done := false; !done; {
		e := next(t, sub)
		if e.Type == EventStopped {
			stopped, done = stoppedOf(t, e), true
			continue
		}
		le := linesOf(t, e)
		delivered += len(le.Lines)
		dropped += le.Dropped
	}
	if stopped.Reason != StopReplaced {
		t.Errorf("stopped %+v, want replaced: the stream must end cleanly", stopped)
	}
	if delivered+dropped != n {
		t.Errorf("delivered %d + dropped %d = %d, want the %d written", delivered, dropped, delivered+dropped, n)
	}
	if dropped == 0 || delivered < streamPendingCap/2 {
		t.Errorf("delivered %d, dropped %d: want the lines past the cap counted and the pending backlog judged and delivered by the drain", delivered, dropped)
	}
	w.sess.Close()
	w.waitIdle(t)
}

// Decision (ADR-0135 §4, plan m3i Q3): `processes` is resolved by one ps at the start, and a
// Start proc line grows the scope while the stream runs, but only the system's own: the
// line must be ActivityManager's, at the system uid. A process the ps did not list (and the
// Start proc line of a service that started later) has its lines delivered from that line
// on; a forged line, from an app's uid or under another tag, adds nothing. Seen failing with
// the pid set fixed at the start (c-new is not delivered), with every Start proc line
// trusted (d-forged and e-forged2 are delivered).
func TestWatchLogsProcesses(t *testing.T) {
	log := wlHeader +
		wlLineP(1, 1000, 4001, "Sys", "a-sys") + "\n" +
		wlLineP(2, 10146, 9999, "App", "b-other") + "\n" +
		wlLineP(3, 1000, 4001, "ActivityManager", "Start proc 7777:com.example.new/u0a146 for service {x/y}") + "\n" +
		wlLineP(4, 10146, 7777, "App", "c-new") + "\n" +
		wlLineP(5, 10146, 8888, "ActivityManager", "Start proc 8888:system_server/u0 for x") + "\n" + // an app's forgery
		wlLineP(6, 10146, 8888, "App", "d-forged") + "\n" +
		wlLineP(7, 1000, 4001, "Evil", "Start proc 8889:com.example.new/u0 for x") + "\n" + // the system uid, not its tag
		wlLineP(8, 10146, 8889, "App", "e-forged2") + "\n" +
		wlLineP(9, 1000, 4001, "Sys", "z-end") + "\n"
	files := map[string]string{
		uiSerial + ".log":    log,
		uiSerial + ".ps.out": "  PID NAME\n 4001 system_server\n 5555 other\n",
	}

	t.Run("a trusted Start proc line adds its pid, a forged one nothing", func(t *testing.T) {
		w := newWatchFixture(t, files)
		res, sub := w.start(t, w.params(`"tail":100,"processes":["system_server","com.example.new"]`))
		want := []LogProcess{{Name: "system_server", PIDs: []int{4001}}, {Name: "com.example.new", PIDs: []int{}}}
		if !reflect.DeepEqual(res.Processes, want) {
			t.Errorf("result processes %+v; want %+v (what the ps resolved when the stream began)", res.Processes, want)
		}
		var events []LinesEvent
		for last := false; !last; {
			le := linesOf(t, next(t, sub))
			events = append(events, le)
			for _, l := range le.Lines {
				last = last || l.Message == "z-end"
			}
		}
		var got []string
		for _, e := range events {
			for _, l := range e.Lines {
				got = append(got, l.Message)
			}
		}
		// The two Start proc messages delivered are system_server's own lines, in scope by
		// its pid; the forged ones' processes (8888, 8889) have no line here.
		wantMsgs := []string{
			"a-sys",
			"Start proc 7777:com.example.new/u0a146 for service {x/y}",
			"c-new",
			"Start proc 8889:com.example.new/u0 for x",
			"z-end",
		}
		if !slices.Equal(got, wantMsgs) {
			t.Errorf("lines %q; want %q", got, wantMsgs)
		}
		calls := strings.Join(devicetest.Calls(t, w.dir), "\n")
		if strings.Count(calls, "ps -A -o PID,NAME") != 1 {
			t.Errorf("calls %q; want exactly one ps", calls)
		}
	})

	t.Run("a ps that cannot be read fails the start", func(t *testing.T) {
		bad := map[string]string{uiSerial + ".log": log, uiSerial + ".ps.exit": "1"}
		w := newWatchFixture(t, bad)
		out, _ := w.startOn(t, w.sess, w.params(`"processes":["system_server"]`))
		if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != ErrLogsFailed ||
			!strings.Contains(out.Error.Message, "the device's process list could not be read") {
			t.Fatalf("outcome %s %+v, want DEVICE_LOGS_FAILED naming the process list", out.State, out.Error)
		}
		w.waitIdle(t)
		if n := w.sess.Len(); n != 0 {
			t.Errorf("the failed start left %d subscriptions", n)
		}
		for _, c := range devicetest.Calls(t, w.dir) {
			if strings.Contains(c, "logcat") {
				t.Errorf("call %q: a failed start ran a logcat", c)
			}
		}
	})

	t.Run("a device that prints no uid column trusts no Start proc line", func(t *testing.T) {
		s, sk := bareStream(t, nil)
		s.procs = newProcessScope([]string{"com.example.new"})
		// The system's own Start proc line, but without the uid column to show whose it is.
		line := func(usec, pid int, tag, msg string) []byte {
			return []byte(fmt.Sprintf("%s.%06d %d %d I %s: %s", wlBase, usec, pid, pid, tag, msg))
		}
		for _, l := range [][]byte{
			line(1, 4001, "ActivityManager", "Start proc 7777:com.example.new/u0a146 for service"),
			line(2, 7777, "App", "c-new"),
		} {
			if err := s.onLine(l); err != nil {
				t.Fatal(err)
			}
		}
		if s.procs.has(7777) {
			t.Error("a Start proc line was trusted on a device with no uid column")
		}
		s.drain()
		if got, dropped := sk.snapshot(); len(got) != 0 || dropped != 0 {
			t.Errorf("delivered %v, dropped %d; want no line: nothing is in scope", got, dropped)
		}
	})
}

// Decision (ADR-0135 §1): a pattern that does not compile is REQUEST_INVALID_PARAMS naming
// match[i] or exclude[i], before any adb command or host call, and leaves no subscription
// and no stream behind. Seen failing with the pattern never compiled (the call completed
// and a stream started).
func TestWatchLogsBadPatternStartsNothing(t *testing.T) {
	for _, params := range []string{`"match":["ok","("]`, `"exclude":["["]`} {
		t.Run(params, func(t *testing.T) {
			w := newWatchFixture(t, nil)
			out, _ := w.startOn(t, w.sess, w.params(params))
			if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != capability.ErrInvalidParams {
				t.Fatalf("outcome %s %+v, want REQUEST_INVALID_PARAMS", out.State, out.Error)
			}
			if text := out.Error.Message + " " + out.Error.Cause; !strings.Contains(text, "match[1]") && !strings.Contains(text, "exclude[0]") {
				t.Errorf("error %q does not name the pattern's field and index", text)
			}
			if c := devicetest.Calls(t, w.dir); len(c) != 0 {
				t.Errorf("adb was invoked %v for a call with a bad pattern", c)
			}
			if n := w.sess.Len(); n != 0 || w.adb.streams.Live() != 0 {
				t.Errorf("a refused call left %d subscriptions and %d streams", n, w.adb.streams.Live())
			}
		})
	}
}

// watchLogsParams040 is device.watchLogs as 0.40.0 declared it: the host an older droidyard
// runs. Decision (ADR-0135 §6, plan m3i step 5): every field the filter added is omitempty,
// so a call that sets none of them sends an older host exactly the keys it knows, and an
// older decoder refuses each new one by its name.
type watchLogsParams040 struct {
	Device        string `json:"device" jsonschema:"required"`
	Tail          int    `json:"tail,omitempty"`
	Since         string `json:"since,omitempty"`
	ApplicationID string `json:"applicationId,omitempty"`
}

func TestWatchLogsParamsStayCompatibleWithAnOlderHost(t *testing.T) {
	reg := capability.NewRegistry()
	reg.MustRegister(capability.Descriptor{
		Name: "device.watchLogs", Kind: capability.Command, Scope: capability.Global, Description: "the 0.40.0 device.watchLogs.",
		Params: watchLogsParams040{}, Result: WatchLogsResult{},
		Handler: func(context.Context, *capability.Call) (any, error) { return WatchLogsResult{}, nil },
	})
	ops := capability.NewOperations(reg)
	ops.RegisterCapabilities()
	for _, field := range []string{"excludeTags", "tagPriorities", "match", "exclude", "processes"} {
		_, err := ops.Start(context.Background(), capability.Request{Capability: "device.watchLogs", Params: []byte(`{"device":"s","` + field + `":["x"]}`)})
		e := capability.AsError(err)
		if e.Code != capability.ErrInvalidParams || e.Cause != `json: unknown field "`+field+`"` {
			t.Errorf("%s on an older decoder: %+v; want REQUEST_INVALID_PARAMS with the unknown-field cause", field, e)
		}
	}

	old := map[string]bool{}
	frozen := reflect.TypeOf(watchLogsParams040{})
	for i := 0; i < frozen.NumField(); i++ {
		name, _, _ := strings.Cut(frozen.Field(i).Tag.Get("json"), ",")
		old[name] = true
	}
	var walk func(reflect.Type)
	walk = func(rt reflect.Type) {
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			if f.Anonymous && name == "" {
				walk(f.Type)
				continue
			}
			if !old[name] && !strings.Contains(opts, "omitempty") {
				t.Errorf("WatchLogsParams.%s (json %q) is new since 0.40.0 and not omitempty", f.Name, name)
			}
			delete(old, name)
		}
	}
	walk(reflect.TypeOf(WatchLogsParams{}))
	if len(old) != 0 {
		t.Errorf("0.40.0 parameters %v are gone from WatchLogsParams: that is a breaking change", old)
	}
	raw, err := hostParams(WatchLogsParams{Device: "qa/S1", Tail: 5, ApplicationID: appID}, "S1")
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"applicationId":"` + appID + `","device":"S1","tail":5}`; string(raw) != want {
		t.Errorf("hostParams = %s; want exactly the keys of 0.40.0: %s", raw, want)
	}
}

// Decision (ADR-0135 §6): the processes a host reports for a relayed stream are bounded as
// the request bounds them, whatever the host sends; absent they stay absent, so an older
// host's answer decodes.
func TestBoundedProcessesBoundsAHostsReport(t *testing.T) {
	if got := boundedProcesses(nil); got != nil {
		t.Errorf("no report became %v; want it absent", got)
	}
	var in []LogProcess
	for i := 0; i < logsProcessesMax+5; i++ {
		pids := make([]int, logsProcessPIDsMax+10)
		in = append(in, LogProcess{Name: strings.Repeat("n", logsProcessNameMax+50), PIDs: pids})
	}
	in[0].PIDs = nil
	got := boundedProcesses(in)
	if len(got) != logsProcessesMax {
		t.Fatalf("%d processes; want at most %d", len(got), logsProcessesMax)
	}
	for i, p := range got {
		if len(p.Name) > logsProcessNameMax || len(p.PIDs) > logsProcessPIDsMax || p.PIDs == nil {
			t.Errorf("process %d: %d-byte name, pids %v; want the bounds and a non-nil list", i, len(p.Name), len(p.PIDs))
		}
	}
}
