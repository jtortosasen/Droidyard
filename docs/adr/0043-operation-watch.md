# ADR-0043: `operation.watch`, the activity feed and the state at load

- Status: Accepted
- Superseded in part by: ADR-0048 (§2)
- Date: 2026-10-10
- Relates to: ADR-0010, ADR-0016, ADR-0020 §Core primitives, ADR-0036, ADR-0041 §4;
  `capability-api.md` §Events and subscriptions; plan m2b Q1
- Decided by the owner, 2026-10-09: the UI shows live what any client does through the
  shared daemon, and its controls mirror operations any client runs. The shape below is
  Delegated (the main session, from plan m2b Q1, reviewed in two rounds).

## Context

`operation.state` is a designed topic with no producer (the register's no-producer row;
m1d's scope names the Phase 2 UI as its first consumer). The UI needs two things from it:
every operation's transitions as they happen, and the operations already running when a
tab opens, without a window in which one is missed. Since 2a, the operation store records
each operation's client kind and writes one trace line per terminal operation outside its
lock (plan m2a step 3), and it already holds a publisher.

## Decision

1. **Capability** `operation.watch`: kind command (it creates a subscription), scope
   global, statefulness `streaming`, audience all, not long-running, since `0.15.0`.
   Params `{}`. Result `{subscriptionId, operations: [ActiveOperation]}`: the operations
   not yet terminal, taken **after** the subscription is open, so the snapshot and the
   events together miss nothing (a duplicate is merged by id). Streaming keeps it off MCP
   (ADR-0010), so the agents' tool budget (ADR-0036) does not change, and no MCP tool
   returns another session's operation ids.
2. **Events**, topic `operation.state`, type `state`, published through the descriptor's
   `events` so the client is generated (ADR-0041 §4). The envelope's `operation` and
   `workspace` name the operation; the payload is `{capability, client?: cli|mcp|ui,
   state, startedAt, durationMs?, code?, blockedBy?}`, `client` omitted for a call
   recorded without a kind. One event when the operation is recorded (`running` or
   `queued`), one when a queued operation starts running, one at its terminal state.
   Never parameters, results, messages or causes (the trace line's rule). Not untrusted:
   no field carries device or project text.
3. **Delivery**: each event takes a seq but never enters the replay ring (as
   `file.changes` and `device.logcat`); it is offered to every `operation.watch`
   subscription and is not subscribable through `events.subscribe`. Loss is the bus's
   `events.gap`, after which the client calls `operation.watch` again. Events are
   published after the store's lock is released, in transition order.
4. **Recorded operations only.** `operation.wait` and `operation.cancel` are not
   operations and publish nothing. Every other call does, `operation.watch` itself and
   the UI's own reads included.
5. **Versioning**: a new capability and topic, `0.14.0` → `0.15.0`.

## Consequences

- The UI's activity panel and its mirrored controls read one source; an agent's build or
  boot shows in the UI as it happens, and a tab opened mid-operation sees it.
- Every watcher receives every operation's transitions, its own UI reads included; the
  panel filters those out by default.
- The register row "event topics with no producer" loses `operation.state`.
- Documents to change: `capability-api.md` §Events and subscriptions, `overview.md`
  §Events (one sentence each), `mvp.md` and `mcp.md` (the capability name), in plan m2b
  step 1.

## Alternatives considered

- **`events.subscribe` on `operation.state` plus an `operation.list` query**: two calls
  with a window between them; the stateless query becomes an MCP tool (the token budget)
  or needs a human-only audience; and `events.subscribe` topics publish no payload schema.
- **A target (serial, AVD) in the payload**: the two mirrors the owner named work without
  it (builds by workspace, boots by the one boot key); a later minor change if a mirror
  needs it.
- **Progress on the same topic**: rejected by m1d; different consumers and rates.
- **Tailing the trace log**: text the client would parse.
