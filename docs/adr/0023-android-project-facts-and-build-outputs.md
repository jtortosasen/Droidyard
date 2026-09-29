# ADR-0023: Android project facts and build outputs — a model-only probe and `output-metadata.json`

- Status: Accepted
- Date: 2026-09-29
- Relates to: [ADR-0017](0017-gradle-execution-strategy.md),
  [ADR-0018](0018-gradle-version-support-policy.md),
  [ADR-0022](0022-build-error-extraction.md)

## Context

Several Phase 1 capabilities need Android facts that the generic TAPI models
(`GradleProject`, `BuildEnvironment`) do not provide:

- `project.variants`: variant names, build types, flavours;
- `sdk.requirements`: `compileSdk`, `buildToolsVersion`, and the SDK directory AGP
  actually uses (`local.properties` `sdk.dir`);
- the toolchain profile: **AGP and KGP versions**, which decide where compile errors
  come from ([ADR-0022](0022-build-error-extraction.md));
- `device.install` / `device.launch`: the APK and its `applicationId`, after a build.

The build → install handoff had no contract. `adb-device.md` took the application id
from `output-metadata.json`, "then the module build script, then a legacy manifest",
and reading a build script is guessing.

Candidate mechanisms: task-name heuristics, AGP's builder model (Android Studio's sync
protocol), an init-script probe, and parsing build scripts or version catalogs.

## Decision

1. **`project.model` is one TAPI `BuildAction`**: a single configuration pass returns
   the generic model plus the probe's Android facts. The result is cached per
   `(root, build-files fingerprint)`, and it serves `project.modules`,
   `project.variants`, `build.tasks`, the build side of `sdk.requirements`, and the
   AGP/KGP profile.
2. **The probe** is the only init script in the design: `aiodev-probe.init.gradle`,
   plus a small Java jar (Java 8 bytecode, no Kotlin stdlib) compiled against Gradle's
   public API at the floor. The jar registers a `ToolingModelBuilder`. It reads:
   - AGP and KGP versions, reflectively from the plugins' own version constants;
   - `namespace`, `compileSdk`, `buildToolsVersion`, `minSdk` and the SDK directory,
     from AGP's public DSL;
   - variants, through `androidComponents.onVariants`.

   Each field is isolated: a failure makes it `unknown` and never fails the fetch.
3. **The probe is injected into `project.model` only, never into a build.** It
   cannot fail a user's build by construction. It is the subject of ADR-0018's canary.
4. **Fallback for variants: task names**, labelled `source: task-names, exact: false`.
   A variant is an `X` with both `assemble<X>` and `package<X>`, minus names ending in
   `AndroidTest`, `UnitTest` or `TestFixtures`.
5. **Build outputs come from `output-metadata.json` only.** For each module whose
   `package<Variant>` task ended `success`, `upToDate` or `fromCache` in this build,
   the core finds the listing file under `<buildDir>/outputs/apk/` whose `variantName`
   matches. The file gives `applicationId`, `versionCode` and the APKs. The build result
   carries `outputs[] {module, variant, applicationId, versionCode, apks[]}` without
   paths, and `device.install` takes `build: {buildId?, module, variant}`. **No build
   script, catalog or manifest is parsed** anywhere in the system.
6. **MVP output scope: one unfiltered APK per variant.** Multi-APK (ABI/density)
   outputs are listed but not installable, and App Bundles, `androidTest` APKs,
   dynamic-feature and instant apps are out.

Details: [`subsystems/gradle.md`](../subsystems/gradle.md) §Project model and §Build
outputs.

## Consequences

- One init script exists, it is named and owned, and it never touches a build.
  ADR-0018's "keep init scripts minimal" holds, and its canary now has a subject.
- The probe tracks AGP's public DSL, which AGP 9 already reshaped. This is an owned
  risk, mitigated by per-field isolation and a CI canary on AGP floor and latest.
- Variants are exact when the probe works and still usable when it does not. After a
  build, the output's `variantName` confirms them either way.
- `sdk.requirements` can answer before the first build, and it checks the SDK AGP
  actually uses.
- The application id has one owner and one source. The device subsystem drops its
  build-script and manifest fallbacks.
- A `project.model` call costs a configuration pass. It is cached, and it is served
  from the cache while a build holds the root.

## Alternatives considered

- **Task-name heuristic only.** Cheapest, and the reviewer's MVP suggestion. It still
  leaves AGP/KGP versions and `compileSdk` without a source, so a probe is needed
  anyway, and flavours make the names ambiguous. Kept as the fallback.
- **AGP's builder model (v2, the Android Studio sync protocol).** The most complete
  source, including the listing-file location. But it needs the `builder-model` jar
  pinned on the sidecar classpath, Studio-version negotiation and per-variant fetches,
  and it gives no KGP version. Revisit if spike S3 shows DSL access through the probe
  to be brittle.
- **Parsing build scripts, version catalogs or manifests.** Convention plugins,
  `buildSrc`, catalogs and computed values make it guessing. Rejected.
- **Injecting the probe into every build.** It adds a failure mode to the user's
  build for data that the model-only path already provides. Rejected (ADR-0018
  rule 4).
- **A Groovy or Kotlin-DSL-only init script, without a jar.** Script compilation
  changes across Gradle majors (Kotlin 2.2 and Groovy 4 in Gradle 9). A precompiled
  jar against the floor API removes that churn. Rejected.
