# Subsystem: emulator

AVD lifecycle: listing, booting, stopping and wiping Android emulators. The adb layer and
device capabilities are in [`adb-device.md`](adb-device.md); SDK packages and the host SDK
root in [`sdk.md`](sdk.md). Parameters, results and error lists are the descriptors'
(`internal/domain/emulator`).

## Design rule: boot it, don't own it

The emulator is launched **detached and unreferenced**, so it **outlives the process that
started it**, exactly like one booted by hand. `adb`, not the launcher, is the contract.

- The emulator is a **device process**, not a sidecar of ours
  ([process-lifecycle](../architecture/process-lifecycle.md)). We start and observe it; we do
  not own its process tree **once its serial is resolved**. Before that, a launch this daemon
  started is ours: a boot that ends without a serial (45 s serial deadline, the boot timeout,
  a cancel, the daemon's shutdown) has its process group sent `SIGTERM`, then `SIGKILL` after
  5 s, so a hung launch never keeps the AVD
  ([ADR-0033](../adr/0033-emulator-launch-owned-until-serial.md)).
- **Closing a workspace does not stop emulators**, and neither does stopping the daemon. They
  are expensive to boot and the user may well want them alive; stopping is explicit. This is
  a deliberate choice with a memory cost, and it mirrors the adb server policy.
- A booted device **has no serial until it appears**. Boot is therefore an explicit,
  long-running operation, never an incidental side effect of another call.

## Binary resolution

The `emulator` launcher is resolved best-effort, in order: `tools.emulator`
([ADR-0032](../adr/0032-configuration-file-v1.md) §3, read once at daemon start; one that is not
executable is rejected and resolution goes on), then `<host SDK>/emulator/emulator`
([sdk](sdk.md) §Host SDK), then derived from the resolved adb's location
([adb-device](adb-device.md) §adb resolution and degradation), then `PATH`. Its absence is normal and
**only affects this subsystem**; the error says how to proceed (start a device by any means and it
will appear in `device.list`).

## AVD discovery

**Names come from the launcher**: `emulator -list-avds` (launcher `INFO` chatter filtered) is
authoritative, because it applies its own search rules. **Directories**, to read an AVD's
configuration: the emulator's documented order is `$ANDROID_AVD_HOME`, `$ANDROID_USER_HOME/avd`,
`~/.android/avd`; two variables also relocate the default and are checked where they apply,
`$ANDROID_EMULATOR_HOME/avd` (after `ANDROID_AVD_HOME`) and the legacy
`$ANDROID_SDK_HOME/.android/avd` (before the home default). Each AVD is a `<name>.ini` whose `path=`
(absolute) or `path.rel=` names the `<name>.avd/` directory, which may live elsewhere: read the
entry, never assume it sits beside the `.ini`. The launcher inherits the daemon's environment
unchanged, so it and our reader see the same directories; a name the launcher lists but we cannot
locate is reported with its config unavailable, never dropped.

## Capabilities

An AVD that is not running is named by **`avd`**; a running emulator is a device and is named by
**`device`** (its serial), like every other device
([capability-api](../architecture/capability-api.md) §One name for each target). `emulator.list`
maps one to the other and reports AVDs, not devices: conflating the two is a common source of
confusion in agent tools. `emulator.stop` is `adb emu kill`, **emulators only**; `emulator.wipe`
stops the AVD first if running (§Wipe).

**No SDK package management here.** `sdk` owns packages ([sdk](sdk.md)): when the system image an AVD
needs is missing, `emulator.create` refuses, names the package, and points at `sdk.install`; it never
calls `sdkmanager` itself. Creation (`emulator.create`, `emulator.delete`) is deliberately post-MVP
([mvp](../roadmap/mvp.md) §Out of scope): an emulator that already exists is enough for the golden
path. It belongs here, not in a separate "provisioning" subsystem, because an AVD *is* the emulator;
its inputs would be a device profile, an installed system image and advanced settings exposed with
sensible defaults, not hidden. Because an AVD is a `config.ini` file, the subsystem can also **read**
one and report it, which is what makes "why is this emulator so slow" answerable.

## Boot sequence

1. **Adopt first.** For each running emulator serial, `adb -s <serial> emu avd name`; if one runs the
   requested AVD, return it with `adopted: true` and spawn nothing. This makes `emulator.start` safe
   to repeat without depending on the launcher, which the prior art trusted to deduplicate a second
   boot of the same AVD; recent launchers refuse it instead (multi-instance needs `-read-only`).
2. **Snapshot** the existing emulator serials, **spawn** the launcher detached with flags chosen for
   speed and determinism, and **resolve the serial by difference**: the one that did not exist
   before, whose `emu avd name` is the requested AVD.
3. **Wait for readiness**: poll `getprop sys.boot_completed` every second. Boot completion is the
   readiness signal, not the launcher starting and not the device merely appearing in `adb devices`.

## Detached launch

What "detached and unreferenced" means in Go, where a literal port would leak zombies or tie the
emulator to the daemon (`internal/domain/emulator/launch.go`):

- **`SysProcAttr{Setsid: true}`**: a new session and process group with no controlling terminal, so
  Ctrl-C or `SIGHUP` to a foreground `droidyard serve`, and group kills aimed at our own children,
  never reach the emulator.
- **stdio**: stdin from `/dev/null`; stdout and stderr to one file, `<state>/emulator/<avd>.log`
  ([ADR-0021](../adr/0021-persistence-format-and-location.md)), truncated at each boot of that AVD. A
  file, not a pipe: a pipe would break when the daemon exits and could take the emulator down with
  it. An error's "stderr tail" is read from this file.
- **Reaping**: one goroutine per launch calls `cmd.Wait()`, so no zombie accumulates while the
  daemon lives and the exit status detects "the launcher died before a device appeared". If the
  daemon exits first, init/launchd reaps it. No double fork is needed.
- **Unsupervised on purpose** after its serial resolves: no pid record (a record would get the
  emulator killed by the next daemon's crash recovery, and ADR-0021 excludes emulators), no Linux
  `Pdeathsig` (the one exception to [process-lifecycle](../architecture/process-lifecycle.md)'s
  "every child" rule) and no idle timeout. The working directory is the state root, never a
  workspace root. Before the serial resolves, the launch has a pid record of kind `emulator-launch`
  (ADR-0033), removed before the serial is returned; a record that cannot be written at spawn, or
  removed at resolution, ends the launch and fails the boot with `EMULATOR_LAUNCH_FAILED` (plan m1p
  Q1). The pid is kept in memory only, to report it and to confirm `emulator.stop`.

## Stop

`adb -s <serial> emu kill` (the console auth token is handled by adb) for `emulator-<port>`
serials only. Then wait, bounded (35 s), until the serial leaves `adb devices` and, when we
launched it, the launcher's `Wait` returns. A timeout is an error naming the serial and the
pid when known. We do not escalate to signals: a running emulator is not ours.

## Wipe

`emulator.wipe {avd}` resets the AVD's user data to factory state:

1. **If the AVD is running**, stop it as above, and also wait for it to release the AVD: the
   emulator holds lock files in the `.avd` directory, and a boot racing a dying instance fails
   as "already running".
2. **Boot it with `-wipe-data`** (implying a cold boot) through the boot sequence, skipping
   adoption.
3. **Return `{device}`**: the AVD is left running, factory-fresh, the state a caller wants
   after a wipe (ratified 2026-09-29). A caller who wants it stopped calls `emulator.stop`.

Wiping without booting, by deleting the user-data images, is what Android Studio's *Wipe Data*
does; we do not, because it depends on the emulator's disk layout, while `-wipe-data` is the
emulator's own contract. Cancelling after step 1 leaves the AVD stopped and unwiped; cancelling
after the spawn behaves like a cancelled boot (§Timeouts): before the serial resolves the
launch is ended and the wipe may be partial (call `emulator.wipe` again); after it the wipe
has happened.

## Flags and configuration

Defaults favour fast, repeatable boots: `-no-boot-anim`, `-no-audio`, `-no-snapshot-save`. `coldBoot`
adds `-no-snapshot-load`; `headless` adds `-no-window`, which makes emulator automation viable on a
machine with no display (the remote/tablet scenario). Every launch ends with `-crash-report-mode
never`: after a crash, a windowed boot otherwise waits on a consent dialog. We do not manage the
emulator's own memory configuration: that is the AVD's business.

## Concurrency

*Milestone 1i (D12, Delegated):* `start` and `wipe` share one `emulator.boot` key: boots are
**serialised** (emulators compete for console/adb ports and machine resources, and racing boots
produce confusing failures), a wipe holds it across its stop and its boot, and a second
`emulator.start` for an AVD already booting is `queued` behind the first and then adopts it
([ADR-0020](../adr/0020-operation-contract.md)). `stop` takes the device's `device.write:<serial>`
key instead, so it can end a boot in flight (that boot then fails `EMULATOR_LAUNCH_FAILED`) rather
than queue behind it; a **short call queued behind a long one** fails `OPERATION_QUEUE_TIMEOUT`
after 20 s (ADR-0020 §States). Adoption of a running AVD waits for readiness and queues behind an
unrelated boot; an emulator listed offline counts as running. Several emulators may be *running* at
once, each an ordinary device with its own lock.

## Failure modes

Codes and causes are the descriptors' and `internal/capability/error.go`'s. The decisions
(milestone 1i unless noted): the policy hook's `DEVICE_NOT_ALLOWED` comes before
`EMULATOR_NOT_AN_EMULATOR` (an emulator reached over `ip:port`); adb's `device offline` during a boot
is not a disconnection; an `EMULATOR_AVD_LOCKED` holder has no console we can address, so its next
step does not offer `emulator.list` and says we never stop what we did not launch; a serial that
does not appear within 45 s ends the launch (its group `SIGTERM`, then `SIGKILL`) so the AVD is
free, while a device that appears but never finishes booting is not ours to signal, so its next
step names `emulator.stop {device: <serial>}` and then a fresh `emulator.start`; a stop that does not
complete is `EMULATOR_STOP_TIMEOUT` with no signal escalation.

## Timeouts

Booting is slow (tens of seconds to minutes for a cold boot). The defaults are constants until
a configuration key exists (1i D14: boot 180 s, stop 35 s, listing 10 s), and `start`/`wipe`
are long-running ([ADR-0020](../adr/0020-operation-contract.md)): an MCP call returns an
operation id after its wait budget. A boot cancelled **after** its serial resolved **leaves the
emulator running** (we do not own it) and stops waiting; one cancelled before then has
its launch ended (ADR-0033). That asymmetry is intentional and must be in the capability's
description, so an agent does not assume cancellation killed the device.

## What was observed

Spike S8 ([findings](../research/spike-s8-s9-emulator-and-logcat.md)) and m1m step 1
([plan](../plans/m1m-emulator-robustness.md)): on emulator 36.6.11, API 31 arm64, macOS, `emulator`
execs into `qemu-system-aarch64(-headless)`, so the launch's pid is the qemu; started through `setsid`
it outlives its starter; only `ANDROID_AVD_HOME` isolates the AVD listing; `emu kill` ends it in about
2 s, also mid-boot; `-wipe-data` cold-boots; a `SIGTERM` before the serial is listed ends the qemu and
frees the AVD. What remains unverified is in the register.
