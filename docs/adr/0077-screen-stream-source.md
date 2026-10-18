# ADR-0077: The live view's source is scrcpy's server, vendored and pinned

- Status: Accepted
- Date: 2026-10-17
- Relates to: ADR-0005, ADR-0021 §Pid records, ADR-0029, ADR-0068; `adb-device.md` §Screen viewing, §Transport to the device; plan m2p Q1, Q2; [`spike-screen-stream-2026-10.md`](../research/spike-screen-stream-2026-10.md)
- Decided by the owner, 2026-10-17: the live view is C3 of [`beyond.md`](../vision/beyond.md), with no stills (2026-10-09), its stream relayable to another Droidyard later (C4) and every input a capability call (C5). The source is Delegated (plan m2p Q1, Q2, reviewed in two rounds; the version, the plan's Changes).

## Context

`adb-device.md` §Screen viewing decided the MVP does not stream, and named the way to do it
if needed: "an H.264 path (scrcpy-style) behind a `StreamSource` seam, not a PNG-per-frame
loop". No ADR held that decision; it lived in that document, `mvp.md` §Out of scope and
m2b Q2. The owner now wants the view, and has ruled out stills.

Streaming needs a program on the device, with shell privileges, that mirrors the display
into a virtual display, feeds it to MediaCodec's H.264 encoder and injects input. Those
are hidden APIs that change across releases. scrcpy's server does exactly that, for API
21 and later, under Apache-2.0, maintained since 2018. The spike (m2p step 1) ran its
4.1 release on API 31 and API 36 emulators: a key frame 0.5–2.2 s after start, about
37 frames/s while scrolling, 20 of 20 control-socket taps landed, the server gone 1–1.3 s
after its client, and headless Google Chrome decoding 100 % of the packets.

ADR-0005 rejects the opaque Android CLI and has the daemon own its pipeline. ADR-0029's
Alternatives read it as "the platform's tools and our own small code". A third-party
prebuilt run on the device departs from that reading; this ADR is where that is decided.

## Decision

1. **The source** of the live view is scrcpy's device-side server, run as
   `adb -s <serial> shell CLASSPATH=<jar> app_process / com.genymobile.scrcpy.Server
   <version> scid=<8 hex> …` over `adb forward tcp:0 localabstract:scrcpy_<scid>`, with
   the options of plan m2p step 1 (H.264, `max_size=1280`, 4 Mbit/s, 60 fps max,
   `i-frame-interval=2`, no audio, clipboard sync off, `cleanup=true`). The `adb shell`
   child is a recorded `device-stream` process (ADR-0021 §Pid records).
2. **Vendored and pinned.** The release asset `scrcpy-server` of one version (4.1 today)
   is embedded in `droidyard` (`internal/domain/device/screen/scrcpyserver`), its
   `Version` and SHA-256 pinned by a test and checked before each push. It is pushed to
   `/data/local/tmp/droidyard-scrcpy-<first 12 hex>.jar` at each start; the server
   deletes it as it starts. A new version is a deliberate change: the new asset compared
   with the release's `SHA256SUMS.txt`, a new digest, re-recorded fixtures.
3. **The licence** (Apache-2.0; the tag has no NOTICE) is embedded and printed by
   `droidyard version --licenses`. The release archive stays one member (ADR-0068).
4. **The seam.** The daemon sees the source only as `screen.Source`: `Join() Viewer`,
   `Size()`, `Inject(ctx, Input)`, `Done()`, `Reason()`. Packets are the device's own H.264
   access units, unchanged; input is a neutral value (`Tap`, `Swipe`, `Key`, normalised
   0..1) that the scrcpy source encodes into its control messages. Another source (a
   relayed remote device, C4; a different server) implements the same seam.

## Consequences

- Droidyard runs third-party code on the user's device with shell
  privileges: the digest check is what makes it the code the release published.
- A scrcpy upgrade is ours to schedule; its protocol is versioned by the server's own
  version check, so a mismatch fails at start (`DEVICE_STREAM_FAILED`).
- A daemon killed outright leaves its forward until adb restarts or the device goes, and
  between `forward` and the connects another local user could connect to the loopback
  port first (the reader's forward has the same class): register rows at m2p's close.
- Documents to change: `adb-device.md` §Screen viewing (replaced), §Lifecycle and
  concurrency; `process-lifecycle.md`'s kinds; `mvp.md` §Out of scope's stream line; in
  plan m2p step 10.

## Alternatives considered

- **`screenrecord --output-format=h264`**: on the device already, but stops after 180 s
  on many releases, has no control channel (input would need a second path), and does
  not follow rotation (from its usage text; not run).
- **A `screencap` loop**: 2–5 fps over USB in the prior art, and stills, which the owner
  rejected.
- **The emulator's gRPC screen stream**: emulators only, and a second identity beside the
  serial.
- **Our own encoder dex**: the same hidden APIs scrcpy wraps, rewritten and kept up with
  each Android release; we already ship a dex (ADR-0029) for the UI tree, where no
  maintained equivalent existed. Wins if scrcpy stops being maintained or its protocol
  stops fitting the seam.
- **Downloading the server at run time, or using a host's Homebrew copy**: a network
  dependency at run time, or a version we do not pin.
- **The newest release (5.0.1)**: twelve days old when chosen; 4.1 had three months in the
  field and three matching digests. Reopened at the next deliberate upgrade.
