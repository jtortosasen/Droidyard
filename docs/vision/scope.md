# Scope

Boundaries for the project. The MVP is elaborated in
[`../roadmap/mvp.md`](../roadmap/mvp.md); this file draws the coarse line between
what we build and what we deliberately do not.

## In scope

### Core runtime

- Persistent core process with a workspace/session model.
- Process supervision for external tools (ADB, Gradle, Kotlin LSP, emulators).
- Filesystem access and file watching.
- Structured logging and events.

### Capability domains (v1)

- `workspace.*` — open/describe a project, modules, variants.
- `code.*` — go-to-definition, references, symbols, diagnostics (via Kotlin LSP).
- `build.*` — assemble, test, last result, structured errors.
- `device.*` — list, boot, install, launch, screenshot, UI tree, input, logs,
  processes.
- `emulator.*` — list, start, stop.
- `git.*` — status, diff, branch, commits.

### Interfaces

- HTTP + WebSocket server for the UI.
- MCP server for agents.
- Raw shell escape hatch for operations not yet modelled.

### UI (minimal)

- Project explorer, light editor, diagnostics, Git view, Gradle/build panel,
  Logcat, device panel, problems panel.

## Out of scope (v1)

- **Own agent harness.** No prompt assembly, no agent loop, no session lifecycle
  of an LLM. The external agent provides this.
- **Android Studio feature parity.** No full refactoring engine, no visual layout
  editor, no APK analyzer, etc.
- **Deep debugger integration.** JDWP stepping/breakpoints are not v1.
- **Embedded emulator.** We orchestrate the platform emulator; we do not build
  one.
- **Multi-user / remote / cloud.** Local-first, single user.
- **Windows.** Unix-first targets are macOS and Linux (see
  [ADR-0002](../adr/0002-unix-first.md)); Windows is a deferred seam, not a v1
  deliverable.
- **A dependency on Android CLI.** We use Gradle and ADB directly instead; see
  [ADR-0005](../adr/0005-reuse-platform-tools.md).
- **Gradle Tooling API sidecar.** v1 uses the Gradle CLI; the Tooling API is a
  possible future JVM sidecar.

## Deferred (after MVP)

- UI tree automation and semantic input at scale.
- Structured Logcat, crash/ANR analysis.
- Screenshot streaming to the tablet UI.
- Deeper code intelligence: call hierarchy, rename, code actions.
- Gradle Tooling API sidecar for rich build integration.
- Distribution as installable MCP server + optional skills per agent.
- Optional agent-specific adapters/plugins/hooks where they add real value.
