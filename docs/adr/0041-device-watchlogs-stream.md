# ADR-0041: `device.watchLogs`, a session-owned Logcat stream

- Status: Accepted
- Date: 2026-10-09
- Superseded in part by: ADR-0042 (§Decision)
- Superseded in part by: ADR-0051 (§4)
- Relates to: ADR-0010, ADR-0015 §1, §2, ADR-0016, ADR-0020, ADR-0021 §Pid records;
  `capability-api.md` §Events and subscriptions; plan m2a Q2
- Decided by the owner, 2026-10-09: Logcat streams to the UI, lines shown as they arrive.
  The shape below is Delegated (the main session, from plan m2a Q2, reviewed in two rounds).

## Context

The UI's Logcat panel must show lines as they arrive. `device.logs` is a snapshot or a
bounded follow that returns at its end; polling it every second shows one-second chunks,
which the owner rejected. The register lists `device.logcat` as a topic with no producer
and no shape. `file.watch` is the precedent for a capability that owns a stream: it opens
a session subscription with an end hook and publishes with `Bus.PublishTo`, outside the
replay ring. ADR-0021 §Pid records already names "a `logcat` … stream" among recorded
children.

## Decision

1. **Capability** `device.watchLogs`: kind command (it creates a subscription), scope
   global (devices are global; `workspace.close` does not touch it), statefulness
   `streaming`, audience `all`, not long-running (it completes once the stream runs),
   since `0.14.0`. Annotations as `events.subscribe`: `destructiveHint` false,
   `openWorldHint` true.
2. **Params**: `device` (required); `tail`, 1–5,000 lines of history first (default 1,000),
   or `since` (RFC 3339, device clock, a reconnect's resume), not both; `applicationId`,
   filtered on the host by logcat's uid column with the uid resolved once at start. A
   device without the uid column, or an application `pm` does not list, fails
   `DEVICE_LOGS_FAILED` with "install it, then start the stream again". The buffers are
   always `main,system,crash`. No priority, tag or text filter: the UI filters; either can
   be added later as a minor change.
3. **Result** `{subscriptionId, device, uid?}`. **Errors**: those of `device.logs` that
   apply (`DEVICE_ADB_NOT_FOUND`, `DEVICE_ADB_FAILED`, `DEVICE_NOT_FOUND`,
   `DEVICE_NOT_READY`, `DEVICE_NOT_ALLOWED`, `DEVICE_DISCONNECTED`, `DEVICE_LOGS_FAILED`,
   `REQUEST_INVALID_PARAMS`) and `SESSION_REQUIRED`. No new code.
4. **Events**, topic `device.logcat`, delivered with `PublishTo` to the call's subscription
   only, outside the replay ring and not subscribable through `events.subscribe` (as
   `file.changes`):
   - `lines`: `{device, lines: [StreamLine], dropped, untrusted: true}`; `StreamLine` is
     its own type (`time`, `pid`, `tid`, `priority`, `tag`, `message`; no `crash`), with
     `tag` and `message` tagged untrusted.
   - `stopped`: `{device, reason: deviceGone | exited | replaced, cause?}`. Only
     `replaced` and a logcat exit the daemon did not cause publish it. Afterwards the
     subscription stays open and empty until `events.unsubscribe` or the session's end.
   - The payloads are published in the schema under a new optional descriptor field
     `events` (`{topic, types: {<type>: <schema>}}`), so the generated client types them
     and the untrusted marks reach it (ADR-0016, ADR-0015 §1).
5. **Batching and backpressure** (working defaults, constants with a comment): lines are
   read without pause into a pending buffer; each 50 ms tick publishes at most one event of
   at most 500 lines or 256 KiB, a ceiling near 10,000 lines/s. A pending buffer over
   10,000 lines drops its oldest, counted in the next event's `dropped`. A subscription
   queue that overflows yields the bus's `events.gap`, as today. Nothing is dropped
   silently.
6. **Lifetime**: one `adb -s <serial> logcat -v threadtime,epoch,usec,uid -b
   main,system,crash -T <tail or since>` child per stream, spawned recorded (a
   `device-stream` pid record, the death signal) on a context of its own, since the
   operation's context ends when the call completes. It is killed by its pid, never its
   group. A daemon-wide stream set holds every stream, and a stream leaves it only once its
   child is reaped and its record removed. A stream ends on `events.unsubscribe`; on the
   session's close (the subscription's end hook); when the same session starts a stream of
   the same device (`replaced`); when logcat exits (`deviceGone` if `device.list` no longer
   has the serial, else `exited`, with a cause naming the exit code, never device text);
   on the operation's cancel during start; and at shutdown, where the daemon's `AfterOps`
   calls the set's `StopAll(ctx)`, which kills each child and waits for every stream,
   including those already ending through their end hook, before
   `Supervisor.Shutdown`. A daemon killed outright leaves a record the next one's
   `Reconcile` reaps. The device policy is checked at start (ADR-0015 §2); a later policy
   change does not end a running stream.
7. **Who may call it**: any session that carries events, today the WebSocket (ADR-0010).
   The MCP adapter lists no streaming capability, and the CLI and `POST /v1/call` get
   `SESSION_REQUIRED`. Agents keep `device.logs`, unchanged.
8. **Versioning**: a new capability and topic are a minor change, `0.13.0` → `0.14.0`.

## Consequences

- The UI shows lines within one batch interval of logcat writing them, and one stream costs
  one `adb logcat` process for as long as its subscription lives.
- Descriptors gain an optional `Events` field, and the schema an `events` object per
  capability that has one; `file.watch` can publish its payloads the same way later.
- The register row "event topics with no producer" loses `device.logcat`.
- Documents to change: `capability-api.md` §Events and subscriptions (`device.logcat`
  joins `file.changes` as a capability's own stream), in plan m2a step 4.

## Alternatives considered

- **Poll `device.logs`**: no contract change, but lines arrive in one-second chunks;
  rejected by the owner. It also retains every poll's result as an operation for
  15 minutes.
- **An `events.subscribe` topic `device.logcat` with a filter**: the filter is deferred,
  and a shared topic would run one logcat per device whatever each subscriber wants,
  with no owner to end it when the last one leaves.
- **Raw text events**: the UI would parse logcat, against the rule that a consumer never
  parses raw output.
- **A hand-written TypeScript payload type**: a second copy of the contract, against
  ADR-0016.
