# Spike S3, part 1: Android facts from a non-Studio client

Milestone 1h step 1 ([plan](../plans/m1h-android-probe-and-sdk.md)). It answers whether
a plugin applied by an init script can read, through AGP's public DSL and Variant API,
what the probe needs: AGP and KGP versions, exact variants with build type and
flavours, the compile target, the effective build-tools and the SDK directory
([`gradle.md`](../subsystems/gradle.md) §The probe; ADR-0023).

> **Status: parts 1 and 2 done, 2026-10-07, macOS, Zulu 17.** Observed with a scratch Groovy
> init script on the Gradle command line, not over the Tooling API and not with the
> Java probe: Groovy's dynamic dispatch can hide what Java reflection meets, so step 2's
> probe checks the variant registration again (it did: §Part 2).

## Cells

| Cell | Project | Gradle | AGP | Kotlin |
| --- | --- | --- | --- | --- |
| C1 | `sidecar/testdata/flavoured` as pinned | 8.13 | 8.13.2 | KGP 2.0.21 |
| C2 | the fixture, Kotlin plugin lines removed | 9.6.0 | 9.4.1 (newest stable 9.x on 2026-10-07) | AGP 9's built-in Kotlin |
| C3 | a copy of DopamineControl's working tree (convention plugins in `build-logic`) | 8.13 | 8.13.2 | KGP 2.0.21 |
| C4 | the fixture | 8.0.2 (zip SHA-256 `ff7bf6a8…d0d7`, as published) | 8.0.2 | KGP 2.0.21 |

The init script, per Android project: `pluginManager.withPlugin('com.android.application' | 'com.android.library')`,
then `androidComponents.pluginVersion`, `Class.forName('com.android.Version', …, androidComponents.class.classLoader).ANDROID_GRADLE_PLUGIN_VERSION`,
`androidComponents.onVariants(androidComponents.selector().all()) { v -> v.name, v.buildType, v.productFlavors (kotlin.Pair .first/.second), v.flavorName }`,
`androidComponents.sdkComponents.sdkDirectory`, and after evaluation `android.compileSdkVersion`, `android.compileSdk`,
`android.buildToolsVersion` and `KotlinPluginWrapperKt.getKotlinPluginVersion(project)` through `project.buildscript.classLoader`.

## What was observed

| Fact | C1 | C2 | C3 | C4 |
| --- | --- | --- | --- | --- |
| G1 AGP version (both reads agree) | 8.13.2 | 9.4.1 | 8.13.2 | 8.0.2 |
| G2 KGP version | 2.0.21, also from `:app`, which does not apply Kotlin (the plugin is on the build classpath) | 2.2.10, AGP 9's built-in Kotlin | 2.0.21 | 2.0.21 |
| G3 `onVariants` from the init script's plugin | `:app` the 8 variants with build type and both flavour pairs (`paidStagingDebug`: `debug`, `[tier:paid, stage:staging]`); `:lib` `debug`, `release` | same | 16 variants over its 8 Android modules (`debug`, `release` each) | same as C1 |
| G4 compile target | `android-36` (`compileSdk` 36) | `android-36` | `android-36` | `android-36` |
| G4 effective build-tools | `35.0.0` explicit on `:app`; AGP's default on `:lib` `35.0.0` | default `36.0.0` | `35.0.0` | default **`30.0.3`**, not installed |
| G4 SDK directory | `local.properties` `sdk.dir` | same | same | same |
| G5 `android.builder.sdkDownload=false` | accepted | accepted, no warning | accepted | accepted |
| G7 task-name fallback vs G3 | `:app` the same 8 names; `:lib` none (a library has no `package<Variant>`) | same | `:app` `debug`, `release` as G3; the libraries none | same as C1 |
| G9 a jar beside the init script, through `initscript.sourceFile`, and its `droidyard-probe: applied` lifecycle line | yes | yes | not run | yes |

Further, on C1:

- **G4, compile target 37.** `compileSdk 37` reads `android-37`, while the installed
  package's `package.xml` path is `platforms;android-37.0`: a consumer matching targets
  to installed platforms must treat `android-N` as satisfied by `android-N.0`.
- **G4, SDK directory.** `sdk.dir` naming a symlink is returned as written, not
  resolved. With no `local.properties`, `ANDROID_HOME` is used. With `sdk.dir` naming a
  directory that does not exist, AGP falls back to `ANDROID_HOME` when it is set, and
  otherwise reports `<project>/missingSdkDirectory`, a path that does not exist: the
  probe's SDK directory must be checked, never assumed to exist.
- **G6, a missing platform** (`compileSdk 33`, not installed). A configuration pass
  (`help`) **succeeds**, with and without the init script; the probe still reads
  `android-33`. The build fails at task-graph time with `Could not determine the
  dependencies of task ':app:compileFreeProdDebugJavaWithJavac'.` and the cause
  `Failed to find target with hash string 'android-33' in: <sdk>` (recorded in
  `internal/domain/build/testdata/s3/missing-platform.assemble.txt`). So `sdk.requirements`
  can name a missing platform before any build, from the probe's compile target.
- **G8, flavoured outputs.** After `:app:assemblePaidStagingDebug`,
  `outputs/apk/paidStaging/debug/output-metadata.json` (depth 2) names
  `variantName: paidStagingDebug`, `applicationId: dev.droidyard.flavoured.paid.staging`
  and `app-paid-staging-debug.apk`; `packagePaidStagingDebug` ended `success`.
- **The Android SDK.** No package was installed by any cell. AGP rewrote
  `~/Library/Android/sdk/.knownPackages` (a 16-byte hash of the package index) when the
  missing platform was looked up; nothing else under the SDK changed.

## Part 2: the probe over the Tooling API (step 2)

The Java probe and the sidecar at protocol 2, driven directly (one sidecar process, the
three embedded jars, the init script beside the probe jar), on the DopamineControl copy,
`project.model` alternating unprobed and probed, three rounds:

| Round | Unprobed | Probed |
| --- | --- | --- |
| 1 (cold daemon connection) | 2.37 s | 0.78 s |
| 2 | 0.51 s | 0.44 s |
| 3 | 0.44 s | 0.39 s |

- **Cost.** Once the configuration is warm, the probe adds nothing measurable to a fetch
  (both about 0.4–0.5 s); the reply grows from 320 244 to 322 129 bytes.
- **Facts.** `plugins {agp: 8.13.2, kgp: 2.0.21}`, `probe.status: ok`, no notes; each of
  the eight Android modules `android-36`, build-tools `35.0.0`, the SDK directory, two
  variants (`debug`, `release`).
- **Cells over TAPI** (the opt-in suite, `DROIDYARD_GRADLE_TESTS=1 go test ./sidecar`): C1,
  C2 and C4 as in part 1, the eight variants on each, so G3 holds for the Java probe's
  reflection too.

## Not run

Isolated Projects, composite builds,
Linux, AGP 9's newest previews.
