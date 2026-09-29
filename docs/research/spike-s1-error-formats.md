# Spike S1, run 1: error formats on the command line (Kotlin, KSP, AAPT2)

First real experiment of the project. It exists because the review
([`../reviews/2026-09-29-pre-implementation-review.md`](../reviews/2026-09-29-pre-implementation-review.md))
flagged that `build.errors` rested on an unverified assumption: that the Problems
API delivers compiler errors with file, line and column.

> **Status: partial.** This is the first of the runs S1 needs
> ([`gradle.md`](../subsystems/gradle.md) §Spikes). It recorded real console formats
> for three producers, on the **command line**, for **one** version combination. It
> did **not** exercise the Tooling API, the Problems API, the S1 matrix or daemon
> reuse. What it shows is below, scoped; what it does not show is listed as not run.

- **Date**: 2026-09-29
- **Subject**: `DopamineControl`, a real multi-module Compose project
  (modules `app`, `core/*`, `feature/*`, plus `build-logic` convention plugins)
- **Versions observed**: Gradle **8.13**, AGP **8.13.2**, KGP **2.0.21**,
  KSP 2.0.21-1.0.27, `targetSdk 36`
- **Environment**: JDK 17 (Zulu), Android SDK present, `./gradlew` with
  `--no-daemon --console=plain`
- **Method**: a copy of the project in a scratch directory, a deliberate error
  introduced, the task run, the raw output captured verbatim. The original project
  was not modified.

## What this run established, and what it did not

| Question | Status |
| --- | --- |
| Kotlin, KSP and AAPT2 console formats, CLI with `--console=plain`, this one cell | **Observed** (below) |
| The same formats as the sidecar reads them: TAPI, colour off, no `--console=plain` | **Not run** |
| Failure-carried problems (`Failure.getProblems()`) | **Not observed** |
| Problems progress events | **Not observed** |
| The failure tree as a TAPI object (only the CLI's rendering of it was seen) | **Not observed** |
| The S1 matrix (KGP 2.3 vs 2.4 × Gradle 8.13 / 9.x × AGP 8.x / 9.x) | **Not run**. KGP 2.0.21 is not one of its cells |
| javac, kapt and manifest-merger formats | **Not observed** |
| Multi-line Kotlin diagnostics and notes | **Not observed** |
| Behaviour with a warm, reused daemon | **Not exercised** (`--no-daemon`) |

## Result 1 — Kotlin compile error

Exact line, as emitted:

```text
e: file:///private/var/folders/.../app/src/main/java/com/example/dopaminecontrol/SpikeBroken.kt:3:42 Unresolved reference 'thisSymbolDoesNotExist'.
```

Grammar: `e: file://<absolute-path>:<line>:<column> <message>`

Notes:

- The path is a **`file://` URI**, not a plain path. A parser must strip the scheme
  (and, by the URI rules, handle percent-encoding; no path with spaces was tested).
- Column is present.
- The one warning observed carried a KSP tag: `w: [ksp] <absolute-path>:<line>:
  <message>`, with a plain path and **no column**. An untagged Kotlin `w:` line was
  not observed.
- The failure as the CLI rendered it carries **no location**:

```text
> Task :app:compileDebugKotlin FAILED

* What went wrong:
Execution failed for task ':app:compileDebugKotlin'.
> A failure occurred while executing org.jetbrains.kotlin.compilerRunner...
   > Compilation error. See log for more details
```

A consumer that reads only this failure text learns that *something* failed and
nothing about where. On this cell, on the command line, **the location appears only in
the compiler's console line.** Whether the TAPI failure tree or the Problems API
carries it here was not observed. KGP's release notes say KGP < 2.4 does not report
compiler messages to the Problems API; that is documentary evidence, not an
observation.

## Result 2 — AAPT2 resource error

Exact lines:

```text
> Task :app:processDebugResources FAILED

* What went wrong:
Execution failed for task ':app:processDebugResources'.
> A failure occurred while executing com.android.build.gradle.internal.res.LinkApplicationAndroidResourcesTask$TaskAction
   > Android resource linking failed
     com.example.dopaminecontrol.app-mergeDebugResources-42:/values/values.xml:210: error: resource string/this_string_does_not_exist (aka com.example.dopaminecontrol:string/this_string_does_not_exist) not found.
     error: failed linking references.
```

Grammar: `<blame-id>:/values/values.xml:<line>: error: <message>`, no column.

- **The location is inside the failure message**, unlike Kotlin's. Here it appears in
  the CLI's `What went wrong` rendering of the failure chain. That the TAPI failure
  node's message carries the same text is an **assumption** (not run).
- **It is not a source path.** It points into AGP's *merged* resources, identified by
  an opaque blame id (`com.example.dopaminecontrol.app-mergeDebugResources-42`), not
  at `app/src/main/res/values/spike_broken.xml`, which is where a human or an agent
  needs to go. The line number is in a file the developer never wrote.
- The message does name the missing resource (`string/this_string_does_not_exist`),
  which is actionable without any path.
- This is a resource-**linking** error. Resource-**compile** errors (malformed XML)
  were not tested and may name source paths; unknown.

Options for the path (the decision is tracked in
[ADR-0022](../adr/0022-build-error-extraction.md) §Known open item):

1. **Resolve the blame id** through AGP's merged-resource metadata. It exists, but it
   is undocumented intermediate state that changes between AGP versions.
2. **Read the merged file** and quote the offending line. Gives the agent the actual
   content and the message, without the original path. Caveat: the blame id is not a
   filesystem path, so finding the merged file also relies on AGP's intermediate
   layout, and whether it survives the failed task on disk was not checked.
3. **Report the virtual path as-is**, labelled as a merged artifact.

Working assumption: 3 always, plus 2 where the merged file can be located.

## Other shapes observed

- Task failure marker: `> Task :app:compileDebugKotlin FAILED`.
- Terminal summary: `BUILD FAILED in 2m 8s` plus
  `137 actionable tasks: 137 executed`.

## Timing (useful for the cold-path budget)

- **Cold** (Gradle 8.13 download + full configuration of a multi-module project +
  task graph + compile to the error): **2m 8s**.
- **"Warm"** (same project, incremental, error in resources instead): **8.8s**. With
  `--no-daemon` this still paid JVM start-up and configuration, so it is probably an
  upper bound for a build on a warm daemon (not measured).
- A `:app:compileDebugKotlin` run on this project schedules **137 tasks**.

So the cold path is dominated by first-run costs, not by our layer. Any latency
budget must separate "first ever build" from "subsequent build".

## Implications for the design

1. **The console extractor is needed.** On this cell (KGP 2.0.21, below the 2.2.0
   that introduced KGP→Problems reporting and the 2.4.0 that added compiler
   *messages*), the only location observed was in the console. This is consistent
   with review finding B7; it does not by itself prove that the Problems API is
   silent here, because that path was not observed.
2. **The extractor needs a small, versioned grammar per producer**, not one regex:
   Kotlin (`e:`, `file://` URI, line + column), KSP-tagged (plain path, line, no
   column), AAPT2 (blame-id path, line, no column, inside the failure message).
3. **AAPT2 needs a decision** about the merged-path problem, above.
4. **The failure text is not a source of Kotlin locations** on this cell; it still
   gives the failed task and the outcome, and it is where AAPT2's detail lives.

## Still to run (S1 run 2 and later)

- The **TAPI path**: the same errors through a minimal TAPI client with colour off,
  capturing the forwarded console, the failure tree, failure-carried problems and
  Problems events. This answers whether the console formats above hold without
  `--console=plain`, and whether the AAPT2 detail arrives in the failure node, the
  console, or both.
- The **matrix** cells, KGP 2.3 vs 2.4 first: whether KGP 2.4 on Gradle 8.13+ puts
  Kotlin locations into the Problems API, and whether they agree with the console.
- **javac**, **kapt** and **manifest-merger** shapes; an AAPT2 **compile** error.
- **Multi-line** Kotlin messages and notes, which the extractor must group.
- A **warm daemon**, for both output stability and the warm-path timing.
