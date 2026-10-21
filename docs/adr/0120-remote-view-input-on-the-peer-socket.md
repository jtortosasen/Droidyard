# ADR-0120: A remote device's view sends its input on the peer screen socket

- Status: Accepted
- Date: 2026-10-21
- Supersedes: ADR-0100 §3 (in part: input from a remote device's view is no longer the host's capability call), §5 (in part: a relayed source injects)
- Relates to: ADR-0117 §1–§5, ADR-0094 §5, ADR-0104 §2; plan m2y step 5
- Decided by the main session under the owner's standing instruction, Delegated: it records for ADR-0100 what ADR-0117 §1 and §3 already decided ("the same wire runs from a client daemon to its host on `/peer/v1/screen`"), which ADR-0117 named under Relates to instead of Supersedes.

## Context

ADR-0100 §3 made a remote device's input the host's capability call, and §5 said a relayed
source never injects. ADR-0117 moved the live view's input onto the view's own socket, for a
remote device too, and m2y step 5 built it: measured on a phone lent through a loopback pair,
the client's send reaches the host's control socket in p50 1.9 ms, against 71 ms as a call.

## Decision

1. A remote device's view sends its input as ADR-0117 §3's frames on its `/peer/v1/screen`
   socket when the host's `device.watchScreen` result granted `control`; the host's grant is
   that call's authorization and lease (ADR-0104 §2). An older host's result lacks the field,
   and the view then takes no input.
2. The relayed source keeps the client's finger as a local source does, and the host keeps
   the device's finger with the peer as a peer holder (ADR-0117 §4). A peer's events take at
   most 128 of a source's 256 queue entries, past which only that peer's socket is closed, and
   a peer's key or scroll is dropped while a local holder holds the finger (ADR-0094 §5).
3. A finger held when the client's view ends is lifted by the host: its release of the
   stream writes ACTION_CANCEL when the socket closes (ADR-0117 §5). The client sends its own
   cancel frame only while the source stays open for another view.

## Consequences

- `device.tap`, `device.swipe`, `device.pressKey` and `device.scroll` on a remote id stay
  relayed calls for the CLI and scripts.
- Documents: `peer.md` §The peer port (step 5).

## Alternatives considered

- **Keep the host's call for a remote view**: the 71 ms call path and a drag delivered only at
  the release, on exactly the link where the owner asked for speed.
- **A cancel frame from the client at every end**: the client's read context closes the socket
  as the last view ends, so the frame cannot be written then; the host's release already lifts
  the finger, and a second path would only race it.
- **One shared queue bound for local and peer events**: a peer filling it would get the host's
  own view closed (1008), against ADR-0094 §5.
