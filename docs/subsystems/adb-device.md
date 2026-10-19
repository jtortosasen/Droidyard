# Subsystem: adb & devices

The device half of the development loop: discover devices, install and launch apps,
observe them (logs, screen), and introspect them (processes, memory, crashes). Emulator
*lifecycle* is in [`emulator.md`](emulator.md); the UI tree, element actions and their
assertions are in [`ui-automation.md`](ui-automation.md). The parameters, results and error
lists are the descriptors' (`internal/domain/device`, `schema/capabilities.json`); this
document keeps the rules behind them.

## Design rule: adb-centric, serial is the only identity

A device's one and only identity is the **serial** reported by `adb devices -l`. Emulators,
USB devices and `ip:port` devices share a single code path, and nothing assumes a
particular emulator product. The `emulator` launcher is **optional**: only the boot-an-AVD
verbs need it. This is the single most portable idea in the prior art
([dsh-android](../research/dsh-android.md),
[concepts](../research/dsh-codex-android-concepts.md)) and it halves the surface compared
with a per-platform split. A device another Droidyard lends is named `<remote>/<serial>`
and runs no local adb: its calls are relayed to the host, which applies this document
([ADR-0100](../adr/0100-remote-devices-on-the-client.md); [peer.md](../interfaces/peer.md)).

## adb resolution and degradation

Order: `tools.adb` (config, read once at daemon start; a change needs a restart), then the
`ADB` environment variable, then `<host SDK>/platform-tools/adb`, then `adb` on `PATH`
([ADR-0032](../adr/0032-configuration-file-v1.md) §3, as its Q2 `Amended:` line records;
this order replaced the earlier ADB-first one in milestone 1k). A configured `tools.adb` or
an `ADB` that names no executable **fails closed**: nothing after it is tried, because
another adb could mismatch the running server and restart it (1e, Q6). When the key wins
with `ADB` set, `system.health` lists `ADB` as "overridden by tools.adb". An invalid
configuration file at start is a rejected entry naming the file, and resolution falls
through. A fence set through `ADB` (a guard script) is bypassed by a configured `tools.adb`.
The host SDK is resolved as in [sdk](sdk.md) §Host SDK; it comes before `PATH` so that we
use the same adb as Android Studio and the same SDK as the emulator launcher.

**Never restart the shared server by accident.** An adb client whose version differs from
the running server's kills and restarts that server, cutting off Android Studio and every other tool
attached to it. Before first use, the core compares its client version (`adb version`) with the
running server's (`host:version` on port 5037). On a mismatch it **warns and proceeds** (ratified
2026-09-29: warn and continue, rather than refuse), naming both binaries and `tools.adb` or `ADB` as
the way to select a matching adb, in `system.health` and the device call's `warnings`. The risk is
accepted and recorded: restarting the shared server can interrupt another tool mid-operation. If no
server is running, the core starts one; starting is not killing.

If nothing resolves, the core **still loads and registers every capability**; each device
call then fails `DEVICE_ADB_NOT_FOUND` with an explanation. Degrade, don't refuse:
`system.capabilities` reports them unavailable with that reason, and `system.health` reports the
resolved binary, version and source. Devices are global, so no workspace is degraded by this.

## Device model

Details are best-effort: a flaky device still lists, its details stay sparse. A device that
`devices -l` reports with `usb:` is `physical` whatever its product says; otherwise an
`emulator-<port>` serial is an emulator, and a known emulator product makes one only on a
loopback serial; anything else is `physical`, so the emulators-only default never admits an
unknown device ([ADR-0103](../adr/0103-shared-serial-and-emulator-class.md) §2). A serial two
devices report is refused for both and never lent (§1).

## Targeting: `device` is required

Every device capability except `device.list` takes a **required** `device` argument, and so does
`emulator.stop`. The capability layer never picks a device: there is no workspace "active device"
([workspace-state](../architecture/workspace-state.md) §Devices are not workspace-owned) and no "the
only online device" fallback ([capability-api](../architecture/capability-api.md) §One name for each
target). A call without `device` fails schema validation at `Start`, before any operation exists.

- A serial not connected is `DEVICE_NOT_FOUND`; a connected but unusable device (`offline`,
  `unauthorized`, `recovery`, `sideload`, still booting) is `DEVICE_NOT_READY`, naming state and fix. A system dialog the target is not inside is the same refusal (nothing
  acted), so it shares the code ([ADR-0034](../adr/0034-device-calls-report-busy-crash-dialog.md) §3).
- A device not permitted by policy is `DEVICE_NOT_ALLOWED`, decided at the re-check just before the
  handler, after one `devices -l`, so the operation fails; `Start` does no adb I/O
  ([ADR-0015](../adr/0015-untrusted-data-authorization.md);
  [ADR-0020](../adr/0020-operation-contract.md), amended 2026-10-04).
- Convenience belongs to adapters, and each one still sends `device` explicitly: the web UI keeps its
  own selection client-side; the CLI would take `--device`, else `ANDROID_SERIAL`, adb's own
  convention (ratified 2026-09-29; not built, the CLI passes `device` in `--params`); an MCP agent
  passes it, since sessions are bound to a workspace, never to a device.
- An MCP session's first acting call on a free device occupies it; another chat's acting call is
  `DEVICE_OCCUPIED`, decided after the policy re-check and before any command that changes the
  device. Reads, the UI and the CLI are never refused ([ADR-0094](../adr/0094-occupation-by-a-session.md)).
- On a host, a paired peer's call of any kind on a lent device leases it: one peer at a time,
  host agents refused too, the host's UI and CLI never
  ([ADR-0104](../adr/0104-lease-of-a-lent-device.md)).

## Transport to the device

Text commands run as `adb -s <serial> shell ...` (CRLF-normalised); binary output uses
`exec-out`, which skips the pty so PNG bytes survive; long-lived children use `spawn`, in a
process group killed as a group. Every invocation has an explicit timeout and a bounded
buffer: no unbounded accumulation anywhere. The reader and the screen server are reached
through `adb forward localfilesystem:` sockets in `<state>/run/fwd/` (0700; past
`sun_path`, `/tmp/droidyard-<uid>`), never a TCP port;
adb prints their paths raw in `forward --list`, spaces included
([ADR-0102](../adr/0102-local-channels-to-the-device.md)).

## Capabilities

The app is named **`applicationId`** in every device capability: the build output's name for it, and
the package name on the device. `device.install` and `device.uninstall` moved from milestone 1f to
the `build.*` milestone, since the install's only input is a build reference (plan m1f D1; D18).
`device.forceStop` and the introspection capabilities (`processes`, `meminfo`, `appInfo`,
`backtrace`; empty is a fact, not a failure) are post-MVP.

### Install

The device subsystem does **not** find APKs or guess application ids. `device.install` takes a build
reference, and the build subsystem resolves it from its registry, whose single source is AGP's
`output-metadata.json` ([ADR-0023](../adr/0023-android-project-facts-and-build-outputs.md),
[gradle](gradle.md) §Build outputs); no build script or manifest is read. This is why
`device.install` is the one workspace-scoped device capability.

- `adb -s <serial> install -r <apk>`; `Failure [INSTALL_FAILED_...]` (bare, or followed by a colon
  and a message, as modern adb prints it) is detected even on exit 0 and reported as
  `DEVICE_INSTALL_FAILED` with the `INSTALL_FAILED_*` token alone.
- The install holds the device's key, not the project's: a build of the same variant started during
  an install can rewrite the APK while adb streams it (1g, accepted for the MVP: one agent per root).
- `device.uninstall` runs `pm path` first: an absent package is a completed `uninstalled: false,
  reason: "notInstalled"`; otherwise `adb uninstall`, whose `Failure [...]` is
  `DEVICE_UNINSTALL_FAILED` with the bracketed token as the cause (plan m1g D18).

### Launch

`monkey -p <applicationId> -c android.intent.category.LAUNCHER 1`: no manifest activity needed. The
device clock is read just before and returned as `launchedAt`; then `pidof` is polled for the pid.
`pid: null` with a reason means the process was not observed, typically a crash on start, or any
failure once `monkey` ran, which is never an error (the app may have launched).

## Logs

Decisions behind `device.logs` (built in 1i, [plan](../plans/m1i-device-logs-and-emulator.md)
D4-D11; the shape is the descriptor's):

- **Snapshot** (`logcat -d`) from a **device-clock** timestamp, so timezone differences between host
  and device cannot silently shift the window; `since: "launch"` means the latest recorded
  `device.launch` of that app. **Follow** is a bounded window (`followMs`, 1 000-60 000) that returns
  accumulated output, then ends: a long-running *stateless* call, not a stream.
- Lines are filtered **while capturing**, then capped, so other apps' noise cannot exhaust the cap. The
  full capture is an artifact of at most 4 MiB, marked untrusted, with a dropped count when cut. A
  disconnection is read from adb's stderr only.
- The follow child is killed by its **pid** during the run (a forked adb server may share its group),
  and by its process group only at reconciliation after a daemon crash (1i D9).
- A `device.watchLogs` line names its application's package, read from `pm list packages -U` by the
  line's uid when the stream first sees it ([ADR-0051](../adr/0051-logcat-line-package.md)).

### Filtering by app survives the app's death

The MVP's crash path is the case that must work: after a crash the process is gone, so
"resolve the package to its current pid" finds nothing, and an empty result would be reported
as a fact. Instead:

1. **By uid, where supported.** The app's uid (`pm list packages -U`) is stable across
   restarts and `-r` reinstalls, and `logcat --uid` covers every process of the app,
   including dead ones. Support is probed once per device and cached. The filter is applied
   host-side by uid.
2. **Otherwise by pid history.** Per `(device, applicationId)`, the daemon keeps a bounded,
   in-memory history of pids with first/last seen, fed by `device.launch`, by `pidof` at each
   call, and by lines announcing a process while capturing (ActivityManager's `Start proc
   <pid>:<applicationId>/...`). The filter is every pid that overlaps the window.
3. **The crash buffer is always read** when an app filter is set, whatever `buffers` says,
   and it is filtered **by block, not by pid**: a Java crash block names the app
   (`Process: <applicationId>, PID: <pid>`), a native one in its tombstone header
   (`>>> <applicationId> <<<`), both only with the app's uid. The crash therefore appears
   even when no pid was ever known. Its pid joins the history. Crash lines carry
   `crash: true` and the result summarises them in a bounded `crashes[]`.

The result states the filter it applied, and an empty result says why: `noLines` (the app ran
and logged nothing matching), `noKnownProcess` (no process of the app in the window, and no
crash), or `filtered` (lines existed but the tag/priority/substring filters excluded them).
A `logcat` or `pm` failure on a ready device, a non-zero exit or a line not in the requested
format is `DEVICE_LOGS_FAILED`, detected from the lines since `logcat -v` with a bad argument
exits 0 (1i, D10).

## Screenshots and artifacts

Screenshots are **in-memory bytes referenced by an opaque id**
([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)), owned by a **single** store in the core
with TTL/eviction. No temp files, no filesystem paths leaking into results, and no duplicate
stores.

## Policy and safety

Physical devices are ordinary devices to us: same code path, same capabilities. What is
specific to them:

- **States**: `unauthorized` and `offline` are surfaced with the fix ("accept the USB
  debugging prompt on the device"), not as an empty device list.
- **Wireless ADB**: pairing and connecting over the network is an adb-layer capability, and
  the one that makes physical devices usable from the remote UI scenario.
- **Policy**: emulators by default; a physical device is listed and usable only when
  `devices.allowPhysical` is true **and** its exact serial is in `devices.allowlist`, re-read at each
  call; otherwise it is hidden from listings and `DEVICE_NOT_ALLOWED`, and an invalid file admits none
  ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §4,
  [ADR-0032](../adr/0032-configuration-file-v1.md) §2). A string that is not a listed serial is
  `DEVICE_NOT_FOUND` before anything runs (adb's `-s` also accepts `usb:`, `model:`, `product:`
  selectors that would name a device under another name). A host lends only devices this
  policy admits, to the peers `hosting.lend` names
  ([ADR-0099](../adr/0099-what-a-host-lends.md) §1); a client admits a remote device by its
  `<remote>/<serial>` id in its own allowlist. `droidyard device allow|disallow`, `share` and
  `join` write this policy, a human's command being the consent
  ([ADR-0108](../adr/0108-share-lifetime-and-consent.md) §1).

What is *not* code is the procedure around them (enabling developer options, vendor quirks,
accepting the prompt); that belongs in a skill
([ADR-0011](../adr/0011-skills-agent-skills-standard.md)), not in the core. Activation is
**re-verified immediately before execution** (TOCTOU-safe), separate from tool annotations,
which are only approval hints. **All device output is untrusted data** (UI labels, logs,
clipboard, package metadata), tagged as such at the boundary. Writes are confined to the
workspace; no arbitrary host paths.

## Lifecycle and concurrency

- **The adb server is shared and never killed implicitly**; other tools use it.
- Long-lived children (logcat follow, the screen server's `adb shell`) are per-operation and die with their
  operation.
- **The UI reader** ([ADR-0030](../adr/0030-persistent-ui-reader.md)) is what Droidyard leaves on a
  device: `/data/local/tmp/droidyard-reader-<sha256>.dex`, pushed once and kept; a process named
  `droidyard-reader-<first 12 hex>`; and one `adb forward` on the host. It ends itself after 30 idle
  minutes; the daemon's stop quits it and removes the forward. The daemon recovers a lost forward, a
  reader that does not answer (killed by its `pidof` pid and restarted) and an earlier daemon's
  reader (adopted); a reader of another digest is killed, since it holds the device's one
  `UiAutomation`. The one-shot fallback uses its own `...-oneshot.dex`, pushed and removed per read.
  Nothing else is listed, removed or killed (`persistent.go`).
- **The screen server** (ADR-0077) runs only while a live view does: its jar is pushed at each start
  and deletes itself as it starts, the server leaves the device about a second after its last viewer,
  its device, a protocol error or the daemon's stop, and its `adb forward` is removed then. A daemon
  killed outright leaves that forward until adb restarts or the device goes; the next start reaps the
  recorded `adb shell` client (`screensource.go`).
- **Multi-device is a first-class assumption**: per-device locking, writes serialised per
  device, reads in parallel, one frame pipeline per device. We do not inherit the
  single-stream-slot design of the prior art.

## Failure modes

Codes and causes are the descriptors' and `internal/capability/error.go`'s. The decisions:
`unauthorized`, `offline` and `recovery` are `DEVICE_NOT_READY`, never an empty device list; a
package that is not installed is a completed uninstall; a device that disconnects mid-operation fails
that operation with the transport error and leaves other devices and workspaces unaffected; adb
failing on the host (the server cannot start, `devices -l` times out or cannot be parsed) and a failed
`screencap` carry the decisive line as the cause (1e, Q3); an adb client/server version mismatch is
**a warning, not an error**. UI automation failures: [ui-automation](ui-automation.md) §Failure modes.

## Screen viewing: a live H.264 view

The UI's live view streams the device's own H.264 from scrcpy's pinned server
([ADR-0077](../adr/0077-screen-stream-source.md)) behind the `screen.Source` seam, so a relayed
device could feed the same seam unchanged; its wire, sharing and lifetimes are
[ADR-0078](../adr/0078-screen-watch-and-endpoint.md), its input
[ADR-0079](../adr/0079-screen-input-capabilities.md), and `FLAG_SECURE`
[ADR-0080](../adr/0080-flag-secure-detection.md). Agents keep `device.screenshot`, on demand. The
facts it rests on are [the spike](../research/spike-screen-stream-2026-10.md).

**Upgrading the server** is deliberate, never automatic: take the new release's `scrcpy-server`
asset, compare its SHA-256 with the release's `SHA256SUMS.txt`, replace the file and its `LICENSE`
in `internal/domain/device/screen/scrcpyserver`, set `Version` and `SHA256`, re-record the fixtures
with `TestSpikeScrcpy` and run `TestRealScreenSource` on a scratch AVD of the newest system image.
A new Android release on a device is the usual reason; a start that fails says so as
`DEVICE_STREAM_FAILED` with its phase.
