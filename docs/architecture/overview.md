# Architecture Overview

The shape of the system and the rules that bind it. What code can say (the package
layout in `internal/`, the descriptor in `internal/capability`, the `Start`/`Wait`/
`Cancel` signatures) lives in code; the contract is `schema/capabilities.json` and
[capability-api](capability-api.md); the rest is in the architecture, interface and
subsystem documents linked from [`../README.md`](../README.md).

## Context

```text
   +-------------------+     +--------------------------+     +-------------+
   |  Human developer  |     |  External AI agent       |     |  Terminal / |
   |  (browser: PC or  |     |  (Cursor/Codex/Claude/…) |     |  scripts    |
   |   tablet/mobile)  |     |  provides the harness    |     |             |
   +---------+---------+     +------------+-------------+     +------+------+
             | HTTP / WebSocket           | MCP (stdio shim)         | CLI (droidyard call)
             v                            v                          v
          +----------------------------------------------------------------+
          |              droidyard: one daemon per user (Go)               |
          +----------------------------------------------------------------+
```

The core is the only component with authority over the environment. Every consumer is a
client of the same capabilities ([ADR-0003](../adr/0003-unified-capability-api.md)).

## Layers

```text
+---------------------------------------------------------------------------+
| Adapters      HTTP/WebSocket (web UI) | MCP (agents) | CLI (scripts)        |
+---------------------------------------------------------------------------+
| Capability API   (typed commands + queries, operations, structured events)|
+---------------------------------------------------------------------------+
| Domain services  (the registry lists them)                                |
|   core services: operation | events | artifact | policy                   |
+---------------------------------------------------------------------------+
| Integrations     ADB | Gradle (Tooling API sidecar) | emulator | SDK tools |
|                  Git | Kotlin LSP (post-MVP)                              |
+---------------------------------------------------------------------------+
| OS primitives    processes | filesystem | sockets | file watchers         |
+---------------------------------------------------------------------------+
```

- Adapters translate protocols; they hold no domain logic.
- The capability API is the single contract for every consumer. The one deliberate
  exception: policy changes are human-only and never reach MCP
  ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §6).
- Domain services own state and orchestration.
- Integrations call external tools and are the only place where tool-specific parsing
  lives. Gradle output is parsed in Go only where the Tooling API gives no structure
  (the console error extractor, [ADR-0022](../adr/0022-build-error-extraction.md)).
- The escape hatch (`shell.exec`) is a capability like any other, off unless policy
  enables it.

**The registry is the single source** for dispatch, JSON Schema generation, MCP tool
definitions, CLI help and `system.capabilities`; if it is not in the registry, it does
not exist. A domain package registers its capabilities explicitly at startup, with no
reflection over the codebase, so the set is greppable and reviewable
([ADR-0016](../adr/0016-capability-schema-in-go.md)). Everything lives under
`internal/`: this is an application, not a library.

## Adapter boundary

Adapters may import `capability`, `event` and `artifact`, **never `domain/*`**. This is
what keeps the edge thin: if an adapter needs behaviour, that behaviour belongs in a
capability. Enforced by package layout and by review ([ADR-0025](../adr/0025-mcp-adapter-implementation.md)).

## Process topology

Fixed by [ADR-0019](../adr/0019-process-topology-daemon-and-shims.md):

```text
 MCP client ──stdio──▶ droidyard mcp ──┐
 MCP client ──stdio──▶ droidyard mcp ──┤  Unix socket
 terminal  ──────────▶ droidyard call ─┼────────────▶ droidyard serve (the core, one per user)
 browser   ──HTTP/WS (loopback)────────┘                  │
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
  health-checked, stopped when idle, reaped after a crash from pid records
  ([process-lifecycle](process-lifecycle.md),
  [ADR-0021](../adr/0021-persistence-format-and-location.md)).
- **Processes that belong to other tools** (the `adb` server, the Gradle daemon, which
  Gradle spawns, reuses and idles out itself, and emulators) are started or used through
  those tools and never killed implicitly. Stopping any of them is an explicit call.
- Concurrency inside the core is goroutines. Serialisation is in-process, which is
  sound because there is one daemon.

## Workspace model

A daemon hosts several **workspaces**. Every workspace-scoped call names its target
([ADR-0020](../adr/0020-operation-contract.md)); an MCP session is bound to one root. A
workspace holds identity (id derived from the canonical root, root, name, trusted),
project facts, builds (running operations, the build registry, last result, console
artifacts) and workspace-owned sidecars (the Kotlin LSP, later). Not in a workspace:
devices and emulators (global, with no "active device"), the shared TAPI sidecar, and
subscriptions, which belong to sessions (file watchers belong to the session's
subscription: [files](../subsystems/files.md)). Detail in
[workspace-state](workspace-state.md).

## Events

- One event bus and one **daemon-wide monotonic sequence** per daemon epoch. Workspace
  events carry their workspace id; global events (devices, emulators, adb, shared
  sidecars) carry `workspace: null` ([capability-api](capability-api.md) §Events).
- Subscribers resume with `since` from a bounded replay ring; overflow and expired
  resumes are an explicit `events.gap`, never silent. A capability's own stream
  (`file.changes`) takes a `seq` but never enters the ring, so a busy watch cannot flush
  what other subscribers resume from; so does `operation.state`.
- The bus is the **only** cross-cutting channel. Progress goes to it tagged with the
  operation id; adapters subscribe and translate (MCP progress notifications, WebSocket
  frames). No adapter polls, and no handler knows which adapter is watching.

## Concurrency

- One goroutine per operation; handlers must be safe for concurrent invocation.
  `ctx` cancellation is a contract: `Cancel` cancels the handler's `ctx`, and handlers
  must honour it and clean up ([ADR-0020](../adr/0020-operation-contract.md)).
- **Per-resource locks, never a global lock**: per workspace, per device, per build root.
  They are in-process, sound because one daemon owns everything.
- **All Tooling API interactions are serialised per build root**: builds, tests and
  model fetches (`build.tasks`, `project.*`) alike. Gradle does not reuse a busy daemon,
  so a model fetch during a build would start a second daemon and cost gigabytes.
  Cached answers (a task list whose build-file fingerprint is unchanged) do not touch
  TAPI and do not wait.
- **Contention queues, it does not reject.** A second request on a busy root (the UI and
  an agent building at once) becomes a `queued` operation, FIFO, reporting `blockedBy`;
  the caller's bounded wait returns that state. A queue past its cap is rejected with
  `BUILD_QUEUE_FULL` (not implemented: one agent per root until the UI is a second
  client). Cancelling a queued operation is immediate.
- **Short calls have a queue bound.** A capability not flagged `LongRunning` that stays
  queued past the bound fails with `OPERATION_QUEUE_TIMEOUT` without having run
  ([ADR-0020](../adr/0020-operation-contract.md) §Core primitives). The scheduler enforces it,
  not the handler.
- Device writes are serialised per device; reads proceed in parallel. `emulator.start`
  and `wipe` share one boot key, and `emulator.stop` takes the device's write key
  ([emulator](../subsystems/emulator.md)).
- A lock is never held while blocked on a subprocess without a timeout.

## State ownership

| State | Owner | Lifetime |
| --- | --- | --- |
| Workspace (config, project facts) | `workspace` | durable config + facts fetched lazily |
| Operations | `capability` | in memory; terminal ones retained 15 min |
| Build registry | `domain/build` | compact results persisted, retention applied |
| Artifacts (screenshots, consoles) | `artifact` | bounded by TTL and size; consoles durable |
| Live devices | `domain/device` | in memory; devices are not ours |
| Pid records of supervised children | `process` via `store` | durable, until reaped |
| Sessions and their subscriptions | `daemon` / `event` | the connection's lifetime |
| Policy, trusted roots included | `policy` | the user configuration file (configuration, not state), changed through **human channels only** (UI, CLI, hand edits), never over MCP |

Persistence: [ADR-0021](../adr/0021-persistence-format-and-location.md); artifacts:
[ADR-0012](../adr/0012-visual-artifacts-as-refs.md); policy:
[ADR-0015](../adr/0015-untrusted-data-authorization.md) §6.

## Cross-cutting concerns

- **Authorization**: device policy and project-root trust are re-checked before
  execution; untrusted device data is marked per field and per result. The HTTP
  listener has a security baseline ([http-websocket](../interfaces/http-websocket.md)).
- **Capability detection**: the core detects tool versions and features (adb, Gradle,
  AGP, KGP) and advertises what is available; the UI adapts.
- **Observability**: structured logs; each operation ends in one `droidyard: op` log
  line, the capability trace.

## What is deliberately not here

- No agent harness or LLM loop ([ADR-0004](../adr/0004-no-own-harness.md)).
- No Android CLI dependency: Gradle, through the Tooling API, and ADB are used directly
  ([ADR-0005](../adr/0005-reuse-platform-tools.md),
  [ADR-0017](../adr/0017-gradle-execution-strategy.md)).
- No protocol logic in the core's domain layer.
- No embedded emulator, and no JVM inside the core. JVMs run only as sidecars.
