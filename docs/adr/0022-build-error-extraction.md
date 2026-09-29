# ADR-0022: Build error extraction — structured sources plus a core console extractor

- Status: Accepted
- Date: 2026-09-29
- Relates to: [ADR-0017](0017-gradle-execution-strategy.md),
  [ADR-0018](0018-gradle-version-support-policy.md)

## Context

ADR-0017 assumed the Problems API delivers compile errors with file, line and column.
Checked on 2026-09-29, the availability depends on three axes, not one:

- **Kotlin (the project's KGP version).** KGP 2.2 started reporting *its own*
  diagnostics to the Problems API. Only KGP **2.4.0** (July 2026) also writes
  **compiler messages** for Kotlin/JVM
  ([what's new in 2.4.0](https://kotlinlang.org/docs/whatsnew24.html)). Most Android
  projects today are on KGP ≤ 2.3, where a Kotlin compile error exists only as console
  text (`e: file:///…:L:C message`).
- **javac (Gradle version).** Reported to the Problems API since Gradle 8.9.
- **Gradle and the TAPI client.** Problems events reach clients since 8.4, but the
  `Problem` shape the current client exposes dates from 8.12, and its origin
  locations from 8.13. `Failure.getProblems()` (8.12) attaches problems to the failure
  tree. Events are throttled per problem id (`ProblemSummariesEvent`, 8.12), and
  Gradle 9.8's documentation steers integrators toward failure-carried problems.
- **Android tools (AGP).** AAPT2 resource linking, the manifest merger and KSP/kapt
  produce the most common Android-specific failures. No Problems API path for them is
  known; they surface as console text and failure messages.

For a typical project in 2026, then, the headline feature (a Kotlin error with its
location) is available only as text.

## Decision

1. **`build.errors` is assembled in the core from three sources**, each used whenever
   the target provides it:
   - **failure-carried problems** (`Failure.getProblems()`): authoritative for errors
     that failed the build, and not subject to event throttling;
   - **Problems progress events**: the live stream for the UI, and the only source for
     warnings, which never reach a failure;
   - **the console extractor**: everything the toolchain does not report
     structurally.
2. **The console extractor is a first-class component**, not a last resort. It is
   pure Go in the core, and runs over the console stream the sidecar forwards and over
   the text of failure-tree nodes. It recognises Kotlin, javac, kapt, KSP, AAPT2,
   manifest-merger and AGP SDK-signature output. Each format is independent and
   covered by golden tests built from recorded real output (spike S1).
3. **Merge and label.** Records are deduplicated on
   `(severity, file, line, column?, message head)`. The richest record wins
   (failure > event > console) and missing locations are filled from the others.
   Every record carries its `source` and `tool`, and the result reports the toolchain
   profile and the expected source per tool.
4. **Nothing is lost silently.** A failed build with no recognised error returns
   `errors: []`, `unrecognised: true`, the root failure messages and a bounded console
   tail. This is the only raw tail in the result, and it is labelled as such.
5. **Only the 8.12+ `Problem` shape is decoded.** Gradle 8.4–8.11 is covered by the
   extractor rather than by a second decoder for an API the current client no longer
   ships.
6. **"No stdout parsing" is a promise to consumers.** The runtime parses so the agent
   does not, and the parsing is confined to one tested component. Principle 5
   (structure over stdout) holds at the API boundary.

Details: [`subsystems/gradle.md`](../subsystems/gradle.md) §Error model.

## Verified against real output (spike S1)

Spike S1 ran this against `DopamineControl`, a real multi-module Compose project
(Gradle 8.13, AGP 8.13.2, **KGP 2.0.21**). Raw output and method:
[`../research/spike-s1-error-formats.md`](../research/spike-s1-error-formats.md).

- **Kotlin** — `e: file://<absolute-path>:<line>:<column> <message>`. The path is a
  `file://` URI; a parser must strip the scheme and handle percent-encoding.
- **KSP/Kotlin tagged warnings** — `w: [ksp] <path>:<line>: <message>`: **no column**.
- **AAPT2** — `<blame-id>:/values/values.xml:<line>: error: <message>`.
- **The failure tree carries no location.** The task fails with
  "`Compilation error. See log for more details`". A consumer reading only the failure
  tree learns nothing about *where*.

### Known open item: AAPT2 paths are not source paths

The AAPT2 location names AGP's **merged** resources (`app-mergeDebugResources-42`),
not the file the developer wrote. Options, none yet chosen:

1. resolve the blame id through AGP's merged-resource metadata — undocumented
   intermediate state, exactly what ADR-0018 rule 4 warns against;
2. read the merged file at the reported path and quote the line, so the agent gets the
   content and the message even without the original path;
3. report the virtual path as-is, labelled.

A combination of 2 and 3 is the working assumption; it must be settled before
`build.errors` is implemented, because resource-linking failures are the most common
Android-specific breakage.

## Consequences

- On today's typical project (Gradle 8.x/9.x, KGP ≤ 2.3), Kotlin errors come from the
  extractor, and the quality of `build.errors` there is the extractor's quality.
- As projects move to KGP 2.4+ on Gradle 8.13+, Problems records take over
  automatically by precedence. No code change is needed.
- Console formats are an owned, version-sensitive risk (Kotlin's has changed before).
  The fixture matrix carries a KGP axis, and a broken format degrades one tool, never
  the whole result.
- The case for the TAPI sidecar no longer rests on error extraction. The extractor is
  needed under every execution option, so it does not favour the CLI path. See the
  ADR-0017 amendment.
- `mvp.md`'s definition of done reads "zero stdout parsing **by the agent**", and the
  risk table's "degrade to raw tail" becomes this decision.

## Alternatives considered

- **Problems API only, degrade to the raw tail.** Most projects would get no Kotlin
  locations at all, which fails the MVP's definition of done. Rejected.
- **An init script that hooks compiler output or internal logging.** It depends on
  `org.gradle.internal.*`, is loaded into every build, and can fail the user's build.
  That is the exact fragility observed in `gradle-mcp` and forbidden by ADR-0018
  rule 4. Rejected.
- **Machine-readable compiler output through compiler flags.** It means modifying the
  user's build configuration. Rejected.
- **Extractor in the sidecar.** The console already flows through the core, which
  writes the artifact. Pure Go functions are the cheapest thing to fixture-test, and
  the sidecar stays a thin executor. Rejected.
- **Require KGP 2.4+.** We do not dictate a project's toolchain. Rejected.
