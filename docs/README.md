# Droidyard — Documentation

> A lean, agent-oriented Android development runtime: the `droidyard` daemon at the
> centre, a web UI and an MCP surface for coding agents.

**Status:** pre-alpha / design phase. Nothing is implemented yet. These documents
are the source of truth for the design; the original brainstorm is kept only for
provenance.

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
| Reviews | [`reviews/2026-09-29-pre-implementation-review.md`](reviews/2026-09-29-pre-implementation-review.md) | ✅ Resolved |
| Reviews | [`reviews/2026-09-29-ratifications.md`](reviews/2026-09-29-ratifications.md) | ✅ Closed |
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
| Research | [`research/spike-s1-error-formats.md`](research/spike-s1-error-formats.md) | ⚠️ Partial — command-line path only |
| Research | [`research/prior-art.md`](research/prior-art.md) | ✅ Draft |
| Research | `research/android-toolchain.md` | ⏳ Planned |
| Research | `research/kotlin-lsp.md` | ⏳ Planned |

---

## Where the work stands

**The design is closed and verified. The first code milestone is done. The daemon is
next.**

| | State |
| --- | --- |
| **Design** | Closed. 24 ADRs, all accepted. A pre-implementation review and a verification pass ran; every finding is resolved, including four blockers the fixes themselves introduced. |
| **Owner decisions** | 19 closed, recorded in [`reviews/2026-09-29-ratifications.md`](reviews/2026-09-29-ratifications.md). |
| **Code** | **Milestone 1 — the capability contract, in process.** `internal/capability` (registry, typed errors, annotation derivation), `internal/schema` (Go to JSON Schema, with `schema/capabilities.json` as the committed golden file), `internal/domain/system`, and a `droidyard` CLI with `caps`, `schema` and `version`. **Nothing is reachable by an agent yet.** |
| **Next** | **Milestone 1b — the daemon and the MCP adapter**: the Unix socket, `flock` single-instance, the handshake, the `droidyard mcp` stdio shim, and the tool translation with the operation view and typed errors. That is what makes the surface usable from Cursor and OpenCode. |
| **Then** | The JVM-free device slice (`device.list`, `device.screenshot`, `device.uiTree`) — it proves the contract, the adapters, artifacts and events with no Kotlin. The Gradle Tooling API sidecar comes after, gated by spikes S1 run 2 and S3. |

Two things to read before the next milestones:

- **Before the Gradle sidecar**: [`subsystems/gradle.md`](subsystems/gradle.md)
  §Unverified assumptions and
  [`research/spike-s1-error-formats.md`](research/spike-s1-error-formats.md). The second
  spike run has not happened, and it settles whether the sidecar keeps its
  justification.
- **Before implementing `build.errors`**: the AAPT2 merged-resource decision in
  [ADR-0022](adr/0022-build-error-extraction.md) §Known open item.

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
