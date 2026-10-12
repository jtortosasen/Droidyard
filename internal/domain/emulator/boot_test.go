package emulator

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/emulator/emulatortest"
	"golang.org/x/sys/unix"
)

// The step-5 tests (plan m1i step 5, check (a)): the boot sequence over the stateful
// fake launcher (emulatortest) and the fake adb (devicetest), the launch table, wipe and
// the boot's failure mapping. Every scenario runs the re-executed launcher for real;
// none finds this machine's emulator, and none reaches the shared adb server.

// emuWrite puts a launcher-scenario file (`<avd>.serial`, `<avd>.launch.*`, …) in the
// fake launcher's own directory: the launcher reads those from its scenario directory,
// while the fake adb reads the table's files from its own.
func (f *fixture) emuWrite(t *testing.T, name, data string) {
	t.Helper()
	emulatortest.Write(t, f.ldir, name, []byte(data))
}

// launchCall is the `-avd <name> …` invocation the fake launcher recorded, "" when it
// was never asked to boot.
func (f *fixture) launchCall(t *testing.T) string {
	t.Helper()
	for _, c := range emulatortest.Calls(t, f.ldir) {
		if strings.HasPrefix(c, "-avd ") {
			return c
		}
	}
	return ""
}

// awaitTableSerial waits until a launcher registered serial in the fake adb's table and
// returns the pid it reported; the boot is then in flight.
func (f *fixture) awaitTableSerial(t *testing.T, serial string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pid := devicetest.EmulatorPID(f.adbDir, serial); pid != 0 {
			return pid
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no launcher registered %s", serial)
	return 0
}

// awaitPolls waits until the fake adb has answered at least want readiness polls for
// serial, so a test can tell that a boot that adopts is already waiting on
// sys.boot_completed before it acts.
func (f *fixture) awaitPolls(t *testing.T, serial string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if n := pollCount(f.adbDir, serial); n >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s never reached %d readiness polls", serial, want)
}

func pollCount(dir, serial string) int {
	b, err := os.ReadFile(filepath.Join(dir, serial+".polls"))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// awaitGone drains the fake's leave counts for serial until it leaves the listing, so a
// test can see a kill it issued take effect even after the call that issued it returned.
func (f *fixture) awaitGone(t *testing.T, serial string) {
	t.Helper()
	for i := 0; i < 20; i++ {
		if !containsString(f.listing(t), serial) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("%s never left the listing", serial)
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// ---- emulator.start: adoption ------------------------------------------------------

// Decision (plan m1i step 5, check; D15): an AVD already running is adopted — adopted is
// true, the serial is the one its console reports, and the launcher is never asked to
// boot. The mutation this fails: adoption skipped, so a second launcher is started on a
// running AVD.
func TestStartAdoptsARunningAVD(t *testing.T) {
	f := newFixture(t, map[string]string{
		"devices.out":                 listOf(emuLine),
		serial + ".avd":               loadedAVD + "\r\nOK\r\n",
		serial + ".bootcompleted.out": "1\n",
	})
	f.launcher(t, loadedAVD+"\n")

	r := mustStart(t, f.boot(t, `{"avd":"`+loadedAVD+`"}`))
	if !r.Adopted || r.Device != serial || r.Avd != loadedAVD {
		t.Fatalf("result = %+v; want adopted %s/%s", r, serial, loadedAVD)
	}
	if r.BootMs != 0 {
		t.Errorf("bootMs = %d; an adoption waited, it did not boot", r.BootMs)
	}
	if c := f.launchCall(t); c != "" {
		t.Errorf("the launcher was asked to boot: %q", c)
	}
}

// Decision (milestone 2c step 6): a windowed, warm start (no options) carries the crash
// flag too, not only the headless and cold ones above: the consent dialog that hangs a
// windowed boot only exists when there is a window. Seen failing with the flag removed
// from the launch.
func TestWindowedStartAnswersTheCrashReportQuestionItself(t *testing.T) {
	scaleBoot(t, 10*time.Second, 10*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.launcher(t, loadedAVD+"\n")
	mustStart(t, f.boot(t, `{"avd":"`+loadedAVD+`"}`))
	want := "-avd " + loadedAVD + " -no-boot-anim -no-audio -no-snapshot-save -crash-report-mode never"
	if c := f.launchCall(t); c != want {
		t.Errorf("launch = %q; want %q", c, want)
	}
}

// Decision (plan m1i step 5, check; D15): a stopped AVD is booted detached — the
// documented flags, a session of its own (Setsid, not Setpgid), and its output under
// `<state>/emulator/<avd>.log` — and the result carries the measured bootMs.
func TestStartBootsDetachedWithTheDocumentedFlags(t *testing.T) {
	scaleBoot(t, 10*time.Second, 10*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.launcher(t, loadedAVD+"\n")
	f.emuWrite(t, loadedAVD+".launch.delay", "120\n")

	r := mustStart(t, f.boot(t, `{"avd":"`+loadedAVD+`","coldBoot":true,"headless":true}`))
	if r.Adopted || r.Device != "emulator-5556" || r.Avd != loadedAVD {
		t.Fatalf("result = %+v; want a fresh boot of %s", r, loadedAVD)
	}
	if r.BootMs < 100 {
		t.Errorf("bootMs = %d; the boot took at least the launcher's 120 ms delay", r.BootMs)
	}
	want := "-avd " + loadedAVD + " -no-boot-anim -no-audio -no-snapshot-save -no-snapshot-load -no-window -crash-report-mode never"
	if c := f.launchCall(t); c != want {
		t.Errorf("launch = %q; want %q", c, want)
	}
	if log := f.logOf(t, loadedAVD); !strings.Contains(log, "boot 1 of "+loadedAVD) {
		t.Errorf("log = %q; want the launcher's own words", log)
	}
	pid := f.launcherPid(t, r.Device)
	if pid <= 0 {
		t.Fatalf("no pid for %s", r.Device)
	}
	own, _ := unix.Getsid(0)
	if sid, err := unix.Getsid(pid); err != nil || sid == own {
		t.Errorf("launcher session %d (err %v); want a session of its own, not the test's %d", sid, err, own)
	}
}

// Decision (plan m1i step 5, check; D15): the serial is resolved by difference *and* the
// AVD its console reports, so a decoy that appears at the same time is not mistaken for
// ours. The mutation this fails: the first new emulator serial taken as the boot's own.
func TestStartFindsItsSerialByDifferenceAndAvdName(t *testing.T) {
	scaleBoot(t, 5*time.Second, 10*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.launcher(t, loadedAVD+"\n")
	// A second emulator appears beside ours: the launcher registers it first.
	f.emuWrite(t, loadedAVD+".extra", "emulator-5554 Decoy_AVD\n")
	f.emuWrite(t, loadedAVD+".serial", "emulator-5556\n")

	r := mustStart(t, f.boot(t, `{"avd":"`+loadedAVD+`"}`))
	if r.Device != "emulator-5556" {
		t.Fatalf("device = %q; want the AVD's own serial, not the decoy's", r.Device)
	}
}

// Decision (plan m1i step 5, check; D15): the boot loop reads the launch table every poll,
// so a launcher that exits before the device finished booting ends the call
// EMULATOR_LAUNCH_FAILED at once, with its exit status and the log's tail. The mutation
// this fails: the launcher's exit ignored, so the call polls to the boot timeout.
func TestStartReportsALauncherThatExitedEarly(t *testing.T) {
	scaleBoot(t, 30*time.Second, 10*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.launcher(t, loadedAVD+"\n")
	f.emuWrite(t, loadedAVD+".launch.exit", "3\n")
	f.emuWrite(t, loadedAVD+".launch.err", "boom: no system image\n")

	started := time.Now()
	out := f.boot(t, `{"avd":"`+loadedAVD+`"}`)
	e := mustFail(t, out, ErrLaunchFailed)
	if !strings.Contains(e.Cause, "3") || !strings.Contains(e.Cause, "boom") {
		t.Errorf("cause = %q; want the exit status and the log tail", e.Cause)
	}
	if time.Since(started) > 5*time.Second {
		t.Errorf("the call took %s; it polled to the bound instead of ending at the exit", time.Since(started))
	}
}

// Decision (plan m1i step 5, check; D15 and step 1's G10; plan m1m step 3, Q1 ¶4): the
// launcher's second-instance refusal is EMULATOR_AVD_LOCKED, and its nextStep gives the
// caller a way out that does not pretend the holder is a device Droidyard can address —
// no emulator.list remedy (the holder has no console to list), and no promise to stop a
// process Droidyard did not launch. The mutations this fails: the old emulator.list
// remedy, and a nextStep that offers to stop the holder.
func TestStartReportsALockedAVD(t *testing.T) {
	scaleBoot(t, 5*time.Second, 10*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.launcher(t, loadedAVD+"\n")
	f.emuWrite(t, loadedAVD+".launch.exit", "1\n")
	f.emuWrite(t, loadedAVD+".launch.err", "ERROR   | "+lockedMarker+" (AVD name: "+loadedAVD+")\n")

	e := mustFail(t, f.boot(t, `{"avd":"`+loadedAVD+`"}`), ErrAVDLocked)
	if strings.Contains(e.NextStep, "emulator.list") {
		t.Errorf("nextStep = %q; want no emulator.list remedy, the holder has no console to list", e.NextStep)
	}
	if !strings.Contains(e.NextStep, "does not stop processes it did not launch") {
		t.Errorf("nextStep = %q; want it to say Droidyard does not stop what it did not launch", e.NextStep)
	}
	// D6: the nextStep says whether repeating is safe. Nothing was launched, so it is once
	// the holding instance is gone (plan m1n, D6).
	if !strings.Contains(e.NextStep, "safe to repeat") {
		t.Errorf("nextStep = %q; want it to say the call is safe to repeat (plan m1n, D6)", e.NextStep)
	}
}

// Decision (plan m1i step 5, check; D14; plan m1m steps 2 and 3, Q1 ¶2): a device that
// appears but never finishes booting ends the call EMULATOR_BOOT_TIMEOUT naming it, at the
// bound, and because its serial is resolved the launcher is left running — it is a device
// now, not ours to signal — and the nextStep names the remedy that serial answers to,
// emulator.stop. The mutations this fail: an unbounded await; `end` run after resolution;
// a nextStep that says only "a wedged emulator may need stopping" without naming the call.
func TestStartTimesOutWhenTheSerialNeverBoots(t *testing.T) {
	scaleBoot(t, 300*time.Millisecond, 20*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.launcher(t, loadedAVD+"\n")
	devicetest.Write(t, f.adbDir, loadedAVD+".bootpolls", []byte("1000000\n"))

	e := mustFail(t, f.boot(t, `{"avd":"`+loadedAVD+`"}`), ErrBootTimeout)
	if !strings.Contains(e.Message, "emulator-5556") {
		t.Errorf("message = %q; want it to name the serial", e.Message)
	}
	if !strings.Contains(e.NextStep, "emulator.stop") || !strings.Contains(e.NextStep, serial) {
		t.Errorf("nextStep = %q; want it to name emulator.stop with %s", e.NextStep, serial)
	}
	// D6: with the serial resolved the same AVD is still running, so the nextStep says
	// repeating is not safe until that instance is stopped (plan m1n, D6).
	if !strings.Contains(e.NextStep, "do not repeat") {
		t.Errorf("nextStep = %q; want it to say not to repeat the call (plan m1n, D6)", e.NextStep)
	}
	if pid := f.launcherPid(t, serial); !alive(pid) {
		t.Errorf("pid %d is gone; a resolved emulator is a device, not ours to signal", pid)
	}
}

// Decision (plan m1i step 5, check; D15): a name the launcher does not list is
// EMULATOR_AVD_NOT_FOUND with the names it does, before any spawn.
func TestStartOfAnUnknownAVDIsNotFound(t *testing.T) {
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.launcher(t, loadedAVD+"\nSecond_AVD\n")

	e := mustFail(t, f.boot(t, `{"avd":"nope"}`), ErrAVDNotFound)
	names, _ := e.Details["avds"].([]string)
	if len(names) != 2 || names[0] != loadedAVD || names[1] != "Second_AVD" {
		t.Errorf("avds = %v; want the names the launcher knows", e.Details["avds"])
	}
	if c := f.launchCall(t); c != "" {
		t.Errorf("a launcher boot was attempted for an unknown AVD: %q", c)
	}
}

// Decision (plan m1i step 5, check; R4, D15): a name that is not a bare AVD name is
// REQUEST_INVALID_PARAMS before any spawn, so no caller string can reach a path or a
// shell and nothing is written outside `<state>/emulator/`. The mutation this fails: the
// log path built from an unchecked name.
func TestStartRefusesANameThatIsNotABareAVDName(t *testing.T) {
	for _, name := range []string{"../x", ".x", "a/b", "", "a b", "-nope"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, map[string]string{"devices.out": listHead})
			f.launcher(t, loadedAVD+"\n")
			out := f.boot(t, `{"avd":`+jsonString(name)+`}`)
			mustFail(t, out, capability.ErrInvalidParams)
			if c := f.launchCall(t); c != "" {
				t.Errorf("the launcher was invoked: %q", c)
			}
			// Only the emulator directory may exist under the state root, and it is
			// built by the spawn: a refused name leaves nothing at all.
			entries, err := os.ReadDir(f.state)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("the state root holds %v; a refused name writes nothing", entries)
			}
		})
	}
}

// Decision (plan m1i step 5, check; D15; plan m1m step 2, Q1 ¶1, ¶2): a launch whose serial
// never appears is ended at the serial deadline — well before the boot bound — with
// EMULATOR_BOOT_TIMEOUT whose cause names the launcher's pid and that it was stopped, and
// whose nextStep names headless. `.launch.child` makes the launcher fork a child in its
// group, so both pids must be gone. The mutations this fails: no `end` at all (both pids
// survive, and the AVD stays held); the pid alone instead of the group (the child survives).
func TestStartEndsItsLaunchWhenNoSerialAppears(t *testing.T) {
	scaleBoot(t, 30*time.Second, 20*time.Millisecond)
	scaleSerial(t, 300*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.launcher(t, loadedAVD+"\n")
	f.emuWrite(t, loadedAVD+".launch.delay", "100000\n") // never registers within the bound
	f.emuWrite(t, loadedAVD+".launch.child", "")
	f.emuWrite(t, loadedAVD+".launch.early", "WARNING | droidyard-test-marker: waiting on a dialog\n")

	started := time.Now()
	e := mustFail(t, f.boot(t, `{"avd":"`+loadedAVD+`"}`), ErrBootTimeout)
	if d := time.Since(started); d > 5*time.Second {
		t.Errorf("the boot took %s; it should end at the serial deadline, not the 30 s boot bound", d)
	}
	l := f.awaitLaunch(t, loadedAVD)
	if !strings.Contains(e.Cause, strconv.Itoa(l.pid)) {
		t.Errorf("cause = %q; want it to name the launcher pid %d", e.Cause, l.pid)
	}
	if !strings.Contains(e.Cause, "was stopped") || strings.Contains(e.Cause, "could not be stopped") {
		t.Errorf("cause = %q; want it to say the launch was stopped", e.Cause)
	}
	if !strings.Contains(e.Cause, "droidyard-test-marker") {
		t.Errorf("cause = %q; want it to quote the launcher log's tail", e.Cause)
	}
	if !strings.Contains(e.NextStep, "headless") {
		t.Errorf("nextStep = %q; want it to name headless", e.NextStep)
	}
	awaitDead(t, l.pid)
	awaitDead(t, f.awaitChildPid(t, loadedAVD))
}

// Decision (plan m1m step 2, Q1 ¶1): the boundary is resolution, not the first listing — a
// new serial the fake lists but whose `emu avd name` never answers is not this launch's, so
// the boot ends at the serial deadline and the launch is stopped. The mutation this fails:
// the boundary moved to the first new serial, which would adopt a serial it has not
// confirmed.
func TestStartEndsALaunchListedButNotResolved(t *testing.T) {
	scaleBoot(t, 30*time.Second, 20*time.Millisecond)
	scaleSerial(t, 300*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead, serial + ".avd.hang": ""})
	f.launcher(t, loadedAVD+"\n")

	started := time.Now()
	e := mustFail(t, f.boot(t, `{"avd":"`+loadedAVD+`"}`), ErrBootTimeout)
	if d := time.Since(started); d > 5*time.Second {
		t.Errorf("the boot took %s; want it ended at the serial deadline", d)
	}
	l := f.awaitLaunch(t, loadedAVD)
	if !strings.Contains(e.Cause, strconv.Itoa(l.pid)) {
		t.Errorf("cause = %q; want it to name the launcher pid %d", e.Cause, l.pid)
	}
	awaitDead(t, l.pid)
}

// Decision (plan m1m step 2, Q1 ¶1): a cancel before the serial is resolved ends the launch.
// The launcher ignores SIGTERM and writes `.ignoreterm.ready` once it does, so the cancel
// reaches a process only the SIGKILL `end` sends after its grace can end: a dead pid proves
// the escalation ran. The mutations this fails: no `end` on a cancel (the pid survives); and
// `end` bounded by the call's cancelled context, which would return before the SIGKILL.
func TestCancelBeforeTheSerialEndsTheLaunch(t *testing.T) {
	scaleBoot(t, 30*time.Second, 20*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.launcher(t, loadedAVD+"\n")
	f.emuWrite(t, loadedAVD+".launch.delay", "100000\n")
	f.emuWrite(t, loadedAVD+".launch.ignoreterm", "")

	id, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "emulator.start", Params: []byte(`{"avd":"` + loadedAVD + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	l := f.awaitLaunch(t, loadedAVD)
	f.awaitFile(t, loadedAVD+".ignoreterm.ready")
	if err := f.ops.Cancel(id); err != nil {
		t.Fatal(err)
	}
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if out.State != capability.StateCancelled {
		t.Fatalf("state = %s (%+v); want cancelled", out.State, out.Error)
	}
	awaitDead(t, l.pid)
}

// Decision (plan m1m step 2, Q1 ¶1): the daemon's shutdown, which cancels the call's
// context like a cancel, also ends a launch before its serial. The mutation this fails: no
// `end` on shutdown, so the launch survives the daemon and holds the AVD.
func TestShutdownBeforeTheSerialEndsTheLaunch(t *testing.T) {
	scaleBoot(t, 30*time.Second, 20*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.launcher(t, loadedAVD+"\n")
	f.emuWrite(t, loadedAVD+".launch.delay", "100000\n")

	if _, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "emulator.start", Params: []byte(`{"avd":"` + loadedAVD + `"}`)}); err != nil {
		t.Fatal(err)
	}
	l := f.awaitLaunch(t, loadedAVD)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.ops.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	awaitDead(t, l.pid)
}

// Decision (plan m1m step 2, Q1 ¶1, ¶3): a wipe whose serial never appears is ended too, and
// its nextStep names emulator.wipe — the wipe may be partial — never coldBoot, which is
// emulator.start's remedy.
func TestWipeEndsItsLaunchWhenNoSerialAppears(t *testing.T) {
	scaleBoot(t, 30*time.Second, 20*time.Millisecond)
	scaleSerial(t, 300*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.launcher(t, loadedAVD+"\n")
	f.emuWrite(t, loadedAVD+".launch.delay", "100000\n")

	e := mustFail(t, f.invoke(t, "emulator.wipe", `{"avd":"`+loadedAVD+`"}`), ErrBootTimeout)
	if !strings.Contains(e.NextStep, "emulator.wipe") {
		t.Errorf("nextStep = %q; want it to name emulator.wipe", e.NextStep)
	}
	if strings.Contains(e.NextStep, "coldBoot") {
		t.Errorf("nextStep = %q; a wipe's remedy is not coldBoot", e.NextStep)
	}
	l := f.awaitLaunch(t, loadedAVD)
	awaitDead(t, l.pid)
}

// Decision (plan m1m step 2, Q1 ¶1): `end` waits until the target is empty, not only for
// the launcher's reaper. The launcher forks a child that ignores SIGTERM (the parent still
// exits on it), so after the parent is reaped the group is not empty and `end` must send
// the SIGKILL its "not empty" answer earns; the child is dead only if it did. The mutations
// this fails: waiting on the reaper's `done` alone (the child survives); no SIGKILL.
func TestEndWaitsForTheWholeTarget(t *testing.T) {
	scaleBoot(t, 30*time.Second, 20*time.Millisecond)
	// 2 s, not 300 ms: the re-executed child must have installed its SIGTERM handler before
	// the deadline fires, or it dies of the SIGTERM and the mutations pass (close review N3).
	scaleSerial(t, 2*time.Second)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.launcher(t, loadedAVD+"\n")
	f.emuWrite(t, loadedAVD+".launch.delay", "100000\n")
	f.emuWrite(t, loadedAVD+".launch.child", "")
	f.emuWrite(t, loadedAVD+".launch.childignoreterm", "")

	mustFail(t, f.boot(t, `{"avd":"`+loadedAVD+`"}`), ErrBootTimeout)
	l := f.awaitLaunch(t, loadedAVD)
	child := f.awaitChildPid(t, loadedAVD)
	awaitDead(t, l.pid)
	awaitDead(t, child)
}

// Decision (plan m1i step 5, check; D15; plan m1m step 2, Q1 ¶2): cancelling a boot after
// its serial is resolved stops waiting and leaves the emulator running — from the
// resolution the emulator is a device, not ours to signal. The mutation this fails: `end`
// run after resolution.
func TestCancelDuringBootLeavesTheEmulatorRunning(t *testing.T) {
	scaleBoot(t, 30*time.Second, 20*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.launcher(t, loadedAVD+"\n")
	devicetest.Write(t, f.adbDir, loadedAVD+".bootpolls", []byte("1000000\n"))

	id, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "emulator.start", Params: []byte(`{"avd":"` + loadedAVD + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	// A readiness poll proves the serial is resolved: the cancel that follows is after the
	// boundary, so this is a device (Q1 ¶2).
	pid := f.awaitTableSerial(t, serial)
	f.awaitPolls(t, serial, 1)
	if err := f.ops.Cancel(id); err != nil {
		t.Fatal(err)
	}
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if out.State != capability.StateCancelled {
		t.Fatalf("state = %s (%+v); want cancelled", out.State, out.Error)
	}
	if !alive(pid) {
		t.Error("the cancel ended a resolved emulator; it is a device now, not ours to signal")
	}
}

// Decision (plan m1i step 5, check; D12): emulator.start and emulator.wipe share one boot
// slot, so a second start queues behind the first with blockedBy set and, once the first
// finishes, adopts the now-running AVD without a second launch.
func TestSecondStartQueuesThenAdopts(t *testing.T) {
	scaleBoot(t, 30*time.Second, 20*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.launcher(t, loadedAVD+"\n")
	devicetest.Write(t, f.adbDir, loadedAVD+".bootpolls", []byte("20\n"))

	first, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "emulator.start", Params: []byte(`{"avd":"` + loadedAVD + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if pid := f.awaitTableSerial(t, "emulator-5556"); pid == 0 {
		t.Fatal("the first boot registered nothing")
	}
	second, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "emulator.start", Params: []byte(`{"avd":"` + loadedAVD + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := f.ops.Wait(context.Background(), second, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != capability.StateQueued || snap.BlockedBy == nil || snap.BlockedBy.Capability != "emulator.start" {
		t.Fatalf("second = %s blockedBy %+v; want queued behind the first", snap.State, snap.BlockedBy)
	}

	firstOut, err := f.ops.Wait(context.Background(), first, time.Now().Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	r := mustStart(t, firstOut)
	secondOut, err := f.ops.Wait(context.Background(), second, time.Now().Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	r2 := mustStart(t, secondOut)
	if !r2.Adopted {
		t.Errorf("second = %+v; it should adopt the AVD the first booted", r2)
	}
	if r.Device != r2.Device {
		t.Errorf("serials %q and %q differ", r.Device, r2.Device)
	}
	if strings.Count(strings.Join(emulatortest.Calls(t, f.ldir), "\n"), "-avd "+loadedAVD) != 1 {
		t.Errorf("the launcher was booted more than once: %v", emulatortest.Calls(t, f.ldir))
	}
}

// Decision (plan m1i step 5, check; D12): a stop takes the device's write key, not the
// boot slot, so it runs at once against an AVD a boot just resolved, ends that boot at
// once (EMULATOR_LAUNCH_FAILED, "exited during boot") and returns. The mutation this
// fails: the stop queued behind the boot, or the boot polling to its timeout after the
// launcher is gone.
func TestStopEndsABootInFlight(t *testing.T) {
	scaleBoot(t, 30*time.Second, 20*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.launcher(t, loadedAVD+"\n")
	devicetest.Write(t, f.adbDir, loadedAVD+".bootpolls", []byte("1000000\n"))

	id, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "emulator.start", Params: []byte(`{"avd":"` + loadedAVD + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	f.awaitTableSerial(t, "emulator-5556")

	started := time.Now()
	stopOut := f.invoke(t, "emulator.stop", `{"device":"emulator-5556"}`)
	if stopOut.State != capability.StateCompleted {
		t.Fatalf("stop = %s %+v; want completed at once", stopOut.State, stopOut.Error)
	}
	bootOut, err := f.ops.Wait(context.Background(), id, time.Now().Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	e := mustFail(t, bootOut, ErrLaunchFailed)
	if !strings.Contains(e.Cause, "exited during boot") {
		t.Errorf("cause = %q; want the launcher's exit seen mid-boot", e.Cause)
	}
	if time.Since(started) > 5*time.Second {
		t.Errorf("the boot took %s to end; it polled to the bound, not to the launcher's exit", time.Since(started))
	}
}

// ---- emulator.start: adoption waits, and a vanishing adoption -----------------------

// Decision (plan m1i step 5, check; D15): adoption waits for readiness like a boot — a
// still-booting AVD is not returned until sys.boot_completed turns 1 — and an adoption
// that never becomes ready ends EMULATOR_BOOT_TIMEOUT. The mutation this fails: adoption
// returning as soon as the AVD is listed.
func TestStartAdoptionWaitsForReadiness(t *testing.T) {
	t.Run("waits then adopts", func(t *testing.T) {
		scaleBoot(t, 5*time.Second, 20*time.Millisecond)
		f := newFixture(t, map[string]string{"devices.out": listHead})
		devicetest.RegisterEmulator(f.adbDir, serial, loadedAVD, 4242)
		devicetest.Write(t, f.adbDir, loadedAVD+".bootpolls", []byte("3\n"))
		f.launcher(t, loadedAVD+"\n")

		r := mustStart(t, f.boot(t, `{"avd":"`+loadedAVD+`"}`))
		if !r.Adopted || r.Device != serial {
			t.Fatalf("result = %+v; want an adoption of %s", r, serial)
		}
		if c := f.launchCall(t); c != "" {
			t.Errorf("the launcher was asked to boot an AVD that is already running: %q", c)
		}
	})

	t.Run("times out", func(t *testing.T) {
		scaleBoot(t, 300*time.Millisecond, 20*time.Millisecond)
		f := newFixture(t, map[string]string{"devices.out": listHead})
		devicetest.RegisterEmulator(f.adbDir, serial, loadedAVD, 4242)
		devicetest.Write(t, f.adbDir, loadedAVD+".bootpolls", []byte("1000000\n"))
		f.launcher(t, loadedAVD+"\n")

		e := mustFail(t, f.boot(t, `{"avd":"`+loadedAVD+`"}`), ErrBootTimeout)
		if !strings.Contains(e.Message, serial) {
			t.Errorf("message = %q; want it to name the adopted serial", e.Message)
		}
	})
}

// Decision (plan m1i step 5, check; D12): an adopted serial that leaves while the call
// waits for readiness is DEVICE_DISCONNECTED naming it — a concurrent stop, or the
// emulator dying, is not a boot timeout.
func TestAdoptedSerialRemovedDuringTheWaitIsDisconnected(t *testing.T) {
	scaleBoot(t, 10*time.Second, 20*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	devicetest.RegisterEmulator(f.adbDir, serial, loadedAVD, 4242)
	devicetest.Write(t, f.adbDir, loadedAVD+".bootpolls", []byte("1000000\n"))
	f.launcher(t, loadedAVD+"\n")

	id, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "emulator.start", Params: []byte(`{"avd":"` + loadedAVD + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	// The adoption is under way (its readiness poll has run at least twice), so the
	// concurrent stop below is what makes the serial vanish, not a lost race with it.
	f.awaitPolls(t, serial, 2)
	// A concurrent stop of the adopted serial: it kills it and waits it out, and the
	// adopted boot's next readiness poll sees it gone.
	if out := f.invoke(t, "emulator.stop", `{"device":"`+serial+`"}`); out.State != capability.StateCompleted {
		t.Fatalf("stop = %s %+v", out.State, out.Error)
	}
	bootOut, err := f.ops.Wait(context.Background(), id, time.Now().Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	e := mustFail(t, bootOut, device.ErrDisconnected)
	if !strings.Contains(e.Message, serial) {
		t.Errorf("message = %q; want it to name %s", e.Message, serial)
	}
}

// ---- emulator.wipe ----------------------------------------------------------------

// Decision (plan m1i step 5, check; emulator.md §Wipe, D15): a wipe of a running AVD
// stops it first (emu kill, and the serial's release waited for), then boots it with
// -wipe-data and skips adoption, returning the new serial. The mutation this fails:
// adoption first, so the running (unwiped) instance is returned and no boot happens.
func TestWipeStopsThenBootsWithWipeData(t *testing.T) {
	scaleBoot(t, 10*time.Second, 20*time.Millisecond)
	scaleStop(t, 5*time.Second, 20*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	// The AVD is running on emulator-5554; the wiped boot comes up on emulator-5556.
	devicetest.RegisterEmulator(f.adbDir, serial2, loadedAVD, 4242)
	f.emuWrite(t, loadedAVD+".serial", "emulator-5556\n")
	f.launcher(t, loadedAVD+"\n")

	r := mustWipe(t, f.invoke(t, "emulator.wipe", `{"avd":"`+loadedAVD+`"}`))
	if r.Device != "emulator-5556" || r.Avd != loadedAVD {
		t.Fatalf("result = %+v; want the wiped boot's serial", r)
	}
	if n := countCall(f.calls(t), "-s "+serial2+" emu kill"); n != 1 {
		t.Errorf("%d `emu kill` calls for the running instance, want one", n)
	}
	want := "-avd " + loadedAVD + " -no-boot-anim -no-audio -no-snapshot-save -wipe-data -crash-report-mode never"
	if c := f.launchCall(t); c != want {
		t.Errorf("launch = %q; want %q", c, want)
	}
}

// Decision (plan m1i step 5, check; emulator.md §Wipe): cancelling a wipe after its stop
// and before its spawn leaves the AVD stopped and unwiped — no launcher is started, so no
// data was erased.
func TestWipeCancelledAfterItsStopSpawnsNothing(t *testing.T) {
	scaleBoot(t, 30*time.Second, 20*time.Millisecond)
	scaleStop(t, 10*time.Second, 100*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	devicetest.RegisterEmulator(f.adbDir, serial2, loadedAVD, 4242)
	// The serial stays listed for three answers, so the stop is still waiting when the
	// test cancels.
	devicetest.Write(t, f.adbDir, serial2+".emukill.leave", []byte("3\n"))
	f.launcher(t, loadedAVD+"\n")

	id, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "emulator.wipe", Params: []byte(`{"avd":"` + loadedAVD + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && countCall(f.calls(t), "-s "+serial2+" emu kill") == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if countCall(f.calls(t), "-s "+serial2+" emu kill") != 1 {
		t.Fatalf("the wipe never killed the running instance: %v", f.calls(t))
	}
	if err := f.ops.Cancel(id); err != nil {
		t.Fatal(err)
	}
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if out.State != capability.StateCancelled {
		t.Fatalf("state = %s (%+v); want cancelled", out.State, out.Error)
	}
	if c := f.launchCall(t); c != "" {
		t.Errorf("a launcher was started after the cancel: %q", c)
	}
	f.awaitGone(t, serial2)
}

// Decision (plan m1i step 5, check; D15): each boot's log is truncated, so the second
// boot's file holds only the second boot's output. The mutation this fails: the log
// appended to across boots, mixing two boots' output.
func TestTheLogIsTruncatedAtTheNextBoot(t *testing.T) {
	scaleBoot(t, 10*time.Second, 20*time.Millisecond)
	scaleStop(t, 5*time.Second, 20*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.emuWrite(t, loadedAVD+".serial.1", "emulator-5554\n")
	f.emuWrite(t, loadedAVD+".serial.2", "emulator-5556\n")
	f.launcher(t, loadedAVD+"\n")

	r := mustStart(t, f.boot(t, `{"avd":"`+loadedAVD+`"}`))
	if r.Device != "emulator-5554" {
		t.Fatalf("first boot = %+v", r)
	}
	if log := f.logOf(t, loadedAVD); !strings.Contains(log, "boot 1 of") {
		t.Fatalf("log = %q; want the first boot's line", log)
	}
	mustStop(t, f.invoke(t, "emulator.stop", `{"device":"emulator-5554"}`))

	r2 := mustStart(t, f.boot(t, `{"avd":"`+loadedAVD+`"}`))
	if r2.Adopted || r2.Device != "emulator-5556" {
		t.Fatalf("second boot = %+v", r2)
	}
	log := f.logOf(t, loadedAVD)
	if strings.Contains(log, "boot 1 of") {
		t.Errorf("log = %q; the previous boot's output survived", log)
	}
	if !strings.Contains(log, "boot 2 of "+loadedAVD) {
		t.Errorf("log = %q; want only the second boot's line", log)
	}
}

// jsonString is a JSON string literal for a test parameter.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Decision (spike S8 G11; level 4, step 5): a booting emulator is listed offline for
// seconds and its getprop fails "device offline"; that is not a disconnection, so the
// boot keeps polling until sys.boot_completed. Seen failing at level 4 (a fresh boot
// failed DEVICE_DISCONNECTED) and with the listing check removed.
func TestStartWaitsThroughAnOfflineBoot(t *testing.T) {
	scaleBoot(t, 10*time.Second, 10*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.launcher(t, loadedAVD+"\n")
	devicetest.Write(t, f.adbDir, loadedAVD+".offlinepolls", []byte("3\n"))
	devicetest.Write(t, f.adbDir, loadedAVD+".bootpolls", []byte("1\n"))

	r := mustStart(t, f.boot(t, `{"avd":"`+loadedAVD+`"}`))
	if r.Adopted || r.Device != "emulator-5556" {
		t.Fatalf("result = %+v; want a fresh boot that waited through offline", r)
	}
}
