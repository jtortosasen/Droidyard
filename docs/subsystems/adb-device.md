# Subsystem: adb & devices

The device half of the development loop: discover devices, install and launch apps,
observe them (logs, screen, UI tree), act on them (input), and introspect them
(processes, memory, crashes).

> Status: draft. Emulator *lifecycle* (listing, booting, stopping AVDs) lives in the
> sibling `emulator.md`; this document covers the adb layer and device capabilities.

## Design rule: adb-centric, serial is the only identity

A device's one and only identity is the **serial** reported by `adb devices -l`.
Emulators, USB devices and `ip:port` devices share a single code path. Nothing
assumes a particular emulator product.

The `emulator` launcher is **optional**: only the boot-an-AVD verb needs it, and
every other capability keeps working without it.

This is the single most portable idea in the prior art
([dsh-android](../research/dsh-android.md),
[concepts](../research/dsh-codex-android-concepts.md)) and it halves the surface
compared with a per-platform split.

## adb resolution and degradation

```text
ADB env var  →  adb on PATH  →  <sdk-root>/platform-tools/adb
                                 (ANDROID_HOME, ANDROID_SDK_ROOT,
                                  per-OS defaults)
```

If nothing resolves, the core **still loads and registers every capability**; each
device call then fails with an explanatory error and the workspace reports
`degraded`. Degrade, don't refuse. The resolved binary and its source are reported
in `system.capabilities`, so a consumer can tell *why* device work is unavailable.

## Device model

```text
Device
├── serial          the identity
├── state           device | offline | unauthorized | recovery | sideload | unknown
├── kind            emulator | physical
├── model / product / transportId
├── androidVersion / sdk / avdName   (getprop, best-effort)
└── online          derived
```

Details are best-effort: a flaky device still lists, its details simply stay sparse.
Emulator detection uses the serial prefix **or** known product strings, so it does
not depend on naming alone.

## Target resolution — never guess

1. Explicit `device` argument.
2. The workspace's active device.
3. **Only** if exactly one online device exists.
4. Otherwise: an error that names the candidates.

Physical devices are **never selected implicitly** and require the explicit serial on
every call ([ADR-0015](../adr/0015-untrusted-data-authorization.md)).

## Transport to the device

| Use | Mechanism | Notes |
| --- | --- | --- |
| Text commands | `adb -s <serial> shell …` | CRLF-normalised |
| Binary output | `exec-out` | Skips the pty so PNG bytes survive |
| Long-lived children | `spawn` | Process group, killed as a group |

Every invocation has an explicit timeout and a bounded buffer. No unbounded
accumulation anywhere.

## Capabilities

| Capability | Notes |
| --- | --- |
| `device.list` | Filtered by policy; details best-effort |
| `device.install` | `-r`; detects `Failure [INSTALL_FAILED_…]` even on exit 0 |
| `device.launch` | `monkey -p <pkg> -c LAUNCHER 1` — no manifest activity needed |
| `device.uninstall`, `device.forceStop` | |
| `device.logs` | Snapshot and follow, structured and bounded |
| `device.screenshot` | Returns an **artifact reference**, never a path |
| `device.uiTree` | Compact, capped, identity-first |
| `device.tapElement` | Taps **and asserts** the effect in the same call |
| `device.processes`, `device.meminfo`, `device.appInfo`, `device.backtrace` | Parse into structured facts; empty is a fact, not a failure |

### Application id

The device subsystem does **not** guess the application id. It is produced by the
build subsystem (`output-metadata.json`, then the module build script, then a legacy
manifest) and passed in. One owner, one answer.

## Input model

- **Coordinates are normalised 0..1** and resolved to pixels using the **current
  display size taken from a fresh screencap**, not `wm size` — which commonly reports
  the portrait panel size even in landscape. This removes rotation math entirely.
- UI-tree-derived actions work in **display pixels**, because the tree's bounds are
  already in that space.
- **Text**: `input text` is ASCII-only. Text is escaped (`%s` for spaces, shell
  metacharacters escaped); non-ASCII (CJK, emoji) goes through the ADBKeyboard IME if
  installed, otherwise the call is **refused with an install hint — never silently
  mistyped**.
- Rotation pins `accelerometer_rotation=0` before setting `user_rotation`, so
  auto-rotate cannot fight an explicit value.
- Device actions (notifications, quick settings, lock, wake, assistant) are plain adb
  verbs; `wm dismiss-keyguard` is best-effort and a locked device simply stays locked.

## UI tree

- `uiautomator dump /dev/tty`, with an `/sdcard` fallback plus cleanup, and one retry
  on the well-known "could not get idle state" failure.
- A compact node model emitting boolean flags **only in the non-default state**, and a
  byte cap that prunes the deepest levels with an explicit truncation hint.
- **Identity-first resolution**: `resource-id` > `content-desc`/text > coordinates,
  exact before case-insensitive contains, nested duplicate chains folded to the
  outermost control, off-screen and disabled gates, and an ambiguity error that lists
  candidates.
- **Compose caveat**: Compose exposes no resource-ids to uiautomator unless the app
  opts in with `testTagsAsResourceId`. Never assume stable ids exist; fall back to
  text/content-desc, and surface *why* a tree is unlabelled (filter matched nothing
  vs. cap pruned vs. genuinely no accessibility info).

## Logs

- **Snapshot** (`logcat -d`) started from a **device-clock** timestamp, so timezone
  differences between host and device cannot silently shift the window.
- **Follow**: a bounded window that returns accumulated output, then ends.
- Filters: package (via pid), tag, priority, buffer, and a literal substring.
- Bounded ring in memory while capturing; the full capture is an artifact reference.
- The follow child is killed as a **process group**.

## Screenshots and artifacts

Screenshots are **in-memory bytes referenced by an opaque id**
([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)), owned by a **single** store in
the core with TTL/eviction. No temp files, no filesystem paths leaking into results,
and no duplicate stores.

## Policy and safety

Physical devices are ordinary devices to us: same code path, same capabilities. What
is specific to them is:

- **States** — `unauthorized` and `offline` are surfaced with the fix ("accept the USB
  debugging prompt on the device"), not as an empty device list.
- **Wireless ADB** — pairing and connecting over the network is an adb-layer
  capability, and it is the one that makes physical devices usable from the remote UI
  scenario.
- **Policy** — emulator-preferred by default; a physical device needs an enable flag,
  an exact allowlist, and its serial on every call
  ([ADR-0015](../adr/0015-untrusted-data-authorization.md)).

What is *not* code is the procedure around them — enabling developer options, vendor
quirks, accepting the prompt. That belongs in a skill
([ADR-0011](../adr/0011-skills-agent-skills-standard.md)), not in the core.

- Activation is **re-verified immediately before execution** (TOCTOU-safe), separate
  from tool annotations, which are only approval hints.
- **All device output is untrusted data** (UI labels, logs, clipboard, package
  metadata) and is tagged as such at the boundary.
- Writes are confined to the workspace; no arbitrary host paths.

## Lifecycle and concurrency

- **The adb server is shared and never killed implicitly** — other tools use it.
- Long-lived children (logcat follow, screen recording) are per-operation and die
  with their operation.
- **Multi-device is a first-class assumption**: per-device locking, writes serialised
  per device, reads in parallel, and one frame pipeline per device. We do not inherit
  the single-stream-slot design of the prior art.

## Failure modes

| Failure | Behaviour |
| --- | --- |
| No device connected | Actionable error; suggests starting an emulator or enabling USB debugging |
| `unauthorized` | Names the state and tells the user to accept the prompt on the device |
| `offline` / `recovery` | Names the state; not treated as an empty device list |
| Install rejected | Parses the `INSTALL_FAILED_*` code and reports it as the cause |
| `uiautomator` idle failure | One retry, then a cause-attributed error that says whether to try again or fall back |
| Device disconnects mid-operation | Operation fails with the transport error; the workspace degrades, not the core |
| adb missing | Capabilities remain listed; every device call explains how to fix it |

## Screen viewing: on demand, not streamed

Evidence from the prior art:

| Implementation | Live frames | What it offers instead |
| --- | --- | --- |
| `dsh-android` | **Yes** — persistent `screencap` loop, PNG splitter, multipart fan-out | ~8 fps on an emulator, **2–5 fps over USB** |
| `codex-android-mcp` | Kept the loop, but only to map coordinates; its multipart writer is dead code | Screenshots on demand |
| `mobile-mcp` | **No** | Screenshots on demand, plus screen **recording** to a file |

The one that streams is the one with a live device panel in its UI; the agent-only
server never built it. And even the streaming implementation is a slideshow rather
than video on real hardware.

**Decision: the MVP does not stream.** Screenshots are captured on demand and returned
as artifact references; the UI panel uses a refresh affordance. If a smooth live view
is ever needed, the right answer is an **H.264 path** (scrcpy-style) behind a
`StreamSource` seam — not a PNG-per-frame loop. We design the seam and do not build
the pipeline.

## Open questions

- The row abstraction for feeds whose controls are unlabelled: after the MVP.
- OCR: deferred and pluggable ([ADR-0013](../adr/0013-ocr-optional-pluggable.md)).
- Whether to pool short adb invocations or accept a process per call.
- How much device state to cache (details, screen size) and how to invalidate it.
