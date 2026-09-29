# Subsystem: adb & devices

The device half of the development loop: discover devices, install and launch apps,
observe them (logs, screen), and introspect them (processes, memory, crashes).

> Status: draft. Emulator *lifecycle* (listing, booting, stopping, wiping AVDs) lives in
> [`emulator.md`](emulator.md). The UI tree, element actions and their assertions live
> in [`ui-automation.md`](ui-automation.md). This document covers the adb layer and the
> other device capabilities.

## Design rule: adb-centric, serial is the only identity

A device's one and only identity is the **serial** reported by `adb devices -l`.
Emulators, USB devices and `ip:port` devices share a single code path. Nothing
assumes a particular emulator product.

The `emulator` launcher is **optional**: only the boot-an-AVD verbs need it, and
every other capability keeps working without it.

This is the single most portable idea in the prior art
([dsh-android](../research/dsh-android.md),
[concepts](../research/dsh-codex-android-concepts.md)) and it halves the surface
compared with a per-platform split.

## adb resolution and degradation

```text
ADB env var  →  configured adb.path  →  <host SDK>/platform-tools/adb  →  adb on PATH
```

The host SDK is resolved as in [sdk](sdk.md) §Host SDK. It comes before `PATH` so that
we use the same adb as Android Studio and the same SDK as the emulator launcher.

**Never restart the shared server by accident.** An adb client whose version differs
from the running server's kills and restarts that server — cutting off Android Studio
and every other tool attached to it. Before first use, the core compares its client
version (`adb version`) with the running server's (`host:version` on port 5037). On a
mismatch it **warns and proceeds**: device calls still run, the warning names both
binaries and the config key that selects a matching adb, and `system.health` carries it.
*Ratified 2026-09-29: warn and continue, rather than refuse.* The risk is accepted and
recorded: restarting the shared server can interrupt another tool mid-operation.
If no server is running, the core starts one; starting is not killing.

If nothing resolves, the core **still loads and registers every capability**; each
device call then fails with `DEVICE_ADB_NOT_FOUND` and an explanation. Degrade, don't refuse.
`system.capabilities` reports device capabilities as unavailable with that reason, and
`system.health` reports the resolved binary, its version and its source. Devices are
global, so no workspace is degraded by this.

## Device model

```text
Device
├── device          the serial: the identity, and the value of every `device` argument
├── state           device | offline | unauthorized | recovery | sideload | unknown
├── kind            emulator | physical
├── model / product / transportId
├── androidVersion / sdk / avdName   (getprop, best-effort)
└── online          derived
```

Details are best-effort: a flaky device still lists, its details simply stay sparse.
Emulator detection uses the serial prefix **or** known product strings, so it does
not depend on naming alone.

## Targeting: `device` is required

Every device capability except `device.list` takes a **required** `device` argument,
and so does `emulator.stop`. The capability layer never picks a device: there is no
workspace "active device" ([workspace-state](../architecture/workspace-state.md)
§Devices are not workspace-owned) and no "the only online device" fallback
([capability-api](../architecture/capability-api.md) §One name for each target). A call
without `device` fails schema validation at `Start`, before any operation exists.

| Situation | Code | Behaviour |
| --- | --- | --- |
| Serial not connected | `DEVICE_NOT_FOUND` | `details.online[]` lists the candidates; next step `device.list` or `emulator.start` |
| Connected but unusable (`offline`, `unauthorized`, `recovery`, `sideload`, still booting) | `DEVICE_NOT_READY` | Names the state and its fix |
| Not permitted by policy | `DEVICE_NOT_ALLOWED` | Rejected at `Start` as a policy denial ([ADR-0015](../adr/0015-untrusted-data-authorization.md)) |

Convenience belongs to adapters, and each one still sends `device` explicitly:

| Adapter | Where `device` comes from |
| --- | --- |
| Web UI | Its own device selection, kept client-side and sent on every call |
| CLI | `--device`, else `ANDROID_SERIAL` — adb's own convention, set explicitly by the user. *Ratified 2026-09-29* |
| MCP | The agent passes it; the server instructions say so. Sessions are bound to a workspace, never to a device |

## Transport to the device

| Use | Mechanism | Notes |
| --- | --- | --- |
| Text commands | `adb -s <serial> shell …` | CRLF-normalised |
| Binary output | `exec-out` | Skips the pty so PNG bytes survive |
| Long-lived children | `spawn` | Process group, killed as a group |

Every invocation has an explicit timeout and a bounded buffer. No unbounded
accumulation anywhere.

## Capabilities

Scope per [capability-api](../architecture/capability-api.md) §Classification. Every
row except `device.list` takes `device`.

| Capability | Phase | Scope | Params and behaviour |
| --- | --- | --- | --- |
| `device.list` | MVP | G | Filtered by policy; details best-effort |
| `device.install` | MVP | W | `build: {buildId?, module, variant}` → `{applicationId, versionCode}` (§Install) |
| `device.launch` | MVP | G | `applicationId` → `{pid, launchedAt}` (§Launch) |
| `device.uninstall` | MVP | G | `applicationId` |
| `device.logs` | MVP | G | Snapshot or bounded follow, structured (§Logs) |
| `device.screenshot` | MVP | G | Returns an **artifact reference**, never a path |
| `device.uiTree`, `device.tapElement`, `device.inputText` | MVP | G | See [ui-automation](ui-automation.md) |
| `device.forceStop` | post-MVP | G | `am force-stop` |
| `device.processes`, `device.meminfo`, `device.appInfo`, `device.backtrace` | post-MVP | G | Parse into structured facts; empty is a fact, not a failure |

The app is named **`applicationId`** in every device capability — the build output's
name for it, and the package name on the device.

### Install

The device subsystem does **not** find APKs or guess application ids. `device.install`
takes a build reference, and the build subsystem resolves it from its registry, whose
single source is AGP's `output-metadata.json`
([ADR-0023](../adr/0023-android-project-facts-and-build-outputs.md),
[gradle](gradle.md) §Build outputs). No build script or manifest is read. This is why
`device.install` is the one workspace-scoped device capability.

- `adb -s <serial> install -r <apk>`; `Failure [INSTALL_FAILED_…]` is detected even on
  exit 0 and reported as `DEVICE_INSTALL_FAILED` with the `INSTALL_FAILED_*` reason.
- Output resolution errors are the build subsystem's: `BUILD_OUTPUT_NOT_FOUND`,
  `BUILD_OUTPUT_STALE`, `BUILD_OUTPUT_UNSUPPORTED`.
- The result carries `applicationId`, which `launch`, `uninstall` and `logs` take.

### Launch

- `monkey -p <applicationId> -c android.intent.category.LAUNCHER 1` — no manifest
  activity needed; "No activities found" becomes `DEVICE_LAUNCH_FAILED`.
- The device clock is read just before, and returned as `launchedAt`.
- Then `pidof <applicationId>` is polled (every 100 ms, up to 3 s). `pid` is returned
  and recorded in the app's pid history (§Logs). `pid: null` with a reason means the
  process was not observed — typically a crash on start; `device.logs` still finds it.

## Logs

`device.logs {device, applicationId?, since?, tags?, minPriority?, buffers?,
contains?, follow?}`

- **Snapshot** (`logcat -d`) from a **device-clock** timestamp, so timezone differences
  between host and device cannot silently shift the window. `since: "launch"` means
  the latest recorded `device.launch` of that app, and is the default when an
  `applicationId` is given and a launch is known; otherwise the default is a tail.
- **Follow**: a bounded window that returns accumulated output, then ends. A
  long-running *stateless* call, not a stream.
- Filters: `applicationId`, tag, priority, buffer, and a literal substring. Lines are
  filtered **while capturing**, then capped, so other apps' noise cannot exhaust the
  cap.
- The full capture is an artifact reference; the follow child is killed as a
  **process group**.

### Filtering by app survives the app's death

The MVP's crash path is the case that must work: after a crash the process is gone, so
"resolve the package to its current pid" finds nothing, and an empty result would be
reported as a fact. Instead:

1. **By uid, where supported.** The app's uid (`pm list packages -U`) is stable across
   restarts and `-r` reinstalls, and `logcat --uid` covers every process of the app,
   including dead ones. Support is probed once per device and cached.
2. **Otherwise by pid history.** Per `(device, applicationId)`, the daemon keeps a
   bounded, in-memory history of pids with first/last seen, fed by `device.launch`,
   by `pidof` at each call, and by lines announcing a process while capturing
   (ActivityManager's `Start proc <pid>:<applicationId>/…`). The filter is every pid
   that overlaps the window.
3. **The crash buffer is always read** when an app filter is set, whatever `buffers`
   says, and it is filtered **by block, not by pid**: a Java crash block names the app
   (`Process: <applicationId>, PID: <pid>`), a native one in its tombstone header
   (`>>> <applicationId> <<<`). The crash therefore appears even when no pid was ever
   known. Its pid joins the history. Crash lines carry `crash: true`, and the result
   summarises them in `crashes[] {pid, time, exception, message}` (bounded).

The result states the filter it applied — `filter: {applicationId, by: uid | pids,
uid?, pids[]}` — and an empty result says why: `noLines` (the app ran and logged
nothing matching), `noKnownProcess` (no process of the app in the window, and no
crash), or `filtered` (lines existed but the tag/priority/substring filters excluded
them).

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
- **Policy** — emulator-preferred by default; a physical device needs an enable flag
  and an exact allowlist, and listings hide it otherwise
  ([ADR-0015](../adr/0015-untrusted-data-authorization.md)). The explicit `device`
  argument is no longer physical-specific: every device call carries one.

What is *not* code is the procedure around them — enabling developer options, vendor
quirks, accepting the prompt. That belongs in a skill
([ADR-0011](../adr/0011-skills-agent-skills-standard.md)), not in the core.

- Activation is **re-verified immediately before execution** (TOCTOU-safe), separate
  from tool annotations, which are only approval hints.
- **All device output is untrusted data** (UI labels, logs, clipboard, package
  metadata) and is tagged as such at the boundary.
- Writes are confined to the workspace; no arbitrary host paths.

## Lifecycle and concurrency

- **The adb server is shared and never killed implicitly** — other tools use it (see
  §adb resolution for the version-mismatch case).
- Long-lived children (logcat follow, screen recording) are per-operation and die
  with their operation.
- **Multi-device is a first-class assumption**: per-device locking, writes serialised
  per device, reads in parallel, and one frame pipeline per device. We do not inherit
  the single-stream-slot design of the prior art.

## Failure modes

| Failure | Code | Behaviour |
| --- | --- | --- |
| No device connected | `DEVICE_NOT_FOUND` | Suggests starting an emulator or enabling USB debugging |
| `unauthorized` | `DEVICE_NOT_READY` | Names the state and tells the user to accept the prompt on the device |
| `offline` / `recovery` | `DEVICE_NOT_READY` | Names the state; not treated as an empty device list |
| Install rejected | `DEVICE_INSTALL_FAILED` | The `INSTALL_FAILED_*` reason as the cause |
| Launch finds no launcher activity | `DEVICE_LAUNCH_FAILED` | The monkey output line as the cause |
| Device disconnects mid-operation | `DEVICE_DISCONNECTED` | That operation fails with the transport error; other devices and workspaces are unaffected |
| adb missing | `DEVICE_ADB_NOT_FOUND` | Capabilities remain listed; every device call explains how to fix it |
| adb client/server version mismatch | **A warning, not an error**: both versions and binaries are reported by `system.health` and on the device call; the shared server is left alone |

UI automation failures are listed in [ui-automation](ui-automation.md) §Failure modes.

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

## To verify (spike S9, plus adb)

- The API level from which `logcat --uid` works, and whether it covers the crash
  buffer; below it, the pid-history path is the only one.
- The exact `Start proc` and crash-header formats across API levels, and whether the
  crash buffer is still readable after the crashed process is gone (expected: yes).
- `pidof` availability (toybox, API 24+) and the fallback below it.
- The adb client/server mismatch behaviour and the `host:version` value it compares.

## Open questions

- Whether to pool short adb invocations or accept a process per call.
- How much device state to cache (details, screen size) and how to invalidate it.
