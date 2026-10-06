# ADR-0017: Gradle execution strategy — Tooling API sidecar vs. CLI

- Status: Accepted
- Date: 2026-09-27
- Amended: 2026-09-29 — staging re-sequenced, the error premise corrected
  ([ADR-0022](0022-build-error-extraction.md)), the pending design items ratified,
  sidecar JVM discovery decided.
- Amended: 2026-09-29 — the error-location promise corrected to what the producers
  emit (spike S1 run 1); S1's status stated; `build.cancel` marked as an internal
  method.
- Amended: 2026-10-06 — staging 0 adjusted and packaging decided (milestone 1g plan
  D25, D4, Delegated). S1 run 2 uses the first sidecar as its minimal TAPI client; S3
  moves to staging 3 with the probe it gates; until the probe lands, the generic model
  is TAPI's `GradleProject` and `BuildEnvironment`, without a `BuildAction`. The
  sidecar is Kotlin, JVM target 17, in `sidecar/`, built by `sidecar/build.sh` into two
  jars checked in and embedded in the Go binary with pinned SHA-256 digests (the
  ADR-0029 pattern), run from the cache directory; the Go build needs no JVM.

## Context

Our `build.*` domain needs, at minimum: execute builds and tasks, **list available
tasks**, and return **structured compile errors** with file, line and column.

The reference implementation for this problem ([`gradle-mcp`](../research/gradle-mcp.md))
gets all of its best features **as byproducts of the Gradle Tooling API (TAPI)**:
structured compiler problems, a failure tree with stable IDs, real test events,
cancellation tokens, rich progress, and daemon reuse.

**TAPI is a Java API.** A Go core cannot call it. This forces a choice.

Both options are consistent with [ADR-0001](0001-core-runtime-go.md) (JVM-only
boundaries are allowed as **sidecars**, never embedded) and with
[`process-lifecycle.md`](../architecture/process-lifecycle.md) (lazy start, idle
shutdown, supervision).

## Options

### A. JVM TAPI sidecar

A small Kotlin/Java process that speaks a narrow JSON-RPC protocol to the core over
stdio and performs all Gradle interaction through TAPI.

- **Gains**: structured task listing and the project model via `BuildAction` (one
  configuration pass), per-task outcomes, the failure tree, test events, token-based
  graceful cancellation, rich progress, correct daemon reuse, full control of Java
  home/environment, and the Problems API where the toolchain supports it.
- **Costs**: a second runtime artifact we write and maintain, in another language;
  ~200–400 MB while alive (unmeasured, spike S4); its own lifecycle (mitigated by lazy
  start + idle shutdown); a versioned internal protocol.

### B. `gradlew` CLI + init scripts

Run the project's wrapper with `--console=plain` and inject init scripts that emit
structured JSON markers.

- **Gains**: no extra artifact, no JVM of ours, simplest supervision (a child
  process).
- **Costs**: task listing requires a custom init-script task; per-task output
  attribution and error extraction must be rebuilt, and doing so pushes toward
  Gradle **internal APIs**, which are the first thing to break on upgrades. Console
  parsing is brittle across Gradle/AGP/Kotlin versions.

### C. Hybrid

TAPI for structure (tasks, problems, tests) and CLI for plain execution. Rejected as
premature: two paths to maintain, to cover the same ground.

## Decision

**A.** Every Gradle interaction goes through the sidecar from the first build. There
is no CLI execution path at any stage.

**Correction (2026-09-29).** The original rationale said "structured errors live
exactly where TAPI is strong". That is only partly true. Kotlin compiler errors reach
the Problems API only with KGP 2.4+ on Gradle 8.13+, and AAPT2, manifest-merger and
KSP/kapt errors are not known to reach it at all. So `build.errors` combines TAPI's
structured sources with a console extractor in the core
([ADR-0022](0022-build-error-extraction.md)). The extractor is needed under every
option, so it does not favour B. The decision stands on what only TAPI gives without
touching the user's build:

- the project model and task listing in one configuration pass;
- a model-only probe that is never injected into builds
  ([ADR-0023](0023-android-project-facts-and-build-outputs.md));
- per-task outcomes;
- the failure tree and failure-carried problems;
- test events;
- cancellation tokens;
- daemon reuse.

Under B each of these is an init script in every build, or console parsing.

**Staging** (supersedes "`build.tasks` and `build.errors` first, then execution",
which was incoherent: errors exist only after a build executes, so that stage would
have needed option C):

0. **Spikes S1 and S3, before sidecar code.** They establish the error sources on
   real Android (KGP × Gradle × AGP) and the probe's feasibility, and they record the
   console formats the extractor is tested against. S1 run 1 recorded Kotlin, KSP and
   AAPT2 formats on the command line for one version cell; the TAPI path and the
   matrix are not yet run.
1. **Sidecar skeleton + `project.model`** (generic TAPI model): `project.modules`,
   `build.tasks`.
2. **Execution + errors together**: `build.run`/`build.assemble` over TAPI, console
   forwarding, failure tree, console extractor, Problems consumption behind capability
   flags, `outputs[]`, `build.errors`, `build.lastResult`.
3. **Probe**: exact `project.variants`, AGP/KGP in the profile, the build side of
   `sdk.requirements`. Until then, variants come from the labelled task-name fallback.
4. **Later**: `build.test`.

**Language: Kotlin.** It is the native language of the Gradle/Android ecosystem; the
Tooling API is a Java API and Kotlin consumes it without friction. The sidecar
speaks JSON-RPC over stdio to the core — the same shape the Kotlin LSP uses — so the
project keeps a single internal protocol idiom. Code that runs **inside the daemon**
(the `BuildAction` and the probe) is plain Java, so no Kotlin stdlib is loaded there.

The honest cost: this is the largest deviation from the "lean" goal so far.

## Consequences

- The core stays Go and JVM-free; the JVM cost is isolated to one sidecar that idles
  out and serves every root ([ADR-0019](0019-process-topology-daemon-and-shims.md)).
- We own a new artifact and an internal protocol, both of which need versioning.
- `build.errors` returns `{severity, tool, source}` for every record, and `file`,
  `line` and `column` **whenever the producer emits them**, plus `problemId` and
  `solutions` where the Problems API supplies them. Not every producer emits all
  three: in S1 run 1, Kotlin gave file, line and column; KSP gave no column; AAPT2
  gave no column and no source file, only a merged-resource path
  ([ADR-0022](0022-build-error-extraction.md) §Known open item). Keeping every field
  a producer does emit is still strictly better than the reference implementation,
  which drops line and column.
- `build.tasks` becomes a first-class structured query instead of "run `tasks` and
  read the console".
- The MVP's idle-memory budget must be restated: the sidecar is a JVM that exists
  while it is used.

## Consequences of option B (rejected)

*Kept for the record:* no new artifact or language in the repo, but we would rebuild
task attribution and error extraction ourselves, accepting brittleness across Gradle
versions, and would likely reach into internal APIs — degrading structured errors to
console parsing plus best-effort init-script output.

## Alternatives considered

The options are described under §Options; this section records why each was not
chosen.

- **B. `gradlew` CLI + init scripts.** Rejected. What the decision rests on (the
  project model and task listing in one pass, per-task outcomes, the failure tree,
  test events, cancellation tokens, daemon reuse) would be an init script in every
  build, or console parsing, and rebuilding task attribution and error extraction
  pushes toward Gradle internal APIs, the first thing to break on upgrades. The
  console extractor does not favour B: it is needed under every option (§Decision).
  What B would have cost is kept under §Consequences of option B (rejected).
- **C. Hybrid.** Rejected as premature: two paths to maintain, to cover the same
  ground.

## Recorded sub-decisions

- **Version support**: see [ADR-0018](0018-gradle-version-support-policy.md) — a
  declared floor, capability gating on Gradle **and** plugin versions, and a published
  supported range.
- **Sidecar JVM**: an LTS runtime ≥ 17, **discovered, not bundled**. Order: configured
  path → `JAVA_HOME` → `java` on `PATH` → macOS `/usr/libexec/java_home -v 17+` →
  Android Studio's bundled JBR at known paths. Every candidate is validated. The first
  two steps mirror `./gradlew`, because the sidecar's JVM becomes the daemon's JVM by
  default, and matching the wrapper maximises daemon reuse with the terminal.
  `jlink` bundling is deferred. Details:
  [`gradle.md`](../subsystems/gradle.md) §Sidecar JVM discovery.
- **The Gradle daemon's JVM** belongs to the project (toolchains / daemon JVM
  criteria) and is **not** forced. `javaHome` is an explicit override only.
- **`lastResult` persists** across core restarts
  ([ADR-0021](0021-persistence-format-and-location.md)): the compact result of the last
  finished build, so the UI or a new agent session can ask how the last build went
  without rebuilding.

## Resolved design (2026-09-29)

Formerly "pending"; now ratified in [`gradle.md`](../subsystems/gradle.md):

- **Method surface**: `hello`, `connection.open/close`, `project.model`, `build.run`,
  `build.test`, `build.cancel`, `ping`, `shutdown`. `build.cancel` is the internal
  sidecar method behind `operation.cancel`, not a capability
  ([ADR-0020](0020-operation-contract.md)). S1/S3 may amend the surface.
- **Where build state lives**: the core owns the registry, IDs, persistence, error
  merge and output resolution. The sidecar is an executor.
- **High-volume output**: batched JSON-RPC notifications on the one pipe. The core
  writes the console to a durable artifact.
- **Persistence shape and location**: the build model in `gradle.md` §The build model,
  stored per [ADR-0021](0021-persistence-format-and-location.md). Retention is 50
  builds or 7 days (working default).
