# Research: Gradle version compatibility (Tooling API)

What it actually costs to support many Gradle versions, based on official Gradle
documentation and on the maintenance burden visible in
[`gradle-mcp`](gradle-mcp.md).

- Researched: 2026-09-28; corrected 2026-09-29 (Problems timeline, plugin axis §3.1)
- Official sources: `docs.gradle.org` (current = 9.8.0, plus 9.0.0, 8.14.3, 8.6,
  8.4, 7.6.4 snapshots; release notes 8.6–8.11; current TAPI javadoc),
  `developer.android.com` for AGP pairing, and
  [Kotlin 2.4.0 what's new](https://kotlinlang.org/docs/whatsnew24.html).
- Local evidence: `.refs/gradle-mcp`.
- The policy that came out of this research is
  [ADR-0018](../adr/0018-gradle-version-support-policy.md); where §6 and the ADR
  differ, the ADR wins.

## 1. The compatibility model

TAPI is a **version-independent client protocol**: the same client library drives
builds whose wrapper selects older or newer Gradle. Two guarantees define it:

- **Backward**: the Tooling API supports **the last five major releases**. The docs
  state: *"the Tooling API 9.0.0 release is compatible with Gradle versions >=
  4.0"* (the floor advances one major per TAPI major).
- **Forward**: it is *"guaranteed to be compatible with future Gradle releases for
  the current and the next major"* — i.e. a 9.x client drives Gradle 10.x and
  *might* break with 11.

Failures are **typed, not silent**: `UnsupportedVersionException` on connect,
`UnsupportedMethodException` for model getters that the target lacks, and
per-method documented gaps. But some features are **silently ignored** below their
minimum (see §3) — the dangerous case.

**Bottom line:** the documented floor (Gradle 4.0) is *mechanical, not valuable*.
No 2026 Android project runs Gradle < 7.x.

## 2. What actually sets our floor

Three independent constraints, in order of importance:

1. **Daemon JVM.** The target's Gradle runs the daemon, and Gradle's Java matrix
   caps it: Java 17 works only on **Gradle 7.3+**; Java 21 only on **8.5+**. Gradle
   9 requires JVM 17–27. So if we assume a modern JDK, the floor moves to 7.3–8.5
   before any feature discussion.
2. **Android reality (AGP).** AGP 8.0 requires Gradle **8.0**; AGP 9.0 requires
   **9.1**; AGP 9.4 requires **9.6**. Android projects cannot be older than their
   AGP allows.
3. **Features we depend on.** Build-phase events and fine-grained test filters at
   **7.6**; Problems events at **8.4**, but the `Problem` shape and failure details at
   **8.12** and problem locations at **8.13** (see §3). Structured *compile* errors
   additionally depend on the project's **KGP and AGP** (§3.1).

## 3. Stability of the surface

| Surface | Since | Status |
| --- | --- | --- |
| `GradleConnector`, `ProjectConnection`, `BuildLauncher`, `ModelBuilder` | 1.0 | **Stable** |
| `GradleProject` / task models (structured task listing) | 1.0 | **Stable**, additive only |
| Task / config / test / status progress events | 2.5–6.0 | **Stable** |
| `OperationType.BUILD_PHASE` | 7.6 | **@Incubating** |
| `TestLauncher.withTestsFor` / `TestSpec` filters | 7.6 | **@Incubating**, *silently ignored* below 7.6 |
| `OperationType.PROBLEMS` (problem events reach TAPI clients) | 8.4 | **@Incubating** |
| Location types (`FileLocation`, `LineInFileLocation`, …) | 8.6 | **@Incubating**; first exposed through a descriptor shape the current client no longer ships |
| Java compiler reports to Problems | 8.9 | Gradle-side integration |
| `Problem` (definition, details, solutions), `ProblemSummariesEvent` | 8.12 | **@Incubating**; events throttled per problem id |
| `withDetailedFailure()` / `Failure.getProblems()` | 8.12 | **@Incubating** |
| `Problem.getOriginLocations()` / `getContextualLocations()` | 8.13 | **@Incubating** — the locations our client decodes |
| `BuildEnvironment.getVersionInfo()` | 9.4 | **@Incubating**, throws below 9.4 |

**The skeleton is stable; everything that makes the product good is incubating.**
That is the central finding: connection, execution, models and task/test events
have not broken across 7/8/9, while problems, phases, failure details and test
filters have been actively evolving through 8.x and 9.x minors.

### The Problems API is the volatile one — and it is our crown jewel

Introduced to TAPI clients in 8.4, announced as public incubation in 8.6, **still
`@Incubating` at 9.8**. Its shape grew step by step (8.8 aggregation, 8.9
definitions, 8.12 details/solutions, 8.13 locations), and **9.6 changed severity
semantics** (severity is now auto-assigned from whether the problem fails the
build; `ADVICE` is only emitted below 9.6). Gradle 9.8's docs also **shift the
recommended integration path** from progress events toward failure-carried
problems.

**When locations became available — reconciled.** Location types exist since 8.6,
on a descriptor shape that the current (9.x) TAPI client no longer ships. The
`Problem` interface the client exposes today dates from 8.12, and its locations
(`getOriginLocations`) from **8.13**. For us, structured locations therefore start at
**8.13**. An earlier draft of `gradle.md` implied 8.4, which was wrong: 8.4 is only
when events began to flow.

Our structured `build.errors` uses this API where it delivers. That is a risk we take
knowingly, and it must be tracked as such. It is not the only source
([ADR-0022](../adr/0022-build-error-extraction.md)).

### 3.1 The plugin axis: KGP and AGP

Gradle only transports problems; plugins must report them. Two plugins matter:

- **KGP.** Kotlin 2.2.0 made KGP report its *own* diagnostics (configuration and
  plugin warnings) to the Problems API. Only **Kotlin 2.4.0** (released 2026-07-14)
  "also writes compiler messages to the Problems API for Kotlin/JVM". Below 2.4, a
  Kotlin compile error exists only as console text (`e: file:///…:L:C message`). Kotlin
  2.4.0 is fully compatible with Gradle 7.6.3–9.5.0 and requires AGP 8.5.2+.
- **AGP.** No Problems reporting is known for AAPT2 resource linking, the manifest
  merger, or KSP/kapt, which are the most common Android-specific failures. Their
  detail lives in the console and, for AAPT2 and the manifest merger, in the failure
  message.

Consequence: structured Kotlin errors need Gradle ≥ 8.13 **and** KGP ≥ 2.4 (per the
documentation above). Most Android projects in 2026 are presumably on KGP ≤ 2.3
(2.4.0 is recent; not measured). A console extractor is therefore first-class, not a
fallback (ADR-0022). Spike S1 is to verify this across the matrix; its first run
covered one cell outside it (KGP 2.0.21), on the command line only, and did not
observe the Problems API
([findings](spike-s1-error-formats.md)).

## 4. Where `gradle-mcp`'s fragility actually lives

Its **public TAPI skeleton has never produced a recorded break**. All observed
fragility is elsewhere:

**(a) Init scripts reaching into `org.gradle.internal.*`.** Two scripts (`cc-report`,
`task-out`) touch ~14 internal types (logging managers, build-operation listeners,
output events). Internal APIs carry **no compatibility contract** and are loaded with
`-I` on *every* build — so a compile failure there **fails the user's build**. A
third script uses `@UntrackedTask`, which is public but **7.3+**, and the init-script
language itself moves (Gradle 9 compiles Kotlin DSL with Kotlin 2.2 and Groovy DSL
with Groovy 4).

**(b) Unguarded use of newer incubating features.** `versionInfo` (9.4+) read
without a guard; `withTestsFor` (7.6+) used without a guard, so on older targets the
filter is **silently dropped and un-filtered tests run**; `withDetailedFailure`
(8.12+) and the whole Problems accumulator used without guards or version checks.

**(c) A maintenance process that confirms the cost.** They have a dedicated skill to
"ingest" each Gradle upgrade: classify the delta, read every release-notes page (plus
family and transition guides for cross-major jumps), and build an **impact ledger**
with one row per material item. Their last recorded ingestion (9.6.1 → 9.7.0) is a
*within-major* bump that still required updating shipped skills, regenerating a
corpus, and re-validating 604 tests. Gradle ships every ~6 weeks.

**And they never document which versions they support** — arguably their biggest
policy gap.

## 5. Detection and adaptation

- **Detect twice**: parse the wrapper's `distributionUrl` (cheap, for the UI), then
  read the **authoritative** version from the `BuildEnvironment` model after
  connecting. If there is no wrapper, TAPI silently uses its own version, so
  detecting the effective one matters.
- **Gate on capabilities, not on version comparisons.** Compute one
  `GradleCapabilities` object per connection (`problemEvents`, `problemLocations`,
  `detailedFailure`, `problemSummaries`, `buildPhaseEvents`, `testSpecFilters`,
  `versionInfo`, `taskProvenance`, …) and make every decision through it.
  Subscription-time decisions must be gated (you cannot subscribe to `PROBLEMS` on
  8.3), and call-time optional methods must still be wrapped in
  `UnsupportedMethodException` catches.
- **Detect the plugins too** (§3.1). AGP and KGP versions are not in any generic
  TAPI model. We read them from the plugins' own version constants through a
  model-only probe ([ADR-0023](../adr/0023-android-project-facts-and-build-outputs.md)),
  never from build scripts or version catalogs.
- **Report it**: every build result should carry the Gradle version, its source
  (`wrapper` or `connector`), AGP and KGP versions, the capability profile
  (`full` / `degraded` / `conservative`, defined in ADR-0018) and whether that version
  was `tested`, `untested` or `unsupported`.

## 6. Recommended policy

*As recommended on 2026-09-28. [ADR-0018](../adr/0018-gradle-version-support-policy.md)
(amended twice on 2026-09-29) supersedes it. The ADR moves 7.3–7.x, including 7.6,
"below the floor", reachable through an escape hatch; puts 9.0 up to the minor below
Tier A (including 9.1) in Tier B, which this table left out; defines `validation` and
the three profiles; adds the KGP/AGP axis; and names the one init script (the
model-only probe) that the canary targets. Tier B's "degradation for test filters"
below does not hold: filters exist from 7.6.*

| Tier | Versions | Commitment |
| --- | --- | --- |
| **A — fully supported** | latest 9.x, previous 9 minor, newest 8.x (8.14.x) | All features on; regressions block merges (CI-gated) |
| **B — supported floor** | **8.0** up to 8.13 | Build/execute/models/tests; documented degradation for problems, failure details, version info, test filters |
| **C — legacy (demand-driven)** | 7.6.x | Degraded: no Problems, no failure details; separate allowed-failure list |
| **D — rejected** | below 7.3 (JDK 17 impossible), and anything below 6 | Fail fast with an actionable message |

Behavior:

- **Unknown or newer than validated**: proceed optimistically with the newest
  capability set (the forward guarantee), mark `validation: untested`, never
  hard-fail.
- **Below the floor**: fail fast naming the floor, the detected version and the
  reason, with an explicit escape hatch that switches to a conservative profile.
- **Never let an auxiliary mechanism fail the user's build.** Any init script or
  optional probe must be isolated so that its failure degrades reporting only.

CI: a matrix of pinned target versions (rows: 8.0, 8.14, 9.0, 9.1, latest-1,
latest; columns: connect, models, task listing, build+progress, tests with filters,
problems capture, failure reporting), with a KGP axis (2.3 vs 2.4) and an AGP axis
(floor, latest). Plus a nightly **non-blocking "future"** job against the newest
RC/nightly (catches the "might break" at Gradle 11), plus the **probe canary** on the
floor and newest versions, plus capability unit tests asserting that the mapping
matches the documented `@since`.

## 7. Implications for us

1. "As many versions as possible" is achievable, but the honest claim is
   **"fully validated on a few, best-effort on the rest via the TAPI guarantee"**.
2. Our **floor should be 8.0** for the first release — below that the value drops
   fast and the daemon-JDK constraint bites.
3. We must **publish the supported range** (unlike the reference implementation).
4. Our exposure to Gradle churn is **structurally smaller** than theirs: TAPI events
   instead of internal APIs and init scripts. We protect that advantage with one init
   script, the probe, which is injected into model fetches only and never into a
   build.
5. The **Problems API being incubating** is a tracked, owned risk. It changes within
   minors, and it delivers Kotlin compiler errors only with KGP 2.4+ on Gradle 8.13+.
   The console extractor is the other half of `build.errors`
   ([ADR-0022](../adr/0022-build-error-extraction.md)).
