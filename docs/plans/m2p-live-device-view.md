# Plan: Milestone 2p — C3, a live device view

- Status: Approved
- Approved: 2026-10-17, Delegated (the main session under the owner's standing instruction to apply the recommended option): Q1–Q10 as recommended; the owner may revert any.
- Date: 2026-10-17
- Milestone: "2p: C3, a live device view — the device screen streamed to the web UI, with input" (owner's choice, 2026-10-17; [`beyond.md`](../vision/beyond.md) §C3; `docs/README.md` §Where the work stands says "next, the owner's choice").
- Phase: none. `mvp.md` §Phase 2 — Minimal web UI (closes the MVP) lists the live stream as a stretch, and §Out of scope says "no stills (owner, 2026-10-09)". The MVP is done (2h).
- Contract: ADR-0076 (Q10), ADR-0077 (Q1, Q2), ADR-0078 (Q3, Q4, Q7, Q8), ADR-0079 (Q5, Q6), ADR-0080 (Q9); Delegated. ADR-0076 was written at approval; the other four from step 1's note, before step 2 (as ADR-0035 from m1n step 2). Measured numbers go to the research note and the area documents, not into an ADR.
  - **ADR-0077:** the live view streams H.264 from the vendored, pinned scrcpy server behind the `screen.Source` seam.
    - It replaces the no-stream decision in `adb-device.md` §Screen viewing: on demand, not streamed.
    - It covers the licence, the digest and pushing at each start.
    - It names its departure from ADR-0029's "our own small code".
  - **ADR-0078:** `device.watchScreen`, the `device.screen` topic and `GET /v1/screen`, with its frame header and close codes.
    - One source per device, with config, GOP and rejoin.
    - Lifetimes.
    - Decoding by WebCodecs only.
    - `DEVICE_STREAM_FAILED`.
  - **ADR-0079:** UI input through the `human` capabilities `device.tap`, `device.swipe` and `device.pressKey`.
    - Normalised coordinates against the stream's frame size, delivered by the source's control channel.
    - `DEVICE_NO_STREAM`.
    - The measured latency cost.
    - No MCP exposure.
  - **ADR-0076:** `operation.state` and `ActiveOperation` carry the call's `target`. It supersedes ADR-0043 §2 in part.
  - **ADR-0080:** `FLAG_SECURE` is detected from the focused window and reported on `device.screenshot` and `device.screen`.
  - Also: ADR-0010, ADR-0012, ADR-0015 §2, §4, §6, ADR-0020, ADR-0021 §Pid records, ADR-0028, ADR-0029, ADR-0036, ADR-0041, ADR-0043, ADR-0048, ADR-0055, ADR-0060 §2, ADR-0068; `adb-device.md` §Transport to the device, §Lifecycle and concurrency; `ui-automation.md` §Input primitives; `capability-api.md` §Events and subscriptions; `http-websocket.md` §Security baseline.
- Register: the main session's commit that adds this plan sets `Work` = `2p` on R1–R5 (Scope).

**The owner should see:**
- A **Live view** tab in the right panel (the slot `panels.ts` reserved) plays the toolbar's device as H.264 video decoded by the browser (WebCodecs). There are no stills: a browser without WebCodecs H.264 support gets a message.
- A click taps, a press-and-hold long-presses, a drag swipes once you let go, and Back, Home and Recents are buttons. A text box types through `device.inputText`, so Unicode works. Each of these is a capability call: it shows in Activity, is checked by the device policy and writes its trace line.
- The device runs **scrcpy's server** (Apache-2.0), pinned and embedded in `droidyard` like the reader dex. `droidyard version --licenses` prints its licence.
- Agents get nothing new over MCP. `device.screenshot` warns when the focused window is `FLAG_SECURE`.
- One minor API version: four capabilities, one topic, one field on the operation view, two error codes, one HTTP endpoint.

## Constraints from the owner's intent (C4, C5)

Neither C4 nor C5 is in scope. The plan keeps both open as follows:

1. **The stream is relayable as is.** The seam is `screen.Source` (step 2).
   - Its `Packet{Key, Config, PTS, Data}` carry the device's own H.264 Annex B access units. The only change, made by the Hub, is that the current config (SPS/PPS) is prepended, unchanged, to the keyframe that follows it, and to the keyframe that starts a join or a rejoin.
   - Size and codec are read from the SPS inside the packets. Rotation is the device's own: it re-encodes upright, with a new SPS.
   - Input is a neutral `screen.Input` (tap, swipe or key, in normalised coordinates with timing). The scrcpy source encodes it. A relayed source would forward it, or rather the capability call it came from.
   - So the bytes on `/v1/screen` are the bytes the device encoded (A8), and the UI cannot tell a local source from a remote one.
2. **Every input is a capability call.** `device.tap`, `device.swipe`, `device.pressKey` (new) and `device.inputText` (existing) each go through `Start`:
   - schema validation;
   - `authorize`, which is one `adb devices -l` plus the device policy (ADR-0015 §2, §4);
   - the device's write key;
   - the operation store, the trace line and `operation.state`.

   That path is where a later recorder hooks in. `Source.Inject` is reachable only from those handlers:
   - the HTTP adapter gets a read-only `Viewer`;
   - `/v1/screen` closes on any client frame after `auth` (step 4, with a mutation).

   The latency this costs is stated in Q5 and measured in steps 1 and 7.

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| The screen streamed to the UI, H.264, no stills (C3; owner 2026-10-09) | yes | steps 1–5 |
| Input from the view: tap, long press, swipe, Back/Home/Recents, text | yes | steps 6, 7 |
| Input as audited, policy-checked capability calls (constraint 2) | yes | steps 4, 6, 7 |
| A source seam relayable without re-encoding (constraint 1) | yes | steps 2, 4 |
| Several tabs on one device; device, emulator, session and daemon lifetimes | yes | steps 2–5 |
| R1 "A `FLAG_SECURE` window screenshots black and nothing says so" | yes, closed | step 8 |
| R2 "The operation view names no device …" | yes, closed | step 9 |
| R3 "Live screen streaming to the UI …" | yes, closed | steps 5, 7 |
| R4 "Event topics with no producer …" | part | `device.screen` closes (steps 4, 8); step 10 splits the row, and the rest stays open |
| R5 "The toolbar names this tab's selected module for any UI build …" | yes, closed | step 9 (independent of steps 2–8; the owner may move it to a light milestone of its own) |
| Agents watching or driving the view over MCP | no | Q6 |
| C4 (a remote device), C5 (recording) | no | `beyond.md`; the constraints above |
| Audio, clipboard sync, wheel scroll, typing into the canvas, live drag, multi-touch | no | Not in this milestone |
| Physical devices, Linux | no | the same code path, not run (Unverified) |

## Starting point

- `main` at `118a38d`. API `0.26.0`. ADRs up to 0075.
- **Screens.** `device.screenshot` (`internal/domain/device/screenshot.go`) runs `exec-out screencap -p` and stores an ephemeral, untrusted artifact. Its `warnings` are adb's and untagged, and its nextStep already guesses "secured". The UI shows no stills (m2b Q2).
- **Input.** `device.tapElement` and `device.inputText` (`tap.go`, `input.go`) act through the reader's `tap`, `keys` and `settext` (ADR-0035; `ACTING_VERBS` in `reader/src/DroidyardReader.java`), with `input` as the fallback. There is no coordinate tap, swipe or key capability; `ui-automation.md` §Input primitives reserves them, with coordinates "normalised 0..1".
- **Streams.** `device.watchLogs` (`watchlogs.go`) is the precedent:
  - streaming and session-owned, ring-less through `PublishTo`, its payloads published via the descriptor's `Events` (ADR-0041 §4);
  - its children are recorded `device-stream` processes, killed by pid;
  - `streamSet.add` refuses once `StopAll` (called from `AfterOps`) has begun.
- `ADB.authorize` (`target.go`) runs `adb devices -l` on every device call.
- `internal/domain/device/reader` embeds `reader.dex`, pins `SHA256` in a test and checks it before each push (ADR-0029).
- **Wire.** `adapters/httpapi` serves `/v1/call`, `/v1/artifacts/{id}` and `/v1/ws`. The last is JSON text only (a client binary frame closes it with 1003, ADR-0028). The CSP is `default-src 'self'; frame-ancestors 'none'`.
- **Release.** `install.sh` extracts only `droidyard` from the archive (ADR-0068; `TestArchiveMembers`).
- **Operation view.** `capability.ActiveOperation` (`envelope.go`) and `operation.state` carry no target (ADR-0043 §2; ADR-0048's Alternatives also rejected a target summary).
- **Web.** `web/src/lib/devices/` (Devices, Tree, Act, AppLogs, `mirror.ts`). `uitree.ts` `UI_TRIGGERS` is also used by `applogs.ts`. `toolbar/status.ts` has the `client === 'ui'` rule (R5). `ui/panels.ts` has `liveview` disabled.
- **Host.** Homebrew's scrcpy 4.1 is installed: `/opt/homebrew/share/scrcpy/scrcpy-server`, 733,706 B, with an Apache-2.0 `LICENSE` (read 2026-10-17).
  - System images: android-31, 35, 36, 36.1 and 37.1.
  - The scratch AVD `droidyard_m2a` (API 31; m2h K5).
- **Baseline, 2026-10-17 at `118a38d`:** `go vet ./...` clean; `go test ./...` ok in every package. This file fails `internal/doccheck` `TestPlansIndexMatchesFiles` until the main session adds its index row.

## Acceptance

- [ ] A1. With the Live view tab open, the toolbar's emulator plays within 3 s. During an animation the panel decodes at least 90% of the packets it received (`data-frames` against `data-packets`). Source: C3; Q1, Q3. Proven by: steps 5, 10.
- [ ] A2. Tap, long press, swipe, Back, Home, Recents and text from the view act on the device. Each is an operation in Activity with client `ui` and its device, and has a trace line. A physical device the policy refuses gets `DEVICE_NOT_ALLOWED` with nothing sent. An MCP session gets `REQUEST_NOT_ALLOWED_HERE`. Source: constraint 2; Q5. Proven by: steps 6, 7, 10.
- [ ] A3. The input latency is measured and stated: the capability path's `durationMs` p50/p95 and click-to-next-frame p50/p95, beside step 1's bare control write and `adb devices -l`. Source: constraint 2. Proven by: steps 1, 7.
- [ ] A4. Two tabs on one device run one server. Closing one leaves the other playing, and a tab that joins or restarts later plays from the next packet it gets. Closing both stops the server and removes its forward within 2 s. Source: Q7, Q8. Proven by: steps 2, 3, 5.
- [ ] A5. Stopping the emulator ends the view with `deviceGone`. `droidyard stop` leaves no server and no forward. A daemon killed outright leaves a record that the next start reaps. Source: Q7; ADR-0021 §Pid records. Proven by: steps 3, 4, 10.
- [ ] A6. With a `FLAG_SECURE` window focused, `device.screenshot` warns and the view shows a banner; otherwise neither does. Source: R1; Q9. Proven by: steps 8, 10.
- [ ] A7. The operation view names its target. The UI tree and app logs re-read only for their device. The mirror names the device being stopped and the AVD being booted. The toolbar names the module of any client's `build.assemble`. Source: R2, R5; Q10. Proven by: step 9.
- [ ] A8. A viewer's bytes on `/v1/screen` are the source's packets, byte for byte. No client frame on that socket reaches the device. Source: constraints 1, 2. Proven by: steps 2, 4.
- [ ] A9. One minor version (0.27.0), with the schema diff the steps declare. The MCP tool list is byte-identical. Source: `capability-api.md` §Discovery and versioning; ADR-0036. Proven by: steps 4, 6, 8, 9.

## Run rules (steps 1, 3, 5, 7, 8, 10: the main session only)

- **K1.** An isolated daemon by the `verify-daemon` recipe (state and config under a fresh `/tmp/dyp.*`), built at the step's commit after `pnpm -C web build`. Fixtures and evidence go under `B=~/Library/Caches/droidyard-dev/m2p`. No owner repository is used.
- **K2.** The scratch AVD `droidyard_m2a` (API 31) as in m2h K5:
  - its `ANDROID_AVD_HOME`, the m2b guard as `ADB`, and `ANDROID_ADB_SERVER_PORT=5199`, ended with `adb -P 5199 kill-server`; 5037 is never touched;
  - a second scratch AVD on android-36 or 37.1 may be created there if it cold-boots within 10 minutes;
  - the owner's Pixel_9a (API 36.1) is used only with the owner's yes for the run, and only to stream: no input, no install;
  - the owner's Samsung is not used;
  - formapp goes only on scratch AVDs.
- **K3.** The owner's daemon and the 5037 server keep their pid and start time. Nothing is killed by name. Headless **Google Chrome** (the branded build, which carries the H.264 decoder) runs with its own profile over CDP. The implementer never runs a device phase.

## Steps

### 1. Spike: the server, its wire, its cost, the browser's decoder

- Builds:
  - **Provenance (main session, before the brief).** The server file comes from the newest scrcpy release (4.1 or later), as its GitHub asset. Its SHA-256 is compared with the release's published checksum, and with Homebrew's copy if the version is the same.
  - **The vendored server (implementer).** `internal/domain/device/screen/scrcpyserver`, on the `reader` package's pattern:
    - the embedded `scrcpy-server`;
    - `LICENSE`, scrcpy's Apache-2.0 text, plus scrcpy's `NOTICE` if the tag has one;
    - `Version` and `SHA256`;
    - `Server() []byte`, which returns a copy, and `Digest()`.

    `droidyard version --licenses` (`cmd/droidyard/main.go`) prints the embedded licence. Bare `droidyard version` stays one line: `install.sh` parses it, and `TestVersionLine` guards it. The archive and ADR-0068 are untouched.
  - **The spike tooling (implementer).** `internal/domain/device/screen/spike_test.go`, `TestSpikeScrcpy`. It is skipped unless `DROIDYARD_SPIKE_SERIAL` and `ADB` are set. Driven by flags, it:
    - pushes the jar and forwards the port;
    - starts the server with the given options;
    - records the video socket's bytes to a file, with a host timestamp per read;
    - writes given control messages (touch down/up, keycode) as encoded from the tag's source, and stamps each write;
    - prints elapsed times.

    It shares no code with step 2.
  - **formapp (implementer).** A "Secure" toggle that sets or clears `FLAG_SECURE` on its window, built by its `build.sh`.
- **The run (main session, K1–K3).** The results go to `docs/research/spike-screen-stream-2026-10.md`, and the fixtures to `internal/domain/device/screen/testdata/` (each at most 2 MiB). The note answers each point with what was observed.
  - (a) **Wire facts**, read from the tag's `server/src/main/java/com/genymobile/scrcpy/` and confirmed by a recording:
    - the start command and the options used (`tunnel_forward`, `video`, `audio=false`, `control`, h264, `max_size=1280`, `video_bit_rate=4000000`, `max_fps=60`, `video_codec_options` with an `i-frame-interval` of 2 s, clipboard sync off, `cleanup`);
    - the video socket's preamble and packet header;
    - **whether config packets come only when the encoder starts or restarts (start, rotation), never with later keyframes**;
    - how a new size is signalled;
    - the touch and keycode messages, as golden bytes that landed;
    - whether the server deletes its jar;
    - what the control socket sends with clipboard sync off.
  - (b) **Fixtures:**
    - `scrcpy-api31-portrait.bin`: from connect, during a swipe animation, spanning at least 3 keyframes;
    - `scrcpy-api31-rotate.bin`: across `user_rotation` 0 → 1 with `accelerometer_rotation=0`;
    - the secure check's outputs, on and off, for each API level run.
  - (c) **Rates**, for each API level run: start to first keyframe (push included), packets/s and bytes/s during the animation, and the GOP length actually seen.
  - (d) **Input.** Whether a control-socket tap on formapp's button lands (a tree read shows its effect). Then, each p50/p95 of 20:
    - control write to the next bytes on the video socket;
    - the reader's `tap` round trip over its forward;
    - `adb devices -l`.
  - (e) **Lifetime.** Once the host `adb shell` client is killed and the sockets close: is the server gone from the device's `ps -A` within 2 s? Is the jar gone? Does the forward stay until it is removed?
  - (f) **`FLAG_SECURE`.** With formapp's toggle on: what the stream shows and what `screencap` gives (black or not; the whole screen or the window); the cheapest command that names **the focused window's** flag, with its output and its time p50/p95 of 10.
  - (g) **The browser.** On a scratch HTML page, not committed, in headless Google Chrome: `VideoDecoder.isConfigSupported` for fixture (a)'s codec string, and the frames a decode of fixture (a) outputs against its packet count.
- Implements: Q1, Q2; the facts A3, Q8 and Q9 rest on.
- Check:
  - Unit:
    - `TestServerDigest` pins `SHA256` and `Version` (seen failing with one byte changed);
    - `TestVersionLicenses`: the output holds "Apache License" and "Version 2.0" (seen failing with the embed emptied);
    - `TestSpikeScrcpy` skips without its variables.
  - **Stop rules.** Each, if it fails, is a Change before step 2:
    - (S1) the server streams on API 31, with a keyframe within 5 s of start;
    - (S2) headless Google Chrome decodes at least 90% of fixture (a)'s packets;
    - (S3) a control-socket tap lands;
    - (S4) the server exits within 2 s of its client and sockets going.
  - The other points are recorded with their numbers.
- Schema: no change.
- Needs: real emulators, Google Chrome, and the SDK's build tools (formapp).
- Not here: no daemon code uses the server. ADR-0077 is written from the note.
- Rests on: S1–S4, at level 5.
- Review: the main session's review of the vendoring (digest, licence, provenance) and of the note.
- [x] Done. Evidence: `docs/research/spike-screen-stream-2026-10.md`. S1: first key frame 0.54–0.74 s after start on API 31, 1.7–2.2 s on API 36. S2: headless Google Chrome 155 decoded 187/187 packets of fixture (a), 73/73 of (b), 67/67 joining at key frame 3. S3: 20/20 control taps landed on API 31 and API 36 ("Likes: 20"), Back landed. S4: server gone 1.04 s (API 31), 1.26 s (API 36) after its client. `TestServerDigest` seen failing with one byte changed, `TestVersionLicenses` with the embed emptied (implementer's run). Fixtures in `internal/domain/device/screen/testdata/`. Evidence under `~/Library/Caches/droidyard-dev/m2p/evidence`.

### 2. Packets, input and fan-out (no device)

- Builds: `internal/domain/device/screen`.
  - **`Packet`** `{Key, Config bool; PTS int64; Data []byte}`.
  - **`Demux(r, yield)`**, for step 1's video socket format:
    - it reads the preamble and discards the device name (device text, never published);
    - it caps a packet at 4 MiB (above that, an error);
    - it yields each packet as the device sent it. A config packet is yielded on its own (`Config: true, Key: false`).

    All prepending happens in one place, the Hub.
  - **`ParseSPS(nal) (SPS{Width, Height int; Codec string}, error)`**, with frame cropping and `avc1.PPCCLL`. It is bounds-checked: an error, never a panic.
  - **`Input`**, a neutral value: `Tap{X, Y float64; Hold time.Duration}`, `Swipe{Points []Point{X, Y, T}}`, or `Key{Name}`, with coordinates normalised 0..1.
  - **`EncodeControl(in Input, size) [][]byte` and its schedule.** These are scrcpy's control messages for one `Input`, against a frame size. Step 1's golden bytes are its test vectors.
  - **`Hub`**, for one producer and N viewers:
    - it keeps the **current config** (the last config packet) apart from the **GOP**, which is the latest keyframe plus the packets after it;
    - the GOP holds at most 16 MiB of device bytes, the config not counted; past that it is emptied until the next keyframe;
    - live, a viewer gets a config packet merged into the keyframe that follows it, never on its own;
    - `Join()` returns a viewer whose first packet is the GOP's keyframe with the current config prepended exactly once (`Config: true`), followed by the rest of the GOP; with the GOP empty (before the first keyframe, or after the bound emptied it), the viewer waits for the next keyframe, which it gets with the config;
    - each viewer's queue holds at most 20 MiB (the GOP bound plus one packet cap plus the config), so a join never overflows it;
    - when a queue overflows, the viewer is emptied and rejoined at the **next** keyframe, never the current GOP, so a stalled viewer cannot loop; that keyframe carries the current config (`Config: true`); each rejoin is counted;
    - `Size()` follows the newest SPS; `Close(reason)` ends every viewer.
  - **`Source`**, the seam: `Join() Viewer`, `Size()`, `Inject(ctx, Input) error`, `Done()`, `Reason()`. `Viewer` has `Next(ctx) (Packet, error)` and `Close()`, nothing else.
  - **`testdata/frame-golden.bin`**: one `/v1/screen` binary message (step 4's header and a short payload), read by step 4's Go test and step 5's Vitest, so that the two encoders of the header are checked against one file.
- Implements: Q4 (the packet form), Q8; constraint 1.
- Check:
  - **Demux on the fixtures:**
    - `TestDemuxRecordedStream` on fixture (a): the packet and keyframe counts step 1 recorded; the first packet is a config, then a keyframe; PTS never decreases; the SPS size equals the codec header's.
    - `TestDemuxRotation` on fixture (b): a new config with the swapped size, before a keyframe.
    - `TestPacketsAreTheDevicesBytes`: `Data` equals the fixture's payloads in order, byte for byte (seen failing with a re-chunking).
    - `TestDemuxBounds`: an oversize header is an error, and a cut stream is `io.ErrUnexpectedEOF`.
  - **Input encoding.** `TestEncodeControl`:
    - `Tap{0.5, 0.25}` on 720×1280 equals step 1's golden down/up bytes at (360, 320);
    - on 1280×720 it maps to (640, 180);
    - a swipe's schedule follows `T`;
    - each key's bytes are right.
  - **The Hub:**
    - `TestHubJoinAfterSecondKeyframe`: the viewer's first packet is keyframe 2 carrying the stream's config (seen failing with the config taken only from a config keyframe);
    - `TestHubJoinAtFirstKeyframe`: the config appears exactly once (seen failing with a double prepend);
    - `TestHubSlowViewerRejoinsOnce`: a viewer that never reads rejoins at most once per keyframe and never gets a packet that precedes a keyframe; with the gap spanning a config change, the rejoin keyframe carries the new config (seen failing with a rejoin from the current GOP, and with the config left off);
    - `TestHubGOPBound`: past 16 MiB the GOP is empty, a join then waits, and its first packet is the next keyframe with the config;
    - `-race` with 8 viewers joining and leaving.
  - **Fuzz.** `FuzzDemux` and `FuzzParseSPS`, seeded from the fixtures, 30 s each, with no panic and no allocation over the caps.
- Schema: no change.
- Needs: step 1's fixtures and facts.
- Not here: adb, the device, the capability.
- Rests on: no device byte sequence makes the daemon panic or exceed its caps, at level 3 (fuzz).
- Review: the Opus verifier (untrusted bytes parsed in the daemon; concurrency), effort `high`.
- [x] Done. Evidence: `go test -race` ok; `FuzzDemux` 4.1M execs and `FuzzParseSPS` 7.2M in 30 s each, no crasher (implementer), 2.0M and 1.2M in 20 s (verifier); the allocation bound on a big claim over a short stream is `TestDemuxBounds` (TotalAlloc ≤ 1 MiB), not the fuzz. Fixture counts 188 packets/4 key frames and 75/2 configs/3 key frames, SPS 590×1280 `avc1.42c029`, golden touch and key bytes equal. Seen failing: re-chunking, config only from a config key frame, double prepend, rejoin from the current GOP (now also at `hub_test.go:273`, the verifier's m1), rejoin without config, GOP bound, packet-count bound, crop unit, rounding. Opus verifier: proceed, 0 blocking; [test] 1 and [doc]/[plan]/[nit] 5, all taken.

### 3. The server on the device: one per device, its lifetime

- Builds: `internal/domain/device`, `screenSources` on `*ADB`.
  - **Acquire.** `acquire(ctx, t) (*screenSource, release, error)` runs one start per serial, and later callers wait on it. A start in progress whose start fails fails every waiter. Acquire is refused once `StopAll` has begun, as `streamSet.add` is.
  - **Start.** In order:
    1. check the digest;
    2. `push` to `/data/local/tmp/droidyard-scrcpy-<first 12 hex>.jar`;
    3. pick a random scid and `forward tcp:0 localabstract:scrcpy_<scid>`;
    4. spawn the recorded `device-stream` child `adb -s <serial> shell CLASSPATH=<jar> app_process / com.genymobile.scrcpy.Server <Version> <step 1's options>`;
    5. connect the video socket, then the control socket, within 5 s;
    6. run `Demux` into a `Hub`.
  - **Control.** `Inject` writes `EncodeControl`'s messages on its schedule, serialised. Input on the control socket is read and discarded, within a bound.
  - **Ends:**
    - The last `release` closes the sockets, kills the child by pid and runs `forward --remove tcp:<port>`. It returns only once the child is reaped and its record removed.
    - The child exiting ends the source with `deviceGone` if `device.list` no longer has the serial, otherwise `exited`.
    - A demux error ends it with `failed`.
    - `StopAll(ctx)` is called from the daemon's `AfterOps`, beside the log streams' (ADR-0041 §6).
  - **Errors.** `DEVICE_STREAM_FAILED` (beside `ErrScreenshotFailed`, with its meaning) is a push, start, connect or protocol failure at start. Its cause names the phase and the exit status, never device output.
- Implements: Q7; ADR-0021 §Pid records; `adb-device.md` §Transport to the device.
- Check:
  - **Fakes.** New fake adb modes print the port of a test listener serving fixture (a), whose control side records its bytes:
    - `TestScreenSourceStreams`: packets reach a viewer; the fake's log shows push, then forward, then shell, the digest-named path and `Version`.
    - `TestScreenSourceSharedPerDevice`: two acquires start one shell; releasing one keeps it; releasing both kills it, logs `forward --remove tcp:<port>` and removes the record (seen failing with the refcount removed).
    - `TestScreenSourceInject`: the listener receives `EncodeControl`'s bytes.
    - `TestScreenSourceDeviceGone`, `TestScreenSourceStopAllWaits`, and `TestScreenSourceRefusedAfterStopAll`.
    - `TestScreenSourceCancelledAcquire`: a cancel during a start, and a waiter behind a start that fails, each leave no record, no forward and no child.
    - `TestScreenSourceStartFailures`: a failed push, and no connect within the bound, give `DEVICE_STREAM_FAILED` naming the phase.
    - A `-race` storm of acquire and release.
  - **Real (main session, K2).** `DROIDYARD_SCREEN_SERIAL=<scratch> go test ./internal/domain/device -run TestRealScreenSource -v`, skipped without the variable:
    - at least one keyframe and at least 20 packets within 5 s while the run swipes;
    - after release, the device's `ps -A` shows no server within 2 s, and `adb forward --list` lacks the port.
- Schema: no change (no descriptor yet).
- Needs: step 2.
- Not here:
  - A daemon killed outright leaves its forward until adb restarts or the device goes: it cannot be told apart from a user's own scrcpy forward. This becomes a register row.
  - Between `forward` and the connects, another local user could connect to the loopback port first; the same class as the reader's forward (ADR-0077 Consequences; a register row).
- Rests on: every server ends with its last viewer, its device or the daemon, and leaves no forward. Level 5 (the real check) and level 4 (fakes, `-race`).
- Review: the Opus verifier (process lifetime, concurrency), effort `high`.
- [x] Done. Evidence: `TestRealScreenSource` on the API 31 scratch AVD, 5 of 5 (before and after the fix rounds): 590×1280, 20 packets and a key frame within 5 s; after release no `com.genymobile` process and no `scrcpy_` forward (`evidence/step3-real-emulator-5580.txt`). `go test -race -run TestScreenSource` ok (121 s). Seen failing: refcount, StopAll wait, refusal after StopAll, ready before teardown, a cancelled acquire's ref, a cancel ending a shared start, digest, the inject token, the dummy byte consumed, the forward lookup by listing, deviceGone never chosen, the cut-stream device check, the release/acquire gap (`TestScreenSourceAcquireDuringLastRelease`), a start beside an ending one (`TestScreenSourceNewStartWaitsForTeardown`). Opus verifier: round 1 revise ([bug] 1 blocking: the last release and a new acquire raced; [contract] 1, [test] 1, nits 5), round 2 revise ([test] 1 blocking: a timing-dependent read; [contract] 1), both fixed (the test asserts the push order only; `adbFail` keys on the source's context).

### 4. `device.watchScreen`, the `device.screen` topic and `GET /v1/screen`

- Builds:
  - **`device.watchScreen`.** Kind command, global, streaming, audience `all`, not long-running, since `0.27.0`, annotations as `device.watchLogs`.
    - Params `{device}`; result `{subscriptionId, device, streamId, width, height}`.
    - Errors: `PreambleErrors`, `DEVICE_STREAM_FAILED`, `SESSION_REQUIRED`, `REQUEST_INVALID_PARAMS`.
    - The policy is checked at start.
  - **Events**, topic `device.screen` (descriptor `Events`, ring-less `PublishTo`, not subscribable): `stopped {device, reason: deviceGone | exited | failed | detached, cause?}`. The subscription's end hook releases its viewer.
  - **`streamId`.** 128 random bits in base64url. A daemon-wide table maps the id to its viewer until the subscription ends, the stream socket closes, or 10 s pass without an attach. Each of these releases the viewer, publishes `stopped` (`detached` for the last two), and makes the id unknown.
  - **`adapters/httpapi` `GET /v1/screen`:**
    - the security baseline applies: Host allowlist, mandatory `Origin`, no CORS, a 4 KiB read limit;
    - the first text frame must arrive within 5 s: `{"auth": <token>, "stream": <id>}`, with a constant-time compare;
    - close codes: 4401 for a bad token, 4404 for an unknown id, 4409 for a second attach;
    - then binary messages `[flags u8: 1 key, 2 config][pts u64 BE][Annex B]`, each with a write deadline, so a slow socket only overflows its own viewer;
    - any client frame after `auth` closes with 1003; the stream's end closes with 4410;
    - the adapter sees only `Attach(id) (Viewer, error)`.
  - API `0.27.0`, `go test ./internal/schema -update`, and the regenerated web client.
- Implements: Q4, Q7; ADR-0010; constraints 1 and 2 on the wire.
- Check:
  - `TestWatchScreenEndToEnd` (`cmd/droidyard`, a daemon with the fake adb and the fixture listener):
    - a WS session calls `device.watchScreen`, and a socket attached with the id receives messages whose payloads equal the fixture's packets byte for byte (seen failing with a re-chunking);
    - `frame-golden.bin` decodes with the Go header code;
    - closing the stream socket publishes `stopped detached`, and a reattach gets 4404;
    - an id never attached is released after 10 s;
    - closing the JSON session closes the socket and stops the server;
    - the fake device disappearing gives `stopped deviceGone`, then 4410.
  - `TestScreenEndpointFence`:
    - no `Origin` gives 403, a wrong token 4401, an unknown id 4404, a second attach 4409;
    - a binary frame after `auth` gives 1003, and the control listener has received zero bytes (seen failing with client frames handed to `Inject`).
  - `POST /v1/call device.watchScreen` gives `SESSION_REQUIRED`.
  - The MCP tool list is byte-identical (`TestToolListModelFacingBytes`).
- Schema: version 0.27.0; `device.watchScreen` with its params, result, errors and `events` (`device.screen`: `stopped`); `DEVICE_STREAM_FAILED` in the codes list.
- Needs: step 3.
- Not here: the UI; the `secure` state (step 8).
- Rests on: only a holder of the daemon's token and the session's id can attach, and nothing it sends reaches the device. Level 4.
- Review: the Opus verifier (contract, security), effort `high`.
- [x] Done. Evidence: `TestWatchScreenEndToEnd` (fixture packets byte for byte on `/v1/screen`, `frame-golden.bin`, `stopped detached` then 4404, unattached id released after 10 s, session close → 4410 and no process, forward or record, device gone → `stopped deviceGone` then 4410), `TestScreenEndpointFence` (403 without Origin, 4401, 4404, 4409, 1009 over 4 KiB before and after auth, 1003 on a client frame with 0 bytes at the control listener), `TestWatchScreenStreamIDIsNotRetained`, `SESSION_REQUIRED` over `POST`, the MCP tool list byte-identical; schema 0.27.0 with exactly the step's entry. Seen failing: re-chunking, client frames to `Inject`, the id left in the table, the claim not recorded, release twice, the write deadline, flag bits, the fence rule, the token compared after the id (verifier's mutation), the Slim removed, the after-auth read limit. Opus verifier: proceed, 0 blocking; [safety] 1 (the stored `streamId`) and [test] 1 fixed, [doc] 1 deferred to the close (the superseding ADR), nits 5 noted.

### 5. The Live view panel: decode and play (the first end to end)

- Builds: Svelte 5 (load `svelte-core-bestpractices` and `svelte-code-writer` first). In `web/src/lib/screen/`:
  - `h264.ts`: the SPS in an Annex B unit, and the codec string.
  - `stream.ts`: the socket, auth, and the header.
  - `decoder.ts`: WebCodecs `VideoDecoder` behind an interface. It takes Annex B with no `description` and sets `optimizeForLatency`. It reconfigures on a `config` message. A decoder error closes the socket and watches again once, which is a rejoin with config; a second error before a frame decodes gives `stopped(failed)` with Restart.
  - `liveview.ts`, a pure model:
    - states `idle`, `starting`, `live`, `stopped(reason)` and `unsupported`;
    - it watches the toolbar's device only while the tab is current and its region open, and unsubscribes otherwise;
    - a device change re-watches;
    - `detached` re-watches once;
    - any other `stopped` shows its reason with Restart.
  - `LiveView.svelte`: a canvas letterboxed to the frame, drawing the newest frame on each animation frame. It exposes `data-frames` (decoded) and `data-packets` (received).
  - `panels.ts`: `liveview` enabled.
- Implements: Q3, Q7 (the UI's part); C3's view.
- Check:
  - Vitest:
    - the codec string from step 1's SPS bytes;
    - `frame-golden.bin` parses;
    - the model: hidden unsubscribes (seen failing without the visibility test); a device change re-watches; `detached` re-watches once; a decoder error re-watches once, and a second gives `stopped(failed)` (seen failing with an unbounded re-watch); `deviceGone` shows Restart; no `VideoDecoder` gives `unsupported`.
  - `pnpm -C web run verify`, and the autofixer gate on the new `.svelte` files.
  - **Level 5 (main session, K1–K3):**
    - on the scratch AVD, the first frame within 3 s;
    - during a swipe animation, `data-frames` at least 90% of `data-packets`;
    - stopping the emulator gives `deviceGone`;
    - two tabs play while the device's `ps -A` shows one server; a third opened 10 s later plays; closing all of them stops the server within 2 s (A4).
- Schema: no change.
- Needs: step 4.
- Not here: input (step 7); the look, which is the owner's.
- Rests on: —
- Review: the main session's diff review and its level-5 run.
- [x] Done. Evidence: level 5 (K1–K3, API 31 scratch AVD, headless Google Chrome 155, `evidence/lv5/`): first frame 0.78–1.15 s after the device is chosen; during a 5 s Settings scroll `data-frames` = `data-packets` (197/197, 129/129); three windows on one device, one forward and one server, a window closed leaves the others playing (237 → 283 frames), all closed → server and forward gone in 1.29–1.30 s; the emulator stopped while viewing → "The device went away." after 1.7 s, with Restart; `droidyard stop` leaves 0 orphans. Vitest 26 new tests; seen failing: hidden not unsubscribing, an unbounded re-watch. `pnpm -C web run verify` ok, autofixer gate no issues.

### 6. Input capabilities: `device.tap`, `device.swipe`, `device.pressKey`

- Builds: `internal/domain/device/screeninput.go`. Three descriptors share these properties:
  - kind command, global, audience `human` (ADR-0060 §2), `Resource` `writeKey(device)`;
  - `authorize` = `adb.authorize`;
  - `destructiveHint` false, with `inputText`'s reason;
  - result `{delivered: true, warnings?}`;
  - errors: `PreambleErrors`, `DEVICE_NO_STREAM`, `DEVICE_STREAM_FAILED`, `REQUEST_INVALID_PARAMS`, `OPERATION_QUEUE_TIMEOUT`.

  The three:
  - `device.tap {device, x, y, durationMs?}`: x and y in 0..1; `durationMs` 0–2,000 (a long press).
  - `device.swipe {device, path}`: 2–64 points `{x, y, t}`, `t` in ms from the first point, strictly increasing, at most 5,000.
  - `device.pressKey {device, key: back | home | appSwitch | enter | delete}`.

  The handlers:
  - validate, then find the device's running source, with no acquire;
  - with none, fail `DEVICE_NO_STREAM` (new, nextStep "open the device's live view") having sent nothing;
  - otherwise call `Inject` with the `Input`;
  - a failure before the first byte is `DEVICE_STREAM_FAILED`; one after it is completed with a warning, never an error (`ui-automation.md` §Design rules).
- Implements: Q5, Q6; constraint 2; `ui-automation.md` §Input primitives (normalised coordinates, sized by the stream, Q5).
- Check:
  - With a fake `Source` recording `Inject`:
    - each capability passes the right `Input`;
    - invalid points (outside 0..1, `t` not increasing, 65 points, 5,001 ms) give `REQUEST_INVALID_PARAMS` with no `Inject`;
    - no source gives `DEVICE_NO_STREAM` with no `Inject`;
    - a non-allowlisted physical device gives `DEVICE_NOT_ALLOWED` before the source is looked up (seen failing with `authorize` skipped);
    - an MCP session gets `REQUEST_NOT_ALLOWED_HERE`;
    - each call is an operation with its client kind and its trace line (the existing trace test's pattern).
  - The MCP tool list is byte-identical.
- Schema: three capabilities with params, results and errors; `DEVICE_NO_STREAM` in the codes list.
- Needs: steps 3, 4.
- Not here: no `expect` (a person sees the effect; agents' coordinate actions come with C5); no reader fallback without a stream (Q5).
- Rests on: no input reaches the device without passing `authorize`, at level 4.
- Review: the Opus verifier (contract, policy), effort `high`.
- [x] Done. Evidence: `TestScreenInput*` (each capability passes its `Input`; bounds refused with no `Inject`, null coordinates included; no source → `DEVICE_NO_STREAM` with nothing sent and no source started, through the real source set; a non-allowlisted physical device → `DEVICE_NOT_ALLOWED` before the lookup; MCP → `REQUEST_NOT_ALLOWED_HERE`; each call an operation on the write key with its trace line; before/after the first byte → error/warning; a cancel ends `cancelled`), `TestScreenSourceControlWriteErrorEndsTheSource`; the MCP tool list unchanged (9 `device_*` tools). Seen failing: authorize skipped, `Resource` removed, audience `all`, the wrong key, the 64-point bound, the `PartialError` branch, x/y swapped, the lookup acquiring, `running`'s ready and ending checks, `Inject` not wrapping, the context check below `PartialError`, `sent := true`, `endOnWriteError` a no-op, null defaulted to 0. Opus verifier: proceed, 0 blocking; [test] 2, [bug] 1 (a partial control write left the stream out of frame: a control write error now ends the source), [contract] 1 (null coordinates), nits 4, all taken but two nits.

### 7. Input in the view, and its latency

- Builds:
  - `web/src/lib/screen/input.ts`, pure:
    - a pointer maps to normalised coordinates inside the letterboxed frame; the bars are ignored;
    - a gesture under 10 px and under 300 ms is a tap; held in place it is a tap with `durationMs`; otherwise it is a swipe, sampled at 16 ms or more and capped at 64 points and 5 s.
  - `LiveView.svelte`:
    - Back, Home and Recents call `pressKey`;
    - a text field with Send calls `device.inputText` with no target;
    - errors show as text.
  - `uitree.ts`: the tree gets its own trigger list, `UI_TRIGGERS` plus the three. App logs keep `UI_TRIGGERS` unchanged, so a gesture does not cost a `device.logs` read.
- Implements: Q5 (the UI's part); constraint 2.
- Check:
  - Vitest:
    - the mapping with bars (seen failing with the bars counted);
    - the classification, and the decimation;
    - the buttons' keys;
    - a `device.tap` reloads the tree and not App logs (seen failing with the shared list).
  - **Level 5 (main session, K2, scratch AVD, formapp):**
    - a CDP click on the canvas over formapp's button changes the tree as a tap does;
    - a drag scrolls Settings;
    - Back and Home work;
    - "ñandú" lands in formapp's field;
    - Activity lists each call with `ui` and its device.
  - **Latency (A3):** p50/p95 of 20 taps for the `durationMs` of the `operation.state` terminal events, and for click to the next `data-frames` increment. The main session sets them beside step 1's numbers in the research note and `ui-automation.md` §Input primitives.
- Schema: no change.
- Needs: steps 5, 6.
- Not here: typing into the canvas, wheel scroll, live drag (Not in this milestone).
- Rests on: —
- Review: the main session's diff review and its level-5 run.
- [x] Done. Evidence: level 5 (K1–K3, API 31 scratch AVD, formapp, headless Google Chrome, `evidence/lv5/dyp.XoioiO`): a CDP click over Like adds one like, 20 add 20; "ñandú" sent from the panel reads "ñandú" in formapp's field (read through `device.uiTree`); Back (twice: the first closes the keyboard) and Home move the focus; a drag scrolls Settings; every call is an Activity row "You · emulator-5580" and a trace line `client=ui`. **Latency (A3)**, 20 taps per run over four runs: click to the next decoded frame p50 51–53 ms, p95 63–119 ms; `device.tap` `durationMs` p50 76–83 ms, p95 91–107 ms; beside step 1's bare control write to the next video bytes (p50 22 ms, p95 141 ms) and `adb devices -l` (p50 49 ms, p95 56 ms). Vitest: bars ignored, the tree's own triggers, the shared list, the Activity target (seen failing each). `pnpm -C web run verify` ok.

### 8. `FLAG_SECURE`: the screenshot warns, the view shows a banner

- Builds: `internal/domain/device/secure.go`.
  - `secureFocus(ctx, t) (bool, error)` runs step 1's command. Its parser reads only the focused window's flags.
  - `device.screenshot` runs it after a capture. When it is true, the result gets the warning "the focused window is secure (FLAG_SECURE): Android captures it black". No package is named, so the field stays untagged. A failed check adds nothing.
  - The screen source polls it every P while it has viewers, where P is a constant: the larger of 2 s and 4 × step 1's p95. It publishes `device.screen` `state {device, secure}` at start and on each change.
  - `LiveView.svelte` shows a banner over the canvas while `secure`.
  - No description changes, so the MCP tool list is byte-identical.
- Implements: Q9; closes R1.
- Check:
  - The parser on step 1's outputs (on and off, for each API level run), including a case where another window's line says `SECURE` and the focused one does not, which must give false (seen failing with a search of the whole output).
  - With the fake adb: a secure check warns, an insecure or failing one does not.
  - A fake source whose check flips publishes one `state` per change.
  - Vitest: the banner.
  - Level 5 on formapp's toggle: the warning on `droidyard call device.screenshot` and the banner, both gone with the toggle off.
- Schema: `device.screen` gains the event type `state`.
- Needs: steps 4, 5.
- Not here: no warning on `device.uiTree`; a secure window stays black (the platform's rule).
- Rests on: —
- Review: the main session's diff review.
- [ ] Done. Evidence:

### 9. The operation view names its target

- Builds:
  - `capability`:
    - `OpTarget{Device, AVD, Module, Variant string}`, all `omitempty` and each capped at 200 bytes;
    - `Descriptor.Target func(params any) *OpTarget`, set on: every `device.*` with a `device`; `emulator.stop` (device); `emulator.start` and `emulator.wipe` (AVD); `build.assemble` (module, variant); `device.install` (device, module, variant); `build.run` gets none;
    - recorded at `Start`, and published as `target?` on the `operation.state` payload and on `ActiveOperation`.
  - Web:
    - `uitree.ts` and `applogs.ts` reload on a terminal operation only when its `target.device` is theirs, or when it has no target;
    - `mirror.ts` `ActiveOp` gains `target`, and `Devices.svelte` names the device being stopped and the AVD being booted;
    - `toolbar/status.ts` names `target.module` for any client's `build.assemble`.
- Implements: Q10; closes R2 and R5.
- Check:
  - `TestOperationStateCarriesTarget` (`cmd/droidyard`): `device.tapElement` gives `{device}`, `emulator.start` `{avd}`, `build.assemble` `{module, variant}`, `device.install` all three, `build.run` none; `operation.watch`'s snapshot agrees.
  - Vitest:
    - no tree reload for another device's tap (seen failing without the filter), and the same for app logs;
    - the mirror's names;
    - `buildStatus` names an MCP client's `build.assemble` module (seen failing with today's `client === 'ui'` rule).
- Schema: `target` on `ActiveOperation` (`operation.watch`) and on the `operation.state` payload.
- Needs: step 4's version only.
- Not here: other parameters (ADR-0043 §2 otherwise holds).
- Rests on: —
- Review: the Opus verifier (contract), effort `high`.
- [x] Done. Evidence: `TestOperationStateCarriesTarget` (`device.tapElement` {device}, `emulator.start` {avd}, `build.assemble` {module, variant}, `device.install` all three, `build.run` none; running, terminal and snapshot agree; a value cleaned of a bidi override and a newline and cut at 199 bytes on a rune boundary; an empty target omitted), `TestTargetsFollowTheParameters` (the 17 capabilities), `TestToolListModelFacingBytes`; Vitest: no tree or app-logs reload for another device's operation, the mirror names the device stopped and the AVD booted, the toolbar names any client's `build.assemble` module through `inert`. Seen failing: the variant left out, `Active()` without the target, `boundTarget` skipped, the cleaning removed, the rune step-back removed, the filter removed, the `client === 'ui'` rule, the mirror's target dropped, `inert` removed. Opus verifier: revise, [safety/contract] 1 blocking (the target held unvalidated caller text, shown raw in the toolbar), [test] 1, nits 3; fixed and reviewed by the main session.

### 10. Level 5 and the close

- Builds: no code. The main session's run under K1–K3:
  - (a) Steps 5, 7 and 8's runs on the final binary, also on the android-36/37.1 scratch AVD if it booted, or on the Pixel_9a with the owner's yes (stream only).
  - (b) `droidyard stop` while viewing leaves no server in the device's `ps -A` and no forward. After a SIGKILL while viewing, the next start's `Reconcile` reaps the recorded client; the forward left behind is recorded.
  - (c) An MCP stdio session: `tools/list` is unchanged, and `device_screenshot` on formapp's secure screen carries the warning.
  - (d) Documents:
    - `adb-device.md` §Screen viewing, rewritten in three sentences linking the ADRs; §Lifecycle and concurrency, on what the server leaves behind;
    - `process-lifecycle.md`'s kinds table and its crash-recovery sentence, which name `screenrecord`;
    - `ui-automation.md` §Input primitives;
    - `capability-api.md` §Events and subscriptions (`device.screen`);
    - `http-websocket.md` §Endpoints (`GET /v1/screen`);
    - `mvp.md` §Out of scope's stream line;
    - `beyond.md` §C3 ("Built in 2p");
    - the register: R1, R2, R3, R5 closed by 2p; R4 split (`device.screen` closed, the rest open); new rows: a forward after a crash, the loopback port race, a tap scrcpy drops after a rotation while the call says `delivered`, physical devices and Linux not run, the owner's latency trial, and no notices for the Go modules in the release;
    - `docs/README.md` §Where the work stands; the index row.
- Implements: A1, A2, A5, A6.
- Check: each run's decisive output under `$B/evidence`, quoted in the evidence line.
- Schema: no change.
- Needs: steps 1–9.
- Not here: the look (`review`).
- Rests on: —
- Review: the Opus verifier at the close.
- [ ] Done. Evidence:

## Not in this milestone

- Agents watching the screen or acting by coordinates over MCP: Q6, with C5.
- C4 and C5: `beyond.md`.
- Audio and clipboard sync: off in the server's options. Clipboard text is device text.
- Wheel scroll, typing straight into the canvas, live drag tracking (a stream of touch events), multi-touch: later minor changes on ADR-0079.
- Input without a live view (a reader fallback): Q5.
- H.265 or AV1, and a quality control in the UI: step 1's constants.
- Waking a sleeping device; a view that survives a reboot (Restart is offered).
- Notices for the Go modules: a register row.

## Design questions

None blocks. Each is Delegated: the main session applies the recommendation and writes the ADR named in Contract.

- **Q1. Transport (ADR-0077).** Recommended: **scrcpy's server** (API 21+, H.264 from MediaCodec, follows rotation, has a control socket, Apache-2.0). Rejected:
  - `screenrecord --output-format=h264`: restarts every 180 s on many releases, has no control channel and does not follow rotation (assumed from its usage text);
  - a `screencap` loop: 2–5 fps, and stills, which the owner rejected;
  - the emulator's gRPC stream: emulator only, against "serial is the only identity";
  - our own encoder dex: a rewrite of scrcpy's server, with hidden-API wrappers for each release.

  **No ADR held "the MVP does not stream"**: it lives in `adb-device.md`, `mvp.md` §Out of scope and m2b Q2. ADR-0077 replaces it, and its `StreamSource` seam is step 2's `Source`. On the earlier ADRs:
  - ADR-0005 rejects the opaque Android CLI. An open-source, pinned, checked server is not that, so there is no Supersedes.
  - ADR-0029's Alternatives read ADR-0005 as "the platform's tools and our own small code". A third-party prebuilt departs from that reading, and ADR-0077's Context says so.
- **Q2. Licence, vendoring, versioning (ADR-0077).** Recommended:
  - the release binary, embedded, with `Version` and `SHA256` pinned and checked before each push;
  - the licence (and `NOTICE`, if any) embedded and printed by `droidyard version --licenses`, so ADR-0068's one-member archive is untouched;
  - pushed at each start; the server deletes its jar (S4 and (e));
  - an upgrade is deliberate: a new file, a new digest, re-recorded fixtures.

  Rejected: downloading at run time; a host's Homebrew copy; building from source; a licence file in the archive (`install.sh` discards it).
- **Q3. Browser decoding (ADR-0078).** Recommended: **WebCodecs only**, Annex B, to a canvas (`127.0.0.1` is a secure context); an unsupporting browser gets a message. Rejected: MSE with a remux to fragmented MP4 (code and latency); a WASM decoder (a dependency); JPEG frames (stills).
- **Q4. The wire (ADR-0078).** Recommended:
  - a streaming **capability** `device.watchScreen` owns the stream;
  - control events go on its subscription (`device.screen`, typed in the schema);
  - packets go on a separate binary **`GET /v1/screen`**, bound by an unguessable id.

  Rejected:
  - base64 events on `/v1/ws`: 33% more bytes, and the 1,024-event queue and `events.gap` break a decoder until the next keyframe;
  - binary frames on `/v1/ws`: video would share one TCP send buffer with every response, and it would change ADR-0028's text-only session;
  - SSE or chunked HTTP: not binary, and no keyframe-aware backpressure.

  The endpoint is also the unit a C4 relay forwards.
- **Q5. The input path (ADR-0079).** The owner fixes the outer shape: capability calls. Recommended behind them: the **source's control channel** (scrcpy's socket). It injects asynchronously, in the video's own coordinate space, and is open whenever a view is. Rejected:
  - the reader's synchronous injection (23–435 ms quiet, blocked 11.2 s on a frozen app, ADR-0035), which also needs a new swipe and key verb and a dex rebuild;
  - `adb shell input` (a JVM per call, about 0.65 s or more).

  Text goes through `device.inputText` (`set_text`, Unicode). Coordinates are normalised 0..1 against the stream's frame size, instead of `ui-automation.md`'s "fresh screencap": the screencap was a source of a rotation-true size, and the SPS is one. Audience `human` (ADR-0060 §2): coordinates for agents go against "identity first" and ADR-0036.

  **The latency it costs**, per input, against a bare socket write:
  - the loopback HTTP round trip, which `durationMs` excludes;
  - the operation bookkeeping;
  - the authorize hook's `adb devices -l`, assumed 10–60 ms;
  - the write key: a view tap queues behind an agent's `device.tapElement`, which holds the key for up to 20 s, until `OPERATION_QUEUE_TIMEOUT`;
  - a drag reaches the device only after release, replayed with its timing, so a drag lands late by its own length.

  Live tracking would make each move an operation, which is too many; it is a later decision.
- **Q6. Agents over MCP (ADR-0079).** Nothing new: streaming is never an MCP tool (ADR-0010), and the input capabilities are `human`. Agents keep the screenshot, with its warning. C5's agent exploration decides coordinate input for agents.
- **Q7. Lifetimes (ADR-0078).** One server per device, shared and reference-counted. It ends:
  - with its last viewer (subscription end, socket close, or an unattached id after 10 s);
  - with its device (`deviceGone`, including `emulator.stop`);
  - with the daemon (`StopAll`);
  - on a protocol error (`failed`).

  The child is recorded, as ADR-0021 already foresees for "`screenrecord` streams". The panel watches only while visible.
- **Q8. Several viewers (ADR-0078).** Recommended: the source keeps the current config and GOP; a join starts at the GOP's keyframe with the config prepended, and an overflow rejoins at the next keyframe. This is source-agnostic: no keyframe request, and a relay can do the same. Rejected: a server per viewer (an encoder each); refusing a second tab.
- **Q9. `FLAG_SECURE` (ADR-0080).** Recommended: a focused-window check (step 1 picks the command), as a screenshot warning and as `device.screen` `state` while viewing. Rejected:
  - an all-black heuristic: the status and navigation bars are other windows;
  - a reader request: `AccessibilityWindowInfo` has no secure flag.
- **Q10. The operation's target (ADR-0076).** Recommended: `target {device?, avd?, module?, variant?}`, declared per descriptor. It supersedes ADR-0043 §2 in part, as a bounded exception to "never parameters".
  - ADR-0043 deferred a target "if a mirror needs it", and ADR-0048's Alternatives repeated the rejection. R2 and R5 are those mirrors, and live-view taps would otherwise reload every device's tree.
  - The values are the caller's own validated parameters, not device text.

  Rejected: all parameters; deriving it in the UI (blind to other clients).

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| The scrcpy server (4.1 or later) streams and takes control input on API 31 and 36.x | 2–8 | step 1, S1, S3; 36.x only on a booted scratch AVD or the owner's yes |
| Headless Google Chrome decodes H.264 with WebCodecs | 5, 7 | step 1 (g), S2 |
| Config comes only at encoder (re)start; a new size is a new SPS at a keyframe | 2, 6 | step 1 (a), (b) |
| `video_codec_options` gives a 2 s GOP | 2 | step 1 (c); otherwise the default, under the 16 MiB bound |
| The server exits when its client and sockets go, and removes its jar | 3 | step 1 (e), S4; step 3's real check |
| `adb devices -l` costs 10–60 ms per input call | 6, 7 | step 1 (d); step 7 |
| A cheap command names the focused window's `FLAG_SECURE` on each API level run | 8 | step 1 (f) |
| A touch for a stale size after a rotation is dropped by scrcpy while the call reports `delivered` | 6 | not settled; a register row |
| Physical devices, other encoders, odd sizes; Linux | all | not run |
| The latency suits a person | 7 | the owner's trial |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | [blocking] B1: the Hub's GOP began at a config keyframe, which scrcpy sends only at encoder start, and a GOP larger than a viewer's queue made joins livelock | Step 1 (a) records when config comes, and fixture (a) spans 3 keyframes. Step 2 keeps config apart, a join starts at the GOP keyframe with config prepended, the queue bound (20 MiB) is at least GOP + packet cap, an overflow rejoins at the next keyframe; tests for a join after keyframe 2 and a single rejoin. Step 5's decoder rejoins on error |
| 1 | revise | [blocking] B2: the licence was added to the release archive, against ADR-0068, and `install.sh` discards it; the check named no test | `build.sh` is untouched. The licence is embedded and printed by `droidyard version --licenses`, with `TestVersionLicenses` seen failing with the embed emptied (step 1, Q2) |
| 1 | revise | [should] S1 `Input` undefined, so scrcpy's wire leaked into handlers; S2 a closed stream socket kept its viewer; S3 gestures would reload App logs | S1: a neutral `screen.Input`, and `EncodeControl` tested in step 2; handlers use a fake `Source`. S2: socket close or 10 s unattached releases, `stopped detached`, 4404 afterwards, the model re-watches. S3: the tree's own trigger list, with a Vitest case |
| 1 | revise | [should] S4 ADR-0048 skipped; S5 no spike tooling, (e) not a stop rule; S6 no daemon packet count; S7 the header in two languages | S4: Q10 cites ADR-0048, with a bounded exception. S5: `TestSpikeScrcpy` (env-gated, implementer) and a scratch page; stop rule S4. S6: `data-packets`. S7: `frame-golden.bin`, read by Go and Vitest |
| 1 | revise | [should] S8 cancel during acquire, refusal after `StopAll`; S9 the write-key queueing and `durationMs`'s scope; S10 ADR-0029 departure and the loopback port race | S8: the two tests in step 3. S9: in Q5's latency list. S10: Q1 and ADR-0077's Context; the race in step 3's Not here and a register row |
| 1 | revise | nits: Rests on wording; `process-lifecycle.md`; screenshot description edits unneeded; the Chrome binary; ADR-0078's close codes; size | All taken: step 4's Rests on; step 10 (d); step 8 changes no description, so the tool list is byte-identical (A9); K3 and S2 name Google Chrome; the Contract lists the codes and header. Q1–Q10 compacted, the ADR list folded into Contract |
| 2 | proceed | B1 resolved (config apart, join with config, bounds consistent, tests aimed at the defect); B2 resolved (checked against ADR-0068, `install.sh:195`, `TestArchiveMembers`, the `version` case) | — |
| 2 | proceed | [should] S-a Demux and the Hub both merged the config; S-b a rejoin's keyframe not said to carry it; S-c a decoder error re-watched without a limit | S-a: Demux yields config packets on their own, and only the Hub prepends; `TestHubJoinAtFirstKeyframe` (exactly once). S-b: the rejoin keyframe carries the current config, with a test case across a config change. S-c: one re-watch, then `stopped(failed)`, with a Vitest case |
| 2 | proceed | nits: bare `version` stays one line; `Join()` with an empty GOP and `TestHubGOPBound`'s assertion; whether the caps count the config | All taken (step 1; step 2: an empty GOP waits for the next keyframe with config; caps count device bytes only, and the queue bound adds the config) |

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.

- 2026-10-17, step 1 (provenance). The vendored server is **scrcpy 4.1** (tag `v4.1`, 2026-07-12), not the newest release: 5.0 (2026-10-05) is a major release twelve days old, and 5.0.1 a hotfix of it, while 4.1 has three months in the field and is the version the plan's research read. Its SHA-256 `deacb991ed2509715160ffdc7907e47b4160eb30d1566217e9047fd5b8850cae` (733,706 B) matches the release's `SHA256SUMS.txt` and Homebrew's copy. `LICENSE` is the tag's (Apache-2.0, 11,387 B); the tag has no `NOTICE`. Moving to 5.x is a deliberate upgrade (Q2). Delegated.
- 2026-10-17, step 1 (run). The spike tool's lifetime poll stopped on `ls`'s exit status; the main session appended `; true` to its device command (one line). The second scratch AVD is `droidyard_m2p36` (API 36, Pixel 6 profile, cold boot ~22 s), so the Pixel_9a was not used; 37.1 was not run.
- 2026-10-17, step 2. The video socket carries a 12-byte **session packet** (first word's bit 31 set; then u32 width, u32 height; no payload) before each encoder start's config, at start and at rotation (the note §(a)). `Demux` reads it and never yields it as media; `TestDemuxRotation` counts two. The preamble is the dummy byte, the 64-byte name and the 4-byte codec id, with no size after it. ADR-0077, ADR-0078.
- 2026-10-17, step 8. On API 31 `screencap` with a secure window focused fails (exit 1, empty output) instead of capturing black; on API 36 it captures black. `device.screenshot` also runs the check when the capture fails, and `SCREENSHOT_FAILED`'s cause then says the focused window is secure. The check is one `adb shell dumpsys window` (focus line plus that window's block; `dumpsys window windows` lacks the focus line on API 31), its fixtures `testdata/dumpsys-window-api{31,36}-{on,off}.txt`; the parser lives with step 8's code and reads them from `internal/domain/device/screen/testdata`. ADR-0080.
- 2026-10-17, steps 7 and 10. Latency numbers go to the research note and `ui-automation.md` §Input primitives, not to ADR-0079 (an accepted ADR is not edited).
- 2026-10-17, step 2. The research note's golden touch lines printed the pointer id as `ffffffffffffffff`; the bytes sent and landed were `fffffffffffffffe` (-2, as the note's next sentence says; `evidence/spike-taps20.txt`). Corrected in the note on this branch before the merge. Step 2 also bounds the GOP and each queue by packet count (`MaxPackets` 16,384) under ADR-0078 §5's byte bounds, and a config packet empties the GOP (the old GOP is the old encoder's).
- 2026-10-17, step 2. `EncodeControl(in Input, size Size) ([]Control, error)`, `Control{At, Data}` carrying the schedule, instead of `[][]byte`; the package bounds (`MaxInputDuration` 60 s, `MaxSwipePoints` 1,024) are looser than ADR-0079 §1, which step 6's handlers check first.
- 2026-10-17, step 3. A start completes at the first config packet, within 5 s (`protocol` phase), not at the connect: a protocol failure at start (ADR-0078 §1) shows only once bytes arrive, and `width`/`height` come from it. A connection counts once its dummy byte arrives (peeked; `Demux` reads it). A forward whose port was never read (a cancel mid-command) is found in `forward --list` by its `scrcpy_<scid>` and removed. The control socket is discarded up to 1 MiB, then the source ends `failed`. A jar left by a start that failed after the push is overwritten by the next push to the same digest-named path (a register row at the close).
- 2026-10-17, step 3. A cut video stream or a demux error chooses its reason by the device check the child-exit path uses: absent or offline → `deviceGone`; online with the child exited → `exited`; otherwise `failed`. A clean EOF on the video socket counts as the child's exit (the same check). A dying device can cut the socket before its child exits, and the view should say the device went. Delegated.
- 2026-10-18, step 4. A subscription's end publishes no `stopped` (its subscriber is gone; as `device.watchLogs`); the other endings do. A frame over 4 KiB after `auth` closes 1009 (the read limit holds for the socket's life); a smaller client frame closes 1003. Each binary message is written under a 10 s deadline. `/v1/screen` has no ping loop: a gone peer is seen by the reader or the next write. `daemonOptions` takes the `*device.ADB` (its `Close` is `AfterOps`), and `ADB.Close` ends the screen streams before the sources. `mvp.md`'s device row and `mcp.md`'s not-exposed row name `watchScreen` (`TestCapabilityNamesAgree`).
- 2026-10-18, step 4 (verifier). The two departures from ADR-0078 above (no `stopped` on a subscription's end, against §3; 1009 for a frame over 4 KiB after `auth`, against §4's "any client frame … 1003") get a short superseding ADR, number 0087 (reserved), written at the close after `main` is merged: `internal/doccheck` wants ADR numbers without gaps, and 0081–0086 are lane 2q's until it merges. If 2q has not merged by then, the number is re-reserved and the plan says so. The `streamId` is blanked from the stored operation result after its delivery (a `Slim`), so `operation.wait` from another session cannot read it back.
- 2026-10-18, step 5. `Project` takes the token App already holds (the generated client does not expose it); `ShellApi.rightOpen()` is new, since the right region has no tabs ("current tab and region open" is "region open"), and the page's visibility is part of "visible". Close codes 4401, 4404, 4409 and 1003 show `stopped(failed)`; 4410 is left to the daemon's `stopped` event. The level-5 run opens each tab in a window of its own: a background tab in the same window is hidden and, by design, does not watch.
- 2026-10-18, step 6. Step 3's `Inject` now says whether anything was sent (a failure after the first written byte is a `screen.PartialError`), and an `Inject` on an ended source writes nothing (a `Done` check after the token; the race failed 12 of 30 runs before). A cancelled input call ends `cancelled`, even after the first byte, and still lifts the finger; "complete with a warning" covers stream failures. A swipe's first point has `t` 0. adb's version warnings are not put on the input results, so their `warnings` are the daemon's words only. The live corpus budget moves 27,670 → 27,684 for the two capability-name rows.
- 2026-10-18, step 9. Seventeen capabilities declare a target, including `device.watchScreen`, the three input capabilities, `device.launch`, `logs`, `uninstall` and `uiTree` (all take `device`); `TestTargetsFollowTheParameters` pins the set and fails for a capability with a `device` or `avd` parameter and no `Target`. A terminal operation whose target names no device reloads the tree and app logs as an untargeted one does. The devices panel's install row names its device too (`targetText` is shared). A value is cut at 200 bytes on a rune boundary; the schema's `maxLength` 200 counts characters, so every value satisfies it.
- 2026-10-18, step 9 (verifier). The target is recorded at Start from the decoded parameters, before a handler checks their patterns, so ADR-0076's "validated parameters" does not hold as written: each field is cleaned of control and format characters (as ADR-0048's session name), cut at 200 bytes on a rune boundary, and the toolbar shows the module through `inert`. The superseding ADR written at the close (number 0087, reserved) also records this reading of ADR-0076 §2–3.
- 2026-10-18, step 7. Activity rows show the operation's target (device, else AVD, else module, through `inert`) beside the client tag, for A2's "with client `ui` and its device" (`blocks.ts` `targetOf`). Only the primary button starts a gesture; a gesture past 5 s keeps its last sample within 5 s as the lift; nothing is sent before the frame size is known. `TestAttachScreen` (step 4) assumed a join starts at the recording's first key frame; under load it starts at a later one, as ADR-0078 §5 says, and its assertion now accepts any of the recording's key frames with the config once. The level-5 run reads the screen through `device.uiTree`: a parallel `uiautomator dump` collides with the reader the UI tree panel keeps running.
