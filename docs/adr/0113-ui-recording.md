# ADR-0113: A UI recording: every distinct screen of a span, with its events

- Status: Accepted
- Date: 2026-10-21
- Supersedes: ADR-0110 §Decision 2 (in part: the daemon now keeps device text on the host, in a recording); ADR-0012 §Durability classes (in part: a daemon-wide durable class, untrusted by class); ADR-0021 §Layout (in part: `recordings/`)
- Relates to: ADR-0010; ADR-0015 §1, §4; ADR-0019 §Sessions; ADR-0020; ADR-0034 §1; ADR-0048 §1; ADR-0088 §4; ADR-0101; ADR-0108; ADR-0111; ADR-0114; plan m2x Q1–Q4, Q6–Q9; [the spike](../research/spike-ui-recording-2026-10.md)
- Decided by the owner, 2026-10-21: recordings of the full dump over a span, started and stopped from the web UI and by agents, to see how an app or a page loads. Q9 is the owner's answer. The shape is Delegated (plan m2x, reviewed in three rounds).

## Context

`device.uiDump` (ADR-0110) is one instant; `device.uiEvents` (ADR-0111) says what changed but
carries no tree. The owner writes parsers over accessibility trees and wants the sequence of
trees between a start and a stop that he syncs to a test by hand. The spike measured a full dump
at 27 ms on native screens and 84–231 ms on a 688-node web page, slower (up to 1.7 s, twice past
the 2.5 s bound) while Chrome builds its tree during a load; a load produces a few dozen events,
and dumps cause none that keep documents changing.

## Decision

1. **Capture rule.** A dump at start; then whenever an event came after the previous dump's
   request and a gap has passed since that dump ended (100 ms, doubled after each dump identical
   to the one before up to 2 s, reset by a stored dump); one at stop. A dump identical by SHA-256
   to the previous is counted (`unchanged`), not stored. After a `busy` answer the recorder
   pauses 5 s, longer than the slowest dump measured, so it never holds both reader workers.
   Lost states are visible: each dump's `time`, `ms` and `waitedMs`, `missed` lines (a dump past
   the bound, over 8 MiB or unreadable) and `lockWaits`.
2. **The lock.** The recorder holds the read lock only for each dump (waiting at most 200 ms),
   uses the persistent reader only, and polls the ring through ADR-0114's patient reach.
3. **A daemon-owned job.** `device.recordUi {device, invisible?, maxSeconds?}` is a short
   command that starts the recorder and returns the running recording after its start dump; it
   outlives the session that started it (as a detached share, ADR-0108). One per device.
   Bounds: `maxSeconds` 10–3600 (default 900), 10,000 dumps, 512 MiB. Ends: `stopped`,
   `maxDuration`, `maxDumps`, `maxBytes`, `deviceGone`, `failed`, `daemonExit`; a reader restart
   is a gap, not an end.
4. **Who stops it.** `device.stopUiRecording {device}` cancels no operation (ADR-0020 untouched);
   ADR-0101's reach rule applies to the recording's stored starter: the UI and CLI stop any, an
   MCP agent only its own session's or chat's. The chat id is kept on disk for that rule only;
   what is published names `startedBy {client, session?}` (ADR-0048 §1, ADR-0088 §4).
5. **One file per recording.** `<state>/recordings/<id>/recording.json` and `timeline.jsonl`
   (0600 under the 0700 root), behind the durable artifact `droidyard://artifact/r.<id>`,
   `application/x-ndjson`, untrusted by class. Lines in write order: `recording` (header),
   `event` (`device.uiEvents`'s fields), `gap`, `dump` (device time, duration, wait, trigger,
   nodes, foreground, sha256, the whole XML), `missed`, `end`. `droidyard artifact cat` saves it
   without the content passing through a model; `GET /v1/artifacts/{id}` serves it as text with
   `Range` (ADR-0012, rule 8). After a crash the next start finalises a running record with whole
   lines and `daemonExit`. Retention: 50 recordings and 2 GiB of ended ones, oldest first, never
   a running one; deleted from the web or the CLI only.
6. **The rest of the surface.** `device.uiRecordings` (query), `device.watchUiRecordings`
   (streaming, web only; topic `device.uiRecording`, broadcast, no device text, no chat id),
   `device.deleteUiRecording` (audience human). One error code, `DEVICE_UI_RECORDING_ACTIVE`.
   The web: Record/Stop and the kept list with Download and Delete in the UI events panel; no
   viewer.
7. **Policy and untrusted text.** `recordUi` authorizes as every device call (ADR-0015 §4); stop,
   list, watch and delete touch only daemon records. Dumps are the active window only
   (ADR-0110 §4) and events carry no notification text (ADR-0111 §3).
8. **One `UiAutomation`.** A recording holds the device's reader, and so its one
   `UiAutomation`, for its span: a UiAutomator, Appium or Maestro run cannot start beside it.
   The owner runs none of them with Droidyard (Q9).

## Consequences

- Device text is kept on the host for the first time, deliberately: the owner wants spans for his
  parsers. It lives under the state root at 0600 and goes with its record.
- A state that lasts less than one dump (an animation frame) can be missed; during a burst the
  recorder dumps as fast as the gap allows and always stores the settled state.
- Each dump first waits up to 500 ms for the screen to go idle (the reader's rule, unchanged), so
  stored states lean toward pauses; an agent's `uiTree` beside a recording waited +67–84 ms at
  the median in the spike.
- API `0.33.0`; three MCP tools (`device_recordUi`, `device_stopUiRecording`,
  `device_uiRecordings`).
- Documents: `ui-automation.md`; the contract is the code and `schema/capabilities.json`.

## Alternatives considered

- **A fixed-interval sampler.** Dumps a static screen over and over and still misses what
  happens between samples; the event rule dumps only when something changed.
- **A dump per event, or only after settle.** The first overruns the reader on a burst; the
  second loses the intermediate states of a load, which are what the owner wants.
- **A long-running `recordUi` operation.** An MCP start would block 20 s, an interrupt would end
  the recording, and a stop from another session would be a domain cancel (ADR-0020, ADR-0101).
- **A zip of XML files.** Needs a byte mode in `artifact.read`, a fourth type under rule 8 and a
  zip writer; one JSONL file is the bundle and every language reads it.
- **Ephemeral artifacts or the workspace's `artifacts/`.** The first expire in 15 minutes; the
  second is per workspace, and devices are global.
