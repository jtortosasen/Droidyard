# ADR-0010: Statefulness is declared per capability

- Status: Accepted
- Date: 2026-09-27

## Context

`mobile-mcp` runs its HTTP transport **statelessly** (a fresh server per request).
As a result, capabilities that need cross-request state — screen recording, and any
long-lived operation — silently fail over HTTP. That is a trap we must not inherit:
several of our capabilities (logcat follow, screen stream, build follow) are
inherently stateful, while many others (queries) are not.

## Decision

Every capability **declares its statefulness**:

- **stateless** — pure query/command, no session required.
- **stateful** — needs a live workspace/operation context across calls.
- **streaming** — produces an ongoing event stream (a stateful case).

Discovery (`system.capabilities`) advertises the classification. Adapters must:

- keep a session for stateful/streaming capabilities (stdio is naturally stateful;
  HTTP requires an explicit session token; the web UI uses WebSocket);
- **reject** a stateful/streaming capability invoked without a session, rather than
  silently degrading.

## Consequences

- No silent breakage of stateful features over stateless transports.
- The UI and agents can discover which capabilities require a session.
- HTTP adapters must implement session management (token/connection scoped).
- The workspace model already provides the natural home for this state.

## Alternatives considered

- **Treat everything as stateful** (no stateless HTTP) — loses the simplicity of
  stateless queries.
- **Hide the distinction** — exactly the mobile-mcp trap. Rejected.
