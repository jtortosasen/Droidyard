# Research: device-layer speed against prior art (2026-10-04)

> Status: done. Four repositories read from source by subagents (file:line in their
> reports), plus one timing run on this machine. Estimates are labelled; the timings
> are measured.

## The question

After milestone 1e step 7, is Droidyard's device layer (the `adb` CLI per command,
`uiautomator dump /dev/tty`, `screencap -p`) heavier or slower than the tools we
studied?

## Measured (Pixel_9a, API 36.1, emulator, macOS; 5 runs each, medians, a ~0.6 s
## timer overhead subtracted)

| | Bare `adb` command | Droidyard, CLI end to end |
| --- | --- | --- |
| UI tree | `exec-out uiautomator dump /dev/tty` ≈ 2.0 s | `device.uiTree` ≈ 2.2 s |
| Screenshot | `exec-out screencap -p` ≈ 0.1 s | `device.screenshot` ≈ 0.2 s |
| One adb spawn | `devices -l`, `getprop` ≈ 0.05–0.08 s | — |

Droidyard adds about 0.1–0.2 s (the preamble and the CLI). The tree's cost is
`uiautomator dump` itself: a JVM started on the device and its idle wait.

## What the others do

| Tool | Transport | UI tree | Notes |
| --- | --- | --- | --- |
| codex-android-mcp | `adb` CLI per call | `uiautomator dump /dev/tty` | same path as ours; 800 ms pause before its one idle retry; full-resolution PNG to the model |
| dsh-android | `adb` CLI per call | `uiautomator dump /dev/tty` | same path; claims 0.6–1.5 s per tree on its emulator; full-resolution PNG |
| mobile-mcp (≥ 1.0, via mobilecli) | a per-user daemon plus a **persistent on-device server** (`mobilecli.dex` run with `app_process`, `adb forward`) | `UiAutomation` held warm; 500 ms quiet / 2 s max settle | tree in 10–26 ms over the socket, ~1.1 s warm end to end on a Pixel_9a (their PR #387); input 5–30× faster than `adb shell input`; JPEG q75 at 1024 px |
| tester-army/e2e (via callstackincubator/agent-device) | an **instrumentation APK** (`am instrument`) kept as a session over `adb forward`, one-shot fallback | `UiAutomation.getWindows()`, idle 100 ms quiet / 500 ms max | systemui bars dropped; one line per node without bounds; images at 768 px |

`docs/research/mobile-mcp.md` described mobile-mcp as of 1.0.0 (a per-call process);
that is no longer its default path.

## What follows

- **Not slower than our peers on the same path.** codex-android-mcp and dsh-android
  use the same commands and per-call spawning; we are lighter on tokens (a compact,
  folded tree; images downscaled).
- **The one structural gap is the on-device agent.** Two independent tools converged
  on `UiAutomation` held on the device and reached over `adb forward`, which removes
  the ~2 s dump. It matters little for one read every few seconds (1e); it matters for
  `device.tapElement`, which dumps repeatedly while it polls. Costs: building and
  shipping a dex or APK from a Go repository, one `UiAutomation` per device (it would
  contend with the user's own instrumented tests), a lifecycle to manage, stale-cache
  bugs (mobile-mcp #439). No ADR covers it; it needs a spike and an ADR before the
  device-actions milestone.
- **Cheap, now:** an 800 ms pause before the idle retry, an attempt limit longer than
  uiautomator's own idle wait and no `/sdcard` fallback after an idle failure (our
  10 s per attempt likely hides "could not get idle state"); decode a screenshot once;
  JPEG for inline images and a 768 px edge, both to settle in spike S2.
- **Later:** cache `boot_completed` per transport; speak the adb server's socket
  protocol instead of spawning the client (`adb-device.md` lists both as open);
  `filter`/`maxDepth` on `uiTree`; drop the systemui bars.

> 2026-10-04, after milestone 1e: ADR-0029 now covers the one-shot reader (only a
> persistent server still needs an ADR), and the "cheap, now" items were done in 1e
> (the idle retry in step 10, decode-once in step 8).

## Addendum: `uiautomator dump` suppresses other accessibility services (2026-10-04)

`uiautomator dump` (what `device.uiTree` runs) opens `UiAutomation` without
`FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES`; while it is connected, Android suspends
every other accessibility service (logcat: `Registering UiTestAutomationService …
flags=0x0`). Observed on the owner's app, which depends on its own
`AccessibilityService` (Qustodio, emulator API 36):

| Read | Runs | Qustodio killed |
| --- | --- | --- |
| `uiautomator dump`, app idle for over ~3 min | 2 | **2**: its malfunction check fires ~50 ms after the registration, throws, `FATAL EXCEPTION`, SIGKILL, restarted |
| `uiautomator dump`, app just relaunched | 5 | 0 |
| spike reader: a 5 KB dex run with `app_process`, `UiAutomation` with the flag (`flags=0x1`) | 6 (1 after idle) | 0 |
| the same reader without the flag | 6 (1 after idle) | 0 |
| `device.uiTree` through the shipped reader (milestone 1e step 10), app idle 215 s | 1 | 0 (`flags=0x1`; 38 nodes; 1.8 s through the CLI, ~1.3 s for push, run and removal) |
| `uiautomator dump`, app idle 215 s, the same session | 1 | **1** (3 of 3 overall) |

What is established: `uiautomator dump` can kill an app that depends on its own
accessibility service (twice, with the full chain in logcat), and a flagged reader read
the same screen (45 nodes) in ~150 ms, against ~2 s for the dump, without disturbing it.
What is not: that the flag alone prevents the kill — the unflagged reader, which holds
the connection ~150 ms instead of ~2 s, did not trigger it either. The flag is the
documented way to keep other services running (Appium's
`disableSuppressAccessibilityService`; mobium PR #101 ships such a dex for the same
reason); the app's own condition (`DetectAccessibilityMalfunctionUseCase`) settles the
rest.

Consequence: a dex reader with the flag is now justified by correctness as well as
speed, and it is the first item of the on-device-agent spike: a one-shot reader (like
mobium's) before any persistent server.

> 2026-10-04, agent-device read at source (`callstack/agent-device` 0.21.20, the engine
> under tester-army/e2e, moved from `callstackincubator`): its instrumentation APK opens
> `UiAutomation` with `getUiAutomation()`, no flags (`SnapshotInstrumentation.java:360`),
> so it suppresses other accessibility services, and in its persistent session
> (`-e sessionPort`) for the session's whole life. Taps go through `adb shell input tap`;
> text through an IME of its own, installed and selected (`android/ime-helper`), because
> `input text` is ASCII-only — an option for `device.inputText` that 1f's plan weighs.
