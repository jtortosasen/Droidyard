# ADR-0017: Gradle execution strategy — Tooling API sidecar vs. CLI

- Status: Accepted
- Date: 2026-09-27

## Context

Our `build.*` domain needs, at minimum: execute builds and tasks, **list available
tasks**, and return **structured compile errors** with file, line and column.

The reference implementation for this problem ([`gradle-mcp`](../../research/gradle-mcp.md))
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

- **Gains**: native structured problems (with line/column), structured task listing
  via the `GradleProject` model, test events, token-based graceful cancellation, rich
  progress, correct daemon reuse, and full control of Java home/environment.
- **Costs**: a second runtime artifact we write and maintain, in another language;
  ~200–400 MB while alive; its own lifecycle (mitigated by lazy start + idle
  shutdown); a versioned internal protocol.

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

**A, staged.** The highest-value outputs we need — structured errors with exact
locations, and structured task listing — live exactly where TAPI is strong and where
the CLI path is weakest and most brittle. Android builds already require a JDK, so a
JVM sidecar is not foreign to the environment, and our architecture already has a
first-class concept for it.

Staging: the sidecar implements `build.tasks` and `build.errors` first (the two
things the reference implementation lacks or does badly), then execution is extended
onto it.

**Language: Kotlin.** It is the native language of the Gradle/Android ecosystem; the
Tooling API is a Java API and Kotlin consumes it without friction. The sidecar
speaks JSON-RPC over stdio to the core — the same shape the Kotlin LSP uses — so the
project keeps a single internal protocol idiom.

The honest cost: this is the largest deviation from the "lean" goal so far.

## Consequences

- The core stays Go and JVM-free; the JVM cost is isolated to one sidecar that idles
  out.
- We own a new artifact and an internal protocol, both of which need versioning.
- `build.errors` can return `{file, line, column, severity, problemId, message,
  solutions}` — strictly better than the reference implementation.
- `build.tasks` becomes a first-class structured query instead of "run `tasks` and
  read the console".
- The MVP's idle-memory budget must be restated: the sidecar is a JVM that exists
  while it is used.

## Consequences of option B (rejected)

*Kept for the record:* no new artifact or language in the repo, but we would rebuild
task attribution and error extraction ourselves, accepting brittleness across Gradle
versions, and would likely reach into internal APIs — degrading structured errors to
console parsing plus best-effort init-script output.

## Status

Accepted on 2026-09-28.

## Recorded sub-decisions

- **Version support**: see [ADR-0018](0018-gradle-version-support-policy.md) — a
  declared floor, capability gating, and a published supported range.
- **JDK**: the sidecar's own JVM targets an **LTS release** (17 or 21); whether it is
  detected on the machine or bundled is still open. The **Gradle daemon's** JVM
  belongs to the project (toolchains / daemon JVM criteria) and must **not** be
  forced — that is the complexity we deliberately avoid.
- **`lastResult` persists** across core restarts: the compact result of the last
  finished build is written to the workspace state directory, so the UI or a new
  agent session can ask how the last build went without rebuilding.

## Pending design (not decided)

- The sidecar's **method surface** and its parameters.
- **Where build state lives**: core vs. sidecar, and what the sidecar owns.
- **Transport for high-volume build output**: batched JSON-RPC, a separate channel,
  or a spill-to-disk reference.
- **Persistence shape and location**, plus the retention policy for the build
  registry.
