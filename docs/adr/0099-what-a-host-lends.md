# ADR-0099: What a host lends, to whom, and what a peer may call

- Status: Accepted
- Date: 2026-10-18
- Relates to: ADR-0012 §Reading artifacts, ADR-0015 §4, §6, ADR-0032 §2, ADR-0097, ADR-0098, ADR-0100; plan m2t Q3
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated (plan m2t Q3 §1–§4, §6, reviewed in two rounds). The owner's use case is physical phones, driven fully as if local (2026-10-18). Q3 §5, the lease, is decided apart, once 2s's occupation is on `main` (plan m2t step 6).

## Context

A paired peer (ADR-0098) must reach the devices its host chooses to lend it, through the
device capabilities, and nothing else of the host: not its projects, files, shell,
builds, emulators or policy. ADR-0015 §4 and ADR-0032 §2 already admit a physical device
only by `devices.allowPhysical` and an exact allowlist entry.

## Decision

1. **Lending.** `hosting.lend: [{device, to: [peer]}]`, re-read at each call. Only a
   device the host classifies physical is lent, and it must also pass the host's
   physical policy: lending never widens it. Emulators are not lent. A device not lent to
   the calling peer is absent from its listing and `DEVICE_NOT_FOUND`.
2. **The peer surface** is the descriptors marked `Lendable`: `device.list`, `uiTree`,
   `screenshot`, `tapElement`, `launch`, `logs`, `watchLogs`, `watchScreen`, `uninstall`,
   `inputText`, `tap`, `swipe`, `pressKey`, `installUpload`; `artifact.read` of the
   peer's own artifacts; `operation.wait`, `operation.cancel` (ADR-0101) and
   `events.unsubscribe`. Anything else from client kind `peer` is
   `REQUEST_NOT_ALLOWED_HERE`, refused in the core with nothing recorded.
3. **`device.installUpload {device, upload}`** (command, global, audience `human`,
   long-running) installs an APK the peer uploaded with `PUT /peer/v1/upload`: at most
   512 MiB, one pending upload per peer, kept 0600 under `<state>/hosting/uploads/` until
   installed or 10 minutes old, swept at start.
4. **Exposure.** Lending lends the whole device: notifications, other apps, accounts.
   Input on the screen reaches all of it already, so no per-capability or per-app fence
   is attempted. `droidyard host lend` says so and recommends a dedicated device. Nothing
   is cleaned after a lease (a phone has no wipe); installs are in the log.
5. **Audit.** Each peer call is an operation on the host with client `peer` and the
   peer's name; its trace line adds `peer=<name>`; `daemon.log` records refused
   handshakes, links opened and closed, and uploads. Retention is the log's.

## Consequences

- `capability.Descriptor` gains `Lendable`; the core refuses a non-lendable capability
  from `peer` before `prepare`.
- `internal/artifact` records the producing session, so a peer reads only its own.
- `adb-device.md` §Policy and safety describes lending beside the physical policy.
- A durable audit file is not built: no requirement asks for state beyond the log.

## Alternatives considered

- **Lending emulators.** The owner excluded the host's emulators and the use case is
  phones; an emulator's owner can share its AVD instead.
- **Fences per capability or per package.** The screen and input reach everything on the
  device, so a fence on reads or on packages protects nothing a tap does not reach.
- **Uninstalling at the lease's end.** QA may need the build to stay; the host's human
  uninstalls when they want.
- **`device.install` taking an upload parameter.** Its MCP schema would grow past the
  tool budget (ADR-0036) for a parameter no local caller uses; the client's relay calls
  `installUpload` on the host instead.
- **A durable audit file.** New state and a retention policy no requirement asks for;
  `daemon.log` and the trace lines carry the same lines.
