# ADR-0135: Host-side filters for device logs, local and lent

- Status: Accepted
- Date: 2026-10-27
- Supersedes: ADR-0041 §Decision (in part: §2, "no priority, tag or text filter" on `device.watchLogs`)
- Relates to: ADR-0012, ADR-0015 §1, ADR-0036 §1, ADR-0041 §5, ADR-0051 §Alternatives considered, ADR-0060 §4, ADR-0099 §2, ADR-0100 §3, §4, ADR-0106; plan m1i R4, D4; [plan m3i](../plans/m3i-agent-filters.md) Q1–Q5
- Decided by the owner, 2026-10-27: every filter an agent needed in a terminal goes into droidyard, "and more that we can think of", so that an agent never falls back to a terminal to filter what droidyard returns. The shape below is the main session's under the owner's standing instruction to apply the recommended option, Delegated (plan m3i, reviewed in two rounds).

## Context

An agent on the owner's Samsung gave up on `device.logs` ("too much SurfaceFlinger noise")
and ran `adb logcat -d -v time | awk '$2>="11:32:28"' | rg "<7 patterns>" | cut -c1-170`.
`device.logs` could not say it: `tags` (≤ 20 exact, OR'd) and one literal `contains`, AND'd
with each other; `applicationId` narrows to one app's uid, which drops exactly the
system_server and SystemUI lines the agent wanted, and without it every line competes;
`maxLines` keeps the newest, so a start time is buried under noise. The pipeline needed an
OR across tags and messages, a regular expression (`Removed [0-9a-f]+ com.facebook.orca#`),
exclusion, several processes at once, a window with an end, the oldest lines kept, and
messages cut. `device.watchLogs` had no filter at all (ADR-0041 §2).

`contains` was literal by plan m1i R4: caller text never reaches a device shell. No ADR
rejected a regular expression run on the host. Go's `regexp` is RE2: time linear in input
× program, no backtracking. That bounds a hostile pattern's shape, not its cost: a
60-instruction alternation already scans at about 10 MB/s on this Mac (plan m3i, review
round 1), so a call's patterns on a 4 MiB read, or a peer's on a lent device, need a time
bound, not only a pattern bound.

## Decision

1. **Caller patterns are Go RE2, compiled and run on the host**, never in an adb argument
   (m1i R4 holds). At most 20 per list, 1–512 bytes each, one instruction budget for all of
   a call's compiled patterns (`textfilter.MaxCallInsts`); case-insensitivity is `(?i)`, not
   a parameter. A bad pattern is `REQUEST_INVALID_PARAMS` naming `match[i]` or `exclude[i]`
   before any adb command or host call.
2. **A time budget, not only a pattern bound.** The clock is read after every line (one
   line is the smallest uninterruptible unit). A `device.logs` filter pass that spends
   `FilterBudget` stops: the result is `truncated`, with a warning naming the budget and
   the last line's device time. A stream judges lines for at most 25 ms of each 50 ms tick;
   what it could not judge is counted in `dropped`. The values are working defaults set by
   the step 2 benchmark of plan m3i and recorded there.
3. **One line filter**, `LineFilter`, on both `device.logs` and `device.watchLogs`:
   `tags`, `minPriority`, `contains` (unchanged in name and meaning), `excludeTags`,
   `tagPriorities` (logcat's `Tag:L`, `S` silences; a listed tag's own minimum replaces
   `minPriority`), `match` (OR'd), `exclude`, `processes`. A line passes when in scope,
   then priority, `tags` if any, not in `excludeTags`, `contains`, any `match` if any, no
   `exclude`. The subject of `match` and `exclude` is `<tag>: <message>`, so `^Tag: `
   selects a tag. A line the filter rejects is not `dropped`.
4. **Processes by name.** `processes` (≤ 10 exact `ps` NAMEs) is resolved by one fixed
   `ps -A -o PID,NAME` per call or stream start, matched on the host, plus
   `Start proc <pid>:<name>/` lines trusted only from `ActivityManager` at the system uid.
   The scope is the union with `applicationId`'s; crash lines join it only through
   `applicationId`. A failed `ps` read is a warning on `device.logs` and
   `DEVICE_LOGS_FAILED` on a stream start.
5. **`device.logs`'s read shape**: `until`; `keep: newest | oldest`; `context` 0–10 from
   in-scope neighbours, counted by `maxLines`, not by `matched`; `maxMessageBytes` with a
   per-line `cut`; `collapse` with `repeats`; `topTags`, five by default when `truncated`,
   so a cut result names its noise; `events` in `buffers`, off by default. The capture
   stays the matched set. A stream takes the line filter only.
6. **Lent devices.** The host filters, since it reads the device; the client validates
   first. Every new field is `omitempty`, so a call that uses none reaches an older host
   unchanged; one that uses one is refused there by strict decoding, and the client gives
   that error the nextStep "the host runs an older droidyard that does not take
   `<field>`". Nothing is filtered on the borrowing side.
7. API `0.41.0`.

## Consequences

- A call's filter cost is bounded in time per call, not in calls: a peer may run several at
  once on a host (no cap; plan m3i §Unverified).
- A process that died before the read, with no trusted `Start proc` line left in the ring,
  is not selected; a process's lines logged before its `Start proc` line in a stream are
  missed.
- `device_logs` and `device_watchLogs` cost more `tools/list` bytes, held by ADR-0060 §4's
  rule.
- The Logcat panel keeps its client-side query (ADR-0051's rejection of a daemon-side query
  holds); `processes` is the engine a `process:` term would reuse.
- To change with this decision: `internal/textfilter`, `internal/domain/device`
  (`logfilter.go`, `logs.go`, `watchlogs.go`, `remote*.go`), the schema, and
  `docs/subsystems/adb-device.md` §Logs.

## Alternatives considered

- **Literal substrings only, several of them**: the owner's case needs `[0-9a-f]+`, and an
  agent would still pipe through `rg` for classes and anchors.
- **Globs**: no alternation and no classes; a second syntax next to RE2's for no gain.
- **PCRE**: lookaround and backreferences cost backtracking, and a hostile pattern
  exponential time on a lent host.
- **logcat's own `-e`**: caller text in the device shell (against R4), and a device-side
  engine that varies by Android version.
- **A pattern bound alone**: RE2's time is input × program, so 20 short patterns on a 4 MiB
  ring can still take seconds; only a clock bounds a call.
- **`match` with a field enum (tag, message, line)**: anchors on `<tag>: <message>` express
  it with one parameter fewer.
- **The `-v time` text as the subject**: pid padding and the priority letter vary, and
  priority has its own filter.
- **`tags` OR'd with `match`**: changes a published meaning.
- **`pidof <name>` or `--pid`**: caller text in the device shell, and `--pid` takes one.
- **Filtering on the borrowing side**: the host caps inline lines and the capture, so
  `matched` and `emptyReason` would be wrong, and a stream would carry every line over the
  link.
- **A version handshake per capability**: `capability-api.md` §Discovery keeps one API
  version; strict decoding already refuses what an older host does not know.
