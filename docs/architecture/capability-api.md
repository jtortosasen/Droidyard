# Capability API

The **internal contract of the core**. Every consumer — the web UI, MCP agents
and the CLI — is a thin adapter over it
([ADR-0003](../adr/0003-unified-capability-api.md)). This document defines the
abstract contract; the concrete wire mappings live in `interfaces/`.

> Status: draft. This is the backbone of the system; most other documents depend
> on it. The invocation contract is fixed by
> [ADR-0020](../adr/0020-operation-contract.md).

## Purpose

Provide **one** typed, semantic surface for every operation the core can perform,
so that:

- a capability is implemented exactly once,
- the UI and agents observe identical semantics,
- results are **structured**, not stdout to be re-parsed,
- adapters stay protocol-only.

## Position

```text
        web UI                 agents                  scripts
          |                      |                        |
     HTTP / WebSocket     MCP (stdio shim)            CLI (droidyard call)
          \                      |                        /
           \                     |      Unix socket      /
            +-----------+--------+--------+-------------+
                        |                 |
                 capability API (this document), inside the daemon
                        |
                domain services
                        |
        ADB | Gradle | Kotlin LSP | emulator | SDK tools | Git
```

All three adapters run in one daemon ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)).

## Concepts

| Concept | Meaning |
| --- | --- |
| **Capability** | A named, typed operation, written `<domain>.<verb>` (e.g. `device.uiTree`). |
| **Query** | A capability with no side effects on the environment. May still be slow. |
| **Command** | A capability that mutates state or triggers work. |
| **Scope** | `global` or `workspace`: whether the call targets a workspace (see below). |
| **Operation** | The record of one invocation — id, state, result. **Every** invocation is one. |
| **Streaming capability** | One whose statefulness is `streaming`: it produces events for a session. Not a separate kind. |
| **Event** | A typed, timestamped message emitted by the core. |
| **Session** | One adapter connection — an MCP shim, a CLI invocation, a WebSocket. Only a session that can carry state and events, today the WebSocket, owns stateful resources ([ADR-0010](../adr/0010-capability-statefulness.md)). |
| **Subscription** | A session's registration to receive events matching a filter. |

## Naming and namespaces

Format: `<domain>.<verb>`, lower camel case, verb last. Names match
`^[a-z]+\.[a-z][A-Za-z0-9]*$`: **never an underscore**, so the MCP mapping (dot →
underscore) is reversible.

```text
workspace.open        workspace.describe      workspace.close
project.modules       project.variants
build.assemble        build.run               build.tasks     build.errors   build.lastResult
device.list           device.install          device.launch   device.uninstall
device.logs           device.screenshot       device.uiTree   device.tapElement   device.inputText
emulator.list         emulator.start          emulator.stop   emulator.wipe
sdk.status            sdk.requirements
file.list             file.read               file.write      file.watch
git.status            git.diff                git.log         git.branches
shell.exec
artifact.read
policy.get            policy.update           (human-only; never over MCP)
system.capabilities   system.health
operation.wait        operation.cancel
events.subscribe      events.unsubscribe
code.definition       code.references         code.symbols    code.diagnostics   (post-MVP)
```

Domains are fixed by the capability registry, not invented ad hoc by adapters. Once
the registry exists, this list and the classification table below are **generated**
from it and checked for drift in CI.

## The registry is the single source of truth

Capabilities are **defined in Go** — descriptor plus params/result structs — and the
JSON Schema is **generated** from them and committed as a golden file
([ADR-0016](../adr/0016-capability-schema-in-go.md)):

```text
       Go descriptors + structs (single source of truth)
                            |  generate
                            v
             JSON Schema (committed golden file)
                            |
              +-------------+-------------+
              v                           v
        MCP tool defs               TS client types
        (runtime, Phase 0)          (Phase 2, ADR-0008)
```

The descriptor fields are listed in [core-runtime](core-runtime.md) §The capability
registry.

## Invocation model

Defined by [ADR-0020](../adr/0020-operation-contract.md): the core offers
`Start → opID`, `Wait(opID, deadline) → outcome`, `Cancel(opID)`, and adapters compose
them. **The caller decides how long to block**; no handler decides to "run in the
background".

### Request envelope

```json
{
  "id": "req-1",
  "capability": "build.assemble",
  "workspace": "/home/me/sample-app",
  "params": { "module": ":app", "variant": "debug" },
  "waitMs": 20000,
  "idempotencyKey": "a1b2",
  "raw": false
}
```

- `workspace` — **required iff the capability is workspace-scoped**, rejected
  otherwise. A workspace id or an absolute project root (canonicalised to the id).
- `waitMs` — how long to wait before returning a still-running operation. Omitted:
  wait to completion.
- `idempotencyKey` — only for capabilities that accept one (see §Idempotency).

### Response envelope

```json
{ "id": "req-1",
  "operation": { "id": "op-7f3a", "state": "completed" },
  "result": { "outcome": "success", "...": "..." },
  "meta": { "durationMs": 41230, "untrusted": true } }
```

```json
{ "id": "req-1",
  "operation": { "id": "op-7f3a", "state": "running",
                 "progress": { "phase": "execution", "done": 112, "total": 340 } } }
```

- `operation.state`: `queued | running | completed | failed | cancelled`. A `queued`
  operation names what it waits behind in `blockedBy {operationId, capability}`.
- `result` is present iff `completed`; `error` iff `failed` or `cancelled` (the latter
  with `OPERATION_CANCELLED`). Never both.
- A request rejected before it starts (unknown capability, bad params, missing
  `workspace`, a workspace not open, a policy denial that needs no I/O) has an `error` and **no**
  `operation`.
- **A failed build is `completed`**, with `outcome: "failed"` in its result. `failed`
  is reserved for calls that could not be performed.

### Error model

One shape everywhere — the envelope, MCP (`isError: true`, the object as JSON text),
CLI output:

```json
{
  "code": "DEVICE_NOT_FOUND",
  "message": "no device with serial 'emulator-5556'",
  "cause": "adb devices lists: emulator-5554 (device)",
  "nextStep": "call device.list, or emulator.start to boot one",
  "details": { "requested": "emulator-5556", "online": ["emulator-5554"] }
}
```

| Field | Rule |
| --- | --- |
| `code` | Required. Stable `<DOMAIN>_<REASON>`, SCREAMING_SNAKE. The only field consumers branch on. |
| `message` | Required. Human-readable, non-normative. Never parsed. |
| `cause` | Optional. What actually went wrong underneath: the tool, its exit status, the decisive line. |
| `nextStep` | Optional. What the caller should do, naming capabilities by canonical name. |
| `details` | Optional. Typed per `code`, declared in the schema. |

**`<DOMAIN>`** is the domain that owns the failure, which is not always the domain of
the capability called: `device.install` returns `BUILD_OUTPUT_STALE`. It is a
capability domain (§Naming), or one of three reserved prefixes for failures that
belong to no domain: `REQUEST` (the envelope), `SESSION` (the connection), `DAEMON` (the
socket handshake). The list is closed; a new prefix is a schema change.

**Versioning of codes.** Error codes are part of the published schema: each descriptor
lists the codes the capability itself may return, and the golden file records them. A
capability that projects another operation's outcome (`operation.wait`) passes that
operation's error through unchanged: any unlisted code a consumer sees is the waited
operation's failure (§Operations and cancellation). Following the API's semantic
version:

- a code, once published, is never renamed or given a new meaning;
- new codes may be added in a minor version, so consumers must treat an unknown code as
  a generic failure of that capability;
- a code is removed only in a major version;
- the shape of a code's `details` evolves additively under the same rules.

Codes carry no version suffix of their own.

**Cross-cutting codes** every adapter may see, whatever the capability:

| Code | Meaning |
| --- | --- |
| `SESSION_REQUIRED` | A stateful/streaming capability was called on a connection that cannot carry it — plain HTTP, a CLI call or an MCP session; the WebSocket can ([ADR-0010](../adr/0010-capability-statefulness.md)) |
| `REQUEST_IDEMPOTENCY_CONFLICT` | An `idempotencyKey` was reused with different params (§Idempotency) |
| `WORKSPACE_NOT_OPEN` | A workspace-scoped call named a root or id that is not open; nextStep `workspace.open` ([ADR-0020](../adr/0020-operation-contract.md)) |
| `WORKSPACE_UNTRUSTED` | The workspace opened in restricted mode; the capability touches project code ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §5) |
| `OPERATION_NOT_FOUND` | `operation.wait`/`cancel` on an id that never existed or is past retention |
| `OPERATION_CANCELLED` | The error of every `cancelled` operation |
| `OPERATION_QUEUE_TIMEOUT` | A short call stayed `queued` past the queue bound; it never ran (§Operations and cancellation) |
| `ARTIFACT_NOT_FOUND` / `ARTIFACT_EXPIRED` | The reference is unknown, or ephemeral and past its TTL ([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)) |
| `POLICY_SHELL_DISABLED` | `shell.exec` is not enabled for the root; its message warns that enabling it gives the agent the user's full reach, and its nextStep names `shell.roots` in the file ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §7, [ADR-0032](../adr/0032-configuration-file-v1.md)) |
| `POLICY_CONFIG_UNUSABLE` | `policy.update` met a configuration file it cannot change: invalid, of a newer version, or unwritable; the file is left as it was ([ADR-0032](../adr/0032-configuration-file-v1.md) §4, §5) |
| `SHELL_EXEC_FAILED` | `shell.exec`'s command could not start, its root was gone, or its pid record could not be written (its group is then killed); a non-zero exit or a timeout is a completed result, not this error |

`DAEMON_VERSION_MISMATCH` is a handshake reply, not a capability error
([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)).

### Operations and cancellation

- `operation.wait {operationId, timeoutMs?}` returns the same shape as the response
  envelope; `timeoutMs: 0` is a status poll. Its `operation` is the waited operation, and
  its `result` or `error` is that operation's own: `OPERATION_CANCELLED` when it ended
  cancelled, the handler's typed error when it failed. Its descriptor lists the codes it
  returns itself (`OPERATION_NOT_FOUND`) plus `OPERATION_CANCELLED`; any other code a
  consumer sees is the waited operation's, passed through unchanged.
- `operation.cancel {operationId}` requests cooperative cancellation: idempotent,
  immediate for `queued`, a no-op on terminal operations. There is **no**
  domain-specific cancel; for builds, the build id is the operation id.
- Operation ids are daemon-wide. Any session — or none — can wait on or cancel any
  operation, and operations outlive the session that started them
  ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)).
- Terminal operations stay waitable for 15 minutes (at least the last 200).
- **Queue bound.** A capability not flagged long-running that is still `queued` after
  20 s fails with `OPERATION_QUEUE_TIMEOUT`, naming `blockedBy`; it never ran, so a
  retry is safe. This keeps a `device.tapElement` queued behind a `device.install` from
  outliving a client timeout
  ([ADR-0020](../adr/0020-operation-contract.md) §States).

### Idempotency

- Queries are idempotent by definition.
- Commands declare whether they are idempotent (descriptor `Idempotent`).
- Commands that accept an `idempotencyKey` (descriptor `AcceptsIdempotencyKey`) dedupe
  on `(workspace or global, capability, key)` within the retention window: a repeat
  returns the existing operation instead of starting new work; the same key with
  different params is `REQUEST_IDEMPOTENCY_CONFLICT`.

## Scope: global vs workspace

A capability is **workspace-scoped** when it reads or writes workspace state; otherwise
it is **global**. Devices, emulators, the SDK installation and the core itself are
global ([workspace-state](workspace-state.md) §Devices are not workspace-owned).

`device.install` is the deliberate exception within `device.*`: it resolves build
outputs, which belong to a workspace. The device is still named explicitly.

Adapters supply `workspace`: the UI from its own selection, the CLI from
`--workspace` or the cwd, and MCP from the session's binding, so agents never pass it
([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)).

The target must be **open**: the core never opens a workspace implicitly, and any
other workspace-scoped call on a root that is not open gets `WORKSPACE_NOT_OPEN`. The
MCP adapter opens its bound root once, when the session starts; after a
`workspace.close` by any client it does not reopen it
([ADR-0020](../adr/0020-operation-contract.md) §Scope).

## Classification of the MVP surface

Scope: **G**lobal / **W**orkspace. Kind: **Q**uery / **C**ommand. LR = long-running
(may outlive an adapter's wait budget, ADR-0020). Untrusted = the result carries
device- or app-derived data ([ADR-0015](../adr/0015-untrusted-data-authorization.md)).
Statefulness per [ADR-0010](../adr/0010-capability-statefulness.md); everything not
marked is `stateless`.

| Capability | Scope | Kind | Statefulness | LR | Untrusted |
| --- | --- | --- | --- | --- | --- |
| `system.capabilities`, `system.health` | G | Q | | | |
| `workspace.open` | W | C | | | |
| `workspace.describe` | W | Q | | | |
| `workspace.close` | W | C | | | |
| `project.modules`, `project.variants` | W | Q | | ✓ (cold model fetch) | |
| `build.tasks` | W | Q | | ✓ (cold model fetch) | |
| `build.assemble`, `build.run` | W | C | | ✓ | |
| `build.errors`, `build.lastResult` | W | Q | | | |
| `device.list` | G | Q | | | |
| `device.install` | W | C | | ✓ | |
| `device.launch`, `device.uninstall` | G | C | | | |
| `device.logs` (snapshot or bounded follow) | G | Q | | ✓ | ✓ |
| `device.screenshot`, `device.uiTree` | G | Q | | | ✓ |
| `device.tapElement`, `device.inputText` | G | C | | | ✓ |
| `emulator.list` | G | Q | | | |
| `emulator.start`, `emulator.wipe` | G | C | | ✓ | |
| `emulator.stop` | G | C | | | |
| `sdk.status` | G | Q | | | |
| `sdk.requirements` | W | Q | | ✓ | |
| `file.list`, `file.read` | W | Q | | | |
| `file.write` | W | C | | | |
| `file.watch` | W | C | **streaming** | | |
| `git.status`, `git.diff`, `git.log`, `git.branches` | W | Q | | | |
| `shell.exec` | W | C | | ✓ | ✓ |
| `artifact.read` | G | Q | | | ✓ |
| `policy.get` | G | Q | | | |
| `policy.update` | G | C | | | |
| `operation.wait` | G | Q | | | |
| `operation.cancel` | G | C | | | |
| `events.subscribe` | G | C | **streaming** | | |
| `events.unsubscribe` | G | C | **stateful** | | |

Only three capabilities need a session. The bounded `device.logs` follow is a
long-running *stateless* call, not a stream. `operation.*` is stateless because
operations belong to the daemon, not to a session.

## Events and subscriptions

### Event envelope

```json
{
  "epoch": "d-5f1c",
  "seq": 91,
  "time": "2026-09-27T10:15:00.123Z",
  "workspace": "sample-app-3f9c2a1b7e04",
  "operation": "op-7f3a",
  "topic": "build.progress",
  "type": "task.started",
  "payload": { "task": ":app:compileDebugKotlin" }
}
```

- **One daemon-wide sequence.** `seq` is monotonic across all events of a daemon
  instance, identified by `epoch` (a new daemon starts a new epoch). Global events —
  devices, emulators, the adb server, shared sidecars — carry `workspace: null`. One
  sequence gives a total order across global and workspace events (a device vanishing
  mid-install is ordered against the install's events).
- Because subscriptions filter, a subscriber sees gaps in `seq` normally: `seq` is for
  ordering and resuming, **not** for loss detection.
- **Loss is explicit.** The core never drops silently: when a subscriber's buffer
  overflows, the dropped events are replaced by one `events.gap {fromSeq, toSeq}`
  marker, and the subscriber resyncs through queries (`workspace.describe`,
  `operation.wait`, `device.list`).
- `topic` groups events; `type` discriminates within a topic.

The **topics** are a closed list; each lands with a milestone, and only a topic that
has landed can be subscribed to (in 0.3.0: `workspace.lifecycle` and
`operation.progress`). `events.gap` is not subscribable: it is the marker every
subscription receives when it lost events.

| Topic | Types / payload | Milestone |
| --- | --- | --- |
| `workspace.lifecycle` | the new state; `{state, degradedBy?}` | 1d |
| `operation.progress` | `{done, total?, message?, phase?}` | 1d |
| `events.gap` | the loss marker `{fromSeq, toSeq}`, `seq` equal to `toSeq` | 1d |
| `file.changes` | `changes {path, changes [{path, kind, type}], truncated}` and `stopped {path, reason}` | 1j (delivered to the subscription `file.watch` creates and to no other; not subscribable, and not in the ring — see below) |
| `build.progress`, `build.output` | Gradle progress and console lines | with their first consumer, the Phase 2 UI (milestone 1g put build progress on `operation.progress`) |
| `device.logcat` | the log stream | with its first consumer, the Phase 2 Logcat panel (milestone 1i shipped `device.logs` without the topic, D18) |
| `device.screen`, `device.state` | device streams and state | with their producers, later device milestones |
| `process.lifecycle`, `process.output` | supervised sidecars | with their first consumer, the Phase 2 UI (the first sidecar shipped in 1g without them) |
| `operation.state` | operation transitions for the UI | Phase 2 |

A subscription to any other topic is rejected with `REQUEST_INVALID_PARAMS`, and
adding a topic is a minor-version change. `file.changes` is a topic a caller never
subscribes to: `file.watch` opens its subscription itself.

### Subscriptions

`events.subscribe({ "topics": ["workspace.lifecycle"], "since": { "epoch": "d-5f1c", "seq": 80 } })`
returns a `subscriptionId`. Events are delivered in `seq` order per subscription.
`events.subscribe` is a command: each call creates a subscription, so it is neither
read-only nor idempotent (milestone 1d; a query that writes is a `Kind` bug,
[mcp-tool-surface](../interfaces/mcp-tool-surface.md) §Annotations). `file.watch` creates a subscription the same way, so it is a command too
(milestone 1j settled the `Q (to revisit)` above).

- **Resume** with `since`: the core replays matching events from a bounded in-memory
  ring buffer (count and byte caps), then continues live. If `since` is older than the
  buffer, or from another epoch, delivery starts with an `events.gap` marker. Events
  are never persisted. `file.changes` is the exception: it takes a `seq` but never
  enters the ring, because a busy watch would otherwise flush the 4096 events and 4 MiB
  that other subscribers resume from. It is offered to the subscription `file.watch`
  created and to no other, and it does not accept `since`: a watch that was not running
  produced nothing to replay, so after a reconnect the client watches again and
  re-lists.
- **Bounds** (working defaults, milestone 1d, D5): the ring holds 4096 events or
  4 MiB, whichever is reached first; each subscription has its own queue of 1024
  events, and overflowing it costs that subscription a gap, not the session. On the
  WebSocket an event arrives as `{"subscriptionId", "event"}`, never before the
  response that gave the id ([http-websocket](../interfaces/http-websocket.md)
  §`GET /v1/ws`).
- Subscriptions belong to the session. Consumers unsubscribe explicitly; the core also
  cleans up on disconnect. `events.unsubscribe` of an unknown, ended or other
  session's id is a no-op returning `{}`, so no session can probe another's ids
  (milestone 1d, D7). `filter` is not accepted yet; it arrives as an optional
  parameter.

## Structured vs raw output

Default: **structured** results modelled for the operation.

Where raw output is genuinely useful, a capability accepts `"raw": true` and adds:

```json
"raw": { "stdout": "...", "stderr": "...", "exitCode": 0 }
```

`shell.exec` is the general escape hatch for anything not yet modelled. It is a
capability like any other and must be **explicitly enabled** by policy: `shell.enabled`
everywhere or `shell.roots` per project ([ADR-0032](../adr/0032-configuration-file-v1.md)),
on a trusted root. It runs `/bin/sh -c` in the root as the user, so once enabled the agent
has the user's full reach, droidyard's configuration and trust included (ADR-0032 and
ADR-0015 §6 `Amended:`; a trusted root's builds already run code the agent can write).
The command's whole process group is killed when the call ends (exit, `timeoutMs`,
cancel, workspace close, daemon shutdown) and reaped after a daemon crash; a process that
detaches into its own group or session, such as Gradle's daemon, survives. Each stream
is its last 16 KiB inline and, over that, its first 4 MiB as an ephemeral artifact; the
output is untrusted.

## Cross-cutting conventions

These apply to every capability and are (where possible) enforced by the schema.
They come from the prior-art analysis ([`../research/prior-art.md`](../research/prior-art.md)).

### Bounded output

Every list/stream result has a byte/line cap, enforced **while capturing**. When
truncated, the result carries `truncated: true` and a hint naming how to narrow
(filter, depth, smaller window). Every long-running capability is bounded in time too:
a timeout, a follow window, or a natural end.

### Empty is a fact, not a failure

A plumbing failure raises an error; only a successful call may return an empty
result. Never conflate "the listing failed" with "the listing is empty". Zero
matches carry candidates or a hint where useful.

### Typed, cause-attributed errors

See §Error model. When a result can be empty for several reasons, the capability says
which (e.g. filter matched nothing vs. cap pruned vs. genuinely none), so consumers do
not flail.

### Advertised output schemas

Capabilities declare a result schema, and the MCP adapter registers an `outputSchema`
for every tool, so consumers can rely on a typed contract — not just
`structuredContent`. For a long-running tool that schema is the **operation view**
`{operationId, capability, state, progress?, blockedBy?, result?}`, with the result
schema nested under `result`, because the call may end before the operation does.
Error results carry no structured content. The exact rule is in
[ADR-0020](../adr/0020-operation-contract.md) §Adapter projections.

### Enum spelling

Enum values are lowerCamelCase: `success | failed | cancelled`, `upToDate`,
`ephemeral`. SCREAMING_SNAKE is reserved for error codes, so a generator needs one
casing rule per kind of token.

### One name for each target

The device argument is named `device` everywhere: no `serial` alias, no implicit
"current device". The workspace is named by the envelope's `workspace`, with no
implicit "current workspace" at the capability layer.

### Discriminated tools over overloaded ones

Prefer a tagged union / one capability per action over a single tool with many
optional fields and a free-form JSON blob.

### Statefulness is declared

Each capability is `stateless`, `stateful` or `streaming`
([ADR-0010](../adr/0010-capability-statefulness.md)); the adapter enforces the
session requirement accordingly. See §Classification.

### Untrusted outputs are marked

Result fields carrying device- or app-derived data are tagged `untrusted` in their Go
result struct; the schema marks them, and the descriptor's `Untrusted` flag is
**derived** from those tags, never written
([ADR-0015](../adr/0015-untrusted-data-authorization.md) §1). Results say so. How the
mark travels on each wire is an adapter concern.

A **failed** call is marked too, but only by its handler: it carries the mark when the
handler set `Call.Untrusted` before returning its error. The descriptor's derived flag
never marks an error, because an error from a capability whose result is tagged need
not quote device text. A marked error is the case that does — its `details` may hold
candidates read from the device — so on every adapter it carries the same mark, and its
`message` ends with the fixed note that candidates are data, appended by the capability
layer once. An unmarked error carries neither, and neither does a cancelled call.
`cause` never quotes device output (§Error model: the tool, its exit status or the
decisive line of its stderr).

### Visual artifacts are references

Screenshots and streams return opaque references, not filesystem paths
([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)).

## Discovery and versioning

- `system.capabilities` returns every capability with its scope, kind, statefulness,
  long-running flag, audience, description and **availability** with the reason when
  unavailable (e.g. no `adb`); the full schema with `since` (and later `deprecated`)
  markers is `droidyard schema` / `schema/capabilities.json`.
- The API has one semantic version, reported by `system.capabilities` and the
  socket handshake. Capabilities are **not** versioned individually: a breaking change
  to one is a new name or a major version. `since` records the API version that
  introduced a capability; `deprecated` names the version and the replacement.
- Consumers (the UI in particular) adapt to what is advertised rather than
  assuming a fixed surface.

## Mapping to adapters

Adapters translate only; they add no domain logic. Projections of Start/Wait/Cancel
are fixed by [ADR-0020](../adr/0020-operation-contract.md).

| Adapter | Invocation | Long-running | Events |
| --- | --- | --- | --- |
| HTTP/WebSocket | one request endpoint carrying the envelope | `waitMs`, then `operation.wait` or events | WebSocket subscription |
| MCP | one tool per capability; `workspace` from the session binding, opened at session start | bounded wait returning the operation view, then `operation_wait` / `operation_cancel` | progress notifications during a wait; `events.*` not exposed |
| CLI | `droidyard call <domain>.<verb> --params …` over the socket | waits; Ctrl-C cancels; `--detach` | none yet: `events.*` is WebSocket-only (1d); `--follow` waits for a native event frame |

Exact wire details belong to [`interfaces/http-websocket.md`](../interfaces/http-websocket.md) and
[`interfaces/mcp-tool-surface.md`](../interfaces/mcp-tool-surface.md).

## Initial domains

| Domain | Kind | Representative capabilities | Backed by |
| --- | --- | --- | --- |
| `workspace` | cmd/query | open, describe, close | core |
| `project` | query | modules, variants | Gradle |
| `file` | cmd/query | list, read, write, watch | OS |
| `code` | query | definition, references, symbols, diagnostics, callHierarchy (post-MVP) | Kotlin LSP |
| `build` | cmd/query | assemble, tasks, run, errors, lastResult (test post-MVP) | Gradle |
| `device` | cmd/query | list, install, launch, uninstall, logs, screenshot, uiTree, tapElement, inputText | ADB |
| `emulator` | cmd/query | list, start, stop, wipe | emulator / avdmanager |
| `sdk` | query | status, requirements | SDK tools |
| `git` | query | status, diff, log, branches | git |
| `shell` | cmd | exec | OS |
| `artifact` | query | read | core |
| `policy` | cmd/query | get, update (human-only) | core |
| `system` | query | capabilities, health | core |
| `operation` | cmd/query | wait, cancel | core |
| `events` | cmd/query | subscribe, unsubscribe | core |

## Adding a capability — checklist

1. Write the descriptor and its params/result structs in the owning domain package:
   name, scope, kind, statefulness, long-running, errors, availability, `since`. Tag
   device- or app-derived result fields `untrusted`; the descriptor's `Untrusted` flag
   is derived from them.
2. Implement the handler; honour `ctx` cancellation.
3. Regenerate the JSON Schema golden file and review the diff.
4. Ensure it returns structured data; add `raw` only if justified.
5. If it introduces a new topic, document the topic and its event types.

## Open questions

- **Batching**: whether to support multiple invocations per round trip.
- **Config file spec**: settled by [ADR-0032](../adr/0032-configuration-file-v1.md)
  (version 1: `trust`, `devices`, `shell`, `gradle`, `tools`; `policy.get`/`update` for
  humans only), as amended by its Q2 line: the HTTP port, the environment source, idle
  periods, build retention and timeouts are deferred to a later version.

Settled since the review: `shell.exec` is **off by default**
([ADR-0015](../adr/0015-untrusted-data-authorization.md) §7) and policy is **never**
exposed over MCP (§6); authorization lives in the core, while each adapter owns its
transport fence ([`interfaces/http-websocket.md`](../interfaces/http-websocket.md)).
