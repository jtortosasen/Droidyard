# ADR-0030: The UI reader stays running on the device

- Status: Accepted
- Date: 2026-10-04
- Relates to: ADR-0029 (it extends it), ADR-0015, ADR-0020 (per-resource keys),
  `docs/subsystems/ui-automation.md`, `docs/research/device-speed-2026-10.md`
- Decided by the owner, 2026-10-04 ("mejor persistente"; no socket authentication; tests
  run through Droidyard). Figures are working defaults until milestone 1f's spike
  measures them.
- Amended: 2026-10-06 — measured and built in milestone 1f
  (`docs/research/spike-persistent-reader-2026-10.md`): warm reads 14 ms median (p95
  460 ms) against 1.4 s one-shot; the reader survives its shell and releases
  `UiAutomation` on its idle exit; Qustodio undisturbed by a flagged registration held
  10 minutes. Decided in the plan (D17, Delegated): reads after an action
  (`tapElement`, `inputText`) use the persistent reader and the one-shot only, never
  `uiautomator dump`. Recovery also finds readers of another digest by process name, not
  only by forward; the instrumented-test integration still lands with the first
  capability that runs tests.
- Amended: 2026-10-07 (Delegated, plan m1n) — [ADR-0034](0034-device-calls-report-busy-crash-dialog.md): a read bounded at 2.5 s answers `busy` and a reader that answers `ping` is never restarted; [ADR-0035](0035-reader-acts-fenced-by-peer-uid.md): the reader also taps, presses keys and sets text, and those requests alone are fenced by the peer's uid (reads stay unauthenticated).

## Context

ADR-0029's one-shot reader costs ~1.3 s per read, most of it a JVM started by
`app_process` and the idle wait; the read itself is ~150 ms. `device.tapElement`
reads the tree several times per action (find, act, poll for the effect), so one-shot
reads would cost seconds per tap. mobile-mcp and agent-device keep a `UiAutomation`
process on the device and reach it over `adb forward`, with reads in tens of
milliseconds.

Android allows one `UiAutomation` per device: while the reader holds it, an
instrumented test (`am instrument`, `connectedAndroidTest`) cannot start.

## Decision

- **The same dex, kept running.** The daemon starts the reader on a device on its first
  read (`nohup app_process …`, so it outlives the adb shell), and every later read, from
  any client, goes to it over `adb forward` to a local socket on the device. The daemon
  owns it; agents, the UI and the CLI never see it. If it is gone (a reboot, a crash, the
  timeout below), the next read starts it again; if `adb forward` is lost (adb restarted),
  the daemon recreates it.
- **It ends itself after 30 minutes without a read**, timed on the device, so it also
  ends while the device is unplugged and releases `UiAutomation`. The daemon stops it
  when the daemon stops. The dex file may stay in `/data/local/tmp`.
- **No authentication on its socket.** Owner's decision: a development tool, a device
  the owner controls, and no app is written to look for this socket.
- **Instrumented tests come first.** Before Droidyard runs instrumented tests on a
  device it takes that device's `UiAutomation` key (ADR-0020's per-resource keys), stops
  the reader, and holds the key for the run; a `device.uiTree` meanwhile queues behind it
  and, past the queue bound, fails naming the test run. Tests started outside Droidyard
  (Android Studio) are not detected: the owner runs them through Droidyard.
- ADR-0029's one-shot run stays as the fallback when the persistent reader cannot start,
  and `uiautomator dump` after it, with their warnings.

## Consequences

- Reads after the first should fall to tens or hundreds of milliseconds (to be measured
  by 1f's spike), which makes `tapElement`'s polling practical.
- A process on the device to manage: start, health, restart, the forward, the device-side
  timeout, the stop at daemon shutdown.
- An unauthenticated local socket on the device and an `adb forward` port on the host,
  accepted by the owner.
- The test-run integration lands with the first capability that runs instrumented tests.
- Documents that change: `ui-automation.md` §The tree, `adb-device.md` §Lifecycle and
  concurrency, and the 1f plan.

## Alternatives considered

- **Keep ADR-0029's one-shot reader.** ~1.3 s per read; seconds per `tapElement`.
  Remains the fallback.
- **Never time out.** Holds `UiAutomation` indefinitely, including on an unplugged
  phone; a test run started later finds it held.
- **A token or a peer-uid check on the socket.** Rejected by the owner as cost without
  a threat worth it for this tool.
- **An installed APK service** (agent-device's instrumentation session). Needs an install
  and a signing key; visible on the device. The pushed dex needs neither.
