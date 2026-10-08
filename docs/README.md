# Droidyard — Documentation

> A lean, agent-oriented Android development runtime: the `droidyard` daemon at the
> centre, a web UI and an MCP surface for coding agents.

**Status:** pre-alpha. Milestones 1b and 1c are implemented and verified: the
capability contract, the daemon, the CLI and MCP shims, the MCP adapter, the process
supervisor and the workspace model are real; the surface is reachable by an agent over
MCP, bound to a project root (OpenCode verified; Cursor not yet run). These documents
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
| Subsystems | [`subsystems/files.md`](subsystems/files.md) | ✅ Draft |
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
| Known limits | [`roadmap/known-limits.md`](roadmap/known-limits.md): every open limit, unverified claim, deferred item and open question, one row each; each milestone's close updates it | ✅ Current |
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
| Research | [`research/spike-s1-error-formats.md`](research/spike-s1-error-formats.md) | Runs 1 and 2 (TAPI); the matrix beyond two cells not run |
| Research | [`research/spike-s3-android-facts.md`](research/spike-s3-android-facts.md) | Done — AGP 8.0.2, 8.13.2, 9.4.1 |
| Research | [`research/spike-s8-s9-emulator-and-logcat.md`](research/spike-s8-s9-emulator-and-logcat.md) | Done — API 31 scratch AVD |
| Research | [`research/prior-art.md`](research/prior-art.md) | ✅ Draft |
| Research | [`research/device-speed-2026-10.md`](research/device-speed-2026-10.md) | ✅ Done — device latency against prior art, measured |
| Research | `research/android-toolchain.md` | ⏳ Planned |
| Research | `research/kotlin-lsp.md` | ⏳ Planned |

---

## Where the work stands

**The design is closed and verified. Milestones 1b to 1i are done and verified: an
external agent over MCP can boot an emulator, pick an exact Android variant, check the
SDK it needs, build it, read its compile errors as structured records, fix them,
install, launch and drive the app, read its logs and crashes, and wipe or stop the
emulator, through Droidyard alone.**

| | State |
| --- | --- |
| **Design** | Closed. 35 ADRs, all accepted. A pre-implementation review and a verification pass ran; every finding is resolved, including four blockers the fixes themselves introduced. |
| **Owner decisions** | 19 closed in [`reviews/2026-09-29-ratifications.md`](reviews/2026-09-29-ratifications.md), plus the two 1b decisions: the MCP implementation ([ADR-0025](adr/0025-mcp-adapter-implementation.md)) and the macOS peer check with the per-root fallback ([ADR-0019](adr/0019-process-topology-daemon-and-shims.md) §6). |
| **Code** | **Milestones 1b and 1c — the daemon, the MCP adapter, the process supervisor and the workspace model — done and verified** ([1b plan](plans/m1b-daemon-and-mcp-adapter.md) A1–A12; [1c plan](plans/m1c-process-supervisor-and-workspace.md) status `Done`, A1–A9 at their stated levels). The 1b surface: state root, socket, `flock`, peer check, handshake, sessions, the native envelope and `operation.wait`/`cancel`; `serve`/`mcp`/`call`/`status`/`stop`; the MCP adapter on `mark3labs/mcp-go`. The 1c layer: durable JSON state and crash reaping, the workspace model (`workspace.open`/`describe`/`close` per ADR-0026/0027), the MCP session binding to a project root, and the process supervisor (lazy start, idle shutdown, no orphans). **Reachable by an agent over MCP, bound to a project** (OpenCode v2.0.18 verified; Cursor not run). **Milestone 1d done and verified** ([1d plan](plans/m1d-http-websocket-and-events.md) status `Done`, A1–A9 at their stated levels): per-connection MCP sessions, one event bus (`workspace.lifecycle`, `operation.progress`, resume with `since` and gap markers), MCP progress notifications, the loopback HTTP listener on 7317 behind the security fence, the per-launch token, `droidyard status`/`ui`, `SESSION_REQUIRED`, `events.subscribe`/`unsubscribe` at API 0.3.0, and the WebSocket session. **Milestone 1e done and verified** ([1e plan](plans/m1e-jvm-free-device-slice.md) status `Done`; A3 and A7 at their stated levels): adb resolution and degradation, `device.list`, `device.uiTree` through Droidyard's own reader that keeps other accessibility services running ([ADR-0029](adr/0029-ui-reader-dex.md)), `device.screenshot`, the ephemeral artifact store, `artifact.read`, `GET /v1/artifacts/{id}`, MCP image blocks, untrusted marking end to end, per-resource queues and the authorization re-check; level 5 on a Pixel_9a with OpenCode. **Milestones 1f to 1k done and verified** (plans [1f](plans/m1f-device-actions.md), [1g](plans/m1g-gradle-sidecar-and-build.md), [1h](plans/m1h-android-probe-and-sdk.md), [1i](plans/m1i-device-logs-and-emulator.md), [1j](plans/m1j-files.md), [1k](plans/m1k-policy-and-shell.md), each `Done` with its items at their stated levels): device actions, the Gradle sidecar and `build.*`, the Android probe and `sdk.*`, `device.logs` and `emulator.*`, `file.*`, and the configuration file with `policy.*`, `shell.exec`, the physical-device policy, the tool keys and the below-floor hatch; API 0.10.0, 39 capabilities, 34 MCP tools. |
| **Process** | 4 agents (`planner`, `plan-reviewer`, `implementer`, `verifier`) and 5 skills under [`.claude/`](../.claude/) (under `.opencode/` until 2026-10-03), on top of [`../AGENTS.md`](../AGENTS.md). Used end to end on 1b (three plan rounds, two verification rounds) and again on 1c (two plan rounds, three verification rounds, whose findings the implementer fixed before close). On 1d, run from Claude Code: Sonnet implements one step at a time, an Opus verifier and a DeepSeek cross-check review each step in parallel (`AGENTS.md` §Agents and models). |
| **Next** | **1o, then Phase 2** (owner, 2026-10-07): [1o](plans/m1o-tokens-and-harness.md) (tokens, assistant integration with Cursor first, the comparison rerun; needs its design questions answered). Then **Phase 2: the minimal web UI** ([`roadmap/mvp.md`](roadmap/mvp.md) §Phase 2); it needs a plan. What is open is in [`roadmap/known-limits.md`](roadmap/known-limits.md). **1p done** ([1p plan](plans/m1p-functional-gaps.md); API 0.12.0): real build cancels proven through the daemon, and a closed and reopened workspace builds again (it could not until a restart); the reader's forward is recreated after an adb server restart; `device.logs` reads API 36.1 (every read there had failed); physical devices hidden by default, checked on the owner's Samsung; an emulator launch is pid-recorded until its serial, so a killed daemon's successor ends it only when adb and the emulator consoles prove it is not a device; any doubt leaves it running (the register) (ADR-0021, ADR-0033 amended); `shell.exec` takes `cwd` and `env`; `policy.update` takes `ifRevision` (`POLICY_REVISION_CONFLICT`, ADR-0032 amended). Gradle 7.x support was deferred by the owner. **1n done** ([1n plan](plans/m1n-daily-use-core.md); [ADR-0034](adr/0034-device-calls-report-busy-crash-dialog.md), [ADR-0035](adr/0035-reader-acts-fenced-by-peer-uid.md); API 0.11.0): the UI reader answers `busy` instead of blocking on a frozen or crashing app and is never restarted for it; a tap or typing that crashes the app returns `appCrash` with the exception and top frames; an ANR or crash dialog is named on every read and an action behind it is refused before acting; the reader also taps, presses keys and sets text, fenced to adbd's uid, so `inputText` types any Unicode into a field that accepts a set text, with nothing installed, and never twice; a target under the soft keyboard has the keyboard hidden first; no fixed settle; error texts name sources and say whether repeating is safe. On API 31: `inputText` 0.46 s and `tapElement` 0.15 s median against 2.27 s and 2.03 s for the shell's units (m1m: 7.3 s, 5.5 s); API 36.1 not verifiable on this host. Before it: **1m done** ([1m plan](plans/m1m-emulator-robustness.md); [ADR-0033](adr/0033-emulator-launch-owned-until-serial.md)) and **Phase 1 accepted 2026-10-07** ([m1l plan](plans/m1l-phase1-acceptance.md)). The owner should see [ADR-0015](adr/0015-untrusted-data-authorization.md)'s 2026-10-07 lines: on a trusted root an agent can already run code as the user through the build. |
| **Then** | The remaining Definition of done items: the browser golden path (Phase 2), `build.errors` on a KGP ≥ 2.4 fixture, and the idle footprint report. Layers nobody calls yet wait. |

Before the next milestones:

- **Before `device.logs`**: [`subsystems/adb-device.md`](subsystems/adb-device.md); the
  build record already carries the `applicationId` it filters by.

Agent guidance for this repository lives in [`../AGENTS.md`](../AGENTS.md).

---

## Conventions

Where each fact lives, how ADRs are written and changed, which files are frozen
records, and the language rule are in [`../AGENTS.md`](../AGENTS.md) §Conventions.

---

## Source material

- [`../android-agent-suite-boceto.md`](../android-agent-suite-boceto.md) —
  original project sketch (Spanish). Superseded by `docs/`; kept for history.
