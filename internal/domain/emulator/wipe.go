package emulator

import (
	"context"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
)

// WipeParams is emulator.wipe's parameters (plan m1i, D16). It has no coldBoot: a wipe
// implies a cold boot (emulator.md §Wipe).
type WipeParams struct {
	Avd string `json:"avd" jsonschema:"required" jsonschema_description:"The AVD to factory-reset, as emulator.list names it."`
	// Headless adds -no-window, as emulator.start's does.
	Headless bool `json:"headless,omitempty" jsonschema_description:"Boot with no window (adds -no-window)."`
}

// WipeResult is emulator.wipe's result (plan m1i, D16): the serial the wiped AVD runs
// on; the AVD is left running, factory-fresh (emulator.md §Wipe 3).
type WipeResult struct {
	Device string `json:"device" jsonschema:"required" jsonschema_description:"The running emulator's serial after the wipe."`
	Avd    string `json:"avd" jsonschema:"required" jsonschema_description:"The AVD that was wiped."`
	// Warnings are adb's (the client/server version mismatch).
	Warnings []string `json:"warnings,omitempty"`
}

func registerWipe(reg *capability.Registry, e *Emulator) {
	reg.MustRegister(capability.Descriptor{
		Name:         "emulator.wipe",
		Kind:         capability.Command,
		Scope:        capability.Global,
		Statefulness: capability.Stateless,
		LongRunning:  true,
		Since:        "0.8.0",
		Availability: []capability.Requirement{capability.RequiresEmulator, capability.RequiresADB},
		Description:  "Factory-resets one AVD's user data by booting it again with -wipe-data, stopping it first if it is running, and returns {device, avd} once it has booted. A wipe that fails or is cancelled may be partial: call it again.",
		Params:       WipeParams{},
		Target:       func(p any) *capability.OpTarget { return &capability.OpTarget{AVD: p.(WipeParams).Avd} },
		Result:       WipeResult{},
		Errors: []capability.ErrorCode{device.ErrADBNotFound, device.ErrADBFailed, device.ErrDisconnected,
			ErrLauncherNotFound, ErrAVDNotFound, ErrLaunchFailed, ErrAVDLocked, ErrBootTimeout, ErrStopTimeout,
			capability.ErrInvalidParams},
		Resource: func(*capability.Call) string { return bootKey },
		Annotations: capability.Annotations{
			Destructive: boolPtr(true),
			Reason:      "it erases the AVD's user data, a factory reset that cannot be undone (ADR-0020 §Annotations)",
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return e.wipe(ctx, call)
		},
	})
}

// wipe runs emulator.md §Wipe: stop a running instance and wait for its release, boot
// with -wipe-data skipping adoption, and return the AVD running (plan m1i, D15, D16).
func (e *Emulator) wipe(ctx context.Context, call *capability.Call) (WipeResult, error) {
	p := call.Params.(WipeParams)
	if !avdName.MatchString(p.Avd) {
		return WipeResult{}, invalidAVDName(p.Avd)
	}
	res := e.Resolve()
	if !res.Found {
		return WipeResult{}, launcherNotFound(res, "emulator.wipe")
	}
	names, err := e.listAVDs(ctx, res.Path)
	if err != nil {
		return WipeResult{}, err
	}
	if !containsName(names, p.Avd) {
		return WipeResult{}, avdNotFound(p.Avd, names)
	}

	// 1. Stop a running instance and wait for its release (emulator.md §Wipe 1). The
	// release is the serial leaving adb devices: step 1's G12 shows the AVD's
	// multiinstance.lock outlives the kill without blocking the next boot, so the stop's
	// own wait is the whole wait.
	serial, err := e.runningSerialOf(ctx, p.Avd)
	if err != nil {
		return WipeResult{}, err
	}
	if serial != "" {
		if err := e.stopSerial(ctx, serial); err != nil {
			return WipeResult{}, err
		}
	}
	// Cancelling after the stop and before the spawn leaves the AVD stopped and unwiped
	// (emulator.md §Wipe).
	if cerr := ctx.Err(); cerr != nil {
		return WipeResult{}, cerr
	}

	deadline := time.Now().Add(bootTimeout)
	booted, err := e.boot(ctx, "emulator.wipe", res.Path, p.Avd, wipeFlags(p), deadline)
	if err != nil {
		return WipeResult{}, err
	}
	return WipeResult{Device: booted, Avd: p.Avd, Warnings: warnings(res.Warnings)}, nil
}

// wipeFlags is emulator.md §Flags and configuration's default set plus -wipe-data, which
// implies a cold boot (step 1's G13).
func wipeFlags(p WipeParams) []string {
	flags := []string{"-no-boot-anim", "-no-audio", "-no-snapshot-save", "-wipe-data"}
	if p.Headless {
		flags = append(flags, "-no-window")
	}
	return flags
}
