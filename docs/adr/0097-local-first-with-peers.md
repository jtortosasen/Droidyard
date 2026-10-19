# ADR-0097: Local-first, with peers that lend devices

- Status: Accepted
- Date: 2026-10-18
- Relates to: ADR-0015, ADR-0098, ADR-0099; [`vision.md`](../vision/vision.md) §Non-goals; [`mvp.md`](../roadmap/mvp.md) §Out of scope, §Beyond the MVP; [`beyond.md`](../vision/beyond.md) §C4, §Decisions it reopens; `http-websocket.md` §Security baseline; plan m2t Q1
- Decided by the owner, 2026-10-18: C4's first slice, lending physical devices to a paired Droidyard over the company VPN, and reopening the two ratified lines it meets. The wording is Delegated (plan m2t Q1, reviewed in two rounds).

## Context

Three lines keep Droidyard on one machine: "A cloud service. Droidyard is local-first"
(`vision.md` §Non-goals); "LAN exposure and access control … remote access … after v1",
ratified 2026-09-29, and "multi-user or cloud use" (`mvp.md` §Out of scope).
`http-websocket.md` §Security baseline rule 1 refuses a non-loopback bind until "its own
ADR". `beyond.md` §Decisions it reopens names C4 as the work that meets them.

The owner's use case (2026-10-18): reach phones of manufacturers a person cannot own,
held by a colleague, and drive them as if they were plugged in: install a local build,
launch it, read its logs, watch its screen and UI tree, and act on it. That needs one
daemon to serve another across the network; it does not need a service, an account or
the browser reaching another machine.

## Decision

1. **Droidyard stays local-first** and is never a service anyone operates: no cloud,
   account, relay or overlay network.
2. **A daemon whose human enables hosting** serves its devices' capabilities, and
   nothing else, to **Droidyards it has paired**, over a private network the users
   already share (the company VPN). Several people may then use one host's devices, one
   at a time per device (plan m2t Q3 §5).
3. **In this decision the browser and MCP clients speak only to their own machine's
   daemon**: the HTTP listener stays loopback with its per-launch token, MCP stays stdio.
   LAN access to the UI stays later (`mvp.md` §Beyond the MVP), for an ADR of its own.

## Consequences

- The three lines change for devices: `vision.md` §Non-goals and `mvp.md` §Out of scope
  name this ADR (plan m2t step 9). `http-websocket.md` rule 1 is unchanged: the peer
  listener is a separate listener (ADR-0098), not the HTTP API on the LAN.
- The register's "LAN exposure and access control" row narrows to the UI and MCP on the
  LAN.
- Internet reach (a relay or an overlay) is a later decision; reopening it reopens §1.

## Alternatives considered

- **A relay or overlay for internet reach.** The owner's use case is inside the company
  VPN; a relay is a service someone must operate, which §1 refuses. It wins when hosts
  and clients share no private network.
- **Raw adb on the network** (`adb tcpip`, a forwarded adb server). `beyond.md` rejects
  it: adb grants full control with no authentication, does not cross NAT and breaks on
  version skew.
- **The browser reaching the host's daemon.** A second authentication model in the
  browser, Origin and CORS across a network, and the token on another machine, for a use
  case the client's own daemon serves. This rejects it for device lending only, not LAN
  access to the UI for its own sake.
