# Architecture Overview

High-level shape of the system. Detail lives in the (planned) architecture
documents linked from [`../README.md`](../README.md).

## Context

```text
   +-------------------+        +--------------------------+
   |  Human developer  |        |  External AI agent       |
   |  (browser: PC or  |        |  (Cursor/Codex/Claude/…)  |
   |   tablet/mobile)  |        |  provides the harness     |
   +---------+---------+        +------------+-------------+
             |                               |
             | HTTP / WebSocket              | MCP
             |                               |
             v                               v
          +------------------------------------------+
          |                  mydev                   |
          |           core runtime (Go)              |
          +------------------------------------------+
```

The core is the only component with authority over the environment. Both
consumers are clients of the same capabilities
([ADR-0003](../adr/0003-unified-capability-api.md)).

## Layers

```text
+---------------------------------------------------------------+
| Adapters                                                       |
|   HTTP/WebSocket (web UI)   |   MCP (agents)   |  CLI (scripts) |
+---------------------------------------------------------------+
| Capability API   (typed commands + queries, structured events) |
+---------------------------------------------------------------+
| Domain services                                                |
|   workspace | code | build | device | emulator | git | runtime |
+---------------------------------------------------------------+
| Integrations                                                   |
|   ADB | Gradle CLI | Kotlin LSP | emulator | SDK tools | Git       |
+---------------------------------------------------------------+
| OS primitives                                                  |
|   processes | filesystem | sockets | file watchers             |
+---------------------------------------------------------------+
```

Rules:

- Adapters translate protocols; they hold no domain logic.
- The capability API is the single contract for both consumers.
- Domain services own state and orchestration.
- Integrations call external tools; they are the only place tool-specific
  parsing lives.
- The escape hatch (`shell.exec`) is a capability like any other.

## Process topology

```text
                    mydev (Go, one process, many goroutines)
                      |         |          |           |
        supervises -> |         |          |           |
                      v         v          v           v
                  adb server  Gradle    Kotlin LSP   emulator
                  (daemon)    daemon    (JVM)        (qemu)
```

- The core is a single process; concurrency is internal (goroutines).
- JVM tools and the emulator are **sidecars**: started on demand by the core,
  supervised, and stoppable.
- Sidecars have an explicit lifecycle (see `architecture/process-lifecycle.md`,
  planned) with no orphan processes.

## Workspace model

State is organised per **workspace**:

```text
Workspace
├── root            (project directory)
├── project         (modules, variants, build files)
├── code            (LSP session, open documents, diagnostics)
├── builds          (running/finished Gradle tasks, last result)
├── devices         (connected/booted, active device)
├── processes       (supervised sidecars: LSP, Gradle, adb, emulators)
└── watchers        (filesystem events)
```

Detail in `architecture/workspace-state.md` (planned).

## Cross-cutting concerns

- **Events**: long-running operations stream structured events to all adapters so
  consumers subscribe rather than poll.
- **Process lifecycle**: every sidecar has an owner, health checks and cleanup.
- **Capability detection**: the core detects installed tool versions/features
  (ADB, Gradle, LSP) and advertises what is available; the UI adapts.
- **Observability**: structured logs and operation traces for both humans and
  agents.

## What is deliberately not here

- No agent harness or LLM loop ([ADR-0004](../adr/0004-no-own-harness.md)).
- No Android CLI dependency; Gradle and ADB are used directly
  ([ADR-0005](../adr/0005-reuse-platform-tools.md)).
- No protocol logic in the core's domain layer.
- No embedded emulator or embedded JVM.
