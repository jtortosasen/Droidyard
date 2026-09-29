# Spike S1: real error formats (Kotlin and AAPT2)

First real experiment of the project. It exists because the review
([`../reviews/2026-09-29-pre-implementation-review.md`](../reviews/2026-09-29-pre-implementation-review.md))
flagged that `build.errors` rested on an unverified assumption: that the Problems
API delivers compiler errors with file, line and column.

- **Date**: 2026-09-29
- **Subject**: `DopamineControl`, a real multi-module Compose project
  (modules `app`, `core/*`, `feature/*`, plus `build-logic` convention plugins)
- **Versions observed**: Gradle **8.13**, AGP **8.13.2**, KGP **2.0.21**,
  KSP 2.0.21-1.0.27, `targetSdk 36`
- **Environment**: JDK 17 (Zulu), Android SDK present, `--no-daemon`,
  `--console=plain`
- **Method**: a copy of the project in a scratch directory, a deliberate error
  introduced, the task run, the raw output captured verbatim. The original project
  was not modified.

## Result 1 — Kotlin compile error

Exact line, as emitted:

```text
e: file:///private/var/folders/.../app/src/main/java/com/example/dopaminecontrol/SpikeBroken.kt:3:42 Unresolved reference 'thisSymbolDoesNotExist'.
```

Grammar: `e: file://<absolute-path>:<line>:<column> <message>`

Notes:

- The path is a **`file://` URI**, not a plain path. A parser must strip the scheme
  (and handle percent-encoding for paths containing spaces).
- Column is present.
- Kotlin warnings use a different prefix (`w:`) and, with KSP, a tag:
  `w: [ksp] <absolute-path>:<line>: <message>` — note this one has **no column**.
- The task failure itself carries **no location**:

```text
> Task :app:compileDebugKotlin FAILED

* What went wrong:
Execution failed for task ':app:compileDebugKotlin'.
> A failure occurred while executing org.jetbrains.kotlin.compilerRunner...
   > Compilation error. See log for more details
```

That last line is the important one: a consumer that reads only the failure tree
learns that *something* failed and nothing about where. **The location lives only in
the console stream** on this project.

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

Grammar: `<blame-id>:/values/values.xml:<line>: error: <message>`

**The finding that matters: that is not a source path.** It points into AGP's
*merged* resources, identified by an opaque blame id
(`com.example.dopaminecontrol.app-mergeDebugResources-42`). It does not name
`app/src/main/res/values/spike_broken.xml`, which is where a human or an agent needs
to go. AAPT2 errors therefore give a line number in a file the developer never
wrote.

Options we now have to choose between:

1. **Resolve the blame id** through AGP's merged-resource metadata. It exists, but it
   is undocumented intermediate state that changes between AGP versions — the kind
   of dependency [ADR-0018](../adr/0018-gradle-version-support-policy.md) warns
   against.
2. **Read the merged file** at the reported path and quote the offending line. Gives
   the agent the actual content and the message, without the original path.
3. **Report the virtual path as-is**, with a note explaining what it is.

A combination of 2 and 3 is probably right: quote the line, be explicit that the path
is a merged artifact, and do not pretend it is a source file.

## Other shapes observed

- A KSP/Room warning: `w: [ksp] <path>:9: <message>` — line but no column.
- Task failure marker: `> Task :app:compileDebugKotlin FAILED`.
- Terminal summary: `BUILD FAILED in 2m 8s` plus
  `137 actionable tasks: 137 executed`.

## Timing (useful for the cold-path budget)

- **Cold** (Gradle 8.13 download + full configuration of a multi-module project +
  task graph + compile to the error): **2m 8s**.
- **Warm** (same project, incremental, error in resources instead): **8.8s**.
- A `:app:compileDebugKotlin` run on this project schedules **137 tasks**.

So the cold path is dominated by first-run costs, not by our layer. Any latency
budget must separate "first ever build" from "subsequent build".

## Implications for the design

1. **The console extractor is the primary path, not a fallback.** With KGP 2.0.21 —
   below the 2.2.0 that introduced KGP→Problems reporting, and far below the 2.4.0
   that added compiler *messages* — the structured location exists only in the
   console. This confirms the review's B7 empirically, on a real project.
2. **The extractor needs a small, versioned grammar per producer**, not one regex:
   Kotlin (`e:`/`w:`, `file://` URI, line + column), KSP/Kotlin-with-tag (line, no
   column), AAPT2 (blame-id path, line, no column), javac (not yet observed).
3. **AAPT2 needs a decision** about the merged-path problem, above.
4. **The failure tree stays useful for other things** — which task failed, and the
   build outcome — but it is not a source of locations.

## What is still not tested

- What the **Problems API** actually delivers on this project (needs a TAPI probe;
  the release notes say KGP 2.0.21 does not report compiler messages, but we have not
  observed the TAPI event stream directly).
- **javac** and **manifest-merger** error shapes.
- A **KGP 2.4+** project, where the Problems API should carry the compiler messages —
  and whether it then agrees with the console.
- **Multi-line** Kotlin messages and notes, which the extractor must group.
- Whether Gradle's `--console=plain` is required for stable output, or whether the
  rich console is also parseable.
