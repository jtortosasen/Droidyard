# Principles

Design principles that guide decisions. When two options conflict, prefer the
one that better satisfies these, in roughly the order listed.

## 1. One capability API, two consumers

The UI and the AI agent must reach every capability through the **same** internal
primitive. Never build a human API and a separate agent API for the same thing.
See [ADR-0003](../adr/0003-unified-capability-api.md).

The one deliberate exception is policy changes. They are capabilities too, but
human-only and never exposed to agents
([ADR-0015](../adr/0015-untrusted-data-authorization.md) §6).

## 2. The core is AI-agnostic

Droidyard must be fully useful with no agent attached. The AI is a consumer, not a
dependency. The core does not know what a "prompt" is.

## 3. Lean by default

Small memory footprint, fast startup, no unnecessary long-lived processes. JVM
tools are external sidecars, started only when needed and shut down when idle.
No Electron, no bundled browser engine.

## 4. Reuse the platform, don't reimplement

Gradle, ADB, the emulator and Kotlin LSP already exist and evolve. We orchestrate
and elevate them; we do not fork or replace them. See
[ADR-0005](../adr/0005-reuse-platform-tools.md).

## 5. Semantic over shell, structure over stdout

The differentiating value is turning frequent operations into semantic calls that
return structured data, so a consumer does not have to parse stdout or guess. Raw
shell remains available as an escape hatch, not as the primary interface.

```text
shell:  adb shell input tap 540 1220
        -> arbitrary stdout -> consumer interprets

droidyard:  device.tapElement(resourceId="...")
        -> structured result -> no coordinate discovery
```

## 6. Protocols are adapters, not the core

HTTP/WebSocket and MCP are *edges*. The internal capability API is the center.
Business logic must not live in an adapter.

## 7. Explicit state and lifecycle

Every persistent thing has an explicit owner and a defined lifecycle, and the owner is
not always us:

- **Our processes** have a lifecycle we enforce: the TAPI sidecar, the LSP, watchers,
  log streams. They start lazily, stop when idle, and leave no orphans.
- **Processes that belong to other tools** are started or used through those tools,
  and never killed implicitly: the adb server, the Gradle daemon (which is Gradle's)
  and emulators. Stopping one of them is an explicit call.

State lives in the workspace model, not scattered across adapters.

## 8. Progressive disclosure with an escape hatch

Model the common and high-value operations first. For everything not yet modelled,
expose raw shell. It is one policy toggle away, and off by default. Never make an
unmodelled operation impossible.

## 9. Modules are separable

A capability domain can be absent, disabled, or replaced without breaking the
rest. The UI adapts to the capabilities the running core advertises.

## 10. Observable by nature

Every long-running operation emits structured events (progress, output, result). The
UI subscribes to them. Agents wait on the operation for a bounded time
([ADR-0020](../adr/0020-operation-contract.md)). Nobody re-runs a command, or scrapes
its output, to find out what happened.

## 11. Bounded output and honest emptiness

Every result is bounded, and truncation is reported explicitly (`truncated` plus a
hint to narrow). An empty result is a fact, never a disguised failure; a failure is
an error with a cause and a next step.

## 12. Authorization is a boundary, not an annotation

Tool annotations are usability hints for client approval UX. Enforcement is a
separate boundary, re-validated immediately before execution. Policy is changed by
humans, never by the agent it governs. See
[ADR-0015](../adr/0015-untrusted-data-authorization.md).

## 13. Treat device data as untrusted

Anything read from a device or app is attacker-controllable. It is marked per field in
the schema and per result on every wire. Agents are told not to treat it as
instructions, and the UI never renders it as markup.

## 14. Artifacts are references

Screenshots, captures and build consoles are opaque references, never filesystem
paths. Every reference handed out is readable through the API. See
[ADR-0012](../adr/0012-visual-artifacts-as-refs.md).
