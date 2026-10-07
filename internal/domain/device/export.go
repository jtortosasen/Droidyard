package device

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// getpropOutputCap bounds a readiness probe's output; `sys.boot_completed` is one digit.
const getpropOutputCap = 256

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

// ADBPath resolves adb for a caller that runs it without the device preamble (the
// emulator subsystem's own stop on emulator.wipe's behalf, which holds no device key and
// runs no hook): the path, or DEVICE_ADB_NOT_FOUND naming what was tried, exactly as
// device.list's resolution answers it.
func (a *ADB) ADBPath(again string) (string, error) {
	res := a.Resolve()
	if !res.Found {
		return "", adbNotFound(res, again)
	}
	return res.Path, nil
}

// BootCompleted reads `-s <serial> shell getprop sys.boot_completed`: value is the
// trimmed property, "1" once the device finished booting; gone is true when adb says the
// serial is no longer there (the device disconnected mid-wait, which is
// DEVICE_DISCONNECTED); err is set when adb could not be run at all, which a readiness
// poll treats as "not yet", never as an error. It is the readiness signal of a boot and
// of an adoption (emulator.md §Boot sequence 5; plan m1i, D15).
func (a *ADB) BootCompleted(ctx context.Context, serial string) (value string, gone bool, err error) {
	res := a.Resolve()
	if !res.Found {
		return "", false, fmt.Errorf("adb was not found")
	}
	out, err := a.Run(ctx, res.Path, bootTimeout, getpropOutputCap, "-s", serial, "shell", "getprop", "sys.boot_completed")
	if err != nil {
		return "", false, err
	}
	if out.ExitCode != 0 {
		if line := disconnectedLine(serial, out); line != "" {
			// A booting emulator is listed offline for seconds (spike S8 G11), and adb
			// then says "device offline": that is not gone. Gone is absent from the
			// listing (level 4, step 5: a fresh boot failed DEVICE_DISCONNECTED).
			if devs, derr := a.Devices(ctx); derr == nil {
				for _, d := range devs {
					if d.Device == serial {
						return "", false, nil
					}
				}
			}
			return "", true, nil
		}
	}
	return strings.TrimSpace(string(out.Stdout)), false, nil
}
