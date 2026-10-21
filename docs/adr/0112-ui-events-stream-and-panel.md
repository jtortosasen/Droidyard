# ADR-0112: The UI events stream and its panel in the web UI

- Status: Accepted
- Superseded in part by: ADR-0114 (§Decision 3)
- Superseded in part by: ADR-0115 (§Decision 1)
- Date: 2026-10-20
- Relates to: ADR-0010; ADR-0015 §1; ADR-0041 §4, §6; ADR-0078; ADR-0111; plan m2w Q8, Q9
- Decided by the owner, 2026-10-20: the web UI shows the screen's events live. The shape is Delegated (plan m2w Q8, reviewed in two rounds).

## Context

ADR-0111 keeps the screen's events in the reader and lets a caller read them since a cursor. The
web UI can hold a stream where an agent cannot, and the live view (ADR-0078) and the log streams
(ADR-0041) already show the pattern: a streaming capability that is never an MCP tool, publishing
to a topic for the session that asked. A browser polling `device.uiEvents` would work too, but
every poll is an operation, and Activity would fill with them.

## Decision

1. **`device.watchUiEvents {device}`** → `{subscriptionId, device}`: kind command, streaming,
   never an MCP tool (ADR-0010), not lendable, one stream per session and device (a second
   replaces the first).
2. **Topic `device.uiEvents`**, not subscribable on its own: `events {device, events[], gap?,
   untrusted: true}`, the same events and gaps as ADR-0111, and `stopped {device, reason:
   deviceGone | replaced | failed, cause?}`.
3. **Its poller** asks the reader for `events` every 250 ms (working default) after its own
   cursor, publishes only when there is something, recovers an unreachable reader as ADR-0111 §8
   does once per failure, and publishes `stopped failed` when a restart fails, never retrying in a
   loop. It ends with the subscription, the session, the device or the daemon's `StopAll`.
4. **The panel** "UI events" in the right region: it watches the toolbar's device only while the
   tab is current and the panel visible, newest first, at most 500 rows, a gap shown as a row,
   `stopped` with a Restart. Device text is rendered as text nodes only (ADR-0015 §1).

## Consequences

- A human sees what the agent's actions did on the screen as it happens, beside the live view.
- One poller per watching session and device, each a request every 250 ms over the forward;
  hidden panels watch nothing. A watched device keeps its reader past the 30-minute idle exit
  (ADR-0111 §4).
- API `0.32.0` with the agent capabilities; no MCP tool is added by this decision.
- Documents: `ui-automation.md` §The tree; the web UI's panel list is the code's.

## Alternatives considered

- **The browser polling `device.uiEvents`.** One operation per poll in Activity, four a second per
  tab, and no session lifetime to stop it when the tab goes.
- **Exposing the stream over MCP.** MCP has no stream for a tool's result (ADR-0010); the agent
  path is ADR-0111's cursor and wait.
- **Leaving the panel out of the milestone.** The owner asked for it, and it adds no risk beyond
  ADR-0111's ring and cursor; the milestone could stop before it with the agent path proven.
