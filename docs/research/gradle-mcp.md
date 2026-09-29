# Research: `gradle-mcp` (rnett)

- Repo: <https://github.com/rnett/gradle-mcp>
- License: Apache-2.0
- Language: Kotlin/JVM (JDK 21+), Gradle Tooling API
- Analyzed: 2026-09-27 (version observed: 0.0.16-SNAPSHOT)
- Local clone: `.refs/gradle-mcp`

## What it is

An MCP server that gives agents deep programmatic access to **generic Gradle**
builds: project mapping, task/test execution, dependency auditing, source search,
a Kotlin REPL with the project classpath, and Gradle docs. It is MCP-only, has no
web UI, and is not Android-specific (no variants, devices, APK/AAB semantics).

It is the most direct prior art for our `build.*` domain.

## The decisive finding

**It uses the Gradle Tooling API (TAPI) in-process. It never executes
`./gradlew` for builds.** Builds run through `ProjectConnection.newBuild().run()`,
tests through `newTestLauncher()`, metadata through `connection.model(...)`. There
is no process spawn of Gradle anywhere in the source.

Consequences: its best features are **byproducts of TAPI** —

- structured compiler problems via Gradle's **Problems API**
  (id, severity, docs link, occurrences, potential solutions);
- a **failure tree** (`withDetailedFailure()`) with stable IDs;
- real **test events** (start/finish/output/metadata/attachments);
- **cancellation tokens** and rich **progress** events;
- proper **daemon reuse** and Java-home/environment control.

**A Go runtime cannot call TAPI.** It is a Java API. This is the biggest
architectural difference between the two projects and it forces a decision.

## Task listing: their biggest gap

There is **no** structured task-discovery capability. Agents are told to run
`tasks` / `tasks --all` / `projects` through a generic `gradle` tool and read the
console, paying a full configuration phase each time, with no caching. There is
also no project discovery: the root must be passed explicitly (or via an env var)
and must already contain the wrapper.

Two lessons: (a) do not implement task listing as "run `tasks`"; (b) distinguish
**available tasks** from **executed tasks** (their `query_build(kind=TASKS)` lists
the latter).

## Errors, output and state

- **Errors**: structured, from Problems API + failure tree — but the Problems
  adapter **drops line/column** (a `LineInFileLocation` is rendered as just the
  path). The data exists; the adapter is lossy.
- **Output**: line-oriented capture with per-task attribution via an init script
  that reaches into **Gradle internal APIs**; tool results are **text, not JSON**;
  console scans are a fallback. `logBuffer` is **unbounded in memory**.
- **State**: in-memory only — build IDs, 60-minute TTL, 1000-build cap, "latest
  finished". Nothing survives a restart; no watchers, no sessions.
- **Execution**: foreground blocks with progress notifications; background returns
  a build ID; `wait_build` supports regex/task/finish conditions; stop by ID.
  Foreground has **no server-side timeout**.
- **Daemon/caches**: pure pass-through. It never forces `--no-daemon`, never
  enables or disables the configuration cache, and never touches the build cache.
  Correct for a dev runtime, and a principle we should copy.

## Tool surface (10 tools)

`gradle` (run/stop, foreground/background), `query_build` (dashboard, console,
tasks, tests, failures, problems), `wait_build`, `kotlin_repl`,
`inspect_dependencies`, `read_dependency_sources`, `search_dependency_sources`,
`lookup_maven_versions`, `gradle_docs`, `install_gradle_skills`.

Transports: **stdio default**, **Streamable HTTP** (port 47813). SSE exists but is
unreachable from the CLI. Packaging: shaded jar on Maven Central, run via **JBang**;
JDK 21+ required.

## Reusable ideas

1. **Semantic model separated from rendering.** `Build`, `TaskResult`, `TestResult`,
   `Failure`, `ProblemAggregation`, `BuildOutcome` — then render to text for MCP and
   to JSON for the web UI. Copy the model, not the renderer.
2. **Build registry with stable IDs** and a lifecycle (TTL + cap). We should
   additionally **persist compact results** so `lastResult` survives restarts.
3. **Outcome taxonomy**: `SUCCESS | FAILED | SKIPPED | UP_TO_DATE | FROM_CACHE |
   NO_SOURCE | CANCELLED | IN_PROGRESS`, with verbatim skip reasons and task
   provenance. This vocabulary is exactly what an agent or UI needs to tell
   "assembled" from "up-to-date".
4. **Problems-first error model**: severity, stable problem id, display name,
   documentation link, occurrence details, contextual locations, potential
   solutions — plus, unlike them, **line and column**.
5. **Failure tree with stable IDs**, so `build.errors` can return a three-item
   "recent error context" instead of megabytes of console.
6. **Three output views**: isolated task output (what you want for a failing
   compile), raw console (forensics), compact summary with pointers (tokens).
7. **Custom-task + init-script pattern** to emit structured metadata as JSON. This
   is the blueprint for our missing `build.tasks` and for structured errors if we
   go the CLI route.
8. **Foreground/background split with explicit stop and wait conditions** — very
   close to what our web UI needs.
9. **Token-conscious output**: tail by default, pagination with explicit
   continuation hints, an escape hatch for huge logs, "next step" hints.
10. **Environment control**: `envSource` (inherit/shell/none), non-inherited system
    properties, explicit `javaHome`. Necessary on macOS, where GUI-launched
    processes miss the shell `PATH`/`JAVA_HOME`.
11. **Daemon hygiene in tests**: pinned `GRADLE_USER_HOME`, pinned `javaHome`, short
    daemon idle timeout, bounded workers. If our tests spawn Gradle, copy this or we
    will orphan daemons.
12. **Tool metadata generated from code and gated in CI** — schemas cannot silently
    diverge from the implementation.
13. **Agent guidance shipped as skills** instead of overloading tool schemas.

## Things to avoid

1. **Do not implement task listing as "run `tasks`".** Build a real structured
   `build.tasks` filtered by group/name/variant.
2. **Do not depend on Gradle internal APIs** for core value (their init script does,
   and their own tests document the breakage under isolated projects).
3. **Do not keep unbounded logs in memory.** Bounded ring buffers + spill to disk +
   byte offsets for streaming.
4. **Do not conflate executed with available tasks**, and do not ship dead
   parameters (they have at least one accepted-and-ignored).
5. **Do not lose error location fidelity** (file, line, column).
6. **Do not rely on console-scanning heuristics** for per-task output as a design.
7. **Do not accept arbitrary host paths** for writes from a web-facing runtime.
8. **Do not allow unbounded concurrent builds per project**; queue and surface
   state.
9. **Do not add a 400-line daemon-JVM precedence engine** unless Android requires
   it. Expose a `javaHome` override and diagnostics; defer the rest to Gradle.
10. **Do not omit a build timeout.** Foreground builds here have none.
11. **Do not treat prose specs as documentation of the code** (their OpenSpec is
    intent, and drifts).

## The decision this forces for our `build.*`

TAPI is a Java API, so we must pick:

| | **A. JVM TAPI sidecar** | **B. `gradlew` CLI + init scripts** |
| --- | --- | --- |
| Structured problems (file/line/col) | Native only where the toolchain reports them (Kotlin: KGP 2.4+ on Gradle 8.13+; javac: 8.13+ for our client); console extraction otherwise | Only via init script / console parsing |
| Structured task listing | Yes (`GradleProject` model) | Only via a custom init-script task |
| Test events | Yes | No |
| Cancellation | Token-based, graceful | Kill the process |
| Progress | Rich events | Parsed from console |
| Cost | A JVM artifact we write and maintain, ~200–400 MB, own lifecycle | Lean; we rebuild task attribution and error extraction |
| Risk | New language/artifact in the repo | Brittle across Gradle/AGP versions |

Both are consistent with our architecture: [ADR-0001](../adr/0001-core-runtime-go.md)
already allows JVM-only boundaries as **sidecars**, and
[process-lifecycle.md](../architecture/process-lifecycle.md) already defines lazy
start, idle shutdown and supervision.

Recommendation: **A**, staged — the sidecar is exactly where our highest-value
outputs live (structured errors, task listing), and the CLI path would force us to
rebuild the two hardest things with brittle techniques. Note the cost honestly: it
is the largest deviation from "lean" so far, and it adds a JVM artifact to the repo.

See [ADR-0017](../adr/0017-gradle-execution-strategy.md) for the recorded decision.

**Correction (2026-09-29).** "Structured problems, native" in the table was
overstated. Kotlin compiler messages reach the Problems API only from KGP 2.4.0, and
AAPT2, manifest-merger and KSP/kapt errors are not known to reach it at all
([gradle-version-compatibility](gradle-version-compatibility.md) §3.1). Error
extraction therefore needs a console extractor under **either** option
([ADR-0022](../adr/0022-build-error-extraction.md)), and it does not differentiate A
from B. The recommendation stands on the other rows: task listing and the project
model, test events, cancellation, progress, daemon reuse, and a model-only probe
that never enters a build
([ADR-0023](../adr/0023-android-project-facts-and-build-outputs.md)). Idea 7 above
(an init script with a custom task) is **not** our blueprint: we use no init script
in builds.

## Legal note

Apache-2.0. Its *model shapes* (outcome taxonomy, failure tree, problems model) are
worth adopting conceptually; code would be ported/rewritten in Go with attribution.
