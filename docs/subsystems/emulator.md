# Subsystem: emulator

AVD lifecycle: listing, booting and stopping Android emulators.

> Status: draft. The adb layer and device capabilities live in the sibling
> [`adb-device.md`](adb-device.md).

## Design rule: boot it, don't own it

The emulator is launched **detached and unreferenced**, so it **outlives the
process that started it** — exactly like one booted by hand. `adb`, not the
launcher, is the contract.

Consequences:

- The emulator is a **device process**, not a sidecar of ours
  ([process-lifecycle](../architecture/process-lifecycle.md)). We start and observe
  it; we do not own its process tree.
- **Closing a workspace does not stop emulators.** They are expensive to boot and the
  user may well want them alive; stopping is explicit. This is a deliberate choice
  with a memory cost, and it mirrors the adb server policy.
- A booted device **has no serial until it appears**. Boot is therefore an explicit,
  long-running, non-idempotent operation, never an incidental side effect of another
  call.

## Binary resolution

The `emulator` launcher is resolved best-effort, in order:

```text
SDK roots (<sdk>/emulator/emulator)
  → derived from the adb location (<sdk>/platform-tools/adb → <sdk>/emulator/emulator)
    → PATH
```

Its absence is normal and **only affects this subsystem**: every other device
capability works without it. When it cannot be found, the error says how to proceed
(start a device by any means and it will appear in `device.list`).

## Capabilities

| Capability | Notes |
| --- | --- |
| `emulator.list` | AVDs available **and** which of them are currently running — two different questions |
| `emulator.create` | From a device profile + a system image (see [`sdk.md`](sdk.md)); advanced settings exposed, not hidden |
| `emulator.delete` | Removes the AVD; reports the disk freed |
| `emulator.start` | Boots a named AVD; returns the serial once online and booted. Supports **cold boot** |
| `emulator.wipe` | Wipes user data — a daily-need operation, not an afterthought |
| `emulator.stop` | `adb emu kill`, **emulators only**; physical devices are refused with an explanation |

`emulator.list` reports AVDs, not devices: what is *available to boot* is a different
question from what is *currently connected*, and conflating them is a common source
of confusion in agent tools.

Creation is deliberately staged after the core loop (see the MVP): an emulator that
already exists is enough to exercise the golden path. But it belongs here, not in a
separate "provisioning" subsystem, because an AVD *is* the emulator.

## Boot sequence

1. **Snapshot** the set of existing emulator serials.
2. **Spawn** the launcher detached and unreferenced, with flags chosen for speed and
   determinism (no boot animation, no audio, no snapshot save).
3. **Resolve the serial by difference** — the one that did not exist before. Fallback:
   match an already-running emulator whose AVD name is the requested one, because the
   launcher **deduplicates** a second boot of the same AVD into the running instance
   instead of failing.
4. **Wait for readiness**: poll `getprop sys.boot_completed` every second.
5. **Return** the serial, now a normal device for every other capability.

Boot completion is the readiness signal — not the launcher starting, and not the
device merely appearing in `adb devices`.

## Flags and configuration

Defaults favour fast, repeatable boots: `-no-boot-anim`, `-no-audio`,
`-no-snapshot-save`. The exact set is **configurable per boot**, because some
workflows genuinely need a cold boot, a writable system image, or a specific device
profile. We do not manage the emulator's own memory configuration: that is the AVD's
business.

**Headless is a supported mode** (`-no-window`), which is what makes emulator
automation viable on a machine with no display — relevant for the remote/tablet
scenario, where the emulator may run on the host while the human is elsewhere.

## Concurrency

- Boots are **serialised**. Emulators compete for console/adb ports and for machine
  resources; racing boots produce confusing failures.
- Several emulators may be *running* at once; each is an ordinary device with its own
  lock ([adb-device](adb-device.md)).
- A boot that is already in flight for the same AVD is joined, not duplicated.

## Failure modes

| Failure | Behaviour |
| --- | --- |
| No launcher found | Actionable error; other device capabilities unaffected |
| AVD name unknown | Error listing the available AVD names |
| Launcher exits non-zero before a device appears | Surface the exit code and the stderr tail |
| No serial appears within the timeout | Name the AVD and the elapsed time |
| Device appears but never finishes booting | Report the serial and how long we waited; the emulator is stuck, not us |
| `emulator.stop` on a physical device | Refused, with the reason |
| Requested AVD already running | Adopt the existing instance instead of failing |

## Timeouts

Booting is slow (tens of seconds to minutes for a cold boot). The default timeout is
generous, and the operation is cancellable — a cancelled boot leaves the emulator
running (we do not own it) and simply stops waiting. That asymmetry is intentional
and must be documented in the capability's description, so an agent does not assume
cancellation killed the device.

## AVD definition

An AVD is a directory (`~/.android/avd/<name>.avd/`) with a `config.ini` describing the
virtual hardware. Creation takes three inputs:

1. A **device profile** (from the SDK's device definitions): `pixel_7`, a tablet, a TV.
2. A **system image** (`system-images;android-35;google_apis;arm64-v8a`), which may need
   to be downloaded first — see [`sdk.md`](sdk.md).
3. **Advanced settings**: RAM, heap, internal storage, SD card, graphics mode, camera,
   network speed and latency. Exposed with sensible defaults, not hidden behind a dial.

Because the definition is a file, the subsystem can also **read** an existing AVD's
configuration and report it, which is what makes "why is this emulator so slow"
answerable.

## Open questions

- Whether to expose SDK package management (`sdkmanager`) at all — probably not; it
  is an installer concern, not a development loop concern.
- The default boot-flag set and how it is surfaced to the caller.
- Whether to report emulator resource usage (the emulator's own memory settings) as
  part of `emulator.list`.
- How to detect an emulator that is wedged (running but never booting) versus one
  that is simply slow.
- Whether `emulator.create` should be able to install the required system image in
  one step, or refuse and point at `sdk.install` (the explicit path is safer).
