# Plan: Milestone 2k — Cursor at Claude Code's level, and the agent's Android commands observed

- Status: Approved
- Date: 2026-10-12
- Approved: 2026-10-12, by the main session under the owner's standing instruction to apply the recommended option (Delegated); the owner asked for the milestone ("lo que veas más indicado. Procede … Y procede con el nuevo hito 2k"). Q1 (ADR-T) and Q2 (ADR-O, API `0.22.0`) applied as drafted, written at step 1, Delegated.
- Milestone: "2k — Cursor at Claude Code's level, and the agent's Android commands observed". The owner, 2026-10-12, in Spanish: they will work on a real project with Cursor (the IDE) instead of Claude Code, so Cursor must be supported as well as Claude Code is. From `docs/research/cli-vs-mcp-hooks-2026-10.md`: intercepting `./gradlew` and `adb` risks blocking commands droidyard does not support, so a hook that monitors what the agent runs instead ("lo que veas más indicado. Procede"). The main session's Delegated choices: observe only Android commands; record each in Activity; a non-blocking hint naming the droidyard equivalent, on by default; never block, never past the 2 s bound, fail open, nothing recorded without a daemon.
- Phase: `docs/roadmap/mvp.md` §Phase 2, a milestone after the Definition of done, by discussion. No phase deliverable is added.
- Contract: Q1's ADR-T and Q2's ADR-O. They get the next free numbers when written, and are named T and O here because `TestADRMentionsExist` refuses a number not on disk.
  - ADR-T supersedes in part ADR-0061 §Decision and §Consequences, ADR-0019 §Processes (items 3, 4) and ADR-0024 §Decision.
  - ADR-O supersedes in part ADR-0061 §Decision (§5).
  - Also: ADR-0004, ADR-0015 §1, §5, §6, ADR-0037, ADR-0043 §2, ADR-0048, ADR-0054; `capability-api.md` §Naming and namespaces; `mcp.md` §What is exposed, §Features beyond tools.

**The owner should see:**
- Cursor's `rm` is not redirected today. Its event carries `cwd: ""`, so only absolute operands route, and agents type relative ones.
- 2k hands the agent `droidyard trash <same operands>`, which runs in the agent's own shell, so relative paths and globs resolve where the agent is. Claude Code gets the same command, a prefix an allow rule can approve.
- The observation never blocks a command, and never delays it beyond 2 s.
- It records the program and at most three plain words per command (`gradle assembleDebug`, `adb install`), and for `adb shell` only the shell's command word.
  - Flags, their values, variables and the command line are never recorded.
  - A plain word can still carry text the agent typed.
- Recording needs a new human-only capability, `agent.recordCommand`, in a new `agent` domain. That is a contract change: API `0.22.0`.
- Nothing about your IDE can be proven from here. Step 10 is a short checklist for you to run in Cursor on your project.

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| `droidyard trash`, the hook's replacement for both clients (gap 1) | yes | Q1, steps 2–4 |
| Cursor's deny with `agent_message` too, and which field reaches the model (gap 2) | yes | Q1 §6, steps 1, 3, 4 |
| Cursor's `Delete` deny reaching the model (gap 3) | part | `-p` refuses the tool first ([obs] m2j step 6). Step 10 |
| In the IDE: `cli.json` permissions, the skill in `.agents/skills`, the MCP enable (gap 4) | part | step 10; `init` already prints the enable note (`assistant.go:131`) |
| Observing Android commands: the hint and the Activity record, both clients | yes | Q2, steps 1, 5–9 |
| Committing the research note, unmodified | yes | step 1 |
| Everything listed in §Not in this milestone | no | there |

## Starting point

At `e3bb62f` (2j merged), API `0.21.0`, with no `agent` domain.

- `internal/assistant/hook.go`:
  - `DecodeHookEvent` reads Claude Code `PreToolUse` Bash, Cursor `beforeShellExecution`, and Cursor `preToolUse` `Delete`.
  - `RouteRm` returns root-relative paths. A relative operand needs an absolute `cwd`, so Cursor's relative operands pass.
  - The parser keeps the quoting of every character (`wordRune.quoted`).
  - `DenyOutput` sends Cursor's text in `user_message` only.
  - `Redirect.Message` renders `[DROIDYARD_STATE_DIR=…] <abs> call --no-autostart --workspace <root> file.delete --params …`.
- `cmd/droidyard/hook.go`: answers within 2 s, dials without auto-start, calls `openProject`, then denies. Every failure passes, and it never exits 2.
- `internal/assistant/hookentry.go` writes:
  - Claude Code: `PreToolUse` with matcher `Bash`;
  - Cursor: `beforeShellExecution`, and `preToolUse` with matcher `Delete`.
- `cmd/droidyard/client.go` `connectOrStart`, and `root.go` `resolveRoot`.
- Activity hides every `cli` operation that `keyOf` maps to null, so read-only and streaming ones are hidden:
  - `blocks.ts`: `hidden`, `admitted` (Agents admits only `mcp`), and `actorOf` (`cli` is "You");
  - `projects/sessions.ts`: `keyOf`.

  `ops/restore.ts` reads a result through `operation.wait` in its wire shape (register row 276).
- `cmd/droidyard/trash_test.go` already holds `TestTrashSurvivesADaemonRestart`.
- Client facts on record:
  - Cursor: m1o §Changes (lines 272, 273, 275, 280); m2j step 6; `daily-use-harness-2026-10.md` §1, where `postToolUse` takes `additional_context` [doc, not run].
  - Claude Code: m2j steps 6 (a) and 11. `PostToolUse`'s `additionalContext` is the reference's, not run.
- Baseline, 2026-10-12 at `e3bb62f`:
  - `go vet ./...` is clean.
  - `go test ./...`: ok in all 31 packages with tests (5 have none).
  - Once this file exists, `TestPlansIndexMatchesFiles` fails until its index row is added.

## Acceptance

- [ ] A1. `droidyard trash` runs in the caller's directory and moves the operands to the trash of the daemon on its state root. It never starts a daemon. It refuses by ADR-0061 §2's target rules, moving nothing, and names `command rm …` instead. Source: Q1 §1–§3. Proven by: steps 2, 4.
- [ ] A2. Cursor denies a simple relative `rm` with the exact `trash` command. That command, run in the agent's shell, trashes exactly the named files (`cursor-agent`, level 5). Source: the owner; Q1 §4, §5. Proven by: steps 3, 4.
- [ ] A3. Claude Code denies a routed `rm` with the `trash` command, and one real run trashes the file. Source: Q1 §4. Proven by: steps 3, 9.
- [ ] A4. Cursor's deny carries both `user_message` and `agent_message`, and which one reaches the model in `cursor-agent` is recorded. Source: Q1 §6. Proven by: steps 1, 3, 4.
- [ ] A5. After an Android command, in either client, the agent gets a hint naming the droidyard tool within 2 s, and nothing is blocked. With a daemon, Activity shows a row attributed to the client, holding only the program and plain words. Source: Q2 §1–§6. Proven by: steps 5, 6, 7, 8, 9.
- [ ] A6. With no daemon, nothing is recorded and no daemon starts. A failure is the client's pass-through, plus at most the hint. Source: Q2 §3. Proven by: step 7.
- [ ] A7. The schema diff is `0.22.0` plus the `agent.recordCommand` block. It is not an MCP tool, and the core refuses it from MCP. Source: Q2 §5. Proven by: step 6.
- [ ] A8. `init --client claude|cursor` writes the after-command entries idempotently, and `--uninstall` removes them. Source: Q2 §7. Proven by: step 7.
- [ ] A9. Every answer from the owner's IDE checklist is recorded, and every item not confirmed is a register row. Source: the owner. Proven by: step 10.

## Run rules (steps 1, 4, 9)

- **K1.** Each run works in a `git archive` copy of `samples/golden`.
  - Its state and config live under a fresh `/tmp/dyk.*`. Never `~/Library/Application Support/droidyard`; never `~/Documents/DopamineControl`.
  - The binary is built at the step's commit, after `pnpm -C web build`.
  - `verify-daemon` §Launch, §Doctor, §Cleanup and §Emulator apply. The scratch daemon gets a guard script as `ADB` and `ANDROID_ADB_SERVER_PORT=5199`, in its own environment only.
- **K2.** Nothing reaches the owner's daemon or adb:
  - Each client is launched with the run's `DROIDYARD_STATE_DIR` and `DROIDYARD_CONFIG_DIR`.
  - Before and after each run, these keep their pid and start time: every daemon this run did not start (pid 15130 among them), and the adb server on 5037. The default state root's `daemon.lock` and `ws/` listing are also unchanged.
  - Nothing is killed by name.
  - droidyard's MCP is denied in both clients (Cursor `Mcp(droidyard:*)`; Claude Code `--disallowedTools mcp__droidyard`). The checks need the hint delivered, not acted on.
  - `adb` is a fake script that agents run by its absolute path. It prints a device list, and exits 1 for `install missing.apk`.
  - `ANDROID_SERIAL=droidyard-m2k-none`. No device, no emulator, no Gradle.
- **K3.** Cursor runs under m1o's K2 and K3, with `m2k` for `m1o`:
  - `cursor-agent -p --trust --output-format stream-json --model cursor-grok-4.6-high` and a scratch `CURSOR_DATA_DIR`, with `github` and `Figma` disabled there. Never `-f`, `--yolo`, `--approve-mcps`, `status` or `login`.
  - `~/.cursor/mcp.json` and `hooks.json` stay byte-identical. A copy of `~/.cursor/cli-config.json` is kept before each run, and its `permissions` and `approvalMode` are compared after.
  - The copy's `.cursor/cli.json`:
    - allows `Shell(cd *)`, `Shell(ls *)`, `Shell(rm *)`, `Shell(<abs droidyard> trash *)` and `Shell(<abs fake adb> *)`;
    - denies `Shell(kill *)`, `Shell(pkill *)`, `Shell(killall *)`, `Shell(adb)`, `Shell(adb *)`, `Shell(*platform-tools*)`, `Shell(*:*platform-tools*)`, `Shell(./adb)`, `Shell(android)`, `Shell(android *)` and `Mcp(droidyard:*)` (m1o §Changes, lines 272 and 273).
  - m2k's allow list replaces m1o K3's `Shell(*)`, and the kit `PATH` is not used.
  - In each copy, after `init` and before its run (steps 4 and 9), `cursor-agent mcp list` in the scratch data dir must show neither droidyard nor `github`/`Figma` enabled. Until it does, Cursor is not driven in that copy.
- **K4.** Claude Code:
  - One `claude -p` run in the milestone, at step 9; a second only if the first is void.
  - `--allowedTools` holds `Bash(cd:*)`, `Bash(rm:*)`, the `trash` prefix and the fake `adb`'s prefix. `--disallowedTools` holds `Bash(adb:*)` and `mcp__droidyard`.
  - `~/.claude/settings*.json` stay byte-identical.
  - Headless Chrome gets its own `--user-data-dir` and is driven over CDP, never with a capture of the owner's screen.

## Steps

### 1. The research note, the clients' facts, then ADR-T and ADR-O

- Builds: nothing in code. The main session does four things:
  - (a) Commits `docs/research/cli-vs-mcp-hooks-2026-10.md` unmodified.
  - (b) Re-reads Claude Code's hooks reference. It records `PostToolUse`'s Bash input and its `additionalContext` output, and whether `PostToolUse` or a failure event fires on a non-zero exit.
  - (c) Probes `cursor-agent` under K3 in a throwaway `$B/probe`, with every event below hooked to a script that records stdin and answers with markers:
    - c1. Re-check m1o: a `beforeShellExecution` deny with different `user_message` and `agent_message` texts. Which does the model quote?
    - c2. `postToolUse` (matcher `Shell`) after `ls`: does it fire, what does its stdin carry (tool name, the command, `cwd`, the exit status), and does a marker in `additional_context` reach the model?
    - c3. The same after `ls /nonexistent`: `postToolUse` or `postToolUseFailure`, and which stdin?
    - c4. K3's fence: `adb version` and `$ANDROID_HOME/platform-tools/adb version` are denied, while the fake `adb` run by its absolute path runs.
    - c5. The version.
    The recorded stdin becomes step 5's fixtures.
  - (d) Writes ADR-T and ADR-O from Q1 and Q2, amended by (b) and (c). Both are Accepted and Delegated, with back-pointers and index rows.
- Implements: risk first. Q2's Cursor half rests on c2 and c3.
- Check:
  - The committed note's `shasum` equals the one taken before the commit.
  - Each question in (b) and (c) has an answer, or "not run" and why.
  - If c2 shows no fire, or no context reaching the model, ADR-O drops Cursor's hint and records from whichever after-event fires, or not at all if none does. A register row says so, and steps 5 and 7 follow the ADR.
  - If c4 fails, steps 4 and 9 wait until the fence holds.
  - `go test ./internal/doccheck` passes.
- Schema: no change.
- Needs: Q1 and Q2 as reviewed.
- Not here: a Claude Code run; K4 makes step 9 the only one.
- Rests on: Cursor's after-event and its context reaching the model, at level 5 (a real `cursor-agent`).
- [x] Done. Evidence: 2026-10-12. (a) `c2bbf33`; the note's SHA-256 `212488ed…0a36` before and in the commit. (b) code.claude.com/docs/en/hooks: `PostToolUse` fires after a tool "completes successfully" with `tool_input`, `tool_response`; a Bash command that exits non-zero fires `PostToolUseFailure` (`error`: `Exit code N` then the output; the reference's `npm test` example); both take `hookSpecificOutput.additionalContext`, shown to the model beside the result (10,000-character cap); a permission denial fires neither; exit 2 on `PostToolUse` blocks nothing. Not run (step 9). (c) `cursor-agent` 2026.10.01-e373342 under K3 in `$B/probe` (`B=~/Library/Caches/droidyard-dev/m2k`), `github` and `Figma` disabled in `$B/cursor-data`, every event hooked to `$B/hook.py`: c1 `user_message` reaches the model, prefixed "Command execution was blocked by a hook:" and followed by "Agent note: Do not suggest workarounds to the blocked tool."; `agent_message` does not. c2 `postToolUse` (`Shell`) fires after `ls` with `tool_input.command`, `tool_input.cwd: ""`, `tool_output` `{"output","exitCode":0}`; its `additional_context` marker reached the model. c3 `ls /nonexistent-m2k` and the fake `adb install missing.apk` (exit 1) fire `postToolUseFailure` (`failure_type: "error"`, `error_message`, `tool_input.command`), whose marker reached the model; the hook's deny fires `postToolUseFailure` with `failure_type: "permission_denied"`; `afterShellExecution` fires too (command, output). Each event carries `user_email`. c4 `adb version` and `$ANDROID_HOME/platform-tools/adb version` "blocked by permissions configuration", the fake adb by its path ran; the two refused commands still fired `postToolUse` with `exitCode: 0` and empty output. c5 as above. Fixtures, `user_email` replaced: `$B/fixtures/`. K2/K3: `~/.cursor/mcp.json` and `~/.claude/settings.json` hashes, `~/.cursor/hooks.json` absent, pid 15130, adb 5037 pid 19285 and the default state root unchanged (`$B/k2.before`, `$B/k2.after-probe`); `cli-config.json` `permissions` and `approvalMode` equal to the copy. (d) ADR-0062 (T) and ADR-0063 (O), with back-pointers in ADR-0061, ADR-0019, ADR-0024 and index rows; `go test ./internal/doccheck` ok.

### 2. `droidyard trash`

- Builds:
  - `cmd/droidyard/trash.go`, its case in `main.go`, and the usage text, per ADR-T §1–§3: `droidyard trash [--state-dir <dir>] [--root <abs>] [-r|-R|-f|-rf…] [--] <path>…`. Flags are parsed as `rm` parses them, combined ones included (not Go's `flag` package), and they end at `--` or the first operand.
  - Operands resolve against the process's cwd.
  - The root is `--root`, else `resolveRoot("", cwd)`.
  - The state root is `--state-dir`, set as `DROIDYARD_STATE_DIR` in its own process before `platform.Current()`, else the environment's.
  - It dials with `connectOrStart(…, false, nil)`.
  - It checks every operand before moving any. It uses `internal/assistant`'s `placeTarget` rules (exported), plus `-r` for a directory and `-f` for a missing operand.
  - Then it calls `openProject` and `file.delete` with root-relative paths.
  - Output and exit codes:
    - stdout: one line per item, `trashed <operand>` or `<operand>: <code>`, and exit 0 only if every item is trashed;
    - a refusal, or no daemon: nothing moves, the reasons go to stderr with `To delete without droidyard's trash: command rm <flags> -- <operands>`, and it exits 1;
    - a usage error exits 2.
- Implements: ADR-T §1–§3.
- Check: new `cmd/droidyard/trash_cmd_test.go`, which runs the re-executed binary against `startInProcess`:
  - From a subdirectory, `trash -- A.kt 'b c.kt'` trashes those two and not the same names at the root.
  - `trash -rf dir` is accepted.
  - A glob run through `/bin/sh -c` in that cwd trashes what `sh` expanded.
  - Each of these refusals moves nothing, exits 1 and prints the `command rm` line: `build/x`, `../../outside`, the root, `.`, a missing file without `-f`, a directory without `-r`. A missing file with `-f` is skipped.
  - No daemon: exit 1, and no `daemon.lock` appears.
  - `--state-dir` reaches that daemon with `DROIDYARD_STATE_DIR` unset and no `droidyard` on `PATH`.
  - Seen failing: with auto-start dialling; with operands resolved against the root; with the build-output check removed.
- Schema: no change.
- Needs: step 1.
- Not here: the hook's message (step 3). An `init` allow rule for `trash`: ADR-0037 §3 allowlists MCP tools only.
- Rests on: the process cwd is the agent shell's directory. Level 3 here, level 5 at step 4.
- [x] Done. Evidence: level 3, 2026-10-12. `cmd/droidyard/trash.go`; `PlaceTarget` and `ErrTargetMissing` exported from `internal/assistant/hook.go` with a reason per refusal (the hook's tests unchanged and green). `trash_cmd_test.go`: `TestTrashResolvesOperandsInTheShellsDirectory`, `TestTrashReadsRmFlagsAndAGlobExpandedByTheShell`, `TestTrashRefusesAllOrNothing` (six refusals, each beside a good operand that stays), `TestTrashWithoutADaemonMovesNothingAndStartsNone`, `TestTrashStateDirFlagReachesTheDaemon`, `TestTrashReadsFlagsAsRmDoes`, run with `PATH=/nonexistent`. Seen failing: auto-start dialling (no-daemon test); operands against the root (the two resolution tests); the build-output check removed (`TestTrashRefusesAllOrNothing/build_output`). Not tested: the per-item failure lines of a partial `file.delete`. `trash` dials twice (open, then delete): a `cli.Call` reads its connection through a goroutine that outlives it. Review: the Opus verifier with step 3.

### 3. The hook hands over `trash`; Cursor's relative operands; `agent_message`

- Builds:
  - `internal/assistant/hook.go`:
    - `RouteRm` returns the typed operand words and the flags.
    - It keeps today's checks wherever it can place a target: an absolute `cwd` (Claude Code), or an absolute operand. With `cwd: ""`, a relative operand routes on lexical checks only (Q1 §5). The `Delete` branch routes its absolute `file_path`.
    - `Redirect.Message` renders `<abs> trash [--state-dir '<dir>'] --root '<root>' [-r] [-f] -- <operands>`. Each operand is re-rendered character by character: a `*` or `?` that was unquoted in the typed word stays bare, and every other character is single-quoted. A single quote or a control character anywhere still passes.
    - `DenyOutput(Cursor, …)` puts the text in both `user_message` and `agent_message`.
  - Documents:
    - `mcp.md` §Features beyond tools' row cites ADR-T.
    - Register row 157 loses only the allow-rule problem; its custom-state-root clause stays. Row 164 narrows to the IDE.
    - `liveBudget` changes by the measured delta.
- Implements: ADR-T §4–§6.
- Check:
  - `TestRmFormsTable` gains Cursor rows with `cwd: ""`:
    - route: `rm a.kt`, `rm -rf src/old`, `rm src/*.kt`;
    - pass: `rm ../x`, `rm build/x`, `rm -rf .gradle`, `rm .`.
  - `hook_test.go`, for Claude Code's event and Cursor's `cwd: ""` event: the exact denied string, run through `/bin/sh -c` in the subdirectory where the `rm` was typed, with no `droidyard` on `PATH` and no state variable, trashes exactly the named files at the test daemon. The cases:
    - `rm Obsolete.kt`;
    - `rm 'a*b.kt'` with `aXb.kt` present, which trashes only the literal file;
    - `rm "my dir"/*.kt`, which behaves as `sh` would.
    No second `daemon.lock` appears.
  - Cursor's deny has both fields.
  - Seen failing:
    - a quoted glob made bare (`aXb.kt` is trashed);
    - an unquoted glob quoted (no match);
    - Cursor's relative rows routed through `cwd`;
    - `--state-dir` dropped.
- Schema: no change.
- Needs: step 2.
- Not here: the after-command entries (step 7).
- Rests on: the agent runs the replacement in the shell where it typed the `rm`, and Cursor's shell keeps a `cd` ([obs] m2j step 6). Level 5 at step 4.
- [x] Done. Evidence: level 3, 2026-10-12. `RouteRm` returns a `Route` (typed words, flags); Cursor's relative operands route on `lexicalOperand`; `Redirect.Message` renders `<abs> trash [--state-dir] --root [-r] [-f] [--] <operands>` with `word.shell()`; Cursor's deny has both fields. Tests: `TestRmFormsTable` (Cursor rows), `TestRedirectMessage`, `TestRedirectRequotesOperandsAsTyped`, `TestHookAnswers`, `TestHookDeniesAndTheReplacementReachesTheSameDaemon` (both clients × four cases, the exact denied string through `/bin/sh -c` in the typed directory, `PATH=/nonexistent`, no state variable, exact gone/kept sets, no second lock), `TestHookHandsCursorsDeleteToolTheAbsolutePath`, `TestHookReplacementReadsAGlobsDashNameAsRmWould`. Seen failing (implementer): quoted glob made bare, unquoted glob quoted, Cursor rows through `cwd`, `--state-dir` dropped; (verifier, rerun) quoted glob made bare, `trash` auto-start; (main session) `--` always added. Opus verifier over steps 2–3: proceed; N1 [safety] fixed (ADR-0064), nits `=` in `quoteArg` and the `Route.Paths` comment fixed; a `trash` connection deadline and a transport error without the `command rm` line left (the call may have reached the daemon). `mcp.md` §Features beyond tools cites ADR-0062. Register rows 157 and 164: unchanged here (157's clause stays true; 164 is updated with step 4's evidence).

### 4. Level 5: Cursor's relative `rm` lands in the trash

- Builds: nothing. The main session runs it under K1–K3.
  - `droidyard init --client cursor --root <copy>` under the run's environment, so the entry and the replacement name the scratch state root. The copy's `cli.json` then gets K3's rules.
  - One `cursor-agent -p` run, prompted to `cd app/src/main/kotlin/dev/droidyard/golden` in one command and to `rm Obsolete.kt` by its relative name in the next.
- Implements: A1, A2 and A4 at level 5.
- Check:
  - The stream shows the `rm` denied with the `trash` command, and the agent running that command.
  - Which field the model quotes is recorded.
  - `daemon.log` has `client=cli file.delete … completed`.
  - The file is in `<state>/ws/<id>/trash` and gone from the copy.
  - K2 and K3 hold, no other `daemon.lock` appears, and there are zero orphans.
- Schema: no change.
- Needs: step 3; step 1's c4.
- Not here: the IDE (step 10); the `Delete` tool, which `-p` refuses.
- Rests on: —
- [x] Done. Evidence: level 5, 2026-10-12, `$B/l5-s4-135425` (binary at `5e7a3bd`, scratch daemon with the `ADB` guard and port 5199, state `/tmp/dyk.eFWHyd`). `init --client cursor` under the run's environment wrote the entries with `DROIDYARD_STATE_DIR` inline; `cli.json` replaced by K3's lists; `cursor-agent mcp list` in the copy: `github`, `Figma` disabled, droidyard "not loaded (needs approval)". One `cursor-agent -p` run (2026.10.01, grok 4.6): `cd app/src/main/kotlin/dev/droidyard/golden`, then `rm Obsolete.kt` → rejected with "Command execution was blocked by a hook: droidyard keeps deletes … `<abs> trash --state-dir /private/tmp/dyk.eFWHyd/state --root <copy> 'Obsolete.kt'`" plus Cursor's "Do not suggest workarounds" note; the agent ran that command next, unprompted ("trashed Obsolete.kt", exit 0), then `ls` confirmed. `daemon.log`: `client=cli capability=file.delete … state=completed`. The package's `Obsolete.kt` is in `ws/golden-5e511bfba699/trash`; the root's `Obsolete.kt` is kept. The model quoted the `user_message` text (A4; `agent_message` not shown by Cursor, as step 1 c1). K2/K3: `k2.before` = `k2.after`, `cli-config.json` permissions and `approvalMode` equal to the copy, daemon gone, zero orphans.

### 5. The Android command reader and the hint

- Builds: `internal/assistant/observe.go`, pure:
  - `ObserveCommands(command) []Observed{Program, Words, Suggested}`, per Q2 §1 and §4:
    - split at unquoted `;`, `&&`, `||`, `|`, `&` and newline;
    - skip `VAR=…` assignments and `command`, `exec`, `env`, `time`, `nohup`;
    - match the program's base name against `gradlew`, `gradle`, `adb`, `emulator`, `sdkmanager`, `avdmanager`, with at most four matches per line;
    - per program, skip flags and the values of flags that take one (tables in code);
    - keep at most three positional words matching `^[A-Za-z0-9][A-Za-z0-9:._-]{0,63}$`, and for `adb shell` only the shell's command word.
  - The hint table maps program and words to an MCP tool, or to none:
    - `build_assemble`, `build_tasks`, `build_run`;
    - `device_install`, `device_logs`, `device_list`, `device_launch`, `device_uninstall`, `device_uiTree`, `device_screenshot`, `device_tapElement`, `device_inputText`;
    - `emulator_list`, `emulator_start`, `emulator_wipe`;
    - `sdk_status`.
    `Hint` writes one line per distinct tool, at most 300 bytes.
  - `DecodeHookEvent` learns the after-events step 1 found.
  - `AfterOutput`: Claude Code's `hookSpecificOutput.additionalContext`, Cursor's `additional_context`, or the pass-through when there is no hint.
- Implements: ADR-O §1, §2, §4.
- Check:
  - `TestObserveTable`:
    - `./gradlew assembleDebug` → `[assembleDebug]`, `build_assemble`;
    - `cd app && ../gradlew -p app :app:installDebug 2>&1 | tail -5` → `[:app:installDebug]`;
    - `adb -s emulator-5554 install -r out/app.apk` → `[install]`, `device_install`;
    - `$ANDROID_HOME/platform-tools/adb devices` → `device_list`;
    - `adb exec-out screencap -p` → `device_screenshot`;
    - `adb shell echo hunter2` → `[shell echo]`;
    - `emulator -avd Pixel -wipe-data` → `emulator_wipe`;
    - `ANDROID_SERIAL=x adb logcat -d` → `device_logs`;
    - `./gradlew assembleDebug && adb install x.apk` → two records.
    - Not observed: `echo adb`, `grep gradle build.gradle`, `cat gradlew`, `droidyard call …`, `sh -c 'adb devices'`.
    - `TOKEN=s3cret ./gradlew publish -Ppassword=hunter2 -Dx=y` → `[publish]`, with no secret in the record or the hint.
  - Decoding runs on step 1's fixtures. Claude Code's fixture follows the reference's shape and is labelled so.
  - `cmd/droidyard` `TestHintToolsAreExposed`: every tool in the table is listed by `tools/list` under `all`.
  - Seen failing: with a misspelt tool; with the flag-value table removed (`app` gets recorded).
- Schema: no change.
- Needs: step 1.
- Not here: the daemon and the hook's wiring (step 7).
- Rests on: —
- [x] Done. Evidence: level 3, 2026-10-12. `internal/assistant/observe.go` (`ObserveCommands`, `Hint`, `HintTools`, `DecodeAfterEvent`, `AfterOutput`; `DecodeHookEvent` untouched); fixtures `cursor-4…7-*.json` from step 1 (`user_email` removed, paths anonymised) and `claude-postToolUse*.json` in the reference's shape, labelled so. `TestObserveTable` (every row of the check, plus `$(which adb)`, `$ADB`, a comment, and `adb exec-out run-as … cat files/token` → `[exec-out run-as]`; no secret in record or hint), `TestObserveBounds`, `TestHintShape`, `TestDecodeAfterEvents` (the `permission_denied` fixture does not decode), `TestAfterOutput`; `cmd/droidyard` `TestHintToolsAreExposed`. Seen failing: a misspelt tool (`TestHintToolsAreExposed`); `-p` removed from the flag-value table (`app` recorded). Main session's review: the word pattern takes one leading `:` ([ADR-0065](../adr/0065-observed-word-leading-colon.md), Delegated: `:app:installDebug`), and `adb exec-out` keeps one word as `adb shell` does.

### 6. `agent.recordCommand`

- Builds:
  - `internal/domain/agent/agent.go`, registered in `main.go` `registry`. The descriptor follows ADR-O §5: kind command, scope workspace, stateless, audience human; destructive explicitly false, with its reason (`registry.go:183-186`); not idempotent; not open-world.
  - Params `{client: claude|cursor, commands: [{program, words, suggested?}] (1–4)}`, with step 5's patterns. The result is the record. Invalid params get `REQUEST_INVALID_PARAMS`.
  - `Version = "0.22.0"`, then `go test ./internal/schema -update`.
  - `mcp.md`'s not-exposed table; `mvp.md` §MVP capability surface (an `agent` row) and §In scope's domain list.
- Implements: ADR-O §5.
- Check:
  - `TestRecordCommandDescriptor` asserts the descriptor.
  - A handler table: a valid record comes back as its result; these get `REQUEST_INVALID_PARAMS`: a bad program, a word failing the pattern, five commands, four words.
  - `cmd/droidyard`:
    - `tools/list` lacks `agent_recordCommand` under `all` and under `core`;
    - `p.ops.Start` as `ClientMCP` gets `REQUEST_NOT_ALLOWED_HERE` and no id;
    - `callIn` after `workspace.open` completes, and `operation.watch` sees the call with `client: cli`.
  - The doccheck tests fail before the document rows and pass after.
  - `go test ./...` and `pnpm -C web run verify` are green.
  - Seen failing with the audience set to `all`.
- Schema: `"version"` `0.21.0` → `0.22.0`, plus the `agent.recordCommand` block. Nothing else.
- Needs: step 5.
- Not here: a new error code or event topic. `operation.state` already carries the call.
- Rests on: the core refuses an audience-human capability to `ClientMCP` (`operation.go:862`), proven in m2j at level 3 and level 5.
- [x] Done. Evidence: level 3, 2026-10-12. `internal/domain/agent` (descriptor per ADR-0063 §5; the package owns `Programs`, `Clients`, `WordPattern` (ADR-0065), `ToolPattern`, `MaxCommands`, `MaxWords`, and `observe.go` now uses them); registered in `main.go` and the schema test; `system.Version` `0.22.0`; `schema/capabilities.json` +139/−1, the version line and the block only. Tests: `TestRecordCommandDescriptor`, `TestRecordCommandHandler` (valid record back; `REQUEST_INVALID_PARAMS` for a bad program, a path, a spaced word, a flag word, a 65-character word, `::app`, four words, five or no commands, another or no client, a non-tool or over-long suggestion), `TestWordPattern`, `TestTagsMatchTheExportedFacts`, `TestObservationsAreWhatTheCapabilityAccepts`, `cmd/droidyard` `TestRecordCommandIsTheHooksChannelOnly` (absent from `tools/list` under default, `all`, `core`; `ClientMCP` → `REQUEST_NOT_ALLOWED_HERE`, no id; CLI round trip; `operation.watch` sees running then completed with `client: cli`). Seen failing: audience `all` (seven tests, among them the new cmd test; the verifier reran it); doccheck without the mcp.md and mvp.md rows (`TestCapabilityNamesAgree`); `liveBudget` raised by 29 (to 26,346), its exactness shown by the verifier at 26,345. Opus verifier: proceed; its [safety] finding fixed (`suggested` bounded to `^[a-z]{1,16}_[A-Za-z]{1,32}$`, schema regenerated, a row added); its [contract] finding (nested `words` missing or null accepted, though the schema requires it) left lenient, and step 7's hook sends `[]`. `pnpm -C web run verify` green.

### 7. The hook observes; `init` writes the after-command entries

- Builds:
  - `cmd/droidyard/hook.go`, on an after-event:
    - Compute the hint first.
    - If anything was observed, dial without auto-start, call `openProject`, then `agent.recordCommand`, all within the same 2 s deadline.
    - Answer `AfterOutput` whatever happened to the record.
    - Never deny, and never exit 2.
  - `internal/assistant/hookentry.go`, with the same command line under ADR-0037 §2's rules:
    - Claude Code: `hooks.PostToolUse[]` with `"matcher":"Bash"`, plus the failure event if step 1 (b) found one;
    - Cursor: `hooks.postToolUse[]` with `"matcher":"Shell"`, plus `postToolUseFailure` if c3 showed it.
  - Documents:
    - the usage text of `hook` and `init`;
    - `mcp.md` §Features beyond tools' row (ADR-O);
    - register: row 174 narrowed (the observation is built; the redirect and the session-start context stay deferred), plus new rows for lexical blind spots (`sh -c`, scripts, aliases), a command recorded under the hook's root when run in another project, and no switch for the hint;
    - `liveBudget` changes by the delta.
- Implements: ADR-O §1–§3, §7.
- Check: `hook_test.go`, with the re-executed binary:
  - Claude Code's `PostToolUse` with `./gradlew assembleDebug`: `additionalContext` names `build_assemble`, and the test daemon holds one completed record with `[assembleDebug]` and `client: claude`. The same for Cursor's fixture, and for a failing command's event.
  - With no daemon: the hint, no record, no lock.
  - A non-Android command passes and dials nothing.
  - With `workspace.open` held past 2 s: the hint comes at about 2 s, with no record.
  - `-Ppassword=hunter2` appears nowhere.
  - The exit code is never 2.
  - `TestInitWritesTheHookEntry`: the new elements, a second run unchanged, `--uninstall` restoring the file, a user's own `PostToolUse` element kept.
  - Seen failing: with auto-start dialling; with the record made before the hint (the hint is lost in the held case); with the `Shell` matcher dropped.
- Schema: no change.
- Needs: steps 5, 6.
- Not here: the row in Activity (step 8).
- Rests on: the hook never blocks and never starts a daemon. Level 3 here, level 5 at step 9.
- [x] Done. Evidence: level 3 (in-process daemon over a real socket, the hook binary re-executed), 2026-10-12. `cmd/droidyard/hook.go`: after-events decoded first (`DecodeAfterEvent`), the hint computed before any dial and stored as the deadline and panic fallback (`atomic.Pointer`), then `openProject` and `agent.recordCommand` on two connections without auto-start, each with the deadline; `words` sent as `[]`, never null. `init` writes Claude Code `PostToolUse` and `PostToolUseFailure` (`Bash`) and Cursor `postToolUse` and `postToolUseFailure` (`Shell`) beside the existing entries. `hook_observe_test.go`: the hint and exactly one completed record (exact JSON, the right client) for all four events, compound lines included, no flag value, assignment or serial in the answer, record, stderr or daemon log, and no words in the daemon log; no daemon → the hint, no lock, no run dir; a non-Android command, another tool, malformed input and Cursor's `permission_denied` → no dial; `workspace.open` held → the hint at about 2 s; never a deny, never exit 2 (a forced panic included); `TestCommandRecordSendsAnEmptyWordsListNotNull`; `TestInitWritesTheHookEntry`, `TestHookEntryShape` and the golden trees with the new elements, a second run unchanged, `--uninstall` exact, a user's own `PostToolUse`/`postToolUse` kept. Seen failing: auto-start dialling (the no-daemon test); the record before the hint (the held test, both clients); the `Shell` matcher dropped (`TestHookEntryShape`, rerun by the verifier, and the golden diff). Register: row 174 narrowed; limit rows for the lexical reading, another project's command recorded under the hook's root, and a Cursor command refused by `cli.json` recorded as run; a deferred row for a hint or record switch. `mcp.md`: two hooks adopted (ADR-0061, ADR-0063). `liveBudget` +31 (to 26,377). Opus verifier: proceed, no blocking or important findings; its four nits fixed (the hook's doc comment, the usage text, `init`'s summary, the daemon-log words assertion).

### 8. Activity shows the record

- Builds: `web/src/lib/ops/blocks.ts`, `Activity.svelte` and `web/src/lib/projects/sessions.ts`, per ADR-O §6. Load the two vendored Svelte skills first, and run `verify` and the autofixer gate (`AGENTS.md`).
  - When an `agent.recordCommand` row completes, read its result once through `operation.wait {timeoutMs: 0}`, in `restore.ts`'s wire shape.
  - `actorOf`: "Claude Code" or "Cursor". The title is "ran `gradle assembleDebug` outside droidyard", with the tool shown as text. `you` is false.
  - `keyOf` returns null for this capability, so no "You" session row lights up. `hidden` exempts it.
  - `admitted` lets it through under All and Agents, not under You.
  - A failed read leaves the default title.
- Implements: ADR-O §6.
- Check: Vitest:
  - actor, title and tool, from a fake in the wire shape;
  - shown under All and Agents, hidden under You;
  - `keyOf` returns null;
  - one read per row.
  - Seen failing: with the result read as `OperationView`; with `hidden` not exempting the row.
- Schema: no change.
- Needs: step 6.
- Not here: records from before the tab loaded (register row 244).
- Rests on: —
- [ ] Done. Evidence:

### 9. Level 5: both clients, the trash and the observation

- Builds: nothing. The main session runs it under K1–K4 in a fresh copy, with the binary at step 8's commit and the Activity tab open in headless Chrome.
  - Claude Code, the one run: `init --client claude` on the copy, `--max-turns 8`. The agent is prompted to:
    - `cd` into the Kotlin package;
    - `rm Obsolete.kt`;
    - run `<abs fake adb> devices`;
    - run `<abs fake adb> install missing.apk`.
  - Cursor, one run: the same two `adb` commands.
- Implements: A3 and A5 at level 5.
- Check:
  - Claude Code:
    - The `rm` is denied with the `trash` command; the agent runs it, and the file is trashed.
    - The session's JSONL under `~/.claude/projects/<copy's slug>/` holds the hint naming `device_list`, delivered as context after the `adb devices` call.
  - Activity, read through CDP: one row per client per `adb` command ("Claude Code ran `adb devices` outside droidyard", …). `install missing.apk` is recorded too, or its absence is put down to step 1's failure-event answer.
  - `daemon.log` has a `client=cli agent.recordCommand … completed` line for each row.
  - For Cursor, where c2 cleared the hint, the model's next message names the tool.
  - K2–K4 hold. Zero orphans.
  - The evidence updates register rows 163, 164 and 169 at the close.
- Schema: no change.
- Needs: steps 4, 7, 8.
- Not here: the IDE (step 10); the Opus verifier's close review, which is due for a contract change.
- Rests on: each client delivers the after-event's context to the model. Level 5 here.
- [ ] Done. Evidence:

### 10. The owner's IDE checklist

- Builds: nothing. After `droidyard init --client cursor`, the main session gives the owner these checks in Spanish, each with the sign to look for:
  1. Enable droidyard's MCP server once. Are its tools listed?
  2. Is the `droidyard-android` skill offered?
  3. Are the read-only droidyard tools approved without a prompt?
  4. A delete by a relative name in the terminal: is the agent told to run `droidyard trash`, does it run it, and does Activity offer Restore?
  5. A delete with no tool named (Cursor's `Delete`): does the agent quote the trash message?
  6. `./gradlew assembleDebug` in the agent's terminal: does Activity show the row, and does the agent mention `build_assemble`?
- Implements: gaps 3 and 4; A9.
- Check: each item has the owner's answer, or "not reported". At the close, each item not confirmed becomes or updates a register row (unverified, MCP/agents). Each failure is a finding for the main session.
- Schema: no change.
- Needs: step 9.
- Not here: driving the owner's IDE or capturing their screen.
- Rests on: —
- [ ] Done. Evidence:

## Not in this milestone

- Blocking or redirecting `gradlew` or `adb`: ADR-0037 §Alternatives considered, and the owner's concern about it.
- A switch to keep the record but drop the hint, or the reverse: a register row (step 7).
- Recording every command, or whole command lines: Q2's alternatives.
- An agent client kind for the CLI, so that `trash`'s deletes show under the agent: register row 244.
- An OpenCode or Codex hook: register rows 169 and 175.
- An `init` allow rule for `trash`: ADR-0037 §3.
- `trash` in the skill or the `AGENTS.md` block: the deny teaches it, and ADR-0037 §2 caps the block at five lines.
- A Gradle build in the level-5 runs: step 5 covers its parsing.

## Design questions

The substance is the owner's: Cursor at Claude Code's level, and observing rather than intercepting. Below are drafts. At step 1 the main session writes them as ADRs, amended by step 1's facts, under the owner's standing instruction (Delegated), as in m2j. Neither blocks unless the main session declines one.

### Q1. How does Cursor's relative `rm` reach the trash, and with what command?

- Blocks: steps 1–4, 9.
- The documents say: ADR-0061 §2 and §3, and research note §6. Nothing covers a command run in the agent's own shell.
- Draft decision, a new ADR:

> **ADR-T: An agent's `rm` is handed `droidyard trash`, which resolves operands in the agent's shell**
>
> - Supersedes: ADR-0061 §Decision (in part: §2's Cursor relative-operand clause; §3's replacement command); ADR-0061 §Consequences (in part: the replacement naming the state root as an inline variable); ADR-0019 §Processes (in part: item 3 gains `trash`; item 4, `trash` never auto-starts); ADR-0024 §Decision (in part: the subcommands row gains `trash`)
> - Relates to: ADR-0054, ADR-0060, ADR-0015 §5; `cli-vs-mcp-hooks-2026-10.md` §6
> - Decided by the owner, 2026-10-12: Cursor supported as well as Claude Code. The shape is Delegated.
>
> **Context.** Cursor's `beforeShellExecution` carries `cwd: ""` while its shell keeps a `cd` ([obs] m2j step 6), so ADR-0061 routes only Cursor's absolute operands. Agents mostly type relative ones. The replacement's JSON needs root-relative paths that only the hook can compute, and its inline `DROIDYARD_STATE_DIR=` defeats allow rules (research note §6). A command run in the agent's own shell resolves relative paths and globs where the agent typed them.
>
> **Decision.**
> 1. `droidyard trash [--state-dir <dir>] [--root <abs>] [rm's -r, -R, -f, combined] [--] <path>…` resolves operands against its cwd.
>    - The root is `--root`, else the project root of its cwd (ADR-0019 §Sessions).
>    - The state root is `--state-dir`, else the environment's (ADR-0021).
>    - It never auto-starts.
> 2. It checks every operand before moving any, by ADR-0061 §2's target rules:
>    - inside the root;
>    - existing, unless `-f`;
>    - a directory only with `-r`;
>    - not the root, `.` or `..`;
>    - not under `build`, `.gradle`, `.kotlin` or `.cxx`;
>    - a link that does not leave the root.
>
>    On any refusal, or with no daemon, it moves nothing, exits 1, and prints the reasons and `command rm <flags> -- <operands>`. The hook passes that command, since a prefix is not routed.
> 3. Otherwise it opens the workspace and calls `file.delete` as `client=cli`. It prints one line per item, and exits 0 only when all are trashed.
> 4. For a routed `rm`, and for Cursor's `Delete`, the hook's message is `<abs droidyard> trash [--state-dir '<dir>'] --root '<root>' [-r] [-f] -- <operands>`.
>    - Each operand is re-rendered from the typed word: an unquoted `*` or `?` stays bare, so the shell expands it again as typed, and every other character is single-quoted.
>    - `--state-dir` appears only when the hook's own state root came from `DROIDYARD_STATE_DIR`.
>    - The prefix `<abs droidyard> trash` can take an allow rule.
> 5. With `cwd: ""`, a relative operand routes on lexical checks only: no `..` component, no build-output component, not `.`. `trash` checks the rest. Where the hook can place a target (an absolute `cwd` or operand), today's checks stay.
> 6. Cursor's deny carries the text in both `user_message` and `agent_message`. Plan m2k step 1 records which one reaches the model.
>
> **Consequences.**
> - When `trash` refuses, the agent spends a turn.
> - `trash` is usable by hand.
> - Its deletes show as "You" (register row 244).
> - `init` writes no allow rule for it (ADR-0037 §3).
> - Documents: `mcp.md` §Features beyond tools; the register.
>
> **Alternatives considered.**
> - **Keep `call file.delete`, with `"$PWD"` in its JSON**: fragile to quote and to approve. Wins if Cursor's event ever carries the real cwd.
> - **Ask the agent for `pwd` first**: a turn on every delete.
> - **Rewrite the command**: `beforeShellExecution` cannot (ADR-0061).
> - **Root-relative paths in the `trash` command**: they would resolve against the shell's cwd.

### Q2. How are an agent's Android commands observed?

- Blocks: steps 1, 5–9.
- The documents say: ADR-0037 rejected a redirect hook, which "wins if a client or model is shown falling back to `adb` or Gradle", and nothing observes that today (research note §5). ADR-0043 §2 keeps parameters and results off Activity's events. `capability-api.md`: domains are fixed by the registry.
- Draft decision, a new ADR:

> **ADR-O: The hook observes an agent's Android commands: a hint, and a record in Activity**
>
> - Supersedes: ADR-0061 §Decision (in part: §5, `init` also writes the after-command entries)
> - Relates to: ADR-0004, ADR-0015 §1, §6, ADR-0037 (the redirect stays rejected), ADR-0043 §2, ADR-0048
> - Decided by the owner, 2026-10-12 ("lo que veas más indicado. Procede"): monitor rather than intercept. The shape is Delegated.
>
> **Context.** Intercepting `./gradlew` or `adb` would block what droidyard does not cover. Nothing shows the owner when an agent goes around droidyard, and nothing tells the agent the equivalent.
>
> **Decision.**
> 1. `droidyard hook` reads the command after a shell command runs: Claude Code `PostToolUse` Bash, and Cursor `postToolUse` Shell, plus the failure events plan m2k step 1 found.
>    - It finds simple commands whose program is `gradlew`, `gradle`, `adb`, `emulator`, `sdkmanager` or `avdmanager`, at most four per line.
>    - It splits at unquoted operators and skips assignments and `command`, `exec`, `env`, `time` and `nohup`.
>    - The reading is lexical, not a boundary.
> 2. It never denies, rewrites or delays past ADR-0061's 2 s. For an observed command it adds a hint naming the droidyard MCP tool that does the same: a table in code, tested against `tools/list`. The hint is on by default with the entry.
> 3. If a daemon answers (never auto-started), it opens the workspace and calls `agent.recordCommand` within the same 2 s. Otherwise nothing is recorded, and the hint still goes.
> 4. The record holds the client, and per command:
>    - the program;
>    - at most three positional words matching `^[A-Za-z0-9][A-Za-z0-9:._-]{0,63}$`, and for `adb shell` only the shell's command word;
>    - the suggested tool.
>
>    Flags, flag values, assignments and the command line are never recorded. A positional word can still carry text the agent typed; the pattern and the cap bound it.
> 5. `agent.recordCommand`, in a new domain `agent`:
>    - kind command, because its effect is the record it leaves; scope workspace; stateless;
>    - audience human: it is the hook's channel and never an MCP tool;
>    - destructive false (it changes nothing in the project or on a device), not idempotent, not open-world;
>    - result: the record; invalid params `REQUEST_INVALID_PARAMS`;
>    - not untrusted: the text is the agent's, and no device or project text, within the pattern;
>    - API `0.22.0`.
> 6. Activity reads a record's result once (`operation.wait`, `timeoutMs` 0). It shows "<client> ran <program words> outside droidyard" with the tool, attributed to the client, under Agents, not You.
> 7. `init --client claude|cursor` writes the after-command entries with the same command line, under ADR-0037 §2's rules.
>
> **Consequences.**
> - Each shell command starts one more short process after it.
> - Activity shows records only while a tab is open, and `daemon.log` names the capability without words.
> - A command in another project is recorded under the hook's root.
> - The records show the owner live when an agent goes around droidyard; they are not yet a durable measurement.
> - Documents: `mcp.md`, `mvp.md`, the register.
>
> **Alternatives considered.**
> - **Deny and redirect**: blocks what droidyard lacks (ADR-0037; the owner).
> - **Record every command, or whole lines**: secrets.
> - **Record before the command runs**: it records commands the user then refuses.
> - **A log file under the state root**: invisible in the UI, and a new state file.
> - **A `hook` client kind in the hello**: a protocol change that rewrites every actor rule. Wins with the agent sessions milestone.
> - **Parameters on `operation.state`**: ADR-0043 §2 rejected them.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| Cursor's `postToolUse` on Shell fires, carries the command, and delivers `additional_context`; what fires on a failing command | 5, 7, 9 (Cursor) | step 1 c2, c3; otherwise ADR-O drops Cursor's hint |
| Claude Code's `PostToolUse` context reaches the model; which event fires on a non-zero exit | 7, 9 | step 1 (b), from the reference; step 9 |
| Which of `user_message` and `agent_message` reaches the model, in `cursor-agent` and in the IDE | 3 | step 1 c1, step 4; step 10 for the IDE |
| The IDE honours `cli.json`, the skill, the `Delete` deny, the hint and the hooks | none in code | step 10, the owner |
| Agents run the `trash` command, and act on the hint | none (fail open) | steps 4, 9: one run per client |
| The per-program flag tables catch every value that could hold a secret | 5 | a heuristic; bounded by the pattern, the cap and `adb shell`'s one word |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | B1: the run rules left a path to the owner's daemon (MCP without the state root; a dropped `--state-dir`) and to the real `adb`; m1o's fence only half carried; allow rules unnamed | K1–K4 rewritten: client launch env has the state and config dirs; droidyard MCP denied in both clients; m1o K2/K3 and §Changes 272/273 by reference, full deny list, `Shell(android)`, github/Figma off, stream-json; allow rules named; `ADB` guard and port 5199; default root's lock and `ws/` compared; step 1 c4 proves the fence |
| 1 | revise | B2: re-quoting with bare `*`/`?` could trash files never named | only characters unquoted in the typed word stay bare (ADR-T §4, step 3); rows `rm 'a*b.kt'` and `rm "my dir"/*.kt`; the mutation named |
| 1 | revise | B3: "not read-only (so Activity lists it)" contradicted by `hidden`/`admitted`; step 8 omitted functions | ADR-O §5 justifies kind on its own terms and sets destructive explicitly; step 8 names `hidden`, `admitted`, `actorOf`, `keyOf`, with filter rows |
| 1 | — | N1 `trash_test.go` exists; N2 no failing command at level 5; N3 positional secrets (`adb shell echo`); N4 transcript source unnamed; N5 the Gradle leg; N6 row 157's clause still true | `trash_cmd_test.go`; the fake `adb` fails `install missing.apk` in both runs; `adb shell` keeps one word, wording narrowed; the JSONL path named; Gradle dropped; row 157 keeps the clause |
| 2 | proceed | B1, B2, B3 resolved. Non-blocking: K3's `mcp list` check ran once, but each `init` writes droidyard's MCP entry. Nits: m1o K3's `Shell(*)` ambiguity; c4 tested only `adb version` | K3: the check runs in each copy after `init`, before steps 4 and 9 drive Cursor; m2k's allow list replaces `Shell(*)`, no kit `PATH`; c4 adds a `platform-tools` path |
| 1 | — | Nits: c1 repeats m1o; "no allow rule can approve" overstated; ADR-O's evidence consequence overstated; size; `-rf` | c1 kept as a re-check; both reworded; trimmed; `trash` parses rm's flags, combined ones included |

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.

- 2026-10-12, step 1 (d). ADR-T is [ADR-0062](../adr/0062-agent-rm-handed-droidyard-trash.md) and ADR-O is [ADR-0063](../adr/0063-hook-observes-android-commands.md); the names T and O stay in this plan's text.
- 2026-10-12, step 1 (b), (c), ADR-0063 §1 (Delegated). Both clients' failure events are observed: Claude Code `PostToolUseFailure` (matcher `Bash`), Cursor `postToolUseFailure` (matcher `Shell`) skipping `failure_type: "permission_denied"`, which is a deny, not a run. Steps 5 and 7 decode and write them; step 7's `init` writes four entries in all (`PostToolUse`, `PostToolUseFailure`, `postToolUse`, `postToolUseFailure`). Cursor's hint stays (c2).
- 2026-10-12, step 1 c4. A command Cursor's `cli.json` refuses fires `postToolUse` with `exitCode: 0` and empty output, indistinguishable from a silent run: it is recorded and hinted as run. Step 7 adds a register row (limit). The decoder never reads `user_email`, and fixtures replace it.
- 2026-10-12, step 3, [ADR-0064](../adr/0064-trash-replacement-dashdash-as-typed.md) (Delegated). The replacement carries `--` only when the `rm` did (the verifier's N1: a Cursor glob expanding to a `-name`).
- 2026-10-12, step 5, ADR-0065 (Delegated). A recorded word may start with one `:` (Gradle task paths); step 6 validates the same pattern. `adb exec-out` keeps one word, as `adb shell`.
- 2026-10-12, step 1 c1. Cursor appends "Do not suggest workarounds to the blocked tool." to a hook's deny; whether its agent still runs `trash` is step 4's question (ADR-0062 §Consequences).
