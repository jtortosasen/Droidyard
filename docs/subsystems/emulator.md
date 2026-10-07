# Subsystem: emulator

AVD lifecycle: listing, booting, stopping and wiping Android emulators.

> Status: draft. The adb layer and device capabilities live in the sibling
> [`adb-device.md`](adb-device.md); SDK packages and the host SDK root in
> [`sdk.md`](sdk.md).

## Design rule: boot it, don't own it

The emulator is launched **detached and unreferenced**, so it **outlives the
process that started it** — exactly like one booted by hand. `adb`, not the
launcher, is the contract.

Consequences:

- The emulator is a **device process**, not a sidecar of ours
  ([process-lifecycle](../architecture/process-lifecycle.md)). We start and observe
  it; we do not own its process tree.
- **Closing a workspace does not stop emulators**, and neither does stopping the
  daemon. They are expensive to boot and the user may well want them alive; stopping
  is explicit. This is a deliberate choice with a memory cost, and it mirrors the adb
  server policy.
- A booted device **has no serial until it appears**. Boot is therefore an explicit,
  long-running operation, never an incidental side effect of another call.

## Binary resolution

The `emulator` launcher is resolved best-effort, in order:

```text
tools.emulator (config, read at start)
  → <host SDK>/emulator/emulator            (sdk.md §Host SDK)
    → derived from the adb location (<sdk>/platform-tools/adb → <sdk>/emulator/emulator)
      → PATH
```

`tools.emulator` ([ADR-0032](../adr/0032-configuration-file-v1.md) §3) is read once at
daemon start; one that is not executable is rejected and resolution goes on. The adb
location the launcher is derived from is the adb resolved with `tools.adb`
([adb-device](adb-device.md) §adb resolution and degradation). An invalid configuration
file at start is a rejected entry naming the file.

Its absence is normal and **only affects this subsystem**: every other device
capability works without it. When it cannot be found, the error says how to proceed
(start a device by any means and it will appear in `device.list`).

## AVD discovery

- **Names come from the launcher**: `emulator -list-avds` (launcher `INFO` chatter
  filtered) is authoritative, because it applies its own search rules.
- **Directories**, to read an AVD's configuration: the emulator's documented order is
  `$ANDROID_AVD_HOME`, `$ANDROID_USER_HOME/avd`, `~/.android/avd`. Two variables also
  relocate the default and are checked where they apply: `$ANDROID_EMULATOR_HOME/avd`
  (after `ANDROID_AVD_HOME`) and the legacy `$ANDROID_SDK_HOME/.android/avd` (before
  the home default).
- Each AVD is a `<name>.ini` in that directory whose `path=` (absolute) or `path.rel=`
  names the `<name>.avd/` directory. The directory may live elsewhere: read the entry,
  never assume it sits beside the `.ini`.
- The launcher inherits the daemon's environment unchanged, so it and our reader see
  the same directories. A name the launcher lists but we cannot locate is reported
  with its config unavailable, never dropped.

## Capabilities

An AVD that is not running is named by **`avd`**; a running emulator is a device and
is named by **`device`** (its serial), like every other device
([capability-api](../architecture/capability-api.md) §One name for each target).
`emulator.list` maps one to the other.

| Capability | Phase | Target | Notes |
| --- | --- | --- | --- |
| `emulator.list` | MVP | — | Every AVD, with `running: {device}` when it is up — what is *available to boot* vs. what is *running*, in one answer |
| `emulator.start` | MVP | `avd` | Boots and returns `{device, adopted}` once online and booted. Options: `coldBoot`, `headless` |
| `emulator.stop` | MVP | `device` | `adb emu kill`, **emulators only**; physical devices are refused with an explanation |
| `emulator.wipe` | MVP | `avd` | Factory-resets user data; stops the AVD first if running (§Wipe) |
| `emulator.create` | post-MVP | `avd` | From a device profile + a system image; advanced settings exposed, not hidden |
| `emulator.delete` | post-MVP | `avd` | Removes the AVD; reports the disk freed |

`emulator.list` reports AVDs, not devices: conflating the two is a common source of
confusion in agent tools.

**No SDK package management here.** `sdk` owns packages ([sdk](sdk.md)): when the
system image an AVD needs is missing, `emulator.create` refuses, names the package,
and points at `sdk.install`. It never calls `sdkmanager` itself.

Creation is deliberately post-MVP ([mvp](../roadmap/mvp.md) §Out of scope): an
emulator that already exists is enough for the golden path. It belongs here, not in a
separate "provisioning" subsystem, because an AVD *is* the emulator.

## Boot sequence

1. **Adopt first.** For each running emulator serial, `adb -s <serial> emu avd name`.
   If one runs the requested AVD, return it with `adopted: true` and spawn nothing.
   This makes `emulator.start` safe to repeat without depending on the launcher, which
   the prior art trusted to deduplicate a second boot of the same AVD; recent launchers
   appear to refuse it instead (multi-instance needs `-read-only`) — see §To verify.
2. **Snapshot** the set of existing emulator serials.
3. **Spawn** the launcher detached (§Detached launch), with flags chosen for speed and
   determinism.
4. **Resolve the serial by difference** — the one that did not exist before, whose
   `emu avd name` is the requested AVD.
5. **Wait for readiness**: poll `getprop sys.boot_completed` every second.
6. **Return** `{device, adopted: false}`, now a normal device for every other
   capability.

Boot completion is the readiness signal — not the launcher starting, and not the
device merely appearing in `adb devices`.

## Detached launch

What "detached and unreferenced" means in Go, where a literal port would leak
zombies or tie the emulator to the daemon:

- **`SysProcAttr{Setsid: true}`**: a new session and process group with no controlling
  terminal. Ctrl-C or `SIGHUP` to a foreground `droidyard serve`, and group kills aimed at
  our own children, never reach the emulator.
- **stdio**: stdin from `/dev/null`; stdout and stderr to one file,
  `<state>/emulator/<avd>.log` ([ADR-0021](../adr/0021-persistence-format-and-location.md)),
  truncated at each boot of that AVD. A file, not a pipe: a pipe would break when the
  daemon exits and could take the emulator down with it. The "stderr tail" in
  §Failure modes is read from this file.
- **Reaping**: one goroutine per launch calls `cmd.Wait()`. It reaps the launcher when
  it exits, so no zombie accumulates while the daemon lives, and it delivers the exit
  status that detects "the launcher died before a device appeared". If the daemon exits
  first, the launcher is re-parented to init/launchd, which reaps it. No double fork is
  needed.
- **Unsupervised on purpose**: no pid record (a record would get the emulator killed by
  the next daemon's crash recovery, and ADR-0021 excludes emulators), no Linux
  `Pdeathsig` — the one exception to [process-lifecycle](../architecture/process-lifecycle.md)'s
  "every child" rule — and no idle timeout. The working directory is the state root,
  never a workspace root.
- The pid is kept in memory only, to report it and to confirm `emulator.stop`.

## Stop

`adb -s <serial> emu kill` — the console auth token is handled by adb — for
`emulator-<port>` serials only. Then wait, bounded (default 20 s), until the serial
leaves `adb devices` and, when we launched it, the launcher's `Wait` returns. A timeout
is an error naming the serial and the pid when known. We do not escalate to signals:
the process is not ours.

## Wipe

`emulator.wipe {avd}` resets the AVD's user data to factory state:

1. **If the AVD is running**, stop it as above, and also wait for it to release the
   AVD: the emulator holds lock files in the `.avd` directory (§To verify), and a
   boot racing a dying instance fails as "already running".
2. **Boot it with `-wipe-data`** (implying a cold boot) through the boot sequence,
   skipping adoption.
3. **Return `{device}`**: the AVD is left running, factory-fresh — the state a caller
   wants after a wipe. *Ratified 2026-09-29.* A caller who wants it
   stopped calls `emulator.stop`.

Wiping without booting, by deleting the user-data images, is what Android Studio's
*Wipe Data* does; we do not, because it depends on the emulator's disk layout, while
`-wipe-data` is the emulator's own contract. Cancelling after step 1 leaves the AVD
stopped and unwiped; cancelling after the spawn behaves like a cancelled boot
(§Timeouts), and the wipe has happened.

## Flags and configuration

Defaults favour fast, repeatable boots: `-no-boot-anim`, `-no-audio`,
`-no-snapshot-save`. `coldBoot` adds `-no-snapshot-load`; `headless` adds
`-no-window`, which is what makes emulator automation viable on a machine with no
display — relevant for the remote/tablet scenario, where the emulator may run on the
host while the human is elsewhere. We do not manage the emulator's own memory
configuration: that is the AVD's business.

## Concurrency

*As built in milestone 1i (D12, Delegated):* `start` and `wipe` share one `emulator.boot`
key (one boot at a time); `stop` takes the device's `device.write:<serial>` key, so it
can end a boot in flight instead of queueing behind it (that boot then fails
`EMULATOR_LAUNCH_FAILED`, which now covers "exited before the device finished
booting"); adoption of a running AVD waits for readiness and queues behind an unrelated
boot. An emulator listed offline counts as running.

- Boots are **serialised**. Emulators compete for console/adb ports and for machine
  resources; racing boots produce confusing failures.
- `start` and `wipe` share one boot key (a wipe holds it across its stop and its
  boot); a second `emulator.start` for an AVD already booting is `queued` behind the
  first (`blockedBy`, [ADR-0020](../adr/0020-operation-contract.md)) and then adopts it.
  `stop` takes the device's write key instead, so it is not queued behind a boot.
- Several emulators may be *running* at once; each is an ordinary device with its own
  lock ([adb-device](adb-device.md)).
- A **short call queued behind a long one** fails with `OPERATION_QUEUE_TIMEOUT` after
  20 s rather than waiting: `emulator.stop` behind a boot in progress, for instance
  ([ADR-0020](../adr/0020-operation-contract.md) §States).

## Failure modes

*As built in milestone 1i:* the policy hook's `DEVICE_NOT_ALLOWED` comes before
`EMULATOR_NOT_AN_EMULATOR`; a failed `emu kill` whose serial stays listed is
`DEVICE_ADB_FAILED` after 3 s; adb's `device offline` during a boot is not a
disconnection; an unknown AVD is `EMULATOR_AVD_NOT_FOUND` from `-list-avds`, a second
instance `EMULATOR_AVD_LOCKED` (the launcher's `Running multiple emulators with the same
AVD` line).

| Failure | Code | Behaviour |
| --- | --- | --- |
| No launcher found | `EMULATOR_LAUNCHER_NOT_FOUND` | Actionable error; other device capabilities unaffected |
| AVD name unknown | `EMULATOR_AVD_NOT_FOUND` | Lists the available AVD names |
| Launcher exits before the device finished booting | `EMULATOR_LAUNCH_FAILED` | Exit status (from `Wait`) and the log tail |
| AVD held by another instance | `EMULATOR_AVD_LOCKED` | Names the lock; `emulator.list` shows whether it is ours to adopt |
| No serial appears within the timeout | `EMULATOR_BOOT_TIMEOUT` | Names the AVD and the elapsed time |
| Device appears but never finishes booting | `EMULATOR_BOOT_TIMEOUT` | Reports the serial and how long we waited; the emulator is stuck, not us |
| `emulator.stop` on a physical device | `DEVICE_NOT_ALLOWED` | The policy hook refuses it first; an emulator reached over `ip:port` is `EMULATOR_NOT_AN_EMULATOR` |
| Stop does not complete | `EMULATOR_STOP_TIMEOUT` | Serial and pid when known; no signal escalation |

## Timeouts

*Milestone 1i (D14):* boot 180 s, stop 20 s, listing 10 s, constants until a
configuration key exists.

Booting is slow (tens of seconds to minutes for a cold boot). The default timeout is
generous (180 s; a constant until a configuration key exists), and `start`/`wipe` are long-running
([ADR-0020](../adr/0020-operation-contract.md)): an MCP call returns an operation id
after its wait budget. A **cancelled boot leaves the emulator running** (we do not own
it) and simply stops waiting. That asymmetry is intentional and must be documented in
the capability's description, so an agent does not assume cancellation killed the
device.

## AVD definition

An AVD is a directory (located per §AVD discovery) with a `config.ini` describing the
virtual hardware. Creation takes three inputs:

1. A **device profile** (from the SDK's device definitions): `pixel_7`, a tablet, a TV.
2. A **system image** (`system-images;android-35;google_apis;arm64-v8a`), which must
   already be installed — see [`sdk.md`](sdk.md).
3. **Advanced settings**: RAM, heap, internal storage, SD card, graphics mode, camera,
   network speed and latency. Exposed with sensible defaults, not hidden behind a dial.

Because the definition is a file, the subsystem can also **read** an existing AVD's
configuration and report it, which is what makes "why is this emulator so slow"
answerable.

## To verify (spike S8)

*Answered by spike S8 (2026-10-07, [findings](../research/spike-s8-s9-emulator-and-logcat.md)):
`emulator` execs into `qemu-system-aarch64(-headless)` and, started through `setsid`,
outlives its starter; only `ANDROID_AVD_HOME` isolates the AVD listing; `emu kill` ends
an emulator in about 2 s, also mid-boot; `multiinstance.lock` outlives a kill without
blocking the next boot; `-wipe-data` cold-boots; a headless boot took 21 s (13 s wiped).*

Claims above that a machine with the SDK can confirm before they are relied on:

- whether the launcher `exec`s into or supervises the QEMU process (what `Wait`
  actually observes, and what the pid identifies);
- that the launcher refuses a second instance of a running AVD without `-read-only`
  (the `-read-only` flag itself is confirmed on emulator 36.6);
- which lock files a running AVD holds, and how long they outlive `emu kill`;
- that `-wipe-data` implies a cold boot;
- where `ANDROID_EMULATOR_HOME` and `ANDROID_SDK_HOME` fall in the AVD search order
  (the documented order omits them);
- headless boot on macOS Apple Silicon and on Linux.

## Open questions

- The default boot-flag set and how it is surfaced to the caller.
- Whether to report emulator resource usage (the emulator's own memory settings) as
  part of `emulator.list`.
- How to detect an emulator that is wedged (running but never booting) versus one
  that is simply slow.
