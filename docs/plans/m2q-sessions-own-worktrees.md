# Plan: Milestone 2q — C2, sessions that own worktrees

- Status: Approved
- Approved: 2026-10-18. Reviewed in two rounds by `plan-reviewer`, round 2 `approve` (Review). The owner decided Q7 (out of 2q: a small milestone after 2p closes) and Q2 §8 (owning is not exclusive). The main session decided Q1–Q6 as drafted, Delegated, into ADR-0081 to ADR-0086.
- Date: 2026-10-18
- Milestone: "2q — C2, sessions that own worktrees" (owner, 2026-10-17; `docs/vision/beyond.md` §The capabilities, C2, refined by the owner's seven decisions of 2026-10-17, quoted in the brief and listed under Scope).
- Phase: none. The MVP is done (`docs/roadmap/mvp.md`); the owner's brief and the four register rows below are the requirement.
- Contract: ADR-0081 to ADR-0086 (Q1 to Q6); ADR-0088 (Changes, 2026-10-18). Also ADR-0010, ADR-0015 §5–6, ADR-0019 §Sessions, ADR-0020 §Scope and workspace addressing, ADR-0021 §Layout, ADR-0031 §1, §4, ADR-0036, ADR-0045 §3, ADR-0047, ADR-0048, ADR-0060 §2, ADR-0061 to ADR-0063, ADR-0066, ADR-0075; `workspace-state.md`, `interfaces/mcp.md` §Transport and session, §Server instructions.
- Lane: `2q`, branch `m2q`, touches `devices,contract,web,process,git`. 2p (C3, `devices,contract,web`) is in flight: the second to close regenerates the schema and bumps the API once more (`work-board` §Touches).
- The main session's commit of this plan sets `Work` to `2q` on the three rows of Scope marked yes; the device row keeps `C2` (Q7).

**The owner should see:**
- An agent's chat that does development makes or reuses a git worktree with its own git (or its client's worktree feature), then calls one new tool, `workspace_enter`, with the worktree's path. From then on its builds and tools act on that worktree. Droidyard never creates, moves or deletes a worktree, and the UI never moves a session (Q2).
- A worktree of a trusted project is trusted, never asked again. Droidyard proves it is one by git's own files inside the trusted repository, which name the worktree back; a `.git` file planted elsewhere proves nothing (Q1).
- The sessions panel lists each project, its connected sessions (also before they run anything) and the worktree each is in; a worktree is never a project row of its own. The Git panel's Worktrees section names the sessions in each (Q3).
- `droidyard` with a Droidyard tab already open switches that tab and opens no other. The tab is not brought to the front: no page can raise itself; and after the daemon restarts, the old tab cannot reconnect, so a second one opens (Q4). A recent project can be removed from the panel (Q6).
- What a chat is to Droidyard is one MCP connection. Step 1 checks that Claude Code and cursor-agent open one per chat; a client that shares one connection among several chats (the Cursor app, perhaps) makes those chats one session that moves together (Q2, Unverified).
- The per-session device reservation is out of 2q (Q7, owner, 2026-10-18): a small milestone after 2p closes.

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| Owner 1: a session is a chat; development acquires a worktree, reusing one when the task exists; a context per worktree | yes | steps 1, 4, 5; Q2 (the agent's workflow decides and creates; Droidyard binds and keeps the worktree's workspace state) |
| Owner 2: the UI never moves a session; it follows and shows who works where | yes | steps 4 (no parameter names another session; refused off MCP), 6, 8 |
| Owner 3: a worktree is another root of the same project, one daemon | yes | steps 3, 4; Q2's reading |
| Owner 4: trust is the project's, proven through git, against a crafted `.git` file | yes | step 2, Opus verifier; Q1 |
| Owner 5: one tab in all; the panel split by project, with sessions and their worktrees | yes | steps 8, 9; Q3, Q4 |
| Owner 6: the Git side panel shows worktrees by session, read-only | yes | step 8 (the Worktrees section exists since 2n; it gains the sessions) |
| Owner 7: several emulators at once; this lane's own serial only | rule | Run rules K2; no step needs a device unless Q7 brings the reservation in |
| Row "Devices belong to no workspace … a per-session device reservation is proposed for the owner" (device, C2) | no | Q7 (owner, 2026-10-18): a small milestone after 2p closes; the row keeps `Work` `C2` |
| Row "A command an agent runs in another project is recorded in Activity under the hook's root" (MCP/agents, C2) | yes | step 7; Q5. Cursor in part, if step 1 confirms it carries no `cwd` |
| Row "A connected agent that has run nothing since the tab loaded is not listed" (web UI, C2) | yes | steps 6, 8; Q3 |
| Row "Each `droidyard` opens a new tab …; a recent project cannot be removed from the projects panel" (web UI) | yes | step 9; Q4, Q6 |
| The other C2 rows (an agent's own edits outside the root; more assistant hooks; Activity's Restore and the You/Agents split of CLI deletes) | no | the brief |
| A daemon-hosted terminal with the agents' chats and subagents | no | its own milestone and ADR (the brief; register row "Agent sessions: a terminal …") |
| C1, non-Android projects | no | the brief |
| Linux | no | macOS only, as every milestone |

## Starting point

`m2q` at `ada41ad` (main `118a38d` plus the work-board change); API `0.26.0` (`internal/domain/system/system.go`); ADRs up to 0075.

- **Trust** is one function: `internal/policy/policy.go` `Trusted(root)` (`trustedBy`: an exact `trust.roots` entry or a `trust.prefixes` entry at a component boundary, canonicalised). Every trust decision goes through it: `Authorize` (the Gradle capabilities, the git writes, `shell.exec`), `internal/workspace/workspace.go` `trusted` (`open`, `describe`, `workspace.watch` rows), `internal/domain/git/git.go` `trusted` (filter neutralising on an untrusted root). `ShellAllowed` matches `shell.roots` with the same `trustedBy`.
- **Git worktrees.** Nothing reads git's administrative files; `git.refs` already lists the repository's worktrees (`internal/domain/git/refs.go` `RefsWorktree`, from `worktree list --porcelain -z`), and the Git side panel shows them read-only (`web/src/lib/git/refs.ts`, `RefsPanel.svelte`; a click reveals the commit).
- **Sessions.** `internal/daemon/daemon.go` holds connections; a hello carries `client`, `root`, `cwd` (`hello.go`). The MCP adapter (`adapters/mcp/mcp.go`) keeps the bound root in its per-connection `session.root`, fixed for the connection's life; `request()` fills a workspace-scoped call's `workspace` from it, and `origin()` gives the core `capability.Origin{ID, Name, Root}`, from which `Operations.sessionRef` derives ADR-0048's `session.workspace`. No registry of sessions exists in the core; the panel's agent rows come from operations (`web/src/lib/projects/sessions.ts`).
- **Workspaces.** `workspace.Info` (`id, name, root, state, trusted, createdAt, degradedBy, gradle`), `Listed` and `StateEvent` have no repository field; the index `workspaces.json` holds `root, name, lastOpenedAt` per id (`indexEntry`). A worktree opens today as an unrelated project.
- **The hook.** `cmd/droidyard/hook.go` records an observed command (`recordCommands`) and routes an `rm` (`assistant.RouteRm`) against `--root`, the root `init` wrote. `assistant.DecodeAfterEvent` reads no `cwd`; `DecodeHookEvent` reads it, and Cursor's `beforeShellExecution` carries `""` [obs, m2j step 6].
- **The tab.** `cmd/droidyard/client.go` `ui` opens a project, then always calls `openURL` (a new tab). `workspace.watch` subscriptions are the tabs' (`internal/workspace/watch.go`, `Bus.Broadcast`).
- **The tool list** is 28,059 B of `maxToolListBytes` 29,000 (`TestToolListModelFacingBytes`, run 2026-10-18).
- **Baseline, 2026-10-18 at `ada41ad`:** `go vet ./...` clean; `go test ./...` ok in every package. This file fails `internal/doccheck` `TestPlansIndexMatchesFiles` until the main session adds its row to `docs/plans/README.md`.

## Acceptance

- [ ] A1. A worktree of a trusted repository is trusted with no human act, and builds; a directory whose `.git` file names a trusted repository's worktree data without that data naming it back is not; trusting a worktree trusts neither its main tree nor its siblings. Source: owner 4; Q1. Proven by: steps 2, 3.
- [ ] A2. A session moves into a worktree of its own project only through its own `workspace_enter`, and back to the main tree the same way; another project, a planted `.git`, another session's binding, the UI and the CLI cannot move it. Source: owner 1–3; Q2. Proven by: step 4.
- [ ] A3. After `workspace_enter`, the session's builds, `workspace_describe` and the `session.workspace` of its operations are the worktree's, while a second session on the main tree is unaffected. Source: owner 1, 3; Q2. Proven by: steps 4, 5.
- [ ] A4. A real Claude Code chat given a development task creates or reuses a worktree by its own means, enters it and builds there; a second chat on the same task reuses it. Source: owner 1; brief ("the level-5 check"). Proven by: steps 5, 10.
- [ ] A5. A connected MCP session is listed from its connection on, before any call, with its name once known and its workspace as it moves, and leaves at its end. Source: register row (web UI, C2); Q3. Proven by: steps 6, 8.
- [ ] A6. An agent's observed command in a worktree of its project is recorded under that worktree, one anywhere else under the project root (`dev/`, `docs/`) under the project as today, and one outside the project is not recorded and opens nothing. Source: register row (MCP/agents, C2); Q5. Proven by: step 7.
- [ ] A7. The sessions panel groups by project, lists each project's sessions with the worktree each is in, never lists a worktree as a project, and follows a session that moves; the Git panel's worktree rows name their sessions. Source: owner 2, 5, 6; Q3. Proven by: step 8.
- [ ] A8. `droidyard` with a Droidyard tab open switches it to the project and opens no browser; with none open, it opens one. A closed recent project can be removed, its state kept. Source: register row (web UI); Q4, Q6. Proven by: step 9.
- [ ] A9. The schema diff is the milestone's: one minor version, `mainRoot`, `workspace.enter`, `session.watch`, `workspace.show`, `workspace.forget`, their events and codes; the MCP tool list stays within its budget. Source: `capability-api.md` §Discovery and versioning; ADR-0036. Proven by: steps 3, 4, 6, 9.
- [ ] A10. A level-5 run with two real chats and the UI shows A1–A8 together. Source: brief. Proven by: step 10.

## Run rules (steps 1, 5, 8, 9, 10)

- **K1.** Fixtures only under `B=~/Library/Caches/droidyard-dev/2q` or `t.TempDir()`; an isolated daemon by `verify-daemon` §Launch, `DROIDYARD_CONFIG_DIR` and `DROIDYARD_STATE_DIR` under `/tmp`. No owner repository is named, opened or read. Real clients run with scratch configuration: Claude Code with `--mcp-config <scratch file> --strict-mcp-config`, `--allowedTools` naming `Bash(git:*)`, `Bash(./gradlew:*)`, the edit tools and `mcp__droidyard` (so a run never stalls on a denied command), and the hooks `init --client claude` writes into the scratch repository; cursor-agent by `verify-daemon`'s Cursor recipe (`CURSOR_DATA_DIR` scratch). A recorded hook or wire line that carries the user's email is never kept.
- **K2.** No adb, emulator or device unless Q7 brings the reservation in; then only an emulator this lane starts, on its own serial and an isolated adb port, never 2p's or the owner's, never `adb kill-server`/`start-server`. No process killed by name. Headless Chrome with its own profile over CDP.

## Steps

### 1. Spike: what a chat is to each client, and what reaches the daemon

- Builds: no product code. The main session's run under K1, recorded in `docs/research/spike-c2-sessions-2026-10.md` with `[obs]`/`[doc]`/not observed per fact (the shape of `spike-s1-error-formats.md`). A scratch git repository `$B/repo` (a copy of `samples/golden`, `git init`, one commit), the shim behind `verify-daemon`'s recorder (`tap.sh`), which also writes the names, never the values, of its environment variables.
  - (a) Claude Code, `claude -p`: MCP connections per run; whether a `Task` subagent's droidyard call arrives on the parent's connection, for a plain subagent and for one run with worktree isolation (whose `workspace_enter` would move its parent, Q2 §1); whether `--resume <id>` of the same chat opens a new connection; `initialize`'s `clientInfo` and `capabilities`; any `_meta` on `tools/call`; any `roots/list` exchange.
  - (b) The same for cursor-agent `-p` (and `--resume`, if offered).
  - (c) The hook's input: Claude Code `PostToolUse` and cursor-agent `postToolUse` (`Shell`): whether `cwd` is present, and whether it is the worktree after an earlier command `cd`'d there; the chat id fields (`session_id`, `conversation_id`) and whether they are stable through the chat.
  - (d) Claude Code's own worktree tool in `-p` (offered or not; the path and branch it makes; whether hooks' `cwd` follows it; any `notifications/roots/list_changed`).
- Implements: nothing; it settles the Unverified rows Q2, Q3 and Q5 rest on.
- Check: the note answers (a) to (d), each with the wire or hook line it rests on. **Stop rule:** if (a) or (b) shows one connection carrying two chats in one process, Q2's "a session is one MCP connection" does not hold for that client: the plan stops and Q2 returns to the owner with the observation. If (c) shows no usable `cwd` for Claude Code, step 7 is cut to the rm route's and record's fallback and the row stays open.
- Schema: no change.
- Needs: nothing built; real `claude` and `cursor-agent` (their versions recorded), the host `git`.
- Not here: the Cursor app (no headless run exists; the owner's trial, Unverified); interactive `/clear` in Claude Code (run once by hand if a terminal is available, else Unverified).
- Rests on: —
- [x] Done. Evidence: [`spike-c2-sessions-2026-10.md`](../research/spike-c2-sessions-2026-10.md), runs a1 to a4 (Claude Code 2.1.302) and b1, b2 (cursor-agent 2026.10.01), `$R` `/private/tmp/dy2q.Kgmn`. The stop rule did not fire: one connection per run in both clients, and subagents, including a worktree-isolated one, ride the parent's (a1, a2). (c): Claude Code's `cwd` is physical and follows `cd` (a2); Cursor's is `""` (b1). `/clear` and `EnterWorktree` in `-p` were not observed (Unverified).

### 2. Trust: a linked worktree of a trusted repository is trusted

- Builds:
  - `internal/platform/worktree.go`: `LinkedWorktree(env Env, root string) (main string, ok bool)`, Q1 §2's five tests, no process started, no repository configuration read. Every file is opened with `O_NONBLOCK`, must be a regular file (checked on the open descriptor), and is read to at most 4 KiB; `R/.git` is checked with `Lstat` (a symlink fails); a relative path is resolved against the directory holding the file that names it (git's rule for `.git`, `commondir` and `gitdir`), then canonicalised with `env.Canonical`. Doc comment: the threat (Q1 §3) and why git itself is not run.
  - `internal/policy/policy.go`: `Trusted(root)` is `trustedBy(root)`, else `LinkedWorktree` and `trustedBy(main)`; `ShellAllowed` the same for `shell.roots` (Q1 §4). `UntrustedError` for a linked worktree names the main root in the message and the nextStep (`droidyard trust '<main>'`): the project is what gets trusted. The package doc comment says so.
- Implements: Q1 §1–§5; ADR-0015 §2 (re-checked before each call: nothing cached).
- Check: `TestLinkedWorktree` (`internal/platform`), real repositories made with the host git in `t.TempDir()`:
  - `git worktree add` inside the main tree and outside it → `main` (both);
  - a directory whose `.git` file names a genuine worktree's admin directory → not (seen failing with the back-link test removed);
  - `.git` naming the common directory itself; naming a submodule's `modules/<x>` (no `commondir`); naming an admin directory of an untrusted repository whose `commondir` names the trusted one → not (seen failing with the `C/worktrees/<name>` parent test removed);
  - `.git` a symlink to a genuine worktree's `.git` file; a FIFO (returns at once); a 5 KiB file; a directory (the main tree itself) → not;
  - a worktree of a bare repository → not; a worktree moved by hand (stale back-link) → not, and after `git worktree repair` → `main`;
  - Q1 §3's residual risk, pinned as behaviour: a worktree directory deleted by hand (no `git worktree remove`), then a new directory made at the same path with a `.git` file naming the old administrative directory → `main`; after `git worktree prune` → not;
  - relative administrative paths (`git -c worktree.useRelativePaths=true worktree add`, git ≥ 2.48) → `main`; on an older git the row is skipped with the version logged, and stays Unverified;
  - each genuine case also listed by `git -C <main> worktree list --porcelain`.
- And `TestWorktreeTrust` (`internal/policy`): `trust.roots [main]` → the worktree trusted (seen failing on `main`); `trust.prefixes` covering main only, worktree outside the prefix → trusted; worktree trusted by path → main and a sibling worktree untrusted; `shell.roots [main]` → `ShellAllowed(worktree)`; the planted directory → untrusted; the error names main.
- Schema: no change.
- Needs: the host git.
- Not here: `mainRoot` in the contract and the UI's Trust (step 3). `droidyard trust <worktree>` still trusts the path it is given (ADR-0031 §4 unchanged).
- Rests on: a root inherits trust only when the trusted repository's own administrative directory names that root back, at level 4 (real git, real files).
- Review: the Opus verifier (the security fence), effort `high`.
- [x] Done. Evidence: `TestLinkedWorktree` and `TestWorktreeTrust` pass on host git 2.51.2. The git-written relative-paths row ran; nothing was skipped. Seen failing with each of these removed in turn: the back-link, the `C/worktrees/<name>` parent test, `Base(C) == ".git"`, the `Lstat`, the 4 KiB bound, `O_NONBLOCK`, the worktree clause in `Trusted` and in `ShellAllowed`, and the worktree error (implementer). `Base(C)` was also caught by the verifier's mutation m1, and the `Trusted` clause by m3. `M/.git`-is-a-directory is implied by a canonical `C` named `.git`, so no reversal can fail it. Opus verifier: proceed, no blocking finding. Fix round by the main session: the git domain's two filter-driver refusals take `policy.UntrustedNextStep`, which names the main tree for a worktree ([contract], ADR-0081 §5); the back-link compares with `R/.git` as `Lstat` saw it, not re-canonicalised ([safety], a same-uid race); reads use `O_NOFOLLOW` ([nit]). After the fix, `go test` of `./internal/platform`, `./internal/policy` and `./internal/domain/...` is ok. Level 4.

### 3. A worktree is a workspace of its project: `mainRoot`, and the first end to end

- Builds:
  - `internal/workspace`: `Info`, `Listed` and `StateEvent` gain `mainRoot` (absolute, canonical; omitted for a root that is not a linked worktree), from `platform.LinkedWorktree` at `open` and `describe` and on each transition published. The index entry gains `mainRoot`, written at every open; `workspace.watch` leaves out an entry with `mainRoot` whose root no longer exists, so removed worktrees neither show nor take one of the 50 places (Q2 §7). Descriptions on the three types.
  - `system.Version` `0.27.0`, the milestone's one bump; `go test ./internal/schema -update`.
  - `web/src/lib/start/trust.ts` and `Trust.svelte` (load `svelte-core-bestpractices` and `svelte-code-writer` first; the autofixer gate): on a workspace with `mainRoot`, Trust adds `mainRoot` to `trust.roots` and its text says the project is trusted (Q1 §5).
- Implements: Q1 §5, Q2 §6, §7.
- Check:
  - `TestWorktreeOfATrustedProject` (`cmd/droidyard`, a real `serve` and the fake sidecar, as `gradle_root_test.go`): `main` holding `settings.gradle.kts`, `git worktree add ../wt -b task`. Untrusted: `workspace.open` on `wt` → `trusted: false`, `mainRoot` main; `build.tasks` on `wt` → `WORKSPACE_UNTRUSTED` whose nextStep names main, no sidecar started. After `droidyard trust main`: `build.tasks` on `wt` lists the fake's tasks and the fake's log has `opened <wt>` and no `opened <main>` (seen failing before step 2). After `workspace.close`, `workspace.watch` returns `wt`'s row with `mainRoot`; after `rm -rf wt`, no row for it (seen failing with the existence test removed), while a closed plain project whose directory is gone is still listed, as today.
  - Vitest (`trust.test.ts`): Trust on a worktree posts `mainRoot`, and on a plain project its root.
- Schema: `version` `0.27.0`; `mainRoot` on `workspace.open`/`workspace.describe`'s result, `workspace.watch`'s rows and the `workspace.state` payload. Nothing else.
- Needs: step 2; the fake sidecar.
- Not here: grouping in the panel (step 8).
- Rests on: —
- Review: the Opus verifier (contract, trust), effort `high`.
- [ ] Done. Evidence:

### 4. `workspace.enter`: a session moves into a worktree of its project

- Builds:
  - `internal/capability/sessions.go`: `Sessions`, the core's registry of MCP sessions: `Connect(id, project string)`, `Named(id, name)`, `Bound(id, workspaceID, root)`, `End(id)`, `Binding(id) (root string, ok bool)`, under one mutex. `project` is the canonical main root of the hello's root (itself when it is not a worktree).
  - `adapters/mcp/mcp.go`: `Serve` registers the session before `openBound` and ends it after `UnregisterSession`; `openBound` records the binding; `request()` and `origin()` read the root from `Sessions.Binding(c.id)`, so `session.workspace` follows a move (ADR-0048 §1 unchanged).
  - `internal/capability`: a descriptor flag `Binding` ("acts on the calling MCP session's binding", Q2 §3); `Operations.Start` (`prepare`, beside the audience check) refuses a `Binding` capability with `REQUEST_NOT_ALLOWED_HERE` and creates no operation unless the request's `Origin.ID` is a session registered in `Sessions` (`Connect`), with its own message ("only an MCP session can move its binding") and nextStep; `error.go`'s comment on the request codes gains the widened meaning. `internal/schema` `CapabilitySchema` gains `binding`, `omitempty` as `longRunning` is (the `Stateless` statefulness stays, Q2 §3).
  - `internal/workspace/capabilities.go`: `workspace.enter`, Q2 §3 (command, global, stateless, `Binding`, audience all, idempotent, not destructive, not long-running), params `{root}`, result the workspace object; codes `WORKSPACE_NOT_A_WORKTREE` (new, `internal/capability/error.go`, its meaning beside `WORKSPACE_NOT_OPEN`), `WORKSPACE_ROOT_INVALID`, `WORKSPACE_STATE_UNREADABLE` (the handler's own; `REQUEST_NOT_ALLOWED_HERE` is Start's and, as for every human capability, not listed). The handler: `root` absolute, canonicalised; allowed when it is the session's project root, or `LinkedWorktree(root)` returns exactly that project root; then `Registry.Open` (no trust granted), `Sessions.Bound`, the result. The description says it moves the calling session, and that after a resumed or new chat the agent enters again.
  - `adapters/mcp/mcp.go` `Instructions` first line and `CoreTools` (Q2 §5); `docs/interfaces/mcp.md` §Server instructions (verbatim) and §Transport and session; `skills/android/droidyard-android/SKILL.md`, a short "Development in a worktree" section (Q2 §2); `references/tools.md` by `go test ./cmd/droidyard -update` if the skill test generates it.
- Implements: Q2 §1–§5; ADR-0019 §Sessions as Q2 supersedes it; ADR-0015 §5 (entering grants no trust).
- Check: `TestSessionEntersAWorktree` (`cmd/droidyard`, scripted MCP stdio sessions against a real `serve` and the fake sidecar, trusted `main`, worktree `wt`):
  - session A bound at `main`: `workspace_enter {root: wt}` → `wt`'s workspace; `build_tasks` → `opened <wt>`; `workspace_describe` → `wt`; an operation's `session.workspace` is `wt`'s id;
  - session B, bound at `main` meanwhile: `workspace_describe` → `main` (seen failing with the binding kept on the adapter `Server` instead of per session);
  - A: `workspace_enter {root: main}` → `main`;
  - A: a genuine `git worktree add` worktree of another repository `other` (trusted too) → `WORKSPACE_NOT_A_WORKTREE`, binding unchanged (seen failing with the comparison to the session's project root removed, so any genuine worktree is accepted);
  - A: a sibling repository, `/`, a planted `.git` directory → `WORKSPACE_NOT_A_WORKTREE`, binding unchanged;
  - a session bound at `wt` from its hello: `workspace_enter {root: main}` allowed (its project is main);
  - `droidyard call workspace.enter` and the same over the WebSocket → `REQUEST_NOT_ALLOWED_HERE`, and `operation.watch` shows no operation for either (seen failing with the Start check removed: the handler would run, or a failed operation be recorded).
  - `TestToolListModelFacingBytes` within 29,000 B; `TestCoreNextStepsNameCoreTools`; the instructions test against `mcp.md`.
- Schema: one new entry, `workspace.enter`, with its params, result, `errors` `["WORKSPACE_NOT_A_WORKTREE", "WORKSPACE_ROOT_INVALID", "WORKSPACE_STATE_UNREADABLE"]` and `binding: true`; no other entry changes (the flag is omitted when false). `REQUEST_NOT_ALLOWED_HERE`'s widened meaning is `error.go`'s comment only, no schema diff. `version` stays `0.27.0`.
- Needs: steps 2, 3.
- Not here: listing sessions (step 6); creating, choosing or deleting worktrees (Q2 §2: the agent's). A session's binding is not persisted: a reconnected chat enters again.
- Rests on: a session can move only itself, and only within its project, at level 4.
- Review: the Opus verifier (contract, a new code, the fence, a lifetime), effort `high`.
- [ ] Done. Evidence:

### 5. Level 5, early: a real chat acquires a worktree

- Builds: no code. The main session's run under K1: `$B/repo` trusted, `droidyard init --client claude` in it, Claude Code through the recorder.
  - Chat 0, `claude -p`: "Which modules does this project have?" A question: `git worktree list` unchanged, no `workspace_enter` on the wire (owner 1: only development acquires one).
  - Chat 1, `claude -p`: "Development task `fix-greeting`: change the app's greeting text, then build the debug APK." No mention of worktrees: the skill and the instructions must lead the agent there. On the wire: a worktree made by the agent's git or client, `workspace_enter` with its absolute path, then `build_assemble` (or `build_run`) answered for the worktree's Gradle root (the build record's root); `git -C $B/repo status --porcelain` empty.
  - Chat 2, a new `claude -p`: "Continue task `fix-greeting`: check the build." It enters the existing worktree; `git worktree list` gains no entry.
  - cursor-agent, one chat as chat 1, if step 1 found it workable.
- Implements: A3, A4 in part (the UI comes at step 10).
- Check: the wire lines quoted (the `workspace_enter` request and answer, the build's root), `git worktree list` before chat 0, after it, and after chats 1 and 2. If chat 1 builds on main without entering, that is the finding: the skill's text is revised once and the run repeated, and a second miss goes to the owner (Unverified row).
- Schema: no change.
- Needs: step 4; Claude Code; real Gradle with `samples/golden`'s dependencies cached (as m2o step 7).
- Not here: the UI; a measurement of how often agents follow the rule (Unverified).
- Rests on: an agent told by the skill makes or reuses the worktree and enters it before building, at level 5 (one run per client).
- [ ] Done. Evidence:

### 6. `session.watch`: an agent listed from the moment it connects

- Builds: `session.watch` (Q3 §1) beside `workspace.watch`: streaming, global, audience all, so off MCP by ADR-0010; result `{subscriptionId, sessions: [{id, name?, workspace?, connectedAt}]}`, oldest first, taken after the subscription opened; events on a new ring-less topic `session.state`, type `state`, payload `{id, name?, workspace?, ended}`, published by `Sessions` on connect, on the name from `initialize` (bounded by `capability.SessionName`), on every binding, and on the end, in that order per session, offered to every `session.watch` subscription (ADR-0045 §3's pattern; `Bus.Broadcast`). The MCP adapter calls `Sessions.Named` after `initialize`. CLI and WebSocket connections are not sessions here.
- Implements: Q3 §1, §2.
- Check: `TestSessionWatch` (`cmd/droidyard`, a WebSocket client and MCP shims): subscribe; a shim connects and sends nothing → a row-less `state` event `{id}` and, in a second `session.watch`, a row with no name (seen failing with `Connect` not publishing); `initialize` → `{id, name}`; `workspace_enter` → `{id, workspace: <wt id>}`; the shim killed → `{id, ended: true}` and no row in a new `session.watch`; a seam between the subscription and the snapshot (as `workspace.watch`'s `SetWatchHook`) shows a connect in between is not lost. `TestSessionWatchOffMCP`: not in `tools/list`.
- Schema: `session.watch` (params none, its result); `session.state` where the event topics are published. `version` stays `0.27.0`.
- Needs: step 4.
- Not here: the panel (step 8); `droidyard call` and the hook as sessions (one-shot connections, not chats).
- Rests on: no event of a session is published before its row can be read, and none after its end, at level 4 (the seam test).
- Review: the Opus verifier (a capability, concurrency), effort `high`.
- [ ] Done. Evidence:

### 7. The hook records where the command ran

- Builds: `assistant.DecodeAfterEvent` returns `Cwd` (Claude Code; Cursor's field as step 1 found it, else `""`). `cmd/droidyard/hook.go`: `hookRoot(flagRoot, cwd) (root string, ok bool)`, Q5 §1, file reads only: `cwd` and `flagRoot` both canonicalised first (`env.Canonical`: `init` writes `--root` from `os.Getwd`, a logical path, while a client's `cwd` is usually physical); from `cwd` up, the nearest directory holding a `.git` entry; when it is a linked worktree whose main tree is `flagRoot`'s main root (`flagRoot` itself when it is not a worktree), that worktree; else, when `cwd` is `flagRoot` or under it at a component boundary, `flagRoot`; else not ok: the record is dropped (the hint still goes) and the rm route keeps `flagRoot`'s rules (a target outside it passes, as today). `cwd` `""` gives `flagRoot`. Both `recordCommands` and the rm route use it; within ADR-0061's 2 s.
- Implements: Q5 §1, §2.
- Check: `TestHookRecordsInTheWorktree` (`cmd/droidyard`, as `hook_observe_test.go`): a Claude `PostToolUse` running `./gradlew assembleDebug` with `cwd` in `wt` → the record's operation has `wt`'s workspace id (seen failing with `flagRoot`); `cwd` `M/dev` where only `dev/` holds the settings file and `--root M` → recorded under `M` (seen failing with `root.go`'s settings rule in place of the `.git` walk); `cwd` `M/.claude/worktrees/x/app` in a worktree nested in `M` → `x` (seen failing with the `under flagRoot` test first); `--root` given through a symlink and `cwd` physical → recorded under `flagRoot` (seen failing with a lexical comparison); a submodule inside `wt` → recorded under `M` when `wt` is nested in `M`, dropped when it is outside (the walk stops at the submodule's `.git`; pinned as behaviour, a register row at step 10); `cwd` in an unrelated project → no record, and `workspaces.json` unchanged; a Cursor event without `cwd` → `flagRoot`, as today. `TestHookRmInTheWorktree`: `rm a.txt` with `cwd` in a `wt` outside `main` → redirected to `droidyard trash` with `--root <wt>` (seen failing with `flagRoot`: passed through).
- Schema: no change.
- Needs: steps 2, 3; step 1 (c).
- Not here: tying a record to its chat's session (no link from a hook to the MCP connection; step 1 records whether the ids could ever join; register row). The record's fields (ADR-0063 §4) are unchanged.
- Rests on: —
- [ ] Done. Evidence:

### 8. The sessions panel by project, and the Git panel's worktrees by session

- Builds: (load `svelte-core-bestpractices` and `svelte-code-writer` first; the autofixer gate on each `.svelte` file)
  - `web/src/lib/projects/projects.ts`: a row with `mainRoot` nests under the row whose `root` is that `mainRoot`; when none is listed, under a group row for `mainRoot` (its base name, Open opens it). No worktree row at the top level.
  - `web/src/lib/projects/sessions.ts`: agent rows from `session.watch`, so an idle session is a row; several sessions may sit in one worktree (Q2 §8); operations still give running, result and unread. A row sits under its workspace's project and names its worktree (the worktree row's name, or "main"); "You" unchanged. A session that ends leaves at once when idle, else after its last operation as today.
  - `Projects.svelte`: under each project, its sessions, then its worktrees with no session; a click on a session or a worktree selects that workspace (`w`). A selection made from a session row follows that session's moves (Q3 §3).
  - `web/src/lib/git/refs.ts`, `RefsPanel.svelte`: each worktree row names the sessions whose workspace root is its path; no action is added (owner 6).
- Implements: Q3 §2–§4; owner 2, 5, 6.
- Check: Vitest: a main with two worktrees and an orphan worktree group correctly; a session listed before any operation (seen failing with operations-only rows); a `state` event with another workspace re-parents the row and moves a session-made selection, not a project-made one; an ended idle session leaves; a Git worktree row names its sessions. `pnpm -C web run verify`. Level 4 in headless Chrome against an isolated daemon with two scripted MCP sessions, one entering `wt`: the panel shows the project, session A under `wt` and B under main; the Git panel's `wt` row names A.
- Schema: no change.
- Needs: steps 3, 4, 6.
- Not here: the look (the owner's, at the UI review); acting on a worktree from the Git panel.
- Rests on: —
- [ ] Done. Evidence:

### 9. One tab, and a recent project removed

- Builds:
  - `workspace.show` (Q4 §1): command, global, audience human, stateless; params `{workspace?}`; `Bus.Broadcast` returns how many subscriptions it offered; it publishes on `workspace.state` an event of type `show` (workspace id in the frame, empty payload) and returns `{tabs}`.
  - `cmd/droidyard/client.go` `ui`: after opening the project, `workspace.show`; `tabs > 0` → prints "switched the open Droidyard tab to <name>" and does not call `openURL`; `0` or a failed call → opens the browser as today.
  - `workspace.forget` (Q6): command, global, audience human, idempotent; params `{workspace}` (an id); removes the index entry of a workspace that is not open, keeps `ws/<wsid>/`, publishes `workspace.state` type `forgotten`; an open one fails `WORKSPACE_OPEN` (new code); an unknown id answers as done.
  - Web: the projects model handles `show` (select, open if closed) and `forgotten` (drop the row); a closed row's menu has Remove.
- Implements: Q4, Q6.
- Check: `TestUIReusesTheOpenTab` (`cmd/droidyard`, `openURL` replaced, as `ui_test.go`): no WebSocket → `openURL` once; a WebSocket holding `workspace.watch` → no `openURL`, and the WebSocket receives `show` for the project's id (seen failing with today's `ui`). `TestWorkspaceForget`: a closed workspace leaves `workspace.watch` and `workspaces.json`, its `ws/<wsid>/` stays, reopening the root lists its last build again; an open one → `WORKSPACE_OPEN`; over MCP → `REQUEST_NOT_ALLOWED_HERE` (human). Vitest: `show` selects; Remove only on closed rows; `forgotten` drops the row.
- Schema: `workspace.show` and `workspace.forget` with their params, results and errors; event types `show` and `forgotten` on `workspace.state`; `WORKSPACE_OPEN` in `workspace.forget`'s `errors` (its meaning in `error.go`). `version` stays `0.27.0`.
- Needs: steps 3, 8 (the same model).
- Not here: raising the browser window (Q4 §3); a desktop window (later, owner 5).
- Rests on: —
- Review: the Opus verifier (contract), effort default.
- [ ] Done. Evidence:

### 10. Level 5, the contract meeting with 2p, and the close

- Builds: no code but what the run finds. The main session's run under K1 and K2, an isolated daemon, `$B/repo` trusted, the UI in headless Chrome:
  - (a) two Claude Code chats connect; both rows show before either calls a tool;
  - (b) chat 1 takes a development task: its row moves under the worktree it enters, the selection made from its row follows; the Git panel's worktree row names it; a build in the worktree; `workspace.describe` of the worktree `trusted: true` with no trust step;
  - (c) chat 1's own `./gradlew tasks` in the worktree is recorded in Activity under the worktree;
  - (d) `droidyard ui --no-autostart` twice, the launcher on PATH replaced to record calls: one launcher call, the tab switched (CDP target count 1);
  - (e) a closed recent removed; reopened from `droidyard`, its build history back;
  - (f) a directory with a `.git` file naming the worktree's administrative directory: `trusted: false`.
- Contract: when 2p closes first, merge `main`, regenerate the schema, bump the version once more (`work-board` §Close).
- Documents: `workspace-state.md` §What a workspace is (worktrees, linking Q1's and Q2's ADRs) and §Identity and reuse (trust); `mcp.md` (checked at step 4); `glossary.md` §Session; `overview.md`'s binding sentence; the register (the four rows closed or split, Unverified rows not settled, new rows: one connection for several chats and a subagent's enter moving its parent, a resumed chat bound to main again, records not tied to their session, a project root below its repository's top, one tab per daemon run, live worktrees counting toward the 50 recents, a command in a submodule inside a worktree, nested worktrees in the main's tree); `docs/README.md` §Where the work stands; the plans index.
- Implements: A4, A10.
- Check: each run's decisive lines under `$E`: the two `state` events before any call, the `workspace_enter` answer, the build's root, the recorded command's workspace id, the launcher call count and CDP targets, `trusted` true and false.
- Schema: none beyond the regeneration of the contract meeting.
- Needs: steps 1–9.
- Not here: the Cursor app (the owner's trial).
- Rests on: —
- Review: the Opus verifier at the close.
- [ ] Done. Evidence:

## Not in this milestone

- The per-session device reservation: Q7 (owner: a small milestone after 2p closes).
- Creating, choosing, naming or removing worktrees in Droidyard: Q2 §2 (the agent's git; ADR-0060 §2).
- A session's binding kept across a reconnect or a daemon restart: sessions are ephemeral (ADR-0010); a resumed chat enters again.
- Tying a hook record, or Activity's attribution, to the chat's session: step 7's Not here.
- Worktrees of a bare repository, of a submodule's repository or with `core.worktree` set: not inherited, fail closed (Q1 §2).
- A project root below its repository's top (a project opened at `M/dev`, which ADR-0075 still allows): `LinkedWorktree` needs `<root>/.git`, so `wt/dev` neither inherits trust nor can be entered, and a session bound at `M/dev` enters no worktree. A register row (step 10); the fix maps a root to its repository's top plus the same offset, and needs its own decision.
- One tab across a daemon restart: the HTTP token is new at each launch (`http-websocket.md` §Security baseline), so after `droidyard stop`, an upgrade or a crash the old tab cannot reconnect and the next `droidyard` opens a second one beside it. Q4 §4; a register row (step 10).
- Bringing the browser window to the front, and the desktop window: Q4 §3; owner 5 ("later").
- The other C2 rows, the hosted terminal, C1: the brief.

## Design questions

Q1 to Q6 were decided as drafted by the main session under the owner's standing instruction (Delegated), 2026-10-18, each becoming the ADR named; Q2 §8 and Q7 were decided by the owner the same day.

### Q1. A worktree inherits its repository's trust (ADR-0081)

- Blocks: steps 2–5, 7, 10.
- The documents say: ADR-0015 §5 and ADR-0031 §1 trust a root equal to a `roots` entry or under a `prefixes` entry, by path; nothing on worktrees. The owner (decision 4) wants a worktree trusted as its project is, proven through git and not the path.
- Draft decision, a new ADR. Supersedes: ADR-0015 §Decision (in part: item 5's "a root becomes trusted" gains the worktree clause); ADR-0031 §1 (in part: the trust rule); ADR-0032 §Decision (in part: item 2's `shell.roots` match); ADR-0045 §5 (in part: Trust on a worktree trusts its main tree). Relates to: ADR-0015 §2, §6, ADR-0066 §3, ADR-0075 §3.
  1. A root that is not trusted by path is trusted when it is a linked worktree of a non-bare repository whose main tree is trusted by path. Trust never flows from a worktree to its main tree or to a sibling, and is checked before every call, as now.
  2. Root `R` is a linked worktree of main tree `M` when, canonically: (i) `R/.git` is a regular file (not a link) of at most 4 KiB holding `gitdir: <P>`; (ii) `P/commondir` names the common directory `C`, and `P` is `C/worktrees/<name>`; (iii) `P/gitdir` names `R/.git` (the back-link); (iv) `C` is named `.git`, and `M/.git` is that directory, where `M` is `C`'s parent. A relative path resolves against the directory holding the file that names it. Anything else (a submodule, a bare repository, `core.worktree`, a moved worktree) is not one.
  3. The threat is a directory whose `.git` file names a trusted repository's data: (i) to (iii) need a back-link that only a write inside the trusted repository's `.git` can make, and a process that can write there already runs code in the trusted root (ADR-0015 §6's same-uid boundary). Residual risk, accepted and pinned by a test: a worktree directory deleted by hand leaves its administrative directory naming that free path until `git worktree prune` (or gc, three months by default); a directory later made at exactly that path with a `.git` file naming it inherits trust. The path is one the user chose, and `git worktree remove` leaves no such entry.
  4. The same rule matches `shell.roots` for `shell.exec` (Delegated: the shell's enablement is the project's too; ADR-0032 §2 otherwise unchanged).
  5. The UI's Trust on a worktree trusts its main tree, and `WORKSPACE_UNTRUSTED` on a worktree names the main tree. `droidyard trust <path>` trusts the path given, unchanged.
- Alternatives:
  - Containment by path (a worktree under the trusted main tree): worktrees often live beside the main tree, and any directory planted under a trusted prefix already passes.
  - `git rev-parse --git-common-dir` in the root: runs git, reading the candidate's configuration, before its trust is known; and as far as we know it follows a `.git` file's `gitdir` and `commondir` without the back-link [assumption, not run], so the planted file would pass.
  - A trust act per worktree: the owner's "never asked again".
  - Recording the worktree in `config.json` when a session enters it: an agent's act would write trust (ADR-0015 §6, ADR-0031).

### Q2. A session moves into a worktree of its project; a worktree is a workspace of its project (ADR-0082)

- Blocks: steps 3–5, 8, 10.
- The documents say: ADR-0019 §Sessions binds an MCP session to one root "for its lifetime", deterministically; ADR-0060 §2 keeps off the agent surface what every agent has (git); ADR-0004 leaves the agent loop and its context to the assistant; `beyond.md` §The capabilities (C2) says a worktree is a workspace of its own, and the owner's decision 3 that it is not a project of its own.
- Draft decision, a new ADR. Supersedes: ADR-0019 §Sessions (in part: the binding moves within its project through `workspace.enter`); ADR-0010 §Decision (in part: a stateless capability may act on the calling MCP session's binding, below). Relates to: ADR-0020 §Scope, §Core primitives, ADR-0021 §Layout, ADR-0036, ADR-0048 §1, ADR-0060 §2, ADR-0075.
  1. **A session is one MCP connection**, Droidyard's view of a chat. A client that carries several chats on one connection makes them one session, and a subagent on its parent's connection is part of it: a subagent with worktree isolation that enters its worktree moves its parent too. A resumed chat is a new connection, bound to the main tree again: the tool's description says to enter again. All three are limits with register rows; step 1 checks the clients we run.
  2. **The agent's workflow creates or reuses the worktree**, with its own git or its client's worktree feature, wherever that puts it; the skill names the rule (reuse the worktree whose branch names the task, else create one) and nothing else. Droidyard creates, names and deletes none.
  3. **`workspace.enter {root}`** (command, global, audience all, idempotent): rebinds the calling session to `root`, absolute, when it is the session's project root (the main tree of its hello's root) or a linked worktree of that same main tree by Q1 §2; opens it if closed, granting no trust; returns the workspace. It acts only on the calling session, so no client can move another, and the UI cannot move any. Another root, a genuine worktree of another repository included, fails the new `WORKSPACE_NOT_A_WORKTREE`.
     - **Statefulness.** The binding is ADR-0019's addressing, which the MCP session already holds, not ADR-0010's session-owned state that needs a push channel; so `workspace.enter` stays `stateless` (which keeps it an MCP tool), and its descriptor carries a new flag, `binding`: "acts on the calling MCP session's binding". This widens ADR-0010's "stateless: no session required" by that one flag.
     - **Refusal.** Start refuses a `binding` capability from a connection with no binding (the WebSocket, the CLI, plain HTTP) with `REQUEST_NOT_ALLOWED_HERE`, creating no operation, beside the audience check. The code's meaning widens from "a human-audience capability called over MCP" to "a capability this connection may not call", both Start-time.
  4. The binding lives in the core's session registry, which the MCP adapter reads per call; ADR-0048's `session.workspace` follows it. It ends with the connection and is never persisted.
  5. In the core profile; the instructions' first line says the session can enter a worktree of its project. Within `maxToolListBytes` (ADR-0036): if it does not fit, the step stops and the budget is the owner's.
  6. **A worktree is a workspace**: its own root, id (ADR-0021), Gradle root (ADR-0075), builds, watches and state under `ws/<wsid>/`: that is the "context per worktree" Droidyard keeps; the chat's own context is the assistant's (ADR-0004). It belongs to its project: `workspace.open`, `describe`, `watch` and `workspace.state` carry `mainRoot`; it is never a project of its own in the UI, and its trust is the project's (Q1).
  7. The index entry keeps `mainRoot`, an additive key in `workspaces.json` (ADR-0021 §Layout's version unchanged). `workspace.watch` leaves out a worktree entry whose directory is gone, so removed worktrees neither show nor take one of the 50 places; live ones do count toward the 50 (a register row if it crowds the panel).
  8. **Owning** a worktree, in Droidyard, is being bound to it: not exclusive. Two sessions may be in one worktree (a new chat on the same task joins it), and the panel shows both; builds in it serialise as any workspace's do.
- Alternatives:
  - A capability that creates the worktree (`workspace.acquire {task}` running `git worktree add`): a git write on the agent surface (ADR-0071 keeps the daemon's git writes human-only) for what every agent and client already does.
  - A `droidyard worktree` command: the same, through the shell.
  - Adopting from the hook's `cwd`: no link from a hook process to the MCP connection, and Cursor carries no `cwd`.
  - MCP roots as the binding source: ADR-0019 rejected it; binding must be deterministic and visible.
  - A second MCP server per worktree (`--root <wt>`): a chat would need a new server mid-chat.
  - A `root` argument on every tool: tokens on every call (ADR-0036) and every schema.
  - The worktree as a second root of one workspace (as ADR-0074's submodule roots): every capability would take a root, and two worktrees could not build at once (the queue is per workspace).
  - `workspace.enter` declared `stateful`: ADR-0010 keeps stateful capabilities off MCP, the one consumer that needs it.
  - An exclusive owner per worktree: a second chat on the same task would be refused the worktree its task lives in.

### Q3. Sessions are listed from their connection, and the panel groups by project (ADR-0083)

- Blocks: steps 6, 8.
- The documents say: ADR-0048 §3 lists agents from operations, and names `session.watch` as the alternative that "wins" with agent sessions; ADR-0047 §1 lists `workspace.watch`'s rows with their sessions.
- Draft decision, a new ADR. Supersedes: ADR-0048 §3 (in part: rows also come from `session.watch`); ADR-0047 §1 (in part: worktrees nest under their project). Relates to: ADR-0010, ADR-0045 §3, ADR-0050, ADR-0052.
  1. `session.watch` as step 6 says: MCP sessions only; events `state` on ring-less `session.state`; names bounded as ADR-0048 §1; ids grant nothing (ADR-0048 §2).
  2. The panel: per project, its sessions (each naming its worktree, or main), then its worktrees with no session; a worktree whose main tree is not listed sits under a group for that main tree.
  3. The UI follows and never moves: a selection made from a session row follows that session's moves; one made from a project or worktree row stays.
  4. The Git panel's worktree rows name the sessions in each; no action.
- Alternatives: rows from operations only (today); sessions inside `workspace.watch`'s rows (two lifetimes in one stream); a selection that never follows (the owner's "the UI follows"); one that always follows (a user looking at main would be pulled away).

### Q4. One tab: `droidyard` switches an open tab (ADR-0084)

- Blocks: step 9.
- The documents say: ADR-0047 §3 and its Consequences: each `droidyard` opens a tab, because the daemon cannot raise an open page; rejected "a capability to focus an open tab: no page can raise itself".
- Draft decision, a new ADR. Supersedes: ADR-0047 §3 (in part: `droidyard` in a project switches an open tab instead of opening one); ADR-0045 §3 (in part: `workspace.state` gains the event type `show`). Relates to: ADR-0045 §1.
  1. `workspace.show {workspace?}` (human): every tab's `workspace.watch` subscription gets a `show` event and selects the project; the result counts them.
  2. `droidyard`/`ui` opens a browser only when no tab answered.
  3. The window is not raised: still no page can (ADR-0047); activating the browser application from the CLI needs the default browser's identity and is left out (Unverified on what the owner then sees).
  4. One tab per daemon run: a tab from before a restart holds a token the new daemon does not know, so it cannot answer and a second tab opens. A limit with a register row; a token kept across launches is the fix and a security decision of its own (`http-websocket.md` §Security baseline).
- Alternatives: raising the tab (impossible from a page); `osascript` activating a named browser (which one is the user's default is not known to us); a lock file for the tab (new state for what the live subscriptions already say).

### Q5. The hook records where the command ran (ADR-0085)

- Blocks: step 7.
- The documents say: ADR-0063 §3 opens and records under the hook's `--root`; its Consequences name the defect; ADR-0062 routes an `rm` against the same root.
- Draft decision, a new ADR. Supersedes: ADR-0063 §3 (in part: which workspace records), ADR-0062 §Decision (in part: the root of the route). Relates to: ADR-0061, ADR-0063 §4.
  1. The hook's root comes from the command's directory: the nearest enclosing directory holding a `.git` entry, when it is a linked worktree of `--root`'s project (Q1 §2); else `--root` when the directory is inside it (a Gradle build in `dev/`, any subfolder); else none: the record is dropped and the route keeps `--root`'s rules.
  2. A client that carries no directory (Cursor, as observed in 2j) keeps `--root`.
- Alternatives: ADR-0019's settings rule from the directory (a `cd dev` would leave the project, ADR-0075's layout); opening whatever project a command ran in (every directory an agent enters would become a recent); recording under another project already open (one project's agent records in another's Activity, for a second call inside the 2 s); keeping `--root` (today's wrong project).

### Q6. A recent project can be removed (ADR-0086)

- Blocks: step 9.
- The documents say: ADR-0045 §3 lists the index's 50 newest; nothing removes one.
- Draft decision, a new ADR. Supersedes: ADR-0045 §3 (in part: `workspace.state` gains the event type `forgotten`). Relates to: ADR-0021 §Layout, ADR-0054.
  1. `workspace.forget {workspace}` (human, idempotent) removes a closed workspace's index entry and publishes `forgotten`; `ws/<wsid>/` (builds, the trash) stays, so reopening the root restores it. An open one fails the new `WORKSPACE_OPEN`.
- Alternatives: hiding it in the browser (other tabs still list it); deleting `ws/<wsid>/` too (the trash holds the user's deleted files); closing an open one first (a removal that stops running builds).

### Q7. The per-session device reservation (the owner's)

- Blocks: nothing in steps 1–10; it decides whether an eleventh step and a split are needed.
- The documents say: the register row proposes it to the owner; plan m2c records "one session acting on the other's emulator is by design … a device reservation is later" (owner, 2026-10-10); decision 7 of 2026-10-17 makes several emulators at once normal and says nothing of reserving one.
- Decided (owner, 2026-10-18): **not in 2q**. It is device work while 2p is in the device code, it is about device calls rather than worktrees, and 2q is at ten steps. If the owner wants it, a small milestone after 2p, whose draft is: an emulator a session started is held by it; another MCP session's acting calls on that serial fail a new `DEVICE_RESERVED` naming the holder; the UI and the CLI are never refused; the hold ends with the session or the emulator; `device.list` shows `reservedBy`.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| Claude Code and cursor-agent open one MCP connection per chat, a subagent rides its parent's | 4–6, 8, 10 | step 1 (a), (b) |
| A worktree-isolated Claude Code subagent calling `workspace_enter` moves its parent (Q2 §1's limit) | 4, 8 | step 1 (a) |
| The Cursor app opens one connection per chat, not per window | 4, 8 | the owner's trial; else Q2 §1's limit, a register row |
| A Claude Code `/clear` (a new chat in the same process) opens a new connection | 6, 8 | step 1 by hand if a terminal is available; else a register row |
| Claude Code's `PostToolUse` carries the shell's directory as `cwd` | 7 | step 1 (c) |
| An agent told by the skill makes or reuses the worktree and enters it before building | 5, 10 | one run per client at steps 5 and 10; not a measurement |
| Relative administrative paths (`worktree.useRelativePaths`) | 2 | step 2's row on a git ≥ 2.48 |
| `git rev-parse` would accept the planted `.git` file (Q1's alternative) | none | not run; the decision does not use git |
| A tab in the background keeps its WebSocket, so it counts as open | 9 | the owner's use; headless Chrome cannot show it |
| A worktree nested inside the main tree (`.claude/worktrees/`) costs the main's file watch and git status little | 8, 10 | step 10's run notes it; else a register row |
| Linux | all | not run (macOS only) |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | [bug] B1 (blocking): step 7's root from `root.go`'s settings rule drops a command run in `M/dev` (ADR-0075's layout) or any subfolder, which today records under `--root` | Q5 §1 and step 7: the nearest `.git` upward, a linked worktree of the project, else `--root` when inside it, else dropped; checks for `M/dev` and a nested worktree, each seen failing with the old rule |
| 1 | revise | [test] B2 (blocking): step 4's refused cases are all non-worktrees, so accepting any genuine worktree passes | step 4: a genuine worktree of another repository refused, seen failing with the project comparison removed |
| 1 | revise | [contract] B3 (blocking): `workspace.enter` called stateless though it needs a binding; `REQUEST_NOT_ALLOWED_HERE` used as a handler failure with the opposite sense, undeclared | Q2 §3: a `binding` descriptor flag, Supersedes ADR-0010 in part with the reason it stays stateless; refused at Start, no operation, the code's widened meaning; step 4's Builds, Check (no operation recorded) and Schema |
| 1 | revise | [plan] roots below the repository top; subagents and resumed chats; one tab across a daemon restart; worktrees filling the 50 recents; "owns" undefined | Not in this milestone (root below the top, tab across restart) and register rows at step 10; Q2 §1 limits, the tool's description, step 1 (a) runs a worktree-isolated subagent; Q4 §4; Q2 §7 gone worktrees not listed, step 3's check; Q2 §8 |
| 1 | revise | [safety] a stale administrative entry lets a directory recreated at the old path inherit trust | Q1 §3 residual risk, accepted; step 2 pins it with a row (prune clears it) |
| 1 | revise | [test] step 5's prompt named the worktree rule, and nothing tested "only development acquires one" | step 5: the hint dropped, chat 0 a question with `git worktree list` unchanged, a miss revises the skill once then goes to the owner |
| 1 | revise | [plan] the open-project fallback in Q5 is more than the row needs | dropped (Q5 §1, A6, step 7) |
| 1 | revise | nits: Supersedes lines (ADR-0032 item 2, ADR-0045 §5, §3); relative paths' base; Svelte skills at step 3; `claude -p` permissions | all taken (Q1, Q4, Q6 headers; Q1 §2 and step 2; step 3; K1 `--allowedTools`) |
| 2 | approve | B1, B2, B3 resolved | — |
| 2 | approve | [bug] step 7's inside-`--root` test was not said to be canonical (`init` writes a logical `--root`, clients send physical `cwd`); [contract] step 4's and 9's Schema lines named a codes list and code descriptions the schema does not have, and listed Start's `REQUEST_NOT_ALLOWED_HERE` in the descriptor's errors | step 7: both canonicalised, a symlink check seen failing lexically; step 4: the diff is the one entry with its handler's errors and `binding: true` (`omitempty`), the code's widening in `error.go` only; step 9 likewise for `WORKSPACE_OPEN` |
| 2 | approve | nits: Start tests a registered session, with its own message; `flagRoot`'s main root when `--root` is a worktree; a submodule inside a worktree | all taken (step 4; step 7's rule and a pinned row) |

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.

- 2026-10-18, steps 4 and 5, ADR-0088 (owner): a session carries its chat's id, and a resumed chat gets its worktree back while the daemon lives. Step 1 observed `CLAUDE_CODE_SESSION_ID` in the MCP server's environment, stable across `--resume`. Step 4 also builds: `Hello.Chat`, filled by `droidyard mcp` from that variable (pattern `^[A-Za-z0-9-]{1,64}$`); `Sessions.Connect` takes the chat id; the registry remembers chat → last bound root (256 chats, oldest dropped, never persisted) and binds a new session with a remembered chat there before it answers `initialize`/`server/discover`, when ADR-0082 §3's rule still holds and the directory exists. Step 4's check adds `TestResumedChatRecoversItsBinding`: session A with chat X enters `wt`, ends; session B with chat X is bound at `wt` from its first call (seen failing with the memory removed); B with chat X after `rm -rf wt` → main, entry dropped; chat X from a session whose hello root is another project → that project, not `wt` (seen failing with the project test removed); no variable → main. Step 5 adds chat 3: `claude -p --resume` of chat 1 builds in the worktree with no new `workspace_enter` on the wire. Tying hook records to the session stays out (register row at step 10).
- 2026-10-18, step 6: the session's name is set from `server/discover` as well as `initialize`. Claude Code 2.1.302 speaks MCP `2026-07-28`, opens with `server/discover` and sends no `initialize` (step 1, (a)). mcp-go sets the client info from either.
