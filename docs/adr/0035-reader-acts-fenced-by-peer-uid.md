# ADR-0035: The reader acts: taps, keys and set-text, fenced by its peer's uid

- Status: Accepted
- Superseded in part by: ADR-0102 (§2)
- Date: 2026-10-07
- Relates to: ADR-0029, ADR-0030 (both amended), ADR-0015 §2, §4, ADR-0020 (its
  2026-10-06 line), ADR-0034, `ui-automation.md` (§Typing, §Input primitives, §Sequence
  and timing), plan m1n Q1, plan m1f D12, `docs/research/daily-use-speed-input-2026-10.md`
- Decided by the main session under the owner's standing instruction (Delegated,
  2026-10-07, plan m1n Q1), from step 2's facts. It changes ADR-0030, an owner decision,
  by adding a check for the new requests only; the owner can revert it.

## Context

`device.inputText` types printable ASCII through `input text` and refuses anything else
unless ADBKeyboard is installed (plan m1f D12 rejected installing an IME); the owner needs
"ñ" and accents. A typed command whose answer is lost may run twice (plan m1m's nonce
wrapper bounds it). Plan m1n step 2's spike dex (API 31): `ACTION_SET_TEXT` through the
reader's `UiAutomation` set "Añadir ñ á é í ó ú ü ¿¡ € 😀 日本" exactly on a Views field
(58 ms) and on a Compose field (the immediate read-back was empty; the next screen showed
the exact text, so a read-back waits a frame); an injected tap took 23–435 ms on a quiet
screen and blocked 11.2 s, then failed, while the app's main thread was frozen. A
connection through the daemon's forward carries peer uid 2000 (adbd); an app's connect to
the abstract socket was refused by SELinux.

## Decision

1. The reader answers `tap`, `keys` and `settext` besides `ping`, `dump`, `quit`:
   injection through the `UiAutomation` it already holds, and `ACTION_SET_TEXT` on the
   focused input node with the text read back in the same request (polling briefly, so a
   field that renders a frame later is read correctly), on one acting thread. Each is
   bounded on the device below the host's bound (a synchronous injection blocks while
   the app's main thread does), and its answer says whether anything was executed.
   Acting requests are capped at 4 KB, a longer one refused before executing.
2. **Fence.** An acting request runs only when the peer's uid is 0 or 2000
   (`LocalSocket.getPeerCredentials()`): adbd, through which the daemon's forward
   arrives; any other peer gets `denied`. Reads keep ADR-0030's rule (no check). The
   daemon sends acting requests only for a call that passed authorization.
3. **Delivery for `inputText`**: `set_text` when the reader runs and the node accepts the
   action (`clear` → replace; otherwise insert at the selection); `keys` for printable
   ASCII when it is refused, for a password field without `clear`, or when the caller
   passes `method: "keys"`; `input text` and ADBKeyboard only when the persistent reader
   cannot run; otherwise `DEVICE_TEXT_NOT_DELIVERABLE` before anything is tapped. Only
   `denied` or a refused `ACTION_SET_TEXT`, both answered before anything executed, may
   lead to another method. An acting request answered `busy` fails the call
   `DEVICE_NOT_READY` before acting. Any other outcome (a timeout, an EOF, a bound passed
   after execution began) ends the call `completed` with a warning that it may have
   applied; nothing is retried. A read-back that differs is reported, never retyped.
   `tapElement` taps through the reader under the same rule, `input tap` only without it.
4. Accepted differences from typing: filters and watchers see the whole value at once, no
   IME behaviour runs, and per-key fields (OTP boxes) may truncate; the read-back shows
   it, and `method: "keys"` types instead.

## Consequences

- Any Unicode text is typed with nothing installed; a replacing set cannot type twice.
- `ui-automation.md` §Typing, §Input primitives and §Sequence and timing change; the
  `inputText` param and result enum move the API to 0.11.0.
- An on-device process that could reach the socket can still read the UI, as before, but
  not act.
- Rests on API 31 facts; the API 36.1 rows are unverified (plan m1n Unverified).

## Alternatives considered

- **Clipboard plus `KEYCODE_PASTE`** (mobilecli). Overwrites the user's clipboard, shows
  Android 13+'s overlay, not idempotent; wins for non-ASCII into fields that refuse
  `ACTION_SET_TEXT`.
- **An installed IME.** Rejected by plan m1f D12.
- **`InputManagerGlobal.injectInputEvent` by reflection.** Faster per event, a hidden API
  that moves between releases; wins if step 9 shows the public path slow.
- **A token on the socket.** Needs a secret on the reader's command line, visible to the
  shell user's `ps`; the uid needs none; wins if an image hides peer credentials.
- **Keep `input` and fix only its timing.** Non-ASCII still needs an install, and the
  typed-twice class stays.
- **Acting without a fence.** An on-device app reaching the socket would inject events.
  Rejected.
