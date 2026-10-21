# ADR-0111: The UI's events kept on the device, read since a cursor and waited on

- Status: Accepted
- Superseded in part by: ADR-0115 (§Decision 9)
- Date: 2026-10-20
- Relates to: ADR-0015 §1, §4, §6; ADR-0020 §Adapter projections; ADR-0029; ADR-0030; ADR-0034 §1; ADR-0099 §2; ADR-0102 §2; `ui-automation.md` §The tree; plan m2w Q4–Q7, Q9; [`spike-ui-dump-events-2026-10.md`](../research/spike-ui-dump-events-2026-10.md)
- Decided by the owner, 2026-10-20: an agent should learn what happened on the screen between two reads, and be able to wait for it, with notification text off. The shape is Delegated (plan m2w Q4–Q7, Q9, reviewed in two rounds); keeping notification text out with no switch at all is Delegated too (Q5).

## Context

`device.uiTree` is a snapshot: what happens between two reads (a toast, a transient window, a
scroll) is invisible to the agent, which can only poll blind. MCP is request and response and an
agent works in turns, so a push stream to an agent is not possible; what is possible is that
nothing is lost between calls.

The reader holds a `UiAutomation` connection for as long as it runs (ADR-0030), and that
connection receives every accessibility event once a listener is set. The spike (API 31 and 36)
showed: events arrive, and a listener changes neither `waitForIdle`'s nor an injected tap's timing
(tap medians 50 → 49 ms and 48 → 49 ms); a toast arrives as `TYPE_NOTIFICATION_STATE_CHANGED`,
class `android.widget.Toast`, with its text, 36–154 ms after the click; a notification arrives with
class `android.app.Notification` and no text in the event; the framework already throttles
content changes (a 20 ms ticker gave at most 10 events a second). Accessibility events are not in
logcat.

## Decision

1. **The ring lives in the reader.** At `serve` start the reader sets one event listener, which
   copies fields and returns: no `getSource()`, no parcelable data, no node query, so it never
   blocks the `UiAutomation` thread. Entries go to a ring of 512 (working default) with a sequence
   number from 1, the device wall time, type, package, class, window id, change types and a count.
   Each reader start draws a 64-bit `instance`.
2. **Folding, never into a returned entry.** Content, scroll and text events of the same type,
   package and window within 250 ms (working default) of the newest entry fold into it (its count
   and time). An `events` answer seals every entry it returns; a fold never touches a sealed entry,
   so a cursor past it misses nothing.
3. **Text is kept for toasts only** (class starting `android.widget.Toast`, at most 512
   characters). A notification event's text is never read on the device, and no other event
   carries text; the screen's content is read with `uiTree` or `uiDump`. There is no switch, in a
   call or in the policy.
4. **The reader's `events <afterSeq> <max>` request** (`max` 1–200) is fenced as every request
   (ADR-0102 §2), served on the accept thread so a blocked dump never delays it, and counts as a
   read for ADR-0030's idle exit.
5. **`device.uiEvents {device, since?, waitMs?, until?, quietMs?, max?}`** returns `{cursor,
   events, more?, gap?, outcome?}`. Each event is `{time, type, package?, class?, window?, count?,
   text?, changes?}`, with the types `windowState`, `windowContent`, `windows`, `textChanged`,
   `focus`, `scroll`, `click`, `selected`, `toast`, `notification`, `announcement`, and no refs.
   `package`, `class` and `text` are untrusted. The cursor is `<instance>.<seq>`, opaque to callers.
6. **A gap is never silent**: `readerStarted` (this call started the reader), `readerRestarted`
   (the cursor's instance is gone: a restart or the idle exit) and `overflow` with `dropped` (the
   cursor is older than the ring). More than `max` pending gives the oldest `max` and `more`.
7. **The wait**: `waitMs` 0–20,000, polled every 100 ms. `until: change` ends at the first event
   after the cursor; `until: settle` ends when no event came for `quietMs` (100–5,000, default
   500), counted from the later of the newest event and the call's start, so a quiet screen
   settles after `quietMs`. `outcome` is `changed`, `settled` or `timeout`.
8. **No read resource.** A poll takes no lock, so a wait never queues a read or an action. A dead
   reader behind a live forward is recovered at most twice per call: the read lock waited at most
   2 s, a `ping`, and a restart only when the `ping` fails (ADR-0034 §1). The worst case, 42 s,
   stays under ADR-0020's 60 s.
9. **No new error code; not lendable** (ADR-0099 §2 unchanged).

## Consequences

- A toast or a transient window between two calls is reported, and an agent can act and then wait
  for the screen to settle instead of polling.
- The reader keeps a listener and a 512-entry ring for its whole life; the one-shot keeps none.
  Events since the reader started are kept even when no client polls, and lost only by overflow
  or a reader restart, both reported.
- An `events` request keeps the reader alive like a dump, so a watching client keeps
  `UiAutomation` held; an unplugged device still releases it after 30 minutes.
- Notification content never leaves the device through events. Whether a human-only policy switch
  should ever let it through is a register row.
- Documents: `ui-automation.md` §The tree; `adb-device.md` §Lifecycle and concurrency.

## Alternatives considered

- **A ring in the daemon, fed by a held reader connection.** Events are lost whenever no
  connection is held, and a held connection per device is a new lifetime for the daemon to manage.
  The reader's ring records with nobody connected.
- **Logcat.** Accessibility events are not logged.
- **Folding into any entry, returned or not.** A cursor already past an entry never sees its later
  count, so a storm would read as one event.
- **Refs or node ids on events.** Each costs a node query on the thread that must not block, and a
  ref belongs to a snapshot, which an event does not have.
- **A long poll held on the device.** A reader thread per waiter, on a process whose two dump
  workers are bounded on purpose (ADR-0034 §1).
- **Reusing the reader's `waitForIdle`.** Bounded at 500 ms, and blind to which events came.
- **A call parameter, or a redaction heuristic, for notification text.** A parameter lets the
  agent grant itself a person's messages and codes (ADR-0015 §6); a heuristic is what ADR-0015 §1
  refuses.
- **Holding the read lock for the wait.** A 20 s wait would queue every read and action on the
  device behind it.
