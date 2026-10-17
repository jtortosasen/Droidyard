# ADR-0080: `FLAG_SECURE` is read from the focused window

- Status: Accepted
- Date: 2026-10-17
- Relates to: ADR-0015, ADR-0078; plan m2p Q9; [`spike-screen-stream-2026-10.md`](../research/spike-screen-stream-2026-10.md)
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated (plan m2p Q9). The owner chose the register row this closes, 2026-10-17.

## Context

A window with `FLAG_SECURE` is not captured: the register's row says a screenshot comes
back black "and nothing says so". The spike found more: on API 31 `screencap` with a
secure window focused **fails** (exit 1, empty output), and on API 36 it succeeds with
the window black; the stream shows the window black and the status and navigation bars
drawn on both. One `adb shell dumpsys window` (p50 33–35 ms, p95 40–45 ms) names the
focused window (`mCurrentFocus=Window{<hash> …}`) and, in that window's
`Window #N Window{<hash> …}:` block, its `fl=` flags, `SECURE` among them, on both levels.

## Decision

1. The check is one `dumpsys window`, parsed for the focused window's block only; any
   other window's flags are ignored. A failed or unparsable check reports nothing.
2. `device.screenshot` runs it after a capture: when it is true, the result gets the
   warning "the focused window is secure (FLAG_SECURE): Android captures it black". When
   the capture fails and the check is true, `SCREENSHOT_FAILED`'s cause says the same. No
   package is named, so neither carries device text.
3. While a live view runs, the source runs the check every P (the larger of 2 s and four
   times its p95) and publishes `device.screen` `state {device, secure}` at start and on
   each change; the view shows a banner while `secure`.

## Consequences

- The agent and the person learn why the screen is black; the screen stays black (the
  platform's rule).
- A check per screenshot adds ~35 ms; while viewing, one `dumpsys` every 2 s.
- Documents to change: none beyond the register row (R1 closed by 2p).

## Alternatives considered

- **An all-black test on the image**: the bars are other windows and are drawn, so a
  secure screen is not all black; a dark app would also trip it.
- **The UI reader** (`AccessibilityWindowInfo`): it has no secure flag.
- **`dumpsys window windows` alone**: smaller, but on API 31 it lacks the focus line.
- **A warning only on the stream**: agents use `device.screenshot`, and the row is theirs
  first.
