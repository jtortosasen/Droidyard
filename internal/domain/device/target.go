package device

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The codes of the device preamble every capability with a `device` argument runs
// (adb-device.md §Targeting, §Failure modes).
const (
	ErrNotFound     capability.ErrorCode = "DEVICE_NOT_FOUND"
	ErrNotReady     capability.ErrorCode = "DEVICE_NOT_READY"
	ErrNotAllowed   capability.ErrorCode = "DEVICE_NOT_ALLOWED"
	ErrDisconnected capability.ErrorCode = "DEVICE_DISCONNECTED"
)

// PreambleErrors lists the codes the preamble can return, for a descriptor's Errors.
var PreambleErrors = []capability.ErrorCode{ErrADBNotFound, ErrADBFailed, ErrNotFound, ErrNotReady, ErrNotAllowed, ErrDisconnected}

// bootTimeout bounds `getprop sys.boot_completed`; a variable so a test is quick.
var bootTimeout = 3 * time.Second

// target is what the authorization hook read and hands to the handler: the adb it
// resolved and the call's one `devices -l`.
type target struct {
	adb    string
	serial string
	dev    *Device // nil when the serial is not listed
	all    []Device
	// warnings are adb's (the client/server version mismatch), carried on the result
	// as device.list carries them (adb-device.md §Failure modes).
	warnings []string
}

// authorize is the authorization hook of a device capability (plan m1e, Q2). It runs
// the call's one `devices -l` and decides DEVICE_NOT_ALLOWED for a physical device
// (the emulators-only default, ADR-0015 §4; no configuration file exists in 1e), then
// hands the listing on. A serial that is not listed is not decided here: the handler
// answers DEVICE_NOT_FOUND from the same listing.
func (a *ADB) authorize(ctx context.Context, serial, again string) (*target, error) {
	res := a.Resolve()
	if !res.Found {
		return nil, adbNotFound(res, again)
	}
	// The client/server version comparison, once per resolved binary and before any
	// command that could restart the shared server; its warnings go on the result.
	warnings := a.Check(ctx).Warnings
	all, err := a.listAll(ctx, res.Path)
	if err != nil {
		return nil, err
	}
	t := &target{adb: res.Path, serial: serial, all: all, warnings: warnings}
	for i := range all {
		if all[i].Device == serial {
			t.dev = &all[i]
			break
		}
	}
	if t.dev != nil && t.dev.Kind != KindEmulator {
		return nil, capability.NewError(ErrNotAllowed, "the device "+serial+" is not permitted by policy").
			WithCause("the default policy admits emulators only; the device is a physical device").
			WithNextStep("Use an emulator serial from device.list.")
	}
	return t, nil
}

// ready decides what the listing says about the device: DEVICE_NOT_FOUND with the
// online candidates, DEVICE_NOT_READY naming the state, and DEVICE_NOT_READY for a
// device that reports `device` but has not finished booting (sys.boot_completed not 1).
func (a *ADB) ready(ctx context.Context, t *target) error {
	if t.dev == nil {
		online := []string{}
		for _, d := range t.all {
			if d.Kind == KindEmulator && d.State == StateDevice {
				online = append(online, d.Device)
			}
		}
		return capability.NewError(ErrNotFound, "no device "+t.serial+" is connected").
			WithDetails(map[string]any{"online": online}).
			WithNextStep("Call device.list for the serials that are connected.")
	}
	if t.dev.State != StateDevice {
		fix := map[string]string{
			StateUnauthorized: "Accept the debugging prompt on the device, then retry.",
			StateOffline:      "Wait for the device to come back, or restart it, then retry.",
			StateRecovery:     "The device is in recovery mode: boot it normally, then retry.",
			StateSideload:     "The device is in sideload mode: boot it normally, then retry.",
		}[t.dev.State]
		if fix == "" {
			fix = "Call device.list until its state is device, then retry."
		}
		return capability.NewError(ErrNotReady, "the device "+t.serial+" is "+t.dev.State).
			WithDetails(map[string]any{"state": t.dev.State}).
			WithNextStep(fix)
	}
	out, err := a.Run(ctx, t.adb, bootTimeout, 256, "-s", t.serial, "shell", "getprop", "sys.boot_completed")
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if err != nil {
		what := err.Error()
		if !errors.Is(err, ErrTimeout) {
			// adb could not be started: a host failure, not a device that is not
			// answering (Q3).
			return capability.NewError(ErrADBFailed, "adb could not be run").
				WithCause("adb shell getprop sys.boot_completed: " + what).
				WithNextStep("Check that the adb binary is executable, then retry.")
		}
		what = "did not answer in " + bootTimeout.String()
		return capability.NewError(ErrNotReady, "the device "+t.serial+" is not answering").
			WithCause("adb shell getprop sys.boot_completed " + what).
			WithDetails(map[string]any{"state": "unresponsive"}).
			WithNextStep("Retry in a few seconds; if it persists, restart the device.")
	}
	if line := disconnectedLine(t.serial, out); line != "" {
		return disconnected(t.serial, line)
	}
	if out.ExitCode == 0 && strings.TrimSpace(string(out.Stdout)) == "1" {
		return nil
	}
	cause := "sys.boot_completed is " + fmt.Sprintf("%q", strings.TrimSpace(string(out.Stdout)))
	if out.ExitCode != 0 {
		cause = fmt.Sprintf("adb shell getprop sys.boot_completed exited with status %d", out.ExitCode)
		if l := decisiveLine(out); l != "" {
			cause += ": " + l
		}
	}
	return capability.NewError(ErrNotReady, "the device "+t.serial+" is still booting").
		WithCause(cause).
		WithDetails(map[string]any{"state": "booting"}).
		WithNextStep("Wait until sys.boot_completed is 1 (a few seconds to a minute after boot), then retry.")
}

func disconnected(serial, line string) *capability.Error {
	return capability.NewError(ErrDisconnected, "the device "+serial+" disconnected during the call").
		WithCause(line).
		WithNextStep("Call device.list to see whether it is still there, then retry.")
}

// disconnectedLine returns the line of out's own stderr or stdout (exec-out passes the
// device's error text through stdout) that says the serial is gone, "" if none does:
// the call's own error, with no extra `devices -l`. adb says "device 'S' not found",
// "device offline" and "no devices/emulators found" when a transport drops.
func disconnectedLine(serial string, out Output) string {
	re := regexp.MustCompile(`(?i)device '` + regexp.QuoteMeta(serial) + `' (not found|offline)|device offline|no devices/emulators found`)
	for _, b := range [][]byte{out.Stderr, out.Stdout} {
		for _, l := range strings.Split(string(b), "\n") {
			if re.MatchString(l) {
				return truncate(strings.TrimSpace(l), 200)
			}
		}
	}
	return ""
}
