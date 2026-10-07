package emulator

import (
	"context"
	"regexp"
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
// `sys.boot_completed`, and returns the serial. adopt is emulator.start's first step and
// emulator.wipe skips it (emulator.md §Wipe 2); a wipe's `-wipe-data` flag is the
// caller's. The one deadline covers the snapshot to readiness.
func (e *Emulator) boot(ctx context.Context, path, avd string, flags []string, deadline time.Time) (string, error) {
	before, err := e.emulatorSerials(ctx)
	if err != nil {
		return "", err
	}
	l, err := e.spawn(path, avd, flags)
	if err != nil {
		return "", err
	}
	serial, err := e.awaitSerial(ctx, avd, before, deadline, l)
	if err != nil {
		return "", err
	}
	l.setSerial(serial)
	if err := e.awaitReady(ctx, serial, deadline, l); err != nil {
		return "", err
	}
	return serial, nil
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
		if d.Kind != device.KindEmulator || !d.Online {
			continue
		}
		if e.adb.AvdName(ctx, d.Device) == avd {
			return d.Device, nil
		}
	}
	return "", nil
}

// awaitSerial polls the device listing until a serial that was not there before the
// spawn reports the requested AVD through `emu avd name` (emulator.md §Boot sequence 4),
// the launcher exits, or the deadline passes. Reading the launch table on every poll is
// what turns a launcher that died during a boot into EMULATOR_LAUNCH_FAILED at once
// (plan m1i, D12, D15) instead of a boot timeout 180 s later.
func (e *Emulator) awaitSerial(ctx context.Context, avd string, before map[string]bool, deadline time.Time, l *launch) (string, error) {
	for {
		if l != nil && l.exited() {
			return "", launchExited(l, "exited during boot")
		}
		if !time.Now().Before(deadline) {
			return "", noSerialAppeared(avd)
		}
		devices, err := e.adb.Devices(ctx)
		if cerr := ctx.Err(); cerr != nil {
			return "", cerr
		}
		if err == nil {
			for i := range devices {
				d := devices[i]
				if !device.IsEmulatorSerial(d.Device) || before[d.Device] {
					continue
				}
				if e.adb.AvdName(ctx, d.Device) == avd {
					return d.Device, nil
				}
			}
		}
		if err := sleepUntil(ctx, deadline); err != nil {
			return "", err
		}
	}
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

// noSerialAppeared is EMULATOR_BOOT_TIMEOUT for a boot whose serial never showed
// (emulator.md §Failure modes: "No serial appears within the timeout").
func noSerialAppeared(avd string) *capability.Error {
	return capability.NewError(ErrBootTimeout, "no emulator for "+avd+" appeared").
		WithCause("no new emulator serial appeared in adb devices within " + bootTimeout.String()).
		WithNextStep("Run `emulator -avd " + avd + "` by hand to see whether it starts; its output is under emulator/ in the state directory.")
}

// bootTimedOut is EMULATOR_BOOT_TIMEOUT for a device that appeared but never finished
// booting (emulator.md §Failure modes: "Device appears but never finishes booting").
func bootTimedOut(serial string) *capability.Error {
	return capability.NewError(ErrBootTimeout, "the emulator "+serial+" did not finish booting").
		WithCause("sys.boot_completed was not 1 after " + bootTimeout.String()).
		WithNextStep("The emulator is not ours to signal: check `adb devices` and sys.boot_completed, then start it again. A wedged emulator may need stopping first.")
}

// disconnectedDuringBoot is DEVICE_DISCONNECTED for an adopted serial that left while the
// boot waited for readiness (plan m1i, D12): a concurrent stop, or the emulator dying.
func disconnectedDuringBoot(serial string) *capability.Error {
	return capability.NewError(device.ErrDisconnected, "the device "+serial+" disconnected during the call").
		WithCause("the serial left adb devices before sys.boot_completed was 1").
		WithNextStep("call emulator.start again")
}
