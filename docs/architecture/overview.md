# Architecture Overview

The high-level shape of the system. The detail lives in the architecture, interface
and subsystem documents linked from [`../README.md`](../README.md).

## Context

```text
   +-------------------+     +--------------------------+     +-------------+
   |  Human developer  |     |  External AI agent       |     |  Terminal / |
   |  (browser: PC or  |     |  (Cursor/Codex/Claude/…) |     |  scripts    |
   |   tablet/mobile)  |     |  provides the harness    |     |             |
   +---------+---------+     +------------+-------------+     +------+------+
             |                            |                          |
             | HTTP / WebSocket           | MCP (stdio shim)         | CLI (mydev call)
             v                            v                          v
          +----------------------------------------------------------------+
          |                  mydev: one daemon per user (Go)               |
          +----------------------------------------------------------------+
```

The core is the only component with authority over the environment. Every consumer is
a client of the same capabilities ([ADR-0003](../adr/0003-unified-capability-api.md)).

## Layers

```text
+---------------------------------------------------------------------------+
| Adapters                                                                  |
|   HTTP/WebSocket (web UI)  |  MCP (agents)  |  CLI (scripts)              |
+---------------------------------------------------------------------------+
| Capability API   (typed commands + queries, operations, structured events)|
+---------------------------------------------------------------------------+
| Domain services                                                           |
|   system | workspace | project | build | device | emulator | sdk | file   |
|   git | shell | code (post-MVP)                                           |
|   core services: operation | events | artifact | policy                   |
+---------------------------------------------------------------------------+
| Integrations                                                              |
|   ADB | Gradle (Tooling API sidecar) | emulator | SDK tools | Git         |
|   Kotlin LSP (post-MVP)                                                   |
+---------------------------------------------------------------------------+
| OS primitives                                                             |
|   processes | filesystem | sockets | file watchers                        |
+---------------------------------------------------------------------------+
```

The authoritative list of domains is the capability registry
([capability-api](capability-api.md) §Naming).

Rules:

- Adapters translate protocols; they hold no domain logic.
- The capability API is the single contract for every consumer. The one deliberate
  exception: policy changes are human-only and never reach MCP
  ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §6).
- Domain services own state and orchestration.
- Integrations call external tools, and they are the only place where tool-specific
  parsing lives. Gradle output is parsed in Go only where the Tooling API gives no
  structure (the console error extractor,
  [ADR-0022](../adr/0022-build-error-extraction.md)).
- The escape hatch (`shell.exec`) is a capability like any other. It is off unless
  policy enables it.

## Process topology

Fixed by [ADR-0019](../adr/0019-process-topology-daemon-and-shims.md):

```text
 MCP client ──stdio──▶ mydev mcp ──┐
 MCP client ──stdio──▶ mydev mcp ──┤  Unix socket
 terminal  ──────────▶ mydev call ─┼────────────▶ mydev serve (the core, one per user)
 browser   ──HTTP/WS (loopback)────┘                  │
                                                      │ supervises (children, no orphans)
                                                      ├── Gradle TAPI sidecar (JVM, one for all roots)
                                                      ├── Kotlin LSP (JVM, per workspace, post-MVP)
                                                      └── short per-operation commands, logcat streams
                                                      │
                                                      │ starts or uses, never owns
                                                      ├── adb server (shared)
                                                      ├── Gradle daemon (Gradle's; spawned for the sidecar)
                                                      └── emulators (detached; devices, not children)
```

- **One daemon owns everything.** Shims hold no state. Operations outlive the session
  that started them.
- **Our sidecars** are supervised children with an explicit lifecycle: started lazily,
  health-checked, stopped when idle, and reaped after a crash from pid records
  ([process-lifecycle](process-lifecycle.md),
  [ADR-0021](../adr/0021-persistence-format-and-location.md)).
- **Processes that belong to other tools** are started or used through those tools and
  never killed implicitly:
  - the `adb` server;
  - the Gradle daemon, which Gradle spawns, reuses and idles out by itself;
  - emulators.

  Stopping any of them is an explicit call.
- Concurrency inside the core is goroutines. All serialisation (per build root, per
  device) is in-process, which is sound because there is one daemon.

## Workspace model

A daemon hosts several **workspaces**. Every workspace-scoped call names its target
([ADR-0020](../adr/0020-operation-contract.md)); an MCP session is bound to one root.

```text
Workspace
├── identity        id (derived from the canonical root), root, name, trusted
├── project         modules, variants, toolchain profile
├── code            LSP session, diagnostics (post-MVP)
├── builds          running operations, build registry, last result, console artifacts
├── processes       workspace-owned sidecars (the Kotlin LSP, later)
└── watchers        filesystem events
```

Not in a workspace: devices and emulators (global, with no "active device"), the shared
TAPI sidecar, and subscriptions, which belong to sessions. Detail in
[workspace-state](workspace-state.md).

## Cross-cutting concerns

- **Operations and events**: every call is an operation that is waited on for a
  bounded time. Long-running work emits structured events. The UI subscribes; agents
  wait on the operation ([capability-api](capability-api.md)).
- **Artifacts**: screenshots, captures and build consoles are opaque references, in
  ephemeral or durable classes, readable through `artifact.read`
  ([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)).
- **Authorization**: device policy and project-root trust are re-checked before
  execution. Untrusted device data is marked per field and per result
  ([ADR-0015](../adr/0015-untrusted-data-authorization.md)). The HTTP listener has a
  security baseline ([http-websocket](../interfaces/http-websocket.md)).
- **Capability detection**: the core detects tool versions and features (adb, Gradle,
  AGP, KGP) and advertises what is available. The UI adapts.
- **Observability**: structured logs, and one operation record per call, which is also
  the capability trace.

## What is deliberately not here

- No agent harness or LLM loop ([ADR-0004](../adr/0004-no-own-harness.md)).
- No Android CLI dependency: Gradle, through the Tooling API, and ADB are used directly
  ([ADR-0005](../adr/0005-reuse-platform-tools.md),
  [ADR-0017](../adr/0017-gradle-execution-strategy.md)).
- No protocol logic in the core's domain layer.
- No embedded emulator, and no JVM inside the core. JVMs run only as sidecars.
