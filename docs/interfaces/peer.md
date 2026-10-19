# Peers: lending devices between Droidyards

A Droidyard may lend the physical devices plugged into its machine to other Droidyards
it has paired with, over a private network both already share (ADR-0097). This document
says how the pieces fit; the rules are the ADRs and the code (`internal/hosting`,
`adapters/peer`, `internal/domain/device/remote*.go`, `lease.go`).

```text
 client (yours)                                   host (a colleague's)
 browser ─┐                                       ┌─ its own UI and CLI (never refused)
 agent ───┼─ daemon ── link: mTLS, pinned ────────┼─ daemon ── adb ── phone
 CLI ─────┘   device.list: qa/<serial>            │   lends <serial> to named peers
              no adb for a remote id              └─ lease: one peer at a time
```

## The two roles

- **Host.** `hosting.listen` binds a peer listener, an IP and a port, separate from the UI's
  loopback listener. `hosting.peers` names whom it admits, by certificate fingerprint;
  `hosting.lend` which serials go to whom. It lends only physical devices its own physical
  policy admits (ADR-0098, ADR-0099).
- **Client.** `remotes` names each host, its address and fingerprint. A lent device appears
  as `<remote>/<serial>` and is used once the client's own `devices.allowPhysical` and
  allowlist admit that id; its calls are relayed, and what comes back is untrusted
  (ADR-0100).

Pairing is by hand: each side runs `droidyard id` and the other pastes the fingerprint
(`droidyard host admit`, `droidyard remote add`). `listen` and `remotes` are read at daemon
start; `peers` and `lend` at each request, so a revocation closes the peer's sockets within
2 s.

## The peer port

TLS 1.3 only, a client certificate required and pinned at the handshake, at each request
and every 2 s on open sockets. Four routes, nothing else: `/peer/v1/ws` (the capability
envelope, as the UI's WebSocket), `/peer/v1/screen` (a stream the same peer opened),
`/peer/v1/upload` (one APK slot per peer, at most 512 MiB) and `/peer/v1/artifacts/{id}`
(artifacts the same peer produced). A request with `Origin` is refused: no browser speaks to
this port.

## What a peer may do

Only the capabilities marked `Lendable` (ADR-0099 §2): the device reads, actions, input,
streams and `device.installUpload`, `artifact.read` of its own artifacts, and waits and
cancels of its own operations (ADR-0101). Everything else is `REQUEST_NOT_ALLOWED_HERE`. A
peer's call of any kind leases the device (ADR-0104); a peer's link holds up to four Logcat
streams per device (ADR-0106). Every call is an operation on the host, its trace line
ending `peer=<name>`.

## What is not here

The browser or an MCP client reaching another machine's daemon; a relay or overlay for
internet reach; pairing from the UI; a client handing a lease back early. Open items are in
the [register](../roadmap/known-limits.md).
