# Subsystem: Gradle

How the core drives Gradle: the project model, task discovery, builds, structured errors and
build outputs. Implements [ADR-0017](../adr/0017-gradle-execution-strategy.md) (TAPI sidecar),
[ADR-0018](../adr/0018-gradle-version-support-policy.md) (version policy),
[ADR-0022](../adr/0022-build-error-extraction.md) (error extraction) and
[ADR-0023](../adr/0023-android-project-facts-and-build-outputs.md) (Android project facts and
build outputs). The client is `internal/domain/build/gradle`, the executor `sidecar/`; the shapes
they exchange are in their code and tests.

## Why a sidecar

The Gradle Tooling API (TAPI) is the only supported way to get **structured** build information
without touching the user's build; it is a Java API, so a Kotlin JVM sidecar speaks JSON-RPC over
stdio to the Go core (one process, all roots; ADR-0017), supervised like any sidecar
([process-lifecycle](../architecture/process-lifecycle.md)). The Problems API does **not** cover the
most common Android failures (Kotlin compiler errors on KGP < 2.4, AAPT2, manifest merger, KSP/kapt),
so those come from the core's console extractor (ADR-0022).

## Division of responsibility

**The core owns the state; the sidecar is an executor** (ADR-0017 §Resolved design). History survives
restarts only if the core owns it, and the sidecar must be restartable (crash, upgrade, idle shutdown)
without losing anything. The core owns the build registry, ids, retention and persistence, version
gating and the toolchain profile, the console extractor, the error merge and output resolution; the
sidecar owns the TAPI connections, TAPI data, the model probe and Gradle daemon reuse. The core
**allocates** build ids and passes them in; the build id **is** the operation id
([ADR-0020](../adr/0020-operation-contract.md)). The sidecar forwards console text raw; everything
interpretive runs in the core, where it is pure Go and fixture-tested. The sidecar method behind
`operation.cancel` is internal (`build.cancel`): there is no `build.cancel` capability, and a cancel
for a build not yet started is kept, so it starts cancelled.

## Protocol

JSON-RPC 2.0 over the sidecar's stdio (the Kotlin LSP's idiom); the methods and notifications are
in `sidecar/` and the client's `codec.go`.

- `build.run` returns once the build is accepted; the terminal notification carries the outcome,
  the failure tree and the failure-carried problems. There is no `build.status`/`build.result`:
  the core already holds everything it was sent.
- **Versioning.** Core and sidecar ship as a **matched pair**; the handshake carries
  `protocolVersion` (`gradle.ProtocolVersion`) and the core refuses a mismatch.
- **No orphans.** The sidecar exits on stdin EOF, so a SIGKILLed core cannot leave it running.
- **Framing and caps** (m1g D6). One message per line, and **stdout carries the protocol only**
  (`sidecar/src/Rpc.kt`: a stray `println` cannot corrupt the stream). **Every notification is
  bounded to 1 MiB by construction**, so no build output can produce an over-long message; the
  per-field caps are commented constants in `sidecar/src/Build.kt`. An **unknown** method arriving as
  a notification (no id) gets no response. **Replies are bounded only by the model's size**
  (`project.model` measured 342 KiB for DopamineControl's 12 projects). The **client accepts a line
  up to 16 MiB** (`process.DefaultMaxProtocolLine`); over it, it kills the sidecar and fails the call
  `BUILD_SIDECAR_CRASHED`, cause "protocol line over 16 MiB": an untrusted build cannot grow one
  without bound.

## Output: control vs. volume

- **Control** (progress, task outcomes, problems) goes over JSON-RPC as normal notifications;
  **console output** is coalesced batches on the same pipe, no separate channel (working defaults in
  `Build.kt`), with TAPI colour off so the text carries no ANSI codes.
- The core writes the **full console** of every build, failed or not, to a **durable artifact**
  ([ADR-0021](../adr/0021-persistence-format-and-location.md)) and feeds the same stream to the
  console extractor. Consumers receive a reference, never a path
  ([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)).
- *Milestone 1g (D26, D27, Delegated):* a build result carries no console tail, inline or in its MCP
  rendering; the agent reads the errors with `build.errors` and the console with `artifact.read
  {ref: consoleRef, offset: -16384}`. `consoleRef` appears in the build's result once it has finished; a
  console while it grows is `build.watchOutput`'s ([ADR-0044](../adr/0044-build-watchoutput.md)),
  which is not an MCP tool.

## The build model

The record the registry keeps per build is `internal/domain/build/result.go`. Build-level and
task-level vocabularies are separate (build outcome `success | failed | cancelled`; a task's
`success | upToDate | fromCache | noSource | skipped | failed`). Rules from the prior art:

- **Task outcomes with verbatim reasons.** "Assembled" and "up-to-date" are different answers;
  `taskSummary` gives the build-level view of the same fact.
- **Failure tree with stable ids**, so `build.errors` can return a short "recent error context"
  rather than megabytes of console.
- **Available tasks are not executed tasks**: `build.tasks` lists what exists; the build result
  lists what ran.

## Build outputs

The build to install handoff; `device.install` consumes it ([adb-device](adb-device.md)). The
shape and the sourcing (AGP's `output-metadata.json` only, located for every module whose
`package<Variant>` task ended `success`, `upToDate` or `fromCache` in this build, so the file
describes this build and not a stale one) are ADR-0023 §Decision 5.

- **Missing is reported, not guessed.** No metadata, unreadable metadata or no `applicationId`
  gives no output for that variant, and the result says why.
- **No paths in results.** The core keeps `(buildId, module, variant)` to APK paths internally
  (principle 14).

**Install reference** (`build: {buildId?, module, variant}`): `buildId` omitted means the newest
finished build in the workspace registry with a matching output. Exactly one APK with no filters
resolves to that APK; the core re-checks that the file still exists with the recorded size and
mtime before handing it to `adb` (`BUILD_OUTPUT_STALE` otherwise, next step: rebuild).
`device.launch` and `device.logs` take the `applicationId` from the same record.

**MVP scope.** In: one unfiltered APK per variant, which is what a debug build of an application
module produces. Out: ABI/density multi-APK outputs (listed with their `filters`, install refused
with `BUILD_OUTPUT_UNSUPPORTED`), App Bundles (`bundle<Variant>` needs bundletool and is not
listed), `androidTest` APKs, dynamic-feature and instant apps.

## Error model

The record is `extract.Record` (its comments hold the `file` cases: project-relative, `external`, an
AAPT2 `merged` path never presented as a source file) and the `build.errors` descriptor.
**`file`, `line` and `column` are required whenever the producer emits them, and absent otherwise**:
in S1 run 1 Kotlin emitted all three, KSP and AAPT2 emitted no column, and AAPT2 no source file. The
reference implementation discards the line; we keep all of it. Severity follows the target's
semantics (Gradle 9.6 changed how it is assigned); `problemId`, `solutions` and `documentationLink`
exist only for Problems API records.

### Three sources, merged

Per ADR-0022: failure-carried problems (`Failure.getProblems()`, 8.12+), Problems progress events
(the only source of warnings) and the core's console extractor, all whenever available. Neither
Problems path alone is complete: failures carry only what failed the build, and events are
throttled per problem id (`ProblemSummariesEvent`). Only the 8.12 `Problem` shape is decoded (ADR-0022 §Decision 5); its
locations arrive from 8.13 (`getOriginLocations`).

### Console extractor

Pure Go, in the core, over the console stream **and** the text of failure-tree nodes, because S1 run 1
found the Kotlin location only in the console and the AAPT2 location only inside the failure message.
The recognised forms and the ones not yet (kapt, the older Kotlin form, AGP SDK signatures) are
`extract/extract.go`'s package comment, pinned by golden tests over the real recordings in
`testdata/s1run2`; each form is independent, so a format change degrades one tool, not the extractor.

### Merged-resource paths (AAPT2)

AAPT2 link errors name AGP's **merged** resources through an opaque blame id, not the file the
developer wrote (ADR-0022 §Known open item). Settled in 1g (D12): the merged path is always
reported verbatim with `merged: true`; `excerpt` quotes its line from AGP's merged file;
`resolved {file, line, column}` names the developer's file when AGP's blame file
(`merged_res_blame_folder`) maps the line. A lookup that fails omits its field.

### Merge rules

The merge key, "richest record wins" and "nothing is lost silently" are ADR-0022 §Decision 3-4
(`extract/merge.go`), numbered here because code cites them: (1) key `(severity, file, line,
column?, normalised message head)`; (2) the same key from several sources keeps the richest record
(failure > event > console), filling a missing location from the others; (3) errors first in
emission order, then warnings, capped at 200 errors and 50 warnings with a `truncated` count
(`extract.ErrorCap`, `WarningCap`); (4) a failed build no source recognised gives `errors: []`,
`extraction.unrecognised: true`, the root failure messages and a bounded console tail
(`extract.TailBytes`), the only raw tail in a result, labelled.

## Project model

`project.model` is one TAPI `BuildAction`: one configuration pass returns everything the
`project.*`, `build.tasks` and build-side `sdk.requirements` capabilities need (ADR-0023 §Decision
1). A field the probe could not read is omitted, never `unknown`; `sdkDir` is published by
`sdk.requirements` only (1h D6, D7). The model is cached per `(root, fingerprint)` (the
fingerprint is `gradle/fingerprint.go`'s; a module's `buildDir` is core-internal and never leaves the core); a configuration pass is
real cost. Composite builds: the root build only in the MVP.

### The probe

The probe ([ADR-0023](../adr/0023-android-project-facts-and-build-outputs.md) §Decision 2-3) is
the only init script in the design, plus a plain-Java jar, **injected into `project.model` only,
never into a user build**. As built in 1h (D4-D7; ADR-0023's 2026-10-07 line records the rule):
one `BuildAction` fetches the generic model and each project's probe model, each project isolated
(`probe.status: partial`, with notes); a fetch the probe breaks (its frames or its init script
named in the failure) is retried once without it (`probe.status: failed`, the probe's own message
as the note), never a cancel or the project's own failure; `project.model { probe: false }`
returns the generic model.
`namespace`, `minSdk`, the module type and `kotlinBuiltIn` wait for a consumer. Spike S3
([findings](../research/spike-s3-android-facts.md)) observed every field on AGP 8.0.2, 8.13.2 and
9.4.1; a probed fetch costs nothing measurable once warm.

### Variants

The probe's variants are `source: probe`, `exact: true`, decided per module: a module the probe
did not read (status `partial` with no Android facts for it, or a failed variant read) falls back
to task names (`source: task-names`, `exact: false`; 1h D8, D9; ADR-0023 §Decision 4). A library's
variants come from the probe too: they are valid `build.assemble` targets and report no `outputs`.
Either way the build output's `variantName` confirms the variant after a build, so a heuristic miss
surfaces as `BUILD_OUTPUT_NOT_FOUND`, not as a wrong install.

### SDK requirements (build side)

`sdk.requirements` ([sdk](sdk.md)) gets **proactive** input from the probe (per Android module:
`compileSdk`, the effective `buildToolsVersion`, the SDK directory AGP will use). The **reactive**
input, AGP SDK signatures in a failed build as `category: sdk.missing` records, was not built in
1h (D12): spike S3 found that a missing platform does not fail configuration on AGP 8.13 or 9.4,
so the probe's compile target names it before any build. Without the probe it answers
`source: failure-signatures`, `complete: false`, with nothing checked and a note saying so; while a
build holds the root and the build files changed, it answers from the cached model with
`stale: true` and `complete: false`.

## Task listing

`build.tasks` is a filter over the cached project model. AGP 8.13 gives the per-variant tasks
(`assembleDebug`) no group, so hidden tasks (noise by default) are opt-in with `includeHidden`
(observed on DopamineControl, 1g). Variant-aware filtering goes through `project.variants`, not
through guessed prefixes.

## Concurrency

**All TAPI operations for one root are serialised**, not only builds: a model fetch during a
running build makes Gradle start a second daemon, because busy daemons are not reused. While a
build runs, `project.model` answers from the cache (flagged `stale` if the fingerprint changed)
instead of queueing behind it. Contention between builds is reported as `queued` with `blockedBy`
(ADR-0020).

## Lifecycle

Every Gradle-touching call requires a **trusted** project root; an untrusted one fails with
`WORKSPACE_UNTRUSTED` before anything is spawned
([ADR-0015](../adr/0015-untrusted-data-authorization.md) §5).

- **Lazy start** on the first `build.*`, `project.*` or `sdk.requirements` call that needs it.
  **Idle shutdown** after 10 minutes (`gradle.IdleTimeout`) with no open operation on any root,
  never while a build is running. **Health**: the handshake covers startup, `ping` liveness.
- **Crash**: a sidecar crash mid-build fails that operation with a clear error and leaves the
  workspace `degraded`; the registry keeps the history; the next call restarts it. (1g D20: the mark
  stays inside the Gradle client, a register row.)
- **Never kill the Gradle daemon.** It belongs to Gradle and is reused across builds by design; an
  explicit stop is available but never implicit. **No orphans**: stdin EOF ends the sidecar; the
  core also stops it on shutdown.

## Environment and JVM

The **environment source** is explicit: inherit, shell-probed, or none; on macOS a GUI-launched core
does not see the shell's `PATH`/`JAVA_HOME` (ADR-0019: the daemon's environment is fixed at start).

### Sidecar JVM discovery

The first candidate that is a runnable JVM **>= 17** wins; each is validated (its `release` file
or `java -version`), never assumed. In order:

1. The configured path (`gradle.sidecarJavaHome`, [ADR-0032](../adr/0032-configuration-file-v1.md)
   §3), read once at daemon start; a home that is not a JVM >= 17 is rejected naming the key, and
   the next candidate is tried. An invalid configuration file at start is a rejected entry naming
   the file.
2. `JAVA_HOME` from the effective environment.
3. `java` on the effective `PATH`, resolved through symlinks; the macOS `/usr/bin/java` stub
   counts only if it resolves to a real JDK.
4. macOS: `/usr/libexec/java_home -v 17+`.
5. Android Studio's bundled JBR at known locations (`discover.go`).

Nothing found: `build.*` and `project.*` report `degraded`, listing each candidate and why it was
rejected; the next step is `JAVA_HOME` or the config key. The chosen JVM and its source are in
`system.health`. Bundling a runtime (`jlink`) is deferred. Steps 2-3 come first because they are
what `./gradlew` does and the sidecar's JVM becomes the daemon's by default, which maximises
daemon reuse with the user's terminal.

### Daemon JVM

- Precedence is Gradle's: the project's daemon JVM criteria (`gradle/gradle-daemon-jvm.properties`)
  or `org.gradle.java.home` win; otherwise TAPI uses the sidecar's JVM. We never select a daemon
  JVM; `javaHome` is an explicit override only; we **report** the daemon JVM in every result.
- **Consequence.** A daemon is reused only by clients with a compatible Java home and JVM args:
  Android Studio defaults to its JBR, the terminal to `JAVA_HOME`, and when they differ each runs its
  own daemon of hundreds of megabytes. A project with daemon JVM criteria removes the problem.
- A JVM too new for the project's Gradle (e.g. JDK 21 with Gradle 8.0-8.4) fails with Gradle's own
  message plus the `javaHome` override as the next step. No silent switching.

## Version detection and toolchain profile

Implements [ADR-0018](../adr/0018-gradle-version-support-policy.md).

1. **Gradle, detected twice**: the wrapper's `distributionUrl` before connecting (cheap, for the UI),
   then the authoritative version from `BuildEnvironment`.
2. **`GradleCapabilities`** per connection, each flag from the documented `@since`
   (`sidecar/src/Session.kt`); subscription-time decisions are gated on them, and call-time optional
   methods stay wrapped in `UnsupportedMethodException` catches, so an unknown shape degrades a
   field, never the build.
3. **Plugin facts** (`agp`, `kgp`, later `kotlinBuiltIn`) come from the probe; `unknown` if it did
   not read them; never parsed from build scripts or catalogs.
4. **Expected error source per tool**: Kotlin (JVM) is structured when `problemLocations` and
   KGP >= 2.4; javac when `problemLocations` (javac reports since Gradle 8.9); AAPT2, the manifest
   merger, KSP and kapt are not assumed. These choose which source is primary and what the result
   reports as `expectedSources` (1h D10); they never switch the extractor off.
5. **Report it** in every build result: Gradle version and source, AGP, KGP, `profile` and
   `validation` (ADR-0018).

## Failure modes

Codes and causes are in `internal/capability/error.go` and the descriptors; the decisions:

- Gradle below the floor (8.0, `gradle.GradleFloor`) is refused naming floor, version and reason;
  with `gradle.allowBelowFloor` (one global key read at daemon start,
  [ADR-0032](../adr/0032-configuration-file-v1.md), ADR-0018 rule 3 as amended) Gradle 7.3-7.x runs
  under the conservative profile, `validation: unsupported`; below 7.3 is refused even with it;
  without the key the next step names the key and the file (1k; observed with Gradle 7.6.4 on
  zulu-17). A version newer than validated proceeds with the newest capability set, `untested`.
- No usable sidecar JVM, a sidecar that fails to start or speaks another protocol version: the
  `[gradle]` capabilities are unavailable in `system.capabilities`; other capabilities are unaffected.
  A crash mid-call or a broken protocol leaves no record for the build it was running; the registry
  keeps history; restart on next use.
- The project's own configuration failure is `PROJECT_MODEL_FAILED` with Gradle's failure chain as
  the cause. Daemon OOM: surface the daemon's own error and the memory flags it suggests; do not
  retry blindly. Task not found: a failed build with Gradle's own message; `build.tasks` is the
  discovery path (no near-matches yet, 1g D19). No wrapper in the project: report it, since TAPI
  would otherwise silently use its own Gradle version (`gradle.source: connector`).

## Persistence

Per ADR-0021: the compact build result per build in `ws/<wsid>/builds/`, the console as a durable
artifact. **`lastResult` survives restarts** (the console is referenced, not inlined), and the
output-path index behind `outputs[]` is persisted with the record, so an install reference stays
resolvable after a restart (subject to the `BUILD_OUTPUT_STALE` check). Retention: 50 builds or 7
days, oldest first (working default, `registry.go`).

## Testing

The version matrix (a KGP axis and an AGP axis) and the probe canary are ADR-0018's; the canary ran
in 1h with `DROIDYARD_GRADLE_TESTS=1 go test ./sidecar` (AGP 8.0.2 on Gradle 8.0.2, 8.13.2 on 8.13,
9.4.1 on 9.6.0; a broken field is omitted, never fails the fetch) and is not yet in CI. Fixtures pin
versions by rewriting the wrapper's `distributionUrl` and the plugin versions. Extractor golden tests
run over recorded real consoles, pure Go, no JVM; a form without a recording needs one before its
test is final. Daemon hygiene: pinned `GRADLE_USER_HOME` and `javaHome`, short daemon idle timeout,
bounded workers; otherwise tests leak daemons.

## Spikes

S1 (error sources) and S3 (Android facts) have run:
[spike-s1-error-formats.md](../research/spike-s1-error-formats.md) (run 1 on the command line; run 2
over TAPI with a warm daemon, which recorded Kotlin, javac, KSP, AAPT2 and the manifest merger; kapt
and the rest of the matrix not run) and [spike-s3-android-facts.md](../research/spike-s3-android-facts.md).
Whatever S1 finds, the extractor ships: the question is which source is primary where, not whether we
parse. S4 (sidecar memory) and S5 (daemon reuse with the terminal and Studio) do not block; they are
register rows.
