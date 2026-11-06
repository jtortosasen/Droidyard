# Workspace State

How the core models state. Workspace-scoped capabilities
([capability-api.md](capability-api.md) §Scope) operate **inside a workspace**; global ones
(devices, emulators, the SDK, the core itself) do not. Sibling:
[process-lifecycle.md](process-lifecycle.md).

## What a workspace is

A **workspace** is a rooted development context: a project directory plus the live
resources attached to it (workspace-owned sidecars, build state). It is the unit of
isolation and of state.

The core, one daemon per user ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)),
hosts **multiple workspaces**. Every project call names its target in a `project`
parameter, an absolute path inside the project; the core has no implicit "current
workspace" and no session is bound to one ([ADR-0137](../adr/0137-machine-level-android-tool.md)
§2, §3). Why a parameter and no binding: a client may share one server between chats
(the Cursor app does) and nothing in MCP names the calling chat, so a binding made at the
server's start gave a chat the tools of whichever folder started it. The agent always knows
its folder, so it says so per call.

A **worktree is a workspace of its project**: its own root, id, builds and state, named by
its own path; Droidyard creates no worktree and occupies none, the agent's git does.

A workspace has two roots ([ADR-0075](../adr/0075-project-root-and-gradle-root.md)): the
**project root**, the folder opened, and the **Gradle root** inside it, where builds run; so a
repository whose build is `dev/` opens at its top.

## Lifecycle

`created -> opening -> ready <-> degraded`, then an idle release -> `closing -> closed`.

| State | Meaning |
| --- | --- |
| `created` | Registered, root validated, nothing started yet. |
| `opening` | Loading durable configuration and any cached project facts. Runs **no** project code and spawns nothing in the MVP (below). |
| `ready` | Usable. The project model has not necessarily been fetched: that happens lazily. |
| `degraded` | Partially usable: a workspace-owned sidecar is down (e.g. the LSP failed), the TAPI sidecar crashed mid-build (every open workspace whose current Gradle root is the crashed one degrades, with `BUILD_SIDECAR_CRASHED`; the next successful sidecar start returns it to `ready`, clearing that cause only), or its `workspace.json` is from a newer version ([ADR-0056](../adr/0056-newer-daemon-wide-state-file.md)). |
| `closing` | Shutting down workspace-owned sidecars. An idle release cancels nothing: it runs only with no operation running. |
| `closed` | All resources released. |

`degraded` is a first-class state, not an error: a crashed LSP or sidecar must not make
the whole workspace unusable. (A missing device never degrades a workspace: devices are
global, below.)

**What an open does**, completely: canonicalise the root and derive the id, validate that
it is a readable directory, load `ws/<wsid>/workspace.json` and any cached project facts,
evaluate trust from policy, and publish the lifecycle event (it carries the root). The
Gradle root was resolved before, by the call that named the project (reads only). It does not contact the TAPI
sidecar, does not fetch the project model, and does not start the Kotlin LSP (post-MVP,
which imports the Gradle build and will therefore start lazily, for a trusted root only).

**When the model is fetched**: on the first `project.*`, `build.*` or `sdk.requirements`
call that needs it, never on open, and only for a trusted root; an untrusted one fails
that call with `WORKSPACE_UNTRUSTED` before anything is spawned
([ADR-0023](../adr/0023-android-project-facts-and-build-outputs.md),
[gradle](../subsystems/gradle.md) §Lifecycle).

**Opened by the first call, released when idle.** A call naming a project resolves it
(ADR-0075's rules: the nearest ancestor holding a settings file is the Gradle root) and
opens the root at Start, if it is not open; a refused call records no operation. Several
clients share one open workspace. A root with no call and no running operation for 30
minutes is released (sidecar stopped, connection closed); the next call opens it again, and
the last build's result survives (`build.lastResult`). Nothing closes a workspace on
request: there is no `workspace.*`. What the release must never do is cancel a call that was
admitted, and the code's lock order exists for that: the transition lock `w.tx`, then the
registry's admission lock, then the operation store's, so a release decides and enters
`closing` inside the same hold that admission needs (package doc of `internal/workspace`).
Invalid roots and state-file errors are [ADR-0026](../adr/0026-workspace-addressing-invalid-roots.md);
a close drains operations for up to 10 s ([ADR-0027](../adr/0027-workspace-close-and-reopen.md)).
A path no Gradle root answers to is refused with the candidates it can name
(`PROJECT_NO_GRADLE_ROOT`).

## State model

A workspace holds its identity (id, name, root, `sdkRoot`, `trusted`, evaluated from policy and not
stored, `createdAt`), its project facts (modules, variants, build files, AGP/Gradle/Kotlin versions,
fetched lazily), its code state (LSP session, open documents, diagnostics and index; post-MVP), its
builds (running operations, build registry, last result) and its workspace-owned sidecars. Each
section is owned by exactly one domain service (`workspace`, `project`, `code`, `build`); no
adapter holds workspace state.

What is **not** in a workspace: devices and emulators (global, below); the TAPI sidecar, shared by
every root ([process-lifecycle](process-lifecycle.md)); and subscriptions, which belong to the
**session** that created them and may filter on any workspace.

## Identity and reuse

- A workspace id is derived **deterministically from the canonical project root**:
  `<basename>-<12 hex of sha256(root)>` ([ADR-0021](../adr/0021-persistence-format-and-location.md)).
  Re-opening the same project reuses the identity, and adapters may address a workspace by
  root or by id. Re-opening restores durable configuration and discards ephemeral state.
- Two cores never manage the same workspace: one daemon per state root owns every workspace
  in it, guarded by the daemon's `flock` (ADR-0019). No per-workspace lock file exists.
- A workspace is **trusted or not**
  ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §5). Trust comes from a
  configured path prefix or an explicit human act (`droidyard trust`, a hand edit), or, for a
  linked worktree, from its trusted main tree, proven by git's own files
  ([ADR-0081](../adr/0081-worktree-inherits-trust.md)); **never** from what an MCP client lists or passes, because a repository can ship its own `.mcp.json`. Trust is judged on the root the path resolved to, not the path passed. Both
  are policy: they live in the user configuration, not in workspace state, so deleting
  `ws/<wsid>/` never grants or revokes trust. Trust is re-evaluated before each call, so a
  grant takes effect without reopening. An untrusted workspace opens in restricted mode,
  where capabilities that run project code fail with `WORKSPACE_UNTRUSTED`. **Opening a
  workspace never runs project code.**

## Durable vs ephemeral state

**Durable**: root, name, `workspaces.json` (nothing reads it now), the build registry and
`lastResult`, durable artifacts (build consoles), and, as the one exception, the **pid records** of
supervised children (until reaped, for crash recovery only). **Derived**: project modules/variants and
AGP/Gradle versions, fetched lazily on first use, trusted roots only, cacheable and deletable.
**Ephemeral**, never persisted: running operations, process *state*, LSP session/index, diagnostics,
watchers, subscriptions. Processes are ephemeral; the *record of what we spawned* is not, so the next
daemon can reap orphans after a crash. Format and location are
[ADR-0021](../adr/0021-persistence-format-and-location.md): JSON files per workspace under
`ws/<wsid>/` in the user state directory, never inside the project tree.

## Concurrency model

- One writer per workspace section; mutations are serialised by the owning domain service.
- Events carry a **daemon-wide monotonic `seq`** and the workspace id (`null` for global
  events), so consumers can order and resume ([capability-api.md](capability-api.md)
  §Events).
- Queries may run concurrently. Commands that need an exclusive resource (a build root, a
  device) wait for it as `queued` operations rather than holding a workspace-wide lock
  ([overview](overview.md) §Concurrency).

## Devices are not workspace-owned

Connected devices and emulators are **global**, and `device.*` / `emulator.*` /
`sdk.status` are global capabilities. A workspace keeps **no** "active device": every
device call names its `device` explicitly, and a client keeps its own selection.
Releasing a workspace must not disconnect devices or kill the shared `adb` server. A device is
held by a session, not by a workspace: the MCP session whose acting call reaches a free device
occupies it until the session ends or the device stops or goes
([ADR-0094](../adr/0094-occupation-by-a-session.md); a worktree's occupation left in 3j). On a host, a paired peer's link holds a
lent device through any call: the lease ([ADR-0104](../adr/0104-lease-of-a-lent-device.md)).

## Cleanup and crash recovery

On a close: drain the workspace's operations (bounded, ADR-0027), stop its
workspace-owned sidecars (subject to policy), close the connection of each Gradle root no other open
workspace uses (a switched-away root closes once no operation of any workspace holds it,
[ADR-0092](../adr/0092-switched-gradle-root-closes.md)). On a crash the next daemon reaps orphans from pid records (matching pid **and**
start time), removes the stale socket, and treats every workspace as closed until reopened; it never
assumes a clean slate, and the kernel reclaims the daemon `flock` when the holder dies.

## Observability

Transitions are published on `workspace.lifecycle` (with the root), so a client reflects
`opening`/`degraded`/`closing` in real time.
