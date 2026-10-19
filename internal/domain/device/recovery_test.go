package device

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/device/reader"
)

// readerNameTest is `droidyard-reader-<d12>`: the abstract socket and process name of
// the reader this build runs (D2).
var readerNameTest = readerNamePrefix + reader.SHA256[:12]

// localAbstract wraps a reader name as a forward's remote.
func localAbstract(name string) string { return "localabstract:" + name }

// listedForward is one expected `forwards.list` line.
func listedForward(serial, sock, name string) string {
	return serial + " localfilesystem:" + sock + " " + localAbstract(name)
}

// pidofCall, killCall and forwardListCall are the fake's argument lines for the process
// and forward commands.
func pidofCall(serial, name string) string { return "-s " + serial + " shell pidof " + name }
func killCall(serial, pid string) string   { return "-s " + serial + " shell kill " + pid }
func forwardListCall(serial string) string { return "-s " + serial + " forward --list" }

// indexOf is the first call equal to want, or -1.
func indexOf(calls []string, want string) int {
	for i, c := range calls {
		if c == want {
			return i
		}
	}
	return -1
}

// indexOfServeStart is the first persistent reader start for uiSerial, or -1.
func indexOfServeStart(calls []string) int {
	for i, c := range calls {
		if strings.HasPrefix(c, "-s "+uiSerial+" shell CLASSPATH=") && strings.Contains(c, "DroidyardReader serve ") {
			return i
		}
	}
	return -1
}

// quickRecovery shrinks the reader's recovery bounds so a fake test runs in
// milliseconds: the 1 s ping check, the 8 s start bound and the warm-read timeout.
// Cleanup restores them.
func quickRecovery(t *testing.T) {
	t.Helper()
	warm, check, start := warmReadTimeout, readerPingCheck, readerStartBound
	warmReadTimeout, readerPingCheck, readerStartBound = time.Second, 200*time.Millisecond, 3*time.Second
	t.Cleanup(func() { warmReadTimeout, readerPingCheck, readerStartBound = warm, check, start })
}

// Decision (plan m1f step 7, check a; item 1; ADR-0030: a wedged reader is replaced): a
// warm read that does not finish within warmReadTimeout is not a dump to fall back from:
// the reader is wedged, holding the abstract socket and UiAutomation, so it is killed by
// the pid `pidof` gave and started once. Seen failing with the timeout falling to the
// one-shot, which cannot get UiAutomation while the wedged reader holds it.
func TestWarmReadTimeoutRestartsTheWedgedReader(t *testing.T) {
	oldWarm, oldCheck, oldStart := warmReadTimeout, readerPingCheck, readerStartBound
	warmReadTimeout, readerPingCheck, readerStartBound = 200*time.Millisecond, 300*time.Millisecond, 5*time.Second
	defer func() {
		warmReadTimeout, readerPingCheck, readerStartBound = oldWarm, oldCheck, oldStart
	}()
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
		uiSerial + ".pidof.out":   "4242\n",
	})
	mustComplete(t, f.tree(t, uiSerial)) // prime the reader

	// The dump is wedged (held past the warm-read timeout) and ping is not answered, so
	// the reader is alive but unreachable: it is replaced once.
	r.SetDumpStall(1)
	r.SetMutePings(1)
	s := mustComplete(t, f.tree(t, uiSerial))
	if len(s.Nodes) == 0 {
		t.Fatal("no tree after the wedged reader was replaced")
	}
	if len(s.Warnings) != 0 {
		t.Errorf("warnings %q: replacing a wedged reader is not a fallback", s.Warnings)
	}
	calls := f.calls(t)
	if n := countServeStarts(calls); n != 2 {
		t.Errorf("%d serve starts, want the prime and the one restart (%q)", n, calls)
	}
	killIdx := indexOf(calls, killCall(uiSerial, "4242"))
	if killIdx < 0 {
		t.Fatalf("the wedged reader was not killed by its pid (%q)", calls)
	}
	// The pidof that found the wedged reader runs once, before the kill.
	if n := count(calls[:killIdx], pidofCall(uiSerial, readerNameTest)); n != 1 {
		t.Errorf("%d pidofs of the wedged reader before the kill, want 1 (%q)", n, calls)
	}
	if n := count(calls, killCall(uiSerial, "4242")); n != 1 {
		t.Errorf("%d kills of the wedged reader, want 1 (%q)", n, calls)
	}
	if n := count(calls, rdRunCall); n != 0 {
		t.Errorf("the one-shot ran although the restart worked: %q", calls)
	}
}

// Decision (plan m1f step 7, check a; item 7): after the kill the old pid is waited for
// (bounded, polling pidof) before the new reader starts, so the start cannot race the
// dying process for the abstract socket. Seen failing with the start issued straight
// after the kill (no pidof between them).
func TestRestartWaitsForTheKilledPidBeforeStarting(t *testing.T) {
	quickRecovery(t)
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
		uiSerial + ".pidof.out":   "4242\n",
	})
	mustComplete(t, f.tree(t, uiSerial))
	r.SetDocs(devicetest.ReaderGone, pixelDoc(t))
	r.SetMutePings(1)
	mustComplete(t, f.tree(t, uiSerial))

	calls := f.calls(t)
	killIdx := indexOf(calls, killCall(uiSerial, "4242"))
	if killIdx < 0 {
		t.Fatalf("the reader was not killed by its pid (%q)", calls)
	}
	// The restart's start is the one after the kill.
	startIdx := indexOfServeStart(calls[killIdx+1:])
	if startIdx < 0 {
		t.Fatalf("no start after the kill (%q)", calls)
	}
	if n := count(calls[killIdx+1:killIdx+1+startIdx], pidofCall(uiSerial, readerNameTest)); n < 1 {
		t.Errorf("no pidof between the kill and the start: the old pid was not waited for (%q)", calls)
	}
}

// Decision (plan m1f step 7, check a; item 8; ADR-0030: started again when gone): a
// reader whose dump connection ended but that still answers ping is not gone: the same
// forward is reused and no second reader is started. Seen failing with a start on every
// vanished dump.
func TestRestartReusesAReaderThatAnswersPing(t *testing.T) {
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	mustComplete(t, f.tree(t, uiSerial))
	r.SetDocs(devicetest.ReaderGone, pixelDoc(t)) // the dump connection ends, ping answers
	s := mustComplete(t, f.tree(t, uiSerial))
	if len(s.Nodes) == 0 || len(s.Warnings) != 0 {
		t.Fatalf("nodes %d warnings %q, want a tree from the reused reader", len(s.Nodes), s.Warnings)
	}
	calls := f.calls(t)
	if n := countServeStarts(calls); n != 1 {
		t.Errorf("%d serve starts, want only the prime: the reader answered ping (%q)", n, calls)
	}
	if n := count(calls, killCall(uiSerial, "4242")); n != 0 {
		t.Errorf("a reader that answered ping was killed (%q)", calls)
	}
}

// Decision (plan m1f step 7, check a; item 2; E6): a reader of another digest whose
// forward is gone still holds UiAutomation, so it is found by `ps -A -o PID,NAME` and
// killed by the pid ps gave, before this daemon starts. A process named anything else is
// never killed. Seen failing with only the forward list consulted (the foreign reader
// survives and the new start has no UiAutomation).
func TestForeignReaderWithoutAForwardIsKilledBeforeStart(t *testing.T) {
	quickRecovery(t)
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
		uiSerial + ".ps.out": "  PID NAME\n" +
			" 5000 droidyard-reader-000000000000\n" +
			" 6000 com.example.app\n",
	})
	mustComplete(t, f.tree(t, uiSerial))
	calls := f.calls(t)
	if n := count(calls, killCall(uiSerial, "5000")); n != 1 {
		t.Errorf("%d kills of the foreign reader, want 1 (%q)", n, calls)
	}
	if n := count(calls, killCall(uiSerial, "6000")); n != 0 {
		t.Errorf("a process named something else was killed (%q)", calls)
	}
	killIdx := indexOf(calls, killCall(uiSerial, "5000"))
	startIdx := indexOfServeStart(calls)
	if killIdx < 0 || startIdx < 0 || killIdx > startIdx {
		t.Errorf("the foreign reader kill at %d, our start at %d: want the kill first (%q)", killIdx, startIdx, calls)
	}
}

// Decision (plan m1f step 7; review of the fix round): a real `ps -A` listing is several
// KB (11 KB on Pixel_9a) and a reader, a recent process, comes last; a foreign reader
// past the first KB is still found. Seen failing with the listing capped at 1 KB.
func TestForeignReaderAtTheEndOfALongListingIsKilled(t *testing.T) {
	quickRecovery(t)
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	listing := "  PID NAME\n"
	for i := 0; i < 400; i++ {
		listing += fmt.Sprintf(" %d com.example.process%03d\n", 100+i, i)
	}
	listing += " 9000 droidyard-reader-000000000000\n"
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
		uiSerial + ".ps.out":      listing,
	})
	mustComplete(t, f.tree(t, uiSerial))
	if n := count(f.calls(t), killCall(uiSerial, "9000")); n != 1 {
		t.Errorf("listing of %d bytes: %d kills of the foreign reader at its end, want 1", len(listing), n)
	}
}

// Decision (plan m1f step 7, check a; item 3): a foreign name already killed is
// remembered, so a later slow path does not look it up and kill it again. Seen failing
// with the ps listing consulted and acted on every slow path.
func TestForeignReaderIsKilledOnce(t *testing.T) {
	quickRecovery(t)
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
		uiSerial + ".ps.out":      "  PID NAME\n 5000 droidyard-reader-000000000000\n",
	})
	mustComplete(t, f.tree(t, uiSerial))
	// A slow path again: the reader is gone and its restart scans ps a second time.
	r.SetDocs(devicetest.ReaderGone, pixelDoc(t))
	r.SetMutePings(1)
	mustComplete(t, f.tree(t, uiSerial))
	if n := count(f.calls(t), killCall(uiSerial, "5000")); n != 1 {
		t.Errorf("%d kills of the foreign reader, want 1: it is remembered (%q)", n, f.calls(t))
	}
}

// Decision (plan m1f step 7, check a; item 3): a warm read does not ask adb for the
// forward list; the list is read on the client's first read and after a failed read.
// Seen failing with `forward --list` issued on every warm read.
func TestWarmReadDoesNotAskForTheForwardList(t *testing.T) {
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	mustComplete(t, f.tree(t, uiSerial)) // the first read reconciles
	before := len(f.calls(t))
	mustComplete(t, f.tree(t, uiSerial))
	tail := f.calls(t)[before:]
	if n := count(tail, forwardListCall(uiSerial)); n != 0 {
		t.Errorf("%d `forward --list` calls on a warm read, want none (%q)", n, tail)
	}
}

// Decision (plan m1f step 7, check a; item 3): after a failed read with a cached port, a
// `forward --list` failure is ignored and the dump is tried on the existing forward, so a
// transient listing failure does not cost the fast reader. Seen failing with the listing
// failure returned as the reason (the read falls to the one-shot).
func TestForwardListFailureWithACachedPortStillTriesTheDump(t *testing.T) {
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	mustComplete(t, f.tree(t, uiSerial)) // prime: the port is cached
	r.SetDocs(devicetest.ReaderGone, pixelDoc(t))
	devicetest.Write(t, f.dir, uiSerial+".forwardlist.exit", []byte("1\n"))
	devicetest.Write(t, f.dir, uiSerial+".forwardlist.err", []byte("adb: failed to list forwards\n"))

	s := mustComplete(t, f.tree(t, uiSerial))
	if len(s.Nodes) == 0 {
		t.Fatal("no tree although the cached forward still answered")
	}
	if len(s.Warnings) != 0 {
		t.Errorf("warnings %q: a listing failure with a cached port is ignored", s.Warnings)
	}
	calls := f.calls(t)
	if n := countServeStarts(calls); n != 1 {
		t.Errorf("%d serve starts, want only the prime: the cached reader answered (%q)", n, calls)
	}
	if n := count(calls, rdRunCall); n != 0 {
		t.Errorf("the one-shot ran although the cached forward worked: %q", calls)
	}
}

// Decision (plan m1f step 7, check a; item 3; ADR-0030: a lost forward is recreated): an
// adb server restart drops the host forward while the device-side reader lives on. The
// cached port then refuses the dial, as real adb does, and the failed read recreates the
// forward to the same socket without pushing the dex or starting a second reader. Seen
// failing with no recreation (the read would fall to the one-shot).
func TestLostForwardIsRecreated(t *testing.T) {
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	mustComplete(t, f.tree(t, uiSerial))
	if got := devicetest.Forwards(t, f.dir); len(got) != 1 {
		t.Fatalf("forwards %q, want the one the read made", got)
	}

	// The adb server restarted: the host forward is gone; the reader is untouched.
	devicetest.ForgetForward(t, f.dir, localAbstract(readerNameTest))
	before := len(f.calls(t))
	s := mustComplete(t, f.tree(t, uiSerial))
	if len(s.Nodes) == 0 {
		t.Fatal("no tree after the lost forward")
	}
	if len(s.Warnings) != 0 {
		t.Errorf("warnings %q: recreating a forward is not a fallback", s.Warnings)
	}

	calls := f.calls(t)
	tail := calls[before:]
	if n := countForward(tail); n != 1 {
		t.Errorf("%d forwards after the loss, want the one recreation (%q)", n, tail)
	}
	if n := countServeStarts(tail); n != 0 {
		t.Errorf("%d serve starts after the loss: the reader lives behind the lost forward (%q)", n, tail)
	}
	if n := count(tail, prPushCall); n != 0 {
		t.Errorf("the dex was pushed again after the loss (%q)", tail)
	}
	if got := devicetest.Forwards(t, f.dir); len(got) != 1 {
		t.Errorf("forwards %q, want exactly one after the recreation", got)
	}
	// The dead forward's file stays behind when the server drops it (ADR-0102): the
	// recreation unlinks it, so one file is left, the new forward's. Seen failing with the
	// unlink dropped from the lost-forward path (two files).
	if got := dirEntries(t, forwardDirFor(t, f.dir)); len(got) != 1 {
		t.Errorf("forward directory holds %q after the recreation, want only the new forward's socket", got)
	}
}

// Decision (plan m1f step 7, check a; item 4): after a lost forward is recreated the
// reader is pinged with the 1 s check (readerPingCheck), not the 8 s start bound: a
// reader slower than the check is replaced rather than reused. Seen failing with the
// start bound used for the check (the slow reader would be reused, no new start).
func TestLostForwardIsRecreatedWithThePingCheck(t *testing.T) {
	oldWarm, oldCheck, oldStart := warmReadTimeout, readerPingCheck, readerStartBound
	warmReadTimeout, readerPingCheck, readerStartBound = 3*time.Second, 100*time.Millisecond, 3*time.Second
	defer func() {
		warmReadTimeout, readerPingCheck, readerStartBound = oldWarm, oldCheck, oldStart
	}()
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	mustComplete(t, f.tree(t, uiSerial))
	// Every request, ping included, takes longer than readerPingCheck but less than the
	// start bound.
	r.SetDelay(300 * time.Millisecond)
	devicetest.ForgetForward(t, f.dir, localAbstract(readerNameTest))
	before := len(f.calls(t))
	s := mustComplete(t, f.tree(t, uiSerial))
	if len(s.Nodes) == 0 || len(s.Warnings) != 0 {
		t.Fatalf("nodes %d warnings %q, want a tree with no warning", len(s.Nodes), s.Warnings)
	}
	tail := f.calls(t)[before:]
	if n := countForward(tail); n != 1 {
		t.Fatalf("%d forwards after the loss, want the one recreation (%q)", n, tail)
	}
	if n := countServeStarts(tail); n != 1 {
		t.Errorf("%d serve starts, want 1: a ping slower than the 1 s check is not a reuse (%q)", n, tail)
	}
}

// Decision (plan m1f step 7, check a; item 5; E6): only a name matching
// ^droidyard-reader-[0-9a-f]{12}$ reaches pidof, and only an all-digit pid reaches kill.
// A hostile forward name and a non-numeric ps pid run nothing. Seen failing without the
// validation (the hostile name is sent to the device's shell).
func TestReconcileValidatesNamesAndPids(t *testing.T) {
	quickRecovery(t)
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	hostile := "droidyard-reader-x;reboot"
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
		uiSerial + ".ps.out":      "  PID NAME\n zzz droidyard-reader-000000000000\n",
	})
	devicetest.AddForward(t, f.dir, uiSerial, seedSock(t, f.dir, "reader-000000000041"), "", localAbstract(hostile))

	mustComplete(t, f.tree(t, uiSerial))
	calls := f.calls(t)
	for _, c := range calls {
		if strings.Contains(c, "reboot") {
			t.Errorf("a hostile forward name reached a shell: %q", c)
		}
	}
	if n := count(calls, killCall(uiSerial, "zzz")); n != 0 {
		t.Errorf("a non-numeric pid was sent to kill (%q)", calls)
	}
	if n := countServeStarts(calls); n != 1 {
		t.Errorf("%d serve starts, want 1 (%q)", n, calls)
	}
}

// Decision (plan m1f step 7, check a; item 6): an adopted reader was not pushed by this
// daemon, so when it is replaced the dex is pushed before the start. Seen failing with
// the start issued against a dex this daemon never put on the device.
func TestRestartOfAnAdoptedReaderPushesTheDex(t *testing.T) {
	quickRecovery(t)
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
		uiSerial + ".pidof.out":   "4242\n",
	})
	// An earlier daemon's forward to our socket: the first read adopts it, pushing and
	// starting nothing.
	devicetest.AddForward(t, f.dir, uiSerial, seedSock(t, f.dir, "reader-0000000000aa"), r.Port, localAbstract(readerNameTest))
	mustComplete(t, f.tree(t, uiSerial))
	if n := count(f.calls(t), prPushCall); n != 0 {
		t.Fatalf("the adopted read pushed the dex (%q)", f.calls(t))
	}

	r.SetDocs(devicetest.ReaderGone, pixelDoc(t))
	r.SetMutePings(1)
	s := mustComplete(t, f.tree(t, uiSerial))
	if len(s.Nodes) == 0 {
		t.Fatal("no tree from the restarted reader")
	}
	calls := f.calls(t)
	pushIdx := indexOf(calls, prPushCall)
	startIdx := indexOfServeStart(calls)
	if pushIdx < 0 || startIdx < 0 || pushIdx > startIdx {
		t.Errorf("push at %d, start at %d: want the dex pushed before the start (%q)", pushIdx, startIdx, calls)
	}
	if n := countServeStarts(calls); n != 1 {
		t.Errorf("%d serve starts, want the one restart (%q)", n, calls)
	}
}

// Decision (plan m1f step 7, check a; item 9): a `forward --list` cut at the output cap
// is an error, never a partial list: the read falls back to the one-shot with the reason
// and no forward is adopted or removed from the prefix. Seen failing with the prefix
// parsed (the partial listing would drive an adoption or a removal).
func TestTruncatedForwardListIsAnErrorNotAPartialList(t *testing.T) {
	filler := strings.Repeat("emulator-5554 tcp:1 localabstract:junk\n", forwardListCap/30) // past forwardListCap
	f := newUIFixture(t, withReader(nil, pixelDoc(t)))
	devicetest.Write(t, f.dir, uiSerial+".forwardlist.out", []byte(listedForward(uiSerial, seedSock(t, f.dir, "reader-0000000000bb"), readerNameTest)+filler))
	s := mustComplete(t, f.tree(t, uiSerial))
	if len(s.Nodes) == 0 {
		t.Fatal("no tree from the one-shot fallback")
	}
	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "adb forward --list was too long") {
		t.Errorf("warnings %q, want the forward-list-too-long reason", s.Warnings)
	}
	calls := f.calls(t)
	for _, c := range calls {
		if strings.Contains(c, "forward --remove") {
			t.Errorf("a partial listing drove a removal: %q", c)
		}
	}
	if n := countServeStarts(calls); n != 0 {
		t.Errorf("%d serve starts with an unreadable listing, want none (%q)", n, calls)
	}
}

// Decision (plan m2t step 8 (a)'s recorded format, ADR-0102): `forward --list` lines are
// `<serial> localfilesystem:<path> localabstract:<name>`; a loopback port (the first
// form of the forwards, which this daemon no longer makes), a non-local remote and a
// malformed line are ignored; the path is printed raw, so one with spaces in it (macOS's
// default state root is under "Application Support") is one path. Seen failing with the line
// split on every space.
func TestParseForwardListReadsTheSocketFormat(t *testing.T) {
	const out = "emulator-5554 localfilesystem:/s/run/fwd/reader-0123456789ab localabstract:droidyard-reader-31edb33baa07\n" +
		"emulator-5554 localfilesystem:/s/run/fwd/screen-0123456789ab localabstract:other\n" +
		"emulator-5556 localfilesystem:/s/run/fwd/reader-ba9876543210 localabstract:droidyard-reader-ffeeddccbbaa\n" +
		"emulator-5554 tcp:63552 localabstract:droidyard-reader-31edb33baa07\n" +
		"emulator-5554 localfilesystem:/Users/me/Library/Application Support/droidyard/run/fwd/reader-0123456789ab localabstract:droidyard-reader-31edb33baa07\n" +
		"garbage\n" +
		"emulator-5554 localfilesystem:/x tcp:5555\n"
	got := parseForwardList([]byte(out))
	want := []forwardEntry{
		{serial: "emulator-5554", sock: "/s/run/fwd/reader-0123456789ab", remote: "droidyard-reader-31edb33baa07"},
		{serial: "emulator-5554", sock: "/s/run/fwd/screen-0123456789ab", remote: "other"},
		{serial: "emulator-5556", sock: "/s/run/fwd/reader-ba9876543210", remote: "droidyard-reader-ffeeddccbbaa"},
		{serial: "emulator-5554", sock: "/Users/me/Library/Application Support/droidyard/run/fwd/reader-0123456789ab", remote: "droidyard-reader-31edb33baa07"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("parseForwardList = %+v, want %+v", got, want)
	}
}

// Decision (plan m1f step 7, check a; ADR-0030, B6): a second daemon over the same device
// and a live reader adopts the earlier daemon's forward if ping answers — no push, no
// start — and the adopted reader counts as used, so its Close quits it and removes the
// forward. Seen failing with adoption skipped (the second daemon pushes and starts its
// own) and with an adopted reader left at Close.
func TestEarlierDaemonsForwardIsAdoptedAndStopped(t *testing.T) {
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	mustComplete(t, f.tree(t, uiSerial)) // the first daemon makes the forward
	before := len(f.calls(t))

	adb2 := newFake(t, f.dir, closedPort(t))
	if _, _, err := adb2.read(context.Background(), readerTarget(t, adb2, uiSerial)); err != nil {
		t.Fatalf("the second daemon's read: %v", err)
	}
	tail := f.calls(t)[before:]
	for _, c := range tail {
		if strings.Contains(c, " push ") || strings.HasPrefix(c, "-s "+uiSerial+" shell CLASSPATH=") {
			t.Errorf("the second daemon touched the device's reader: %q", c)
		}
	}
	if len(devicetest.Forwards(t, f.dir)) != 1 {
		t.Errorf("forwards %q, want the one adopted", devicetest.Forwards(t, f.dir))
	}
	sock := adb2.reader(uiSerial).sockValue()

	adb2.Close(context.Background())
	if r.Quits() != 1 {
		t.Errorf("the adopted reader saw %d quits, want 1", r.Quits())
	}
	if n := count(f.calls(t), "-s "+uiSerial+" forward --remove localfilesystem:"+sock); n != 1 {
		t.Errorf("%d removals of the adopted forward, want 1 (%q)", n, f.calls(t))
	}
	if got := devicetest.Forwards(t, f.dir); len(got) != 0 {
		t.Errorf("forwards %q after Close, want none", got)
	}
}

// Decision (plan m1f step 7, check a; E6): reconciliation acts only on its own. A reader
// of another digest named by a forward is killed by the pid its own name returned; a
// forward to `localabstract:other` and another digest's forward are left where they are;
// nothing else is removed. Seen failing with a broader kill pattern.
func TestReconcileTouchesOnlyItsOwn(t *testing.T) {
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	foreign := "droidyard-reader-000000000000" // a different digest's reader
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
		uiSerial + ".pidof.out":   "7777\n",
	})
	sockForeign, sockOther := seedSock(t, f.dir, "reader-000000040001"), seedSock(t, f.dir, "reader-000000040002")
	devicetest.AddForward(t, f.dir, uiSerial, sockForeign, "", localAbstract(foreign))
	devicetest.AddForward(t, f.dir, uiSerial, sockOther, "", "localabstract:other")

	mustComplete(t, f.tree(t, uiSerial))
	calls := f.calls(t)
	if n := count(calls, killCall(uiSerial, "7777")); n != 1 {
		t.Errorf("%d kills of the other digest's reader, want 1 (%q)", n, calls)
	}
	if n := count(calls, pidofCall(uiSerial, foreign)); n != 1 {
		t.Errorf("%d pidofs of the other digest, want 1 (%q)", n, calls)
	}
	if n := count(calls, pidofCall(uiSerial, readerNameTest)); n != 0 {
		t.Errorf("our own reader was looked up although none was unreachable (%q)", calls)
	}
	for _, c := range calls {
		if strings.Contains(c, "kill") && !strings.Contains(c, "kill 7777") {
			t.Errorf("a kill other than the pid pidof returned: %q", c)
		}
		if strings.Contains(c, "forward --remove") {
			t.Errorf("reconciliation removed a forward that was not ours: %q", c)
		}
	}
	fwds := devicetest.Forwards(t, f.dir)
	for _, want := range []string{
		listedForward(uiSerial, sockForeign, foreign),
		listedForward(uiSerial, sockOther, "other"),
	} {
		if !slices.Contains(fwds, want) {
			t.Errorf("forwards %q miss %q", fwds, want)
		}
	}
	if sock := devicetest.ForwardSocket(t, f.dir, readerNameTest); sock == "" {
		t.Errorf("forwards %q miss our own reader's forward", fwds)
	}
}

// Decision (plan m1f step 7, check a; ADR-0030): an earlier daemon's forward to our socket
// that is alive by pidof but does not answer ping within 1 s is removed, its process
// killed by that pid, and the reader started again once. The new forward is the only one
// of ours: no stale entry is kept beside it. Seen failing with the stale forward kept
// beside the new one.
func TestWedgedReaderIsKilledAndStartedAgainOnce(t *testing.T) {
	quickRecovery(t)
	stale := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(stale.Close)
	stale.SetMutePings(1) // an earlier reader that is alive but does not answer ping
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
		uiSerial + ".pidof.out":   "4242\n",
	})
	devicetest.AddForward(t, f.dir, uiSerial, seedSock(t, f.dir, "reader-0000000000cc"), stale.Port, localAbstract(readerNameTest))

	mustComplete(t, f.tree(t, uiSerial))
	calls := f.calls(t)
	if n := countServeStarts(calls); n != 1 {
		t.Errorf("%d serve starts, want the one restart (%q)", n, calls)
	}
	killIdx := indexOf(calls, killCall(uiSerial, "4242"))
	if killIdx < 0 {
		t.Fatalf("the wedged reader was not killed (%q)", calls)
	}
	if n := count(calls[:killIdx], pidofCall(uiSerial, readerNameTest)); n != 1 {
		t.Errorf("%d pidofs of the wedged reader before the kill, want 1 (%q)", n, calls)
	}
	if n := count(calls, killCall(uiSerial, "4242")); n != 1 {
		t.Errorf("%d kills of the wedged reader, want 1 (%q)", n, calls)
	}
	if got := devicetest.Forwards(t, f.dir); len(got) != 1 || !strings.HasSuffix(got[0], localAbstract(readerNameTest)) {
		t.Errorf("forwards %q, want only the new one of ours (no stale entry beside it)", got)
	}
}

// Decision (plan m1f step 7, check a; ADR-0030: started again once, no error after the
// tap): when a read's connection ends before a document and a process of our name is
// alive but does not answer ping, it is killed by its pid and the reader started again;
// the read then succeeds with no warning and no one-shot fallback.
func TestRestartAfterAVanishKillsTheUnreachableReader(t *testing.T) {
	quickRecovery(t)
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
		uiSerial + ".pidof.out":   "4242\n",
	})
	mustComplete(t, f.tree(t, uiSerial))

	// The reader stops answering: its next dump ends the connection, and ping is mute.
	r.SetDocs(devicetest.ReaderGone, pixelDoc(t))
	r.SetMutePings(1)
	s := mustComplete(t, f.tree(t, uiSerial))
	if len(s.Nodes) == 0 || len(s.Warnings) != 0 {
		t.Fatalf("nodes %d warnings %q, want a tree from the restarted reader", len(s.Nodes), s.Warnings)
	}
	calls := f.calls(t)
	if n := countServeStarts(calls); n != 2 {
		t.Errorf("%d serve starts, want the prime and the restart (%q)", n, calls)
	}
	if n := count(calls, killCall(uiSerial, "4242")); n != 1 {
		t.Errorf("%d kills of the unreachable reader, want 1 (%q)", n, calls)
	}
	if n := count(calls, rdRunCall); n != 0 {
		t.Errorf("the one-shot reader ran although the restart worked: %q", calls)
	}
	if len(devicetest.Forwards(t, f.dir)) != 1 {
		t.Errorf("forwards %q, want the one forward reused", devicetest.Forwards(t, f.dir))
	}
}
