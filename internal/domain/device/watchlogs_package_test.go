package device

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/event"
)

// The package a streamed line names (ADR-0051; plan m2e step 2), over the re-executed
// fake adb. The fake's `pm list packages -U` is scripted per call (pm#N.out, .delay,
// .exit, .hang) and records the pid of each call in <serial>.pm.pids, so a test can count
// the reads and tell a killed one is gone.

const (
	pkgApp   = "com.example.app"
	pkgOther = "com.example.other"
	pkgTable = "package:" + pkgApp + " uid:10146\n"
)

// pmReadCalls is how many package reads (pm list packages -U with no filter) the stream
// has made.
func pmReadCalls(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	for _, c := range devicetest.Calls(t, dir) {
		if c == "-s "+uiSerial+" shell pm list packages -U" {
			n++
		}
	}
	return n
}

// pmPids are the pids of the fake pm calls so far, in order.
func pmPids(t *testing.T, dir string) []int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, uiSerial+".pm.pids"))
	if err != nil {
		return nil
	}
	var pids []int
	for _, f := range strings.Fields(string(b)) {
		if n, err := strconv.Atoi(f); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}

// waitPMPids waits until the fake pm has been started n times, and returns its pids.
func (w *watchFixture) waitPMPids(t *testing.T, n int) []int {
	t.Helper()
	for deadline := time.Now().Add(wlWait); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if p := pmPids(t, w.dir); len(p) >= n {
			return p
		}
	}
	t.Fatalf("the fake pm was started %d times, want %d", len(pmPids(t, w.dir)), n)
	return nil
}

// nextWithin is next with a bound of its own.
func nextWithin(t *testing.T, sub *event.Subscription, d time.Duration) event.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	e, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("waiting %s for an event: %v", d, err)
	}
	return e
}

// readLines reads lines events until n lines were delivered, each event within d.
func readLines(t *testing.T, sub *event.Subscription, n int, d time.Duration) (lines []StreamLine, dropped int) {
	t.Helper()
	for len(lines) < n {
		le := linesOf(t, nextWithin(t, sub, d))
		lines = append(lines, le.Lines...)
		dropped += le.Dropped
	}
	return lines, dropped
}

// uidLine is a log line of the given uid column text (a number, or a name like root).
func uidLine(ts, uid, msg string) string {
	return fmt.Sprintf("%s %s 6406 6406 I App: %s\n", ts, uid, msg)
}

// waitReaped requires the stream out of the set and the pm child reaped at the moment it
// leaves it: the set is polled tightly and the child checked at the first sight of zero,
// not after a settling pause that would hide a stream that leaves before its child is
// reaped.
func (w *watchFixture) requireReapedWhenGone(t *testing.T, pmPid int) {
	t.Helper()
	deadline := time.Now().Add(wlWait)
	for w.adb.streams.Live() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stream never left the set")
		}
	}
	if !gone(pmPid) {
		t.Errorf("the stream left the set with its pm child %d still running or unreaped", pmPid)
	}
}

// Decision (ADR-0051 §1): a line carries the one package pm lists for its application id
// (uid % 100000, 10000-19999 only), also under another user (1010146); a uid shared by two
// packages, a system uid, a named uid and a device without the uid column give no package,
// and the last three never cause a pm read. Seen failing with the application range
// widened (a system uid and root asked pm: calls were made) and with the id taken as the
// whole uid (1010146 unlabelled).
func TestWatchLogsLinesCarryPackage(t *testing.T) {
	t.Run("packages by application id", func(t *testing.T) {
		w := newWatchFixture(t, map[string]string{
			uiSerial + ".pm.out": pkgTable + "package:com.shared.a uid:10200\npackage:com.shared.b uid:10200\n",
			uiSerial + ".log": wlHeader +
				uidLine(wlBase+".000001", "10146", "m1") +
				uidLine(wlBase+".000002", "1010146", "m2") +
				uidLine(wlBase+".000003", "10200", "m3") +
				uidLine(wlBase+".000004", "1000", "m4") +
				uidLine(wlBase+".000005", "root", "m5") +
				uidLine(wlBase+".000006", "10146", "m6"),
		})
		_, sub := w.start(t, w.params(`"tail":100`))
		lines, dropped := readLines(t, sub, 6, wlWait)
		want := map[string]string{"m1": pkgApp, "m2": pkgApp, "m3": "", "m4": "", "m5": "", "m6": pkgApp}
		for i, l := range lines {
			if m := fmt.Sprintf("m%d", i+1); l.Message != m {
				t.Fatalf("line %d is %q, want %q: the order is the log's", i, l.Message, m)
			}
			if l.Package != want[l.Message] {
				t.Errorf("%s has package %q, want %q", l.Message, l.Package, want[l.Message])
			}
		}
		if dropped != 0 {
			t.Errorf("dropped %d", dropped)
		}
		if n := pmReadCalls(t, w.dir); n != 1 {
			t.Errorf("%d package reads, want 1: the first read lists every package", n)
		}
	})

	t.Run("system and named uids never ask pm", func(t *testing.T) {
		w := newWatchFixture(t, map[string]string{
			uiSerial + ".pm.out": pkgTable,
			uiSerial + ".log": wlHeader +
				uidLine(wlBase+".000001", "1000", "s1") +
				uidLine(wlBase+".000002", "root", "s2") +
				uidLine(wlBase+".000003", "0", "s3") +
				uidLine(wlBase+".000004", "9999", "s4") +
				uidLine(wlBase+".000005", "20000", "s5"),
		})
		_, sub := w.start(t, w.params(`"tail":100`))
		lines, _ := readLines(t, sub, 5, wlWait)
		for _, l := range lines {
			if l.Package != "" {
				t.Errorf("%s has package %q, want none", l.Message, l.Package)
			}
		}
		if n := pmReadCalls(t, w.dir); n != 0 {
			t.Errorf("%d package reads for system and named uids, want 0", n)
		}
	})

	t.Run("a device without the uid column", func(t *testing.T) {
		w := newWatchFixture(t, map[string]string{
			uiSerial + ".pm.out": pkgTable,
			uiSerial + ".nouid":  "",
			uiSerial + ".log":    wlHeader + wlLog(wlBase, 0, 3, 10146, func(i int) string { return fmt.Sprintf("n%d", i) }),
		})
		_, sub := w.start(t, w.params(`"tail":100`))
		lines, _ := readLines(t, sub, 3, wlWait)
		for _, l := range lines {
			if l.Package != "" {
				t.Errorf("%s has package %q, want none without the uid column", l.Message, l.Package)
			}
		}
		if n := pmReadCalls(t, w.dir); n != 0 {
			t.Errorf("%d package reads without a uid column, want 0", n)
		}
	})
}

// Decision (ADR-0051 §2): a line waits for the read its application id started, so the
// first line of a fresh install is named, and lines keep their order: a later line of
// another uid does not overtake the held one. Seen failing without the hold (the first
// line went out at once with no package, and the system line first or beside it).
func TestWatchLogsHoldsUntilRefresh(t *testing.T) {
	const delay = 700 * time.Millisecond
	w := newWatchFixture(t, map[string]string{
		uiSerial + ".pm.out":     pkgTable,
		uiSerial + ".pm#1.delay": strconv.Itoa(int(delay / time.Millisecond)),
		uiSerial + ".pm#1.out":   pkgTable,
		uiSerial + ".log":        wlHeader + uidLine(wlBase+".000001", "10146", "m1") + uidLine(wlBase+".000002", "1000", "m2") + uidLine(wlBase+".000003", "10146", "m3"),
	})
	begun := time.Now()
	_, sub := w.start(t, w.params(`"tail":100`))
	first := linesOf(t, next(t, sub))
	if took := time.Since(begun); took < delay-100*time.Millisecond {
		t.Errorf("the first event came after %s, before pm (%s) answered", took, delay)
	}
	lines := first.Lines
	if more, _ := readLines(t, sub, 3-len(lines), wlWait); len(more) > 0 {
		lines = append(lines, more...)
	}
	var order []string
	for _, l := range lines {
		order = append(order, l.Message+"="+l.Package)
	}
	if got := strings.Join(order, " "); got != "m1="+pkgApp+" m2= m3="+pkgApp {
		t.Errorf("lines %q, want m1 named first, m2 behind it, m3 named", got)
	}
}

// Decision (ADR-0051 §2): reads are bounded, one at a time and at most one a second, and an
// application id a completed read does not list is entered as none and never asked again.
// Fifty unseen ids appearing over about a second cost no more than the gap allows, and ids
// pm does not list cause no further read afterwards. Seen failing without the gap (a read
// per batch of new ids) and without the none entry (a read every second for the same ids).
func TestWatchLogsRefreshIsBounded(t *testing.T) {
	w := newWatchFixture(t, map[string]string{
		uiSerial + ".pm.out": "package:" + pkgOther + " uid:10999\n",
		uiSerial + ".log":    wlHeader + uidLine(wlBase+".000000", "10100", "seed"),
	})
	begun := time.Now()
	_, sub := w.start(t, w.params(`"tail":100`))
	log := wlHeader + uidLine(wlBase+".000000", "10100", "seed")
	// 49 more unseen ids, five every 100 ms.
	for i := 1; i < 50; i++ {
		log += uidLine(fmt.Sprintf("%s.%06d", wlBase, i), strconv.Itoa(10100+i), fmt.Sprintf("u%d", i))
		if i%5 == 0 {
			w.setLog(t, log)
			time.Sleep(100 * time.Millisecond)
		}
	}
	w.setLog(t, log)
	lines, _ := readLines(t, sub, 50, wlWait)
	for _, l := range lines {
		if l.Package != "" {
			t.Errorf("%s has package %q: pm lists none of these ids", l.Message, l.Package)
		}
	}
	elapsed := time.Since(begun)
	calls := pmReadCalls(t, w.dir)
	if bound := 1 + int(elapsed/packagesRefreshGap); calls > bound {
		t.Errorf("%d package reads in %s, want at most %d (one a second)", calls, elapsed.Round(time.Millisecond), bound)
	}
	if calls < 1 {
		t.Fatal("no package read")
	}

	// Every id is now none: more lines of them, past the gap, ask nothing.
	before := pmReadCalls(t, w.dir)
	time.Sleep(packagesRefreshGap + 300*time.Millisecond)
	log += uidLine(wlBase+".000900", "10100", "again1") + uidLine(wlBase+".000901", "10120", "again2")
	w.setLog(t, log)
	readLines(t, sub, 2, wlWait)
	time.Sleep(packagesRefreshGap + 300*time.Millisecond)
	if after := pmReadCalls(t, w.dir); after != before {
		t.Errorf("%d package reads after the ids were entered as none, were %d: an id pm does not list was asked for again", after, before)
	}
}

// Decision (ADR-0051 §2): a read the gap deferred is started by the batcher's tick, so a
// held line never waits on a later unseen uid; and an id first seen while a read ran is
// not entered as none by it, but asked for by the next read. Seen failing with the read
// triggered only when a line is queued (the deferred line was never published: the lines
// after it were all of known uids) and with every id seen by a read's end entered as none
// (the second read never ran and the line went out unnamed).
func TestWatchLogsDeferredRefreshRuns(t *testing.T) {
	t.Run("a read deferred by the gap starts on the tick", func(t *testing.T) {
		w := newWatchFixture(t, map[string]string{
			uiSerial + ".pm#1.out": pkgTable,
			uiSerial + ".pm#2.out": pkgTable + "package:" + pkgOther + " uid:10147\n",
			uiSerial + ".log":      wlHeader + uidLine(wlBase+".000000", "10146", "a1"),
		})
		_, sub := w.start(t, w.params(`"tail":100`))
		if l, _ := readLines(t, sub, 1, wlWait); l[0].Package != pkgApp {
			t.Fatalf("a1 has package %q, want %q", l[0].Package, pkgApp)
		}
		time.Sleep(300 * time.Millisecond)
		appended := time.Now()
		// An unseen id, then only ids the stream knows.
		w.setLog(t, wlHeader+
			uidLine(wlBase+".000000", "10146", "a1")+
			uidLine("1791337100.000001", "10147", "b1")+
			uidLine("1791337100.000002", "10146", "a2")+
			uidLine("1791337100.000003", "10146", "a3"))
		lines, _ := readLines(t, sub, 3, packagesRefreshGap+logsPMTimeout)
		if took := time.Since(appended); took > packagesRefreshGap+logsPMTimeout {
			t.Errorf("b1 waited %s, want at most the gap plus one read", took)
		}
		var got []string
		for _, l := range lines {
			got = append(got, l.Message+"="+l.Package)
		}
		if s := strings.Join(got, " "); s != "b1="+pkgOther+" a2="+pkgApp+" a3="+pkgApp {
			t.Errorf("lines %q, want b1 named by the second read, then a2 and a3", s)
		}
		if n := pmReadCalls(t, w.dir); n != 2 {
			t.Errorf("%d package reads, want 2", n)
		}
	})

	t.Run("an id first seen during a read is asked for by the next one", func(t *testing.T) {
		w := newWatchFixture(t, map[string]string{
			// The first read is slow and does not list 10300.
			uiSerial + ".pm#1.delay": "1500",
			uiSerial + ".pm#1.out":   pkgTable,
			uiSerial + ".pm#2.out":   pkgTable + "package:" + pkgOther + " uid:10300\n",
			uiSerial + ".log":        wlHeader + uidLine(wlBase+".000000", "10146", "a1"),
		})
		_, sub := w.start(t, w.params(`"tail":100`))
		w.waitPMPids(t, 1) // the first read is running
		w.setLog(t, wlHeader+
			uidLine(wlBase+".000000", "10146", "a1")+
			uidLine("1791337100.000001", "10300", "c1"))
		lines, _ := readLines(t, sub, 2, wlWait)
		if lines[0].Message != "a1" || lines[0].Package != pkgApp {
			t.Errorf("first line %+v, want a1 named %s", lines[0], pkgApp)
		}
		if lines[1].Message != "c1" || lines[1].Package != pkgOther {
			t.Errorf("second line %+v, want c1 named %s by the second read", lines[1], pkgOther)
		}
		if n := pmReadCalls(t, w.dir); n != 2 {
			t.Errorf("%d package reads, want 2", n)
		}
	})
}

// Decision (ADR-0051 §2): a failed read enters nothing and releases the lines held for it
// without a package; the stream keeps going (no stopped event); later lines of that id do
// not wait for the retry; the next read resolves the id. Seen failing with the failure
// entering the asked ids as none (no second read ran), and with later lines of a failed id
// waiting (a2 held until the retry answered).
func TestWatchLogsPMFailure(t *testing.T) {
	w := newWatchFixture(t, map[string]string{
		uiSerial + ".pm#1.exit":  "1",
		uiSerial + ".pm#2.delay": "3000",
		uiSerial + ".pm#2.out":   pkgTable,
		uiSerial + ".log":        wlHeader + uidLine(wlBase+".000000", "10146", "a1"),
	})
	_, sub := w.start(t, w.params(`"tail":100`))
	first, _ := readLines(t, sub, 1, wlWait)
	if first[0].Message != "a1" || first[0].Package != "" {
		t.Fatalf("first line %+v, want a1 without a package: the read failed", first[0])
	}
	retry := w.waitPMPids(t, 2)[1] // the retry runs, slowly
	// A later line of the failed id does not wait for the retry (review N1: each line of
	// it, and every line behind it, would otherwise wait again for every retry).
	log := wlHeader + uidLine(wlBase+".000000", "10146", "a1") + uidLine("1791337100.000001", "10146", "a2")
	w.setLog(t, log)
	appended := time.Now()
	// readLines fails the test on any event that is not lines: no stopped.
	second, _ := readLines(t, sub, 1, wlWait)
	if took := time.Since(appended); second[0].Message != "a2" || second[0].Package != "" || took > 1500*time.Millisecond {
		t.Errorf("second line %+v after %s, want a2 without a package at once, not held for the retry", second[0], took)
	}
	// The retry resolves the id for the lines after it: wait for its child to end, then for
	// the read to record what it got.
	for deadline := time.Now().Add(wlWait); !gone(retry); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the retry's pm never ended")
		}
	}
	time.Sleep(200 * time.Millisecond)
	w.setLog(t, log+uidLine("1791337100.000002", "10146", "a3"))
	third, _ := readLines(t, sub, 1, wlWait)
	if third[0].Message != "a3" || third[0].Package != pkgApp {
		t.Errorf("third line %+v, want a3 named %s by the retry", third[0], pkgApp)
	}
	if n := pmReadCalls(t, w.dir); n != 2 {
		t.Errorf("%d package reads, want 2: the failed one and its retry", n)
	}
}

// Decision (ADR-0051 §2): a pending buffer that reaches streamPendingCap with its oldest
// line held for a read releases the held lines without a package rather than drop them.
// Seen failing with the release removed (the held lines were dropped: dropped over 0 and
// no m0 among the first published lines).
func TestWatchLogsHeldLinesAtTheCap(t *testing.T) {
	w := newWatchFixture(t, map[string]string{
		uiSerial + ".pm.hang": "",
		uiSerial + ".log":     wlHeader + wlLog(wlBase, 0, 5000, 10146, func(i int) string { return fmt.Sprintf("m%d", i) }),
	})
	res, sub := w.start(t, w.params(`"tail":5000`))
	w.waitPMPids(t, 1)
	// 6,000 more lines, past the cap, while the read hangs.
	w.setLog(t, wlHeader+wlLog(wlBase, 0, 11000, 10146, func(i int) string { return fmt.Sprintf("m%d", i) }))
	le := linesOf(t, nextWithin(t, sub, 4*time.Second)) // before the read's own timeout
	if len(le.Lines) == 0 || le.Lines[0].Message != "m0" || le.Lines[0].Package != "" {
		t.Errorf("first published line %+v, want m0 without a package", le.Lines)
	}
	if le.Dropped != 0 {
		t.Errorf("dropped %d: the held lines were dropped instead of released", le.Dropped)
	}
	w.sess.Unsubscribe(res.SubscriptionID)
	w.waitIdle(t, w.waitPMPids(t, 1)...)
}

// Decision (ADR-0051 §2, ADR-0041 §6): no pm child or goroutine outlives its stream: run
// waits for the read, its child reaped, before the stream leaves the set. (a) A logcat
// that exits while pm hangs publishes the held lines (without a package) before stopped.
// (b) An unsubscribe while pm hangs publishes nothing. In both the pm child is gone at the
// moment the stream leaves the set. Seen failing without the join (the stream left the set
// with its pm child alive) and, for (a), with drain waiting for the read (no event
// before the read's own timeout).
func TestWatchLogsEndsDuringRefresh(t *testing.T) {
	files := func() map[string]string {
		return map[string]string{
			uiSerial + ".pm.hang": "",
			uiSerial + ".log":     wlHeader + uidLine(wlBase+".000000", "10146", "a1") + uidLine(wlBase+".000001", "10146", "a2"),
		}
	}

	t.Run("logcat exits while pm hangs", func(t *testing.T) {
		w := newWatchFixture(t, files())
		_, sub := w.start(t, w.params(`"tail":100`))
		rec := w.waitRecords(t, 1)[0]
		pm := w.waitPMPids(t, 1)[0]
		devicetestWrite(t, w.dir, uiSerial+".follow.exitcode", "7")
		got, _ := readLines(t, sub, 2, 3*time.Second) // before the read's own timeout
		for _, l := range got {
			if l.Package != "" {
				t.Errorf("%s has package %q, want none: the read never answered", l.Message, l.Package)
			}
		}
		if stopped := stoppedOf(t, nextWithin(t, sub, 3*time.Second)); stopped.Reason != StopExited {
			t.Errorf("stopped %+v, want exited", stopped)
		}
		w.requireReapedWhenGone(t, pm)
		w.waitIdle(t, rec.PID, pm)
	})

	t.Run("unsubscribe while pm hangs", func(t *testing.T) {
		w := newWatchFixture(t, files())
		res, sub := w.start(t, w.params(`"tail":100`))
		rec := w.waitRecords(t, 1)[0]
		pm := w.waitPMPids(t, 1)[0]
		if e, ok := sub.TryNext(); ok {
			t.Fatalf("an event %s/%s arrived while the lines were held", e.Topic, e.Type)
		}
		w.sess.Unsubscribe(res.SubscriptionID)
		w.requireReapedWhenGone(t, pm)
		w.waitIdle(t, rec.PID, pm)
		if e, ok := sub.TryNext(); ok {
			t.Errorf("an event %s/%s was published after the unsubscribe", e.Topic, e.Type)
		}
	})
}

// A name over packageNameCap is unreadable, and its id shares it: the id gets none rather
// than the other package's name (verifier nit). Seen failing with the long line skipped.
func TestParsePackagesLongNameMakesTheIDAmbiguous(t *testing.T) {
	long := "com." + strings.Repeat("a", packageNameCap)
	got := parsePackages([]byte("package:com.example.app uid:10146\npackage:" + long + " uid:10146\npackage:com.example.other uid:10147\n"))
	if got[10146] != "" || got[10147] != pkgOther {
		t.Errorf("parsePackages = %v, want 10146 none and 10147 %s", got, pkgOther)
	}
}
