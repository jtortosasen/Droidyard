# ADR-0133: Agents act on the device by coordinates, keys and system verbs

- Status: Accepted
- Date: 2026-10-26
- Supersedes: ADR-0079 §Decision 1 (in part: audience, keys, result), §2 (in part: without a view, the screen), §3 (in part: delivery without a view), §5; ADR-0117 §Decision 7 (in part: the three change for agents; the UI still does not call them); ADR-0035 §Decision 1 (in part: `gesture`, `press`, the screen size on `dump`); ADR-0099 §Decision 2 (in part: the list gains `device.systemPanel`, `device.forceStop`, `device.rotate`)
- Relates to: ADR-0015 §4, ADR-0036 §1, ADR-0060 §2, §4, ADR-0094 §2, ADR-0100 §3, §4; `ui-automation.md` §Input primitives; [plan m3g](../plans/m3g-agent-device-gestures.md) Q1–Q7
- Decided by the main session under the owner's standing instruction to apply the recommended option, Delegated (2026-10-26), at the owner's request of 2026-10-26 ("all the gestures, or more things an agent could use over adb"), from plan m3g's drafts, reviewed in two rounds.

## Context

An agent driving a Samsung over MCP could not pull down the notification shade or quick
settings over a third-party lock screen, nor tap the Settings gear there. `device.tap`,
`device.swipe` and `device.pressKey` were for people only and needed a live view (ADR-0079
§3, §5), and `shell.exec` is disabled by policy. The reader already injects taps (ADR-0035),
and `ui-automation.md` §Input primitives reserved normalised coordinate actions sharing
`device.tapElement`'s `expect` for agents.

## Decision

1. **The three are agent tools.** `device.tap`, `device.swipe` and `device.pressKey` are
   audience `all` and occupy the device for an MCP session. Delivery is the open view's
   stream; else the reader's new `gesture` and `press` verbs; else `adb shell input` for a
   tap, a long press, a two-point swipe and a key. A longer path with neither the view nor
   the reader is refused `DEVICE_NOT_READY` before acting. The result names its `delivery`.
   Nothing the reader may have taken is sent again (ADR-0035 §3).
2. **Coordinates** are fractions of the screen that `device.uiTree` reports as `screen` (the
   reader's real size with rotation applied; on the `uiautomator` fallback, the on-device
   screencap header), not `display` (the active window's extent, a dialog's when one is up).
   The reader sizes them at injection, `input` by a fresh screencap header. Paths move in
   straight segments. `device.scroll` stays for people.
3. **Keys.** `pressKey` takes the five keys plus `power`, `wakeUp`, `sleep`, `volumeUp`,
   `volumeDown`, `volumeMute`, `menu`, `search`, `tab`, `escape`, the five d-pad keys,
   `notification` and `assist`. The view's wire (ADR-0117 §3) keeps its five.
4. **`device.systemPanel`** `{panel: notifications | quickSettings | none}` runs `cmd
   statusbar` and does not claim the panel opened; the agent verifies with `expect` or a read.
5. **More verbs.** `device.forceStop` (destructive); `device.rotate`, auto-rotate pinned off
   before `user_rotation`, the previous state returned; `device.launch`'s optional `url`
   (validated, single-quoted, with its app). `DEVICE_ACTION_FAILED` when the device refuses a
   command.
6. **`expect` and `timeoutMs`** on the three gestures, with `tapElement`'s meaning; nothing is
   read without `expect`.
7. **Lending.** All are `Lendable`; the host chooses its own delivery.
8. **The fence** is the device policy's admission, as `tapElement`'s. No caller text reaches
   the device shell but a validated `applicationId` and the quoted URL; `shell.exec` is
   unchanged. API `0.40.0`.

## Consequences

- About 8–9 KB more `tools/list` (47 tools), held by ADR-0060 §4's rule; the core profile
  and the server instructions are unchanged.
- A new reader digest is pushed on first use.
- A reader gesture holds the device's read lock for its travel (up to about 9 s): reads wait,
  and a recording skips its dumps meanwhile.
- An older host answers a no-view tap `DEVICE_NO_STREAM`, an unknown capability as missing
  (`DEVICE_NOT_READY`, `incompatible`), and refuses `url`, `expect` and the new key names
  `REQUEST_INVALID_PARAMS`.
- Documents: `ui-automation.md` §Input primitives, `adb-device.md` §Capabilities, `mcp.md`
  §What is exposed, and what is not.

## Alternatives considered

- **A control-only scrcpy source with no view**: a server start per call, or a device process
  outside a view's life (ADR-0077).
- **`adb shell input` only**: a process per call (0.08–0.72 s and more under load), waiting
  on the app's UI thread, and no timed path.
- **The reader only**: nothing when another UiAutomation holds the device.
- **Agent-only capabilities beside the three**: two names for one act.
- **`display` as the size**: wrong under a dialog.
- **A raw keycode**: any key, silent state changes, and a number to look up.
- **`KEYCODE_NOTIFICATION` or a swipe instead of `device.systemPanel`**: no quick settings,
  vendor-dependent; the owner asked for both.
- **`device.openUrl`**: a second launch path, with no pid.
- **`DEVICE_ADB_FAILED` for a refused command**: that code means adb could not run.
- **Not lending them**: against the owner's rule; the trigger's device was lent.
