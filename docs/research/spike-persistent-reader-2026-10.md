# Spike: the persistent UI reader kept running (milestone 1f step 1)

Tests ADR-0030 §Decision on the emulator: the same dex kept running, reached over
`adb forward`, ending itself after an idle period and releasing `UiAutomation`. The
one-shot reader of ADR-0029 stays and is measured against it.

> **Status: done, 2026-10-05. Every gate passed; ADR-0030's design stands.** The Java
> and the dex are the implementer's; the runs are the main session's, on the emulator
> under the plan's §Emulator rules, all observed. Two runs: the first on digest
> `31edb33b…` (G1–G5); review then found that one stalled connection blocked the reader
> and its idle exit, the fix round changed the dex, and the second run (on the pinned
> digest below) repeated G1, G3 with reads after the first fresh one, G5 with stalled
> clients, and a one-shot. G4 (Qustodio) ran on the first digest only.

- **Date built**: 2026-10-05
- **Built by**: the implementer (Java and dex); the G1–G5 runs are the main session's.
- **Subject of the runs**: `Pixel_9a`, API 36.1, `emulator-5554`, the device as it is
  (plan E7). Qustodio is the app G4 watches.
- **Dex digest**: `91eab8d9f89d3490a8c5b79b73078a271cd614b82dbbcfc45f9c1bf3704821db`
  (first 12 hex `91eab8d9f89d`, the `<d12>` below). Reproduced by `build.sh` with javac
  17.0.11 (`--release 11`), build-tools 37.0.0, android-36.

## Built: what the dex now does (observed)

`reader/src/DroidyardReader.java`. No arguments is ADR-0029's one-shot run, unchanged.
`serve <abstract-socket-name> <idleSeconds>` is ADR-0030's persistent mode:

- `UiAutomation` is connected **once**, with `FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES`.
- A `LocalServerSocket` is bound in the abstract namespace under the name given.
- One request per connection, newline-terminated, response read to EOF:
  - `ping` -> `ok <pid>`;
  - `dump` -> the same XML document the one-shot writes (same `waitForIdle(100, 500)`,
    same node/flag/attr code, same `NO_ROOT` handling as the line
    `droidyard-reader: no active window root`);
  - `quit` -> the reader disconnects and exits;
  - anything else -> `droidyard-reader: unknown request`.
- Only a `dump` resets the idle timer. After `idleSeconds` without one the reader
  disconnects `UiAutomation` and exits.
- The idle thread wakes a blocked `accept()` by connecting to the reader's own socket;
  the main thread then disconnects and exits. Assumed, not observed: that closing the
  server from another thread would not wake it (the reason given for the self-connect).
- Each accepted connection has a 5 s `SO_TIMEOUT`, so a client that never sends (or
  never reads) cannot hold the single-threaded loop, `quit` or the idle exit; the socket
  is bound before `UiAutomation` connects, so a second start fails at the name; the idle
  clock is `SystemClock.elapsedRealtime()`.
- A response is read to EOF: the XML document, `ok <pid>`, nothing (`quit`, or a request
  that met the idle exit), or an error starting with `droidyard-reader:` (the no-root
  case is `droidyard-reader: no active window root`, not an exit status).

`reader.go`'s `SHA256` is the digest above; `TestEmbeddedDexMatchesThePinnedDigest` and
`TestBuildScriptReproducesTheDex` pass. `build.sh` now hands `d8` every `.class` javac
emits (the serve mode has nested classes), not only the top-level one.

Command forms the daemon (step 5) and the runs below use:

- Push: `adb -s emulator-5554 push <reader.dex> /data/local/tmp/droidyard-reader-<sha>.dex`
- One-shot: `adb -s emulator-5554 exec-out CLASSPATH=/data/local/tmp/droidyard-reader-<sha>.dex app_process /system/bin DroidyardReader`
- Serve: `adb -s emulator-5554 shell "CLASSPATH=/data/local/tmp/droidyard-reader-<sha>.dex nohup app_process /system/bin --nice-name=droidyard-reader-<d12> DroidyardReader serve droidyard-reader-<d12> <idleSeconds> >/dev/null 2>&1 &"`. Observed: `--nice-name` before `/system/bin` is a VM argument, and the runtime exits 1 (`Unknown argument: --nice-name=…`); it goes after the class directory. `CLASSPATH=` goes before `nohup`, which does not take assignments.
- Forward: `adb -s emulator-5554 forward tcp:0 localabstract:droidyard-reader-<d12>`
- Requests: the out-of-tree driver `$TMPDIR/droidyard-reader-driver/main.go`
  (`ping`, `dump`, `time <port> <n>`, `quit`).

## Runs (2026-10-05, Pixel_9a API 36.1, observed)

### First run, digest `31edb33b…`

All under the plan's §Emulator rules; E1 (5037's pid), E2 (Qustodio's pid and logcat)
recorded before and after. Each row is filled with the number or the observation.

| Check | What to record | Result |
| --- | --- | --- |
| G1, reader outlives its shell | the exact start command; `adb shell pidof` 60 s after the shell returned; `ps -A -o PID,NAME` | **pass.** The Serve form above; `adb shell` returned 798 ms after the push began; `pidof` named pid 5329 60 s later. |
| G2, warm reads against the one-shot | 20 sequential `dump`s on Settings: per-read ms, median, p95; 5 one-shot runs (push, `exec-out app_process`, `rm`), median; first read (push to first document) | **pass.** Warm, Settings (29 KB): median 14.4 ms, p95 460 ms, min 11.3, max 574.8 (the slow reads are not attributed: the per-read list was not kept; `waitForIdle`'s 500 ms bound is the likely cause). One-shot, same screen: 1477, 1396, 1379, 1408, 1450 ms, median 1408. First read, push to first document: 1880 ms (its `dump` 150 ms). |
| G2, RSS and `forward --list` | the reader's RSS; the line format of `adb forward --list` | RSS 38 184 kB after start and unchanged after the 10-minute hold. `forward --list`: `emulator-5554 tcp:63552 localabstract:droidyard-reader-31edb33baa07`. |
| G3, freshness after a real tap | 5 trials: warm read, `adb shell input tap` a Settings row, read every 250 ms until the new title shows, `BACK` between; time-to-observe and `input tap`'s own latency per trial | **pass, 5 of 5.** "Network & internet" tapped from a warm read's bounds; the new screen (has "Internet", no "Connected devices") seen 2010, 1831, 1840, 1807, 1886 ms after the tap began; `input tap` itself took 718, 642, 649, 644, 666 ms. Not checked: a stale read after one had shown the new screen (the loop stopped at the first fresh one). |
| G4, the observed kill condition | Qustodio foregrounded, untouched ≥ 4 min; reader started; logcat's `Registering … flags=0x1` timestamp at least 4 min after Qustodio's last start; held ≥ 10 min with a read at the end; Qustodio's pid and logcat | **pass.** Qustodio in front since 09:44:51 (re-sent 09:52:23 to the same instance), its process (pid 1719) running since long before; `Registering UiTestAutomationService … flags=0x1` at 09:56:36; held to 10:07:41 with Qustodio's pid checked every 30 s, then a read of its screen (14.5 KB, 35 ms). Pid 1719 throughout; no `FATAL EXCEPTION` and no `DetectAccessibilityMalfunctionUseCase` line in logcat. |
| G5, the idle exit | started with `idleSeconds` 60: gone within 70 s of the last `dump`; then a one-shot run succeeds (UiAutomation released); `quit` ends it at once | **pass.** Gone 60.8 s after the last `dump`; a one-shot run right after printed the hierarchy (exit 0, 29 KB). `quit` ended the earlier reader in 699 ms (`pidof` polled every 100 ms). |
| One-shot on the emulator | one one-shot run prints a hierarchy | **pass** (G2's five runs and G5's). |

### Second run, digest `91eab8d9…` (after the fix round)

| Check | Result |
| --- | --- |
| G1 | **pass.** `ps -A -o PID,NAME`: `6869 droidyard-reader-91eab8d9f89d`; `pidof` 60 s later: 6869. |
| G3, with reads after the first fresh one | **pass, 5 of 5.** New screen seen 1475, 1477, 1538, 1390, 1500 ms after the tap began; then 4 more reads 250 ms apart in each trial: 0 of 20 showed the old screen. |
| A stalled client | **pass.** With a connection open that sent nothing, a `ping` on a second one answered after 5605 ms (the 5 s timeout, then the answer). |
| G5, with a stalled client opened after the last `dump` | **pass.** Gone 60 277 ms after the last `dump`; a one-shot right after printed the hierarchy (exit 0, 29 KB). |
| Emulator rules | adb server pid 48737 and Qustodio pid 1719 unchanged; no reader, file or forward left; no `FATAL EXCEPTION` or malfunction line. |

A stalled connection delays the next request by up to 5 s. Step 7's rule (no `ping`
answer within 1 s → kill by pid and start again) would therefore restart a reader that
one stalled client is holding: a recovery, not a failure, but worth knowing in step 5.

## What follows

A warm read is ~100 times cheaper than a one-shot run (14 ms against 1.4 s), so
`tapElement`'s polls cost little; what a tap costs now is `input tap` itself (~650 ms,
a JVM started by `input`) and the app's own transition. Settling those is step 6's and
spike S6's, not this one's. ADR-0030's "tens or hundreds of milliseconds" holds: the warm median is 14 ms, though
about 1 read in 20 took ~0.5 s (p95 460 ms). One first read took 1.9 s.

## Stop conditions (from the plan)

ADR-0030 is invalid, and the milestone stops for the owner, if G1 shows the reader dies
with its shell; G3 shows the pre-tap tree after a read showed the new screen, or 2 of 5
trials observe nothing within 3 s; G4 shows Qustodio disturbed; or G5 shows the idle
exit does not release `UiAutomation`. A warm median above 650 ms (half the one-shot's
~1.3 s) stops the milestone for the owner's decision too. On a stop the dex change is
discarded and only this note is committed.

## Not run (labelled)

- The 30-minute idle period: G5 uses 60 s; 30 minutes is not observed.
- Emulator rules held: the adb server's pid was 48737 before and after, Qustodio's 1719;
  no reader or `droidyard-*` file was left on the device; only our forwards were removed.
- The first attempt failed on the `--nice-name` position (above); it registered no
  `UiAutomation` and left one forward, removed by its port.
- G4 on the second digest: not rerun. The fix changed the socket handling, the idle
  clock and the bind order, not how `UiAutomation` is connected or its flag.
- A client that sends `dump` and never reads the response: not exercised (whether
  `SO_TIMEOUT` bounds writes on Android's `LocalSocket` is not established).
- Other API levels, a physical device, Linux.
- Concurrent requests to one reader: the daemon serialises per device (step 5).
