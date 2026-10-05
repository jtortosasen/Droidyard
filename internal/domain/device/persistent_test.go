package device

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/device/reader"
)

// mustErrorCode asserts an error's code, for the paths that call the client directly.
func mustErrorCode(t *testing.T, err error, code capability.ErrorCode) *capability.Error {
	t.Helper()
	ce := capability.AsError(err)
	if ce == nil || ce.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
	return ce
}

func readerTarget(t *testing.T, a *ADB, serial string) *target {
	t.Helper()
	return &target{adb: a.Resolve().Path, serial: serial}
}

// Decision (plan m1f step 5, check a; R2): the reader client's per-device lock is what
// keeps one device's reads from overlapping at the reader, while two devices read in
// parallel. Driven directly, two goroutines on one device never have two connections at
// the fake reader; on two devices they do. Seen failing with the lock removed (Max 2 on
// one device).
func TestReaderClientSerialisesPerDeviceAndNotTwoDevices(t *testing.T) {
	// One device.
	log1 := &devicetest.ReaderLog{}
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	r.SetLog(log1)
	r.SetDelay(60 * time.Millisecond)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := f.adb.read(context.Background(), readerTarget(t, f.adb, uiSerial)); err != nil {
				t.Errorf("read: %v", err)
			}
		}()
	}
	wg.Wait()
	if m := log1.Max(); m != 1 {
		t.Errorf("%d reads to one device overlapped at the reader, want 1 (the per-device lock)", m)
	}

	// Two devices, one shared log: their reads may be in flight together.
	log2 := &devicetest.ReaderLog{}
	rA := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(rA.Close)
	rA.SetLog(log2)
	rA.SetDelay(60 * time.Millisecond)
	rB := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(rB.Close)
	rB.SetLog(log2)
	rB.SetDelay(60 * time.Millisecond)
	f2 := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":     "0",
		uiSerial + ".start.out":     "",
		uiSerial + ".forward.out":   rA.Port,
		"emulator-5556.push.exit":   "0",
		"emulator-5556.start.out":   "",
		"emulator-5556.forward.out": rB.Port,
	})
	for _, serial := range []string{uiSerial, "emulator-5556"} {
		wg.Add(1)
		go func(serial string) {
			defer wg.Done()
			if _, _, err := f2.adb.read(context.Background(), readerTarget(t, f2.adb, serial)); err != nil {
				t.Errorf("read %s: %v", serial, err)
			}
		}(serial)
	}
	wg.Wait()
	if m := log2.Max(); m != 2 {
		t.Errorf("reads to two devices reached the readers %d at a time, want 2", m)
	}
}

// Decision (plan m1f step 5, check a; B3): the per-device lock's wait is bounded by the
// read's own deadline, not unbounded. A client held by another caller makes a read fail
// inside dumpDeadline; seen failing with an unbounded wait (the read would hang).
func TestReadWaitsInsideTheDeadlineForAReaderHeldElsewhere(t *testing.T) {
	old := dumpDeadline
	dumpDeadline = 300 * time.Millisecond
	defer func() { dumpDeadline = old }()
	f := newUIFixture(t, withPersistentReader(t, nil, pixelDoc(t)))

	c := f.adb.reader(uiSerial)
	if err := c.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.release()

	start := time.Now()
	_, _, err := f.adb.read(context.Background(), readerTarget(t, f.adb, uiSerial))
	if d := time.Since(start); d > dumpDeadline+300*time.Millisecond {
		t.Errorf("a read behind a held lock took %v, want the %v deadline", d, dumpDeadline)
	}
	ce := mustErrorCode(t, err, ErrUIDumpFailed)
	if !strings.Contains(ce.Cause, "busy") {
		t.Errorf("cause %q does not name the busy reader", ce.Cause)
	}
	if !strings.Contains(ce.NextStep, "another read of this device is in progress") {
		t.Errorf("nextStep %q does not name the other read", ce.NextStep)
	}
	if strings.Contains(ce.NextStep, "animating") {
		t.Errorf("nextStep %q is the dump's, not the lock wait's", ce.NextStep)
	}
}

// Decision (plan m1f step 5, check a; ADR-0030: started again when gone): a reader whose
// forward answers with no document is started again once, on the same forward, and the
// read succeeds with no warning. Seen failing with no restart (the read would fall to
// the one-shot).
func TestReaderGoneIsStartedAgainOnce(t *testing.T) {
	r := devicetest.StartReader(t, devicetest.ReaderGone, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	s := mustComplete(t, f.tree(t, uiSerial))
	if s.Foreground != "com.android.settings" || len(s.Nodes) == 0 {
		t.Fatalf("snapshot %+v", s)
	}
	if len(s.Warnings) != 0 {
		t.Errorf("warnings %q: a restart is not a fallback", s.Warnings)
	}
	calls := f.calls(t)
	if n := countServeStarts(calls); n != 2 {
		t.Errorf("%d serve starts, want the original and the one restart (calls %q)", n, calls)
	}
	if n := count(calls, prPushCall); n != 1 {
		t.Errorf("%d pushes, want 1: the restart reuses the dex (calls %q)", n, calls)
	}
	if n := countForward(calls); n != 1 {
		t.Errorf("%d forwards, want 1: the restart reuses the forward (calls %q)", n, calls)
	}
	if n := count(calls, rdRunCall); n != 0 {
		t.Errorf("the one-shot ran although the restart worked: %q", calls)
	}
	if r.Dumps() != 2 {
		t.Errorf("the reader saw %d dumps, want the vanished one and the tree", r.Dumps())
	}
}

// Decision (plan m1f step 5, check a; D3): when the restart fails, the one-shot reader
// runs, and the result carries a warning that names, in its text, why the persistent
// reader failed: the reader's own line, not only the warning's prefix.
func TestReaderRestartFailureFallsBackToOneShotWithAWarning(t *testing.T) {
	r := devicetest.StartReader(t, devicetest.ReaderGone)
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":    "0",
		uiSerial + ".start.out":    "",
		uiSerial + ".start#2.err":  "droidyard-reader: serve died\n",
		uiSerial + ".start#2.exit": "1",
		uiSerial + ".forward.out":  r.Port,
		uiSerial + ".reader.out":   pixelDoc(t),
	})
	s := mustComplete(t, f.tree(t, uiSerial))
	if len(s.Nodes) == 0 {
		t.Fatal("no tree from the one-shot fallback")
	}
	if len(s.Warnings) != 1 || !strings.HasPrefix(s.Warnings[0], oneShotWarning) {
		t.Errorf("warnings %q, want one starting with %q", s.Warnings, oneShotWarning)
	}
	if !strings.Contains(s.Warnings[0], "droidyard-reader: serve died") {
		t.Errorf("warning %q does not carry the reader's reason", s.Warnings[0])
	}
	if n := count(f.calls(t), rdRunCall); n != 1 {
		t.Errorf("the one-shot ran %d times, want 1: %q", n, f.calls(t))
	}
}

// Decision (plan m1f step 5, check a; ADR-0029/D3): when the one-shot also fails, the
// uiautomator dump runs and its warning names both reasons: the one-shot's and the
// persistent reader's.
func TestReaderOneShotFailureFallsBackToTheDumpWithItsWarning(t *testing.T) {
	r := devicetest.StartReader(t, devicetest.ReaderGone)
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":    "0",
		uiSerial + ".start.out":    "",
		uiSerial + ".start#2.err":  "droidyard-reader: serve died\n",
		uiSerial + ".start#2.exit": "1",
		uiSerial + ".forward.out":  r.Port,
		// No .reader files: the one-shot's app_process produces no hierarchy.
	})
	s := mustComplete(t, f.tree(t, uiSerial))
	if len(s.Warnings) != 1 || !strings.HasPrefix(s.Warnings[0], fallbackWarning) {
		t.Errorf("warnings %q, want the dump's warning", s.Warnings)
	}
	if w := s.Warnings[0]; !strings.Contains(w, "exited with status 0 and no hierarchy") || !strings.Contains(w, "droidyard-reader: serve died") {
		t.Errorf("warning %q does not carry both the one-shot's and the persistent reader's reasons", w)
	}
	if n := count(f.calls(t), ttyCall); n != 1 {
		t.Errorf("%d dumps, want 1: %q", n, f.calls(t))
	}
}

// Decision (plan m1f step 5, check a; the warm read's own end bound): a warm read that
// hangs must be cut by the read's deadline, not by warmReadTimeout alone. A good first
// read primes the forward; then the reader is frozen, so the warm read, the one-shot and
// the dump all hang, and the whole chain still ends within dumpDeadline as
// DEVICE_UI_DUMP_FAILED. Seen failing with the warm read's `deadline = end` cap removed
// (the warm read would block for warmReadTimeout, 10 s).
func TestReaderChainHangsWithinTheDeadline(t *testing.T) {
	old := []time.Duration{dumpDeadline, cleanupReserve, warmReadTimeout, readerStartBound, readerForwardTimeout}
	dumpDeadline, cleanupReserve, warmReadTimeout, readerStartBound, readerForwardTimeout =
		500*time.Millisecond, 100*time.Millisecond, 10*time.Second, 10*time.Second, 10*time.Second
	defer func() {
		dumpDeadline, cleanupReserve, warmReadTimeout, readerStartBound, readerForwardTimeout =
			old[0], old[1], old[2], old[3], old[4]
	}()
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":       "0",
		uiSerial + ".start.out":       "",
		uiSerial + ".forward.out":     r.Port,
		uiSerial + ".reader.hang":     "",
		uiSerial + ".dumptty.hang":    "",
		uiSerial + ".dumpsdcard.hang": "",
	})
	// The first read primes the client: the forward exists, so the timed read goes
	// straight to a warm dump instead of spending its budget on a start.
	if _, _, err := f.adb.read(context.Background(), readerTarget(t, f.adb, uiSerial)); err != nil {
		t.Fatalf("the priming read: %v", err)
	}
	r.SetDelay(10 * time.Second) // every later request hangs
	start := time.Now()
	_, _, err := f.adb.read(context.Background(), readerTarget(t, f.adb, uiSerial))
	if d := time.Since(start); d > dumpDeadline+300*time.Millisecond {
		t.Errorf("the chain took %v, over the %v deadline", d, dumpDeadline)
	}
	mustErrorCode(t, err, ErrUIDumpFailed)
}

// Decision (plan m1f step 5, check a; ADR-0030: the daemon stops it): Close quits every
// reader this daemon used and removes exactly its forward — never `forward
// --remove-all`, never another device's forward.
func TestCloseQuitsReadersAndRemovesOnlyItsForwards(t *testing.T) {
	rA := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(rA.Close)
	rB := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(rB.Close)
	f := newUIFixture(t, map[string]string{
		"devices.out":                     uiList(phoneLine, tabletLine),
		uiSerial + ".push.exit":           "0",
		uiSerial + ".start.out":           "",
		uiSerial + ".forward.out":         rA.Port,
		"emulator-5556.push.exit":         "0",
		"emulator-5556.start.out":         "",
		"emulator-5556.forward.out":       rB.Port,
		"emulator-5556.bootcompleted.out": "1\n",
	})
	mustComplete(t, f.tree(t, uiSerial))
	mustComplete(t, f.tree(t, "emulator-5556"))

	f.adb.Close(context.Background())
	if rA.Quits() != 1 || rB.Quits() != 1 {
		t.Errorf("quits = %d and %d, want 1 each", rA.Quits(), rB.Quits())
	}
	calls := f.calls(t)
	wantA := "-s " + uiSerial + " forward --remove tcp:" + rA.Port
	wantB := "-s emulator-5556 forward --remove tcp:" + rB.Port
	if count(calls, wantA) != 1 || count(calls, wantB) != 1 {
		t.Errorf("calls %q: want exactly one removal of each of our forwards (%q, %q)", calls, wantA, wantB)
	}
	for _, c := range calls {
		if strings.Contains(c, "--remove-all") {
			t.Errorf("Close issued %q", c)
		}
	}
}

// Decision (plan m1f step 5, check a): Close's per-device work is bounded, a hanging
// device included. A hanging `forward --remove` does not hold the daemon past
// readerCloseBound, and the other work (the reader's quit) still happens.
func TestCloseBoundsAHangingForwardRemove(t *testing.T) {
	old := readerCloseBound
	readerCloseBound = 300 * time.Millisecond
	defer func() { readerCloseBound = old }()
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":          "0",
		uiSerial + ".start.out":          "",
		uiSerial + ".forward.out":        r.Port,
		uiSerial + ".forwardremove.hang": "",
	})
	mustComplete(t, f.tree(t, uiSerial))

	start := time.Now()
	f.adb.Close(context.Background())
	if d := time.Since(start); d > readerCloseBound+300*time.Millisecond {
		t.Errorf("Close took %v with a hanging removal, want within %v", d, readerCloseBound)
	}
	if r.Quits() != 1 {
		t.Errorf("the reader saw %d quits, want 1 even with a hanging removal", r.Quits())
	}
}

// Decision (plan m1f step 5; ADR-0030: the dex stays, the fallback has its own file):
// the one-shot reader pushes and removes only its own dex, so a fallback never takes
// away the file the persistent reader starts from. After a fallback, a reader found
// gone is started again from that dex and the next read succeeds with no fallback. The
// fake models the device's files, so sharing one path shows: the restart's start fails
// against the removed file.
func TestOneShotFallbackDoesNotRemoveThePersistentReadersDex(t *testing.T) {
	r := devicetest.StartReader(t, devicetest.ReaderGone, devicetest.ReaderGone, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":    "0",
		uiSerial + ".start.out":    "",
		uiSerial + ".start#2.err":  "droidyard-reader: serve died\n",
		uiSerial + ".start#2.exit": "1",
		uiSerial + ".forward.out":  r.Port,
		uiSerial + ".reader.out":   pixelDoc(t),
	})
	s1 := mustComplete(t, f.tree(t, uiSerial))
	if len(s1.Warnings) != 1 || !strings.HasPrefix(s1.Warnings[0], oneShotWarning) {
		t.Fatalf("read 1 warnings %q, want the one-shot's fallback", s1.Warnings)
	}
	s2 := mustComplete(t, f.tree(t, uiSerial))
	if len(s2.Nodes) == 0 {
		t.Fatal("read 2 produced no tree")
	}
	if len(s2.Warnings) != 0 {
		t.Errorf("read 2 warnings %q: the restart should have run the persistent reader", s2.Warnings)
	}
	calls := f.calls(t)
	if n := countServeStarts(calls); n != 3 {
		t.Errorf("%d serve starts, want the original, the failed restart and the successful restart (%q)", n, calls)
	}
	if n := count(calls, prRmCall); n != 0 {
		t.Errorf("the persistent reader's dex was removed: %q", calls)
	}
	if n := count(calls, rdRmCall); n != 1 {
		t.Errorf("%d one-shot removals, want 1 (%q)", n, calls)
	}
}

// Decision (plan m1f step 5; R5, D3): the persistent reader's no-root answer is
// DEVICE_UI_DUMP_FAILED with the fixed sentence, never the reader's device line, and
// neither the one-shot reader nor uiautomator runs: both would meet the same missing
// window.
func TestPersistentNoRootIsDumpFailedWithoutAFallback(t *testing.T) {
	r := devicetest.StartReader(t, "droidyard-reader: no active window root\n")
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	e := mustFail(t, f.tree(t, uiSerial), ErrUIDumpFailed)
	if !strings.Contains(e.Cause, "no active window to read") {
		t.Errorf("cause %q is not the fixed sentence", e.Cause)
	}
	if strings.Contains(e.Cause, "no active window root") {
		t.Errorf("cause %q quotes the reader's device line", e.Cause)
	}
	calls := f.calls(t)
	if n := count(calls, rdRunCall); n != 0 {
		t.Errorf("the one-shot reader ran: %q", calls)
	}
	if n := count(calls, ttyCall); n != 0 {
		t.Errorf("uiautomator ran: %q", calls)
	}
}

// Decision (plan m1f step 5): the start bound is measured from the start command, not
// from the read's start, so a slow push does not spend time the reader start has not
// used. The push outlasts readerStartBound; the read still gets its tree from the
// persistent reader. Seen failing measured from the read's start (the ping is already
// past its bound, so the read falls back).
func TestReaderStartBoundIsMeasuredFromTheStartCommand(t *testing.T) {
	old := readerStartBound
	readerStartBound = 200 * time.Millisecond
	defer func() { readerStartBound = old }()
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".push.delay":  "300", // longer than readerStartBound, before the start
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	s := mustComplete(t, f.tree(t, uiSerial))
	if len(s.Nodes) == 0 {
		t.Fatal("no tree")
	}
	if len(s.Warnings) != 0 {
		t.Errorf("warnings %q: the read fell back although the start command was within its bound", s.Warnings)
	}
	if n := countServeStarts(f.calls(t)); n != 1 {
		t.Errorf("%d serve starts, want 1", n)
	}
}

// Decision (plan m1f step 5; process-lifecycle.md §Shutdown): Close's own bound comes
// from context.WithoutCancel, so a shutdown context the operations drained still removes
// the forwards. Seen failing with Close inheriting the context (no removal is issued).
func TestCloseRemovesForwardsDespiteADrainedContext(t *testing.T) {
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	mustComplete(t, f.tree(t, uiSerial))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.adb.Close(ctx)
	want := "-s " + uiSerial + " forward --remove tcp:" + r.Port
	if n := count(f.calls(t), want); n != 1 {
		t.Errorf("the forward removal ran %d times with a drained context, want 1", n)
	}
	if r.Quits() != 1 {
		t.Errorf("the reader saw %d quits, want 1", r.Quits())
	}
}

// Decision (plan m1f step 5; E6): a reader that was started but never forwarded cannot
// be reached over a local port, so Close stops it by its process name only: pidof
// droidyard-reader-<d12>, then kill each pid.
func TestCloseKillsAStartedButNeverForwardedReader(t *testing.T) {
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":    "0",
		uiSerial + ".start.out":    "",
		uiSerial + ".forward.exit": "1", // the forward fails: no port, but the reader started
		uiSerial + ".reader.out":   pixelDoc(t),
		uiSerial + ".pidof.out":    "4242\n",
	})
	mustComplete(t, f.tree(t, uiSerial))
	f.adb.Close(context.Background())
	calls := f.calls(t)
	name := "droidyard-reader-" + reader.SHA256[:12]
	if n := count(calls, "-s "+uiSerial+" shell pidof "+name); n != 1 {
		t.Errorf("pidof %s ran %d times, want 1 (%q)", name, n, calls)
	}
	if n := count(calls, "-s "+uiSerial+" shell kill 4242"); n != 1 {
		t.Errorf("kill 4242 ran %d times, want 1 (%q)", n, calls)
	}
	for _, c := range calls {
		if strings.Contains(c, "kill") && !strings.Contains(c, "kill 4242") {
			t.Errorf("Close killed more than its own reader: %q", c)
		}
	}
}

// Decision (plan m1f step 5; process-lifecycle.md §Shutdown): a warm read blocked on its
// connection deadline watches the caller's context. When the context is cancelled the
// read returns promptly with that cancellation instead of waiting out warmReadTimeout.
// Seen failing without context.AfterFunc (the read waits out the deadline).
func TestReadObservesCancellationWhileTheWarmReadHangs(t *testing.T) {
	old := warmReadTimeout
	warmReadTimeout = 5 * time.Second
	defer func() { warmReadTimeout = old }()
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	// Prime the client, so the timed read goes straight to a warm dump.
	if _, _, err := f.adb.read(context.Background(), readerTarget(t, f.adb, uiSerial)); err != nil {
		t.Fatalf("the priming read: %v", err)
	}
	r.SetDelay(1200 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, _, err := f.adb.read(ctx, readerTarget(t, f.adb, uiSerial))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the caller's cancellation", err)
	}
	if d := time.Since(start); d > 800*time.Millisecond {
		t.Errorf("the read took %v after cancellation, want well under warmReadTimeout %v", d, warmReadTimeout)
	}
}
