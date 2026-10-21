# Subsystem: UI automation

Observing and acting on an app's UI through the accessibility tree: `device.uiTree`,
`device.tapElement`, `device.inputText`, the selector and assertion vocabulary they share, and
the input primitives underneath. Device targeting, transport and the device model are in
[`adb-device.md`](adb-device.md). The parameters, results and error lists are the descriptors'
(`internal/domain/device`); this document keeps the rules behind them. It encodes the prior-art
findings on identity-first resolution and one-call verification
([prior-art](../research/prior-art.md),
[concepts](../research/dsh-codex-android-concepts.md) §4, [mobile-mcp](../research/mobile-mcp.md)).

## Design rules

- **Tree-only in v1.** Every match and assertion reads the accessibility tree from the reader
  (ADR-0029). Screenshots are for humans and models, never an input to matching. The prior art's
  `expect_text` polled **OCR**; OCR is out of v1
  ([ADR-0013](../adr/0013-ocr-optional-pluggable.md)), so assertions are defined on the tree alone.
- **Identity first**: `resource-id` > content-desc / text > coordinates.
- **Act and verify in one call** ([mvp](../roadmap/mvp.md) golden path step 7).
- **An unmet expectation is a result, not an error**, like a failed build. Errors mean the action
  could not be performed.
- **Device text is untrusted data** ([ADR-0015](../adr/0015-untrusted-data-authorization.md)):
  compared as literal strings, never interpreted.
- **After an action, never an error because a check failed**: the effect landed, an error would
  invite a retry, and a retry taps or types twice. Only a failure *before* the action is an error,
  and then nothing was done.

## The tree: `device.uiTree`

- Droidyard's own reader, kept running ([ADR-0030](../adr/0030-persistent-ui-reader.md),
  [ADR-0029](../adr/0029-ui-reader-dex.md)): `UiAutomation` connected with
  `FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES` so the device's other accessibility services keep
  running (a warm read took 14 ms on a Pixel_9a; the first ~1.9 s). When it cannot run, the one-shot
  reader of ADR-0029 reads once, and only then `uiautomator dump /dev/tty` (with its `/sdcard`
  fallback and one idle retry), each with a warning; reads after a `tapElement` or `inputText` action
  never use the dump.
- Reads are **serialised per device**: Android allows one `UiAutomation` at a time per device, so
  `uiTree` and the polls of an action share one per-device read lock, waited inside the read's
  deadline.
- **Flat**: `nodes[]` is in document order and `children` holds refs, not nested nodes, so the
  published output schema stays fully inlined with no recursive type (1e, Q1). A ref (`n12`) is
  valid with its snapshot only; each read mints a snapshot id and the last 16 per device are kept.
- **Compact**: flags appear only in their non-default state, and layout-only nodes (no id, text,
  desc or flag) are folded into their parent. `editable` is carried by the persistent reader only.
- **Bounded**: a byte cap (~40 KB, `DefaultUITreeBytes`) prunes the deepest levels first, with
  `truncated: true` and a hint to narrow with `within`.
- **Empty says why** (`emptyReason`): `within` matched nothing, the cap pruned everything below it,
  or the window exposes no accessibility nodes at all (a WebView without accessibility, Flutter
  without semantics, a canvas or a game); the last points at `device.screenshot`, since OCR is
  post-v1.
- **Compose caveat**: Compose exposes no resource-ids unless the app opts in with
  `testTagsAsResourceId` (the sample app does); never assume stable ids exist.

## The full dump and the events between reads

`device.uiDump` ([ADR-0110](../adr/0110-full-ui-dump.md)) is the tree for a parser, not for an
agent that acts: every node of the active window, uncompacted and uncapped, in `uiautomator dump`'s
XML with the attributes it lacks added, stored as an artifact that `droidyard artifact cat` saves.
The reader already reads every node `uiautomator dump` reads; what `uiTree` leaves out it leaves out
when it compacts ([spike](../research/spike-ui-dump-events-2026-10.md)). Other windows are not read:
Android lists only interactive ones, and SystemUI's carry notification text.

`device.uiEvents` ([ADR-0111](../adr/0111-ui-events-between-reads.md)) tells what happened between
two reads, from a ring the reader fills with no client connected, and waits for a change or for the
screen to settle instead of polling blind; a lost stretch is always a gap. Only a toast keeps its
text. `device.watchUiEvents` ([ADR-0112](../adr/0112-ui-events-stream-and-panel.md)) streams the
same events to the web UI's panel.

## Selectors

One grammar, used for the tap target, for `within` and inside expectations. `id`, `text`, `desc`,
`class` combine with AND. `within` is an ancestor selector (not nested further) and **matches its
fields exactly**; the contains fallback applies to the target only. `index` (0-based, document
order) breaks an ambiguity deliberately.

**`ref` resolves by identity, not by position.** The daemon looks the ref up in its snapshot,
takes that node's identity (class, id, text, desc, bounds) and finds it in the fresh tree. Not
found is `DEVICE_STALE_REF` with candidates, so `gone: {ref}` means "that element is gone". Every
ref a result returns comes with its snapshot; an unknown or evicted snapshot is `DEVICE_STALE_REF`.

### Resolution

1. **Exact** match on every given field.
2. None: **case-insensitive contains** on `text` and `desc` (`id` and `class` stay exact); the
   result reports `matchedBy: exact | contains`.
3. **Fold nested duplicates**: when a match and one of its ancestors both match (Compose merges
   semantics, so a clickable row and its label often carry the same text), keep the outermost.
4. **Off-screen** matches (no intersection with the display, or zero area) are dropped; if they
   were the only ones, `DEVICE_ELEMENT_OFFSCREEN`.
5. More than one left and no `index`: `DEVICE_ELEMENT_AMBIGUOUS`, listing up to 10 candidates.
6. None: `DEVICE_ELEMENT_NOT_FOUND`, with near candidates and the tree's `emptyReason`.
7. The one match is **disabled**: `DEVICE_ELEMENT_DISABLED`. Disabled targets are not dropped in
   step 4: tapping one is a caller error worth naming.

The tap lands at the centre of the node's visible bounds, **unless a node drawn above it covers
that centre** (`occlusion.go`, plan m1f D22): the point moves to the centre of the largest
uncovered strip, at least 8 px inside the node, ignoring a node whose bounds are the whole display
(a full-screen overlay is usually transparent), and the result warns naming the covering node;
when nothing visible is left, the call fails `DEVICE_ELEMENT_OFFSCREEN` with the covering node and
taps nothing. When the node itself is not clickable, the result says so and names its nearest
clickable ancestor, so a tap with no effect is explainable.

## Acting: `device.tapElement`

### Assertion vocabulary

`expect` is a list (at most 5) of conditions, all of which must hold: `appears: Selector` (at least
one on-screen node matches, in one dump); `gone: Selector` (no node matches, in **two consecutive**
successful dumps, because a dump taken mid-transition can miss nodes still there); `foreground:
applicationId` (the top window belongs to this app, which catches a crash or a navigation out of
it). Counting arrives with the row abstraction, post-MVP; there is no text-by-regex condition (`appears`
with `text`, exact then contains, covers the MVP's cases).

### Sequence and timing

1. **Before**: dump, resolve the target, evaluate every condition against this tree. A condition
   that already holds is flagged **vacuous**: flagged, not refused (the prior art's guard refused
   before tapping; flagging keeps the single round trip, and a vacuous match proves nothing).
2. **Tap**: through the persistent reader when it can run (an injected touch at the display point,
   [ADR-0035](../adr/0035-reader-acts-fenced-by-peer-uid.md)), `input tap x y` in display pixels
   when it cannot or when the reader denied the request before executing it. A tap the reader took
   but never answered is never repeated: the call goes on to the read and poll with a warning that
   the tap may have landed.
3. **Poll**: read right after the tap and again and again, each read starting at least 250 ms after
   the previous one started, until every condition holds or `timeoutMs` elapses (default 5 s,
   maximum 20 s). The first read after the tap runs to the call's running limit, not to
   `timeoutMs`, so a slow fallback read is still observed.
4. **Return.** With no `expect`: one read after the tap, for `foreground` and the optional tree;
   the result omits `expect` rather than reporting a `matched` it never checked.

There is **no fixed settle** after the tap (plan m1n D2, superseding m1f D10's 300 ms constant):
the first read starts at once, and its settle is the persistent reader's own idle wait (answering
once the screen has been quiet for 100 ms, 500 ms max); each later poll asks for a shorter bound
(100/200), so a screen that never goes quiet does not make every poll pay the full wait. Only the
reader has this idle wait. The defaults are starting values (spike S6 would set them); the 20 s cap
keeps `tapElement` a bounded, non-long-running call ([ADR-0020](../adr/0020-operation-contract.md)).

Between the before read and the tap, the call checks the **soft keyboard** (the rule is the
descriptor's): a tap point under the IME is moved off it by one BACK and a re-resolve, with a
warning, and a point still covered fails `DEVICE_ELEMENT_OFFSCREEN` naming the keyboard, having tapped
nothing. On the `uiautomator` path the guard never runs, since only the reader's dump carries
`editable`. (Delegated, plan m1n step 5: found at level 5, where a target under the keyboard took a
key press; the owner can revert it.)

### Result

- `matched: false` is a **completed** call; `unmet` lists each failing condition with what the
  last tree showed. A read that fails while polling counts as a poll without an observation
  (`failedDumps`); if every poll failed, `matched: false` with the reason `noObservation`, and
  `foreground.after` is omitted with a warning. A cancellation after the tap is a completed call
  too, carrying `tapped` and, with `expect` not yet matched, the reason `cancelled`.
- **A vanished app is named, not guessed at**
  ([ADR-0034](../adr/0034-device-calls-report-busy-crash-dialog.md) §2): `appCrash`, from the crash
  buffer else `exit-info`. It is never read on a call whose foreground held, so a normal tap pays
  nothing, and never for the system ui's own `android` window; an `inputText` without `expect` has no
  after read, so one `pidof` decides.
- **A system dialog over the screen is named** (ADR-0034 §3): `systemDialog` when a read's tree holds
  the system error dialog's buttons **and the tree's foreground is the system ui's `android`, not an
  app**. A target **inside** the dialog is acted on as usual; a target the dialog hides (one that does
  not resolve at all, never an ambiguous, disabled or off-screen one) and any `inputText` without a
  target fail `DEVICE_NOT_READY` before anything is tapped or typed, the failed call marked untrusted.
  The package is device text, so it appears in `details`, never in the error's prose. Droidyard never
  dismisses the dialog itself.

### Concurrency

A `tapElement` holds the device's **write lock** from the before-dump to the last poll, so two
callers cannot interleave a tap with another's verification; `uiTree` is a read and waits only for
the dump lock.

## Typing: `device.inputText`

The delivery paths, the read-back, the partial-result and the timing bounds are
`device.inputText`'s descriptor (`internal/domain/device/input.go`); the rules behind them:

- **A before read always runs**, even with no `target` and no `expect` (plan m1n step 6): it is
  what sees a system dialog before anything is typed.
- **Delivery** goes through the persistent reader when it can run (ADR-0035), the path chosen
  before anything is tapped or typed, never by guesswork. `set_text` needs no installed IME, so any
  Unicode text is typed with nothing installed; `keys` is the reader's virtual keyboard, for
  printable ASCII only. With no reader, or the request denied before it executed, `input text`
  (printable ASCII) or ADBKeyboard's broadcast (non-ASCII, when that IME is the selected one) take
  over; without it the call fails `DEVICE_TEXT_NOT_DELIVERABLE` naming the IME, and non-ASCII is
  **never silently mistyped**.
- **Only a `denied`, or an `ACTION_SET_TEXT` the field refused before executing anything, may lead
  to another method**: once the reader has taken a request, another method could type the text
  twice. A request the reader took without answering ends the call **completed** with a warning
  that it may have applied, nothing retyped; one it answered `busy` fails `DEVICE_NOT_READY`
  **before acting**, so a pending effect is never followed by a second delivery.
- With a `target`, the node is tapped once to focus it and, on the reader path, the call waits until
  that node reports `focused` (a Compose field renders a frame later); the wait accepts only a
  focused node containing the tap point, since a field elsewhere that keeps focus (the one just
  typed into) must not count. The non-reader path delivers right after the tap, with no fixed settle
  (m1n D2). The focusing tap takes `tapElement`'s soft-keyboard guard. If the focusing tap was
  issued and the field never reported focus, the call ends **completed** with a warning that
  nothing was typed, and a crash the tap caused is diagnosed like `tapElement`'s.
- `characters` is what the field **read back**, not what was sent; a differing read-back warns
  quoting both and is **never retyped**, because a second set could type it twice; on the
  non-reader path a later chunk's failure is a *completed* result with the partial count, since a
  retry would type the prefix again.

## Input primitives

The adb verbs beneath UI actions. Agents get `tapElement` and `inputText`; scroll-to-find and
coordinate actions for agents are later and use these rules and the same `expect` vocabulary. **The
live view's input** ([ADR-0079](../adr/0079-screen-input-capabilities.md), people only) normalises
coordinates against the **stream's frame size** and needs a live view; on an emulator a click
reaches the next decoded frame in p50 51–53 ms, a tap's operation takes p50 76–83 ms (plan m2p
step 7). On a device another Droidyard lends, each input is the host's own capability call
([ADR-0100](../adr/0100-remote-devices-on-the-client.md) §3); over loopback that added about
1 ms (plan m2t step 3). **Coordinates for agents' coordinate actions are normalised 0..1** too, resolved to
pixels using the **display size from a fresh screencap**, not `wm size`, which commonly reports the
portrait panel size even in landscape; this removes rotation math entirely. **Tree-derived actions work in display
pixels**, because the tree's bounds are already in that space. Text and taps go through the persistent
reader when it can run; `input tap`, `input text` and the ADBKeyboard IME are the fallback.
**Rotation** pins `accelerometer_rotation=0` before setting `user_rotation`, so auto-rotate cannot
fight an explicit value. **Device actions** (notifications, quick settings, lock, wake, assistant) are
plain adb verbs; `wm dismiss-keyguard` is best-effort and a locked device stays locked.

## Failure modes

Codes and `details` are the descriptors' and `internal/capability/error.go`'s; the decisions are
above (never an error after the action, no retry of a taken request, a dialog refuses before
acting). A dump that fails before the tap (after one retry) is `DEVICE_UI_DUMP_FAILED`,
cause-attributed, with nothing tapped; a cancel once the tap or a typing command was issued is
completed with a warning that the effect may have landed and `expect.reason: cancelled` (D21; an
error would invite a retry that repeats the effect, and a cancel before that command is issued still
ends `cancelled`).

## Post-MVP

- **Row abstraction** for feeds whose controls are unlabelled: repeated isomorphic siblings (>= 3,
  same class, near-equal height) as rows, with counter parsing, and `count` assertions built on it.
- Coordinate taps, swipes and scroll-to-find, sharing the `expect` vocabulary.
- An OCR backend behind the same conditions, for trees with no accessibility information
  ([ADR-0013](../adr/0013-ocr-optional-pluggable.md)).
