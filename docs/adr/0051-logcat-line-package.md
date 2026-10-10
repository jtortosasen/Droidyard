# ADR-0051: `device.watchLogs` lines carry their application's package, from the line's uid

- Status: Accepted
- Date: 2026-10-11
- Supersedes: ADR-0041 §4 (in part: the `StreamLine` fields)
- Relates to: ADR-0041 §2, §5–§7, ADR-0042, ADR-0015 §1, ADR-0036; `adb-device.md`
  §Filtering by app survives the app's death; plan m2e Q2, D38, D39
- Decided by the owner, 2026-10-11: Logcat filters with Android Studio's query language,
  `package:mine` included. The shape below is Delegated (plan m2e Q2, reviewed in two rounds).

## Context

`package:` and `package:mine` filter by the application a line belongs to. A streamed line
carries `pid` only, and the stream's one application filter (`applicationId`, by uid) is
resolved once at start. The query runs on the client over whole-device streams, so each
line must name its application.

## Decision

1. Each `lines` event's line carries an optional `package`, untrusted (device-derived): the
   one package `pm list packages -U` lists for the line's appId (`uid % 100000`, the
   application range 10000–19999 only). It is absent for a shared, system, named or unknown
   uid, and on a device without logcat's uid column.
2. The stream reads `pm` when a line's appId is not yet known: one refresh at a time, at
   most one a second, a short child on the stream's context, killed and joined with it,
   unrecorded like `device.logs`' `pm` read. A line waits for the refresh its appId
   started, so a fresh install's first lines are named and lines keep their order; a full
   pending buffer, a failed refresh or the stream's end releases held lines without the
   field. An appId absent from a completed refresh is not asked again in that stream.
3. `device.logs` is unchanged. Version `0.18.0`; streaming, so no MCP change. No new error
   code.

## Consequences

- A line waits at most the rate gap plus one `pm` call, at an application's first line.
- An appId freed and reused by another package within one stream keeps the old label until
  the stream restarts.
- `process:` needs a process name per line: a later minor change.

## Alternatives considered

- **A process name from `ps -A` per unseen pid**: a pid that dies before `ps` runs (a
  startup crash) loses its name, pid reuse makes names stale, and a process name is not
  always the application id. It stays the way to add `process:`.
- **`uid` on each line and a uid → package query for the client**: an MCP tool (ADR-0036's
  budget) or a widened audience for one panel, and the client would still hold lines.
- **Filter by query in the daemon**: the query language would enter the contract, and every
  keystroke would restart a stream.
- **`package` on `device.logs` too**: tokens for a field its callers, which filter by
  `applicationId`, do not need.
