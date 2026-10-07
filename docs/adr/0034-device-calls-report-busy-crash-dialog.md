# ADR-0034: Device calls report a busy app, an app crash and a system dialog

- Status: Accepted
- Date: 2026-10-07
- Relates to: ADR-0029, ADR-0030 (the reader), ADR-0004 (the agent decides), ADR-0015,
  ADR-0020 (§Adapter projections; its 2026-10-06 line), `ui-automation.md` (§Result,
  §Failure modes, §Open questions), `adb-device.md` (§Logs), plan m1n Q2,
  `docs/research/daily-use-crash-anr-2026-10.md`
- Decided by the main session under the owner's standing instruction (Delegated,
  2026-10-07, plan m1n Q2), from step 2's facts; the owner can revert it.
- Amended: 2026-10-07 (Delegated, plan m1n step 3 review) — Decision 1's package is the
  focused window's (`dumpsys window`, read on the busy path only), not the reader's last
  read, which is stale after a foreground change; when it cannot be read, or is not a
  package name, it is omitted and the error, then carrying no device text, is not marked
  untrusted. The host's warm-read bound is 4 s, above the device's 2.5 s.

## Context

Plan m1n step 2 (API 31, recorded in the plan's §Changes): a read that reaches the reader
while the foreground app's main thread does not answer blocks for the platform's 5 s
accessibility timeout, and the reader's one thread blocks `ping` with it. A read sent
within about 100 ms of a crash blocked 5.1 s; a frozen app made the daemon time the warm
read out at 3 s, restart the reader, time out again, and fail both fallbacks because the
restarted reader still held the device's one `UiAutomation`: `DEVICE_UI_DUMP_FAILED` after
8.6 s. Nothing in a result says the app crashed or that a system dialog is up, though the
tree shows the dialog (root `content`, ids `aerr_close`, `aerr_wait`, `aerr_app_info`) and
the events buffer names it (`am_anr`, `am_crash` with the exception, message, file and
line). `exit-info` gives the reason (`4 (APP CRASH(EXCEPTION))`, `6 (ANR)`).

## Decision

1. **Busy.** The reader bounds each read at 2.5 s on the device and answers `busy`; it
   answers `ping` while a read runs; a read arriving while one worker is blocked starts a
   second, and with two out it answers `busy` at once. The daemon never restarts a reader
   that answers `ping`, and a busy read goes to no fallback reader. A busy read is a
   failed read: before an action, `DEVICE_UI_DUMP_FAILED`, whose cause says the
   foreground app did not answer accessibility queries, with the package in `details`,
   the error marked untrusted (the cause never quotes device output), and whose nextStep
   says to read again in a few seconds; after the action, a failed poll (`failedDumps`),
   as today. No new error code.
2. **Crash.** After a `tapElement` or `inputText`, when the foreground left the app or no
   read observed it, and the app's process is gone, the result carries `appCrash`: from
   the crash buffer since the action began (exception, message, up to five frames),
   filtered by the app's package, else from `exit-info` (the reason). Those reads are paid
   only on that path.
3. **System dialog.** A read whose tree holds the system's error-dialog buttons sets
   `systemDialog {kind: "anr" | "crash", package?}` on `uiTree`, `tapElement` and
   `inputText`, the package from the events buffer. An action whose target is inside the
   dialog proceeds; one whose target is elsewhere, and an `inputText` with no target,
   fails `DEVICE_NOT_READY` before acting, with `details.state: "systemDialog"` and
   `details.systemDialog`, marked untrusted; its nextStep names the dialog's buttons for
   `tapElement`, or waiting. Droidyard never dismisses a dialog itself.
4. The fields are optional and tagged untrusted; the API's minor version moves.

## Consequences

- A frozen or crashing app costs a read at most about 2.5 s and never a reader restart;
  removing the fixed settle (plan m1n D2) cannot put a read into the crash window
  unbounded.
- An agent learns of a crash or ANR from the call that caused it, with the exception, and
  is stopped before tapping blindly behind a dialog.
- A dialog left by an earlier crash blocks actions outside it until the agent closes it.
- Rests on API 31 facts; the API 36.1 rows are unverified (plan m1n Unverified).

## Alternatives considered

- **`am monitor` or a reader `IActivityController`.** Unsafe below API 34, and the single
  controller slot is cleared by `device.launch`'s `monkey`. Rejected.
- **Automatic dismissal and relaunch** (agent-device). Hides the state, and is the
  agent's decision (ADR-0004). Rejected.
- **A new `DEVICE_APP_NOT_RESPONDING` code.** One more code where the cause suffices;
  wins if agents misread the cause.
- **Tapping through a dialog.** The tap lands on the dialog, an effect the agent did not
  ask for. Rejected.
- **One worker answering `busy` until it returns.** No tree for up to 5 s after a crash.
- **A reader-side `getProcessesInErrorState()` poll.** A hidden API; wins if the adb reads
  prove late.
