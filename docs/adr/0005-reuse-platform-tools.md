# ADR-0005: Use Gradle and ADB directly; no Android CLI dependency

- Status: Accepted
- Date: 2026-09-27
- Amended: 2026-09-29 — the Gradle execution path is settled: a Tooling API sidecar
  ([ADR-0017](0017-gradle-execution-strategy.md)), not the Gradle CLI.

## Context

Earlier drafts proposed depending on Google's Android CLI as the primary
orchestration layer, as it covers `run`, `install`, emulator/SDK management, UI
inspection, screenshots and visual element resolution.

On review we drop it as a dependency:

- It is a partly-distributed binary that cannot be fully inspected; its
  behaviour is a black box.
- It overlaps with tools we already need and control: Gradle for the build and
  ADB/emulator/SDK tools for the device.
- Depending on it introduces version/feature drift against a moving target.

It is important to be precise about coverage, because it is a common
misconception:

- **Gradle builds.** It configures, compiles, tests and assembles APK/AAB. It has
  **no device capabilities**.
- **ADB and the emulator tools act on the device.** Install, launch, uninstall,
  shell, Logcat, screencap, input, UI hierarchy, plus `emulator`, `avdmanager`
  and `sdkmanager` for emulator/SDK management.

So dropping Android CLI does **not** mean Gradle covers everything; it means
**ADB / the emulator become first-class integrations**, not a fallback.

## Decision

- Do **not** depend on Android CLI.
- Build through Gradle. The execution strategy is the **Tooling API sidecar**
  decided in [ADR-0017](0017-gradle-execution-strategy.md); the Gradle CLI is no
  longer the primary path.
- Perform device and runtime operations through **ADB directly** and through the
  emulator/SDK tools (`emulator`, `avdmanager`, `sdkmanager`).
- **Own the pipeline** `build → install → launch → inspect` and model each step's
  result structurally.

## Consequences

- No opaque dependency; full control over structured results and error mapping.
- One less external tool to detect, version and reconcile.
- We must implement orchestration the CLI used to provide: resolving installable
  outputs from the build ([ADR-0023](0023-android-project-facts-and-build-outputs.md)),
  install flags, launch intents, incremental/delta install, and semantic element
  resolution via `uiautomator` / accessibility.
- The ADB/device subsystem becomes central and must be designed carefully
  ([adb-device](../subsystems/adb-device.md), [emulator](../subsystems/emulator.md)).
- Android CLI may still be used later as an optional accelerator if it offers
  something clearly better (e.g. delta install), but never as a dependency.

## Alternatives considered

- **Depend on Android CLI** — opaque binary, overlap, version drift. Rejected.
- **Gradle only** — insufficient: Gradle has no device capabilities.
- **ADB only, no Gradle** — insufficient: we need the real build system.
