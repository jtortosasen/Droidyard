package emulator

import (
	"context"
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
		Description: "Stops one running emulator with `adb emu kill` and returns once its serial leaves adb devices, bounded at 20 s; a timeout is EMULATOR_STOP_TIMEOUT and we never escalate to signals, because the emulator is not ours. It holds the device's write lock, so it serialises with installs and other writes to that device and can end a stuck boot. " +
			"Only emulator-<port> serials answer the console: anything else — a physical device, or an emulator reached over ip:port — is EMULATOR_NOT_AN_EMULATOR. The call runs the device preamble's policy check (a physical device is DEVICE_NOT_ALLOWED) but not its readiness check, so an emulator that is still booting or offline can be stopped. " +
			"Stopping is not idempotent: a second call for an already-gone serial is DEVICE_NOT_FOUND. " +
			"Fails with the preamble's errors (DEVICE_ADB_NOT_FOUND, DEVICE_ADB_FAILED, DEVICE_NOT_FOUND, DEVICE_NOT_ALLOWED, DEVICE_DISCONNECTED), EMULATOR_NOT_AN_EMULATOR, EMULATOR_STOP_TIMEOUT, and OPERATION_QUEUE_TIMEOUT behind another write to the same device.",
		Params: StopParams{},
		Result: StopResult{},
		Errors: append(append([]capability.ErrorCode{}, device.PreambleErrors...),
			ErrNotAnEmulator, ErrStopTimeout, capability.ErrOperationQueueTimeout),
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

	// One bound covers the kill and the wait, so the whole stop cannot exceed stopTimeout.
	deadline := time.Now().Add(stopTimeout)
	// The exit status is ignored: a kill that reported failure may still have stopped it,
	// and only the listing says. A successful one may too, so the wait always follows.
	_, _ = e.adb.Run(ctx, auth.ADB, time.Until(deadline), emuOutputCap, "-s", auth.Serial, "emu", "kill")
	if cerr := ctx.Err(); cerr != nil {
		return StopResult{}, cerr
	}
	gone, err := e.waitGone(ctx, auth.Serial, deadline)
	if err != nil {
		return StopResult{}, err
	}
	if !gone {
		return StopResult{}, stopTimedOut(auth.Serial)
	}
	return StopResult{Device: auth.Serial, Avd: avd, Warnings: auth.Warnings}, nil
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

// notFound is DEVICE_NOT_FOUND for a serial the listing does not have, with the online
// emulator serials the caller could use, exactly as the device preamble's readiness check
// answers it.
func notFound(auth *device.Authorized) *capability.Error {
	online := []string{}
	for _, d := range auth.All {
		if d.Kind == device.KindEmulator && d.State == device.StateDevice {
			online = append(online, d.Device)
		}
	}
	return capability.NewError(device.ErrNotFound, "no device "+auth.Serial+" is connected").
		WithDetails(map[string]any{"online": online}).
		WithNextStep("Call device.list for the serials that are connected.")
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
