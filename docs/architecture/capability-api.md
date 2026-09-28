# Capability API

The **internal contract of the core**. Every consumer — the web UI, MCP agents
and the CLI — is a thin adapter over it
([ADR-0003](../adr/0003-unified-capability-api.md)). This document defines the
abstract contract; the concrete wire mappings live in `interfaces/` (planned).

> Status: draft. This is the backbone of the system; most other documents depend
> on it.

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
     HTTP / WebSocket           MCP                      CLI
          \                      |                        /
           \                     |                       /
            +-----------+--------+--------+-------------+
                        |                 |
                 capability API (this document)
                        |
                domain services
                        |
        ADB | Gradle | Kotlin LSP | emulator | SDK tools | Git
```

## Concepts

| Concept | Meaning |
| --- | --- |
| **Capability** | A named, typed operation, written `<domain>.<verb>` (e.g. `code.definition`). |
| **Command** | A capability that mutates state or triggers work; may be long-running. |
| **Query** | A read-only capability; expected to be fast and side-effect free. |
| **Operation** | A running/finished instance of a long-running command, identified by an id. |
| **Stream** | A capability that produces an ongoing sequence of events (e.g. logcat). |
| **Event** | A typed, timestamped message emitted by the core. |
| **Subscription** | A consumer registration to receive events matching a filter. |

## Naming and namespaces

Format: `<domain>.<verb>`, lower camel case, verb last.

```text
workspace.open        workspace.describe      project.modules
code.definition       code.references         code.symbols    code.diagnostics
build.assemble        build.test              build.errors    build.lastResult
device.list           device.install          device.launch   device.screenshot
device.uiTree         device.tapElement       device.logs     device.processes
emulator.list         emulator.start          emulator.stop
git.status            git.diff                git.branches
runtime.logcat        runtime.crash
shell.exec
system.capabilities   operation.status        operation.cancel
events.subscribe
```

Domains are fixed by the capability schema, not invented ad hoc by adapters.

## The schema is the single source of truth

Capabilities are declared once in a machine-readable schema and everything else
is **generated** from it:

```text
             capability schema (single source of truth)
                            |
        +-------------------+-------------------+
        v                   v                   v
   Go types             TS client          MCP tool defs
   (core)               (UI, ADR-0008)     (agents)
```

This is what prevents the UI, the agents and the core from drifting. The schema
language itself is an open decision (see below); JSON Schema is the working
assumption.

## Invocation model

### Request / response envelope

```json
// request
{
  "id": "req-1",
  "capability": "code.definition",
  "params": { "file": "app/src/main/java/Foo.kt", "line": 42, "character": 10 },
  "raw": false
}
```

```json
// success
{
  "id": "req-1",
  "status": "ok",
  "result": { "locations": [ { "file": "...", "line": 83, "character": 5 } ] },
  "meta": { "durationMs": 12 }
}
```

### Error model

Errors are **typed and stable**; consumers must never parse message strings.

```json
{
  "id": "req-1",
  "status": "error",
  "error": {
    "code": "CODE_SYMBOL_NOT_FOUND",
    "message": "no definition found for symbol 'Foo'",
    "details": { "file": "app/src/main/java/Foo.kt", "line": 42 }
  }
}
```

- `code` is a namespaced, versioned identifier (`<DOMAIN>_<REASON>`).
- `message` is human-readable and non-normative.
- `details` is optional structured context.

### Long-running operations

Commands that take time do **not** block. They return an operation handle
immediately:

```json
{ "id": "req-2", "status": "ok",
  "result": { "operationId": "op-7f3a", "state": "running" } }
```

Progress and completion arrive as **events** on the operation's topic; the final
event carries the same structured result a short command would have returned.
Polling is available via `operation.status` for consumers that cannot subscribe
(e.g. simple MCP tool calls; see mapping below).

### Cancellation

`operation.cancel({ "operationId": "op-7f3a" })` requests cooperative
cancellation. Cancellation is best-effort and reported via a terminal event.

### Idempotency

- Queries are idempotent by definition.
- Commands declare whether they are idempotent.
- Commands may accept an optional `idempotencyKey` to make retries safe.

## Events and subscriptions

### Event envelope

```json
{
  "eventId": "evt-91",
  "seq": 91,
  "time": "2026-09-27T10:15:00.123Z",
  "workspace": "ws-1",
  "topic": "build.progress",
  "type": "task.started",
  "payload": { "task": ":app:compileDebugKotlin" }
}
```

- `seq` is a monotonic per-workspace counter so consumers can detect gaps.
- `topic` groups events; `type` discriminates within a topic.

Representative topics: `build.progress`, `build.output`, `device.logcat`,
`device.screen`, `code.diagnostics`, `process.lifecycle`.

### Subscriptions

`events.subscribe({ "topics": ["device.logcat"], "filter": { "device": "emulator-5554" } })`
returns a `subscriptionId`. Events are delivered in `seq` order per subscription.
Consumers unsubscribe explicitly; the core also cleans up on disconnect.

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
They come from the prior-art analysis (`../../research/prior-art.md`).

### Bounded output

Every list/stream result has a byte/line cap, enforced **while capturing**. When
truncated, the result carries `truncated: true` and a hint naming how to narrow
(filter, depth, smaller window).

### Empty is a fact, not a failure

A plumbing failure raises an error; only a successful call may return an empty
result. Never conflate "the listing failed" with "the listing is empty". Zero
matches carry candidates or a hint where useful.

### Typed, cause-attributed errors

Errors carry a stable `code`, a human `message`, and where possible the **cause**
and the **next step**. When a result can be empty for several reasons, the
capability says which (e.g. filter matched nothing vs. cap pruned vs. genuinely
none), so consumers do not flail.

### Advertised output schemas

Capabilities declare a result schema, and the MCP adapter registers it as
`outputSchema`, so consumers can rely on a typed contract — not just
`structuredContent`.

### One name for the target

The device/target argument is named `device` everywhere. No `serial` alias, no
implicit "current device".

### Discriminated tools over overloaded ones

Prefer a tagged union / one capability per action over a single tool with many
optional fields and a free-form JSON blob.

### Statefulness is declared

Each capability is `stateless`, `stateful` or `streaming`
([ADR-0010](../adr/0010-capability-statefulness.md)); the adapter enforces the
session requirement accordingly.

### Untrusted inputs are tagged

Data originating from a device or app is marked untrusted at the boundary
([ADR-0015](../adr/0015-untrusted-data-authorization.md)).

### Visual artifacts are references

Screenshots and streams return opaque references, not filesystem paths
([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)).

## Discovery and versioning

- `system.capabilities` returns every capability with its schema, version,
  `since`/`deprecated` markers, and **availability** (e.g. whether the Kotlin LSP
  or an emulator is present).
- The API is semantically versioned.
- Consumers (the UI in particular) adapt to what is advertised rather than
  assuming a fixed surface.

## Mapping to adapters

Adapters translate only; they add no domain logic.

| Adapter | Invocation | Events |
| --- | --- | --- |
| HTTP/WebSocket | one request endpoint carrying the envelope | WebSocket subscription |
| MCP | one tool per capability, plus generic `operation_status` / `operation_cancel`; event-bearing capabilities surface via progress notifications where supported, otherwise polling | progress notifications or polling |
| CLI | `mydev <domain>.<verb> --params …` | streamed to stdout or via `--follow` |

Exact wire details belong to `interfaces/http-websocket.md` and
`interfaces/mcp-tool-surface.md` (planned).

## Initial domains

| Domain | Kind | Representative capabilities | Backed by |
| --- | --- | --- | --- |
| `workspace` | cmd/query | open, describe, close | core |
| `project` | query | modules, variants | Gradle |
| `file` | cmd/query | list, read, write, watch | OS |
| `code` | query | definition, references, symbols, diagnostics, callHierarchy | Kotlin LSP |
| `build` | cmd/query | assemble, tasks, run, test, errors, lastResult | Gradle |
| `device` | cmd/query | list, install, launch, screenshot, uiTree, tapElement, logs, processes | ADB |
| `emulator` | cmd/query | list, start, stop | emulator / avdmanager |
| `git` | cmd/query | status, diff, branches, log | git |
| `runtime` | stream/query | logcat, crash, memory | ADB |
| `shell` | cmd | exec | OS |
| `system` | query | capabilities, health | core |
| `operation` | cmd/query | status, cancel | core |
| `events` | cmd | subscribe, unsubscribe | core |

## Adding a capability — checklist

1. Add it to the schema (name, params, result, errors, kind, availability).
2. Implement it in the owning domain service.
3. Regenerate Go types, TS client and MCP tool defs.
4. Ensure it returns structured data; add `raw` only if justified.
5. Document it in `interfaces/mcp-tool-surface.md` if it is agent-facing.
6. If it introduces a new topic, document the topic and its event types.

## Open questions

- **Schema language**: JSON Schema vs. a compact custom IDL vs. protobuf. Must
  generate Go, TS and MCP definitions cleanly.
- **MCP event delivery**: how to surface ongoing streams (progress notifications
  vs. polling vs. resource subscriptions across transports).
- **Naming across protocols**: capability names use dots; MCP tools typically use
  underscores — confirm the canonical mapping.
- **Batching**: whether to support multiple invocations per round trip.
- **Auth/authorisation placement**: in the capability API or in each adapter
  (relevant once the UI is LAN-reachable).
- **`shell.exec` policy**: default-enabled for local single-user dev, or opt-in.
