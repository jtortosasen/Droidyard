# Core Runtime

How the Go core is organised. **Deliberately short**: it fixes only what must be
decided before writing code. Everything that is better decided with code in front of
us stays open, and is listed at the end.

## What it encodes

- **One binary, one process, many goroutines.** Sidecars are separate processes
  ([ADR-0017](../adr/0017-gradle-execution-strategy.md)); nothing else is.
- **Capabilities are the product; adapters are edges**
  ([ADR-0003](../adr/0003-unified-capability-api.md)).
- **The core owns state** — workspace, operations, build registry, artifacts
  ([workspace-state](workspace-state.md), [ADR-0012](../adr/0012-visual-artifacts-as-refs.md)).
- **Sidecars are supervised children** with an explicit lifecycle
  ([process-lifecycle](process-lifecycle.md)).

## Package layout

```text
cmd/mydev/                 entrypoint: flags, wiring, signal handling

internal/
  capability/              registry, descriptors, invocation, typed errors
  schema/                  Go → JSON Schema generation and golden files
  workspace/               workspace model, lifecycle, durable configuration
  process/                 supervisor: descriptors, lifecycle, no-orphans
  event/                   event bus, per-workspace sequences, subscriptions
  artifact/                single artifact store; URI-shaped refs; eviction
  policy/                  authorization boundary, allowlists, live configuration
  platform/                OS specifics: paths, signals, process groups
  domain/
    project/               project, modules, variants
    build/                 Gradle: client of the TAPI sidecar
    device/                adb layer and device capabilities
    emulator/              AVD lifecycle
    sdk/                   SDK inspection
    file/                  filesystem
    git/                   later

adapters/
  mcp/                     MCP stdio: tools, instructions, annotations, schemas
  httpapi/                 HTTP + WebSocket for the web UI
  cli/                     thin development CLI (`mydev call <capability>`)

sidecar/                   Kotlin TAPI sidecar (separate module, own build)
```

`internal/` because this is an application, not a library — the layout should say so.

## The capability registry

The central abstraction. A capability is a Go value, registered explicitly by its
domain package at startup — **no reflection magic over the codebase**, so the set of
capabilities is greppable and reviewable.

```go
type Descriptor struct {
    Name        string        // "device.tapElement"
    Kind        Kind          // Query | Command
    Stateful    bool          // ADR-0010
    Params      any           // struct instance; schema generated from it
    Result      any           // struct instance; schema generated from it
    Errors      []ErrorCode
    Annotations Annotations   // readOnly, destructive, idempotent, openWorld
    Availability []Requirement // needs adb, needs the sidecar, needs an SDK …
    Handler     Handler
}
```

The registry is the **single source** for: dispatch, JSON Schema generation, MCP tool
definitions, CLI help, and the tool table in the docs. If it is not in the registry,
it does not exist.

## Invocation

One entry point:

```go
Invoke(ctx context.Context, name string, params json.RawMessage) (Result, error)
```

- Params are decoded from JSON into the registered struct; unknown fields are
  rejected.
- Errors are typed — `{code, message, cause, nextStep}` — never stringly-typed
  ([capability-api](capability-api.md)).
- Every invocation is recorded as an **operation**: an id, its state, and how to
  cancel it. A long command may run in the background (returning the operation id) or
  block and return its final result; either way progress is emitted on the event bus.
- **`ctx` cancellation is a contract**, not a suggestion: handlers must honour it and
  clean up.

## Events

- One event bus. Events carry a **monotonic per-workspace sequence**, so subscribers
  can detect gaps ([capability-api](capability-api.md)).
- The bus is the **only** cross-cutting channel. Adapters subscribe and translate:
  MCP progress notifications, WebSocket frames.
- No adapter polls. No handler knows which adapter is watching.

## Concurrency

- One goroutine per invocation. Handlers must be safe for concurrent invocation.
- **Per-resource locks, never a global lock**: per workspace, per device, per build
  root. Gradle builds are serialised per project; device writes are serialised per
  device.
- A lock is never held while blocked on a subprocess without a timeout.

## State ownership

| State | Owner | Lifetime |
| --- | --- | --- |
| Workspace (config, project facts) | `workspace` | durable config + rediscovered facts |
| Operations and build registry | `capability` / `domain/build` | in memory, compact results persisted |
| Artifacts (screenshots, consoles) | `artifact` | bounded by TTL and size |
| Live devices | `domain/device` | in memory; devices are not ours |
| Policy | `policy` | durable, **live-updatable** |

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
- **Persistence format and location** for workspace state and the build registry
  (already open in ADR-0011 and ADR-0017).
- **Whether the CLI is an adapter over the registry** or a separate thin binary.
- **Where the sidecar binary lives** and how it is built and shipped relative to the
  core.
- **Package-level naming** once the first real code exists — the layout above is a
  starting point, not a commitment.

## What comes first

1. `capability` (registry, descriptors, typed errors) and `schema` (generation +
   golden file). Everything else depends on the contract being real.
2. `process` (supervisor) — because both the sidecar and adb children need it.
3. `workspace`, then a first domain package to prove the shape end to end.
