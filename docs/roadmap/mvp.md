# MVP

The first version: the smallest end-to-end system that can validate the core
hypothesis, and nothing else. Anything not listed under **In scope** is a non-goal for
the MVP. Where a milestone stands is in [`../plans/README.md`](../plans/README.md); this
document holds the scope, the phases' deliverables and acceptance, and the definition of
done.

## Terms

- **MVP**: this release. It exists to test the core hypothesis.
- **v1**: the first release meant for daily use: the MVP plus §Beyond the MVP's v1 list.
  v1 is where [vision](../vision/vision.md) success criterion 1 (code intelligence
  without Android Studio) is met.
- **Later**: after v1, with no commitment.

## Goal

Run the **golden path** (below) from an external agent over MCP and, in phase 2, from a
browser, and use it to test the core hypothesis from
[`../vision/vision.md`](../vision/vision.md):

> A stable semantic layer over the Android toolchain lets a general-purpose agent
> complete a `build → run → inspect → debug` loop with **fewer steps and fewer
> misinterpretations** than shell + adb + raw output.

It is falsifiable by comparison: the same task, agent and project, run twice, once
through our MCP surface and once through raw shell/adb.

## The golden path

One scenario. Everything in the MVP exists to serve it, on the golden sample,
`samples/golden/` ([below](#validation-target-the-golden-sample)):

| # | Step | Capability |
| --- | --- | --- |
| 1 | Open the project and list modules | `workspace.open`, `project.modules` |
| 2 | Build the debug variant | `build.assemble` |
| 3 | Introduce a compile error and recover | `build.errors` → file / line / column / message |
| 4 | Start (or adopt) an emulator | `emulator.start` / `device.list` |
| 5 | Install and launch the app | `device.install`, `device.launch` |
| 6 | Observe: logs, screen, UI tree | `device.logs`, `device.screenshot`, `device.uiTree` |
| 7 | Act and verify in one call | `device.tapElement`, `device.inputText` — with assertions |

**The agent never parses raw stdout.** The *core* does parse build console output (that
is its job, and how compile errors are extracted on real projects,
[ADR-0022](../adr/0022-build-error-extraction.md)); the guarantee is about the agent's
surface, not what happens underneath.

**"Act and verify in one call"** means the agent's *interaction* is one tool call.
Without assertions the pattern is `tap → wait → screenshot → model interprets → decide`
(three to five calls, the model reading pixels). With assertions one call acts, waits,
re-reads the tree and returns `{ acted, expected: { matched, afterMs } }`: fewer round
trips, fewer tokens, no image interpretation.

## Scope

### In scope

- **Go core**: one daemon owning all state, reachable over a Unix socket
  ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)).
- **Adapters**: an MCP stdio shim and a CLI shim over that socket; HTTP/WebSocket on
  loopback for the web UI (phase 2).
- **Invocation contract**: `Start` / `Wait` / `Cancel`, with `operation.wait` and
  `operation.cancel` ([ADR-0020](../adr/0020-operation-contract.md)).
- **Capability domains**: `system`, `workspace`, `project`, `build`, `device`,
  `emulator`, `sdk`, `file`, `git`, `artifact`, `policy`, `agent`, `operation`, `events`,
  `shell`, `session`. `file` is a first-class domain.
- **Sample app** as the acceptance target.
- **Minimal web UI** (phase 2), grown as needed rather than built to a fixed spec.
- **`git.*` as the last item**, a thin wrapper over the `git` CLI. It may slip to
  post-MVP without blocking: the agent already has git; the value is for the human UI.

### Out of scope (explicit)

- **Kotlin LSP and all code intelligence** (`code.*`): definition, references, symbols,
  diagnostics. Compile diagnostics come from `build.errors` instead
  ([ADR-0007](../adr/0007-kotlin-lsp-sidecar.md): the design stands, delivery is later).
- **Debugger / JDWP** stepping and breakpoints.
- **Multi-device concurrency.** One device at a time is acceptable; devices are always
  named explicitly, and the core must not be architected around a single global slot.
- **LAN exposure and access control.** The engine must be reachable remotely by design
  (no localhost assumptions in the capability layer), but remote access, TLS and
  authentication get no effort in the MVP. *Ratified 2026-09-29: remote access comes
  after v1*, though it is where the project started. The loopback HTTP baseline is
  still required before the UI ships.
- **Live screen streaming** to the UI: left out of the MVP (no stills, owner, 2026-10-09);
  built after it, in 2p (ADR-0077).
- **OCR** ([ADR-0013](../adr/0013-ocr-optional-pluggable.md) keeps the seam).
- **Distribution** beyond the binaries and the one-line installer
  ([ADR-0014](../adr/0014-agent-distribution-model.md)): registries, `llms-install.md`,
  signing. The binaries and the installer are in
  ([ADR-0068](../adr/0068-release-builds-and-installer.md)), as are the skill and `init`
  ([ADR-0037](../adr/0037-assistant-integration-by-init.md)).
- **Windows** ([ADR-0002](../adr/0002-unix-first.md)).
- **SDK provisioning and AVD creation.** `sdk.packages`, `sdk.install`, `sdk.licenses`,
  `emulator.create` and `emulator.delete` are post-MVP: the golden path needs only an
  emulator that already exists, and a package manager with licence consent and
  gigabyte downloads is a large block that does not validate the hypothesis.
  `sdk.requirements` **is** in scope because it makes Gradle failures explainable from
  day one.
- **Split APKs, App Bundles, dynamic features and instant apps.** One unfiltered APK per
  variant ([ADR-0023](../adr/0023-android-project-facts-and-build-outputs.md)).
- **Refactors, rename, call hierarchy.**
- **Batching.**
- **An own agent harness, Android Studio parity** (no full refactoring engine, visual
  layout editor or APK analyser), **an embedded emulator, multi-user or cloud use,**
  **a dependency on Android CLI, a Gradle CLI execution path** (every Gradle
  interaction goes through the TAPI sidecar,
  [ADR-0017](../adr/0017-gradle-execution-strategy.md); Android CLI:
  [ADR-0005](../adr/0005-reuse-platform-tools.md)).

### Beyond the MVP

- **v1 adds**: `code.*` (definition, references, symbols, diagnostics, through the
  Kotlin LSP) and a light editor with diagnostics in the UI; `build.test`; `device`
  processes, app info, backtrace and wireless pairing; `emulator.create`/`delete`;
  `sdk` package installation and licences.
- **Later**, with no commitment: LAN and tablet access to the UI (TLS and
  authentication need their own ADR); UI automation at scale (the row abstraction for
  unlabelled lists, OCR); crash and ANR analysis beyond structured logs; deeper code
  intelligence (call hierarchy, rename, code actions); MCP resources, prompts,
  elicitation and Streamable HTTP; distribution as an installable MCP server plus
  optional skills per agent; optional agent-specific adapters, plugins or hooks where
  they add real value.

## Phases

### Phase 0 — Foundations (walking skeleton)

Deliverables:

- **The daemon**: `droidyard serve`, single-instance via `flock`, Unix socket with
  peer-uid checks, the `droidyard mcp` / `droidyard call` shims with auto-start.
- The capability registry and schema as the single source of truth, the JSON Schema
  golden file generated and checked in. TypeScript generation is phase 2
  ([ADR-0016](../adr/0016-capability-schema-in-go.md)).
- The invocation contract, operation records and the error shape.
- Process supervisor: descriptors, lifecycle states, idle shutdown, no orphans.
- Workspace model (open / describe / close) and persistence in the per-user state
  directory ([ADR-0021](../adr/0021-persistence-format-and-location.md)).
- HTTP + WebSocket server skeleton on loopback.

Acceptance:

- `system.capabilities` lists what is available; when `adb` is absent it reports the
  device capabilities unavailable with the reason, each device call failing with
  `DEVICE_ADB_NOT_FOUND` (no workspace is degraded).
- A supervised dummy sidecar starts lazily, idles out, and leaves no orphans after
  `workspace.close`.
- The same capability is reachable from the CLI and from MCP with identical semantics.
- Killing a shim does not stop an operation it started.
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
- `artifact.*`: `read` — screenshots, log captures and build consoles reach consumers as
  opaque references in two durability classes
  ([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)).
- `policy.*`: `get`, `update` — **human-only** (UI, CLI or config file); the core
  rejects them from an MCP session
  ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §6).
- `shell.exec` (**off by default**; a call returns `POLICY_SHELL_DISABLED`).

Acceptance — the golden path is completed end-to-end by an **external agent** (**Cursor**
and **OpenCode**, the two the owner uses) over MCP, against the golden sample, with
structured results at every step and **no stdout parsing by the agent**. Accepted
2026-10-07 ([m1l plan](../plans/m1l-phase1-acceptance.md)).

### Phase 2 — Minimal web UI (closes the MVP)

Deliverables:

- Svelte 5 + TypeScript app the daemon serves ([ADR-0039](../adr/0039-frontend-svelte.md)).
- Panels: **Logcat**, streaming (2a); **build / Gradle tasks** with problems and
  **devices**, no stills (2b) ([m2a](../plans/m2a-minimal-web-ui.md)). Then (owner,
  2026-10-10): **2c**, fixes, the live build console and a project start without typed
  paths; **2d**, a redesign and a read-only file tree; **2e**, a UX round; **2f**, a sessions overlay and fixed panel controls; **2g**, the tree's file operations. After 2g, a milestone each, each after a
  discussion (owner, 2026-10-10): **agent sessions** (a terminal the daemon hosts on a
  pseudo-terminal, the agents' chats in it, a worktree per session); a **live emulator
  view** (the stretch below); **Git** (the tree's Git view and a Diff tab in 2l; branches and
  commits later); the **editor** ([ADR-0009](../adr/0009-editor-component.md)).
- TypeScript client generated from the capability schema.
- The loopback HTTP baseline (127.0.0.1, Host and Origin checks, a per-launch token),
  delivered early with the Phase 0 skeleton; Phase 2 adds no rule to it.

Stretch (not required to close the MVP): live screen stream to the device panel.

Acceptance:

- The golden path is drivable from the browser, including reading the structured build
  errors and seeing device state (step 6: logs and the UI tree, no screen).
- **Same-primitive check**: the UI and the agent reach the golden path through the same
  capabilities, verified from the capability trace (every call is an operation record,
  [ADR-0020](../adr/0020-operation-contract.md)).

## Validation target: the golden sample

**The acceptance target is `samples/golden/`** (owner, 2026-10-07: "Si dopamine control
no te sirve, create una app de ejemplo sencilla que te haga el mismo papel"). The
ratified 2026-09-29 choice, DopamineControl (the owner's real multi-module project),
lacked the form, the `testTagsAsResourceId` and the crash path; the argument for a real
project still holds, and it stays the project the Gradle work was tried on (1g).

What the golden path needs from the target:

- Kotlin + Jetpack Compose, one activity, a navigation with 2–3 screens.
- A **form** (text input, button, validation) to exercise `uiTree` / `tapElement` /
  `device.inputText`.
- A **list with a counter** (items with a like count) to exercise element identity and,
  later, the row abstraction.
- **`testTagsAsResourceId = true`** on the Compose root: Compose exposes no resource-ids
  to uiautomator by default, and stable ids are what makes identity-first automation
  work.
- A deliberate **compile-error branch** (documented, reverted) for `build.errors`.
- A deliberate **crash path** (a button that throws) for `device.logs`.
- Instrumented test(s) or a scripted journey that defines the expected end state.
- **Two fixtures of the same shape**, one on a Kotlin plugin **below 2.4** and one **at
  or above**: that decides whether Kotlin compiler errors reach us structured or only
  through the console ([ADR-0022](../adr/0022-build-error-extraction.md)). The real
  project of the spike runs KGP 2.0.21.

Two practical notes:

- **An acceptance run needs a clean checkout**, so the measurement is reproducible: the
  sample is copied with `git archive` from the commit under test.
- **CI still needs small synthetic fixtures.** The acceptance target is built only in
  scratch copies; the Gradle subsystem's fixture pattern (a generated project per
  pinned version) covers CI.

## MVP capability surface

| Domain | Capabilities | Phase |
| --- | --- | --- |
| `system` | `capabilities`, `health` | 0 |
| `workspace` | `open`, `enter`, `describe`, `close`, `watch`, `chooseRoot`, `show`, `forget` (the last three human-only; never over MCP) | 0, 2 |
| `project` | `modules`, `variants` | 1 |
| `build` | `assemble`, `tasks`, `run`, `errors`, `lastResult`, `watchOutput` | 1–2 |
| `emulator` | `list`, `start`, `stop`, `wipe` | 1 |
| `sdk` | `status`, `requirements` | 1 |
| `device` | `list`, `install`, `launch`, `uninstall`, `logs`, `watchLogs`, `watchScreen`, `screenshot`, `uiTree`, `tapElement`, `inputText`, `tap`, `swipe`, `pressKey` | 1–2 |
| `file` | `list`, `read`, `write`, `watch`, `create`, `move`, `copy`, `delete`, `restore` (human-only; never over MCP) | 1–2 |
| `git` | `watch`, `read`, `log`, `refs`, `show`, `fetch`, `pull`, `checkout`, `createBranch`, `submodules`, `updateSubmodules` (human-only; never over MCP) | last |
| `operation` | `wait`, `cancel`, `watch` | 0, 2 |
| `events` | `subscribe`, `unsubscribe` | 0 |
| `session` | `watch` | 2 |
| `shell` | `exec` (off by default) | 1 |
| `artifact` | `read` | 1 |
| `policy` | `get`, `update` (human-only; never over MCP) | 1 |
| `agent` | `recordCommand`, `recordEdit`, `checkWrite` (human-only; never over MCP) | 2 |

`build.test` and `code.*` are **not** part of the MVP. `git.*` is included but scheduled
last and may slip.

## Definition of done

- [x] The golden path completes from an external agent with **zero stdout parsing by the
      agent** (m1l: OpenCode and Cursor, on the golden sample).
- [x] The golden path completes from the browser (m2b step 7).
- [x] `build.errors` returns structured `{file, line, column, message}` for a real Kotlin
      compile failure — on both fixtures, the KGP < 2.4 one going through the console
      extractor (m2h step 3).
- [x] `device.tapElement` and `device.inputText` verify their effect in a single call
      (m1l, both clients, on Compose).
- [x] `workspace.close` leaves no orphan processes of droidyard's (1c, on a supervised
      workspace-owned sidecar; the Gradle sidecar is global and ends with
      `droidyard stop`). **Not** the adb server, the Gradle daemon or emulators: those
      are not ours and are meant to survive.
- [x] Idle footprint reported **per process** (see Metrics), with the core under 50 MB
      RSS and the Gradle sidecar stopped when idle (m2h step 5: 26.1 MB at the end, 56.8 MB 30 s after the last call).
- [x] A recorded comparison of agent steps, tokens and failures, our MCP vs raw shell,
      on the golden path: **supported** by the threshold below
      ([m1m](../plans/m1m-emulator-robustness.md); [m1o](../plans/m1o-tokens-and-harness.md), Cursor).
- [x] Every deviation from the design recorded as an ADR: those the Done plans recorded
      (m2h §Audit ledger); unrecorded ones are a register row.

## Metrics

The comparison is **not** "our MCP vs. adb": our core invokes the *same* `adb` binary.
What differs is the **surface the agent works against**. Baseline: the agent with a
plain shell (`adb devices`, `adb install`, `adb shell uiautomator dump`), parsing XML
and stdout, guessing coordinates, chaining steps itself. Our case: semantic
capabilities returning JSON.

### How the comparison is run

LLM variance swamps a single run, so the measurement is specified before it is taken:

- **N ≥ 5 runs per arm**, same task, same starting state, reset by script between runs.
- **The agent and model version are pinned**, recorded, and the same for both arms.
- Recorded per run: **tool calls**, **failed/retried calls**, **tokens**, and **wall
  time** to reach the verified end state.
- **A pilot run comes first; the threshold is fixed afterwards, with one guard.**
  *Ratified 2026-09-29.* The pilot **sizes** the test (run-to-run variance, hence runs
  per arm) and does not count as a measurement. The threshold is fixed **before the
  comparison runs**, from the question "is this difference worth building on", and
  **never from the pilot's observed difference**, which would be grading on a curve.
- **As run (m1m)**: N = 5 per arm, fixed by the owner's brief rather than sized by the
  pilot, ABBA order, a recreated scratch AVD and fresh copy per run. The threshold,
  fixed before the runs and applied in order: droidyard is *not supported* if it
  completes fewer than 4 of 5 runs or fewer than the shell; if its median failed calls
  are higher; or if its median calls exceed 75 % of the shell's; otherwise *supported*
  if at least 20 of the 25 (droidyard, shell) run pairs have fewer droidyard calls,
  else *inconclusive*. Calls are every tool part the agent issued. The results are in
  the m1m plan.

### Other measures

- **Time to first useful result**: `workspace.open` → device list.
- **Idle memory, reported per process** and honestly: the core (budget: under 50 MB
  RSS), our Gradle sidecar (must be stopped when idle), the Gradle daemon, the Kotlin
  daemon if present, and the emulator. The core's budget is ours to meet; the rest is
  reported so the total is not a surprise.
- **Orphan process count** after close (target: zero).

Honest caveat: a strong agent with a plain shell is capable, and if the difference
turns out small, the hypothesis fails. That is why this is measured in the MVP rather
than assumed.

## Risks and unknowns

| Risk | Mitigation |
| --- | --- |
| Build error shapes vary by AGP, Kotlin plugin and producer | A core console extractor for Kotlin, javac, kapt, KSP, AAPT2 and manifest-merger messages, with golden tests from recorded output (recorded from the command line so far: Kotlin, KSP, AAPT2, [spike S1](../research/spike-s1-error-formats.md); javac, kapt and manifest merger still to record). The Problems API where the plugin versions deliver it; a labelled raw tail only when nothing is recognised ([ADR-0022](../adr/0022-build-error-extraction.md)) |
| AAPT2 errors name AGP's *merged* resources, not source files | Report the virtual path labelled `merged: true`, and quote the merged line where the file can be located ([ADR-0022](../adr/0022-build-error-extraction.md) §Known open item) |
| Compose exposes no resource-ids | The golden sample ships `testTagsAsResourceId` on its Compose root, and Droidyard's reader resolves the tags as ids; fall back to text/content-desc |
| `uiautomator dump` is flaky ("could not get idle state"); since ADR-0029 it is only the fallback to Droidyard's reader | Retry once, then a cause-attributed error that routes to text/OCR later |
| MCP client differences (tool timeouts, tool limits, annotations) | Bounded wait with `operation.wait`; keep the exposed set small; verified against **Cursor and OpenCode** (spike S2) |
| The comparison metric is noisy | N runs, a pinned model, a scripted reset and a pre-stated threshold |
| `adb` version/serial drift | Detect and surface the version; keep the adb surface conservative |
| Gradle support range | Floor 8.0: fail fast below it with an escape hatch; newer versions proceed as `untested`; versions detected and reported ([ADR-0018](../adr/0018-gradle-version-support-policy.md)) |
| The probe is heavier than a heuristic | *Ratified 2026-09-29: the probe.* An init script plus a small jar, used only for `project.model` and never injected into a user's build. Revisit if a simpler path proves sufficient ([ADR-0023](../adr/0023-android-project-facts-and-build-outputs.md)) |
