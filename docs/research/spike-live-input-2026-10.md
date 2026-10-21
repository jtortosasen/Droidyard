# Spike: the live view's input path, the floor and a slow link (m2y step 1)

- Date: 2026-10-21
- Plan: [m2y step 1](../plans/m2y-live-view-that-responds.md#1-spike-and-baseline-a-screen-that-changes-only-on-touch-the-floor-the-facts-the-tunnel-rests-on)
- Devices: the lane's scratch AVDs only, `m2y31` (API 31 `google_apis` arm64, `emulator-5594`) and
  `m2y36` (API 36 `google_apis_playstore` arm64, `emulator-5596`), 1080×2400, streamed at 576×1280,
  on the lane's own adb server (port 5215) behind a guard that refuses any other serial.
- Code: step 1's commit (`ed85490`), daemons built with `-tags droidyard_inputprobe`; formapp's
  `TouchActivity` with the immersive-mode confirmation suppressed (`immersive_mode_confirmations
  confirmed`; until then its dialog held focus and dimmed the activity to half brightness, so the
  first runs were discarded); scrcpy v4.1's source at `.refs/scrcpy`.
- Host: an 8-core, 16 GB Mac with **load averages of 14–58** throughout: another lane's API 36
  emulator held 350–370 % CPU. Every timing below carries that load; the video-rate figures are
  dominated by it (§S8).
- Evidence: `~/Library/Caches/droidyard-dev/m2y/` (`spike/*.log|.bin|.times`, `batch{1,2,3}.out`,
  the runs' `evidence/` under `/tmp/dyy.*`).

## Stop rules

| Rule | API 31 | API 36 | Verdict |
| --- | --- | --- | --- |
| S1 a scroll in 4.1's layout (21 bytes) | `TouchActivity` turned `rgb(0, 255, 176)` after 1 + 2 notches; Settings' screen changed (screencap digest `540bc433…` → `547ca84a…`) | the same colour; digest `2f4ee88c…` → `e3174e04…` | pass: the wheel stays in (Q4) |
| S2 F, 20 touches held 150 ms, production vs `latency=0,priority=0` (two runs each) | down p50 89.8 / 86.0 ms, p95 179 / 149 ms; low-latency p50 63.1 / 78.6 ms, p95 131 / 244 ms | down p50 75.1 / 64.2 ms, p95 143 / 123 ms; low-latency p50 77.6 / 58.5 ms, p95 163 / 114 ms | **not adopted**: p95 does not fall by 20 % on either level consistently; under this load the runs disagree with each other more than the options do. A phone's hardware encoder: unverified |
| S3 reset-video (type 17) → next key frame | 83–156 ms over five requests | 0.5–2.0 s (one session packet 2.0 s after its reset) | exists; under 500 ms on API 31 only, so Q6's key-frame request holds for API 31 and is not relied on for API 36 |
| S4 down, move, `ACTION_CANCEL` | `rgb(255, 0, 255)` (cancel), an `UP` instead gives black; a down after a cancel lands (`rgb(44, 128, 255)` at x = 100) | the same three | pass: the lift is `ACTION_CANCEL` (Q2) |
| S5 a touch for the pre-rotation size | from the source, not a device run: `PositionMapper.map` returns null when the event's screen size is not the current video size, and `Controller.injectTouch` returns without injecting (`Controller.java:485–510`, `PositionMapper.java:33–40`, v4.1); nothing is sent back | the same code | the touch is dropped silently (R4). A lift dropped this way leaves the app's finger down: when the size changes while a finger is held, the daemon must cancel it with the new size |
| S6 the before, 20 taps held 400 ms through `POST /v1/call device.tap` (a UI-identified session with a live view) | T1 p50 50.3 ms, p95 68.6 ms; the call's round trip p50 453 ms (it ends after the 400 ms hold) | T1 p50 65.0 ms, p95 129.0 ms; round trip p50 469 ms. The first `watchScreen` failed `DEVICE_STREAM_FAILED` ("sockets did not answer within 5s") under load; the retry streamed | recorded. Through a loopback peer pair (`qa/emulator-5594`, m2t's stand-in): T1 p50 71.1 ms, p95 110.6 ms. T2 in Chrome: not run here (§Not run) |
| S7 `TCP_NOTSENT_LOWAT` through `linkem` | — | — | **not measurable on this host** (§S7) |
| S8 F, bytes and Lag on L1–L3 for three presets; H.265 | — | — | **inconclusive** (§S8); H.265 does not start on the API 36 emulator at 1280 / 4 Mbit/s |

## S7: why a userspace link cannot show the sender's backlog on macOS

`notsentprobe` read `TCP_NOTSENT_LOWAT` back as 65,536, yet arrival delay was the same with and
without it (p95 3.3–4.1 s in three modes, `SO_SNDBUF` 128 KiB included). The backlog was not in
the sender's socket: `netstat` during a run showed 196,140 bytes in the writer's Send-Q and
**346,066 bytes in the relay's Recv-Q**. macOS grows a loopback socket's receive buffer past
`SO_RCVBUF`: set to 32,768 before connect (read back as 32,768), it read 326,640 after 2 s with
318,500 bytes queued (`doautorcvbuf: 1`, `autorcvbufmax: 4194304`; changing them needs root). On
a real link the backlog sits in the sender's socket, where the option acts; here it moves into the
relay's receive buffer, which no sender option sees. So S7 is not settled by this setup, and
neither is any figure that depends on where the queue forms (step 7's socket option). An
application-level bound that does not depend on kernel buffers (the receiver acknowledging what
it drew, the sender keeping at most a bounded amount unacknowledged) would hold whatever the
kernel does; that is a design question for step 6 (Q6), not a measured fact.

## S8: why the presets were not chosen

Without any link (`local`), 60 s of drags at 1280 / 4 Mbit/s / 60 fps gave **0.7 fps and a lag
p50 of 14 s** on API 31; 1024 gave 8.2 fps (lag p50 1.6 s), 720 gave 11.6 fps (0.7 s). Through
L3, the slowest link, 1280 gave 16.4 fps and lag p50 0.4 s: better than with no link at all. The
emulator's software encoder, starved of CPU by the host's load, set the rate, and the link did
not. The figures are in `batch3.out`; they are not evidence about links or presets. H.265 on
API 36: the server logged `Capture/encoding error: java.lang.IllegalArgumentException` at the
encoder's configuration, locally and on L2, and sent no frame. Chrome 155 on this Mac reports
WebCodecs H.265 (`hev1`, `hvc1`) as supported with `prefer-hardware` and not with
`prefer-software`; H.264 both ways.

## Not run

- **T2 in Chrome** (pointer → drawn frame): the CDP harness is written in step 4, where the UI
  changes, and measures the before binary (this commit) and the after one in the same run.
- The S8 presets and the step-8 link figures need a host without another lane's emulator at full
  load; Q7's preset is chosen from that rerun.
- Physical devices, a real VPN, Linux.
