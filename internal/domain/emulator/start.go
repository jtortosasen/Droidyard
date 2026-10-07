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
		Description: "Boots one AVD and returns {device, avd, adopted} once its device reports sys.boot_completed 1. If the AVD is already running it is adopted: adopted is true, nothing is spawned, and the call still waits for readiness. Otherwise the launcher is started detached — a session and process group of its own, output to <state>/emulator/<avd>.log — and the serial it creates is found by difference and its `emu avd name`, then polled every second until boot completes, bounded at 180 s. The emulator is not ours to own: it outlives this daemon, and no pid record is kept. " +
			"Boots are serialised: a second emulator.start or emulator.wipe runs behind the first as a queued operation, and once the first finishes it adopts the AVD. Cancelling a boot stops waiting and leaves the emulator running. A timeout is EMULATOR_BOOT_TIMEOUT. " +
			"The AVD is looked up in emulator -list-avds first, so an AVD the launcher does not know is EMULATOR_AVD_NOT_FOUND; a launcher that refuses a second instance is EMULATOR_AVD_LOCKED, and one that exits before the device finished booting (the log tail carries why) EMULATOR_LAUNCH_FAILED. " +
			"Fails with the preamble's errors, EMULATOR_LAUNCHER_NOT_FOUND, EMULATOR_AVD_NOT_FOUND, EMULATOR_LAUNCH_FAILED, EMULATOR_AVD_LOCKED, EMULATOR_BOOT_TIMEOUT, REQUEST_INVALID_PARAMS for a name that is not a bare AVD name, and OPERATION_QUEUE_TIMEOUT behind another boot.",
		Params: StartParams{},
		Result: StartResult{},
		Errors: append(append([]capability.ErrorCode{}, device.PreambleErrors...),
			ErrLauncherNotFound, ErrAVDNotFound, ErrLaunchFailed, ErrAVDLocked, ErrBootTimeout,
			capability.ErrInvalidParams, capability.ErrOperationQueueTimeout),
		Resource: func(*capability.Call) string { return bootKey },
		Annotations: capability.Annotations{
			Destructive: boolPtr(false),
			Reason:      "it adopts a running AVD and joins a boot already in flight (capability-api.md §Classification of the MVP surface)",
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
	serial, err := e.boot(ctx, res.Path, p.Avd, startFlags(p), deadline)
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
