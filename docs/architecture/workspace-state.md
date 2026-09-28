# Workspace State

How the core models state. Everything the capability API
([capability-api.md](capability-api.md)) does happens **inside a workspace**.

> Status: draft. Complements `process-lifecycle.md` (sibling document).

## What a workspace is

A **workspace** is a rooted development context: a project directory plus the
live resources attached to it (sidecar processes, devices, build state,
watchers, subscriptions). It is the unit of isolation and the unit of state.

The core can host **multiple workspaces**; they do not share sidecars or
subscriptions.

## Lifecycle

```text
                 workspace.open
                       |
                       v
   created -> opening -> ready <-> degraded
                       |              |
                       +------+-------+
                              |
                    workspace.close
                              v
                          closing -> closed
```

| State | Meaning |
| --- | --- |
| `created` | Registered, root validated, nothing started yet. |
| `opening` | Discovering project, starting required sidecars. |
| `ready` | Usable; necessary sidecars healthy. |
| `degraded` | Partially usable; one or more sidecars unavailable (e.g. LSP failed, no device). Capabilities report availability accordingly. |
| `closing` | Shutting down sidecars and draining operations. |
| `closed` | All resources released. |

`degraded` is a first-class state, not an error: a missing emulator or a crashed
LSP must not make the whole workspace unusable.

## State model

```text
Workspace
├── identity      id, name, root, sdkRoot, createdAt
├── project       modules, variants, build files, AGP/Gradle/Kotlin versions
├── code          lsp session ref, open documents, diagnostics cache, index state
├── builds        running operations, last build/test result
├── devices       known devices, connected devices, active device
├── processes     supervised sidecars (see process-lifecycle.md)
├── watchers      watched roots, filters, debounce state
└── subscriptions active event subscriptions owned by consumers
```

Each section is owned by exactly one domain service
(`workspace`, `project`, `code`, `build`, `device`, `emulator`, `runtime`, `git`,
`system`). No adapter holds workspace state.

## Identity and reuse

- A workspace id is derived **deterministically from the project root path**, so
  re-opening the same project reuses the same identity.
- Re-opening a workspace restores durable configuration and discards ephemeral
  state (see below).
- Two cores must never manage the same workspace concurrently; a lock guards this.

## Durable vs ephemeral state

| Kind | Examples | Survives restart |
| --- | --- | --- |
| **Durable** | root, name, selected variant, preferences, recent workspaces | ✅ persisted |
| **Derived** | project modules/variants, AGP/Gradle versions | ❌ re-discovered on open |
| **Ephemeral** | running operations, LSP session/index, diagnostics, watchers, subscriptions | ❌ never persisted |

Persistence format and location are an **open decision** (see
`adr/README.md`). Working assumption: a small local store keyed by workspace id,
human-inspectable.

## Concurrency model

- One writer per workspace section; mutations are serialised by the owning domain
  service.
- Events are emitted with a **monotonic `seq` per workspace** so consumers can
  detect gaps (see [capability-api.md](capability-api.md)).
- Queries may run concurrently; long commands return operation handles rather than
  holding a lock.

## Devices are not workspace-owned

Connected devices and emulators are **global** to the core, not owned by a
workspace. A workspace has an **active device** *selection*, but the device
itself (and the `adb` server) is shared. This matters for teardown: closing a
workspace must not disconnect devices or kill the shared `adb` server.

## Cleanup and crash recovery

- On graceful close: stop workspace-owned sidecars (subject to policy), drain and
  cancel operations, release watchers and subscriptions.
- On crash: the next start must detect **stale state** (orphan sidecars, stale
  pids/sockets, lock files) and reconcile it rather than assuming a clean slate.
- The workspace lock must be reclaimable if the previous owner is provably gone.

## Observability

- `workspace.describe` returns the full workspace state (minus ephemeral
  internals) as structured data.
- State transitions are published on an events topic (e.g. `workspace.lifecycle`)
  so the UI reflects `opening`/`degraded`/`closing` in real time.

## Open questions

- Persistence format and location (SQLite vs. plain files).
- Exact lock/recovery mechanism for concurrent cores and crashes.
- Whether a workspace can be *reopened* while operations are still draining, or
  must fully close first.
- How much derived state (e.g. project module graph) to cache between restarts to
  speed up `opening`.
- Multi-root workspaces (one project root vs. several) — out of scope for v1?
