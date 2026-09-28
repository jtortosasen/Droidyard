# mydev — Documentation

> **Working codename: `mydev`** (provisional). A lean, agent-oriented Android
> development runtime with a web UI.

**Status:** pre-alpha / design phase. Nothing is implemented yet. These documents
are the source of truth for the design; the original brainstorm is kept only for
provenance.

---

## What this is

`mydev` is a persistent, AI-agnostic runtime for Android development that aims to
replace most day-to-day Android Studio usage without launching Android Studio or
IntelliJ. It exposes a single internal capability API that is consumed by:

- a lightweight **web UI** (desktop and tablet/mobile browser), and
- external **AI agents** through **MCP**.

It deliberately does **not** implement its own LLM agent harness. The agent loop
is provided by an external tool (Cursor, Codex, Claude, etc.); `mydev` provides
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
| Architecture | `architecture/core-runtime.md` | ⏳ Planned |
| Architecture | [`architecture/capability-api.md`](architecture/capability-api.md) | ✅ Draft |
| Architecture | `architecture/adapters.md` | ⏳ Planned |
| Architecture | [`architecture/workspace-state.md`](architecture/workspace-state.md) | ✅ Draft |
| Architecture | [`architecture/process-lifecycle.md`](architecture/process-lifecycle.md) | ✅ Draft |
| Subsystems | `subsystems/adb-device.md` | ⏳ Planned |
| Subsystems | `subsystems/emulator.md` | ⏳ Planned |
| Subsystems | [`subsystems/gradle.md`](subsystems/gradle.md) | ✅ Draft |
| Subsystems | `subsystems/kotlin-lsp.md` | ⏳ Planned |
| Subsystems | `subsystems/git.md` | ⏳ Planned |
| Subsystems | `subsystems/logcat.md` | ⏳ Planned |
| Subsystems | `subsystems/ui-automation.md` | ⏳ Planned |
| Interfaces | `interfaces/http-websocket.md` | ⏳ Planned |
| Interfaces | `interfaces/mcp-tool-surface.md` | ⏳ Planned |
| UI | `ui/web-ui.md` | ⏳ Planned |
| Roadmap | [`roadmap/mvp.md`](roadmap/mvp.md) | ✅ Draft |
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
| Research | [`research/prior-art.md`](research/prior-art.md) | ✅ Draft |
| Research | `research/android-toolchain.md` | ⏳ Planned |
| Research | `research/kotlin-lsp.md` | ⏳ Planned |

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
