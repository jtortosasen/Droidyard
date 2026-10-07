# Plan: Milestone 1l — the Phase 1 acceptance run

- Status: Approved
- Approval: Delegated, 2026-10-07, by the main session under the owner's standing instruction, after a diff-only check of round 3's answers; D1–D3 stay revertible. E7 is the owner's: the run waits for their emulators to be shut down.
- Date: 2026-10-07
- Milestone: "**The Phase 1 acceptance run** (`roadmap/mvp.md` §Phase 1): DopamineControl through Droidyard alone, from Cursor and OpenCode; it needs a plan." (`docs/README.md` §Where the work stands, row **Next**)
- Owner decision, 2026-10-07 (the owner's words): "Si dopamine control no te sirve, create una app de ejemplo sencilla que te haga el mismo papel" (if DopamineControl does not serve, create a simple sample app that plays the same role). **The acceptance target is a sample app built for the purpose, not DopamineControl**: a departure from the ratified validation target (`docs/reviews/2026-09-29-ratifications.md`), decided by the owner and recorded in `mvp.md` at the close (step 6).
- Phase: `docs/roadmap/mvp.md` §Phase 1, its acceptance (and the Definition of done items it reaches; see Scope)
- Contract: `mvp.md` §The golden path, §Phase 1 (Acceptance), §Validation target: DopamineControl (the role the sample plays), §Definition of done, §Risks and unknowns (MCP client differences, Compose resource-ids); `docs/reviews/2026-09-29-pre-implementation-review.md` §Assumptions that need a spike (S2); `mcp-tool-surface.md` §Errors vs. failures, §Untrusted data, §Long-running work; ADR-0018 (Gradle 8.13 in the supported range); ADR-0019 §Sessions; ADR-0020 §Adapter projections; ADR-0022 (console extractor on KGP < 2.4); ADR-0015 §6, §7; ADR-0032 (the configuration under a scratch state); `verify-daemon` §Real client, §Cleanup and the 1g, 1i, 1k blocks; `m1i-device-logs-and-emulator.md` §Emulator rules (E7, the adb guard); `m1k-policy-and-shell.md` R3.

Step 1 writes the sample app (code, by the implementer); the rest is a run: evidence, a `verify-daemon` block, a small step per defect found, and the documents saying whether Phase 1 is accepted. No product code is planned.

**The owner should see:** E7 (the sample's build in step 1 and the agent runs need your emulators shut down: the run never stops them, and only with none online can no Gradle device task reach them); C5 and R5 are answered (owner, 2026-10-07: "Se puede usar si, usa grok 4.6 mismamente, que está gratis para mi. Está el login hecho": the signed-in Cursor account may be used, with Grok 4.6); R6 (reading the build console instead of `build_errors` counts as stdout parsing); D1–D3 (where the sample lives and what it contains, Delegated).

## Owner's constraints (verbatim)

- Never modify, build in, or run Gradle inside ~/Documents/DopamineControl (the owner has uncommitted changes there). Work on an rsync copy under ~/Library/Caches/droidyard-dev/<plan>/ excluding build/, .gradle/, .kotlin/, .idea/.
- Never touch, kill or restart the shared adb server on port 5037; record its pid before and after.
- Never name, stop, wipe, install on, or otherwise address the owner's emulators (Pixel_9a = emulator-5554 and the other owner AVDs). Use only a scratch AVD in an isolated ANDROID_AVD_HOME behind an adb guard script that refuses writes to emulators online before the run, as 1i did. Never use uiautomator dump against owner emulators.
- Never modify the Android SDK (.knownPackages rewrites by AGP are the allowed exception).
- Kill only pids the run started. Gradle daemons in ~/.gradle are not stopped.

Since the owner's decision, DopamineControl is not used at all: no step reads, copies or builds it.

## Delegated decisions

- D1. **Where the sample lives**: `samples/golden/`, committed, beside no Go code (`mvp.md` §In scope names a "Sample app as the acceptance target"; `sidecar/testdata/` holds the sidecar's own test fixtures, which Go tests copy and rewrite). Built only in scratch copies, never in the repository (m1g S3–S5, m1h S8); like the fixtures, it carries `gradle/wrapper/gradle-wrapper.properties` and no wrapper script or jar.
- D2. **Toolchain**: the fixtures' proven cell (1g, 1h): Gradle 8.13, AGP 8.13.2, KGP 2.0.21 with the matching `org.jetbrains.kotlin.plugin.compose` 2.0.21 (in `~/.gradle`'s cache), `compileSdk` 36, `buildToolsVersion` 35.0.0 (both installed), `minSdk` 24, `targetSdk` 36, Java 17; `gradle.properties` carrying `android.builder.sdkDownload=false` (so AGP can never install an SDK package), `org.gradle.daemon.idletimeout=60000` (as the fixtures) and `android.useAndroidX=true` (Compose needs it; the fixtures set `false`). One Compose BOM that resolves with that cell, pinned and recorded. Layout: `:app` (the activity and the navigation) and `:feature:golden` (the screens), with `build-logic/` holding one convention plugin both modules apply, as a light echo of the multi-module, convention-plugin shape the ratified target had; if `build-logic` cannot be made to work in step 1, it is dropped and recorded under Changes.
- D3. **What the sample contains** (`mvp.md` §Validation target: DopamineControl, item by item): package and `applicationId` `dev.droidyard.golden`; one activity whose Compose root sets `testTagsAsResourceId = true`; two screens with navigation. **Form**: a text field tagged `name`, a button tagged `submit` ("Continue"), and validation: an empty submit shows "Name is required" (tagged `nameError`), which disappears as soon as the field is edited; a valid submit navigates to the list. **List**: a heading "Hello, <name>" (tagged `greeting`), three rows (Alpha, Beta, Gamma), each with a like button tagged `like_alpha`, `like_beta`, `like_gamma` and a text tagged `likes_alpha`, `likes_beta`, `likes_gamma` reading "Likes: <n>"; a button tagged `crash` ("Crash") that throws `IllegalStateException("golden app crash")`. **Log line**: `Log.i("GoldenApp", "started")` in `onCreate`. **Compile-error site**: a marker comment `// golden-path: compile-error site` on its own line inside `MainActivity.onCreate`, where the prompt has the agent add a call. Instrumented tests are not written: the prompt and the G rows are the scripted journey that defines the end state.

## Scratch rules

- S1. Everything lives under `~/Library/Caches/droidyard-dev/m1l/` (`B` below): one copy of the sample per run (`g-build`, `g-pre`, `g-reh`, `g-opencode`, `g-cursor`) and a reference `g-fresh`; one `DROIDYARD_STATE_DIR` per run (`state-<run>`), with `DROIDYARD_CONFIG_DIR` **unset**, so the configuration is `<state>/config/config.json` and the user's is never read (m1k R3: a configured `tools.adb` would bypass the guard); `avd/` (the isolated `ANDROID_AVD_HOME`), `bin/` (the guard), `cursor-data/`, `l5-<run>-<stamp>/` (evidence).
- S2. A copy: `git archive <commit> samples/golden | tar -x -C $B/<dir>` from the commit under test (never the working tree, which may hold step 1's leftovers), then `echo "sdk.dir=$HOME/Library/Android/sdk" >$B/<dir>/local.properties` (1h). Each copy is trusted with `droidyard trust` under its run's state only.
- S3. SDK: a stamp file at each run's start; at its end `find ~/Library/Android/sdk -maxdepth 3 -newer <stamp> | grep -v knownPackages` prints nothing (m1h S7).
- S4. Gradle runs with the user's `~/.gradle` (dependency downloads land there, as 1g's and 1k's did); Gradle and Kotlin daemons are never stopped. The sidecar is ours and ends with `droidyard stop`.
- S5. Kill only pids the run started: the daemon and the shim, and only through `droidyard stop`, `emulator.stop` on the scratch serial, or `kill <pid>` of a pid this run recorded; never `pkill`/`killall` (`verify-daemon` §Cleanup).
- S6. The implementer may run Gradle in temporary copies of the sample outside the repository (m1g S4), only under E7 and S3's stamp taken before its first run, with `ANDROID_SERIAL=droidyard-m1l-none` exported, every copy carrying D2's `gradle.properties` from its first Gradle run, and never a task matching `install*`, `uninstall*`, `connected*` or `device*` (its `guard-bash.sh` profile blocks `adb` and `emulator`, not these Gradle tasks, so the brief says it and step 1's check greps its log). It never runs `adb`, `emulator`, `avdmanager`, a client or a daemon against a device. Every run step is the main session's.
- S7. Nothing read from an owner emulator is committed or quoted (m1i S3).

## Emulator rules

- E1. The 5037 server's pid (`lsof -t -nP -iTCP:5037 -sTCP:LISTEN`) is recorded before and after each run and must match. After every run nothing listens on 5990 (`lsof -nP -iTCP:5990 -sTCP:LISTEN` empty).
- E2. Every daemon is built with `-tags droidyard_nodump` (`go version -m` shows it), so no daemon of this milestone can run `uiautomator dump`.
- E3. At each device run's start every serial online (`adb devices`) is written to `$B/bin/protected` and `refused.log` is truncated. The `qemu-system` pids (`pgrep -l qemu-system`) and every owner AVD's `userdata-qemu.img*` mtime (`stat -f %m`) are recorded before and after and must match; `ls ~/.android/avd` is unchanged. No call names a protected serial except `device_list` and `emulator_list`, which only list.
- E4. Every daemon that can see adb has `ANDROID_AVD_HOME=$B/avd` and `ADB=$B/bin/adb`: m1i E7's guard, copied from `~/Library/Caches/droidyard-dev/m1i/bin/adb` with its messages and paths renamed to m1l. It is an allowlist: `version`, `devices`, `devices -l`; any `-s <serial>` command for an unprotected serial; for a protected serial exactly `shell getprop`, `shell getprop sys.boot_completed` and `emu avd name`; everything else is refused (exit 97, logged). `system.health` must name it as the adb in use.
- E5. The scratch AVD `droidyard_m1l` (`avdmanager`, `system-images;android-31;google_apis;arm64-v8a`, `-d pixel_5`, under `$B/avd`) is the only AVD booted, wiped or stopped, and only the sample is installed on it. Every device run ends with `emulator.wipe` then `emulator.stop` on its serial through `droidyard call` (a wipe leaves it running), so each client starts from the same wiped, stopped AVD. Step 6 deletes it with `avdmanager delete avd -n droidyard_m1l`.
- E6. **Gradle reaches devices without the guard**: AGP's `install*`, `uninstall*` and `connected*` tasks talk to an adb server through ddmlib, and `build.run` accepts any task; with `ANDROID_ADB_SERVER_PORT` set, ddmlib can even start a new server that attaches to every running emulator. So every daemon of this milestone has `ANDROID_SERIAL=droidyard-m1l-none`, inherited by the sidecar's builds (Droidyard's own adb calls pass `-s` but for `version` and `devices -l`; the guard refuses anything else without `-s`), and every run's hygiene requires no `build_run`/`build_assemble` task matching `install*`, `uninstall*`, `connected*` or `device*`. Whether AGP honours `ANDROID_SERIAL` is unproven (Unverified), and a task the agent writes into a build script with `file_write` is not fenced by either: E7 is what closes both.
- E7. **Agent runs need the owner's emulators shut down.** Steps 1 (the implementer's Gradle runs included), 2, 4 and 5 do not start while any emulator other than the scratch one is online (`adb devices`), and at their end `adb devices` lists at most the scratch one. The owner shuts down their own emulators before those steps and does not boot one during them; the run never stops them. CLI-only device steps (3) may run with owner emulators online behind the guard (1i's rule), and then `device.list` and `emulator.list` send them `shell getprop` and `emu avd name`, as in 1i; nothing else passes the guard.

## Client rules

- C1. Never edited by the run: `~/.cursor/mcp.json` (byte-identical before and after), the `permissions` and `approvalMode` of `~/.cursor/cli-config.json` (compared with `jq -S`; cursor-agent may rewrite its caches in that file, and writes `agent-cli-state.json` and `statsig-cache.json`), and OpenCode's global configuration. Neither client is signed in or out, updated or reconfigured globally by the run.
- C2. Cursor: the server is declared in `<copy>/.cursor/mcp.json`; `<copy>/.cursor/cli.json` has `"allow": ["Mcp(droidyard:*)"]` and denies the client's own shell, read and write tools (the global allows `Shell(ls)`, `Shell(git status)`, `Shell(git branch)` merge in, so step 2's `ls` probe tests that the project's deny wins); `CURSOR_DATA_DIR=$B/cursor-data` (it moves only per-project approvals and trust); in each copy, `cursor-agent mcp enable droidyard` then `mcp list` (droidyard enabled; the global `github` and `Figma` servers and the cached `firebase` plugin's server not loaded). Never `--approve-mcps` (it approves every server) and never `-f/--force`.
- C3. OpenCode: `<copy>/opencode.jsonc` with 1j's permission block (`bash`, `edit`, `read`, `list`, `glob`, `grep`, `webfetch` denied), checked with `opencode debug agents`; model `opencode-go/deepseek-v4.1-flash`.
- C4. Each client starts the shim through a recorder (`verify-daemon` §Real client), a script that exports `DROIDYARD_STATE_DIR=$B/state-<run>`, appends its `pwd` to the evidence, and runs `tee` of both directions into `wire.c2s.jsonl`/`wire.s2c.jsonl` around `droidyard mcp --no-autostart --root <copy>` (Cursor's spawn directory is unknown). With `--no-autostart` a wrong state fails instead of reaching another daemon.
- C5. **Answered by the owner** (2026-10-07): the account is signed in and may be used. The run never runs `cursor-agent status` or `login` (`status` started a login flow when the coordinator tried it) nor signs out; if a call fails on authentication, the Cursor half stops and is reported, and the OpenCode half goes on.

## Scope

| Phase item (deliverable or acceptance) | Here? | If not, where and why |
| --- | --- | --- |
| Phase 1 acceptance: the golden path from Cursor and OpenCode over MCP, structured at every step, no stdout parsing by the agent | yes | on the sample, by the owner's decision; steps 4, 5 |
| Phase 1 deliverables (the capabilities) | exist | built in 1e–1k; the golden path exercises 15 tools (§The golden-path run); the rest only if an agent picks them |
| Validation target: Compose, one activity, 2–3 screens, a form with validation, a list with a counter, `testTagsAsResourceId`, a compile-error site, a crash path | yes | the sample (D3), step 1 |
| Validation target: instrumented tests or a scripted journey | part | the prompt and G rows are the scripted journey; no instrumented test (D3) |
| Validation target: a multi-module, convention-plugin real project | part | a light echo (D2); DopamineControl's scale is not reproduced |
| Validation target: two KGP fixtures; DoD `build.errors` on both | no | CI fixtures; KGP ≥ 2.4 observed in m1g step 2 (F11); the DoD item stays open |
| DoD: golden path from an external agent, zero stdout parsing | yes | A1–A3 |
| DoD: tap and input verify their effect in one call | yes | G7 |
| DoD: `workspace.close` leaves no orphans | yes | the run hygiene (A4) |
| DoD: idle footprint per process | no | RSS recorded, not asserted |
| DoD: the recorded comparison (N ≥ 5 per arm, pilot first) | no | its own plan after Phase 1 (`mvp.md` §How the comparison is run) |
| DoD: golden path from the browser | no | Phase 2 |
| Spike S2 (client behaviour) | part | by-products for two clients (A5); Claude Code, Codex, interrupt cancellation and progress-reset timeouts not run |

## Starting point

Code at `731351e` (the 1k close): API `0.10.0`, 39 capabilities, 34 MCP tools; `git.*` not built (`mvp.md`: last, may slip). Level-5 runs so far were OpenCode v2.0.18 only, and the golden path ran in pieces: steps 1, 2, 3, 5 on DopamineControl on `Pixel_9a` (1g), 2, 4–6 on the View-based flavoured fixture on a scratch AVD (1i), 7 on the View-based form app (1f). Cursor has never connected. m1f's Unverified row "Compose screens … resolve as Views do" waits for this run: no tap or input has targeted a Compose node. No sample app exists; the closest are `sidecar/testdata/flavoured` (Java, Views, a Crash button) and `internal/domain/device/formapp` (Views, built without Gradle).

Baseline on 2026-10-07 at `731351e` (macOS): `go vet ./...` printed nothing; `gofmt -l .` printed nothing; `go test ./...` ok in every package (rechecked by both review rounds). Green: no repair step.

Host: SDK platforms 34–37.0, build-tools 34.0.0–37.0.0; the android-31 `google_apis` arm64 image (1i); in `~/.gradle`, the Gradle 8.13 distribution, the Kotlin Compose compiler plugin 2.0.21 (`compose-compiler-gradle-plugin`, `kotlin-compose-compiler-plugin-embeddable`; its plugin marker only at 2.4.20), and Compose libraries in part (`ui`/`runtime` 1.7.0 and 1.9.0, `material3` 1.3.0, `activity-compose` 1.8.2, `navigation-compose` 2.5.1), no Compose BOM: step 1 downloads the marker and the BOM into `~/.gradle` at least. `cursor-agent` 2026.01.28-fd13201: its `--help` could not run under the planner's command guard, so its options were read from the installed bundle (`-p/--print`, `--output-format text|json|stream-json`, `--model`, `--workspace`, `--approve-mcps`, `-f/--force`; `about`, `models`, `login`, `mcp list|enable|disable|list-tools`; `.cursor/cli.json` and `.cursor/mcp.json` per project, merged with the global `~/.cursor/mcp.json`); per the round-1 reviewer's reading, an MCP call needs an explicit `Mcp(<server>:<tool>)` allow unless approval is unrestricted (C2). The 1i guard is at `~/Library/Caches/droidyard-dev/m1i/bin/adb`.

## Readings

- R1. **The owner's decision replaces the target, not the bar.** The sample plays DopamineControl's role item by item (D3); the acceptance wording ("against DopamineControl") is amended at the close with the decision cited, and the ratifications record stays as it is (closed).
- R2. **One passing run per client is the acceptance.** N ≥ 5 runs, a pilot and a threshold belong to the comparison (`mvp.md` §Metrics). Per client, at most two attempts on one tree. An attempt fails "on the agent" only if every Droidyard result in it was correct and matched its description (a step skipped, a wrong argument never corrected); it is recorded and retried once with the same prompt. Any other failure goes to §Defects. Every failed attempt is kept, and the close verifier rechecks its classification.
- R3. **"No stdout parsing by the agent"** is observed as G9: the agent has no shell (its own denied, `shell.exec` off as by default, never enabled by the run) and every tool result is structured (`mcp-tool-surface.md` §Errors vs. failures).
- R4. **Golden step 1's `workspace.open`** is the session's own open of its bound root before it answers `initialize` (ADR-0019 §Sessions); the agent shows it with `workspace_describe`.
- R5. **Models.** OpenCode: `opencode-go/deepseek-v4.1-flash`, as in every earlier run. Cursor: Grok 4.6, `--model cursor-grok-4.6-high` (the id `cursor-agent models` lists as "Grok 4.6"; the CLI's default is `cursor-grok-4.6-medium-fast`), passed explicitly so the evidence pins it (the owner's choice, 2026-10-07). The acceptance measures no model.
- R6. **The build console is stdout.** A3 holds for a run only if the compile error's location the agent reports comes from a `build_errors` result returned before any `artifact_read` of a build console. An attempt that reads the console first counts as failed under R2. Reading a console after `build_errors` is recorded, not failed.

## Acceptance

- [ ] A1. OpenCode completes G1–G8 on the sample in one session, each golden-path capability returning a structured result. Source: `mvp.md` §Phase 1 (Acceptance), §The golden path (G1–G7), §Validation target: DopamineControl (G8, the crash path); the owner's decision. Proven by: step 4
- [ ] A2. Cursor does the same. If step 2 (a) finds Cursor cannot be fenced, A2 stays unticked and Phase 1 is not accepted. Source: as A1. Proven by: step 5
- [ ] A3. Neither agent parses stdout: G9 holds in both runs (R3, R6). Source: `mvp.md` §The golden path ("The agent never parses raw stdout"), §Definition of done item 1. Proven by: step 4, step 5
- [ ] A4. Nothing of the owner's was touched (S3, E1, E3, E7, C1) and every run ends with zero orphans as the run hygiene counts them. Source: §Owner's constraints; `mvp.md` §Definition of done (orphans). Proven by: step 2, step 3, step 4, step 5
- [ ] A5. Per client, recorded: version, model, tool count, `clientInfo`, whether `tools/call` carries a `progressToken`, whether the screenshot's image block was delivered and used, whether `operation_wait` was needed, the shim's working directory. Source: `mvp.md` §Risks and unknowns (MCP client differences); spike S2. Proven by: step 2, step 4, step 5
- [ ] A6. The sample builds from a scratch copy with the SDK as installed: `sdk.requirements` reports nothing missing, and the SDK is unchanged after its build. Source: the owner's decision; §Owner's constraints. Proven by: step 1
- [ ] A7. `verify-daemon` has an m1l block; `docs/README.md` and `mvp.md` say whether Phase 1 is accepted, with the evidence paths; `mvp.md` §Validation target: DopamineControl and §Risks and unknowns name the sample as the target, citing the owner's decision. Source: `mvp.md` §Definition of done (last item); the milestone. Proven by: step 6

## The golden-path run

Steps 4 and 5 run this, one client each, on a fresh copy (`g-opencode`, `g-cursor`), a fresh state, the scratch AVD wiped and stopped, E7 holding, and a daemon under §Emulator rules. `<file>`, `<line>` and `<column>` come from step 1; the prompt is the same for both clients and is fixed under Changes before step 4:

> Use only the droidyard tools; never use your own shell or file tools. Do these in order and report each result. 1) Confirm the workspace is open and list the project's modules. 2) Build the debug variant of :app with build_assemble. 3) In <file>, add the line `droidyardAcceptanceMissing()` right after the comment `// golden-path: compile-error site`, build again, and give the compile error's file, line, column and message from build_errors; then remove that line and build until it succeeds. 4) Start the emulator droidyard_m1l and from then on use only the device it returns. 5) Install the debug build on it with device_install and launch dev.droidyard.golden. 6) Read the app's logs since that launch, take a screenshot, and read the UI tree. 7) Each in one call that verifies its effect: tap Continue with the name empty, expecting "Name is required" to appear; type Ana into the name field, expecting that message to be gone; tap Continue, expecting "Hello, Ana" to appear; tap Alpha's like button, expecting "Likes: 1" to appear. 8) Tap Crash, then tell me from the app's logs which exception crashed it and its message.

| G | Golden-path step (`mvp.md`) | The wire (`wire.c2s`/`wire.s2c`) shows |
| --- | --- | --- |
| G1 | Open the project and list modules | `tools/list` with 34 tools; `workspace_describe` → `ready`, `trusted: true`, `root` the copy; `project_modules` → the projects step 1 recorded, `:app` and `:feature:golden` among them; `system_health` (the agent's or the recorder's first call) names the guard as adb, so the session reached `state-<run>` |
| G2 | Build the debug variant | `build_assemble {":app", "debug"}` → completed (through `operation_wait` when it returns the operation view), `outcome: success`, `outputs[0].applicationId` `dev.droidyard.golden` |
| G3 | Introduce a compile error and recover | `file_write` adding the line; `build_assemble` → `failed`; `build_errors` → a record with `file` `<file>`, `line` `<line>`, `column` `<column>` (the line the agent actually wrote if it differs), a message naming the reference, `tool: kotlin`, `source: console`; `file_write` without it; `build_assemble` → `success` |
| G4 | Start (or adopt) an emulator | `emulator_start {avd: "droidyard_m1l"}` → a serial not in `protected`, `adopted: false` |
| G5 | Install and launch the app | `device_install` → `applicationId` `dev.droidyard.golden`; `device_launch` → a pid |
| G6 | Observe: logs, screen, UI tree | `device_logs` → `filter.by: uid`, the `GoldenApp: started` line; `device_screenshot` → a reference and an image block; `device_uiTree` → `foreground` `dev.droidyard.golden`, ids `name` and `submit` |
| G7 | Act and verify in one call | `device_tapElement` on `submit` expecting `nameError` (or its text) to appear → `expect.matched: true`; `device_inputText {target {id: name}, text "Ana"}` expecting it gone → matched; `device_tapElement` on `submit` expecting "Hello, Ana" → matched; `device_tapElement` on `like_alpha` expecting "Likes: 1" → matched |
| G8 | Not a golden-path step: `mvp.md` §Validation target: DopamineControl's "deliberate crash path … to exercise `device.logs`" | `device_tapElement` on `crash`; `device_logs` → `crashes[0]` `java.lang.IllegalStateException` / `golden app crash`, `filter.by: uid` |
| G9 | No stdout parsing (R3, R6) | (a) the client's own shell and file tools never invoked (OpenCode's `tool_use` parts: droidyard tools and its `execute` tool search only; Cursor's stream: MCP calls only); (b) no completed `shell_exec`; (c) every `tools/call` response has `structuredContent`, or `isError: true` with a JSON error carrying `code` and `message`: the count of others is 0; (d) the reported location equals the `build_errors` record, and no console `artifact_read` precedes that `build_errors` (R6) |

Run hygiene, all required: no call names a protected serial but the two listings, and `refused.log` is empty; no `build_run`/`build_assemble` task matches E6; `diff -r -x build -x .gradle -x .kotlin -x .idea -x .cursor -x opencode.jsonc -x local.properties` of the run's copy against `g-fresh` is empty (the agent's edit was removed); E1 and E7 at the end. Then, in order: `droidyard call workspace.close` on the copy, after which no `processes.json` pid of that root (the sidecar) is alive; E5's wipe and stop; the `verify-daemon` §Cleanup snapshot, `droidyard stop` and its orphan count, 0 (the adb server, Gradle and Kotlin daemons excluded and listed, as §Cleanup says). Recorded, not asserted: wall time, tool calls, failed calls, tokens where the client reports them, daemon and sidecar RSS.

**Defects.** A Droidyard result that is wrong, unstructured or unusable where the documents say otherwise is a defect. Each gets its own step, inserted before the close (which is renumbered), with a test that fails before the fix and the review `milestone-workflow` §Verification by risk sets; the run that found it is then rerun from a fresh copy. A defect whose fix needs a design decision is recorded and goes to the owner, and the acceptance item it blocks stays unticked. A fault in the sample is fixed in the sample (its own small step) and recorded.

## Steps

### 1. The sample app

- Builds: `samples/golden/` per D1–D3: `settings.gradle.kts` (its header says what the sample is for and that it is built only in copies), `build.gradle.kts`, `gradle.properties`, `gradle/wrapper/gradle-wrapper.properties` (8.13), `build-logic/` (one convention plugin), `app/` (manifest, `MainActivity.kt` with the marker and the log line, the navigation), `feature/golden/` (the two screens).
- Implements: the owner's decision of 2026-10-07; `mvp.md` §Validation target: DopamineControl (the role, D3); D1, D2.
- Run by: the implementer (`ds-implement.sh`), iterating with Gradle in temporary copies outside the repository under S6; the main session first confirms E7 and takes S3's stamp, then briefs it with S6's lines; the check by the main session.
- Check: level 4, main session. The implementer's `<out>` log greps clean of `install`, `uninstall`, `connected` and `device` task names; `adb devices` lists no device. `g-build` per S2, trusted under `state-build`; a tagged daemon with `ADB=/nonexistent`, `ANDROID_ADB_SERVER_PORT=5990`, `ANDROID_SERIAL` per E6 and an empty `ANDROID_AVD_HOME`: `project.modules` → `:app`, `:feature:golden` (the full list recorded for G1); `sdk.requirements` → `complete: true`, `missing: []`; `build.assemble {":app","debug"}` → `success`, `outputs[0].applicationId` `dev.droidyard.golden`, and `aapt2 dump xmltree` (or `apkanalyzer`) of the APK shows one launcher activity; the marker line found in `MainActivity.kt` and the call inserted after it in the copy → `build.assemble` `failed`, `build.errors` → `tool: kotlin`, `source: console`, the file and the expected line and column (`<file>`, `<line>`, `<column>` recorded under Changes); the call removed → `success`. S3 unchanged; `git status --ignored samples/` shows only the committed files (nothing was built in the repository); E1 (nothing on 5990). The decisive check is the SDK stamp: it fails if AGP installed or rewrote any package.
- Schema: no change.
- Needs: the green baseline.
- Not here: a device (step 3 runs the app); instrumented tests (D3).
- Rests on: the Compose libraries resolve for the D2 cell, from the cache or a download into `~/.gradle` (level 4, this check).
- Review: the main session's review of the files (an acceptance fixture, no contract).
- [ ] Done. Evidence:

### 2. Client preflight and the first agent build, no device

- Builds: no repository file. The run kit under `$B`: `g-pre`, `state-pre`, the recorder, each client's project configuration (C2–C4), `cursor-data/`.
- Implements: `mvp.md` §Risks and unknowns (MCP client differences); golden-path steps 1–3 with OpenCode; C1–C5; A5 in part.
- Run by: the main session; E7 holds (the owner's emulators shut down).
- Check: tagged build; a daemon under `state-pre` with `ADB=/nonexistent`, `ANDROID_ADB_SERVER_PORT=5990`, `ANDROID_SERIAL` per E6, `ANDROID_AVD_HOME=$B/avd` (empty); `g-pre` trusted; the recorder's shim given `--wait 2s`, so a cold `project_modules` comes back as an operation view. (a) Cursor: `cursor-agent --help`, `--version`, `models` (R5's id listed; never `status` or `login`, C5); C2's configuration and `mcp enable droidyard`; `mcp list`, `mcp list-tools droidyard` (34); then `cursor-agent -p --output-format stream-json --model <R5> --workspace g-pre "<probe>"`: "Call droidyard's system_health and workspace_describe; call project_modules and, if it returns a running operation, operation_wait until it completes, then list the modules. Then try to run `ls` with your own shell and to read settings.gradle.kts with your own file tool, and say whether you could." Passes when droidyard calls succeed without `-f` or `--approve-mcps`; the wire shows `initialize`, 34 tools, `system_health` naming `/nonexistent` as the rejected adb (the session reached `state-pre`), `workspace_describe` `trusted: true` on `g-pre`, `project_modules` → `running`, then `operation_wait` → step 1's list; the stream shows the shell and read attempts refused, never executed; no process of the global or plugin servers ran (their command names checked before, during and after; names only recorded); C1 holds; nothing new under `~/.cursor/projects` (else listed). (b) OpenCode: `opencode --version`, `opencode debug agents` (C3), the same probe; then, in a new `opencode run`, the prompt's items 1–3: the wire as G1 (with `system_health` naming `/nonexistent`), G2, G3 and G9; the run hygiene's `diff` of `g-pre` against `g-fresh` empty; no task matching E6. (c) A5's fields recorded for both. E1 (5990 empty) and E7 at the end. If Cursor cannot run headless, cannot be kept to droidyard alone, or ignores a denial, the plan records exactly what it cannot do (which tool, which rule, what the stream showed) under Changes, step 5 does not run, A2 stays open, and the owner decides.
- Schema: no change.
- Needs: step 1; OpenCode Go's key; the signed-in Cursor CLI (C5).
- Not here: a device or the emulator; the golden path's device half.
- Rests on: Cursor in print mode calls the allowed server, loads no other, and honours the project's denies over the global allows (level 5, observed here).
- Review: the main session's record; the close verifier reads it.
- [ ] Done. Evidence:

### 3. Rehearsal on the scratch AVD, CLI only

- Builds: outside the repository: `$B/bin/adb` (E4), the AVD `droidyard_m1l` (E5), `g-reh`, `state-reh`.
- Implements: §Emulator rules; the golden path's device half by CLI, settling Unverified rows before an agent depends on them.
- Run by: the main session.
- Check: (a) the guard in dry run, with a protected serial written for the test: `emu kill`, `install`, `push`, `shell monkey`, `exec-out screencap -p`, `shell input`, `shell uiautomator dump`, `logcat`, `forward`, the UI reader's start command, `shell getprop x; reboot` and `-t 1 emu kill` refused; `devices -l`, `emu avd name`, `shell getprop`, `shell getprop sys.boot_completed` passed. (b) The tagged daemon (E2, E4, E6, S1): `system.health` names the guard; `emulator.start {avd: "droidyard_m1l"}` → an unprotected serial, `adopted: false`; `device.list` with `refused.log` still empty; `build.assemble {":app","debug"}` (cold-copy wall time recorded: it says whether G2 needs `operation_wait`); the E6 probe, only if a live `adb devices` taken just before shows the scratch emulator alone: `build.run {tasks: [":app:installDebug"]}` → fails with a message naming `droidyard-m1l-none` (any other failure does not count), the Gradle daemon pid that served it recorded (new or reused); else skipped, its row stays Unverified; `device.install`, `device.launch` → pid; `device.logs {since: "launch"}` → `filter.by: uid`, the `GoldenApp: started` line; `device.screenshot`; `device.uiTree` → ids `name`, `submit`; G7's four calls through `droidyard call`, each `matched: true`; G8's crash and `device.logs` → the crash summary; E5's wipe and stop; the run hygiene's close, stop and count. E1, E3, S3 unchanged. The selectors that resolved each G7 target are recorded under Changes. Stops: the app crashes on start on API 31, a guard refusal, a Compose node that does not resolve (a defect, or a sample fault).
- Schema: no change.
- Needs: step 1 (the sample), step 2 (the kit).
- Not here: an agent.
- Rests on: the guard passes only listings to a protected serial and everything `device.list` needs (level 4: the dry run, then an empty `refused.log` after a live listing); Droidyard's reader resolves and taps Compose nodes by `testTagsAsResourceId` ids and verifies `appears` and `gone` on them (level 4, here).
- Review: the main session's record.
- [ ] Done. Evidence:

### 4. The golden path from OpenCode

- Builds: no repository file; `g-opencode`, `state-opencode`.
- Implements: `mvp.md` §Phase 1 (Acceptance) with OpenCode; A1, A3, A4, A5.
- Run by: the main session, under every rule above; E7 holds.
- Check: §The golden-path run with OpenCode (`opencode run --standalone --format json --print-logs --auto -m opencode-go/deepseek-v4.1-flash`, cwd `g-opencode`): G1–G9 and the run hygiene, each from `wire.*.jsonl` and `opencode.out`.
- Schema: no change.
- Needs: step 3.
- Not here: Cursor; the comparison (R2).
- Rests on: —
- Review: the main session's record; the close verifier reads it.
- [ ] Done. Evidence:

### 5. The golden path from Cursor

- Builds: no repository file; `g-cursor` (C2's configuration and its own `mcp enable droidyard`), `state-cursor`; the scratch AVD wiped and stopped after step 4.
- Implements: `mvp.md` §Phase 1 (Acceptance) with Cursor; A2, A3, A4, A5.
- Run by: the main session; E7 holds.
- Check: §The golden-path run with `cursor-agent -p --output-format stream-json --model <R5> --workspace g-cursor "<prompt>"`: G1–G9 and the run hygiene, from `wire.*.jsonl` and the stream.
- Schema: no change.
- Needs: step 2 (a) passed; step 4 (the prompt proven against the scenario).
- Not here: Claude Code and Codex (S2's other clients).
- Rests on: —
- Review: the main session's record; the close verifier reads it.
- [ ] Done. Evidence:

### 6. Close: the recipe and the documents

- Builds: `.claude/skills/verify-daemon/SKILL.md`: an m1l block (these rules by reference, the copy, the guard, both clients' configurations, the prompt, the G checks as `jq` lines). Documents, by the main session: `mvp.md` (§Validation target: DopamineControl and §Decisions fixed by this MVP: the sample is the acceptance target by the owner's decision of 2026-10-07, quoted, with DopamineControl's ratified role recorded as superseded and why; the Compose row of §Risks and unknowns; §Phase 1's acceptance marked; §Definition of done item 1 ticked only if A1–A3 hold, the tap-and-input item if G7 held in both runs, the orphan item if A4 held); `docs/README.md` (Where the work stands: Phase 1 accepted or not, the evidence paths, the next milestone); `mcp-tool-surface.md` (the two S2 assumptions on image content, with what each client did); `docs/plans/README.md`. The scratch AVD deleted (E5).
- Implements: A7.
- Run by: the main session.
- Check: `go test ./internal/doccheck` green over the edits; `grep -rn DopamineControl docs/roadmap/mvp.md docs/README.md` shows no line still naming it as the acceptance target; the m1l block's commands are the ones steps 4–5 ran (copied from their evidence); the Opus verifier over A1–A7, reading the evidence directories and rechecking R2's classification of every failed attempt.
- Schema: no change.
- Needs: steps 1–5 and any defect steps.
- Not here: Phase 2 planning; editing the closed ratifications record (R1).
- Rests on: —
- Review: the milestone's close: Opus verifier.
- [ ] Done. Evidence:

## Not in this milestone

- DopamineControl, in any form (the owner's decision).
- The comparison of agent steps, tokens and failures against a raw shell (N ≥ 5 per arm, a pilot, a threshold fixed first): its own plan after Phase 1 (`mvp.md` §Metrics).
- The idle footprint report per process; the browser golden path (Phase 2); `build.errors` on a KGP ≥ 2.4 fixture (CI fixtures; DoD); instrumented tests for the sample (D3).
- The rest of spike S2: Claude Code and Codex, interrupt cancellation, whether progress resets a client's timeout.
- A product fence for Gradle device tasks (E6 is a run rule; `build.run` accepting any task is ADR-0017's design); physical devices, Linux, `git.*`.

## Design questions

None open. Q1 of the first draft (DopamineControl has no text field, no `testTagsAsResourceId` and no crash path, so golden-path step 7 had no target in it; the gap came from the sample app's specification being retitled when DopamineControl replaced it, commit `7c9164d`) was answered by the owner on 2026-10-07 with the sample app (header). Its draft, an overlay on a DopamineControl copy and a new ADR, is dropped.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| A Compose BOM resolves with Gradle 8.13, AGP 8.13.2, KGP 2.0.21 and its compose plugin, `compileSdk` 36, build-tools 35.0.0, with no SDK package installed; one whose libraries carry `kotlin-stdlib` ≥ 2.2 metadata will not compile on KGP 2.0.21 (the BOM matching the cached Compose 1.7.0 / material3 1.3.0 is the low-download choice, its version not checked) | 1–5 | step 1 (the SDK stamp, `sdk.requirements`) |
| A small `build-logic` included build works under the sidecar's model fetch | 1–5 | step 1 (D2: dropped if not) |
| `cursor-agent` runs headless (`-p`) with an MCP server and Grok 4.6, no workspace-trust prompt (signed in, per the owner) | 2, 5 | step 2 (a) |
| With `Mcp(droidyard:*)` allowed, print mode calls droidyard without `-f` (the round-1 reviewer's reading of the bundle) | 2, 5 | step 2 (a) |
| Cursor loads neither the global `github`/`Figma` servers nor the `firebase` plugin's when only droidyard is enabled | 2, 5 | step 2 (a) |
| The project's denies beat the global `Shell(…)` allows in print mode | 2, 5 | step 2 (a) probe |
| Cursor's tool timeout outlasts droidyard's 20 s wait budget, and it follows the operation view with `operation_wait` | 2, 5 | step 2 (`--wait 2s`), step 5 |
| Cursor accepts `structuredContent` and image blocks (S2) | 5 | step 5 |
| OpenCode is still v2.0.18 with the same behaviour | 2, 4 | step 2 (b) |
| AGP's device tasks honour `ANDROID_SERIAL`, in a new and in a reused Gradle daemon | 2–5 | step 3's probe (one of the two cases, recorded); E7 carries the fence either way |
| The sample runs on API 31 `google_apis`, and the crash is attributed by uid there as the fixture's was (1i) | 3–5 | step 3 |
| The reader resolves and taps Compose nodes, with `testTagsAsResourceId` ids, and `gone` holds after an edit (m1f's open row) | 3–5 | step 3, then steps 4–5 |
| The guard passes everything `device.list` runs on a protected serial since 1i | 3 | step 3; a refusal stops the run |
| A running owner emulator leaves its `userdata-qemu.img*` mtimes alone during step 3 | 3 | m1i's evidence; else E3 compares only stopped AVDs, recorded |
| One passing run per client is evidence of the path, not of reliability | 4, 5 | the comparison (R2) |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | B1: AGP's `install*`/`uninstall*`/`connected*` tasks reach every device through ddmlib, past the guard, and the prompt invited `build_run :app:installDebug` | E6: `ANDROID_SERIAL=droidyard-m1l-none` in every daemon and a hygiene row on task names; the prompt names `build_assemble` and `device_install`; E7 (round 2) closes the rest |
| 1 | revise | B2: without an `Mcp(droidyard:*)` allow, Cursor's print mode denies every droidyard call, and `-f` is forbidden | C2 allows `Mcp(droidyard:*)`; step 2 (a) passes only on a droidyard call without `-f` or `--approve-mcps`; the `ls` probe tests project deny over global allow |
| 1 | revise | B3: `git checkout -- .` left untracked files in the DopamineControl copy | moot after the owner's decision: copies come from `samples/golden` at the commit under test (S2) |
| 1 | revise | B4: a console `artifact_read` was silently allowed under "no stdout parsing" | R6, on the owner list; G9 (d) checks the order |
| 1 | — | N1 more before Q1; N2 the guard misdescribed, rows lost, static `protected`; N3 per-copy approvals, the plugin server, C1's files; N4 the shim's state; N5 `diff` over `.git`; N6 no `workspace.close` before the count, DoD tap/input; N7 the original's index; N8 R2 undefined; N9 the model unnamed; N10 Q1 sharper; N11 R1's fallback | Taken: step 2 runs G1–G3 with OpenCode before any device; E4 states the allowlist, step 3 restores `logcat`, `forward`, `push` and the reader's start, E7; C1, C2, step 5 per copy; C4 exports the state, G1 checks it; the hygiene closes the workspace first, step 6 ticks the tap/input item; R2 defined; R5 names the model. N5, N7, N10, N11 moot after the owner's decision (no `.git` in the copies, DopamineControl unused, Q1 answered) |
| 2 | revise | B1: step 1 ran agents outside E6: its daemon had `ANDROID_ADB_SERVER_PORT=5990` and no `ANDROID_SERIAL`, so a device task could start a new adb server on 5990 attached to the owner's emulators | E6 now covers every daemon of the milestone; step 2 (the old step 1) runs under E7 and the task hygiene; E1 checks nothing listens on 5990 after every run |
| 2 | revise | B2: shutting the owner's emulators down was a request, not a precondition, while the `ANDROID_SERIAL` fence stayed unproven | E7: agent runs (steps 2, 4, 5) do not start while any emulator but the scratch one is online; CLI-only step 3 keeps 1i's rule, and the listing reading applies only there |
| 2 | — | N1 the E6 probe from a live `adb devices`, its failure naming the serial, the Gradle daemon pid; N2 the orphan count cannot be 0 before the stop; N3 step 1 (b)'s `diff` without excludes; N4 G1's guard check under `ADB=/nonexistent`; N5 ignored files carried by S2 | All taken: step 3's probe; the hygiene checks the sidecar gone after `workspace.close` and counts after `droidyard stop`; step 2 (b) uses the hygiene's `diff`; step 2 checks `system_health` names `/nonexistent`; N5 moot (fresh copies of the committed sample) |
| 3 | revise | B1: step 1's implementer ran Gradle before the SDK stamp and outside E6/E7; its `guard-bash.sh` profile lets `installDebug`, `uninstall*` and `connected*` through | S6 and step 1: E7 and the stamp before the implementer's first run, `ANDROID_SERIAL` exported, D2's `gradle.properties` from the first Gradle run, no device task, and the check greps the implementer's log and runs `adb devices` |
| 3 | — | N1 `android.useAndroidX` must be `true` for Compose; N2 the cache overstated (no BOM, the plugin marker only at 2.4.20); N3 G8 traces to §Validation target, not the golden path; N4 D3's tag casing; N5 `cp -R` copies the working tree | All taken: D2 lists the three properties; the Host line and the first Unverified row corrected; G8's row and A1 cite §Validation target; D3 names the tags; S2 uses `git archive` of the commit |
| owner | decision | C5, R5: "Se puede usar si, usa grok 4.6 mismamente, que está gratis para mi. Está el login hecho." (2026-10-07) | C5 and R5 answered: the signed-in account, `--model cursor-grok-4.6-high`; never `status` or `login`; an authentication failure stops the Cursor half only |
| owner | decision | Q1: "Si dopamine control no te sirve, create una app de ejemplo sencilla que te haga el mismo papel" (2026-10-07) | The target is a sample app written in step 1 (D1–D3); the overlay ADR and R1's DopamineControl reset dropped; the deviation recorded in the header and in `mvp.md` at the close |

Rounds 1 and 2 reviewed the DopamineControl draft; round 3 was a diff-focused review after the owner's decision, as the coordinator asked. Every blocking finding is answered in the plan, and no design question is open, so the plan is `Reviewed`; a further look is a diff-only check by the main session (`milestone-plan` §Writing one, item 7).

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design. Step 1's `<file>`, `<line>`, `<column>` and module list, step 3's G7 selectors, and the prompt as fixed are recorded here before step 4.
