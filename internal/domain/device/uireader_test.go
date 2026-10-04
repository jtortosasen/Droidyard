package device

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/device/reader"
)

const (
	rdPushCall = "-s emulator-5554 push LOCAL /data/local/tmp/droidyard-reader-" + reader.SHA256 + ".dex"
	rdRunCall  = "-s emulator-5554 exec-out CLASSPATH=/data/local/tmp/droidyard-reader-" + reader.SHA256 + ".dex app_process /system/bin DroidyardReader"
	rdRmCall   = "-s emulator-5554 shell rm -f /data/local/tmp/droidyard-reader-" + reader.SHA256 + ".dex"
)

func pixelDoc(t *testing.T) string { return fixture(t, "pixel9a-uidump-devtty.xml") }

// Decision (ADR-0029: the reader is the path, uiautomator the fallback): with a reader
// that runs, the device sees the dex pushed to a file named for the pinned hash, the
// reader run from it, and the file removed, in that order; no uiautomator command runs;
// the result has no warning; and the bytes pushed are the embedded dex.
func TestUITreeReadsThroughTheReaderAndRemovesIt(t *testing.T) {
	f := newUIFixture(t, withReader(nil, pixelDoc(t)))
	s := mustComplete(t, f.tree(t, uiSerial))
	if s.Foreground != "com.android.settings" || len(s.Nodes) == 0 {
		t.Errorf("snapshot %+v", s)
	}
	if len(s.Warnings) != 0 {
		t.Errorf("warnings %q on the reader path", s.Warnings)
	}
	calls := f.calls(t)
	i, j, k := slices.Index(calls, rdPushCall), slices.Index(calls, rdRunCall), slices.Index(calls, rdRmCall)
	if i < 0 || !(i < j && j < k) {
		t.Errorf("calls %q: want push, run, then removal", calls)
	}
	for _, c := range calls {
		if strings.Contains(c, "uiautomator") {
			t.Errorf("uiautomator ran although the reader did: %q", c)
		}
	}
	pushed, err := os.ReadFile(filepath.Join(f.dir, uiSerial+".pushed"))
	if err != nil {
		t.Fatal(err)
	}
	if string(pushed) != string(reader.Dex()) {
		t.Errorf("pushed %d bytes that are not the embedded dex", len(pushed))
	}
}

// Decision (ADR-0029: fallback only when the reader cannot run, labelled): a reader
// that fails (app_process exits non-zero), and a push that fails, each end in the
// uiautomator dump, whose snapshot comes with exactly the suspension warning; the dex
// is removed in both; with a failed push the reader is never run. The warning ends with
// the reader's decisive line (its own, or adb's), never with other device output.
func TestUITreeFallsBackWhenTheReaderCannotRun(t *testing.T) {
	cases := []struct {
		name   string
		files  map[string]string
		ran    bool
		reason string
	}{
		{"app_process fails", map[string]string{uiSerial + ".push.exit": "0", uiSerial + ".reader.err": "droidyard-reader: no UiAutomation constructor\n", uiSerial + ".reader.exit": "1"}, true, "droidyard-reader: no UiAutomation constructor"},
		// exec-out merges app_process's stderr into stdout: the reader's own line is found there.
		{"reader line on stdout", map[string]string{uiSerial + ".push.exit": "0", uiSerial + ".reader.out": "droidyard-reader: java.lang.NoSuchMethodException: connect\n", uiSerial + ".reader.exit": "1"}, true, "droidyard-reader: java.lang.NoSuchMethodException: connect"},
		{"reader prints no hierarchy", map[string]string{uiSerial + ".push.exit": "0", uiSerial + ".reader.out": "oops\n"}, true, "it exited with status 0 and no hierarchy"},
		{"push fails", map[string]string{uiSerial + ".push.err": "adb: failed to copy\n", uiSerial + ".push.exit": "1"}, false, "adb: failed to copy"},
	}
	for _, c := range cases {
		f := newUIFixture(t, c.files)
		s := mustComplete(t, f.tree(t, uiSerial))
		if want := fallbackWarning + " Reader: " + c.reason; len(s.Nodes) == 0 || len(s.Warnings) != 1 || s.Warnings[0] != want {
			t.Errorf("%s: nodes %d, warnings %q; want the dump's snapshot with the one warning %q", c.name, len(s.Nodes), s.Warnings, want)
		}
		calls := f.calls(t)
		if got := slices.Contains(calls, rdRunCall); got != c.ran {
			t.Errorf("%s: reader run = %v, want %v (%q)", c.name, got, c.ran, calls)
		}
		if count(calls, ttyCall) != 1 {
			t.Errorf("%s: %d uiautomator dumps, want 1 (%q)", c.name, count(calls, ttyCall), calls)
		}
		if i, j := slices.Index(calls, rdRmCall), slices.Index(calls, ttyCall); i < 0 || i > j {
			t.Errorf("%s: the dex is removed before the fallback runs; calls %q", c.name, calls)
		}
	}
}

// Decision (ADR-0029 and plan step 10: a second failure is DEVICE_UI_DUMP_FAILED): the
// reader failing and then the dump failing is the dump's code, not a reader error.
func TestUITreeReaderAndDumpBothFailing(t *testing.T) {
	f := newUIFixture(t, map[string]string{
		uiSerial + ".dumptty.out":     "nothing\n",
		uiSerial + ".dumpsdcard.exit": "1",
	})
	mustFail(t, f.tree(t, uiSerial), ErrUIDumpFailed)
}

// Decision (a vanished device is its own error on both paths): adb saying the device is
// gone during the reader is DEVICE_DISCONNECTED, not a fallback to a dump that would
// meet the same; the dex removal is still issued.
func TestUITreeDisconnectedDuringTheReader(t *testing.T) {
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".reader.err":  "adb: device 'emulator-5554' not found\n",
		uiSerial + ".reader.exit": "1",
	})
	mustFail(t, f.tree(t, uiSerial), ErrDisconnected)
	calls := f.calls(t)
	if count(calls, ttyCall) != 0 || count(calls, rdRmCall) != 1 {
		t.Errorf("calls %q: want no dump and one removal", calls)
	}
}

// Decision (one budget for reader and dump): a reader that hangs and a dump that hangs
// together end inside the one dump deadline, as DEVICE_UI_DUMP_FAILED, with the dex
// removal issued; the reader's attempt limit is longer than the deadline, so only the
// deadline can have cut it.
func TestUITreeReaderSharesTheDumpDeadline(t *testing.T) {
	old1, old2, old3, old4 := dumpDeadline, dumpAttempt, cleanupReserve, readerRun
	dumpDeadline, dumpAttempt, cleanupReserve, readerRun = 500*time.Millisecond, 4*time.Second, 150*time.Millisecond, 4*time.Second
	defer func() { dumpDeadline, dumpAttempt, cleanupReserve, readerRun = old1, old2, old3, old4 }()
	f := newUIFixture(t, withReader(map[string]string{
		uiSerial + ".reader.hang":  "",
		uiSerial + ".dumptty.hang": "",
	}, ""))
	start := time.Now()
	_, _, err := f.adb.read(context.Background(), &target{adb: f.adb.Resolve().Path, serial: uiSerial})
	mustFail(t, capability.Outcome{State: capability.StateFailed, Error: capability.AsError(err)}, ErrUIDumpFailed)
	if d := time.Since(start); d > dumpDeadline+300*time.Millisecond {
		t.Errorf("took %v with a %v deadline for reader and dump together", d, dumpDeadline)
	}
	if !slices.Contains(f.calls(t), rdRunCall) {
		t.Errorf("calls %q: the reader never ran, so the budget was not shared with it", f.calls(t))
	}
	if count(f.calls(t), rdRmCall) != 1 {
		t.Errorf("calls %q: want the dex removal issued after the hung reader", f.calls(t))
	}
}

// Decision (plan step 10, idle retry on the dump path): the retry of "could not get idle
// state" waits idleRetryPause first, and is not an /sdcard fallback.
func TestUITreeIdleRetryPauses(t *testing.T) {
	old := idleRetryPause
	idleRetryPause = 200 * time.Millisecond
	defer func() { idleRetryPause = old }()
	f := newUIFixture(t, map[string]string{
		uiSerial + ".dumptty#1.out":  idleLine,
		uiSerial + ".dumptty#1.exit": "1",
	})
	start := time.Now()
	mustComplete(t, f.tree(t, uiSerial))
	if d := time.Since(start); d < idleRetryPause {
		t.Errorf("retry came after %v, before the %v pause", d, idleRetryPause)
	}
	if count(f.calls(t), sdDumpCall) != 0 {
		t.Error("an idle failure fell back to /sdcard")
	}
}

// Decision (N2, reader path): the dex is removed even when the caller cancels while the
// reader runs; the removal does not inherit the caller's cancellation, and the
// cancellation is returned as it is, not turned into a fallback.
func TestUITreeReaderRemovesTheDexWhenCancelled(t *testing.T) {
	f := newUIFixture(t, withReader(map[string]string{uiSerial + ".reader.delay": "5000"}, pixelDoc(t)))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		waitLine(t, func() []string { return devicetest.Events(t, f.dir) }, "begin "+uiSerial+".reader")
		cancel()
	}()
	_, _, err := f.adb.read(ctx, &target{adb: f.adb.Resolve().Path, serial: uiSerial})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the caller's cancellation", err)
	}
	calls := f.calls(t)
	if count(calls, rdRmCall) != 1 || count(calls, ttyCall) != 0 {
		t.Errorf("calls %q: want one dex removal and no dump", calls)
	}
}

// Decision (ADR-0029: the SHA-256 is checked before each push): an embedded dex whose
// digest is not the pinned one is never pushed; the read falls back with the reason.
func TestUITreeDoesNotPushADexThatIsNotThePinnedOne(t *testing.T) {
	old := readerDigest
	readerDigest = func() string { return "0000" }
	defer func() { readerDigest = old }()
	f := newUIFixture(t, withReader(nil, pixelDoc(t)))
	s := mustComplete(t, f.tree(t, uiSerial))
	calls := f.calls(t)
	for _, c := range calls {
		if strings.Contains(c, " push ") || c == rdRunCall {
			t.Errorf("a dex with the wrong digest reached the device: %q", c)
		}
	}
	if len(s.Warnings) != 1 || !strings.HasPrefix(s.Warnings[0], fallbackWarning+" Reader: the embedded reader.dex has SHA-256 0000, not the pinned "+reader.SHA256) {
		t.Errorf("warnings %q", s.Warnings)
	}
	if count(calls, ttyCall) != 1 {
		t.Errorf("calls %q: want the dump fallback", calls)
	}
}

// Decision (a null root is a failure, not an empty screen, and not a fallback): the
// reader's own status 3 is DEVICE_UI_DUMP_FAILED; uiautomator dump is not run, because it
// would meet the same missing window and suspend the other services; the dex is removed.
func TestUITreeNullRootIsDumpFailedWithoutFallback(t *testing.T) {
	f := newUIFixture(t, withReader(map[string]string{
		uiSerial + ".reader.err":  "droidyard-reader: no active window root\n",
		uiSerial + ".reader.exit": "3",
	}, ""))
	e := mustFail(t, f.tree(t, uiSerial), ErrUIDumpFailed)
	if !strings.Contains(e.Cause, "no active window") {
		t.Errorf("cause %q", e.Cause)
	}
	calls := f.calls(t)
	if count(calls, ttyCall) != 0 || count(calls, sdDumpCall) != 0 || count(calls, rdRmCall) != 1 {
		t.Errorf("calls %q: want no dump of any kind and one removal", calls)
	}
}
