# Subsystem: Gradle

How the core drives Gradle: task discovery, builds, tests, and structured errors.
Implements [ADR-0017](../adr/0017-gradle-execution-strategy.md) (TAPI sidecar) and
[ADR-0018](../adr/0018-gradle-version-support-policy.md) (version policy).

> Status: draft. Proposals for the items left open in ADR-0017 are marked
> **Proposed** and need ratification before implementation.

## Why a sidecar

The Gradle Tooling API (TAPI) is the only way to get **structured** build
information: real per-task outcomes, a failure tree with stable IDs, test events,
and the Problems API (compiler errors with locations). TAPI is a Java API, so it
cannot be called from the Go core.

```text
core (Go)
  │  JSON-RPC over stdio
  ▼
gradle-sidecar (Kotlin, JVM)
  │  Tooling API
  ▼
Gradle daemon  ──▶  project build
```

The sidecar is a **sidecar in the architecture's sense**
([process-lifecycle](../architecture/process-lifecycle.md)): started on demand,
supervised, stopped when idle, never able to take the core down.

## Division of responsibility

**The core owns the state; the sidecar is an executor.**
*(Proposed — this is the "where does build state live" item from ADR-0017.)*

| Owns | Core | Sidecar |
| --- | --- | --- |
| Build registry, IDs, retention | ✅ | — |
| Persistence of `lastResult` | ✅ | — |
| Capability mapping and version gating policy | ✅ | — |
| TAPI connection and its lifecycle | — | ✅ |
| Progress/failure/problem extraction | — | ✅ |
| Gradle daemon reuse | — | ✅ |

Rationale: persistence and `lastResult` survive restarts only if the core owns
them; and the sidecar must be restartable (crash, upgrade, idle shutdown) without
losing history. The core **allocates** build IDs and passes them in, so identity is
consistent with the rest of the capability API.

## Capability mapping

| Capability | Sidecar method | Notes |
| --- | --- | --- |
| `build.tasks` | `tasks.list` | Structured, from the project model — not "run `tasks`" |
| `build.assemble` | `build.run` | `assemble<Variant>`; returns a build id immediately |
| `build.run` | `build.run` | Arbitrary task, foreground or background |
| `build.test` | `build.test` | TAPI test launcher, with filters gated by capability |
| `build.errors` | `build.result` / `problems` | Structured; see the error model |
| `build.lastResult` | — | Served by the core from its registry |
| `build.cancel` | `build.cancel` | Cancellation token, graceful |
| events | notifications | Progress, output, problems, task/test results |

## Protocol

**Transport**: JSON-RPC 2.0 over stdio, the same idiom as the Kotlin LSP.

Handshake and methods *(Proposed)*:

```text
hello              → { protocolVersion, sidecarVersion, tapiVersion, capabilities }
connection.open    → { gradleVersion, source, javaHome, profile, validation }
connection.close
tasks.list         { root, filter?, group? }
build.run          { buildId, root, tasks[], args?, background? }
build.test         { buildId, root, task, filters? }
build.status       { buildId }
build.result       { buildId }
build.cancel       { buildId }
shutdown
```

Notifications:

```text
event.build.started      { buildId }
event.build.progress     { buildId, phase, counts, activeTask }
event.build.output       { buildId, lines[] }        // batched, see below
event.build.taskFinished { buildId, path, outcome, durationMs, reason? }
event.build.problem      { buildId, problem }
event.test.finished      { buildId, suite, test, outcome, durationMs }
event.build.finished     { buildId, outcome }
```

**Protocol versioning.** Core and sidecar ship as a **matched pair**; the handshake
carries `protocolVersion` and the core refuses a mismatch with a clear error instead
of degrading silently. (Versus Gradle versions, see below.)

## Output: control vs. volume

*(Proposed — the "transport for high-volume output" item.)*

A build can emit hundreds of megabytes of console output. Sending one JSON message
per line would be wasteful and would flood the pipe.

- **Control** (progress, task outcomes, problems) goes over JSON-RPC as normal
  notifications.
- **Console output** is sent as **coalesced batches**: up to N lines or M bytes per
  message, whichever comes first, with a small flush interval. Bounded message rate,
  one pipe, no separate channel.
- The **full console** is written by the core to a file under the workspace state
  directory and referenced as an opaque artifact
  ([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)); consumers read it by byte
  offset. The UI streams it; MCP renders a bounded tail.

## The build model

```text
Build
├── id              allocated by the core
├── root            project root
├── requests        tasks, args, test filters
├── startedAt / finishedAt
├── status          running | finished | cancelled
├── outcome         SUCCESS | FAILED | UP_TO_DATE | FROM_CACHE | SKIPPED |
│                   NO_SOURCE | CANCELLED
├── tasks[]         { path, outcome, durationMs, reason?, provenance? }
├── problems[]      structured errors/warnings (below)
├── tests[]         { suite, test, outcome, durationMs }
├── failures[]      failure tree with stable ids
├── gradleVersion   + source (wrapper|connector) + profile + validation
└── consoleRef      artifact reference
```

Rules copied from the prior art because they are right:

- **Outcome taxonomy with verbatim reasons** — "assembled" and "up-to-date" are
  different answers, and an agent needs to tell them apart.
- **Failure tree with stable IDs**, so `build.errors` can return a three-item
  "recent error context" rather than megabytes of console.
- **Available tasks ≠ executed tasks**: `build.tasks` lists what exists;
  the build result lists what ran.

## Error model

```json
{
  "severity": "error",
  "problemId": "kotlin.compiler.unresolved-reference",
  "file": "app/src/main/java/Foo.kt",
  "line": 83,
  "column": 12,
  "message": "Unresolved reference: Bar",
  "solutions": ["import com.example.Bar"],
  "documentationLink": "https://..."
}
```

Non-negotiable: **file, line and column**. The reference implementation receives
them from the Problems API and discards them (its location mapping only keeps the
path). We keep them, and we keep the severity semantics of the target Gradle version
aware that Gradle 9.6 changed how severity is assigned.

When the Problems API is not available on the target version (below 8.4), the
capability degrades explicitly: the result says which extraction path was used,
rather than silently returning fewer errors.

## Task listing

`build.tasks` returns structured tasks from the project model:

```json
{ "tasks": [
  { "path": ":app:assembleDebug", "name": "assembleDebug",
    "group": "build", "description": "Assembles main output..." } ] }
```

- Filterable by group and name substring.
- **Cost is real**: resolving the task model requires configuring the project. The
  result is cached per `(root, build files fingerprint)` and invalidated when the
  build files or the wrapper change.
- Hidden tasks are opt-in (they are noise by default).

## Lifecycle

- **Lazy start**: the sidecar starts on the first `build.*` or `project.*` call that
  needs it, not at core start.
- **Idle shutdown**: stopped after a configurable idle period (working default:
  10 minutes), never while a build is running.
- **Health**: the handshake covers startup; a lightweight ping covers liveness.
- **Crash**: a sidecar crash mid-build fails that operation with a clear error and
  leaves the workspace `degraded`; the registry keeps the history. The next call
  restarts it.
- **Never kill the Gradle daemon.** It belongs to Gradle and is reused across builds
  by design; an explicit stop is available but never implicit.
- **No orphans**: the sidecar is a child of the core, killed on core shutdown.

## Environment and JVM

- **Environment source** is explicit: inherit, shell-probed, or none. This matters on
  macOS, where a GUI-launched core does not see the shell's `PATH`/`JAVA_HOME`.
- **`javaHome` override** is available; otherwise we do not touch the daemon JVM.
  The project (toolchains, daemon-JVM criteria) decides — we only **report** which
  JVM was used.
- **The sidecar's own JVM** targets an LTS release. Whether we detect it on the
  machine or bundle a runtime is still **open**.

## Version detection and capability gating

Implements [ADR-0018](../adr/0018-gradle-version-support-policy.md).

1. **Detect twice**: parse the wrapper's `distributionUrl` before connecting (cheap,
   for the UI), then read the authoritative version from `BuildEnvironment`.
2. **Compute one `GradleCapabilities`** per connection: `problemsEvents`,
   `problemDetails`, `problemLocations`, `buildPhaseEvents`, `testSpecFilters`,
   `detailedFailure`, `versionInfo`, `taskProvenance`. Every decision passes through
   it — no bare version comparisons scattered in the code.
3. **Subscription-time decisions must be gated**: subscribing to a problem event on a
   version that cannot emit it is a bug. Call-time optional methods stay wrapped in
   `UnsupportedMethodException` catches regardless.
4. **Report it** in every build result: version, source, profile
   (`full`/`degraded`), and `validation` (`tested`/`untested`).

## Failure modes

| Failure | Behavior |
| --- | --- |
| Gradle below the floor (8.0) | Fail fast naming floor, detected version and reason; explicit escape hatch to conservative mode |
| Version newer than validated | Proceed with the newest capability set, marked `untested` |
| Sidecar fails to start | Workspace `degraded`; `build.*` returns an actionable error; other capabilities unaffected |
| Sidecar crashes mid-build | Operation fails with cause; registry keeps history; restart on next use |
| Problems API unavailable | Explicit degradation, reported in the result — never fewer errors silently |
| Daemon OOM | Surface the daemon's own error and the memory flags it suggests; do not retry blindly |
| Task not found | `build.tasks` is the discovery path; the error lists near-matches where possible |
| No wrapper in the project | Report it; TAPI would otherwise silently use its own Gradle version |

## Persistence

- Location: the workspace state directory (see
  [workspace-state.md](../architecture/workspace-state.md)).
- Written: the compact build result (the model above) per build, plus the console as
  a separate artifact file.
- **`lastResult` survives restarts**; the console is referenced, not inlined.
- Retention: capped by count and age (working default: last 50 builds or 7 days),
  oldest first. Configurable.

## Testing

- **Fixtures with pinned Gradle versions** (the reference implementation's pattern:
  rewrite the wrapper's `distributionUrl` per fixture) so tests run against specific
  targets rather than the build's own version.
- **Compatibility matrix** per ADR-0018, including an **init-script canary** on the
  floor version.
- **Daemon hygiene**: pinned `GRADLE_USER_HOME`, pinned `javaHome`, short daemon idle
  timeout, bounded workers — otherwise tests leak daemons.
- Protocol tests that spawn the real sidecar and assert the handshake, the method
  surface and failure behavior.

## Open questions

- Sidecar JVM: detect on the machine vs. bundle a runtime (`jlink`).
- Whether to expose a raw "arbitrary Gradle arguments" escape hatch, mirroring
  `shell.exec`.
- Whether `build.tasks` should be variant-aware (Android variants map to task
  name prefixes) or purely model-driven.
- Exact batching parameters for console output (lines/bytes/interval).
- Whether to persist the console at all, or only on failure.
