# ADR-0107: Pairing by a single-use code

- Status: Accepted
- Date: 2026-10-19
- Supersedes: ADR-0098 §2 (in part: a single-use code pairs too); ADR-0098 §3 (in part: the join exchange, an unpinned handshake that reaches one route)
- Relates to: ADR-0097, ADR-0099, ADR-0100 §4, ADR-0108, ADR-0109; `docs/research/orca-2026-10-remote.md` §2; plan m2u Q1
- Decided by the owner, 2026-10-19: one command on the host prints one line, pasted on the client, and nothing is written by hand; the line does not expire with time and serves one person. The format, the exchange and its bounds were drafted by the planner and decided by the main session under the owner's standing instruction, Delegated (plan m2u Q1, reviewed in two rounds).

## Context

2t pairs by mutual pinning (ADR-0098 §2): each human runs `droidyard id`, pastes the
other's fingerprint (`host admit`, `remote add`), then edits the physical policy and
restarts the daemon. The owner found it too long for its purpose, reaching a phone
another person does not have. ADR-0098 rejected a one-time code as "a secret on the wire
and a pairing state machine". Orca's link (`orca-2026-10-remote.md` §2.3) is the
opposite risk: a reusable bearer that never expires and is stored in clear.

## Decision

1. **The code**: `dy1.` and unpadded base64url of a version byte, the host's addresses
   (at most 8, each tagged `vpn` or `lan`, ADR-0109 §4), its peer port, its certificate fingerprint (32 bytes) and a 128-bit random
   secret. No time expiry. Single use. At most 8 unused per share (a ninth voids the
   oldest); all void when the share stops (ADR-0108 §4).
2. **The host keeps only the SHA-256 of each secret**, compared in constant time and
   removed under one lock when consumed. The whole code exists as `hosting.share`'s result
   and `hosting.join`'s parameter, and is never logged.
3. **The exchange**: `POST /peer/v1/join` on the peer port. A TLS hello offering ALPN
   `droidyard-join/1` may present an unpinned certificate while a code is pending; that
   connection is served one join request through `TLSNextProto` and closed, and reaches
   nothing else. Every other unpinned hello is refused at the handshake, as before. A
   paired peer may join too (a new share). The client pins the code's fingerprint before
   it sends the secret, so the secret travels only inside TLS to that host. The peer
   admitted is the certificate the client presented in that handshake.
4. **Bounds**: at most 4 join connections at once, counted from the hello and freed when
   the connection closes; 10 s each; a 4 KiB body. No lockout: 2^128 secrets. Each refused
   join is a `daemon.log` line.
5. **Names**: each side names the other from its host name (lowercase, cut to the peer
   name pattern, `-2`, `-3`… when taken). A fingerprint already known keeps its name. No
   name is asked.
6. **Errors**: a new prefix `HOSTING`: `HOSTING_JOIN_FAILED` (`details.reason`:
   `unreachable`, `notThisHost`, `refused`) and `HOSTING_UNAVAILABLE`.

## Consequences

- **The code is a bearer credential until used.** It does not expire, a detached share
  keeps its hash across restarts, and the first presenter wins. The intended client then
  sees `refused`, the host's log names who joined, and `droidyard host revoke` undoes it.
- A machine on the same network can hold the 4 join slots in turn and delay a join, and
  can read the host's certificate; it cannot pass the handshake to anything else.
- The host's answer is untrusted on the client (ADR-0100 §4): its name is cleaned and its
  serial checked before anything is written.
- One minor API version: two error codes.
- `docs/interfaces/peer.md` describes pairing by share and join, and the fifth route.

## Alternatives considered

- **A reusable invitation** (Orca): valid for anyone who sees it until revoked; a
  screenshot is enough.
- **A code that expires after minutes**: the owner rejected it ("no me interesa"); the
  share's own lifetime bounds it.
- **A non-secret invitation** (the register's Orca idea): the host's human must then
  admit the client's fingerprint, a second paste back.
- **SPAKE2 or another PAKE**: a dependency or our own cryptography, for what pinning the
  host's fingerprint from the code already gives. It would win if the code had to be short
  enough to read aloud.
- **A separate join port**: a second listener and firewall rule for one request.
- **Any unpinned certificate while a code is pending, without ALPN**: every other
  connection would lose 2t's refusal at the handshake for as long as a code is pending.
- **A slow hash of the secret**: a 128-bit random secret has nothing to stretch.
