# Droidyard — Documentation

> A lean, agent-oriented Android development runtime: the `droidyard` daemon at the
> centre, a web UI and an MCP surface for coding agents. Pre-alpha.

Droidyard is a persistent, AI-agnostic runtime for Android development, consumed by a
lightweight web UI and by external AI agents over MCP. It deliberately has no agent
harness of its own. The full statement is in [`vision/vision.md`](vision/vision.md).

The code is the documentation for the contract (`schema/capabilities.json`, the
descriptors, the tests); these documents hold what code cannot say: why, how the pieces
fit, what was rejected. Where each kind of fact lives, how ADRs are written and changed,
which files are frozen records, and the language rule are in
[`../AGENTS.md`](../AGENTS.md) §Conventions.

## Documentation map

| Directory | What it holds |
| --- | --- |
| [`vision/`](vision/vision.md) | The vision, non-goals and principles in one document; the [glossary](vision/glossary.md). |
| [`architecture/`](architecture/overview.md) | The overview (layers, topology, events, concurrency, state ownership); the [capability API](architecture/capability-api.md), [process lifecycle](architecture/process-lifecycle.md) and [workspace state](architecture/workspace-state.md). |
| [`subsystems/`](subsystems/) | One document per integration: `adb-device`, `emulator`, `files`, `gradle`, `sdk`, `ui-automation`. |
| [`interfaces/`](interfaces/) | The HTTP/WebSocket and MCP adapters. |
| [`roadmap/`](roadmap/mvp.md) | The MVP (scope, phases, definition of done, metric) and [`known-limits.md`](roadmap/known-limits.md), the register of everything open. |
| [`plans/`](plans/README.md) | One plan per milestone, written before the code and kept as its record. |
| [`adr/`](adr/README.md) | Decisions: context, decision, consequences, alternatives considered. |
| [`research/`](research/) | Prior art and spikes, dated records. |
| [`reviews/`](reviews/) | The pre-implementation review, the owner's ratifications and the session retrospective: closed records. |

A document is written when a milestone needs it; none is listed ahead of its work. The
original sketch, [`../android-agent-suite-boceto.md`](../android-agent-suite-boceto.md)
(Spanish), is superseded by `docs/` and kept for history.

## Where the work stands

Phase 2's acceptance is met (2b); 2c added fixes, the live build console and the project start; 2d the redesign, the projects and agents panel and a read-only file tree; 2e a UX round (collapsible panels, the sessions column, a status bar, a run toolbar, a Logcat query bar); the next is 2f, the file tree's operations ([`roadmap/mvp.md`](roadmap/mvp.md) §Phase 2). Every milestone's status is in
[`plans/README.md`](plans/README.md); what is open is in
[`roadmap/known-limits.md`](roadmap/known-limits.md).
