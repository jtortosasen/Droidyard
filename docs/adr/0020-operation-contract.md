# ADR-0020: Invocation contract — Start, Wait, Cancel, and explicit scope

- Status: Accepted
- Date: 2026-09-29
- Amended: 2026-09-29. What changed: `cancelled` carries an error, not a result; the
  queue bound for short calls; `WORKSPACE_NOT_OPEN`; the MCP operation view and
  error results under `outputSchema`; lowercase outcomes; `REQUEST_IDEMPOTENCY_CONFLICT`
  (verification pass). The queue bound and the MCP session opening its bound root were
  **ratified on 2026-09-29**.
- Amended: 2026-10-02 — `operation.wait`'s descriptor lists `OPERATION_CANCELLED` beside
  `OPERATION_NOT_FOUND`: the waited operation's own error is forwarded (cross-family
  review of milestone 1b).
- Amended: 2026-10-03 — pointers to [ADR-0026](0026-workspace-addressing-invalid-roots.md)
  and [ADR-0027](0027-workspace-close-and-reopen.md): the failure codes of
  `workspace.open` and the close/reopen rules.
- Amended: 2026-10-03 (second pass) — the bound-open failure returns `WORKSPACE_NOT_OPEN`
  ([ADR-0026](0026-workspace-addressing-invalid-roots.md)).
- Amended: 2026-10-04 — a policy denial that needs device state to decide (a device's
  kind needs `adb devices -l`) is decided at the re-check before the handler, so it
  fails the operation instead of rejecting at `Start`; `Start` still does no I/O
  (owner decision, milestone 1e, 2026-10-04, Q2).
- Amended: 2026-10-04 — a retained result that carries bulk bytes (`artifact.read`'s
  `data`) keeps its metadata and drops the bytes once delivered, so retention never holds
  what the artifact store's budget and TTL already bound, except a read whose client left before delivery, kept until retention drops it (delegated decision, milestone
  1e, step 7 verification).
- Amended: 2026-10-06 (plan m1f D21, Delegated) — a command whose side effect has
  already happened on the device when Cancel arrives (`device.tapElement` after its tap)
  ends `completed`, reporting the effect with `expect.reason: "cancelled"`: an error would
  invite a retry that repeats the effect. Cancel before the effect still ends `cancelled`.

## Context

The invocation contract was described three incompatible ways:

- `capability-api.md`: commands return an operation handle immediately; MCP polls
  through `operation_status` / `operation_cancel`.
- `core-runtime.md`: every invocation is an operation, and a long command "may run in
  the background … or block", without saying who decides.
- `mcp-tool-surface.md`: `operation.*` is not exposed; calls block and stream progress.
- `gradle.md` adds a separate `build.cancel`, and `build.assemble` "returns a build id
  immediately".

Pure blocking does not survive real MCP clients: tool timeouts around 60 s are common,
progress only flows when the client sends a `progressToken`, progress does not always
reset the timeout, and a timeout sends `notifications/cancelled` — which, mapped to a
core cancel, kills the build. Pure async doubles the round trips of calls that finish
in a second, and agent steps are exactly what the MVP hypothesis measures.

The contract also never said **which workspace** a call targets, although the core
hosts several ([ADR-0019](0019-process-topology-daemon-and-shims.md)).

## Decision

### Core primitives

```go
Start(ctx, Request) (OpID, error)                      // validate, authorize, record, schedule
Wait(ctx, OpID, deadline time.Time) (Outcome, error)   // terminal state or deadline, whichever first
Cancel(OpID) error                                     // cooperative, idempotent
```

- **Every invocation is an operation** — queries included; they simply finish at once.
  One record type gives one trace format (the Phase 2 same-primitive check reads it).
  `operation.*` calls themselves are not recorded.
- **`Start`** rejects synchronously — unknown capability, params that fail strict
  decoding, a missing or superfluous `workspace`, a workspace that is not open, a
  policy denial that needs no I/O — with a typed error and **no operation**. Otherwise it returns an id
  without waiting. Authorization is re-checked immediately before the handler runs
  ([ADR-0015](0015-untrusted-data-authorization.md)); a denial there fails the
  operation. A denial that needs device state (`DEVICE_NOT_ALLOWED`) is decided only
  there (2026-10-04).
- **`Wait`** returns `Outcome{state, result | error, progress, blockedBy?}`. A deadline
  in the past is a status snapshot. Waiting has no side effects: abandoning a wait does
  not touch the operation, and any number of callers may wait on the same one.
- **`Cancel`** cancels the handler's `ctx`; a queued operation is cancelled at once;
  cancelling a terminal operation is a no-op.
- **States**: `queued → running → completed | failed | cancelled`. A terminal
  operation carries a `result` iff `completed`, and an `error` iff `failed` or
  `cancelled` — never both.
  - `completed` means the call worked — including a build whose outcome is `failed`; a
    failed build is information, not an error.
  - `failed` means the call could not be performed; it carries a typed error.
  - `cancelled` carries `OPERATION_CANCELLED`. Partial work that a domain keeps is
    read through that domain, not through the operation: a cancelled build is a build
    record with outcome `cancelled`, and its id is the operation id.
- **`queued`** is how contention is reported: an operation waiting for a per-resource
  lock (a build root, a device, an AVD) names the operation it waits behind in
  `blockedBy {operationId, capability}`.
- **Queue bound.** An operation **not** flagged `LongRunning` that is still `queued`
  after 20 s (working default, configurable) fails with `OPERATION_QUEUE_TIMEOUT`
  (`details: {blockedBy, waitedMs}`; nextStep: wait on `blockedBy`, then retry). It
  never ran, so the retry is safe. `LongRunning` operations have no queue bound beyond
  their domain's queue cap (e.g. `BUILD_QUEUE_FULL`); callers see `queued` through
  their bounded wait.
- **Retention**: terminal operations stay waitable for 15 minutes and at least the last
  200. Domain registries (builds) keep their own, longer history. A retained result drops
  bulk bytes once delivered (`artifact.read`'s `data`): a later wait returns the
  metadata, and the caller re-reads the reference (2026-10-04).
- `Invoke = Start + Wait(no deadline)` is a helper for tests and internal callers, not a
  second path.

**The caller decides how long to block**, through the deadline. The descriptor's
`LongRunning` flag only tells adapters which capabilities may outlive a short wait. No
handler decides whether to "run in the background".

### Operation capabilities

- `operation.wait {operationId, timeoutMs?}` → the `Outcome`; `timeoutMs: 0` polls.
  The outcome is the waited operation's own, so the error it carries may be that
  operation's (`OPERATION_CANCELLED` included); the descriptor lists that code and
  `OPERATION_NOT_FOUND`.
- `operation.cancel {operationId}`.
- Both are global and stateless: operation ids are daemon-wide, so any session — or
  none — can wait on or cancel any operation.
- **No domain-specific cancel.** `build.cancel` is removed. For builds, the build id
  *is* the operation id.

### Scope and workspace addressing

- Every capability is **global** or **workspace-scoped** (descriptor `Scope`). A
  capability is workspace-scoped when it reads or writes workspace state.
- The request envelope carries `workspace`, **required iff the capability is
  workspace-scoped** and rejected otherwise. Its value is a workspace id or an absolute
  project root; the core canonicalises a root to its deterministic id
  ([workspace-state](../architecture/workspace-state.md) §Identity). `workspace.open` is
  therefore workspace-scoped too: its target is the root it opens.
- **A workspace is never opened implicitly.** A workspace-scoped call on a root that is
  not open is rejected at `Start` with `WORKSPACE_NOT_OPEN` (nextStep:
  `workspace.open`). Only `workspace.open` and `workspace.close` accept a root that is
  not open; closing one is a no-op.
- `workspace.close` affects every session: it cancels the workspace's operations (their
  waiters see `cancelled`), and later calls get `WORKSPACE_NOT_OPEN` until some client
  opens it again.
- A target that `workspace.open` cannot validate, and what `close` guarantees while it
  drains, are fixed by [ADR-0026](0026-workspace-addressing-invalid-roots.md) and
  [ADR-0027](0027-workspace-close-and-reopen.md).
- `workspace` is an envelope field, not a parameter: schemas stay clean, and it mirrors
  the `workspace` field that events already carry. It is the workspace counterpart of
  the `device` rule — no implicit target at the capability layer.
- Adapters supply it: the UI from its own selection; the CLI from `--workspace`, else
  the cwd's root; **MCP from the session binding** ([ADR-0019](0019-process-topology-daemon-and-shims.md)),
  so agents never see it.

### Adapter projections

| Adapter | A call is | If still running | Cancel |
| --- | --- | --- | --- |
| MCP | `Start` + `Wait(budget)` | successful result: the operation view (below), text says "call `operation_wait`" | `operation_cancel`, or `notifications/cancelled` (below) |
| HTTP | `Start` + `Wait(waitMs)` from the envelope | `{operation: {id, state}}`; progress over WebSocket | `operation.cancel` |
| CLI | `Start` + `Wait(∞)`, progress on stderr | `--detach` prints the id | Ctrl-C → `Cancel` |

MCP specifics:

- **The bound root is opened at session start.** When the MCP session initializes,
  the adapter calls `workspace.open` for its bound root (idempotent; runs no project
  code). A failure does not fail the session: workspace tools then return
  `WORKSPACE_NOT_OPEN` ([ADR-0026](0026-workspace-addressing-invalid-roots.md)).
  If any client later closes the workspace, the binding stays, workspace tools return
  `WORKSPACE_NOT_OPEN`, and the adapter **does not reopen it by itself** — a close is a
  deliberate act; the agent may call `workspace_open`.
- Capabilities not flagged `LongRunning` are waited on fully. They are bounded by the
  queue bound plus their own timeout, which together must stay under 60 s, the
  shortest client tool timeout we target. A capability that cannot promise this is
  flagged `LongRunning`.
- `LongRunning` ones get a **wait budget**: default 20 s, configurable per server
  (`droidyard mcp --wait 45s`), always below the client's tool timeout. Most device calls
  and warm builds finish inside it, and the agent sees a plain result.
- **Output schemas.** MCP requires `structuredContent` to conform to the declared
  `outputSchema`, and a `LongRunning` tool returns either its result or a handle. So:
  - a tool not flagged `LongRunning` declares its result schema;
  - a `LongRunning` tool and `operation_wait` declare the **operation view**, one
    object with no top-level union:
    `{operationId, capability, state, progress?, blockedBy?, result?}`. `result` is
    present iff `state` is `completed`. For a `LongRunning` tool it is that
    capability's result schema, nested and inlined; for `operation_wait` it is an open
    object, and `capability` names the schema that applies;
  - an `isError: true` result (`failed` or `cancelled`, or a rejection) carries **no**
    `structuredContent`: its text is the typed error as JSON. An error is not the
    declared result. SDK clients accept a missing `structuredContent` on error results
    (to be confirmed in spike S7);
  - the text rendering is unaffected: a completed operation renders as the plain
    result.
- `operation_wait` and `operation_cancel` are MCP tools. `events.*` is not exposed.
- Progress notifications flow during any wait when the client sent a `progressToken`;
  they are a nicety, never relied on to keep a call alive.
- **`notifications/cancelled` cancels what that request started.** On the call that
  started an operation it cancels the operation (a user interrupt). On `operation_wait`
  it only abandons the wait. A client timeout on the starting call therefore still
  cancels, which the budget exists to prevent; spike S2 verifies client behaviour.
- A disconnect never cancels (ADR-0019).

### Idempotency keys

Capabilities that accept one (descriptor `AcceptsIdempotencyKey`) dedupe on
`(workspace or global, capability, key)` for the operation retention window: a repeat
returns the existing operation id instead of starting new work. The same key with
different params is `REQUEST_IDEMPOTENCY_CONFLICT`. This makes a retry after a lost
response safe — the companion of a disconnect-tolerant operation model.

## Consequences

- One contract for every adapter; `Invoke`'s semantics are settled before any code.
- Agents learn one extra pattern — wait on a returned id — stated once in the server
  instructions. Two tools join the MCP surface.
- Documents that must follow: `capability-api.md` and `core-runtime.md` (updated with
  this ADR); `interfaces/mcp-tool-surface.md` (expose `operation_wait` /
  `operation_cancel`, replace "calls block" with the bounded wait);
  `subsystems/gradle.md` (drop `build.cancel` and "returns a build id immediately");
  `roadmap/mvp.md` (`operation.status` becomes `operation.wait`).
- The capability trace falls out for free: every call is an operation record.
- `LongRunning` MCP tools advertise the operation view, not their bare result: hosts
  read `structuredContent.result`. The model reads the text rendering, which is the
  same either way.
- A short call stuck behind a long one fails fast with `OPERATION_QUEUE_TIMEOUT`
  instead of running into a client timeout that would cancel it.

## Alternatives considered

- **Pure blocking over MCP with progress** (the previous `mcp-tool-surface.md`). Breaks
  on ~60 s client timeouts, and a timeout cancels the build. Rejected.
- **Always async** — return a handle, then poll. Doubles the round trips of every fast
  call; agents pay in steps. Rejected.
- **The handler decides sync vs async.** Two behaviours per capability, invisible to
  callers — the ambiguity this ADR removes. Rejected.
- **MCP's experimental Tasks utility** (call now, fetch the result later). It would
  express this natively, but client support is too uneven today. The bounded-wait
  projection maps onto it directly when clients catch up.
- **A required `workspace` parameter in every workspace-scoped schema.** Equivalent
  semantics, but it pollutes every schema, and the MCP adapter would have to strip it
  per tool. The envelope field keeps schemas about the capability itself.
- **A session-bound "current workspace" at the capability layer.** Implicit and
  mutable — exactly what the `device` rule forbids. Binding is confined to the MCP
  adapter, fixed for the session's lifetime, and visible in the server config.
- **Implicit open on any workspace-scoped call.** Saves one step, but it silently
  undoes a close made by the human or another client. Rejected; the MCP adapter opens
  its bound root once, at session start, which gives agents the same convenience.
- **Returning a handle for any call still `queued` after the budget**, not only for
  `LongRunning` ones. Every tool would then have to advertise the operation view, and
  every short call would lose its plain result schema. Rejected for the queue bound.
- **No MCP output schema for `LongRunning` tools**, or **omitting structured content
  while running.** The first drops the typed contract exactly for builds; the second
  violates the declared schema (and SDK clients reject a successful result without
  structured content). Rejected for the operation view.
