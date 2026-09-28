# Research: Gradle version compatibility (Tooling API)

What it actually costs to support many Gradle versions, based on official Gradle
documentation and on the maintenance burden visible in
[`gradle-mcp`](gradle-mcp.md).

- Researched: 2026-09-28
- Official sources: `docs.gradle.org` (current = 9.8.0, plus 9.0.0, 8.14.3, 8.6,
  8.4, 7.6.4 snapshots) and `developer.android.com` for AGP pairing.
- Local evidence: `.refs/gradle-mcp`.

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
3. **Features we depend on.** Structured problems arrive at **8.4**; build-phase
   events and fine-grained test filters at **7.6**; failure details at **8.12**.

## 3. Stability of the surface

| Surface | Since | Status |
| --- | --- | --- |
| `GradleConnector`, `ProjectConnection`, `BuildLauncher`, `ModelBuilder` | 1.0 | **Stable** |
| `GradleProject` / task models (structured task listing) | 1.0 | **Stable**, additive only |
| Task / config / test / status progress events | 2.5–6.0 | **Stable** |
| `OperationType.BUILD_PHASE` | 7.6 | **@Incubating** |
| `TestLauncher.withTestsFor` / `TestSpec` filters | 7.6 | **@Incubating**, *silently ignored* below 7.6 |
| `OperationType.PROBLEMS` and the Problems API | 8.4 | **@Incubating** |
| `withDetailedFailure()` / `Failure.getProblems()` | 8.12 | **@Incubating** |
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

Our structured `build.errors` depends on this API. That is a risk we take
knowingly, and it must be tracked as such.

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
  `GradleCapabilities` object per connection (`problemsEvents`,
  `problemDetails`, `problemLocations`, `buildPhaseEvents`, `testSpecFilters`,
  `detailedFailure`, `versionInfo`, `taskProvenance`, …) and make every decision
  through it. Subscription-time decisions must be gated (you cannot subscribe to
  `PROBLEMS` on 8.3), and call-time optional methods must still be wrapped in
  `UnsupportedMethodException` catches.
- **Report it**: every build result should carry the Gradle version, its source
  (`wrapper` or `connector`), the capability profile (`full` / `degraded`) and
  whether that version was `tested` or `untested`.

## 6. Recommended policy

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
problems capture, failure reporting), plus a nightly **non-blocking "future"** job
against the newest RC/nightly (catches the "might break" at Gradle 11), plus an
**init-script canary** on the floor version, plus capability unit tests asserting
that the mapping matches the documented `@since`.

## 7. Implications for us

1. "As many versions as possible" is achievable, but the honest claim is
   **"fully validated on a few, best-effort on the rest via the TAPI guarantee"**.
2. Our **floor should be 8.0** for the first release — below that the value drops
   fast and the daemon-JDK constraint bites.
3. We must **publish the supported range** (unlike the reference implementation).
4. Our exposure to Gradle churn is **structurally smaller** than theirs: TAPI events
   instead of internal APIs and init scripts. We must protect that advantage by
   keeping init scripts to a minimum and isolating the ones we cannot avoid.
5. The **Problems API being incubating** is a tracked, owned risk: it powers our
   best feature and it changes within minors.
