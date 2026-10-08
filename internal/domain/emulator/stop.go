package emulator

import (
	"context"
	"fmt"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
)

// emuOutputCap bounds what `adb emu kill` may print ("OK: killing emulator, bye bye").
const emuOutputCap = 4 << 10

// StopParams is emulator.stop's parameters.
type StopParams struct {
	Device string `json:"device" jsonschema:"required" jsonschema_description:"The serial of the running emulator to stop, as device.list reports it (emulator-<port>)."`
}

// StopResult is emulator.stop's result (plan m1i, D16): the serial it stopped and, when
// the console reported it before the stop, the AVD that serial ran.
type StopResult struct {
	Device string `json:"device" jsonschema:"required"`
	Avd    string `json:"avd,omitempty" jsonschema_description:"The AVD the stopped emulator ran, when its console reported it."`
	// Warnings are adb's (the client/server version mismatch).
	Warnings []string `json:"warnings,omitempty"`
}

func registerStop(reg *capability.Registry, e *Emulator) {
	reg.MustRegister(capability.Descriptor{
		Name:         "emulator.stop",
		Kind:         capability.Command,
		Scope:        capability.Global,
		Since:        "0.8.0",
		Availability: []capability.Requirement{capability.RequiresADB},
		Description:  "Stops one running emulator with adb emu kill and returns once its serial leaves adb devices. Only emulator serials qualify, not a physical device.",
		Params:       StopParams{},
		Result:       StopResult{},
		// The hook's codes without readiness (stop skips it) and without a disconnection
		// it cannot return (review B1).
		Errors: []capability.ErrorCode{device.ErrADBNotFound, device.ErrADBFailed, device.ErrNotFound, device.ErrNotAllowed,
			ErrNotAnEmulator, ErrStopTimeout, capability.ErrOperationQueueTimeout},
		Resource: func(call *capability.Call) string {
			if s := call.Params.(StopParams).Device; s != "" {
				return device.WriteKey(s)
			}
			return ""
		},
		Authorize: func(ctx context.Context, call *capability.Call) (any, error) {
			return e.adb.Authorize(ctx, call.Params.(StopParams).Device, "emulator.stop")
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return e.stop(ctx, call)
		},
	})
}

// stop runs the hook-bound stop: refuse a serial that is not an emulator console, kill it
// through adb, and wait, bounded, until it leaves the listing. It reaches an emulator that
// is booting or offline (the hook ran, the readiness check did not), because a stop must
// be able to end a stuck boot (emulator.md §Stop; plan m1i, D12).
func (e *Emulator) stop(ctx context.Context, call *capability.Call) (StopResult, error) {
	auth := call.Authorized.(*device.Authorized)
	if auth.Device == nil {
		return StopResult{}, notFound(auth)
	}
	if !device.IsEmulatorSerial(auth.Serial) {
		return StopResult{}, notAnEmulator(auth.Serial)
	}
	// The AVD name, read before the kill only when the console answers today. An offline
	// emulator has no console to ask, so the name is simply absent.
	avd := ""
	if auth.Device.Online {
		avd = e.adb.AvdName(ctx, auth.Serial)
	}
	if err := e.stopSerialWith(ctx, auth.ADB, auth.Serial); err != nil {
		return StopResult{}, err
	}
	return StopResult{Device: auth.Serial, Avd: avd, Warnings: auth.Warnings}, nil
}

// stopSerial is emulator.wipe's own stop: it resolves adb itself, as the device preamble
// would, since a wipe holds no device key and runs no hook (plan m1i, D12).
func (e *Emulator) stopSerial(ctx context.Context, serial string) error {
	path, err := e.adb.ADBPath("emulator.wipe")
	if err != nil {
		return err
	}
	return e.stopSerialWith(ctx, path, serial)
}

// killFailedGrace is how long a stop waits for the serial to leave after `emu kill`
// reported failure.
var killFailedGrace = 3 * time.Second

// stopSerialWith runs `adb -s <serial> emu kill` and waits, bounded, until the serial
// leaves adb devices and — when this daemon launched the emulator — our launcher's Wait
// returns (emulator.md §Stop; plan m1i, D12, D15). We never escalate to signals: the
// emulator is not ours. One bound covers the kill and both waits, so the whole stop
// cannot exceed stopTimeout.
func (e *Emulator) stopSerialWith(ctx context.Context, adbPath, serial string) error {
	deadline := time.Now().Add(stopTimeout)
	// A kill that reported failure may still have stopped it, and only the listing says,
	// so the wait follows either way; but after a failed kill it is short, and a serial
	// still listed then is adb's failure, reported at once instead of after the whole
	// bound (level 4, step 5: a refused kill took the full 20 s to time out).
	out, runErr := e.adb.Run(ctx, adbPath, time.Until(deadline), emuOutputCap, "-s", serial, "emu", "kill")
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	failed := runErr != nil || out.ExitCode != 0
	waitUntil := deadline
	if failed {
		if g := time.Now().Add(killFailedGrace); g.Before(waitUntil) {
			waitUntil = g
		}
	}
	gone, err := e.waitGone(ctx, serial, waitUntil)
	if err != nil {
		return err
	}
	if !gone && failed {
		return capability.Errorf(device.ErrADBFailed, "adb could not stop the emulator %s", serial).
			WithCause(fmt.Sprintf("adb -s %s emu kill exited with status %d and the serial stayed listed", serial, out.ExitCode)).
			WithNextStep("check `adb devices` and the emulator; close it from its own window if it is wedged")
	}
	if !gone {
		return stopTimedOut(serial)
	}
	// The launcher is the emulator (step 1 G9: the launcher execs into QEMU), so once the
	// serial has left its Wait returns at once; waiting confirms it and reaps no zombie.
	if l := e.launchForSerial(serial); l != nil {
		exited, err := awaitExit(ctx, l, deadline)
		if err != nil {
			return err
		}
		if !exited {
			return stopTimedOut(serial)
		}
	}
	return nil
}

// waitGone reads the device listing every stopPollInterval until serial leaves it, and no
// later than deadline: at the bound it returns false without reading again. A listing that
// cannot be read is not the answer, so the wait goes on to the bound.
func (e *Emulator) waitGone(ctx context.Context, serial string, deadline time.Time) (bool, error) {
	for {
		if !time.Now().Before(deadline) {
			return false, nil
		}
		pctx, cancel := context.WithDeadline(ctx, deadline)
		devices, err := e.adb.Devices(pctx)
		cancel()
		if cerr := ctx.Err(); cerr != nil {
			return false, cerr
		}
		if err == nil && !listed(devices, serial) {
			return true, nil
		}
		next := time.Now().Add(stopPollInterval)
		if next.After(deadline) {
			next = deadline
		}
		select {
		case <-time.After(time.Until(next)):
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

// listed reports whether serial is in the device listing.
func listed(devices []device.Device, serial string) bool {
	for _, d := range devices {
		if d.Device == serial {
			return true
		}
	}
	return false
}

// notFound is DEVICE_NOT_FOUND for a listed serial that left before the stop, with the
// online emulator serials the caller could use. A serial the listing never held is
// refused earlier, by the device hook (m1k step 5 B1).
func notFound(auth *device.Authorized) *capability.Error {
	online := []string{}
	for _, d := range auth.All {
		if d.Kind == device.KindEmulator && d.State == device.StateDevice {
			online = append(online, d.Device)
		}
	}
	return capability.NewError(device.ErrNotFound, "no device "+auth.Serial+" is connected").
		WithDetails(map[string]any{"online": online}).
		WithNextStep("Call device.list for the serials that are connected; nothing on the device was changed, so this call is safe to repeat.")
}

// notAnEmulator is EMULATOR_NOT_AN_EMULATOR for a serial the console cannot answer, before
// any `emu` command runs.
func notAnEmulator(serial string) *capability.Error {
	return capability.NewError(ErrNotAnEmulator, "the device "+serial+" is not an emulator").
		WithCause("only emulator-<port> serials answer the emulator console; " + serial + " is not one").
		WithNextStep("emulator.stop addresses an emulator's console; use an emulator serial from device.list.")
}

// stopTimedOut is EMULATOR_STOP_TIMEOUT for a kill after which the serial did not leave.
func stopTimedOut(serial string) *capability.Error {
	return capability.NewError(ErrStopTimeout, "the emulator "+serial+" did not stop").
		WithCause("adb -s " + serial + " emu kill did not make " + serial + " leave adb devices within " + stopTimeout.String()).
		WithNextStep("The emulator is not ours to signal: check `adb devices`, and if it is wedged, close it from its own window or reboot the host.")
}
