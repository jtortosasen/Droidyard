# Plan: Milestone 2y — A live view that responds: input over the open stream, live drag, a remote view that keeps up

- Status: Approved
- Approved: 2026-10-21, Delegated (the main session, under the owner's standing instruction to apply the recommended option): Q1–Q6 as recommended; Q8 is decided at step 8 by design. ADR-A takes number 0117, ADR-B 0118, ADR-C 0119 (reserved for 2y). **Q7 decided by the owner** (2026-10-21): full quality by default; a remote marked fast in the client's configuration (or by a command) gets S8's preset. Steps 7 and ADR-B follow that, not the draft below.
- Date: 2026-10-21
- Milestone: "2y: a live view that responds: the view's input travels the stream it already opened, a drag and a wheel reach the device as they happen, and a remote view keeps up over a slow link" (the owner's requests of 2026-10-21: "a direct interaction tunnel … speed and response come first, not losing quality … it was authorized at the start", and, approving the milestone, "it will be used over the internet, remote through a VPN … a mode that switches to UDP or WebRTC might win something"; `docs/README.md` §Where the work stands says "next, the owner's choice").
- Phase: none. The MVP is done (2h); this plan's Scope table is the boundary.
- Contract: ADR-0077 §1 (the server's options), §4 (the seam), ADR-0078 §3–§6 (the stream, the socket, the Hub, lifetimes), ADR-0079 (all of it), ADR-0080, ADR-0099 §2 (what a host lends), §5 (each peer call an operation), ADR-0100 §3, §5, ADR-0104 §2–§4 (the lease and its 2 s sweep), ADR-0098 §3 and ADR-0109 §3, §4 (the 2 s pin and reconcile; listener tags), ADR-0015 §2, §4, §6, ADR-0060 §2 (audience `human`), ADR-0010, ADR-0020 §Adapter projections, ADR-0043 §2 (the trace line's rule), ADR-0076 (the operation target), ADR-0094 §5 (the UI is never refused); `ui-automation.md` §Input primitives, `adb-device.md` §Screen viewing, `http-websocket.md` §`GET /v1/screen`, `peer.md` §The peer port, §What a peer may do. **New, Delegated, written by the main session:** **ADR-A** (Q1–Q5; number 0117; supersedes ADR-0079 §1 (in part: the live view's own input), §3 and its Consequences' per-input cost and drag line, ADR-0078 §4 in part (the socket receives input), ADR-0077 §4 in part (the seam gains `Control`), ADR-0100 §3 and §5 in part (a remote view's input travels the peer's screen socket; a relayed source injects), ADR-0099 §2 (`device.scroll` joins the lent list) and §5 in part (tunnel input is recorded as an operation, not called)); **ADR-B** (Q6, Q7, from step 1's note; supersedes ADR-0078 §5 in part, and ADR-0077 §1 in part if S2 adopts options); **ADR-C** (Q8, at step 8). B and C take numbers from `go run .claude/skills/work-board/board.go adr 2y`, never from `docs/adr/` alone.
- Register: the main session's commit that adds this plan sets `Work` = `2y` on R2, R3, R4 and R6 (Scope), gathered under the owner's standing instruction (Delegated). R1 keeps `trial` and R5 keeps `C4`: this plan measures and decides them but leaves them open (recommended; the main session's call). The trial rows for physical devices and two real Macs stay the owner's.
- Lanes: 2x (UI recording; lending `uiDump`/`uiEvents`) is open on devices, contract, web and docs. This plan touches none of `uidump.go`, `uievents.go`, `watchuievents.go` or the UI events panel. Shared risk: formapp (step 1 adds an activity; a manifest conflict at merge is possible) and the API version (the next free minor at the close's merge).

**The owner should see:**
- A tap, a key or a wheel notch in the live view reaches the device in a few milliseconds (today's `device.tap` call takes p50 76–83 ms before the touch is written), and a drag moves the app under the pointer while the button is held, not after release.
- The same for a phone lent by another Droidyard, through both daemons.
- The view is authorized once, when it opens; it closes within about 2 s when the policy stops admitting the device, the device or session goes, or the peer is unpaired, and a finger held at that moment is cancelled, never released into a click; each gesture is still one Activity row and one trace line.
- Over a slow or lossy link the remote view stays close to the device instead of falling further behind, with measured numbers and a recommendation on UDP, QUIC or WebRTC.
- One minor API version: `device.watchScreen`'s result gains `control`; `device.scroll` is new (human only, not an MCP tool).

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| The owner's rule change: authorize once per view, input over the open connection, revocation bounded, every input recorded | yes | Q1–Q5, ADR-A; steps 2, 3, 5 |
| R1 "Whether the live view's input latency (`ui-automation.md` §Input primitives) suits a person" | part | measured before (step 1) and after (steps 4, 5, 9); the owner's feel stays a trial, so the row stays open with the numbers |
| R2 "The live view has no wheel scroll, typing into the canvas, live drag tracking, multi-touch, audio or clipboard; a drag is sent once released" | part | live drag and wheel in (steps 2–4, Q4); the row narrows to typing into the canvas, multi-touch, audio, clipboard |
| R3 "The live view does not wake a sleeping device, … no quality control (size and bit rate are constants)" | part | the remote part of quality (step 7, Q7); waking and reboot stay; the row narrows |
| R4 "A touch for a stale size right after a rotation may be dropped by scrcpy while the call reports `delivered`" | yes | step 1 S5 settles the fact; the tunnel claims no `delivered` per event; the row is closed or rewritten as the fact found |
| R5 "H.265 or AV1 for the stream …" (C4) | decide | step 1 S8's numbers; Q7 recommends not building it now; the row keeps `C4` with the numbers |
| R6 (quick) "The revocation sweep … revokes an unpaired peer's session and view sockets together, so that peer's relayed screen may end `failed` rather than `deviceGone` …" | yes | step 5 |
| A UDP, QUIC or WebRTC path | no, decided | step 8 measures; Q8 decides whether a later milestone builds one |
| Typing into the canvas, multi-touch, audio, clipboard, waking a device | no | R2/R3 as narrowed |
| The trial rows: physical devices, hardware encoders, two real Macs over the company VPN | no | the owner's (`trial`) |
| Agents' coordinate input over MCP (C5) | no | `device.scroll` is human only, as the other three (ADR-0079 §5) |

## Starting point

- `m2y` at `f9854e1` (main after 2w), API `0.32.0` (`internal/domain/system/system.go`), ADRs up to 0112.
- **Input today** (`internal/domain/device/screeninput.go`): `device.tap`, `device.swipe`, `device.pressKey`, audience `human`, `adb.plain` (lendable), resource the device's write key, `authorize` = `adb.authorize` (one `adb devices -l` per call, `target.go`), handler `injectScreen` → `Source.Inject`. `screen.EncodeControl` (`screen/input.go`) writes scrcpy's touch (32 B) and key (14 B) messages, every touch on pointer id -2; no scroll, no cancel. `screenSource.Inject` (`screensource.go`) holds a token for a whole gesture, so two gestures never interleave, and writes each message under a 2 s deadline; a failed write ends the source `failed`.
- **The view** (`watchscreen.go`): `device.watchScreen` → `streamId`; `screenStream` ends once (subscription, socket, 10 s unattached, source, lease, `StopAll`). `AttachScreen` hands the HTTP adapter a `screen.Viewer` with no input. `adapters/httpapi/screen.go`: `/v1/screen` closes 1003 on any client frame after auth (`sendScreen`, shared with `peerscreen.go`'s `ServePeerScreen`).
- **Remote** (`remotescreen.go`): `relayedSource.Inject` returns `errRelayedInput`; a remote view's input is the client's `device.tap` relayed to the host's (ADR-0100 §3). `relayedSource.start` reads the host's result with a plain `json.Unmarshal`. `hosting/screen.go` `ScreenConn` only reads. The host's lease sweep (`lease.go` `RunLeases`, every 2 s, started by `serve.go` in every daemon) ends a peer's streams when the device is no longer lent or admitted; the peer listener's sweep (`adapters/peer/peer.go`, 2 s) closes an unpaired peer's sockets with 4403. **No sweep ends a local view whose physical device the policy stops admitting**: the policy is checked at start only. `hosting.classify` tags a loopback address `lan`.
- **Hub** (`screen/hub.go`): a join queues the current GOP (its key frame up to about 3 s old); a viewer is reset to the next key frame only past 20 MiB or 16,384 packets queued, which is tens of seconds of video on a slow link.
- **Operations** (`internal/capability/operation.go`): a trace line names the operation and who asked, never a parameter; `flush` publishes `operation.state` and then writes trace lines with `logf` on the caller's goroutine.
- **Web** (`web/src/lib/screen/`): `input.ts` classifies a gesture at the lift and calls the capabilities over `POST /v1/call`; `stream.ts` never sends after auth; `decoder.ts` uses `optimizeForLatency`; `LiveView.svelte` draws the newest frame per animation frame.
- **Measured** (m2p step 1, step 7; m2t step 3): control write → next video bytes p50 22–42 ms, p95 141–195 ms; `adb devices -l` p50 49 ms; `device.tap` operation p50 76–83 ms, through a loopback peer pair p50 98 ms. m2p's "click to the next decoded frame p50 51–53 ms" is **not causal**: formapp's screen produces frames of its own (a focused field's cursor), so the next frame need not be the tap's; step 1 measures against a screen that changes only on touch.
- **Spike tools**: `internal/domain/device/screen/spike_test.go` (`TestSpikeScrcpy`, skipped without `DROIDYARD_SPIKE_SERIAL`): starts the vendored server directly, writes a control script, prints write → next bytes. formapp has `FormActivity`, `ProbeActivity`, `WidgetsActivity`.
- **Baseline, 2026-10-21 at `f9854e1`:** `go vet ./...` clean; `go test ./...` ok in every package (`internal/domain/device` 161 s). With this file added, `internal/doccheck` fails `TestPlansIndexMatchesFiles` until the main session adds the index row in the same commit (the reviewer's run also showed template and ADR-mention failures of the first draft; both fixed here).

## Run rules (the device phases: the main session only)

- **K1.** Isolated daemons by the `verify-daemon` recipe (state and config under a fresh `/tmp/dyy.*`). **The before binary** is step 1's commit built with `-tags droidyard_inputprobe`: step 1 changes no product behaviour, so it measures today's path with the probe. Each later run uses its step's commit with the same tag. A peer pair is two isolated daemons on this Mac, host and client, paired by `share`/`join` as m2u did; the host lends one scratch emulator through m2t step 9's stand-in guard (it reports the emulator as a `usb:` physical device). Evidence under `B=~/Library/Caches/droidyard-dev/m2y`.
- **K2.** The lane's own scratch AVDs under `$B/avd` (API 31 and API 36, google_apis arm64), on an adb port no other lane uses (never 5037, 5197, 5199; check `pgrep -fl qemu-system` and the board first), behind m2p's guard copied to `$B/bin/adb`. Only the run's own server is ended (`adb -P <port> kill-server`). No owner device, AVD, daemon or repository is touched; nothing is killed by name.
- **K3.** Headless Google Chrome with its own profile, driven over CDP by the main session; the implementer never runs a device phase.
- **K4.** The probe exists only under `-tags droidyard_inputprobe`; the product build carries none. Both processes of a measurement run on this Mac, so wall clocks are comparable to well under a millisecond.

## Measures and targets

- **F** (the floor): a control write → the first video bytes after it, `TestSpikeScrcpy` against formapp's `TouchActivity` (step 1), whose screen changes only on a touch. The device's reaction plus encoding; no path can beat it.
- **T1** (input → device): the client's send of a touch-down (the harness's WebSocket write; before, its `POST /v1/call`) → the end of that down's control-socket write in the daemon that owns the device, from the probe's `probe write` line.
- **T2** (touch → frame showing it), headless Chrome over CDP, `TouchActivity`: from the pointer event that makes the client send the touch-down (after: `pointerdown`; before: `pointerup`, since the old UI sends `device.tap` at the lift) to the first drawn canvas frame whose sampled centre pixel is within ±6 levels per channel of that down's colour. Each tap is held 400 ms (H) on both paths: at or above the old UI's 300 ms `TAP_MS` (`input.ts`), so before the change it sends `durationMs` and the device holds the down 400 ms after the call lands (a shorter hold is sent with `Hold` 0, a down and an up back to back, and the down's colour may never be drawn); after the change the down is held as long by the pointer. **Drags**: moves spaced at least 4 colour levels (2 % of the width) apart; each drawn frame is matched to the nearest move sent before it; T2 is reported per drawn frame, and moves never drawn are counted, not timed.
- **Lag** (remote): a packet's arrival in the client daemon's Hub minus its push into the host's Hub (`probe packet` lines). **Staleness**: sampled every 100 ms during continuous scrolling, now minus the host push time of the newest packet the client's Hub holds; it grows while a reset viewer waits for a key frame, which Lag cannot see.
- **Links** (step 1's userspace proxy, `linkem`): L1 RTT 20 ms, 50 Mbit/s, no stall; L2 RTT 100 ms, 6 Mbit/s, 1 % stall; L3 RTT 150 ms, 2.5 Mbit/s, 2 % stall. A "stall" holds one 16 KiB chunk of one direction for max(200 ms, 2 × RTT): the effect TCP shows for a lost segment, not loss itself (no packet loss can be injected without root).

## Acceptance

- [ ] A1. **T1 local**: p50 ≤ 3 ms, p95 ≤ 10 ms on API 31 and 36, against step 1's before. Source: the owner's request; Q1, Q2. Proven by: steps 2, 9.
- [ ] A2. **T1 through the loopback peer pair** (client's socket → host's control socket): p50 ≤ 8 ms, p95 ≤ 20 ms, against step 1's before (m2t: p50 98 ms). Source: the owner's request; Q1. Proven by: steps 5, 9.
- [ ] A3. **T2 for a tap**, local, both levels: p50 ≤ F p50 + 25 ms and p95 ≤ F p95 + 40 ms, and p50 at least 40 ms under step 1's before. If step 1's before is already within F p50 + 40 ms, that last clause is recorded as a Change (the tap's lag is not the call path) and the rest stands. Through the peer pair: p50 within 10 ms of local. Source: R1. Proven by: steps 4, 5, 9.
- [ ] A4. **Live drag and wheel**: during a 2 s drag, `TouchActivity`'s colour follows the pointer before the release, drawn frames at p50 ≤ F p50 + 25 ms after the move they show, with the count of moves never drawn reported; wheel notches scroll Settings' list; Back, Home and Recents work over the socket. Source: R2; Q4. Proven by: steps 4, 9.
- [ ] A5. **Revocation**: a view ends within 2.5 s when the policy stops admitting its physical device (local, and a remote id on the client), when the device is no longer lent to the peer (host), when the peer is unpaired (both sides, `stopped deviceGone`, R6), and as today when the session, the device or the daemon ends. After the end nothing from that view reaches the control socket but one `ACTION_CANCEL` for a finger it held; no `ACTION_UP` is ever written for it. Source: Q1, Q2. Proven by: steps 2, 5 (level 4), 9 (level 5, the stand-in; `TouchActivity` shows the cancel's colour, not the up's).
- [ ] A6. **Records**: each gesture (tap, long press, drag, key, wheel burst) is one operation of the capability that replays it (`device.tap`, `device.swipe`, `device.pressKey`, `device.scroll`): `completed`; `cancelled` when the daemon or the browser cancelled it; `failed` with `DEVICE_STREAM_FAILED` when a write failed. Each has its Activity row and trace line (`client=ui`, or `client=peer … peer=<name>` on the host); its params pass that capability's validation; a stalled trace writer or event publisher never delays an input, and every gesture is in the store once the writer resumes. Source: Q5; ADR-0043 §2. Proven by: steps 3, 9.
- [ ] A7. **Remote view**: on L2 and L3, over 60 s of scrolling with the peer having started the source (so Q7's preset is in force; the run states the options), Lag p95 ≤ 1 s and Staleness p95 ≤ 1 s, Staleness max ≤ 1.5 s when S3's key-frame request exists and ≤ the GOP + 1 s otherwise, with rejoins per minute reported; step 8's before is reported as it is. Source: the owner's second request; Q6, Q7. Proven by: steps 6, 7, 8.
- [ ] A8. **Transport**: step 8's numbers on L1–L3 compare today's TCP, the TCP path after steps 6–7, and an estimate of what a datagram transport would remove; ADR-C records the decision and names the milestone that would build a UDP path, or none. Source: the owner's second request; Q8. Proven by: step 8.
- [ ] A9. **Contract**: one minor version; the schema diff is exactly `device.watchScreen`'s result field `control` and the new `device.scroll`; the MCP tool list is unchanged; `device.tap`, `device.swipe`, `device.pressKey` are unchanged. Source: `capability-api.md` §Discovery and versioning; Q3. Proven by: steps 2, 3.

## Steps

### 1. Spike and baseline: a screen that changes only on touch, the floor, the facts the tunnel rests on

- Builds:
  - **formapp `TouchActivity`** (implementer; `am start -n dev.droidyard.formapp/.TouchActivity`): one full-window view, system bars hidden (immersive), no text, no focusable field. Background on `ACTION_DOWN`/`MOVE`: `rgb(round(255·x/w), round(255·y/h), 255)`; on `ACTION_UP`: black; on `ACTION_CANCEL`: `rgb(255, 0, 255)`; on a scroll event: `rgb(0, 255, 128 + 16·notches)` clamped. `formapp_test.go` builds it (seen failing with the manifest entry removed).
  - **`TestSpikeScrcpy`** (implementer): control kinds `cancel:<x>:<y>`, `scroll:<x>:<y>:<h>:<v>` (scrcpy 4.1's layouts, read from the source at tag v4.1 cloned into `.refs/scrcpy`, not from memory) and, if that source has one, `reset` (its reset-video message); `-spike.dial=<host:port>` to reach the forward through a proxy.
  - **The probe** (implementer) `internal/domain/device/inputprobe_tagged.go` (`//go:build droidyard_inputprobe`) and its no-op twin, as `nodump_tagged.go`: one daemon-log line per control write (`probe write <action> <unix ns>`) and per Hub push of the scrcpy and relayed sources (`probe packet <role> <pts> <unix ns>`); and `DROIDYARD_PROBE_LISTENER_TAG=vpn`, read only by the tagged build, which tags every peer listener `vpn` so a loopback pair takes Q7's preset (step 7). `TestProbeIsANoOpByDefault`.
  - **`linkem`** (implementer), uncommitted, in the worktree's gitignored `.refs/m2y-spike/linkem/` (read in full by the main session before it runs): a TCP relay with, per direction, a one-way delay, a rate cap with at most 64 KiB queued, and the stall model of §Measures; it prints per-run byte and stall counts. With it, `notsentprobe`: a Go TLS server writing 64 KiB messages to a client through `linkem` at 2.5 Mbit/s, with and without `TCP_NOTSENT_LOWAT` 64 KiB, printing how long each write blocks and each message's arrival delay.
- The run (main session, K1–K4, both AVDs). Results to `docs/research/spike-live-input-2026-10.md`, with these stop rules, each recorded as a Change with the dependent Q redrafted before step 2:
  - **S1** a scroll message in 4.1's layout scrolls Settings' list and turns `TouchActivity` green on both levels; its golden bytes and bounds are recorded. Otherwise the wheel leaves the milestone (Q4).
  - **S2** F, p50/p95 of 20 touches held 150 ms, with the production options and with `video_codec_options=i-frame-interval=2,latency=0,priority=0`. Adopted (ADR-B) only if F p95 falls by at least 20 % on a level and the frame rate during a drag does not fall; say what a phone's encoder would need (unverified).
  - **S3** whether 4.1 has a reset-video message, and the time from it to the next key frame on both levels (Q6 asks for a key frame only if it comes within 500 ms).
  - **S4** a down, a move, then `ACTION_CANCEL` at the move's point: `TouchActivity` shows the cancel's colour, a `Button` under a cancelled press (formapp's Like) does not count a click, and the next down lands, on both levels. Otherwise Q2's lift is redrafted before step 2 (the lift is the plan's safety rule; no step proceeds on an `UP`).
  - **S5** a touch written with the pre-rotation size within 100 ms of `settings put system user_rotation 1`: dropped or landed, and the time until the new size's config packet (R4).
  - **S6** the before: T1 and T2 for 20 taps held 400 ms (§Measures) through today's daemon (`device.tap` from the live view), and through a peer pair (K1); 20 drags' T2 at the release.
  - **S7** `notsentprobe` on macOS: with the option, write blocking replaces arrival delay (arrival delay p95 under 0.5 s); without it, arrival delay grows. Otherwise step 7 uses `SO_SNDBUF` 128 KiB, measured the same way here.
  - **S8** through `linkem` on L1–L3 with `-spike.dial`: F, bytes per second and Lag during 60 s of drags, for the presets 1280/4 Mbit/s/60 fps (today), 1024/2 Mbit/s/30 fps and 720/1 Mbit/s/30 fps; and on API 36, `video_codec=h265` at 1280/4 Mbit/s: F, bytes per second, and whether Chrome decodes it.
- Implements: the facts Q2, Q4, Q6, Q7 and A1–A4's before rest on.
- Check: `TestFormAppBuilds` with `TouchActivity`; `TestProbeIsANoOpByDefault` (fails with the probe's log call in the untagged file); the S-rules above, each with its numbers in the note.
- Schema: no change.
- Needs: real emulators, the SDK's build tools, network for the scrcpy source.
- Not here: no product behaviour changes; `linkem` and `notsentprobe` are not committed (measurement tools, not tests of the product).
- Rests on: F is causal (the screen changes only on touch), level 5.
- [ ] Done. Evidence:

### 2. The tunnel in the daemon: a view's input over its own socket, authorized when it opened

- Builds:
  - `screen/control.go`: `ParseControl([]byte) (Event, error)` for the wire of Q2 (a type byte, then its fixed length; an unknown type, a wrong length, an unknown action or key is an error), `Event` values, and `EncodeEvent(Event, Size) ([]byte, error)` producing scrcpy's touch (down, move, up, cancel), key (down and up) and S1's scroll bytes. Pure, no panic on any input. A golden file `screen/testdata/control-wire.json` (wire bytes → event → scrcpy bytes) that step 4's Vitest also reads.
  - **The finger lives in the source** (Q2). `screen.Source` gains `Control(holder, Event) error`, non-blocking. `screenSource` keeps who holds the device's one finger: none, an `Inject` (for its whole gesture, under its existing token), or a stream. `Inject` gains the holder (`Inject(ctx, holder, in)`; `injectScreen` passes the call's origin), so **an `Inject` is classed by its call's origin**: a peer's lent `device.tap` or `device.swipe` (`client=peer`) is a peer holder, any other call a local one. While an `Inject` holds the finger, the writer drops tunnel touches of holders it does not yield to; an `Inject` waits for a stream's gesture to end (bounded by the call's context). A down from a local holder (the daemon's own UI stream, or a local `Inject`) takes the finger from a peer holder (stream or `Inject`), writing `ACTION_CANCEL` for the peer's gesture first; a preempted peer `Inject` stops writing and ends with today's partial-delivery warning. A peer's down never takes it from a local holder (ADR-0094 §5, ADR-0104 §2); between two local holders the first keeps it. A stream whose down was dropped has the rest of that gesture (moves, up, cancel) dropped until its next down. One writer goroutine per source, fed by a queue of 256: a queued move is replaced by a newer move of the same holder, a queued scroll summed with the next (clamped to S1's bound); a full queue refuses the event and the sending socket is closed 1008, the source and its other viewers untouched; a failed write ends the source `failed` as today. The writer drops a queued event whose stream has ended, except that stream's cancel. `relayedSource.Control` returns `errRelayedInput` until step 5.
  - The view (`watchscreen.go`): `screenStream` gains `Input([]byte) error`: refused once the stream ended; at most 1,000 messages in any second (more closes the socket 1008); move/up without a down are ignored; events wait for a known frame size. **Every lift the daemon writes is `ACTION_CANCEL`**, never `UP`: on a client `cancel`, and on every end of the stream (socket, subscription, revocation, lease, `StopAll`), before the viewer is released; only the client's own `up` writes `UP`. `WatchScreenResult` gains `Control bool` ("true: binary input frames on the stream socket reach the device"). **In this step `control` is true only in a `droidyard_inputprobe` build** and `Input` is refused otherwise, so the product never has an unrecorded input path; step 3 makes it true for a local device. A remote id stays false until step 5; `AttachPeerScreen`'s viewer keeps refusing input until step 5.
  - The adapters: `httpapi.ScreenViewer` gains `Input([]byte) error`; `sendScreen` hands each binary frame after auth to it and closes 1003 on a text frame or a parse error, 1008 on a refusal for rate or queue; `/v1/screen`'s 4 KiB read limit stays. `cmd/droidyard/serve.go`'s `screenViewer` forwards it.
  - **The view sweep** (`lease.go`): `SweepViews`, called on `RunLeases`' 2 s tick, ends with `stopped deviceGone` (cause "the policy no longer admits the device") every view of this daemon's own clients on a physical device (the kind recorded at the start, no adb run) or a remote id whose `admitted` is now false. The host's peer views keep the lease sweep.
  - API version: the next free minor (`0.33.0` unless 2x has taken it), `go test ./internal/schema -update`, the regenerated web client.
- Implements: ADR-A (Q1, Q2).
- Check:
  - `TestParseControlRefusesWhatIsNotTheWire` (table: each type at each wrong length, unknown type, action and key, empty), `FuzzParseControl`, `TestEncodeEventGolden` (the golden; S1's scroll and S4's cancel bytes).
  - With a fake source recording writes: `TestInputReachesTheControlSocket`; `TestInputAfterTheStreamEndedSendsNothing` (seen failing with the ended check removed); `TestEveryEndCancelsAHeldFinger` (table over socket close, subscription end, sweep, lease end, `StopAll`: the last write is `ACTION_CANCEL` at the last point, and no `ACTION_UP` is ever written; seen failing with the lift as `UP`); `TestInjectAndTunnelNeverInterleave` (a 1 s local `Inject` swipe and a tunnel drag begun during it: the swipe's writes are whole, and none of the drag's down, moves or up is written; seen failing with the per-message lock only); `TestHostUIPreemptsAPeerFinger` (a peer stream holds; a local stream's down: a cancel for the peer, then the local down; the reverse writes nothing for the peer); `TestHostUIPreemptsAPeerInject` (a peer-origin 5 s `Inject` swipe; a local stream's down mid-swipe: a cancel, then the local down, and the call returns `delivered` with the partial-delivery warning; seen failing with every `Inject` classed local); `TestMovesCoalesceBehindAStalledWrite` (a blocked write, 100 moves queued: one move written after it, the down and up never dropped); `TestFullQueueClosesOnlyTheSender` (the source and another viewer go on); `TestInputRateClosesTheSocket`; `TestViewEndsWhenThePolicyStopsAdmittingIt` (fake policy flipped, `SweepViews`: `stopped deviceGone`, then `Input` refused; seen failing with the sweep's admitted check removed); an emulator's view is never ended by the sweep; `TestControlFalseWithoutTheProbeBuild`; `TestControlFalseForARemoteID`; `TestPeerViewerRefusesInput`.
  - `adapters/httpapi`: a binary frame after auth reaches `Input`; a text frame closes 1003; a frame before auth is the auth frame (4401 as today).
  - **Level 5 (main session, K1–K4, both AVDs, the probe build)**: a Go WebSocket harness opens `device.watchScreen` over `/v1/ws`, attaches `/v1/screen`, and sends 20 taps (held 400 ms) and a 2 s drag to `TouchActivity`: A1's T1; the colour follows the drag before the up; the harness closes mid-drag and the screen shows the cancel's colour.
- Schema: the version; `device.watchScreen`'s result gains `control` (boolean, required). Nothing else.
- Needs: step 1 (S1, S4, the probe); ADR-A.
- Not here: no record yet (step 3, which also turns `control` on in the product); no remote (step 5).
- Rests on: no input of a view reaches the device after the view ended, and no daemon-written lift completes a gesture: level 4 (fake source, every end path) and level 5 (the cancel's colour).
- Review: the Opus verifier (untrusted bytes from the browser, the finger's ownership, the writer's concurrency, the security rule), effort `high`.
- [ ] Done. Evidence:

### 3. Each gesture recorded, off the hot path; `device.scroll`; control on

- Builds:
  - `internal/capability/operation.go`: `Operations.Record(Recorded)` inserts a terminal operation (capability, origin's client, session and workspace, peer, target, params, started, finished, state `completed` | `cancelled` | `failed` with its code) and queues its start and terminal `operation.state` events and its trace line under `mu`, without flushing; a flush on another goroutine publishes and writes them, so a stalled `logf` or publisher never blocks `Record`. It takes no resource and runs no hook.
  - `internal/domain/device/inputrecord.go`: each `screenStream` turns its events into gestures (Q5): down…up is a `device.tap` (travel within 1 % of each axis and at most 2,000 ms; `durationMs` the hold) or a `device.swipe` (at most 64 points resampled by time; a gesture over 5,000 ms has its times scaled into 5,000 ms, the operation's `ms` keeping the real duration); down…cancel is the same shape in state `cancelled`; a key is `device.pressKey`; scroll events less than 250 ms apart at one point are one `device.scroll`, split when their sum passes its bound; a gesture whose write failed is `failed` with `DEVICE_STREAM_FAILED`. Gestures go to the store through a channel of 256 drained by one goroutine calling `Record`; the channel fills only if `Record`'s lock is held for long, and then a record is dropped and counted (one daemon-log line per minute at most): the one exception ADR-A states. The record's origin and target are the `device.watchScreen` call's.
  - `device.scroll {device, x, y, dx, dy}` in `screeninput.go`, shaped as the other three (human, `adb.plain`, write key, `DEVICE_NO_STREAM`), its bounds from S1; `screen.EncodeControl` gains `Scroll`.
  - `control` becomes true for a local device in every build; `Input` accepted.
- Implements: ADR-A (Q4, Q5); ADR-0043 §2 (the trace line names no parameter).
- Check: `TestGestureRecords` (table: a tap, a long press, a 3 s drag, a 7 s drag, a cancelled drag, a key, a wheel burst, a failed write → one record each, the right capability and state, params that pass the descriptor's own validation; seen failing with the drag's resampling removed); `TestRecordsNeverBlockInput` (a trace writer and a state publisher that block: 1,000 inputs still reach the fake control socket within 1 s, and once they unblock every gesture is in the store and traced; seen failing with the record flushed synchronously); `TestRecordedOperationIsTraced` (the line `client=ui capability=device.swipe … state=completed`, no parameter); `TestRecordedOperationFromAPeerNamesIt`; `TestControlTrueForALocalDevice`; `device.scroll`'s policy, remote and no-stream tests as `device.tap`'s; the MCP tool list unchanged.
- Schema: `device.scroll` with its params, result and errors. Nothing else.
- Needs: step 2.
- Not here: no change to `device.tap`'s, `swipe`'s or `pressKey`'s contract.
- Rests on: an input never waits on its record, level 4.
- Review: the Opus verifier (contract, the operation store's locking), default effort.
- [ ] Done. Evidence:

### 4. The web streams its input (the first end to end in the UI)

- Builds:
  - Load `svelte-core-bestpractices` and `svelte-code-writer` first.
  - `web/src/lib/screen/control.ts`: pure; encodes Q2's wire from pointer and wheel samples normalised to the frame; reads `control-wire.json`.
  - `stream.ts`: `ScreenSocket.send(bytes)`, nothing sent after close.
  - `input.ts` / `LiveView.svelte`: with `control: true`, `pointerdown` (with capture) → touch down, every `pointermove` while down → move, `pointerup` → up, `pointercancel` and `lostpointercapture` → cancel; `wheel` → scroll (pixel deltas over 100 px a notch, line deltas one each, clamped); the nav buttons → keys; a pointer that goes down on a bar sends nothing (as today). With `control: false` the canvas takes no input and says so in one line. Text keeps `device.inputText`. The calls to `device.tap`, `device.swipe`, `device.pressKey` leave the UI.
- Implements: ADR-A (Q2, Q4); R2.
- Check: Vitest: the golden; a drag's messages in order with every move; a cancel on lost capture; a bar press sends nothing; `control: false` sends nothing; the wheel's notches. `pnpm -C web run verify`; the autofixer gate. **Level 5 (main session, K1–K4, both AVDs, headless Chrome)**: A3's T2 for 20 taps; A4's drag (colour before the release, per-frame T2, undrawn moves counted); a wheel over Settings' list; Back, Home, Recents; each gesture one Activity row and one trace line `client=ui` with its capability.
- Schema: no change.
- Needs: steps 2, 3.
- Not here: the look (`review`); a remote device (step 5).
- Rests on: —
- Review: the main session's diff review and Svelte review.
- [ ] Done. Evidence:

### 5. The tunnel for a lent device, both daemons authorized once

- Builds:
  - `hosting/screen.go`: `ScreenConn.Send(ctx, []byte)`, one writer, bounded queue as step 2's.
  - `remotescreen.go`: `relayedSource.start` reads the host's `control` (an older host's result lacks it: false; nothing else changes, since no parameter is sent). `relayedSource.Control` re-encodes the event into Q2's wire and sends it on the video socket when the host granted control, else `errRelayedInput`; it keeps the client side's finger as step 2's source does. The client's `WatchScreenResult.Control` is the host's grant.
  - The host: `AttachPeerScreen`'s viewer accepts input for a stream that peer opened (its grant was that call's `authorize`, which leased the device, ADR-0104 §2) and holds the finger as a peer holder (step 2's preemption); `ServePeerScreen` passes binary frames to it as `/v1/screen` does. The host records the peer's gestures (`client=peer`, `peer=<name>`); the client records its UI's against the remote id.
  - R6: `relayedSource.readEnded` reads close code 4403 (`PeerCloseRevoked`) as `stopped deviceGone`, cause "the remote revoked the pairing".
- Implements: ADR-A (Q1 for a lent device).
- Check: `cmd/droidyard` with two in-process daemons and a fake device: `TestRemoteInputReachesTheHostsControlSocket`; `TestRemoteFingerCancelledWhenTheClientViewEnds` (a cancel on the host, no up); `TestRemoteViewEndsWhenTheDeviceIsNoLongerLent` (the host's lease sweep: the client sees `deviceGone`; no input after it reaches the fake); `TestHostUIPreemptsTheRemoteFinger`; `TestUnpairedPeerViewEndsDeviceGone` (R6; seen failing `failed` before); `TestOlderHostGivesAViewWithoutControl` (a host stub whose result lacks `control`); host and client records.
  **Level 5 (main session, the peer pair, probe build)**: A2's T1; A3's T2 through the pair; a drag on the lent emulator; `droidyard unshare <serial>` on the host mid-drag ends the client's view `deviceGone` within 2.5 s and `TouchActivity` shows the cancel's colour.
- Schema: no change.
- Needs: steps 2, 3, 4.
- Not here: no change to how the host decides what it lends.
- Rests on: a host injects only for a stream that peer opened and still holds, and never over its own UI, level 4.
- Review: the Opus verifier (the peer surface, revocation, the finger), effort `high`.
- [ ] Done. Evidence:

### 6. A viewer never far behind

- Builds:
  - `screen/hub.go`: each queued packet carries the time it entered **this viewer's** queue (a join's GOP enters at the join, so it is not lag); a viewer whose oldest queued packet entered more than `MaxViewerLag` (500 ms, Q6) ago is emptied and rejoined at the next key frame, as an overflow is; the byte and packet bounds stay. With S3's reset-video, the Hub's owner is told (an `OnLag` hook) and the scrcpy source writes one reset per 2 s at most, on the control writer's queue.
  - S2's codec options in `screenServerOptions`, if S2 adopted them.
- Implements: ADR-B (Q6).
- Check: `TestLaggingViewerRejoinsAtTheNextKeyFrame` (fake clock; seen failing with only the byte bound); `TestAJoinIsNotLag` (a viewer joining a 3 s old GOP and reading at once is never reset; seen failing with the time taken from the Hub's push); `TestAViewerThatKeepsUpIsNeverReset` (a static screen's one packet a minute); `TestLagAsksForOneKeyFrame` (two lags 1 s apart: one reset written) if S3 passed. Level 5 (main session, local, both levels): F with the final options equals S2's; a viewer joining mid-drag plays within one GOP, as before.
- Schema: no change.
- Needs: step 1 (S2, S3).
- Not here: the network's own queues (step 7).
- Rests on: a reset never shows a delta frame without its key frame, level 4 (the existing join tests).
- Review: the Opus verifier (Hub concurrency), default effort.
- [ ] Done. Evidence:

### 7. The remote link: a small send queue and a preset for a VPN peer

- Builds:
  - The host keeps few bytes in the kernel: `TCP_NOTSENT_LOWAT` 64 KiB (or `SO_SNDBUF` 128 KiB, per S7) on every TCP connection the peer listener accepts, set by a wrapped `net.Listener` in `adapters/peer` before TLS (which route a connection will serve is unknown at accept; the session's small frames are not affected), on darwin and linux. A slow link then fills the Hub's queue, where step 6 bounds it, not the socket's.
  - Q7's preset: a source started for a peer whose link arrived on a `vpn`-tagged listener (ADR-0109 §4) uses step 1 S8's chosen preset; any other start uses today's options; a source keeps its options for its life, shared by every later viewer (the rule ADR-B states).
- Implements: ADR-B (Q7); R3's remote part.
- Check: `TestPeerListenerSetsNotSentLowat` (reads the option back from an accepted connection); `TestPresetFollowsTheListenersTag` (a fake listener tag; seen failing with the tag ignored); `TestASourceKeepsTheOptionsItStartedWith`. **Level 5 (main session, the peer pair through `linkem` L3, `DROIDYARD_PROBE_LISTENER_TAG=vpn`, the peer starting the source)**: with step 1's before binary, Lag and Staleness as they come; with this step's, A7.
- Schema: no change.
- Needs: steps 5, 6; step 1 (S7, S8).
- Not here: adaptation to measured throughput (Q7); H.265.
- Rests on: the kernel queue is bounded on macOS, level 5 (S7 and this run).
- Review: the Opus verifier (network, process), default effort.
- [ ] Done. Evidence:

### 8. The link measured: TCP before and after, and what a datagram transport would remove

- Builds: no product code. The main session runs the peer pair through `linkem` on L1–L3 (the probe build, the `vpn` tag), with step 1's before binary and with step 7's, 60 s each: T1 for 50 taps, T2 for 20, Lag and Staleness p50/p95/max, rejoins per minute, and per stall the frames and inputs held behind it. From the stall log, an estimate of a datagram transport: inputs and frames after a stall arrive on time, but a lost video datagram costs a key frame (S3's reset time, or the GOP) or a retransmission (one RTT). Results to the research note.
- Implements: Q8; the main session writes ADR-C from it.
- Check: the numbers, quoted; A7 and A8.
- Schema: no change.
- Needs: step 7.
- Not here: no UDP code; real packet loss and a real VPN are not measured (Unverified).
- Rests on: —
- [ ] Done. Evidence:

### 9. Level 5, the documents, the close

- Builds: no code. (a) A1–A7 on the final binary, both levels, local and through the pair; A5's policy case on m2t's stand-in (allowlist edited mid-drag: the view ends, the cancel's colour shows). (b) Documents: `ui-automation.md` §Input primitives (the tunnel, the numbers, ADR-A); `adb-device.md` §Screen viewing (lag bound, options, preset); `http-websocket.md` §`GET /v1/screen` and `peer.md` (input frames on the screen sockets; tunnel input recorded, not called); `capability-api.md` if `device.scroll` needs a line; the register: R1 with the numbers, R2 and R3 narrowed, R4 per S5, R5 per Q7, R6 closed; new rows (one finger per device and its preemption; an older host's view has no input; drags over 5 s recorded compressed; a record dropped under lock contention; `TCP_NOTSENT_LOWAT` on Linux not run; a shared source keeps its starter's options; the transport's later milestone if ADR-C names one); `docs/README.md` §Where the work stands; the index row; the API minor re-checked against main.
- Implements: A1–A9.
- Check: each run's decisive output under `$B/evidence`, quoted in the evidence line.
- Schema: no change.
- Needs: steps 1–8.
- Not here: the owner's trial of the feel, physical devices and the real VPN.
- Rests on: —
- Review: the Opus verifier at the close.
- [ ] Done. Evidence:

## Not in this milestone

- A UDP, QUIC or WebRTC path: Q8 decides at step 8 whether a later milestone builds one.
- H.265 or AV1: Q7; R5 keeps `C4` with step 1's numbers.
- Quality that adapts to a measured throughput, a per-viewer quality, a quality setting: Q7.
- Typing into the canvas, multi-touch, audio, clipboard, waking a sleeping device: R2, R3 as narrowed.
- Agents' coordinate input (C5): the four input capabilities stay human only.

## Design questions

Each is Delegated: the main session applies the recommendation and writes the ADR named in Contract. No question changes the shape of the rule the owner set (authorize at the view's open, a direct path, bounded revocation, every input recorded): the grant gives no power the caller lacked, since every client that can open a view (a WebSocket session holding the UI's token, or a paired peer; never an MCP session, ADR-0010) can already call `device.tap`. Q7's trade of quality for latency on a VPN peer is flagged to the owner, who asked for "not losing quality".

- **Q1. Authorized once per view, and what stays bounded (ADR-A; the owner's rule, 2026-10-21).** Recommended: `device.watchScreen`'s `authorize` (the device policy, ADR-0015 §4; for a peer the lending check and the lease, ADR-0104 §2) is the input's authorization for the view's life; the input then travels the view's own socket, never as a call. The view, input included, ends within 2.5 s when: the policy stops admitting a physical device or a remote id (a new 2 s sweep on the lease ticker, `stopped deviceGone`, since `device.list` no longer lists it either); the device is no longer lent (the host's lease sweep); the peer is unpaired (the listener's 2 s sweep); and, as today, the session, the token's daemon, the device or the source ends. An ended stream accepts nothing. No per-input `adb devices -l`, HTTP round trip or queue on the write key. Rejected: a separate control socket (a second attach and lifetime for what the open socket carries); a per-input policy read (a file read per move for a window the 2 s sweep already bounds); revoking only the input while the video runs on (the view was admitted by the same policy).
- **Q2. The wire, the finger and the lift (ADR-A).** Recommended: binary client frames after auth, big endian: touch `[1][action: 0 down, 1 up, 2 move, 3 cancel][x u16][y u16]` (6 B), key `[2][key: 1 back, 2 home, 3 appSwitch, 4 enter, 5 delete]` (2 B, down and up), scroll `[3][x u16][y u16][h i16][v i16]` (9 B, notches × 256). Coordinates are normalised to the frame (0..65535), so a rotation or a relay changes nothing. The daemon parses, validates and encodes; client bytes never reach the control socket, so scrcpy's other messages (clipboard, power, app start) are unreachable. **One finger per device, owned by the source**: an `Inject` holds it for its whole gesture and is classed by its call's origin (a peer's lent call is a peer holder); a local holder takes it from a peer holder, stream or call, with a cancel, never the reverse (ADR-0094 §5); a gesture whose down was dropped is dropped whole. **A lift the daemon writes is `ACTION_CANCEL`**, never `UP`, so ending a view never completes the click or drop its user began (S4 proves the app treats it so). At most 1,000 messages a second per socket; a queue of 256 with moves coalesced, whose overflow closes only the sending socket. The same wire runs client daemon → host on `/peer/v1/screen`. Rejected: JSON (parsing per move for no reader); scrcpy's bytes passed through (exposes every control message); several fingers (multi-touch, R2); a lift as `UP` (a click at revocation); a lock per message (an `Inject` and a drag interleave on one pointer id).
- **Q3. The three capabilities stay; a result field, not a parameter (ADR-A).** Recommended: `device.tap`, `device.swipe`, `device.pressKey` stay unchanged, for the CLI, scripts and C5; the UI stops calling them. `device.watchScreen` gains no parameter: its result says `control: true` when the socket accepts input. A parameter would make a 2y client's call fail on an older host (unknown parameters are refused), where a missing result field reads as "no input". Rejected: removing the three (a breaking change for no gain; C5 decides agents' coordinate input); an opt-in `control` parameter (the same power, and the version break above).
- **Q4. Live drag and the wheel in; `device.scroll` (ADR-A).** Recommended: the drag streams down, moves and up (ADR-0079 rejected live drag only because each event would be an operation); the wheel uses scrcpy's scroll message (S1) in the same wire; `device.scroll` is added so that a recorded wheel burst names a capability that replays it, as the other gestures do. Rejected: recording a wheel burst as a swipe (an app reads a wheel and a drag differently); the wheel without a capability (its record would name nothing).
- **Q5. Records: one operation per gesture, off the hot path (ADR-A).** Recommended: each gesture becomes one operation of its replaying capability (`completed`, `cancelled` or `failed`), with params valid for it, the view's origin and target, its Activity row and its trace line (which names no parameter, ADR-0043 §2), recorded after the gesture by a goroutine whose `Record` only takes the store's lock, so a stalled log or publisher delays neither input nor record; the one drop is under lock contention past 256 queued gestures, counted and logged. Tunnel input takes no write key, so it does not wait behind an agent's `tapElement` (ADR-0094 §5 already lets the UI act on an occupied device). Rejected: a record per event (a 2 s drag would be about 240 rows); a topic of its own (Activity and the trace would lose the input, which the owner's rule keeps).
- **Q6. A viewer never far behind; the encoder's options (ADR-B, after step 1).** Recommended: a viewer whose oldest queued packet entered its queue over 500 ms ago rejoins at the next key frame (a join's GOP is not lag); with S3's reset-video under 500 ms, the source asks for one key frame (at most one per 2 s); S2's options only on its rule. ADR-0078's rejection of asking for a key frame ("a request a relay could not forward") no longer holds: the request is the host's own, on its own source. Rejected: a larger queue (latency); dropping delta frames (the decoder breaks until a key frame anyway).
- **Q7. Quality for a remote view; H.265 (ADR-B, after step 1; the quality trade flagged to the owner).** Recommended: a source started for a peer that arrived on a `vpn` listener uses S8's preset (the largest of the three whose Lag on L2 stays under 1 s); a LAN peer and the host's own UI keep today's options (ADR-0109 prefers the LAN for latency); a source keeps its options for its life, so a peer that joins a source the host's UI started gets today's options, and the host's UI joining a peer-started source gets the preset. This trades picture quality for latency on a VPN, where the owner asked for both ("speed and response come first, not losing quality"); the main session's brief listed a remote bit rate and size among the cheaper wins. H.265 is not built: the emulator encodes it in software, Chrome decodes it only in hardware, and a lower bit rate already bounds the bandwidth; its quality per bit is a trial on the owner's phone. Rejected: adapting to measured throughput by restarting the server (0.5–2.2 s of black for every viewer, step 1's figures); a per-call quality parameter (one shared source per device).
  - **Owner, 2026-10-21: per remote, not automatic.** Full quality (today's options) by default, local, LAN and VPN alike. A remote the client marks fast (a key on its `remotes` entry, settable by a CLI command, live like every peer key, ADR-0109) asks its host for S8's preset when it opens a view; the host starts its source with it only when it starts the source (a source keeps its options for its life, as above). Rejected by the owner: the automatic preset on every `vpn` peer (it lowers the picture without being asked); always full quality (no way to trade quality for speed on a slow link).
- **Q8. The transport (ADR-C, decided at step 8 on its numbers).** Draft: TCP with steps 6–7 stays if, on L2, T2's p95 is within RTT + F p95 + 150 ms and A7 holds. If not, a later milestone builds QUIC between the two daemons (a reliable stream for input and control, datagrams for video with a key-frame request on loss), on the pinned certificates; a new module dependency and its own ADR. WebRTC is not recommended: the browser never talks to the host (ADR-0097 §3), the VPN already gives reachability, and ICE, SDP, DTLS and SRTP would be a second identity beside the pinned one. Either way the measurement says plainly that the datagram side is an estimate.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| scrcpy 4.1 carries `ACTION_CANCEL` to the app, a cancelled press is no click, and the next down lands | 2–5 (the lift) | step 1 S4; read from 4.1's source |
| scrcpy 4.1's scroll message layout and bounds, and that it lands | 2–4 (wheel) | step 1 S1 |
| `latency`/`priority` codec options help the emulator's encoder; a phone's hardware encoder | 6 | step 1 S2 on emulators; phones a trial row |
| 4.1 has a reset-video message and it brings a key frame quickly | 6 | step 1 S3 |
| `TCP_NOTSENT_LOWAT` bounds the kernel queue on macOS | 7 | step 1 S7, step 7's level 5; Linux not run |
| `linkem`'s stall stands for TCP's behaviour under loss; the datagram side is an estimate | 8 | not settled here: a real lossy link and a UDP path would |
| CDP pointer and wheel events stand for a person's mouse and trackpad (rate, pixel deltas) | 4, 9 | the owner's trial |
| A touch for a stale size after a rotation | 2 | step 1 S5 |
| Physical devices, two real Macs, the company VPN, Linux | all | the owner's trial rows; not run |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | [concurrency] B1 (blocking): a per-message lock lets `Inject` interleave with a held tunnel finger on pointer -2; a peer's finger would refuse the host's UI, against ADR-0094 §5 and ADR-0104 §2 | The finger lives in the source: `Inject` holds it for its gesture, the writer drops tunnel touches meanwhile; a local holder preempts a peer with a cancel, never the reverse (step 2, Q2); `TestInjectAndTunnelNeverInterleave`, `TestHostUIPreemptsAPeerFinger`, step 5's remote twin |
| 1 | revise | [safety] B2 (blocking): the lift on end is an `UP`, so a revocation can complete a click; `TouchActivity` painted up and cancel alike | Every daemon lift is `ACTION_CANCEL` (step 2, Q2, A5); S4 proves the app treats it as no click; `TouchActivity` paints cancel magenta; `TestEveryEndCancelsAHeldFinger` asserts no `UP`; records of a cancelled gesture are `cancelled` (step 3, A6) |
| 1 | revise | [test] B3 (blocking): the before binary at `f9854e1` has no probe | The before binary is step 1's commit with `-tags droidyard_inputprobe` (K1; steps 7, 8) |
| 1 | revise | [test] B4 (blocking): T2 was not causal: a quick tap may never draw the down; before timed from `pointerdown` includes the hold; drag colours ambiguous | T2 per path (after `pointerdown`, before `pointerup`), taps held 150 ms, ±6 levels; drags with moves 4 levels apart, per-frame T2, undrawn moves counted (§Measures, A3, A4) |
| 1 | revise | [test] B5 (blocking): Lag cannot see a frozen view; loopback is tagged `lan`, so the preset never applied | Staleness added and bounded in A7; `DROIDYARD_PROBE_LISTENER_TAG=vpn` in the probe build; A7 requires the peer to start the source and the run to state the options |
| 1 | revise | [bug] B6 (blocking): lag from the Hub push would reset every new viewer at its join | Lag counted from the packet's entry into this viewer's queue; `TestAJoinIsNotLag` (step 6, Q6) |
| 1 | revise | N1 records dropped behind a stalled log; N2 a full queue ended the source for all; N3 step 2 left an unrecorded input path; N4 doccheck failures; N5 shared-source options; N6 `TCP_NOTSENT_LOWAT` first exercised at step 7; N7 the supersede list | N1 `Record` takes only the store's lock, the flush is elsewhere, the remaining drop stated (step 3, Q5); N2 overflow closes only the sender 1008; N3 `control` true only in the probe build until step 3; N4 Status line, `Builds:` fields, ADR-A instead of an unwritten number, the index row the main session's; N5 the rule in Q7 and A7; N6 S7 in step 1; N7 ADR-0077 §4, ADR-0099 §2, §5 added |
| 2 | revise | B2, B3, B5, B6 resolved. B1 partly: every `Inject` was classed local, so a peer's lent `device.swipe` would drop the host UI's touches for up to 5 s (ADR-0104 §2 again); the fate of a dropped down's moves and up unstated | `Inject(ctx, holder, in)` classed by the call's origin; the host's UI preempts a peer `Inject` with a cancel, the call ending with its partial-delivery warning; a gesture whose down was dropped is dropped whole (step 2, Q2); `TestHostUIPreemptsAPeerInject`; the interleave test asserts the drop. Answered in the plan without a third round (`milestone-plan` §Writing one, 7) |
| 2 | revise | B4 partly: before-path taps under the old UI's 300 ms `TAP_MS` are sent with `Hold` 0, so the down's colour may never be drawn | Taps held 400 ms on both paths (§Measures, S6, step 2's level 5) |
| 1 | revise | nits: R1/R5 `Work`; the impossible u16 bound; system bars in `TouchActivity`; how a failed write is recorded; quote the owner on quality | R1 and R5 keep their `Work`; bound removed; bars hidden; `failed` records; Q7 quotes both and is flagged to the owner |

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.

- 2026-10-21, Q7 (the owner's answer): the remote preset follows a per-remote fast mark on the client, not the listener's `vpn` tag. So step 1 drops `DROIDYARD_PROBE_LISTENER_TAG` (a loopback pair takes the preset by marking its remote fast), and step 7's `TestPresetFollowsTheListenersTag` becomes a test that the preset follows the remote's mark (seen failing with the mark ignored), with the mark's key and command in ADR-B.
- 2026-10-21, step 1: scrcpy v4.1 has reset-video (`TYPE_RESET_VIDEO` 17, no payload), so S3 runs; a scroll is 21 bytes (axes as notches/16 × 0x8000); a cancel is the touch message with action 3, and only `ACTION_UP` marks the server's pointer up, which S4 must watch (the next down after a cancel). `TouchActivity`'s scroll colour counts the notches since the activity started. A first `notsentprobe` run on macOS read `TCP_NOTSENT_LOWAT` back as set but saw no change in arrival delay (p95 3.8 s both ways, one 8 s run each): S7's `SO_SNDBUF` branch is likely.
