# ADR-0042: `device.watchLogs`: `deviceGone` means not listed online; the no-uid-column next step

- Status: Accepted
- Date: 2026-10-10
- Supersedes: ADR-0041 §Decision (in part: item 6's `deviceGone` test and item 2's next step for a device without the uid column)
- Relates to: plan m2a steps 4 and 6
- Delegated: the main session, under the owner's standing instruction to apply the
  recommended option; raised by the Opus verifiers of plan m2a.

## Context

ADR-0041 §6 publishes `stopped: deviceGone` when logcat ends and `device.list` "no longer
has the serial". On the scratch emulator (API 31, plan m2a step 6), `emulator.stop` ended a
stream whose logcat exited while adb still listed the emulator, `offline`, so the stream
reported `exited`. ADR-0041 §2 gives both start failures of the application filter the
next step "install it, then start the stream again"; for a device whose logcat prints no
uid column, installing changes nothing.

## Decision

1. `deviceGone` when logcat ends and `device.list` does not hold the serial **online**:
   absent, or listed in any state but `device`. The published `stopped.reason`
   description says so.
2. A device whose logcat prints no uid column fails `DEVICE_LOGS_FAILED` with the next
   step "start the stream without applicationId and filter in the client". An
   application `pm` does not list keeps ADR-0041's "install it, then start the stream
   again".

## Consequences

- An emulator stopped mid-stream reads as gone, which is what happened; a device that goes
  `offline` and comes back reads as gone too, and the client starts a new stream.
- The rule lives in `internal/domain/device/watchlogs.go` and the schema's description.

## Alternatives considered

- **Keep "listed in any state"**: reports a stopped emulator as `exited (status 255)`,
  which reads as a logcat fault.
- **Wait for the serial to drop off the list before deciding**: a shutdown can stay
  `offline` for seconds, and the stream's end would wait on it with nothing to gain.
