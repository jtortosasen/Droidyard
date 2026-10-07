package device

import (
	"context"
	"time"
)

// What the emulator subsystem (internal/domain/emulator) needs from this package,
// exported rather than duplicated (plan m1i step 4): the device preamble's policy hook,
// the raw listing and the emulator console's AVD name. The emulator package owns no
// device logic, so these are the whole seam between the two.

// avdNameTimeout bounds `emu avd name`; a variable so a test of the timeout is quick.
var avdNameTimeout = 3 * time.Second

// Authorized is one device call's authorization hook, projected. It is what a caller
// that runs the hook but not the readiness check that follows it reads: emulator.stop
// must reach an emulator that is still booting or offline, which the readiness check
// refuses (emulator.md §Stop; plan m1i, B3, D12).
type Authorized struct {
	// ADB is the resolved adb binary the call's commands run with.
	ADB string
	// Serial is the serial the caller named.
	Serial string
	// Device is the serial's entry in the listing, nil when it is not listed.
	Device *Device
	// All is the whole `adb devices -l` listing the hook read, for the online
	// candidates an error reports.
	All []Device
	// Warnings are adb's (the client/server version mismatch).
	Warnings []string
}

// Authorize runs a device call's authorization hook over serial — the policy re-check
// and the call's one `devices -l` (ADR-0015 §4) — and returns what it read, without the
// readiness check a device capability runs next. A physical device is refused
// DEVICE_NOT_ALLOWED here, exactly as a device call refuses it; a serial that is not
// listed is not an error (Device is nil) and the caller decides, as the preamble
// answers DEVICE_NOT_FOUND. again names the capability, for the error's nextStep.
func (a *ADB) Authorize(ctx context.Context, serial, again string) (*Authorized, error) {
	t, err := a.authorize(ctx, serial, again)
	if err != nil {
		return nil, err
	}
	return &Authorized{ADB: t.adb, Serial: t.serial, Device: t.dev, All: t.all, Warnings: t.warnings}, nil
}

// Devices reads `adb devices -l` raw: every device adb reports, physical ones included,
// with none of the details List adds. The emulator subsystem reads it for the online
// emulator serials and, while a stop waits, to see a serial leave. Its errors are
// DEVICE_ADB_NOT_FOUND and DEVICE_ADB_FAILED, as device.list's.
func (a *ADB) Devices(ctx context.Context) ([]Device, error) {
	res := a.Resolve()
	if !res.Found {
		return nil, adbNotFound(res, "emulator.list")
	}
	return a.listAll(ctx, res.Path)
}

// AvdName reads the AVD name a running emulator's console reports (`adb -s <serial> emu
// avd name`): the one way to map a serial to the AVD it runs (emulator.md
// §Capabilities). "" when adb cannot be found, the serial is not an emulator console, or
// the console does not answer, so a caller treats "" as "unknown", never as an error.
func (a *ADB) AvdName(ctx context.Context, serial string) string {
	if !emulatorSerial.MatchString(serial) {
		return ""
	}
	res := a.Resolve()
	if !res.Found {
		return ""
	}
	out, err := a.Run(ctx, res.Path, avdNameTimeout, avdOutputCap, "-s", serial, "emu", "avd", "name")
	if err != nil || out.ExitCode != 0 {
		return ""
	}
	return parseAvdName(string(out.Stdout))
}
