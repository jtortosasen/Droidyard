# Spike: UI events, windows and the full dump on a device (m2w step 1)

- Date: 2026-10-20
- Plan: [m2w step 1](../plans/m2w-ui-dump-and-changes.md#1-spike-events-windows-and-the-full-dump-on-a-device)
- Devices: the lane's scratch AVDs only, `m2w31` (API 31 `google_apis` arm64, `emulator-5580`) and
  `m2w36` (API 36 `google_apis_playstore` arm64, `emulator-5582`), on the lane's own adb server
  (port 5207) behind a guard that refuses any other serial. The 5037 server kept its pid (70176).
- Code: formapp's `WidgetsActivity` as of the step-1 commit; the throwaway spike dex
  (`beb6628b…58c0`), which connects `UiAutomation` exactly as the reader does. No daemon ran.
- Evidence: `~/Library/Caches/droidyard-dev/m2w/spike-{31,36}/`; the fixtures named below are in
  `internal/domain/device/testdata/`.

## Stop rules

| Rule | API 31 | API 36 | Verdict |
| --- | --- | --- | --- |
| S1 the listener receives events; idle and tap timing unchanged | events received; `waitForIdle(100,500)` median 0 ms with and without; injected tap median 50 → 49 ms | events received; 0 → 0 ms; 48 → 49 ms | pass |
| S2 a toast arrives with its text | `TYPE_NOTIFICATION_STATE_CHANGED`, class `android.widget.Toast`, text `[Saved ñ ✓]`, 154 ms after the click | the same, 36 ms after | pass |
| S3 `getWindows()` lists the dialog and the app window; notification text in SystemUI windows | empty at connect; with `FLAG_RETRIEVE_INTERACTIVE_WINDOWS`: nav bar, status bar, app. With the dialog open: the dialog window (title "Dialog title", active) and SystemUI, **not the app window under it**. During the heads-up a SystemUI window holds "Code 123456" | the same: 2 windows at rest, the dialog without the app under it, "Code 123456" in a SystemUI window | **fail**: interactive windows only, and SystemUI exposes notification text |
| S4 the reader's plain dump has `uiautomator dump`'s nodes | 25 and 25 nodes in the same order; values equal except two bounds: `uiautomator` clips the root to the app area (`[0,0][1080,2274]`) and gives the navigation bar background `[0,0][0,0]`, the reader the real display (`[0,0][1080,2400]`, `[0,2274][1080,2400]`) | 23 and 23 nodes, identical; `uiautomator dump` now writes `drawing-order` and `hint` itself | pass, with the API 31 bounds noted |
| S5 a full dump within 2.5 s | 74 nodes in 3 windows, 67 ms; 170 nodes during the heads-up | 52 nodes, 41 ms; 162 during the heads-up | pass |
| S6 `sha256sum` on the device | toybox `sha256sum` and plain `sha256sum` print the pushed dex's digest | the same | pass |

Fixtures: `uiautomator-dump-widgets-api{31,36}.xml` and `reader-dump-widgets-api{31,36}.xml`,
taken back to back (the reader's one-shot mode); `spike-events-api{31,36}.txt`.

## What else was measured

- **The connection's flags at connect** (both levels): `FLAG_INCLUDE_NOT_IMPORTANT_VIEWS |
  FLAG_REPORT_VIEW_IDS | FLAG_FORCE_DIRECT_BOOT_AWARE`, `eventTypes` all. Not-important views are
  already included, so the owner's fear that the reader reads fewer nodes than `uiautomator dump`
  is not borne out: what `device.uiTree` leaves out, it leaves out when it compacts.
- **Content-change storms are throttled by the framework.** `spinner` rewrites a TextView every
  20 ms; the listener saw at most 6 `TYPE_WINDOW_CONTENT_CHANGED` a second on API 31 and 10 on
  API 36, and a fling of `list` gave one `TYPE_VIEW_SCROLLED` a second. 25 s of driving gave 34
  events on API 31 and 44 on API 36.
- **The notification event carries no text**: `TYPE_NOTIFICATION_STATE_CHANGED`, class
  `android.app.Notification`, `text=[]`, on both levels. Its text is only in the parcelable, which
  nothing reads.
- **Q3's getters on `WidgetsActivity`**, the same on both levels: `state-description` (the SeekBar
  "30%", the Switch "OFF"), `range-*` (0–10 at 3), `error`, `heading`, `pane-title` (the app's and
  SystemUI's "Status bar"), `tooltip`, `input-type`, `max-text-length`, `custom-actions`
  (`2130771983:Archive`), `text-links` (`8-17=https://example.com/a%20b`), `collection-rows` and
  `-columns` on the ListView, standard actions on every node. **No `collection-item-*` on the
  ListView's rows**, and no `getExtras()` keys on any node.
- **Hidden children**: 9 of 74 nodes (API 31) and 3 of 54 (API 36) are not visible to the user; they
  are the ScrollView's content below the fold. The INVISIBLE `hidden` TextView is in no dump:
  the framework does not expose a view that is not VISIBLE.
- `POST_NOTIFICATIONS` is unknown to `pm grant` on API 31 (it is an API 33 permission); the
  notification posts without it there.

## Not observed

Physical devices, other OEMs, API 36.1, a RecyclerView or Compose list (where collection-item info
may be set), an IME window, a PopupWindow, the lock screen. The plain `dump` with
`FLAG_RETRIEVE_INTERACTIVE_WINDOWS` set was not compared, since S3 takes `windows` out.
