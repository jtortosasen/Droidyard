# ADR-0020: Invocation contract — Start, Wait, Cancel, and explicit scope

- Status: Accepted
- Date: 2026-09-29

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
  decoding, a missing or superfluous `workspace`, a policy denial — with a typed error
  and **no operation**. Otherwise it returns an id without waiting. Authorization is
  re-checked immediately before the handler runs
  ([ADR-0015](0015-untrusted-data-authorization.md)); a denial there fails the
  operation.
- **`Wait`** returns `Outcome{state, result | error, progress, blockedBy?}`. A deadline
  in the past is a status snapshot. Waiting has no side effects: abandoning a wait does
  not touch the operation, and any number of callers may wait on the same one.
- **`Cancel`** cancels the handler's `ctx`; a queued operation is cancelled at once;
  cancelling a terminal operation is a no-op.
- **States**: `queued → running → completed | failed | cancelled`. `completed` means
  the call worked — including a build whose outcome is `FAILED`; a failed build is
  information, not an error. `failed` means the call could not be performed and carries
  a typed error. `cancelled` carries whatever partial result the capability defines.
- **`queued`** is how contention is reported: an operation waiting for a per-resource
  lock (a build root, a device) says what it waits behind in `blockedBy`.
- **Retention**: terminal operations stay waitable for 15 minutes and at least the last
  200. Domain registries (builds) keep their own, longer history.
- `Invoke = Start + Wait(no deadline)` is a helper for tests and internal callers, not a
  second path.

**The caller decides how long to block**, through the deadline. The descriptor's
`LongRunning` flag only tells adapters which capabilities may outlive a short wait. No
handler decides whether to "run in the background".

### Operation capabilities

- `operation.wait {operationId, timeoutMs?}` → the `Outcome`; `timeoutMs: 0` polls.
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
- `workspace` is an envelope field, not a parameter: schemas stay clean, and it mirrors
  the `workspace` field that events already carry. It is the workspace counterpart of
  the `device` rule — no implicit target at the capability layer.
- Adapters supply it: the UI from its own selection; the CLI from `--workspace`, else
  the cwd's root; **MCP from the session binding** ([ADR-0019](0019-process-topology-daemon-and-shims.md)),
  so agents never see it.

### Adapter projections

| Adapter | A call is | If still running | Cancel |
| --- | --- | --- | --- |
| MCP | `Start` + `Wait(budget)` | successful result `{operationId, state, progress}`, text says "call `operation_wait`" | `operation_cancel`, or `notifications/cancelled` (below) |
| HTTP | `Start` + `Wait(waitMs)` from the envelope | `{operation: {id, state}}`; progress over WebSocket | `operation.cancel` |
| CLI | `Start` + `Wait(∞)`, progress on stderr | `--detach` prints the id | Ctrl-C → `Cancel` |

MCP specifics:

- Capabilities not flagged `LongRunning` are waited on fully; each bounds itself.
- `LongRunning` ones get a **wait budget**: default 20 s, configurable per server
  (`mydev mcp --wait 45s`), always below the client's tool timeout. Most device calls
  and warm builds finish inside it, and the agent sees a plain result.
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
different params is `IDEMPOTENCY_KEY_CONFLICT`. This makes a retry after a lost
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
