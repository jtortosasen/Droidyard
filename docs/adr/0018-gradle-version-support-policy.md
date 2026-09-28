# ADR-0018: Gradle version support policy

- Status: Accepted
- Date: 2026-09-28
- Relates to: [ADR-0017](0017-gradle-execution-strategy.md)

## Context

The Tooling API is version-independent with two guarantees: it supports the **last
five major Gradle releases**, and it is **compatible with the current and next
major** going forward (a 9.x client drives Gradle 10.x and *might* break with 11).
The documented floor (Gradle 4.0) is mechanical, not valuable.

What actually constrains us:

- **Daemon JVM**: Java 17 needs Gradle 7.3+; Java 21 needs 8.5+.
- **Android reality (AGP)**: AGP 8.0 → Gradle 8.0; AGP 9.0 → 9.1; AGP 9.4 → 9.6.
- **Features we depend on**: structured problems from **8.4**; build-phase events
  and fine-grained test filters from 7.6; failure details from 8.12.

Full analysis: [`../research/gradle-version-compatibility.md`](../research/gradle-version-compatibility.md).

Two findings shape the policy:

1. The **stable TAPI skeleton** (connection, execution, models, task/test events)
   has not broken across Gradle 7/8/9 in the reference implementation.
2. Everything that makes the product good — Problems, build phases, failure
   details, test filters — is **`@Incubating`** and has been changing within minor
   releases. The **Problems API is our crown jewel and our biggest versioned risk**.

## Decision

**Floor: Gradle 8.0** for the first release. Support tiers:

| Tier | Versions | Commitment |
| --- | --- | --- |
| **A — fully supported** | latest 9.x, previous 9 minor, newest 8.x | All features; CI-gated, regressions block |
| **B — supported floor** | 8.0 – 8.13 | Core features; documented degradation for problems, failure details, version info and test filters |
| **C — legacy (demand-driven)** | 7.6.x | Deferred; only if real demand appears |
| **D — rejected** | below 7.3 | Fail fast with an actionable message |

Rules:

1. **Gate on capabilities, never on bare version comparisons.** One
   `GradleCapabilities` object is computed per connection; all decisions pass
   through it. Subscription-time decisions must be gated, and optional call-time
   methods stay wrapped in `UnsupportedMethodException` catches.
2. **Unknown or newer versions proceed optimistically** with the newest capability
   set, marked `validation: untested`. Never hard-fail on a version we have not
   seen.
3. **Below the floor, fail fast** naming the floor, the detected version and the
   reason, with an explicit escape hatch that switches to a conservative profile.
4. **Auxiliary mechanisms must never fail the user's build.** Any init script or
   optional probe is isolated so its failure degrades reporting only.
5. **We publish the supported range.** The reference implementation does not, and
   that is a policy gap we do not repeat.
6. Every build result reports `gradleVersion`, its source (`wrapper`/`connector`),
   the capability profile (`full`/`degraded`) and `validation` (`tested`/`untested`).

## Consequences

- A defensible, honest claim: fully validated on a few versions, best-effort on the
  rest through the TAPI guarantee.
- CI carries a version matrix plus a non-blocking nightly "future" job and an
  init-script canary on the floor.
- We must track the incubating APIs we adopt (Problems above all) as owned risk,
  and expect to adapt them on minor Gradle releases.
- Keeping init scripts to a minimum is now a **policy**, not just a preference: they
  are the highest-churn surface and the only one that can break a user's build.

## Alternatives considered

- **Chase the documented Gradle 4.0 floor.** Rejected: no 2026 Android project is
  there, and the daemon-JDK and AGP constraints make it unreachable in practice.
- **Floor at 7.6 for broader coverage.** Deferred to Tier C: the cost is not the
  TAPI protocol but re-validating init scripts under older Kotlin/Groovy and older
  features, and the Android demand is thin.
- **No declared floor; attempt anything.** Rejected: produces silent degradation and
  unexplainable failures, which is exactly the failure mode observed in the
  reference implementation.
