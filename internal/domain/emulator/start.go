package emulator

import (
	"context"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
)

// StartParams is emulator.start's parameters (plan m1i, D16): the AVD to boot and the
// two flags the caller chooses.
type StartParams struct {
	Avd string `json:"avd" jsonschema:"required" jsonschema_description:"The AVD to boot, as emulator.list names it."`
	// ColdBoot adds -no-snapshot-load, so the boot ignores a saved snapshot (emulator.md
	// §Flags and configuration).
	ColdBoot bool `json:"coldBoot,omitempty" jsonschema_description:"Boot without loading a saved snapshot (adds -no-snapshot-load)."`
	// Headless adds -no-window, which is what makes the emulator viable with no display
	// (emulator.md §Flags and configuration).
	Headless bool `json:"headless,omitempty" jsonschema_description:"Boot with no window (adds -no-window)."`
}

// StartResult is emulator.start's result (plan m1i, D16): the serial the AVD runs on,
// whether it was adopted or booted, and how long a boot took.
type StartResult struct {
	Device  string `json:"device" jsonschema:"required" jsonschema_description:"The running emulator's serial, ready for every device capability."`
	Avd     string `json:"avd" jsonschema:"required" jsonschema_description:"The AVD the serial runs."`
	Adopted bool   `json:"adopted" jsonschema:"required" jsonschema_description:"True when the AVD was already running and nothing was spawned."`
	BootMs  int64  `json:"bootMs,omitempty" jsonschema_description:"How long a fresh boot took, in milliseconds; absent when the AVD was adopted."`
	// Warnings are adb's (the client/server version mismatch).
	Warnings []string `json:"warnings,omitempty"`
}

func registerStart(reg *capability.Registry, e *Emulator) {
	reg.MustRegister(capability.Descriptor{
		Name:         "emulator.start",
		Kind:         capability.Command,
		Scope:        capability.Global,
		Statefulness: capability.Stateless,
		LongRunning:  true,
		Idempotent:   true,
		Since:        "0.8.0",
		Availability: []capability.Requirement{capability.RequiresEmulator, capability.RequiresADB},
		Description:  "Boots one AVD and returns {device, avd, adopted} once its device has finished booting. An AVD that is already running is adopted, not restarted, and boots are serialised. Cancelling it before the device appears ends the launch, and afterwards only stops waiting and leaves the emulator running.",
		Params:       StartParams{},
		Target:       func(p any) *capability.OpTarget { return &capability.OpTarget{AVD: p.(StartParams).Avd} },
		Result:       StartResult{},
		// Only what the handler returns (close review B1): no preamble runs, and a
		// long-running call has no queue bound (ADR-0020).
		Errors: []capability.ErrorCode{device.ErrADBNotFound, device.ErrADBFailed, device.ErrDisconnected,
			ErrLauncherNotFound, ErrAVDNotFound, ErrLaunchFailed, ErrAVDLocked, ErrBootTimeout,
			capability.ErrInvalidParams},
		Resource: func(*capability.Call) string { return bootKey },
		Annotations: capability.Annotations{
			Destructive: boolPtr(false),
			Reason:      "it adopts a running AVD and joins a boot already in flight, so a repeat changes nothing",
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return e.start(ctx, call)
		},
	})
}

// bootKey is the one scheduler key emulator.start and emulator.wipe hold
// (plan m1i, D12): boots are serialised, so a second boot queues behind the first and
// then adopts it.
const bootKey = "emulator.boot"

// start runs the boot sequence of emulator.md §Boot sequence: adopt a running AVD, else
// snapshot, spawn, resolve by difference and wait for readiness (plan m1i, D15).
func (e *Emulator) start(ctx context.Context, call *capability.Call) (StartResult, error) {
	p := call.Params.(StartParams)
	if !avdName.MatchString(p.Avd) {
		return StartResult{}, invalidAVDName(p.Avd)
	}
	res := e.Resolve()
	if !res.Found {
		return StartResult{}, launcherNotFound(res, "emulator.start")
	}
	names, err := e.listAVDs(ctx, res.Path)
	if err != nil {
		return StartResult{}, err
	}
	if !containsName(names, p.Avd) {
		return StartResult{}, avdNotFound(p.Avd, names)
	}
	deadline := time.Now().Add(bootTimeout)

	// 1. Adopt first (emulator.md §Boot sequence 1). An adopted boot still waits for
	// readiness, within the same bound (plan m1i, D15).
	if serial, err := e.runningSerialOf(ctx, p.Avd); err != nil {
		return StartResult{}, err
	} else if serial != "" {
		if err := e.awaitReady(ctx, serial, deadline, nil); err != nil {
			return StartResult{}, err
		}
		return StartResult{Device: serial, Avd: p.Avd, Adopted: true, Warnings: warnings(res.Warnings)}, nil
	}

	// 2–6. Snapshot, spawn, resolve and wait.
	started := time.Now()
	serial, err := e.boot(ctx, "emulator.start", res.Path, p.Avd, startFlags(p), deadline)
	if err != nil {
		return StartResult{}, err
	}
	return StartResult{Device: serial, Avd: p.Avd, BootMs: time.Since(started).Milliseconds(),
		Warnings: warnings(res.Warnings)}, nil
}

// startFlags is emulator.md §Flags and configuration's default set, with the caller's two
// options.
func startFlags(p StartParams) []string {
	flags := []string{"-no-boot-anim", "-no-audio", "-no-snapshot-save"}
	if p.ColdBoot {
		flags = append(flags, "-no-snapshot-load")
	}
	if p.Headless {
		flags = append(flags, "-no-window")
	}
	return flags
}
