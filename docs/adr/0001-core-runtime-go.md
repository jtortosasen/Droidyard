# ADR-0001: Core runtime implemented in Go

- Status: Accepted
- Date: 2026-09-27

## Context

The core (`mydev`) is a persistent local process that must:

- spawn and supervise many external processes (adb server, Gradle daemon, Kotlin
  LSP, emulators, screenrecord, watchers),
- speak JSON-RPC over stdio to the Kotlin LSP,
- serve HTTP and WebSocket to the UI,
- serve MCP to agents,
- parse large volumes of tool output into structured data,
- watch the filesystem,
- stay small in memory and fast to start.

The original sketch leaned toward Kotlin/Native, mainly to keep a single Kotlin
language. The key realisation is that the only strong reason to keep a JVM was
the Kotlin LSP, and the LSP runs as an **external process** reached over
JSON-RPC. Therefore the core does not need to be Kotlin or JVM-based to use it.

## Decision

Implement the core in **Go**.

## Consequences

- Single static binary, no runtime to install, easy to ship and start.
- Strong standard library for process management, HTTP, sockets and JSON.
- Mature libraries for WebSocket, file watching and MCP.
- Must add an interop boundary for anything JVM-only (e.g. Gradle Tooling API)
  as an optional sidecar, not embedded in the core.
- The parsers for ADB / Gradle / Logcat output are written from scratch in
  Go (they are small and well-defined).
- Go's GC and type system are a deliberate trade against Rust's compile-time
  guarantees; acceptable for an I/O- and orchestration-bound process.

## Alternatives considered

- **Kotlin/Native** — one language with the Android ecosystem, but immature
  HTTP/WS server, file watching and concurrency ecosystem for exactly the core's
  workload. Rejected.
- **Rust** — strongest safety and control; slower to build and more code for
  protocol/JSON modelling. Viable but not chosen for v1 velocity.
- **Kotlin/JVM** — best Gradle/LSP integration, but adds a full JVM to the
  baseline footprint and startup time, contradicting the lean goal.
