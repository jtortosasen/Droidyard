# Workspace State

How the core models state. Workspace-scoped capabilities
([capability-api.md](capability-api.md) §Scope) operate **inside a workspace**;
global ones (devices, emulators, the SDK, the core itself) do not.

> Status: draft. Complements `process-lifecycle.md` (sibling document).

## What a workspace is

A **workspace** is a rooted development context: a project directory plus the
live resources attached to it (workspace-owned sidecars, build state); a file watch
belongs to the session's subscription, and the workspace only stops its pollers on close.
It is the unit of isolation and the unit of state.

The core — one daemon per user ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md))
— hosts **multiple workspaces**. Every workspace-scoped call names its target in the
envelope's `workspace` field ([ADR-0020](../adr/0020-operation-contract.md)); there is
no implicit "current workspace" in the core. An MCP session is bound to one root at
attach, and its adapter fills the field.

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
| `opening` | Loading durable configuration and any cached project facts. Runs **no** project code and spawns nothing in the MVP (below). |
| `ready` | Usable. The project model has not necessarily been fetched: that happens lazily. |
| `degraded` | Partially usable: a workspace-owned sidecar is down (e.g. the LSP failed), the TAPI sidecar crashed mid-build (milestone 1g keeps that mark inside the Gradle client; the workspace state does not show it yet), or a state file is from a newer version ([ADR-0021](../adr/0021-persistence-format-and-location.md) §Format, `WORKSPACE_STATE_NEWER`). Capabilities report availability accordingly. |
| `closing` | Cancelling the workspace's operations, shutting down workspace-owned sidecars. |
| `closed` | All resources released. |

`degraded` is a first-class state, not an error: a crashed LSP or sidecar must not
make the whole workspace unusable. (A missing device never degrades a workspace:
devices are global, below.)

**What `open` does**, completely: canonicalise the root and derive the id, validate
that it is a readable directory, load `ws/<wsid>/workspace.json` and any cached
project facts, evaluate trust from policy, and publish the lifecycle event. It does
not contact the TAPI sidecar, does
not fetch the project model, and does not start the Kotlin LSP (post-MVP, which
imports the Gradle build and will therefore start lazily, for a trusted root only).

**When the model is fetched**: on the first `project.*`, `build.*` or
`sdk.requirements` call that needs it — never on open — and only for a trusted root;
an untrusted one fails that call with `WORKSPACE_UNTRUSTED` before anything is spawned
([ADR-0023](../adr/0023-android-project-facts-and-build-outputs.md),
[gradle](../subsystems/gradle.md) §Lifecycle).

`workspace.open` on an already open workspace is a no-op returning its state, so
several sessions (two agent windows, the UI) share one workspace. A target it cannot
validate ends `failed` with `WORKSPACE_ROOT_INVALID`; a state file from a newer version
opens the workspace `degraded` with `WORKSPACE_STATE_NEWER`, and an unexpected
state-file IO error ends `failed` with `WORKSPACE_STATE_UNREADABLE`
([ADR-0026](../adr/0026-workspace-addressing-invalid-roots.md)). `workspace.close`
closes it for all of them and completes at `closed`: entering `closing` rejects every
other workspace-scoped call on it with `WORKSPACE_NOT_OPEN`, its operations are
cancelled and drained for up to 10 s, and its sidecars stop; an `open` during `closing`
waits and then opens, and a second `close` is a no-op
([ADR-0027](../adr/0027-workspace-close-and-reopen.md)). The core never reopens a
workspace implicitly; an MCP session opens its bound root once, at session start
([ADR-0020](../adr/0020-operation-contract.md) §Scope).

## State model

```text
Workspace
├── identity      id, name, root, sdkRoot, trusted (evaluated from policy, not stored), createdAt
├── project       modules, variants, build files, AGP/Gradle/Kotlin versions (fetched lazily)
├── code          lsp session ref, open documents, diagnostics cache, index state
├── builds        running operations, build registry, last result
└── processes     workspace-owned sidecars (the Kotlin LSP, later)
```

Each section is owned by exactly one domain service
(`workspace`, `project`, `code`, `build`, `file`, `git`). No adapter holds workspace
state.

What is **not** in a workspace: devices and emulators (global, below); the TAPI
sidecar, which is shared by every root ([process-lifecycle](process-lifecycle.md));
and subscriptions, which belong to the **session** that created them and may filter on
any workspace. A `file.watch`'s poller belongs to the subscription its call created,
not to the workspace (milestone 1j, D9): the workspace holds no watcher state, only the
knowledge of which of its pollers its close must stop. An earlier sketch of this model
listed `watchers` under the workspace; the shipped design ties each watch to the
session's subscription, so it lives and dies with it.

## Identity and reuse

- A workspace id is derived **deterministically from the canonical project root**:
  `<basename>-<12 hex of sha256(root)>` ([ADR-0021](../adr/0021-persistence-format-and-location.md)).
  Re-opening the same project reuses the same identity, and adapters may address a
  workspace by root or by id.
- Re-opening a workspace restores durable configuration and discards ephemeral
  state (see below).
- Two cores never manage the same workspace: one daemon per state root owns every
  workspace in it, guarded by the daemon's `flock` (ADR-0019). No per-workspace lock
  file exists.
- A workspace is **trusted or not**
  ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §5). Trust comes from a
  configured path prefix or an explicit human act — the UI, or `droidyard trust` — and
  **never** from an MCP binding, because a repository can ship its own `.mcp.json`.
  Both kinds of decision are policy: they live in the user configuration, not in
  workspace state, so deleting `ws/<wsid>/` never grants or revokes trust. Trust is
  re-evaluated before each call, so a grant takes effect without reopening. An
  untrusted workspace opens in restricted mode, where capabilities that run project
  code fail with `WORKSPACE_UNTRUSTED`. **Opening a workspace never runs project
  code** (§Lifecycle).

## Durable vs ephemeral state

| Kind | Examples | Survives restart |
| --- | --- | --- |
| **Durable** | root, name, selected variant, preferences, recent workspaces, build registry and `lastResult`, durable artifacts (build consoles) | ✅ persisted |
| **Durable exception** | **pid records** of supervised children — `{pid, pgid, startTime, command}` | ✅ persisted until reaped, for crash recovery only |
| **Derived** | project modules/variants, AGP/Gradle versions | ❌ fetched lazily on first use, trusted roots only (§Lifecycle); may be cached, deletable |
| **Ephemeral** | running operations, process *state*, LSP session/index, diagnostics, watchers, subscriptions | ❌ never persisted |

Processes are ephemeral; the *record of what we spawned* is not. That record is what
lets the next daemon find and reap orphans after a crash.

Format and location are decided by [ADR-0021](../adr/0021-persistence-format-and-location.md):
JSON files under `$XDG_STATE_HOME/droidyard/` (Linux) or
`~/Library/Application Support/droidyard/` (macOS), per workspace under `ws/<wsid>/`,
never inside the project tree.

## Concurrency model

- One writer per workspace section; mutations are serialised by the owning domain
  service.
- Events carry a **daemon-wide monotonic `seq`** and the workspace id (`null` for
  global events), so consumers can order and resume
  ([capability-api.md](capability-api.md) §Events).
- Queries may run concurrently. Commands that need an exclusive resource (a build
  root, a device) wait for it as `queued` operations rather than holding a
  workspace-wide lock ([core-runtime](core-runtime.md) §Concurrency).

## Devices are not workspace-owned

Connected devices and emulators are **global** to the core, not owned by a
workspace, and `device.*` / `emulator.*` / `sdk.status` are global capabilities. The
device and the `adb` server are shared. A workspace keeps **no** "active device": every
device call names its `device` explicitly, and the UI keeps its own selection
client-side and passes it. This matters for teardown too: closing a workspace must not
disconnect devices or kill the shared `adb` server.

## Cleanup and crash recovery

- On graceful close: cancel and drain the workspace's operations (bounded,
  [ADR-0027](../adr/0027-workspace-close-and-reopen.md)), stop its workspace-owned
  sidecars (subject to policy), close the root's connection in the shared TAPI sidecar,
  release watchers.
- On crash: the next daemon reaps orphans from pid records (matching pid **and**
  start time), removes the stale socket, and treats every workspace as closed until
  reopened. It never assumes a clean slate.
- The daemon lock is reclaimed by the kernel when the holder dies (`flock`).

## Observability

- `workspace.describe` returns the full workspace state (minus ephemeral
  internals) as structured data.
- State transitions are published on an events topic (e.g. `workspace.lifecycle`)
  so the UI reflects `opening`/`degraded`/`closing` in real time.

## Open questions

- How much derived state (e.g. project module graph) to cache between restarts to
  speed up the first `project.*` call.
- Multi-root workspaces (one project root vs. several) — out of scope for v1?
