# Droidyard — Documentation

> A lean, agent-oriented Android development runtime: the `droidyard` daemon at the
> centre, a web UI and an MCP surface for coding agents.

**Status:** pre-alpha. Milestone 1b — the daemon and the MCP adapter — is implemented
and verified: the capability contract, the daemon, the CLI and MCP shims and the MCP
adapter are real, and the surface is reachable by an agent over MCP (OpenCode
verified; Cursor not yet run). These documents are the source of truth for the
design; the original brainstorm is kept only for provenance.

---

## What this is

Droidyard is a persistent, AI-agnostic runtime for Android development that aims to
replace most day-to-day Android Studio usage without launching Android Studio or
IntelliJ. It exposes a single internal capability API that is consumed by:

- a lightweight **web UI** (desktop and tablet/mobile browser), and
- external **AI agents** through **MCP**.

It deliberately does **not** implement its own LLM agent harness. The agent loop
is provided by an external tool (Cursor, Codex, Claude, etc.); Droidyard provides
the environment that agent operates on.

See [`vision/vision.md`](vision/vision.md) for the full statement.

---

## Documentation map

| Area | Document | Status |
| --- | --- | --- |
| Vision | [`vision/vision.md`](vision/vision.md) | ✅ Draft |
| Vision | [`vision/scope.md`](vision/scope.md) | ✅ Draft |
| Vision | [`vision/principles.md`](vision/principles.md) | ✅ Draft |
| Vision | [`vision/glossary.md`](vision/glossary.md) | ✅ Draft |
| Architecture | [`architecture/overview.md`](architecture/overview.md) | ✅ Draft |
| Architecture | [`architecture/core-runtime.md`](architecture/core-runtime.md) | ✅ Draft |
| Architecture | [`architecture/capability-api.md`](architecture/capability-api.md) | ✅ Draft |
| Architecture | `architecture/adapters.md` | ⏳ Planned |
| Architecture | [`architecture/workspace-state.md`](architecture/workspace-state.md) | ✅ Draft |
| Architecture | [`architecture/process-lifecycle.md`](architecture/process-lifecycle.md) | ✅ Draft |
| Subsystems | [`subsystems/adb-device.md`](subsystems/adb-device.md) | ✅ Draft |
| Subsystems | [`subsystems/emulator.md`](subsystems/emulator.md) | ✅ Draft |
| Subsystems | [`subsystems/gradle.md`](subsystems/gradle.md) | ✅ Draft |
| Subsystems | `subsystems/kotlin-lsp.md` | ⏳ Planned |
| Subsystems | [`subsystems/sdk.md`](subsystems/sdk.md) | ✅ Draft |
| Subsystems | `subsystems/git.md` | ⏳ Planned |
| Subsystems | [`subsystems/ui-automation.md`](subsystems/ui-automation.md) | ✅ Draft |
| Interfaces | [`interfaces/http-websocket.md`](interfaces/http-websocket.md) | ✅ Draft |
| Interfaces | [`interfaces/mcp-tool-surface.md`](interfaces/mcp-tool-surface.md) | ✅ Draft |
| Interfaces | [`interfaces/mcp-features.md`](interfaces/mcp-features.md) | ✅ Draft |
| UI | `ui/web-ui.md` | ⏳ Planned |
| Roadmap | [`roadmap/mvp.md`](roadmap/mvp.md) | ✅ Draft |
| Plans | [`plans/`](plans/) — one plan per milestone, dated records | ✅ Ready |
| Reviews | [`reviews/2026-09-29-pre-implementation-review.md`](reviews/2026-09-29-pre-implementation-review.md) | ✅ Resolved |
| Reviews | [`reviews/2026-09-29-ratifications.md`](reviews/2026-09-29-ratifications.md) | ✅ Closed |
| Reviews | [`reviews/2026-09-29-session-retrospective.md`](reviews/2026-09-29-session-retrospective.md) | ✅ Record |
| Skills | `skills/README.md` (format + packaging) | ⏳ Planned |
| Interfaces | `interfaces/distribution.md` (install snippets) | ⏳ Planned |
| Decisions | [`adr/`](adr/README.md) | ✅ Started |
| Research | [`research/mobile-mcp.md`](research/mobile-mcp.md) | ✅ Draft |
| Research | [`research/dsh-android.md`](research/dsh-android.md) | ✅ Draft |
| Research | [`research/codex-android-mcp.md`](research/codex-android-mcp.md) | ✅ Draft |
| Research | [`research/android-skills.md`](research/android-skills.md) | ✅ Draft |
| Research | [`research/dsh-codex-android-concepts.md`](research/dsh-codex-android-concepts.md) | ✅ Draft |
| Research | [`research/gradle-mcp.md`](research/gradle-mcp.md) | ✅ Draft |
| Research | [`research/gradle-version-compatibility.md`](research/gradle-version-compatibility.md) | ✅ Draft |
| Research | [`research/pstack.md`](research/pstack.md) | ✅ Draft |
| Research | [`research/spike-s1-error-formats.md`](research/spike-s1-error-formats.md) | ⚠️ Partial — command-line path only |
| Research | [`research/prior-art.md`](research/prior-art.md) | ✅ Draft |
| Research | `research/android-toolchain.md` | ⏳ Planned |
| Research | `research/kotlin-lsp.md` | ⏳ Planned |

---

## Where the work stands

**The design is closed and verified. Milestone 1b — the daemon and the MCP adapter —
is done and verified. The process supervisor and the workspace model are next.**

| | State |
| --- | --- |
| **Design** | Closed. 27 ADRs, all accepted. A pre-implementation review and a verification pass ran; every finding is resolved, including four blockers the fixes themselves introduced. |
| **Owner decisions** | 19 closed in [`reviews/2026-09-29-ratifications.md`](reviews/2026-09-29-ratifications.md), plus the two 1b decisions: the MCP implementation ([ADR-0025](adr/0025-mcp-adapter-implementation.md)) and the macOS peer check with the per-root fallback ([ADR-0019](adr/0019-process-topology-daemon-and-shims.md) §6). |
| **Code** | **Milestone 1b — the daemon and the MCP adapter — done and verified** ([plan](plans/m1b-daemon-and-mcp-adapter.md), status `Done`; acceptance A1 to A12 ticked at their stated levels). State root, socket path rules and single-instance `flock`; the Unix socket with the peer-uid check, handshake and sessions; the native envelope and `operation.wait` / `operation.cancel`; the `serve` (foreground and `--detach`), `mcp`, `call`, `status` and `stop` subcommands; the MCP adapter on `mark3labs/mcp-go`. **The surface is reachable by an agent over MCP** (OpenCode v2.0.18 verified; Cursor not run). |
| **Process** | 4 agents (`planner`, `plan-reviewer`, `implementer`, `verifier`) and 5 skills under [`.opencode/`](../.opencode/), on top of [`../AGENTS.md`](../AGENTS.md). First full use on 1b: plan, three review rounds, implementation, two verification rounds — the verifier reproduced one missed lock race and one weak retention test, both fixed. |
| **Next** | **Milestone 1c — the process supervisor and the workspace model** ([plan](plans/m1c-process-supervisor-and-workspace.md), `Reviewed` 2026-10-03 after two review rounds; implementation awaits the owner's approval): descriptors, lifecycle, idle shutdown and no orphans; `workspace.open` / `describe` / `close` with ADR-0021 persistence and [ADR-0026](adr/0026-workspace-addressing-invalid-roots.md) / [ADR-0027](adr/0027-workspace-close-and-reopen.md); the MCP session binding (root resolve, open at session start). |
| **Then** | **Milestone 1d (proposed) — HTTP/WebSocket skeleton, the event bus and `events.*`**; then the JVM-free device slice (`device.list`, `device.screenshot`, `device.uiTree`), which proves the contract, the adapters, artifacts and events with no Kotlin. The Gradle Tooling API sidecar comes after, gated by spikes S1 run 2 and S3. |

Three things to read before the next milestones:

- **Before the Gradle sidecar**: [`subsystems/gradle.md`](subsystems/gradle.md)
  §Spikes before the sidecar is written and
  [`research/spike-s1-error-formats.md`](research/spike-s1-error-formats.md). The second
  spike run has not happened, and it settles whether the sidecar keeps its
  justification.
- **Before implementing `build.errors`**: the AAPT2 merged-resource decision in
  [ADR-0022](adr/0022-build-error-extraction.md) §Known open item.
- **Before 1d**: per-connection MCP notifications need an `Amended:` line on
  [ADR-0025](adr/0025-mcp-adapter-implementation.md). `mcp-go`'s `Listen` shares one
  package-level session, so progress notifications can reach the wrong connection; the
  first consumer is the progress notifications ADR-0020 requires. The candidate fix is
  `server.NewInProcessSession` feeding `MCPServer.HandleMessage` (1b plan, §Not in this
  milestone). 1b sends no notifications, so this is not a 1b gap.

Agent guidance for this repository lives in [`../AGENTS.md`](../AGENTS.md).

---

## Conventions

### Documents

- One concern per file. Prefer small, focused documents over one large one.
- Every architectural claim that could reasonably be decided differently should
  live in an **ADR**, not buried in prose.
- Use ASCII diagrams in fenced `text` blocks so they survive any renderer.

### Architecture Decision Records

Decisions live in [`adr/`](adr/README.md), numbered sequentially
(`NNNN-short-title.md`). Each ADR has a **Status**:

- `Proposed` — under discussion, not yet binding.
- `Accepted` — binding; code and docs should conform to it.
- `Superseded by ADR-XXXX` — replaced; keep the file for history.
- `Deprecated` — no longer recommended, no replacement yet.

### Language

Project documentation and code are written in **English**. The original
brainstorm (`android-agent-suite-boceto.md`, Spanish) is retained at the repo
root for provenance only and is **superseded** by these documents.

---

## Source material

- [`../android-agent-suite-boceto.md`](../android-agent-suite-boceto.md) —
  original project sketch (Spanish). Superseded by `docs/`; kept for history.
