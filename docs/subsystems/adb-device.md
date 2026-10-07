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
ADB env var  →  configured adb.path (with the configuration file)  →  <host SDK>/platform-tools/adb  →  adb on PATH
```

The host SDK is resolved as in [sdk](sdk.md) §Host SDK. It comes before `PATH` so that
we use the same adb as Android Studio and the same SDK as the emulator launcher.

**Never restart the shared server by accident.** An adb client whose version differs
from the running server's kills and restarts that server — cutting off Android Studio
and every other tool attached to it. Before first use, the core compares its client
version (`adb version`) with the running server's (`host:version` on port 5037). On a
mismatch it **warns and proceeds**: device calls still run, the warning names both
binaries and `ADB` (the config key, once the configuration file exists) as the way to
select a matching adb, and `system.health` and the device call's `warnings` carry it.
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
not depend on naming alone. A device that `devices -l` reports with `usb:` is
`physical` whatever its product says, and an unclassifiable device is `physical`, so
the emulators-only default never admits an unknown device (milestone 1e).

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
| Not permitted by policy | `DEVICE_NOT_ALLOWED` | Decided at the re-check just before the handler, after one `devices -l`, so the operation fails; `Start` does no adb I/O ([ADR-0015](../adr/0015-untrusted-data-authorization.md); [ADR-0020](../adr/0020-operation-contract.md), amended 2026-10-04) |

`DEVICE_NOT_READY` carries `details.state`: adb's own state (`offline`, `unauthorized`,
`recovery`, `sideload`, `unknown`), or `booting` when `sys.boot_completed` is not `1`, or
`unresponsive` when the device does not answer it (milestone 1e).

Convenience belongs to adapters, and each one still sends `device` explicitly:

| Adapter | Where `device` comes from |
| --- | --- |
| Web UI | Its own device selection, kept client-side and sent on every call |
| CLI | `--device`, else `ANDROID_SERIAL` — adb's own convention, set explicitly by the user. *Ratified 2026-09-29*; not built in 1e (the CLI passes `device` in `--params`) |
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
| `device.install` | MVP, with `build.*` | W | `build: {buildId?, module, variant}` → `{applicationId, versionCode, apk}` (§Install); moved out of milestone 1f to the `build.*` milestone, since its only input is a build reference (plan m1f D1) |
| `device.launch` | MVP | G | `applicationId` → `{pid, launchedAt}` (§Launch) |
| `device.uninstall` | MVP, with `build.*` | G | `applicationId` → `{applicationId, uninstalled, reason?}` (moves with `device.install`, plan m1f D1; D18) |
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

- `adb -s <serial> install -r <apk>`; `Failure [INSTALL_FAILED_…]` (bare, or followed
  by a colon and a message, as modern adb prints it) is detected even on exit 0 and
  reported as `DEVICE_INSTALL_FAILED` with the `INSTALL_FAILED_*` token alone.
- Output resolution errors are the build subsystem's: `BUILD_OUTPUT_NOT_FOUND`,
  `BUILD_OUTPUT_STALE`, `BUILD_OUTPUT_UNSUPPORTED`; and `BUILD_NOT_FOUND` for a
  `buildId` that names no build of the workspace, `WORKSPACE_STATE_UNREADABLE` when its
  records cannot be read.
- The install holds the device's key, not the project's: a build of the same variant
  started during an install can rewrite the APK while adb streams it (milestone 1g,
  accepted for the MVP: one agent per root; see the plan's Changes).
- The result carries `applicationId` (which `launch`, `uninstall` and `logs` take), the
  output's `versionCode` and the installed `apk`'s file name — never a path.
- `device.uninstall {device, applicationId}` runs `pm path` first: an absent package is a
  completed `{applicationId, uninstalled: false, reason: "notInstalled"}`; otherwise
  `adb uninstall`, whose `Success` is `uninstalled: true` and whose `Failure […]` is
  `DEVICE_UNINSTALL_FAILED` with the bracketed token as the cause (plan m1g D18).

### Launch

- `monkey -p <applicationId> -c android.intent.category.LAUNCHER 1` — no manifest
  activity needed; "No activities found" becomes `DEVICE_LAUNCH_FAILED`.
- The device clock is read just before, and returned as `launchedAt`.
- Then `pidof <applicationId>` is polled (every 100 ms, up to 3 s). `pid` is returned;
  recording it in the app's pid history (§Logs) lands with `device.logs`. `pid: null`
  with a reason means the process was not observed — typically a crash on start, or any
  failure once `monkey` ran, which is never an error (the app may have launched).

## Logs

`device.logs {device, applicationId?, since?, tags?, minPriority?, buffers?,
contains?, follow?}`

*As built in milestone 1i ([plan](../plans/m1i-device-logs-and-emulator.md) D4–D11):*
`follow?` is `followMs` (1 000–60 000), a window that starts at the call (or at an
explicit `since`); `since` is `"launch"` (the default after a recorded `device.launch`)
or a device-clock time. Lines are read in `-v threadtime,epoch,usec,uid` and filtered
host-side by the app's uid from `pm list packages -U` (the pid history when no uid is
known), so the app's lines survive its death; crash blocks in `-b crash` are reported in
`crashes[]` (Java by `Process: <pkg>, PID:`, native by `>>> <pkg> <<<`, both only with
the app's uid); the result carries `filter {by, uid?}`, bounded inline `lines`, a
capture artifact of at most 4 MiB (marked untrusted, with a dropped count when cut),
`truncated`, `emptyReason` and `warnings`. A disconnection is read from adb's stderr
only. Every device-derived field is untrusted.

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
- **The UI reader** ([ADR-0030](../adr/0030-persistent-ui-reader.md)) is what Droidyard
  leaves on a device: `/data/local/tmp/droidyard-reader-<sha256>.dex`, pushed once and
  kept; a process named `droidyard-reader-<first 12 hex>` serving reads on a local
  socket; and one `adb forward tcp:<port> localabstract:droidyard-reader-<12 hex>` on
  the host. The process ends itself after 30 idle minutes; the daemon's stop quits it
  and removes the forward. The daemon recovers: a lost forward is recreated, a reader
  that does not answer is killed by its `pidof` pid and started again, an earlier
  daemon's answering reader is adopted, and a reader of another digest (found with
  `ps -A -o PID,NAME`) is killed, since it holds the device's one `UiAutomation`. The
  one-shot fallback uses its own `…-oneshot.dex`, pushed and removed per read. Nothing
  else is listed, removed or killed.
- **Multi-device is a first-class assumption**: per-device locking, writes serialised
  per device, reads in parallel, and one frame pipeline per device. We do not inherit
  the single-stream-slot design of the prior art.

## Failure modes

| Failure | Code | Behaviour |
| --- | --- | --- |
| No device connected | `DEVICE_NOT_FOUND` | Suggests starting an emulator or enabling USB debugging |
| `unauthorized` | `DEVICE_NOT_READY` | Names the state and tells the user to accept the prompt on the device |
| `offline` / `recovery` | `DEVICE_NOT_READY` | Names the state; not treated as an empty device list |
| Install rejected | `DEVICE_INSTALL_FAILED` | The `INSTALL_FAILED_*` reason as the cause; detected even on exit 0 |
| Uninstall rejected | `DEVICE_UNINSTALL_FAILED` | The `Failure […]` token as the cause; a package that is not installed is a completed call, not an error (plan m1g D18) |
| Launch finds no launcher activity | `DEVICE_LAUNCH_FAILED` | The monkey output line as the cause |
| `logcat` or `pm` fails on a ready device, exits non-zero, or prints a line that is not the requested format | `DEVICE_LOGS_FAILED` | `cause` names the command, its exit status or the format mismatch, never a device line; the wrong format is detected from the lines, since `logcat -v` with a bad argument exits 0 (milestone 1i, D10) |
| Device disconnects mid-operation | `DEVICE_DISCONNECTED` | That operation fails with the transport error; other devices and workspaces are unaffected |
| adb missing, or `ADB` names no file | `DEVICE_ADB_NOT_FOUND` | Capabilities remain listed; every device call explains how to fix it. A bad `ADB` value fails closed, naming the value, and is not skipped: another adb could mismatch the running server and restart it (milestone 1e, Q6) |
| adb fails on the host (the server cannot start, `devices -l` times out or cannot be parsed) | `DEVICE_ADB_FAILED` | `cause` names the command, its exit status or timeout and the decisive line (milestone 1e, Q3) |
| `screencap` fails or returns no PNG on a ready device | `DEVICE_SCREENSHOT_FAILED` | The command's decisive line as the cause (milestone 1e, Q3) |
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

*Answered by spike S9 (2026-10-07, [findings](../research/spike-s8-s9-emulator-and-logcat.md)),
API 31: `--uid` works and passes the crash buffer; the Java and the native crash stay
readable in `-b crash` after the process died, with the app's uid; killing the
`adb logcat` client ends the device's logcat. Older API levels not run.*

- The API level from which `logcat --uid` works, and whether it covers the crash
  buffer; below it, the pid-history path is the only one.
- The exact `Start proc` and crash-header formats across API levels, and whether the
  crash buffer is still readable after the crashed process is gone (expected: yes).
- `pidof` availability (toybox, API 24+) and the fallback below it.
- The adb client/server mismatch behaviour and the `host:version` value it compares.

## Open questions

- Whether to pool short adb invocations or accept a process per call.
- How much device state to cache (details, screen size) and how to invalidate it.
