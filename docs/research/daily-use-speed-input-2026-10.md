# Research: interaction speed and Unicode text input (2026-10-07)

> Status: research note, read-only. Nothing was run on a device. **Observed** means read
> from code or from files recorded on disk; **reported** means a third party's published
> number; **inferred** means my reasoning, not measured. Paths starting `repo:` are under
> `/Users/jorgetortosa/Documents/AioAndroidDev`; `runs:` is
> `~/Library/Caches/droidyard-dev/m1m/runs`; `refs:` is the scratchpad's `refs-speed/`
> (shallow clones, 2026-10-07: maestro 60f9ba4, mobilecli aca77be,
> appium-uiautomator2-server 56bf27a, io.appium.settings bf8c11b, droidrun 1935ada,
> droidrun-portal cc3fa91, ADBKeyBoard d7b2728, agent-device 136eb63).

## 1. The comparison numbers are not like for like (observed)

- The shell arm's "`input` 3.4 s" is **not a bare `input` command**. Every shell call in
  that bucket is a compound Bash call: `input tap …; sleep 1; uiautomator dump; cat`
  (sh1 3.0–4.8 s, sh4 4.1–4.4 s, sh5 3.4–4.6 s; typing is `input tap; sleep 1; input text
  Ana; sleep 1; dump`, 4.6–4.8 s). Source: `runs:sh{1..5}/out.jsonl`, `.part.state.time`
  of the `shell` tool. So the unit the shell arm paid ~3.4–4.6 s for was tap + verify,
  with 1–2 s of fixed sleep and a ~2 s dump. Bare `input tap` time in the shell arm is not
  isolated anywhere.
- Droidyard `tapElement`, 24 non-crash calls in dy1–dy7 (`runs:dy*/out.jsonl`):
  wall − `expect.afterMs` (the before phase plus transport) has a median of ~0.5 s;
  `afterMs` (tap command + 300 ms settle + polls) has a median of **~2.7 s**
  (range 0.44–6.0 s). Most of a tap's time is after the tap.
- Droidyard `inputText`: `afterMs` 1.05–13.4 s, median ~6.6 s, with only 2–3 polls each.
  Two polls cost about 0.6–1.2 s, so **clear plus typing took about 4–12 s**. In five
  calls (dy1, dy4 ×2, dy5, dy6) `input text 'Ana'` timed out on the host at the pre-fix
  5 s bound ("0 character(s) were typed for certain…" warnings in the results).
- On a quiet emulator (`m1m/perf-1617/release-*.json`, scripted, no agent), the same calls
  measured `afterMs` 384 ms for a one-poll tap and 1013 ms for inputText with `clear`
  and two polls. With 300 ms of that being the settle, **`input tap` plus one warm read
  took about 80 ms** there. In agent runs the one-poll taps had `afterMs` of 2.3–3.4 s
  (dy1, dy5 ×2, dy7), and dy2's `input tap` overran the 2 s `actionTapTimeout`. So under
  agent-run load **the device-side `input` command itself grows from about 0.1 s to
  2–5 s or more**; the reads do not.

**Conclusion (inferred from the above):** taps are roughly even with the shell's
tap+sleep+dump unit. The gap is `inputText`, and inside it the `input` commands.
The 300 ms settle and the poll cadence are second-order costs.

## 2. Where the seconds go, per call (code: observed; costs: labelled)

### `device.tapElement` (repo:`internal/domain/device/tap.go:213-319`)

| # | Step | Code | Cost |
|---|---|---|---|
| 1 | `adb devices -l` (+ `adb version` once per binary) | `target.go:70-71`, `list.go:199` | one host spawn: 0.05–0.08 s quiet; 0.6 s under host load (m1f step 9 evidence) — observed |
| 2 | `adb shell getprop sys.boot_completed`, on **every** call | `target.go:160` | one spawn, same as above — observed |
| 3 | Before read over the forward (TCP, no spawn) | `dump.go:137`, `persistent.go:243` | warm median 14 ms, p95 460 ms (spike) — observed |
| 4 | `adb shell input tap x y` | `tap.go:349-352`, 2 s bound `tap.go:27` | one spawn plus the device-side `input`. ~0.08 s quiet (perf-1617, inferred by subtraction), 0.64–0.72 s in spike G3, 2 s or more in agent runs — observed. `cmd input` injects motion with `INJECT_INPUT_EVENT_MODE_WAIT_FOR_FINISH` (AOSP `InputShellCommand.java`, injectMotionEvent, ~l.228 per WebFetch), so it **waits for the app's UI thread** to consume DOWN and UP (inferred link to the load figure) |
| 5 | Fixed settle of 300 ms | `tap.go:29,291`; plan m1f D10 | 300 ms, always — observed |
| 6 | Polls at least 250 ms apart, start to start | `tap.go:31,487-528` | each read on the device runs `ua.waitForIdle(100, 500)` first (`DroidyardReader.java:308`), so while the app animates **each poll can block ~500 ms**. That matches the "~0.6 s" cadence seen in dy6 and dy4's 11 polls in 6.0 s — inferred. `gone` needs at least 2 reads |

Cold-path extras (observed): the first read after a reader start pushes, starts, forwards
and pings (0.8–1.9 s). A reader restart after the app crashed made one tap take 8.7 s
(dy6, `ui-automation.md:313-317`).

### `device.inputText` with `target`, `clear` and `submit` (repo:`input.go:235-390`)

Steps 1–3 as above. Then:

- the focusing `input tap`, plus the 300 ms settle (`input.go:294-301`);
- `getprop ro.build.version.sdk`, **not cached**, on every clear (`input.go:616`);
- `input keycombination 113 29`, then `input keyevent 67` (`input.go:626-629`);
- one `input text '<chunk>'` per 200 bytes (`input.go:446`);
- `input keyevent 66` (`input.go:586`);
- the polls.

That is 7–8 host spawns and 4–5 device `input` processes per call; non-ASCII adds
`settings get` (`input.go:399`). `sendText` injects **every key with WAIT_FOR_FINISH**
(`injectKeyEvent(e, INJECT_SYNC)` in AOSP `sendText`, verified via WebFetch of
aosp-mirror `InputShellCommand.java`). So "Ana" is 6 synchronous injections, each waiting
for the focused window, and through it the IME (inferred: the key goes through
ViewRootImpl's IME stage first).

**The pid-file and nonce wrapper** (`input.go:57-114`): `rm -f; echo "$$ nonce" >
file; exec input …` adds two shell builtins inside the same `adb shell`, so its own cost
is negligible (inferred). Its cost appears only after a host timeout: `awaitDeviceInput`
then issues one more `adb shell` probe every 250 ms, for up to 15 s (`input.go:527-548`).
The wrapper exists because the typing runs as a separate device process that can outlive
its host client; once input moves into a process we already own, that problem goes away
(see R1).

### Why `input text` was 5 s+ for us but under ~1 s in the shell arm (hypotheses, none measured)

- **H1 — timing.** We type 300 ms after the focusing tap, while the soft keyboard is still
  animating in and the IME is binding, cold on a fresh scratch AVD. The shell agents slept
  0.5–1 s first. Each WAIT_FOR_FINISH key waits for the IME and the app.
- **H2 — the persistent `UiAutomation` connection.** While the reader is connected, apps
  see accessibility enabled for its whole life (up to 30 min), not only during a 2 s dump.
  Compose then computes and dispatches semantics events on every text change. More work
  per keystroke on a software-GL emulator.
- **H3 — memory.** The reader's RSS was 85–105 MB in dy6 (`m1m/dy6-monitor.txt`), not the
  spike's 38 MB, with 300–450 MB free.
- **Cheap experiment to decide between them** (level 5, owner's emulator, golden app,
  10 trials each): time `adb shell input text Ana` (a) 0.3 s against 1.5 s after the
  focusing tap; (b) with the reader alive against after `quit`; (c) RSS at start against
  after 30 interactions.

## 3. How others do it

| Tool | Tap path | Text path | Unicode | Extra APK / IME switch | Settle | Latency (labelled) |
|---|---|---|---|---|---|---|
| **mobilecli** (mobile-mcp's engine) | dex via `app_process` (shell uid), hidden `UiAutomationConnection` — the **same architecture as our reader** (`refs:mobilecli/agents/android/java/UiAutomationFactory.java:15-57`). Injects through `InputManagerGlobal.injectInputEvent` by reflection, falling back to `UiAutomation.injectInputEvent` (`InputInjector.java:18-73`). Only the last event is WAIT_FOR_FINISH (`Gestures.java:214-222`, `Input.java:92-94`) | `KeyCharacterMap.VIRTUAL_KEYBOARD.getEvents` with `FLAG_KEEP_TOUCH_MODE` (`Input.java:76-97`) | Clipboard through the `IClipboard` binder as `com.android.shell`, signature-agnostic reflection for API 29+ (`Clipboard.java:9-109`), then `KEYCODE_PASTE`, then clear (`devices/android.go:978-1016`) | **None** | `waitForIdle(500 quiet, caller max)` and node-cache clear (`UiTreeSerializer.java:22-35`) | **Reported** (PR #412, Pixel 9 Pro emulator, median of 30, taps on a dead area so the app's reaction is excluded): tap 20.2→4.0 ms, text 19.9→4.9 ms, button 202.7→6.1 ms. PR #286 (API 33 emulator): tap 45→6 ms. The PR notes `UiAutomation.injectInputEvent` costs "tens of milliseconds per event" against InputManager directly |
| **mobile-mcp** (legacy adb path) | `adb shell input tap` | `input text` | devicekit APK: broadcast to set clipboard, then `KEYCODE_PASTE` (`.refs/mobile-mcp/src/android.ts:420-445`) | devicekit APK, no IME switch | — | — |
| **Maestro** | instrumentation APK + gRPC; `InteractionController.clickNoSync` (`refs:maestro/maestro-android/src/androidTest/java/androidx/test/uiautomator/UiDeviceExt.kt:9-13`); `Configurator` waitForIdle, ack and selector timeouts all **0** (`MaestroDriverService.kt:87-90`) | ASCII per character via `pressKeyCode`, `Thread.sleep(75)` each (`MaestroDriverService.kt:314-330, 516-565`); other characters are dropped by that `when` | Host switches to **its own IME** (`ime enable`/`ime set`), polls a status broadcast every 300 ms (up to 5 s), commits base64 chunks with `commitText` via a receiver that reports `result=0`, waits 250 ms, then restores the previous IME in `finally` (`maestro-client/.../AndroidDriver.kt:547-558, 1447-1475, 1541-1548`; `input/MaestroInputMethodService.kt:58-83`) | APK + IME switch for Unicode | `waitForWindowUpdate(appId, 500)`, then hierarchy equality (≤10 × 200 ms) (`AndroidDriver.kt:716-748`, `utils/ScreenshotUtils.kt:38-73`); screenshot-diff `waitUntilScreenIsStatic` (`ScreenshotUtils.kt:76-97`) | not documented |
| **Appium UiAutomator2** | instrumentation server | `sendKeys` = **`ACTION_SET_TEXT`** on the element, or on the focused node when none is given (`refs:appium-uiautomator2-server/.../utils/ElementHelpers.java:58-71`; `handler/SendKeysToElement.java:62-117`). It appends by reading the current text, clearing, and setting old+new (l.73-85), truncates to maxLength | Native through setText; the W3C key actions fail for characters the virtual KCM cannot map (`utils/w3c/ActionsExecutor.java:108-116`) | None for setText. `io.appium.settings` UnicodeIME (UTF-7 through `input text`) and AppiumIME are legacy or optional and need an IME switch (`refs:io.appium.settings/README.md:239-276`) | `waitForIdleTimeout` default **10 s**, a per-session setting (`model/settings/WaitForIdleTimeout.java:23,40`); speed guides set it to 0 | not documented |
| **droidrun-portal** | accessibility service APK | Own IME first (`commitText`, verified, retries; `input/InputConnectionTextEditor.kt:42-87`), then an **accessibility `ACTION_SET_TEXT` fallback** that splices at the selection and treats text equal to the hint as empty (`service/MobilerunAccessibilityService.kt:954-1004`; `api/ApiHandler.kt:363-401`) | Yes, by either path | APK, an accessibility service the user enables, IME optional | — | — |
| **ADBKeyBoard** | — | `am broadcast -a ADB_INPUT_B64` → `ic.commitText` (`refs:ADBKeyBoard/keyboardservice/.../AdbIME.java:87,148`) | Yes | APK + `ime set` (`README.md:48-49`) | — | Inferred: its receiver sets no result, so `am broadcast` exits 0 even with no input connection. **Our ADBKeyboard path counts that as typed** (`input.go:496-497`) |
| **agent-device** (tester-army) | `adb shell input tap` (`refs:agent-device/packages/platform-android/src/input-actions.ts:34`) | `input text` in **8-character chunks**: "`input text` truncates long strings in some app/IME states (#531)" (`text-input.ts:41-45`). We use 200 bytes | Own test-IME APK: batch broadcast. A whole transaction to record and restore the previous IME, with a crash-recovery marker (`ime-activation.ts`, `ime-restore.ts`; ~2.7 k lines with text-input) | APK + IME switch | idle 100 ms / 500 ms max (prior note) | — |

## 4. Unicode options for Droidyard (no manual install)

**U1. `ACTION_SET_TEXT` from our reader.** The reader already holds a connected
`UiAutomation` (`DroidyardReader.java:257-279`). From it, `findFocus(FOCUS_INPUT)` or the
resolved node, then `performAction(ACTION_SET_TEXT, {ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE})`.

- **Behaviour (verified in source):**
  - `TextView` refuses unless enabled and editable, calls `setText`, and puts the cursor at
    the end (AOSP `TextView.java:14987-15000`, main).
  - `setText` applies `InputFilter`s (incl. maxLength) and fires the TextWatchers'
    before/on/after callbacks (`TextView.java:7351, 7360, 7501`).
  - Compose BasicTextField(state): `setText` → `textFieldState.replaceAll`, refused when
    not editable.
  - Compose legacy value TextField: `DeleteAllCommand` + `CommitTextCommand`, sent as an
    IME command during an active session, refused when readOnly or disabled
    (androidx `TextFieldDecoratorModifier.kt`, `CoreTextFieldSemanticsModifier.kt`, via
    WebFetch).
- **Gains:**
  - any Unicode in one request;
  - no IME and no APK;
  - no WAIT_FOR_FINISH key storm;
  - **idempotent:** setting the same final value twice cannot type twice, which removes
    the D19 "typed twice" class on this path;
  - the result can be verified by `refresh()` and reading the text back in the same
    request, giving exact `characters` with no polling.
- **Contract mapping:**
  - `clear` → set `text`;
  - no `clear` → splice at `textSelectionStart/End` (as droidrun does), treating hint text
    as empty (`isShowingHintText`, API 26+).
- **What breaks (inferred unless noted):**
  - fields that react to keys, not text: OnKeyListener, OTP or PIN boxes that move focus
    on each key, terminals, games, custom Views without accessibility actions;
  - TextWatcher formatters run once on the whole text instead of per character, so
    results can differ;
  - IME-only behaviour: autocorrect, suggestions;
  - password fields: their text reads masked, so splice-append is impossible — set the
    whole value when `clear` is given, otherwise fall back;
  - WebView/Chromium editable nodes: probably supported, not verified;
  - the action returns `false` on non-editable nodes, so there is a clean fallback signal.

**U2. Clipboard + `KEYCODE_PASTE` from the reader** (mobilecli's path, `IClipboard` as
shell). No install, and it uses the real paste path, so it covers fields without
SET_TEXT. Costs:

- it overwrites the user's clipboard (save, then restore or clear it);
- **Android 13+ shows the system clipboard-editor overlay on every copy** (developer.android.com
  "Copy and paste"; 9to5google 2022-04-26). That overlay may cover the lower-left and the
  next tap — inferred;
- `IClipboard` signatures drift across APIs (handled by reflection in mobilecli);
- paste is non-idempotent, like typing.

**U3. Our own IME APK** (Maestro, agent-device, droidrun, ADBKeyBoard). This is the most
faithful path (a real `commitText` into any InputConnection). It is already **rejected by
plan m1f D12** (an installed APK plus a change of the user's IME), and agent-device shows
the restore and crash-recovery burden it brings.

**Rejected (inferred):**
- `input text` with non-ASCII: `KeyCharacterMap.getEvents` returns null for unmappable
  characters, and AOSP `sendText` does not null-check, so it fails (verified in source);
  Appium errors the same way.
- Injected `ACTION_MULTIPLE` key events with a character string: the native KeyEvent
  carries no characters, and the dispatcher validates actions.

## 5. Ranked recommendation for Droidyard

**R1. Make the reader act: `tap`, `key`, `text` and `settext` requests on the existing
socket.** This is the main lever for both problems.

- **How:** inject through `InputManagerGlobal.injectInputEvent` (shell holds
  INJECT_EVENTS), with `UiAutomation.injectInputEvent` as the fallback. Make every event
  ASYNC except the last, which is WAIT_FOR_FINISH, and add `FLAG_KEEP_TOUCH_MODE` on keys
  (copy mobilecli's design).
- **For text:** `settext` (U1), then read back. Fall back to in-reader key injection for
  printable ASCII when `performAction` returns false or the field is a password. Fall
  back to U2 (paste) for non-ASCII. Keep `input text` and ADBKeyboard only when the
  reader cannot run.
- **Expected effect (inferred):**
  - taps and keys drop to milliseconds of injection (reported 4–6 ms) plus TCP;
  - `inputText` becomes: before read + focusing tap + setText + read-back. That is about
    0.1–0.3 s on a quiet emulator, and no longer exposed to the per-key WAIT_FOR_FINISH
    stalls that cost 4–12 s in the runs;
  - the pid-file, nonce and probe machinery and the 10 s/15 s typing bounds stop
    applying on this path.
- **Cost:**
  - Java in `DroidyardReader.java` (~150 lines), a new dex digest, the build.sh
    reproduction, and a spike (G-style gates: tap and setText on EditText and Compose,
    Qustodio undisturbed, `default_input_method` unchanged);
  - a **new ADR or an ADR-0030 amendment**: the reader stops being read-only; that is
    the owner's call;
  - `method` enum values (`set_text`, `paste`, `keys`) — a published contract change in
    `schema/capabilities.json`;
  - `ui-automation.md` (§Typing, §Input primitives) updated.
- **Risk:**
  - hidden-API reflection (`InputManagerGlobal` is API 34+; `InputManager.getInstance`
    before it);
  - SetText semantics differ from typing (see U1);
  - one more privileged verb on an unauthenticated socket (ADR-0030 accepted that for
    reads; injection is a bigger capability: any local process that reaches the forward
    can tap). Owner decision.

**R2. Replace the fixed 300 ms settle and shorten the per-poll idle wait.**

- Run the first read immediately after the action, and let the reader's own idle wait be
  the settle.
- For polls after an action, use a shorter `waitForIdle` bound (e.g. 100 ms quiet,
  200 ms max) or pass it per request. Maestro runs UiAutomator's idle timeout at 0, and
  Appium users tune its 10 s default down.
- Optional: the reader tracks the time of the last accessibility event, so "changed
  since the tap?" costs nothing.
- **Effect (inferred):** about 0.3 s saved per call, and a poll cadence of ~0.25 s
  instead of ~0.55 s while the app animates.
- **Cost:** small (D10 changes; a dex change if the request carries the idle bound).
- **Risk:** reading mid-transition. The two-read `gone` rule already guards that.

**R3. Cut host spawns per call.**

- Cache `sys.boot_completed`=1 per transport (already listed as "later" in
  `device-speed-2026-10.md`).
- Cache `ro.build.version.sdk` per serial (spawned on every `clear`).
- Optionally, reuse a fresh `devices -l` for ~1 s or subscribe with `track-devices`.
- **Effect:** 2–3 spawns, 0.1–0.2 s quiet and over 1 s under host load (each spawn
  0.6 s in m1f).
- **Cost:** low. **Risk:** a stale boot state after a reboot, which the transport id
  catches.

**R4. Until R1 lands (cheap):**

- wait for focus or the IME before typing instead of 300 ms (H1);
- run the §2 experiment to settle H1–H3;
- drop `inputChunkBytes` to about 8–16 if truncation shows (agent-device #531);
- make the ADBKeyboard path check that the field changed, since today an exit-0
  broadcast is counted as typed.

**Not recommended:** an installed IME (U3; D12 rejected it). Clipboard paste as the
primary path, because of the overlay and clipboard side effects; keep it as a fallback.

**What would change these conclusions:**
- the experiment showing H2: then holding the a11y connection itself costs keystroke
  latency, and an ADR-0030 trade-off appears (disconnect between calls);
- a target app whose fields reject SET_TEXT: then R1's key-injection fallback carries
  ASCII, and U2 carries non-ASCII.
