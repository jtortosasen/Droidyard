# ADR-0116: The client adopts only a remote recording whose start it lost

- Status: Accepted
- Date: 2026-10-21
- Supersedes: ADR-0115 §Decision 5 (in part: which recordings the client adopts at link up, and the host's delete after the copy)
- Relates to: ADR-0113; ADR-0115 §Decision 3; plan m2x step 10
- Delegated (plan m2x step 10, from the implementer's departure and the Opus verifier's judgement; the main session under the owner's standing instruction to apply the recommended option).

## Context

ADR-0115 §5 has the client adopt, at link up, any ended recording of its own on the host that it
holds no record of. Read literally, that also brings back a recording the user deleted on the
client whose delete on the host failed, and it adds one relayed listing to every link up of every
remote, including clients that never record.

## Decision

1. **Only a lost start is adopted.** A relayed `device.recordUi` whose answer this daemon lost
   (`DEVICE_DISCONNECTED`, or the call cancelled) leaves a hint in memory: the device, the
   caller and the start time. At link up the client lists its recordings on the host and adopts
   the one that matches a hint, by device and a start no earlier than 10 minutes before the
   hint (the host's clock may lag). The hint lasts until a host listing settles it, however long
   the link stays down. A match still running is waited for, at most the longest recording
   (3600 s) plus 5 minutes from the first listing.
2. **The host's delete after the copy is one attempt.** A failed delete is logged. The host's
   per-peer retention (10 recordings, 1 GiB, ADR-0115 §3) removes the copy later.

## Consequences

- A recording deleted on the client never comes back.
- A client that crashes or restarts between relaying a start and saving its record loses the hint.
  The host's recording is then orphaned: it ends at its bound or with the lease, and stays until
  the host's per-peer sweep. It holds no device and does not block the peer.
- A host copy whose delete failed also waits for that sweep.

## Alternatives considered

- **Adopt every unknown ended recording (ADR-0115 §5 as written).** It brings back deleted
  recordings, and its listing runs at every link up for every client.
- **A persisted hint.** It covers the crash window, but it needs a second record format and its
  own recovery for a case the host's sweep already bounds.
- **Retrying the host's delete.** Another pending state per record, for a copy the sweep removes.
