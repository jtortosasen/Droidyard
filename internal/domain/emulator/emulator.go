// Package emulator is the AVD lifecycle subsystem (docs/subsystems/emulator.md):
// listing the AVDs the launcher knows and stopping the emulators running them. Booting
// and wiping follow (plan m1i step 5).
//
// It owns no device logic: the policy hook, the device listing and the emulator
// console's AVD name all come from internal/domain/device, which adapts adb. This
// package resolves the launcher and reads AVD files on the host, and nothing else. The
// launcher is run only for `-list-avds`; it is never spawned as an emulator here.
package emulator

import (
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
)

// The codes of emulator.md §Failure modes. Every one is EMULATOR_-prefixed and stable;
// listing and stopping return the first, sixth and seventh today, and booting and
// wiping (step 5) the rest.
const (
	ErrLauncherNotFound capability.ErrorCode = "EMULATOR_LAUNCHER_NOT_FOUND"
	ErrAVDNotFound      capability.ErrorCode = "EMULATOR_AVD_NOT_FOUND"
	ErrLaunchFailed     capability.ErrorCode = "EMULATOR_LAUNCH_FAILED"
	ErrAVDLocked        capability.ErrorCode = "EMULATOR_AVD_LOCKED"
	ErrBootTimeout      capability.ErrorCode = "EMULATOR_BOOT_TIMEOUT"
	ErrNotAnEmulator    capability.ErrorCode = "EMULATOR_NOT_AN_EMULATOR"
	ErrStopTimeout      capability.ErrorCode = "EMULATOR_STOP_TIMEOUT"
)

// Timeouts are constants: emulator.md §Timeouts calls the boot timeout configurable but
// no configuration key exists (capability-api.md §Open questions), so none of them is
// (plan m1i, D14). Variables so a test injects a scale.
var (
	// listTimeout bounds `emulator -list-avds`.
	listTimeout = 10 * time.Second
	// stopTimeout bounds the wait for a killed emulator's serial to leave `adb devices`,
	// and the `emu kill` command itself.
	stopTimeout = 20 * time.Second
	// stopPollInterval is how often the listing is read while a stop waits.
	stopPollInterval = time.Second
)

// Emulator is the subsystem's handle: the daemon's environment, the adb subsystem it
// shares, and the starter the launcher is run through. start is the daemon's locked
// spawner (device.Starter); without one, resolution and file reading still work and
// only running the launcher fails.
type Emulator struct {
	env   []string
	adb   *device.ADB
	start device.Starter
}

// New returns an Emulator over env, the environment the launcher inherits unchanged (so
// it and our AVD reader see the same directories, emulator.md §AVD discovery) and adb
// clients are resolved in, with adb for the device side and start for the launcher.
func New(env []string, adb *device.ADB, start device.Starter) *Emulator {
	return &Emulator{env: append([]string(nil), env...), adb: adb, start: start}
}

// Resolve finds the launcher again, afresh, as ADB.Resolve does for adb: an emulator
// installed or removed while the daemon runs is seen.
func (e *Emulator) Resolve() Resolution { return Resolve(e.env) }

// Register adds the subsystem's capabilities to reg.
func Register(reg *capability.Registry, e *Emulator) {
	registerList(reg, e)
	registerStop(reg, e)
}
