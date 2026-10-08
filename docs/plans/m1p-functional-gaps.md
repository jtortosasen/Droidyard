# Plan: Milestone 1p — the functional gaps of Phase 1, and three ideas from earlier plans

- Status: Approved
- Approved: 2026-10-07, Delegated (owner's standing instruction to apply the recommended option): Q1 and Q2 accepted as drafted; their `Amended:` lines are written by the main session before steps 6 and 8. The owner can revert either.
- Date: 2026-10-07
- Milestone: not in `docs/README.md`. The owner's brief of 2026-10-07 (see `m1n-daily-use-core.md`'s header), items G (the functional gaps among the "in part" acceptance items, project functionality only) and H (a pid record for a pre-serial emulator launch, `shell.exec` with `cwd` and `env`, a revision token on `policy.update`). It depends on neither `m1n-daily-use-core.md` nor `m1o-tokens-and-harness.md` except for the register (`docs/roadmap/known-limits.md`, written at m1n's close), and runs after them in the brief's order.
- Phase: `docs/roadmap/mvp.md` §Phase 1 (accepted 2026-10-07; this closes items its milestones left in part).
- Contract: ADR-0018 (rule 3, the below-floor hatch); ADR-0020 §Core primitives (cancel); ADR-0021 §Pid records, ADR-0033 §Decision (both amended by Q1); ADR-0032 §5 (amended by Q2); ADR-0015 §4; ADR-0030 §Decision (the forward is recreated); `docs/subsystems/gradle.md` §The probe, §Version detection and toolchain profile; `docs/subsystems/adb-device.md` §Logs, §Policy and safety, §Lifecycle and concurrency; `docs/architecture/process-lifecycle.md` §Crash recovery; `docs/architecture/capability-api.md` §Structured vs raw output (`shell.exec`); plans m1g A4, m1i A1, m1e A3, m1f A2, m1k A7 and D3.

**The owner should see:** Q1 (a pre-serial emulator launch gets a pid record, reversing ADR-0021's "devices and emulators are not recorded" for that window); Q2 (a new error code, `POLICY_REVISION_CONFLICT`); step 5 **asks the owner before the Samsung (`R5CX62JXR3L`) is used**, and closes without it; step 1 downloads AGP 7.4.2 into `~/.gradle` and must leave the SDK untouched.

## Scope

| Brief item (2026-10-07) | Here? | If not, where and why |
| --- | --- | --- |
| G: a real build cancelled through the daemon, including before `{accepted}` (m1g A4) | yes | step 2 |
| G: `device.logs` on API 36.1, the owner's `Pixel_9a`, reading logs only (m1i A1) | yes | step 4 |
| G: physical devices hidden by default, with the owner's Samsung after asking (m1e A3) | yes | step 5; without the owner's yes it closes adding nothing |
| G: the reader's forward recreated after an adb server restart, on a scratch port (m1f A2) | yes | step 3 |
| G: Gradle 7.x: `project.*`, `build.tasks`, `build.assemble` and the probe under the hatch (m1k A7) | yes | step 1 |
| H: a pid record for a pre-serial emulator launch (ADR-0033 alternative, amends ADR-0021) | yes | step 6 (Q1) |
| H: `shell.exec` with `cwd` and `env` | yes | step 7 (D1) |
| H: a revision token on `policy.update` (m1k D3) | yes | step 8 (Q2) |
| The gaps that are missing evidence files only (m1m A2, A3, A5; m1e A7) | no | the brief excludes them |

## Starting point

Expected: the code at `m1o-tokens-and-harness.md`'s close; the four commands recorded at step 1. Facts at `3abf127`: the hatch drove a real Gradle 7.6.4 `build.run` (m1k step 7, `~/Library/Caches/droidyard-dev/m1k/plain7`, a copy of the JVM-only `sidecar/testdata/plain`); no Android fixture below Gradle 8 exists. A build cancel before the sidecar's `{accepted}` is proven only against the fake sidecar (m1g A4). The reader's forward is recreated when removed by hand (m1f step 10), never after an adb server restart. `device.logs`'s uid column is recorded on API 31 only (m1i A1). Physical hiding is proven on synthetic `usb:` lines (m1e A3; m1k step 5). `internal/domain/emulator/launch.go`'s launch is ended on every pre-resolution return (ADR-0033) but has no pid record, so a daemon killed in that window leaves it. `shell.exec` runs `/bin/sh -c` in the root with the daemon's environment. `policy.update` applies RFC 7386 under the lock; arrays are replaced whole (m1k D3).

## Run rules

- K1. `B=~/Library/Caches/droidyard-dev/m1p`; the scratch AVD `droidyard_m1p` (API 31 `google_apis` arm64) is the only AVD booted, signalled or stopped, except step 4's `Pixel_9a` (K3).
- K2. m1n's K2 (the owner's constraints) and K3 (isolated daemons behind the guard; the 5037 pid, owner AVDs and the SDK stamp unchanged).
- K3. `Pixel_9a` (step 4): if it is not running, this run starts it with `-no-snapshot-save` and stops it with `emu kill` (m1e's precedent); only then is it exempt from m1n K3's mtime invariant, and the step says so. An owner AVD's `userdata-qemu.img*` mtimes change while it runs, so `Pixel_9a` is exempt from that invariant either way and the guard log stands in for it. Its serial is in `protected` before the daemon starts, and the daemon runs behind a guard variant logging every invocation and admitting exactly what the two capabilities issue: `version`; `devices -l`; `-s S shell getprop` and `shell getprop sys.boot_completed`; `-s S emu avd name`; `-s S shell pm list packages -U <pkg>`; `-s S shell pidof <pkg>`; `-s S logcat -d -v threadtime,epoch,usec,uid -b main -t 5`; and `-s S logcat -d -v threadtime,epoch,usec,uid -b <main,system,crash subset> [-T <epoch>]`, each matched whole (no `-c`, no `-G`). The main session's manual reads, `logcat -d -v threadtime,epoch,usec,uid -t 50` and `logcat -g`, are named and run directly, logged. `refused.log` empty afterwards. No install, uninstall, wipe, UI read or dump.
- K4. The Samsung `R5CX62JXR3L` (step 5): used only after the owner's explicit yes, recorded under Changes with its date; then only `device.list` and one call the policy refuses, behind a guard that logs every invocation; nothing installed or launched, and no read beyond `device.list`'s `getprop`.

## Delegated decisions

- D1. **`shell.exec`'s `cwd` and `env`.** `cwd`: a path relative to the root, resolved by `internal/domain/file`'s resolver, exported for this (canonical, symlinks resolved, inside the root, a directory), default the root; outside or missing → `REQUEST_INVALID_PARAMS` naming `cwd` (not a `FILE_*` code). `env`: an object of names (`^[A-Za-z_][A-Za-z0-9_]*$`) to string values without NUL, at most 64 entries and 32 KiB, applied over the daemon's environment. Neither is a fence: the shell already has the user's reach (ADR-0032's first `Amended:` line).
- D2. **The Gradle 7 fixture** `sidecar/testdata/android7`: one `:app` module, Java only, `debug` and `release`; AGP 7.4.2, the Gradle 7.6.4 wrapper; `compileSdk 34` and `buildToolsVersion "34.0.0"` (installed), `android.suppressUnsupportedCompileSdk=34`, and `android.builder.sdkDownload=false` so AGP fails rather than installs into the SDK. Run under `gradle.sidecarJavaHome` zulu-17 and `gradle.allowBelowFloor`.
- D3. **Cancel evidence**: the daemon logs the phase a build cancel lands in (`before-accepted`, `begin-build`, `running`), so a level-5 run can tell which path it took.

## Acceptance

- [ ] A1. (Deferred by the owner, 2026-10-08; see Changes.) On `android7` under the hatch: `project.modules`, `project.variants`, `build.tasks` and `build.assemble` (with `outputs[]` and an installable APK) complete with `profile: conservative`, `validation: unsupported`, and the probe's facts are returned; whatever cannot work fails with a typed error naming the Gradle version and the hatch, never a raw failure. Source: plan m1k A7; ADR-0018 rule 3. Proven by: step 1.
- [x] A2. Through the daemon, against a real Gradle build: a cancel landing before `{accepted}` ends `cancelled` with no build record; one during the build ends `cancelled` with a record whose outcome is `cancelled`; no build of it is still running after either. Source: plan m1g A4. Proven by: step 2.
- [x] A3. After the scratch adb server on port 5199 is restarted, the next read recreates the reader's forward and succeeds; the 5037 server keeps its pid. Source: plan m1f A2; ADR-0030 §Decision. Proven by: step 3.
- [x] A4. On `Pixel_9a` (API 36.1) the `logcat` layout with the uid column is recorded and `device.logs {applicationId}` keeps only that app's lines by uid. Source: plan m1i A1 (D5). Proven by: step 4.
- [x] A5. With no configuration a physical device is hidden from `device.list` and refused by device calls; admitted by `devices.allowPhysical` and the allowlist, it is listed. Level 5 with the owner's Samsung only if the owner agrees. Source: plan m1e A3; ADR-0015 §4. Proven by: step 5.
- [ ] A6. A launch is recorded in `processes.json` from spawn until its serial is resolved; a daemon killed in that window has its launch ended by the next daemon; a record whose AVD already answers as a listed serial, or whose adb query fails or times out, is dropped with nothing signalled. Source: Q1. Proven by: step 6.
- [x] A7. `shell.exec` runs in `cwd` with `env` applied, per D1. Source: brief H. Proven by: step 7.
- [x] A8. `policy.get` returns a `revision`; `policy.update {ifRevision}` with a stale one fails `POLICY_REVISION_CONFLICT` and writes nothing; without it, behaviour is unchanged. Source: plan m1k D3; Q2. Proven by: step 8.
- [ ] A9. The register, `docs/README.md` and the plans index updated; the earlier plans' in-part items annotated with where they closed. Source: brief I. Proven by: step 9.

## Steps

### 1. Gradle 7.x under the hatch, on an Android fixture

- Builds: D2's fixture. The implementer fixes what step 1's run shows broken in `project.*`, `build.tasks`, `build.assemble` or the probe on AGP 7.4.2, or maps it to a typed error naming the Gradle version and the hatch (`gradle.md` §Version detection and toolchain profile in the same change).
- Implements: plan m1k A7; ADR-0018 rule 3.
- Run by: the implementer writes the fixture; the main session runs it (level 4: a built daemon, `$B/state-s1`, the fixture copied to `$B/android7`); the implementer fixes; the main session reruns. A missing SDK package is never remedied by installing it: the step stops instead.
- Check: (a) `project.modules`, `project.variants`, `build.tasks`, `build.assemble {module: ":app", variant: "debug"}` each completed, `conservative`/`unsupported`, `outputs[0]` an APK that `aapt2 dump badging` reads; the probe's facts present in `project.variants`. (b) For a capability that cannot work: a unit test with the fake sidecar reporting 7.6.4 asserts the typed error. (c) The SDK stamp unchanged (K2); `~/.gradle` gains AGP 7.4.2. **Decisive**: if the sidecar cannot configure an AGP 7 project at all, the step stops, the finding goes under Changes and to the owner, and A1 stays open.
- Schema: no change expected; any new error text is description-free.
- Needs: nothing in this plan.
- Not here: KGP or Compose under Gradle 7; Gradle below 7.3 (refused, ADR-0018 rule 3).
- Rests on: AGP 7.4.2 builds with `compileSdk 34` and build-tools 34.0.0 without downloading SDK packages (level 4, this step).
- [ ] Done. Evidence:

### 2. A real build cancelled through the daemon

- Builds: D3's log line (implementer). Then the main session's runs.
- Implements: plan m1g A4; ADR-0020 §Core primitives.
- Run by: the implementer (log), the main session (level 4: a built daemon, the golden sample copied and trusted).
- Check: unit: `TestCancelPhaseIsLogged` (fake sidecar, each phase). Level 4: twelve `droidyard call --detach build.assemble` each followed by `operation.cancel`: four at once on a cold sidecar (a fresh daemon or after `workspace.close`), four after 200 ms, four after 2 s: every operation `cancelled`; the log's phase per cancel; `before-accepted` and `begin-build` ones leave no record in `builds/index.json` and their console is swept at the next open; `running` ones leave a record with outcome `cancelled`; `build.lastResult` names none of the cancelled builds as finished, and a following `build.assemble` completes (the Gradle daemon is free). If no cancel lands `before-accepted`, that phase stays level 3 (fake), goes to Unverified, and A2 stays unticked.
- Schema: no change.
- Needs: nothing in this plan.
- Not here: cancelling a `build.run` with arbitrary tasks (the same path).
- Rests on: —
- [x] Done. Evidence: `TestCancelPhaseIsLogged` (before-accepted, running, begin-build for `build.run` and `build.assemble`; seen failing by swapped labels and by the unlogged assemble path) and `TestCloseReachesAnIdleSidecarSoTheRootReopens` (seen failing on the old check). Level 4 on the golden copy, a built daemon in `/tmp/dyp2`: four cancels right after `workspace.close` and reopen, four at 200 ms and four at 2 s after a source edit: nine ended `cancelled` (seven `begin-build`, one `before-accepted`, one `running`), three finished before their 2 s cancel; the `running` one has a record with outcome `cancelled`, the `before-accepted` one left a console without a record that the next open swept; `build.lastResult` names the following completed build; that build completed.

### 3. The reader's forward after an adb server restart

- Builds: no repository file unless the run fails (then the implementer fixes `persistent.go`'s forward recovery).
- Implements: plan m1f A2; ADR-0030 §Decision ("if `adb forward` is lost, the daemon recreates it").
- Run by: the main session, K1–K2, `droidyard_m1p`.
- Check: preconditions: `lsof -iTCP:5037 -sTCP:LISTEN` shows the owner's server (so the next command starts none), then 5037's `adb devices -l` lists nothing (a server on 5199 attaches to every running emulator, `verify-daemon` and m1l E6; otherwise the run waits, or the owner is asked to stop theirs); nothing listens on 5199 (`lsof -iTCP:5199`); 5037's `devices -l` snapshotted. `droidyard_m1p` is booted by `emulator.start` on a daemon with `ANDROID_ADB_SERVER_PORT=5199`, whose first call starts the 5199 server. **Decisive**: whether 5199 lists the scratch emulator as `device` while 5037 runs; if not, the step stops, records it, and A3 stays open (5037 is never touched). Then `device.uiTree` warm; `adb -P 5199 kill-server`, `adb -P 5199 start-server`; the next `device.uiTree` succeeds and `adb -P 5199 forward --list` shows the reader's forward again; the reader's pid recorded. Cleanup: `emulator.stop`, `adb -P 5199 kill-server`. 5037's pid and its `devices -l` snapshot identical before and after.
- Schema: no change.
- Needs: nothing in this plan.
- Not here: a restart of the 5037 server (never).
- Rests on: two adb servers can each reach one emulator (level 5, this step).
- [x] Done. Evidence: no repository change needed. Level 5 on `droidyard_m1p` (API 31) with a daemon on `ANDROID_ADB_SERVER_PORT=5199`: 5199 listed `emulator-5554` as `device` while 5037 ran and listed nothing; two warm `device.uiTree` reads, forward `tcp:62724 → localabstract:droidyard-reader-965cd045902a`, reader pid 6060; `adb -P 5199 kill-server` and `start-server` left no forward; the next `device.uiTree` succeeded in 0.9 s (log: `UI reader restart: refused`, then the forward recreated as `tcp:62822`), the reader still pid 6060; the read after it took 0.44 s. Cleanup: `emulator.stop`, 5199 killed; 5037's pid (19285) and `devices -l` identical before and after; the guard's `refused.log` empty.

### 4. `device.logs` on API 36.1

- Builds: `docs/research/spike-s8-s9-emulator-and-logcat.md` gains the API 36.1 rows (G1 layout, G7 ring sizes) and `internal/domain/device/testdata` a recording from it, read by a parser test.
- Implements: plan m1i A1 (D5 on 36.1).
- Run by: the main session under K3, then the implementer for the parser test.
- Check: under K3's guard variant, on `Pixel_9a`: `adb logcat -d -v threadtime,epoch,usec,uid -t 50` recorded; `logcat -g`; `device.logs {device, applicationId: <an installed app>}` returns only lines whose uid is that app's (`pm list packages -U`); the parser test over the recording passes, and fails with the uid column's position changed.
- Schema: no change.
- Needs: nothing in this plan.
- Not here: crashing an app on `Pixel_9a` (K3).
- Rests on: —
- [x] Done. Evidence: under K3 (Pixel_9a started here with `-no-snapshot-save -no-window`, stopped with `emu kill`; the guard variant logged 28 invocations, `refused.log` empty; 5037's pid 19285 unchanged). The first `device.logs {applicationId}` failed `DEVICE_LOGS_FAILED` (format): 4 lines in 37,930 carry no `: ` after the tag. Fixed in `parseLogLine` (the tag ends at the first `:` then; no `:` at all, the whole rest); `TestLogcatAPI36LineWithoutColonSpace` over the recording, seen failing with the fallback removed and with the uid column forced off. Rerun: `device.logs {applicationId: com.google.android.apps.messaging}` completed, `filter.by: uid`, `uid: 10153` (`pm list packages -U`), 100 lines, every one matched in a raw dump to uid 10153. G1/G7 rows added to the S8/S9 note.

### 5. Physical devices hidden by default

- Builds: no repository file unless the run fails.
- Implements: plan m1e A3; ADR-0015 §4.
- Run by: the main session. **Before the Samsung is used, the main session asks the owner**; the answer and its date go under Changes.
- Check: this step adds evidence only with the owner's yes; without it nothing is added, A5 stays unticked at its level 4 (m1e, m1k step 5), the level-5 half stays in Unverified, and step 9 does not annotate m1e A3 as closed. With it, under K4: with no configuration `device.list` omits `R5CX62JXR3L` and `device.uiTree {device: "R5CX62JXR3L"}` fails `DEVICE_NOT_ALLOWED`, the guard's invocation log showing no command addressed to that serial but `devices -l` and `getprop`; with `devices.allowPhysical: true` and the serial allowlisted (a scratch configuration), `device.list` lists it with `kind: physical`.
- Schema: no change.
- Needs: nothing in this plan.
- Not here: any action on the phone.
- Rests on: —
- [x] Done. Evidence: the owner's yes on 2026-10-08 (Changes). Under K4, a daemon in `/tmp/dyp5` behind the step's guard: with no configuration `device.list` returned `{devices: []}` and `device.uiTree {device: "R5CX62JXR3L"}` failed `DEVICE_NOT_ALLOWED` (cause `devices.allowPhysical is false`), the guard's log showing only `version` and `devices -l`; with a scratch `config.json` of `allowPhysical: true` and the serial allowlisted, `device.list` listed it with `kind: physical` (SM_A256B, Android 16, sdk 36), the one command addressed to it `shell getprop`. `refused.log` empty.

### 6. A pid record for a launch before its serial (Q1)

- Builds: the main session first writes Q1's amendments (ADR-0021 §Pid records, ADR-0033 §Decision and Consequences, ADR-0001's 2026-10-07 `Amended:` line, `process-lifecycle.md` §Kinds of processes, §Shutdown, cancellation and no-orphans, §Crash recovery, `emulator.md` §Detached launch). Then the implementer: `process.Record` gains `avd` and `adbPort`, and the store's `schemaVersion` moves so an older daemon does not reap such a record blindly (ADR-0021 §Format); `launch.go` writes an `emulator-launch` record at spawn (a failed write ends the launch and fails `EMULATOR_LAUNCH_FAILED`, as `ErrStreamUnrecorded` does for streams) and removes it before the serial is returned or once `end` has emptied the group; a failed removal ends the launch and fails the boot. Reconcile takes a judging function from `serve.go` (`process` imports no domain), called only for `emulator-launch` records whose pid and start time match, so a normal start runs no adb; it judges per Q1 ¶2, within one 2 s deadline for all its adb work.
- Implements: Q1.
- Run by: the implementer (code, unit tests); the main session (level 5); review: **Opus verifier** (process lifetime).
- Check: `TestLaunchIsRecordedUntilResolution` (record present while the fake has no serial; absent at the moment `boot` returns the serial, asserted there; absent after `end`); `TestFailedRecordWriteFailsTheLaunch`; `TestFailedRecordRemovalFailsTheBoot`; `TestReconcileEndsAPreSerialLaunch` (a re-executed daemon SIGKILLed while `emulatortest` delays its serial; the next daemon's `Reconcile` ends the group, `reconcile_test.go`'s pattern); `TestReconcileLeavesAResolvedEmulator` (an offline serial answering the AVD: the pid alive, the record dropped); `TestReconcileStartsNoServer` (nothing listening on the recorded port: no adb process spawned, nothing signalled); `TestReconcileDoubtsAVersionMismatch`; `TestReconcileDoubtsASilentConsole` (a listed serial whose `emu avd name` times out: nothing signalled); `TestReconcileDoubtsWhenAdbHangs` (two records, adb hanging: both dropped within the 2 s deadline); seen failing with no record, with removal after the serial is returned, with the adb check skipped, with "signal unless adb says yes", and with the listing read by spawning `adb devices`. Level 5, the qemu's start time read with `platform.StartTime`: (a) m1m step 4 (a)'s `SIGSTOP` model, the daemon `kill -9`ed inside the window; before the new `serve`, the recorded port's listing recorded (if the stopped qemu is listed, A6's signalling half stays level 3); after it, the qemu and its group gone within the reconcile's grace, `processes.json` empty; (b) the dangerous branch: a booted scratch emulator, a record written by hand with its qemu's pid and start time, a new `serve`: the qemu alive and listed, the record dropped; (c) as (b) with `adb -P <scratch port> kill-server` first: the record dropped, the qemu alive, nothing listening on that port afterwards.
- Schema: no change.
- Needs: Q1 accepted.
- Not here: `emulator.stop {avd}` (brief: "Not now"); instances an earlier daemon resolved (devices, never signalled).
- Rests on: no emulator that has become a device is ever signalled by reconcile (level 5: (b), and the adb-failure tests at level 3).
- [ ] Done. Evidence:

### 7. `shell.exec` with `cwd` and `env`

- Builds: `internal/domain/shell`: D1's params and validation; the description.
- Implements: D1; brief H.
- Run by: the implementer; review: the main session's diff review.
- Check: `pwd` with `cwd: "app"` prints `<root>/app`; `cwd: "../x"` and a symlink leaving the root → `REQUEST_INVALID_PARAMS` naming `cwd`, nothing spawned; `env {FOO: "bar"}` → `echo $FOO` prints `bar`; an invalid name or 65 entries refused at `Start`; seen failing with the containment check removed.
- Schema: `shell.exec` gains optional `cwd` and `env`; API minor version moves.
- Needs: nothing in this plan.
- Not here: a per-call `PATH` search for the command itself (it is `sh -c`).
- Rests on: —
- [x] Done. Evidence: `TestCwdRunsInAnInnerDirectoryAndRefusesEscapes` (`pwd` in `cwd: "app"` and through an in-root symlink; `../x`, `..`, a symlink leaving the root, a path under it, a missing path, a file and an absolute outside path each `REQUEST_INVALID_PARAMS` naming `cwd`, no marker written) and `TestEnvIsAppliedOverTheDaemonsAndBounded` (applied over the daemon's, same name replaced; bad and empty names, NUL, 65 entries, over 32 KiB refused naming `env`; 64 entries and the byte bound accepted). Seen failing with `file.ResolveDir` reduced to a join (the `../x` row) and, by the main session, with its `os.Root` and `stripRoot` checks removed (the `leak` symlink row completed); with `validateEnv` skipped; with `mergeEnv` skipped. Schema: `shell.exec` gains `cwd` and `env`; API 0.12.0 (shared with step 8).

### 8. A revision token on `policy.update` (Q2)

- Builds: the main session first writes Q2's amendment of ADR-0032 §5. Then the implementer: `internal/policy`: `revision` in `policy.get`'s result, computed from the same bytes the returned policy was parsed from (the cached snapshot keeps them); `ifRevision` in `policy.update`'s params, compared under the lock with the bytes just re-read; `POLICY_REVISION_CONFLICT` in the error list. In the same change: `capability-api.md` §Error model's code table.
- Implements: Q2; plan m1k D3.
- Run by: the implementer; review: **Opus verifier** (a new error code, concurrency).
- Check: `TestUpdateWithCurrentRevisionWrites`; `TestStaleRevisionConflictsAndWritesNothing` (a `droidyard trust` between `get` and `update`: the error, the file byte-identical, the trusted root kept); `TestWriteWhileUpdateWaitsForTheLockConflicts` (the test holds `config.json.lock`, starts `update` with the current revision, writes the file, releases the lock: `POLICY_REVISION_CONFLICT`); `TestAbsentFileRevision` (`ifRevision: "absent"` writes only while there is no file); `TestUpdateWithoutRevisionIsUnchanged`; `TestRevisionIsOfTheParsedBytes` (a seam writes the file between parse and hash: the revision returned is the parsed bytes'); seen failing with the comparison made before the lock (the lock-held test) and with the revision hashed from a fresh read (the seam test).
- Schema: `policy.get` result gains `revision`; `policy.update` params gain `ifRevision`; one new code; API minor version moves.
- Needs: Q2 accepted.
- Not here: `droidyard trust` sending a revision (it re-reads under the lock already).
- Rests on: —
- [x] Done. Evidence: `internal/policy/revision_test.go`: the six tests of the Check plus `TestConflictDropsAStaleSnapshot`, `TestNullIfRevisionIsRefused`, `TestEmptyIfRevisionConflicts`. Seen failing with the comparison moved before the lock (the lock-held test, which now waits on a seam before `lockExclusive`, no sleep) and with the revision hashed from a fresh read (the seam test); the review-round tests each with their mutation. Opus verifier: proceed, N1–N5 fixed in a round (the sleep, the cached snapshot after a conflict, `ifRevision: null` refused rather than disabling the check, the empty token, the texts); N6 (a hand edit inside the update's critical section is still overwritten) to the register at the close. Schema: `revision` on both results, `ifRevision`, `POLICY_REVISION_CONFLICT`; API 0.12.0.

### 9. Close

- Builds: by the main session: the register updated (rows closed by steps 1–8 marked, new limits added); m1g, m1i, m1e, m1f and m1k gain a dated §Changes line naming the step that closed each in-part item; `docs/README.md`; `docs/plans/README.md`; `verify-daemon` rows for steps 3 and 6; the scratch AVD deleted.
- Implements: A9.
- Run by: the main session; review: the Opus verifier over A1–A9.
- Check: `go test ./internal/doccheck` green; every Acceptance item's evidence names its level.
- Schema: no change.
- Needs: steps 1–8.
- Not here: Phase 2 planning.
- Rests on: —
- [ ] Done. Evidence:

## Not in this milestone

- The brief's "Not now" list (`m1n-daily-use-core.md` §Not in this milestone), in the register.
- In-part items whose gap is a missing evidence file (m1m A2, A3, A5; m1e A7): the brief excludes them.
- A full physical-device pass (brief: "Not now"); any use of the Samsung beyond step 5's.
- `droidyard trust` built on `policy.update`; array-merge semantics for policy lists (Q2's alternatives).

## Design questions

### Q1. May droidyard record an emulator launch so that crash recovery ends it?

- Blocks: step 6.
- The documents say: ADR-0021 §Pid records: "Devices and emulators are not recorded"; ADR-0033 §Decision ¶4: "no pid record", and its Consequences record a daemon dying in the pre-serial window as a limit; ADR-0033's Alternatives: "A pid record for pre-serial launches … Wins if that crash is observed". The owner chose it in the brief (H), without the crash having been observed.
- Draft decision, `Amended:` lines on ADR-0021 and ADR-0033, and `process-lifecycle.md` §Crash recovery (Delegated):
  1. An emulator launch is recorded as kind `emulator-launch` (with its AVD and the adb server port used) from spawn until its serial is resolved, removed before the serial is returned to the caller, or until `end` has emptied its group. If the removal fails, the launch is ended and the boot fails: no serial has been handed out.
  2. At start, reconcile signals a matching record's group **only on a positive answer**: the listing read over the adb server's own socket at the recorded port (`host:devices-l`, as the daemon already reads `host:version`: it starts nothing and restarts nothing), within one 2 s deadline for all its adb work; a server that answers, whose version matches the client's; and every listed serial (offline included) answering `emu avd name` with another AVD. **Doubt** drops the record and signals nothing, logged: nothing listening on the port, a version mismatch, an error, the deadline, or a listed serial whose console does not answer. A launch left running is today's limit; a device killed is what ADR-0033's second clause forbids.
  3. Clauses this changes: ADR-0021 §Pid records ("Devices and emulators are not recorded"; reconcile "before spawning anything" now includes one bounded socket read and console queries, only when such a record exists, inside the shim's 10 s auto-start retry of ADR-0019 §4: at worst the 5 s term grace, the 1 s kill grace and the 2 s deadline); ADR-0033's fourth clause ("no pid record"; "launched in an earlier daemon, is never signalled" now excepts a pre-serial launch proven not to be a device) and its Consequences' limit; ADR-0001's 2026-10-07 `Amended:` line; `process-lifecycle.md`'s device-process row, its shutdown layer 2 and its "devices and emulators are not reconciled here".
  - Alternatives considered: no record (today): a daemon crash in a 45 s window leaves a qemu holding the AVD, which the next `emulator.start` reports `EMULATOR_AVD_LOCKED` with no way out but the user. `emulator.stop {avd}` killing a serial-less instance (ADR-0033's alternative b; the brief's "Not now"). Removing the record after handing out the serial: a crash between the two would let the next daemon kill a running device; hence removal first, and the positive-answer rule for the remaining race. Signalling unless adb says the AVD is a device: a restarted or different adb server would answer "no serial", and a booted emulator would be killed.

### Q2. How does `policy.update` avoid losing a concurrent edit?

- Blocks: step 8.
- The documents say: ADR-0032 §5: RFC 7386 under an exclusive lock; its `Amended:` line (Q2 of m1k): a patch carrying `trust.roots` that races `droidyard trust` can lose a root. Plan m1k D3: "a revision token would fix it and is not in ADR-0032". No error code exists for it (`capability-api.md` §Error model: codes are listed per descriptor).
- Draft decision, an `Amended:` line on ADR-0032 (Delegated): `policy.get` returns `revision`, the hex SHA-256 of the bytes its policy was parsed from, `"absent"` when there is no file (an invalid file has the hash of its bytes); `policy.update` accepts an optional `ifRevision`; under the lock, after the re-read, a different revision fails `POLICY_REVISION_CONFLICT` (new code; nextStep: call `policy.get` and apply the change again) and writes nothing. Without `ifRevision` the behaviour is unchanged. Any writer, a hand edit included, changes the revision.
  - Alternatives considered: an HTTP `ETag`/`If-Match` only (adapter-specific; the CLI writes too); merging arrays instead of replacing them (not RFC 7386, and removal becomes ambiguous); a lock held from `get` to `update` (spans calls and sessions); a modification counter in the file (a hand edit forgets to bump it).

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| AGP 7.4.2 builds offline from the SDK's installed packages with `sdkDownload=false`, and parses this SDK's newer `package.xml` files | 1 | step 1 (c); the decisive stop |
| `emu avd name` answers within the 2 s reconcile bound while an emulator boots | 6 (it only makes reconcile drop more records) | step 6's tests and level-5 (b) |
| A hung launch is never listed by adb (ADR-0033 §Context: the pilot's hang never showed a serial), so reconcile can prove it is not a device | 6 (its signalling half) | step 6 (a)'s listing before the new `serve` |
| An adb server started moments earlier by someone else lists every running emulator within the deadline | 6 (else a device could look absent) | not settled by step 6 (c), which leaves nothing listening (verifier N3, 2026-10-08); the judge also asks every listening emulator console 5554–5584 for its AVD before signalling, so a booted instance of the AVD is found without adb's listing |
| The sidecar and the probe can configure an AGP 7 project | 1 | step 1 (a) |
| A cancel can land before `{accepted}` at level 4 | 2 | step 2's phase log |
| A second adb server on 5199 can hold the scratch emulator while 5037 runs | 3 | step 3's first check |
| The owner agrees to the Samsung's use | 5 (its level-5 half) | the owner's answer |
| A qemu's start time read at spawn matches after a daemon restart | 6 | step 6's level-5 run |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | B1: reconcile killed a booted emulator whenever adb failed, timed out or was another server; the amendment missed ADR-0021's "before spawning anything", ADR-0033's "earlier daemon, never signalled" and the 10 s auto-start bound; removal failure unhandled; the dangerous branch never run | Q1 ¶2: signal only on a positive, bounded (2 s) answer from the recorded port, offline serials counted; else drop and log; ¶1: failed removal ends the launch; ¶3 names the clauses; step 6 adds adb-failure and timeout tests and level-5 (b), a hand-written record over a booted emulator; Rests on restated |
| 1 | revise | B2: step 4 could not run behind m1n's guard (protected serials) and booting `Pixel_9a` breaks the mtime invariant | K3: a guard variant admitting exactly `device.logs`'s and `device.list`'s argument lists, logging every call; `-no-snapshot-save`, `emu kill`, the mtime exemption only when this run starts it; the host commands listed |
| 1 | revise | B3: a 5199 server attaches to every running emulator, the owner's included; boot, port and cleanup unnamed | step 3: 5037 lists nothing first, 5199 free, 5037's `devices -l` identical before and after, the boot named, `kill-server` on 5199 at cleanup |
| 1 | revise | B4: step 8's test could not catch a pre-lock comparison; the revision's bytes and the absent-file value ambiguous | a lock-held test; the revision from the parsed bytes; `"absent"` for no file; the error-code table in the same change |
| 2 | revise | B1: the reconcile query could start or restart the adb server it asked, whose first empty answer counted as positive | Q1 ¶2: the listing read over the server's socket (`host:devices-l`), starting nothing; nothing listening and a version mismatch are doubt; `TestReconcileStartsNoServer`, `TestReconcileDoubtsAVersionMismatch`; level-5 (c) with `kill-server` first; two Unverified rows |
| 2 | revise | B2: a listed serial whose console fails was decided both ways | Q1 ¶2: doubt; `TestReconcileDoubtsASilentConsole`; level-5 (a) records the listing first, else level 3; Unverified "a hung launch is never listed" |
| 2 | revise | B3: K3's guard allowlist was not what `device.logs` and `device.list` issue | K3 lists the exact argument lists (`version`, `pidof`, `emu avd name`, the two `logcat` forms matched whole), the manual reads named apart |
| 2 | — | N1 one deadline, adb only for matching records, the worst case; N2 the seam, the record fields, the store version; N3 a failed write; N4 documents; N5 who runs level 5, start times; N6 A5 without consent; N7 `Pixel_9a` mtimes, `protected`; N8 5037 started by the precondition; N9 the parsed-bytes test | all taken: Q1 ¶3, step 6; step 6 Builds; `TestFailedRecordWriteFailsTheLaunch`; step 6 and Q1 ¶3's lists; step 6 Run by, `platform.StartTime`; step 5, Scope; K3; step 3's `lsof` first; `TestRevisionIsOfTheParsedBytes` |
| 1 | — | N1 cancel timings and the worker check; N2 step 1's runner, package.xml; N3 the resolver and NUL; N4 the code table; N5 step 5 adds nothing without consent, guard logs; N6 the removal-order assertion; N7 the research note's citation | all taken: step 2 cold-sidecar cancels, twelve runs, `build.lastResult` instead of workers, A2 unticked at level 3; step 1's runner and "never install"; D1; step 8; step 5 and K4; step 6 asserts at the return; N7 is m1n step 1 |
Two rounds ran (round 1: B1–B4; round 2: B1–B3, all in reconcile's adb query and step 4's guard); every blocking finding of both is answered in the plan text, so a further look is a diff-only check by the main session (`milestone-plan` §Writing one, item 7). The status stays `Blocked` on Q1 and Q2; once both are answered it becomes `Reviewed`. The index row, added by the main session in the plans' commit: `| [m1p-functional-gaps.md](m1p-functional-gaps.md) | 1p — the functional gaps of Phase 1, and three ideas from earlier plans | Blocked |`.

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.

- 2026-10-08, owner: after being asked for the Samsung `R5CX62JXR3L` (step 5), the owner answered "te he conectado el device fisico" and connected it; taken as the yes K4 requires.
- 2026-10-08, step 6 (Opus verifier, fix first: B1 and N1–N9): B1, ADR-0033's clause 2 contradicted the code (a failed record removal ends an instance whose serial was read): its 2026-10-08 line extended. N3, an empty listing from a server started moments earlier was proof: the judge also asks every listening emulator console (5554–5584) for its AVD before signalling. N2, a listed `usb:` device was doubt, so an attached phone disabled the recovery: skipped. N5 (no AVD), N6 (an empty port variable is 5037), N9 (a panicking judge is doubt), N4 and N7 (comments, `emulator.md` §Failure modes) fixed. N8 accepted: the judge reaches `Reconcile` through `Supervisor.SetJudge`, wired in `main.go`'s `registry()`, not through `serve.go`; `process` still imports no domain package. N1 to the register: processes.json is written at schemaVersion 2, so an older binary's daemon then refuses every supervised spawn until the file is removed. Observed by the main session: with `ANDROID_ADB_SERVER_PORT` on a free port, `adb version` and `adb -s emulator-5999 emu avd name` started no server.

- 2026-10-08, owner: Gradle 7.x support is not wanted for now; step 1 is dropped from this milestone and A1 stays open (the register's Gradle 7.x row says so). A first implementer run was stopped and its partial fixture discarded; nothing of it is in the tree.
- 2026-10-08, step 2: the first level-4 run found that a closed and reopened workspace could not build again until the daemon restarted (`connection.open failed: root is already open`): `gradle.Client.Close` spoke to the sidecar only in state `ready`, and after any call it is `idle`, so the close never reached it. Fixed (`running` covers ready, busy and idle); the fake sidecar now refuses a second open of a root, as the real one does, which is what hid it. The same run showed `build.assemble` fetching the model outside D3's logged path; it now shares `projectModel` with `build.run`.
