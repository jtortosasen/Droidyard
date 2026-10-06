# Subsystem: Gradle

How the core drives Gradle: the project model, task discovery, builds, structured
errors and build outputs. Implements
[ADR-0017](../adr/0017-gradle-execution-strategy.md) (TAPI sidecar),
[ADR-0018](../adr/0018-gradle-version-support-policy.md) (version policy),
[ADR-0022](../adr/0022-build-error-extraction.md) (error extraction) and
[ADR-0023](../adr/0023-android-project-facts-and-build-outputs.md) (Android project
facts and build outputs).

> Status: draft. The division of responsibility, the protocol, the output transport
> and the sidecar JVM discovery are **ratified** (2026-09-29). Spike S1 has had one
> partial run (command line only, one version cell); its TAPI run and S3
> ([below](#spikes-before-the-sidecar-is-written)) come before the sidecar is written.
> Their results may amend the method surface and the extractor fixtures, not the
> ownership model.

## Why a sidecar

The Gradle Tooling API (TAPI) is the only supported way to get **structured** build
information without touching the user's build: real per-task outcomes, a failure
tree, test events, token-based cancellation, the project model in one configuration
pass, daemon reuse and, where the project's toolchain supports it, the Problems API.
TAPI is a Java API, so it cannot be called from the Go core.

```text
core (Go)
  │  JSON-RPC over stdio
  ▼
gradle-sidecar (Kotlin, JVM)       one process, all roots (ADR-0019)
  │  Tooling API
  ▼
Gradle daemon  ──▶  project build
```

The sidecar is a **sidecar in the architecture's sense**
([process-lifecycle](../architecture/process-lifecycle.md)): started on demand,
supervised, stopped when idle, never able to take the core down. One sidecar serves
every open root ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)); every
method carries `root`.

The Problems API does **not** cover the most common Android failures (Kotlin compiler
errors on KGP < 2.4, AAPT2, manifest merger, KSP/kapt), according to the plugins'
documentation; S1 has not yet observed the TAPI side. Those come from the core's
console extractor ([ADR-0022](../adr/0022-build-error-extraction.md)). The sidecar's
case rests on the rest of the list above, not on errors alone.

## Division of responsibility

**The core owns the state; the sidecar is an executor.** *(Ratified.)*

| Owns | Core | Sidecar |
| --- | --- | --- |
| Build registry, IDs, retention, persistence | ✅ | — |
| Capability mapping, version gating policy, toolchain profile | ✅ | — |
| Console extractor, error merge, build outputs resolution | ✅ | — |
| TAPI connections and their lifecycle | — | ✅ |
| TAPI data: events, failure tree, failure-carried problems, models | — | ✅ |
| The model probe (see [Project model](#project-model)) | — | ✅ |
| Gradle daemon reuse | — | ✅ (Gradle's) |

Rationale: history survives restarts only if the core owns it, and the sidecar must
be restartable (crash, upgrade, idle shutdown) without losing anything. The core
**allocates** build IDs and passes them in; the build ID **is** the operation ID
([ADR-0020](../adr/0020-operation-contract.md)). The sidecar forwards console text
raw; everything interpretive runs in the core, where it is pure Go and fixture-tested.

## Capability mapping

| Capability | Sidecar method | Notes |
| --- | --- | --- |
| `project.modules` | `project.model` | From the cached model |
| `project.variants` | `project.model` | Probe (exact); task-name heuristic as labelled fallback |
| `build.tasks` | `project.model` | Filtered in the core; not "run `tasks`" |
| `build.assemble` | `build.run` | `:<module>:assemble<Variant>`; result carries `outputs[]` |
| `build.run` | `build.run` | Arbitrary tasks |
| `build.test` | `build.test` | Post-MVP; filters gated by capability |
| `build.errors` | — | Core, from the build record (merged sources) |
| `build.lastResult` | — | Core, from the registry |
| `sdk.requirements` (build side) | `project.model` | Probe facts + failure signatures ([below](#sdk-requirements-build-side)) |
| cancellation | `build.cancel` | Reached through `operation.cancel`; there is no `build.cancel` capability |

Long-running behaviour (how long a caller blocks, polling, cancel) is the operation
contract's ([ADR-0020](../adr/0020-operation-contract.md)), not this subsystem's.

## Protocol

**Transport**: JSON-RPC 2.0 over the sidecar's stdio, the same idiom as the Kotlin
LSP. *(Ratified.)*

```text
hello            → { protocolVersion, sidecarVersion, tapiVersion, java: {home, version} }
connection.open  { root, javaHome?, env } → { gradleVersion, source, daemonJavaHome,
                                              capabilities, profile, validation }
connection.close { root }
project.model    { root, probe: bool }    → ProjectModel (below)
build.run        { buildId, root, tasks[], args[] } → { accepted }
build.test       { buildId, root, task, filters? } → { accepted }
build.cancel     { buildId }                // internal; the only route to it is operation.cancel;
                                            // a cancel for a build not yet started is kept, so it starts cancelled
ping
shutdown
```

Notifications:

```text
event.build.started        { buildId }
event.build.progress       { buildId, phase?, counts, activeTask }
event.build.output         { buildId, stream, lines[] }        // batched, see below
event.build.taskFinished   { buildId, path, outcome, durationMs, reason? }
event.build.problem        { buildId, problem }                // live; see Error model
event.build.problemSummary { buildId, problemId, suppressed }  // Gradle throttled events
event.test.finished        { buildId, suite, test, outcome, durationMs }
event.build.finished       { buildId, outcome, failures[], failureProblems[],
                             missingCapabilities[] }
```

- `build.run` returns once the build is accepted; the terminal notification carries
  the outcome, the failure tree and the failure-carried problems. There is no
  `build.status`/`build.result`: the core already holds everything it was sent.
- **Versioning.** Core and sidecar ship as a **matched pair**; the handshake carries
  `protocolVersion` and the core refuses a mismatch with a clear error.
- **No orphans.** The sidecar exits on stdin EOF, so a SIGKILLed core cannot leave it
  running (plus `Pdeathsig` on Linux).
- **Framing and caps** (m1g D6). One message per line, `\n`-terminated. **stdout carries
  the protocol only**: at start the sidecar points `System.out` at stderr and writes the
  protocol to the captured original stdout, so a stray `println` — ours, the Tooling
  API's or a plugin's — cannot corrupt the stream; its stderr goes to the supervisor's
  log sink. **Every notification is bounded to 1 MiB by construction**, so no build
  output can produce an over-long message: each failure's message and description is cut
  at 8 KiB, a failure tree at 200 nodes, and a single console line at 64 KiB, followed by
  a marker naming how many bytes were dropped (the cut lands on a UTF-8 boundary, so a
  multi-byte character is never split); console batches are counted in the encoded line's
  bytes. The terminal `event.build.finished` draws its `failures` and `failureProblems`
  from one shared 512 KiB budget — when it or the 200-node cap is reached a parent stops
  recursing and carries a `truncatedCauses` count instead of more nodes — and
  `failureProblems` is capped at 500 with a dropped count. An **unknown** method that
  arrives as a notification (no id) gets no response. **Replies are bounded only by the
  model's size** (`project.model` measured 342 KiB for DopamineControl's 12 projects, about 28 KiB a project, in S1 run 2; the 16 MiB line cap allows several hundred modules).
  The **client accepts a line up to 16 MiB**; over it, it kills the sidecar and fails the
  call `BUILD_SIDECAR_CRASHED`, cause "protocol line over 16 MiB" — an untrusted build
  cannot grow one without bound.

## Output: control vs. volume

*(Ratified.)*

- **Control** (progress, task outcomes, problems) goes over JSON-RPC as normal
  notifications.
- **Console output** is sent as **coalesced batches**, one pipe, no separate channel.
  Working defaults: 200 lines or 64 KiB per message, flushed every 100 ms. TAPI colour
  output is off, so the text carries no ANSI codes.
- The core writes the **full console** of every build, failed or not, to a **durable
  artifact** under the workspace state directory
  ([ADR-0021](../adr/0021-persistence-format-and-location.md)) and feeds the same
  stream to the console extractor. Consumers receive a reference, never a path
  ([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)).
- Consumers read it through **`artifact.read`** (`{ref, offset, limit}`), which reports
  `growing: true` while the build is running; the UI uses `GET /v1/artifacts/{id}` with
  `Range` support.
- *Milestone 1g (plan D26, D27, Delegated):* a build result carries no console tail,
  inline or in its MCP rendering; the agent reads the errors with `build.errors` and the
  console with `artifact.read {ref: consoleRef, offset: -16384}`. `consoleRef` appears
  only once the build has finished: reading a console while it grows lands with the
  `build.output` topic and its first consumer, the Phase 2 UI. A project without a
  wrapper is named by `gradle.source: connector` and a first console line (D19).

## The build model

Build-level and task-level vocabularies are separate.

```text
Build
├── id              allocated by the core (= operation id)
├── root            project root
├── request         { tasks[], args[], module?, variant?, testFilters? }
├── state           running | finished
├── outcome         success | failed | cancelled          (build level, set when finished)
├── startedAt / finishedAt
├── taskSummary     { executed, upToDate, fromCache, noSource, skipped, failed }
├── tasks[]         { path, outcome, durationMs, reason?, provenance? }
│                   outcome: success | upToDate | fromCache | noSource | skipped | failed
├── problems[]      merged errors and warnings (see Error model)
├── failures[]      failure tree with stable ids
├── tests[]         { suite, test, outcome: success | failed | skipped, durationMs }
├── outputs[]       installable outputs (see Build outputs)
├── profile         toolchain profile snapshot (see Version detection)
├── extraction      { sources[], unrecognised }
└── consoleRef      artifact reference
```

Rules copied from the prior art because they are right:

- **Task outcomes with verbatim reasons.** "Assembled" and "up-to-date" are different
  answers; `taskSummary` gives the build-level view of the same fact.
- **Failure tree with stable IDs**, so `build.errors` can return a short "recent error
  context" rather than megabytes of console.
- **Available tasks ≠ executed tasks**: `build.tasks` lists what exists; the build
  result lists what ran.

## Build outputs

The build → install handoff. `device.install` consumes this contract
([adb-device](adb-device.md)).

```text
outputs[]
└── { module: ":app", variant: "debug", applicationId: "com.example.app",
      versionCode: 1, versionName: "1.0",
      apks: [ { name: "app-debug.apk", filters: [], sizeBytes: 8123456 } ] }
```

- **One source: AGP's `output-metadata.json`** (the APK listing file: `applicationId`,
  `variantName`, `elements[]` with `outputFile`, `versionCode`, `versionName`,
  `filters`). No build script, version catalog or manifest is ever parsed for an
  application id.
- **Located, not assumed.** For every module whose `package<Variant>` task ended
  `success`, `upToDate` or `fromCache` **in this build**, the core searches
  `<buildDir>/outputs/apk/` (buildDir from the project model, depth-bounded) for the
  `output-metadata.json` whose `variantName` matches. The task condition guarantees
  the file describes this build's output, not a stale one.
- **Missing is reported, not guessed.** No metadata, unreadable metadata or no
  `applicationId` → no output for that variant, and the result says why.
- **No paths in results.** The core keeps `(buildId, module, variant) → APK paths`
  internally (principle 14).

**Install reference** (the contract `device.install` accepts):

```text
build: { buildId?, module, variant }
```

- `buildId` omitted → the newest finished build in the workspace registry with a
  matching output.
- Resolution: exactly one APK with no filters → that APK. The core re-checks that the
  file still exists with the recorded size and mtime before handing it to `adb`.
- Errors: `BUILD_OUTPUT_NOT_FOUND` (no build produced it), `BUILD_OUTPUT_STALE` (file gone or
  changed; next step: rebuild), `BUILD_OUTPUT_UNSUPPORTED` (shape outside MVP scope).
- `device.launch` and `device.logs` take the `applicationId` from the same record.

**MVP scope.** In: one unfiltered APK per variant, which is what a debug build of an
application module produces. Out: ABI/density multi-APK outputs (listed with their
`filters`, install refused with `BUILD_OUTPUT_UNSUPPORTED`), App Bundles (`bundle<Variant>`
needs bundletool and is not listed), `androidTest` APKs, dynamic-feature and instant
apps.

## Error model

Illustrative record from a Problems source (values invented):

```json
{
  "severity": "error",
  "source": "failure",
  "tool": "kotlin",
  "problemId": "kotlin.compiler.unresolved-reference",
  "file": "app/src/main/java/Foo.kt",
  "line": 83,
  "column": 12,
  "message": "Unresolved reference 'Bar'.",
  "solutions": ["import com.example.Bar"],
  "documentationLink": "https://..."
}
```

- `source`: `failure` | `event` | `console` — which path produced the record.
- `tool`: `kotlin` | `javac` | `kapt` | `ksp` | `aapt2` | `manifest-merger` | `agp` |
  `gradle`.
- `file` has three cases:
  - inside the project: relative to the project root (the normal case);
  - outside it: reported as given and flagged `external: true`;
  - an AAPT2 **merged-resource path** (`<blame-id>:/values/values.xml`): reported
    verbatim, flagged `merged: true`, and never presented as a source file. The record
    may carry `excerpt`, the quoted line from the merged file (see
    [Merged-resource paths](#merged-resource-paths-aapt2)).

  `generated: true` marks locations in generated sources (kapt stubs) that are not the
  user's code.
- **`file`, `line` and `column` are required whenever the producer emits them, and
  absent otherwise.** In S1 run 1, Kotlin emitted all three; KSP and AAPT2 emitted no
  column, and AAPT2 no source file. javac's column is expected from its caret line
  (not yet recorded). The reference implementation receives locations from the
  Problems API and discards the line; we keep all of it. Severity follows the
  target's semantics (Gradle 9.6 changed how it is assigned).
- `problemId`, `solutions` and `documentationLink` exist only for Problems API records.

### Three sources, merged

Per [ADR-0022](../adr/0022-build-error-extraction.md). All three run whenever available.

| Source | Gradle | Role |
| --- | --- | --- |
| Failure-carried problems (`Failure.getProblems()`) | 8.12+ | Authoritative for errors that failed the build; not subject to event throttling; the path Gradle 9.8 steers integrators toward |
| Problems progress events | 8.12+ (decoded shape) | Live stream for the UI, and warnings, which never reach the failure |
| Console extractor (core) | any | Everything the toolchain does not report structurally |

Why both Problems paths: failures carry only what failed the build, and events are
throttled per problem id (`ProblemSummariesEvent`). Neither alone is complete.

Only the `Problem` shape introduced in 8.12 is decoded; locations on it arrive from
8.13 (`getOriginLocations`). The 8.4–8.11 event shapes are not decoded: that range is
covered by the extractor, which is cheaper than a second decoder for an API the
current TAPI client no longer ships.

### Console extractor

Pure Go, in the core. Input: the console stream the sidecar forwards **and** the text
of failure-tree nodes. Both are read because S1 run 1 found the Kotlin location only
in the console and the AAPT2 location only inside the failure message. That run was
on the command line with `--console=plain`; that the TAPI-forwarded console (colour
off) carries the same lines is an assumption until S1 runs over TAPI.

Recognised forms. **Observed** = recorded verbatim in S1 run 1 (Gradle 8.13,
AGP 8.13.2, KGP 2.0.21, command line). **Expected** = from documentation or
experience, not yet recorded; each needs a real recording before its golden test is
final.

| Tool | Form | Yields | Status |
| --- | --- | --- | --- |
| Kotlin | `e: file:///abs/Foo.kt:3:42 message` (`file://` URI) | file, line, column | Observed |
| Kotlin | untagged `w: file:///…` warnings; the older `e: /abs/Foo.kt: (83, 12): message` | file, line, column | Expected |
| KSP | `w: [ksp] /abs/Foo.kt:9: message` (plain path) | file, line; no column | Observed |
| KSP | `e: [ksp] …` errors, same shape | file, line | Expected |
| AAPT2 (link) | inside the failure message, under `Android resource linking failed`: `<blame-id>:/values/values.xml:210: error: message`, then file-less `error: failed linking references.` | merged path (`merged: true`), line; no column | Observed |
| AAPT2 (compile) | malformed-resource errors, possibly naming the source path (`…/res/layout/main.xml:12: … AAPT: error: …`) | file, line when present | Expected; shape unknown |
| javac | `/abs/Foo.java:83: error: message` + source line + caret line + `symbol:`/`location:` | column from the caret; continuation folded into the message | Expected |
| kapt | javac form, often prefixed `e: `, under `build/tmp/kapt3/stubs/` | as javac, `generated: true` | Expected |
| Manifest merger | `/abs/AndroidManifest.xml:12:5-40 Error:` + indented lines; `Manifest merger failed : …` | file, line, column (range start) | Expected |
| AGP SDK | licence-not-accepted and missing-package messages naming SDK package ids | `category: sdk.missing`, `package` | Expected |

Each form is independent: a format change degrades one tool, not the extractor.

### Merged-resource paths (AAPT2)

AAPT2 link errors name AGP's **merged** resources through an opaque blame id
(`com.example.app-mergeDebugResources-42:/values/values.xml:210`), not the file the
developer wrote ([ADR-0022](../adr/0022-build-error-extraction.md) §Known open item).
The message still names the missing resource, which is actionable on its own.
Options:

1. **Resolve the blame id** to the source file through AGP's merged-resource
   metadata. Undocumented intermediate state that changes between AGP versions.
2. **Read the merged file and quote the line** (`excerpt`). Best-effort: locating the
   file from the blame id also depends on AGP's intermediate layout, and whether it
   survives the failed task on disk is unverified.
3. **Report the virtual path as-is**, labelled `merged: true`.

Working assumption: 3 always, plus 2 where the merged file can be located. The
choice **must be settled before `build.errors` is implemented**.

### Merge rules

1. Key: `(severity, file, line, column?, normalised message head)`.
2. Same key from several sources → keep the richest record (failure > event >
   console), filling a missing location from the others.
3. Errors first in emission order, then warnings (capped, default 50; errors capped at
   200 with a `truncated` count).
4. **Nothing is lost silently.** If the build failed and no source recognised an
   error, the result is `errors: []`, `extraction.unrecognised: true`, the root
   failure messages and a bounded console tail. This is the only place a raw tail
   appears, and it is labelled.

## Project model

`project.model` is one TAPI `BuildAction`: one configuration pass returns everything
the `project.*`, `build.tasks` and build-side `sdk.requirements` capabilities need.
*(Ratified.)*

```text
ProjectModel
├── root, fingerprint
├── gradle      { version, source, daemonJavaHome }
├── plugins     { agp?, kgp?, kotlinBuiltIn? }            probe; "unknown" otherwise
├── modules[]
│   ├── path, name, dir (root-relative), buildDir (core-internal)
│   ├── tasks[]  { name, path, group, description, public }
│   └── android? { type: application | library | dynamic-feature | test,
│                  namespace, compileSdk, buildToolsVersion, minSdk,
│                  sdkDir (core-internal),
│                  variants[] { name, buildType, flavors[] } }
└── probe       { status: ok | partial | failed | skipped, notes[] }
```

- **Cost is real**: a configuration pass. The model is cached per
  `(root, fingerprint)`; the fingerprint covers settings and build scripts,
  `gradle.properties`, `local.properties`, version catalogs, the wrapper properties and
  `buildSrc`/included build logic.
- Composite builds: the root build only in the MVP.

### The probe

Generic TAPI models give modules, tasks and directories, but not Android variants,
`compileSdk`, build-tools or plugin versions. Those come from **the probe**
([ADR-0023](../adr/0023-android-project-facts-and-build-outputs.md)):

- **The only init script in the design**: `droidyard-probe.init.gradle`, a few lines that
  put a small jar on the init-script classpath and apply a plugin registering a
  `ToolingModelBuilder`. The jar is plain Java (Java 8 bytecode, no Kotlin stdlib in the
  daemon), compiled against Gradle's public API at the floor.
- **Injected into `project.model` only, never into a user build.** It cannot fail
  `build.*` by construction.
- **What it reads**, each field isolated (a failure makes that field `unknown`, never
  the model fetch):
  - AGP and KGP versions, reflectively from the plugins' own version constants on the
    project's build classpath (this covers AGP 9's built-in Kotlin);
  - `namespace`, `compileSdk`, `buildToolsVersion`, `minSdk` and the SDK directory AGP
    resolved (honouring `local.properties` `sdk.dir`), from AGP's public DSL;
  - variants from `androidComponents.onVariants`, the public Variant API.
- `project.model { probe: false }`, or any probe failure, still returns the generic
  model.

### Variants

`project.variants {module?}` returns `{ modules: [{ module, variants[], source, exact }], stale }`;
variant names are lowercase-first (`assembleDebug` → `debug`).

- **Probe available** → `source: probe`, `exact: true`, with build type and flavours.
- **Otherwise** → `source: task-names`, `exact: false`: in each module, the variants
  are the `X` for which both `assemble<X>` and `package<X>` exist, minus names ending
  in `AndroidTest`, `UnitTest` or `TestFixtures`. `package<X>` exists only per variant
  and only for APK-producing modules, which excludes the flavour and build-type
  aggregates (`assembleDebug` in a flavoured project) and library modules. Names only;
  no build-type/flavour split.
- Either way, the build output's `variantName` confirms the variant after a build, so
  a heuristic miss surfaces as `BUILD_OUTPUT_NOT_FOUND`, not as a wrong install.

### SDK requirements (build side)

`sdk.requirements` ([sdk](sdk.md)) gets two inputs from this subsystem:

- **Proactive**, from the probe: per Android module, `compileSdk` (→
  `platforms;android-N`), the effective `buildToolsVersion` (→ `build-tools;X`) and the
  SDK directory AGP will use.
- **Reactive**, from the extractor: AGP SDK signatures in a failed build, as
  `category: sdk.missing` records naming the package.

Without the probe, `sdk.requirements` answers from failure signatures only and says so
(`source: failure-signatures`, `complete: false`).

## Task listing

`build.tasks` is a filter over the cached project model:

```json
{ "tasks": [
  { "module": ":app", "path": ":app:assemble", "name": "assemble",
    "group": "build", "description": "Assembles main output..." } ],
  "stale": false }
```

AGP 8.13 gives the per-variant tasks (`assembleDebug`) no group, so they are hidden by
default and listed with `includeHidden` (observed on DopamineControl, milestone 1g).

- Filterable by module, group and name substring. Variant-aware filtering goes through
  `project.variants`, not through guessed prefixes.
- Hidden tasks are opt-in (they are noise by default).

## Concurrency

**All TAPI operations for one root are serialised**, not only builds: a model fetch
during a running build makes Gradle start a second daemon, because busy daemons are
not reused. While a build runs, `project.model` answers from the cache (flagged
`stale` if the fingerprint changed) instead of queueing behind it. Contention between
builds is reported as `queued` with `blockedBy` ([ADR-0020](../adr/0020-operation-contract.md)).

## Lifecycle

Every Gradle-touching call requires a **trusted** project root; an untrusted one fails
with `WORKSPACE_UNTRUSTED` before anything is spawned
([ADR-0015](../adr/0015-untrusted-data-authorization.md) §5).

- **Lazy start**: on the first `build.*`, `project.*` or `sdk.requirements` call that
  needs it, not at core start.
- **Idle shutdown**: after a configurable idle period (working default: 10 minutes)
  with no open operation on any root; never while a build is running.
- **Health**: the handshake covers startup; `ping` covers liveness.
- **Crash**: a sidecar crash mid-build fails that operation with a clear error and
  leaves the workspace `degraded`; the registry keeps the history. The next call
  restarts it.
- **Never kill the Gradle daemon.** It belongs to Gradle and is reused across builds
  by design; an explicit stop is available but never implicit.
- **No orphans**: stdin EOF ends the sidecar; the core also stops it on shutdown.

## Environment and JVM

- **Environment source** is explicit: inherit, shell-probed, or none. On macOS a
  GUI-launched core does not see the shell's `PATH`/`JAVA_HOME`
  ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md): the daemon's
  environment is fixed at start).

### Sidecar JVM discovery

*(Ratified.)* The first candidate that is a runnable JVM **≥ 17** wins. Each candidate
is validated (its `release` file or `java -version`), never assumed.

1. The configured path (`gradle.sidecarJavaHome`).
2. `JAVA_HOME` from the effective environment.
3. `java` on the effective `PATH`, resolved through symlinks. The macOS
   `/usr/bin/java` stub counts only if it resolves to a real JDK.
4. macOS: `/usr/libexec/java_home -v 17+`.
5. Android Studio's bundled JBR at known locations: macOS
   `/Applications/Android Studio*.app/Contents/jbr/Contents/Home` and the same under
   `~/Applications`; Linux `/opt/android-studio/jbr`, `~/android-studio/jbr` and the
   JetBrains Toolbox install directories.

Nothing found → `build.*` and `project.*` report `degraded`, listing each candidate
and why it was rejected; the next step is `JAVA_HOME` or the config key. The chosen
JVM and its source are reported in `system.health`. Bundling a runtime (`jlink`) is
deferred.

Why 2–3 before 4–5: they are exactly what `./gradlew` does, and the sidecar's JVM
becomes the daemon's JVM by default (below). Mirroring the wrapper maximises daemon
reuse with the user's terminal. Steps 4–5 find *a* JDK when the environment has none.

### Daemon JVM

- Precedence is Gradle's: the project's daemon JVM criteria
  (`gradle/gradle-daemon-jvm.properties`) or `org.gradle.java.home` win; otherwise TAPI
  uses the client's JVM, i.e. the sidecar's. We never select a daemon JVM; `javaHome`
  is an explicit override only. We **report** the daemon JVM in every result.
- **Consequence.** A daemon is reused only by clients with a compatible Java home and
  JVM args. Android Studio defaults to its JBR, the terminal to `JAVA_HOME`: when they
  differ, our builds, the terminal's and Studio's run in separate daemons, each
  hundreds of megabytes. Spike S5 measures it; a project with daemon JVM criteria
  removes the problem.
- A JVM too new for the project's Gradle (e.g. JDK 21 with Gradle 8.0–8.4) fails with
  Gradle's own message plus the `javaHome` override as the next step. No silent
  switching.

## Version detection and toolchain profile

Implements [ADR-0018](../adr/0018-gradle-version-support-policy.md).

1. **Gradle, detected twice**: the wrapper's `distributionUrl` before connecting
   (cheap, for the UI), then the authoritative version from `BuildEnvironment`.
2. **`GradleCapabilities`**, one per connection, each flag from the documented `@since`:
   `problemEvents` (8.12, the shape we decode), `problemLocations` (8.13),
   `detailedFailure` (8.12), `problemSummaries` (8.12), `buildPhaseEvents` (7.6),
   `testSpecFilters` (7.6), `versionInfo` (9.4), `taskProvenance`. Subscription-time
   decisions are gated on these; call-time optional methods stay wrapped in
   `UnsupportedMethodException` catches regardless.
3. **Plugin facts**, per project model: `agp`, `kgp`, `kotlinBuiltIn`, from the probe.
   `unknown` if the probe did not run. Never parsed from build scripts or catalogs.
4. **Expected error source per tool**, derived from 2 and 3:

   | Tool | Structured when | Otherwise |
   | --- | --- | --- |
   | Kotlin (JVM) | `problemLocations` and KGP ≥ 2.4 | console |
   | javac | `problemLocations` (javac reports since Gradle 8.9) | console |
   | AAPT2, manifest merger, KSP, kapt | not assumed; S1's TAPI run checks | console |

   These choose which source is primary and what the result reports as expected. They
   never switch the extractor off.
5. **Report it** in every build result: Gradle version and source, AGP, KGP,
   `profile` and `validation`. Both are defined in
   [ADR-0018](../adr/0018-gradle-version-support-policy.md): `full` (Gradle ≥ 8.13,
   every gated capability working), `degraded` (8.0–8.12, or a capability that failed
   at call time; the result lists what is missing), `conservative` (below the floor
   through the escape hatch); `tested` / `untested` / `unsupported` by CI row and
   tier.

## Failure modes

| Failure | Behavior |
| --- | --- |
| Gradle below the floor (8.0) | `BUILD_GRADLE_UNSUPPORTED`, naming floor, detected version and reason; the explicit escape hatch runs the conservative profile ([ADR-0018](../adr/0018-gradle-version-support-policy.md)) — not yet: the hatch waits for the configuration specification (milestone 1g, D9) |
| Version newer than validated | Proceed with the newest capability set, marked `untested` |
| No usable sidecar JVM | `BUILD_SIDECAR_UNAVAILABLE`, the `[gradle]` capabilities unavailable in `system.capabilities`; the error lists the candidates tried and why each was rejected |
| Sidecar fails to start, or speaks another protocol version | `BUILD_SIDECAR_UNAVAILABLE`; other capabilities unaffected |
| Sidecar crashes mid-call or mid-build, or breaks the protocol | `BUILD_SIDECAR_CRASHED`; every root with a connection is marked degraded until the next handshake; no record for a build it was running; registry keeps history; restart on next use |
| Configuration of the project fails | `PROJECT_MODEL_FAILED`, Gradle's root failure as the cause |
| `build.errors` or `build.lastResult` with no finished build, or an unknown `buildId` | `BUILD_NOT_FOUND` |
| Probe fails, fully or per field | Generic model still returned; affected fields `unknown`; variants fall back to task names |
| Problems API unavailable or partial | Console extractor covers it; the result reports the source per tool |
| Failed build, nothing recognised | `unrecognised: true`, root failure messages and a bounded tail — never an empty success |
| Output metadata missing | No output for that variant, with the reason; install gets `BUILD_OUTPUT_NOT_FOUND` |
| Daemon OOM | Surface the daemon's own error and the memory flags it suggests; do not retry blindly |
| Task not found | A failed build with Gradle's own message; `build.tasks` is the discovery path (no near-matches yet, milestone 1g D19) |
| No wrapper in the project | Report it; TAPI would otherwise silently use its own Gradle version |

## Persistence

Per [ADR-0021](../adr/0021-persistence-format-and-location.md):

- The compact build result (the model above) per build in `ws/<wsid>/builds/`, and
  the console as a durable artifact in `ws/<wsid>/artifacts/`.
- **`lastResult` survives restarts**: the newest finished record. The console is
  referenced, not inlined.
- The output-path index behind `outputs[]` is persisted with the record, so an install
  reference stays resolvable after a restart (subject to the `BUILD_OUTPUT_STALE` check).
- Retention: capped by count and age (working default: last 50 builds or 7 days),
  oldest first. Configurable.

## Testing

- **Fixtures with pinned versions**: rewrite the wrapper's `distributionUrl` and the
  plugin versions per fixture, so tests run against specific targets.
- **Compatibility matrix** per ADR-0018, with a KGP axis (2.3 vs 2.4) and an AGP axis
  (floor and latest).
- **Extractor golden tests**: recorded real consoles and failure texts in, expected
  records out. Pure Go, no JVM. S1 run 1 supplies Kotlin, KSP and AAPT2 (command
  line); TAPI-forwarded recordings replace them where they differ, and every
  "Expected" form in the extractor table needs its own recording.
- **Probe canary**: the probe against the Gradle floor and the newest release, and AGP
  floor and latest. It must return a model, and a broken field must degrade to
  `unknown`, never fail the fetch.
- **Daemon hygiene**: pinned `GRADLE_USER_HOME`, pinned `javaHome`, short daemon idle
  timeout, bounded workers — otherwise tests leak daemons.
- Protocol tests that spawn the real sidecar and assert the handshake, the method
  surface and failure behaviour.

## Spikes before the sidecar is written

**S1 — error sources on real Android.** Matrix: KGP 2.3 vs 2.4 × Gradle 8.13 / 9.x ×
AGP 8.x / 9.x. Errors: Kotlin, javac, AAPT2, manifest merger, KSP. For each, capture
failure-carried problems, Problems events and the console. Output: which cells give
structured locations (updates the table in step 4 above) and the recorded console
formats (the extractor's fixtures). Whatever S1 finds, the extractor ships: the
question it answers is which source is primary where, not whether we parse.

*Run 1 (2026-09-29), partial* — [findings](../research/spike-s1-error-formats.md):
one project, one cell outside the matrix (Gradle 8.13, AGP 8.13.2, KGP 2.0.21), via
`./gradlew --no-daemon --console=plain`. It recorded the Kotlin, KSP and AAPT2 forms
marked Observed above and raised the merged-resource path problem. **Not yet run:**
the TAPI path, the Problems API (failure-carried and events), the matrix, javac,
kapt, manifest merger, a warm daemon.

*Run 2 (2026-10-06)* — [findings](../research/spike-s1-error-formats.md): the TAPI path
on the first sidecar, a warm daemon, DopamineControl's cell plus one KGP 2.4.20 cell.
Kotlin locations in the forwarded console (Problems events from KGP 2.4), javac
structured on Gradle 8.13, KSP, AAPT2 (failure node and console; AGP's blame file
resolves the merged path) and the manifest merger recorded. Not run: kapt, the rest
of the matrix.

**S3 — Android facts from a non-Studio client.** *Moved to staging 3 with the probe it
gates (milestone 1g, D25; ADR-0017 amended 2026-10-06).* Run the probe on the sample app and
on a flavoured, multi-module fixture across AGP 8.0 / 8.x / 9.x: which fields resolve,
cost of the model fetch, and the accuracy of the task-name fallback against the
probe's variants. If AGP DSL access proves brittle, evaluate AGP's builder model
(ADR-0023 §Alternatives) before Phase 1 ends.

S5 (daemon reuse with the terminal and Android Studio) informs the JVM defaults but
does not block.

## Open questions

- Whether to expose a raw "arbitrary Gradle arguments" escape hatch, mirroring
  `shell.exec`.
- Included builds (composite projects) in `project.model`.
- Isolated Projects: whether the probe's per-project registration survives it (S3).
