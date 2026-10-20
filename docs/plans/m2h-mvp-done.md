# Plan: Milestone 2h — the MVP's three open Definition of done items

- Status: Done
- Date: 2026-10-11
- Approved: 2026-10-11, by the main session under the owner's standing instruction to apply the recommended option (Delegated); the owner approved the milestone and the download consent in writing the same day.
- Milestone: "close the three unticked items of `docs/roadmap/mvp.md` §Definition of done so the MVP is closed. Nothing new beyond what those three need." (owner, 2026-10-11, approving the milestone; `docs/README.md` §Where the work stands does not name it yet, the close adds the line)
- Phase: `docs/roadmap/mvp.md` §Definition of done (items 3, 6, 8), §Validation target (the two fixtures), §Metrics §Other measures (idle memory)
- Contract: ADR-0022 §Decision 1–4, §Consequences; ADR-0018 §Consequences (the KGP axis); ADR-0019 §Lifetime (the daemon does not idle-exit); ADR-0012 §Durability classes (the 15-minute ephemeral TTL; m1e Q5 left open whether idle includes that window, and m1e step 9 measured both sides); ADR-0025 criterion 7; `gradle.md` §Error model, §Lifecycle (`gradle.IdleTimeout`, 10 min), §Version detection item 4 (`expectedSources`), §Testing (fixtures pin versions by rewriting them in a copy); `process-lifecycle.md` §Idle shutdown and its ownership table; `AGENTS.md` §Conventions (decisions go to ADRs); the `adr` skill; `docs/research/spike-s1-error-formats.md` §Run 2 (F11); ratifications row "Validation target" (CI keeps two KGP fixtures; no CI exists, register row on the CI matrix).

**The owner should see:** D1 (the second fixture is the golden sample with its Kotlin plugin rewritten in a scratch copy, not a second sample), D2 (one script in the repository, `samples/mvp-dod.sh`, is the reproducible proof of items 1 and 2), D4 (idle means after the 15-minute artifact TTL; the figure 30 s after the last call is reported against the budget too), D6–D7 (what the deviation audit covers, that its evidence is a ledger in this plan, and that DoD 8's tick names that boundary: unrecorded divergences stay a register row), D8 (missing ADRs are accepted by the main session, Delegated, as ADR-0054 was). A download Gradle needs for the fixtures is allowed and does not stop the milestone: the coordinator relayed your written consent of 2026-10-11 (the planner did not see it itself; confirm if that is wrong). What was downloaded and its size are recorded as evidence (K3).

## Delegated decisions

- D1. **The KGP ≥ 2.4 fixture** is `samples/golden` copied by `git archive`, with exactly three strings rewritten from `2.0.21` to `2.4.20`: `org.jetbrains.kotlin.android` and `org.jetbrains.kotlin.plugin.compose` in `settings.gradle.kts`, `kotlin-gradle-plugin` in `build-logic/build.gradle.kts`. Nothing else changes (Gradle 8.13, AGP 8.13.2, the Compose BOM). The one that decides the cell is `build-logic`'s `implementation` dependency: the convention plugin applies both Kotlin plugins by id from that classpath (`GoldenAndroidConventionPlugin.kt`), so the settings versions are never resolved (which is why the 2.0.21 compose marker is not cached); they are rewritten so the copy does not contradict itself. Source: `gradle.md` §Testing ("fixtures pin versions by rewriting … the plugin versions"), `mvp.md` §Validation target ("two fixtures of the same shape"); KGP 2.4.20 is S1 run 2's cell (F11) on the same Gradle and AGP. A rewrite that does not replace exactly three occurrences, or leaves a `2.0.21`, fails. Rejected: a second committed sample (a full duplicate that drifts); a CI fixture (no CI, register row on the CI matrix).
- D2. **The proof is a script kept in the repository**, `samples/mvp-dod.sh`, beside the sample it drives and outside `samples/golden` (so no archive of the sample carries it). Why: the owner's brief asks for a reproducible measurement script; `mvp.md` §Validation target: the golden sample wants the measurement reproducible from the commit under test; `prove-change` prefers a durable form over a one-off session, and DoD 6's budget is worth rechecking after later milestones. It drives a built binary through `droidyard call` under a scratch state, the daemon end to end as m1l G3 and m2b G3 did, and its launch, identity check and cleanup are `verify-daemon` §Launch, §Doctor and §Cleanup transcribed (its comments cite them); step 3 adds one line to `verify-daemon` §Drive naming it, so the recipe stays the one home and the script its DoD run (§Keeping it true). Its footprint mode needs K5's guard and AVD, supplied by the environment: rerunnable from a checkout plus those. Nothing lints `samples/`, so the script is kept true only by being rerun (Unverified). Rejected: a gated Go test in `cmd/droidyard` (its `TestMain` disables JVM discovery for every re-executed daemon, and the footprint needs 17 minutes and an emulator, beyond `go test`'s default timeout and the tests' no-device rule); the gated real-Gradle harness in `sidecar/sidecar_gradle_test.go` (`DROIDYARD_GRADLE_TESTS=1`, cells C1, C2, C4), which drives the sidecar alone, so neither the core's extractor, merge and registry nor the daemon's idle stop would be under test; no script, the main session running `verify-daemon` by hand as m1l, m2b and m2c step 13 did (smaller: steps 2 and 4 go; the owner's brief asked for the script, and the next budget check would start from scratch).
- D3. **The injected error** is m1l's: `droidyardAcceptanceMissing()` on the line after `// golden-path: compile-error site`, so `MainActivity.kt:35:9` on both cells, edited in the scratch copy by the script (the edit's tool is not under test).
- D4. **The footprint scenario**: the golden path by CLI on the KGP 2.0.21 cell (open, modules, assemble, the D3 error and `build.errors`, restore and assemble, `emulator.start` of the scratch AVD, install, launch, logs, one screenshot, `uiTree`, one `tapElement` with `expect`, one `inputText`), then no call for 16 minutes, sampled every 30 s, under `caffeinate -i` (a host sleep would stretch the timers). The DoD figure is the final sample, after the sidecar's 10-minute idle exit and the artifacts' 15-minute TTL: the reading m1e Q5 left open, taken here (Delegated); the figure 30 s after the last call and the window's maximum are reported beside it, each against the budget. The Gradle and Kotlin daemons are sampled 30 s after the last build ended: the sample's `org.gradle.daemon.idletimeout=60000` ends the Gradle daemon about a minute later, a property of the sample (Gradle's default keeps it 3 hours), stated as such and not changed (K3). RSS is `ps -o rss=` in KiB; the budget check is `< 48,828` KiB (50 × 10⁶ bytes, the stricter reading). The binary is the `droidyard_nodump` build (K1), labelled so. No UI tab or MCP session is connected during the window (Unverified). After the final sample, one `build.assemble` of `:app` (up to date) must succeed with a new sidecar pid in `processes.json`, and `daemon.log` must hold no `<sidecar id> (pid <old>) exited unexpectedly` line and no `killed; its group outlived` line for the old pid: the supervisor logs the first for every exit nobody asked for (`internal/process/supervisor.go`, `wait`) and nothing for an idle stop (`idleExpired` → `terminate`), so the log tells an idle stop from a crash. The workspace state does not: a crash degrades the workspace only under a running build (m2c D21), and the next sidecar start clears it. `project.modules` would not do: it answers from the model cache without a sidecar (`gradle/client.go` `Model`).
- D5. **Attribution** follows `process-lifecycle.md`'s table: ours, the core and the Gradle sidecar; not ours, the Gradle daemon and the Kotlin compile daemon (tool-owned), the adb server and the emulator (external). Each is identified by pid lists taken before and after the run (a pid present before is reported as reused, not the run's).
- D6. **The audit's boundary.** Population: in each of the 23 plans with Status `Done`, every entry of its `## Delegated decisions` or `## Delegated choices` (m1q) section (D items, or the header's owner decisions where it has neither), every design question with its answer, every R item of a `## Readings` or `## Readings the steps take` section (m1c–m1l record their readings of the design there, about 79 items; code cites them, `client.go` "plan m1g R5"), and every dated entry under `## Changes`. A unit is a **deviation** when it made the shipped behaviour depart from an accepted ADR, or from an area document (`docs/architecture/`, `docs/subsystems/`, `docs/interfaces/`) on a decision of the kind the `adr` skill §When names (a capability, an error code, a dependency that shapes a contract, a file format or path, a default a consumer observes). Classes: **N** not a deviation (fills a gap, implementation detail, order or split, run-only choice, or a roadmap scope change the owner recorded in `mvp.md`); **A** a deviation recorded by an ADR (a new or superseding ADR, or a pre-1q `Amended:` line); **M** a deviation with no ADR (an ADR is written in step 7); **X** a defect against an ADR rather than a choice (a register row, existing or added; no ADR, no code fix here). Out: divergences no plan recorded (finding them needs a code-versus-design review of every area, a milestone of its own), the frozen records' own wording, ADR `Amended:` lines themselves (already ADR-recorded).
- D7. **The audit's evidence** is §Audit ledger below: one row per Done plan, its unit counts by kind, and only its A, M and X units by id with their record. N units are implied by the counts. A reviewer re-derives a plan's counts from its sections and spot-checks its N units; no separate document is added (`AGENTS.md`, density over volume).
- D8. **The ADRs step 7 writes** follow the `adr` skill (Proposed, then accepted by the main session under the owner's standing instruction, labelled Delegated, as ADR-0054 was), with the superseded ADR's header line, the index row and the area document corrected in the same change.

## Run rules

- K1. `B=~/Library/Caches/droidyard-dev/m2h`. Each run: a sample copy by `git archive HEAD samples/golden` (the script refuses if `git status --porcelain samples/golden` is not empty, or if its output directory is inside the repository) plus `local.properties` naming the host SDK; `DROIDYARD_STATE_DIR` and `DROIDYARD_CONFIG_DIR` both under a fresh short `/tmp/dyh.*` directory (`sun_path`); the binary built with `-tags droidyard_nodump`; `verify-daemon` §Launch's `pre-health` must fail. Never the real state root.
- K2. The owner's daemon (pid 15130) and the shared adb server on 5037: pid and start time recorded before and after each run and unchanged; nothing is killed by name; `droidyard stop` only under the scratch environment.
- K3. Gradle uses the user's `~/.gradle`. A download Gradle needs for the two cells is allowed (the owner's written consent, 2026-10-11, relayed by the coordinator) and recorded: a stamp before each run, and after it `find ~/.gradle/caches/modules-2/files-2.1 ~/.gradle/wrapper/dists -newer <stamp> -type f` lists what arrived, kept with each file's size and the total as evidence; an empty list is the expected case once step 1 has run. No other network use. Gradle and Kotlin daemons are never stopped. `ANDROID_SERIAL=droidyard-m2h-none`; the only tasks are `:app:assembleDebug` (no install, uninstall, connected or device task).
- K4. m1l S3's SDK stamp: at the end `find ~/Library/Android/sdk -maxdepth 3 -newer <stamp>` prints nothing but `.knownPackages`.
- K5. Device runs (step 5 only): the scratch AVD `droidyard_m2a` under `ANDROID_AVD_HOME=~/Library/Caches/droidyard-dev/m2a/avd`; m2b's guard (`~/Library/Caches/droidyard-dev/m2b/bin/adb`, the pattern m2c reused) copied to `$B/bin/adb` (names changed to m2h) as `ADB`, serials online at the start protected; `ANDROID_ADB_SERVER_PORT=5199`, so the adb server is the run's own and is ended with `adb -P 5199 kill-server` at the end (5037 untouched); the run ends with `emulator.stop` on its serial; `~/.android/avd` and the owner AVDs' images unchanged. The implementer never runs a device phase.
- K6. Cleanup per `verify-daemon` §Cleanup: zero orphans; the Gradle and Kotlin daemons and the adb server listed as excluded.

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| DoD 3: `build.errors` structured on both fixtures, KGP < 2.4 through the console extractor | yes | steps 1–3 |
| §Validation target: two fixtures of the same shape, one below KGP 2.4, one at or above | yes | D1, step 1 |
| DoD 6 and §Other measures: idle memory per process, core under 50 MB, sidecar stopped when idle | yes | steps 4, 5 |
| DoD 8: every deviation recorded as an ADR | yes, within D6's boundary | steps 6, 7 |
| §Other measures: orphan count after close | recorded only | K6 in steps 3, 5 (DoD 5 is ticked since 1c) |
| §Other measures: time to first useful result | no | not a DoD item; register row added at step 8 |
| DoD 1, 2, 4, 5, 7 | no | ticked (m1l, m2b, 1c, m1m/m1o) |
| The rest of the S1 matrix (Gradle 9, AGP 9, KGP 2.3), kapt, a KSP error | no | spike S1 §Still to run; register |
| The milestones after 2g that `mvp.md` lists under Phase 2 | no | untouched; whether they become v1 is the owner's |

## Starting point

Code at `045d158` (2g merged). The event path exists: the sidecar forwards Problems events (`event.build.problem`, `sidecar/src/Build.kt`, gated on `problemEvents`); `internal/domain/build/build.go` keeps them in `buildSink.problems` and passes them to `extract.Extract` as `ProblemEvents` (source `event`); `expectedSources` says `kotlin: problems` when `problemLocations` and the probe's KGP ≥ 2.4. `extract_test.go` `TestKotlinKGP2420Console` pins the merge on S1 run 2's OwnerAppA recording (level 4: one error, `source: event`, `UNRESOLVED_REFERENCE`). No KGP 2.4 build has run through the daemon. The golden cell's `build.errors` is proven at level 5 (m1l, m2b: `kotlin`, `console`, `MainActivity.kt:35:9`). The sidecar's idle stop is `process.Supervisor` with `gradle.IdleTimeout` (10 min, not configurable: register row on configuration v1's deferrals); core RSS was 33.6 MB in m2c step 13, the sidecar's exit not observed. `samples/` holds only `golden/`; no script exists.

Host cache (2026-10-11, `ls ~/.gradle/caches/modules-2/files-2.1`): `kotlin-gradle-plugin`, `compose-compiler-gradle-plugin`, `kotlin-compose-compiler-plugin-embeddable`, `kotlin-compiler-embeddable`, `kotlin-build-tools-impl`, `kotlin-daemon-embeddable`, `kotlin-stdlib` at 2.4.20; the `org.jetbrains.kotlin.android` and `org.jetbrains.kotlin.plugin.compose` markers at 2.4.20 (the compose marker not at 2.0.21, though golden builds); `wrapper/dists/gradle-8.13-bin`. Scratch AVD `droidyard_m2a` exists.

Baseline on 2026-10-11 at `045d158` (macOS): `go vet ./...` printed nothing; `gofmt -l .` nothing; `go test ./...` green. Live corpus 26,309 words of a 26,317 budget (`TestLiveCorpusWithinBudget`): step 8's document edits must cut or raise it with a reason.

## Acceptance

- [x] A1. KGP 2.0.21 through the daemon: `build.errors` holds one error, `tool: kotlin`, `source: console`, `file: app/src/main/kotlin/dev/droidyard/golden/MainActivity.kt`, `line: 35`, `column: 9`, the message naming `droidyardAcceptanceMissing`, and no error record with `source: event`; the failed build's result says `kgp: 2.0.21`, `expectedSources.kotlin: console`. (A whole-build `extraction.sources` is not asserted: an AGP or Gradle warning event would add `event` for a reason unrelated to Kotlin; it is recorded.) Source: DoD 3; ADR-0022 §Decision 2. Proven by: step 3
- [x] A2. KGP 2.4.20 through the daemon: the same file, line, column and named reference in one error, `source: event`, `problemId: UNRESOLVED_REFERENCE` (the console's `e:` line merged into it, not a second record); `kgp: 2.4.20`, `expectedSources.kotlin: problems`, `extraction.sources` holding `event` and `console`. Source: DoD 3; ADR-0022 §Decision 1, 3, §Consequences; F11. Proven by: step 3
- [x] A3. Both cells built in scratch copies, every download recorded with its size (K3), and the restore building `success`. Source: AGENTS.md §Before touching the Gradle sidecar; owner's rules. Proven by: step 1, step 3
- [x] A4. A per-process footprint table (core, sidecar, Gradle daemon, Kotlin daemon, emulator processes, adb server; pid, ours or not, RSS 30 s after the last call or, for the Gradle and Kotlin daemons, after the last build, at the end or when it exited, the window's maximum); the core's final RSS under 48,828 KiB, with its 30 s and maximum figures stated against the budget; the sidecar gone between 10:00 and 11:00 after its last Gradle operation and its record gone from `processes.json`; no unexpected-exit or forced-kill line for the old pid in `daemon.log`; then a `build.assemble` succeeding on a new sidecar pid. Source: DoD 6; `mvp.md` §Other measures; `gradle.md` §Lifecycle; `process-lifecycle.md` §Idle shutdown. Proven by: step 5
- [x] A5. §Audit ledger complete for the 23 Done plans under D6; every A unit names its record, every M unit its ADR written in step 7, every X unit its register row. Source: DoD 8. Proven by: step 6, step 7
- [ ] A6. `mvp.md`'s three boxes ticked with evidence, DoD 8's naming D6's boundary and the register row for unrecorded divergences; register rows "`build.errors` on a KGP ≥ 2.4 fixture" and "Idle footprint per process" closed by 2h; the sidecar-memory row updated; the plans index row and the §Where the work stands line. Source: `milestone-workflow` §Close. Proven by: step 8

## Steps

### 1. The KGP 2.4.20 cell builds, offline or after a recorded download

- Builds: nothing in the repository; evidence under `$B/s1/`. Main session.
- Implements: D1; K3, before anything rests on the cell.
- Check: two `git archive` copies at `HEAD` (K1), one rewritten per D1 (three replacements counted, `grep -c 2.0.21` 0 after); in each, `~/.gradle/wrapper/dists/gradle-8.13-bin/*/gradle-8.13/bin/gradle --offline -p <copy> :app:assembleDebug` with `ANDROID_SERIAL` per K3 and JDK 17. If `--offline` fails for a missing artifact, the same command without `--offline` downloads it (K3's list and sizes recorded), and a fresh copy then builds `--offline`. Passes when both cells end `BUILD SUCCESSFUL` under `--offline`, so the later daemon runs need no network; it fails on a compatibility error. Recorded: whether a download was needed, what and how large, wall time, the first lines naming the Kotlin version.
- Schema: no change.
- Needs: none. A real Gradle (level 5); no daemon, no device.
- Not here: no droidyard run; the error injection is step 3's. A missing artifact does not stop the milestone (K3: downloaded and recorded); a compatibility failure (the BOM, `kotlin-dsl` against KGP 2.4.20) is recorded under Changes with the smallest further rewrite, added to D1 before step 2.
- Rests on: after this step every artifact of both cells is in `~/.gradle` (level 5, `--offline`).
- [x] Done. Evidence: 2026-10-11, `$B/s1/`: the rewrite replaced exactly three strings (`grep -c 2.4.20`: settings 2, build-logic 1; `2.0.21` left: 0); Gradle 8.13, JDK 17 (Zulu), `ANDROID_SERIAL=droidyard-m2h-none`, `--offline -p <copy> :app:assembleDebug`: KGP 2.0.21 `BUILD SUCCESSFUL in 13s`, KGP 2.4.20 `BUILD SUCCESSFUL in 28s` (61 tasks each), so no download was needed: `find … -newer stamp -type f` listed 0 files; the 2.4.20 build warns `Deprecated Gradle Version` (KGP 2.5 will want Gradle 8.14.4), recorded, not changed; the SDK stamp found nothing newer.

### 2. `samples/mvp-dod.sh errors <kgp> <outdir>`

- Builds: `samples/mvp-dod.sh` (POSIX `sh`, `jq`), its `errors` mode. Implementer.
- Implements: D1, D2, D3, K1–K4, K6; ADR-0022 §Decision 1–3 as A1/A2 state them.
- Behaviour: per K1, the copy, D1's rewrite (count checked), launch and Doctor, `droidyard trust`, `build.assemble {module: ":app", variant: "debug"}` (waited to its end), D3's insertion, the failed build, `build.errors {buildId}`, the restore and its build, then Cleanup; it writes `build-ok.json`, `build-failed.json`, `errors.json`, `build-restored.json`, `summary.txt`, `daemon.log` and the cleanup snapshot under `<outdir>`, and exits non-zero naming the first mismatch with A1 or A2. The assertions are one function over the saved JSON (`mvp-dod.sh check-errors <kgp> <dir>`), so they can be exercised without a build.
- Check: `sh -n samples/mvp-dod.sh`; `check-errors` over JSON files the implementer writes from `extract.Record` and `build.Result`'s shapes: an A1 set passes for `2.0.21` and fails for `2.4.20`, an A2 set the reverse, a set with the console record duplicated beside the event one fails A2, a 2.0.21 set with an AGP warning event passes A1. The refusals: a dirty `samples/golden` (a scratch clone), an `<outdir>` inside the repository, a `kgp` other than `2.0.21` and `2.4.20`, a rewrite count of 2 (a scratch copy with one string removed). No real run: level-5 runs are the main session's (`implementer-run.md`).
- Schema: no change.
- Needs: step 1. No JVM, adb or emulator.
- Not here: the `footprint` mode (step 4); no Go code; no MCP session (parity is Phase 0's, and m1l ran G3 over MCP).
- Rests on: —
- [x] Done. Evidence: 2026-10-11, `8b60f72`, implemented with step 4 in one run (§Changes): `sh -n` and `dash -n` clean; `check-errors` over hand-written sets: the A1 set passes for 2.0.21 and fails for 2.4.20, the A2 set the reverse, a console record duplicated beside the event one fails ("2 records at …MainActivity.kt:35:9"), a 2.0.21 set with an AGP warning event passes; the refusals (an unsupported kgp, an outdir inside the repository, a dirty `samples/golden` in a scratch clone, a copy with two occurrences of 2.0.21) each stop before anything is created. The level-5 runs of step 3 then passed.

### 3. Both fixtures through the daemon (level 5)

- Builds: nothing; evidence under `$B/errors-<kgp>-<stamp>/`. Main session.
- Implements: DoD 3; A1, A2, A3.
- Check: `samples/mvp-dod.sh errors 2.0.21` and `errors 2.4.20` at the step's commit, each exit 0, with K2, K3 (its list of new files recorded; expected empty after step 1), K4 and K6 holding; the decisive `errors.json` record of each quoted, and each build's `extraction.sources` recorded. `verify-daemon` §Drive gains one line naming the script as the Definition of done run (D2).
- Schema: no change.
- Needs: step 2. Real Gradle, sidecar and daemon (level 5); no device.
- Not here: a defect the run exposes (the event dropped, throttled into a summary, a duplicate record, `kgp` read as `unknown` so `expectedSources` says `console`) is fixed inside the milestone as a step inserted after this one, recorded under Changes, with a level-4 test over a recording of this run's notifications, verified by the Opus verifier (it changes what a field means), and this step rerun.
- Rests on: a real KGP 2.4.20 Problems event reaches `build.errors` labelled `event` (level 5).
- [x] Done. Evidence: 2026-10-11, at `8b60f72` (the 2.4.20 run with §Changes' `save_log` fix uncommitted, which touches only the log copy), `DROIDYARD_OWNER_PID=15130`. `errors 2.0.21 $B/errors-2.0.21-210318` exit 0: `check-errors 2.0.21: ok (["console"] sources; kotlin/console …MainActivity.kt:35:9)`; the record `{"severity":"error","source":"console","tool":"kotlin","file":"app/src/main/kotlin/dev/droidyard/golden/MainActivity.kt","line":35,"column":9,"message":"Unresolved reference 'droidyardAcceptanceMissing'."}`; the failed build `kgp=2.0.21 expected=console sources=["console"]`, the restore `success`. `errors 2.4.20 $B/errors-2.4.20-210403` exit 0: one record, `{"severity":"error","source":"event","tool":"kotlin","problemId":"UNRESOLVED_REFERENCE","file":"app/src/main/kotlin/dev/droidyard/golden/MainActivity.kt","line":35,"column":9,"message":"Unresolved reference 'droidyardAcceptanceMissing'."}` (the console's `e:` merged into it); the failed build `kgp=2.4.20 expected=problems sources=["event","console"]`; the OK and restored builds `success`, `sources=["event"]` (KGP's Gradle-version warning). Both runs: downloads 0 files; the SDK stamp clean; K2's 5037 pid 19285 and 15130 unchanged with their start times; 0 orphans after stop, the Gradle and Kotlin daemons excluded (K6). No fix step was needed. `verify-daemon` §Drive names the script (D2).

### 4. `samples/mvp-dod.sh footprint <outdir> [avd]`

- Builds: the script's `footprint` mode. Implementer.
- Implements: D4, D5, K5, K6; `gradle.md` §Lifecycle; `process-lifecycle.md` §Idle shutdown.
- Behaviour: D4's scenario (the device calls only when an AVD is named), D5's pid lists before and after, samples every 30 s into `footprint.tsv` (time, pid, role, ours, RSS), D5's table in `footprint.txt`, the sidecar's exit time relative to the end of the last `build.assemble`, then D4's restart build. It exits non-zero if the core's final RSS is 48,828 KiB or more, if the sidecar is alive at 11:00 or gone before 10:00, if `daemon.log` holds an unexpected-exit or forced-kill line for the old sidecar pid, if the restart build fails or reuses the old pid, or if `ANDROID_ADB_SERVER_PORT` is 5037 or unset when an AVD is named. The verdict is one function over the saved files (`mvp-dod.sh check-footprint <dir>`).
- Check: `sh -n`; `check-footprint` over files the implementer writes: a passing set; a core at 48,828 KiB fails; a sidecar gone at 9:50 fails and one alive at 11:05 fails; a restart on the same pid fails, and a `daemon.log` holding `exited unexpectedly` for the old pid fails. The refusal with `ANDROID_ADB_SERVER_PORT=5037` and an AVD named. No real run (`implementer-run.md`).
- Schema: no change.
- Needs: step 2 (shares its launch and copy). No JVM, adb or emulator.
- Not here: no UI tab or MCP session in the window (D4); no change to `gradle.IdleTimeout`.
- Rests on: —
- [x] Done. Evidence: 2026-10-11, `8b60f72`, with step 2: `check-footprint` over hand-written sets: a passing set passes; a core at 48,828 KiB fails and 48,827 passes; a sidecar gone at 9:50 fails, at 10:00 passes; one alive at 11:05 fails, one never seen gone fails; a restart on the same pid fails; `daemon.log` with `(pid 222) exited unexpectedly` for the old pid fails, for another pid passes. The forced-kill line's case ran only before a reordering fix, not after (labelled). The refusals with `ANDROID_ADB_SERVER_PORT` 5037 or unset, or no `ANDROID_AVD_HOME`, with an AVD named. Step 5's level-5 run then passed.

### 5. The idle footprint, golden path with an emulator (level 5)

- Builds: nothing; evidence under `$B/footprint-<stamp>/`. Main session.
- Implements: DoD 6; A4.
- Check: `samples/mvp-dod.sh footprint <outdir> droidyard_m2a` under K5, exit 0; `footprint.txt` quoted (each process's pid, attribution and RSS), the core's final figure, the sidecar's exit time; K2–K6 hold (`emulator.stop` done, the 5199 server ended, 5037 and 15130 unchanged).
- Schema: no change.
- Needs: step 4. Real daemon, Gradle, emulator (level 5).
- Not here: a sidecar that does not exit is a defect handled as step 3's are (a step inserted, recorded under Changes); an RSS over budget is recorded for the owner and the box stays unticked.
- Rests on: the sidecar stops on the supervisor's idle timer, not by a crash (level 5: the 10:00–11:00 window, no unexpected-exit line for its pid in `daemon.log`, then a build on a new sidecar pid).
- [x] Done. Evidence: 2026-10-11, `$B/footprint-210610` at `8b60f72` with §Changes' `save_log` and `gcall` fixes uncommitted (`evidence/env`: `8b60f72-dirty`; the tree then equal to `a893d0a` but for a comment), `ANDROID_AVD_HOME` m2a's, `ADB` the m2h guard (`protected` empty: no emulator online at the start), `ANDROID_ADB_SERVER_PORT=5199`. Exit 0: `check-footprint: ok (core 25456 KiB; sidecar 48849 gone 604 s after its last build; restart on pid 64011)`. The golden path: three builds, `emulator.start` → `emulator-5554`, `adopted: false`, 14.7 s; install, launch, logs, screenshot, uiTree, inputText, tapElement `expect` `matched: true` (802 ms). `footprint.txt` (KiB; ours: the core and the sidecar):

  | role | pid | ours | 30 s after | end | max | exited |
  | --- | --- | --- | --- | --- | --- | --- |
  | core | 48739 | yes | 55,504 | 25,456 | 70,576 | alive |
  | sidecar | 48849 | yes | 33,712 | 24,400 (last alive) | 189,488 | 604 s after its last build |
  | Gradle daemon | 48894 | no | 1,148,464 | 74,656 (last alive) | 1,148,464 | 90 s after the last build (the sample's 60 s idle timeout) |
  | Kotlin daemon | 49031 | no | 706,112 | 35,440 (last alive) | 706,112 | 90 s after the last build |
  | emulator | 50398 | no | 3,716,208 | 2,426,496 | 3,726,928 | stopped by `emulator.stop` |
  | adb server (5199) | 50129 | no | 11,392 | 8,880 | 11,520 | killed at the end |

  The core's final figure, 25,456 KiB (26.1 MB), is under 48,828 KiB; 30 s after the last call it was 55,504 KiB (56.8 MB) and its maximum 70,576 KiB (72.3 MB), both over the budget; it fell under it 244 s after the last call and to about 25 MB at 395 s, before the artifacts' 15-minute TTL (register row). The sidecar's record left `processes.json`; `daemon.log` holds no `exited unexpectedly` line; the restart build `success` on pid 64011. K2: 5037's pid 19285 and 15130 unchanged with their start times; K3 0 files downloaded; K4 the SDK stamp clean; K5 `refused.log` absent, no qemu left, no listener on 5199; K6 0 orphans. RSS is `ps -o rss=` on macOS (Unverified row).

### 6. The deviation audit: the ledger

- Builds: §Audit ledger in this plan. Main session, with read-only Opus agents (`verifier` or `Explore`) fanned out over batches of plans; the implementer writes no document.
- Implements: D6, D7; DoD 8.
- Check: (a) every Done plan in `docs/plans/README.md` has one ledger row; (b) every line of those plans' decision, design-question, Readings and Changes sections matching `grep -n -i -e deviat -e departure -e supersed -e amended -e "instead of" -e "contrary"` is a ledger unit classed A, M or X, or is listed in its row as N with a reason; (c) the Opus verifier re-derives the unit counts of three plans it picks and reads every N unit of two of them: a count mismatch or a misclassed unit is blocking.
- Schema: no change.
- Needs: none (documents only; may run beside steps 1–5).
- Not here: no ADR written (step 7); no code; frozen records are not edited.
- Rests on: D6's population is complete for each plan (level 3: the verifier's re-derivation).
- [x] Done. Evidence: 2026-10-11. (a) 23 ledger rows, one per Done plan of `docs/plans/README.md`, 519 units. (b) Each fan-out agent ran the grep on its plans; the verifier reran it on m1d and m1h: every hit inside a unit section is an A, M or X unit or a listed N. (c) The Opus verifier re-derived m1d 8/0/10/5, m1h 17/0/5/7 and m2c 5/3/0/4, each equal to its row, and read every N unit of m1d and m1h: none misclassed; two N reasons and one A record corrected (Review). The other 20 plans' counts rest on the fan-out agents (level 3 by reading).

### 7. ADRs for the unrecorded deviations

- Builds: one ADR per M unit (or one per group the ledger shows to be one decision), `docs/adr/README.md` index rows, the superseded ADRs' header lines, and the one area document per ADR that states the rule; X units' register rows. Main session.
- Implements: D8; the `adr` skill; DoD 8.
- Check: every M unit in the ledger names an ADR file that exists; `go test ./internal/doccheck` passes (supersession pointers both ways, index, links, corpus budget: an area document that grows cuts as many words elsewhere or raises `liveBudget` in the same change with its reason, 8 words of headroom at the baseline); the Opus verifier reads each ADR against the ledger unit and the code it describes (a mismatch is blocking). With no M unit, the step is ticked on (a) and the ledger.
- Schema: no change (an ADR recording shipped behaviour changes no contract; a unit that would need one is a design question for the owner, and the plan stops).
- Needs: step 6.
- Not here: no code change; a deviation better reverted than recorded is an X unit and a register row.
- Rests on: —
- [x] Done. Evidence: 2026-10-11. ADR-0055 (m1d D5, C4), ADR-0056 (m1c R16 with R10; supersedes ADR-0021 §Format in part) and ADR-0057 (m1h D6; supersedes ADR-0023 §Decision in part) exist, Accepted, Delegated (D8), indexed; ADR-0021 and ADR-0023 carry their `Superseded in part by:` lines; `workspace-state.md`'s `degraded` row names only `workspace.json`. `go test ./internal/doccheck` ok (26,307 of 26,317 words). The Opus verifier read each ADR against its units and code: ADR-0055 and ADR-0056 §1, §2, §4 and ADR-0057 §2 match; ADR-0056 §3 (B1: `Spawn`'s short commands still run with a newer `processes.json`, shown by an overlay test) and ADR-0057's note bound (B2: per project) were corrected in the verifier's words, with `Supervisor.Load`'s comment. The X units: six register rows exist; m1o C9/C13's row is added at the close.

### 8. Close

- Builds: `mvp.md` (three boxes ticked, each with its step's evidence pointer, words cut elsewhere in the file to match or `liveBudget` raised with a reason), `known-limits.md` (the two rows closed by 2h, the sidecar-memory row split so S5 stays open, a row for time to first useful result, rows for what steps 3–7 left open), `docs/plans/README.md`, `docs/README.md` §Where the work stands. Main session.
- Implements: `milestone-workflow` §Close; A6.
- Check: `go test ./internal/doccheck` and `go test ./...` pass; the Opus close verifier over A1–A6 finds no blocking item.
- Schema: no change.
- Needs: steps 3, 5, 7.
- Not here: no record edited (Done plans, research, reviews, accepted ADRs); the milestones after 2g that `mvp.md` lists under Phase 2 untouched.
- Rests on: —
- [x] Done. Evidence: 2026-10-11. `go build`, `go vet`, `gofmt -l .` clean and `go test ./...` ok at `095b854`. The Opus close verifier: pass, no blocking finding; it reran `check-errors` on both cells (ok, and failing with the versions crossed) and `check-footprint` (ok, `footprint.txt` byte-identical), failed it with each forced-kill and unexpected-exit line injected for the old pid (closing step 4's labelled gap), and checked the table, the 244 s claim and A6. Its script findings fixed in the close: the orphan exclusion matched every `adb` (now the server's `fork-server server` only), and a footprint run with an AVD now refuses to start while an emulator runs or the run's adb port is taken; a reused Gradle daemon left out of the table and a failed `emulator.start` leaving the scratch emulator up are a register row. The merge commit is the evidence of the merge.

## Audit ledger

Filled by step 6 (2026-10-11), by four read-only Opus agents over m1b–m1f, m1g–m1l, m1m–m2a and m2b–m2g, the borderline units settled by the main session. Kinds: D (decisions), Q (design questions), R (readings), C (Changes entries). A counts a departure, or a design question answered, that an ADR or an `Amended:` line records; a gap an ADR left, filled and also written into its `Amended:` line, is N. The N column lists the units step 6's grep hit and the borderline ones. Totals: 23 plans, 519 units; 4 M units in 3 groups (ADR-0055 to ADR-0057, step 7), 7 X units (6 with a register row, 1 added at the close); a linked pair (m1c R16 with R10, m1o C9 with C13) counts once.

| Plan | Units (D/Q/R/C) | A | M | X | N listed with a reason |
| --- | --- | --- | --- | --- | --- |
| m1b | 0/2/11/8 | Q1 (ADR-0025); Q2 (ADR-0019 `Amended:` 2026-09-30); C3, C4 (ADR-0019 `Amended:`, third and fourth passes); C8 (ADR-0020 `Amended:` 2026-10-02) | — | — | C1: steps reordered |
| m1c | 0/2/17/12 | Q1 (ADR-0026); Q2 (ADR-0027); R7 (ADR-0026 `Amended:` 2026-10-03); C3 (ADR-0026 `Amended:`); C4 (ADR-0021 `Amended:` 2026-10-03) | R16 with R10 (ADR-0056) | — | C1: `SysctlKinfoProcSlice`, the same sysctl; C7: `createdAt` omitted for an unread newer `workspace.json`, a gap ADR-0026 left |
| m1d | 8/0/10/5 | D1 (ADR-0025 `Amended:`); D2 (ADR-0028); D3 (ADR-0032 `Amended:` 2026-10-07); D6 (ADR-0010, ADR-0019 `Amended:`); D8 (ADR-0019, ADR-0021 `Amended:`); C3 (ADR-0019, ADR-0021 `Amended:` 2026-10-03); C5 (ADR-0028 `Amended:`) | D5, C4 (ADR-0055) | — | R1: the version bump capability-api.md prescribes; C5's declared deviations: implementation details |
| m1e | 0/6/9/13 | Q2, C7 (ADR-0020 `Amended:` 2026-10-04); Q4 (ADR-0012 `Amended:` 2026-10-04); C5 (ADR-0029); C9 (ADR-0032 §Consequences) | — | — | Q1: flat `nodes[]` follows ui-automation.md's own drawing |
| m1f | 22/0/8/9 | D5 (ADR-0015 `Amended:` 2026-10-05); D17 (ADR-0030 `Amended:` 2026-10-06); D21, C8 (ADR-0020 `Amended:` 2026-10-06) | — | — | none beyond the A units |
| m1g | 27/0/7/11 | D1 (ADR-0031; ADR-0021, ADR-0024 `Amended:` 2026-10-06); D2 (ADR-0019 `Amended:` 2026-10-06); D9 (ADR-0018 `Amended:` 2026-10-06, 2026-10-07; ADR-0032); D13 (ADR-0012 `Amended:` 2026-10-06); D25 (ADR-0017, ADR-0023 `Amended:` 2026-10-06); D27 (ADR-0044) | — | — | R4: applies ADR-0020's 2026-10-04 line; D4, D12, C1: gaps ADR-0017 and ADR-0022 left, also in their `Amended:` lines; D26: follows ADR-0022 §Decision 4 over area lines that contradicted it; C5: an internal core–sidecar bound |
| m1h | 17/0/5/7 | — | D6 (ADR-0057) | — | R5, D4, D5: gaps, also in ADR-0017, ADR-0021, ADR-0023 `Amended:` 2026-10-07; D9: description wording; D11: `sdk.status` reports discovery, the `system.health` SDK block deferred (register row); D12: the published `projectSdk.root` follows `sdk.md` §Project SDK |
| m1i | 20/0/7/10 | D14 (ADR-0032 §Consequences); D18 (ADR-0041) | — | — | D5: mechanism only; D7: within ADR-0012's 2026-10-04 line; D12: lock keys, a private detail; C6: truncation instead of failure, a fix |
| m1j | 11/0/5/12 | D7 (ADR-0049 §Alternatives: no OS watcher); D8 (ADR-0041, ADR-0043 adopt it) | — | — | C3: a fix |
| m1k | 10/2/5/14 | Q1 (ADR-0032, ADR-0015 `Amended:` 2026-10-07); Q2, R3, R4 (ADR-0032, ADR-0018 `Amended:` 2026-10-07); C6 (ADR-0015 `Amended:` 2026-10-07); C12 (ADR-0032 `Amended:` 2026-10-07) | — | C3 (`operation.wait` with no audience check, against ADR-0032 §5; register row exists); C11 (`sdk_gphone*` off USB taken for an emulator, against ADR-0015 §4; register row exists) | D10: a list of documents; C4: verifier fixes |
| m1l | 3/1/6/10 | — | — | — | R1, C7: the owner's change of validation target, recorded in mvp.md |
| m1m | 5/2/0/13 | Q1 (ADR-0033; ADR-0001 `Amended:` 2026-10-07); C1 2026-10-07 step 1 (ADR-0033, ADR-0001 `Amended:`) | — | C3 2026-10-07 step 2 verifier (against ADR-0033 ¶1 and ¶4; register row "Ending a launch misses the qemu if a launcher forks it into a new group") | C5, C9: `inputCommandTimeout` and `inputTypeBound`, plan bounds no ADR fixes |
| m1n | 7/2/0/4 | Q1 (ADR-0035; ADR-0029, ADR-0030 `Amended:` 2026-10-07); Q2 (ADR-0034; ADR-0030 `Amended:` 2026-10-07) | — | C4 2026-10-08 close review N3 (against ADR-0034 ¶3; register row "A system dialog that appears between an action's before read and its tap takes the tap") | D2: "supersedes plan m1f D10", a plan's settle constant; C4's "Superseded header lines": the plan's own header history |
| m1o | 6/2/0/13 | Q1 (ADR-0036); Q2 (ADR-0037); C7 2026-10-08 (hooks dropped, ADR-0037) | — | C9, C13 2026-10-09 (Cursor's empty `deny`: an `init --uninstall` on a `cli.json` that had `allow` and no `deny` leaves `"deny": []`, against ADR-0037 ¶2 "removes exactly what it would write now"; register row added at the close) | C11: the core profile grows to 25 tools, ADR-0036 ¶4 hands the list to the plan |
| m1p | 3/2/0/6 | Q1 (ADR-0021, ADR-0033, ADR-0001 `Amended:` 2026-10-08); Q2 (ADR-0032 `Amended:` 2026-10-08); C4 2026-10-08 step 6 verifier (ADR-0033's and ADR-0021's 2026-10-08 lines) | — | — | D1: `cwd` and `env` for `shell.exec`, two parameters, no ADR or area rule departed from |
| m1q | 9/0/0/5 | — | — | — | D2: a process rule; C1: rules restated in area documents, no behaviour change; C4 "Declared deviation": from the plan's A9 check, not an ADR |
| m2a | 8/3/0/4 | Q1 (ADR-0040); Q2 (ADR-0041); C2 2026-10-09 step 4 (ADR-0042); C3 2026-10-10 step 6 (ADR-0042) | — | — | D8: the defaults equal ADR-0041 ¶2 and ¶5; Q3: the trace line fills what ADR-0020 leaves open |
| m2b | 8/2/0/2 | Q1 (ADR-0043) | — | C1 (against ADR-0030 §Decision; register row "persistent UI reader … one-shot fallback", closed by 2c step 5) | Q2: the live screen was not built, scope only |
| m2c | 5/3/0/4 | Q1 (ADR-0044), Q2 (ADR-0045), Q3 (ADR-0046) | — | C2 (the folder dialog spawned unrecorded, against ADR-0021 §Pid records; register row "A daemon killed with SIGKILL leaves the folder dialog on screen", reworded at the close) | C1: CGWindowList instead of `screencapture`, run-only; the emulator's `-crash-report-mode never`, no ADR decides launch flags (`emulator.md` §Flags) |
| m2d | 9/4/0/7 | Q1 (ADR-0047), Q2 (ADR-0048), Q3 (ADR-0049) | — | — | C2: the walk benchmark tripped ADR-0049's stated gate, the gate re-based on real trees and a register row added; what shipped is ADR-0049's Decision |
| m2e | 11/2/0/6 | Q1 (ADR-0050), Q2 (ADR-0051) | — | — | C1: lines after a failed `pm` read pass without `package`, ADR-0051 §2's failure path; C4: a menu split, UI detail |
| m2f | 5/1/0/3 | Q1 (ADR-0052) | — | — | D46: a CSS layout detail |
| m2g | 12/2/0/7 | Q1 (ADR-0053), Q2 (ADR-0054) | — | — | C1: `file.move` not idempotent, ADR-0053 names no hint; C3: `file.copy` accepts `from == to`, ADR-0053 §2's list does not refuse it |

## Not in this milestone

- The rest of S1's matrix, kapt, a KSP error, an AAPT2 compile error: `spike-s1-error-formats.md` §Still to run; register.
- KGP 2.4 as a CI fixture or gated Go test; the CI version matrix: register (ADR-0018 §Consequences).
- S5 (daemon reuse with the terminal and Studio): register.
- A configurable sidecar idle period: register (configuration v1 deferrals).
- Code fixes for X units: register rows; a fix is its own change.
- Footprint with the web UI open or an MCP session connected; Linux: Unverified.

## Design questions

None. The steps rest on documented decisions (D1–D8 apply them); a step that would need a new decision stops the plan.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| The KGP 2.4.20 cell resolves (from the cache, or after a recorded download) and builds with the Compose BOM and `kotlin-dsl` as they are | 2–3 | step 1 |
| Golden's one Kotlin error reaches the daemon as one Problems event, not throttled into a summary, and the probe reads KGP 2.4.20 | 3 | step 3 |
| Nothing holds a lease on the sidecar after the golden path, so it idles out | 5 | steps 4, 5 |
| RSS by `ps` on macOS (compressed pages not counted; `phys_footprint` differs) is the figure the budget means | 5 | stays labelled in the evidence |
| The idle footprint with a UI tab or an MCP session connected | — | not run; stays a register row |
| The script stays true after later changes (nothing lints or runs `samples/`) | — | each rerun; stays labelled |
| The audit finds few M units (about 8 or fewer); more splits step 7, recorded under Changes | 7 | step 6 |
| Divergences no plan recorded | — | outside D6; stays a register row |
| Linux | 3, 5 | not run (register) |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | B1 [test] blocking: `project.modules` after the idle exit answers from the model cache (`gradle/client.go` `Model`), so the restart proof could never happen | Changed: D4, A4, steps 4–5 use a `build.assemble` (up to date) asserting a new sidecar pid and the workspace still `ready` (a crash degrades it, m2c D21) |
| 1 | revise | B2 [plan] blocking: D6 omits the Readings sections (about 79 R items in m1b–m1l, some ADR-worthy: m1c R8, R9, R16) | Changed: R items in D6's population, step 6's grep and the ledger (D/Q/R/C) |
| 1 | revise | N1 [plan]: D2's rationale and Rejected list; the script duplicates `verify-daemon`; footprint depends on an outside guard | Changed: D2 cites the owner's brief, `mvp.md` and `prove-change`; transcribes `verify-daemon` sections and gets a §Drive line at step 3; rejects the sidecar harness with its reason; offers the no-script option; the guard dependency stated; a staleness row in Unverified |
| 1 | revise | N2 [plan]: steps 2, 4 had the implementer run level-5 runs | Changed: their checks are `sh -n`, `check-errors`/`check-footprint` over crafted JSON and the refusals; real runs stay in steps 3, 5 |
| 1 | revise | N3 [test]: the sample's 60 s Gradle idle timeout | Changed: D4 samples the Gradle and Kotlin daemons 30 s after the last build and labels the property |
| 1 | revise | N4 [doc]: m1e Q5 misread; D4 not shown to the owner | Changed: Contract line and D4 corrected; D4 in "The owner should see"; 30 s and maximum figures against the budget |
| 1 | revise | N5 [test]: 50,000 KiB is not the stricter reading | Changed: `< 48,828` KiB |
| 1 | revise | N6 [doc]: the corpus budget first bites in step 7 | Changed: step 7's Check carries the cut-or-raise rule |
| 1 | revise | N7 [doc]: an outright DoD 8 tick overstates | Changed: A6 and "The owner should see": the tick names D6's boundary and the register row |
| 1 | revise | N8 [test]: `extraction.sources: [console]` is whole-build | Changed: A1 asserts no error record with `source: event`; sources recorded |
| 1 | revise | N9 [test]: host sleep during the window | Changed: D4 runs under `caffeinate -i` |
| 2 | revise | B1-r2 [test] blocking: "workspace still `ready`" cannot fail (a crash degrades only under a running build, and the restart build clears it); B2 resolved | Changed: D4, A4, step 4's Behaviour and Check, step 5's Rests on use `daemon.log`'s `exited unexpectedly` / `killed; its group outlived` lines for the old pid (`supervisor.go`), crafted-log case fails; D21 sentence corrected; new-pid and build-success assertions kept |
| 2 | revise | Nits: m1c–m1l, not m1b–m1l; m1q's `## Delegated choices` | Both taken in D6 |
| 1 | revise | Nits: which D1 string matters; the guard is m2b's; the `nodump` label; ledger kind R | All taken (D1, K5, D4, ledger header) |
| Steps 6–7 | revise | B1 [doc] blocking: ADR-0056 §3 said a newer `processes.json` starts no child; `Spawn` (adb's one-shot commands) still runs | Fixed: §3 names the refused starts and the short commands that run; `Supervisor.Load`'s comment too |
| Steps 6–7 | revise | B2 [doc] blocking: ADR-0057's 50-note bound is per project | Fixed: "per project" |
| Close | pass | Non-blocking: [test] the orphan exclusion matched every `adb`; [safety] the guard's protected list never set, the run's adb port never checked free; [test] a reused Gradle or Kotlin daemon dropped from the table; [safety] a failed `emulator.start` leaves the scratch emulator up; nits on the register's closed row, the DoD 6 wording and step 5's commit | The first two fixed in the script (an emulator running or the port taken refuses the run); the next two a register row; the nits taken where they cost no words (step 5's commit, the register row) |
| Steps 6–7 | revise | Nits [doc] ADR-0055 cited a gone section; [plan] m1d C3 named no record, m1h D12's N reason missed its point, the totals did not say a linked pair counts once | All taken |

## Changes

- 2026-10-11, steps 2–5: steps 2 and 4 were implemented in one run (one file); its device order is inputText then tapElement, as the form needs a name before submit. Step 3 found that a daemon started by `serve` in the foreground logs to its standard error, not `<state>/daemon.log`: the script's `save_log` keeps both as `$OUT/daemon.log` (the 2.0.21 run missed the file; its assertions do not read it). Step 5's first run stopped at `emulator.start` with `REQUEST_UNEXPECTED_WORKSPACE`: global capabilities refuse a workspace, so the script's `gcall` sends them without one.
- 2026-10-11 (before approval), the coordinator's course correction: Gradle downloads for the fixtures are allowed (the owner's written consent of 2026-10-11, relayed by the coordinator) and recorded with their sizes; the header, K3, A3, step 1 and step 3 no longer stop for consent.
