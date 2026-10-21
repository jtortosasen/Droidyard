# Plan: Milestone 2za — The live view's quality from the client: a toggle, and a view that comes back by itself

- Status: Approved
- Approved: 2026-10-22, Delegated (the main session, under the owner's standing instruction to apply the recommended option): Q1 and Q2 as recommended; the owner approved the scope on 2026-10-21 and asked to proceed on 2026-10-22.
- Date: 2026-10-22
- Milestone: "2za: the client switches a lent (remote) device's live view between fast and full quality, by the CLI (`droidyard remote fast <name> on|off`) or by the web UI, and the open view comes back by itself at the new quality" (the owner's scope, 2026-10-21; `docs/README.md` §Where the work stands says "next, the owner's choice").
- Phase: none. The MVP is done (2h); this plan's Scope table is the boundary.
- Contract: ADR-0118 §3 (a client speaks a newer frame only to a host whose hello names the version), §4 (the fast mark in the hello; a source keeps its options for its life), ADR-0121 §3, ADR-0078 §2 (the stop reasons), §6 (lifetimes), ADR-0100 §3, ADR-0104 §3, §4 (the lease belongs to the link session and ends with it), ADR-0115 Decision 4 (a peer's recording ends with its lease), ADR-0109 §3 (live reconcile every 2 s), ADR-0015 §6 and ADR-0060 §2 (`policy.update` is the UI's, never MCP's), ADR-0041 §4; `docs/interfaces/peer.md` §The peer port, `mcp.md` (no live view: the streaming row). **New, Delegated, written by the main session before step 1:** ADR-N (Q1, Q2; number 0123, reserved for 2za), superseding ADR-0078 §2 in part (a fifth reason, `renewed`) and ADR-0118 §4 in part (a changed mark travels in the open session to a host of the new API, and the client restarts its views of that host).
- Register: the main session's commit that adds this plan sets `Work` = `2za` on the web UI row "The live view does not wake a sleeping device and does not survive a reboot (Restart is offered); its quality is fixed but for a remote marked fast" (Delegated). The row "The device list does not refresh when a remote's link comes back…" (m2u step 7) is not joined: a host restart, which this plan does not touch.
- Lanes: none in parallel. 2z closed before this plan's review (merge `034f8a4`, API 0.35.0); `remote fast` survives it unchanged (`cmd/droidyard/hosting.go:309`); this plan touches no CLI file.

**The owner should see:** a Fast toggle on a lent device's live view; clicking it (or `droidyard remote fast qa on` in a terminal) blanks the view for a moment and it comes back by itself, lighter or full, on the same device, with no "The device went away." and no Restart, while Logcat and a UI recording of that device go on.

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| A Fast / full toggle on a remote device's live view, writing `remotes[].fast` through `policy.update` with the revision | yes | step 3 (owner's item 1) |
| The open view reopens by itself after a fast change, from the UI or the CLI | yes | steps 1, 2, 3 (owner's item 2; Q1) |
| The re-watch bounded | yes | step 3 (one re-watch per failure streak, as `detached`) |
| Register: the quality part of "The live view does not wake a sleeping device…" | yes | narrowed at step 4 |
| A source other viewers hold restarted so they switch too (register "A source keeps the options it started with…") | no | ADR-0118 §4; the row stays open (owner, 2026-10-21) |
| A host older than this milestone's API reopening by itself | no | Q2: it keeps today's reconnect; a register row |
| Local devices' quality; MCP; per-view quality | no | ADR-0118 §4 (local is full); `mcp.md` (no live view); ADR-0118 Alternatives |
| The CLI command | no | exists (m2y step 7) |

## Starting point

- `m2za` at `034f8a4` (main after 2z), API `0.35.0` (`internal/domain/system/system.go`), ADRs up to 0122; 0123 reserved. Since 2z a CLI session may call `device.watchScreen` (hidden from its listings, `adapters/cli/tree.go:93-99`) and writes each event frame unchanged (`adapters/cli/cli.go:398-417`), so a new stop reason needs no CLI change.
- **The mark.** `policy.Remote.Fast` (`internal/policy/hosting.go`), in `policy.get`/`policy.update`'s `remotes` (m2y step 7); `remote fast` rewrites the list through them with the revision (`cmd/droidyard/hosting.go:309-332`). `Host.Reconcile` (every 2 s, `serve.go`) calls `Link.update`, which on a changed mark closes the link's connection (`internal/hosting/link.go:94-104`, m2y step 7's "a change reconnects"); the next hello carries the mark (`link.go:473-479`) and the host records it per peer (`adapters/peer/peer.go:750-751`, `host.PeerFast`). **A latent defect, by reading:** `update` closes only `l.conn`, nil between the hello and `session` marking the link up (`link.go:431`), so a mark changed in that window leaves the link up with the old mark until its next reconnect.
- **What today's reconnect costs.** The client's link session ends, so: its relayed views end `deviceGone` (`remotescreen.go:455-463`) and the UI re-reads the device list, which omits the remote while the link is down and drops the selection (`liveview.ts:295`, `devices.ts:153-154`); its remote Logcat and UI events streams end `deviceGone` (`remotelogs.go:231-237`, `remoteuievents.go:296-302`); on the host, the lease ends with the session (ADR-0104 §4) and with it a recording the peer started (`lease.go:90-100`, `uirecordpeer.go:180-186`), and the client ends every mirrored running record `deviceGone` (`remoteuirecord.go:826-828`).
- **The host's session** (`adapters/httpapi/ws.go:310-326`) reads frames in order and serves each envelope on its own goroutine. **The host's view** releases its source reference on a goroutine after the stream ended (`watchscreen.go:245-249`); `screenSource.unref` claims the end at zero refs (`screensource.go:303-321`), and an acquire meeting an ending source waits for its `done`. So nothing orders a stream's end before a later acquire: a re-watch can join the old source and keep its options (ADR-0118 §4). `events.unsubscribe` runs the stream's `end` before it answers (`event.Session.unsubscribe` → `Subscription.Close` runs `onClose` synchronously, `internal/event/session.go:260-268`, `event.go:390-405`; the hook is `watchscreen.go:647`); a concurrent `Close` returns at once (`closeOnce`), which is why step 1 makes a second end wait for the first's drop.
- **The client's view.** One relayed source per remote device; `end` marks it ending at once, so a re-watch's `acquire` waits for its teardown (`remotescreen.go:119-126, 251-260`), which closes the video socket and waits for the host's answer to `events.unsubscribe` (`remotescreen.go:468-488`).
- **Web.** `LiveViewModel` (`web/src/lib/screen/liveview.ts`) re-watches once per failure streak, for `detached` only (`liveview.ts:291-307`); `LendingModel.stopLending` (`web/src/lib/devices/lending.ts`) is the pattern for rewriting a policy list with `ifRevision` and one retry on `POLICY_REVISION_CONFLICT`.
- **Tests to build on.** `adapters/peer/fast_test.go` (`newSide`: two hosting sides over real TLS), `cmd/droidyard/remote_screen_test.go` and `remote_input_test.go` (`screenLab`: two daemons, the host's fake adb behind the fixture listener), `internal/domain/device/screenfast_test.go` (`callsWith(t, "app_process")` reads a start's options), `screenAfterLastUnref` (a nil-in-production hook).
- **Baseline, 2026-10-22 at `034f8a4`:** `go vet ./...` clean; `go test ./...` ok in every package but `cmd/droidyard` (`TestRemoteRecordingEndToEnd` timed out waiting for the host's dump 3 in the full run; it passes alone, `-count=3`, 7 s) and `internal/doccheck` (`TestPlansIndexMatchesFiles`: this plan has no index row until the main session adds it in the same commit). The plan calls the new ADR "ADR-N" because `TestADRMentionsExist` refuses a number not yet written.

## Acceptance

- [ ] A1. **The toggle writes the mark**: on the live view of `qa/<serial>`, Fast on and off change only `remotes[qa].fast` in the client's configuration; every other field of that entry and every other entry is sent back as `policy.get` returned it, with `ifRevision`; a concurrent edit is read again and retried once; off removes the key, as the CLI writes it. Source: the owner's item 1. Proven by: steps 3, 4.
- [ ] A2. **The view comes back by itself, at the new quality**: a fast change from the UI or the CLI, with the view live, stops it `renewed` (never `deviceGone`) and the UI watches again without a click; when no other viewer holds the host's source (ADR-0118 §4), the host starts a new server with the new preset (long side 1024 fast, 1280 full, on an AVD whose long side exceeds 1280), never the old source joined; live within 5 s of the change (working target: the reconcile's 2 s plus a start). Source: the owner's item 2; Q1. Proven by: steps 1, 2, 3, 4.
- [ ] A3. **Nothing else of the remote ends**: the link session is kept (one hello), so the lease, a Logcat or UI events stream of the remote and a UI recording of it go on, and the toolbar's device is unchanged. Source: Q1; ADR-0104 §4, ADR-0115 Decision 4. Proven by: steps 1, 2, 4.
- [ ] A4. **Bounded**: a re-watch after `renewed` is the failure streak's one re-watch; a failed one shows the error with Restart. Source: Q1. Proven by: step 3.
- [ ] A5. **An older host and other losses unchanged**: a host whose hello names an API below the new one gets today's reconnect; a lost link, a revoked pairing or a device no longer lent still end the view `deviceGone`. Source: Q2; ADR-0118 §3. Proven by: steps 1, 2.
- [ ] A6. **Contract**: one minor version; the schema diff is exactly `renewed` in `device.watchScreen`'s `stopped.reason` enum and its description; the MCP tool list is unchanged; the peer protocol gains one client-to-host frame, sent only to a host of the new API. Source: `capability-api.md` §Discovery and versioning; Q1. Proven by: steps 1, 2.

## Steps

### 1. The mark travels in the open session, and the host orders a view's end before the next start

- Builds:
  - **The frame** (`internal/capability`, beside `PeerHelloFrame`): `{"mark": {"fast": <bool>}}`, client to host on `/peer/v1/ws`.
  - **The client** (`internal/hosting/link.go`): on a changed mark with the link up and the host's hello naming the new API (`hostAPI` at or past it, as ADR-0118 §3 gates the acknowledgements), `update` only records the mark and wakes the link's *mark sender* (a goroutine, as `go l.conn.CloseNow()` is today), never writing under `l.mu` or `Host.mu`. The sender reads the connection and the **current** `l.fast` under `l.mu`, writes the frame outside both locks through `l.write` (the link's one writer, its own bound), and after the write returns, under `l.mu`, closes the *mark channel* that was current when the write began and only then installs a fresh one (`Marked() <-chan struct{}`, taken like `Down()`; while the link is down it returns a channel that is open, not `closedChan`, since a loss is `Down()`'s to report). So two quick changes leave the host with the last value, and a source that took `Marked()` while a write was pending ends after it and watches again behind the frame. A write that fails is the connection's loss, as any. An older host, or a link down, keeps today's path (close, and the next hello carries the mark). **The hello race**: when `session` marks the link up, a mark that changed since its hello is sent at once by the same rule (frame, or close for an older host).
  - **The host** (`adapters/httpapi/ws.go`'s read loop, for a peer session only): a mark frame is applied in the loop, before the next frame is read, through the hook `adapters/peer` passes (`SetFast(name, fast)`), after the same `allowed()` check `serve` makes (`ws.go:330`); a malformed one closes 1003 as other protocol errors do; it gets no answer. A UI session treats it as today (an envelope that does not parse).
  - **The host's view end** (`internal/domain/device/watchscreen.go`, `screensource.go`): a stream's end drops its source reference synchronously (the last one marks the source ending, under the set's lock, before `end` returns); only the wait for the teardown stays on the goroutine. A second, concurrent end returns only once the first has dropped it. So an answer sent after an end (to `events.unsubscribe`) is ordered before any later acquire, which waits for the old source and starts a new one. The relayed source gets the same rule. The hook that holds the release goroutine in tests is an exported test seam (nil in production), reachable from `cmd/droidyard`'s `screenLab` the way `hostWrapStream` is passed through `labOptions`, and released at cleanup.
  - API version `0.36.0` (`system.go`); the frame's gate names it.
- Implements: ADR-N (Q1's transport and ordering, Q2); ADR-0118 §3's gating rule.
- Check:
  - `adapters/peer` with `newSide`: `TestAMarkChangeTravelsInTheSession` (the host's `setFast` sees the new mark and the host counts one hello; seen failing with today's close: two hellos); `TestAnOlderHostIsReconnectedAsToday` (the host's hello names 0.35.0: two hellos, the second with the mark); `TestAMarkFrameIsAppliedBeforeTheNextFrame` (the test's `SetFast` sleeps 200 ms; a mark frame then, on the same connection, a call whose handler reads `PeerFast`: it reads the new mark; seen failing with the frame served on its own goroutine); `TestTwoQuickChangesLeaveTheLast` (on then off within one write: the host ends with off); a malformed mark closes 1003; a UI session's mark-shaped frame is not applied.
  - `internal/hosting`: `TestAMarkChangedDuringTheHelloIsSent` (a nil-in-production hook between the hello and up flips the mark: the host ends with the new mark; seen failing as today, the old mark kept); `TestMarkedClosesOnlyAfterTheFrameIsWritten` (a blocked write: the channel stays open until it returns); `TestAPendingMarkWriteBlocksNothing` (a write held open: `Reconcile`, a `Call` on another request and `health` return; seen failing with the write under `l.mu`).
  - `internal/domain/device`: `TestAViewsEndDropsItsSourceAtOnce` (a hook, nil in production, holds the release goroutine; after `end` returns, an acquire does not join the old source and a second server start is logged; seen failing with today's release on the goroutine: one start); `TestConcurrentEndsReturnAfterTheDrop`.
  - `go test -race` on `internal/hosting`, `adapters/peer`, `adapters/httpapi`, `internal/domain/device`.
- Schema: the version only.
- Needs: ADR-N written; fakes only.
- Not here: nothing ends a view yet on a mark (step 2); no web change.
- Rests on: the host applies the mark before it reads the client's next frame, and a stream's end drops its source before any answer that follows it: level 4, each test seen failing reversed.
- Review: none of its own; the Opus verifier reviews steps 1 and 2 together after step 2 (the owner's call for pace), effort `high`.
- [ ] Done. Evidence:

### 2. The client restarts its views of that host, `renewed`

- Builds:
  - `internal/domain/device`: `StopRenewed = "renewed"` beside `StopDetached`; `ScreenStoppedEvent.Reason`'s enum gains it, its description adding: "renewed: this daemon changed how the remote that lends the device streams it (its fast mark) and ended the view so it can start again that way; watch again." `relayedSource.start` takes `rt.link.Marked()` with `Down()`; `watchLink` ends the source `&ScreenEnd{Reason: StopRenewed, Cause: "the remote <name>'s fast mark changed"}` when it closes. Nothing else of the remote is ended: the link, the lease, Logcat, UI events and recordings stay.
  - `go test ./internal/schema -update`, the regenerated web client.
- Implements: ADR-N (Q1's reason); ADR-0078 §2 as superseded.
- Check: `cmd/droidyard` with `screenLab`: `TestAFastChangeReopensTheRemoteViewAtTheNewQuality`: open `qa/phone1`'s view (the host's start carries `max_size=1280`) and a remote `device.watchLogs` on it; `policy.update` on the client marks `qa` fast; the client's session gets `stopped renewed` for the view (seen failing `deviceGone` on step 1's base without this step) and nothing for the Logcat stream; a `device.watchScreen` sent at once answers, and the host's second `app_process` start carries `max_size=1024` with step 1's hook holding the host's release goroutine (seen failing with step 1's drop reverted: no second start); the host counts one hello. `TestRemoteViewEndsWhenTheDeviceIsNoLongerLent`, `TestUnpairedPeerViewEndsDeviceGone` still pass (A5). `go test -race ./cmd/droidyard -run 'Remote'`.
- Schema: `device.watchScreen`'s `stopped.reason` gains `renewed`, its description the sentence above. Nothing else.
- Needs: step 1.
- Not here: the web (step 3); a view starting while the mark changes ends `renewed` at once after its start and is watched again, nothing more.
- Rests on: a `renewed` end is published only for a mark change, never for a loss: level 4.
- Review: the Opus verifier (steps 1 and 2: the peer frame, the ordering, the contract), effort `high`.
- [ ] Done. Evidence:

### 3. The web: the toggle, and a view that watches again on `renewed`

- Builds:
  - Load `svelte-core-bestpractices` and `svelte-code-writer` first.
  - `web/src/lib/screen/liveview.ts`: `StopReason` gains `renewed`; a `renewed` stop does not call `onDeviceGone` and earns the streak's one re-watch, as `detached`; a second failure before a frame decodes stops with its reason and Restart.
  - `web/src/lib/screen/quality.ts` (new, pure, as `lending.ts`): `RemoteQualityModel` over `policy.get`/`policy.update`. The remote is the selected device's `host` (`device.list`); `fast` is read from `policy.get`'s `remotes` at each answered watch of a remote device and after its own write. `setFast(on)`: `policy.get`, the entry by name (none: an error, nothing written), the list sent back with only that entry changed (`fast: true`, or the key removed for off), `ifRevision`; one re-read and retry on `POLICY_REVISION_CONFLICT`; an unchanged mark sends nothing; a failure is `failure(e)`.
  - `LiveView.svelte`: a Fast toggle (`aria-pressed`, title "A lighter stream for a slow link; the view restarts"), shown only for a remote device whose remote `policy.get` lists, disabled while its write is in flight, its error in the existing alert line; `WHY.renewed`: "The view restarted for its new quality." `Project.svelte` builds the model with `callOn` and `deviceList`.
- Implements: the owner's item 1; Q1's UI half.
- Check: Vitest. `quality.test.ts`: the rewrite keeps the entry's `address`, `addresses`, `fingerprint` and every other entry, with `ifRevision` (seen failing with an entry rebuilt from name and mark); off removes the key; a conflict retried once, a second shown; an unchanged mark calls no update; a local device has no remote. `liveview.test.ts`: `renewed` → one `device.watchScreen` of the same device, `onDeviceGone` not called (seen failing with `renewed` handled as `deviceGone`); a second `renewed` before a frame decodes → `stopped` with Restart; `deviceGone` still calls `onDeviceGone`. `pnpm -C web run verify`; the autofixer gate.
- Schema: no change.
- Needs: step 2.
- Not here: the look (`review`); an indicator of the quality actually received (a shared source keeps its options; the register row stays).
- Rests on: —
- Review: the main session's diff and Svelte review.
- [ ] Done. Evidence:

### 4. Level 5, the documents, the close

- Builds: no product code.
  - (a) **Level 5** (main session; K1–K3 below): the client's UI in headless Chrome on `qa/<serial>`, Logcat open on it and a UI recording of it running: Fast on: the time from the click to the first frame of the new watch, the new result's size, no "device went away", the toolbar's device unchanged, Logcat still streaming, the recording still running and later stopped whole; Fast off: back to full. `droidyard remote fast qa on` on the client: the open view comes back at 1024 by itself and the toggle reads Fast. The host's daemon log: one server start per change, one hello for the run.
  - (b) **Documents**: `docs/interfaces/peer.md` §The peer port (a changed mark travels in the session; the client restarts its views, ADR-N); the register (the joined row closed `2za` and narrowed to waking and reboot; a new row: toward a host older than 0.36.0 a mark change still reconnects, ending its views `deviceGone`, the lease, Logcat and UI events streams and a recording of it); `docs/README.md` §Where the work stands; the index row; the `Superseded in part by` lines of ADR-0078 and ADR-0118; the API minor re-checked against main.
- Implements: A1–A6 at level 5.
- Check: each run's decisive output under the lane's evidence directory, quoted in the evidence line.
- Schema: no change.
- Needs: steps 1–3.
- Not here: a phone, a real VPN, an older host at level 5 (step 1's test only).
- Rests on: —
- Review: the Opus verifier at the close.
- [ ] Done. Evidence:

**Run rules (step 4, the main session only).** K1: two isolated daemons by the `verify-daemon` recipe (state and config under a fresh `/tmp/dyza.*`), paired by `share`/`join` as m2u did. K2: the lane's own scratch AVD (long side over 1280, so both presets differ), on an adb port no other lane uses (check `pgrep -fl qemu-system` and the board), lent through m2t K2's stand-in guard (`usb:` appended, so the host classes it physical); only the run's own adb server is ended; no owner device, AVD, daemon or repository is touched; nothing killed by name. K3: headless Google Chrome with its own profile, driven over CDP.

## Not in this milestone

- Restarting a source other viewers hold, so they switch quality too: ADR-0118 §4; register "A source keeps the options it started with…" stays open.
- A host older than 0.36.0: today's reconnect and its costs (Q2); a register row at step 4.
- The device list after a host restart (register, m2u step 7): a loss, not a mark change; unchanged.
- An unknown key inside a `remotes` entry (register, m2t step 7): the UI sends back every field `policy.get` returns, as the CLI does; the row stays.
- Local devices, MCP, per-view quality: Scope.

## Design questions

Each is Delegated: the main session applies the recommendation and writes ADR-N before step 1. The steps follow the drafts; if either goes otherwise, steps 1–3 are redrafted first.

- **Q1. How the open view comes back (ADR-N; supersedes ADR-0078 §2 and ADR-0118 §4 in part).** Recommended: the changed mark travels in the open link session as one frame, which the host applies before reading the client's next frame; the client then ends its relayed views of that host with a fifth stop reason, `renewed`, and the UI watches again for it as for `detached`, never re-reading the device list for it. The host drops a view's source reference before its end returns, so the re-watch's start, which reaches the host after the client's `events.unsubscribe` was answered, waits for the old source and starts the new preset. Trade-off: a peer frame (gated by the host's API, as ADR-0118 §3's acknowledgements) and one enum value; in exchange the link, the lease (ADR-0104 §4), the remote's Logcat and UI events streams and a running recording (ADR-0115 Decision 4) all survive the change, and no call meets a link down. Rejected: **today's reconnect with a `renewed` reason** (smaller in the peer protocol, but the session's end releases the lease, ends a running recording on the host and its mirror on the client, ends Logcat and UI events streams, and opens a gap in which a call is `unreachable` and a list read drops the selection; a waiting window for calls would be a second mechanism, and the host's release still races the new session's watch); **the relayed source re-attaching itself** (the browser's socket survives, but the source's one-shot life becomes a loop over host streams, subscription, control line, finger and Hub GOP, for one setting); **the UI recognising it alone** (it cannot know of a CLI change; the cause text is the daemon's words, not the contract); **reusing `detached`** (no enum change, but `detached` is the browser's socket, and a consumer could not tell the two apart).
- **Q2. A host older than the new API (ADR-N).** Recommended: the client sends it no mark frame and reconnects as today (its hello carries the mark), with today's costs: the view ends `deviceGone` and does not reopen by itself, the lease and a recording end. A register row says so. Rejected: a `renewed` end for an older host too (the view would reopen, but into the gap of a reconnect, the costs Q1 rejects); refusing the change (the CLI's mark would mean nothing to an older host).

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| A real VPN's round trips keep the reopen within 5 s | 2, 4 | not measured here (loopback); the owner's trial |
| Headless Chrome stands for the owner's browser | 3, 4 | the owner's use |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | [design] B1 (blocking): Q1 rejected the in-session mark for a release race the reconnect shares, and hid the reconnect's certain costs: the lease ends (ADR-0104 §4) and a running remote recording ends on both sides | Q1 now recommends the in-session mark (step 1) and states the reconnect's costs as fact; ADR-0104 §4 and ADR-0115 Decision 4 in Contract; the reconnect kept only for an older host (Q2, register row at step 4) |
| 1 | revise | [test] B2 (blocking): the host releases a view's source on a goroutine, so a re-watch may join the old full-quality source, and a passing test cannot show otherwise | Step 1 drops the reference before `end` returns; `TestAViewsEndDropsItsSourceAtOnce` and step 2's two-daemon test hold the release goroutine with a nil-in-production hook and are seen failing with the old release |
| 1 | revise | [contract] B3 (blocking): an immediate redial changes ADR-0098 §4 without a supersede | No redial change any more: a newer host keeps the session, an older one reconnects as today; ADR-0098 is untouched |
| 1 | revise | N1 the window's early end; N2 in-gap checks seen failing only by timing; N3 old-session calls in the window; N4 the level-5 bound could not create a window; N5 a web-only alternative to Q2; N6 `Connected()`-gated paths | All moot with the window gone: no call waits and no gap exists for a newer host; A4's bound is the UI's streak (Vitest) |
| 2 | revise | B1, B2, B3 resolved. New [concurrency] (blocking): "`update` writes the frame" reads as a write of up to 10 s under `l.mu` and `Host.mu`, stalling the link's reader and able to lose the link; on a goroutine, two quick changes could reorder and a source could start ahead of the frame | Step 1: `update` only records and wakes a sender; the sender writes the current mark outside both locks, then closes the `Marked()` channel current at the write's start; `TestAPendingMarkWriteBlocksNothing`, `TestTwoQuickChangesLeaveTheLast`. Answered in the plan without a third round (`milestone-plan` §Writing one, 7) |
| 2 | revise | the hook must reach `cmd/droidyard`; the in-loop mutation fails only by timing; A2's shared-source case; `allowed()` in the loop; `Marked()` while down | An exported test seam passed through `labOptions`; `SetFast` sleeps 200 ms in the test; A2 qualified; the loop checks `allowed()`; `Marked()` is open while down |
| 1 | revise | nits: "a change reconnects" is m2y's code, not ADR-0118's text; `lastErr` during a renewal; `peer.md`'s path; the register row names the lease; chained changes | Cited to `link.go` and m2y step 7; no reconnect, so no `lastErr`; the path is `docs/interfaces/peer.md`; the older-host row names the lease; a change during a pending frame write is the next `update`, and `Marked()` is fresh per change (step 1) |

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.
