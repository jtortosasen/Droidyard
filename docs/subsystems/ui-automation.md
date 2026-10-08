# Subsystem: UI automation

Observing and acting on an app's UI through the accessibility tree: `device.uiTree`,
`device.tapElement`, the selector and assertion vocabulary they share, and the input
primitives underneath.

> Status: draft. Device targeting, transport and the device model live in
> [`adb-device.md`](adb-device.md). This document encodes the prior-art findings on
> identity-first resolution and one-call verification
> ([prior-art](../research/prior-art.md),
> [concepts](../research/dsh-codex-android-concepts.md) §4,
> [mobile-mcp](../research/mobile-mcp.md)).

## Design rules

- **Tree-only in v1.** Every match and every assertion reads the accessibility tree
  from the reader (ADR-0029). Screenshots are for humans and models, never an input to
  matching. The prior art's `expect_text` polled **OCR**; OCR is out of v1
  ([ADR-0013](../adr/0013-ocr-optional-pluggable.md)), so the assertions here are
  defined on the tree alone.
- **Identity first**: `resource-id` > content-desc / text > coordinates.
- **Act and verify in one call** ([mvp](../roadmap/mvp.md) golden path step 7).
- **An unmet expectation is a result, not an error** — like a failed build. Errors
  mean the tap could not be performed.
- **Device text is untrusted data** ([ADR-0015](../adr/0015-untrusted-data-authorization.md)):
  compared as literal strings, never interpreted.

## The tree: `device.uiTree`

- Droidyard's own reader, kept running ([ADR-0030](../adr/0030-persistent-ui-reader.md),
  [ADR-0029](../adr/0029-ui-reader-dex.md)): a small dex started with `app_process` on the
  first read, connecting `UiAutomation` with `FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES`
  so the device's other accessibility services keep running, and reached over
  `adb forward` (a warm read took 14 ms on a Pixel_9a; the first ~1.9 s). It ends itself
  after 30 idle minutes. When it cannot run, the one-shot reader of ADR-0029 reads once,
  and only then `uiautomator dump /dev/tty` (with its `/sdcard` fallback and one idle
  retry), each with a warning; reads after a `tapElement` or `inputText` action never use
  the dump.
- Reads are **serialised per device**: Android allows one `UiAutomation` at a time per
  device, so `uiTree` and the polls of an action share one per-device read lock, waited
  inside the read's deadline.

```text
Snapshot
├── snapshot     "s-81c2ab34"; each read mints one and the last 16 per device are kept for ref resolution
├── foreground   package of the top window
├── display      { width, height, rotation }
├── nodes[]
│   ├── ref      "n12", valid with its snapshot only
│   ├── class    short name: Button, TextView, View
│   ├── id       resource-id (app package prefix stripped)
│   ├── text, desc
│   ├── bounds   [left, top, right, bottom], display pixels
│   ├── flags    non-default only: clickable, scrollable, checkable, checked,
│   │            selected, focused, password, disabled, editable (persistent
│   │            reader only)
│   └── children the child nodes' refs ["n13", "n14"]
├── truncated
└── emptyReason  noMatch (within matched nothing) | pruned | noAccessibilityInfo
```

- **Flat**: `nodes[]` is in document order and `children` holds refs, not nested
  nodes, so the published output schema stays fully inlined with no recursive type
  (milestone 1e, Q1).
- **Compact**: flags appear only in their non-default state, and layout-only nodes (no
  id, text, desc or flag) are folded into their parent.
- **Bounded**: a byte cap (default ~40 KB) prunes the deepest levels first, with
  `truncated: true` and a hint to narrow with `within` (a selector for the subtree to
  return).
- **Empty says why**: `within` matched nothing, the cap pruned everything below it, or
  the window exposes no accessibility nodes at all (a WebView without accessibility,
  Flutter without semantics, a canvas or a game).
- **Compose caveat**: Compose exposes no resource-ids to uiautomator unless the app opts
  in with `testTagsAsResourceId`. Never assume stable ids exist; fall back to
  text/desc. The sample app opts in.

## Selectors

One grammar, used for the tap target, for `within`, and inside expectations:

```json
{ "id": "login_button" }
{ "text": "Sign in", "class": "Button" }
{ "desc": "Like", "within": { "id": "item_3" } }
{ "ref": "n12", "snapshot": "s-81c2ab34" }
```

- Fields `id`, `text`, `desc`, `class` combine with AND. `within` is an ancestor
  selector (not nested further). `index` (0-based, document order) breaks an ambiguity
  deliberately.
- **`within` matches its fields exactly**; the contains fallback of §Resolution 2
  applies to the target only.
- **`ref` resolves by identity, not by position.** The daemon looks the ref up in its
  snapshot, takes that node's identity (class, id, text, desc, bounds) and finds it in
  the fresh tree. Not found → `DEVICE_STALE_REF` with candidates. So `gone: {ref}`
  means "that element is gone".

### Resolution

1. **Exact** match on every given field.
2. None → **case-insensitive contains** on `text` and `desc` (`id` and `class` stay
   exact). The result reports `matchedBy: exact | contains`.
3. **Fold nested duplicates**: when a match and one of its ancestors both match (Compose
   merges semantics, so a clickable row and its label often carry the same text), keep
   the outermost.
4. **Off-screen** matches (no intersection with the display, or zero area) are dropped;
   if they were the only ones → `DEVICE_ELEMENT_OFFSCREEN`.
5. More than one left and no `index` → `DEVICE_ELEMENT_AMBIGUOUS`, listing up to 10
   candidates (ref, class, id, text, bounds).
6. None → `DEVICE_ELEMENT_NOT_FOUND`, with near candidates and the tree's `emptyReason`
   when it was empty.
7. The one match is **disabled** → `DEVICE_ELEMENT_DISABLED`. Disabled targets are not
   dropped in step 4: tapping one is a caller error worth naming.

The tap lands at the centre of the node's visible bounds, **unless a node drawn above it
covers that centre**. The tree keeps every node's `drawing-order`, so the point moves to
the centre of the largest strip of the node left uncovered, at least 8 px inside it; a
node whose bounds are the whole display is ignored (a full-screen overlay is usually
transparent). When the point moves, the result warns, naming the covering node by class
and id. When nothing visible is left, the call fails `DEVICE_ELEMENT_OFFSCREEN` with the
covering node and taps nothing. When the node itself is not clickable, the result says
so and names its nearest clickable ancestor, so a tap with no effect is explainable.

## Acting: `device.tapElement`

```json
{
  "device": "emulator-5554",
  "target": { "text": "Sign in" },
  "expect": [ { "appears": { "text": "Welcome" } },
              { "gone": { "id": "login_form" } } ],
  "timeoutMs": 5000,
  "returnTree": false
}
```

### Assertion vocabulary

`expect` is a list (at most 5) of conditions, all of which must hold:

| Condition | Holds when |
| --- | --- |
| `appears: Selector` | At least one on-screen node matches, in one dump |
| `gone: Selector` | No node matches, in **two consecutive** successful dumps — a dump taken mid-transition can miss nodes that are still there |
| `foreground: applicationId` | The top window belongs to this app — catches a crash or a navigation out of it |

Counting (`expect_count` in the prior art) arrives with the row abstraction,
post-MVP. There is no text-by-regex condition: `appears` with `text` (exact, then
contains) covers the MVP's cases, such as a counter reading "Likes: 1".

### Sequence and timing

1. **Before**: dump, resolve the target, and evaluate every condition against this
   tree. A condition that already holds is flagged **vacuous**.
2. **Tap**: through the persistent UI reader when it can run — an injected touch at the
   display point ([ADR-0035](../adr/0035-reader-acts-fenced-by-peer-uid.md)) — and
   `input tap x y` in display pixels when it cannot, or when the reader denied the request
   before executing it. A tap the reader took but never answered is never repeated: the
   call goes on to the settle and poll with a warning that the tap may have landed, since
   a second tap could land twice.
3. **Settle**: wait 300 ms, a constant (plan m1f D10).
4. **Poll**: read again and again — each read starts at least 250 ms after the previous
   one started; a warm read takes tens of milliseconds, a fallback read 1–2 s — until every condition holds or
   `timeoutMs` elapses (default 5 s, maximum 20 s). The first read after the tap runs to
   the call's running limit, not to `timeoutMs`, so a slow fallback read is still observed;
   `timeoutMs` bounds the polls after it.
5. **Return.** With no `expect`: one read after the settle, for `foreground` and the
   optional tree, then return; the result omits `expect` rather than reporting a `matched`
   it never checked.

Between the before read and the tap, the call checks the **soft keyboard**. When the before
tree shows a focused editable node, `dumpsys input_method` reports `mInputShown=true`, and the
IME window's touchable region (from `dumpsys window windows`) contains the target's tap point,
the keyboard covers the target — a tap would land on a key. The call sends one BACK
(`input keyevent 4`), re-reads the tree, re-resolves the same selector and re-chooses the
point, and the result carries a warning saying the keyboard was hidden. If the point is still
covered after BACK the call fails `DEVICE_ELEMENT_OFFSCREEN` naming the keyboard, having
tapped nothing. A tree with no focused editable node, a keyboard reported not shown, or a
region that cannot be read, behaves exactly as before this rule (nothing is hidden). Only the
persistent reader's own dump carries the `editable` flag this needs, so on the `uiautomator`
path the guard never runs. (Delegated, plan m1n step 5: found at level 5, where a target under
the keyboard took a key press; the owner can revert it.) The focus wait after the focusing tap
accepts only a focused node containing the tap point: a field elsewhere that keeps focus (the
one just typed into) must not count.

The defaults are starting values: spike S6 measures dump latency and the idle-failure
rate on API 34–36 and sets them. The 20 s cap keeps `tapElement` a bounded,
non-long-running call ([ADR-0020](../adr/0020-operation-contract.md)).

### Result

```json
{
  "tapped": { "ref": "n12", "snapshot": "s-1a2b3c4d", "matchedBy": "exact",
              "class": "Button", "text": "Sign in", "point": [540, 1210],
              "clickable": true },
  "expect": { "matched": true, "afterMs": 840, "polls": 2, "failedDumps": 0,
              "unmet": [], "vacuous": [] },
  "foreground": { "before": "com.example.sample", "after": "com.example.sample" },
  "snapshot": "s-9f8e7d6c",
  "tree": null,
  "appCrash": null,
  "systemDialog": null
}
```

- Every ref the result returns comes with the snapshot it belongs to: `tapped.snapshot`
  for `tapped.ref`, and the top-level `snapshot` for the refs in `unmet` candidates and
  `tree` (the last read). Pass a ref back only with its own snapshot; an unknown or
  evicted one is `DEVICE_STALE_REF`.
- `matched: false` is a **completed** call. `unmet` lists each failing condition with
  what the last tree showed (near candidates for `appears`, the still-present match for
  `gone`).
- **Vacuous conditions are flagged, not refused.** The prior art's probe guard refused
  before tapping; flagging keeps the single round trip, and a vacuous match proves
  nothing, which `vacuous` says.
- **After the tap, never an error.** A read that fails while polling counts as a poll
  without an observation (`failedDumps`). If every poll failed, the result is
  `matched: false` with the reason `noObservation`, and `foreground.after` is omitted with
  a warning saying the effect was not observed. Raising an error there would invite a
  retry, and a retry taps twice. Only a failure *before* the tap is an error
  (`DEVICE_UI_DUMP_FAILED`), and then nothing was tapped.
- **A cancellation after the tap is a completed call too.** The tap landed, so the result
  carries `tapped` and, with `expect` not yet matched, the reason `cancelled`; an error would invite a
  retry that taps twice.
- `returnTree: true` adds the last snapshot, saving a `uiTree` call at the cost of
  tokens.
- **A vanished app is named, not guessed at** ([ADR-0034](../adr/0034-device-calls-report-busy-crash-dialog.md) §2). When the
  action's aftermath leaves the app's process gone — `foreground.after` differs from
  `foreground.before`, or no read observed the foreground at all, and `pidof` the app is
  empty — `appCrash` carries what `logcat -b crash` since the action began named (its
  `exception`, `message` and up to five `frames`), or, with no block, the newest
  `dumpsys activity exit-info` reason that is a crash or an ANR (`4`, `5` or `6`) and is
  timestamped at or after the action. `source` says which. A crash is read then and there,
  and never for the system ui's own `android` window: it is never read on a call whose
  foreground held, so a normal tap pays nothing for this. An `inputText` without `expect`
  has no after read, so the process check alone decides: it pays one `pidof`, and the crash
  read only when that says the app is gone.
- **A system dialog over the screen is named** (ADR-0034 §3). When a read's tree holds the
  system error dialog's buttons — `aerr_close`/`aerr_wait` for an ANR,
  `aerr_close`/`aerr_app_info` for a crash — **and the tree's foreground is the system ui's
  `android`, not an app**, the result's `systemDialog` carries `kind` (`anr` or `crash`) and,
  from one `logcat -b events` read bounded to a couple of seconds, the app's `package` (the
  newest event of the tree's own kind). It appears on `uiTree`, `tapElement` and `inputText`
  results. A target **inside** the dialog is acted on as usual; a target the dialog hides —
  one that does not resolve at all, never an ambiguous, disabled or off-screen one, whose own
  error is returned — and any `inputText` without a target fails `DEVICE_NOT_READY` before
  anything is tapped or typed, with `details.state` `"systemDialog"` and the dialog in
  `details`, and the failed call marked untrusted. The package is device text, so it appears
  in `details`, never in the error's prose. An action on the dialog is not the app's action:
  it never triggers the crash path. Droidyard never dismisses the dialog itself.

### Concurrency

A `tapElement` holds the device's **write lock** from the before-dump to the last
poll, so two callers cannot interleave a tap with another's verification. `uiTree` is a
read, and waits only for the dump lock.

## Typing: `device.inputText`

```text
device.inputText {device, text, target?, clear?, submit?, method?, expect?, timeoutMs?}
```

- `target?` — a selector, the same vocabulary as `tapElement`. Omitted, the text goes
  to whatever holds focus.
- `clear?` — empty the field first (select-all, delete), only meaningful with a target.
- `submit?` — press ENTER after typing.
- `method?` — `"auto"` (default) or `"keys"`: ask for key events instead of a set-text,
  which a per-key field such as an OTP box needs.
- `expect?` — the same assertion vocabulary, sequencing and timing as `tapElement`.
- `timeoutMs?` — the poll window for `expect` (default 5000, maximum 20000).

**A before read always runs**, even with no `target` and no `expect` (plan m1n step 6). It
is what sees a system dialog before anything is typed — the refusal below has to see it
before the text goes in — and it is the tree a dialog is named from. So an `inputText`
without a target is not free of reads; it pays one read and, at the end, one `pidof`.

**Delivery** goes through the persistent UI reader when it can run
([ADR-0035](../adr/0035-reader-acts-fenced-by-peer-uid.md)), and the path is chosen
before anything is tapped or typed, never by guesswork:

| Text | Path | On failure |
| --- | --- | --- |
| Any | `set_text` on the focused field — `replace` when `clear` is set, `insert` at the selection otherwise | a refused `ACTION_SET_TEXT` is retried as `keys` when the text is printable ASCII |
| Printable ASCII, when `method: "keys"` is asked, or on a password field without `clear` | `keys` — the reader's virtual keyboard | — |
| No reader, or the request denied before it executed: printable ASCII (space..~) | `input text`, single-quoted for the device shell, with spaces as `%s` and a literal `%s` split across two invocations | — |
| No reader, or the request denied, and the text is non-ASCII | ADBKeyboard's `ADB_INPUT_B64` broadcast, when that IME is installed and selected | `DEVICE_TEXT_NOT_DELIVERABLE`, naming ADBKeyboard |

`set_text` needs no installed IME, so any Unicode text is typed with nothing installed —
ñ, CJK, an emoji. Only a `denied`, or an `ACTION_SET_TEXT` the field refused before
executing anything, may lead to another method: once the reader has taken a request,
another method could type the text twice. Non-ASCII on the non-reader path is **never
silently mistyped**: without the IME the call fails and says what to install, and a
`method: "keys"` text that is not printable ASCII fails the same way — the reader's virtual
keyboard cannot map it — before anything is tapped or typed. With a
`target`, the node is tapped once to focus it and, on the reader path, the call then waits
until that node reports `focused` — a Compose field renders a frame later — before
delivering; the non-reader path waits the fixed settle instead. The focusing tap takes the
same soft-keyboard guard as `tapElement` (hide with BACK, re-read, re-resolve), so a field
that sits under the keyboard is focused and typed into rather than the key above it, and a
target the keyboard still covers after BACK fails `DEVICE_ELEMENT_OFFSCREEN` naming typing.
When the focusing tap was issued — the reader answered it or not — and the field never
reported focus, the call ends **completed** with a warning that nothing was typed, and a
crash the tap caused is diagnosed like `tapElement`'s; an error there would invite a retry
that taps twice.

**Result**: `{typed, characters, method: "set_text" | "keys" | "input_text" | "adbkeyboard", tapped?, expect?, appCrash?, systemDialog?, warnings?}`.
`tapped` is the focused node when `target` was given. `appCrash` and `systemDialog` are the
`tapElement` ones ([ADR-0034](../adr/0034-device-calls-report-busy-crash-dialog.md) §2–3): a
crash the typing left behind, and the system error dialog a read's tree showed. `expect` is the `tapElement`
verification object and appears only when `expect` was given: its `matched` is the
`matched` of this call, and an already-true condition is `vacuous`. `characters` is what
the field **read back**, not what was sent: the reader reads back the whole field, so an
`insert` into a field that already held text reads back as that text followed by the new
one — the field's own text is the prefix, a match counts only the delivered text, and the
prefix is the node's text (read as empty when it equals the field's hint). An equal
read-back counts the text, a differing one counts the field's own characters beyond the
prefix and carries a warning quoting both, and
it is **never retyped**, because a second set could type it twice. A password field's
read-back is the platform's mask, so it is compared by length alone. On the non-reader
path `characters` is the prefix that was delivered when a later chunk failed, which is a
*completed* result with a warning, never an error: a retry would type the prefix again.

**An acting request the reader took without answering** — a timeout, an EOF, a bound
passed after execution began — ends the call **completed** with a warning that it may have
applied; nothing is retyped and no other method is tried. A request the reader answered
`busy` (its single acting thread still out on an earlier request) fails `DEVICE_NOT_READY`
**before acting**, so a pending effect is never followed by a second delivery.

**The rules shared with `tapElement` apply unchanged**: after typing, the call never
returns an error because a check failed — `matched: false` is a *completed* call, since
an error would invite a retry and a retry types twice; and a check that was already
true before typing is flagged `vacuous`.

**Errors**: `DEVICE_UI_DUMP_FAILED`, `DEVICE_ELEMENT_NOT_FOUND`, `DEVICE_ELEMENT_AMBIGUOUS`,
`DEVICE_ELEMENT_OFFSCREEN` (including a target the soft keyboard still covers after BACK),
`DEVICE_ELEMENT_DISABLED`, `DEVICE_STALE_REF`, `DEVICE_TEXT_NOT_DELIVERABLE` (undeliverable
text, a `method: "keys"` text that is not printable ASCII, or a text over the reader's
request-line cap), `REQUEST_INVALID_PARAMS` (a `keys` text the call's typing slot cannot
cover, before it is sent), and `DEVICE_NOT_READY` when the reader's acting thread was busy,
when the reader refused an acting request it could not parse (the daemon's request, not the
call's; retrying the same call cannot help), when the focusing tap did not take, or when the
screen shows a system dialog and the call is not aimed inside it — a target behind it, or any
call without a target — with `details.state` `"systemDialog"` and the dialog in `details`
(nothing typed; [ADR-0034](../adr/0034-device-calls-report-busy-crash-dialog.md) §3). (A tap request over the reader's length cap is also `DEVICE_NOT_READY`. A set-text or keys
request over it means the text itself is too long to deliver, so `inputText` reports that
refusal as `DEVICE_TEXT_NOT_DELIVERABLE` above.)

## Input primitives

The adb verbs beneath UI actions. The MVP exposes `tapElement` and `inputText`;
coordinate taps, swipes and scroll-to-find are later capabilities, and use
these rules and the same `expect` vocabulary.

- **Coordinates for coordinate-based actions are normalised 0..1**, resolved to pixels
  using the **display size from a fresh screencap**, not `wm size` — which commonly
  reports the portrait panel size even in landscape. This removes rotation math
  entirely.
- **Tree-derived actions work in display pixels**, because the tree's bounds are
  already in that space.
- **Text goes through the persistent reader** when it can run: `set_text` (an
  `ACTION_SET_TEXT` on the focused field) carries any Unicode text with nothing installed,
  and `keys` types printable ASCII as virtual-keyboard events (§Typing). `input text` —
  printable ASCII, single-quoted with spaces as `%s` — and the ADBKeyboard IME for
  non-ASCII are the fallback, taken only when the reader cannot run or denied the request
  before executing it; without the IME the call is **refused with an install hint — never
  silently mistyped**.
- **Taps go through the persistent reader too** when it can run; `input tap x y` is the
  fallback, and a tap the reader took but never answered is never repeated (§Sequence and
  timing).
- **Rotation** pins `accelerometer_rotation=0` before setting `user_rotation`, so
  auto-rotate cannot fight an explicit value.
- **Device actions** (notifications, quick settings, lock, wake, assistant) are plain
  adb verbs; `wm dismiss-keyguard` is best-effort and a locked device simply stays
  locked.

## Failure modes

| Failure | Code | Behaviour |
| --- | --- | --- |
| Dump fails before the tap (after one retry) | `DEVICE_UI_DUMP_FAILED` | Cause-attributed; says whether to retry. Nothing tapped |
| Target not found | `DEVICE_ELEMENT_NOT_FOUND` | Near candidates, and the tree's `emptyReason` |
| Several targets | `DEVICE_ELEMENT_AMBIGUOUS` | Up to 10 candidates; `index` or a narrower selector resolves it |
| Target off-screen | `DEVICE_ELEMENT_OFFSCREEN` | Its bounds and the display size |
| Target covered by a node drawn above it, no visible strip left (D22) | `DEVICE_ELEMENT_OFFSCREEN` | The node and the covering node; nothing tapped. A partly covered target is tapped in its largest visible strip, with a warning |
| Target whose tap point lies under the soft keyboard, still covered after one BACK | `DEVICE_ELEMENT_OFFSCREEN` | Names the keyboard; the tree was re-read and the target re-resolved first, and nothing was tapped |
| `inputText`: the target the soft keyboard still covers after BACK | `DEVICE_ELEMENT_OFFSCREEN` | Names typing; nothing typed |
| `inputText`: `method: "keys"` with non-ASCII text, or a password field without `clear` and non-ASCII text | `DEVICE_TEXT_NOT_DELIVERABLE` | The reader's virtual keyboard cannot map it; nothing tapped or typed |
| `inputText`: a `keys` text the call's typing slot cannot cover | `REQUEST_INVALID_PARAMS` | Refused before the tap rather than sent and cut |
| Target disabled | `DEVICE_ELEMENT_DISABLED` | The node |
| Stale ref | `DEVICE_STALE_REF` | Candidates with the same text/desc |
| No accessibility info | `DEVICE_ELEMENT_NOT_FOUND`, `emptyReason: noAccessibilityInfo` | Points at `device.screenshot`; OCR is post-v1 |
| Expectation unmet | — (completed) | `matched: false` with `unmet` |
| Reader's acting thread busy with an earlier request | `DEVICE_NOT_READY` | Nothing acted; a pending effect may still be applying (ADR-0035 §3) |
| An acted request the reader took but never answered (timeout, EOF, bound passed) | — (completed) | A warning that it may have applied; nothing retyped, no other method tried (ADR-0020's 2026-10-06 line) |
| `inputText`: the focusing tap was issued but the field never reported focus | — (completed) | A warning that nothing was typed; an `appCrash` when the tap left the app's process gone |
| `inputText`: the focusing tap overran the before phase, the input-method check failed, or the field could not be cleared | `DEVICE_NOT_READY` | Nothing typed; retry |
| Cancelled once the tap or a typing command was issued | — (completed) | A warning that the effect may have landed; `expect.reason: cancelled` (D21) |
| A system dialog over the screen, and the target is not inside it (or an `inputText` without a target) | `DEVICE_NOT_READY` | `details.state: "systemDialog"` and the dialog in `details`; nothing tapped or typed; the failed call marked untrusted (ADR-0034 §3) |
| An action leaves the app's process gone | — (completed) | `appCrash` names the crash (crash buffer, else `exit-info`); a completed result, never a retry (ADR-0034 §2) |
| A read's tree shows the system error dialog | — (completed) | `systemDialog` names its `kind` and package (ADR-0034 §3) |

## To verify (spike S6)

- The dump latency and the "could not get idle state" rate on API 34–36 emulators with
  animated Compose UIs, and whether `/dev/tty` works on all of them.
- Whether a Compose screen with an infinite animation ever lets `uiautomator` reach
  idle, which would make polling impossible on that screen.
- That the dump's top-window package reports `android` (not the app) while a crash
  dialog is showing, which `foreground` relies on.

## Post-MVP

- **Row abstraction** for feeds whose controls are unlabelled: repeated isomorphic
  siblings (≥ 3, same class, near-equal height) as rows, with counter parsing, and
  `count` assertions built on it.
- Coordinate taps, swipes and scroll-to-find, sharing the `expect` vocabulary.
- An OCR backend behind the same conditions, for trees with no accessibility
  information ([ADR-0013](../adr/0013-ocr-optional-pluggable.md)).

## Open questions

- **An ANR over the app — closed (ADR-0034 §3).** In the m1l comparison pilot (`m1l/cmp-20261007-1303`) the app showed "isn't responding"
  while typing on a memory-pressed emulator; the device then stopped answering adb for
  seconds (`getprop` unanswered in 3 s, `screencap` timing out at 10 s), so a structured
  report of the dialog would not have reached the agent either. The agent recovered by
  tapping "Wait". `device.*` now names the dialog (`systemDialog {kind, package?}`, its
  package from one bounded `logcat -b events` read), refuses a target outside it with
  `DEVICE_NOT_READY` before anything acts — marked untrusted, since the dialog and its
  package are device text — and lets the agent act *inside* it.
- **The persistent reader after an app crash — closed (ADR-0034 §2).** In m1m's monitored run the reader process
  died around the app's crash and was restarted for the next read, which made that tap
  take 8.7 s; a restart is not reported (by design, warnings are for fallbacks). The crash
  is now reported instead: an action that leaves the app's process gone carries `appCrash`,
  from the crash buffer since the action began (else `dumpsys activity exit-info`), and the
  restart stays unreported.
- **Verified interactions in agent runs** took 1.5–6 s for taps and up to 13 s for inputs
  across the m1m comparison, against 0.4–1.7 s on a quiet emulator. In the monitored run
  (dy6) the reader kept its pid through every interaction and the emulator had 300–450 MB
  free; the app's rendering on a software-GL emulator, polled every ~0.6 s, is the
  hypothesis, not measured.
