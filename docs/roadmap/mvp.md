# MVP

The first version: the smallest end-to-end system that can validate the core
hypothesis, and nothing else.

> Status: draft. Everything here is deliberately narrow. Anything not listed under
> **In scope** is a non-goal for the MVP.

## Goal

Ship a system that can run the **golden path** (below) both from an external agent
over MCP and — in phase 2 — from a browser, and use it to test the core hypothesis
from [`../vision/vision.md`](../vision/vision.md).

## Hypothesis to validate

> A stable semantic layer over the Android toolchain lets a general-purpose agent
> complete a `build → run → inspect → debug` loop with **fewer steps and fewer
> misinterpretations** than shell + adb + raw output.

It is falsifiable by comparison: the same task, the same agent, the same project,
run twice — once through our MCP surface, once through raw shell/adb. The metric is
**agent tool calls and failed/retried calls to reach a verified end state**.

## The golden path

One scenario. Everything in the MVP exists to serve it.

On the sample app ([below](#sample-app)):

| # | Step | Capability |
| --- | --- | --- |
| 1 | Open the project and list modules | `workspace.open`, `project.modules` |
| 2 | Build the debug variant | `build.assemble` |
| 3 | Introduce a compile error and recover | `build.errors` → file / line / message |
| 4 | Start (or adopt) an emulator | `emulator.start` / `device.list` |
| 5 | Install and launch the app | `device.install`, `device.launch` |
| 6 | Observe: logs, screen, UI tree | `device.logs`, `device.screenshot`, `device.uiTree` |
| 7 | Act and verify in one call | `device.tapElement` with an assertion |

At no point does the agent parse raw stdout to complete a step.

**"Act and verify in one call"** means the agent's *interaction* is a single tool
call, not that the work is magic. Without assertions the pattern is
`tap → wait → screenshot → model interprets → decide` (three to five calls, with
the model reading pixels). With assertions, one call taps, waits, checks the
expected state (re-reads the tree, or polls for text) and returns
`{ tapped, expected: { matched, afterMs } }`. Fewer round trips, fewer tokens, no
image interpretation.

## Scope

### In scope

- **Go core**: workspace state, capability API, process supervisor, event bus.
- **Adapters**: MCP (stdio) for agents; HTTP/WebSocket (needed by phase 2); a thin
  CLI for development and testing.
- **Capability domains**: `system`, `workspace`, `project`, `build`, `device`,
  `emulator`, `file`, `git`, `operation`, `events`, `shell`.
- **Sample app** as the acceptance target.
- **Minimal web UI** (phase 2), grown as needed rather than built to a fixed spec.
- **`git.*` as the last item** — a thin wrapper over the `git` CLI. It is allowed to
  slip to post-MVP without blocking, because the agent already has git; the value
  here is for the human UI.

### Out of scope (explicit)

- **Kotlin LSP and all code intelligence** (`code.*`): definition, references,
  symbols, diagnostics. Compile diagnostics come from `build.errors` instead. See
  [ADR-0007](../adr/0007-kotlin-lsp-sidecar.md) — the design stands, delivery is
  later.
- **Debugger / JDWP** stepping and breakpoints.
- **Multi-device concurrency.** One active device is acceptable; the core must not
  be architected around a single global slot.
- **LAN exposure and access control.** The engine must be designed so it *can* be
  reached remotely — no localhost assumptions in the capability layer — but remote
  access, TLS and authentication get no effort in the MVP.
- **Live screen streaming** to the UI. The device panel refreshes stills; streaming
  is a stretch goal inside phase 2, not required to close the MVP.
- **OCR** ([ADR-0013](../adr/0013-ocr-optional-pluggable.md) keeps the seam).
- **Skills, distribution and registries**
  ([ADR-0011](../adr/0011-skills-agent-skills-standard.md),
  [ADR-0014](../adr/0014-agent-distribution-model.md)).
- **Windows** ([ADR-0002](../adr/0002-unix-first.md)).
- **Refactors, rename, call hierarchy.**
- **Batching.**

## Phases

### Phase 0 — Foundations (walking skeleton)

Deliverables:

- Go module layout and the capability schema as the single source of truth, with
  generation of Go types, the TypeScript client and MCP tool definitions.
- Process supervisor with descriptors, lifecycle states, idle shutdown and
  no-orphans guarantees.
- Workspace model: open / describe / close, durable vs ephemeral state.
- HTTP + WebSocket server skeleton.
- MCP adapter over the capability API (stdio).
- Thin CLI (`mydev call <capability> --json`) for development.

Acceptance:

- `system.capabilities` lists what is available and the core degrades to `degraded`
  when `adb` is absent.
- A supervised dummy sidecar starts lazily, idles out, and leaves no orphans after
  `workspace.close`.
- The same capability is reachable from the CLI and from MCP with identical
  semantics.

### Phase 1 — Agent loop (the heart of the MVP)

Deliverables (capabilities):

- `project.*`: `modules`, `variants`.
- `build.*`: `assemble`, `tasks` (list), `run` (arbitrary task), `errors`,
  `lastResult`.
- `emulator.*`: `list`, `start`, `stop`.
- `device.*`: `list`, `install`, `launch`, `logs` (snapshot + follow),
  `screenshot`, `uiTree`, `tapElement` (with assertions), `uninstall`.
- `file.*`: `list`, `read`, `write` (needed by phase 2, useful now for fixtures).
- `operation.*`, `events.*`, `shell.exec` (policy-gated).

Acceptance — the golden path is completed end-to-end by an **external agent**
(Cursor / Claude / Codex, whichever is available) over MCP, against the sample app,
with structured results at every step and no stdout parsing.

### Phase 2 — Minimal web UI (closes the MVP)

Deliverables:

- React + TypeScript app served by the core; CodeMirror 6 editor
  ([ADR-0008](../adr/0008-frontend-react.md),
  [ADR-0009](../adr/0009-editor-component.md)).
- Panels, in priority order: **Logcat**, **build / Gradle tasks**, **Git**,
  **devices + screen**, then project explorer and problems. A code viewer/editor is
  deferred: it only becomes useful with code intelligence.
- TypeScript client generated from the capability schema.

Stretch (not required to close the MVP): live screen stream to the device panel.

Acceptance:

- The golden path is drivable from the browser, including reading the structured
  build errors and seeing device state.
- **Same-primitive check**: the UI and the agent reach the golden path through the
  same capabilities (verifiable in the capability trace).

## Sample app

A small Android project created for the MVP and kept in the repo (or generated by
a script), designed so the golden path is exercisable and reproducible in CI:

- Kotlin + Jetpack Compose, one activity, a navigation with 2–3 screens.
- A **form** (text input, button, validation) to exercise `uiTree` / `tapElement` /
  text input.
- A **list with a counter** (e.g. items with a like count) to exercise element
  identity and, later, the row abstraction.
- **`testTagsAsResourceId = true`** on the Compose root: Compose exposes no
  resource-ids to uiautomator by default, and stable ids are what makes
  identity-first automation work.
- A deliberate **compile-error branch** (documented, reverted) to exercise
  `build.errors`.
- A deliberate **crash path** (a button that throws) to exercise `device.logs`.
- Instrumented test(s) or a scripted journey that defines the expected end state.

## MVP capability surface

| Domain | Capabilities | Phase |
| --- | --- | --- |
| `system` | `capabilities`, `health` | 0 |
| `workspace` | `open`, `describe`, `close` | 0 |
| `project` | `modules`, `variants` | 1 |
| `build` | `assemble`, `tasks`, `run`, `errors`, `lastResult` | 1 |
| `emulator` | `list`, `start`, `stop` | 1 |
| `device` | `list`, `install`, `launch`, `uninstall`, `logs`, `screenshot`, `uiTree`, `tapElement` | 1 |
| `file` | `list`, `read`, `write`, `watch` | 1–2 |
| `git` | `status`, `diff`, `log`, `branches` | last |
| `operation` | `status`, `cancel` | 0 |
| `events` | `subscribe`, `unsubscribe` | 0 |
| `shell` | `exec` (policy-gated) | 1 |

`code.*` is **not** part of the MVP. `git.*` is included but scheduled last and may
slip.

## Definition of done

- [ ] The golden path completes from an external agent with zero stdout parsing.
- [ ] The golden path completes from the browser.
- [ ] `build.errors` returns structured `{file, line, message}` for a real Kotlin
      compile failure.
- [ ] `device.tapElement` verifies its effect in a single call.
- [ ] `workspace.close` leaves no orphan processes.
- [ ] Idle footprint: core process < 50 MB RSS, and the Gradle sidecar **stopped
      when idle** (it is our JVM and must idle out). The Gradle daemon is
      Gradle-managed, reused, and never killed implicitly.
- [ ] A recorded comparison of agent steps: our MCP vs raw shell, on the golden
      path.
- [ ] `docs/README.md` updated; every deviation from the design recorded as an ADR.

## Metrics

The comparison below is **not** "our MCP vs. adb": our core invokes the *same* `adb`
binary. What differs is the **surface the agent works against**.

- **Baseline**: the agent with a plain shell — `adb devices`, `adb install`,
  `adb shell uiautomator dump` — parsing XML and stdout, guessing coordinates, and
  chaining steps itself.
- **Our case**: the agent calls semantic capabilities and receives JSON.

Measured:

- **Agent steps** (tool calls) and **failed/retried calls** on the golden path,
  ours vs. the shell baseline.
- **Time to first useful result**: `workspace.open` → device list.
- **Idle RSS of the core process only** (see the note below), and peak during a
  build.
- **Orphan process count** after close (target: zero).

Honest caveat: a strong agent with a plain shell is capable, and if the difference
turns out to be small, the hypothesis fails. That is precisely why this is measured
in the MVP rather than assumed. The task and the starting state must be fixed for
the numbers to mean anything.

**What the memory budget covers.** We measure the core process, not everything on
the machine. Two JVMs may exist while working, and neither is the core:

- the **Gradle daemon**, which belongs to Gradle: started on first build, kept alive
  by Gradle for idle hours, reused across builds, never killed implicitly;
- the **Gradle TAPI sidecar** ([ADR-0017](../adr/0017-gradle-execution-strategy.md)),
  which is ours and therefore subject to our discipline: lazy start and idle
  shutdown like any other sidecar.

The emulator likewise belongs to the platform, not to us.

## Risks and unknowns

| Risk | Mitigation |
| --- | --- |
| Gradle error parsing varies by AGP/Kotlin version | Fixture-driven tests; anchor on the file/line/message contract, degrade to raw tail |
| Compose exposes no resource-ids | Ship the sample app with `testTagsAsResourceId`; fall back to text/content-desc |
| `uiautomator dump` is flaky ("could not get idle state") | Retry once, then a cause-attributed error that routes to text/OCR later |
| MCP client differences (tool limits, annotations) | Keep the exposed set small; verify against at least two assistants |
| The comparison metric is noisy | Fix a scripted task and the exact starting state |
| `adb` version/serial drift | Detect and surface the version; keep the adb surface conservative |
| TAPI sidecar compatibility across Gradle/AGP versions | Pin and test supported versions; detect the Gradle version and fail with a clear, actionable error |

## Decisions fixed by this MVP

- **MCP transport: stdio for the MVP** (works across assistants today; HTTP is an
  adapter to add later). *Needs an ADR.*
- **No LSP in the MVP**; `code.*` is designed but not delivered.
- **One active device**; multi-device is designed-for, not delivered.
- **`git.*` is in scope but last**, and may slip without blocking the MVP.
- **LAN exposure is designed-for, not delivered**: no localhost assumptions in the
  capability layer, but no remote access, TLS or authentication effort in the MVP.
- **UI priority order**: Logcat → build/Gradle tasks → Git → devices/screen; the
  editor comes later, with code intelligence.
- **The sample app** is the acceptance target; real projects come after the MVP.
- **`file.*` is a first-class domain** (added to
  [`../architecture/capability-api.md`](../architecture/capability-api.md)).
- **Gradle runs through a JVM Tooling API sidecar**
  ([ADR-0017](../adr/0017-gradle-execution-strategy.md)), staged: `build.tasks` and
  `build.errors` first, then execution.
- **Gradle floor is 8.0**, with capability gating and a published supported range
  ([ADR-0018](../adr/0018-gradle-version-support-policy.md)).
