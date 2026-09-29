# ADR-0001: Core runtime implemented in Go

- Status: Accepted
- Date: 2026-09-27

## Context

The core (`mydev`) is a persistent local process that must:

- spawn and supervise external processes: its JVM sidecars (the Gradle Tooling API
  sidecar, later the Kotlin LSP), per-operation `adb`/`git` commands, `logcat` and
  `screenrecord` streams, and watchers;
- start or use processes that belong to other tools without owning them: the `adb`
  server, emulators, and the Gradle daemon, which is Gradle's own
  ([process-lifecycle](../architecture/process-lifecycle.md));
- speak JSON-RPC over stdio to its sidecars,
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
- Must add an interop boundary for anything JVM-only, as a sidecar and never embedded
  in the core. The Gradle Tooling API is the first
  ([ADR-0017](0017-gradle-execution-strategy.md)).
- The parsers for ADB and Logcat output, and the console error extractor for what the
  Tooling API does not structure (Kotlin, AAPT2 and similar compiler output;
  [ADR-0022](0022-build-error-extraction.md)), are written from scratch in Go. They are
  small, well defined and fixture-tested.
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
