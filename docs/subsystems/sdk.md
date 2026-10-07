# Subsystem: sdk

The Android SDK as a host resource: what is installed, what a project requires, and
(later) installing what is missing. Also the source of system images and device
profiles for [`emulator.md`](emulator.md).

> Status: draft. Read-only capability is recommended early because it turns a
> confusing Gradle failure into a structured, actionable one.

## Why this exists

The SDK is not just an emulator concern. A build needs the platform matching the
project's `compileSdk`, plus build-tools and platform-tools. When something is
missing, Gradle reports it in its own way, often deep in a plugin stack.

Checking **what the project requires against what is installed** is therefore one of
the highest-value structured answers we can give — and it needs no download, no
license and no side effect.

## Two SDK roots, two questions

A machine can hold several SDKs, and "which SDK" has two answers that may differ:

| Root | Question | Scope | Used by |
| --- | --- | --- | --- |
| **Host SDK** | Where do *our* tools come from? | global | `adb`, `emulator`, `avdmanager`, `sdk.status` |
| **Project SDK** | Which SDK does *this build* compile against? | workspace | `sdk.requirements` |

AGP resolves its SDK from the project's `local.properties` `sdk.dir` first, so
checking requirements against the host SDK could check a different SDK than the build
uses. Android Studio writes `sdk.dir` on import; the real project used in spike S1
(`DopamineControl`) has one.

### Host SDK

The first candidate that exists and looks like an SDK (it contains at least one of
`platform-tools/`, `platforms/`, `emulator/`, `cmdline-tools/`) wins:

1. The configured path (`sdk.root`).
2. `ANDROID_HOME` from the effective environment.
3. `ANDROID_SDK_ROOT` (deprecated). If both are set and differ, `ANDROID_HOME` wins and
   `system.health` reports the inconsistency, as AGP does.
4. Derived from `adb` on `PATH` (`<sdk>/platform-tools/adb`, symlinks resolved).
5. The per-OS default: macOS `~/Library/Android/sdk`, Linux `~/Android/Sdk`.

Windows is out of scope ([ADR-0002](../adr/0002-unix-first.md)); its default belongs
to the discovery seam when Windows is added.

A daemon auto-started by a GUI-launched client has no shell `ANDROID_HOME`
([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md) §8), so step 5 or the
config key usually decides there. The root, its source and every rejected candidate
are reported in `system.health`. If nothing resolves, SDK-dependent capabilities are
reported unavailable and name the variable or key that would fix it; the build
subsystem is unaffected (Gradle finds its own SDK).

### Project SDK

1. **What AGP actually resolved**, from the probe's `sdkDir`
   ([ADR-0023](../adr/0023-android-project-facts-and-build-outputs.md)):
   `source: agp`, exact.
2. **Otherwise, AGP's order emulated**: `local.properties` `sdk.dir` (Java properties
   syntax; a relative path is resolved against the project root), then `ANDROID_HOME`,
   then `ANDROID_SDK_ROOT`, from the environment builds run with. `source:
   local.properties | env`, `exact: false`.
3. Nothing → `sdk.requirements` says the build will fail with "SDK location not found"
   before Gradle does.

We read `local.properties`; we never write it. `sdk.requirements` inspects the project
SDK, and reports the host SDK beside it with `sameAsHost: false` when they differ —
not an error, but the reason the emulator and the build can disagree about what is
installed.

### Reading what is installed

Every installed package directory carries a `package.xml` whose `path` attribute is
the package id (`platforms;android-35`, `build-tools;35.0.0`,
`system-images;android-35;google_apis;arm64-v8a`). Walking those files answers
"what is installed" with **no JVM and no `sdkmanager` call**; `sdkmanager` is needed
only for what is *available* (post-MVP). Package ids are matched as strings: platform
directories may carry a minor version (`platforms;android-37.0` on a current SDK), so a
`compileSdk` is mapped through AGP's compile-target string from the probe, not rebuilt
from an integer. (Layout checked against a local SDK, 2026-09-29.)

## What we model

```text
Sdk
├── root, source         host SDK (see above)
├── installed            from package.xml files
│   ├── platforms        android-34, android-35, …
│   ├── buildTools       35.0.0, …
│   ├── platformTools    adb, fastboot + version
│   ├── cmdlineTools     sdkmanager, avdmanager + version
│   └── systemImages     system-images;android-35;google_apis;arm64-v8a, …
├── licenses             which licence texts are already accepted
└── deviceProfiles       the definitions avdmanager can build an AVD from
```

## Capabilities

| Capability | Phase | Scope | Notes |
| --- | --- | --- | --- |
| `sdk.status` | MVP | global | Host SDK: root and source, tool versions, what is installed |
| `sdk.requirements` | MVP | workspace | **What this project needs vs. what its SDK has**, with the missing list |
| `sdk.packages` | post-MVP | global | Available and installed packages, with sizes |
| `sdk.install` | post-MVP | global | Explicit-consent install; long-running operation with progress |
| `sdk.licenses` | post-MVP | global | Which licences are accepted, and accept them on explicit request |

`sdk.requirements` is the one to build first: it is cheap, side-effect free, and it is
the difference between "Gradle failed with a wall of text" and "platform android-35 is
missing". Its build-side inputs (probe or failure signatures) are defined in
[gradle](gradle.md) §SDK requirements; this subsystem compares them against the project
SDK. Until `sdk.install` exists, the next step names the package and `sdkmanager` for
the human.

**`sdk` owns all SDK package management.** No other subsystem calls `sdkmanager`:
`emulator.create` refuses a missing system image and points at `sdk.install`
([emulator](emulator.md)).

## Provisioning (later phase)

Installing SDK components is a **package-manager concern** with real consequences:

- **Licences must be accepted.** We never accept them on the user's behalf: the
  capability reports which are pending and accepts them only on an explicit call.
- **Downloads are large and slow.** System images are hundreds of megabytes to
  gigabytes. These are long-running operations with progress and cancellation
  ([ADR-0020](../adr/0020-operation-contract.md)).
- **Disk is finite.** Report sizes before downloading, and report what an uninstall
  would free.
- **The tools are JVM binaries** (`sdkmanager`, `avdmanager` live in
  `cmdline-tools`). They need a JDK, which an Android developer already has — the
  same assumption the Gradle sidecar makes.

## Device profiles

`avdmanager` builds AVDs from device definitions shipped in the SDK. We expose them
so an emulator can be created from a named profile (`pixel_7`, a tablet, a TV)
rather than from raw numbers, which is what makes creation approachable.

## Failure modes

| Failure | Behaviour |
| --- | --- |
| Host SDK not found | Capabilities that need it report unavailable, naming the env var / config key that would fix it |
| Project SDK not resolvable | `sdk.requirements` reports it up front: the build will fail to locate the SDK |
| Project SDK ≠ host SDK | Reported (`sameAsHost: false`), not an error |
| `cmdline-tools` missing | Capabilities that need it say so; the rest of the SDK is still reported |
| Platform required by the project is missing | `sdk.requirements` names it; the build error is then explainable |
| Licence not accepted | Install fails with a clear message pointing at `sdk.licenses` |
| Download interrupted | The operation fails; nothing is half-installed silently |
| Disk full | Report the required size and the available space |

## To verify

*Answered by spike S3 (2026-10-07, [findings](../research/spike-s3-android-facts.md)):
`sdk.dir` wins over `ANDROID_HOME`; a symlinked `sdk.dir` comes back unresolved; a
`sdk.dir` naming nothing falls back to `ANDROID_HOME` or to a non-existent placeholder;
`compileSdk 37` reads `android-37` against the installed `platforms;android-37.0`.
`ANDROID_HOME` against `ANDROID_SDK_ROOT` disagreeing was not run. Milestone 1h
(plan D11, D12, Delegated) ships `sdk.status` (always answers, `found: false` with the
candidates when there is none; no `system.health` SDK block) and `sdk.requirements` (the
probe's facts against the project SDK, whose root it publishes as the `sdkmanager
--sdk_root` target; a missing platform is named from the probe's compile target, since
configuration does not fail on it). `RequiresSDK` stays unused until `emulator.*`.*

Cheap to confirm on a machine with the SDK and JDK 17, before the fallback is relied on:

- AGP's actual order and conflict handling: a project with `sdk.dir` and a different
  `ANDROID_HOME`, then with `ANDROID_HOME` and `ANDROID_SDK_ROOT` disagreeing.
- That the probe's SDK directory matches `sdk.dir` in both cases.
- The compile-target string AGP reports for a minor-versioned `compileSdk`, against the
  `package.xml` path of the installed platform.

## Open questions

- How to present licence acceptance to an *agent* consumer — a human UI can show a
  dialog; an agent needs an explicit, auditable step. Candidate answer: **elicitation**
  when the client supports it, with an explicit capability call as the fallback (see
  [`interfaces/mcp-features.md`](../interfaces/mcp-features.md)).
- Whether to surface `sdkmanager --list` output verbatim or model it, for `sdk.packages`
  (the output is a formatted table, and parsing it is version-sensitive). What is
  *installed* is already answered by `package.xml`.

Resolved: `sdk.install` is post-MVP ([mvp](../roadmap/mvp.md) §Out of scope); AVD
creation lives in `emulator` ("`sdk` owns packages and profiles; `emulator` owns
AVDs"); multiple SDK roots are handled by the host/project split above.
