# ADR-0028: WebSocket library — coder/websocket

- Status: Accepted
- Date: 2026-10-03
- Relates to: ADR-0006, ADR-0010, ADR-0019 §5, `docs/interfaces/http-websocket.md` §Security baseline

## Context

The WebSocket session is the web UI's session ([ADR-0010](0010-capability-statefulness.md))
and, from milestone 1d, carries the `events.*` subscriptions. The Go standard library
has no WebSocket server, so this adds a public dependency on the daemon's edge and is
recorded here rather than taken silently.

Candidates read on 2026-10-03 (level 2: documentation and source, nothing compiled):
`github.com/coder/websocket` v1.8.15 — ISC licence, and its `go.mod` requires no
third-party module; `github.com/gorilla/websocket`; and `golang.org/x/net/websocket`,
which is frozen. The choice was taken under the owner's standing delegation of
2026-10-03 ("the recommended option"), and the milestone's checks settle the details.

## Decision

Use `github.com/coder/websocket` **v1.8.15**, pinned in `go.mod`.

- It must not replace our own security fence: `Origin` is checked before the upgrade by
  our wrapper ([`http-websocket.md`](../interfaces/http-websocket.md) §Security baseline),
  and the library's own origin check is relaxed through `OriginPatterns` for exactly the
  `Host`/`Origin` pairs our rules allow (for example `Origin: http://localhost:<p>` on
  `Host: 127.0.0.1:<p>`).
- Read limit 1 MiB, matching the request-body cap of `POST /v1/call`.
- The token is the first frame within 5 s or the connection closes with code `4401`;
  fragmentation, close codes and ping/pong are held to the contract by the milestone's
  checks.

## Consequences

- One new module requirement, ISC-licensed, pulling nothing else in.
- The upgrade's wire details follow the library; `docs/interfaces/http-websocket.md` is
  the contract, and the tests hold it.
- If the library fails a rule, the wrapper around it can be replaced without touching
  the session model or the event bus.
- Upgrades are deliberate, like every dependency in this project.

## Alternatives considered

- **`github.com/gorilla/websocket`.** Mature and widely used; it brings its own origin
  checking and a larger API surface, and its maintenance revival is younger than
  coder's. Wins only if coder fails a required behaviour.
- **`golang.org/x/net/websocket`.** Frozen; not recommended for new code.
- **Hand-rolled framing.** Masking, fragmentation and the close handshake are exactly
  the security-sensitive code not to write.
- **Server-sent events instead.** Not bidirectional, and not the reviewed
  [`http-websocket.md`](../interfaces/http-websocket.md) contract.
