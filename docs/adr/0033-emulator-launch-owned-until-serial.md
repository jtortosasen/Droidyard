# ADR-0033: An emulator launch is droidyard's until its serial is resolved

- Status: Accepted
- Date: 2026-10-07
- Relates to: ADR-0001 §Context (who owns which process), ADR-0021 §Pid records,
  `emulator.md` (§Design rule, §Detached launch, §Stop, §Wipe, §Failure modes,
  §Timeouts), `process-lifecycle.md` §Kinds of processes, §Shutdown, plan m1i D15
- Decided by the main session under the owner's standing instruction (Delegated,
  2026-10-07, plan m1m Q1), after the owner's "Necesito que sea igual de robusto, sí. Y
  rápido. Arréglalo."; the owner can revert it.
- Amended: 2026-10-07 (m1m close review) — Consequences' "at most 45 s" is the serial
  deadline; ending the launch adds up to 10 s (50.0 s observed for a stopped qemu).
- Amended: 2026-10-08 (plan m1p Q1, Delegated) — clause 4's "no pid record" no longer
  holds: the pre-serial window is recorded (ADR-0021's 2026-10-08 line), and a launch an
  earlier daemon left without a serial is ended by the next daemon's reconcile when adb
  proves it is not a device; any doubt leaves it running. The Consequences' limit (a
  daemon dying in the window leaves the launch) narrows to that doubt. Clause 2's
  "from resolution" now starts when that record is removed: if the removal fails, the
  launch is ended as in clause 1 and no serial is returned, the one signal sent to an
  instance whose serial was read.

## Context

`emulator.md` says droidyard boots an emulator but never owns it: a cancelled or
timed-out boot leaves it running, and droidyard never signals it. In the m1l comparison
pilot a windowed launch twice never opened its console (no serial ever listed): the boot
ended `EMULATOR_BOOT_TIMEOUT` after 3 min, and the hung qemu kept the AVD's lock, so
every retry (`headless`, `coldBoot`, `emulator.wipe`) answered `EMULATOR_AVD_LOCKED` and
`emulator.stop` had no serial to name. The agent looped for 10 min with no way out.

Plan m1m step 1 observed, on this host (`~/Library/Caches/droidyard-dev/m1m/spike-*`):
the launch's pid **is** the qemu (the launcher execs; its pgid is its pid under
`Setsid`); it takes `hardware-qemu.ini.lock` 0.5–0.7 s after spawn and is listed by adb
at 0.8–2.6 s in every mode (windowed, headless, cold, wipe; ten boots, ready in
11.7–19.4 s); a headless launch also starts the SDK's `crashpad_handler` in a group of
its own, monitoring the qemu's pid and exiting with it; no `netsimd` or other shared
process was in the group. A `SIGTERM` between the lock and the listing ended the qemu
in 4.0 s, released the AVD, and the next windowed launch booted. The hang was not
reproduced (a `SIGKILL`ed instance, and an AVD recreated while its qemu exited, both
relaunched normally); its cause is not known.

## Decision

1. **From spawn until this daemon resolves the launch's serial** (`emulator.md` §Boot
   sequence 4: a new serial whose `emu avd name` is the AVD), the launch is droidyard's:
   this daemon started it, in a session it created, and has handed no serial to anyone.
   If the boot ends before then (the serial deadline, the boot timeout, a cancel, the
   daemon's shutdown), droidyard ends it: `SIGTERM` to the launch's process group, then
   `SIGKILL` if the group is not empty after 5 s, and it waits until the group is empty
   (not only for the launcher's reaper). A `-wipe-data` launch ended this way may leave
   the wipe partial; the error says to call `emulator.wipe` again.
2. **From resolution it is a device** and the rule of `emulator.md` holds unchanged:
   only `emu kill`, never a signal; a cancelled or timed-out boot leaves it running; it
   outlives the daemon.
3. **Serial deadline: 45 s from spawn** (the slowest observed listing was 2.6 s). Its
   failure is the existing `EMULATOR_BOOT_TIMEOUT`, whose cause names the pid, whether
   it was stopped, and the launcher log's tail, and whose nextStep names the remedies
   (`headless: true`, `coldBoot: true`; for a wipe, call `emulator.wipe` again). The
   180 s boot timeout still bounds readiness.
4. Nothing else changes: no new parameter, result field or error code; no pid record
   (ADR-0021 still excludes emulators); an instance droidyard did not launch, or
   launched in an earlier daemon, is never signalled.

## Consequences

- A hung launch costs at most 45 s and frees its AVD, so a retry (headless, cold) can
  boot; the agent is never left with a lock nothing can release.
- A person who leaves a windowed emulator's first dialog open for more than 45 s before
  its console opens loses that boot.
- A daemon that dies inside the pre-serial window still leaves the launch running
  (no pid record); recorded as a limit.
- Documents changed with it: `emulator.md` (§Design rule, §Detached launch, §Stop,
  §Wipe, §Failure modes, §Timeouts, §Open questions), `process-lifecycle.md` (§Kinds of
  processes, §Shutdown layer 2), `mcp-tool-surface.md` (the `emulator_start` cancelled
  row), the `emulator.start`/`emulator.wipe` descriptions; ADR-0001 is amended.

## Alternatives considered

- **Keep "never signal" and shorten the timeout.** The hung launch keeps the AVD and every
  retry, headless included, is `EMULATOR_AVD_LOCKED`, as in the pilot. Rejected.
- **`emulator.stop {avd}` killing a serial-less instance by pid.** A second name for one
  target, and for an earlier daemon's instance a pid droidyard cannot prove it launched;
  with Decision 1, this daemon's own launch never outlives its boot. Wins if serial-less
  instances of earlier daemons prove common.
- **`-report-console <socket>`.** The console port without adb, at about the moment the
  serial is listed (0.8–2.6 s observed), for a listener per boot. Wins if adb's listing
  proves late or unreliable.
- **A pid record for pre-serial launches**, so crash recovery ends them: covers a daemon
  dying in the window, for an ADR-0021 change. Wins if that crash is observed.
- **`headless` by default, or retrying headless automatically.** Hides the window the
  owner may want, or does something the caller did not ask for. The owner's call.
- **The boundary at the serial's first listing.** A listed, unresolved serial is not yet
  known to be this launch's; the code resolves by `emu avd name`.
