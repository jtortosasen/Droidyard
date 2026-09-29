# ADR-0018: Gradle version support policy

- Status: Accepted
- Date: 2026-09-28
- Amended: 2026-09-29 — plugin (KGP/AGP) axis added, Gradle 7.3–7.x given a tier
  (below the floor), the conservative profile defined, the canary named.
- Amended: 2026-09-29 (second pass) — the tiers made exhaustive in fact: the first
  amendment claimed it, but left Gradle 9.0 up to the minor below Tier A (including
  9.1, which AGP 9.0 requires) in no tier. `validation` and all three profiles
  defined. Tier B's "degradation for test filters" removed: filters exist from 7.6,
  below the floor.
- Relates to: [ADR-0017](0017-gradle-execution-strategy.md),
  [ADR-0022](0022-build-error-extraction.md),
  [ADR-0023](0023-android-project-facts-and-build-outputs.md)

## Context

The Tooling API is version-independent with two guarantees: it supports the **last
five major Gradle releases**, and it is **compatible with the current and next
major** going forward (a 9.x client drives Gradle 10.x and *might* break with 11).
The documented floor (Gradle 4.0) is mechanical, not valuable.

What actually constrains us:

- **Daemon JVM**: Java 17 needs Gradle 7.3+; Java 21 needs 8.5+.
- **Android reality (AGP)**: AGP 8.0 → Gradle 8.0; AGP 9.0 → 9.1; AGP 9.4 → 9.6.
- **Gradle features we depend on**: build-phase events and fine-grained test filters
  from 7.6; Problems events from 8.4, but the `Problem` shape the current client
  decodes, `Failure.getProblems()` and event summaries from **8.12**, and problem
  **locations from 8.13**; javac reporting to Problems from 8.9; version info from 9.4.
- **Plugin versions (the axis the original policy missed)**: Kotlin compiler errors
  reach the Problems API only with **KGP 2.4+** (KGP 2.2–2.3 report only KGP's own
  diagnostics). AAPT2, manifest-merger and KSP/kapt errors have no known Problems path.
  Whether a project gets structured compile errors therefore depends on its KGP and
  AGP, not only on its Gradle.

Full analysis: [`../research/gradle-version-compatibility.md`](../research/gradle-version-compatibility.md).

Two findings shape the policy:

1. The **stable TAPI skeleton** (connection, execution, models, task/test events)
   has not broken across Gradle 7/8/9 in the reference implementation.
2. Everything that makes the product good — Problems, build phases, failure
   details, test filters — is **`@Incubating`** and has been changing within minor
   releases. The **Problems API is our biggest versioned risk**, and it is also
   only one of the sources behind `build.errors`
   ([ADR-0022](0022-build-error-extraction.md)).

## Decision

**Floor: Gradle 8.0** for the first release (which implies AGP 8.0+). Every Gradle
version falls in exactly one row; patch releases follow their minor. Tier A moves with
each release. As of 2026-09-28 (current 9.8), A is 9.8, 9.7 and 8.14.x, and B is
8.0–8.13 plus 9.0–9.6.

| Tier | Gradle | Commitment |
| --- | --- | --- |
| **A — fully supported** | newest 9.x minor, the previous 9.x minor, 8.14.x | All capabilities the version has. Each is a CI row; regressions block |
| **B — supported** | 8.0 – 8.13, and 9.0 up to the minor below Tier A (includes 9.1, AGP 9.0's minimum) | Build, models, tests, failure tree, console extractor, plus each incubating capability from the version that introduced it (rule 1): failure-carried problems from 8.12, problem locations from 8.13, version info from 9.4. Below 8.13, errors come from the extractor. CI rows 8.0, 9.0 and 9.1 block like Tier A; other versions rely on the TAPI guarantee |
| **Newer** | above the newest validated release | Rule 2 |
| **C — below the floor** | 7.3 – 7.x (including 7.6) | Refused by default (rule 3). The escape hatch runs the **conservative profile**, `validation: unsupported`. 7.6 is the first candidate if demand ever justifies lowering the floor |
| **D — rejected** | below 7.3 | Refused, no escape hatch: cannot run on the Java 17+ we assume, and no AGP 8 project can be there |

**`validation`** says how much evidence stands behind a result: `tested` when the
Gradle minor is a CI row; `untested` for any other version at or above the floor,
including newer ones; `unsupported` for Tier C through the escape hatch.

**Plugins are reported, not tiered.** AGP and KGP versions are detected by the probe
([ADR-0023](0023-android-project-facts-and-build-outputs.md)), reported, and select the
primary error source per tool. They never refuse a build. Unknown plugin versions
(probe unavailable) mean "assume unstructured", so the console extractor is primary.

Rules:

1. **Gate on capabilities, never on bare version comparisons.** One toolchain profile
   per connection and project model: `GradleCapabilities` from the Gradle version,
   plus plugin facts from the probe, plus the expected error source per tool derived
   from both. All decisions pass through it. Subscription-time decisions must be gated,
   and optional call-time methods stay wrapped in `UnsupportedMethodException` catches.
2. **Newer or unrecognised versions proceed optimistically** with the newest
   capability set, marked `validation: untested`. Never hard-fail on a version we have
   not seen — Gradle or plugin. (Known Gradle versions at or above the floor are in
   Tier A or B, not here.)
3. **Below the floor (Tier C), fail fast** naming the floor, the detected version and
   the reason. The error names the escape hatch: per-workspace configuration
   `gradle.allowBelowFloor: true`. With it, the **`conservative` profile** applies
   ([below](#capability-profile-values)): the stable skeleton only; test filters
   **refused** with an error, never silently dropped; errors from the failure tree and
   the console extractor; the probe still attempted, isolated as always; results carry
   `validation: unsupported`.
4. **Auxiliary mechanisms never touch the user's build.** No init script or probe is
   ever injected into a build (`build.run`, `build.assemble`, `build.test`). The only
   init script, the probe, runs inside `project.model`, and its failure degrades the
   model only.
5. **We publish the supported range**: Gradle tiers, AGP 8.0+, and which KGP/Gradle
   combinations give structured Kotlin errors. The reference implementation does not
   publish one, and that is a policy gap we do not repeat.
6. Every build result reports `gradleVersion`, its source (`wrapper`/`connector`),
   `agpVersion`, `kgpVersion`, the capability profile (`full` / `degraded` /
   `conservative`, below) and `validation` (`tested` / `untested` / `unsupported`,
   above).

### Capability profile values

The `profile` value (rule 6) summarises the Gradle side of the toolchain profile
(rule 1): which Gradle capabilities a connection uses. It is derived from
`GradleCapabilities` alone.

| | `full` | `degraded` | `conservative` |
| --- | --- | --- | --- |
| **When** | Gradle ≥ 8.13 (Tier A, B from 8.13, newer) | Gradle 8.0–8.12; or any version where a capability it should have fails at call time | Tier C through the escape hatch only |
| Stable skeleton: connection, `BuildEnvironment`, `GradleProject`, execution, task/test events, cancellation | ✓ | ✓ | ✓ |
| Build-phase events, test filters (7.6) | ✓ | ✓ | off; filters refused with an error |
| Failure-carried problems, Problems events, summaries (8.12) | ✓ | on at 8.12 | off |
| Problem locations (8.13) | ✓ | off | off |
| Failure tree, console extractor | ✓ | ✓ | ✓ (the only error sources) |
| Probe (`project.model` only) | attempted | attempted | attempted |

- Version info (9.4) is not part of the distinction: below 9.4 the version comes from
  `BuildEnvironment`, and nothing reported changes.
- **Call-time downgrade.** A gated capability that throws `UnsupportedMethodException`
  is switched off for that connection, the profile drops to `degraded`, and the result
  names the missing capability. A `degraded` result always lists what is missing.
- The profile describes what **Gradle** delivers, not whether plugins use it: a `full`
  profile on KGP < 2.4 still gets Kotlin errors from the console, per the expected
  source per tool (rule 1).
- `degraded` here is a profile value, unrelated to the workspace state of the same
  name ([workspace-state](../architecture/workspace-state.md)); a `degraded` profile
  does not degrade the workspace.

## Consequences

- A defensible, honest claim: fully validated on a few versions, best-effort on the
  rest through the TAPI guarantee.
- CI carries a version matrix with a **KGP axis** (2.3 vs 2.4) and an **AGP axis**
  (floor and latest), plus a non-blocking nightly "future" job and the **probe
  canary**. The canary runs `droidyard-probe.init.gradle` against the Gradle floor and the
  newest release, and against AGP floor and latest. It must return a model, and a
  broken field must degrade to `unknown`.
- On Gradle below 8.13, and on any project with KGP < 2.4, Kotlin errors are
  console-extracted. The fixture corpus is part of the support commitment.
- We must track the incubating APIs we adopt (Problems above all) as owned risk,
  and expect to adapt them on minor Gradle releases.
- Keeping init scripts to one, and out of builds, is a **policy**: they are the
  highest-churn surface and the only one that can break a user's build.

## Alternatives considered

- **Chase the documented Gradle 4.0 floor.** Rejected: no 2026 Android project is
  there, and the daemon-JDK and AGP constraints make it unreachable in practice.
- **Floor at 7.6 for broader coverage.** Deferred: 7.6 sits in Tier C, reachable
  through the escape hatch. The cost is re-validating the probe under older Gradle
  APIs, and the Android demand is thin.
- **Gate on Gradle only.** This was the original policy. It cannot predict whether
  Kotlin errors are structured, which depends on KGP. Rejected.
- **Refuse projects on KGP < 2.4.** Most projects would be excluded for the sake of
  one feature that the extractor already covers. Rejected.
- **No declared floor; attempt anything.** Rejected: produces silent degradation and
  unexplainable failures, which is exactly the failure mode observed in the
  reference implementation.
