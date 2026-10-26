# Subsystem: sdk

The Android SDK as a host resource: what is installed, what a project requires, and installing
what is missing. Also the source of system images and device profiles for
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
subsystem compares them against the project SDK. The next step names `sdk.install` when the project
SDK is the host SDK, and the `sdkmanager` command for another SDK.

**`sdk` owns all SDK package management** and the one runner of `avdmanager`: `emulator.create`
refuses a missing system image and points at `sdk.install` ([emulator](emulator.md)). AVD creation
lives in `emulator` ("`sdk` owns packages and profiles; `emulator` owns AVDs"); multiple SDK roots
are handled by the host/project split below.

## Two SDK roots, two questions

A machine can hold several SDKs, and "which SDK" has two answers that may differ: the **host
SDK** (global: where do *our* tools come from, used by `adb`, `emulator`, `avdmanager` and
`sdk.status`) and the **project SDK** (workspace: which SDK does *this build* compile against,
used by `sdk.requirements`). AGP resolves its SDK from the project's `local.properties`
`sdk.dir` first, so checking requirements against the host SDK could check a different SDK than
the build uses. Android Studio writes `sdk.dir` on import; the real project used in spike S1
(`OwnerAppA`) has one.

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
installed" with **no JVM and no `sdkmanager` call**. A package unpacked by hand may carry only
`source.properties` (this host's cmdline-tools, 1h): it is read as the fallback, its id taken from
its place in the layout. A package's path is its id with `;` as `/`, so every id the repository
offers is looked for there, not only in the fixed locations; a directory holding neither file is
an incomplete install and is said so. Package ids are matched as strings: platform directories may
carry a minor version (`platforms;android-37.0`), so a `compileSdk` is mapped through AGP's
compile-target string from the probe, not rebuilt from an integer. Spike S3
([findings](../research/spike-s3-android-facts.md)) observed that `sdk.dir` wins over `ANDROID_HOME`.

## Provisioning

Droidyard installs, updates and removes packages itself
([ADR-0129](../adr/0129-sdk-provisioning-by-droidyard.md),
[ADR-0130](../adr/0130-droidyard-installs-sdk-packages.md)): today's `sdkmanager` is Android CLI and
accepts licences with nobody's answer ([spike](../research/spike-sdk-provisioning-2026-10.md)). Go
reads Google's repository XML, checks each archive's sha1 while streaming, unpacks it under `.temp/`
and renames it into place last with the `package.xml` sdklib, AGP and `avdmanager` read; a kill
leaves nothing at a package's path.

- **Consent is a person's.** Only a human call carrying sdklib's hash of a text the repository
  serves now writes `licenses/<id>`; an agent meets an error naming that step.
- **Nothing in use is replaced.** An update or uninstall is refused while a process runs from the
  package, a process that runs SDK code holds a file there open (`lsof`), or a Droidyard boot or
  build needs it; any doubt refuses ([ADR-0131](../adr/0131-sdk-provisioning-narrowings.md)).
- **Network and space.** One fixed HTTPS host, every read bounded, kept an hour. An image unpacks to
  about 6.6 times its archive, so space is checked again from each zip's unpacked total.

Device profiles come from `avdmanager list device`, so an AVD is created from a named profile.
