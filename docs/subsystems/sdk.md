# Subsystem: sdk

The Android SDK as a host resource: what is installed, what a project requires, and (later)
installing what is missing. Also the source of system images and device profiles for
[`emulator.md`](emulator.md). Parameters and results are the descriptors'
(`internal/domain/sdk`).

## Why this exists

The SDK is not just an emulator concern. A build needs the platform matching the project's
`compileSdk`, plus build-tools and platform-tools; when something is missing, Gradle reports it
in its own way, often deep in a plugin stack. Checking **what the project requires against what
is installed** is therefore one of the highest-value structured answers we can give, and it
needs no download, no licence and no side effect: `sdk.requirements` is the difference between
"Gradle failed with a wall of text" and "platform android-35 is missing". Its build-side inputs
(probe or failure signatures) are defined in [gradle](gradle.md) §SDK requirements; this
subsystem compares them against the project SDK. Until `sdk.install` exists, the next step names
the package and `sdkmanager` for the human.

**`sdk` owns all SDK package management.** No other subsystem calls `sdkmanager`:
`emulator.create` refuses a missing system image and points at `sdk.install`
([emulator](emulator.md)). Resolved: `sdk.install` is post-MVP ([mvp](../roadmap/mvp.md) §Out of
scope); AVD creation lives in `emulator` ("`sdk` owns packages and profiles; `emulator` owns
AVDs"); multiple SDK roots are handled by the host/project split below.

## Two SDK roots, two questions

A machine can hold several SDKs, and "which SDK" has two answers that may differ: the **host
SDK** (global: where do *our* tools come from, used by `adb`, `emulator`, `avdmanager` and
`sdk.status`) and the **project SDK** (workspace: which SDK does *this build* compile against,
used by `sdk.requirements`). AGP resolves its SDK from the project's `local.properties`
`sdk.dir` first, so checking requirements against the host SDK could check a different SDK than
the build uses. Android Studio writes `sdk.dir` on import; the real project used in spike S1
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

Windows is out of scope ([ADR-0002](../adr/0002-unix-first.md)); its default belongs to the
discovery seam when Windows is added. A daemon auto-started by a GUI-launched client has no shell
`ANDROID_HOME` ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md) §8), so step 5 or
the config key usually decides there. If nothing resolves, SDK-dependent capabilities are
reported unavailable and name the variable or key that would fix it; the build subsystem is
unaffected (Gradle finds its own SDK). *Milestone 1h (D11, Delegated):* the root, its source and
the rejected candidates are reported by `sdk.status`, which always answers (`found: false` naming
`ANDROID_HOME`), not yet by `system.health`; `RequiresSDK` gates nothing until `emulator.*`.

### Project SDK

1. **What AGP resolved**, from the probe's `sdkDir`
   ([ADR-0023](../adr/0023-android-project-facts-and-build-outputs.md)): `source: agp`, exact.
2. **Otherwise, AGP's order emulated**: `local.properties` `sdk.dir` (Java properties syntax; a
   relative path is resolved against the project root), then `ANDROID_HOME`, then
   `ANDROID_SDK_ROOT`, from the environment builds run with: `source: local.properties | env`,
   `exact: false`.
3. Nothing: `sdk.requirements` says the build will fail with "SDK location not found" before
   Gradle does.

We read `local.properties`; we never write it. `sdk.requirements` inspects the project SDK and
reports the host SDK beside it with `sameAsHost: false` when they differ: not an error, but the
reason the emulator and the build can disagree about what is installed. A missing platform is
named from the probe's compile target, since configuration does not fail on it (1h D12).

### Reading what is installed

Every installed package directory carries a `package.xml` whose `path` attribute is the package
id (`platforms;android-35`, `build-tools;35.0.0`,
`system-images;android-35;google_apis;arm64-v8a`). Walking those files answers "what is
installed" with **no JVM and no `sdkmanager` call**; `sdkmanager` is needed only for what is
*available* (post-MVP). *Not on every host:* a package unpacked by hand may carry only
`source.properties` (this host's cmdline-tools, 1h); such a package is not listed until a
fallback is decided. Package ids are matched as strings: platform directories may carry a minor
version (`platforms;android-37.0` on a current SDK), so a `compileSdk` is mapped through AGP's
compile-target string from the probe, not rebuilt from an integer. Spike S3
([findings](../research/spike-s3-android-facts.md)) observed that `sdk.dir` wins over `ANDROID_HOME`.

## Provisioning (later phase)

Installing SDK components is a **package-manager concern** with real consequences. **Licences must be
accepted**, never on the user's behalf: the capability reports which are pending and accepts them only
on an explicit call. **Downloads are large and slow** (system images are hundreds of megabytes to
gigabytes): long-running operations with progress and cancellation
([ADR-0020](../adr/0020-operation-contract.md)); sizes are reported before downloading, and what an
uninstall would free; an interrupted download fails and leaves nothing half-installed silently. **The
tools are JVM binaries** (`sdkmanager`, `avdmanager` in `cmdline-tools`) and need a JDK, which an
Android developer already has: the same assumption the Gradle sidecar makes. `avdmanager` builds AVDs
from device definitions shipped in the SDK; we expose them so an emulator can be created from a named
profile (`pixel_7`, a tablet, a TV) rather than from raw numbers.
