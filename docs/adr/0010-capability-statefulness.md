# ADR-0010: Statefulness is declared per capability

- Status: Accepted
- Date: 2026-09-27
- Amended: 2026-09-29. What changed: what a session is on each transport (plain HTTP
  has none; the WebSocket is the UI's session), and where session state lives — in the
  session, not the workspace; operations belong to the daemon
  ([ADR-0019](0019-process-topology-daemon-and-shims.md)).

## Context

`mobile-mcp` runs its HTTP transport **statelessly** (a fresh server per request).
As a result, capabilities that need cross-request state — screen recording, and any
long-lived operation — silently fail over HTTP. That is a trap we must not inherit:
several of our capabilities (logcat follow, screen stream, build follow) are
inherently stateful, while many others (queries) are not.

## Decision

Every capability **declares its statefulness**:

- **stateless** — pure query/command, no session required. Long-running work is
  stateless too: operations belong to the daemon, and any session — or none — can wait
  on them.
- **stateful** — needs session-owned state across calls (e.g. unsubscribing a
  subscription).
- **streaming** — produces an ongoing event stream for its session (a stateful case).

Discovery (`system.capabilities`) advertises the classification. Adapters must:

- keep a session for stateful/streaming capabilities. A session is one connection
  ([ADR-0019](0019-process-topology-daemon-and-shims.md) §Sessions): a stdio shim (one
  MCP session), one `droidyard call`, or one WebSocket. **Plain HTTP requests have no
  session**; the web UI's session is its WebSocket;
- **reject** a stateful/streaming capability invoked without a session with
  `SESSION_REQUIRED`, rather than silently degrading.

## Consequences

- No silent breakage of stateful features over stateless transports.
- The UI and agents can discover which capabilities require a session.
- The HTTP adapter needs no session tokens (its bearer token authenticates; it does
  not make a session): sessions are WebSocket connections, and plain requests are
  sessionless by construction.
- Session-owned state (subscriptions, watches) is released when the connection ends;
  operations are not session state and survive it.

## Alternatives considered

- **Treat everything as stateful** (no stateless HTTP) — loses the simplicity of
  stateless queries.
- **Hide the distinction** — exactly the mobile-mcp trap. Rejected.
- **HTTP session tokens** (the original wording) — a second session mechanism beside
  the WebSocket, for no capability that needs it. Rejected.
