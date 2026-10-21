# ADR-0123: A fast change travels in the open session and renews the view

- Status: Accepted
- Date: 2026-10-22
- Supersedes: ADR-0078 §2 (in part: a fifth stop reason, `renewed`), ADR-0118 §4 (in part: a changed mark travels in the open link session, not in a new hello)
- Relates to: ADR-0118 §3, ADR-0104 §4, ADR-0115 Decision 4, ADR-0098 §4; plan m2za Q1, Q2
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated (plan m2za Q1, Q2, reviewed in two rounds). The scope is the owner's (2026-10-21): the client switches a lent device's view between fast and full quality from the CLI or the UI, and the open view comes back by itself.

## Context

Today a changed `fast` mark closes the client's link session so that the next hello carries
it (ADR-0118 §4). The open view then ends `deviceGone` and waits for a Restart, the UI
re-reads the device list while the remote is missing and drops the selection, and the
session's end costs more than the view: the lease (ADR-0104 §4), a running UI recording
(ADR-0115 Decision 4), the remote's Logcat and UI events streams. On the host a view's end
releases its source reference on a goroutine, so a quick re-watch can join the old source
and keep its options.

## Decision

1. **The mark travels in the session.** The client sends `{"mark": {"fast": <bool>}}` on
   `/peer/v1/ws` to a host whose hello names API 0.36.0 or later, written outside the link's
   and the host's locks; the host applies it in its read loop, before the next frame. A mark
   changed between the hello and the link coming up is sent as soon as it is up.
2. **The client renews its views of that host.** Once the frame is written, it ends each of
   its relayed views of that host with a fifth stop reason, `renewed`; the UI watches again
   for it as for `detached` (once per failure streak) and does not re-read the device list.
3. **A view's end drops its source at once.** On the host, and on a relayed source, the last
   reference is dropped before `end` returns; only the teardown waits on a goroutine. So the
   re-watch, which follows the answer to `events.unsubscribe`, starts a new source with the
   new preset when nobody else holds the old one (ADR-0118 §4's rule otherwise stands).
4. **An older host** gets no frame: the client reconnects as before, and its view ends
   `deviceGone`.

## Consequences

- A fast change blanks the client's view for a start (about 1–2 s on loopback) and keeps the
  link, the lease, a recording and the other streams.
- `stopped.reason` gains `renewed` (API 0.36.0); a consumer that does not know it reads a
  stream that ended.
- A host older than 0.36.0 keeps the reconnect's costs; the register says so.
- Documents: `peer.md` §The peer port (the mark frame), `adb-device.md` §Screen viewing (the
  reason).

## Alternatives considered

- **Today's reconnect with a `renewed` reason**: smaller in the peer protocol, but the
  session's end still releases the lease, ends a recording and the other streams, and opens
  a gap in which calls are unreachable and a list read drops the selection.
- **The relayed source re-attaching itself**: the browser's socket would survive, but the
  source's one-shot life becomes a loop over host streams, subscription, control line,
  finger and GOP, for one setting.
- **The UI recognising it alone**: it cannot know of a CLI change, and the cause text is not
  the contract.
- **Reusing `detached`**: no enum change, but `detached` means the browser's socket, and a
  consumer could not tell the two apart.
- **A `renewed` end for an older host too**: the view would reopen into the reconnect's gap,
  with the costs above.
