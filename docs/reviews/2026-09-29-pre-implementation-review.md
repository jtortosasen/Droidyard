# Pre-implementation design review

- **Date**: 2026-09-29
- **Reviewer**: Claude Opus 5.5 (high reasoning), run as an isolated reviewer subagent
- **Scope**: everything under `docs/`
- **Verdict**: **do not start implementation yet**
- **Status**: **resolved.** All twelve blocking issues and all twenty-one non-blocking
  ones were closed in two rounds of fixes plus a verification pass; the four
  contract-level blockers that the fixes themselves introduced were closed in a third.
  The fifteen decisions left to the owner are recorded, closed, in
  [`2026-09-29-ratifications.md`](2026-09-29-ratifications.md). This document is kept as
  the record of what was found, not as a list of open work.

---

## Verdict

**No. The design is not ready for implementation to start.** The core ideas hold up:
one capability API, thin adapters, no in-house harness, a TAPI sidecar, and ADRs that
list their alternatives. The problems are in how the pieces connect, and all of them
are cheap to fix on paper and expensive to fix in code. Before Phase 0 you need to
settle:

1. **Process topology and MCP transport.** How a persistent core coexists with stdio
   MCP servers that agents spawn (B1).
2. **Workspace addressing.** Which workspace a call applies to (B2).
3. **One invocation contract.** Sync, async and cancellation behaviour is currently
   described three different ways (B3).
4. **Where the schema comes from.** `capability-api.md` and `mvp.md` contradict
   ADR-0016 (B4).
5. **The `Descriptor` struct.** It is the Phase 0 deliverable and does not match
   ADR-0010 (B5).
6. **Persistence format and location** (B6).

Before Phase 1, the Gradle premise needs a spike. The Problems API only delivers
Kotlin compiler errors on Kotlin Gradle Plugin (KGP) 2.4+ (B7). You also need a
contract for the build → install handoff (B8) and a mechanism for `project.variants`
(B9). The device-targeting contradiction must be resolved (B10). Agents need a way to
read the artifacts they are handed (B11).

---

## Blocking issues

### Before Phase 0

**B1. There is no process topology linking the "persistent core" to stdio MCP.**

- *Where:* `vision/vision.md` §What we are building ("persistent local process").
  `architecture/core-runtime.md` §Package layout (`adapters/mcp/ — MCP stdio`).
  `roadmap/mvp.md` §Decisions fixed ("MCP transport: stdio… *Needs an ADR*").
  `adr/README.md` §Open decisions (transport still open).
  `interfaces/mcp-tool-surface.md` §Statefulness ("over stdio, the session is the
  process"). `architecture/workspace-state.md` §Identity and reuse ("Two cores must
  never manage the same workspace concurrently").
- *Why it blocks:* an MCP client spawns a stdio server per session and kills it when
  the session ends.
  - If that spawned process is the core, the web UI dies with the agent session and
    running builds are cancelled.
  - Two agent windows on the same project become two cores fighting over one
    workspace lock.
  - Nothing says how the core is started (foreground, daemon, launchd/systemd) or how
    `mydev call` (the CLI) reaches it.
  - "The session is the process" is only true if the core *is* the stdio process, and
    that contradicts "persistent".
- *Smallest fix:* write one ADR:
  - a single long-lived `mydev serve` owns all state and listens on a Unix domain
    socket, plus loopback HTTP/WS for the UI;
  - `mydev mcp` is a stdio↔socket shim that attaches to the daemon and auto-starts it
    if absent;
  - `mydev call` works the same way;
  - define what an MCP "session" is (one shim connection), and what happens to
    in-flight operations when it disconnects (continue, or cancel).

**B2. The invocation contract never says which workspace a call targets.**

- *Where:* `architecture/capability-api.md` §Request/response envelope has
  `{id, capability, params, raw}` and no workspace. The same doc's §Event envelope
  *does* carry `workspace`. `architecture/workspace-state.md` §What a workspace is:
  "The core can host **multiple workspaces**". No MCP tool in
  `interfaces/mcp-tool-surface.md` takes a workspace parameter.
- *Why it blocks:* it changes every capability signature and every schema. The only
  implicit option, a session-bound "current workspace", conflicts with the project's
  own no-implicit-target stance (`capability-api.md` §One name for the target).
- *Smallest fix:* choose one:
  - (a) one workspace per core in v1, stated explicitly; or
  - (b) a required `workspace` parameter on workspace-scoped capabilities, mirroring
    the `device` rule.

  Also mark each capability as *global* or *workspace-scoped*. `device.*`,
  `emulator.*` and `sdk.status` are global per `workspace-state.md` §Devices are not
  workspace-owned.

**B3. Sync, async and cancellation behaviour is described three incompatible ways.**

- *Where:*
  - `capability-api.md` §Long-running operations: commands return an operation handle
    immediately.
  - `capability-api.md` §Mapping to adapters: MCP gets `operation_status` /
    `operation_cancel` plus polling.
  - `core-runtime.md` §Invocation: *every* invocation is an operation, and a long
    command "may run in the background… or block". It never says who decides.
  - `mcp-tool-surface.md` §What is deliberately not exposed: `operation.*` is **not**
    exposed; calls block and stream progress.
  - `subsystems/gradle.md` §Capability mapping: a separate `build.cancel` capability,
    and `build.assemble` "returns a build id immediately".
- *Why it blocks:* `Invoke` is the first function in the core, and every adapter
  depends on its semantics.
- *Smallest fix:* one core primitive set: `Start(name, params) → opID`,
  `Wait(opID, deadline) → result | stillRunning`, `Cancel(opID)`. Adapters compose
  these. Delete `build.cancel` or make it an alias of `operation.cancel`. Then decide
  the MCP projection. See R1: pure blocking will not survive real client timeouts.

**B4. The docs disagree on where the schema comes from.**

- *Where:* ADR-0016 (Accepted) says Go structs are the source, JSON Schema is
  generated from them, and the TS client comes in phase 2. `capability-api.md` §The
  schema is the single source of truth says the schema *generates* Go types, the
  language is "an open decision", and JSON Schema is "the working assumption"; its
  §Open questions still lists "Schema language". `roadmap/mvp.md` Phase 0 deliverable
  1 is "generation of Go types, the TypeScript client and MCP tool definitions".
- *Why it blocks:* it is literally the first Phase 0 deliverable, and a builder cannot
  satisfy both documents.
- *Smallest fix:* rewrite `capability-api.md` §Schema and `mvp.md` Phase 0 to match
  ADR-0016. Decide whether TS generation belongs in Phase 0 or Phase 2.

**B5. The `Descriptor` struct doesn't match the contract it is supposed to be the
single source for.**

- *Where:* `core-runtime.md` §The capability registry has `Stateful bool` and
  `Kind Query|Command`. ADR-0010 has three values (`stateless | stateful | streaming`),
  and `capability-api.md` §Concepts has a *Stream* kind.
- *What's missing from the struct:*
  - scope (global vs workspace, see B2);
  - a long-running flag, or the default sync mode (B3);
  - an untrusted-output marker (ADR-0015);
  - version and `since`/`deprecated` (`capability-api.md` §Discovery and versioning);
  - `idempotencyKey` support (`capability-api.md` §Idempotency).
- *Why it blocks:* `core-runtime.md` §What comes first puts this struct first, and it
  feeds dispatch, the schema, MCP and the docs.
- *Smallest fix:* fix the struct in the doc now.

**B6. Persistence is undecided, but Phase 0 needs durable state.**

- *Where:* `adr/README.md` §Open decisions. `workspace-state.md` §Durable vs ephemeral
  ("open decision"). `mvp.md` Phase 0 ("durable vs ephemeral state"). `gradle.md`
  §Persistence ("the workspace state directory", never defined). ADR-0017 §Recorded
  sub-decisions (`lastResult` persists). `core-runtime.md` §Open questions cites
  "ADR-0011 and ADR-0017" for this, but ADR-0011 is about skills.
- *Also contradictory:* `process-lifecycle.md` §Crash recovery reconciles "processes
  recorded as running", which requires persisted pids and lock files.
  `workspace-state.md` §Durable vs ephemeral says processes are ephemeral and "never
  persisted".
- *Smallest fix:* JSON files under `$XDG_STATE_HOME/mydev/<wsid>/` on Linux and
  `~/Library/Application Support/mydev/<wsid>/` on macOS, never inside the project
  tree. Plus a pid file holding pid and start time for lock reclamation. Make pid
  records an explicit durable exception.

### Before Phase 1 (the golden path)

**B7. `build.errors` rests on an assumption that is probably false for most real
projects, and the version policy gates on the wrong axis.**

- *Where:* `subsystems/gradle.md` §Unverified assumptions, ADR-0017 §Decision,
  ADR-0018 §Decision, `mvp.md` §Definition of done.
- *Evidence:* the Kotlin 2.4.0 release notes say that in Kotlin 2.2 KGP began
  reporting *its own* diagnostics to the Problems API. Only in **2.4.0** does it "also
  write compiler messages to the Problems API for Kotlin/JVM". So file/line/column for
  Kotlin compile errors depends on the **project's KGP version**, not just its Gradle
  version. ADR-0018 and `GradleCapabilities` only look at Gradle. Most Android
  projects in September 2026 will still be on KGP ≤ 2.3.
- *Further gaps:*
  - `research/gradle-version-compatibility.md` §3 dates Problems *locations* to 8.13,
    but `gradle.md` §Error model implies they are available from 8.4.
  - AAPT2 resource-linking, manifest-merger and KSP/kapt errors are not discussed at
    all, and these are the most common Android-specific failures.
  - `mvp.md` §Risks says "degrade to raw tail", which contradicts the DoD's "zero
    stdout parsing".
  - ADR-0017 §Decision staging ("`build.tasks` and `build.errors` first, then
    execution") is incoherent: errors only exist after a build *executes*. During that
    stage, `build.assemble` would have to run via the CLI, which is option C, rejected
    in the same ADR.
- *Why it blocks:* ADR-0017's rationale is "the highest-value outputs live exactly
  where TAPI is strong". If Kotlin errors need console parsing anyway
  (`e: file:///…:L:C msg`), part of the case for the sidecar moves.
- *Smallest fix:* run spike S1 **before** writing the sidecar. Add KGP (and AGP) to
  the capability profile. Define a first-class fallback extractor for Kotlin, javac
  and AAPT2 output, applied by the core to the console stream the sidecar forwards.
  Fix the ADR-0017 staging text.

**B8. The build → install handoff has no contract.**

- *Where:*
  - `mcp-tool-surface.md` table: `device_install` "Install an APK", with no input
    specified.
  - `gradle.md` §The build model has no outputs field.
  - `adb-device.md` §Application id: the applicationId comes from
    `output-metadata.json`, "then the module build script, then a legacy manifest".
    Parsing a build script *is* guessing, which contradicts the section's own "does
    not guess".
  - Principle 14 and ADR-0012 forbid paths in results.
- *Why it blocks:* golden path steps 2 → 5.
- *Smallest fix:* the build result carries
  `outputs[] {module, variant, applicationId, versionCode, apks[]}`, read from
  `output-metadata.json`. Then `device.install({device, build: <buildId> | {module,
  variant}})` resolves the paths internally. Drop build-script parsing. State whether
  split APKs and AABs are out of scope for the MVP.

**B9. `project.variants` (and `sdk.requirements`) have no mechanism.**

- *Where:* `mvp.md` §MVP capability surface (both are Phase 1). `capability-api.md`
  §Initial domains ("`project` … backed by Gradle"). `gradle.md` §Protocol has no
  project or model method, yet §Lifecycle says the sidecar starts on the first
  `project.*` call. `sdk.md` §Capabilities (`sdk.requirements(project)`).
- *Why it blocks:* generic TAPI models (`GradleProject`) give modules and tasks, not
  Android variants, `compileSdk`, `buildToolsVersion` or applicationId. Those need
  AGP's builder model (the Android Studio sync protocol). That model is versioned per
  AGP release and needs model classes on the sidecar classpath. The alternative is
  heuristics over `assemble<Variant>` task names, which get awkward with flavours.
- *Smallest fix:* decide per capability. For the MVP, `variants` = parsed from task
  names, documented as a heuristic. `sdk.requirements` = map known Gradle/AGP failure
  signatures, or a small init-script probe, isolated per ADR-0018 rule 4. Or drop both
  from the MVP. Add a `project.model` method to the sidecar protocol.

**B10. Device targeting contradicts itself.**

- *Where:*
  - Against implicit targets: `capability-api.md` §One name for the target ("no
    implicit current device"); `mcp-tool-surface.md` §Server instructions ("never
    assume one"); `research/prior-art.md` convergent finding 5.
  - For implicit targets: `adb-device.md` §Target resolution (step 2 "workspace's
    active device", step 3 "only if exactly one online device"); `workspace-state.md`
    §State model ("active device"); `mvp.md` §Decisions ("One active device").
- *Why it blocks:* it sets whether `device` is required in every device schema.
- *Smallest fix:* `device` is required at the capability layer. The UI keeps its
  selection client-side and passes it explicitly. Drop steps 2 and 3, or restrict them
  to the UI adapter.

**B11. Agents cannot read the artifacts they are given.**

- *Where:*
  - `mcp-tool-surface.md` table: `device_screenshot` returns "an artifact reference".
  - `mcp-features.md` §2: MCP resources are post-MVP.
  - `adb-device.md` §Logs and `gradle.md` §Output: logs and the build console are refs
    too; `gradle.md` adds "consumers read it by byte offset" but never says through
    what.
  - No `artifact.*` capability exists in any list.
- *Why it blocks:* golden path step 6 ("observe… screen") is impossible for the agent.
- *Smallest fix:*
  - the MCP adapter inlines a bounded, downscaled `ImageContent` alongside the ref;
  - add `artifact.read {ref, offset, limit}`, or a `build.console` capability;
  - specify the UI HTTP route;
  - resolve ADR-0012 (in-memory, TTL) against `gradle.md` (console persisted and
    referenced from a `lastResult` that survives restarts). That needs a durable
    artifact class whose refs stay valid across restarts.

**B12. `gradle.md` says its own design is not ratified, and the sidecar cannot be
launched yet.**

- *Where:* `gradle.md` status note ("**Proposed** and need ratification before
  implementation") covers ownership, protocol and output transport. §Environment and
  JVM: the sidecar JVM "detect vs bundle" question is still open. The same question is
  open in ADR-0017.
- *Smallest fix:* ratify the proposals, or amend them. For the MVP, decide a JVM
  discovery order:
  1. configured path
  2. `JAVA_HOME`
  3. Android Studio's bundled JBR at known paths
  4. `/usr/libexec/java_home -v 17+` on macOS
  5. `PATH`

  Defer jlink bundling.

---

## Non-blocking issues

**N1. Stale and superseded statements are still presented as live.**

- `vision/scope.md` §Out of scope says "**Gradle Tooling API sidecar.** v1 uses the
  Gradle CLI". This flatly contradicts ADR-0005 and ADR-0017. The same file has `code.*`
  in v1 (the MVP excludes it), "UI tree automation" deferred (the MVP includes
  `uiTree`/`tapElement`), and "Structured Logcat" deferred. How "v1" relates to "MVP"
  is never defined.
- `architecture/overview.md` §Layers lists "Gradle CLI". §Process topology has the core
  supervising the "Gradle daemon" and shows no TAPI sidecar. Its domain list lacks
  `sdk`, `file`, `project` and `system`.
- ADR-0001 §Context says the core supervises the "Gradle daemon"; §Consequences says
  Gradle output parsers are written in Go. `vision/principles.md` #7 says the Gradle
  daemon has an explicit owner. The glossary and ADR-0017 both say it is Gradle's.
- Stale "(planned)" references: ADR-0003 (capability-api), ADR-0005 (adb-device),
  `overview.md` (process-lifecycle, workspace-state), `glossary.md` §Workspace.
- Broken links: ADR-0017 → `../../research/gradle-mcp.md` (should be `../research/`),
  and `capability-api.md` §Cross-cutting → `../../research/prior-art.md`.
- ADR-0017's header says Accepted 2026-09-27; its body says "Accepted on 2026-09-28".

**N2. The capability names disagree across documents.**

- `device.logs` (MVP) vs `runtime.logcat` / `runtime.crash` (`capability-api.md`
  §Naming).
- The `sdk` domain is missing from `capability-api.md` §Initial domains.
- `git.log` is missing from §Naming.
- `build.cancel` exists only in `gradle.md`.
- `file.watch` is in the `mvp.md` surface table but missing from the Phase 1 list and
  the MCP table.
- `build.test` is in capability-api and gradle but not in the MVP, while the sample app
  ships instrumented tests.

`capability-api.md` says "Domains are fixed by the capability schema". Make one
generated catalogue authoritative and delete the hand lists.

**N3. The annotation table breaks its own rules.** See `mcp-tool-surface.md`
§Annotations vs §The MVP tool surface.

- `openWorldHint` is "true for anything that touches a device", yet `device_list`,
  `device_logs`, `device_screenshot`, `device_uiTree` and `emulator_list` aren't marked.
- `build_run` runs arbitrary tasks (`clean`, `uninstallAll`, publishing) but isn't
  `destructive`.
- `emulator_start` is marked `idem`, while `emulator.md` §Design rule says boot is
  "non-idempotent" (and its §Failure modes says "adopt").
- `workspace_close` is `idem` but cancels running builds.

Deriving hints from `Kind` alone cannot express `build.run`. Allow a per-descriptor
override, or split the capability.

**N4. Untrusted-data tagging is inconsistent.** `mcp-tool-surface.md` §Untrusted data
says *per result*. ADR-0015 §Consequences says consumers must "distinguish trusted
control data from untrusted device payloads **in results**", which is per field. The
mechanism (a result field or MCP `_meta`) is unspecified.

**N5. The error shape varies.** `capability-api.md` §Error model has
`{code, message, details}`. `core-runtime.md` §Invocation and `mcp-tool-surface.md`
§Errors have `{code, message, cause, nextStep}`. Error codes are called "versioned"
without saying how.

**N6. Event sequencing is underspecified.** `seq` is per workspace
(`capability-api.md`, `core-runtime.md` §Events), but devices, emulators and the adb
server are global (`workspace-state.md` §Devices). Which sequence do their events use?
Gap detection is also useless without a replay buffer or a `since` resume, and neither
is specified.

**N7. Gradle concurrency and sidecar scope are unclear.**

- `core-runtime.md` §Concurrency serialises *builds* per project. But a `tasks.list`
  model fetch during a running build makes Gradle start a **second daemon**, because
  busy daemons are not reused. That costs gigabytes. Serialise *all* TAPI interactions
  per root.
- Queue vs reject is unspecified for when the UI and the agent both build at once.
- `process-lifecycle.md` §Kinds of processes makes the sidecar per workspace, but the
  `gradle.md` protocol passes `root` on every call. One JVM can serve all roots.
  Decide which.

**N8. The build model conflates levels.** `gradle.md` §The build model mixes
build-level and task-level outcomes. `UP_TO_DATE`, `FROM_CACHE` and `NO_SOURCE` are
task outcomes, and `status: cancelled` duplicates `outcome: CANCELLED`. Also,
`research/gradle-version-compatibility.md` §3 notes that Gradle 9.8 steers integrators
toward failure-carried problems, while the design subscribes to progress events
(`event.build.problem`).

**N9. The version policy has holes.** ADR-0018:

- Gradle 7.3–7.5 belongs to no tier.
- The "conservative profile" escape hatch is never defined.
- Tier C (7.6) sits below the 8.0 floor, which "fails fast", so which rule applies?
- `mvp.md` §Risks says "fail with a clear, actionable error", which conflicts with
  rule 2 (never hard-fail on newer versions).
- An "init-script canary" is required (ADR-0018 §Consequences, `gradle.md` §Testing),
  but no init script exists in the design. Name the scripts or drop the canary.

**N10. Filtering logcat by package breaks on exactly the MVP crash path.**
`adb-device.md` §Logs filters "package (via pid)". After the sample app's deliberate
crash (`mvp.md` §Sample app), the process is gone, so pid resolution fails, and the
rule that empty is a fact would report *nothing*. Fix: have `device.launch` return the
pid, keep a per-package pid history, and always include `-b crash`.

**N11. Emulator details.**

- A literal Go port of the prior art's "Detached and unreferenced"
  (`emulator.md` §Design rule) needs `Setsid`, stdio redirected to a file (the
  §Failure modes "stderr tail" depends on this), and a reaping `Wait` or a double
  fork. Otherwise you get zombies.
- AVD discovery must honour `ANDROID_AVD_HOME` and `ANDROID_USER_HOME`, not only
  `~/.android/avd`.
- `emulator.wipe` (MVP, destructive) on a running AVD is unspecified: it needs a stop
  plus a `-wipe-data` boot.
- `emulator.md` §Open questions ("expose `sdkmanager` at all — probably not")
  contradicts `sdk.md`'s `sdk.install` and `sdk.packages`.

**N12. SDK resolution ignores `local.properties`.** `sdk.md` §SDK root resolution and
`adb-device.md` §adb resolution never read `local.properties` `sdk.dir`, which is what
AGP actually uses. `sdk.requirements` may check a different SDK than the one the build
uses. The Windows path in `sdk.md` is also out of scope.

**N13. Orphans after a core crash.** Putting sidecars in their own process groups
(`process-lifecycle.md` §Shutdown) means a SIGKILLed core does *not* take them down.
Specify that the sidecar exits on stdin EOF (we write it, so we can enforce this), plus
`Pdeathsig` on Linux. The "Gradle and LSP both fork" rationale is also inaccurate: the
Gradle daemon detaches itself by design.

**N14. The local HTTP security baseline is missing.** `mvp.md` defers auth. But a
loopback HTTP server that exposes `shell.exec` and `file.write` is reachable from any
web page via CSRF or DNS rebinding. Before the Phase 0 HTTP skeleton ships, require:

- a default bind to 127.0.0.1;
- Host and Origin checks;
- a per-launch token.

The prior art did this (`research/dsh-codex-android-concepts.md` §7). Separately,
`workspace.open` configures Gradle, which executes project code. ADR-0015 covers
devices only; it needs a project-root allowlist.

**N15. Policy mutation has no home, and one version of it would let the agent grant
itself access.** ADR-0015 §5 says policy is "live-configurable through the capability
API", but no `policy.*` capability exists anywhere. If one is added, principle 1
exposes it over MCP, and the agent can enable physical devices or `shell.exec` for
itself. `shell.exec` enablement also contradicts itself: `capability-api.md`
§Structured vs raw says "must be explicitly enabled", while §Open questions in both
`capability-api.md` and `mcp-tool-surface.md` still asks the question. Record a
deliberate exception: policy mutation goes through the UI, CLI or config file only,
never MCP.

**N16. The memory budget no longer tests anything.** The `mvp.md` DoD only checks core
RSS < 50 MB, which is trivially met. That does not test vision success criterion 3
("dominated by the unavoidable JVM sidecars"). ADR-0017 said the budget "must be
restated"; excluding the JVMs is not restating it. Report the realistic post-build idle
total per process: Gradle daemon, Kotlin daemon, sidecar and emulator.

**N17. Definition-of-done wording.** "`workspace.close` leaves no orphan processes"
must explicitly exclude the adb server, the Gradle daemon and emulators, which are
meant to survive. The DoD asks for `{file, line, message}`, while `gradle.md` calls
column "non-negotiable". The "capability trace" used for the Phase 2 same-primitive
check has no spec.

**N18. The hypothesis test is under-designed.** `mvp.md` §Metrics compares one run per
arm, but LLM variance swamps that. Specify N runs, a pinned model version, tokens and
wall time, and a *pre-stated* threshold. The sample app exercises text input, but the
MVP surface has no text-input capability. The agent will fall back to `shell_exec`,
which contaminates the metric. Add `device.inputText` or drop text from the journey.

**N19. `tapElement` assertions depend on an unwritten document.** The semantics rest on
the planned `subsystems/ui-automation.md`. The prior art's `expect_text` used OCR
polling (`research/dsh-codex-android-concepts.md` §4), and OCR is out of v1. Define the
assertion vocabulary, settle and poll timing, and tree-only matching before Phase 1.

**N20. MCP wire details.**

- Dot→underscore mapping is only reversible if capability names never contain `_`.
  State that rule.
- Go JSON-Schema generators emit `$ref`/`$defs` and unions by default, and several
  clients reject these in `inputSchema`. Constrain the generator: inline everything and
  no top-level `oneOf`.

**N21. Nothing is classified by statefulness yet.** No MVP capability has a
classification. After the bounded-follow decision, probably only `events.subscribe`
and `file.watch` are stateful. Put it in the catalogue.

---

## Risks: decisions that will likely break on contact

**R1. Blocking MCP calls for builds and boots.** `mcp-tool-surface.md` §Long-running
work assumes minute-long blocking calls kept alive by progress notifications. In
practice:

- many clients have tool-call timeouts around 60 s (Codex's `tool_timeout_sec`, for
  instance);
- progress only flows if the client sends a `progressToken`;
- progress does not always reset the timeout;
- the client's timeout sends `notifications/cancelled`, which the adapter maps to a
  core cancel, so **a client timeout kills the build**.

Plan for a bounded wait: `build_assemble` blocks up to N seconds, then returns
`{operationId, state: running}`, and an `operation_wait` tool continues. That reverses
the "`operation.*` not exposed" decision, so decide it now.

**R2. Serialising only builds per project** (see N7) will cause silent daemon
multiplication.

**R3. The sidecar's environment differing from the user's shell.** On macOS, a
GUI-launched agent spawns the core without the shell's `JAVA_HOME` or environment.
Gradle daemon compatibility is keyed on Java home and JVM args, so the sidecar's builds
and the user's terminal or Android Studio builds won't share a daemon. That doubles
resident memory and cold-start time. `gradle.md` §Environment names the problem but not
the consequence.

---

## Assumptions that need a spike

- **S1. Problems API on real Android** (the critical one). Matrix: KGP 2.3 vs 2.4 ×
  Gradle 8.13 / 9.x × AGP 8.x / 9.x. Error types: Kotlin, javac, AAPT2, manifest
  merger, KSP. Compare progress events against `Failure.getProblems()`. Record the
  console format for the fallback.
- **S2. MCP client behaviour** (Claude Code, Codex, Cursor at minimum):
  - default tool timeout;
  - whether a `progressToken` is sent;
  - whether progress resets the timeout;
  - whether a user interrupt sends cancellation;
  - `outputSchema`/`structuredContent` handling;
  - `ImageContent` support;
  - practical limits on tool count.
- **S3. AGP variant and output model from a non-Studio TAPI client.** Is it feasible,
  what does it cost, and how accurate is the task-name heuristic on a flavoured
  project?
- **S4. Cold path latency and memory.** From `workspace.open` to the first
  `build.tasks` on the sample app: sidecar JVM start, then daemon start, then
  configuration. Measure sidecar RSS; "~200–400 MB" (ADR-0017) is unmeasured. Measure
  the daemon's idle footprint.
- **S5. Daemon reuse** between our sidecar, `./gradlew` in a terminal, and Android
  Studio.
- **S6. `uiautomator dump` reliability.** Test `/dev/tty` on API 34–36 emulators with
  animated Compose UIs, and measure the rate of "could not get idle state".
- **S7. Go JSON-Schema library choice.** Check its handling of optional values, enums
  and unions, and whether target clients accept the output.
- **S8. Emulator detach, reaping and headless mode** on macOS Apple Silicon and Linux.
- **S9. Logcat tracking across app restarts.** Pid tracking vs `--uid` availability, by
  API level.

---

## What is genuinely solid

- **The ADR-0003 adapter boundary**, enforced by an import rule (`core-runtime.md`
  §Adapter boundary): adapters never import `domain/*`.
- **ADR-0015's split between annotations and enforcement**, with re-checks just before
  execution to avoid TOCTOU races.
- **ADR-0018's `GradleCapabilities` gating principle**, and "auxiliary mechanisms must
  never fail the user's build".
- **`mcp-tool-surface.md` §Errors vs. failures**: a failed build is a successful call.
- **`adb-device.md`'s rules**: serial as the only identity, degrade instead of refuse,
  `INSTALL_FAILED` detection on exit 0, the Compose `testTagsAsResourceId` caveat, and
  refusing to mistype non-ASCII text.
- **`emulator.md`'s boot sequence**: serial found by diff, readiness via
  `sys.boot_completed`, and the documented cancel asymmetry.
- **The MVP's framing**: a falsifiable hypothesis, a hard cut of the LSP, and explicit
  non-goals.
- **The research docs**: honest, sourced, with a clear line between evidence and
  recommendation. ADR-0016's alternatives analysis in particular.
- **`mcp-features.md`**: the rejection of sampling, and elicitation kept strictly
  optional.

## What I would add if I owned the project

1. **ADR-0019, process topology and transports** (B1). A daemon plus stdio and CLI
   shims, a socket path, single-instance locking, and auto-start.
2. **ADR-0020, the operation contract** (B3 and R1). Start/Wait/Cancel, the bounded-wait
   MCP pattern, and disconnect semantics.
3. **One generated capability catalogue.** A single table with name, scope, kind,
   statefulness, long-running flag, annotations with overrides, trust marking and
   phase. It replaces the four hand-maintained lists.
4. **A "build outputs → device" contract doc** (B8, B9).
5. **Re-sequence Phase 1 to start with a JVM-free vertical slice.** Run `device.list` →
   `device.screenshot` (with image inlining) → `device.uiTree` end to end over MCP and
   HTTP. That proves the contract, the adapters, artifacts and events before any Kotlin
   is written. Run S1–S3 in parallel, and commit to the sidecar method surface only
   after them.
6. **A local security baseline doc** (N14, N15) and a **config file spec** covering
   policy, idle timeouts, paths and JVM discovery.
7. **A capability-trace format.** The Phase 2 "same-primitive check" and the hypothesis
   metrics both depend on it.
8. **A test strategy**: fake `adb` and emulator fixtures for the pure parsers, recorded
   MCP transcripts as golden tests, and daemon hygiene in CI (`gradle.md` §Testing
   covers only Gradle).
