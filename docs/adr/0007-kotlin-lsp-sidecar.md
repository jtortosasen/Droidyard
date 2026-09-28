# ADR-0007: Kotlin LSP as an external sidecar process

- Status: Accepted
- Date: 2026-09-27

## Context

Rich Kotlin code intelligence (definition, references, symbols, diagnostics,
call hierarchy) comes from the official Kotlin LSP, which is JVM-based and built
on JetBrains/Kotlin infrastructure. Two shapes are possible: embed Kotlin
analysis in the core, or run the LSP as a separate process.

Embedding would force a JVM into the core and duplicate what the LSP already
does.

## Decision

Run the **Kotlin LSP as an external sidecar process**. The core is its
**client/proxy** over JSON-RPC (stdio):

```text
core (Go)
  |
  | JSON-RPC / stdio
  v
Kotlin LSP (JVM)
```

The core exposes simplified, semantic capabilities on top:

```text
code.definition()
code.references()
code.symbols()
code.diagnostics()
code.callHierarchy()
```

The UI never talks to the LSP directly.

## Consequences

- The core stays JVM-free; the JVM cost is isolated to one sidecar that can be
  started lazily and stopped when idle.
- The core owns LSP lifecycle, document synchronization and request routing.
- The same semantic layer serves both UI and agents (see
  [ADR-0003](0003-unified-capability-api.md)).
- Requires designing LSP lifecycle, project import/warm-up and memory management
  for large projects — see `subsystems/kotlin-lsp.md` (planned).
- Mapping across `.kt`, `.kts`, Java/XML and Gradle Kotlin DSL must be modelled
  explicitly.

## Alternatives considered

- **Embed Kotlin analysis (JVM core)** — rejects the lean goal and duplicates LSP
  behaviour.
- **Talk to the LSP from the browser** — leaks protocol complexity into the UI
  and breaks the single-capability-API principle.
