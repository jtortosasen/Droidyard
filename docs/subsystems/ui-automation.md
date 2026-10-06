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

- Droidyard's own reader ([ADR-0029](../adr/0029-ui-reader-dex.md)): a small dex run with
  `app_process`, pushed to `/data/local/tmp/` and removed after the read, connecting
  `UiAutomation` with `FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES`, so the device's other
  accessibility services keep running. `uiautomator dump /dev/tty` (with its `/sdcard`
  fallback and one idle retry) only when the reader cannot run, with a warning that it
  suspended them.
- Dumps are **serialised per device**: Android allows one `UiAutomation` at a time per
  device (the reader, ADR-0029) and `uiautomator` does not tolerate concurrent dumps, so `uiTree` and the polls of a `tapElement` share one per-device dump lock.

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
│   │            selected, focused, password, disabled
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

The tap lands at the centre of the node's visible bounds. When the node itself is not
clickable, the result says so and names its nearest clickable ancestor, so a tap with
no effect is explainable.

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
2. **Tap**: `input tap x y` in display pixels.
3. **Settle**: wait `settleMs` (default 300 ms).
4. **Poll**: read again and again — each read starts at least 250 ms after the previous
   one started; a read itself takes roughly 0.5–2 s — until every condition holds or
   `timeoutMs` elapses (default 5 s, maximum 20 s). The first read after the tap runs to
   the call's running limit, not to `timeoutMs`, so a slow fallback read is still observed;
   `timeoutMs` bounds the polls after it.
5. **Return.** With no `expect`: one read after the settle, for `foreground` and the
   optional tree, then return; the result omits `expect` rather than reporting a `matched`
   it never checked.

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
  "tree": null
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
- `returnTree: true` adds the last snapshot, saving a `uiTree` call at the cost of
  tokens.

### Concurrency

A `tapElement` holds the device's **write lock** from the before-dump to the last
poll, so two callers cannot interleave a tap with another's verification. `uiTree` is a
read, and waits only for the dump lock.

## Typing: `device.inputText`

```text
device.inputText {device, text, target?, clear?, submit?, expect?}
```

- `target?` — a selector, the same vocabulary as `tapElement`. Omitted, the text goes
  to whatever holds focus.
- `clear?` — empty the field first (select-all, delete), only meaningful with a target.
- `submit?` — press ENTER after typing.
- `expect?` — the same assertion vocabulary, sequencing and timing as `tapElement`.

**Delivery**, chosen by the text itself, never by guesswork:

| Text | Path | On failure |
| --- | --- | --- |
| ASCII only | `input text`, with spaces as `%s` and shell metacharacters escaped | — |
| Non-ASCII (CJK, emoji) | ADBKeyboard's `ADB_INPUT_B64` broadcast, when that IME is installed and selected | `DEVICE_TEXT_NOT_DELIVERABLE`, naming ADBKeyboard |

Non-ASCII is **never silently mistyped**: without the IME the call fails and says what
to install. The `method` in the result records which path was taken.

**Result**: `{typed: true, characters, method: "input_text" | "adbkeyboard", matched?}`.
`matched` appears only when `expect` was given.

**The rules shared with `tapElement` apply unchanged**: after typing, the call never
returns an error because a check failed — `matched: false` is a *completed* call, since
an error would invite a retry and a retry types twice; and a check that was already
true before typing is flagged `vacuous`.

**Errors**: `DEVICE_ELEMENT_NOT_FOUND`, `DEVICE_ELEMENT_AMBIGUOUS`,
`DEVICE_ELEMENT_DISABLED`, `DEVICE_TEXT_NOT_DELIVERABLE`.

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
- **Text**: `input text` is ASCII-only. Text is escaped (`%s` for spaces, shell
  metacharacters escaped); non-ASCII (CJK, emoji) goes through the ADBKeyboard IME if
  installed, otherwise the call is **refused with an install hint — never silently
  mistyped**.
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
| Target disabled | `DEVICE_ELEMENT_DISABLED` | The node |
| Stale ref | `DEVICE_STALE_REF` | Candidates with the same text/desc |
| No accessibility info | `DEVICE_ELEMENT_NOT_FOUND`, `emptyReason: noAccessibilityInfo` | Points at `device.screenshot`; OCR is post-v1 |
| Expectation unmet | — (completed) | `matched: false` with `unmet` |

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
