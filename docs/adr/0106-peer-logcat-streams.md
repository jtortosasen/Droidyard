# ADR-0106: A peer's link holds up to four Logcat streams per lent device

- Status: Accepted
- Date: 2026-10-19
- Supersedes: ADR-0041 §6 (in part: a stream of a peer's link session is not replaced by that session's next stream of the device; the fifth replaces the oldest)
- Relates to: ADR-0098 §4, ADR-0099, ADR-0100 §3; plan m2t step 5
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated. The owner's use case (2026-10-18): a remote phone used as if local, by the UI and by agents at once.

## Context

ADR-0041 §6 ends a session's Logcat stream when the same session starts another stream of
the same device (`replaced`). On a host, every call of a paired client arrives on one link
session (ADR-0098 §4), so two of the client's own sessions watching one lent phone (two
tabs; a tab and a WebSocket tool) ended each other's streams: found in plan m2t step 5.

## Decision

1. **On the host**, a stream opened by a peer's link session is not replaced by that
   session's next stream of the same device. One link session holds up to **four** streams
   per lent device (a constant); a fifth replaces that session's oldest on the device
   (`stopped: replaced`), and the new logcat child starts only once the replaced one has
   ended (and any stream it was itself waiting on), so the host's children for that link
   and device stay at four. A new stream that cannot start so within 5 s is refused and
   starts nothing; one replaced before it started answers with its subscription and then
   `stopped: replaced`.
2. **On the client**, ADR-0041 holds unchanged for its own sessions: a client session's
   second stream of a remote id replaces its first. Each client session's stream is its
   own stream on the host, so the cap separates distinct client sessions.
3. A local session keeps ADR-0041 §6.

## Consequences

- `device.watchLogs`' description and `replaced`'s text stay true for every caller of the
  client; on the host, `replaced` for a peer can mean the link's fifth stream.
- A client with five sessions watching one remote device loses the oldest's stream.
- The rule lives in `watchlogs.go` (`peerStreamsMax`) and its tests; no area document
  restates it.

## Alternatives considered

- **One shared host stream per remote device, fanned out on the client** (as the relayed
  screen is). Streams differ in `tail`, `since` and `applicationId`, so one host stream
  cannot serve them; the union would move filtering to the client and drop the host's uid
  match.
- **A client-supplied key per stream on the peer protocol.** Unbounded streams unless capped
  anyway, and a new field in an internal protocol for what a cap already gives.
- **No cap.** A peer could start unbounded logcat children on the host.
