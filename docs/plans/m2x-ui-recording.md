# Plan: Milestone 2x — A UI recording: every distinct screen of a span, with its events, local or lent

- Status: Approved
- Approved: 2026-10-21, Delegated (the main session, under the owner's standing instruction to apply the recommended option): Q1–Q8, Q10 and Q11 as recommended, a remote recording ending with its lease included; Q9 is the owner's answer.
- Reviewed: three rounds (the third on the parts the owner's two inputs changed); every blocking finding is answered in the plan.
- Date: 2026-10-21
- Milestone: "2x: record the UI between a start and a stop" — the owner's request of 2026-10-21: "get the dumps over spans of time, to know how an app or a web page loads, what it does in between … hard to catch if I launch a specific uiDump by hand", and "the UI to also have the option to start recording and stop, so I can sync it myself exactly right before starting the test and when the test ends". Widened the same day by the owner to lent devices: "Si precisamente esta opcion la hice para poder hacer dumps remotos!!", "mandaselo a 2x, asi que lo haga esa sesion aprovechando su desarrollo".
- Phase: none. The MVP is done (2h); this plan's Scope table is the boundary.
- Contract: ADR-0110 (full dump; §2, §6 amended), ADR-0111 (ring, cursor, reach §8; §9 amended), ADR-0112 (§1, §3 amended), ADR-0034 §1, ADR-0030, ADR-0029 §Consequences, ADR-0012 §Durability classes, §Reading artifacts, ADR-0021 §Layout (ADR-0054 the precedent), ADR-0020, ADR-0101, ADR-0019 §Sessions, ADR-0010, ADR-0048 §1, ADR-0088 §4, ADR-0108 (a daemon-owned job), ADR-0083 (watch and broadcast), ADR-0015 §1, §4, §6, ADR-0036; lending: ADR-0097, ADR-0098, ADR-0099 §1, §2, ADR-0100, ADR-0104 (lease; §3 amended), ADR-0106 (per-link streams); `http-websocket.md` rule 8, §`GET /v1/artifacts/{id}`; `ui-automation.md` §The full dump and the events between reads. New, Delegated, written by the main session after step 1: **ADR-R** (the UI recording: Q1–Q4, Q6–Q8), **ADR-P** (the patient events reach: Q5), **ADR-L** (the UI reads and the recording lent to a peer: Q10, Q11; supersedes in part ADR-0099 §2 (its closed list), ADR-0110 §6, ADR-0111 §9, ADR-0112 §1 and ADR-0104 §3 in part). Numbers reserved with `board.go adr`: ADR-R is 0113, ADR-P 0114, ADR-L 0115 (named by letter here until written, as m2w did).
- Register: the commit that adds this plan sets `Work` = `2x` on R1–R4 (Scope). R1–R3 gathered Delegated by the main session; R4 by the owner.
- Standing rule (owner, 2026-10-21): a new device capability is lendable by default; leaving one out is the owner's question. Every capability this plan adds is lendable.

**The owner should see:**
- **Record** and **Stop** in the UI events panel for the toolbar's device, local or lent. While it runs, the daemon stores the full dump (`device.uiDump`'s document) each time the screen changes, skipping one identical to the previous, with every `device.uiEvents` event in between.
- One file per recording, `<id>.jsonl`: header, then event, gap, dump, missed and end lines, each dump with its device time and whole XML. The panel lists the kept recordings with Download and Delete. An agent uses `device_recordUi`, `device_stopUiRecording` and `droidyard artifact cat <ref> > rec.jsonl`; the XML never passes through the model.
- A lent phone works the same: `device.uiDump`, `device.uiEvents`, the UI events panel and recordings on `<remote>/<serial>`. A remote recording runs on the host; when it ends, the client copies it whole and the host deletes its copy.
- A recording belongs to the daemon: it outlives the session that started it, ends by Stop, a bound, the device leaving, a reader that cannot be restarted, the lease ending or the daemon's exit, and says which. The UI events panel no longer stops `failed` while another call holds the reader (R3).
- While recording, the reader holds the device's one `UiAutomation`: a UiAutomator, Appium or Maestro run cannot start beside it (Q9: the owner uses none).
- One minor API version, 0.33.0 (or the next free minor).

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| The owner's request: start, a full dump per screen change, stop, a timeline; MCP and web; download | yes | steps 4–8 |
| The same on a lent device (owner, 2026-10-21) | yes | steps 3, 9, 10 |
| R1 "A full dump of a busy screen fits ADR-0034 §1's 2.5 s bound: formapp's 25 nodes took 2–11 ms; larger real apps not measured" | yes, closed or narrowed | step 1; step 11 |
| R2 "The events' 250 ms fold window and 512-entry ring suit real apps: measured on formapp only" | yes, closed or narrowed | step 1; step 11 |
| R3 "A `device.watchUiEvents` stream ends `failed` when another call holds the reader's lock past its two busy recoveries (about 4 s) …" | yes, closed | step 2 |
| R4 "Lending and relaying `device.uiDump`, `device.uiEvents` and `device.watchUiEvents` to a peer (ADR-0099 §2 unchanged)" | yes, closed | step 3 |
| "One `UiAutomation` per device: a user's instrumented test or another tool holding it conflicts with the reader …" | no | a recording holds it for its span, accepted by the owner (Q9); the row stays |
| "`device.uiDump` for every window …", "A fling's scroll events stay one … entry each …", "`collection-item-*` is absent …", "A human-only policy setting letting notification text into `device.uiEvents` …" | no | rows stay |
| A viewer; a zip or directory bundle | no | Q7, Q3; register rows |
| Two Macs over a VPN | not run | two isolated daemons on one Mac (m2t's stand-in); the real trial is the owner's (`trial` row) |
| API 36.1, physical devices, Linux | not run | Unverified |

Eleven steps, one over the skill's heuristic. A cut after step 8 (local recording done; steps 3, 9, 10 as a milestone of their own) is possible; it is not proposed, since steps 9 and 10 reuse 4–6 directly and the owner sent the remote half to 2x. The plan can stop at any step boundary.

## Starting point

- `m2x` at `f9854e1`, API `0.32.0` (`internal/domain/system/system.go:19`), ADRs up to 0112.
- **Reused** (`internal/domain/device`): the reader's `dump full [invisible]` (`dumpfull.go`; `persistentRead`, `readerDumpRequest`), which waits `waitForIdle(100, 500)` first (`DroidyardReader.java:138`); `events` (`events.go`); `eventsReach` (`uievents.go:398`; a lock held over 2 s is `recoverBusy`, two per call); the stream and `stopOf` (`watchuievents.go`); `readerClient.lock` (`persistent.go:219`), held by a read chain up to `dumpDeadline` 22 s (`dump.go:34`) and by an act up to its host bound (`tap.go:502`); `parseDump`; `devicetest.FakeReader`. Two dump workers, `busy` past 2.5 s with the worker left to finish (`DroidyardReader.java:486`). **2x changes no Java**: no dex rebuild, no G1–G5 gates.
- **Lending** (m2t, m2u): `adb.plain`/`plainThen` make a descriptor `Lendable` and relay a remote id's call to its host (`remote.go:57–98`); a relayed artifact is fetched over the peer port, checked and stored locally as untrusted (`remoteartifact.go`, `fetchArtifact`); `watchLogs` (`remotelogs.go`) and `watchScreen` (`remotescreen.go`) are relayed streams; the peer artifact route serves only a peer's own ephemeral artifacts, whole in memory, as PNG, JPEG or `text/plain` (`adapters/peer/peer.go:635–699`; `GetOwned` refuses durable ids). `device.uiDump`, `uiEvents`, `watchUiEvents` use `adb.authorize`: a remote id is `DEVICE_NOT_FOUND` with no host call (`TestUIDumpRemoteIDNotFound`). The lease: ADR-0104 (any peer call holds; idle 15 min with no call and no open stream; ends with the link, unlending, the device leaving, `session.release`; the end stops the holder's streams `deviceGone`). m2t's level 5 lent a scratch emulator as a physical stand-in (`usb:` appended by the host's adb guard).
- **Artifacts**: ephemeral in memory (15 min, 64 MiB); durable only as `ws/<wsid>/artifacts/<owner>.log`, and `CreateDurable` refuses `Untrusted` (`durable.go:72`). `artifact.read` pages any non-image as text, pages concatenating byte-exact (`text.go:137`); `droidyard artifact cat` pages at 256 KiB.
- **HTTP**: `GET /v1/artifacts/{id}` serves only PNG and JPEG (`httpapi.go:471`), though rule 8 allows `text/plain; charset=utf-8` and ADR-0012 promises text with `Range`. The web fetches no artifact today.
- **Operations and jobs**: a long-running MCP tool blocks for its 20 s wait budget (`adapters/mcp/mcp.go:723`); ADR-0101's reach rule (`operation.go:426`); `hosting.share` with `detach` is a daemon-owned job (ADR-0108); `session.watch` returns a list and broadcasts changes; `ADB.Close` runs in `AfterOps` within 5 s, after `Ops.Shutdown` (`daemon.go:448`); `AudienceHuman` keeps a capability off MCP.
- **MCP**: 34 tools, 31,187 B against 32,000 (`cmd/droidyard/toollist_bytes_test.go`; raised to the logged size rounded up to the next 1,000 B).
- **Baseline, 2026-10-21 at `f9854e1`:** `go vet ./...` clean; `go test -count=1 ./...` ok in every package; `-race` on `internal/domain/device` ok.

## Acceptance

- [ ] A1. `device.recordUi {device}` returns at once with a running recording that stores a full dump at start, after each change the ring reports (rate-bounded, Q1) and at stop; an identical dump is counted, not stored. Source: the owner's request; Q1. Proven by: steps 5, 11.
- [ ] A2. The timeline is one JSONL artifact; every event the ring held appears once or a gap says why not; each dump's XML is the reader's `dump full` document byte for byte, with device time, duration and wait; `json` plus ElementTree read it. Source: Q3. Proven by: steps 4, 5, 11.
- [ ] A3. A recording ends `stopped`, `maxDuration`, `maxDumps`, `maxBytes`, `deviceGone`, `failed` or `daemonExit`, in its summary and `end` line; after a crash the next start finalises it with whole lines. Source: Q2. Proven by: steps 5, 6, 11.
- [ ] A4. One recording per device, owned by the daemon: started on the web and read by an agent, started by an agent and stopped on the web; an MCP agent stops only its own chat's, a peer only its own (ADR-0101). Source: Q2, Q4, Q11. Proven by: steps 5, 9, 11.
- [ ] A5. Recordings live in `<state>/recordings/<id>/` (0600 under 0700), at most 50 and 2 GiB of ended ones; a peer's on a host at most 10 and 1 GiB; deleted from the web or the CLI; a deleted one's reference reads `ARTIFACT_EXPIRED`. Source: Q3, Q11. Proven by: steps 4, 6, 9.
- [ ] A6. While recording, an agent's `uiTree` and `tapElement` work, each delayed by at most one dump; after a `busy` the recorder pauses past step 1's slowest dump, so it never holds both workers. Source: Q5. Proven by: steps 1, 5, 11.
- [ ] A7. A `device.watchUiEvents` stream never ends because another call holds the read lock; it ends `failed` only when its own restart fails. Source: R3; Q5. Proven by: steps 2, 11.
- [ ] A8. Device-derived data is marked untrusted (the timeline by its class, after a restart too; `artifact cat`'s note; the route's header; relayed answers as a whole, ADR-0100 §4); summaries carry no device text and no chat id; a physical device the policy refuses gets `DEVICE_NOT_ALLOWED` before any `adb shell` or host call. Source: ADR-0015 §1, §4; ADR-0088 §4; Q6. Proven by: steps 3–5, 7.
- [ ] A9. The web starts and stops a recording for the toolbar's device, local or remote, shows one started elsewhere within 1 s, lists the kept ones and downloads an ended one byte-identical to `artifact cat`'s. Source: the owner's request; Q7, Q8. Proven by: steps 6–8, 10, 11.
- [ ] A10. `device.uiDump`, `device.uiEvents` and `device.watchUiEvents` on `<remote>/<serial>` answer as on a local device: the dump stored locally as untrusted and byte-identical to the host's, the events and cursor the host's, the stream's events republished with the remote id; the client runs no adb command. Source: R4; Q10. Proven by: steps 3, 11.
- [ ] A11. A recording of a lent device runs on the host, is stopped from the client, holds the lease while it runs and ends with it; once it ends the client holds a verified copy (same SHA-256) and the host none. Source: the owner's widening; Q11. Proven by: steps 9, 10, 11.
- [ ] A12. One minor version with exactly the schema diffs steps 3, 5, 6 and 9 declare; MCP gains exactly `device_recordUi`, `device_stopUiRecording`, `device_uiRecordings`. Source: `capability-api.md` §Discovery and versioning; ADR-0036. Proven by: steps 3, 5, 6, 9.
- [ ] A13. R1 and R2 carry numbers from Settings, Chrome and Play Store and are closed or narrowed. Source: R1, R2. Proven by: steps 1, 11.

## Run rules (the device phases: the main session only)

- **K1.** Isolated daemons by `verify-daemon` (state and config under fresh `/tmp/dyx.*`), built at the step's commit. For a lent device, two (`/tmp/dyxh.*` host, `/tmp/dyxc.*` client) paired as m2t's K1 did, the host listening on `127.0.0.1:<free port>`. Evidence under `B=~/Library/Caches/droidyard-dev/m2x`.
- **K2.** The lane's own AVDs under `$B/avd`: `m2x31` (API 31 google_apis arm64) and `m2x36` (API 36 google_apis_playstore arm64; Chrome and Play Store first-run screens tapped once); never older lanes' names. An adb port no other lane uses (not 5037, 5197, 5199, 5207; `pgrep -fl qemu-system` and the board first), behind m2p's guard at `$B/bin/adb`; only that server is ended. For lending, m2t's stand-in: the host's guard appends ` usb:2x-standin` to `m2x31`'s `devices -l` line so the host classifies it physical; the client's guard answers `devices -l` with the header and `version`, and logs and refuses everything else (its log proves the client runs nothing). Pages load over the emulator's network, else a local HTML file on `/sdcard`, said in the note.
- **K3.** One baseline of the owner's daemon and 5037 server at step 1, compared at every later run. Nothing killed by name; a daemon only by the pid in its own lock file. Headless Google Chrome with its own profile. The implementer runs no device phase.

## Steps

### 1. Spike: real apps' dumps and events, and what a dump costs others

- Builds: no code. The main session under K1–K3 with the existing `device.uiDump`, `uiEvents`, `uiTree`; results to `docs/research/spike-ui-recording-2026-10.md`.
  - (M1) `uiDump` ×20 on Settings' main list, Settings › Apps › See all, a long Chrome article, Play Store's first screen: nodes, bytes, `meta.durationMs` p50/p95/max, busy failures.
  - (M2) A Settings cold start and a Chrome page load, read with `uiEvents {since, waitMs: 20000, until: settle, quietMs: 1000}`: entries and raw counts per type, the most entries in any 100 ms, any `more` or `overflow`.
  - (M3) On static Settings and Chrome pages: a cursor, 10 `uiDump` 300 ms apart, `uiEvents {since}`: events the dumps cause, by type, and whether those dumps differ.
  - (M4) 20 `uiTree` alone, then 20 while another client calls `uiDump` back to back, on M1's heaviest screen and on formapp's `spinner` (each dump's idle wait runs its 500 ms): medians and max.
- Implements: the values Q1 and Q5 rest on.
- Check: the note's four tables. Gates, each a Change before step 4: (S1) the busy pause is max(5 s, M1's slowest + 1 s); a screen past 2.5 s records `missed: bound` and narrows R1; (S2) if M3's dumps cause events and keep differing over 5 s on a static page, events of those types arriving within a dump's window plus 100 ms are written but do not make a dump due; (S3) if a load puts over 200 entries in 100 ms, the tick drops to 50 ms; (S4) if M4's `uiTree` median grows by 200 ms or more, Q1's gap is 250 ms. Otherwise the defaults stand.
- Schema: no change.
- Needs: real emulators.
- Not here: no code.
- Rests on: S1–S4, level 5.
- [ ] Done. Evidence:

### 2. A patient events reach (R3)

- Builds: `uievents.go`: a patient mode of `eventsReach.poll` for streams and, in step 5, the recorder. Meeting `recoverBusy` it returns `errReaderBusy` at once, using no recovery and not failing; its caller skips the tick. Every lock holder is bounded by its own deadline, so a held lock is never a failure. With the lock it still `ping`s and restarts a dead reader once, failing only when that restart fails; its time bound covers one exchange and one recovery. `watchuievents.go` `step` uses it (`errReaderBusy`: an empty tick). `device.uiEvents` calls keep ADR-0111 §8 and 42 s. `stopOf` becomes `readerStopOf`, shared.
- Implements: ADR-P (Q5).
- Check: with `uiStreamEvery` and `eventsLockWait` shortened: `TestWatchUIEventsWaitsOutAHeldLock` (the lock held across 50 ticks while the reader is dead, then the holder restarts it: `readerRestarted`, streaming on; seen failing with the old rule, `stopped failed`); `TestWatchUIEventsFailsOnlyWhenItsRestartFails`; `TestUIEventsCallKeepsTwoRecoveries`. `-race`.
- Schema: no change.
- Needs: nothing earlier.
- Not here: no change to calls or the reader.
- Rests on: a patient poller ends with its subscription and restarts only a reader whose lock it holds, level 4.
- Review: the Opus verifier (concurrency, stream lifetime), effort `high`.
- [ ] Done. Evidence:

### 3. Lend and relay `device.uiDump`, `device.uiEvents` and `device.watchUiEvents` (R4)

- Builds:
  - `uidump.go`: `adb.plainThen(d, uiDumpFetched)`; `uiDumpFetched` (`remoteartifact.go`) fetches the host's dump with `fetchArtifact` as `application/xml` under `dumpOutputCap`, checks it with `parseDump` (fixed cause, no device text), stores it locally untrusted (producer `device.uiDump`) and recounts `nodes` and `foreground` from the fetched bytes.
  - `uievents.go`: `adb.plain(d)`; the cursor is the host's, passed through unchanged (opaque, ADR-0111 §5), its shape checked on the client before the call.
  - `watchuievents.go` and a new `remoteuievents.go`: a remote id's stream is relayed as `remotelogs.go` relays `watchLogs`: the client opens its subscription and one on the host, republishes `events` with `device` the remote id, maps the host's `stopped` and a lost link (`deviceGone`); the caller's unsubscribe or session end ends the host's subscription. On the host a peer link session keeps up to four streams per lent device, the fifth replacing its oldest (ADR-0106's rule); on the client one per client session and device (ADR-0112 §1).
  - The lease (ADR-0104 §3, §4): a peer's UI events stream calls `streamActivity` and `leaseLost` when it starts, as `watchlogs.go:713` and `watchscreen.go:484` do, and `endLease` (`lease.go:62`) stops `uiStreams` too (`stopped deviceGone`, `leaseEndedCause`), so a stream never outlives its holder's lease nor lets the lease go idle while it runs.
  - `adapters/peer/peer.go` `serveArtifact` and the client's `hosting.artifactTypes` (`internal/hosting/artifact.go:58`): both gain `application/xml` (served as itself; the peer protocol is not a browser route).
  - The three descriptors' errors gain `DEVICE_OCCUPIED` (ADR-0104 Consequences; `TestOccupiesFollowsTheDeviceOccupationRule`); the "not lendable" code comments go. API `0.33.0` with this first schema change (`system.go`, `git_read_test.go`), `go test ./internal/schema -update`, the client, `mcp.md`.
- Implements: ADR-L (Q10); ADR-0100 §3, §4, §6; ADR-0104 §2–§4.
- Check: `TestRemoteUIReads` (`cmd/droidyard`, two daemons, real TLS, `devicetest` on each): the client's `uiDump` artifact equals the host reader's document byte for byte and is untrusted, the host's reference never passed through (seen failing with it passed); `uiEvents` with `since` and a wait across a scripted toast returns the host's events; `watchUiEvents` events arrive with the remote id, the client's unsubscribe ends the host's poller, a cut link gives `deviceGone`; five client sessions: four host streams, the oldest replaced; the client's adb log has no device command; a second peer's call while the first holds: `DEVICE_OCCUPIED`. `TestLeaseEndStopsPeerUIEventsStream` (the lease ended by `session.release` and by unlending: the peer's poller stops, `stopped deviceGone`, no further `events` request; seen failing without the hook); `TestPeerUIEventsStreamKeepsTheLease` (only a stream open past a shortened idle: the lease holds). `TestUIDumpRemoteIDNotFound` becomes `TestUIDumpRemoteIDRelayed`; `TestPeerArtifactRouteServesXML`. **Level 5 (two daemons, the stand-in, `m2x31`):** from the client's CLI, `device.uiDump` on `qa/<serial>` then `artifact cat` equals the host's own `uiDump` of the same screen; `uiEvents` reports a toast tapped from the client; the client's UI events panel streams the remote device. The client guard's log holds only `devices -l` and `version`.
- Schema: version `0.33.0`; `DEVICE_OCCUPIED` added to the three capabilities' errors. Nothing else.
- Needs: step 2 (the host's stream is patient).
- Not here: no recording (steps 9, 10).
- Rests on: a remote dump is stored only after it parses, no host reference reaches a caller, and a peer's UI events stream ends with its lease, level 4.
- Review: the Opus verifier (peer surface, untrusted relay), effort `high`.
- [ ] Done. Evidence:

### 4. The recordings store and the timeline artifact

- Builds:
  - `internal/artifact`: `(*Store).CreateTimeline(id string, cap int64) (*Durable, error)` creates `<state>/recordings/<id>/timeline.jsonl` (directory 0700, file 0600) behind `droidyard://artifact/r.<id>`, `application/x-ndjson`, untrusted always (the class's mark, so it holds after a restart). `Get` resolves `r.<id>`: open handle `growing`; a record that is remote and not yet `copied` also `growing` (its bytes are a `.part` file not yet served, step 10); closed file untrusted; a well-formed id whose directory is gone `ARTIFACT_EXPIRED` ("deleted with its recording"); a malformed id or a path leaving `recordings/` `ARTIFACT_NOT_FOUND` with nothing opened (regular files only). `GetOwned` refuses it, so a peer's `artifact.read` never reaches a timeline (step 9 serves a peer its own only through the peer route). Ids match `^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$` before any path is built. The writer keeps a running SHA-256 of what it wrote.
  - `internal/domain/device/uirecordstore.go`: `recordings/<id>/recording.json` (`internal/store`, `schemaVersion` 1, written before the timeline): `{id, device, remote?, hostId?, invisible, maxSeconds, startedBy, state: recording|ended, startedAt, endedAt?, endReason?, cause?, dumps, unchanged, missed, lockWaits, events, gaps, bytes, sha256?, copied?}`. `startedBy` keeps client kind, session, chat and peer name on disk for the stop rule only; anything published carries `startedBy {client, session?}` (ADR-0048 §1), **never the chat id** (ADR-0088 §4: under ADR-0101 a chat id reaches another chat's operations). `List`, `Finalise`, `Delete` (refuses a running one), `Recover` (a local record still `recording` becomes `daemonExit`, its timeline truncated to the last newline and an `end` line appended with `hostTime` the file's mtime and no device `time`; a directory without a readable record deleted, ADR-0012's owner rule; a newer `schemaVersion` left untouched, ADR-0056), `Sweep` (50 recordings and 2 GiB of ended ones, oldest first, never a running one, which may add up to 512 MiB beyond).
  - The line structs (Q3) and a writer that refuses a line past the cap (the recorder then ends `maxBytes`; no line is cut).
- Implements: ADR-R (Q3, Q6), which amends ADR-0012 §Durability classes, ADR-0021 §Layout and ADR-0110 §Decision 2.
- Check: `TestTimelineRefIsUntrustedAfterReopen` (seen failing with the mark from the handle); `TestTimelineRefRefusesPathsOutsideRecordings`; `TestTimelineDeletedIsExpired`; `TestTimelineGrowingUntilClosed`; `TestTimelinePagesConcatenateByteExact`; `TestPeerCannotReadATimeline`; `TestTimelineFilesAre0600`; `TestTimelineSHA256IsTheFiles`; `TestRecoverFinalisesARunningRecord` (seen failing without the truncation); `TestRecoverDeletesAnOwnerlessDirectory`; `TestSweepKeepsTheRunningAndTheNewest`; `TestTimelineWriterRefusesALinePastTheCap`.
- Schema: no change.
- Needs: step 1's Changes applied.
- Not here: no capability.
- Rests on: no reference resolves outside `<state>/recordings/<id>/timeline.jsonl`, level 4.
- Review: the Opus verifier (paths from ids, untrusted marking), default effort.
- [ ] Done. Evidence:

### 5. The recorder, `device.recordUi` and `device.stopUiRecording` (the first end to end)

- Builds: `internal/domain/device/uirecord.go`.
  - `device.recordUi {device, invisible?, maxSeconds?}` (`maxSeconds` 10–3600, default 900): command, global, stateless, **not long-running**: it starts a daemon-owned recorder and returns `UIRecording` (the record's published fields plus `timeline: ArtifactRef`, `state: recording`) after the start dump, which waits for the lock within `dumpDeadline` (22 s), so the worst case (that, a reader start of 8 s, the sealing read) stays under ADR-0020's 60 s. Not idempotent, no `Resource`, `Target` the device, since `0.33.0`, not in `core`. Local ids only in this step (`adb.authorize`; step 10 relays). Errors: the preamble's, `DEVICE_UI_DUMP_FAILED` (the reader cannot run; nothing created), `DEVICE_UI_RECORDING_ACTIVE` (new: one runs on the device; details `{id}`), `REQUEST_INVALID_PARAMS`.
  - The recorder (own goroutine and context): a patient reach; each tick (100 ms, S3) reads events after its cursor, up to four pages, and writes them. A dump is due when an event came after the previous dump's request (less S2's exclusion) and the gap since that dump ended has passed: 100 ms (S4), doubled after each identical dump up to 2 s, reset by a stored dump; S1's pause after `busy`. It takes `readerClient.lock` waiting at most 200 ms (held: `lockWaits`, still due), reads with `persistentRead` and `readerDumpRequest(nil, &fullOpts{invisible})` (**persistent reader only**: no one-shot, never `uiautomator dump`), releases. Busy, over 8 MiB, unreadable: a `missed` line. A reader failure: no line; the next patient poll recovers it and writes the gap. A dump's device `time` is the last `events` answer's `nowWallMs` plus host monotonic time since; `ms` its duration (idle wait included); `waitedMs` from due to request.
  - Ends: `stopUiRecording` (`stopped`, after a `stop` dump); `maxSeconds`, 10,000 dumps, 512 MiB; the patient reach failing (`readerStopOf`: `deviceGone` or `failed`, a fixed cause); `StopRecordings` (`daemonExit`). Each reads the remaining events (1 s), writes `end` (device `time`, `hostTime`), closes the timeline (its `sha256` into the record), finalises.
  - `device.stopUiRecording {device}`: command, global, idempotent, not long-running, no adb call; ends the device's recording and waits for its summary (10 s) → `{stopped: true, recording}`; none running: `{stopped: false, recording?}` (the newest kept). Who may stop is ADR-0101's rule on the record's `startedBy` (`operation.go:426`, an exported helper over a stored owner): UI and CLI any; an MCP session its own session's or chat's; a peer its own name's; else `DEVICE_UI_RECORDING_ACTIVE` saying who may. Errors: that code, `REQUEST_INVALID_PARAMS`.
  - `ADB.StopRecordings(ctx)`: sets `stopping` (a later start is `DEVICE_UI_DUMP_FAILED` "the daemon is stopping"), ends every recorder `daemonExit` without a final dump, waits at most 2 s; first in `ADB.Close`, inside `AfterOps`'s 5 s. No operation is involved.
  - Renderings: `recording <id> <device> <state> [end=<reason>] dumps=<n> unchanged=<n> missed=<n> events=<n> gaps=<n> <bytes>B <seconds>s`, then `save "droidyard artifact cat <ref> > <id>.jsonl"`. Descriptions within ADR-0036 §1, one sentence saying the recording holds the device's UI reader, so a UiAutomation-based runner cannot run beside it.
  - Schema, client, skill map and one skill line (start, act, stop, save; a three-line Python reader; the runner sentence), `mvp.md`, `mcp.md`, `maxToolListBytes` by its rule, tool count 36.
- Implements: ADR-R (Q1, Q2, Q4, Q6); ADR-P; ADR-0034 §1; ADR-0110 §5; ADR-0015 §1, §4.
- Check: FakeReader, intervals shortened by their variables:
  - `TestRecordStoresADumpPerChangeAndSkipsIdentical` (seen failing without the hash); `TestRecordDumpsAfterTheLastEventOfABurst`; `TestRecordGapDoublesOnIdenticalDumps` (seen failing without the doubling); `TestRecordCountsLockWaits`.
  - `TestRecordTimelineHoldsEveryEventOnce` (a restart mid-way: one gap); `TestRecordDumpXMLIsTheReadersDocument`.
  - `TestRecordNeverUsesTheOneShotOrUiautomator` (seen failing with `readFull`); `TestRecordPausesAfterBusy`; `TestRecordHoldsTheLockOnlyForADump` (seen failing with the lock held across ticks).
  - `TestRecordEndReasons` (the seven; summary and `end` agree; `maxBytes` cuts no line); `TestRecordOnePerDevice`; `TestRecordOutlivesItsSession`; `TestStopFollowsTheOperationReachRule` (seen failing with the check removed); `TestRecordingNeverPublishesTheChatID` (summary, stop result, error details; seen failing with the record published as stored); `TestStopWithoutRecording`; `TestStopRecordingsRefusesNewStarts`.
  - `TestRecordPhysicalNotAllowed`; the renderings' goldens; MCP gains `device_recordUi` and `device_stopUiRecording` only.
  - **Level 5 (K1–K3, `m2x31`):** `recordUi` returns in under 3 s (CLI and MCP); a Settings cold start, a scroll, a `tapElement`; `stopUiRecording`; `artifact cat` `cmp` with the pages; `python3 -I` parses every line and dump, launcher first, Settings last, its `windowState` between; 20 `uiTree` during a recording of `spinner` against 20 without (A6).
- Schema: `device.recordUi`, `device.stopUiRecording`; the code `DEVICE_UI_RECORDING_ACTIVE`. Nothing else.
- Needs: steps 2, 4.
- Not here: no list, watch or delete (step 6); remote ids (steps 9, 10).
- Rests on: the recorder never holds both workers (S1's pause, level 5 from M1) nor the lock between dumps, and ends on every path, level 4.
- Review: the Opus verifier (contract, device concurrency, process lifetime, untrusted text), effort `high`.
- [ ] Done. Evidence:

### 6. Listing, watching, deleting; recovery and retention

- Builds: `uirecord.go`.
  - `device.uiRecordings {device?, id?}`: query, global, no adb: kept recordings newest first (at most 50), filtered; a running one with live counts and its growing ref; an unknown id an empty list.
  - `device.watchUiRecordings {}`: command, streaming (never MCP), as `session.watch`: `{subscriptionId, recordings}`; topic `device.uiRecording`, not subscribable, broadcast to every such subscription: `state {recording}` at start, at most every 2 s while running, at end (only once the record is finalised and the timeline closed); `deleted {id}`. No device text, no chat id.
  - `device.deleteUiRecording {id}`: command, destructive, idempotent, `AudienceHuman` (web and CLI), no adb; a running one `DEVICE_UI_RECORDING_ACTIVE`; unknown `{deleted: false}`.
  - `Recover` and `Sweep` once when the device domain is built in `serve.go`, before serving.
  - Renderings, schema, client, skill map, `mcp.md`; budget by its rule, tool count 37.
- Implements: ADR-R (Q3, Q4, Q7).
- Check: `TestUIRecordingsListsTheRunningWithLiveCounts`; `TestWatchUIRecordingsSeesAnotherSessionsStart` (an MCP start reaches a WS watcher; seen failing with `PublishTo`); `TestRecordingNeverPublishesTheChatID` extended to the list and the watch; `TestDeleteIsHumanOnly`; `TestDeleteRunningRefused`; `TestDeleteThenRefExpired`; `TestRecoveryRunsBeforeServing`; goldens; MCP gains `device_uiRecordings` only. **Level 5:** a recording running, the isolated daemon `kill -9` by its own pid and restarted: `daemonExit`, whole lines ending with `end`; a clean `droidyard stop` during another: `daemonExit` with its final events.
- Schema: `device.uiRecordings`, `device.watchUiRecordings` (`device.uiRecording`: `state`, `deleted`), `device.deleteUiRecording`. Nothing else.
- Needs: step 5.
- Not here: age-based retention (Q3); peers (step 9).
- Rests on: after any exit a recording's file is whole lines ending with `end`, level 5.
- Review: the main session's diff review; the verifier if the broadcast touches `internal/event`.
- [ ] Done. Evidence:

### 7. `GET /v1/artifacts/{id}` serves text artifacts

- Builds: `adapters/httpapi/httpapi.go` `artifactRoute`: a non-image runs `artifact.read` once with the smallest limit (authorization, existence, mark; one operation); `growing` gets today's 400 `REQUEST_INVALID_PARAMS` "the artifact is still being written; read it with artifact.read, or download it once it ends" (Q8); otherwise the bytes come from an injected `TextArtifacts` reader (the peer route's `ArtifactReader` pattern, wired in `serve.go`) through `http.ServeContent` (`Range`), `text/plain; charset=utf-8`, `no-store`, `nosniff`, `X-Droidyard-Untrusted: true` when marked. Images unchanged.
- Implements: ADR-0012 §Reading artifacts; rule 8; Q8.
- Check: `TestArtifactRouteServesATimelineAsText` (seen failing with the image-only rule); `TestArtifactRouteRefusesGrowingText`; `TestArtifactRouteTextRange`; `TestArtifactRouteXMLIsServedAsPlainText`; token, Host and Origin tests unchanged.
- Schema: no change.
- Needs: step 4.
- Not here: no new route, status or header.
- Rests on: no artifact is served under a type outside rule 8's three, level 4.
- Review: the Opus verifier (HTTP security baseline), default effort.
- [ ] Done. Evidence:

### 8. Record, list and download in the web UI

- Builds: (load `svelte-core-bestpractices` and `svelte-code-writer` first) `web/src/lib/devices/uirecording.ts`, a pure model over `device.watchUiRecordings` while the panel is visible; Record → `device.recordUi`, Stop → `device.stopUiRecording`; Download (ended and, for a remote one, `copied`) fetches `GET /v1/artifacts/<id>` with the token header into a `Blob` and `<a download="<id>.jsonl">`; Delete with a confirmation. `UiRecording.svelte` in the UI events panel's header (Record/Stop, elapsed, counts) and a collapsible list (time, duration, end reason, counts, Download, Delete). Only daemon fields are rendered.
- Implements: ADR-R (Q7).
- Check: Vitest: the toolbar device's running recording shows Stop, another device's Record (seen failing with the device ignored); no Download while running or uncopied; `deleted` removes the row; the Blob's bytes are the response's; hidden unsubscribes. `pnpm -C web run verify`; the autofixer gate. **Level 5 (`m2x36`, headless Chrome):** Record, a Chrome load, Stop; Download `cmp`-identical to `artifact cat`'s; a recording started over MCP shows Stop within 1 s, and Stop on the web ends it.
- Schema: no change.
- Needs: steps 6, 7.
- Not here: no viewer; the look (`review`).
- Rests on: —
- Review: the main session's Svelte review.
- [ ] Done. Evidence:

### 9. A peer's recording on the host

- Builds:
  - `recordUi`, `stopUiRecording`, `uiRecordings`, `watchUiRecordings` become `Lendable`; `deleteUiRecording` too (a peer deletes only its own: the client's cleanup after its copy). For a peer: `recordUi` takes the lease as any peer call (ADR-0104 §2); `stopUiRecording` and `uiRecordings` with a `device` run `leaseFor` (`target.go:117`) without any adb command, so they take and hold the lease as any device call of a peer does; the record's `startedBy` is the peer's name; `uiRecordings` and `watchUiRecordings` show it only its own recordings; stop and delete only its own (ADR-0101's peer branch); `DEVICE_UI_RECORDING_ACTIVE` answered to a peer about a recording not its own carries no id and no starter. The host's UI and CLI see, stop and delete every recording (ADR-0104 §2: the owner can always act); a host MCP agent cannot stop a peer's.
  - The lease: a peer's running recording counts as an open stream of the holder for ADR-0104 §3's idle rule; the lease's end (§4: link, unlending, the device leaving, `session.release`) ends the peer's recording `deviceGone` with the lease stream cause ("the host ended the lease", or "the peer's link ended"). A recorder that registers while the lease is ending checks `leaseLost` once it is in its table (`lease.go:96`), so the window `endLease` cannot see is closed.
  - Retention on the host: a peer's recordings at most 10 and 1 GiB per peer, its oldest ended first, inside the global bounds.
  - The peer artifact route serves a peer its own recordings' timelines: the `ArtifactReader` wiring (`serve.go`, `peerArtifacts`) resolves `r.<id>` when the record's `startedBy` is that peer and the recording has been finalised, and the route streams the file with `http.ServeContent` (`Range`), `application/x-ndjson`, untrusted; its own recording not yet finalised is **409** (the client retries); every other `r.<id>` (another's, the host's, a deleted or unknown one) is 404, as `GetOwned`'s invariant keeps for ephemeral ids, so nothing tells a peer which host recordings exist. Ephemeral artifacts stay whole in memory as today.
- Implements: ADR-L (Q11 host half); ADR-0104 §2–§4 as amended; ADR-0099 §2 as extended.
- Check: two daemons, `devicetest`: `TestPeerRecordingIsTheHosts` (a peer's start runs the recorder on the host, its record names the peer, the host's UI lists it); `TestPeerSeesOnlyItsOwnRecordings` (another peer's and the host's absent from list and watch, their stop `DEVICE_UI_RECORDING_ACTIVE` with no id, their timeline, a deleted one's and an unknown one's 404 on the peer route; seen failing with the filter removed); `TestPeerStopAndListTakeTheLease` (a second peer's stop while the first holds: `DEVICE_OCCUPIED`; no adb command in the log); `TestPeerRecordingHoldsTheLease` (no call past a shortened idle: the lease stays while it runs, goes idle after it ends; seen failing without the activity); `TestLeaseEndEndsThePeersRecording` (the link cut, unlending, `session.release`: each ends it `deviceGone` with its cause); `TestRecorderStartingAsTheLeaseEnds` (a start hook holds the start while the lease ends: the recorder ends at once, as `SetStartHook`'s tests do); `TestPeerRecordingRetention`; `TestPeerTimelineRange` (two ranges concatenate to the file; its own running one 409).
- Schema: `DEVICE_OCCUPIED` added to `device.recordUi`, `device.stopUiRecording` and `device.uiRecordings` (lendable, with a `device`: `TestOccupiesFollowsTheDeviceOccupationRule`). Nothing else.
- Needs: steps 3, 6.
- Not here: the client's half (step 10).
- Rests on: a peer reaches no recording it did not start, level 4.
- Review: the Opus verifier (peer surface, lease, untrusted files), effort `high`.
- [ ] Done. Evidence:

### 10. Recording a remote device from the client

- Builds: `remoteuirecord.go`.
  - `recordUi` on `<remote>/<serial>`: the client's own physical policy first (ADR-0100 §2), then relayed (`plainThen`); on success the client creates its record of it (its own id, `remote`, `hostId`, `device` the remote id, `startedBy` the client's caller) and returns the client's own `r.<id>`, never the host's reference; then a proxy: one relayed `watchUiRecordings` per remote while any of its recordings runs (`remotelogs.go`'s pattern), mirroring the host's counts and end into the client's record and broadcast.
  - `stopUiRecording` on a remote id: the client's reach rule, then relayed; `uiRecordings`, `watchUiRecordings` and `deleteUiRecording` on the client read and write only client records (a remote one deleted before its copy also deletes the host's).
  - The copy: when the host's recording ends (stop, bound, lease end seen at the next link), the client fetches its timeline by ranges of 8 MiB into `recordings/<id>/timeline.jsonl.part`, resuming from its size after a lost link (a 409 is retried), bounded by the host summary's `bytes`; only when the host's `sha256` matches is it renamed to `timeline.jsonl`, `copied` set, and the host's copy deleted (the peer's own). Until then `Get` of the client's `r.<id>` reads `growing` (step 4), so `artifact.read` returns no bytes as complete, `artifact cat` says it is still being written, and `GET /v1/artifacts` answers step 7's 400. A mismatch discards the `.part` and keeps the host's, `failed` with a fixed cause. A link lost while recording: the client's record ends `deviceGone` ("the link to <remote> was lost"), copy pending. At each link up the client retries pending copies, one per remote at a time, taking the host's final reason, and lists its own recordings on the host to adopt any ended one it holds no record of (a relayed start whose answer was lost, `DEVICE_DISCONNECTED`). `Recover` on the client leaves remote records and their `.part` as they are; a host that no longer holds the recording: `failed`, "the host no longer holds it".
  - `hosting.Link` gains a ranged artifact fetch to a writer (`ArtifactRange`), the `r.<id>` shape checked before it becomes a URL path (`artifactIDRE` accepts ephemeral ids only).
  - Stop's rendering on a remote id and the skill line say to wait for `copied` (`uiRecordings {id}`) before `artifact cat`. The web needs nothing new: remote records come through the client's watch; Download waits for `copied`.
- Implements: ADR-L (Q11 client half); ADR-0100 §2–§4, §6.
- Check: two daemons, `devicetest`: `TestRemoteRecordingEndToEnd` (start on the remote id from the client's MCP origin, scripted events and dumps on the host, stop from the client's UI origin: the client's timeline equals the host's bytes before deletion, `copied`, the host's directory gone; the client's adb log empty); `TestRemoteRecordingResumesTheCopy` (the link cut mid-copy, restored: the copy completes from its offset, same SHA-256; seen failing with a restart from zero); `TestRemoteRefIsGrowingUntilCopied` (during the copy and after a client restart mid-copy: `artifact.read` `growing`, the route 400; seen failing with an in-place copy and a restart); `TestRemoteRecordingAdoptedAtLinkUp` (the start's answer lost: the host's ended recording is copied at the next link up); `TestRemoteRecordingDigestMismatchKeepsTheHosts`; `TestRemoteRecordingLinkLostWhileRecording` (client `deviceGone`, the host's lease end, the copy at link up with the host's reason); `TestRemoteRecordingAfterClientRestart`. **Level 5 (two daemons, the stand-in `m2x31`, headless Chrome on the client):** Record on `qa/<serial>` in the client's panel, a Settings cold start driven from the client, Stop; the client's Download equals the host's timeline digest; the host's `recordings/` no longer holds it; the client guard's log holds only `devices -l` and `version`.
- Schema: no change.
- Needs: steps 8, 9.
- Not here: reading a remote recording while it runs (counts only; the copy is at its end).
- Rests on: no consumer reads a remote timeline as complete before its digest matched, and the host deletes its copy only after that, level 4.
- Review: the Opus verifier (relay, resumable copy, untrusted files), effort `high`.
- [ ] Done. Evidence:

### 11. Level 5, the register rows and the close

- Builds: no code. The main session under K1–K3 on the final binary: (a) a Chrome page load and Play Store's first screen recorded on `m2x36` through `droidyard mcp` (`device_recordUi`, `device_stopUiRecording`, then `artifact cat`): start latency, distinct dumps during the load, events between, the parse; ends `maxDuration` (10 s), `deviceGone` (`adb emu kill` on the run's own emulator), `daemonExit`; A6 on the heaviest screen; A7: the UI events panel open while a recording and an agent's `uiTree` run and the reader is killed by its own pid. (b) Two daemons: steps 3 and 10's runs again, and the host daemon stopped during a remote recording (the client's record `deviceGone`, the copy after restart). (c) Documents: `ui-automation.md` §The full dump and the events between reads (ADR-R, ADR-P, ADR-L); `adb-device.md` §Lifecycle and concurrency (the lent surface stays `Lendable`'s and ADR-L's, not a list); `http-websocket.md` §`GET /v1/artifacts/{id}`; the register (R1–R4; new rows: a viewer, a bundle of files, a policy change mid-recording, `artifact cat` of a large recording filling Activity, the two-Mac trial (`trial`), API 36.1 and physical devices; row "One `UiAutomation` per device …" gains the recording); `docs/README.md` §Where the work stands; the index row.
- Implements: A1–A13 at level 5.
- Check: each run's decisive output under `$B/evidence`, quoted in the evidence line.
- Schema: no change.
- Needs: steps 1–10.
- Not here: the owner's phone; a second Mac.
- Rests on: —
- Review: the Opus verifier at the close.
- [ ] Done. Evidence:

## Not in this milestone

- A viewer (Q7); a zip or directory of XML files (Q3): register rows.
- Every-window dumps, notification text, fling folds, `collection-item-*`: their rows stay.
- Ending a recording when the policy stops allowing its device mid-way: a register row.
- `droidyard artifact cat` pages at 256 KiB, one `artifact.read` in Activity each (about 800 rows for 200 MiB): a register row.
- Reading a remote recording while it runs; a remote recording surviving the lease's end.
- A screen video; tree diffs; a recording resumed after a restart; age-based retention.

## Design questions

None blocks the plan: Q1–Q8, Q10 and Q11 are Delegated, and the main session writes ADR-R, ADR-P and ADR-L from them after step 1 (as m2w wrote ADR-0110–0112); where a step and an ADR then differ, the ADR wins. Q9 is answered by the owner.

- **Q1. Capture trigger and rate (ADR-R).** Recommended: one rule on the ring: a dump at start, then whenever an event came after the previous dump's request and the gap since that dump ended has passed, and one at stop; so a burst is dumped as fast as the gap allows and its settled state is always stored. Gap 100 ms (S4), doubled after each identical dump up to 2 s; S2's exclusion; S1's pause after `busy`. Identical by SHA-256: counted in `unchanged`. Each `dump full` first waits up to 500 ms for quiet (unchanged), so during an animation a stored state leans toward a pause and a dump holds the lock up to about 0.5 s plus its traversal. Lost states are shown: `time`, `ms`, `waitedMs`, events between dumps, `missed`, `lockWaits`. Rejected: a fixed-interval sampler; a dump per event; a dump only after settle; dropping the idle wait (a reader change and its gates).
- **Q2. Bounds, ends, lifetime (ADR-R).** Recommended: `maxSeconds` 10–3600 (900), 10,000 dumps, 512 MiB; one recording per device. Ends: `stopped`, `maxDuration`, `maxDumps`, `maxBytes`, `deviceGone`, `failed`, `daemonExit`; a reader restart is a gap; the reader cannot exit idle while polled (ADR-0111 §4). A daemon-owned job, as a detached share (ADR-0108), bounded by `maxSeconds` with no session state (ADR-0019 §Sessions, ADR-0010). Rejected: ending with the starting session; several recordings of one device.
- **Q3. Where and in what form (ADR-R; amends ADR-0021 §Layout, ADR-0012 §Durability classes, ADR-0110 §2 in part).** Recommended: `<state>/recordings/<id>/` with `recording.json` and `timeline.jsonl`, a durable artifact `r.<id>`, `application/x-ndjson`, untrusted by class. The first durable store of device text on the host (ADR-0110 §2 "the daemon writes no host file"), kept because the owner wants spans for his parsers: 0600 under the 0700 root, deleted with its record, no age limit. Lines: `{kind: "recording", id, device, invisible, hostStartedAt, deviceStartedAt, format: 1}`; `{kind: "event", …}` (`device.uiEvents`'s fields); `{kind: "gap", reason, dropped?}`; `{kind: "dump", n, time, ms, waitedMs, trigger: start|change|stop, nodes, foreground, bytes, sha256, unchanged, xml}`; `{kind: "missed", time, reason}`; `{kind: "end", time?, hostTime, reason, cause?}`. Write order; `time` device clock (a dump is the screen at an instant within `[time, time+ms]`); `hostTime` the host's. One file is the bundle. Retention 50 and 2 GiB of ended ones. Rejected: a zip (a byte mode in `artifact.read`, a fourth type under rule 8, a browser zip writer or a new route); an artifact per dump; the workspace's `artifacts/` (devices are global); ephemeral artifacts.
- **Q4. The contract (ADR-R).** Recommended: `device.recordUi` (a short command starting the job), `stopUiRecording`, `uiRecordings`, `watchUiRecordings` (web), `deleteUiRecording` (human only); one code, `DEVICE_UI_RECORDING_ACTIVE`; API 0.33.0; three MCP tools (budget by its rule). The stop cancels no operation, so ADR-0020's "no domain-specific cancel" is untouched; who may stop is ADR-0101's reach rule on the starting origin, its chat id kept on disk and never published (ADR-0088 §4), only `startedBy {client, session?}` (ADR-0048 §1). Rejected: a long-running `recordUi` (an MCP start blocks 20 s, an interrupt cancels the recording, a cross-session stop is a domain cancel against ADR-0101); an `action` parameter (none exists; `capability-api.md` §Discriminated tools over overloaded ones); polling from the web.
- **Q5. The lock and the stream (ADR-P; amends ADR-0112 §3 in part).** Recommended: the recorder holds `readerClient.lock` only per dump (200 ms wait), takes no `Resource`, uses the persistent reader only, pauses after `busy` past the slowest abandoned dump. A patient reach (streams, the recorder) treats a held lock as an empty tick, failing only when its own restart fails; calls keep ADR-0111 §8. Rejected: holding `dumpKey` for the span; dumping on the second worker without the lock; a busy bound (any value below an act's 60 s host bound fails a stream behind a legitimate holder).
- **Q6. Untrusted and policy (ADR-R).** Recommended: `recordUi` authorizes as every device call (ADR-0015 §4; a remote id by the client's own policy first, ADR-0100 §2); stop, list, watch and delete touch only daemon records. The timeline is untrusted by class; summaries and watch events carry no device text; the web renders none. Dumps are the active window only (ADR-0110 §4) and events carry no notification text (ADR-0111 §3).
- **Q7. The web (ADR-R).** Recommended: Record/Stop and the kept list in the UI events panel over `watchUiRecordings`; no viewer.
- **Q8. Text over `GET /v1/artifacts/{id}` (no new ADR).** Recommended: serve text as ADR-0012 and rule 8 already say, whole or by `Range`; refuse a growing one with today's 400 rather than add the growing header (a new ADR-0024 identifier). Rejected: browser paging (an Activity row per 256 KiB).
- **Q9. A recording and the owner's tests — answered by the owner, 2026-10-21:** "No voy a usar ni UiAutomator, ni Appium ni Maestro con droidyard. Droidyard está pensado para sustituirlos." A recording holding the device's one `UiAutomation` for its span is accepted; the descriptions and the skill keep one sentence saying a UiAutomation-based runner cannot run beside it.
- **Q10. The UI reads lent (ADR-L; supersedes in part ADR-0099 §2, ADR-0110 §6, ADR-0111 §9, ADR-0112 §1).** m2w's Q9 left them unlendable, a Delegated call made without the owner's use case; the owner's standing rule makes lending the default. Recommended: `uiDump` relayed with its artifact fetched (`application/xml` added to the peer route's types), `uiEvents` relayed plain with the host's cursor passed through, `watchUiEvents` relayed as `watchLogs` with ADR-0106's four streams per link and device on the host; `DEVICE_OCCUPIED` in their errors, and a peer's stream ends with its lease and counts as activity (ADR-0104 §3, §4).
- **Q11. Recording a lent device (ADR-L; supersedes ADR-0104 §3 in part).** Recommended: the recorder runs on the host that owns the device; start, stop, list and watch are relayed; the client copies the timeline once the recording ends and the host then deletes its copy. A peer's recording takes and holds the lease (it counts as an open stream for §3's idle rule) and ends with it (§4: link, unlending, device leaving, `session.release`), `deviceGone` with the lease's cause; the timeline stays on the host until the client copies it. A peer sees, stops and deletes only its own (ADR-0101's peer branch); the host's UI and CLI see and stop all; a host MCP agent cannot stop a peer's. On the host a peer keeps at most 10 recordings and 1 GiB. For a peer, stop and a device-filtered list run `leaseFor` without adb, so they hold the lease as any device call. The peer route serves a peer its own finalised timelines by `Range` (409 while not finalised, 404 for anything else, deleted ones included: no existence oracle); a peer's `artifact.read` never reaches a timeline. The client copies to a `.part` file renamed only after the SHA-256 matches, its own reference reading `growing` until then, and adopts at link up any ended recording of its own the host holds that it has no record of. Rejected: a recorder on the client over relayed `uiEvents` polls and `uiDump` calls (ten host operations a second in the host's audit, a dump's artifact fetched per change over the VPN); a recording that outlives the lease (another peer could take the device while it records the first's session; ADR-0104's one person at a time); the client reading the host's growing timeline live (a remote artifact class; counts suffice while it runs).

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| A full dump of Settings, Chrome and Play Store finishes within 2.5 s | 5, 11 | step 1 M1 (S1); a miss is a `missed` line |
| Dumps cause no events that keep their documents changing | 5 | step 1 M3 (S2) |
| A load fits one 200-entry page per tick | 5 | step 1 M2 (S3) |
| One dump at a time, idle wait included, delays an agent's `uiTree` by under 200 ms on an animating screen | 5, 11 (A6) | step 1 M4 (S4); step 5's level 5 |
| A dump's device time is right within its `ms` | 5 | estimated from the last `events` answer plus host time; no device stamp (a reader change) |
| Two Macs over a VPN behave as two daemons on one Mac (latency, a 512 MiB copy, link drops) | 3, 9, 10 | the owner's trial (`trial` row) |
| API 36.1, physical devices, other OEMs, Linux | all | not run |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | [contract] B1 (blocking): a cross-session stop of `recordUi`'s operation is a domain cancel against ADR-0020 and ADR-0101 | The recording is a daemon-owned job (ADR-0108); the stop follows ADR-0101's reach rule (Q4, step 5) |
| 1 | revise | [contract] B2 (blocking): durable device text contradicts ADR-0110 §2, unnamed | ADR-R amends ADR-0110 §2 in part; at-rest posture in Q3; `TestTimelineFilesAre0600` |
| 1 | revise | [contract] B3 (blocking): a long-running MCP tool blocks 20 s, an interrupt cancels the recording | `recordUi` is a short command (Q4); timed at level 5 |
| 1 | revise | [bug] B4 (blocking): a 15 s busy bound is shorter than a legitimate hold | No busy bound: a held lock is an empty tick for a patient reach (step 2, Q5) |
| 1 | revise | [plan] B5 (blocking): a recording holds the one `UiAutomation` | Q9; answered by the owner since (none of those runners is used) |
| 1 | revise | [plan] N1–N7: idle wait per dump; gates that changed nothing; two-worker claim; delete over MCP; `artifact cat` fills Activity; shutdown race; lock yields unseen | Q1 and A6 say it, M4 on `spinner`; S1–S4 each set a value; step 5 Rests on; `AudienceHuman`; a register row; `StopRecordings` refuses new starts, bounded; `lockWaits`, `waitedMs` |
| 1 | revise | [nit] cancel semantics; recovered end time; sweep overshoot; real-time tests; size | Moot; `hostTime`; said in step 4; variables; size |
| 2 | revise | B1–B5 resolved; new [contract] N-B1 (blocking): `startedBy` would publish the chat id (ADR-0088 §4) | Chat kept on disk only; published `startedBy {client, session?}`; `TestRecordingNeverPublishesTheChatID` |
| 2 | revise | [nit] start dump's lock wait; ADR-0019 §Sessions and ADR-0010 uncited | Within `dumpDeadline`; cited |
| 3 (changed parts) | revise | [safety] B1 (blocking): a lent `watchUiEvents` ignores the lease (`endLease` stops only logs and screens) | Step 3: `streamActivity`/`leaseLost` at start, `uiStreams` in `endLease`; `TestLeaseEndStopsPeerUIEventsStream`, `TestPeerUIEventsStreamKeepsTheLease`; Rests on |
| 3 | revise | [test][contract] B2 (blocking): `DEVICE_OCCUPIED` on non-lendable step-5 capabilities fails the schema test, missing on lendable `uiRecordings`; the lease of stop and list undecided | Moved to step 9's diff for `recordUi`, `stopUiRecording`, `uiRecordings`; for a peer both run `leaseFor` without adb (step 9, Q11); `TestPeerStopAndListTakeTheLease` |
| 3 | revise | [contract][bug] B3 (blocking): a remote timeline before its copy undefined; a partial copy served as whole | `.part` renamed after the SHA-256; the client's own `r.<id>` reads `growing` until `copied`; stop's rendering and the skill say wait; `TestRemoteRefIsGrowingUntilCopied` |
| 3 | revise | [bug] N1 client type filter, `r.` id shape; N2 a status for not finalised, end broadcast order; [safety] N3 existence oracle, ids in errors to peers, step 4/9 disagreement; [test] N4 start during lease end; [plan] N5 lost start answer | `hosting.artifactTypes` and the id check (steps 3, 10); 409 retried, broadcast after finalising (step 6); 404 for all but its own, no id to a peer, peer `artifact.read` reaches no timeline; `leaseLost` after registering and a test; adoption at link up and a test |
| 3 | revise | [nit] no description says "not lendable"; no surface list in `adb-device.md`; "extends" a closed list; Range unnamed in Q11; a query with a side effect | Diff is the version and `DEVICE_OCCUPIED`; list not added; supersedes ADR-0099 §2 in part; named; retry at link up only |

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.

- 2026-10-21, before approval (the owner, relayed by the main session): Q9 answered (no UiAutomation-based runner); lent devices in scope (Q10, Q11; steps 3, 9, 10; R4); the standing rule that new device capabilities are lendable by default; ADR numbers 0113–0115 reserved. A third review round checked the changed parts; its three blocking findings are answered in the plan.
