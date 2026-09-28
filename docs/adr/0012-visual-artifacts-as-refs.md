# ADR-0012: Visual artifacts as in-memory bytes with opaque references

- Status: Accepted
- Date: 2026-09-27

## Context

`codex-android-mcp` writes each screenshot to a temp file, base64-encodes it, then
deletes it, leaking the filesystem `path` into the text content for the user.
`dsh-android` reaches a similar end with two duplicate screenshot stores plus a
route counter sharing one directory. Temp directories can be reaped by the OS, and
paths leak host internals.

## Decision

The core treats visual artifacts (screenshots, streamed frames) as **in-memory
bytes** identified by an **opaque, scoped reference** with a TTL. A capability
returns the reference plus metadata (dimensions, format, byte size), not a
filesystem path.

- One component in the core owns the artifact store.
- Image delivery to a consumer (MCP image content, web UI) is an **adapter**
  concern that resolves the reference to bytes on demand.
- Eviction is bounded by count, bytes and TTL.

## Consequences

- No temp-dir reaping failures and no host-path leakage.
- Works uniformly over WebSocket and MCP (base64 for image content).
- Requires a bounded memory budget and an eviction policy (aligns with the lean
  goal).
- For very large artifacts, spilling to a core-owned durable cache is allowed, but
  consumers still receive a reference, never a raw path.

## Alternatives considered

- **Path-based plumbing** — leaks host internals, fragile. Rejected.
- **Host-specific hosting (e.g. a webserver route)** — couples the artifact to one
  adapter. Rejected.
