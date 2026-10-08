# Spikes S8 and S9: the emulator lifecycle and logcat

Milestone 1i step 1 ([plan](../plans/m1i-device-logs-and-emulator.md)). S9 asks whether
an app's logs, and its crash, can be read and attributed after its process died
(`adb-device.md` §To verify); S8 how a detached emulator is started, listed, stopped and
wiped (`emulator.md` §To verify).

> **Status: done, 2026-10-07, macOS, emulator 36.6.11, adb 1.0.41 (36.0.0-13206524).**
> Everything below was observed on the scratch AVD `droidyard_m1i`
> (`system-images;android-31;google_apis;arm64-v8a`) in an isolated `ANDROID_AVD_HOME`,
> with the flavoured fixture's `paidStagingDebug` (uid 10146). The owner's `Pixel_9a`
> (API 36.1) was only read, and only as prose. Recordings:
> `internal/domain/device/testdata/logcat/`, `internal/domain/emulator/testdata/`.

Neither stop condition held: the emulator outlives its starter (G9), and the Java crash
is readable after the process died (G4).

## Logcat (S9)

| Fact | Observed |
| --- | --- |
| G1 line layout | `-v threadtime,epoch,usec,uid` combine: `<epoch>.<usec> <uid> <pid> <tid> <lvl> <tag>: <msg>`, the epoch right-aligned in a fixed width; the uid column is numeric (`10146`), matching `pm list packages -U`'s `uid:10146`, `root`/`1000` for system lines; `printable` changes nothing for ASCII. `-v threadtime,uid` keeps the calendar date |
| G2 `--uid=<uid>` | accepted on API 31; passes the crash buffer's lines too |
| G3 `-T` | accepts an epoch value (`1791336908.778`) and a calendar value (`10-07 03:35:24.000`); the first line shown is at or after it |
| G4 Java crash | after the process died (`pidof` empty): `-b crash` holds `FATAL EXCEPTION: main`, `Process: <pkg>, PID: <pid>`, the exception and its stack, every line with the app's uid; not in main or system. The app's `created` line stays in main; `ActivityManager: Start proc <pid>:<pkg>/u0a146 for …` (uid 1000) in main/system |
| G5 native crash | `run-as <pkg> kill -SEGV <pid>`: `libc: Fatal signal 11 …` and `DEBUG: pid: …, name: …  >>> <pkg> <<<` / `signal 11 (SIGSEGV)` lines, uid 10146 (crash_dump runs as the app), in the crash buffer; `Zygote: Process <pid> exited due to signal 11` (uid root). Whether an app uid can write to the crash buffer: **not settled** — API 31's `log` has no `-b`, and liblog was not tried |
| G6 | `pidof`, `date +%s.%N` as on 1f; `logcat -v nonsense` exits 0 with no stderr and falls back to a default format, so an invalid format is not detectable by exit status; killing the host-side `adb logcat` client ends the device's `logcat` process within 2 s; a 5 s follow of an idle app after launch showed **no** line of its uid |
| G7 | `logcat -g`: main, system, crash, kernel 2 MiB rings each; a whole `-b all -d` read of 5.9 MB took 0.53 s |
| G1 on API 36.1 (`Pixel_9a`, ps16k, 2026-10-08, plan m1p step 4) | the same layout and numeric uid column (`root` for root's lines); 4 lines in 37,930 of `-b main,system,crash` print no `: ` after the tag (`PluginActionManager[…PLUGIN_NETWORK_FETCHER_ACTIONS]:Found 0 plugins`, while a sibling prints `…]: Found 0 plugins`), which failed every `device.logs` read there until 1p; cause not known. Recording: `testdata/logcat/api36-threadtime,epoch,usec,uid.txt` |
| G7 on API 36.1 | `logcat -g`: main, system, crash, kernel 2 MiB rings each, as on API 31 |

## The emulator (S8)

| Fact | Observed |
| --- | --- |
| G8 | `avdmanager create avd` under `ANDROID_AVD_HOME` writes only there (`~/.android/avd` unchanged; it fetched the remote repository index); `emulator -list-avds` lists only `droidyard_m1i`, nothing on stderr. `ANDROID_EMULATOR_HOME` or `ANDROID_SDK_HOME` alone do **not** redirect the listing (all owner AVDs listed); `ANDROID_AVD_HOME` wins |
| G9 | started through `setsid`, `emulator` execs into `qemu-system-aarch64-headless` (with `-no-window`), reparented to init in its own session: it outlives the starter |
| G10 | a second `-avd droidyard_m1i` while running: exit 1, `FATAL | Running multiple emulators with the same AVD is an experimental feature.`; `-avd nope`: exit 1, `ERROR | ANDROID_AVD_HOME is defined but there is no file nope.ini in $ANDROID_AVD_HOME`, no fallback to `~/.android/avd` |
| G11 | headless boot: listed `offline` at 3 s, `device` at 8 s, `emu avd name` answering from 3 s, `sys.boot_completed` 1 at 21 s (13 s on the wiped cold boot) |
| G12 | `emu kill` answers `OK: killing emulator, bye bye` / `OK`; the qemu process is gone within 2.2 s and the serial leaves `devices`; `multiinstance.lock` stays in the `.avd` and does not block a new boot; `emu kill` works while the serial is still `offline` mid-boot |
| G13 | `-wipe-data`: the launcher tries `default_boot`, fails, and cold-boots; the fixture's package is gone |
| G14 | `emu avd name` → `droidyard_m1i\r\nOK\r\n` |
| G15 | the shipped `device.uiTree` and `device.tapElement {text: "Crash"}` work on API 31 through a daemon (the fixture crashed); **`device.launch` failed**: `monkey -p <pkg> -c LAUNCHER 1` exits 251 with `SYS_KEYS has no physical keys` on this keyless image; `--pct-syskeys 0` launches it (exit 0). Fixed in `launch.go` with this step |
| G16 | the adb guard: the eleven refused rows exit 97 having run nothing, the passed rows pass; a daemon with the guard as `ADB` reports the same client version; a long command's pid is `adb` itself (it execs); the run's `refused.log` stayed empty |

## Not run

A native crash's tombstone file; an app writing to the crash buffer through liblog;
`ANDROID_HOME` vs `ANDROID_SDK_ROOT`; Linux; images other than API 31 for the emulator
facts.
