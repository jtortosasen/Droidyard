# Spike: scrcpy's server as the live view's source (milestone 2p step 1)

Tests plan m2p's Q1–Q9 on real emulators before any daemon code uses the server: the
wire, the cost, input through the control socket, the server's lifetime, `FLAG_SECURE`,
and the browser's decoder.

> **Status: done, 2026-10-17. Stop rules S1–S4 pass on API 31 and API 36.** Every
> number below was observed by the main session with `TestSpikeScrcpy`
> (`internal/domain/device/screen/spike_test.go`) unless marked otherwise. Three
> findings change the plan (§What changes): the video socket carries **session packets**
> the plan did not name; a secure window makes **`screencap` fail on API 31** instead of
> capturing black; `i-frame-interval=2` gives keyframes about **3.2 s** apart.

- **Server**: scrcpy 4.1, `scrcpy-server` SHA-256 `deacb991…0cae` (the plan's Changes,
  2026-10-17), pushed to `/data/local/tmp/scrcpy-server.jar` by the spike tool.
- **Devices**: scratch AVDs only, on the run's own adb server (port 5199, the m2p guard):
  `droidyard_m2a` (API 31, `google_apis`, 1080×2340, `emulator-5580`) and
  `droidyard_m2p36` (API 36, `google_apis_playstore`, Pixel 6 profile, 1080×2400,
  `emulator-5582`, created for this run, cold boot about 22 s). The owner's Pixel_9a and
  5037 were not touched. Host: macOS, Apple silicon.
- **Options** (production, the plan's list): `tunnel_forward=true video=true audio=false
  control=true video_codec=h264 max_size=1280 video_bit_rate=4000000 max_fps=60
  video_codec_options=i-frame-interval=2 clipboard_autosync=false cleanup=true`, after
  `<version> scid=<8 hex> log_level=info`.
- **Evidence**: `~/Library/Caches/droidyard-dev/m2p/evidence/` (each run's output) and
  `…/spike/` (recordings).

## (a) The wire, as recorded

Read from the tag's source by the implementer and confirmed by every recording:

```text
video socket:  [dummy 0x00]  [device name, 64 B, NUL-padded]  [codec id, 4 B: "h264"]
               then 12-byte headers, big endian:
                 session packet: u32 flags (bit 31 set; bit 0 of byte 3 "client resized")
                                 u32 width, u32 height            -- no payload
                 media packet:   u64 (bit 62 config, bit 61 key frame, else pts in µs)
                                 u32 size, then size bytes of Annex B
control socket: opened second (audio off); with clipboard sync off the server sent 0 bytes
```

- The dummy byte comes on the first socket only (video). The device name is device text.
- **Order at every encoder start**: session packet, then one config packet (SPS/PPS,
  33 B on API 31, 32 B on API 36), then a key frame. Observed at start and at rotation.
- **Config comes only at encoder start or restart, never with a later key frame**: the
  5 s animated recording has 1 config and 4 key frames; the 8 s one, 1 config and 3 key
  frames. Plan B1's reading holds: a late viewer needs the config kept apart.
- **A new size**: `user_rotation` 0 → 1 gave a session packet `1280x590`, then a new config,
  then a key frame, 1.5 s after the first; the encoder restarts. The size is in the session
  packet and in the new SPS.
- **Jar**: the server deletes `/data/local/tmp/scrcpy-server.jar` as it starts (`ls`
  right after the first packets: "No such file"), on both levels.

## (b) Fixtures (`internal/domain/device/screen/testdata/`)

| File | What | Size |
| --- | --- | --- |
| `scrcpy-api31-portrait.bin` | the video socket from connect, 5 s while `input swipe` scrolls Settings: 1 session, 1 config, 187 frames, 4 key frames | 1,252,081 B |
| `scrcpy-api31-rotate.bin` | 5 s across `user_rotation` 0 → 1 (`accelerometer_rotation=0`): 2 sessions (590×1280, 1280×590), 2 configs, 73 frames, 3 key frames | 353,907 B |
| `dumpsys-window-api{31,36}-{on,off}.txt` | `adb shell dumpsys window` with formapp focused and its Secure toggle on and off | 39–57 KB |

The two recordings use `video_bit_rate=2000000` and `i-frame-interval=1` instead of the
production options, to fit three key frames under the 2 MiB cap; the framing is the same.

## (c) Rates (production options, `input swipe` scrolling Settings)

| | API 31 | API 36 |
| --- | --- | --- |
| server start → first key frame (push before it: ~0.1–0.2 s) | 0.54–0.74 s | 1.7–2.2 s (3.6 s on the first start after boot) |
| frames/s while scrolling | ~37 | ~36 |
| bytes/s while scrolling | ~3.2 Mbit/s | ~2.0 Mbit/s |
| key frame interval seen | 3.0–3.2 s | 3.3–3.5 s |

- A static screen sends almost nothing: 35 frames in 6 s on API 31, one key frame.
- **`i-frame-interval=2` gives about 3.2 s, not 2 s** (`=1` gave ~1.5 s): the emulator's
  software encoder (`c2.android.avc.encoder`) stretches it. A GOP of 3.5 s at 4 Mbit/s is
  ~1.8 MB, far inside the Hub's 16 MiB bound.

## (d) Input

- **A tap through the control socket lands**: 20 taps on formapp's Like, 400 ms apart,
  gave "Likes: 20" on API 31 and on API 36. Back (keycode 4) moved the focus from formapp
  to the window below it on API 31.
- **Golden bytes** (all landed; 590×1280 frame, point (295, 331)):

  ```text
  touch down  02 00 fffffffffffffffe 00000127 0000014b 024e 0500 ffff 00000000 00000000
  touch up    02 01 fffffffffffffffe 00000127 0000014b 024e 0500 0000 00000000 00000000
  key down    00 00 00000004 00000000 00000000
  key up      00 01 00000004 00000000 00000000
  ```

  (Corrected 2026-10-17 before the merge: the first draft printed the pointer id as
  `ffffffffffffffff`; the bytes sent were `fffffffffffffffe`.)

  The pointer id is -2 (scrcpy's generic finger); the frame size in the message is the
  stream's. A socket write takes 10–110 µs.
- **Up → next video bytes** (20 taps): API 31 p50 22 ms, p95 141 ms, max 350 ms; API 36
  p50 42 ms, p95 195 ms, max 246 ms. This is the device's reaction plus encoding, the floor
  under any path.
- **`adb devices -l`** (the `authorize` hook, 20 runs, run's adb server, one device):
  p50 48.7 ms, p95 56.1 ms with the real adb; 75.7 / 86.4 ms through the guard script.
  The plan's assumed 10–60 ms holds for the real adb.
- **Not measured here**: the reader's `tap` round trip. ADR-0035's figures (23–435 ms
  quiet) stand; step 7 measures the capability path itself.

## (e) Lifetime

With the host `adb shell` client killed and both sockets closed, the server left the
device's `ps -A` after **1.04 s** (API 31) and **1.26 s** (API 36); the jar was already
gone. The forward stays until `forward --remove` (the tool removes it itself).

## (f) `FLAG_SECURE`

| | API 31 | API 36 |
| --- | --- | --- |
| the stream | the window black; status and navigation bars drawn | the same |
| `screencap -p` | **exit 1, empty output** | exit 0, the window black, bars drawn |

- An all-black test fails on both: the bars are other windows (plan Q9).
- **The check**: one `adb shell dumpsys window`, which holds
  `mCurrentFocus=Window{<hash> u0 <component>}` and, further down, the block
  `Window #N Window{<hash> …}:` whose `fl=` line lists `SECURE` when set. Both levels
  share the form. `dumpsys window windows` alone lacks the focus line on API 31.
  Host round trip, 10 runs: API 31 p50 33 ms, p95 40 ms; API 36 p50 35 ms, p95 45 ms.
- The toggle's first tap right after `am start -W` was lost once; a second tap set it.

## (g) The browser

Headless Google Chrome 155.0.8059.80 (own profile), a scratch page on `127.0.0.1`
feeding `VideoDecoder` (`optimizeForLatency`, Annex B, no `description`), each config
merged into the next key frame:

- `isConfigSupported({codec: "avc1.42c029"})`: true (API 31's SPS: Constrained Baseline, level 4.1).
- Fixture (a): **187 of 187** packets decoded, no error. Fixture (b): **73 of 73**, the
  rotation's new SPS taken in band without a `configure`. A join at the third key frame
  with the stream's config prepended: **67 of 67**.
- H.265 (`hvc1.1.6.L120.90`) is supported with hardware only (`prefer-software`: false);
  AV1 (`av01.0.08M.08`) either way. The API 36 emulator lists software encoders for
  h264, h265, av1, vp8 and vp9 (`list_encoders=true`).

## Stop rules

- **S1** pass: a key frame within 5 s of start on API 31 (0.54–0.74 s) and API 36.
- **S2** pass: 100 % of fixture (a)'s packets decoded.
- **S3** pass: 20 of 20 control-socket taps landed, on both levels.
- **S4** pass: the server gone within 2 s of its client (1.04 s, 1.26 s).

## What changes in the plan

1. **Session packets.** `Demux` reads the 12-byte session packet (bit 31 of the first
   word) and does not yield it as media; its width and height may cross-check the SPS.
   Fixture (b)'s count test names two.
2. **`screencap` failing on a secure window** (API 31). Step 8 runs the check also when
   the capture fails, and `SCREENSHOT_FAILED` then says the focused window is secure.
3. **The key frame interval.** `i-frame-interval=2` stays (about 3.2 s on the emulator):
   a join waits at most one GOP only on a stalled viewer, since joins start at the
   current GOP's key frame.
4. **The secure check** is one `dumpsys window`; its parser reads the focused window's
   block only (step 8's "another window says SECURE" case exists in no fixture: it is a
   synthetic test).

## Not established

- Physical devices, hardware encoders, odd sizes, Linux.
- API 36.1 and 37.1: not run (the Pixel_9a was not needed; a 37.1 AVD was not created).
- Whether a tap for a stale size after a rotation is dropped (plan Unverified).
- The capability path's latency and whether it suits a person: steps 7 and 10.
