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

## SDK root resolution

```text
ANDROID_HOME / ANDROID_SDK_ROOT
  → per-OS defaults (~/Library/Android/sdk, ~/Android/Sdk, %LOCALAPPDATA%\Android\Sdk)
```

The resolved root is reported in `system.capabilities`, together with what was found
in it. If nothing resolves, the SDK-dependent capabilities report `degraded` and say
which environment variable or path would fix it — the build and device subsystems
keep working if their own tools are reachable.

## What we model

```text
Sdk
├── root
├── installed
│   ├── platforms        android-34, android-35, …
│   ├── buildTools       35.0.0, …
│   ├── platformTools    adb, fastboot + version
│   ├── cmdlineTools     sdkmanager, avdmanager + version
│   └── systemImages     system-images;android-35;google_apis;arm64-v8a, …
├── licenses             which licence texts are already accepted
└── deviceProfiles       the definitions avdmanager can build an AVD from
```

## Capabilities

| Capability | Phase | Notes |
| --- | --- | --- |
| `sdk.status` | early | Root, versions of the tools, what is installed |
| `sdk.requirements(project)` | early | **What this project needs vs. what is installed**, with the missing list |
| `sdk.packages` | later | Available and installed packages, with sizes |
| `sdk.install` | later | Explicit-consent install; long-running operation with progress |
| `sdk.licenses` | later | Which licences are accepted, and accept them on explicit request |

`sdk.requirements` is the one to build first: it is cheap, side-effect free, and it is
the difference between "Gradle failed with a wall of text" and "platform android-35 is
missing; install it with `sdk.install`".

## Provisioning (later phase)

Installing SDK components is a **package-manager concern** with real consequences:

- **Licences must be accepted.** We never accept them on the user's behalf: the
  capability reports which are pending and accepts them only on an explicit call.
- **Downloads are large and slow.** System images are hundreds of megabytes to
  gigabytes. These are long-running operations with progress and cancellation
  ([capability-api](../architecture/capability-api.md)), not blocking calls.
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
| SDK root not found | Report `degraded` and name the env var / path that would fix it |
| `cmdline-tools` missing | Capabilities that need it say so; the rest of the SDK is still reported |
| Platform required by the project is missing | `sdk.requirements` names it; the build error is then explainable |
| Licence not accepted | Install fails with a clear message pointing at `sdk.licenses` |
| Download interrupted | The operation fails; nothing is half-installed silently |
| Disk full | Report the required size and the available space |

## Open questions

- Whether `sdk.install` belongs in the MVP or immediately after it. It is valuable
  but large, and the golden path only needs an emulator that already exists or can be
  started.
- How to present licence acceptance to an *agent* consumer — a human UI can show a
  dialog; an agent needs an explicit, auditable step. Candidate answer: **elicitation**
  when the client supports it, with an explicit capability call as the fallback (see
  [`interfaces/mcp-features.md`](../interfaces/mcp-features.md)).
- Whether to surface `sdkmanager --list` output verbatim or model it (the output is
  a formatted table, and parsing it is version-sensitive).
- Whether AVD creation should live here or in `emulator` — the boundary is
  "`sdk` owns packages and profiles; `emulator` owns AVDs".
- How to handle multiple SDK roots on one machine.
