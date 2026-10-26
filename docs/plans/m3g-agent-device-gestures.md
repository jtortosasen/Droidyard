# Plan: Milestone 3g — Agents act on the device: gestures, keys, system panels and adb verbs

- Status: Approved
- Approval: main session, Delegated (2026-10-26, the owner's standing instruction to apply the recommended option): Q1–Q7 as recommended, recorded in ADR-0133; round 2's R2-B1 answer (the space-free live payload `P`, its positive control, step 6 (c)'s accepted call with the file absent, the wholeness an Unverified row) judged sufficient by the main session, no third round.
- Date: 2026-10-26
- Milestone: the owner, 2026-10-26 (in Spanish, translated): an agent driving a Samsung over Droidyard's MCP could not pull down the notification shade or quick settings over a third-party lock screen, nor tap the Settings gear there, because agents have no gesture tool and `shell.exec` is disabled; "implement this, and all the gestures, or more things an agent could use over adb, so it is not so limited". Read as: agents get coordinate tap, long press, swipe along a path and the system keys over MCP with no live view open; the notification and quick-settings panels; force-stop, rotation and a deep link; on local and lent devices. `docs/README.md` §Where the work stands says "next, the owner's choice".
- Phase: none (Phase 3, use and polish). The Scope table is the boundary.
- Contract: ADR-0079 (§1–§3, §5), ADR-0117 (§3–§5, §7), ADR-0035 (§1–§3), ADR-0036 §1, ADR-0060 §2, §4, ADR-0094 §2, ADR-0099 §2, §4, ADR-0100 §3, §4, ADR-0104 §2, ADR-0015 §1, §4, §6, ADR-0063; `ui-automation.md` §Input primitives, §Acting, §Design rules; `adb-device.md` §Capabilities, §Policy and safety; `mcp.md` §What is exposed, and what is not, §Annotations, §Descriptions. New: ADR-0133 (reserved for 3g), drafted below.
- Register: the plan's commit sets `Work` 3g on row "Agents watching the screen or acting by coordinates over MCP" (C5), split: acting by coordinates to 3g, watching stays C5 as its own row; and on row "`device.backtrace`, `device.forceStop`, `device.processes`" (v1-device), split: `forceStop` to 3g.
- Parallel: step 3 beside step 2. Both edit package `internal/domain/device`, so step 2 runs with `isolation: "worktree"` (its files: `reader/`, `persistent.go`, the actor in `tap.go`, `uitree.go`, a new test file; m3g's code equals main's then) and is merged into `m3g` after step 3's commit.
- Another lane: none in flight; API `0.39.0` on main, so this milestone takes `0.40.0`, checked again at the close.
- Devices: every device check runs on the lane's own scratch AVDs (`B=~/Library/Caches/droidyard-dev/3g`, AVDs under `$B/avd`, an adb port and emulator ports of its own, checked free with `pgrep -fl qemu-system` and the board), never the owner's phone or AVDs. Samsung behaviour is Unverified.
- Review: one Opus verifier over steps 2–5 (the reader's fence and bounds, the contract, the URL into the device shell) before step 6, and one at the close.
- Token cost, estimated, measured by `TestToolListModelFacingBytes` at each step and recorded in its evidence: three tools newly exposed about 2.7 KB, three new tools about 2 KB, `expect` on three tools about 3.3 KB, `url` about 0.15 KB: about 8–9 KB over today's 38,249 B, 47 tools. `CoreTools` and the server instructions are unchanged.

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| Coordinate tap and long press, swipe along a path, system keys, for agents, with no live view | yes | Q1; steps 2, 4 |
| The screen size an agent normalises by | yes | Q1 (6); steps 2, 4 |
| More keys for `device.pressKey` | yes | Q2; step 4 |
| Notification and quick-settings panels | yes | Q3; step 3 |
| `device.forceStop` (row 152 in part), rotation, a deep link | yes | Q4; step 3 |
| `expect` on the coordinate actions | yes | Q5; step 5 |
| Lending of every new or changed device capability | yes | Q6; steps 3, 4, 6 |
| The policy fence; `shell.exec` stays disabled | yes | Q7; steps 3, 4 |
| Everything in §Not in this milestone | no | there, with where each lands |

## Starting point

- Branch `m3g` at `df6d151` (main), API `0.39.0` (`internal/domain/system/system.go`).
- Inputs: `internal/domain/device/screeninput.go` (`device.tap`, `swipe`, `pressKey`, `scroll`: audience human, no `Occupies`, `adb.authorize`, `adb.plain`; only `src.Inject` on a running view, else `DEVICE_NO_STREAM`); `screen/input.go` (`EncodeControl`, no moves between points; `keycodes`, five keys).
- The reader: `reader/src/DroidyardReader.java` (acting verbs `tap`, `keys`, `settext`; `ACTING_VERBS`; `actBoundMs`; `display(Point)`, `Integer.MAX_VALUE` when it fails), `reader/reader.go` (`SHA256`), `reader/build.sh`; host side `persistent.go` (`actOnReader`, `classifyAct`, `readerActDeviceBound`, `readerActHostBound`), `tap.go:480-531` (`actor`, `locked`), `tap.go:435` (`inputTap`).
- The tree: `uitree.go:253` sets `display` to the first root's extent (a dialog's, when a dialog is the active window); `parseDump` reads `<hierarchy rotation>`.
- `tap.go` (`pinConditions`, `holds`, `verifyAfter`), `launch.go` (`applicationIDRE`, `monkey`, `pollPID`, `runShell`), `input.go` (`inputTextChunks`).
- Agent surface: 41 tools, `maxToolListBytes` 39,000 (`cmd/droidyard/toollist_bytes_test.go`), `CoreTools` (`adapters/mcp/mcp.go:69`), `safetyExceptions` (`internal/schema/descriptions_test.go`), hints (`internal/assistant/observe.go`), the generated skill tool map (`go test ./cmd/droidyard -update`).
- Lending: `TestDeviceLendableSet` (`lending_test.go:163`), `TestRemoteDevices` (`cmd/droidyard/remote_devices_test.go:308`); level-5 precedent m2y K1 (a peer pair on one Mac, a scratch emulator lent through m2t step 9's stand-in guard). Real-device test precedent: `TestRealScreenSource` (skipped unless `DROIDYARD_SCREEN_SERIAL`).
- **Baseline, 2026-10-26 at `df6d151`:** `go vet ./...` clean. `go test ./...`: `internal/domain/chats` `TestSQLiteBounded` and `internal/domain/shell` `TestRecordsTheCommandWhileItRuns` failed in the full run and passed alone with `-count=3`; the reviewer's full run passed. Load-sensitive, not red.

## Acceptance

- [ ] A1. **The owner's case**: an MCP agent with no live view pulls the shade by `device_swipe` from the top edge, opens quick settings with `device_systemPanel`, and taps a quick-settings control by coordinates, each with `expect` `appears` of a node only that panel or screen shows and `vacuous` empty, on API 31 and 36 scratch emulators, unlocked and over a swipe keyguard; over a PIN keyguard the outcome is recorded. Source: the owner; Q1, Q3, Q5. Proven by: steps 2–6.
- [ ] A2. **Delivery**: a view open → its stream (ADR-0117 §4's finger); else the reader; else `adb shell input` for a tap, a long press, a two-point swipe and a key; a longer path refused before acting; nothing the reader may have taken is sent again. Source: Q1; ADR-0035 §3. Proven by: steps 2, 4, 6.
- [ ] A3. **Coordinates**: normalised 0..1 of the screen, whose size `device_uiTree` reports as `screen` (the reader's real size, or on the `uiautomator` fallback the screencap header); a tap inside a dialog lands where the agent computed. Source: Q1 (6). Proven by: steps 2, 4, 6.
- [ ] A4. **Keys**: Q2's list on every path; the view's wire unchanged. Source: Q2. Proven by: steps 4, 6.
- [ ] A5. **System verbs**: `device.systemPanel`, `device.forceStop`, `device.rotate`, `device.launch`'s `url`, with fixed commands and validated arguments; a live shell payload in a valid URL runs nothing on the device. Source: Q3, Q4. Proven by: steps 3, 6.
- [ ] A6. **Expect**: `expect` and `timeoutMs` with `device.tapElement`'s meaning; no read without `expect`. Source: Q5. Proven by: steps 5, 6.
- [ ] A7. **Lending**: each capability this milestone adds or changes is `Lendable` and run by the host for `<remote>/<serial>`. Source: Q6. Proven by: steps 3, 4 (level 4), 6 (level 5, stand-in guard).
- [ ] A8. **Fence and contract**: admission as `device.tapElement`'s; an MCP session occupies the device it acts on; no caller text reaches the device shell but a validated `applicationId` and a validated, single-quoted `url`; `shell.exec` untouched; one minor (`0.40.0`); the schema diff is exactly steps 3–5's; 47 tools within `maxToolListBytes` raised by ADR-0060 §4's rule; descriptions within ADR-0036 §1. Source: Q7; ADR-0015 §4; ADR-0094 §2; ADR-0036. Proven by: steps 3, 4, 5.

## Steps

### 1. The facts the steps rest on (spike, main session)

- Builds: no product code. The main session writes `docs/research/spike-agent-gestures-2026-10.md`, each fact observed or "not observed, because". Two scratch AVDs (API 31 and 36 google_apis arm64, images already in the owner's SDK, read only) under `ANDROID_AVD_HOME=$B/avd`, booted headless on the lane's ports; plain `adb -P <own port>`, no daemon.
  - (a) **The shade by injection**: `input swipe <W/2> 0 <W/2> <0.6H> 300`, and the bare version (`input motionevent DOWN`, one `MOVE`, `UP`, where it exists), in four states: unlocked; a swipe keyguard (`input keyevent 223`, `224`); a PIN keyguard (`locksettings set-pin 1111`, cleared after); expansion disabled (`cmd statusbar send-disable-flag statusbar-expansion`, a stand-in for a kiosk or lock task, cleared after). Seen by `dumpsys window` (`mCurrentFocus`) and a screenshot; and, open, the resource-ids of nodes only the shade and only quick settings show (for step 6's `appears`), from `uiautomator dump`, each checked absent in every closed state, both keyguards included.
  - (b) **`cmd statusbar`** `expand-notifications`, `expand-settings`, `collapse` as shell, in the same four states: exit status, output, effect.
  - (c) **Keys** by `input keyevent`: Q2's codes and their effect.
  - (d) **Rotation and size**: `settings put system accelerometer_rotation 0`, then `user_rotation 1`: the display rotates (`dumpsys input` `SurfaceOrientation`); `wm size` against the on-device screencap header (`screencap | head -c 8 | od -An -tu4`, and its time) in both orientations; restored.
  - (e) **Force-stop and deep link**: `am force-stop` of a running app and of an absent package; `am start -a android.intent.action.VIEW -d '<url>' -p <pkg>` for a URL an installed app resolves (recorded for step 6), one nothing resolves and an absent package: exit status, decisive line, and how `am` prints `dat=` (the secure form hides path and query). **Positive control**: `P = https://x.example/a?b='c';touch${IFS}/data/local/tmp/x;'$(id)` (bytes 0x21–0x7E only), wrapped naively in `'…'` without escaping its `'`, through `adb shell am start … -d '<P>' …`, creates `/data/local/tmp/x` (then removed): the payload is live, so its absence later proves the quoting.
  - (f) **Cost**: `adb shell input tap` and `input swipe`, ten runs each per level, the median.
- Implements: risk first: Q1 rests on (a), (f); Q3 on (b); Q2 on (c); Q4 on (d), (e); Q1's fallback size on (d).
- Check: the note answers (a)–(f); before steps 2 and 3 start, the main session records under Changes what redrafts a step: injection not opening the shade unlocked on either level stops the milestone for the owner; the bare version failing where `input swipe` works makes step 4 add moves to the stream encoder too (Q1 (3)); `cmd statusbar` refused to shell redrafts step 3 (Q3 amended); `user_rotation` ignored on 36 redrafts step 3 to `cmd window user-rotation lock`; the header unreadable on the device redrafts step 4's fallback size.
- Schema: no change.
- Needs: the owner's SDK images and `emulator` binary, read only.
- Not here: the reader's own injection (step 2's real test); a physical device.
- Rests on: —
- [x] Done. Evidence: [`spike-agent-gestures-2026-10.md`](../research/spike-agent-gestures-2026-10.md) answers (a)–(f) on API 31 and 36 (2026-10-26); injection opens the shade unlocked on both, so the milestone goes on; what it redrafts is under Changes.

### 2. The reader injects paths and keys, and reports the screen (Q1)

- Builds: in an isolated worktree (see Parallel):
  - `DroidyardReader.java`: verbs `gesture` and `press` in `ACTING_VERBS` (ADR-0035 §2's fence), parsed on the accept thread (`refused malformed` before anything runs):
    - `gesture <n> <x1> <y1> <t1> … <xn> <yn> <tn>`: 2 ≤ n ≤ 64, coordinates 0..65535 (the view wire's normalisation), `t` ms, the first 0, strictly increasing, at most 5,000. On the acting thread: `display(Point)` read once; a failed read answers `refused nosize` with nothing injected; pixel `min(round(v·size/65535), size−1)`; `ACTION_DOWN` at the first point, `ACTION_MOVE` along straight segments at least every 16 ms and at each point, `ACTION_UP` at the last `t` (DOWN and MOVE asynchronous, UP synchronous, `SOURCE_TOUCHSCREEN`); nothing once the bound has answered (N1); a bound passing mid-path injects `ACTION_CANCEL` at the last point injected and stops (ADR-0117 §5's rule).
    - `press <keycode>`: 1 ≤ code ≤ `KeyEvent.getMaxKeyCode()`, down and up, synchronous, `SOURCE_KEYBOARD`.
    - `actBoundMs`: `gesture` the base plus its last `t`; `press` the base.
    - Every `dump` writes `screenWidth` and `screenHeight` (the real size, rotation applied) on `<hierarchy>` beside `rotation`, only when `display()` succeeded.
  - `reader.dex` rebuilt by `build.sh`; `SHA256` updated.
  - `persistent.go`: `gestureRequest`, `pressRequest`, `actGesture`, `actPress`; `nosize` a refusal token that executed nothing; the device and host bounds take the request's span (one formula, the host's margin above). `tap.go`: `actor.gesture`, `actor.press`. `uitree.go`: `parseDump` reads the two attributes into an unexported snapshot field (published at step 4, so this step's golden file is unchanged), ignoring absent, non-numeric or non-positive values.
  - `reader_gesture_real_test.go`: `TestRealReaderGestures`, skipped unless `DROIDYARD_READER_SERIAL`: through the real `ADB` and the actor, a 300 ms `gesture` from (0.5, 0) to (0.5, 0.6) then a read whose root package is `com.android.systemui` and holds step 1 (a)'s shade node; `press 4`, then a read without it; a 3,000 ms `gesture` answered `done`; two equal points 800 ms apart on a launcher icon, then a read showing its menu; `press 223`, `dumpsys power` `mWakefulness=Asleep`, `press 224`, `Awake`; every read's `screen` equal to `wm size`'s in portrait.
- Implements: Q1 (2), (3), (4), (6); ADR-0035 §1 as Q1 extends it.
- Check:
  - `TestGestureRequestEncoding`: 0 → 0, 1 → 65535, 0.5 → 32768, `t` in ms, 64 points under `REQUEST_CAP`. Seen failing with pixels sent.
  - `TestGestureBoundGrowsWithThePath`: host bound > device bound > 5 s for a 5,000 ms path. Seen failing with the base bound.
  - `TestActGestureOutcomes`, `TestActPressOutcomes` (the fake socket of `reader_act_test.go`): `done` executed; `denied` another method allowed; `busy` and `refused nosize` → `DEVICE_NOT_READY`, nothing executed; `timeout` and EOF after the write → may-have-applied; `refused malformed` → an error, nothing executed.
  - `TestScreenSizeFromTheReader`: a dialog-rooted reader dump gives `display` the dialog's extent and the screen 1080×2400; a `uiautomator` dump, and a reader dump with `screenWidth="0"` or `"2147483647x"`, give none. Seen failing on today's parser.
  - `go test ./internal/domain/device/reader` reproduces the pinned digest.
  - Real, by the main session on both AVDs: `DROIDYARD_READER_SERIAL=emulator-<port> ADB=<guard> go test ./internal/domain/device -run TestRealReaderGestures -v` passes.
- Schema: no change.
- Needs: step 1; JDK 17 and the owner's SDK for `build.sh` (read only).
- Not here: no capability calls the verbs (step 4); no second pointer; no scroll axis.
- Rests on: UiAutomation-injected paths reach SystemUI's top-edge gesture, level 5 on API 31 and 36; the mid-path cancel is level 3 (read, not provoked).
- [ ] Done. Evidence:

### 3. System verbs: `device.systemPanel`, `device.forceStop`, `device.rotate`, `device.launch`'s `url` (Q3, Q4)

- Builds:
  - `systemverbs.go`: three descriptors, `adb.plain`, command, global, audience all, since `0.40.0`, `OccupiesAct`, `authorizeActing`, resource `writeKey`, errors `ActingErrors` plus `DEVICE_ACTION_FAILED` (new, `ErrActionFailed`), `REQUEST_INVALID_PARAMS`, `OPERATION_QUEUE_TIMEOUT`; parameters validated in `Authorize` before any adb runs:
    - `device.systemPanel {device, panel: notifications|quickSettings|none}` → `cmd statusbar expand-notifications|expand-settings|collapse`; result `{panel}`. Not destructive, idempotent. A non-zero exit or step 1 (b)'s refusal line → `DEVICE_ACTION_FAILED` (the exit status in the cause, the output in `details`, the call untrusted).
    - `device.forceStop {device, applicationId}` → `pidof`, then `am force-stop`; result `{applicationId, wasRunning}`. Destructive (unsaved in-memory state), idempotent.
    - `device.rotate {device, rotation: auto|0|90|180|270}` → reads `accelerometer_rotation` and `user_rotation`, writes `accelerometer_rotation 0` before `user_rotation <0..3>`; `auto` writes `accelerometer_rotation 1` only; result `{previous: {rotation, auto}, warnings?}`. A failure after the first write succeeded is a completed result with a warning naming what was written (`ui-automation.md` §Design rules). Not destructive, idempotent.
  - `launch.go`: `url` (optional): 1–2,048 bytes of 0x21–0x7E beginning with a scheme (`^[A-Za-z][A-Za-z0-9+.-]*:`); with it, `am start -a android.intent.action.VIEW -d <shellSingleQuote(url)> -p <applicationId>` instead of `monkey`; step 1 (e)'s error lines → `DEVICE_LAUNCH_FAILED` (exit 0 included); `launchedAt`, the pid poll and `noteLaunch` unchanged. `shellSingleQuote`: `'…'`, each `'` as `'\''`.
  - `observe.go`: `cmd statusbar` → `device_systemPanel`, `am force-stop` → `device_forceStop`, `settings put system user_rotation|accelerometer_rotation` → `device_rotate`; `hintTools` gains them.
  - API `0.40.0`; `go test ./internal/schema -update`; the web client regenerated (as m3c step 2) and `pnpm -C web run verify`; the skill tool map regenerated; `mvp.md` §MVP capability surface names the three; 44 tools and `maxToolListBytes` by ADR-0060 §4's rule.
- Implements: Q3; Q4; Q6; Q7.
- Check: with the fake adb of `fakeadb_test.go`:
  - `TestSystemPanelCommands`: each panel's argv; a non-zero exit → `DEVICE_ACTION_FAILED`, untrusted. Seen failing with two panels swapped.
  - `TestRotatePinsAutoRotateFirst`: `accelerometer_rotation 0` before `user_rotation 1`; `auto` writes one setting; `previous` from the reads; the second write failing → completed with the warning. Seen failing with the order reversed.
  - `TestForceStopValidatesBeforeADB`: `-rf`, `a;reboot`, `x` → `REQUEST_INVALID_PARAMS`, no adb call; a running app → `wasRunning: true`.
  - `TestLaunchURLReachesTheShellLiterally`: step 1 (e)'s `P` (no space, so it passes validation) is one word in the recorded argv, and `/bin/sh -c "printf %s <word>"` on the host prints the URL unchanged. Seen failing unquoted. Space, a control byte, `ñ`, 2,049 bytes and no scheme → `REQUEST_INVALID_PARAMS` before adb.
  - `TestLaunchURLFailure`: step 1 (e)'s line with exit 0 → `DEVICE_LAUNCH_FAILED`.
  - `TestSystemVerbsOccupy`: another chat holds the device → `DEVICE_OCCUPIED`, no command that changes it.
  - `TestDeviceLendableSet` gains the three; `TestRemoteDevices` subtest: `device.systemPanel` and `device.rotate` on the remote phone run on the host's fake adb for its serial and on none of the client's.
  - `internal/assistant` hint tests; `TestHintToolsAreExposed`.
- Schema: three capabilities; `device.launch`'s `url` and description; `DEVICE_ACTION_FAILED`; `0.40.0`.
- Needs: step 1 ((b), (d), (e)).
- Not here: whether a panel opened (the agent reads; a disabled status bar ignores the command, Unverified); a URL without naming its app.
- Rests on: the device shell receives the URL as one literal word: level 4 here; level 5 at step 6 (c) as "the live payload `P`, accepted by the call, runs nothing" (that `am` gets the URL whole is not observable: it prints `dat=` in its secure form; Unverified).
- [ ] Done. Evidence:

### 4. Agents' coordinate gestures and keys (Q1, Q2)

- Builds:
  - `screeninput.go`: `device.tap`, `swipe`, `pressKey` audience all, `OccupiesAct`, `authorizeActing`; the `device` description loses "Its live view must be open."; `x`/`y` described as fractions of the screen `device_uiTree` reports; tool descriptions within ADR-0036 §1, pointing to `device_tapElement` for elements (identity first). `device.scroll` stays human and stream-only.
  - `deliverInput`, Q1 (1): the running source → `Inject` as today; else the actor → `gesture`/`press`; `denied` → the next path; `busy` or `refused nosize` → `DEVICE_NOT_READY` before acting; may-have-applied → completed with a warning, nothing sent again; else `adb shell input`: `input tap`, a long press `input swipe x y x y <ms>`, a two-point path `input swipe x1 y1 x2 y2 <t>`, a key `input keyevent <code>`, pixels from step 1 (d)'s on-device header; more than two points → `DEVICE_NOT_READY` before anything (cause: the reader cannot run; nextStep: two points, or the live view); an issued command with an unknown outcome → completed with a warning (as `inputTap`).
  - `ScreenInputResult.delivery` (`stream|reader|input`, optional, so an older host's relayed result decodes). `DEVICE_NO_STREAM` leaves the three's errors.
  - `screen/input.go`: `keycodes` gains Q2's keys; moves between swipe points only if step 1 (a) required them. `ScreenKeyParams.Key` is Q2's enum.
  - `uitree.go`: `Snapshot.Screen {width, height}` (optional, untrusted with `display`) published in `device.uiTree`'s and `tapElement`'s results and in the tree's text rendering's header; on the `uiautomator` fallback (no reader can run) it is filled from the same on-device screencap header the `input` path reads, so `screen` is present whenever a tree is, and absent only when that read fails too (the descriptions name `device_screenshot`'s size then).
  - `observe.go`: `input swipe` → `device_swipe`, `input keyevent` → `device_pressKey`.
  - `mcp.md` §What is exposed, and what is not: the row keeps `device.scroll` with its reason; 47 tools; the budget, the skill tool map and the web client as step 3.
- Implements: Q1 (1), (5), (6); Q2; Q6; Q7.
- Check:
  - `TestAgentTapWithoutAView`: an MCP-origin `device.tap` with no view, the fake reader answering `done` → `delivery: reader`, request `gesture 2 32768 32768 0 32768 32768 50`. Seen failing on today's code (`DEVICE_NO_STREAM`).
  - `TestAgentInputPrefersTheView`: a running fake source gets the input; the reader socket sees no connection.
  - `TestAgentInputFallback`: reader unreachable → `input swipe 540 0 540 1440 300` for a 1080×2400 header, the landscape equivalent for 2400×1080; three points → `DEVICE_NOT_READY`, no `input`; `denied` → `input`; `busy` → no `input`; a reader timeout → completed with a warning and no `input`. Seen failing with the fallback taken after a timeout.
  - `TestPressKeyEveryPath`: each name → its code on the stream encoder, in `press`, in `input keyevent`; an unknown name → `REQUEST_INVALID_PARAMS`; the view's five key frames unchanged.
  - `TestUITreeReportsTheScreen`: a dialog-rooted dump's result and rendering carry `screen` 1080×2400 beside the dialog's `display`; on the `uiautomator` path the fake's screencap header gives it. Seen failing with `screen` taken from `display`.
  - `TestInputAudience`: MCP lists the three; `device_scroll` "tool not found" and `REQUEST_NOT_ALLOWED_HERE` in the core. `TestAgentInputOccupies`: `DEVICE_OCCUPIED`, nothing sent. `TestRemoteDevices` subtest: `device.swipe` on the remote phone is the host's call.
  - `TestToolListModelFacingBytes` (47, the new budget), `TestDescriptionsFollowTheRule`, `TestCapabilityNamesAgree`.
- Schema: the three `audience: all`, the key enum, `delivery`, their errors and descriptions; `screen` in the `uiTree` and `tapElement` results.
- Needs: steps 1 ((a), (d)), 2, 3.
- Not here: `expect` (step 5); pinch; `device.scroll` for agents.
- Rests on: an agent's request reaches the device at most once whatever path answers: level 4 (no `input` after a timeout); the reader path is step 2's level 5.
- [ ] Done. Evidence:

### 5. `expect` on the coordinate actions (Q5)

- Builds: `screeninput.go`: `expect` (at most 5 `Condition`s, the selector described once per tool) and `timeoutMs` (1–20,000, default 5,000) on the three. With `expect`: `ready`, a before read (failing → `DEVICE_UI_DUMP_FAILED`, nothing sent), `pinConditions`, vacuous flags, `deliverInput`, `verifyAfter` from the input's start; the result gains `expect`, `foreground` and `snapshot` (untrusted as `tapElement`'s). Without `expect`: no read. No target resolution, keyboard guard, system-dialog refusal or crash diagnosis. The three join `safetyExceptions` under ADR-0036 §1's named categories ("repeat", "vacuous", "two reads"). Schema regenerated; the web client and the skill tool map regenerated and verified as step 3.
- Implements: Q5; `ui-automation.md` §Input primitives, §Design rules.
- Check: `TestCoordinateActionExpect`: `appears` met → `matched: true`; true before → `vacuous`; unmet → completed, `matched: false` with candidates; the before read failing → `DEVICE_UI_DUMP_FAILED` and no `gesture` at the fake reader; no `expect` → no `dump` at the fake reader. Seen failing on step 4's code. `TestToolListModelFacingBytes` with the budget raised by the same rule.
- Schema: `expect`, `timeoutMs` on the three; `expect`, `foreground`, `snapshot` in their result.
- Needs: step 4.
- Not here: `returnTree`; `appCrash`, `systemDialog`.
- Rests on: —
- [ ] Done. Evidence:

### 6. Level 5, the documents, the close

- Builds: no product code.
  - **The run** (main session; `verify-daemon` Launch, Doctor, Cleanup; an isolated daemon from the tree, `ANDROID_ADB_SERVER_PORT` the lane's, step 1's AVDs; evidence in `$B/evidence/l5/`), a scripted MCP stdio session:
    - (a) The owner's case, API 36 then 31, no view: `device_swipe` (0.5, 0) → (0.5, 0.7) over 300 ms with `expect appears` step 1 (a)'s shade node; `device_systemPanel quickSettings`; a quick-settings control's centre from `device_uiTree`, normalised by its `screen`, tapped with `expect appears` a node of the screen it opens; `device_systemPanel none`. Each `vacuous` empty. Again over the swipe keyguard, and over the PIN keyguard (outcome recorded).
    - (b) Delivery: (a) reports `reader`; with the view open in headless Chrome a tap and a `power` key report `stream`; after the run's own reader process is ended by its pid on the scratch AVD and `uiautomator events` started (both the run's, ended after), a tap and a two-point swipe report `input` and a three-point swipe is refused with nothing sent.
    - (c) `device_pressKey` `sleep`, `wakeUp`, `volumeUp`, `home`; `device_rotate 90`, the tree's `display.rotation` and `screen`, then `auto`; `device_forceStop com.android.settings` (`wasRunning: true`); `device_launch` with step 1 (e)'s URL; then with step 1 (e)'s live payload `P` and the same app: the call is accepted (completed, or `DEVICE_LAUNCH_FAILED` carrying `am`'s line, never `REQUEST_INVALID_PARAMS`) and `ls /data/local/tmp/x` finds nothing; a tap inside an `AlertDialog` (a Settings dialog), normalised by `screen`, hits the dialog's button.
    - (d) A lent device: m2y K1's peer pair, the host lending a scratch emulator through m2t step 9's stand-in guard; from the client's MCP session `device_swipe` and `device_systemPanel` on `<remote>/<serial>`; the host's Activity shows client `peer`.
    - (e) The CLI: `droidyard device tap` with no view completes (`delivery: reader`).
  - **After**: zero orphans; the scratch adb server stopped by its port; the AVDs deleted; `rm -rf $B`, recorded.
  - **Documents**: `ui-automation.md` §Input primitives (agents' coordinate actions, delivery, the screen size, `expect`) and §Post-MVP; `adb-device.md` §Capabilities; the register (rows 157 and 152 split as the header says; a row per item of §Not in this milestone; the read lock held for a gesture's travel); `docs/README.md` §Where the work stands; the plans index; the API minor checked against main.
- Implements: A1–A7 at level 5.
- Check: each run's decisive output in the evidence line: (a) `matched: true` with empty `vacuous` and the reads naming the shade and the opened screen; (b) the three `delivery` values and the refusal; (c) the rotation, `wasRunning`, `P`'s call accepted and the file absent, the dialog's button; (d) the host's operation with client `peer`; zero orphans.
- Schema: no change.
- Needs: steps 1–5.
- Not here: a physical device; Samsung; a second level-5 pass.
- Rests on: —
- [ ] Done. Evidence:

## Not in this milestone

Each a register row at step 6 unless said otherwise.

- Agents watching the screen (row 157's other half): C5.
- `device.scroll` for agents: a swipe scrolls; the wheel would need a reader verb and one more tool.
- Pinch and multi-touch: ADR-0117 §4's one finger; the reader's `gesture` would extend to pointers.
- A path of more than two points with neither the reader nor a view.
- A key held long (the power menu) and key combinations.
- Clearing app data, permission grants, `appops`, arbitrary `settings put`, network and battery state, location, `wm dismiss-keyguard`: destructive or policy-like (Q4).
- Opening a URL without naming the app that handles it.
- Crash and system-dialog diagnosis on coordinate actions (Q5).
- Live-view buttons for panels, rotation or the new keys: not asked; no row.
- `device.backtrace`, `device.processes`: row 152, v1-device.

## Design questions

Delegated (the owner's standing instruction to apply the recommended option, restated in the 2026-10-26 brief): the main session writes ADR-0133 from these drafts and lands it with this plan's commit; step 1 may run first, and a fact that contradicts a draft redrafts the steps it touches under Changes before they start.

- **Q1. Coordinate input for agents with no live view** (steps 2, 4). The documents say: ADR-0079 §2–§3, normalised to the stream's frame and delivered only through a running view; §5, agents later; `ui-automation.md` §Input primitives, normalised 0..1, sized by a fresh screencap, the reader first and `input` as fallback. Recommended: (1) the three keep names and parameters, become audience all; delivery is the open view's stream, else the reader's new `gesture`/`press`, else `adb shell input` (a tap, a long press, a two-point swipe, a key); a longer path with neither is refused `DEVICE_NOT_READY` before acting; the result names the `delivery`. (2) The reader sizes coordinates by the display's real size at injection, the `input` path by a fresh on-device screencap header. (3) The reader moves along straight segments, a move at least every 16 ms; the stream encoder only if step 1 (a) needs it. (4) Nothing the reader may have taken is sent again (ADR-0035 §3); `refused nosize` executed nothing and fails the call. (5) `device.scroll` stays human. (6) Agents normalise by the screen `device_uiTree` reports as `screen` (the reader's real size, rotation applied; on the `uiautomator` fallback the on-device screencap header), not `display` (the active window's extent, a dialog's). ADR-0079 rejected the reader for the view because its control socket was already open; with no view open, that reason is gone. Rejected: a control-only scrcpy source (a server start per call, or a device process outside a view's life, ADR-0077); `input` only (a process per call, 0.08–0.72 s and more under load, waiting on the app's UI thread, no timed path); the reader only (nothing when another UiAutomation holds the device); agent-only capabilities beside these (two names for one act); `display` as the size (wrong under a dialog).
- **Q2. Keys** (step 4). Recommended: the five, plus `power` 26, `wakeUp` 224, `sleep` 223, `volumeUp` 24, `volumeDown` 25, `volumeMute` 164, `menu` 82, `search` 84, `tab` 61, `escape` 111, `dpadUp` 19, `dpadDown` 20, `dpadLeft` 21, `dpadRight` 22, `dpadCenter` 23, `notification` 83, `assist` 219; the view's wire (ADR-0117 §3) keeps five. Rejected: a raw keycode (any key, silent state changes, a number to look up); extending the wire (no UI asked).
- **Q3. System panels** (step 3). Recommended: `device.systemPanel {device, panel: notifications | quickSettings | none}` through `cmd statusbar`; the result does not claim the panel opened (a disabled status bar ignores the command), the agent verifies with `expect` on a read or a swipe. Rejected: `KEYCODE_NOTIFICATION` only (no quick settings, vendor-dependent); a `pressKey` value (not a key); a swipe only (the owner asked for both).
- **Q4. Further adb verbs** (step 3). Recommended: `device.forceStop` (destructive); `device.rotate`, auto-rotate pinned off first, the previous state returned, a failure after the first write a completed result with a warning; a deep link as `device.launch`'s optional `url` with its app (`launchedAt`, pid and since-launch kept, about 150 B); `DEVICE_ACTION_FAILED` when the device refuses a command, since no code says so. Left out as rows: §Not in this milestone. Rejected: `device.openUrl` (a second launch path, no pid); `DEVICE_ADB_FAILED` for refusals (it means adb could not run).
- **Q5. `expect`** (step 5). The documents say: `ui-automation.md` §Input primitives, coordinate actions share the `expect` vocabulary. Recommended: `expect` and `timeoutMs` on the three, reusing `tapElement`'s engine, the before read only with `expect`; no resolution, keyboard guard, dialog refusal or crash diagnosis. About 1.1 KB per tool. Rejected: none (a round trip per act); `tap` only (saves about 2.2 KB; a panel's swipe and a back key need verifying too); `returnTree` (one `device_uiTree` call).
- **Q6. Lending** (steps 3, 4). Recommended: every capability added or changed is `Lendable` through `adb.plain`, the host choosing its own delivery; ADR-0099 §2's list gains the three new ones. An older host answers a no-view tap `DEVICE_NO_STREAM` and an unknown capability as missing, both seen as `DEVICE_NOT_READY` (`incompatible`, ADR-0100 §4), and refuses `url`, `expect` and the new key names `REQUEST_INVALID_PARAMS` (strict decoding). ADR-0099 §4: lending lends the whole device already. Rejected: not lending them (the owner's rule; the trigger's device is lent).
- **Q7. The fence** (steps 3, 4). Recommended: device-policy admission as `tapElement`'s (emulators by default; a physical device by `allowPhysical` and its exact allowlist entry, or a lend or join its human made); occupation for MCP sessions; the audit of every operation. A coordinate tap reaches what a finger reaches on an admitted device, as `tapElement` already does, so the fence is admission, not per act. No new shell reach: fixed command words, enums, numbers, the `applicationId` pattern and the validated, single-quoted URL; `shell.exec` untouched. `forceStop` destructive; the others not. A reader gesture holds the device's read lock for its travel (up to about 9 s): reads wait and a recording skips its dumps meanwhile, a register row.

**Draft ADR-0133** (for the main session to record):

> # ADR-0133: Agents act on the device by coordinates, keys and system verbs
>
> - Status: Accepted · Date: 2026-10-26 · Decided by the main session under the owner's standing instruction (Delegated, plan m3g Q1–Q7), at the owner's request of 2026-10-26.
> - Supersedes: ADR-0079 §Decision 1 (in part: audience, keys, result), §2 (in part: without a view, the screen), §3 (in part: delivery without a view), §5; ADR-0117 §Decision 7 (in part: the three change for agents; the UI still does not call them); ADR-0035 §Decision 1 (in part: `gesture`, `press`, the screen size on `dump`); ADR-0099 §Decision 2 (in part: the list gains `device.systemPanel`, `device.forceStop`, `device.rotate`).
>
> **Context.** An agent on a lent Samsung could not pull down the shade or quick settings over a third-party lock screen: the three inputs were human-only and needed a live view, and `shell.exec` is disabled. The reader already injects taps (ADR-0035); `ui-automation.md` reserved normalised coordinate actions with `expect` for agents.
>
> **Decision.** (1) `device.tap`, `swipe`, `pressKey` are agent tools, occupying, delivered down the open view's stream, else by the reader's `gesture` and `press`, else `adb shell input` for a tap, a long press, a two-point swipe and a key; a longer path with neither is refused `DEVICE_NOT_READY` before acting; the result names its `delivery`; nothing the reader may have taken is sent again. (2) Coordinates are fractions of the screen `device.uiTree` reports as `screen`; the reader sizes them at injection, `input` by a fresh screencap header. Paths move in straight segments. `device.scroll` stays human. (3) `pressKey` takes Q2's twenty-two keys; the view's wire keeps five. (4) `device.systemPanel` runs `cmd statusbar` and does not claim the panel opened. (5) `device.forceStop` (destructive), `device.rotate` (auto-rotate off first, the previous state returned) and `device.launch`'s `url` (validated, single-quoted, with its app); `DEVICE_ACTION_FAILED` when the device refuses a command. (6) The three take `expect` and `timeoutMs` with `tapElement`'s meaning, reading only when given. (7) All are `Lendable`; the host delivers. (8) Admission is the device policy's; no caller text reaches the device shell but a validated `applicationId` and the quoted URL; `shell.exec` is unchanged. API `0.40.0`.
>
> **Consequences.** About 8–9 KB more `tools/list` (47 tools), held by ADR-0060 §4's rule; the core profile and the instructions unchanged. A new reader digest is pushed on first use. A reader gesture holds the device's read lock for its travel. An older host refuses the new parameters and capabilities as Q6 says. Documents: `ui-automation.md` §Input primitives, `adb-device.md` §Capabilities, `mcp.md` §What is exposed, and what is not.
>
> **Alternatives considered.** As plan m3g Q1–Q7's "Rejected", each with its reason.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| Injection from the top edge opens the shade on API 31 and 36, unlocked and over a keyguard | 2, 4, 6 | step 1 (a) (`input`), step 2's real test (reader), step 6 (a) |
| `cmd statusbar` runs as shell on API 31–36 and over a keyguard | 3 | step 1 (b); step 6 (a) |
| Samsung (One UI): the gear in quick settings, `cmd statusbar`, the edge swipe | all | not run: the owner's phone is off limits |
| A third-party lock screen (overlay, kiosk, lock task) lets a panel open | 3, 4 | step 1 (a), (b) with the disable flag as a stand-in; the real case not run |
| The reader's real size and the stream frame are one space (cutouts, foldables, multi-display) | 2, 4 | emulators in step 6; the rest not run |
| The mid-path `ACTION_CANCEL` when the bound passes | 2 | level 3; not provoked |
| `user_rotation` takes effect on API 36 | 3 | step 1 (d); step 6 (c) |
| The on-device screencap header on API 24–30 | 4 | step 1 (d) covers 31 and 36 only |
| scrcpy injects the new keys (power, sleep) through a view | 4 | step 6 (b) presses `power` with a view open; the others not run |
| `uiautomator events` holds UiAutomation once the reader is ended | 6 | step 6 (b) itself |
| Lending with a real phone and two Macs | 3, 4 | level 4 and step 6 (d)'s stand-in; the owner's trial |
| `am` receives the URL whole (it prints `dat=` in its secure form) | 3 | not observable; step 6 (c) proves only that a live payload runs nothing |
| The tool-list cost (about 8–9 KB) | 3–5 | measured at each step |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise: 3 blocking, 10 non-blocking, 5 nits | B1 [contract]: agents had no reliable size to normalise by (`display` is the root window's extent, a dialog's). B2 [security]: the hostile URL never reached a device through the product code. B3 [check]: over a keyguard `expect foreground com.android.systemui` is vacuous | B1: Q1 (6); the reader writes the screen size on `dump` (step 2), `device.uiTree` reports `screen` (step 4); `TestScreenSizeFromTheReader`, `TestUITreeReportsTheScreen`, step 6 (c)'s dialog tap. B2: step 6 (c) launches the hostile URL, quotes the whole `dat=` and shows `/data/local/tmp/x` absent. B3: step 1 (a) records shade and quick-settings node ids; A1 and step 6 (a) expect `appears` of them with `vacuous` empty |
| 1 | | N1 the read lock over a gesture's travel. N2 `display()` failing injects off-screen. N3 the Java bound untested; one-point gesture; sleep unobserved. N4 rotate's partial write. N5 `uiautomator events` while the reader holds UiAutomation. N6 steps 2 and 3 in one package. N7 older-host mismatches. N8 cuts. N9 the verifier's span. N10 size | N1: Q7, the ADR's Consequences, a register row. N2: `refused nosize`, `DEVICE_NOT_READY`. N3: a 3,000 ms gesture, two equal points, `mWakefulness` read between. N4: completed with a warning. N5: the run's reader ended by pid first. N6: step 2 in an isolated worktree. N7: Q6 lists them. N8: the disable-flag warning cut; stream interpolation only if step 1 (a) needs it; the `input` fallback kept (Q1's rejection of "the reader only"). N9: the verifier covers steps 2–5. N10: Qs, Starting point and Scope cut; the ADR's alternatives point to the Qs |
| 1 | | Nits: ADR-0079 §2 not superseded and its reader rejection unanswered; step 5's regeneration; descriptions to point at `tapElement`; step 4's Needs and the ADR's refusal and `delivery`; the new keys through scrcpy | All taken: the Supersedes line and Q1's last sentence; step 5 regenerates; step 4's descriptions; Needs step 1 (a), (d), the ADR names both; an Unverified row and step 6 (b)'s `power` |


| 2 (B1–B3 only) | revise: 1 blocking, 2 non-blocking, 2 nits | B1 and B3 resolved. R2-B1 [security]: B2 not resolved: the hostile URL held a space, which step 3's validation refuses, so nothing reached a device shell; and `am` prints `dat=` in its secure form, so "the URL whole" is unobservable | Step 1 (e)'s space-free live payload `P` with a positive control (naive quoting creates the file); step 3's test uses `P`; step 6 (c) asserts the call accepted and the file absent; the wholeness is an Unverified row |
| 2 | | N-a: no `screen` on the `uiautomator` fallback. N-b: a failed size read would publish 2147483647. Nits: the step-2 field unexported; the shade nodes absent in every closed state | N-a: filled from the on-device screencap header (step 4, `TestUITreeReportsTheScreen`). N-b: written only on success, parser ignores bad values, a test case. Nits taken (step 2, step 1 (a)) |

No third round: R2-B1 is answered in the plan (`milestone-plan` §Writing one, item 7); the main session or the owner judges the answer.

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.

- 2026-10-26, step 1's evidence ([spike](../research/spike-agent-gestures-2026-10.md)), before steps 2 and 3 start:
  - **Step 4: the stream encoder interpolates.** The bare version with one `MOVE` opened the shade 0 of 3 on API 36, and with a `MOVE` every 100 px 3 of 3. So `device.swipe` through a view also moves along straight segments at least every 16 ms, as the reader does (Q1 (3)'s "only if step 1 (a) needs it" is met); `TestAgentInputPrefersTheView` checks the moves.
  - **Steps 3 and 6: the live payload is `P = https://x.example/a?b=';touch${IFS}/data/local/tmp/x;'`.** Step 1 (e)'s first payload was not live (naive quoting: `no closing quote`, nothing created). This one, naively quoted, created `/data/local/tmp/x` on both levels, and quoted with `'\''` it created nothing. `TestLaunchURLReachesTheShellLiterally` and step 6 (c) use it.
  - **Step 3: `am start`'s refusal is its line, never its exit.** `Error: Activity not started, unable to resolve Intent` exits 0 on 31 and 1 on 36; both map to `DEVICE_LAUNCH_FAILED`. `cmd statusbar` exits 0 even when expansion is disabled and nothing opens, so `DEVICE_ACTION_FAILED` is for a non-zero exit or an error line only; `device.systemPanel`'s result claims nothing (Q3).
  - **Step 3: `device.rotate`** writes as planned; an app that fixes its orientation (the launcher) keeps the display as it is, so the result reports `previous` only and the description says the foreground app may keep its orientation. Step 6 (c) rotates with Settings in front.
  - **Step 4: the `input` fallback's size** is the on-device header `adb shell 'screencap | head -c 8 | od -An -tu4'`, which follows rotation (2400 1080 rotated); `wm size` does not.
  - **Step 6 (a)'s nodes**: the shade unlocked, `com.android.systemui:id/quick_qs_panel`; quick settings, `quick_settings_panel`. Over a keyguard the shade has no node of its own (the keyguard is the shade window and holds `notification_panel` and `notification_stack_scroller`), so step 6 (a) over the keyguards swipes twice and expects `quick_settings_panel`, which two swipes opened on both levels.
