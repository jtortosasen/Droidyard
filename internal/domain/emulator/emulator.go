// Package emulator is the AVD lifecycle subsystem (docs/subsystems/emulator.md):
// listing the AVDs the launcher knows, booting one, stopping it and wiping it.
//
// It owns no device logic: the policy hook, the device listing and the emulator
// console's AVD name all come from internal/domain/device, which adapts adb. This
// package resolves the launcher, reads AVD files on the host, and starts the launcher
// detached for a boot; nothing else. The design rule is "boot it, don't own it": an
// emulator we start outlives the daemon (emulator.md §Design rule).
package emulator

import (
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
)

// The codes of emulator.md §Failure modes. Every one is EMULATOR_-prefixed and stable.
const (
	ErrLauncherNotFound capability.ErrorCode = "EMULATOR_LAUNCHER_NOT_FOUND"
	ErrAVDNotFound      capability.ErrorCode = "EMULATOR_AVD_NOT_FOUND"
	ErrLaunchFailed     capability.ErrorCode = "EMULATOR_LAUNCH_FAILED"
	ErrAVDLocked        capability.ErrorCode = "EMULATOR_AVD_LOCKED"
	ErrBootTimeout      capability.ErrorCode = "EMULATOR_BOOT_TIMEOUT"
	ErrNotAnEmulator    capability.ErrorCode = "EMULATOR_NOT_AN_EMULATOR"
	ErrStopTimeout      capability.ErrorCode = "EMULATOR_STOP_TIMEOUT"
)

// clock is the stop's source of time: a test replaces it with a virtual one, so that a
// wait of 25 seconds costs nothing.
var clock = struct {
	now   func() time.Time
	after func(time.Duration) <-chan time.Time
}{time.Now, time.After}

// Timeouts are constants: emulator.md §Timeouts calls the boot timeout configurable but
// no configuration key exists (known-limits.md, configuration version 1 deferrals), so none of them is
// (plan m1i, D14). Variables so a test injects a scale.
var (
	// listTimeout bounds `emulator -list-avds`.
	listTimeout = 10 * time.Second
	// stopTimeout bounds the wait for a killed emulator's serial to leave `adb devices`,
	// and the `emu kill` command itself. It stays above the emulator's own 20 s grace to
	// shut down, which a stop right after a wipe can need in full (plan m2c, D18).
	stopTimeout = 35 * time.Second
	// stopPollInterval is how often the listing is read while a stop waits.
	stopPollInterval = time.Second
	// bootTimeout bounds a boot from its snapshot to `sys.boot_completed` being 1, and
	// an adoption's wait for the same readiness (emulator.md §Timeouts; plan m1i, D15).
	bootTimeout = 180 * time.Second
	// bootPollInterval is how often the device listing and `sys.boot_completed` are read
	// while a boot waits (emulator.md §Boot sequence 3).
	bootPollInterval = time.Second
	// serialTimeout bounds the time from spawn to a serial this daemon resolves as the
	// launch's own (ADR-0033 ¶3; plan m1m, Q1 ¶3): step 1 saw the slowest listing at 2.6 s,
	// so 45 s is the working default. The earlier of it and the boot deadline bounds
	// awaitSerial, and a boot that reaches it before resolving its serial is ended.
	serialTimeout = 45 * time.Second
	// endGrace is how long a launch's process group is given to empty after SIGTERM before
	// SIGKILL follows; endKillGrace bounds the wait after SIGKILL. Together they hold `end`
	// under 10 s, inside the daemon's 15 s shutdown bound (ADR-0033 ¶1; plan m1m, step 2).
	endGrace     = 5 * time.Second
	endKillGrace = 5 * time.Second
	// endPollInterval is how often `end` reads the process group while it waits.
	endPollInterval = 5 * time.Millisecond
)

// Emulator is the subsystem's handle: the daemon's environment, the adb subsystem it
// shares, the starter the launcher is run through and the state root whose
// `emulator/<avd>.log` a boot writes. start is the daemon's locked spawner
// (device.Starter); without one, resolution and file reading still work and only
// running the launcher fails. state is "" in a subsystem that never boots.
type Emulator struct {
	env     []string
	adb     *device.ADB
	starter device.Starter
	state   string
	// keys is the configuration's tool keys as read at daemon start, handed to every
	// resolution (ADR-0032 §3, §6; plan m1k D8). Empty means the file set no key.
	keys Keys

	// mu guards launches, the in-memory table of launchers we started (plan m1i, D15).
	// It is deliberately not a pid record: a record would get the emulator killed by the
	// next daemon's crash recovery (emulator.md §Detached launch; ADR-0021).
	mu       sync.Mutex
	launches map[string]*launch
	// recorder writes and removes the pid record of a launch until its serial is
	// resolved (plan m1p Q1); the zero value records nothing.
	recorder LaunchRecorder
}

// New returns an Emulator over env, the environment the launcher inherits unchanged (so
// it and our AVD reader see the same directories, emulator.md §AVD discovery) and adb
// clients are resolved in, with adb for the device side, starter for the launcher and
// stateRoot as the state root a boot's log lives under ("" when nothing is booted).
func New(env []string, adb *device.ADB, starter device.Starter, stateRoot string) *Emulator {
	return &Emulator{env: append([]string(nil), env...), adb: adb, starter: starter, state: stateRoot,
		launches: map[string]*launch{}}
}

// LaunchRecorder writes and removes the pid record that makes a launch without a serial
// findable by the next daemon's crash recovery (plan m1p Q1; ADR-0021's 2026-10-08 line).
// The daemon gives the supervisor's. Either function nil: no record is kept.
type LaunchRecorder struct {
	// Record writes the record of the launcher pid, which leads a group of its own, for the
	// AVD and the adb server port it registers with. An error means no record is on disk.
	Record func(avd string, adbPort, pid int, command []string) error
	// Drop removes pid's record. An error means it may still be on disk.
	Drop func(pid int) error
}

// SetLaunchRecorder gives the Emulator the recorder of launches. Called once, at wiring,
// before any boot.
func (e *Emulator) SetLaunchRecorder(r LaunchRecorder) { e.recorder = r }

// SetToolKeys gives the Emulator `tools.emulator` and `tools.adb` as the daemon read
// them at start (ADR-0032 §3, §6; plan m1k D8): the first launcher candidate and the
// adb the launcher derived from adb's location uses. The values are snapshots, so an
// edit of the configuration file does not change them until a new daemon starts.
func (e *Emulator) SetToolKeys(keys Keys) { e.keys = keys }

// Resolve finds the launcher again, afresh, as ADB.Resolve does for adb: an emulator
// installed or removed while the daemon runs is seen. The configured `tools.emulator`
// and `tools.adb` are fixed at start (ADR-0032 §6; plan m1k D8).
func (e *Emulator) Resolve() Resolution { return ResolveWith(e.env, e.keys) }

// Register adds the subsystem's capabilities to reg.
func Register(reg *capability.Registry, e *Emulator) {
	registerList(reg, e)
	registerStart(reg, e)
	registerStop(reg, e)
	registerWipe(reg, e)
}

// boolPtr is a pointer to b, for a Descriptor's Annotations.
func boolPtr(b bool) *bool { return &b }
