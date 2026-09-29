# Core Runtime

How the Go core is organised. **Deliberately short**: it fixes only what must be
decided before writing code. Everything that is better decided with code in front of
us stays open, and is listed at the end.

## What it encodes

- **One binary, one daemon, many goroutines.** `mydev serve` is the core; `mydev mcp`
  and `mydev call` are shims that attach to it over a Unix socket, and `mydev ui`,
  `mydev trust <path>`, `mydev status` and `mydev stop` complete the CLI
  ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)). Sidecars are separate
  processes ([ADR-0017](../adr/0017-gradle-execution-strategy.md)); nothing else is.
- **Capabilities are the product; adapters are edges**
  ([ADR-0003](../adr/0003-unified-capability-api.md)).
- **The core owns state** — workspaces, operations, build registry, artifacts
  ([workspace-state](workspace-state.md), [ADR-0012](../adr/0012-visual-artifacts-as-refs.md),
  [ADR-0021](../adr/0021-persistence-format-and-location.md)).
- **Sidecars are supervised children** with an explicit lifecycle
  ([process-lifecycle](process-lifecycle.md)).

## Package layout

```text
cmd/mydev/                 entrypoint: subcommands serve | mcp | call | status | stop

internal/
  daemon/                  single-instance lock, socket listener, handshake, sessions
  capability/              registry, descriptors, operations (Start/Wait/Cancel), typed errors
  schema/                  Go → JSON Schema generation and golden files
  workspace/               workspace model, lifecycle, durable configuration
  store/                   durable JSON state, atomic writes, pid records (ADR-0021)
  process/                 supervisor: descriptors, lifecycle, no-orphans
  event/                   event bus, daemon-wide sequence, replay ring, subscriptions
  artifact/                single artifact store; URI-shaped refs; eviction
  policy/                  authorization boundary, allowlists, live configuration
  platform/                OS specifics: paths, signals, process groups, start times
  domain/
    project/               project, modules, variants
    build/                 Gradle: client of the TAPI sidecar
    device/                adb layer and device capabilities
    emulator/              AVD lifecycle
    sdk/                   SDK inspection
    file/                  filesystem
    git/                   later

adapters/
  mcp/                     MCP adapter, inside the daemon; `mydev mcp` only relays stdio
  httpapi/                 HTTP + WebSocket for the web UI
  cli/                     `mydev call <capability>`: native envelope over the socket

sidecar/                   Kotlin TAPI sidecar (separate module, own build)
```

`internal/` because this is an application, not a library — the layout should say so.

## The capability registry

The central abstraction. A capability is a Go value, registered explicitly by its
domain package at startup — **no reflection magic over the codebase**, so the set of
capabilities is greppable and reviewable.

```go
type Descriptor struct {
    Name         string        // "device.tapElement"; ^[a-z]+\.[a-z][A-Za-z0-9]*$, never "_"
    Kind         Kind          // Query | Command
    Scope        Scope         // Global | Workspace — whether the envelope's workspace is required
    Statefulness Statefulness  // Stateless | Stateful | Streaming (ADR-0010)
    LongRunning  bool          // may outlive an adapter's wait budget (ADR-0020)
    Idempotent   bool          // repeating is safe and equivalent (queries: always)
    AcceptsIdempotencyKey bool // dedupes retries on idempotencyKey (ADR-0020)
    Audience     Audience      // All | Human — Human is never exposed over MCP (policy.*)
    Params       any           // struct instance; schema generated from it
    Result       any           // struct instance; schema generated from it
    Errors       []ErrorCode   // every code it may return; part of the published schema
    Annotations  Annotations   // {Destructive *bool; OpenWorld *bool; Reason string}; nil = derived
    Availability []Requirement // needs adb, needs the sidecar, needs an SDK …
    Since        string        // API version that introduced it
    Deprecated   *Deprecation  // {since, replacement}; nil if current
    Handler      Handler       // func(ctx, *Call) (any, error); Call carries workspace, params, progress
}
```

Kind describes side effects; statefulness describes session needs. A "stream" is not a
kind: it is `Statefulness: Streaming`. There is no per-capability version — see
[capability-api](capability-api.md) §Discovery and versioning.

The registry is the **single source** for: dispatch, JSON Schema generation, MCP tool
definitions, CLI help, `system.capabilities`, and the capability tables in the docs.
If it is not in the registry, it does not exist.

## Invocation

Three primitives ([ADR-0020](../adr/0020-operation-contract.md)):

```go
Start(ctx context.Context, req Request) (OpID, error)          // Request{Capability, Workspace, Params, IdempotencyKey, Raw, Session}
Wait(ctx context.Context, id OpID, deadline time.Time) (Outcome, error)
Cancel(id OpID) error
```

- `Start` decodes params into the registered struct (unknown fields rejected), checks
  `Workspace` against `Scope`, authorizes, records an **operation**, and schedules it.
  A rejection returns a typed error and creates no operation.
- **Every invocation is an operation**, queries included. The caller decides how long
  to block by the deadline it passes to `Wait`; handlers never choose between
  "background" and "blocking".
- Errors are typed — `{code, message, cause, nextStep, details}` — never
  stringly-typed ([capability-api](capability-api.md) §Error model).
- Progress goes to the event bus, tagged with the operation id.
- **`ctx` cancellation is a contract**, not a suggestion: `Cancel` cancels the
  handler's `ctx`, and handlers must honour it and clean up.

## Events

- One event bus, one **daemon-wide monotonic sequence** per daemon epoch. Workspace
  events carry their workspace id; global events (devices, emulators, adb, shared
  sidecars) carry `workspace: null` ([capability-api](capability-api.md) §Events).
- Subscribers resume with `since` from a bounded replay ring; overflow and expired
  resumes are reported as an explicit `events.gap`, never silent.
- The bus is the **only** cross-cutting channel. Adapters subscribe and translate:
  MCP progress notifications, WebSocket frames.
- No adapter polls. No handler knows which adapter is watching.

## Concurrency

- One goroutine per operation. Handlers must be safe for concurrent invocation.
- **Per-resource locks, never a global lock**: per workspace, per device, per build
  root. They are in-process locks, sound because one daemon owns everything
  (ADR-0019).
- **All Tooling API interactions are serialised per build root** — builds, tests and
  model fetches (`build.tasks`, `project.*`) alike. Gradle does not reuse a busy
  daemon, so a model fetch during a build would start a second daemon and cost
  gigabytes. Cached answers (e.g. a task list whose build-file fingerprint is
  unchanged) do not touch TAPI and do not wait.
- **Contention queues, it does not reject.** A second request on a busy root (the UI
  and an agent building at once) becomes a `queued` operation, FIFO, reporting
  `blockedBy`; the caller's bounded wait returns that state. A queue deeper than 4 per
  root is rejected with `BUILD_QUEUE_FULL`. Cancelling a queued operation is immediate.
- Device writes are serialised per device; reads proceed in parallel.
- A lock is never held while blocked on a subprocess without a timeout.

## State ownership

| State | Owner | Lifetime |
| --- | --- | --- |
| Workspace (config, project facts) | `workspace` | durable config (ADR-0021) + rediscovered facts |
| Operations | `capability` | in memory; terminal ones retained 15 min |
| Build registry | `domain/build` | compact results persisted, retention applied (ADR-0021) |
| Artifacts (screenshots, consoles) | `artifact` | bounded by TTL and size; consoles durable |
| Live devices | `domain/device` | in memory; devices are not ours |
| Pid records of supervised children | `process` via `store` | durable, until reaped (ADR-0021) |
| Sessions and their subscriptions | `daemon` / `event` | the connection's lifetime |
| Policy | `policy` | durable; changed through **human channels only** (UI, CLI, config file) — never over MCP ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §7) |

## Adapter boundary

Adapters may import `capability`, `event` and `artifact` — **never `domain/*`**. This
is what keeps the edge thin: if an adapter needs behaviour, that behaviour belongs in
a capability. Enforced by package layout, and by review.

## Open questions — deliberately

- **JSON Schema library**, and how it handles:
  - descriptions from Go doc comments,
  - enums,
  - **discriminated unions**, which Go cannot express natively and our conventions
    require for some capabilities.
- **Golden-file workflow**: where the generated schema lives and how the CI check
  reports drift.
- **Where the sidecar binary lives** and how it is built and shipped relative to the
  core.
- **Package-level naming** once the first real code exists — the layout above is a
  starting point, not a commitment.

## What comes first

1. `capability` (registry, descriptors, operations, typed errors) and `schema`
   (generation + golden file). Everything else depends on the contract being real.
2. `daemon` and `store` (lock, socket, handshake, sessions, state directory) — every
   adapter, including the CLI used to test everything else, goes through them.
3. `process` (supervisor) — because both the sidecar and adb children need it.
4. `workspace`, then a first domain package to prove the shape end to end.
