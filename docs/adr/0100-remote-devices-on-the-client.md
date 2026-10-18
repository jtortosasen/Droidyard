# ADR-0100: Remote devices on the client, the same capabilities relayed

- Status: Accepted
- Date: 2026-10-18
- Supersedes: ADR-0077 §4 (in part: the seam carries the secure state)
- Relates to: ADR-0015 §4, ADR-0032 §2, ADR-0076, ADR-0078 §5, ADR-0079 §3, ADR-0080, ADR-0098, ADR-0099; `adb-device.md` §Design rule; plan m2t Q4
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated (plan m2t Q4, reviewed in two rounds). The owner asked that a remote device be driven as if it were local, by the UI and by agents (2026-10-18).

## Context

`adb-device.md` §Design rule makes the serial a device's only identity, and every device
call runs adb locally. A remote device has no local adb, and two hosts may lend the same
serial. ADR-0077 §4's `screen.Source` seam was built to be relayed (C4), but the secure
state (ADR-0080) is published from inside the scrcpy source, outside the seam.

## Decision

1. **Identity** `<remote>/<serial>`, `<remote>` the `remotes` entry's name: the device
   argument, the operation target (ADR-0076), the occupation key and the write key.
   `device.list` rows gain `host`; an unreachable remote is a warning on the list.
2. **Admission is unchanged** (ADR-0015 §4, ADR-0032 §2): a remote device is physical, so
   the client lists and uses it only when its own `allowPhysical` is true and its
   allowlist names `<remote>/<serial>`, checked before any host call. Not lent, or
   refused by the host: `DEVICE_NOT_FOUND`; refused by the client's policy:
   `DEVICE_NOT_ALLOWED` with its cause and nextStep, as a local phone.
3. **Relay, per class.** Plain calls are forwarded and waited on the host; `install`
   resolves the build locally, uploads it and calls `device.installUpload`; artifacts are
   fetched and stored locally; `watchLogs` events are republished; `watchScreen` runs
   through a relayed `screen.Source`. Input from a remote device's view is the host's
   capability call. `emulator.*` on a remote id is `DEVICE_NOT_FOUND`.
4. **Trust and errors.** A relayed result or error is untrusted as a whole (another
   machine wrote it). A code the client's descriptor does not declare becomes
   `DEVICE_NOT_READY` (cause `incompatible`); a link down before the call
   `DEVICE_NOT_READY` (`unreachable`); a link lost during it `DEVICE_DISCONNECTED`. No new
   error code.
5. **The seam carries the secure state** for every source. A relayed source never
   injects (input is a host call, §3), and splits a joined key frame back into config and
   key frame, so the client's Hub keeps ADR-0078 §5.
6. **No local adb** is needed or run for a remote device.

## Consequences

- `adb-device.md` §Design rule: the serial is the identity of a local device; a remote
  one is `<remote>/<serial>`.
- The relayed stream's bytes are the host device's own H.264, unchanged but for one
  config prepend per join.
- A host lending one more phone does not reach the client's agents until the client's
  human admits it.

## Alternatives considered

- **The `remotes` entry as consent to every device the host lends.** The host could add a
  phone the client's human never saw, and the client's agents would reach it.
- **A `remote.*` capability family, or a `host` parameter.** Not "unchanged for the
  caller", and every device tool's MCP schema grows (ADR-0036).
- **Transcoding on either side.** m2p's constraint 1 keeps the device's H.264 unchanged;
  H.265 or AV1 waits for a measured bandwidth limit.
- **Proxying adb.** `beyond.md` and ADR-0097 refuse adb on the network.
- **The browser connecting to the host.** ADR-0097 §3.
