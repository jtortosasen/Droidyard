# Plan: Milestone 1n — the daily-use core: crashes, ANRs, typing and speed

- Status: Blocked
- Approved in part: 2026-10-07, Delegated: steps 1 and 2 only. Q1 and Q2 are answered after step 2's facts; the main session then applies the recommended drafts under the owner's standing instruction unless the owner says otherwise, and labels them Delegated.
- Date: 2026-10-07
- Milestone: not in `docs/README.md` (its **Next** row names Phase 2). Inserted by the owner, 2026-10-07, through the main session's brief: before Phase 2 the core must be solid enough to use every day with Cursor and to build on, "sin dejar cosas a medias" (without leaving things half done). One brief, three plans: this one (A crash, B ANR, C typing and speed, F error texts, I the known-limits register, J test hygiene); `m1o-tokens-and-harness.md` (D tokens, E harness integration, the comparison rerun with a Cursor arm); `m1p-functional-gaps.md` (G the "in part" acceptance gaps, H the earlier plans' ideas).
- Phase: none of `docs/roadmap/mvp.md`'s phases; it serves `mvp.md` §Definition of done only through m1o's rerun. Phase 2 waits for 1n–1p.
- Contract: ADR-0029 §Decision, ADR-0030 §Decision (both amended by Q1); ADR-0015 §1, §2, §4; ADR-0020 §Adapter projections and its 2026-10-06 `Amended:` line (once an effect is issued, the call ends `completed` with a warning, never an error inviting a repeat); `docs/subsystems/ui-automation.md` §Sequence and timing, §Result, §Typing, §Input primitives, §Failure modes, §Open questions; `docs/subsystems/adb-device.md` §Logs, §Lifecycle and concurrency, §adb resolution and degradation; `docs/research/daily-use-crash-anr-2026-10.md` §4; `docs/research/daily-use-speed-input-2026-10.md` §2, §5; `docs/research/spike-persistent-reader-2026-10.md` (gates G1–G5).

**Approval now covers steps 1 and 2 only**, which rest on neither question; steps 3 onward start once Q2 and Q1 are accepted (`milestone-workflow` §Rules between the steps, "Blocked"). **Q1 is the owner's to decide, not delegable**: it changes ADR-0030, an owner decision (no check on the socket), by adding a check for the new acting requests; the main session should put it to the owner. Q2 may be accepted as Delegated.

**The owner should see:** Q1 (the reader stops being read-only: it taps, presses keys and sets text, and its acting requests are fenced by the peer's uid while reads stay unauthenticated as ADR-0030 decided; `inputText` sets the field's text by default instead of typing key by key, with `method: "keys"` to force typing); Q2 (three optional result fields, `details.systemDialog` on a refused action, no new error code; droidyard never dismisses a dialog); D1 (a scratch AVD on the owner's installed `android-36.1` image, read-only use of the image); step 2 (e) builds a throwaway spike dex outside the repository so that Q1 is written from observed facts; the baseline is red today only because the uncommitted research note `daily-use-speed-input-2026-10.md` cites a section that does not exist (step 1).

## Why three plans

The brief holds about thirty verifiable steps. G and H depend on nothing in A–F; D and E must be measured after the device path stops changing, with a Cursor baseline taken before them. So: 1n changes the device path (it is what the rerun measures), 1o measures before and after the token and harness work, 1p closes the functional gaps. I and J sit here: J is part of a green baseline, and I exists from this milestone's close so that 1o's and 1p's closes update it.

## Scope

| Brief item (2026-10-07) | Here? | If not, where and why |
| --- | --- | --- |
| A R0: a monitored crash run that logs the restart reason | yes | step 2 |
| A R1: the reader not wedged by a dying app; a busy reader never killed | yes | step 3 (Q2 ¶1) |
| A R2: the crash's exception and top frames after an action; `exit-info` fallback | yes | step 6 (Q2 ¶2) |
| B R3: an ANR named from `aerr_*` ids and `am_anr`; dismissal left to the agent | yes | step 6 (Q2 ¶3) |
| C: Unicode with nothing installed, never twice, at least as fast as the shell | yes | steps 2 (e), 4, 5, 9 (Q1) |
| C: event-based settle, cached per-call adb lookups, waiting for focus | yes | steps 5 (focus), 7 |
| C: the comparison's mislabelled shell `input` figure | yes | step 10 |
| F: "Tried:" texts name key and file; nextSteps of the errors agents hit | yes | step 8 |
| I: the known-limits register; the close row amended | yes | step 10 |
| J: `TestFollowWindowKeepsOnlyItsLinesAndRecordsTheClient` under `-race` | yes | step 1 |
| D tokens, E harness, the comparison rerun | no | `m1o-tokens-and-harness.md` |
| G functional gaps, H pid record / `shell.exec` cwd-env / policy revision | no | `m1p-functional-gaps.md` |
| The brief's "Not now" list | no | §Not in this milestone; seeded in the register at step 10 |

## Starting point

Code at `3abf127` (the 1m close): API `0.10.0`, 39 capabilities, 34 MCP tools. The reader `internal/domain/device/reader/src/DroidyardReader.java` (408 lines): one `LocalServerSocket` in the abstract namespace, a single-threaded serve loop answering `ping`, `dump`, `quit`; `document()` calls `waitForIdle(100, 500)` and `getRootInActiveWindow()`; the dex and its SHA-256 pinned in `reader.go`, rebuilt by `reader/build.sh`. `persistent.go`: `warmReadTimeout` 3 s; `restartReader` pings with `readerPingCheck` 1 s and otherwise `killOwnReader` and starts afresh; `dump.go:193` restarts on `dumpUnreachable`, and other failure kinds fall through to the one-shot and `uiautomator dump` (`dump.go:206-234`). `tap.go`: `input tap`, a fixed 300 ms settle (plan m1f D10), polls 250 ms apart. `input.go`: `input text` chunks or the ADBKeyboard broadcast under the m1m nonce wrapper; `getprop ro.build.version.sdk` on every `clear`. `target.go`: `adb devices -l` and `getprop sys.boot_completed` on every device call. `logcat.go`: the crash-block parser (m1i D6); `logs.go:536` reads the device clock with `date`. `formapp/`: the Views fixture app of m1f (D14), built by `build.sh` without Gradle, not debuggable, no password field.

Baseline on 2026-10-07 at `3abf127` with the four research notes untracked: `go vet ./...` and `gofmt -l .` print nothing; `go test ./...` fails one test, `internal/doccheck` `TestSectionReferencesResolve` (`daily-use-speed-input-2026-10.md:203` cites a "Typing and" section of `ui-automation.md` that does not exist); every other package ok. `go test -race -count=1 -run TestFollowWindowKeepsOnlyItsLinesAndRecordsTheClient ./internal/domain/device` fails: "the follow took 6.27s, want its 2 s window and no more than 1 s over". `TestPlansIndexMatchesFiles` fails until `docs/plans/README.md` has the three new rows, which the main session adds in the plans' commit.

## Run rules

- K1. `B=~/Library/Caches/droidyard-dev/m1n`. Only the scratch AVDs of D1 are booted, signalled, wiped or stopped, with `ANDROID_AVD_HOME=$B/avd`; deleted at step 10. A copy of m1m's kit at `$B/kit`, its AVD renamed `droidyard_m1n`, for step 9 (e).
- K2. The owner's constraints, in force: never touch, kill or restart the adb server on 5037; never modify or build in `~/Documents/DopamineControl`; never `uiautomator dump`, wipe or uninstall on the owner's emulators (starting and stopping them is allowed; this plan uses none of them); never modify the Android SDK (`.knownPackages` excepted); kill only pids this run started (`kill <pid>`, never `pkill`/`killall`); never bypass the pre-commit hook; the OpenCode Go key only at `~/.config/droidyard-dev/opencode-go.key`, never echoed; `opencode` inside a copy only as `opencode run --standalone`; DeepSeek writes code, every check is Opus's.
- K3. Every daemon: `DROIDYARD_STATE_DIR=$B/state-<step>`, `DROIDYARD_CONFIG_DIR` unset, `ADB` = m1m's guard copied to `$B/bin/adb` (`protected` = the serials online at the run's start), `ANDROID_SERIAL=droidyard-m1n-none`. The 5037 server's pid, the owner AVDs' `userdata-qemu.img*` mtimes and the SDK stamp are recorded before and after every level-5 run and must not change.

## Delegated decisions

- D1. **Scratch AVDs**: `droidyard_m1n` on `system-images;android-31;google_apis;arm64-v8a` (the image of every earlier level-5 run) and `droidyard_m1n36` on `system-images;android-36.1;google_apis_playstore_ps16k;arm64-v8a` (the owner's daily API level; the image is installed, `avdmanager create avd` writes only under `$B/avd`). Level-5 checks run on both unless a step says otherwise.
- D2. **The settle** (supersedes plan m1f D10, `ui-automation.md` §Sequence and timing): the first read starts right after the action and the reader's own idle wait is the settle; polls after it ask the reader for a shorter idle bound (`dump` gains an optional `idle <quietMs> <maxMs>`, default today's 100/500, polls 100/200). Carried by step 4's dex.
- D3. **Per-device caches**: `sys.boot_completed=1` and `ro.build.version.sdk`, keyed by serial and `transport_id` (as m1i D5's uid-column probe), dropped when the transport id changes or a call fails `DEVICE_NOT_FOUND`.
- D4. **The register** is `docs/roadmap/known-limits.md`: one table, a row per open item (limit, unverified claim, deferred item), with its source link, the milestone that recorded it and, once closed, the one that closed it. The Close row of `.claude/skills/milestone-workflow/SKILL.md`'s table gains "the register updated".
- D5. **"At least as fast as the shell"**, on the golden sample's form, `droidyard_m1n`, 10 alternating trials each, the text alternating "Ana" and "Bob", `droidyard call` against `adb` run directly, neither side with an expectation: `inputText {target: {id: name}, text, clear: true}` (focusing tap, clear, set, one read) against `input tap x y; input keycombination 113 29; input keyevent 67; input text <text>; uiautomator dump /dev/tty` with no sleeps (the shell's most favourable unit); `tapElement {id: like_alpha}` against `input tap x y; uiautomator dump /dev/tty`. Pass: droidyard's median wall time ≤ the shell's, per pair.
- D6. **Error texts (F)**: the codes agents met in recorded runs (m1l, m1m, the m1l pilot): `DEVICE_ELEMENT_NOT_FOUND`, `DEVICE_NOT_FOUND`, `DEVICE_NOT_READY` (its dialog and pending-action variants included), `EMULATOR_AVD_LOCKED`, `EMULATOR_BOOT_TIMEOUT`, `POLICY_SHELL_DISABLED`, plus `WORKSPACE_UNTRUSTED` (every first session). Each nextStep names the capability to call with its deciding argument, or the human's exact command, and says whether repeating the failed call is safe.
- D7. **The crash's device time** (Q2 ¶2): no `date` per action; on detection, one device `date` read minus the host time elapsed since the action began gives `-T`, so the cost stays on the crash path.

## Acceptance

- [ ] A1. `go test ./...` passes and the follow test passes under `-race` with its window bound. Source: brief J; the baseline. Proven by: step 1.
- [ ] A2. After the app crashes under a `tapElement`, the next `uiTree` returns a tree (its foreground not the crashed app) within 3.5 s of the crash, or within the bound step 2 (a) sets and records under Changes, and the daemon log shows no reader restart. Source: brief A (R0, R1); Q2 ¶1. Proven by: step 2, step 3, step 9.
- [ ] A3. A `tapElement` or `inputText` whose action crashed the app returns `appCrash` with the exception, message and top frames from the crash buffer, or the `exit-info` reason when the buffer has none, with or without `expect`; untrusted-marked. Source: brief A (R2); Q2 ¶2. Proven by: step 6, step 7, step 9.
- [ ] A4. While an "isn't responding" dialog shows, `uiTree` and the results of `tapElement` and `inputText` carry `systemDialog {kind, package}`; an action whose target is outside the dialog fails before acting with `details.systemDialog`; droidyard taps nothing on the dialog by itself; a `tapElement` on the dialog's wait button (the id step 2 (b) records) dismisses it. The repeat-crash dialog likewise, with `kind: "crash"`. Source: brief B (R3); Q2 ¶3. Proven by: step 6, step 9.
- [ ] A5. `inputText` delivers "ñandú café 🦩 東京" to an `EditText` and to a Compose `TextField`, with nothing installed and the input method unchanged, `method: "set_text"`, `characters` equal to its length and the field read back equal, once. Source: brief C; Q1 ¶3. Proven by: step 2, step 4, step 5, step 9.
- [ ] A6. Per D5, droidyard's median wall time ≤ the shell's for both pairs, on `droidyard_m1n`. Source: brief C. Proven by: step 9.
- [ ] A7. The reader's acting requests are refused for a connecting app's uid and executed for adbd's; reads are answered as before; the spike gates G1, G2, G3 and G5 pass on the new digest; `default_input_method` is unchanged by any call. Source: Q1 ¶2; ADR-0030 §Decision. Proven by: step 3, step 4.
- [ ] A8. An acting request that got no definitive answer is never followed by another delivery method: the call completes with a warning that it may have applied; a later call meeting a still-pending acting request fails before acting. Source: Q1 ¶3; ADR-0020 (the 2026-10-06 `Amended:` line). Proven by: step 4, step 5.
- [ ] A9. No fixed 300 ms settle remains; a warm device call issues no `getprop` after the first on its transport. Source: brief C; D2, D3. Proven by: step 7.
- [ ] A10. The adb, emulator and JVM resolution failures' "Tried:" lists name each candidate's source (`tools.adb` in `<config path>`, `$ADB`, the SDK, `PATH`); D6's codes follow D6's rule. Source: brief F; plan m1k A2 (N4). Proven by: step 8.
- [ ] A11. `docs/roadmap/known-limits.md` exists, seeded per D4, this plan's §Not in this milestone list included; the workflow's close row amended; the m1m shell `input` figure corrected. Source: brief I, C. Proven by: step 10.

## Steps

### 1. A green baseline

- Builds: the main session corrects `daily-use-speed-input-2026-10.md:203` to "§Typing, §Input primitives" when it commits the four research notes. The implementer finds why the follow test takes ~6.3 s under `-race` in `internal/domain/device` (`logs.go` follow path or the fake adb) and fixes the cause.
- Implements: brief J; the baseline rule (`milestone-plan`: a red baseline is step one).
- Check: `go test ./...` green; `go test -race -count=5 -run TestFollowWindowKeepsOnlyItsLinesAndRecordsTheClient ./internal/domain/device` passes 5 of 5 with the 3 s bound unchanged. If the cause is the race detector's cost in the fake adb rather than the follow, the bound is widened under `-race` only, with the measured overhead in the commit; the step says which.
- Schema: no change.
- Needs: nothing.
- Not here: other `-race` slowness (none known).
- Rests on: —
- [ ] Done. Evidence:

### 2. Spike: what a crash, an ANR, typing and acting cost on a device

- Builds: by the implementer, kept: the daemon logs every reader restart and fallback with its reason (`timeout <ms>`, `eof`, `refused`, `ping failed`) and every warm read over 1 s with its duration, in `daemon.log`; `formapp` gains a `crash` button (throws on the main thread), a `freeze` button (sleeps 15 s on the main thread), a password field, and a probe activity (`am start -n … -e socket <name> -e request <line>`) that connects to an abstract socket from the app's own uid and shows the reply in a text view. By the implementer, **outside the repository** (`$B/spike-dex/`, read in full by the main session before it runs, kept until Q1 and Q2 are accepted so the ADRs cite something rerunnable, deleted at step 10; the daemon's reader is sent `quit` first, one `UiAutomation` per device): a dex answering `peer` (the connecting uid), `settext`, `tap` (`UiAutomation.injectInputEvent`, timing the reply) and a `dump` on a second thread. Then the main session, under K1–K3 on both D1 AVDs, `$B/spike-<stamp>/`.
- Implements: brief A R0; the facts Q1 and Q2 rest on (`daily-use-crash-anr-2026-10.md` §Not established; `daily-use-speed-input-2026-10.md` §2's experiment).
- Check: unit: `TestReaderRestartIsLoggedWithItsReason` (a fake reader that stalls, one that closes, one that refuses: three distinct reasons); seen failing with the reason dropped. Level 5, each observation timestamped: (a) golden sample `tapElement {text: "Crash"}` then `uiTree`, five times, the app relaunched and 2 minutes apart, each noting whether a "keeps stopping" dialog showed: the log's restart reason; and the same crash with the reader driven directly over its forward (`adb forward --list`'s port, `nc`): a `dump` sent right after the crash while `ping` is sent every 200 ms, until the dump answers: how long a read stays blocked, which sets A2's bound; and what the first read after the crash returned (pre-crash tree, busy, or the launcher). **Decisive**: `timeout` then kill and restart confirms Q2 ¶1; an `eof` or a reader pid gone before the kill stops the plan and Q2 ¶1 is redrafted before step 3. (b) Formapp `freeze`, then a tap on it: time until the ANR dialog; during it, a `uiTree`: whether the root is the dialog's, the dialog buttons' `id` strings as the tree shows them; `logcat -d -b events` for `am_anr`, `am_crash`; `dumpsys window`'s focus line. **Decisive**: if the dialog is not the active window's root, Q2 ¶3 is redrafted to find it through the window list before step 6, and G4 (Qustodio) is added to step 4 because the service flags change. (c) `dumpsys activity exit-info dev.droidyard.formapp` after a crash and after an ANR closed: the format. (d) The speed note's experiment, 10 trials each: `input text Ana` 0.3 s against 1.5 s after the focusing tap; with the reader alive against after `quit`; the reader's RSS at start and after 30 interactions. (e) With the spike dex: the peer uid over the forward (expected 2000); the probe activity's uid and the reply it gets (connected, or refused by SELinux); `settext` of A5's string into formapp's `name` and the golden sample's `name`, read back, then the greeting after `Continue` shows it; an injected tap's reply time on a quiet screen and during `freeze`, and when the tap landed; a second-thread `dump` during `freeze`. Results go to Changes before Q1's and Q2's ADRs are written.
- Schema: no change.
- Needs: step 1.
- Not here: any change of behaviour (steps 3–7).
- Rests on: —
- [ ] Done. Evidence:

### 3. A busy app no longer costs a reader restart (Q2 ¶1)

- Builds: the main session first writes Q2's ADR (next number) from step 2's facts, with its row in `docs/adr/README.md` and the ADR counts in `AGENTS.md` and `docs/README.md`. Then the implementer: `DroidyardReader.java` runs each `dump` on a worker bounded at 2.5 s on the device, abandoning its document between node queries at the bound, and answers `busy <ms> <package>`; `ping` and `quit` are answered on the accept thread while a dump runs; a dump arriving while one worker is out starts a second, and with two out answers `busy` at once (Q2 ¶1). `persistent.go`/`dump.go`: `busy` is a failed read that never restarts the reader and skips the one-shot and `uiautomator dump`; a restart only when `ping` fails (the 1 s rule unchanged). The new digest in `reader.go`; `build.sh` reproduces it.
- Implements: Q2 ¶1; `daily-use-crash-anr-2026-10.md` §4 (R1).
- Run by: the implementer; review: **Opus verifier** (concurrency, process lifetime).
- Check: unit, with a fake reader over a real socket: `TestBusyReadDoesNotRestartTheReader` (answers `busy`: the call fails `DEVICE_UI_DUMP_FAILED` with a generic cause, the package in `details`, the error marked untrusted with the fixed note; no kill, no one-shot, no `uiautomator dump` issued), `TestMissedPingStillRestarts`, `TestBusyPollAfterTheTapIsNotAnError`; seen failing with `busy` treated as `dumpUnreachable` and with the fallthrough restored. Level 5 on both AVDs: step 2 (a) rerun through the daemon: the post-crash `uiTree` returns a tree within A2's bound and the log shows no restart; spike gates G1 (outlives its shell), G2 (20 warm reads, median), G3 (fresh after a real tap, 5 of 5), G5 (idle exit at 60 s; a stalled client) on the new digest; during `freeze` a `uiTree` answers within 3.5 s (failed, naming the app) and the reader keeps its pid.
- Schema: description-only diff of `device.uiTree`, `device.tapElement`, `device.inputText` (the busy cause); no field or code.
- Needs: step 2; Q2 accepted.
- Not here: reporting the crash or the dialog (step 6); acting verbs (step 4). G4 (Qustodio) not rerun unless step 2 (b) changes the service flags.
- Rests on: a query blocked on one reader thread blocks neither `ping` nor a second worker (level 5: step 2 (e), then this step's freeze check).
- [ ] Done. Evidence:

### 4. The reader acts, fenced by its peer's uid (Q1)

- Builds: the main session first writes Q1's ADR from step 2's facts (ADR-0029 and ADR-0030 amended). Then the implementer: `DroidyardReader.java` gains `tap <x> <y>` (DOWN and UP through `UiAutomation.injectInputEvent`, the UP synchronous), `keys <base64 ASCII>` (`KeyCharacterMap.VIRTUAL_KEYBOARD` events, asynchronous but the last, `FLAG_KEEP_TOUCH_MODE`), `settext <replace|insert> <base64>` (`ACTION_SET_TEXT` on `findFocus(FOCUS_INPUT)`, inserting at the selection for `insert` and treating hint text as empty, then `refresh()` and the node's text read back), and D2's `idle` option on `dump`. Acting requests run on one acting thread, never on a dump worker; one arriving while another is out answers `busy` before executing anything. The request line cap rises from 256 bytes to 4 KB for acting requests (a 500-character `inputText` is up to about 2.7 KB of base64); a longer one answers `refused` before executing, never a cut line. `submit` is `keys` of a newline (`KEYCODE_ENTER` through the virtual key map). Each answers `denied` unless `getPeerCredentials().getUid()` is 0 or 2000, and the answer says whether anything was executed (`refused` before execution, `done` with its read-back, or a timeout after execution began). `persistent.go` gains the client calls. New digest.
- Implements: Q1 ¶1, ¶2; D2's reader half.
- Run by: the implementer; review: **Opus verifier** (the security fence).
- Check: unit on the Go client with a fake reader (request encoding; `denied` mapped to an error naming the fence; `TestUnansweredActingRequestIsReportedAsMaybeApplied`). Level 5 on both AVDs, by `droidyard call` and raw requests over the forward (`nc`): `settext replace` of A5's string into formapp's `name` (`EditText`) and the golden sample's `name` (Compose) reads back equal; a 500-character mixed string through `settext` and a 500-character ASCII string through `keys`, each read back equal; a 5 KB request answered `refused` with the field unchanged; `insert` after "ab" reads "ab…"; `tap` on formapp's `greet` changes the greeting; `keys "Ana"` types it once; `settings get secure default_input_method` and `ime list -s` unchanged before and after; **the fence**: formapp's probe activity (an ordinary app uid and domain) sending `tap` gets `denied` and nothing is tapped, while its `dump` is answered; or, if step 2 (e) showed SELinux refusing the app's connection, that refusal recorded and the uid branch level 3 (Unverified); a `tap` during `freeze`: its reply and landing times recorded; gates G1, G2, G3, G5 on this digest.
- Schema: no change.
- Needs: step 3; Q1 accepted.
- Not here: wiring the capabilities (step 5); the clipboard paste path (Q1 alternative a); `InputManagerGlobal` reflection (Q1 alternative c).
- Rests on: a forwarded connection's peer is adbd, uid 2000 (0 under `adb root`), on both images (level 5, step 2 (e), then here).
- [ ] Done. Evidence:

### 5. `inputText` and `tapElement` act through the reader (Q1 ¶3)

- Builds: `input.go`: delivery per Q1 ¶3 (`set_text` by default, `keys` for printable ASCII when `set_text` is refused before execution or when `method: "keys"` is asked, `input text`/ADBKeyboard only when the persistent reader cannot run); after the focusing tap, wait until the target reads `focused` (bounded by the before phase) instead of a fixed settle; `characters` from the read-back; a read-back that differs from the requested text is a completed result with a warning quoting it (untrusted), never a retype; an acting request without a definitive answer ends the call `completed` with a "may have applied" warning and no other method tried. `tap.go`: the tap through the reader, `input tap` only without it, under the same rule. The params gain `method?: "auto" | "keys"`; the result's `method` enum gains `set_text`, `keys`. Descriptions. In the same change: `ui-automation.md` §Typing, §Input primitives, §Failure modes.
- Implements: Q1 ¶3, ¶4; brief C (Unicode, never twice).
- Run by: the implementer; review: **Opus verifier** (a capability's semantics change).
- Check: fake-reader unit tests: `TestInputTextSetsTextByDefault` (non-ASCII, no ADBKeyboard: `set_text`, no `input` command issued), `TestKeysWhenSetTextIsRefused`, `TestMethodKeysForcesTyping`, `TestNonASCIIRefusedWhenNothingCanDeliverIt` (`DEVICE_TEXT_NOT_DELIVERABLE` before any tap), `TestReadBackDifferenceIsAWarningNotARetype` (exactly one `settext` sent), `TestPasswordFieldWithoutClearUsesKeys`, `TestUnansweredSetTextIsNotRetyped`, `TestInsertTimeoutWarnsNotRetypes`, `TestUnansweredTapIsNotRepeatedWithInputTap`, `TestBusyActingThreadFailsBeforeActing` (no request of any method sent); a password field's read-back compared by length only (it reads masked); seen failing with the old order restored, with a retype on mismatch, and with a fallback after an unanswered request. The m1m nonce tests still pass on the fallback path. Level 5 on both AVDs: A5's string into both fields (`method: set_text`, read back equal, typed once); "Ana" with `method: keys` once; formapp's password field with `clear` (`characters` 8 for "s3cr3t!x", no difference warning).
- Schema: `device.inputText` gains the `method` param and two enum values on the result's `method`; descriptions of `inputText` and `tapElement`; API `0.10.0` → `0.11.0`.
- Needs: step 4.
- Not here: a `method` for paste; per-key timing control.
- Rests on: `ACTION_SET_TEXT` on a Compose `TextField` sets the value the app's state holds (level 5, step 2 (e), then here).
- [ ] Done. Evidence:

### 6. Crashes and system dialogs are named (Q2 ¶2, ¶3)

- Builds: `tap.go`/`input.go`: when `foreground.after` differs from `foreground.before`, or no read after the action observed the foreground, and `pidof <before>` is empty: one `logcat -d -b crash -T <D7's time>` through the m1i D6 parser, filtered by the package, gives `appCrash {package, pid, time, exception, message, frames[≤5], source: "logcat"}`; with no block, `dumpsys activity exit-info <package>` (step 2 (c)'s format) gives `{package, reason, source: "exitInfo"}`. Every read (`uiTree`, the before and later reads of an action) whose tree holds step 2 (b)'s dialog ids sets `systemDialog {kind: "anr" | "crash", package?}`, the package from `am_anr`/`am_crash` in one `logcat -d -b events -T <since>` (`since`: the action's start for an action, 60 s back for `uiTree`). On the before read: a target inside the dialog is acted on as usual; any other target, and an `inputText` without one, fails per Q2 ¶3 before anything is tapped or typed. Fields tagged untrusted; the refusal marked through `Call.Untrusted`. In the same change: `ui-automation.md` §Result, §Failure modes, §Open questions (the ANR and crash rows closed); `adb-device.md` §Targeting (the `systemDialog` state).
- Implements: Q2 ¶2, ¶3; brief A (R2), B (R3); D7.
- Run by: the implementer; review: **Opus verifier** (new result fields, untrusted marking).
- Check: fake-adb unit tests with step 2's recordings as testdata: `TestTapThatCrashesTheAppReportsTheCrash`, `TestCrashReportedWhenNoAfterReadObservedIt` (every read after the tap `busy`), `TestCrashWithEmptyBufferFallsBackToExitInfo`, `TestNoCrashReadWhenTheForegroundHeld` (no `logcat -b crash`, no `date` issued), `TestTargetBehindADialogIsRefusedBeforeTheTap` (no input command; `details.state` and `details.systemDialog`; the error marked untrusted), `TestTargetInsideTheDialogIsTapped`, `TestCrashFieldsAreUntrusted` (schema carries `x-droidyard-untrusted`); seen failing with the trigger removed, with the refusal removed, and with the mark removed. Level 5 in step 9.
- Schema: optional `appCrash` on `tapElement` and `inputText` results; optional `systemDialog` on `uiTree`, `tapElement`, `inputText`; `DEVICE_NOT_READY`'s description gains the dialog case (the code is already in both error lists); untrusted tags; API stays `0.11.0` (same milestone).
- Needs: step 3 (step 2's recordings).
- Not here: a crash between calls or at `device.launch` (§Not in this milestone); dismissing a dialog.
- Rests on: the dialog's buttons reach the tree while it shows (level 5, step 2 (b)).
- [ ] Done. Evidence:

### 7. The cheap speed items

- Builds: `tap.go`, `input.go`: D2 (no fixed settle; the polls' shorter idle bound through step 4's `idle` option). `target.go`, `input.go`: D3's caches. In the same change: `ui-automation.md` §Sequence and timing.
- Implements: D2, D3; brief C (cheap items).
- Run by: the implementer; review: the main session's diff review.
- Check: `TestNoFixedSettleAfterTheTap` (the fake records the first read's start ≤ 50 ms after the tap returns); `TestWarmCallIssuesNoGetprop`; `TestTransportChangeDropsTheCache`; step 6's crash tests rerun under the new timing; seen failing with the 300 ms constant restored and the cache keyed by serial alone.
- Schema: description-only diff of `device.tapElement` and `device.inputText` (the "waits 300 ms" sentence).
- Needs: step 6.
- Not here: `track-devices` instead of `devices -l` per call (speed note R3, optional; kept as is).
- Rests on: —
- [ ] Done. Evidence:

### 8. Error texts agents can act on

- Builds: `internal/domain/device/resolve.go`, `internal/domain/emulator/resolve.go`, `internal/domain/build/gradle/discover.go`: each "Tried:" candidate names its source. D6's codes' nextSteps rewritten where they miss D6's rule.
- Implements: brief F; D6.
- Run by: the implementer; review: the main session's diff review.
- Check: the existing resolution tests assert "tools.adb in <path>", "$ADB", "SDK", "PATH" in the cause; one assertion per D6 code in the test that already produces it (the capability named, "safe to repeat" or "do not repeat"); seen failing on today's texts.
- Schema: no change (no description touched).
- Needs: step 7.
- Not here: rewriting capability names to MCP tool names in errors.
- Rests on: —
- [ ] Done. Evidence:

### 9. The device path end to end

- Builds: no repository file; `$B/l5-<stamp>/`.
- Implements: A2–A6 at level 5.
- Run by: the main session under K1–K3, on both AVDs (A6 on `droidyard_m1n` only).
- Check: (a) D5's two pairs, medians and spread recorded; pass per A6; as a secondary, unthresholded figure, `set_text` against a bare `input text` on the same field. (b) Golden sample, twice, once with no `expect` and once with `expect: [foreground: …]`: `tapElement {text: "Crash"}` → `appCrash` with `IllegalStateException` "golden app crash" and `GoldenListScreen.kt` in `frames`; the next `uiTree` returns a tree within A2's bound, no restart logged; a third crash within a minute → `systemDialog {kind: "crash"}` on the next read. (c) Formapp `freeze` then a tap: `systemDialog {kind: "anr", package: "dev.droidyard.formapp"}` on a read; a `tapElement` on another formapp element refused with `details.systemDialog`; a `tapElement` on the dialog's wait button dismisses it. (d) A5's string, read back, once. (e) One OpenCode golden-path run with K1's kit copy (`dy` arm, this commit): completes, and its `device_inputText` and `device_tapElement` durations recorded against m1m's medians (7.3 s, 5.5 s), reported, not thresholded. K3 unchanged.
- Schema: no change.
- Needs: steps 3–8.
- Not here: the comparison (m1o).
- Rests on: —
- [ ] Done. Evidence:

### 10. Close, and the known-limits register

- Builds: by the main session: `docs/roadmap/known-limits.md` per D4, seeded from every plan's §Unverified and §Not in this milestone, the ADRs' consequences that state limits, the documents' §Open questions, and this plan's §Not in this milestone; `docs/README.md` (its map and §Where the work stands); `docs/plans/README.md`; `.claude/skills/milestone-workflow/SKILL.md` (the Close row); `m1m-emulator-robustness.md` §Changes gains a dated entry: its shell `input` 3.4 s was a compound tap, sleep and dump (`daily-use-speed-input-2026-10.md` §1); `verify-daemon` gains an m1n block (step 9's commands). The scratch AVDs deleted.
- Implements: brief I; A11.
- Run by: the main session; review: the Opus verifier over A1–A11.
- Check: `go test ./internal/doccheck` green; every row of the register links a source that exists (the link check); each item of this plan's "Not now" bullet has a register row.
- Schema: no change.
- Needs: steps 1–9.
- Not here: m1o's planning beyond its existing plan.
- Rests on: —
- [ ] Done. Evidence:

## Not in this milestone

- Tokens (D), harness integration (E) and the comparison rerun: `m1o-tokens-and-harness.md`. Functional gaps (G) and H: `m1p-functional-gaps.md`.
- The brief's "Not now" list (owner, 2026-10-07): the windowed-emulator hang and `headless` by default; `device.list` silent under an invalid configuration; `tools.adb` bypassing an `ADB` guard; trusted-root code execution, `sdk_gphone` spoofing, shared serials, `setsid` escapes; the concurrency and process limits; alias paths, root watch, 232-byte names; Linux, a full physical-device pass, the KGP ≥ 2.4 fixture, idle footprint; configuration-v1 deferrals; `emulator.stop {avd}`; the documents' Open questions. All seeded in the register at step 10.
- Cut by this plan: clipboard paste for non-ASCII on fields that refuse `ACTION_SET_TEXT` (Q1 alternative a); `InputManagerGlobal` injection (Q1 alternative c); the reader-side error poll (crash note R4); a crash report at `device.launch` or between calls (needs per-device foreground state); rerunning G4 with Qustodio (connect flags unchanged unless step 2 (b) says otherwise); `track-devices` (speed note R3).
- If step 2 (d) supports H2 (an open accessibility connection slows each key), ADR-0030's trade-off reopens; it goes to the owner as a finding under Changes, and nothing here changes it (`set_text` sends no keys).

## Design questions

### Q1. May the reader act, and how is its socket fenced?

- Blocks: steps 4, 5, 9.
- The documents say: ADR-0029 and ADR-0030 describe a reader that reads; ADR-0030 §Decision: "No authentication on its socket", the owner's call, and its Alternatives reject "a token or a peer-uid check on the socket" as cost without a threat, for reads. `ui-automation.md` §Typing: delivery "chosen by the text itself"; `input text` for ASCII, ADBKeyboard otherwise. Plan m1f D12 rejected an installed IME. Nothing decides injection from the reader.
- Draft decision, a new ADR (next number after Q2's), written after step 2 from its facts, "The reader acts: taps, keys and set-text, fenced by its peer's uid", amending ADR-0029 and ADR-0030:
  1. The reader answers `tap`, `keys` and `settext` besides `ping`, `dump`, `quit`: injection through the `UiAutomation` it already holds (public API), `ACTION_SET_TEXT` on the focused input node with the text read back in the same request, on one acting thread. Each is bounded on the device below the host's bound, and its answer says whether anything was executed.
  2. **Fence.** An acting request is executed only when the connecting peer's uid is 0 or 2000 (`LocalSocket.getPeerCredentials()`): adbd, through which the daemon's forward arrives; any other peer gets `denied`. Reads keep ADR-0030's rule (no check), the owner's decision; extending the check to them is one line if the owner wants it. On the host the forward listens on 127.0.0.1, where any local process can already reach the adb server and run `adb shell input`, so the host gains no new power; the daemon sends acting requests only for a call that passed authorization (ADR-0015 §2, §4).
  3. **Delivery for `inputText`**: `set_text` for any text when the reader runs and the focused node accepts the action (`clear` → replace; otherwise insert at the selection, hint text read as empty); `keys` for printable ASCII when it is refused, for a password field without `clear`, or when the caller passes `method: "keys"`; `input text` and ADBKeyboard only when the persistent reader cannot run; otherwise `DEVICE_TEXT_NOT_DELIVERABLE` before anything is tapped. **Only `denied` or a refused `ACTION_SET_TEXT`, both given before anything was executed, may lead to another method** (after `denied`, the path used when the reader cannot run). An acting request answered `busy` (an earlier one is still pending on the device) fails the call `DEVICE_NOT_READY` before acting, with nextStep "read the field or screen before acting again", so a pending effect is never followed by a second delivery across calls. Any other outcome (a timeout, an EOF, a bound passed after execution began) ends the call `completed` with a warning that it may have applied, and nothing is retried (ADR-0020's 2026-10-06 line). A replacing set is idempotent, so a late-applied or repeated one cannot type twice; an insert and keys are not, which is why they are never repeated. A read-back that differs is reported, never retyped. `tapElement` taps through the reader under the same rule, `input tap` only without it.
  4. Accepted differences from typing: filters and watchers run once on the whole value, IME behaviour (autocorrect, suggestions) does not run, and per-key fields (OTP boxes) may truncate; the read-back shows it and `method: "keys"` types instead.
  - Consequences: `ui-automation.md` §Typing, §Input primitives, §Sequence and timing; the `inputText` param and result enum (API 0.11.0); an on-device app that could reach the socket can still read the UI, as before, but not act.
  - Alternatives considered: (a) clipboard plus `KEYCODE_PASTE` (mobilecli): overwrites the user's clipboard, shows Android 13+'s overlay, not idempotent; wins for non-ASCII into fields that refuse `ACTION_SET_TEXT`. (b) An installed IME: rejected by plan m1f D12. (c) `InputManagerGlobal.injectInputEvent` by reflection: faster per event (reported 4–6 ms against tens), a hidden API that moves between releases; wins if step 9 shows the public path slow. (d) A token on the socket: needs a secret on the reader's command line, visible to the shell user's `ps`; the uid needs none; wins if an image hides peer credentials. (e) Keep `input` and fix only its timing (speed note R2–R4): non-ASCII still needs an install, and the typed-twice class stays. (f) Acting without a fence: an on-device app reaching the socket would inject events.

### Q2. What does a device call say about a busy app, a crash and a system dialog?

- Blocks: steps 3, 6, 9.
- The documents say: `ui-automation.md` §Result ("After the tap, never an error"; `failedDumps`), §Open questions (the ANR and the reader after a crash are open); `adb-device.md` §Logs (the crash buffer, m1i D6); the reader's restart rule is plan m1f step 7's, not ADR-0030's text. No document names a crash or a dialog in an action's result.
- Draft decision, a new ADR (next number), written after step 2 from its facts, "Device calls report a busy app, an app crash and a system dialog":
  1. **Busy.** The reader bounds each read at 2.5 s on the device and answers `busy`; it answers `ping` while a read runs; a read arriving while one worker is still blocked starts a second worker, and with two out it answers `busy` at once. The daemon never restarts a reader that answers `ping`, and a busy read goes to no fallback reader. A busy read is a failed read: before an action, `DEVICE_UI_DUMP_FAILED` whose cause says the foreground app did not answer accessibility queries, with the package in `details` and the error marked untrusted (`cause` never quotes device output, `capability-api.md` §Error model), and whose nextStep says to read again in a few seconds; after the action, a failed poll (`failedDumps`), as today. No new error code.
  2. **Crash.** After a `tapElement` or `inputText`, when the foreground left the app or no read observed it, and the app's process is gone, the result carries `appCrash`: from the crash buffer since the action's start (exception, message, up to five frames), else from `exit-info` (the reason). The reads it needs are paid only on that path.
  3. **System dialog.** A read whose tree holds the system's error-dialog buttons sets `systemDialog {kind: "anr" | "crash", package?}` on `uiTree`, `tapElement` and `inputText`, the package from the events buffer. An action whose target is inside the dialog proceeds; one whose target is elsewhere, and an `inputText` with no target, fails `DEVICE_NOT_READY` before acting, with `details.state: "systemDialog"` (a new value of that code's state list, `adb-device.md`) and `details.systemDialog`, the error marked untrusted; its nextStep names the dialog's buttons for `tapElement`, or waiting. Droidyard never dismisses a dialog itself: the agent decides (ADR-0004).
  4. The fields are optional and tagged untrusted; the API's minor version moves.
  - Alternatives considered: `am monitor` or a reader `IActivityController`: unsafe below API 34, and the single controller slot is cleared by `device.launch`'s `monkey` (crash note §4, "Do not adopt now"). Automatic dismissal and relaunch (agent-device): hides the state and is a harness decision. A new `DEVICE_APP_NOT_RESPONDING` code: one more code to learn where the cause suffices; wins if agents misread the cause. Tapping through a dialog: the tap lands on the dialog, an effect the agent did not ask for. A single worker that answers `busy` until it returns: no tree for up to the platform's 5 s query timeout after a crash. A reader-side `getProcessesInErrorState()` poll (crash note R4): a hidden API; wins if the adb reads prove late.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| The 8.7 s post-crash read was the daemon killing a reader blocked on the app, not the reader dying | 3 | step 2 (a) |
| A query blocked on one reader thread blocks neither `ping` nor a second worker | 3 | step 2 (e), step 3 |
| The ANR dialog's buttons reach the tree; the root comes from the dialog while it shows | 6 | step 2 (b) |
| `exit-info`'s format on API 31 and 36.1 | 6 | step 2 (c) |
| A forwarded connection's peer uid is 2000 (0 under root) on both images | 4 | step 2 (e), step 4 |
| An ordinary app can reach the abstract socket (else SELinux fences it before the uid check does) | 4 | step 2 (e)'s probe activity |
| `ACTION_SET_TEXT` updates a Compose `TextField`'s state on both images | 5 | step 2 (e), step 5 |
| A synchronous injected tap into a frozen app may land after the reply | 4, 5 (the "may have applied" rule covers it) | step 2 (e), step 4 |
| Qustodio stays undisturbed (G4) with a reader that acts | none: connect flags unchanged | level 3 (code); rerun if step 2 (b) changes the flags |
| The typing hypotheses H1–H3 | 7 (its value) | step 2 (d) |
| The first read after a crashing tap never shows the app still in front while its process dies (else no `appCrash` without `expect`) | 6, 7 | step 2 (a) records it; step 9 (b) |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 2 | revise | B1: `busy` on an acting request permitted another method, so a pending effect could be followed by a second delivery in the next call | Q1 ¶3: only `denied` or a refused `ACTION_SET_TEXT` lead to another method; acting `busy` fails `DEVICE_NOT_READY` before acting; `TestBusyActingThreadFailsBeforeActing`; A8 |
| 2 | revise | B2: the reader's 256-byte request cap would cut `settext`/`keys` silently | step 4: a 4 KB cap for acting requests, longer refused before executing; level 5 with 500-character strings and a 5 KB request |
| 2 | revise | B3: the busy cause quoted the device's package; error `details` unmarked | Q2 ¶1, ¶3: generic cause, package in `details`, errors marked through `Call.Untrusted`; asserted in steps 3 and 6 |
| 2 | — | N1 wrong schema declarations (steps 6, 7); N2 `DEVICE_NOT_READY`'s state list, no-target input, nextStep; N3 the spike dex unreviewed; N4 Blocked mechanics, Q1 the owner's; N5 an early first read; N6 `since` for `uiTree`; N7 masked password read-back; N8 `submit`, tap after `denied`; N9 a secondary speed figure | all taken: steps 6, 7 Schema; Q2 ¶3, step 6, D6; step 2's spike dex read, kept, deleted; header; Unverified and step 2 (a); step 6; step 5 (length); step 4 and Q1 ¶3; step 9 (a) |
| 1 | revise | B1: an acting request with no answer (timeout, EOF, bound passed) could fall back to another method; "set text is idempotent" is false for insert and keys | Q1 ¶3: only a definitive answer before execution allows another method; otherwise `completed` with a "may have applied" warning; idempotency limited to replace; step 4's answers say whether anything ran; three tests in step 5, one in step 4; A8 |
| 1 | revise | B2: Q1 was to be written "from step 2's facts", which held none of them | step 2 (e): a throwaway spike dex outside the repository tests peer uid, app reachability, `settext` on both fields, injection timing and a second-thread dump before Q1 is written |
| 1 | revise | B3: A2 passed with a failed read; one worker answering `busy` could make a tree within 3.5 s impossible | A2 needs a tree; step 2 (a) measures the block over the forward and sets the bound; Q2 ¶1 allows a second worker |
| 1 | revise | B4: formapp is not debuggable, so `run-as` cannot run, and its domain is not an app's | formapp's probe activity connects from its own uid and domain (step 2, step 4); `run-as` dropped |
| 1 | revise | B5: a dialog on the before read was undecided, and the test contradicted A4 | Q2 ¶3: a target inside the dialog proceeds, any other fails `DEVICE_NOT_READY` with `details.systemDialog`; tests renamed; schema declared |
| 1 | revise | B6: the crash trigger needed an after read that D2 and busy reads can remove | trigger "foreground changed or not observed"; `TestCrashReportedWhenNoAfterReadObservedIt`; step 7 reruns step 6's tests; step 9 (b) with and without `expect` |
| 1 | revise | B7: D5's expectation could never hold on the form screen | D5 without expectations on either side, alternating texts |
| 1 | — | N1 busy skips fallbacks; N2 repeat-crash dialog skews timings, crash dialog unchecked; N3 acting thread; N4 no password field; N5 m1m's kit and AVD; N6 `date` per action; N7 the grep's source; N8 G2; N9 the dialog id; N10 H2 | all taken: step 3; step 2 (a) spaced, step 9 (b) third crash; step 4 acting thread; formapp password field; K1's kit copy; D7; step 10's check against this plan; G2 in steps 3, 4; A4 and step 6 use step 2 (b)'s ids; §Not in this milestone |
Two rounds ran (round 1: B1–B7; round 2: B1–B3, all from the rewrite); every blocking finding of both is answered in the plan text, so a further look is a diff-only check by the main session (`milestone-plan` §Writing one, item 7). The status stays `Blocked` on Q1 (the owner's) and Q2; once both are answered it becomes `Reviewed`. The index row, added by the main session in the plans' commit: `| [m1n-daily-use-core.md](m1n-daily-use-core.md) | 1n — the daily-use core: crashes, ANRs, typing and speed | Blocked |`.

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.
