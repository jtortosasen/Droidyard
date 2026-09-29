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
     HTTP / WebSocket     MCP (stdio shim)            CLI (mydev call)
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
| **Session** | One adapter connection — an MCP shim, a CLI invocation, a WebSocket. Owns stateful resources. |
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
  operation names what it waits behind in `blockedBy`.
- `result` is present iff `completed`; `error` iff `failed` or `cancelled`.
- A request rejected before it starts (unknown capability, bad params, missing
  `workspace`, policy denial) has an `error` and **no** `operation`.
- **A failed build is `completed`**, with `outcome: FAILED` in its result. `failed` is
  reserved for calls that could not be performed.

### Error model

One shape everywhere — the envelope, MCP (`isError: true`), CLI output:

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

**Versioning of codes.** Error codes are part of the published schema: each descriptor
lists the codes it may return, and the golden file records them. Following the API's
semantic version:

- a code, once published, is never renamed or given a new meaning;
- new codes may be added in a minor version, so consumers must treat an unknown code as
  a generic failure of that capability;
- a code is removed only in a major version;
- the shape of a code's `details` evolves additively under the same rules.

Codes carry no version suffix of their own.

**Cross-cutting codes** every adapter may see, whatever the capability:

| Code | Meaning |
| --- | --- |
| `SESSION_REQUIRED` | A stateful/streaming capability was called without a session ([ADR-0010](../adr/0010-capability-statefulness.md)) |
| `ARTIFACT_NOT_FOUND` / `ARTIFACT_EXPIRED` | The reference is unknown, or ephemeral and past its TTL ([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)) |
| `WORKSPACE_UNTRUSTED` | The workspace opened in restricted mode; the capability touches project code ([ADR-0015](../adr/0015-untrusted-data-authorization.md)) |
| `POLICY_SHELL_DISABLED` | `shell.exec` is not enabled ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §7) |

### Operations and cancellation

- `operation.wait {operationId, timeoutMs?}` returns the same shape as the response
  envelope; `timeoutMs: 0` is a status poll.
- `operation.cancel {operationId}` requests cooperative cancellation: idempotent,
  immediate for `queued`, a no-op on terminal operations. There is **no**
  domain-specific cancel; for builds, the build id is the operation id.
- Operation ids are daemon-wide. Any session — or none — can wait on or cancel any
  operation, and operations outlive the session that started them
  ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)).
- Terminal operations stay waitable for 15 minutes (at least the last 200).

### Idempotency

- Queries are idempotent by definition.
- Commands declare whether they are idempotent (descriptor `Idempotent`).
- Commands that accept an `idempotencyKey` (descriptor `AcceptsIdempotencyKey`) dedupe
  on `(workspace or global, capability, key)` within the retention window: a repeat
  returns the existing operation instead of starting new work; the same key with
  different params is `IDEMPOTENCY_KEY_CONFLICT`.

## Scope: global vs workspace

A capability is **workspace-scoped** when it reads or writes workspace state; otherwise
it is **global**. Devices, emulators, the SDK installation and the core itself are
global ([workspace-state](workspace-state.md) §Devices are not workspace-owned).

`device.install` is the deliberate exception within `device.*`: it resolves build
outputs, which belong to a workspace. The device is still named explicitly.

Adapters supply `workspace`: the UI from its own selection, the CLI from
`--workspace` or the cwd, and MCP from the session's binding, so agents never pass it
([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)).

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
| `file.watch` | W | Q | **streaming** | | |
| `git.status`, `git.diff`, `git.log`, `git.branches` | W | Q | | | |
| `shell.exec` | W | C | | ✓ | ✓ |
| `artifact.read` | G | Q | | | ✓ |
| `policy.get` | G | Q | | | |
| `policy.update` | G | C | | | |
| `operation.wait` | G | Q | | | |
| `operation.cancel` | G | C | | | |
| `events.subscribe` | G | Q | **streaming** | | |
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

Representative topics: `build.progress`, `build.output`, `device.logcat`,
`device.screen`, `device.state`, `workspace.lifecycle`, `process.lifecycle`,
`operation.state`.

### Subscriptions

`events.subscribe({ "topics": ["device.logcat"], "filter": { "device": "emulator-5554" }, "since": { "epoch": "d-5f1c", "seq": 80 } })`
returns a `subscriptionId`. Events are delivered in `seq` order per subscription.

- **Resume** with `since`: the core replays matching events from a bounded in-memory
  ring buffer (count and byte caps), then continues live. If `since` is older than the
  buffer, or from another epoch, delivery starts with an `events.gap` marker. Events
  are never persisted.
- Subscriptions belong to the session. Consumers unsubscribe explicitly; the core also
  cleans up on disconnect.

## Structured vs raw output

Default: **structured** results modelled for the operation.

Where raw output is genuinely useful, a capability accepts `"raw": true` and adds:

```json
"raw": { "stdout": "...", "stderr": "...", "exitCode": 0 }
```

`shell.exec` is the general escape hatch for anything not yet modelled. It is a
capability like any other and must be **explicitly enabled** by policy.

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

Capabilities declare a result schema, and the MCP adapter registers it as
`outputSchema`, so consumers can rely on a typed contract — not just
`structuredContent`.

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

Capabilities whose results carry device- or app-derived data are flagged in their
descriptor ([ADR-0015](../adr/0015-untrusted-data-authorization.md)), and results say
so. How the mark travels on each wire is an adapter concern.

### Visual artifacts are references

Screenshots and streams return opaque references, not filesystem paths
([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)).

## Discovery and versioning

- `system.capabilities` returns every capability with its schema, scope, kind,
  statefulness, long-running flag, `since`/`deprecated` markers, and **availability**
  (e.g. whether an emulator or the Gradle sidecar can run).
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
| MCP | one tool per capability; `workspace` from the session binding | bounded wait, then `operation_wait` / `operation_cancel` | progress notifications during a wait; `events.*` not exposed |
| CLI | `mydev call <domain>.<verb> --params …` over the socket | waits; Ctrl-C cancels; `--detach` | streamed to stderr or `--follow` |

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
   name, scope, kind, statefulness, long-running, untrusted, errors, availability,
   `since`.
2. Implement the handler; honour `ctx` cancellation.
3. Regenerate the JSON Schema golden file and review the diff.
4. Ensure it returns structured data; add `raw` only if justified.
5. If it introduces a new topic, document the topic and its event types.

## Open questions

- **Batching**: whether to support multiple invocations per round trip.
- **Config file spec**: policy, JVM discovery, idle timeouts and paths are all
  configured, but no document yet defines the file. It is needed before Phase 1.

Settled since the review: `shell.exec` is **off by default** and policy is
**never** exposed over MCP ([ADR-0015](../adr/0015-untrusted-data-authorization.md)
§7); authorization lives in the core, while each adapter owns its transport fence
([`interfaces/http-websocket.md`](../interfaces/http-websocket.md)).
