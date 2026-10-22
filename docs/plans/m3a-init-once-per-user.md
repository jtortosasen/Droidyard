# Plan: Milestone 3a — `init` once per user: every project and worktree, with no `init` per project

- Status: Approved
- Approved: 2026-10-23, Delegated (the main session, under the owner's standing instruction to apply the recommended option): Q1–Q4 as recommended, recorded in ADR-0124 after step 1.
- Date: 2026-10-23
- Milestone: "3a: `droidyard init --user` installs the assistant integration once, at user level, so every project and every worktree has it with no `init` per project; per-project `init` gains `--exclude` and `--gitignore` for the files it wrote that git does not track" (the owner's request, 2026-10-22, with the main session's recommendation accepted: "me parece perfecto"; `docs/README.md` §Where the work stands: "Phase 3: use and polish; next, the owner's choice").
- Phase: 3, use and polish. It has no phase table; this plan's Scope table is the boundary.
- Contract: ADR-0037 §Decision 2, 5 (as superseded in part by ADR-0038, ADR-0061, ADR-0090); ADR-0038 §Decision and §Alternatives considered (user level, rejected on 2026-10-09, with the conditions it named); ADR-0061 §1, §3, §5 and §Alternatives considered; ADR-0019 §Processes 2, 4, §Sessions; ADR-0075 §6; ADR-0082 §3, §6; ADR-0085 §1, §2; ADR-0095 §1, §3, §5; ADR-0090 §1–§3; ADR-0015 §5; `docs/interfaces/mcp.md` §Transport and session, §Features beyond tools.
  - **New, Delegated, written by the main session after step 1:** ADR-0124 (Q1–Q4; the number is reserved for 3a).
  - It supersedes in part ADR-0037 §Decision 2, ADR-0038 §Decision, ADR-0061 §Decision (§1, §5), ADR-0019 §Processes (2, 4) and §Sessions, ADR-0075 §Decision (§6: the MCP binding without `--root`) and ADR-0085 §Decision (§1, §2). It extends ADR-0090 §1.
- Register: the commit that adds this plan sets `Work` = `3a` on row 288 ("A worktree made from a commit without `init`'s files … has no hook entries"). The row closes with the user install; a setup that is only per project keeps the limit. Rows 270 and 325 are not joined (§Not in this milestone).
- Lanes: none in parallel that touch these files. The main session edits `docs/plans/README.md`, `docs/README.md` and `.claude/skills/work-board/board.go` in this tree.

**What the owner should see:** you run `droidyard init --user --client all` once.

- Then a Claude Code chat started in an Android project, in a subfolder below its settings file, or in a `git worktree add` worktree of it lists droidyard's tools, is bound to that tree, and records its Android commands there. No file is written in the project.
- In a folder that is not an Android project, nothing starts and nothing is recorded.
- Per project, `init --client claude --exclude` keeps `git status` clean of the files init wrote.

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| `init --user` for claude, cursor and opencode: the MCP entry, the hooks (claude, cursor), the allowlist and the skill, at user level, with no `--root` in any entry; idempotent, `--uninstall`, `--dry-run`; its own entries recognised | yes | steps 4, 6 (owner item 2; Q1) |
| A project-level droidyard entry that takes precedence is reported | yes | step 6 (Q1) |
| `droidyard mcp` outside an Android project starts no daemon and opens no workspace | yes | step 2 (Q2) |
| The hook stays cheap and silent outside a project; Cursor's root comes from `workspace_roots` | yes | step 3 (Q3) |
| Per-project `--exclude` / `--gitignore` for untracked files; tracked files reported; a rerun adds lines; `--uninstall` removes them | yes | steps 5, 6 (owner item 1; Q4) |
| Row 288 | yes | closed at step 7 |
| Level 5 with a real Claude Code in a scratch repository and its worktree, configuration isolated | yes | steps 1, 7 |
| Row 270, the state-root half | no | user entries carry the state root as project ones do (ADR-0061 §3); unchanged |
| Row 325 (Orca: hook `timeout`, a missing-binary guard) | no | it changes the shape ADR-0090 recognises, at both levels; step 7 adds a row for its cost at user level |
| The Cursor IDE | no | it cannot be driven here; Unverified |
| The Sessions panel rework; Git's `--ignore-submodules=all` | no | other milestones (the brief) |

## Starting point

- **`init`** (`cmd/droidyard/init.go`) works per project only. Its root is `--root`, else `projectOf(cwd)` (ADR-0075 §6). It refuses the home directory and builds the read-only tool list from the registry without a daemon. `internal/assistant` writes:
  - `plan` (`assistant.go`): `mcp --root <root>` in `.mcp.json`, `.cursor/mcp.json` (with `env` for a custom state root) and `opencode.json`;
  - the hooks in `.claude/settings.json` and `.cursor/hooks.json`, through `HookCommand` (`hookentry.go`). `initHookShape` recognises exactly `<bin> hook --client <c> --root <root>`, six words, for one root (ADR-0090 §1);
  - the allowlists in `.claude/settings.json` and `.cursor/cli.json`, plus `deny: []`;
  - the skill in `.claude/skills/` and `.agents/skills/` (`keepIfShared`).

  `fsRoot` (`fsutil.go`) confines every path to one root. `maxFileSize` is 1 MiB.
- **The shim** `mcpShim` (`client.go:225`) roots the session with `resolveRoot`, which falls back to cwd (`root.go:15`). It always auto-starts, unless `--no-autostart`. The daemon already serves a session with no root: `openBound` returns at once (`adapters/mcp/mcp.go:585`), and workspace tools get `REQUEST_MISSING_WORKSPACE`.
- **The hook** (`hook.go`):
  - `parseHookArgs` requires an absolute `--root` (`hook.go:75`).
  - `hookRoot`/`commandCwd` implement ADR-0085 and ADR-0095 §5.
  - `hookCheckEdit` dials `agent.checkWrite` whatever the root.
  - The decoders in `internal/assistant` read `cwd`, never `workspace_roots`.
- **Git:** `internal/domain/git/run.go` is "the only place a git process is started", with the filter overrides for untrusted roots.
- **mcp-go v1.1.1** answers `tools/list` with METHOD_NOT_FOUND when the server has no tools capability (`server/request_handler.go:477-483`). The daemon declares it with `server.WithToolCapabilities(false)` (`mcp.go:313`).
- **Client facts** (the brief's research, 2026-10-22, claude 2.1.308, cursor-agent 2026.10.18, opencode 2.0.18):
  - Every stdio server starts in the launch directory [obs]. Claude Code sets `CLAUDE_PROJECT_DIR` to it [obs].
  - User hooks merge across levels, and stdin `cwd` follows worktrees [docs].
  - cursor-agent passes `${workspaceFolder}` literally [obs]. A global Cursor server runs without approval [obs].
  - The Cursor IDE's server cwd is undocumented [unverified].
- **Tests to build on** (`cmd/droidyard`): `initRun`; `runHook` and `startFakeDaemon`, which counts connections; `worktreeRepo` and `addWT`; `projectWith` and `boundReady`. `samples/golden` is an Android project with an included build (`build-logic`) that has a settings file of its own.
- **Baseline, 2026-10-22 at `566783c`:** `go vet ./...` clean. `go test ./...` passes everywhere. `TestLiveCorpusWithinBudget` failed once while the main session was editing `docs/README.md`, and passes at `566783c`. The API is `0.36.0`, unchanged here. `TestADRMentionsExist` refuses ADR-0124 until the main session writes it. The index row comes in the plan's commit.

## Acceptance

- [ ] A1. **One install per user.** `init --user --client all` writes Q1's files in the user's configuration and nothing in any project, and starts no daemon.
  - A second run changes nothing, and `--dry-run` writes nothing.
  - `--uninstall --user` removes init's own files and keys, and keeps the user's own, including a `droidyard hook --root …` entry the user wrote.
  - A hook entry from another binary path is replaced, not doubled. A differing MCP entry is kept and reported (ADR-0037 §2).
  Source: owner item 2; ADR-0037 §2, §5; ADR-0090. Proven by: steps 4, 6, 7.
- [ ] A2. **Every worktree, no file in it.** With only the user install, a Claude Code session started in a `git worktree add` worktree of a project lists droidyard's tools and is bound to the worktree (`mainRoot` is the main tree). Its Android command is recorded in the worktree's Activity. Source: owner item 2; row 288; ADR-0082 §6, ADR-0085 §1. Proven by: steps 2, 3, 7 (the session half reaches level 5 only if step 1's authentication probe passes; else it stays at level 4 and in Unverified).
- [ ] A3. **Silent outside a project.** In a folder that is no project by `projectOf`:
  - `droidyard mcp`, from the Claude entry or the Cursor one, answers a session with no tools, and dials and starts nothing;
  - the hook answers the pass-through (or the hint), dials nothing, and costs no more than the bound measured at step 7;
  - no workspace is opened.
  Source: Q2, Q3; the two conditions ADR-0038 §Alternatives considered named. Proven by: steps 2, 3, 7.
- [ ] A4. **Cursor.** The user hook takes its project from `workspace_roots`, else `CURSOR_PROJECT_DIR`, else `cwd`. The user MCP entry's `--from ${workspaceFolder}` is the folder the project is found from when the client expands it, and is ignored when it arrives literally. Source: Q1, Q2, Q3. Proven by: steps 2, 3, 4 (level 4; the IDE is Unverified).
- [ ] A5. **Per-project ignore.**
  - `--exclude` writes one marked block into the common directory's `info/exclude`, which every worktree reads; `--gitignore` writes it into the root's `.gitignore`.
  - The block holds lines for the run's files that git does not track; tracked ones are reported.
  - A rerun brings the block up to date; `--uninstall` removes it and nothing else.
  Source: owner item 1; Q4. Proven by: steps 5, 6, 7.
- [ ] A6. **Contract.**
  - No change in `schema/capabilities.json`; no capability or error code; the API version is unchanged.
  - In a project the MCP tool list is unchanged.
  - Only the help of `init`, `mcp` and `hook` changes, and `mcp` gains `--from`.
  Source: `capability-api.md` §Discovery and versioning. Proven by: steps 2, 3, 4, 5, 6.

## Steps

**Order and lanes.** Steps 2, 4 and 5 touch disjoint files and may run in parallel (`isolation: "worktree"`). Step 3 runs after steps 2 and 4: it uses step 4's shape recognition in `hookentry.go`, and it shares `commandhelp.go` with step 2. No test may read or write the real home. Every helper sets `HOME`, `CLAUDE_CONFIG_DIR` and `XDG_CONFIG_HOME` to temporary directories, and fails if a resolved path lies under the real `os.UserHomeDir()`.

### 1. Probes before the design is written (the main session, no product code)

- Builds: nothing in the tree; evidence under the lane's evidence directory, K1–K3 applying. Run in this order:
  - **P0, authentication.** Does `claude -p 'say ok'` (and `cursor-agent -p`) authenticate under K1's isolated configuration? No credential of the owner's is copied. If not, the main session asks the owner whether to provide a dedicated token for the isolated environment (`claude setup-token`, `CLAUDE_CODE_OAUTH_TOKEN`). Without one, P2 and P3's runs are recorded as not run, and A2's session half stays at level 4 and goes into Unverified.
  - **P1, Claude's user-scope write.** Run `claude mcp add-json --scope user droidyard '{"type":"stdio","command":"<bin>","args":["mcp"]}'` (main's binary), then `claude mcp list` from a worktree, then `claude mcp remove --scope user droidyard`. Do it once with `CLAUDECODE=1` set and once without. Record:
    - which file changed, with `CLAUDE_CONFIG_DIR` set and with `HOME` alone;
    - the entry as it was stored (step 4 normalises against it);
    - whether `mcp list` started a shim bound to the worktree (the lane daemon's log names the root).
  - **P2, Claude's hooks** (if P0 passed). Install a probe hook at user level and one at project level, each appending its stdin and `CLAUDE_PROJECT_DIR`. Launch `claude -p` in the worktree and once in a project subfolder. Record whether both hooks fire for one Bash call, whether `CLAUDE_PROJECT_DIR` is the launch directory while `cwd` follows the shell, and which directory's `.claude/settings.json` the project hook was read from (the yield's file, N13).
  - **P3, cursor-agent.** With `HOME` isolated, record:
    - whether a `~/.cursor/hooks.json` probe fires in a `-p` run, and which of `workspace_roots`, `cwd` and `CURSOR_PROJECT_DIR` it carries;
    - what `cursor-agent mcp list` shows for a global entry with `--from ${workspaceFolder}`;
    - whether `~/.cursor/cli-config.json` is rewritten by a `mcp list` and a `-p` run (hashes before and after), and whether it needs `deny`.
  - **P4, a server with no tools.** Register a 10-line stub stdio server at user scope in the isolated configuration. It answers `initialize` as step 2 will (the tools capability, an empty `tools/list`). Record what `claude mcp list` and `cursor-agent mcp list` show from a non-project folder, and `-p` too if P0 passed: whether there is any failure, warning or notice.
- Implements: risk first. P1 decides Q1's Claude write path, P2 and P3 decide Q3's sources and its yield, P3 decides how Q1 treats `cli-config.json`, and P4 decides Q2's answer.
- Check: each probe's decisive output, quoted. The outcomes redraft the questions as follows, recorded under Changes before ADR-0124 is written:
  - P1 finds no `add-json`, or another file: Q1's Claude MCP entry becomes a refusal that prints the line to run.
  - P2 finds no `CLAUDE_PROJECT_DIR`: Q3 uses `cwd` for Claude.
  - P2 finds the hooks not merged: the yield is dropped.
  - P2 finds the settings read from the repository top: the yield reads there.
  - P3 finds `cli-config.json` rewritten as live state: init still merges it, but prints "close cursor-agent first" (Q1).
  - P3 finds no user hook fired: Cursor's user hooks stay Unverified.
  - P4 finds a client flagging a server with no tools: Q2 is reopened with that evidence before step 2.
- Schema: no change.
- Needs: K1; `claude`, `cursor-agent`, `python3` on PATH.
- Not here: the Cursor IDE; OpenCode (step 7).
- Rests on: —
- Review: none; the facts feed ADR-0124.
- [x] Done. Evidence: probes run 2026-10-23, isolated `HOME`/`CLAUDE_CONFIG_DIR`/`XDG_CONFIG_HOME`/state under `/private/tmp/dy3a.YtnjOv` (scripts in `bin/`, outputs in `evidence/`); the owner's configuration hashed before and after, identical. P0: neither client authenticates in isolation (`claude -p` "Not logged in", cursor-agent "Authentication required"); no credential copied. P1: `claude mcp add-json --scope user` writes `$CLAUDE_CONFIG_DIR/.claude.json` (else `$HOME/.claude.json`), the entry stored as given, identical with `CLAUDECODE=1`; `claude mcp list` from the worktree "✔ Connected", the lane daemon opening the worktree. P2: not run (P0); Claude passed `CLAUDE_PROJECT_DIR` (the launch directory) to a stdio server. P3: hooks not run (P0); a global `--from ${workspaceFolder}` arrives literally, no `CURSOR_*` variable, "ready"; a user `cli-config.json` without `deny` unchanged by every unauthenticated run. P4: a server with the tools capability and an empty list shows "✔ Connected" (Claude) and "ready" (cursor-agent), no warning.

### 2. `droidyard mcp`: the project or a session with no tools, and `--from`

- Builds: `cmd/droidyard/client.go` (`mcpShim`), `commandhelp.go` (`mcpHelp`).
  - **`--from <dir>`** (new) names the folder the project is found from, in place of the cwd. A value of the form `${…}` (a client variable left unexpanded) is ignored. `--from` and `--root` exclude each other.
  - **Without `--root`,** the project is `projectOf(<from or cwd>)` (the rule `ui` and `init` use), no longer `resolveRoot`'s fallback to cwd. With a project, today's hello, auto-start and binding are unchanged. `--root` is unchanged, and per-project entries keep it.
  - **No project:** the shim dials nothing and starts nothing. It serves the session itself with mcp-go:
    - `initialize` answers as server `droidyard` at the build's version, with the tools capability declared (`server.WithToolCapabilities(false)`) and no tools;
    - the instructions are one line: "No Android project at <dir>: droidyard has no tools in this session. Start the assistant in the project's folder or one of its worktrees.";
    - `tools/list` returns `[]`; `ping` is answered;
    - the shim exits 0 when stdin ends, writes nothing on stderr, and creates no file.
- Implements: ADR-0124 Q2; ADR-0019 §Processes 2, 4 and §Sessions, and ADR-0075 §6, as superseded.
- Check: `cmd/droidyard`, the binary run as `mcp_test.go` does, with a fresh `DROIDYARD_STATE_DIR`:
  - `TestMCPOutsideAProjectIsASessionWithNoTools`: cwd is a folder with no Gradle build. `initialize` is answered, `tools/list` gives `[]`, stderr is empty, and the shim exits 0 at EOF. No `daemon.lock` and no socket exist. Seen failing on today's code, where a daemon starts.
  - `TestMCPOutsideAProjectNeverDials`: with `startFakeDaemon` listening, zero connections. Seen failing with the dial kept.
  - `TestMCPFrom`:
    - `--from <non-project dir>` gives the session with no tools and zero dials. Seen failing with `--from` handled as `--root`: a dial.
    - `--from <project subfolder>` binds the project (`workspace_describe`).
    - `--from '${workspaceFolder}'` from a project subfolder binds the cwd's project, and from a non-project folder gives the session with no tools.
    - `--from` with `--root` exits 2.
  - `TestMCPInALinkedWorktreeBindsTheWorktree`: with no `--root` and cwd an `addWT` worktree, the session is bound to the worktree, and `mainRoot` is the main tree.
  - **The existing shim tests move into a project.** About 40 of them start a shim without `--root` from the test's own cwd, `cmd/droidyard`, which is no project. To keep them on the daemon path:
    - `startShimIn` with `dir == ""` (`mcp_test.go`) defaults the cwd to a fresh `projectWith(t, "settings.gradle")` root: one helper change, not 40 edits;
    - case (c) of `TestMCPSessionBindsAndOpensItsRoot` ("No ancestor holds a settings file: the cwd is the root") becomes the no-tools assertion, and its doc comment's "else to the cwd" goes;
    - `TestNoAutostartStartsNothing` and `TestMCPShimAutoStarts` (`autostart_test.go`) run from a project directory.

    No other assertion is changed. `TestMCPBadBoundRootDegrades`, `TestMCPBoundOpenPrecedesTheFirstAnswer` and the rest pass. `toollist_bytes_test` and `mcp_test.go`'s tool listing, both run inside a project, prove A6's "in a project the tool list is unchanged".
  - **`--from` edge cases.** An empty `--from`, the expansion of an IDE window with no folder, is treated like an unexpanded one. A relative `--from` is resolved against the cwd. Both are covered in `TestMCPFrom`.
- Schema: no change.
- Needs: ADR-0124; fakes only. In parallel with 4, 5.
- Not here: device tools outside a project (Q2); a real client (step 7).
- Rests on: outside a project the shim never connects. Level 4 (the fake's count).
- Review: the main session's diff review; effort `high` (process lifetime).
- [ ] Done. Evidence:

### 3. `droidyard hook` without `--root`: the client's project, or nothing

- Builds:
  - **`internal/assistant`:**
    - `ClientProjectDir(c Client, event []byte, getenv func(string) string) string` takes, for Claude, `CLAUDE_PROJECT_DIR`, else the event's `cwd`; for Cursor, the first `workspace_roots` entry, else `CURSOR_PROJECT_DIR`, else `cwd`.
    - `HasProjectHook(c, dir) bool` tells whether `dir/.claude/settings.json` (or `dir/.cursor/hooks.json`) holds a hook entry of init's per-project shape for `c`, whatever its root. It uses step 4's shape function.
  - **`cmd/droidyard/hook.go`:** `--root` becomes optional.
    - Without it, the root is `projectOf(ClientProjectDir(…))`. With none, every event kind passes at once with nothing dialled: an `rm`, after a command (the hint is still computed; it needs no daemon), and an edit before and after.
    - When `HasProjectHook` holds for the client's directory, the hook passes at once: the project's own entry runs too (Claude Code merges hooks [docs]) and answers.
    - Otherwise ADR-0085 §1 and ADR-0095 §5 run with that root as `--root`.
    - With `--root`, everything is unchanged.
  - `hookHelp`, `hookUsage`.
- Implements: ADR-0124 Q3; ADR-0085 §1–2 and ADR-0095 §5 with the root the client names.
- Check: `cmd/droidyard` with `startFakeDaemon`'s count and `worktreeRepo`/`addWT`:
  - `TestRootlessHookFollowsTheClientsProjectNotCwd` (the central decision):
    - with `CLAUDE_PROJECT_DIR` project A and `cwd` inside an unrelated Android project B (a sibling), a `PostToolUse` of `./gradlew build` makes no record connection, because `cwd` lies outside A and its worktrees (ADR-0085 §1). With `cwd` in A's subfolder, it records in A;
    - with `CLAUDE_PROJECT_DIR` a non-project folder and `cwd` in B, zero connections;
    - seen failing with the root taken from `cwd`: a record in B.
  - `TestRootlessHookOutsideAProjectDialsNothing`: with `CLAUDE_PROJECT_DIR` and `cwd` a non-project folder, a `PostToolUse` `./gradlew build` gives the hint, and a `PreToolUse` `rm -r x` and an `Edit` give the pass-through, all with zero connections. Seen failing with the root from `resolveRoot`'s fallback to cwd: a dial.
  - `TestRootlessHookRecordsInTheWorktree`: with `CLAUDE_PROJECT_DIR` the main tree and `cwd` a linked worktree, the record lands in the worktree's workspace. The same holds with `CLAUDE_PROJECT_DIR` the worktree.
  - `TestRootlessCursorHookReadsWorkspaceRoots`:
    - `workspace_roots: [<project>]` with `cwd: ""` records in the project;
    - `CURSOR_PROJECT_DIR` alone does the same;
    - with neither, nothing is dialled;
    - seen failing with `workspace_roots` unread.
  - `TestRootlessHookYieldsToTheProjectsOwn`: the project's `.claude/settings.json` holds an init entry with any `--root`; zero connections. Seen failing without the check: one record.
  - Unit tests: `ClientProjectDir`'s sources and their order; `HasProjectHook` does not take a user's wrapper for init's.
  - Every existing hook test with `--root` passes.
- Schema: no change.
- Needs: ADR-0124; steps 2, 4; fakes.
- Not here: OpenCode hooks (ADR-0061 §Alternatives considered); joining Cursor's hooks to their session (the register).
- Rests on: the rootless hook never connects outside the client's project. Level 4.
- Review: the main session's diff review; effort `high`.
- [ ] Done. Evidence:

### 4. `internal/assistant`: the run at user level

- Builds: `internal/assistant` (a new `user.go`; `hookentry.go`).
  - **`Options.User` with `Options.Dirs`:** the absolute paths of Claude's config directory, Claude's `.claude.json`, `~/.cursor`, OpenCode's config directory and `~/.agents`. The caller resolves them (step 6), and `Root` is empty. Each directory is its own `fsRoot`, so a `~/.claude` that links into a dotfiles repository is followed, and nothing leaves it.
  - **Per client, Q1's table:**
    - The MCP entries are `<bin> mcp`; Cursor's is `<bin> mcp --from ${workspaceFolder}`, with today's `env` for a custom state root; OpenCode's is `[<bin>, "mcp"]` (`opencode.jsonc` refused as today).
    - The hooks are `[DROIDYARD_STATE_DIR=… ]<bin> hook --client <c>` in today's matcher shapes, in `<claude>/settings.json` and `~/.cursor/hooks.json`.
    - The allowlists go into `<claude>/settings.json` and `~/.cursor/cli-config.json`. `deny: []` is added there as in `cli.json` only if P3 shows cursor-agent needs it.
    - The skill goes into `<claude>/skills/droidyard-android/` and `~/.agents/skills/droidyard-android/`, with `keepIfShared` over the user files.
  - **Claude's MCP entry** goes through an injected `ClaudeMCP` (`Add(entry json.RawMessage) error`, `Remove() error`) and is never written here.
    - It is read from `.claude.json` read-only, under a read bound of its own (Claude's state: over 1 MiB is ordinary), and compared after P1's normalisation.
    - Absent: `Add`. Equal: unchanged. Different: kept and reported.
    - `--uninstall`, when equal: `Remove`.
    - A dry run calls nothing. No runner: the change is refused, with the command to run as its entry for the user.
  - **The shape (ADR-0090 §1, extended).** `hookentry.go` gains one function, used here and by step 3, that parses a hook command into its client, its root (optional) and its state word. In a user file, the rootless shape is init's: replaced when stale, removed on uninstall. An entry with `--root` in a user file is the user's.
- Implements: ADR-0124 Q1; ADR-0037 §2's rules and ADR-0090 §1–3 at user level.
- Check: `internal/assistant`, temporary directories, a fake `ClaudeMCP` that records its calls:
  - `TestUserRunWritesEachClientsFiles`: every file and key in Q1's table. No `--root` appears anywhere, Cursor's `--from ${workspaceFolder}` is there, and the binary is absolute.
  - `TestUserRunIsIdempotent`: a second run leaves everything unchanged, with no `Add`.
  - `TestUserUninstallRestoresTheFiles`: files seeded with the user's keys, rules and a `droidyard hook --root /p` entry, then an install and an uninstall: each file equals its seed after parsing. Seen failing with the shape matching any `droidyard hook`.
  - `TestUserStaleHookIsReplaced`.
  - `TestClaudeUserMCPGoesThroughTheRunner`: each case above, the dry run, and the nil runner.
  - `TestUserSkillSharedByCursorAndOpenCode`.
  - A symlinked config directory is followed; a file in it that links outside is refused.
  - Every per-project test passes.
- Schema: no change.
- Needs: ADR-0124; step 1's P1 and P3. In parallel with 2, 5.
- Not here: the flags and the real runner (step 6); per-project entries without `--root` (§Not in this milestone).
- Rests on: an uninstall at user level removes only what init wrote. Level 4 (seeded files, the shape mutation).
- Review: the main session's diff review; effort `high` (the user's own configuration).
- [ ] Done. Evidence:

### 5. Ignoring init's untracked files: one git read and the block

- Builds:
  - **`internal/domain/git`:** one exported read in a new file, `Tracked(ctx, start device.Starter, gitPath string, environ []string, root string, paths []string) (Repo, error)`, where `Repo` holds the top, the common directory and the tracked set. It runs `rev-parse --show-toplevel --git-common-dir` and `ls-files -z --full-name -- <paths>` through `run.go`'s runner, untrusted.
  - **`internal/assistant/ignore.go`:** `Ignore(o IgnoreOptions) (Change, error)` takes `Repo`'s facts as input and never imports git.
    - The input is the run's changes other than refused ones, with the skill's files as their directory.
    - Each untracked path becomes one line in one marked block: `# droidyard init: files it wrote that git does not track (droidyard init --uninstall removes this block)` … `# end droidyard init`. In `<commonDir>/info/exclude` (`--exclude`) the lines are anchored at the top, so they are prefixed when the root lies below it. In `<root>/.gitignore` (`--gitignore`) they are anchored at the root.
    - An existing block is replaced by the current set, and nothing outside it is touched.
    - An uninstall removes the block from both files, and deletes a file that held nothing else. Outside a repository, or with no git, the uninstall's block part is a no-op, never a refusal.
    - A dry run writes nothing. An install with a flag outside a repository is refused: "not a git repository: nothing to ignore".
- Implements: ADR-0124 Q4.
- Check:
  - `TestIgnoreExcludeReachesEveryWorktree`: a real temporary repository and a linked worktree, with init's files present in both and the block written from the worktree's run. `git status --porcelain` lists none of the paths in either tree. Seen failing with the block written to the literal `<gitdir>/worktrees/<name>/info/exclude`.
  - `TestIgnoreLeavesTrackedPathsOut`: a committed `.mcp.json` gets no line and is returned as tracked.
  - `TestIgnoreRerunAddsAndKeepsTheUsersLines`.
  - `TestIgnoreUninstallRemovesOnlyTheBlock`: the user's lines are byte-identical, and a `.gitignore` that held only the block is deleted.
  - `TestIgnoreRootBelowTheTop`: for both files, `git status` is clean of the paths. Seen failing with `.gitignore`'s lines prefixed.
  - `TestTrackedRunsThroughTheRunner` (`internal/domain/git`): an untrusted repository with a filter driver gets the overrides, as `run_test.go`'s driver tests check.
- Schema: no change.
- Needs: ADR-0124; `git` on PATH. In parallel with 2, 4.
- Not here: the flags (step 6); any ignore at user level.
- Rests on: —
- Review: the main session's diff review.
- [x] Done. Evidence: `git.Tracked` (`internal/domain/git/tracked.go`, through the runner, untrusted) and `assistant.Ignore` (`internal/assistant/ignore.go`, one call per target file, `IgnoreCandidates` for the paths to ask git). `TestIgnoreExcludeReachesEveryWorktree` seen failing with the block in `<commondir>/worktrees/<name>/info/exclude`; `TestIgnoreRootBelowTheTop` seen failing with `.gitignore` lines prefixed; `TestTrackedRunsThroughTheRunner` seen failing with `trusted=true`; `TestIgnoreLeavesTrackedPathsOut`, `TestIgnoreRerunAddsAndKeepsTheUsersLines`, `TestIgnoreUninstallRemovesOnlyTheBlock` pass (no reversal run). `go test ./...` passed but for the doccheck rows the plan commit fixed. A user file with no final newline gains one that an uninstall keeps.

### 6. `droidyard init --user`, `--exclude`, `--gitignore`

- Builds: `cmd/droidyard/init.go`, `commandhelp.go`.
  - **The flags.** `--user` excludes `--root`, `--exclude` and `--gitignore`, and `--exclude` excludes `--gitignore`. Each such pair exits 2 and writes nothing.
  - **`--user`** needs no project. The directories come from the environment:
    - `CLAUDE_CONFIG_DIR`, else `~/.claude`;
    - `.claude.json` where P1 found it;
    - `~/.cursor`;
    - `XDG_CONFIG_HOME`, else `~/.config`, for `opencode`;
    - `~/.agents`.
  - **The real `ClaudeMCP`** runs `claude mcp add-json --scope user droidyard <entry>` and `claude mcp remove --scope user droidyard` with the `claude` on PATH. With none, the runner is nil.
  - **The output** gives the changes, the hook lines and the allowlist, as today, plus P3's note if it applies. Then the report of a project that takes precedence: when cwd lies in a project (`projectOf`) whose files hold a droidyard entry for a client chosen, one line per client names the file, says its entries take precedence there, and gives the `init --uninstall --client <c>` that removes them.
  - **Per project**, `--exclude` or `--gitignore` follows `assistant.Run` with `git.Tracked` and `assistant.Ignore`. The output prints the block's file and lines, and "tracked, not ignored: <path>" for each tracked file. `--uninstall` always removes the block.
  - `initHelp`, `initUsage`.
- Implements: ADR-0124 Q1, Q4; ADR-0037 §5 (no daemon).
- Check: `init_test.go`, with fresh `HOME`, `CLAUDE_CONFIG_DIR` and `XDG_CONFIG_HOME`, and a fake `claude` on PATH that appends its argv to a file:
  - `TestInitUserWritesTheUserFiles`: the fake's argv is exactly `mcp add-json --scope user droidyard {…}`, with no `--root`. Nothing is written under cwd's project, and no daemon starts.
  - `TestInitUserUninstallLeavesHomeAsItWas`: the tree's hash is the same before the install and after the uninstall, and the fake's argv is `mcp remove --scope user droidyard`.
  - `TestInitUserReportsAProjectThatTakesPrecedence`.
  - `TestInitUserWithoutClaudePrintsTheCommand`.
  - `TestInitFlagCombinations`.
  - `TestInitExcludeHidesInitsFilesFromGit`: `init --exclude` run in a repository and in a `git worktree add` of it. `git status --porcelain` lists none of init's paths in either. A tracked `.mcp.json` is reported. `--uninstall` removes the block.
  - `TestInitGitignore`; `TestInitUninstallOutsideARepository` (exit 0).
  - The existing init tests pass.
- Schema: no change.
- Needs: steps 4, 5.
- Not here: level 5 (step 7).
- Rests on: —
- Review: the main session's diff review.
- [ ] Done. Evidence:

### 7. Level 5, the documents, the close

- Builds: no product code.
  - **(a) Level 5** (the main session, K1–K3, with the lane binary):
    - **Setup.** A scratch git repository is made from a copy of `samples/golden`, with `git worktree add ../wt`; neither tree holds a per-project file. Run `droidyard init --user --client all`.
    - **Claude from `wt`.** `claude mcp list` shows the server connected. With P0, one `claude -p` shows the tools listed, `workspace_describe` naming `wt` with `mainRoot` the main tree, an `adb version` Bash call recorded in `wt`'s Activity, and an `rm` in `wt` denied with the trash.
    - **Outside a project, with no daemon running.** `claude mcp list` (and `-p` with P0) shows how the server with no tools looks, that no daemon starts and that nothing goes to stderr. **Cost:** the median wall time of 20 rootless `droidyard hook` runs per event in a non-project folder with over 1,000 subfolders, against a 100 ms bound; past it, a register row.
    - **Other clients.** cursor-agent `mcp list` from `wt`, and the hook's record if P3 fired. OpenCode `opencode mcp list` from `wt`, if it runs isolated.
    - **Per project.** In a second scratch repository, `init --client claude --exclude` there and in a worktree of it: `git status` is clean of init's files in both, and `--uninstall` removes the block.
    - **Uninstall.** `init --uninstall --user --client all`. The files and keys init wrote are compared with their seed: `settings.json`, `hooks.json`, `cli-config.json`, `opencode.json`, the skills, and `mcpServers.droidyard` in `.claude.json`. The clients' own state is not compared.
  - **(b) Documents.**
    - `mcp.md` §Transport and session: the binding without `--root` is the project of the folder, or a session with no tools, and `--from` is named.
    - `mcp.md` §Features beyond tools: the Roots sentence ("from `--root` or the cwd") gains `--from` and the no-project case, and `init` writes per project or once per user (ADR-0124).
    - `docs/README.md`'s status line and install section (the main session's).
    - The register:
      - row 288 is closed `3a`;
      - the Cursor IDE: its server cwd, `--from ${workspaceFolder}` in a global entry, one name at both levels, and its user hooks (unverified, `trial`);
      - a user hook whose binary is gone fails in every project until `init --uninstall --user` (a limit; `Work`, the 325 idea);
      - an older Claude Code that discards a `settings.json` holding an event it does not know (row 319) would discard the user's whole settings (a limit);
      - per-project entries still name the main tree with `--root` (a limit);
      - a chat started outside a project, or in a subfolder of a top-level project with no settings file above it (ADR-0075's layout, `top/docs`), has no droidyard for its life (a limit);
      - whatever steps 1 and 7 left unverified.
    - The ADRs' `Superseded in part by` lines are added at ADR-0124's commit.
    - The live corpus stays within its budget.
- Implements: A1–A6 at level 5.
- Check: each run's decisive output under the lane's evidence directory, quoted in the evidence line.
- Schema: no change.
- Needs: steps 1–6.
- Not here: the Cursor IDE; the owner's own configuration and repositories.
- Rests on: —
- Review: the Opus verifier at the close, over steps 2–6 (one verifier at the close, the owner's pace), effort `high`.
- [ ] Done. Evidence:

**Run rules (steps 1 and 7, the main session only).**

- **K1.** `HOME`, `CLAUDE_CONFIG_DIR`, `XDG_CONFIG_HOME` and `DROIDYARD_STATE_DIR` are fresh, under `/tmp/dy3a.*`, with the daemon from the `verify-daemon` recipe. The owner's `~/.claude`, `~/.claude.json`, `~/.cursor`, `~/.config/opencode` and `~/.agents` are hashed before and after each run and must be unchanged. No credential is copied.
- **K2.** Only scratch repositories made from `samples/golden`; never android-kids-app or DopamineControl, not even read.
- **K3.** No adb server, emulator or device (`adb version` starts no server). Nothing is killed by name.

## Not in this milestone

- **Per-project entries without `--root`** (a committed or copied `.mcp.json` that works in a worktree): Cursor's server cwd is unknown, which was plan m1l C4's reason for `--root`. The user install is the way to reach worktrees. A row at step 7.
- **Copying files into worktrees, `.worktreeinclude`:** the per-project files name the main tree, and `.worktreeinclude` covers only the worktrees Claude itself creates [docs]. The Orca idea 7 row stays.
- **Device tools outside a project:** Q2.
- **Row 325:** it changes the entry shape ADR-0090 recognises. A new row at step 7.
- **Row 270:** user entries carry the state root as ADR-0061 §3 has project ones do.
- **OpenCode hooks:** ADR-0061 §Alternatives considered.
- **Finding the project from the nearest `.git` top** (for `top/docs` in ADR-0075's layout): a new rule, not needed for the owner's request. A row at step 7.
- **The Sessions panel; Git's submodules:** other milestones (the brief).

## Design questions

Each one is Delegated: the main session applies the recommendation and writes ADR-0124 after step 1, with step 1's findings. The steps follow the drafts. If one goes otherwise, the steps that rest on it are redrafted first.

### Q1. What `init --user` writes, and how

- Blocks: steps 4, 6
- The documents say:
  - ADR-0037 §2 has `init` run "in a project", and ADR-0061 §5 "never a user-level file".
  - ADR-0061 §Alternatives considered rejects user-level hook files: "they act in every project, and the house rule forbids them".
  - ADR-0038 §Alternatives considered records user level as rejected by the owner on 2026-10-09. It named two conditions: "an empty-session mode outside a project and per-client evidence to stay cheap".
  - The owner reversed both on 2026-10-22. Q2 and Q3 meet the two conditions, and acting in every project is the point of the install.
- Draft decision: ADR-0124, superseding in part ADR-0037 §Decision 2, ADR-0038 §Decision and ADR-0061 §Decision (§5), and extending ADR-0090 §1. The flag is `--user`, Claude's own scope word.

  | Client | MCP entry | Hooks | Allowlist | Skill |
  | --- | --- | --- | --- | --- |
  | claude | `<bin> mcp`, by `claude mcp add-json --scope user` | `<claude>/settings.json` | `<claude>/settings.json` | `<claude>/skills/` |
  | cursor | `~/.cursor/mcp.json`: `<bin> mcp --from ${workspaceFolder}` | `~/.cursor/hooks.json` | `~/.cursor/cli-config.json` | `~/.agents/skills/` |
  | opencode | `<xdg>/opencode/opencode.json`: `<bin> mcp` | none | none | `~/.agents/skills/` |

  - No entry names a root.
  - Claude's entry goes through Claude's own CLI. `.claude.json` is Claude's live state, rewritten by every running session, and over 1 MiB is ordinary.
  - ADR-0037's merge, keep, refuse and uninstall rules apply to every other file. If P3 shows cursor-agent rewriting `cli-config.json`, init prints "close cursor-agent first".
  - In a user file, the rootless hook shape is init's.
  - A project's own entries take precedence (Claude Code: whole-entry precedence [docs]). `init --user` reports them for the project it runs in.
  - Consequence: the user `settings.json` carries `PostToolUseFailure`. An older Claude Code that discards a file holding an event it does not know (row 319) would lose the user's whole settings, not one project's. A register row.

  Rejected:
  - **Editing `.claude.json` directly:** it races the running sessions and exceeds the read bound.
  - **A Claude Code plugin:** ADR-0037 §Alternatives considered, the coupling ADR-0004 avoids.
  - **Copying the per-project files into worktrees:** they name the main tree.
  - **`--global`:** Cursor's word, but not Claude's or OpenCode's.

### Q2. `droidyard mcp` without `--root`, and outside a project

- Blocks: step 2
- The documents say:
  - ADR-0019 §Processes 2 gives the shim "no MCP logic" and §4 auto-start; §Sessions falls back to "else its cwd".
  - ADR-0075 §6 keeps that fallback for the MCP binding.
  - The daemon already serves a session with no root (`mcp.go:585`).
- Draft decision: ADR-0124, superseding in part ADR-0019 §Processes (2, 4) and §Sessions, and ADR-0075 §Decision (§6).
  - Without `--root`, the project is `projectOf` of `--from` or the cwd. An unexpanded `${…}`, or an empty value, in `--from` is ignored. `droidyard call` and `trash` keep `resolveRoot`'s cwd fallback, as ADR-0075 §6 already distinguishes them. `--root` keeps today's meaning, since per-project entries need it.
  - With no project, the shim answers a session with no tools through mcp-go itself: tools capability declared, an empty list. It never dials and never starts a daemon.
  - This is deterministic: the same session with or without a daemon. It is free: no daemon, no workspace, no recent project, and no tool tokens in a chat that is not about Android.
  - `--from` exists because a value the client expands is not a root the user chose. As `--root`, an expanded `${workspaceFolder}` would bind and open every Cursor window's folder, Android or not.
  - The costs:
    - a chat started outside a project, or in `top/docs` of ADR-0075's layout, has no droidyard for its life (one session, one project, ADR-0019);
    - `mcp` without `--root` in a folder that is no project no longer binds that folder.

  Rejected:
  - **Exiting at once:** every chat outside an Android project would show a failed server.
  - **Attaching to a running daemon unbound:** the device tools would be there, but the tool list would depend on whether a daemon runs, and every chat would pay its tokens.
  - **Auto-starting unbound:** a resident daemon for every chat on the machine.
  - **Answering the handshake from the binary's own registry and starting the daemon at the first call:** MCP logic twice, a handshake replayed into the daemon's session mid-stream, and the tokens of every chat all the same.
  - **Using `--root ${workspaceFolder}` for Cursor:** see above.

### Q3. The hook without `--root`

- Blocks: step 3
- The documents say: ADR-0061 §1 requires `--root`, and ADR-0085 §1–2 places a command against it. ADR-0085 §Alternatives considered rejects "opening whatever project a command ran in".
- Draft decision: ADR-0124, superseding in part ADR-0061 §1 and ADR-0085 §1–2.
  - The root is `projectOf` of the directory the client names as its project: Claude's `CLAUDE_PROJECT_DIR`; Cursor's `workspace_roots[0]`, else `CURSOR_PROJECT_DIR`; else the event's `cwd`. ADR-0085 and ADR-0095 §5 then apply with it as `--root`.
  - With no project, every event passes with nothing dialled.
  - When the client's project directory (the one P2 shows the client reads its settings from) holds init's own per-project hook for that client, the user hook passes, so a project set up both ways records once.
  - Records thus go to the session's project, and a `cd` into another project does not open it.

  Rejected:
  - **The event's `cwd` alone:** an agent that `cd`s into another Android project, or into `build-logic`'s own settings file, would open it.
  - **`init --user` refusing a project with its own entries:** it cannot see every project.
  - **Both hooks recording, with the daemon deduplicating:** a rule in the core about a client's configuration.

### Q4. `--exclude` and `--gitignore`

- Blocks: steps 5, 6
- The documents say: nothing. ADR-0090 §Alternatives considered rejected a manifest of what `init` wrote.
- Draft decision: ADR-0124.
  - Both flags are per project only.
  - The run's untracked paths become lines in one marked block, in the common directory's `info/exclude` (every worktree reads it; anchored at the top) or in the root's `.gitignore` (anchored at the root).
  - Tracked paths are reported, never listed: a line has no effect on them and would say something false.
  - A rerun replaces the block. An uninstall removes it wherever it is, and deletes a file it leaves empty.
  - Git is read only through `internal/domain/git`'s runner, untrusted.

  Rejected:
  - **Lines without a block:** an uninstall could not tell init's lines from the user's.
  - **An `exec.Command("git")` of `init`'s own:** a second invocation without the runner's hardening, in a repository whose configuration may name commands.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| The Cursor IDE: its server cwd, its expansion of `${workspaceFolder}` in a global entry, one name at both levels, and whether it fires `~/.cursor/hooks.json` | 2, 3, 4 | the owner's trial (a register row at step 7) |
| `claude mcp add-json --scope user` writes `.claude.json` where step 6 reads it, with `CLAUDE_CONFIG_DIR` and under `CLAUDECODE=1` | 4, 6 | step 1, P1 |
| Claude Code's hooks carry `CLAUDE_PROJECT_DIR`, merge across scopes, and read project settings from that directory | 3 | step 1, P2 (needs P0) |
| cursor-agent fires user hooks with `workspace_roots`, and leaves `cli-config.json` as merged | 3, 4 | step 1, P3 |
| A client shows a server with no tools without a failure | 2 | step 1, P4 |
| A real session in a worktree is bound there and records there (A2's session half) | 2, 3 | step 7 with P0; else the owner's trial |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | [design][contract] B1 (blocking): Cursor's `--root ${workspaceFolder}`, once expanded, is an explicit root, so every Cursor window would auto-start and open its folder, breaking A3; the plan also contradicted "no `--root` in any entry" | The new `mcp --from <dir>` (Q2): the folder the project is found from, with no project giving the session with no tools; `--root` unchanged for per-project entries; `TestMCPFrom` seen failing with `--from` handled as `--root` |
| 1 | revise | [contract] B2 (blocking): ADR-0075 §6 keeps cwd as the MCP binding's fallback, and was not superseded | Added to ADR-0124's supersede list (header, Q2); `mcp.md` §Transport and session joins step 7(b) |
| 1 | revise | [test] B3 (blocking): no step 3 check failed if the client's project directory were replaced by `cwd`; the outside-a-project mutation was incoherent | `TestRootlessHookFollowsTheClientsProjectNotCwd` (sources disagree; seen failing with `cwd`); the mutation restated as `resolveRoot`'s fallback; `workspace_roots` unread as Cursor's mutation |
| 1 | revise | N1 `cli-config.json` live state; N6 P0 a gate with no fallback; N7 the no-tools display left to step 7; N13 the yield's file | P3 hashes `cli-config.json`, and Q1 notes the outcome; P0 runs first, with the owner asked for a dedicated token or A2's session half at level 4 (A2, Unverified); P4, a stub server at user scope; P2 also launches from a subfolder |
| 1 | revise | N2 the worktree half of `--exclude` vacuous; N3 `.gitignore` anchoring; N8 no tools capability means METHOD_NOT_FOUND | The files are present in both trees, with the literal per-worktree path as the mutation; `.gitignore` lines anchored at the root, tested for both files; the capability is declared with an empty list, and stderr is asserted empty |
| 1 | revise | N4 `top/docs` in ADR-0075's layout; N5 no cost evidence; N10 a false rejection reason; N11 the home comparison; N12 a user `settings.json` and row 319 | A cost in Q2, a register row and §Not in this milestone; a 100 ms median at step 7; the reason restated; the comparison limited to init's files and keys; a consequence in Q1 and a register row |
| 2 | revise | B1, B2, B3 resolved. New [test][order] (blocking): step 2 removes the cwd fallback, but about 40 shim tests start from `cmd/droidyard`, which is no project; `TestMCPSessionBindsAndOpensItsRoot` case (c) and `autostart_test.go`'s two tests assert the old rule, and the plan said they pass unchanged | Step 2: `startShimIn` defaults to a fresh project; case (c) becomes the no-tools assertion; the autostart tests run from a project; `toollist_bytes_test` is A6's proof. Answered in the plan without a third round (`milestone-plan` §Writing one, 7) |
| 2 | revise | Nits: `mcp.md`'s Roots sentence; an empty or relative `--from`; `call` and `trash` keep the fallback | Step 7(b) names the sentence; empty is treated as unexpanded, and relative is resolved against the cwd (`TestMCPFrom`); Q2 says `call` and `trash` keep the fallback |
| 1 | revise | N9 the steps were not disjoint; nits: `deny` garbled, A1's stale MCP entry, the "house rule" alternative, uninstall outside git, size | Step 3 after 2 and 4, the shape function in step 4; `deny` per P3; A1 splits hooks (replaced) from MCP (kept); Q1 cites and answers ADR-0061's alternative; the block's uninstall is a no-op outside git; trimmed |

## Changes

- 2026-10-23, step 1: P0 failed for both clients, so P2 and P3's hook runs were not run. Q3 keeps `CLAUDE_PROJECT_DIR` and the yield on the clients' documents, labelled Unverified in ADR-0124; Q1 keeps the conservative `cli-config.json` branch (merge, say to close cursor-agent first), the evidence being inconclusive. P1 and P4 left Q1 and Q2 as drafted. Claude's `mcp list` and `mcp get` start every user server as a health check, which Q2's no-project session makes free.
