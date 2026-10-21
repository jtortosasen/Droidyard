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
| [`vision/`](vision/vision.md) | The vision, non-goals and principles in one document; the [glossary](vision/glossary.md); the owner's vision [beyond the MVP](vision/beyond.md). |
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

## Installing

Install, or update by re-running ([ADR-0068](adr/0068-release-builds-and-installer.md));
`curl -fsSL https://raw.githubusercontent.com/jtortosasen/Droidyard/main/install.sh | sh`
replaces it once the repository is public:

```sh
gh api -H 'Accept: application/vnd.github.raw' repos/jtortosasen/Droidyard/contents/install.sh | sh
```

## Where the work stands

Phase 2 accepted (2b); 2c–2g UI, files; 2h the Definition of done; 2i the editor; 2j file capabilities off MCP, an agent's `rm` to the trash; 2k Cursor on par, agents' Android commands in Activity; 2l the Git tab and a Review diff; 2m CI, release builds and the one-line installer; 2n the Git history and its writes; 2o a Gradle build in a subfolder; 2p the live device view, with input (C3); 2q sessions that own worktrees, one tab for every project (C2); 2r the quick rows and a register cleanup; 2s a session occupies its worktree and the device it acts on, its hooks joined to it (C2); 2t devices lent between paired Droidyards (C4a); 2u a phone shared and joined in one step each (C4b); 2v tests that hold under load; 2w a full UI dump for parsers and the screen's events between reads; 2x a UI recording over a span, local or lent; 2y a live view that responds; 2z a CLI for people, commands per domain from the descriptors; next, the owner's choice (`work-board`). Status is in
[`plans/README.md`](plans/README.md); what is open is in
[`roadmap/known-limits.md`](roadmap/known-limits.md).
