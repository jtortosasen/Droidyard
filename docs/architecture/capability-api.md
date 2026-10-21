# Capability API

The **internal contract of the core**. Every consumer (the web UI, MCP agents and the
CLI) is a thin adapter over it ([ADR-0003](../adr/0003-unified-capability-api.md)).
This document holds what code cannot say: why, and the rules that hold across
capabilities. The contract itself (names, parameters, results, error codes and their
meanings, versions) is `schema/capabilities.json`, the descriptors in
`internal/capability` and the domain packages, and `internal/capability/error.go`. The
wire mappings are in `interfaces/`. The invocation contract is fixed by
[ADR-0020](../adr/0020-operation-contract.md).

## Purpose

One typed, semantic surface for every operation the core can perform: a capability is
implemented once, the UI and agents see identical semantics, results are **structured**
rather than stdout to re-parse, and adapters stay protocol-only.

## Naming and namespaces

`<domain>.<verb>`, lower camel case, verb last. **Never an underscore**, so the MCP
mapping (dot to underscore) is reversible; the registry rejects any other name. Domains
are fixed by the capability registry, not invented ad hoc by adapters.

## The registry is the single source of truth

Capabilities are **defined in Go**, as a descriptor plus params and result structs, and
the JSON Schema is **generated** from them and committed as a golden file
([ADR-0016](../adr/0016-capability-schema-in-go.md)). The golden file feeds the MCP tool
definitions at runtime and the TypeScript client types in Phase 2
([ADR-0008](../adr/0008-frontend-react.md)).

## Invocation model

The core offers `Start` (an operation id), `Wait(opID, deadline)` and `Cancel(opID)`,
and adapters compose them. **The caller decides how long to block**; no handler decides
to "run in the background". Every invocation is an operation, queries included.

### Request envelope

- `workspace` is **required iff the capability is workspace-scoped**, and rejected
  otherwise. It is a workspace id or an absolute project root (canonicalised to the id).
- `waitMs` is how long to wait before returning a still-running operation. Omitted: wait
  to completion.
- The fields are `capability.RequestEnvelope`.

### Response envelope

- `operation.state` is one of the `capability.State` values. A `queued` operation names
  what it waits behind in `blockedBy {operationId, capability}`.
- `result` is present iff `completed`; `error` iff `failed` or `cancelled` (the latter
  with `OPERATION_CANCELLED`). Never both.
- A request rejected before it starts (unknown capability, bad params, missing
  `workspace`, a workspace not open, a policy denial that needs no I/O) has an `error` and
  **no** `operation`.
- **A failed build is `completed`**, with `outcome: "failed"` in its result. `failed` is
  reserved for calls that could not be performed. A file batch whose items failed is
  `completed` the same way, each item carrying its code
  ([ADR-0053](../adr/0053-file-operations.md) §2).
- The fields are `capability.ResponseEnvelope`.

### Error model

One shape everywhere: the envelope, MCP (`isError: true`, the object as JSON text) and
CLI output (`capability.Error`). `code` is the only field consumers branch on; `message`
is non-normative and never parsed; `nextStep` names capabilities by canonical name.

A code is `<DOMAIN>_<REASON>`. The domain is the one that owns the failure, not
necessarily the one called (`device.install` returns `BUILD_OUTPUT_STALE`), or one of the
reserved prefixes `REQUEST`, `SESSION` and `DAEMON`; the list is closed, and a new prefix
is a schema change. The meaning of each cross-cutting code is a doc comment on its
constant in `internal/capability/error.go`.

**Versioning of codes.** Codes are part of the published schema: each descriptor lists
the codes the capability itself may return, and the golden file records them. Following
the API's semantic version:

- a code, once published, is never renamed or given a new meaning;
- new codes may be added in a minor version, so consumers must treat an unknown code as a
  generic failure of that capability;
- a code is removed only in a major version;
- the shape of a code's `details` evolves additively under the same rules.

Codes carry no version suffix of their own.

### Operations and cancellation

- `operation.wait {operationId, timeoutMs?}` returns the response envelope's shape;
  `timeoutMs: 0` is a status poll. Its `operation`, `result` and `error` are the waited
  operation's own, passed through unchanged (`OPERATION_CANCELLED` when it ended
  cancelled, the handler's typed error when it failed). Its descriptor lists
  `OPERATION_NOT_FOUND` and `OPERATION_CANCELLED`, so any other code a consumer sees is
  the waited operation's.
- `operation.cancel {operationId}` is cooperative cancellation: idempotent, immediate for
  `queued`, a no-op on terminal operations. There is **no** domain-specific cancel; for
  builds, the build id is the operation id.
- Operation ids are daemon-wide. Any session, or none, can wait on or cancel any
  operation, and operations outlive the session that started them
  ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)).
- Retention of terminal operations and the **queue bound** for short calls are
  [ADR-0020](../adr/0020-operation-contract.md) §Core primitives; the bound keeps a
  `device.tapElement` queued behind a `device.install` from outliving a client timeout.

### Idempotency

Queries are idempotent by definition; commands declare it (descriptor `Idempotent`).
An `idempotencyKey` deduping repeats is designed (ADR-0020 §Idempotency keys) but not built
(the register).

## Scope: global vs workspace

A capability is **workspace-scoped** when it reads or writes workspace state; otherwise
it is **global**. Devices, emulators, the SDK installation and the core itself are
global ([workspace-state](workspace-state.md) §Devices are not workspace-owned).
`device.install` is the deliberate exception within `device.*`: it resolves build
outputs, which belong to a workspace. The device is still named explicitly.

Adapters supply `workspace`: the UI from its own selection, the CLI from `--workspace`
or the cwd, and MCP from the session's binding, so agents never pass it
([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)). The target must be
**open**: the core never opens a workspace implicitly, and any other workspace-scoped
call on a root that is not open gets `WORKSPACE_NOT_OPEN`. The MCP adapter opens its bound
root once, when the session starts, and does not reopen it after a `workspace.close` by
any client ([ADR-0020](../adr/0020-operation-contract.md) §Scope and workspace
addressing).

## Events and subscriptions

### Event envelope

The fields are `event.Event`.

- **One daemon-wide sequence.** `seq` is monotonic across all events of a daemon
  instance, identified by `epoch` (a new daemon starts a new epoch). Global events
  (devices, emulators, the adb server, shared sidecars) carry `workspace: null`. One
  sequence gives a total order across global and workspace events: a device vanishing
  mid-install is ordered against the install's events.
- Because subscriptions filter, a subscriber sees gaps in `seq` normally: `seq` is for
  ordering and resuming, **not** for loss detection.
- **Loss is explicit.** The core never drops silently: when a subscriber's buffer
  overflows, the dropped events are replaced by one `events.gap {fromSeq, toSeq}` marker,
  and the subscriber resyncs through queries (`workspace.describe`, `operation.wait`,
  `device.list`). `events.gap` is not subscribable: every subscription that lost events
  receives it.
- `topic` groups events; `type` discriminates within a topic. The topics are a closed
  list, only a topic that has landed can be subscribed to (`subscribable` in
  `internal/event/session.go`), and any other is `REQUEST_INVALID_PARAMS`. Adding a topic
  is a minor-version change; the topics that have no producer yet are a register row.

### Subscriptions

`events.subscribe` is a command: each call creates a subscription, so it is neither
read-only nor idempotent (a query that writes is a `Kind` bug,
[mcp](../interfaces/mcp.md) §Annotations). `file.watch` creates a subscription the same
way, so it is a command too. Subscriptions belong to the session: consumers unsubscribe
explicitly, and the core also cleans up on disconnect. `events.unsubscribe` of an
unknown, ended or other session's id is a no-op returning `{}`, so no session can probe
another's ids.

- **Resume** with `since`: the core replays matching events from a bounded in-memory ring
  (count and byte caps in `internal/event/event.go`), then continues live. If `since` is
  older than the buffer, or from another epoch, delivery starts with an `events.gap`
  marker. Events are never persisted.
- `file.changes`, `git.changes`, `device.logcat` ([ADR-0041](../adr/0041-device-watchlogs-stream.md)),
  `device.screen` ([ADR-0078](../adr/0078-screen-watch-and-endpoint.md)) and `device.uiEvents`
  ([ADR-0112](../adr/0112-ui-events-stream-and-panel.md)) are the exceptions:
  each takes a `seq` but never enters the ring, which a busy stream would flush for other
  subscribers. It is offered only to the subscription that `file.watch`, `git.watch`,
  `device.watchLogs`, `device.watchScreen` or `device.watchUiEvents` created, is not subscribable and accepts no `since`:
  after a reconnect the client starts the stream again. A stream ended by its own subscription
  publishes nothing more on it, since no one is left to receive it; the live view's video is not
  an event but `GET /v1/screen`'s binary messages. `operation.state`
  ([ADR-0043](../adr/0043-operation-watch.md)) is ring-less, offered to **every**
  `operation.watch` subscription, and `build.output`
  ([ADR-0044](../adr/0044-build-watchoutput.md)) to every `build.watchOutput` subscription of
  the build's workspace, `workspace.state`
  ([ADR-0045](../adr/0045-project-start-flow.md)) to every `workspace.watch` subscription, and
  `session.state` ([ADR-0083](../adr/0083-session-watch-and-panel-by-project.md)) to every
  `session.watch` subscription.
- Overflowing a subscription's own queue costs that subscription a gap, not the session.
  On the WebSocket an event arrives as `{"subscriptionId", "event"}`, never before the
  response that gave the id ([http-websocket](../interfaces/http-websocket.md)
  §`GET /v1/ws`).

## Structured vs raw output

Default: **structured** results modelled for the operation. Where raw output is
genuinely useful, a capability may accept `"raw": true` and add `raw {stdout, stderr,
exitCode}`. `shell.exec` is the general escape hatch for anything not yet modelled: a
capability like any other, **off unless policy enables it**
([ADR-0015](../adr/0015-untrusted-data-authorization.md) §7,
[ADR-0032](../adr/0032-configuration-file-v1.md)), and once on the agent has the user's
full reach.

## Cross-cutting conventions

These apply to every capability and are, where possible, enforced by the schema. They
come from the prior-art analysis ([`../research/prior-art.md`](../research/prior-art.md)).

### Bounded output

Every list or stream result has a byte or line cap, enforced **while capturing**. When
truncated, the result carries `truncated: true` and a hint naming how to narrow (filter,
depth, smaller window). Every long-running capability is bounded in time too: a timeout,
a follow window, or a natural end.

### Empty is a fact, not a failure

A plumbing failure raises an error; only a successful call may return an empty result.
Never conflate "the listing failed" with "the listing is empty". Zero matches carry
candidates or a hint where useful. When a result can be empty for several reasons, the
capability says which (filter matched nothing, cap pruned, genuinely none), so consumers
do not flail.

### Advertised output schemas

Capabilities declare a result schema, and the MCP adapter registers an `outputSchema`
for every tool. For a long-running tool that schema is the **operation view**, with the
result schema nested under `result`, because the call may end before the operation does.
Error results carry no structured content
([ADR-0020](../adr/0020-operation-contract.md) §Adapter projections).

### Enum spelling

Enum values are lowerCamelCase (`success | failed | cancelled`, `upToDate`,
`ephemeral`). SCREAMING_SNAKE is reserved for error codes, so a generator needs one
casing rule per kind of token.

### One name for each target

The device argument is named `device` everywhere: no `serial` alias, no implicit
"current device". The workspace is named by the envelope's `workspace`, with no implicit
"current workspace" at the capability layer.

### Discriminated tools over overloaded ones

Prefer a tagged union, or one capability per action, over a single tool with many
optional fields and a free-form JSON blob.

### Statefulness is declared

Each capability is `stateless`, `stateful` or `streaming`
([ADR-0010](../adr/0010-capability-statefulness.md)); the adapter enforces the session
requirement. A streaming capability is not a separate kind. Only a session that can
carry state and events, today the WebSocket, owns stateful resources. The bounded
`device.logs` follow is a long-running *stateless* call, not a stream, and `operation.*`
is stateless because operations belong to the daemon, not to a session.

### Untrusted outputs are marked

Result fields carrying device- or app-derived data are tagged `untrusted` in their Go
result struct; the schema marks them, and the descriptor's `Untrusted` flag is
**derived** from those tags, never written
([ADR-0015](../adr/0015-untrusted-data-authorization.md) §1). How the mark travels on
each wire is an adapter concern.

A **failed** call is marked only by its handler (`Call.Untrusted`), never by the
descriptor's derived flag, because an error from a capability whose result is tagged
need not quote device text. A marked error is the case that does (its `details` may hold
candidates read from the device), so on every adapter it carries the same mark and its
`message` ends with the fixed note that device text in it is data, appended once by the
capability layer. An unmarked or cancelled call carries neither. `cause` never quotes
device output.

### Visual artifacts are references

Screenshots and streams return opaque references, not filesystem paths
([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)).

## Discovery and versioning

- `system.capabilities` returns every capability with its availability and the reason
  when unavailable (for example, no `adb`); the full schema, with `since` and later
  `deprecated` markers, is `droidyard schema` and `schema/capabilities.json`.
- The API has one semantic version, reported by `system.capabilities` and the socket
  handshake. Capabilities are **not** versioned individually: a breaking change to one is
  a new name or a major version. `since` records the API version that introduced a
  capability; `deprecated` names the version and the replacement.
- Consumers (the UI in particular) adapt to what is advertised rather than assuming a
  fixed surface.

## Mapping to adapters

Adapters translate only; they add no domain logic. The projections of Start, Wait and
Cancel are fixed by [ADR-0020](../adr/0020-operation-contract.md) §Adapter projections;
the wire details are in [`http-websocket.md`](../interfaces/http-websocket.md) and
[`mcp.md`](../interfaces/mcp.md).
