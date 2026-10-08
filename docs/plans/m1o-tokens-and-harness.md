# Plan: Milestone 1o — tokens, assistant integration, and the comparison with Cursor

- Status: Approved
- Approved: 2026-10-08, Delegated: the main session took Q1's and Q2's draft decisions under the owner's standing instruction; the owner may revert either.
- Date: 2026-10-07; revised 2026-10-08 for 1p and 1q
- Milestone: not in `docs/README.md`. The owner's brief of 2026-10-07 (see `m1n-daily-use-core.md`'s header), items D (tokens) and E (harness integration, Cursor first, then OpenCode), with "a Cursor arm added to the m1m comparison kit and the comparison rerun after D and E, to measure the gains rather than estimate them". `m1n-daily-use-core.md` (A, B, C, F, I, J) and `m1p-functional-gaps.md` (G, H) are `Done`; the rerun measures the device path they leave.
- Phase: `docs/roadmap/mvp.md` §Definition of done (the recorded comparison, rerun), §How the comparison is run, §Out of scope (skills and distribution, in part moved in by Q2).
- Contract: `docs/interfaces/mcp.md` §Descriptions, §Server instructions, §Results, §Untrusted data, §Schemas on the wire, §What is exposed; `docs/interfaces/mcp.md` §Features beyond tools (prompts, assistant-side hooks); ADR-0004, ADR-0011, ADR-0014 §Decision, ADR-0019 §Processes 3, 4 and ADR-0024 §Decision (superseded in part by Q2's ADR), ADR-0015 §1, §5, §6, ADR-0020 §Adapter projections and ADR-0019 §Processes 7 (superseded in part by Q1's ADR), ADR-0019 §Processes 8, ADR-0025 §Decision; `docs/architecture/capability-api.md` §Discovery and versioning; `docs/research/daily-use-tokens-2026-10.md` §4; `docs/research/daily-use-harness-2026-10.md` §3, §4, §5.

**The owner should see:** Q1 (published contract changes, all additive: shorter descriptions, `crashes[]` entries with causes and frames, `maxLines` 50, a compact text rendering the model reads (crash lines folded, `system_capabilities` without descriptions) while `structuredContent` keeps the full result, an opt-in `core` tool profile); Q2 (one skill and `droidyard init` leave `mvp.md` §Out of scope; two subcommands, `init` and `hook`, neither auto-starting the daemon; ADR-0014 superseded in part: generated per-assistant configuration and a generated OpenCode shim holding no logic; the read-only allowlist also auto-approves tools that run project code, `project_*`, `build_tasks`, `sdk_requirements`); D1 (the measurement: four arms, 40 runs, about three to four hours of runs on Cursor's and OpenCode's subscriptions; a `dys` arm with the agent's own shell, droidyard as used daily); step 2's count of shell commands a hook would redirect is the evidence `mcp.md` §Features beyond tools (assistant-side hooks) asks for ("if a real need appears"), and if it is zero the owner hears it before step 7.

## Scope

| Brief item (2026-10-07) | Here? | If not, where and why |
| --- | --- | --- |
| D: descriptions to `mcp.md`'s rule, enforced by a test, safety caveats kept | yes | step 3 (Q1 ¶1) |
| D: the selector schema not repeated three times | yes | step 3 (Q1 ¶1: nested copies bare; no `$ref`, which §Schemas on the wire forbids) |
| D: compact `device_logs`; compact `system_capabilities` by default | yes | steps 4, 5 (Q1 ¶3: the schema stays additive, the compaction is in the rendering) |
| D: boilerplate dropped from success results; a text rendering of the uiTree | yes | step 5 (Q1 ¶2: in the rendering, not the schema) |
| D: a "core" tool profile | yes | step 6 (Q1 ¶4) |
| E: server instructions | yes | step 3 |
| E: an `AGENTS.md` block and one `SKILL.md` | yes | step 8 (Q2) |
| E: a Cursor `beforeShellExecution` redirect; one Go handler for Cursor, Claude Code/Codex and an OpenCode plugin | yes | step 7 (Q2) |
| E: read-only tools allowlisted; session-start facts and warm-up | yes | step 8 |
| E: a stop-time build check (optional in the brief) | no | cut: §Not in this milestone |
| E: a `droidyard init`-style command | yes | step 8 |
| E: the ADR moving skills and distribution, superseding ADR-0014 in part, with `mcp.md` §Features beyond tools (hooks) | yes | Q2, written at step 7 |
| A Cursor arm in the kit; the comparison rerun after D and E | yes | steps 1, 2, 10 (D1) |
| `mvp.md` §Definition of done: the comparison, now with Cursor | part | step 11 records it; the other open items stay open |

## Starting point

Expected: `main` at the 1q merge (`cc0c27b`, the branch base): API `0.12.0`, 39 capabilities, 34 tools; the four commands recorded at step 1. Measured 2026-10-08 at `cc0c27b` with the tokens note's §1 method (a `go test -overlay` file building `serve.go`'s server, then `initialize` and `tools/list`): `tools/list` is 142,483 B, of which `outputSchema` 75,000, descriptions 39,227, `inputSchema` 21,913; 66.7 KB without `outputSchema`; the instructions 860 characters; 31 of 34 descriptions exceed two sentences or 400 characters; the selector object is inlined three times in `device_inputText` and `device_tapElement`. (The note's figures at `3abf127` were 130,942 B and 60.5 KB: 1n and 1p lengthened descriptions.) `adapters/mcp/mcp.go`: `Instructions` is `mcp.md` §Server instructions verbatim; a result's text is the untrusted first line (when marked) plus the compact JSON result, and `markUntrusted` checks it is one line (`mcp.go:759-768`), the only central guard on untrusted text. `cmd/droidyard`: `serve`, `mcp`, `call`, `ui`, `trust`, `status`, `stop` (ADR-0019 §Processes 3's set), plus the developer commands `caps`, `schema`, `version`. No `skills/` directory. The kit (`~/Library/Caches/droidyard-dev/m1m/kit/`) runs OpenCode only: K7's fence is a PATH and `opencode.jsonc` deny rules, its binary `$K/dy` reached through `rec.sh` with `DROIDYARD_STATE_DIR` and `--no-autostart`, and `metrics.py` keys arms by two characters. m1l ran Cursor headless (`m1l-phase1-acceptance.md` C1, C2, C5).

## Run rules

- K1. `B=~/Library/Caches/droidyard-dev/m1o`; a copy of m1m's kit at `$B/kit`; the scratch AVD `droidyard_m1o` (API 31 `google_apis` arm64, as m1m's) is the only one booted, wiped or stopped. m1m's K2–K7 apply with `m1o` for `m1m`.
- K2. Cursor, as m1l C1, C2, C5: `cursor-agent -p --trust --output-format stream-json --model cursor-grok-4.6-high --workspace <copy>`; never `-f`, `--yolo`, `--approve-mcps`, `cursor-agent status` or `login`; `CURSOR_DATA_DIR=$B/cursor-data`; the global `~/.cursor/mcp.json`, `~/.cursor/hooks.json` byte-identical and `~/.cursor/cli-config.json`'s permissions unchanged before and after every run; `github` and `Figma` disabled in the scratch data dir only.
- K3. **K7 for Cursor's shell arms** (`sh-cu`, `dys-cu`): cursor-agent is launched with K7's full environment (`env -u ANDROID_HOME -u ANDROID_SDK_ROOT`, `PATH=$B/kit/bin:/usr/bin:/bin:/usr/sbin:/sbin`, `JAVA_HOME` zulu-17, `ANDROID_AVD_HOME=$B/avd`, `DROIDYARD_STATE_DIR` the run's), and the copy's `.cursor/cli.json` allows `Shell(*)`, `Read(**)`, `Write(**)` (grade 3 needs an edit) and denies `Shell(kill *)`, `Shell(pkill *)`, `Shell(killall *)`, any path to the SDK's `platform-tools/adb`, and `~/.android/avd`; a denied command counts as m1m D3 says. Step 1 (a0) proves it before any shell arm runs.
- K4. **Pinned clients**: step 1 copies the current `cursor-agent` version directory (`~/.local/share/cursor-agent/versions/<v>/`) and the `opencode` binary into `$B/clients/`, and every run uses those copies; their versions are recorded before every run. If a copy cannot run, the run stops and the owner is told; a fresh before-measurement means building the branch base (`cc0c27b`) into `$B/kit-before/`. The real state root (`~/Library/Application Support/droidyard/`, absent today) is absent, or its `daemon.lock` unchanged, before and after every run.
- K5. m1n's K2 (the owner's constraints) in force.

## Delegated decisions

- D1. **The measurement.** Arms: `sh-cu` (Cursor, its own shell under K3, no droidyard); `dy-cu` (Cursor, droidyard only: its own shell, read and write denied, `Mcp(droidyard:*)` allowed, as m1l C2); `dys-cu` (Cursor, droidyard and its own tools under K3, `Mcp(droidyard:*)` allowed); `dy-oc` (m1m's `dy` arm). **Before** (step 2, at the branch base): `sh-cu`, `dy-cu`, `dys-cu`, `dy-oc`, five each, rotated so no arm runs twice in a row. **After** (step 10): the same four arms, five each, `dys-cu` now with `droidyard init --client cursor` in the copy; `sh-cu` rerun as the drift control. Metrics: m1m D3, plus `tools/list` bytes on the wire, `execute`/`search` calls in `dy-oc`, and in `dys-cu` the shell commands D4 would redirect (step 2, counted offline) or did redirect (step 10), reported apart from failed calls; m1m's threshold reads failed calls both with and without redirects. Grades: m1m D4. Gains: before/after medians per arm, beside `sh-cu`'s drift, reported, not thresholded; `dy-oc`'s gain has no drift control of its own (OpenCode is pinned by K4; the model API is not) and is labelled so. Cursor's verdicts: m1m's threshold (`mvp.md` §How the comparison is run), `dy-cu` and `dys-cu` each against `sh-cu`. Arm names are keyed in full by `metrics.py`.
- D2. **Prompts**, fixed at step 1 before any run: `sh-cu` m1m's `prompt-sh.txt`; `dy-cu`, `dy-oc` m1m's `prompt-dy.txt`; `dys-cu` the task of `prompt-dy.txt` with every instruction about which tools to use removed, keeping "never stop or restart the adb server".
- D3. **The core profile**: `workspace_open`, `workspace_describe`, `project_modules`, `build_assemble`, `build_errors`, `build_lastResult`, `file_list`, `file_read`, `file_write`, `emulator_list`, `emulator_start`, `device_list`, `device_install`, `device_launch`, `device_uiTree`, `device_tapElement`, `device_inputText`, `device_logs`, `device_screenshot`, `operation_wait`, `operation_cancel`, `artifact_read` (22, the golden path's and the reopen ADR-0020 allows).
- D4. **The redirect map**, exact equivalents only, decided without the daemon (in `internal/assistant`, naming registered tools). Device rows apply only to `adb -s emulator-<n> …` or `-s <a serial the configuration admits>` (without `-s`, or for an unadmitted physical device, the tools would refuse, so the command is allowed): `uninstall <pkg>` → `device_uninstall`; `shell input tap`/`input text` → `device_tapElement`/`device_inputText`; `shell uiautomator dump`, `shell screencap`/`exec-out screencap` → `device_uiTree`, `device_screenshot`; `logcat` with `-d`, no `-c`/`-G`, and only main, system or crash buffers → `device_logs`; `shell monkey -p <pkg> -c android.intent.category.LAUNCHER 1` → `device_launch`. `emulator -avd <name>` → `emulator_start`. `gradlew`/`gradle` whose only task is one module-qualified `:<module>:assemble<Variant>`, on a root the configuration trusts (read from the file, as `session-start` does) → `build_assemble`. Everything else is allowed, explicitly including `adb install` (the tool installs only outputs in the daemon's build registry), `adb devices`, a bare `assembleDebug`, any command on an untrusted root, `input keyevent`, `input swipe`, `am start` with an action or component, `logcat -c`, `logcat -b events`, and any other Gradle task. A test asserts every named tool exists.
- D5. **What `init` writes, and how**: the command in every entry is an absolute path (a GUI-launched client has no shell `PATH`, ADR-0019 §8): the `droidyard` found on `PATH` without resolving its symlink when it is the running binary, else the running binary's path unresolved, so a package-manager upgrade keeps it valid. `AGENTS.md` between `<!-- droidyard:begin -->` and `<!-- droidyard:end -->` (≤ 5 lines) and `.agents/skills/droidyard-android/SKILL.md`; for `--client claude` only, `CLAUDE.md` and the copy in `.claude/skills/`; Cursor's hook entries carry a `matcher` built from D4's prefixes, so other commands spawn nothing; `cursor`: `.cursor/mcp.json` (`mcpServers.droidyard`), `.cursor/cli.json` (`permissions.allow`: `Mcp(droidyard:<tool>)` for every tool whose `readOnlyHint` is true), `.cursor/hooks.json` (`beforeShellExecution`, `sessionStart`); `claude`: `.claude/settings.json` `PreToolUse` (matcher `Bash`) and `SessionStart`; `opencode`: `opencode.json` `mcp.droidyard` with `codemode: false` (the measured configuration) and `.opencode/plugins/droidyard.ts`, a generated file that only calls `droidyard hook opencode`. JSON objects are merged by key, and **an existing entry under the same key is kept and reported, never replaced**; in arrays (hook lists, `permissions.allow`) droidyard's element is appended only if no equal element exists, and the user's elements are never touched; a file that does not parse, or an `opencode.jsonc` (comments would be lost), is refused and named, untouched, with the entry to add printed. `--uninstall` removes only the keys, elements and marked blocks equal, after parsing, to what this `init` would write now, and the files it would create whole when unchanged; anything else is reported, not removed. "Unchanged" for JSON means equal after parsing. Codex: the hook handler answers its shape; `init` writes nothing for it.
- D6. **The skill's source**: `skills/android/droidyard-android/SKILL.md` (ADR-0011 §Decision's layout), written by the main session, embedded in the binary through a small Go package at `skills/` (`go:embed` cannot reach a parent directory), with the tool map generated into its `references/` by a test that fails on drift.

## Acceptance

- [ ] A1. Each arm runs headless under K1–K5 to exit or cap, the shell arms behind a proven fence; the before-measurement is recorded per D1, with the count of redirectable shell commands. Source: brief E (the Cursor arm); `mvp.md` §How the comparison is run. Proven by: step 1, step 2.
- [ ] A2. Every description has at most two sentences and 400 characters; the only longer ones are the listed safety exceptions (a repeat repeats an effect; `expect` vacuous; `gone` needs two reads; `emulator_start`'s cancel, which `mcp.md` §Long-running work, its last bullet, requires; `shell_exec`'s reach), each one extra sentence; property descriptions at most 120; nested selector copies carry none; a test enforces it; `tools/list` without `outputSchema` is at most 35 KB. Source: `mcp.md` §Descriptions; Q1 ¶1. Proven by: step 3.
- [ ] A3. The server instructions (≤ 2,000 characters) carry the shared manual (selectors, the untrusted rule, never repeat an action call, the golden order, droidyard tools for adb and Gradle actions, tools are called directly) and equal `mcp.md` §Server instructions. Source: Q1 ¶1. Proven by: step 3.
- [ ] A4. `device.logs`'s `crashes[]` entries gain `causes` and up to five app `frames`, keeping every existing field; `maxLines` defaults to 50; the change is additive. Source: Q1 ¶3. Proven by: step 4.
- [ ] A5. Over MCP, `device_uiTree`, the action results (with `returnTree`), `device_logs` (crash blocks as one entry each, not their lines), `system_capabilities` (names, flags and availability) and the build results render as compact text, through `operation_wait` too; `structuredContent` is the full result; every untrusted-tagged string is JSON-quoted and the untrusted first line holds; on the m1m recordings tree and log text is at least 50 % smaller. Source: Q1 ¶2; ADR-0015 §1. Proven by: step 5.
- [ ] A6. `droidyard mcp --tools core` lists D3's 22 tools; without the flag every tool is listed. Source: Q1 ¶4. Proven by: step 6.
- [ ] A7. `droidyard hook <client> <event>` denies exactly D4's commands, naming the tool, in Cursor's, Claude Code's and OpenCode's shapes; allows the rest, the near misses included; allows on any error of its own; never starts a daemon. Source: Q2 ¶3. Proven by: step 7, step 9.
- [ ] A8. `droidyard init` writes D5's files, is idempotent, keeps the user's content and existing entries, refuses unparseable files and `opencode.jsonc`, and `--uninstall` restores the tree. Source: Q2 ¶2. Proven by: step 8.
- [ ] A9. In Cursor (`cursor-agent -p`) a denied `adb shell input tap` names `device_tapElement`; the session-start context reaches the agent; a read-only droidyard tool runs under `init`'s allowlist alone and a non-read-only one does not. In OpenCode the plugin's denial text reaches the model, or what it shows instead is recorded. Source: brief E. Proven by: step 9.
- [ ] A10. The after-measurement recorded; gains, drift and Cursor's verdicts per D1; `mvp.md`, `mcp.md`, `docs/README.md` and the register updated. Source: brief E; `mvp.md` §Definition of done. Proven by: step 10, step 11.

## Steps

### 1. The Cursor arms, the fence, the probes, one pilot each

- Builds: no repository file. `$B/kit/`: `run.sh` and `reset.sh` take D1's arms; each Cursor copy gets K2's `.cursor/mcp.json` (the recorder), K3's or m1l C2's `.cursor/cli.json`, and `CURSOR_DATA_DIR`; `metrics.py` reads Cursor's stream and keys arms in full. D2's prompts.
- Implements: D1, D2; K3; the risks the later steps rest on.
- Run by: the main session; the four commands recorded on the branch base first.
- Check: (pre) K4's client copies run (`$B/clients/…/cursor-agent --version`, `opencode --version`). (a0) **the fence**: in `sh-cu`'s configuration the agent, asked to run them, shows `command -v adb` = `$B/kit/bin/adb`, K3's `PATH`, `ANDROID_HOME` unset, `emulator -list-avds` printing only `droidyard_m1o`, `adb kill-server` refused by the wrapper, `~/Library/Android/sdk/platform-tools/adb version` and `ls ~/.android/avd` denied; a `kill -0 1` denied. **Decisive**: otherwise no shell arm runs, and the owner is told. (a) **Probes**, in a scratch copy: a throwaway `.cursor/hooks.json` whose `beforeShellExecution` and `sessionStart` commands append their stdin and environment to a file and deny `echo probe` with a marker: does `cursor-agent -p` run them, with which environment, and does the marker reach the model's next message; a throwaway OpenCode plugin whose `tool.execute.before` throws a marker on `echo probe` (`opencode run --standalone`): does the marker reach the model. (b) What Cursor's stream shows of a droidyard result (the text, `structuredContent`, or both); whether it reports tokens; whether `$B/cursor-data/projects/*/mcps/droidyard/tools/*.json` carry `outputSchema` and the stream shows the agent reading them. (c) One pilot per arm: exit or cap; `sh-cu` scores D4 grades 2 and 4; `dy-cu` calls only droidyard tools; K2 and K4 hold. **Decisive**: an arm that cannot run headless without a forbidden flag is dropped and the owner told; a hook that does not fire in `-p` moves A9's Cursor half to the IDE by hand, recorded under Changes before step 7; a hook environment without the run's `DROIDYARD_STATE_DIR` makes the kit's copies write it inline in the hook command (a kit edit, not `init`'s), recorded; no tokens in Cursor's stream makes its gains calls and wall time only; Cursor reading `structuredContent` makes step 5's gain OpenCode-only, recorded.
- Schema: no change.
- Needs: — (m1n, 1p and 1q are `Done`).
- Not here: the measurement (step 2).
- Rests on: —
- [x] Done. Evidence: level 5, 2026-10-08, `$B/runs/` (pre: `cursor-agent` 2026.10.01-e373342 and `opencode` 2.0.18 copied to `$B/clients/` and run from there; the four commands green at `cc0c27b`). (a0) held with Changes' deviation: `command -v adb` = `$B/kit/bin/adb`, `emulator -list-avds` = `droidyard_m1o` only, `adb kill-server` refused by the wrapper (5037 pid 19285 unchanged), the SDK adb by path, `./adb`, `ls ~/.android/avd`, `kill -0 1`, `killall` denied; `ANDROID_HOME` comes back from the login profile. (a) Cursor `-p` runs project hooks; the deny's `user_message` reaches the model, `agent_message` does not; `sessionStart` context reaches it; no `DROIDYARD_STATE_DIR` in the hook env. OpenCode's v2 plugin throw reaches the model. (b) text block only; `getMcpTools` 80,301 B without `outputSchema`; tokens in `result.usage`. (c) pilots: `sh-cu` (p2) 211 s, 51 calls; `dy-cu` 159 s, 21 calls, droidyard only; `dys-cu` 164 s, 22 calls, no shell command; `dy-oc` 101 s, 21 calls; each finished the task; K2 and K4 notes empty.

### 2. The before-measurement

- Builds: no repository file. `$B/runs/before/`.
- Implements: D1 (before).
- Run by: the main session, at the branch base, under K1–K5.
- Check: twenty valid runs (voids repeated and listed as m1m D5); per run m1m D3's metrics and D4's grades; per arm medians, min and max; Cursor's verdicts; `dys-cu`'s shell commands classified by the main session against D4's table (recorded, so step 7's tests replay them and must agree): the count a hook would have redirected. If it is zero in five runs, the owner hears it before step 7 (the need §8 asks for did not appear).
- Schema: no change.
- Needs: step 1.
- Not here: changes to droidyard.
- Rests on: —
- [ ] Done. Evidence:

### 3. Descriptions to the rule; the manual into the instructions (Q1 ¶1)

- Builds: the main session first writes Q1's ADR (next number, Accepted, Delegated; two `Supersedes:` lines, ADR-0020 §Adapter projections and ADR-0019 §Processes (item 7), each old file gaining its `Superseded in part by:` line). Then the implementer: every descriptor's `Description` and property descriptions per Q1 ¶1 and A2's exception list, the selector's `within` saying it excludes the boundary node itself (the register's `within` row, m1n step 9); the generator emits property descriptions on the first selector copy of a tool only. `adapters/mcp/mcp.go` `Instructions` and `mcp.md` §Server instructions, the same text, in the same change.
- Implements: Q1 ¶1; brief D (descriptions, selector), E (instructions).
- Run by: the implementer; review: the main session's diff review (description text).
- Check: `TestDescriptionsFollowTheRule` in `internal/schema` (fails today on 31 tools; the exceptions are a named list); `TestNestedSelectorCopiesAreBare`; `TestToolListModelFacingBytes` (`serve.go`'s server, `tools/list` without `outputSchema`, ≤ 35,000 B; 66.7 KB at `cc0c27b`); the instructions ≤ 2,000 characters and equal to the document's block; one assertion per safety exception; the `within` property description names the exclusion.
- Schema: descriptions and property descriptions only; no field, code or enum; API unchanged.
- Needs: step 2; Q1 accepted.
- Not here: result shapes (step 4); renderings (step 5).
- Rests on: —
- [ ] Done. Evidence:

### 4. Results that carry less (Q1 ¶3)

- Builds: `internal/domain/device/logs.go`: `crashes[]` entries gain `causes[]` and `frames[≤5]` (the app's package first, through the frame extractor m1n step 6 wrote for `appCrash`), tagged untrusted, every existing field kept; `maxLines` default 50 (in the descriptor's text too). No document: the fields are the descriptor's (`adb-device.md` §Logs says so).
- Implements: Q1 ¶3.
- Run by: the implementer; review: **Opus verifier** (a capability's semantics change).
- Check: `TestCrashEntryCarriesCausesAndFrames` (dy3's recorded crash read: `IllegalStateException`, `GoldenListScreen.kt` in `frames`, `exception` and `message` unchanged); `TestMaxLinesDefaultsTo50`; seen failing with the extractor bypassed.
- Schema: `device.logs` (`crashes[]` gains two fields with untrusted tags; `maxLines` default); additive; API `0.12.0` → `0.13.0`.
- Needs: step 3.
- Not here: folding crash lines and a compact `system_capabilities` (step 5's renderings: Q1 ¶3 keeps the schema additive).
- Rests on: —
- [ ] Done. Evidence:

### 5. Compact text renderings (Q1 ¶2)

- Builds: `internal/capability`: an unpublished descriptor hook `Text func(result any) string` and one quoting helper every renderer uses for tagged strings. Renderings: `device.uiTree` and the action results (one line per node: `ref "Class" "#id" "text" desc="…" flags [l,t,r,b]`, every device string quoted; system-bar views and unnamed single-child wrappers folded, refs still resolving; the rest of an action's result, `expect`, warnings, `appCrash`, `systemDialog`, as quoted lines), `device.logs` (`HH:MM:SS.mmm P/"tag": "message"`, consecutive same-header lines grouped, each crash block as its one `crashes[]` entry instead of its lines), `system.capabilities` (name, flags, availability; descriptions only in `structuredContent`), `build.assemble`/`build.run`/`build.lastResult` (outcome, failed tasks, outputs, `consoleRef`; empty and default fields omitted). `adapters/mcp/mcp.go`: the text block is the rendering when declared, for the tool and for `operation_wait`; `structuredContent` unchanged; `markUntrusted`'s one-line check replaced by the helper's guarantee. In the same change: `mcp.md` §Results.
- Implements: Q1 ¶2; brief D (boilerplate, uiTree text).
- Run by: the implementer; review: **Opus verifier** (the untrusted rendering).
- Check: golden renderings from `internal/domain/device/testdata/*-uitree.golden.json` and the m1m recordings; `TestEveryTaggedStringIsQuoted` finds every `x-droidyard-untrusted` string field of each rendered result type by reflection over the tags (so m1n's `appCrash`, `systemDialog` and any later field are covered), injects a newline, a code fence and "untrusted:" into each, and asserts no forged line; `TestOperationWaitRendersLikeTheTool`; `TestRenderedLineResolves` (a selector built from a rendered line, inlined text included, resolves to that node); `TestStructuredContentUnchanged`; byte counts on dy1–dy5's tree and log results ≥ 50 % smaller; seen failing with the helper bypassed for one field.
- Schema: no change.
- Needs: step 4.
- Not here: diffs between successive trees (tokens note §3, item 4); a `bounds` switch (bounds stay in the line).
- Rests on: the model reads the text block, not `structuredContent`: observed for OpenCode (tokens note §1.2), step 1 (b) for Cursor.
- [ ] Done. Evidence:

### 6. The core profile (Q1 ¶4)

- Builds: `droidyard mcp --tools core|all` (default `all`); the hello's optional `tools` field (ADR-0019 §Processes 7, superseded in part by Q1's ADR); the adapter lists only the profile's tools and answers a call to another with MCP's "tool not found"; the instructions say the `all` profile has the rest. In the same change: `mcp.md` §What is exposed, and the register's MCP rows.
- Implements: Q1 ¶4; D3.
- Run by: the implementer; review: the main session's diff review.
- Check: `TestCoreProfileListsTheGoldenPath` (D3's names, plus any tool a core tool's nextStep names: `TestCoreNextStepsNameCoreTools` greps the core domains' nextStep texts, and each tool found joins D3, recorded under Changes); `TestDefaultListsEverything`; an unknown profile refused by the shim before connecting; seen failing with the filter removed. The profile's `tools/list` bytes recorded.
- Schema: no change.
- Needs: step 3.
- Not here: per-project tool switches (harness note §5); measuring the profile in a run (no arm uses it).
- Rests on: —
- [ ] Done. Evidence:

### 7. `droidyard hook` (Q2 ¶3)

- Builds: the main session first writes Q2's ADR (next number, Accepted, Delegated) with steps 1–2's results, superseding in part ADR-0014 §Decision, ADR-0019 §Processes (items 3, 4) and ADR-0024 §Decision (the subcommands row), each old file gaining its `Superseded in part by:` line; `mcp.md` §Features beyond tools (hooks), `mvp.md` §Out of scope (one skill and `init` move in) and the register's skills row follow in the same commit. Then the implementer: `internal/assistant`, wired from `cmd/droidyard` (not an adapter: it parses command lines, reads the configuration and writes project files, which `overview.md` §Adapter boundary keeps out of `adapters/`) (D4's map; per-client decoding and answers: Cursor `permission`, `user_message` and `agent_message`; Claude Code/Codex `hookSpecificOutput.permissionDecision` and reason; OpenCode a JSON verdict the plugin turns into a throw) and `cmd/droidyard/hook.go`. `before-shell` contacts no daemon; `session-start` answers the root, whether it is trusted (read from the configuration file) and the one-line tool hint, and, only if a daemon already answers on the socket and the root is trusted, starts `project.modules` detached; it never starts a daemon; all within 2 s.
- Implements: Q2 ¶3; D4.
- Run by: the implementer; review: **Opus verifier** (it decides what the user's agent may run).
- Check: table tests per client: every D4 row denied with its tool named in both Cursor messages; D4's near misses (`adb install x.apk`, `adb devices`, `adb shell input tap` without `-s`, a physical serial not admitted, `./gradlew assembleDebug`, `:app:assembleDebug` on an untrusted root) and `ls`, `git status`, `cat adb.txt`, `./gradlew --version` allowed; step 2's recorded `dys-cu` commands replayed, agreeing with its count; garbage stdin, an unknown client or event → allow, exit 0; `TestRedirectMapNamesRegisteredTools`; `session-start` with no daemon starts none (the state root's `daemon.lock` absent after), and on an untrusted root starts nothing; `before-shell` under 50 ms; seen failing with fail-closed on parse errors and with a near miss denied.
- Schema: no change.
- Needs: step 2; Q2 accepted.
- Not here: writing the files (step 8); a stop hook.
- Rests on: Cursor's `-p` runs project hooks and shows the deny text to the model (level 5, step 1 (a)).
- [ ] Done. Evidence:

### 8. `droidyard init` and the skill (Q2 ¶2, ¶4)

- Builds: the main session writes `skills/android/droidyard-android/SKILL.md` (D6). The implementer: `cmd/droidyard/init.go` and `internal/assistant` (D5: write, merge, keep existing entries, refuse, `--uninstall`, `--client cursor|claude|opencode|all`, `--dry-run`); the skill embedded; its tool map generated. `init` never starts a daemon.
- Implements: Q2 ¶2, ¶4; D5, D6.
- Run by: the implementer; review: **Opus verifier** (writes into the user's project).
- Check: golden trees per client in a temp dir (commands are the binary's absolute path); a second `init` changes nothing; an `AGENTS.md` keeps the user's text byte-identical outside the marks, and a `.cursor/mcp.json` with the user's servers and an existing `droidyard` entry parses equal after (the entry kept, reported); `--uninstall` restores the pre-`init` tree (text byte for byte, JSON equal after parsing); a user's own hook in `.cursor/hooks.json` and a user's `Mcp(droidyard:x)` allow survive both `init` and `--uninstall`; an unparseable `.cursor/hooks.json` and an `opencode.jsonc` refused and untouched; no `daemon.lock` under the state root after `init`; `cli.json`'s allows equal the registry's read-only tools; the skill map test fails on a renamed tool; seen failing with overwrite instead of merge.
- Schema: no change.
- Needs: step 7.
- Not here: an installer, binaries, registries, `llms-install.md` (ADR-0014 §Decision items 1, 2, 4: still out); an IDE-side allowlist (`.cursor/cli.json` binds the CLI; the IDE's is a user setting the skill names).
- Rests on: —
- [ ] Done. Evidence:

### 9. The integration on real clients

- Builds: no repository file. `$B/l5-<stamp>/`.
- Implements: A7–A9 at level 5.
- Run by: the main session, K1–K5, scratch copies of the golden sample, cursor-agent under K3's environment with the run's `DROIDYARD_STATE_DIR` and a daemon already running there.
- Check: Cursor: `init --client cursor` in a copy; `cursor-agent -p` asked to "install and launch the app with adb, then tap Continue": the stream shows the shell call denied with `device_install`/`device_tapElement` named, then the droidyard tool used (a fail-open run reaches only the kit's wrapper, K3); the `sessionStart` context quoted when the agent is asked what it knows of the project; with only `init`'s allowlist (no `Mcp(droidyard:*)`), `device_uiTree` runs and `device_tapElement` is refused by Cursor. OpenCode: `init --client opencode`; `opencode run --standalone` with the same task: the plugin's denial text in the model's next step, or what it showed recorded. `init --uninstall` restores each copy (`diff -r` empty). K2's and K4's files unchanged.
- Schema: no change.
- Needs: step 8.
- Not here: Claude Code and Codex at level 5 (Unverified).
- Rests on: —
- [ ] Done. Evidence:

### 10. The after-measurement

- Builds: no repository file. `$B/runs/after/`.
- Implements: D1 (after); the brief's "measure the gains".
- Run by: the main session, at step 9's commit, under K1–K5.
- Check: twenty valid runs; per arm medians; gains against step 2 beside `sh-cu`'s drift; Cursor's verdicts; `tools/list` bytes against step 2's; `dy-oc`'s `execute` calls; `dys-cu`'s redirects against step 2's offline count.
- Schema: no change.
- Needs: step 9.
- Not here: another model or client.
- Rests on: —
- [ ] Done. Evidence:

### 11. Close

- Builds: by the main session, the close commit `milestone-workflow` §Close names: this plan `Done` with the results and Cursor's verdicts; its index row; one line in `docs/README.md` §Where the work stands; Q1's and Q2's ADRs indexed; the register's m1o rows added or closed (the `within`, `--tools core` and skills rows); `mvp.md` §Definition of done's comparison line gains a link to this plan beside m1m's. No `verify-daemon` block: steps 1 and 9's Checks and the kit at `$B/kit` are the recipe. Then `git merge --no-ff m1o` and the branch deleted.
- Implements: A10.
- Run by: the main session; review: the Opus verifier over A1–A10.
- Check: `go test ./...` green, `internal/doccheck` included (the supersession pointers both ways, no new `Amended:` line, the register's rows well formed).
- Schema: no change.
- Needs: steps 1–10.
- Not here: Phase 2 planning.
- Rests on: —
- [ ] Done. Evidence:

## Not in this milestone

- A stop-time build check (harness note §5): it reverses `mcp.md` §Features beyond tools (hooks): "build after editing should not be automatic", and the brief made it optional. In the register.
- Dropping `outputSchema` from `tools/list`, `$ref` deduplication, `tools/list_changed` disclosure: Q1's alternatives (step 1 (b) records whether Cursor pays for `outputSchema`).
- Tree diffs between reads; an OpenCode Code Mode A/B (`dy-oc`'s `execute` count measures the instructions' fix).
- Binaries, an installer, `llms-install.md`, registries, telemetry (ADR-0014 items 1, 2, 4, 6); `init` for Codex.
- Level-5 runs of Claude Code and Codex hooks.
- G, H: `m1p-functional-gaps.md`. The brief's "Not now" list: `m1n-daily-use-core.md` §Not in this milestone.

## Design questions

### Q1. How does the agent surface spend fewer tokens?

- Blocks: steps 3–6.
- The documents say: `mcp.md` §Descriptions: one or two sentences, guidance in the instructions or a skill (not enforced; 31 of 34 break it); §Schemas on the wire: everything inlined, no `$ref`; §Results: a "compact text rendering", which today is the JSON; §Untrusted data: every tagged string JSON-quoted; §What is exposed: "none [is excluded] just to save tokens", and a minimal surface is an open row in the register; `capability-api.md` §Discovery and versioning: `system.capabilities` returns every description; ADR-0020 §Adapter projections: a completed operation's text is "the plain result".
- Draft decision, a new ADR (next number) "The agent surface's token budget":
  1. **Descriptions** follow the documented rule, enforced by a test: at most two sentences and 400 characters, with a named list of one-sentence safety exceptions (A2); property descriptions at most 120 characters; a repeated object (the selector) described once per tool. The manual moves to the server instructions (≤ 2,000 characters) and the skill.
  2. **Rendering.** A capability may declare a compact text rendering; the MCP text block is that rendering, also through `operation_wait`, and `structuredContent` is the full result. Every untrusted-tagged string is JSON-quoted through one helper, and the untrusted first line stays (ADR-0015 §1). ADR-0020 §Adapter projections' "plain result" becomes "the plain result or its declared rendering".
  3. **Results stay additive** (`capability-api.md` §Discovery and versioning: a breaking change is a new name or a major version). `device.logs`'s `crashes[]` entries gain causes and app frames; `maxLines` defaults to 50. Folding crash lines into their entry and a compact `system.capabilities` happen in the renderings (¶2), not the schema.
  4. **Profile.** `droidyard mcp --tools core` lists only the golden path's tools; `all` stays the default, so the default surface is unchanged and stable for prompt caches.
  - Supersedes in part: ADR-0020 §Adapter projections (the text block), ADR-0019 §Processes item 7 (the hello's `tools`).
  - Consequences: `mcp.md` §Descriptions, §Server instructions (the block grows to at most 2,000 characters), §Results, §What is exposed, and the register's MCP rows; API 0.13.0. A client that shows the model `structuredContent` instead of the text gains nothing from ¶2 (step 1 (b) says which Cursor does).
  - Alternatives considered: drop `outputSchema` from `tools/list` (70 KB on the wire; about zero model tokens in OpenCode; Cursor may copy it into the descriptor files it reads, harness note §1): contradicts ADR-0025 §Decision; wins if step 1 (b) shows Cursor paying for it. `$ref`/`$defs` deduplication: forbidden by §Schemas on the wire, and ¶1 gets the same bytes. A separate short `summary` field keeping the long text in the schema: two texts to keep true; the subsystem documents already hold the manual. `tools/list_changed` progressive disclosure: breaks the client's prompt-cache prefix, uneven support. Removing the build result's fields from the schema instead of the rendering: a breaking change for the coming UI, for tokens only the model pays.

### Q2. May droidyard write assistant integration files and answer assistant hooks?

- Blocks: steps 7–9.
- The documents say: `mvp.md` §Out of scope: "Skills, distribution and registries"; ADR-0014 §Decision: generated per-assistant MCP snippets and one optional skill, with per-assistant plugins rejected as "the exact coupling we are avoiding"; ADR-0011 §Decision: no bundled executables in skills; ADR-0004 §Decision and its Alternatives: harness-level integrations "can be added later as non-essential adapters/plugins", a one-agent plugin "may be revisited as an optional adapter"; `mcp.md` §Features beyond tools: assistant hooks "not adopted", returning "as optional adapters … if a real need appears"; ADR-0019 §3: the complete subcommand set, §4: auto-start; ADR-0015 §6: same-uid code is inside the trust boundary.
- Draft decision, a new ADR (next number) "Optional assistant integration, written by `droidyard init`":
  1. **Optional.** Every capability works without these files (ADR-0004).
  2. **`droidyard init`**, run by the user in a project, never through MCP, writes D5's files: an `AGENTS.md` (or `CLAUDE.md`) block, one skill (ADR-0011's format), and per client the MCP entry, the read-only allowlist and the hook entries, each naming the binary by its absolute path. It merges, marks, keeps existing entries, never overwrites the user's content, and `--uninstall` removes exactly what it wrote. These are files the user asked for, not state (ADR-0021's "never inside the project tree" is about state). The allowlist follows `readOnlyHint`, so it also approves the queries that run project code on a trusted root (`project_*`, `build_tasks`, `sdk_requirements`); the user sees the list when `init` writes it.
  3. **`droidyard hook <client> <event>`**, one handler in `internal/assistant` (a subcommand's helper, outside `adapters/`): before a shell command it denies only commands a droidyard tool replaces exactly for that target and root (D4) and names that tool; it allows everything else and fails open. **It is not a security boundary**: a renamed binary or `sh -c` passes, and enforcement stays in the core (ADR-0015). At session start it returns the root, whether it is trusted and the tool hint, and warms `project.modules` only on a trusted root and only through a daemon already running; it never boots or reads a device.
  4. **Generated from one source**: the redirect map, the allowlist, the instructions and the skill's tool map come from the registry, with tests that fail on drift (`mcp.md` §Features beyond tools, prompts).
  5. Subcommands `init` and `hook` join ADR-0019 §3's set; neither auto-starts the daemon (superseding in part ADR-0019 §Processes 3, 4 and ADR-0024's subcommands row). ADR-0014 §Decision is superseded in part: the per-assistant files are generated configuration, and the OpenCode plugin is a generated one-line shim around `droidyard hook` holding no logic (ADR-0011's "no bundled executables" concerns skills; the shim is configuration). The cost is named: three clients' hook formats live in this adapter, and since it fails open, a client that changes its format silently loses the redirect, never the work. `mcp.md`'s hooks paragraph becomes "optional, through `droidyard hook`"; `mvp.md` §Out of scope keeps distribution (binaries, installer, registries) and drops the one skill.
  - Alternatives considered: plugins or marketplace packages per assistant (ADR-0014's rejection stands); a stop hook that builds after edits (§8: builds are not automatic; the brief's optional item, cut); device facts in the session context (untrusted, and reading a device unasked); prompt-type hooks (they spend model calls); disabling tools per project (little gain under deferred tool loading, harness note §5); shell scripts per client instead of one Go handler (three copies of the map, a `jq` dependency, drift); auto-starting the daemon from a hook (a hook would start a daemon at whatever state root its environment names, and run Gradle there).

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| Cursor's shell keeps the launch environment (else K3's fence fails) | 1, 2, 10 | step 1 (a0) |
| `cursor-agent -p` runs project hooks, with which environment, and shows their deny text to the model | 7, 9 | step 1 (a) |
| OpenCode's plugin throw reaches the model | 7, 9 | step 1 (a), step 9 |
| Cursor's model reads the text block, not `structuredContent` | 5 (its gain in Cursor) | step 1 (b) |
| Cursor's stream reports tokens | 2, 10 (token metrics) | step 1 (b) |
| Cursor shows the server instructions to the model | 3 (its gain in Cursor) | step 9, by asking the agent; level 4 at best |
| Claude Code's and Codex's hook shapes match their documents | 7 | unit tests against the documented shape only (level 2) |
| The read-only allowlist saves interactive approval time | 8 (its value) | not measurable headless; the owner's daily use |
| Five runs per arm separate the before and after medians | 10 | the spread and `sh-cu`'s drift; reported as such |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 2 | revise | B1: D4 redirected commands the tools would refuse (`install` of unregistered APKs, `adb devices`, a bare `assembleDebug`, untrusted roots, unadmitted devices) | D4: `install` and `devices` rows dropped; Gradle only `:<m>:assemble<V>` on a trusted root read from the file; device rows only for an emulator or admitted serial; the near misses tested |
| 2 | revise | B2: D5 had no rule for arrays, and `--uninstall` could not tell `init`'s entries from the user's | D5: append-if-absent in arrays, never touch user elements; uninstall removes only what `init` would write now; step 8 tests a user hook and allow surviving both |
| 2 | revise | B3: step 4's schema changes were breaking under a minor version, against Q1's own reasoning | Q1 ¶3: results additive (`crashes[]` gains fields, keeps its own); folding and the compact `system_capabilities` move into step 5's renderings; A4, A5 |
| 2 | revise | B4: K3 dropped `ANDROID_AVD_HOME`, `JAVA_HOME` and the AVD deny; a0 did not test them | K3 carries K7's full environment and the AVD deny; a0 checks `-list-avds`, the SDK adb and `~/.android/avd` refused |
| 2 | revise | B5: versions recorded but not pinned, and no way to a fresh before-measurement; the real state root does not exist | K4: client copies under `$B/clients/`, a fresh before built into `$B/kit-before/`; "absent, or `daemon.lock` unchanged" |
| 2 | revise | B6: `adapters/assistant` broke the adapter boundary | `internal/assistant`, wired from `cmd/droidyard`; Q2 ¶3 lists it in the package layout (code since 1q) |
| 2 | — | N1 arm count; N2 `dy-oc` drift; N3 the matcher before it exists; N4 core nextSteps; N5 "destructive"; N6 hook environment; N7 `daemon.lock` after `init`; N8 reflection; N9 `--client all` duplicates; N10 the path rule; N11 `go:embed`; N12 §Server instructions; N13 hook `matcher`; N14 size | all taken but N14: header; D1 label; step 2 classifies, step 7 replays; step 6 test; A9; step 1 branch; step 8; step 5; D5; D5; D6; Q1 Consequences; D5. N14: the plan is about 42 KB; its Review table and K/D items carry what two rounds found, and cutting them would hide it |
| 1 | revise | B1: the rendering printed tagged strings (class, id, tag) unquoted, and removed the one-line guard | Q1 ¶2 and step 5: one quoting helper for every tagged string; `TestEveryTaggedStringIsQuoted` walks every untrusted field of each rendered type |
| 1 | revise | B2: D4 denied commands no tool replaces (keyevent, swipe, `am start -a`, other APKs, `logcat -c`, events buffer) | D4 narrowed to exact equivalents, the near misses listed as allowed and tested |
| 1 | revise | B3: K7 does not bind Cursor; no fence or probe for the Cursor shell arms; `PATH` unknown | K3 translates K7 to Cursor; step 1 (a0) proves it, decisive |
| 1 | revise | B4: `init` and hooks could escape isolation (command name, key collision, hook environment, auto-start at the real state root) | D5: absolute binary path, existing entries kept; Q2 ¶3, ¶5: no auto-start for `init` or `hook`; step 1 (a) records the hook environment; K4 checks the real `daemon.lock` |
| 1 | revise | B5: client and model versions not pinned across days | K4 records versions per run, a change voids; `sh-cu` rerun after as the drift control (D1) |
| 1 | — | N1 redirects as failures; N2 evidence of need; N3 outputSchema in Cursor; N4 branches on step 1 (b); N5 A2 vs step 3; N6 JSON, JSONC, codemode; N7 allowlist scope; N8 `operation_wait`, `returnTree`, bounds; N9 folding vs selectors; N10 `workspace_open`, profile bytes; N11 Q2's coupling, package; N12 shared extractor, tags; N13 small items | all taken: D1 reports redirects apart; step 2's offline count and owner notice; step 1 (b); step 1's decisive branches; A2's named exceptions; D5; Q2 ¶2, Unverified, step 8's Not here; step 5 (bounds always shown, no param); `TestRenderedLineResolves`; D3 with 22, bytes recorded; Q2 ¶5, `adapters/assistant`; step 4; step 1's four commands, Codex out of `init`, `CLAUDE.md`, A3's bound, D2, `metrics.py` |
Two rounds ran (round 1: B1–B5; round 2: B1–B6), every blocking finding answered in the plan text. 2026-10-08, the main session's diff-only revision for 1p and 1q (`milestone-plan` §Writing one, item 7): baselines retaken at `cc0c27b`; API move 0.12.0 → 0.13.0; ADR amendments replaced by superseding ADRs; the `within` register row taken into step 3; step 4's document edit dropped; step 11 to the new close, no `verify-daemon` block; Q1 and Q2 taken as drafted (Delegated), so `Approved`.

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.

- 2026-10-08, step 1 (a0), K3. Cursor's shell does not keep the launch environment: it rebuilds `PATH` from the user's login profile with the launch `PATH` appended, and `ANDROID_HOME` comes back from the profile (`ANDROID_AVD_HOME`, `JAVA_HOME` and `DROIDYARD_STATE_DIR` survive). `adb` still resolves to the kit's wrapper (no profile entry before it has one), and m1m K7's fallback applies: the deny rules fence the SDK. They grew to `Shell(*platform-tools*)`, `Shell(*:*platform-tools*)`, `Shell(./adb)` beside m1m's list. Proven: `adb kill-server` refused by the wrapper, `./adb`, `$ANDROID_HOME/platform-tools/adb`, `ls ~/.android/avd`, `kill`, `killall` denied, the 5037 pid unchanged.
- 2026-10-08, step 1 (c), K3 (Delegated). Cursor loads the owner's global `~/.cursor/skills/android-cli`, and the `sh-cu` pilot drove the device with `/opt/homebrew/bin/android`, an adb client outside the fence. The shell arms deny `Shell(android)`: the baseline stays `mvp.md` §Metrics' plain shell, as in m1m. The skill itself cannot be hidden without editing the global configuration (K2), so the agent may try the command and be denied; such a denial counts as a failed call, as m1m D3 says.
- 2026-10-08, step 1 (c), kit. `cursor-agent -p` emits no result while a background shell job it started lives, so the `sh-cu` pilot finished the task in 214 s and then hung on its `emulator -avd` until the 30-minute cap, without its token counts. The kit's `emulator` is now a wrapper that starts a launch detached (own session, output to a log) and returns, as `emulator_start` does; `-list-avds` and the rest pass through.
- 2026-10-08, step 1 (a), D5 and step 7. Cursor `-p` runs project hooks: a `beforeShellExecution` deny reaches the model as "Command execution was blocked by a hook: <user_message>", and `agent_message` does not appear; `sessionStart`'s `additional_context` reaches the model. The hook's environment has neither `DROIDYARD_STATE_DIR` nor `ANDROID_AVD_HOME` (it is built like the shell's), so the kit's copies write the state directory inline in the hook command (the plan's step 1 branch). OpenCode 2.0.18's plugins are `export default {id, setup(context)}` registering `context.tool.hook("execute.before", …)`, the shell tool is `shell` with `input.input.command`, a throw reaches the model as the call's error, and the plugin inherits the launch environment: D5's generated `droidyard.ts` takes that shape.
- 2026-10-08, step 1 (b). Cursor shows the model the text block only; at session start its `getMcpTools` call returns every tool's description and `inputSchema` (80,301 B for 34 tools) without `outputSchema`, and the server instructions as the namespace description; the stream's `result` carries `usage` (input, output, cache-read tokens). So step 5's gain applies in Cursor, and Q1's alternative of dropping `outputSchema` stays rejected (ADR-0036).
- 2026-10-08, order. Steps 3–6 are implemented while step 2's runs go on (the runs use `$B/kit/dy`, built from the branch base, so the code under change does not reach them); step 7 still waits for step 2's recorded commands. Cost, named: the implementer's builds and tests share the host with the runs, so step 2's wall and tool times may read high against step 10's; calls, failures and tokens are unaffected.
