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
run twice — once through our MCP surface, once through raw shell/adb.

## The golden path

One scenario. Everything in the MVP exists to serve it.

On DopamineControl ([below](#validation-target-dopaminecontrol)):

| # | Step | Capability |
| --- | --- | --- |
| 1 | Open the project and list modules | `workspace.open`, `project.modules` |
| 2 | Build the debug variant | `build.assemble` |
| 3 | Introduce a compile error and recover | `build.errors` → file / line / column / message |
| 4 | Start (or adopt) an emulator | `emulator.start` / `device.list` |
| 5 | Install and launch the app | `device.install`, `device.launch` |
| 6 | Observe: logs, screen, UI tree | `device.logs`, `device.screenshot`, `device.uiTree` |
| 7 | Act and verify in one call | `device.tapElement`, `device.inputText` — with assertions |

**The agent never parses raw stdout.** The *core* does parse build console output —
that is its job, and it is how compile errors are extracted on real projects
([ADR-0022](../adr/0022-build-error-extraction.md)). The guarantee is about the
agent's surface, not about what happens underneath.

**"Act and verify in one call"** means the agent's *interaction* is a single tool
call, not that the work is magic. Without assertions the pattern is
`tap → wait → screenshot → model interprets → decide` (three to five calls, with
the model reading pixels). With assertions, one call acts, waits, checks the
expected state (re-reads the tree) and returns
`{ acted, expected: { matched, afterMs } }`. Fewer round trips, fewer tokens, no
image interpretation.

## Scope

### In scope

- **Go core**: one daemon owning all state — workspaces, operations, the event bus,
  the process supervisor — reachable over a Unix socket
  ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)).
- **Adapters**: an MCP stdio shim and a CLI shim over that socket; HTTP/WebSocket on
  loopback for the web UI (phase 2).
- **Invocation contract**: `Start` / `Wait` / `Cancel`, with `operation.wait` and
  `operation.cancel` ([ADR-0020](../adr/0020-operation-contract.md)).
- **Capability domains**: `system`, `workspace`, `project`, `build`, `device`,
  `emulator`, `sdk`, `file`, `git`, `artifact`, `policy`, `operation`, `events`,
  `shell`.
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
- **Multi-device concurrency.** One device at a time is acceptable; devices are
  always named explicitly, and the core must not be architected around a single
  global slot.
- **LAN exposure and access control.** The engine must be designed so it *can* be
  reached remotely — no localhost assumptions in the capability layer — but remote
  access, TLS and authentication get no effort in the MVP. The loopback HTTP
  baseline is still required before the UI ships (see Risks).
- **Live screen streaming** to the UI. The device panel refreshes stills; streaming
  is a stretch goal inside phase 2, not required to close the MVP.
- **OCR** ([ADR-0013](../adr/0013-ocr-optional-pluggable.md) keeps the seam).
- **Skills, distribution and registries**
  ([ADR-0011](../adr/0011-skills-agent-skills-standard.md),
  [ADR-0014](../adr/0014-agent-distribution-model.md)).
- **Windows** ([ADR-0002](../adr/0002-unix-first.md)).
- **SDK provisioning and AVD creation.** `sdk.packages`, `sdk.install`,
  `sdk.licenses`, `emulator.create` and `emulator.delete` are post-MVP: the golden
  path only needs an emulator that already exists, and a package manager with licence
  consent and gigabyte downloads is a large block that does not validate the
  hypothesis. `sdk.requirements` **is** in scope precisely because it makes Gradle
  failures explainable from day one.
- **Split APKs, App Bundles, dynamic features and instant apps.** One unfiltered APK
  per variant ([ADR-0023](../adr/0023-android-project-facts-and-build-outputs.md)).
- **Refactors, rename, call hierarchy.**
- **Batching.**

## Phases

### Phase 0 — Foundations (walking skeleton)

Deliverables:

- **The daemon**: `droidyard serve`, single-instance via `flock`, Unix socket with peer-uid
  checks, and the `droidyard mcp` / `droidyard call` shims with auto-start
  ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)).
- The capability registry and the schema as the single source of truth, with the
  JSON Schema golden file generated and checked in. TypeScript generation is phase 2
  ([ADR-0016](../adr/0016-capability-schema-in-go.md)).
- The invocation contract: `Start` / `Wait` / `Cancel`, operation records and the
  error shape ([ADR-0020](../adr/0020-operation-contract.md)).
- Process supervisor with descriptors, lifecycle states, idle shutdown and
  no-orphans guarantees.
- Workspace model: open / describe / close, and persistence in the per-user state
  directory ([ADR-0021](../adr/0021-persistence-format-and-location.md)).
- HTTP + WebSocket server skeleton on loopback.

Acceptance:

- `system.capabilities` lists what is available and the core reports `degraded` when
  `adb` is absent.
- A supervised dummy sidecar starts lazily, idles out, and leaves no orphans after
  `workspace.close`.
- The same capability is reachable from the CLI and from MCP with identical
  semantics.
- Killing a shim does not stop an operation it started
  ([ADR-0020](../adr/0020-operation-contract.md)).
- The HTTP skeleton binds to **loopback only** and enforces the Host, Origin and
  per-launch-token baseline ([`../interfaces/http-websocket.md`](../interfaces/http-websocket.md)).

### Phase 1 — Agent loop (the heart of the MVP)

Deliverables (capabilities):

- `project.*`: `modules`, `variants`.
- `build.*`: `assemble`, `tasks` (list), `run` (arbitrary task), `errors`,
  `lastResult`, and the `outputs[]` record that `device.install` consumes
  ([ADR-0023](../adr/0023-android-project-facts-and-build-outputs.md)).
- `emulator.*`: `list`, `start`, `stop`, `wipe`.
- `sdk.*`: `status`, `requirements` — read-only: what is installed, and what this
  project needs but lacks.
- `device.*`: `list`, `install`, `launch`, `logs` (snapshot + bounded follow),
  `screenshot`, `uiTree`, `tapElement` (with assertions), `inputText`, `uninstall`.
- `file.*`: `list`, `read`, `write`, `watch` (watch is needed by phase 2).
- `artifact.*`: `read` — screenshots, log captures and build consoles reach consumers
  as opaque references, in two durability classes
  ([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)).
- `policy.*`: `get`, `update` — **human-only**, through the UI, the CLI or the config
  file; the core rejects them from an MCP session
  ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §6).
- `shell.exec` (**off by default**; a call returns `POLICY_SHELL_DISABLED`).

Acceptance — the golden path is completed end-to-end by an **external agent**
(**Cursor** and **OpenCode**: the two the owner uses) over MCP, against DopamineControl,
with structured results at every step and **no stdout parsing by the agent**.

### Phase 2 — Minimal web UI (closes the MVP)

Deliverables:

- React + TypeScript app served by the daemon; CodeMirror 6 editor
  ([ADR-0008](../adr/0008-frontend-react.md),
  [ADR-0009](../adr/0009-editor-component.md)).
- Panels, in priority order: **Logcat**, **build / Gradle tasks**, **Git**,
  **devices + screen**, then project explorer and problems. A code viewer/editor is
  deferred: it only becomes useful with code intelligence.
- TypeScript client generated from the capability schema.
- The loopback HTTP baseline: bind to 127.0.0.1, Host and Origin checks, a
  per-launch token. Delivered early, in milestone 1d with the Phase 0 skeleton; Phase 2
  adds no rule to it.

Stretch (not required to close the MVP): live screen stream to the device panel.

Acceptance:

- The golden path is drivable from the browser, including reading the structured
  build errors and seeing device state.
- **Same-primitive check**: the UI and the agent reach the golden path through the
  same capabilities, verified from the capability trace (every call is an operation
  record, [ADR-0020](../adr/0020-operation-contract.md)).

## Validation target: DopamineControl

**Ratified 2026-09-29: the acceptance run happens against `DopamineControl`**, the
owner's real Android project — multi-module, with `build-logic` convention plugins and
Compose. A real project exercises the design far harder than a template would.

What the golden path needs from it:

- Kotlin + Jetpack Compose, one activity, a navigation with 2–3 screens.
- A **form** (text input, button, validation) to exercise `uiTree` / `tapElement` /
  `device.inputText`.
- A **list with a counter** (e.g. items with a like count) to exercise element
  identity and, later, the row abstraction.
- **`testTagsAsResourceId = true`** on the Compose root: Compose exposes no
  resource-ids to uiautomator by default, and stable ids are what makes
  identity-first automation work.
- A deliberate **compile-error branch** (documented, reverted) to exercise
  `build.errors`.
- A deliberate **crash path** (a button that throws) to exercise `device.logs`.
- Instrumented test(s) or a scripted journey that defines the expected end state.
- **Two fixtures of the same shape**: one on a Kotlin plugin version **below 2.4** and
  one **at or above**, because that is what decides whether Kotlin compiler errors
  reach us structured or only through the console
  ([ADR-0022](../adr/0022-build-error-extraction.md)). A real project confirms this
  matters: the spike project runs KGP 2.0.21.

Two practical notes:

- **DopamineControl has uncommitted work.** An acceptance run needs a clean checkout — a
  branch or a worktree — so the measurement is reproducible and nothing is disturbed.
- **CI still needs small synthetic fixtures.** DopamineControl is the acceptance target,
  not a test fixture: too large and too personal to pin in CI. The Gradle subsystem's
  fixture pattern (a generated project per pinned version) covers CI, and the two
  Kotlin-plugin fixtures above exist for that reason.

## MVP capability surface

| Domain | Capabilities | Phase |
| --- | --- | --- |
| `system` | `capabilities`, `health` | 0 |
| `workspace` | `open`, `describe`, `close` | 0 |
| `project` | `modules`, `variants` | 1 |
| `build` | `assemble`, `tasks`, `run`, `errors`, `lastResult` | 1 |
| `emulator` | `list`, `start`, `stop`, `wipe` | 1 |
| `sdk` | `status`, `requirements` | 1 |
| `device` | `list`, `install`, `launch`, `uninstall`, `logs`, `screenshot`, `uiTree`, `tapElement`, `inputText` | 1 |
| `file` | `list`, `read`, `write`, `watch` | 1–2 |
| `git` | `status`, `diff`, `log`, `branches` | last |
| `operation` | `wait`, `cancel` | 0 |
| `events` | `subscribe`, `unsubscribe` | 0 |
| `shell` | `exec` (off by default) | 1 |
| `artifact` | `read` | 1 |
| `policy` | `get`, `update` (human-only; never over MCP) | 1 |

`build.test` and `code.*` are **not** part of the MVP. `git.*` is included but
scheduled last and may slip.

## Definition of done

- [ ] The golden path completes from an external agent with **zero stdout parsing by
      the agent**.
- [ ] The golden path completes from the browser.
- [ ] `build.errors` returns structured `{file, line, column, message}` for a real
      Kotlin compile failure — on both fixtures, the KGP < 2.4 one going through the
      console extractor.
- [ ] `device.tapElement` and `device.inputText` verify their effect in a single call.
- [ ] `workspace.close` leaves no orphan processes. (**Not** the adb server, the
      Gradle daemon or emulators: those are not ours and are meant to survive.)
- [ ] Idle footprint reported **per process** (see Metrics), with the core under
      50 MB RSS and the Gradle sidecar stopped when idle.
- [ ] A recorded comparison of agent steps, tokens and failures: our MCP vs raw
      shell, on the golden path.
- [ ] `docs/README.md` updated; every deviation from the design recorded as an ADR.

## Metrics

The comparison below is **not** "our MCP vs. adb": our core invokes the *same* `adb`
binary. What differs is the **surface the agent works against**.

- **Baseline**: the agent with a plain shell — `adb devices`, `adb install`,
  `adb shell uiautomator dump` — parsing XML and stdout, guessing coordinates, and
  chaining steps itself.
- **Our case**: the agent calls semantic capabilities and receives JSON.

### How the comparison is run

LLM variance swamps a single run, so the measurement is specified before it is taken:

- **N ≥ 5 runs per arm** (ours and the shell baseline), same task, same starting
  state, reset by script between runs.
- **The agent and model version are pinned** and recorded; the same one is used for
  both arms.
- Recorded per run: **tool calls**, **failed/retried calls**, **tokens**, and
  **wall time** to reach the verified end state.
- **A pilot run comes first; the threshold is fixed afterwards — with one guard.**
  *Ratified 2026-09-29: pilot first, threshold afterwards.* The pilot exists to **size**
  the test — how much run-to-run variance the agent shows, and therefore how many runs
  each arm needs. The threshold itself is fixed **before the comparison runs**, from the
  question "is this difference worth building on", and **never from the pilot's observed
  difference**, which would be grading on a curve. The pilot does not count as a
  measurement.

### Other measures

- **Time to first useful result**: `workspace.open` → device list.
- **Idle memory, reported per process** and honestly: the core (budget: under 50 MB
  RSS), our Gradle sidecar (must be stopped when idle), the Gradle daemon, the Kotlin
  daemon if present, and the emulator. The core's budget is ours to meet; the rest is
  reported so the total is not a surprise.
- **Orphan process count** after close (target: zero).

Honest caveat: a strong agent with a plain shell is capable, and if the difference
turns out to be small, the hypothesis fails. That is precisely why this is measured
in the MVP rather than assumed.

## Risks and unknowns

| Risk | Mitigation |
| --- | --- |
| Build error shapes vary by AGP, Kotlin plugin and producer | A core console extractor for Kotlin, javac, kapt, KSP, AAPT2 and manifest-merger messages, with golden tests from recorded output. **Recorded so far: Kotlin, KSP and AAPT2, from the command line only** ([spike S1 run 1](../research/spike-s1-error-formats.md)); javac, kapt and manifest merger are still to record. The Problems API where the plugin versions deliver it; a labelled raw tail only when nothing is recognised ([ADR-0022](../adr/0022-build-error-extraction.md)) |
| AAPT2 errors name AGP's *merged* resources, not source files | Observed in spike S1 run 1. Working assumption: report the virtual path labelled `merged: true` always, and quote the merged line where the file can be located. **Must be settled before `build.errors` is implemented** ([ADR-0022](../adr/0022-build-error-extraction.md) §Known open item) |
| Compose exposes no resource-ids | DopamineControl ships `testTagsAsResourceId` on its Compose root; fall back to text/content-desc |
| `uiautomator dump` is flaky ("could not get idle state") | Retry once, then a cause-attributed error that routes to text/OCR later |
| MCP client differences (tool timeouts, tool limits, annotations) | Bounded wait with `operation.wait`; keep the exposed set small; verified against **Cursor and OpenCode**, the two the owner uses (spike S2) |
| The comparison metric is noisy | N runs, a pinned model, a scripted reset and a pre-stated threshold |
| `adb` version/serial drift | Detect and surface the version; keep the adb surface conservative |
| Gradle support range | Floor 8.0: fail fast below it with an escape hatch; newer versions proceed as `untested`; Gradle, AGP and Kotlin plugin versions detected and reported ([ADR-0018](../adr/0018-gradle-version-support-policy.md)) |
| The probe is heavier than a heuristic | *Ratified 2026-09-29: the probe.* It is an init script plus a small jar, used only for `project.model` and never injected into a user's build. Revisit if a simpler path proves sufficient ([ADR-0023](../adr/0023-android-project-facts-and-build-outputs.md)) |

## Decisions fixed by this MVP

- **Topology**: one daemon per user, attached by stdio and CLI shims
  ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)). This settles MCP
  transport: the stdio shim for the MVP, streamable HTTP later as another adapter.
- **Invocation**: `Start` / `Wait` / `Cancel`; `operation.wait` and `operation.cancel`
  are exposed; there is no domain-specific cancel
  ([ADR-0020](../adr/0020-operation-contract.md)).
- **Persistence**: JSON files in a per-user state directory, never inside the project
  ([ADR-0021](../adr/0021-persistence-format-and-location.md)).
- **No LSP in the MVP**; `code.*` is designed but not delivered.
- **Devices are named explicitly**; multi-device concurrency is designed-for, not
  delivered.
- **`git.*` is in scope but last**, and may slip without blocking the MVP.
- **LAN exposure is designed-for, not delivered**; the loopback HTTP baseline is
  required before the UI ships. *Ratified 2026-09-29: remote access comes after v1*, even
  though it is where the project started.
- **UI priority order**: Logcat → build/Gradle tasks → Git → devices/screen; the
  editor comes later, with code intelligence.
- **SDK provisioning is staged**: read-only requirement checking in the MVP, package
  installation and AVD creation after it.
- **The validation target is DopamineControl**, the owner's real project
  ([ADR-0024](../adr/0024-project-name-and-identifiers.md) covers the name);
  acceptance runs against it, while CI keeps small synthetic fixtures.
- **The assistants that matter are Cursor and OpenCode**; spike S2 measures their real
  client behaviour.
- **Remote access comes after v1**, although it is where the project started.
- **`file.*` is a first-class domain.**
- **Gradle runs through a JVM Tooling API sidecar**
  ([ADR-0017](../adr/0017-gradle-execution-strategy.md)), staged per the amended ADR:
  spikes **[S1 (one partial run so far; the Tooling API run is still pending) and S3]** →
  project model and `build.tasks` → execution with `build.errors` and
  outputs → the probe → `build.test`.
- **Gradle floor is 8.0**, with capability gating and a published supported range
  ([ADR-0018](../adr/0018-gradle-version-support-policy.md)).
- **`shell.exec` is off by default** ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §7),
  and **policy is never exposed over MCP**, not even in read-only form (§6).
- **Project roots are trusted explicitly** ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §5).
  An untrusted root opens in restricted
  mode, and capabilities that run project code fail with `WORKSPACE_UNTRUSTED`. Trust
  never comes from an MCP binding. *Ratified 2026-09-29, accepting the cost: friction on
  the first agent session in a new project.*
- **Artifacts are read through one capability**, `artifact.read`, in two durability
  classes ([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)).
