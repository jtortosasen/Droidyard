package emulator

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
)

// avdName is what an AVD name may be (plan m1i step 5 check): the pattern is checked
// before any path is built or any launcher starts, so a caller string never reaches a
// path or a shell. A bare launcher name: letters, digits, dot, dash and underscore,
// starting with a letter or digit, at most 100 bytes — the shape step 1's recording and
// emulator.list use.
var avdName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// launchGoneGrace bounds how long a boot we launched waits for our launcher's own exit
// after its serial has left the listing (plan m1i, D12; emulator.md §Failure modes): the
// launcher execs into the emulator (step 1 G9), so its exit follows the serial's
// departure, and the boot reports EMULATOR_LAUNCH_FAILED rather than a bare disconnect.
const launchGoneGrace = 2 * time.Second

// boot is the shared boot sequence of emulator.start and emulator.wipe (emulator.md
// §Boot sequence; plan m1i, D15). It snapshots the emulator serials, spawns the launcher
// detached, resolves the new serial by difference and its AVD name, waits for
// `sys.boot_completed`, and returns the serial. adopting is emulator.start's first step
// and emulator.wipe skips it (emulator.md §Wipe 2); a wipe's `-wipe-data` flag is the
// caller's. The one deadline covers the snapshot to readiness, and the serial must be
// resolved by the earlier of it and the serial deadline (ADR-0033 ¶3).
//
// Until the serial is resolved the launch is this daemon's to end (ADR-0033 ¶1): every
// return from here before then ends it, unless its group is already empty. From the
// resolution on it is a device and is never signalled (ADR-0033 ¶2), so a failure in
// awaitReady leaves it running. capability is the calling capability's name, used only to
// phrase the serial-deadline failure (plan m1m, Q1 ¶3).
func (e *Emulator) boot(ctx context.Context, capability, path, avd string, flags []string, deadline time.Time) (string, error) {
	before, err := e.emulatorSerials(ctx)
	if err != nil {
		return "", err
	}
	spawned := time.Now()
	l, err := e.spawn(path, avd, flags)
	if err != nil {
		return "", err
	}
	serial, err := e.awaitSerial(ctx, avd, before, serialDeadline(spawned, deadline), l)
	if err != nil {
		// The serial was never resolved: end the launch (ADR-0033 ¶1) before reporting, so
		// the caller learns whether it was stopped. end is a no-op if the group is empty.
		stopped := e.endLaunch(l)
		if errors.Is(err, errNoSerial) {
			return "", noSerialAppeared(capability, avd, l.pid, stopped, time.Since(spawned), logTail(l.logPath))
		}
		return "", err
	}
	// The serial is resolved: the emulator is a device from here and its pid record goes
	// first, so no daemon that dies after this point lets its successor reach a device
	// (plan m1p Q1 ¶1). If the record cannot be removed the launch is ended, and no serial
	// is handed out.
	if err := e.dropRecord(l); err != nil {
		return "", launchNotReleased(avd, err, l.end())
	}
	l.setSerial(serial)
	if err := e.awaitReady(ctx, serial, deadline, l); err != nil {
		return "", err
	}
	return serial, nil
}

// serialDeadline is the earlier of the serial deadline (spawn + serialTimeout) and the
// boot's own deadline: awaitSerial and its probes are bounded by it (ADR-0033 ¶3).
func serialDeadline(spawned, boot time.Time) time.Time {
	d := spawned.Add(serialTimeout)
	if d.After(boot) {
		return boot
	}
	return d
}

// emulatorSerials is the set of emulator serials `adb devices -l` lists, in any state: a
// serial appearing during a boot first shows offline, so the snapshot must count it too
// (plan m1i, D15; step 1 G11).
func (e *Emulator) emulatorSerials(ctx context.Context) (map[string]bool, error) {
	devices, err := e.adb.Devices(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(devices))
	for _, d := range devices {
		if device.IsEmulatorSerial(d.Device) {
			out[d.Device] = true
		}
	}
	return out, nil
}

// runningSerialOf is the serial of the emulator running avd, "" when none is: the
// adoption test (emulator.md §Boot sequence 1), read through `emu avd name` on every
// online emulator.
func (e *Emulator) runningSerialOf(ctx context.Context, avd string) (string, error) {
	devices, err := e.adb.Devices(ctx)
	if err != nil {
		return "", err
	}
	for _, d := range devices {
		// Offline counts: a boot cancelled seconds in leaves an emulator listed offline
		// that answers `emu avd name` (spike S8 G11); skipping it would let a wipe launch
		// -wipe-data beside the live instance (close review N1).
		if d.Kind != device.KindEmulator {
			continue
		}
		if e.adb.AvdName(ctx, d.Device) == avd {
			return d.Device, nil
		}
	}
	return "", nil
}

// errNoSerial marks a boot that reached the serial deadline without resolving a serial
// (ADR-0033 ¶3). boot turns it into noSerialAppeared once it knows whether the launch was
// stopped, which only `end` can say.
var errNoSerial = errors.New("no emulator serial appeared")

// awaitSerial polls the device listing until a serial that was not there before the
// spawn reports the requested AVD through `emu avd name` (emulator.md §Boot sequence 4),
// the launcher exits, or the deadline passes. Reading the launch table on every poll is
// what turns a launcher that died during a boot into EMULATOR_LAUNCH_FAILED at once
// (plan m1i, D12, D15) instead of a boot timeout 180 s later. The deadline is the serial
// deadline, and both probes are bounded by it (ADR-0033 ¶3). A resolved serial wins over a
// context that ends in the same instant, so a cancel racing the resolution never ends a
// launch that has become a device (ADR-0033 ¶2).
func (e *Emulator) awaitSerial(ctx context.Context, avd string, before map[string]bool, deadline time.Time, l *launch) (string, error) {
	for {
		if l != nil && l.exited() {
			return "", launchExited(l, "exited during boot")
		}
		if !time.Now().Before(deadline) {
			return "", errNoSerial
		}
		if serial := e.serialAppeared(ctx, avd, before, deadline); serial != "" {
			return serial, nil
		}
		if cerr := ctx.Err(); cerr != nil {
			return "", cerr
		}
		if err := sleepUntil(ctx, deadline); err != nil {
			return "", err
		}
	}
}

// serialAppeared reads the device listing once, bounded by deadline, and returns the new
// emulator serial whose `emu avd name` is avd, "" when none has yet. Both probes are
// bounded as awaitReady's are, so a hanging adb cannot stretch the serial wait past its
// bound. An unreadable listing is not an answer: it reports "" and the caller polls on.
func (e *Emulator) serialAppeared(ctx context.Context, avd string, before map[string]bool, deadline time.Time) string {
	pctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	devices, err := e.adb.Devices(pctx)
	if err != nil {
		return ""
	}
	for i := range devices {
		d := devices[i]
		if !device.IsEmulatorSerial(d.Device) || before[d.Device] {
			continue
		}
		if e.adb.AvdName(pctx, d.Device) == avd {
			return d.Device
		}
	}
	return ""
}

// awaitReady polls `sys.boot_completed` every bootPollInterval until it is 1
// (emulator.md §Boot sequence 5), the launcher exits, the serial leaves, or the deadline
// passes. It serves a fresh boot (l is our launcher) and an adoption (l is nil) alike:
// both wait for readiness within the same bound (plan m1i, D15).
func (e *Emulator) awaitReady(ctx context.Context, serial string, deadline time.Time, l *launch) error {
	for {
		if l != nil && l.exited() {
			return launchExited(l, "exited during boot")
		}
		if !time.Now().Before(deadline) {
			return bootTimedOut(serial)
		}
		// The probe is bounded by the boot's own deadline as well as by its own timeout,
		// so a hanging adb cannot stretch a boot past the 180 s bound (emulator.md
		// §Timeouts).
		pctx, cancel := context.WithDeadline(ctx, deadline)
		value, gone, err := e.adb.BootCompleted(pctx, serial)
		cancel()
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if gone {
			// The serial left before our launcher's Wait returned. For a boot we
			// launched the launcher *is* the emulator (step 1 G9), so its exit follows
			// the serial's departure: wait a moment for it and report its end, which is
			// the explanation a caller can act on (plan m1i, D12). An adoption has no
			// launcher of ours; a serial that leaves is simply disconnected.
			if l != nil {
				until := time.Now().Add(launchGoneGrace)
				if until.After(deadline) {
					until = deadline
				}
				if exited, err := awaitExit(ctx, l, until); err != nil {
					return err
				} else if exited {
					return launchExited(l, "exited during boot")
				}
			}
			return disconnectedDuringBoot(serial)
		}
		if err == nil && value == "1" {
			return nil
		}
		if err := sleepUntil(ctx, deadline); err != nil {
			return err
		}
	}
}

// sleepUntil waits one poll interval, no longer than the deadline, or until ctx ends.
func sleepUntil(ctx context.Context, deadline time.Time) error {
	next := time.Now().Add(bootPollInterval)
	if next.After(deadline) {
		next = deadline
	}
	t := time.NewTimer(time.Until(next))
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// listAVDs runs `emulator -list-avds` and returns the AVD names the launcher knows
// (emulator.md §AVD discovery). Its failure is EMULATOR_LAUNCH_FAILED, as listing's.
func (e *Emulator) listAVDs(ctx context.Context, path string) ([]string, error) {
	out, err := e.launcherRun(ctx, path, listTimeout, launcherOutputCap, "-list-avds")
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}
	if err != nil {
		return nil, launcherFailed(path, "emulator -list-avds", out, err)
	}
	if out.ExitCode != 0 {
		return nil, launcherFailed(path, "emulator -list-avds", out, nil)
	}
	return parseListAvds(out.Stdout), nil
}

// containsName reports whether names holds avd.
func containsName(names []string, avd string) bool {
	for _, n := range names {
		if n == avd {
			return true
		}
	}
	return false
}

// avdNotFound is EMULATOR_AVD_NOT_FOUND, listing the names the launcher knows
// (emulator.md §Failure modes).
func avdNotFound(avd string, names []string) *capability.Error {
	if names == nil {
		names = []string{}
	}
	return capability.NewError(ErrAVDNotFound, "no AVD is named "+avd).
		WithDetails(map[string]any{"avds": names}).
		WithNextStep("Call emulator.list for the AVD names the launcher knows.")
}

// noSerialAppeared is EMULATOR_BOOT_TIMEOUT for a boot whose serial never showed within
// the serial deadline (emulator.md §Failure modes: "No serial appears within the timeout";
// ADR-0033 ¶3). Its cause names the launcher's pid, whether it was stopped, the time
// waited and the launcher log's tail; its nextStep names the remedy, which differs between
// a start and a wipe (plan m1m, Q1 ¶3).
func noSerialAppeared(capabilityName, avd string, pid int, stopped bool, elapsed time.Duration, tail string) *capability.Error {
	fate := "was stopped"
	if !stopped {
		fate = "could not be stopped; end its process by hand"
	}
	cause := "no new emulator serial answered as " + avd + " within " + serialTimeout.String() +
		" (waited " + elapsed.Round(time.Millisecond).String() + "); the launcher pid " + strconv.Itoa(pid) + " " + fate
	if tail != "" {
		cause += "; its log ends: " + tail
	}
	err := capability.NewError(ErrBootTimeout, "no emulator for "+avd+" appeared").WithCause(cause)
	if capabilityName == "emulator.wipe" {
		return err.WithNextStep("The wipe may be partial: call emulator.wipe again, with headless: true if a windowed emulator may be waiting on a dialog nobody sees. Wiping again is safe to repeat once no instance of the AVD is left running.")
	}
	return err.WithNextStep("Retry with headless: true — a windowed emulator can wait on a dialog nobody sees — and with coldBoot: true if its last instance did not shut down cleanly. This call is safe to repeat once no instance of the AVD is left running.")
}

// bootTimedOut is EMULATOR_BOOT_TIMEOUT for a device that appeared but never finished
// booting (emulator.md §Failure modes: "Device appears but never finishes booting"). Its
// serial is resolved, so the emulator is a device, not ours to signal (ADR-0033 ¶2); its
// nextStep names the remedy the serial answers to, emulator.stop (plan m1m, step 3).
func bootTimedOut(serial string) *capability.Error {
	return capability.NewError(ErrBootTimeout, "the emulator "+serial+" did not finish booting").
		WithCause("sys.boot_completed was not 1 after " + bootTimeout.String()).
		WithNextStep("The device is not ours to signal: call emulator.stop {device: \"" + serial + "\"} to end the wedged emulator, then start it again. Until that instance is stopped, do not repeat this call: the same AVD is still running.")
}

// disconnectedDuringBoot is DEVICE_DISCONNECTED for an adopted serial that left while the
// boot waited for readiness (plan m1i, D12): a concurrent stop, or the emulator dying.
func disconnectedDuringBoot(serial string) *capability.Error {
	return capability.NewError(device.ErrDisconnected, "the device "+serial+" disconnected during the call").
		WithCause("the serial left adb devices before sys.boot_completed was 1").
		WithNextStep("call emulator.start again")
}
