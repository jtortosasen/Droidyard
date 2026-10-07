# Research: detecting and recovering from an app crash and an ANR during UI automation (2026-10-07)

> Status: done, read-only. No device, adb or downloaded code was run. Sources are read at
> source; every claim is labelled **[obs]** (read in code or in a recorded run) or
> **[inf]** (inferred, not checked on a device). Clones, shallow, under
> `scratchpad/refs-crash/`: maestro `60f9ba4` (2026-10-06), appium-uiautomator2-server
> `56bf27a`, appium-uiautomator2-driver `7a54db0`, droidrun `1935ada`, droidrun-portal
> `cc3fa91`, mobilecli `aca77be` (the engine under mobile-mcp), callstack/agent-device
> `136eb63` (the engine under tester-army-e2e). AOSP files were fetched as data from
> `aosp-mirror/platform_frameworks_base@main` (the mirror's last commit is 2025-03-27, so
> roughly Android 16 era; line numbers below are for that snapshot) and
> `aosp-mirror/platform_development@main` (Monkey).

## 1. What happened in m1m's 8.7 s tap: the reader did not die with the app

Evidence (the m1m `dy6` run, `~/Library/Caches/droidyard-dev/m1m/`):

- [obs] The app threw at **17:03:43.216** local (`runs/dy6/wire.s2c.jsonl`, reply to id 23:
  `FATAL EXCEPTION: main`, `IllegalStateException: golden app crash`, at
  `GoldenListScreen.kt:62`). `dy6-monitor.txt` (1 s samples): app pid 4495 gone at
  17:03:44; **reader pid 5314 still alive at 17:03:44, :46, :47**; gone at 17:03:49; new reader
  6387 at 17:03:50. Free memory 446–452 MB at the time, so not the low-memory killer.
- [obs] The reader cannot die from an exception in a request: `serve` catches `Throwable` per
  connection and loops (`internal/domain/device/reader/src/DroidyardReader.java:160`). It is
  an `app_process` under the shell uid, not an instrumentation in the app's process.
- [obs] The daemon kills it on purpose: a warm read that does not finish in
  `warmReadTimeout` = 3 s is `dumpUnreachable` (`persistent.go:33-34`, `:737`); `dump.go:193`
  then calls `restartReader`, which pings with a 1 s bound (`persistent.go:357`,
  `readerPingCheck` `:44-49`) and, on no answer, `killOwnReader` and a fresh start
  (`persistent.go:360`). The reader is single-threaded (`DroidyardReader.java:144-170`), so
  a ping cannot be answered while a dump is blocked.
- [obs, AOSP] Why a dump blocks: every node query that is not in the cache is an IPC to
  the **app's UI thread** (`AccessibilityInteractionController.java:135-138`: the handler is
  the ViewRootImpl's looper), and the client waits up to **5 s** for the answer
  (`AccessibilityInteractionClient.java:114` `TIMEOUT_INTERACTION_MILLIS = 5000`,
  `waitForResultTimedLocked` `:1414-1438`). Our `document()` calls
  `getRootInActiveWindow()` up to 5 times and `getChild()` per node
  (`DroidyardReader.java:313-316`, `:367-370`).
- [inf] So: the post-tap read hit the crashing app's window while its main thread was in the
  uncaught-exception path (blocked in `handleApplicationCrash`; AMS's
  `crashApplicationInner` waits on `result.get()`, `AppErrors.java:666`) or already dead;
  the query waited ~5 s; the host gave up at 3 s; ping failed at +1 s; kill; restart
  (~1.5–2 s, `spike-persistent-reader-2026-10.md` G1/first read); read. 0.3 + 3 + 1 + ~2 +
  read ≈ 7–9 s, matching 8.7 s. **Not proven**: the daemon logs no restart reason. One
  monitored crash run that records the reader's per-request timings and the daemon's
  restart reason would settle it (Recommendation R0).
- [inf] An ANR is the same mechanism without the death: the app's main thread is blocked,
  so every uncached query of its window costs 5 s, until the system's ANR dialog (a
  `system_server` window, answered promptly) takes focus.

**Answer to "why would an instrumentation-based reader die with the app"**: [obs, AOSP]
`am instrument` runs the instrumentation **inside the target package's process**; when that
process crashes, AMS finishes the instrumentation with `shortMsg = "Process crashed."`
(`ActivityManagerService.java:3346-3351`) and `AppErrors.crashApplicationInner` returns early
for a process running instrumentation (`AppErrors.java:633-639`). A reader instrumenting the
app under test therefore dies with it. Every tool studied avoids this by targeting **its own
package**, never the app's: Maestro `dev.mobile.maestro.test` → `dev.mobile.maestro`
(`maestro-android/build.gradle.kts:44`, `AndroidDriver.kt:128-134`); Appium
`io.appium.uiautomator2.server.test` → `io.appium.uiautomator2.server`
(driver `lib/uiautomator2-server/packages.ts:1-2`, server `app/build.gradle.kts:96`);
agent-device `android/snapshot-helper/AndroidManifest.xml:14` (self-targeting); mobilecli and
Droidyard use `app_process` with no instrumentation at all. Droidyard's reader already has
this property; nothing to change there.

## 2. How the studied tools handle a crash and an ANR

| Tool | App crash | ANR | On-device server lifecycle |
| --- | --- | --- | --- |
| **Maestro** (Android) | Not detected during a flow: `AndroidDriver` does not override `findAppCrash` (default `null`, `Driver.kt:133`; only `IOSDriver.kt:627` does). At **flow end** `collectCrashArtifacts` reads `logcat -v time -b crash -t 2000 -d` (`android/LogcatExtensions.kt:5-9`), parses `FATAL EXCEPTION:` / `Process:` and native `Fatal signal` / `>>> pkg <<<` (`android/crashes/LogcatReader.kt:47-100`), keeps the newest for the app since flow start (`AndroidDriver.kt:885-913`). The orchestra raises `AppCrash` only if the flow also failed (`Orchestra.kt:295-304`). | Flow end only: `logcat -v time -b main -s ActivityManager:D -d` (`LogcatExtensions.kt:13-15`), parses `ANR in <pkg>` … `Completed ANR of` with `PID:`/`Reason:` (`LogcatReader.kt:114-190`). [inf] `ANR in` is written with `Slog.e` (`ProcessErrorStateRecord.java:563`) and Slog writes the **system** buffer (`Slog.java:51`), so reading `-b main` likely misses it. No dialog dismissal anywhere (grep for `aerr_`/`responding`: none). | gRPC server inside an instrumentation of its own app, started with `am instrument -w -m … &`, retried until startup timeout (`AndroidDriver.kt:124-150`). On a gRPC death it probes adbd: alive ⇒ `DeviceServerDiedException`, else `DeviceUnreachableException` (`AndroidDeviceConnection.kt:305-334`); it marks the connection dead and **does not restart** mid-flow. Dumps **all window roots** via `UiDevice.getWindowRoots` (`ViewHierarchy.kt:54-70`), so system dialogs appear in its tree. |
| **Appium UiAutomator2** | Neither server nor driver detects the app's crash (grep: no `crash`/`ANR` in server `app/src/main`). The driver detects its **own** instrumentation's death from the `adb shell am instrument -w` subprocess `exit` event (`core.ts:399-401`), then refuses every proxied command with "instrumentation process is not running (probably crashed)" (`core.ts:60-75`). | None. A busy main thread surfaces as a root-retrieval timeout: `getActiveWindowRoot` retries `getRootInActiveWindow` for **10 s** and fails with "make sure the active window is not constantly hogging the main UI thread" (`AXWindowHelpers.java:41`, `:120-140`). | Started at session start with up to 2 attempts, 3 s apart (`core.ts:185-237`); no restart within a session. |
| **mobilecli / mobile-mcp** | `crashes list/get` reads `adb logcat -b crash -d -v year` and parses Java and native blocks (`devices/android_crash.go:84-100`); on demand only, not tied to actions. | None. | `app_process` dex with `nohup`, `adb forward`, 30 min idle exit (like ours). Host timeout **10 s** (`devices/android_rpc.go:21`); a **timed-out call is never resent and the server is not killed**; it restarts only when the server is *unreachable* (`android_device_server.go:318-340`). Single-threaded accept loop (`agents/android/java/JsonRpcSocketServer.java:60-80`). Tree from `getWindows()` roots, falling back to the active root (`UiTreeSerializer.java:41-57`). |
| **agent-device / tester-army** | Test-harness evidence only: `pidof <app>` empty next to a launcher `mResumedActivity` "is a crash", then `logcat -d -b crash` (`test/integration/android-emulator-e2e/device-evidence.ts:31-72`). | **The only tool that handles it in product code.** Before and after each interaction (`interaction-gesture.ts:227-236`) it reads `dumpsys window windows` (or `dumpsys window` on Android 16, `platform-android/src/window-state.ts:71-76`) and matches `mCurrentFocus=Window{… Application Not Responding: <pkg>}` or "isn't responding" (`platform-android/src/observation.ts:12-28`, `:110-200`). If the package is the session's app it taps the "Close app" button found by text, relaunches the app and returns a warning (before the command) or an error telling the agent to retry (after it) (`src/daemon/android-system-dialog.ts:168-232`, `:259-290`); any other system dialog is an error naming it. | Instrumentation APK of its own package, as a session over `adb forward`, one-shot fallback (prior note). |
| **droidrun / Portal** | None. | None. | Portal is an AccessibilityService in its own app, so the app's crash does not kill it. Host retries `get_state` 7 times with 1–10 s backoff (29 s total) and, after 5 failures, toggles the accessibility service and Portal's socket server (`mobilerun/tools/ui/provider.py:32-40`, `:249-292`). |
| dsh-android, codex-android-mcp (local `.refs`) | Expose `buffer: "crash"` in their logs tool (`dsh-android/src/tool-logs.ts:62`, `:465`). | dsh-android's backtrace tool tries `kill -3` + `/data/anr/` and says `/data/anr` is `system:system 0770`, unreadable by the shell user on production builds, so it degrades to `-b crash` (`dsh-android/src/tool-debug.ts:13-17`, `:418-451`). | — |

Summary: nobody reports a crash at the moment of the action except by side effects
(foreground change, a dead server); only agent-device reacts to an ANR, by window title,
and it auto-closes and relaunches. Crash text always comes from `logcat -b crash`.

## 3. Mechanisms available, with costs and caveats

| # | Mechanism | Signal and latency | API / permission | Separates the app from the reader? |
| --- | --- | --- | --- | --- |
| M1 | `logcat -d -b crash -T <epoch>` (Droidyard already parses it, `logcat.go:160-260`, plan m1i D6) | Exception, message, full stack, pid, uid; written by the crashing process before it dies (S9 G4 [obs]). One adb call, ~0.1–0.5 s [inf from S9 G7: a 5.9 MB `-b all -d` read took 0.53 s] | All APIs; no root | Yes: `Process: <pkg>, PID:` plus the uid column (D6) |
| M2 | `logcat -b events`: `am_crash` (30039: pid, process, exception, message, file, line), `am_anr` (30008: pid, package, reason), `am_proc_died` (30011), `am_kill` (30023) (`EventLogTags.logtags:17,22,34,56`) | One line per event, written by system_server; `am_anr` is written **before** the stack dump (`ProcessErrorStateRecord.java:374` vs `:535`), so it is the earliest ANR line [obs, source]. Same cost as M1, or zero with a follower | All APIs; no root | Yes, by package name; the reader never appears there (not an app process) |
| M3 | `ANR in <pkg>` / `Reason:` in the **system** buffer (`ProcessErrorStateRecord.java:446`, `:563`) | Full ANR summary with CPU usage; after the stack dump (seconds) | All APIs | Yes |
| M4 | `dumpsys window` focus line: title `Application Not Responding: <pkg>` (`AppNotRespondingDialog.java:97`) or `Application Error: <pkg>` (`AppErrorDialog.java:92`) | Locale-independent window titles; one dumpsys (~50 KB, agent-device's comment) | All APIs; on Android 16 the focus line moved from `dumpsys window windows` to `dumpsys window` (agent-device `window-state.ts:71`) [obs in their code, not by us] | Yes, by package in the title |
| M5 | The dialog in **our own tree**: buttons `android:id/aerr_wait`, `aerr_close`, `aerr_report` (ANR, `app_anr_dialog.xml:25-41`) and `aerr_restart`, `aerr_app_info`, `aerr_close`, `aerr_mute` (crash, `app_error_dialog.xml:28-60`) | Free: it is in the dump we already take; the dialog lives in system_server, so reading it does not wait on the hung app [inf] | All APIs; no root | Package not in the ids; the title text is localized ("<App> isn't responding") — pair with M4 or M2 for the package |
| M6 | `ActivityManager.getProcessesInErrorState()` called **from the reader** | `CRASHED` / `NOT_RESPONDING` with `shortMsg`, `longMsg` (crash: stack) for live processes in error state (`ActivityManagerService.java:9992-10032`). A binder call to system_server, sub-ms [inf]; does not touch the app's UI thread | Shell uid holds `DUMP` (`packages/Shell/AndroidManifest.xml:164`), so it sees every app (`ActivityManagerService.java:10002-10013`) | Yes, by process name. Only while the process is alive in that state: an ANR with its dialog up, a crash only in its short dying window |
| M7 | `dumpsys activity exit-info <pkg>` (`ApplicationExitInfo`) | Why the last process died: `REASON_CRASH`=4, `CRASH_NATIVE`=5, `ANR`=6, `LOW_MEMORY`=3 … (`ApplicationExitInfo.java:98-113`); recorded at kill (`AppErrors.java:605-610`) | API 30+ (`"exit-info"` present in android-11.0.0_r1 AMS) [obs, source]; output format not seen by us | Yes, per package |
| M8 | DropBox: `dumpsys dropbox --print data_app_crash` / `data_app_anr` (`ActivityManagerService.addErrorToDropBox`, `ProcessErrorStateRecord.java:646`) | Crash text; for an ANR, the report **with the traces file**, the only shell-readable place for the ANR's main-thread stack on user builds [inf] | Needs DUMP + PACKAGE_USAGE_STATS (`DropBoxManagerService.java:688`); shell has DUMP, PACKAGE_USAGE_STATS not checked | Yes. Large output; on demand only |
| M9 | `am monitor [-p pkg] [-s] [-c|-k]` | Synchronous callbacks: `appCrashed` (with stack), `appEarlyNotResponding`, `appNotResponding` (`ActivityManagerShellCommand.java:1838-1935`); immediate | `-p/-s/-c/-k` exist from **Android 14** only (help text absent in android-13.0.0_r1, present in 14/15) [obs, source]. Without `-c`/`-k` the controller **waits on stdin with no timeout** (`waitControllerLocked` `:2025-2030`) while AMS called it under `mGlobalLock` (`ActivityTaskManagerService.java:7205-7212`): the device's crash/ANR handling hangs until answered | Yes, by process name |
| M10 | An `IActivityController` registered by **our reader** via hidden `IActivityManager.setActivityController` | Same callbacks as M9, pushed into the reader | Shell has `SET_ACTIVITY_WATCHER` (`Shell/AndroidManifest.xml:177`). **One global slot**: `mController = controller` (`ActivityTaskManagerService.java:2388-2395`); **`monkey` sets it and clears it to null on exit** (`Monkey.java:1140`, `:785`), and Droidyard's `device.launch` runs `monkey` (`launch.go:155`), so every launch would silently unregister us; a dead controller is dropped on `RemoteException` (`ActivityTaskManagerService.java:7215-7217`). Also makes the reader something that can stall AMS if it ever blocks | Yes |
| M11 | `pidof <pkg>` | Alive or not; ~50–80 ms per adb spawn (`device-speed-2026-10.md`) | All APIs | Yes; says nothing about why |
| M12 | `UiAutomation.getWindows()` in the reader | Window list from the accessibility manager, no app IPC [inf]; shows a system error window appearing | API 21+; needs `FLAG_RETRIEVE_INTERACTIVE_WINDOWS` on the service info [inf, not checked in our reader] | Indirect |
| — | `/data/anr/traces*`, `kill -3` | Real stacks, but `/data/anr` is unreadable by shell on user builds (dsh-android note above) | Root or userdebug | — |
| — | `UiDevice` watchers | Run only inside UiAutomator selector waits; no studied tool uses them. Not applicable to a dex reader [inf] | — | — |

Related facts that shape the design:
- [obs, AOSP] First crash: no dialog unless "show first crash dialog" is on; AMS acts as if
  "force quit" was pressed (`AppErrors.java:1043-1073`, `CANT_SHOW` at `:1072`). A repeat crash shows
  the "keeps stopping" dialog, auto-dismissed after 5 min (`AppErrorDialog.java:57`).
- [obs, AOSP] `input tap` injects motion events with `INJECT_INPUT_EVENT_MODE_WAIT_FOR_FINISH`
  (`InputShellCommand.java:240-241`): a tap into a hung app waits on the app, so the tap
  itself is slow during an ANR and is often what triggers it [inf].
- [inf] adb going unresponsive during the m1l ANR fits the ANR stack dump
  (`StackTracesDumpHelper.dumpStackTraces`, `ProcessErrorStateRecord.java:535`, which signals
  the app, system_server and native daemons) on a memory-pressed software-GL emulator. Not
  measured. Nothing on the host can make the device answer faster; the useful thing is to
  name the state when the next call gets through.

## 4. Recommendation for Droidyard (ranked)

**R0. Confirm the cause before changing anything** (cost: one monitored run; risk: none).
Crash the golden app with `tapElement {id: "crash"}` under a build that logs, per warm read,
the reader's dump duration and the daemon's restart reason (timeout vs EOF vs gone).
Expected: a ~3 s timeout followed by kill-and-restart, not an EOF. If an EOF shows up
instead, the reader really died and R1 changes.

**R1. Stop killing a reader that is only waiting on the app** (cost: small Java + Go change,
a new dex digest, rerun of spike gates G1, G3, G5; risk: low–medium, touches ADR-0030's
recovery rule, which is plan m1f step 7 rather than the ADR text).
- In the reader, run `dump` on a worker thread with a device-side bound shorter than the
  host's (e.g. 2.5 s). Past the bound, answer
  `droidyard-reader: busy <ms> <active window package>` and let the worker finish in the
  background. Answer `ping` from the accept thread even while a dump runs. [inf]
  `AccessibilityInteractionClient` keeps one instance per thread (`getInstanceForThread`, `AccessibilityInteractionClient.java:121`, `:190-195`; used by
  `AccessibilityInteractionController.java:155`), so a second thread is not held by the
  first one's wait; check that during R0.
- In the daemon, treat `busy` (and a ping that answers) as "the app is not answering
  accessibility queries": no restart. Keep kill-and-restart for a reader that misses ping.
  This removes the 8.7 s penalty (est. ≤ 3 s), and the reader keeps its warm
  `UiAutomation`. mobilecli's rule (a timeout never kills or resends,
  `android_device_server.go:318-322`) is the precedent.
- Optional, same change: read the window list (`getWindows()`) first, without app IPC, and
  include the active window's type and title, so a `busy` answer can say which window
  blocked it.

**R2. Report a crash of the app when an action or read sees it** (cost: low, Go only, reuses
`logcat.go`'s D6 parser; risk: low; it adds result fields, i.e. a change to
`schema/capabilities.json`, so it needs a plan decision and probably an ADR).
Trigger: the target app's process is gone (M11), or `foreground.after` left the app, after a
`tapElement`/`inputText`, or on a `uiTree` read whose foreground differs from the last one.
Then one `logcat -d -b crash -T <action start, device clock>` with the uid filter (M1),
attached as e.g. `appCrash {pid, time, exception, message, topFrames[≤5]}` (untrusted).
Cost: one adb call (~0.1–0.5 s), paid only when the foreground changed. This is Maestro's
flow-end collection moved to the action that caused the crash. Fallback when the crash
buffer is empty: `exit-info` (M7, API 30+) to name a low-memory kill or an ANR kill.

**R3. Name an ANR, or a crash dialog, over the app; do not dismiss it** (cost: low; risk:
low; result-field change as R2).
- From the tree we already read: a node with `android:id/aerr_wait` or `aerr_close` (M5)
  ⇒ `systemDialog {kind: "anr" | "crash"}`. For the package, one `logcat -d -b events -T
  <since>` for `am_anr`/`am_crash` (M2), or the `dumpsys window` focus title (M4).
- From R1's `busy`: "the app's main thread is not answering (possible ANR in progress)",
  which arrives before the dialog does.
- Leave the decision to the agent: it can tap `aerr_wait` or `aerr_close` with
  `tapElement {id: …}` (the m1l agent recovered by tapping "Wait"). agent-device's automatic
  close-and-relaunch hides the state and is a harness decision; ADR-0004 puts that loop
  in the external agent.

**R4. Reader-side error poll, if R2/R3's adb calls prove too slow or too late**
(cost: medium, hidden-API reflection in the dex; risk: medium). A reader request `errors`
that returns `getProcessesInErrorState()` (M6): a binder call, no app IPC, no global slot,
crash stack and ANR reason included. Polled with each warm read, it costs about nothing.
It only sees processes still in error state, so it complements R2's logcat read rather
than replacing it.

**Do not adopt now**
- `am monitor` (M9): unsafe below API 34 (AMS waits on stdin), and on 34+ it takes the
  single controller slot that our own `monkey` launch clears.
- A reader `IActivityController` (M10): the same slot problem, plus a new way to stall
  system_server. Reconsider only if `device.launch` stops using `monkey` and a push signal
  turns out to be needed.
- Automatic dismissal or relaunch, droidrun's accessibility toggling, DropBox or
  `/data/anr` on every action (M8 belongs in an on-demand `device.logs` option at most).
- Any instrumentation that targets the app under test: it dies with the app (§1).

## Not established

- No step of R0–R4 was run. All latencies above that are not marked [obs] are estimates.
- That `getRootInActiveWindow` returns the ANR dialog's root (focus) rather than the hung
  app's window while the dialog shows; that the dialog's nodes keep the `aerr_*` ids through
  the accessibility tree; the `exit-info` output format; whether shell passes DropBox's
  PACKAGE_USAGE_STATS check; per-thread independence of blocked accessibility queries.
- AOSP line numbers are from the 2025-03-27 mirror; Android 16/17 behaviour may differ.
