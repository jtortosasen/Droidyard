# Peers: lending devices between Droidyards

A Droidyard may lend the physical devices plugged into its machine to other Droidyards
it has paired with, over a private network both already share: a VPN or the LAN (ADR-0097,
ADR-0109). This document
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

- **Host.** A peer listener, separate from the UI's loopback one, binds `hosting.listen`
  when it is set; otherwise, while something is lent and `hosting.on` is set, every private
  address of the machine on `hosting.port`: a VPN's (a tunnel interface) and the LAN's,
  never a public one. `hosting.peers` names whom it admits, by certificate fingerprint;
  `hosting.lend`, and a foreground share in memory, which serials go to whom. It lends only physical devices its own physical
  policy admits (ADR-0098, ADR-0099, ADR-0109).
- **Client.** `remotes` names each host, its addresses (tagged `vpn` or `lan`, its own
  subnet's tried first) and fingerprint. A lent device appears
  as `<remote>/<serial>` and is used once the client's own `devices.allowPhysical` and
  allowlist admit that id; its calls are relayed, and what comes back is untrusted
  (ADR-0100).

## Sharing: one command, one paste

`droidyard share [<serial>]` on the host, with no serial, lists its physical devices and takes
the only one; it admits the device in the host's physical policy, opens the listener and prints one line,
`droidyard join dy1.…`: the addresses, port, the host's fingerprint and a 128-bit secret.
In the foreground the share lasts while the command runs (Ctrl-C ends it); with `-d` until
`droidyard unshare`, across restarts. On the client, `droidyard join <code>` pins the host,
sends the secret inside that TLS, and writes the remote and `<remote>/<serial>` to its own
policy. The code works once and never expires; the host keeps only its hash; another person
needs another `share` (ADR-0107, ADR-0108).

The advanced path pairs by hand: each side runs `droidyard id` and the other pastes the
fingerprint (`droidyard host admit`, `droidyard remote add`). Every key is live: the
listeners and links follow the configuration and the interfaces every 2 s, and a revocation
closes the peer's sockets within 2 s (ADR-0109).

## The peer port

TLS 1.3 only, a client certificate required and pinned at the handshake, at each request
and every 2 s on open sockets. One exception: a hello offering ALPN `droidyard-join/1`
may present an unpaired certificate while a code is unused, and that connection is served
one `POST /peer/v1/join` and closed. Otherwise four routes, nothing else: `/peer/v1/ws` (the capability
envelope, as the UI's WebSocket), `/peer/v1/screen` (a stream the same peer opened),
`/peer/v1/upload` (one APK slot per peer, at most 512 MiB) and `/peer/v1/artifacts/{id}`
(artifacts the same peer produced). A request with `Origin` is refused: no browser speaks to
this port.

## What a peer may do

Only the capabilities marked `Lendable` (ADR-0099 §2): the device reads, actions, input,
streams and `device.installUpload`, `artifact.read` of its own artifacts, and waits and
cancels of its own operations (ADR-0101). Everything else is `REQUEST_NOT_ALLOWED_HERE`. A
peer's call of any kind leases the device (ADR-0104); a peer's link holds up to four Logcat
streams and four UI events streams per device (ADR-0106, ADR-0115). A peer records a lent device on the host, sees only its own
recordings, and gets its own finished timelines from the artifacts route. Every call is an operation on the host, its trace line
ending `peer=<name>`.

## What is not here

The browser or an MCP client reaching another machine's daemon; a relay or overlay for
internet reach; sharing and joining from the UI; a client handing a lease back early. Open items are in
the [register](../roadmap/known-limits.md).
