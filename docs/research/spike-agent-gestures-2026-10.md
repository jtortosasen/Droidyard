# Spike: the edge swipe, `cmd statusbar`, keys, rotation, force-stop and a deep link by adb (m3g step 1)

- Date: 2026-10-26
- Plan: [m3g step 1](../plans/m3g-agent-device-gestures.md#1-the-facts-the-steps-rest-on-spike-main-session)
- Devices: two scratch AVDs under `~/Library/Caches/droidyard-dev/3g/avd`, pixel_6 (1080×2400),
  booted headless by emulator 36.6.11.0: `m3g31` (API 31, `google_apis` arm64) on
  `emulator-5570`, `m3g36` (API 36, `google_apis_playstore` arm64) on `emulator-5572`. Plain
  `adb -s <serial>` through the shared adb server (5037), no daemon. A second adb server was not
  started: the owner's phone was on USB in use, and two servers would contend for it.
- Evidence: the scripts in `~/Library/Caches/droidyard-dev/3g/evidence/spike/`; outputs as
  quoted here. "Shade open" means `dumpsys window`'s `mCurrentFocus` is `NotificationShade`
  and, unlocked, the dump's package is `com.android.systemui`.

## (a) The shade by injection

- **`input swipe 540 <y0> 540 1400 <ms>`, unlocked, opens the shade on both levels**: 22 of 22
  (y0 0, 1, 5, 20; 100, 300, 500, 1,000 ms; right after a `uiautomator dump`). The very first
  swipe of the session, 1 s after HOME, did not open it on either level; a second did. Read as
  the launcher still settling after HOME (inferred, not isolated).
- **The bare version** (`input motionevent DOWN 540 0`, `MOVE`, `UP 540 1400`, each its own
  process): with one `MOVE` it opened 3 of 3 on API 31 and **0 of 3 on API 36**; with a `MOVE`
  every 100 px it opened 3 of 3 on both. A sparse path is not enough on 36.
- **Swipe keyguard and PIN keyguard**: the keyguard is itself the `NotificationShade` window, and
  its dump already holds `notification_panel` and `notification_stack_scroller`, so neither can
  show the shade opening there. One swipe over the keyguard changes the dump on 36 (the large
  clock leaves) and not on 31; **two swipes open quick settings on both** (`quick_settings_panel`,
  `settings_button_container`, `brightness_slider` appear).
- **Expansion disabled** (`cmd statusbar send-disable-flag statusbar-expansion`, exit 0, a
  stand-in for a kiosk): the swipe opens nothing on either level.
- **Nodes for `appears`**: unlocked shade, `com.android.systemui:id/quick_qs_panel` (31 and 36);
  quick settings, `quick_settings_panel` and `settings_button_container`. All three are absent in
  every closed state observed (launcher, swipe keyguard, PIN keyguard).

## (b) `cmd statusbar` as shell

`expand-notifications`, `expand-settings`, `collapse`: exit 0, no output, on both levels.
Unlocked, each has its effect. Over the swipe keyguard and the PIN keyguard, `expand-settings`
opens quick settings with the gear (31 both; 36 PIN). On 36 over the swipe keyguard, the dump
after it showed the launcher, read as the keyguard dismissed by the swipe before it (inferred).
With expansion disabled, `expand-settings` changes nothing and **still exits 0**: the exit status
does not say the panel opened.

## (c) Keys by `input keyevent`

Every code of plan Q2 exits 0 with no output on both levels: 26 (power: 31 `Asleep`, 36
`Dozing`), 224 (`Awake`), 223 (31 `Asleep`, 36 `Dozing`), 24, 25, 164, 82, 84 (31: focus
null, an overlay), 61, 111, 19–22, 23, 83, 219. Their effects other than power, wake and sleep
were not checked one by one.

## (d) Rotation and the screen size

- With Settings in front: `settings put system accelerometer_rotation 0`, then
  `user_rotation 1`: the display is 2400×1080, and the dump's `<hierarchy rotation="1">`.
  `wm size` still prints `Physical size: 1080x2400`.
- **The on-device screencap header** (`adb shell 'screencap | head -c 8 | od -An -tu4'`) gives
  `2400 1080` rotated and `1080 2400` back at `user_rotation 0`, on both levels, in about
  230–310 ms from the host.
- With the launcher in front (portrait-locked), the same writes leave the display portrait: an
  app that fixes its orientation wins over `user_rotation`.
- `accelerometer_rotation` was 1 and `user_rotation` 0 at boot; both were restored.

## (e) Force-stop and a deep link

- `am force-stop com.android.settings`: exit 0, no output, and `pidof` empty after. An absent
  package: exit 0, no output.
- `am start -W -a android.intent.action.VIEW -d '<url>' -p com.android.chrome`: `Status: ok`,
  Chrome's activity. Unresolved (no app for `zzq://`, an absent package, `android-app://` to
  Settings): `Error: Activity not started, unable to resolve Intent { … }`, **exit 0 on 31 and
  exit 1 on 36**. `am` prints `dat=` cut after the host (`https://x.example/...`).
- **Positive control.** The plan's payload (`…b='c';touch${IFS}…;'$(id)`) is not live: naive
  quoting fails with `/system/bin/sh: no closing quote` and creates nothing. **A live one**:
  `P = https://x.example/a?b=';touch${IFS}/data/local/tmp/x;'` (no space, 0x21–0x7E). Through
  `adb shell "am start … -d '<P>' -p com.nope.absent"` it **creates `/data/local/tmp/x`** on both
  levels (exit 126). Single-quoted with each `'` as `'\''`, it runs `am` with the URL and creates
  nothing.

## (f) Cost of `input`

Ten runs each from the host, median: `input tap` 145 ms (31), 151 ms (36); `input swipe … 100`
452 ms, 500 ms; `input keyevent` 146 ms, 148 ms. A quiet host, two emulators running.

## Not observed

Samsung (One UI), a real third-party lock screen or kiosk, a physical device, API levels other
than 31 and 36, the reader's own injection (plan step 2), multi-display and cutouts.
