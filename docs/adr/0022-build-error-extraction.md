# ADR-0022: Build error extraction — structured sources plus a core console extractor

- Status: Accepted
- Date: 2026-09-29
- Amended: 2026-09-29 — the S1 evidence scoped to what run 1 observed (command line,
  one cell, three producers); golden-test coverage stated as to be recorded; the
  AAPT2 open item given its caveat and a single wording.
- Amended: 2026-10-06 — the AAPT2 open item settled (milestone 1g plan D12 and its
  step-2 change, Delegated): a merged path is always reported verbatim with
  `merged: true`; `excerpt` quotes its line from AGP's merged file; and `resolved
  {file, line, column}` names the developer's file when AGP's blame file
  (`merged_res_blame_folder`, observed on AGP 8.13 in spike S1 run 2) maps the line.
  Either lookup failing omits its field. S1 run 2 also recorded javac structured on
  Gradle 8.13 and Kotlin structured from KGP 2.4 through Problems events.
- Relates to: [ADR-0017](0017-gradle-execution-strategy.md),
  [ADR-0018](0018-gradle-version-support-policy.md)

## Context

ADR-0017 assumed the Problems API delivers compile errors with file, line and column.
Checked on 2026-09-29, the availability depends on three axes, not one:

- **Kotlin (the project's KGP version).** KGP 2.2 started reporting *its own*
  diagnostics to the Problems API. Only KGP **2.4.0** (July 2026) also writes
  **compiler messages** for Kotlin/JVM
  ([what's new in 2.4.0](https://kotlinlang.org/docs/whatsnew24.html)). Most Android
  projects today are presumably on KGP ≤ 2.3 (2.4.0 is two months old; not measured),
  where a Kotlin compile error exists only as console text (`e: file:///…:L:C message`).
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
   manifest-merger and AGP SDK-signature output. Each format is independent and gets
   golden tests built from recorded real output. Those recordings are **to be made**:
   spike S1 run 1 covers Kotlin, KSP and AAPT2 on the command line only; javac, kapt,
   manifest-merger and the TAPI-forwarded console are still to be recorded.
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

## Observed in spike S1, run 1 (command line, one cell)

Run 1 used `OwnerAppA`, a real multi-module Compose project (Gradle 8.13,
AGP 8.13.2, **KGP 2.0.21**, which is outside the S1 matrix), through `./gradlew
--no-daemon --console=plain`. Raw output, method and the full list of what was not
run: [`../research/spike-s1-error-formats.md`](../research/spike-s1-error-formats.md).

- **Kotlin** — `e: file://<absolute-path>:<line>:<column> <message>`. The path is a
  `file://` URI; a parser must strip the scheme and handle percent-encoding.
- **KSP-tagged warning** — `w: [ksp] <path>:<line>: <message>`: **no column**.
- **AAPT2** — `<blame-id>:/values/values.xml:<line>: error: <message>`, **no column**,
  inside the failure message ("Android resource linking failed").
- **For Kotlin, the failure carries no location.** The task fails with
  "`Compilation error. See log for more details`"; on the command line the location
  appeared only in the compiler's console line.

**Not observed:** the TAPI path (console as forwarded, the failure tree as an object),
the Problems API in either form, any matrix cell, a warm daemon, and javac, kapt and
manifest-merger output. Which source is primary per tool therefore rests on the
documentation cited in §Context until S1 runs over TAPI.

### Known open item: AAPT2 paths are not source paths

The AAPT2 location names AGP's **merged** resources (`app-mergeDebugResources-42`),
not the file the developer wrote. The message still names the missing resource, which
is actionable on its own. Options:

1. resolve the blame id through AGP's merged-resource metadata — undocumented
   intermediate state that changes between AGP versions;
2. read the merged file and quote the line, so the agent gets the content and the
   message even without the original path. Locating that file from an opaque blame id
   also depends on AGP's intermediate layout (unverified), so this is best-effort;
3. report the virtual path as-is, labelled as merged.

Working assumption: 3 always, plus 2 where the merged file can be located. The
choice **must be settled before `build.errors` is implemented**, because
resource-linking failures are likely the most common Android-specific breakage (an
assumption, not measured).

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
