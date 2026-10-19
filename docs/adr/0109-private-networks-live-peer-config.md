# ADR-0109: Private networks and live peer configuration

- Status: Accepted
- Date: 2026-10-19
- Supersedes: ADR-0097 §Decision (in part: the network may be the VPN or the LAN); ADR-0098 §3 (in part: the listener binds the private addresses when `hosting.listen` is unset), §4 (in part: a remote has an address list), §6 (in part: `listen` and `remotes` are live)
- Relates to: ADR-0107, ADR-0108; `docs/research/orca-2026-10-remote.md` §3.2; plan m2u Q3
- Decided by the owner, 2026-10-19: listen on the VPN and the LAN at once, never everywhere; the client prefers the LAN for the screen's latency and falls back to the VPN; a fixed port; follow address changes; no restart. The details were drafted by the planner and decided by the main session under the owner's standing instruction, Delegated (plan m2u Q3, reviewed in two rounds).

## Context

ADR-0097 lends over "a private network the users already share (the company VPN)";
ADR-0098 binds one `hosting.listen` literal, dials one `remotes` address, and reads both
at start. A one-command share (ADR-0108) cannot ask for an address or a restart. Orca
binds `0.0.0.0`, public interfaces included.

## Decision

1. **Where**: while the host lends anything, `hosting.on` is set and `hosting.listen` is
   unset, the listener binds every private IPv4 address of the machine: 100.64.0.0/10 on
   a tunnel interface (point-to-point or `utun*`: Tailscale, Headscale; the same range on
   another interface is carrier-grade NAT and left out) and RFC 1918. Never unspecified,
   public, link-local or multicast. `hosting.on` (`private`, `vpn`, `lan`, or one IP
   literal) restricts it; the first `share` writes it. `hosting.listen` keeps its meaning
   and wins. A 2t configuration with neither binds nothing.
2. **Port**: the first bind takes 7318, or a free port when it is busy, and writes it to
   `hosting.port`. Once written it is never replaced automatically; a busy port is a
   reported bind error, retried.
3. **Live**: the listener set follows the configuration and the interfaces' addresses,
   checked every 2 s and at once after a share. One address's listener closing ends only
   its own sockets. `remotes` is reconciled the same way, a changed address list updated
   in place.
4. **Addresses**: a remote has `address` and an optional `addresses` (at most 8); each
   dial tries those in one of the client's own subnets first, then 100.64.0.0/10, then the
   rest.

## Consequences

- A LAN listener exposes a TLS handshake: only a pinned certificate or a single-use code
  (ADR-0107) gets past it. `--on vpn` keeps a host off an untrusted LAN.
- `system.health` `hosting` gains `listening` and per-address `errors`; `policy.get` and
  `update` gain `hosting.on`, `hosting.port` and `remotes[].addresses`; one minor API
  version.
- `http-websocket.md` §Security baseline and `docs/interfaces/peer.md` follow.
- Register: IPv6 private addresses; a host telling its paired clients a new address.

## Alternatives considered

- **`0.0.0.0`**: includes public interfaces (`orca-2026-10-remote.md` §3.2).
- **The VPN only**: the owner wants the LAN for the screen's latency.
- **Discovery by mDNS**: a dependency and multicast, for what one paste carries.
- **A random port per share**: paired clients would lose the host.
- **IPv6 ULA**: both networks here carry IPv4. It would win on an IPv6-only network.
- **The host pushing new addresses over the link**: the VPN address is stable. It would
  win if hosts moved between networks often with no VPN.
