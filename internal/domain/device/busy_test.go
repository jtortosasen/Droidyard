package device

import (
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// busyDoc is the reader's busy answer (ADR-0034 §1): what DroidyardReader writes when its
// device-side bound passes while the foreground app's main thread does not answer
// accessibility queries. The package it names is the last app the reader read, which is
// stale once the foreground changed; the daemon does not use it for Details (plan m1n step
// 3, fix round), so a test can tell it apart from the window manager's focus below.
const (
	busyStalePackage = "dev.droidyard.previousapp"
	busyDoc          = "droidyard-reader: busy 2500 " + busyStalePackage
)

// focusCall is the busy path's one focus read for uiSerial.
const focusCall = "-s " + uiSerial + " shell dumpsys window"

// busyFocus is a `dumpsys window` line naming pkg as the current focus, in the form the
// busy path parses (plan m1n step 3, fix round).
func busyFocus(pkg string) string {
	return "  mCurrentFocus=Window{1a2b3c4d u0 " + pkg + "/.MainActivity}\n"
}

// Decision (ADR-0034 §1; plan m1n step 3, fix round): the host's warm-read bound leaves at
// least a second above the reader's device-side bound (DroidyardReader.DUMP_BOUND_MS,
// 2500 ms), the time the busy answer spends crossing the forward included, so a busy read
// at the device's bound reaches the client as `busy` instead of being cut as a timeout
// (which would restart the reader). Seen failing with warmReadTimeout at 3 s: 500 ms of
// margin, under the second this requires.
func TestWarmReadBoundLeavesMarginOverTheDeviceBound(t *testing.T) {
	const readerBusyBound = 2500 * time.Millisecond // DroidyardReader.DUMP_BOUND_MS
	if warmReadTimeout < readerBusyBound+time.Second {
		t.Errorf("warmReadTimeout %v leaves under a second over the reader's %v bound: a busy answer at that bound could be cut as a timeout", warmReadTimeout, readerBusyBound)
	}
}

// Decision (ADR-0034 §1; plan m1n step 3): a read whose reader answers `busy` is a failed
// read, not a reader failure. It fails DEVICE_UI_DUMP_FAILED with the fixed cause (never
// the reader's own line), the app's package in details, and the failed call marked
// untrusted with the note; the reader is not restarted (no kill, no second start), the
// one-shot reader never runs, and no `uiautomator dump` is issued, which would suspend the
// device's other accessibility services for an app that is merely busy. Seen failing with
// a busy answer treated as an unreachable reader: a restart, then the one-shot, then a
// dump.
//
// The package in Details is the window manager's focus, not the reader's busy line: the
// two are made different here, so this also fails if the daemon trusts the reader's stale
// package (plan m1n step 3, fix round).
func TestBusyReadDoesNotRestartTheReader(t *testing.T) {
	quickRecovery(t)
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":         "0",
		uiSerial + ".start.out":         "",
		uiSerial + ".forward.out":       r.Port,
		uiSerial + ".pidof.out":         "4242\n",
		uiSerial + ".dumpsyswindow.out": busyFocus(tapPackage),
	})
	// A healthy read primes the reader; then the app stops answering.
	mustComplete(t, f.tree(t, uiSerial))
	r.SetDocs(busyDoc)

	out := f.tree(t, uiSerial)
	ce := mustFail(t, out, ErrUIDumpFailed)
	if !out.Untrusted || out.Meta() == nil || !out.Meta().Untrusted {
		t.Errorf("the busy read's failed call is not marked untrusted (mark %v, meta %+v)", out.Untrusted, out.Meta())
	}
	if got, want := ce.Cause, "the foreground app did not answer accessibility queries"; got != want {
		t.Errorf("cause %q, want the fixed %q", got, want)
	}
	if pkg, _ := ce.Details["package"].(string); pkg != tapPackage {
		t.Errorf("details %+v, want the window manager's focus %q, not the reader's stale %q", ce.Details, tapPackage, busyStalePackage)
	}
	if strings.Contains(ce.Cause, readerPrefix) || strings.Contains(ce.Message, readerPrefix) {
		t.Errorf("the failure quotes the reader's own line: cause %q message %q", ce.Cause, ce.Message)
	}
	if !strings.HasSuffix(ce.Message, capability.UntrustedErrorNote) {
		t.Errorf("message %q does not end with %q", ce.Message, capability.UntrustedErrorNote)
	}
	calls := f.calls(t)
	if n := countServeStarts(calls); n != 1 {
		t.Errorf("%d serve starts, want only the prime: a busy reader is never restarted (%q)", n, calls)
	}
	if n := count(calls, killCall(uiSerial, "4242")); n != 0 {
		t.Errorf("%d kills of the busy reader, want 0 (%q)", n, calls)
	}
	if n := count(calls, focusCall); n != 1 {
		t.Errorf("%d focus reads, want the one of the busy read (%q)", n, calls)
	}
	if n := count(calls, rdRunCall); n != 0 {
		t.Errorf("the one-shot reader ran for a busy app: %q", calls)
	}
	if n := count(calls, ttyCall) + count(calls, sdDumpCall); n != 0 {
		t.Errorf("uiautomator dump ran for a busy app, and would suspend the device's accessibility services: %q", calls)
	}
}

// Decision (ADR-0034 §1; plan m1n step 3): the 1 s ping rule is unchanged by the busy
// path. A reader a busy read leaves alone (no restart) is still killed by its pid and
// started once when it then misses ping: kill-and-restart stays for a reader that does not
// answer, even though a busy answer does not invoke it. Seen failing with the busy
// short-circuit also swallowing the restart path, so the reader that missed ping would
// never be replaced.
func TestMissedPingStillRestarts(t *testing.T) {
	quickRecovery(t)
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
		uiSerial + ".pidof.out":   "4242\n",
	})
	mustComplete(t, f.tree(t, uiSerial)) // prime the reader
	// The app is busy: the read fails, and the reader is left alone.
	r.SetDocs(busyDoc)
	mustFail(t, f.tree(t, uiSerial), ErrUIDumpFailed)

	// The reader then stops answering: its next dump ends the connection and ping is
	// mute, so it is killed by its pid and started once.
	r.SetDocs(devicetest.ReaderGone, pixelDoc(t))
	r.SetMutePings(1)
	s := mustComplete(t, f.tree(t, uiSerial))
	if len(s.Nodes) == 0 || len(s.Warnings) != 0 {
		t.Fatalf("nodes %d warnings %q, want a tree from the restarted reader", len(s.Nodes), s.Warnings)
	}
	calls := f.calls(t)
	if n := countServeStarts(calls); n != 2 {
		t.Errorf("%d serve starts, want the prime and the one restart (%q)", n, calls)
	}
	if n := count(calls, killCall(uiSerial, "4242")); n != 1 {
		t.Errorf("%d kills, want the one restart of the reader that missed ping (%q)", n, calls)
	}
	if n := count(calls, rdRunCall); n != 0 {
		t.Errorf("the one-shot ran although the restart worked: %q", calls)
	}
}

// Decision (ADR-0034 §1; plan m1n step 3, fix round): the read that follows a restart can
// itself come back busy (the first read timed out or met a reader reused because it
// answered ping). That is the same busy failure, not a reason to fall back: no one-shot
// reader, no `uiautomator dump`, and the app's package in Details. Seen failing with the
// post-restart busy read falling through the switch to the one-shot reader.
func TestBusyAfterRestartIsStillTheBusyFailure(t *testing.T) {
	quickRecovery(t)
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":         "0",
		uiSerial + ".start.out":         "",
		uiSerial + ".forward.out":       r.Port,
		uiSerial + ".pidof.out":         "4242\n",
		uiSerial + ".dumpsyswindow.out": busyFocus(tapPackage),
	})
	mustComplete(t, f.tree(t, uiSerial)) // prime the reader

	// The reader vanishes, so the read restarts it; its replacement answers busy.
	r.SetDocs(devicetest.ReaderGone, busyDoc)
	r.SetMutePings(1)

	out := f.tree(t, uiSerial)
	ce := mustFail(t, out, ErrUIDumpFailed)
	if !out.Untrusted {
		t.Errorf("the post-restart busy failure is not marked untrusted")
	}
	if pkg, _ := ce.Details["package"].(string); pkg != tapPackage {
		t.Errorf("details %+v, want the focus package %q", ce.Details, tapPackage)
	}
	calls := f.calls(t)
	if n := countServeStarts(calls); n != 2 {
		t.Errorf("%d serve starts, want the prime and the one restart (%q)", n, calls)
	}
	if n := count(calls, rdRunCall); n != 0 {
		t.Errorf("the one-shot ran for a post-restart busy read: %q", calls)
	}
	if n := count(calls, ttyCall) + count(calls, sdDumpCall); n != 0 {
		t.Errorf("uiautomator ran for a post-restart busy read: %q", calls)
	}
}

// Decision (ADR-0034 §1; plan m1n step 3, fix round): a busy before read fails
// device.tapElement with the app's package in details and the failed call marked untrusted,
// and nothing is tapped. Seen failing with marksCall's `return busyApp(err)` replaced by
// `return false`: the failure comes back unmarked.
func TestBusyBeforeReadMarksTapElementUntrusted(t *testing.T) {
	quickAct(t)
	files, r := withPersistentReaderR(t, map[string]string{
		uiSerial + ".dumpsyswindow.out": busyFocus(tapPackage),
	}, busyDoc)
	f := newUIFixture(t, files)
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
	ce := mustFail(t, out, ErrUIDumpFailed)
	if !out.Untrusted || out.Meta() == nil || !out.Meta().Untrusted {
		t.Errorf("the busy tap failure is not marked untrusted (mark %v, meta %+v)", out.Untrusted, out.Meta())
	}
	if pkg, _ := ce.Details["package"].(string); pkg != tapPackage {
		t.Errorf("details %+v, want the focus package %q", ce.Details, tapPackage)
	}
	if n := countInputTaps(f.calls(t)); n != 0 {
		t.Errorf("%d input taps after a failed before read, want none", n)
	}
	// The busy before read fails before the reader path opens, so a recorded act would mean
	// something was tapped over the forward; none means nothing was (plan m1n step 5, N1).
	if got := r.Acts(); len(got) != 0 {
		t.Errorf("the reader received %q after a failed before read, want none", got)
	}
}

// Decision (ADR-0034 §1; plan m1n step 3, fix round): as with tapElement, a busy before
// read fails device.inputText with the app's package in details and the failed call marked
// untrusted, and nothing is typed. Seen failing with marksCall's `return busyApp(err)`
// replaced by `return false`: the failure comes back unmarked.
func TestBusyBeforeReadMarksInputTextUntrusted(t *testing.T) {
	quickAct(t)
	files, r := withPersistentReaderR(t, map[string]string{
		uiSerial + ".dumpsyswindow.out": busyFocus(tapPackage),
	}, busyDoc)
	f := newUIFixture(t, files)
	out := f.inputCall(t, `{"device":"emulator-5554","text":"x","target":{"id":"name"}}`)
	ce := mustFail(t, out, ErrUIDumpFailed)
	if !out.Untrusted || out.Meta() == nil || !out.Meta().Untrusted {
		t.Errorf("the busy input failure is not marked untrusted (mark %v, meta %+v)", out.Untrusted, out.Meta())
	}
	if pkg, _ := ce.Details["package"].(string); pkg != tapPackage {
		t.Errorf("details %+v, want the focus package %q", ce.Details, tapPackage)
	}
	if got := inputTextArgs(f.calls(t)); len(got) != 0 {
		t.Errorf("the device was told to type %q after a failed before read, want nothing", got)
	}
	// The busy before read fails before the reader path opens, so a recorded act would mean
	// a focusing tap or typing went over the forward (plan m1n step 5, N1).
	if got := r.Acts(); len(got) != 0 {
		t.Errorf("the reader received %q after a failed before read, want none", got)
	}
}

// Decision (ADR-0034 §1; plan m1n step 3, fix round): the busy read names the focused app
// from `dumpsys window`, reading the `mCurrentFocus=Window{...}` line in its
// `<pkg>/<activity>` form and the `Application Not Responding: <pkg>` / `Application
// Error: <pkg>` dialog forms; a read with no such line names nothing.
func TestFocusedPackageFromDumpsys(t *testing.T) {
	cases := []struct{ name, out, want string }{
		{"activity", "WINDOW MANAGER\n  mCurrentFocus=Window{1a2b3c u0 dev.droidyard.formapp/.MainActivity}\n", "dev.droidyard.formapp"},
		{"anr", "  mCurrentFocus=Window{abc u0 Application Not Responding: dev.droidyard.formapp}\n", "dev.droidyard.formapp"},
		{"error", "  mCurrentFocus=Window{abc u0 Application Error: dev.droidyard.formapp}\n", "dev.droidyard.formapp"},
		{"no focus line", "WINDOW MANAGER\n  mFocusedApp=AppWindowToken{xyz}\n", ""},
		{"null focus", "  mCurrentFocus=null\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := focusedPackageFromDumpsys(c.out); got != c.want {
				t.Errorf("focusedPackageFromDumpsys(%q) = %q, want %q", c.out, got, c.want)
			}
		})
	}
}

// Decision (ADR-0034 §1; plan m1n step 3, fix round): a focused title that is not a
// well-formed Android package name (or exceeds 255 bytes) names no package, so no other
// device text reaches Details.
func TestValidPackageNameRejectsNonPackages(t *testing.T) {
	good := []string{"dev.droidyard.formapp", "com.example.app", "a.b"}
	for _, s := range good {
		if !validPackageName(s) {
			t.Errorf("validPackageName(%q) = false, want true", s)
		}
	}
	bad := []string{"", "android", "NoDot", "com.example!", "1bad.pkg", "com..dot", "com.example/app", strings.Repeat("a.", 200) + "b"}
	for _, s := range bad {
		if validPackageName(s) {
			t.Errorf("validPackageName(%q) = true, want false", s)
		}
	}
}

// Decision (ADR-0034 §1; plan m1n step 3, fix round): a busy read whose focus cannot be
// read (here the `dumpsys window` exits non-zero) carries no package and is not marked
// untrusted: it holds nothing from the device. Seen failing with the reader's busy package
// used as a fallback, which would put unverified device text in Details.
func TestBusyWithoutAReadableFocusCarriesNoPackage(t *testing.T) {
	quickRecovery(t)
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":          "0",
		uiSerial + ".start.out":          "",
		uiSerial + ".forward.out":        r.Port,
		uiSerial + ".dumpsyswindow.exit": "1\n",
	})
	mustComplete(t, f.tree(t, uiSerial))

	r.SetDocs(busyDoc)
	out := f.tree(t, uiSerial)
	ce := mustFail(t, out, ErrUIDumpFailed)
	if _, ok := ce.Details["package"]; ok {
		t.Errorf("details %+v carry a package although the focus could not be read", ce.Details)
	}
	if out.Untrusted {
		t.Errorf("a busy failure with no package is marked untrusted although it holds nothing from the device")
	}
	if strings.HasSuffix(ce.Message, capability.UntrustedErrorNote) {
		t.Errorf("message %q ends with the untrusted note although nothing is untrusted", ce.Message)
	}
}

// Decision (ADR-0034 §1; plan m1n step 3, fix round): a focus line whose `<package>` part
// is not a well-formed Android package name (here the single-segment `android` of a system
// resolver window) names no package, so nothing unvalidated reaches Details and the failure
// is not marked untrusted. The title parser accepts it (it has a `/`), so only the package
// validator drops it.
func TestBusyWithANonPackageFocusCarriesNoPackage(t *testing.T) {
	quickRecovery(t)
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":         "0",
		uiSerial + ".start.out":         "",
		uiSerial + ".forward.out":       r.Port,
		uiSerial + ".dumpsyswindow.out": "  mCurrentFocus=Window{abc u0 android/com.android.internal.app.ResolverActivity}\n",
	})
	mustComplete(t, f.tree(t, uiSerial))

	r.SetDocs(busyDoc)
	out := f.tree(t, uiSerial)
	ce := mustFail(t, out, ErrUIDumpFailed)
	if _, ok := ce.Details["package"]; ok {
		t.Errorf("details %+v carry a package from a title that is not one", ce.Details)
	}
	if out.Untrusted {
		t.Errorf("a busy failure with no valid package is marked untrusted")
	}
}

// Decision (ADR-0034 §1; plan m1n step 3): after the tap, a busy poll is a failed read,
// not an error (D17). The call completes with reason noObservation and failedDumps equal
// to polls, having tapped once, restarted nothing and issued no uiautomator dump. Seen
// failing with a busy read after the tap returned as an error.
func TestBusyPollAfterTheTapIsNotAnError(t *testing.T) {
	quickAct(t)
	before := tappable(tapLike("Like"), tapLikes("Likes: 0"))
	f := newUIFixture(t, withReadOnlyReader(t, map[string]string{
		uiSerial + ".dumpsyswindow.out": busyFocus(tapPackage),
	}, before, busyDoc))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"},"expect":[{"appears":{"text":"Likes: 1"}}],"timeoutMs":60}`)
	r := mustTap(t, out)
	if r.Expect == nil || r.Expect.Matched || r.Expect.Reason != ReasonNoObservation {
		t.Fatalf("expect %+v, want matched false with reason %s", r.Expect, ReasonNoObservation)
	}
	if r.Expect.FailedDumps != r.Expect.Polls || r.Expect.Polls == 0 {
		t.Errorf("failedDumps %d of %d polls, want every poll to have failed", r.Expect.FailedDumps, r.Expect.Polls)
	}
	calls := f.calls(t)
	if n := countInputTaps(calls); n != 1 {
		t.Errorf("%d input taps, want one", n)
	}
	if n := countServeStarts(calls); n != 1 {
		t.Errorf("%d serve starts, want only the prime: no poll restarts the reader (%q)", n, calls)
	}
	if n := count(calls, rdRunCall); n != 0 {
		t.Errorf("the one-shot reader ran for a busy poll: %q", calls)
	}
	if n := count(calls, forwardListCall(uiSerial)); n != 1 {
		t.Errorf("%d forward listings, want only the prime's: a busy poll does not reconcile the forward (%q)", n, calls)
	}
	if n := count(calls, ttyCall) + count(calls, sdDumpCall); n != 0 {
		t.Errorf("uiautomator ran after the tap: %q", calls)
	}
}
