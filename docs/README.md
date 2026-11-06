# Droidyard — Documentation

> A lean, machine-level Android tool: the `droidyard` daemon at the centre, an MCP
> surface for coding agents, a CLI, and an HTTP/WebSocket API for any UI. Pre-alpha.

Droidyard is a persistent, AI-agnostic tool for Android work (adb, emulators, the SDK,
Gradle builds), consumed by external AI agents over MCP and by people on a CLI; a UI is
a client of its API, not part of it ([ADR-0137](adr/0137-machine-level-android-tool.md)). It
deliberately has no agent harness of its own. The full statement is in [`vision/vision.md`](vision/vision.md).

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
| [`subsystems/`](subsystems/) | One document per integration: `adb-device`, `emulator`, `gradle`, `sdk`, `ui-automation`. |
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

Install, or update by re-running ([ADR-0068](adr/0068-release-builds-and-installer.md)):

```sh
curl -fsSL https://raw.githubusercontent.com/jtortosasen/Droidyard/main/install.sh | sh
```

Then, once, for every project and worktree: `droidyard init --user --client all`
([ADR-0124](adr/0124-init-once-per-user.md)).

**Upgrading to 1.0.0** (the API's first major version; the release's notes are generated
from the merge subjects, so this is where its upgrade steps live). Rerun `droidyard init
--user` (or `init` in a project): it refreshes the entries and the skill, and removes the
assistant hooks it wrote earlier; until then `droidyard hook` passes every event, for one
minor version. `droidyard mcp` ignores `--root` and `--from` for one minor version, with
one line on stderr. A project tool takes `project`, the absolute path of the folder or
worktree you work in, on every call. Removed: the web UI and `droidyard ui` (a bare
`droidyard` prints the help), `git.*`, `file.*`, `session.*`, `workspace.*`, `agent.*`,
`shell.exec`, `droidyard trash` and the `.droidyard/project.json` file. Files already in the
trash stay where they lie, unread: `<state>/ws/<wsid>/trash/` under the state directory
(`~/Library/Application Support/droidyard`, or `$XDG_STATE_HOME/droidyard`).

## Where the work stands

Phase 2 accepted (2b); 2c–2g UI, files; 2h the Definition of done; 2i the editor; 2j file capabilities off MCP, an agent's `rm` to the trash; 2k Cursor on par, agents' Android commands in Activity; 2l the Git tab and a Review diff; 2m CI, releases, the installer; 2n the Git history and its writes; 2o a Gradle build in subfolders; 2p the live device view, with input (C3); 2q sessions that own worktrees, one tab for every project (C2); 2r quick rows, a register cleanup; 2s a session occupies its worktree and device, its hooks joined to it (C2); 2t devices lent between paired Droidyards (C4a); 2u a phone shared and joined in one step each (C4b); 2v tests that hold under load; 2w a full UI dump and the screen's events between reads; 2x a UI recording over a span, local or lent; 2y a live view that responds; 2z a CLI for people; 2za view quality from the client. Phase 3: use and polish; 3a `init` once per user; 3b submodule changes in Git; 3c every chat in Sessions; 3d the Graphite facelift; 3e a Gradle sidecar that holds; 3f the SDK and AVDs provisioned by Droidyard; 3g agents tap, swipe, press keys and open panels; 3h the column as the project's worktrees and its sessions by folder; 3i filters for logs, artifacts and build errors instead of a terminal; 3j a machine-level Android tool, the API's first major version: the project named per call, the web UI, Git, files and sessions out to a separate project (ADR-0137); next, the owner's choice (`work-board`). Status is in
[`plans/README.md`](plans/README.md); what is open is in
[`roadmap/known-limits.md`](roadmap/known-limits.md).
