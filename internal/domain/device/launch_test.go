package device

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

const launchHead = "-s " + uiSerial + " shell monkey --pct-syskeys 0 -p dev.droidyard.formapp -c android.intent.category.LAUNCHER 1"

// quickLaunch shrinks the launch's bounds so a fake test runs in milliseconds.
func quickLaunch(t *testing.T) {
	t.Helper()
	date, monkey, window, interval, bound := launchDateTimeout, launchMonkeyTimeout, launchPidWindow, launchPidInterval, launchRunningBound
	launchDateTimeout, launchMonkeyTimeout, launchPidWindow, launchPidInterval, launchRunningBound =
		500*time.Millisecond, time.Second, 150*time.Millisecond, 10*time.Millisecond, 3*time.Second
	t.Cleanup(func() {
		launchDateTimeout, launchMonkeyTimeout, launchPidWindow, launchPidInterval, launchRunningBound = date, monkey, window, interval, bound
	})
}

func (f *uiFixture) launchCall(t *testing.T, params string) capability.Outcome {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: "device.launch", Params: []byte(params)})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return f.wait(t, id)
}

func mustLaunch(t *testing.T, out capability.Outcome) LaunchResult {
	t.Helper()
	if out.State != capability.StateCompleted {
		t.Fatalf("operation %s with error %+v", out.State, out.Error)
	}
	r, ok := out.Result.(LaunchResult)
	if !ok {
		t.Fatalf("result is %T, want LaunchResult", out.Result)
	}
	return r
}

// Decision (plan m1f step 8, check): the pid is the one pidof answers with on its third
// poll; launchedAt comes from the device clock the fake printed, as RFC 3339 UTC with
// milliseconds.
func TestLaunchPidFromTheThirdPoll(t *testing.T) {
	quickLaunch(t)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".date.out":       "1696118400.123456789\n",
		uiSerial + ".monkey.out":     "",
		uiSerial + ".apppidof#3.out": "4242\n",
	})
	out := f.launchCall(t, `{"device":"emulator-5554","applicationId":"dev.droidyard.formapp"}`)
	r := mustLaunch(t, out)
	if r.PID == nil || *r.PID != 4242 {
		t.Fatalf("pid = %v, want 4242 from the third poll", r.PID)
	}
	want := time.Unix(1696118400, 123456789).UTC().Format("2006-01-02T15:04:05.000Z")
	if r.LaunchedAt != want {
		t.Errorf("launchedAt = %q, want %q (the fake's device clock)", r.LaunchedAt, want)
	}
	// The launcher runs once, never twice (plan m1f step 8).
	if n := count(f.calls(t), launchHead); n != 1 {
		t.Errorf("%d monkey launches, want one", n)
	}
	// The device clock is read just before the launch (adb-device.md §Launch).
	calls := f.calls(t)
	dateAt, monkeyAt := -1, -1
	for i, c := range calls {
		if strings.HasPrefix(c, "-s "+uiSerial+" shell date ") {
			dateAt = i
		}
		if c == launchHead {
			monkeyAt = i
		}
	}
	if dateAt < 0 || monkeyAt < 0 || dateAt > monkeyAt {
		t.Errorf("the device clock is read at %d and the launch at %d; the clock must be read first", dateAt, monkeyAt)
	}
}

// Decision (plan m1f step 8, check): a pid that never appears is pid null with a
// reason, not an error, after polling pidof until the window passes.
func TestLaunchNoPidIsNullWithReason(t *testing.T) {
	quickLaunch(t)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".date.out":   "1696118400.0\n",
		uiSerial + ".monkey.out": "",
	})
	out := f.launchCall(t, `{"device":"emulator-5554","applicationId":"dev.droidyard.formapp"}`)
	r := mustLaunch(t, out)
	if r.PID != nil {
		t.Fatalf("pid = %v, want null", r.PID)
	}
	if r.Reason == "" {
		t.Error("pid is null with no reason")
	}
	polls := 0
	for _, c := range f.calls(t) {
		if strings.HasPrefix(c, "-s "+uiSerial+" shell pidof dev.droidyard.formapp") {
			polls++
		}
	}
	if polls < 2 {
		t.Errorf("%d pidof polls, want repeated polling until the window passed", polls)
	}
}

// Decision (plan m1f step 8, check; reversal): monkey's "No activities found" is
// DEVICE_LAUNCH_FAILED even on exit 0. Seen failing with "No activities" accepted on
// exit 0 (the call would report a launch with a null pid).
func TestLaunchNoActivitiesIsLaunchFailed(t *testing.T) {
	quickLaunch(t)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".date.out":   "1696118400.0\n",
		uiSerial + ".monkey.out": "No activities found\n",
	})
	out := f.launchCall(t, `{"device":"emulator-5554","applicationId":"dev.droidyard.formapp"}`)
	ce := mustFail(t, out, ErrLaunchFailed)
	if !strings.Contains(ce.Cause, "No activities found") {
		t.Errorf("cause %q does not carry monkey's line", ce.Cause)
	}
}

// Decision (plan m1f step 8, item 4, reversal): after monkey ran, a pidof the running
// bound cuts is pid null with a reason, a completed call, never an error. Seen failing
// with the deadline returned as an error (the whole call failed instead of reporting a
// crash on start).
func TestLaunchPidofCutByTheRunningBoundIsNullWithReason(t *testing.T) {
	quickLaunch(t)
	oldWindow, oldBound := launchPidWindow, launchRunningBound
	launchPidWindow, launchRunningBound = 2*time.Second, 300*time.Millisecond
	defer func() { launchPidWindow, launchRunningBound = oldWindow, oldBound }()

	f := newUIFixture(t, map[string]string{
		uiSerial + ".date.out":      "1696118400.0\n",
		uiSerial + ".monkey.out":    "",
		uiSerial + ".apppidof.hang": "",
	})
	out := f.launchCall(t, `{"device":"emulator-5554","applicationId":"dev.droidyard.formapp"}`)
	r := mustLaunch(t, out)
	if r.PID != nil {
		t.Fatalf("pid = %v, want null", r.PID)
	}
	if r.Reason == "" {
		t.Error("pid is null with no reason")
	}
}

// Decision (plan m1f step 8, check; R6, reversal): an applicationId that is not a
// package name is rejected at Start, before the device is touched, so the `;` never
// reaches a device shell or even calls.log. Seen failing with the pattern removed (the
// hook and `devices -l` run, and the id would reach the shell).
func TestLaunchRejectsABadApplicationIDBeforeTheDevice(t *testing.T) {
	quickLaunch(t)
	f := newUIFixture(t, nil)
	out := f.launchCall(t, `{"device":"emulator-5554","applicationId":"a.b;reboot"}`)
	mustFail(t, out, capability.ErrInvalidParams)
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("calls = %q, want none: a malformed applicationId must reach no adb command", calls)
	}
}

// Decision (plan m1f step 8, check; R2): a launch behind another action on the device is
// queued and names the holder in blockedBy.
func TestLaunchQueuesBehindAnActionOnTheDevice(t *testing.T) {
	quickAct(t)
	quickLaunch(t)
	doc := tappable(tapLike("Like"))
	f := newUIFixture(t, withPersistentReader(t, map[string]string{
		uiSerial + ".date.out":   "1696118400.0\n",
		uiSerial + ".monkey.out": "",
	}, doc))
	start := func(name, params string) string {
		id, err := f.ops.Start(context.Background(), capability.Request{Capability: name, Params: []byte(params)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := start("device.tapElement", `{"device":"emulator-5554","target":{"id":"like"},"expect":[{"appears":{"text":"Nope"}}],"timeoutMs":10000}`)
	second := start("device.launch", `{"device":"emulator-5554","applicationId":"dev.droidyard.formapp"}`)
	snap, err := f.ops.Wait(context.Background(), second, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != capability.StateQueued || snap.BlockedBy == nil || snap.BlockedBy.OperationID != first {
		t.Fatalf("launch is %s blockedBy %+v, want queued behind %s", snap.State, snap.BlockedBy, first)
	}
	f.ops.Cancel(first)
	f.ops.Cancel(second)
	// A running operation ends when its handler returns: wait for both, so quickAct's cleanup
	// does not restore the bounds under a handler that still reads them.
	for _, id := range []string{first, second} {
		if out, err := f.ops.Wait(context.Background(), id, time.Now().Add(10*time.Second)); err != nil || !out.State.Terminal() {
			t.Fatalf("%s did not end after its cancel: %+v %v", id, out.State, err)
		}
	}
}
