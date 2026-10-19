# ADR-0098: Peers: mutual TLS between daemons, pinned identities, pairing by hand

- Status: Accepted
- Date: 2026-10-18
- Supersedes: ADR-0032 §5 (in part: `policy.update` also patches `hosting` and `remotes`); ADR-0019 §Processes (in part: §7's client kinds gain `peer`); ADR-0048 §1 (in part: a peer's `session.name` is its configured name)
- Relates to: ADR-0015 §6, ADR-0021 §Layout, ADR-0024, ADR-0028, ADR-0031, ADR-0097, ADR-0099; plan m2t Q2
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated (plan m2t Q2, reviewed in two rounds). The owner asked for mutual TLS with pairing, 2026-10-18.

## Context

ADR-0097 lets a host serve its devices to paired Droidyards. Nothing in the daemon is
networked today: the HTTP adapter binds loopback (`CheckLoopback`) with a per-launch
token, there is no TLS, no daemon identity and no outbound connection. Pairing must admit
one person's daemon, not anyone on the VPN, and survive restarts on both sides.

## Decision

1. **Identity.** Each daemon makes, at its first start, an Ed25519 key and a self-signed
   certificate in `<state>/identity/` (0700, the key 0600). Its **fingerprint** is the
   hex SHA-256 of the certificate's SubjectPublicKeyInfo; `droidyard id` prints it. Lost
   state is a new identity, paired again.
2. **Pairing is mutual pinning**, exchanged out of band (a chat): the host's human adds
   `{name, fingerprint}` to `hosting.peers`; the client's human adds
   `{name, address, fingerprint}` to `remotes`. No CA, no secret, no pairing mode. Names
   and fingerprints are unique within each list.
3. **The listener** binds `hosting.listen`, an IP literal and port. An unspecified
   address or a host name fails validation (ADR-0032 §4: the file is unusable, the daemon
   runs); loopback is allowed. A bind that fails leaves the daemon running without the
   listener, reported in `system.health` and retried every 30 s. TLS 1.3 only, a client
   certificate required and pinned against `peers` at each handshake, each request, and
   every 2 s on open sockets, so a revoked peer loses its links within 2 s. It serves only
   `/peer/v1/ws`, `/peer/v1/screen`, `/peer/v1/upload` and `/peer/v1/artifacts/{id}`; a
   request carrying `Origin` is refused.
4. **The link.** The client dials each `remotes` entry at start, pins its fingerprint,
   sends `{peerProtocol: 1, api}`, and reconnects with backoff (1 s to 30 s). Equal
   `peerProtocol` is required; a different API version is a warning. The peer protocol is
   internal (both ends are this code) and is not in `schema/capabilities.json`.
5. **Client kind `peer`.** A peer's operations carry its configured name (from
   `hosting.peers`) as the session name, not a name the peer sends.
6. **Configuration.** `hosting {listen, peers, lend}` and `remotes` are policy
   (ADR-0015 §6): human channels only, `policy.update` included. `listen` and `remotes`
   are read at start; `peers` and `lend` live, by ADR-0032 §6's re-read.
7. **CLI**: `droidyard id`, `droidyard host …` (admit, revoke, lend, unlend, list) and
   `droidyard remote …` (add, remove, list) join ADR-0024's subcommands; each writes
   through `policy.update`.
8. **No new module dependency**: `crypto/tls`, `crypto/ed25519`, `crypto/x509`, and
   coder/websocket (ADR-0028).

## Consequences

- ADR-0021 §Layout gains `identity/` and `hosting/uploads/` (ADR-0099 §3).
- `system.health` gains `identity`, `hosting` and `remotes`; `policy.get`/`update` gain
  `hosting` and `remotes`; the client enums gain `peer` (one minor API version).
- `http-websocket.md` §Security baseline names the peer listener beside the loopback one;
  `config.md` (or the area document for policy) describes the two sections.
- Unverified: whether the macOS firewall prompts for each ad-hoc-signed build accepting
  connections (the owner's trial).

## Alternatives considered

- **A one-time pairing code** (SPAKE or a short secret). One copy-paste fewer, for a
  secret on the wire and a pairing state machine on both sides; fingerprints are pasted
  once per pair.
- **A private CA.** A PKI with issuance and revocation for what is, in practice, a few
  machines; pinning revokes by deleting one line.
- **A bearer token.** No identity of its own, and it leaks the way the HTTP token's
  register row says (command lines, logs).
- **SSH tunnels.** They grant the peer shell access to the host, which ADR-0097 §2
  refuses: a host serves capabilities and nothing else.
- **The system trust store.** It cannot pin a self-signed identity, and trusting any
  certificate a corporate CA signs admits anyone on the VPN.
- **gRPC.** A dependency and generated code for a protocol both ends already speak
  (the capability envelope over WebSocket, ADR-0028).
