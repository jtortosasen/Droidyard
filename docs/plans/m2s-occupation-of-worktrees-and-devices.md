# Plan: Milestone 2s — C2 completed: a session occupies its worktree and its device

- Status: Approved
- Approved: 2026-10-18 by the main session under the owner's standing instruction (apply the recommended option), Delegated: Q1 to Q3 decided as drafted into ADR-0094 to ADR-0096; the owner's decisions 1 to 4 are theirs. Reviewed in two rounds by `plan-reviewer`, round 2 `approve` (Review).
- Date: 2026-10-18
- Milestone: "2s — C2 completed: a session occupies its worktree and its device" (owner, 2026-10-18; `docs/vision/beyond.md` §The capabilities, C2; the owner's four decisions of 2026-10-18, quoted in the brief and listed under Scope).
- Phase: none. The MVP is done; the owner's decisions and the eight register rows below are the requirement.
- Contract: Q1 to Q3 (ADR-0094 to ADR-0096). Also ADR-0010, ADR-0015 §2, §6, ADR-0019 §Sessions, ADR-0032 §1, §4, ADR-0036, ADR-0037, ADR-0043, ADR-0048 §1–§2, ADR-0054, ADR-0060 §Consequences, ADR-0061 §5, ADR-0062 §4, ADR-0063, ADR-0081 §2, ADR-0082, ADR-0083, ADR-0085, ADR-0088, ADR-0089, ADR-0090; `workspace-state.md`, `adb-device.md` §Targeting, `mcp.md` §Transport and session, §Features beyond tools.
- Lane: `2s`, branch `m2s` from `main` at `c4bb5c5`, touches `devices,contract,web,process`. No other lane is open.
- Size: nine steps. If the owner wants 2s smaller, the cut is step 8 (row 334's Restore for earlier deletes): it depends only on step 3 and nothing depends on it. Once ADR-0094 to ADR-0096 are written, Q1 to Q3 shrink to links to them.

**The owner should see:**
- A chat that enters a worktree **occupies** it. Another live chat is refused entry, with a message saying to make a worktree of its own or ask the user to release this one; its own edits there are refused the same way. Writes anywhere else (the main tree, a free worktree, `/tmp`, another repository) are allowed and shown in Activity, marked "outside the project" where they are. The main tree is never occupied (Q1).
- The same occupation holds a device: the first chat that acts on an emulator or phone holds it; another chat's installs, launches and taps on it are refused. Reading the screen, the tree or the logs is never refused, and the UI and the CLI never are (Q1).
- An occupation ends when the chat's connection closes, when the chat enters the main tree (the skill says to, when its task is done), or when you release it from the panel, which does not move the chat. After two hours with no call and no hook from the chat it is shown **idle**: another chat is then warned instead of refused, and may take it over. `config.json` can relax the rule to `ask` or `allow` (Q1).
- Claude Code's hooks are tied to their chat, so Activity puts an agent's own commands, edits and `droidyard trash` deletes under that agent. Cursor's MCP connection carries no chat id, so its hooks stay untied: they warn rather than refuse when the holder cannot be told apart (Q2).
- Activity offers Restore for a delete made before the tab opened (Q3).

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| Owner 1: Droidyard coordinates, not secures; never merges, branches or makes worktrees | rule | every step; Q1 §9 (a forged identity is out of the threat model); ADR-0082 §2 stands |
| Owner 2: refuse only a write into a place another live session occupies, with a way out; record every other write, "outside the project" where so; refuse / ask / allow in the configuration; "ask" measured per client | yes | steps 1, 2, 4, 5; Q1 §5–§7, Q2 §3 |
| Owner 3: one concept, occupation by a session, for worktrees and devices, one ADR | yes | steps 2, 6; Q1 |
| Owner 4: the lifetime (start at `workspace_enter`, main tree never; end on close, release, idle, the owner's release; a resumed chat does not get back a lost occupation; idleness needs joined hooks) | yes | steps 2, 3, 6, 7; Q1 §2–§4, Q2 §1 |
| Row 107 (device, C2): per-session device reservation | yes | step 6; Q1 |
| Row 171 (files, C2): an agent's own edits not recorded, not contained, not under `ifRevision`, seen by polling | part | step 4: recorded, refused in an occupied worktree, the tree told at once. `ifRevision` cannot apply to the client's own tool and writes by shell commands cannot be seen: the row narrows to those (Q2 §4) |
| Row 219 (MCP/agents, C2): a hook record not tied to its session | part | step 3: Claude Code joined by chat id; Cursor's MCP connection names no chat, so the row narrows to Cursor (Q2 §1) |
| Row 220 (MCP/agents, C2): the hook's `cwd` for a command in a worktree beside the project; `claude -w` unverified | yes | step 1 (c) measures, step 3 fixes what it shows (Q2 §5) |
| Row 334 (web UI, C2): Restore only for deletes while the tab was open; a CLI delete by an agent under You | yes | steps 3 (attribution, Claude Code), 8 (Restore); Q2 §2, Q3 |
| Row 217 (MCP/agents), Delegated: one connection is one session; a worktree-isolated subagent's `workspace_enter` moves its parent | part | Q1 §8: occupation is per connection; joined hooks of every subagent count as the session's, so a subagent is never refused its parent's worktree; the move itself stays, the row narrows with the reason |
| Row 221 (MCP/agents), Delegated: commands dropped in the main tree when `--root` is a worktree, and in a submodule inside a worktree outside the project | yes | step 3; Q2 §5 |
| Row 234 (MCP/agents), Delegated: Claude Code's `EnterWorktree` tool | part | step 1 (c) observes it if offered; the skill says to `workspace_enter` after it; the row closes or narrows to what was not observed |
| Row 361, the daemon-hosted terminal | no | its own milestone, a security ADR first (brief) |
| Row 246, more assistant hooks (a shell redirect, a session-start context) | no | rejected by ADR-0037 with measurements; the edit hooks of Q2 §3 are neither |
| C4 (device hosting) | no | Q1 §10 keeps a remote device and a remote user possible; nothing is built for them |
| Linux | no | macOS only, as every milestone |

## Starting point

`m2s` at `c4bb5c5`; API `0.28.0` (`internal/domain/system/system.go`); ADRs up to 0093.

- **Sessions.** `internal/capability/sessions.go` `Sessions`: per MCP connection its chat id, name, project, bound root and workspace, published on `session.state` under one lock (ADR-0083); a chat's last binding remembered (ADR-0088). Nothing is exclusive (ADR-0082 §7). `Operations.prepare` refuses a `Binding` capability unless `req.Origin.ID` is a registered session (`operation.go:892`). `Origin{ID, Name, Root}` reaches every handler as `Call.Origin` (`descriptor.go`).
- **The hello.** `internal/daemon/hello.go` `Hello.Chat`, set only by `droidyard mcp` from `CLAUDE_CODE_SESSION_ID` (`cmd/droidyard/client.go:250`); the daemon clears a value outside `ChatPattern`.
- **The hook.** `cmd/droidyard/hook.go`: before a shell command, the `rm` route (ADR-0061, ADR-0062); after one, the observation and `agent.recordCommand` under `hookRoot` (ADR-0085). Each call dials as `client=cli` with no chat. `internal/assistant/hook.go` `DecodeHookEvent` (Claude `PreToolUse` Bash, Cursor `beforeShellExecution` and `preToolUse` Delete), `DenyOutput`, `PassOutput`; `hookentry.go` writes Claude `PreToolUse`/`PostToolUse`/`PostToolUseFailure` with matcher `Bash`, and Cursor `beforeShellExecution`, `preToolUse` (Delete), `postToolUse`/`postToolUseFailure` (Shell). No hook sees an edit tool.
- **Devices.** `internal/domain/device/target.go` `authorize`, every device call's preamble (policy re-check, one `devices -l`); the emulator domain reuses it through `export.go` `Authorize`. Nothing records who acts on a serial.
- **Files.** The tree polls (`internal/domain/file/watch.go`, `watchInterval` 1 s, a `tick` seam). The trash is `ws/<wsid>/trash/<id>/entry.json` (ADR-0054); no capability lists it. Activity (`web/src/lib/ops/activity.ts`) holds operations from load on (`operation.watch` returns only non-terminal ones, ADR-0043 §1; the store keeps terminal ones 15 min, at least 200); `blocks.ts:92` puts every `ui` and `cli` operation under You.
- **Budgets.** `tools/list` without `outputSchema` is 28,811 B of `maxToolListBytes` 29,000 (`TestToolListModelFacingBytes`, run 2026-10-18): no room for a new MCP tool. The server instructions' block in `mcp.md` is about 1,900 characters and the core note about 100, of 2,000 (`wc -m`): no room for a new rule there either.
- **Baseline, 2026-10-18 at `c4bb5c5`:** `go vet ./...` clean; `go test ./...` ok in every package. This file fails `internal/doccheck` `TestPlansIndexMatchesFiles` until the main session adds its row to `docs/plans/README.md`.

## Acceptance

- [ ] A1. A session that enters a linked worktree, or is bound in one from its hello, occupies it when it is free; another live session's `workspace_enter` there fails `WORKSPACE_OCCUPIED` naming the holder and the way out, binding unchanged; the main tree is never occupied; an idle holder is taken over. Source: owner 3, 4; Q1 §1–§3. Proven by: steps 2, 5.
- [ ] A2. An occupation ends when the connection closes, when the holder moves (the main tree also releases its devices), and at the owner's release, which leaves the session's binding where it was; a resumed chat whose worktree another live session now occupies is bound at the main tree. Source: owner 4; Q1 §3, §4. Proven by: steps 2, 6, 7, 9.
- [ ] A3. A Claude Code hook event is tied to its chat's live MCP session: it keeps the session from going idle, and what it records shows under that session; a joined connection can move no binding. A Cursor hook stays untied. Source: row 219; Q2 §1, §2. Proven by: step 3.
- [ ] A4. A command run in a worktree beside the project is recorded under that worktree as step 1 shows Claude Code reports it; one in the main tree with `--root` a worktree, and one in a submodule inside a worktree outside the project, are recorded under where they ran. Source: rows 220, 221; Q2 §5. Proven by: steps 3, 9.
- [ ] A5. An agent's own edit or create is recorded in Activity under its session and its place ("outside the project" outside it) and the tree shows it without waiting for a poll; one into a worktree another live session occupies is refused with a message naming the holder and what to do, or asked, or allowed, by the configuration; one where the holder is idle, or cannot be told from the writer, is allowed with a warning. Source: owner 2; row 171; Q1 §5, Q2 §3. Proven by: steps 4, 5.
- [ ] A6. An MCP session's first acting call on a device occupies it; another MCP session's acting call on it fails `DEVICE_OCCUPIED` naming the holder, before any adb command changes the device; reads, the UI and the CLI are never refused; the occupation ends with the session, the emulator, the holder's return to the main tree or the owner's release. Source: owner 3; row 107; Q1 §2–§4. Proven by: steps 6, 9.
- [ ] A7. The panel shows who occupies each worktree and device and which holders are idle, and the owner releases one without moving its session. Source: owner 4; Q1 §4. Proven by: steps 7, 9.
- [ ] A8. Activity offers Restore for a delete made before the tab opened, and a Claude Code agent's delete through `droidyard trash` shows under that agent. Source: row 334; Q2 §2, Q3. Proven by: steps 3, 8, 9.
- [ ] A9. The schema diff is the milestone's: one minor version; `WORKSPACE_OCCUPIED`, `DEVICE_OCCUPIED`; `occupiedBy` on describe's worktrees and on `device.list`; `occupies`, `devices`, `stale` on `session.watch`; `agent.checkWrite`, `agent.recordEdit`, `session.release`, `file.trash`. The MCP tool list stays within 29,000 B with no new tool. Source: `capability-api.md` §Discovery and versioning; ADR-0036. Proven by: steps 2, 4, 6, 7, 8.
- [ ] A10. A level-5 run with two real Claude Code chats, a cursor-agent chat, this lane's emulator and the UI shows A1 to A8 together. Source: brief (run rules). Proven by: step 9.

## Run rules (steps 1, 5, 7, 8, 9)

- **K1.** Fixtures only under `B=~/Library/Caches/droidyard-dev/2s` or `t.TempDir()`; an isolated daemon by `verify-daemon` §Launch (`DROIDYARD_CONFIG_DIR`, `DROIDYARD_STATE_DIR` under `/tmp`). Synthetic repositories only; no owner repository is named, opened or read. Claude Code runs with `--mcp-config <scratch> --strict-mcp-config`, `--allowedTools` naming `Bash(git:*)`, `Bash(./gradlew:*)`, the edit tools and `mcp__droidyard`, and the hooks `init` writes in the scratch repository; cursor-agent by `verify-daemon`'s Cursor recipe (`CURSOR_DATA_DIR` scratch). A recorded hook or wire line carrying the user's email is never kept.
- **K2.** Devices only at steps 6's level-4 fake and 9: an emulator this lane starts, acted on by its own serial; stopped only if this lane started it; never `adb kill-server` or `start-server`; the owner's adb server pid recorded before and after; no phone without asking the owner. No process killed by name; headless Chrome with its own profile over CDP.

## Steps

### 1. Spike: what the hooks receive, and what each client does with the answer

- Builds: no product code. The main session's run under K1, recorded in `docs/research/spike-c2-occupation-2026-10.md` with `[obs]`/`[doc]`/not observed per fact (`spike-c2-sessions-2026-10.md`'s shape). `$B/repo`: a copy of `samples/golden`, `git init`, one commit, `git worktree add ../wt -b t`; trusted; `init --client claude` and `--client cursor`, then every hook command pointed at a recorder `$B/rec-hook.sh` that logs stdin (email removed), its environment's variable names, whether `CLAUDE_CODE_SESSION_ID` is set and equal to the event's id, and its parent chain (`ps -o pid=,ppid=,comm=` up to the client), and answers from `$B/answer.json`. Edit-tool entries added by hand: Claude `PreToolUse` and `PostToolUse`, matcher `Write|Edit|MultiEdit|NotebookEdit`; Cursor `preToolUse` and `postToolUse` with no matcher, and `afterFileEdit`.
  - (a) Claude Code edits: the events for Write (new file, existing file), Edit, MultiEdit, in the main tree, in `../wt`, in `/tmp`, from the parent and from a plain and a worktree-isolated `Task` subagent: `tool_name`, `tool_input.file_path` (absolute?), `cwd`, `session_id`, `agent_id`, and what `tool_response` says of a create versus an update. Whether Claude Code itself lets a `-p` run write outside its directory (with and without `--add-dir`).
  - (b) Claude Code's answers on an edit's `PreToolUse`: `deny` with a reason (the model reads it? it takes the way out or tries another path?); `ask` under `-p` (prompted, denied, allowed?), and interactively if a terminal is available; `allow` with `additionalContext` (does it reach the model?); `PostToolUse` `additionalContext` after an edit.
  - (c) The shell's directory (rows 220, 234): `cd ../wt && ./gradlew tasks` as one command; `cd ../wt`, then `./gradlew tasks`; both with `--add-dir ../wt`; `claude -w t1` (the hello's root and the hooks' `cwd`); whether `-p` offers `EnterWorktree`, and if used, its hooks' `cwd` and any new connection or MCP notification.
  - (d) The join: `CLAUDE_CODE_SESSION_ID` in the Bash tool's environment (so `droidyard trash` could carry it), equal to the hooks' `session_id` and the hello's chat.
  - (e) cursor-agent `-p`: which events fire for its own edit and create tools, their tool names and fields (an absolute path?), `conversation_id`; whether `preToolUse` `permission: "deny"` and `"ask"` on an edit tool are honoured, with and without `--force`; any chat id in its shell's environment.
  - (f) The end signal: `kill -9` of a running `claude -p`, and of a `cursor-agent -p`: does the shim exit, and how long until `session.watch` publishes `ended`; the same after a normal exit.
- Implements: nothing; it settles the Unverified rows steps 3 to 5 rest on.
- Check: the note answers (a) to (f), each with the hook or wire line it rests on. **Stop rules:** if a Claude Code `deny` on an edit tool does not stop the write, step 4's refusal cannot be built: the plan stops and Q2 returns to the owner. A client that does not honour `ask` gets `deny` for it (Q1 §6), no stop. No pre-edit event in Cursor: Cursor's edits are recorded and never refused (a register row), no stop. Step 3 builds Q2 §5's leading-`cd` rule only if (c) shows `cwd` is not the worktree after `cd <wt> && …`.
- Schema: no change.
- Needs: nothing built; real `claude` and `cursor-agent` (versions recorded); a daemon from `c4bb5c5` for (f).
- Not here: the Cursor IDE (the owner's trial); an interactive `ask` without a terminal (Unverified).
- Rests on: —
- [ ] Done. Evidence:

### 2. Occupation of worktrees in the core: the first end to end

- Builds:
  - `internal/policy`: `config.json`'s `occupation` section (Q1 §7), read by size and mtime like the rest: `Occupation() (conflict string, staleAfter time.Duration)`, defaults `refuse` and 120 min; `conflict` one of `refuse|ask|allow`, `staleAfterMinutes` 1 to 10,080. A wrong value makes the file unusable as any wrong key does (ADR-0032 §4). Injected into the session registry at `serve` (`capability` cannot import `policy`).
  - `internal/capability/sessions.go`: per session `seen` and `stale`; a holder per linked-worktree root (canonical), under the same lock. One registry method decides and binds under that lock (`Occupy(id, workspace, root, linked, conflict)`), so `enter.go`'s separate `Project`/`Open`/`Bound` takes cannot race a second enter. A session **holds** a worktree when it binds there while no other live, non-idle session holds it: at `Opened` from a hello root that is a linked worktree (a `claude -w` chat; one inside a worktree another session holds is bound, not holding), at `workspace.enter`, at ADR-0088's restore. An idle holder is taken over. "Another session" is another session of another chat: two live sessions of one chat (a second `claude --resume` of it) share what either holds. A held worktree is released when the holder's binding moves (to the main tree too) and at `End`. `Seen(id)` on every MCP request (the adapter's `AddOnSuccess`, where `Named` is called) clears `stale`; a ticker (1 min, a clock seam) marks `stale` past `staleAfter`. Each change publishes `session.state`.
  - `internal/workspace/enter.go`: into a worktree another live, non-idle session holds: `refuse` and `ask` fail the new `WORKSPACE_OCCUPIED` (`internal/capability/error.go`, its meaning there), binding unchanged; message: the holder's name and since when; nextStep: "make a worktree of your own (git worktree add) and workspace_enter it, or ask the user to release <root> in Droidyard's sessions panel". `allow`: bound, not holding. The message names the holder as "another chat (<name>, since <time>)", since every Claude Code chat is named `claude-code`. The restore (`adapters/mcp/mcp.go` around line 605, which today calls `Forget(chat)` on any failed restore): a remembered worktree another session holds is not restored (ADR-0019's binding instead) and the memory is kept.
  - `workspace.describe`'s `worktrees[]` gains `occupiedBy {name?, since, stale?}` for a worktree held by another session (no session id: ADR-0048 §2). `session.watch` rows and `session.state` gain `occupies` (the held workspace id, omitted when none) and `stale` (omitted when false).
  - `system.Version` `0.29.0`, the milestone's one bump; `go test ./internal/schema -update`. `workspace.enter`'s description says the release within its two sentences and 400 characters (ADR-0036 §1; it is 348 today), for example "Moves this session into a git worktree of its project, occupying it, or back to the project's root, which frees what it occupies: …" (about 45 B of the 189 B left). The skill's "Development in a worktree": `WORKSPACE_OCCUPIED` means another chat works there, make your own; when the task is done, `workspace_enter` the project's root.
- Implements: Q1 §1–§4, §6, §7 for worktrees; owner 3, 4.
- Check: `TestWorktreeOccupation` (`cmd/droidyard`, scripted MCP shims against a real `serve`, real `git worktree add`, trusted `main`, worktrees `wt1`, `wt2`):
  - A enters `wt1` → A's row `occupies` `wt1`'s id; B enters `wt1` → `WORKSPACE_OCCUPIED` naming A, B's `workspace_describe` still `main` (seen failing with the check removed);
  - A and B enter `main` repeatedly: nothing held (seen failing with the linked-worktree test removed);
  - A enters `wt2` → `wt1` free, B enters it; A's shim killed → `ended`, `wt2` free, C enters it;
  - `staleAfterMinutes: 1`, clock advanced: A's row `stale`; B enters A's worktree → B holds, A's row loses `occupies`; A calls a tool → `stale` cleared;
  - `allow`: B enters a held worktree, bound, A still holds; `ask`: refused as `refuse`;
  - restore: chat X in `wt1` ends; chat Y enters `wt1`; a new chat-X session is bound at `main` (seen failing with the restore unchanged); after Y moves away, the next chat-X session is back in `wt1`;
  - a hello bound in a linked worktree holds it when free; one bound in a worktree A holds is bound, not holding;
  - two live sessions with chat X: the second enters what the first holds → allowed, nothing refused;
  - B's `workspace_describe` lists `wt1` with `occupiedBy.name` A.
  - `TestOccupationConfig` (`internal/policy`): defaults, each mode, the bounds, a wrong value unusable. Under `-race`, through `workspace.enter` itself (two concurrent `Invoke`s on one registry): two sessions entering one free worktree → exactly one holds and the other gets `WORKSPACE_OCCUPIED`; two sessions taking over one idle holder → one holds (seen failing with the decision taken outside `Occupy`'s lock). `TestDescriptionsFollowTheRule` (`internal/schema`, ADR-0036 §1) and `TestToolListModelFacingBytes` within 29,000.
- Schema: `version` `0.29.0`; `WORKSPACE_OCCUPIED` in `workspace.enter`'s `errors`; `occupiedBy` on describe's `worktrees` items; `occupies`, `stale` on `session.watch`'s rows and the `session.state` payload; `workspace.enter`'s description. Nothing else.
- Needs: step 1's stop rules not fired (the enter path rests on none of its facts).
- Not here: refusing writes (step 4); devices (step 6); the panel and the owner's release (step 7); hooks keeping a session fresh (step 3: until then only MCP requests do). Nothing is persisted: occupations end with the daemon (ADR-0010).
- Rests on: one holder per worktree, decided under one lock, level 4 (the `-race` test).
- Review: the Opus verifier (contract, concurrency), effort `high`.
- [ ] Done. Evidence:

### 3. The hook knows its session and where it ran

- Builds:
  - `internal/assistant`: `DecodeHookEvent` and `DecodeAfterEvent` return `Chat`: Claude Code's `session_id`, Cursor's `conversation_id`, kept only when it matches `daemon.ChatPattern`.
  - `cmd/droidyard/hook.go`: every hello the hook sends carries `Chat`. `droidyard trash` carries `CLAUDE_CODE_SESSION_ID` in its hello when it is set and valid, if step 1 (d) found it in the Bash tool's environment; else `trash` takes `--chat <id>`, which the hook's replacement names (ADR-0062 §4's prefix unchanged). `droidyard call` does not join: no row needs it, and it is the widest door.
  - `internal/daemon` (`native.go:75`, where a CLI connection's requests get their `Origin`) and `Sessions.Joined(chat)`: a non-MCP connection whose hello chat is a live MCP session's chat is **joined** to it (Q2 §1); with two live sessions of that chat, the most recently seen. `Origin.ID` stays the connection's own id; `Origin` gains `Client` (the connection's kind), `Chat` (its hello chat) and `Joined` (the joined session's id). Each request calls `Seen` on the joined session, and its operation's `session` (ADR-0048's field) is the joined session's ref. Nothing else reads `Joined`: `prepare`'s `Binding` check also requires `Client == mcp`, and step 6's holding reads `Client` and `ID`, never `Joined`.
  - `hookRoot` (Q2 §5): with `--root` a linked worktree, a `cwd` in its main tree gives the main tree; the upward walk passes a `.git` that is not a linked worktree of the project (a submodule, a nested clone) and goes on; and, only if step 1 (c) showed it is needed, a command whose first simple command is `cd <dir>` followed by `&&` or `;` runs in `<dir>`, resolved against `cwd` with the observation's splitter.
  - `web/src/lib/ops/blocks.ts`: an operation whose `session` has a name is an agent's, under that name, whatever its `client`.
- Implements: Q2 §1, §2, §5; rows 219, 220, 221, 334 (attribution).
- Check: `TestHookJoinsItsSession` (`cmd/droidyard`, an MCP shim with chat X, the real `droidyard hook`):
  - a Claude `PostToolUse` `./gradlew tasks` with `session_id` X → the record's operation carries the shim's session `{id, name}` (seen failing with the hello's chat unset); with `session_id` Y → no MCP session ref, as today;
  - A idle (clock seam), a joined hook event → `stale` cleared (seen failing with no `Seen` on joined requests);
  - a joined CLI connection calling `workspace.enter` → `REQUEST_NOT_ALLOWED_HERE`, binding unchanged (seen failing with `Origin.ID` set to the joined session and the client check removed: the only way the fence breaks, since only the MCP adapter registers sessions);
  - a Cursor event with `conversation_id` C beside a Cursor-like shim with no chat → not joined;
  - `droidyard trash` with chat X (variable or flag, as built) → its `file.delete` carries the shim's session.
  - `TestHookRecordsInTheWorktree` gains: `--root` a worktree and `cwd` in main → under main (seen failing with ADR-0085 §1 as written); a submodule inside a worktree outside the project → under the worktree (seen failing with the walk stopping at the first `.git`); the leading-`cd` row, if built (seen failing without the rule).
  - Vitest `blocks.test.ts`: a `cli` operation with a named session shows under Agents, one without under You, in `actorOf` and in the You and Agents filters (`blocks.ts:138`, `:174`).
- Schema: no change but the description of the operations' `session` field ("the calling session, or the MCP session a call from its agent's hook or shell was joined to"), wherever it is generated.
- Needs: steps 1 (c), (d), 2.
- Not here: joining Cursor (Q2 §1); a subagent's identity (Q1 §8); refusing anything (step 4).
- Rests on: a joined connection gains attribution and freshness, never a binding move, level 4.
- Review: the Opus verifier (a field's meaning, the binding fence), effort `high`.
- [ ] Done. Evidence:

### 4. The agent's own edits: recorded, and refused in another session's worktree

- Builds:
  - `internal/domain/agent`, both audience `human` (the hook's channel, ADR-0063 §5's reasoning), not untrusted:
    - `agent.checkWrite` (query, global): params `{client, paths}` (1 to 20 absolute paths); result `{verdict, occupiedBy?, message?}`, `verdict` one of `allow|warn|ask|refuse`, by Q1 §5's rule: a path under a worktree root held by another session (canonical, at a component boundary) whose holder the caller is told apart from → the configured `conflict` (`refuse`, `ask`, or `allow`); a held worktree whose holder is idle, or cannot be told from the caller → `warn`; anything else → `allow`. The longest held root containing the path decides, so a worktree nested in the main tree (`main/.claude/worktrees/<n>`, `claude -w`'s layout) is matched before the main tree; paths are canonicalised by their nearest existing ancestor (a new file in a new folder; `/tmp` and `/private/tmp`). The caller is `Call.Origin.Joined` and `Call.Origin.Chat` (step 3); it is the holder's own when it is the holder's session or carries the holder's chat. The message names the holder, since when, and the way out (step 2's nextStep).
    - `agent.recordEdit` (command, workspace, not idempotent, not destructive): params `{client, files: [{path, created?}]}` (1 to 20), `outside?`; `path` relative to the workspace root, absolute when `outside`; each path bounded (4,096 bytes, control and format characters refused). After recording, the workspace's file-watch pollers walk at once (a nudge on `internal/domain/file`'s watcher registry), so `file.changes` does not wait for the tick.
  - `internal/assistant`: decode the edit events step 1 (a), (e) found (Claude Write, Edit, MultiEdit, NotebookEdit; Cursor's) into the absolute path and, where the event says it, created or updated; `AskOutput` (Claude `permissionDecision: "ask"`; Cursor's form if step 1 (e) showed it honoured, else `DenyOutput`); the warning as context where step 1 (b) showed it reaches the model.
  - `internal/assistant/hookentry.go`: `init` writes Claude `PreToolUse` and `PostToolUse` entries with matcher `Write|Edit|MultiEdit|NotebookEdit` beside the `Bash` ones, and Cursor's per step 1 (e); ADR-0090's recognition and `--uninstall` cover them.
  - `cmd/droidyard/hook.go`: before an edit, `agent.checkWrite` within ADR-0061's 2 s: `refuse` → the deny with the message, `ask` → the ask, `warn`/`allow` → pass (the warning as step 1 (b) allows). A routed `rm` whose targets lie in a worktree another session holds is checked the same way, and a `refuse` denies with the occupation message instead of the trash replacement. After an edit: `hookRoot` of the file's directory gives the place; none → `--root` with `outside` and the absolute path; then `agent.recordEdit`. Everything fails open.
  - Activity (`web/src/lib/ops`): hides `agent.checkWrite` as it hides the UI's reads; renders a record "<agent> edited|created <path>", "outside the project" when so.
- Implements: Q1 §5, §6; Q2 §3, §4; owner 2; row 171.
- Check:
  - `TestCheckWrite` (`cmd/droidyard`, real `serve`, step 2's shims; A with chat X holds `wt1`): joined to B, `wt1/x.kt` → `refuse` naming A (seen failing with the holder test removed); joined to A → `allow`; unjoined with chat Z → `refuse` (A's chat differs); A chatless and the caller unjoined → `warn`; A idle → `warn`; `allow` → `allow` with `occupiedBy`; `ask` → `ask`; a second live session of chat X → `allow`; a path in `main`, in a free worktree, in `/tmp`, in `wt1-other/` → `allow` (the last seen failing with a string prefix); a held worktree nested in `main` → `refuse` (seen failing with the main tree matched first); `wt1/new/dir/x.kt` with `new/` absent, and a held root spelt `/tmp/…` against a path spelt `/private/tmp/…` → `refuse`.
  - `TestHookEdits` (`cmd/droidyard`, the real `droidyard hook` fed step 1's captured events as testdata, email removed): Claude `PreToolUse` Write into A's `wt1` with `session_id` Y → deny whose reason names A and `git worktree add` (seen failing with the hook passing edits); `ask` mode → `permissionDecision: "ask"`; no daemon → pass within 2 s; `PostToolUse` Write of a new file in `wt2` → an `agent.recordEdit` under `wt2` with `created` as the event says; Edit in `/tmp` → under `--root`, `outside`, the absolute path; a `file.watch` on `wt2` with a one-hour tick seam gets the change within 500 ms of the record (seen failing with the nudge removed); `rm` in `wt1` from chat Y → the occupation deny, not the trash.
  - `TestInitWritesEditHooks` (`internal/assistant`): written, merged, kept, recognised (ADR-0090), removed by `--uninstall`.
  - `TestToolListModelFacingBytes` unchanged.
- Schema: `agent.checkWrite` and `agent.recordEdit` with params, results and errors. Nothing else.
- Needs: steps 1 (a), (b), (e), 2, 3.
- Not here: `ifRevision` for the client's own write (Q2 §4); writes by shell commands; the look of the records (step 7).
- Rests on: a write is refused only when its place is held by a session the hook can tell is not its own, level 4 (the identity matrix); that the client obeys the deny is level 5 (steps 1, 5).
- Review: the Opus verifier (two capabilities, a refusal on writes), effort `high`.
- [ ] Done. Evidence:

### 5. Level 5, early: two real chats and one worktree

- Builds: no code. The main session's run under K1: `$B/repo` trusted, `init --client claude` and `--client cursor`, an isolated daemon, `conflict` `refuse`; the UI in headless Chrome.
  - Chat A, `claude -p` kept open (`--input-format stream-json`, stdin held): a development task; it makes `repo-a`, enters it, edits a file there.
  - Chat B, a second `claude -p` while A is live: told to change that file at its absolute path in `repo-a`. Its Edit is denied with the message; whether it then makes a worktree of its own is recorded. Its `workspace_enter` into `repo-a`, if tried, answers `WORKSPACE_OCCUPIED`.
  - B edits `/tmp/2s-scratch.txt`: allowed, and in Activity under B, "outside the project".
  - A's edits are in Activity under A, and A's new file shows in the tree without a reload.
  - `conflict` `ask`: B's edit gets Claude Code's ask, as step 1 (b) found it in `-p`.
  - cursor-agent, one chat told to edit in `repo-a`: refused (A has a chat id), or recorded with the register row, as step 1 (e) found.
- Implements: A1, A5 at level 5.
- Check: the decisive lines under `$E`: the hook answers, the `WORKSPACE_OCCUPIED` wire line, each record's operation with its session, the tree's CDP read, `git -C repo-a status --porcelain` holding A's change only.
- Schema: no change.
- Needs: steps 2 to 4; Claude Code, cursor-agent, headless Chrome.
- Not here: devices (steps 6, 9); the panel (step 7); a measurement of how often agents take the way out (Unverified).
- Rests on: a real client stops on the deny and its agent reads the way out, level 5 (one run per client).
- [ ] Done. Evidence:

### 6. Device occupation

- Builds:
  - `internal/capability`: the registry holds devices too: a holder per serial; released at the holder's `End`, when its binding moves to the main tree (also when it is already there: `workspace_enter` of the project's root is the skill's "task done"), and by `ReleaseDevice(serial)`. `session.watch` rows and `session.state` gain `devices` (serials held, omitted when none). `internal/domain/device` and `internal/domain/emulator` reach it, and the configured `conflict`, through an interface injected at `serve`, as the device `Policy` is.
  - A descriptor field `Occupies` (internal; the schema shows the code in `errors`): `device.install`, `device.uninstall`, `device.launch`, `device.tapElement`, `device.inputText`, `emulator.stop`, `emulator.wipe`; and `emulator.start`, which holds the serial it boots (taking it over) and an adopted serial only when free, at its serial's resolution in its handler, and is never refused.
  - The check, for a call whose `Origin.Client` is `mcp` and whose `Origin.ID` is a registered session (never `Origin.Joined`: a joined CLI call is the CLI) to an `Occupies` capability, runs in the device preamble after the policy check (`target.go` `authorize`; `emulator/stop.go`'s hook through `export.go` `Authorize`), and for `emulator.wipe` in its handler once `runningSerialOf` has found the serial, before `stopSerial`: free → held; own → on; held by a live, non-idle other → `refuse`/`ask` fail the new `DEVICE_OCCUPIED` (`device` package; message: the holder's name and since when; nextStep: "use another device or emulator (emulator_start), or ask the user to release <serial> in Droidyard's devices panel"), `allow` proceeds; idle holder → taken over. Reads, the UI and the CLI neither hold nor are refused. A serial absent from the preamble's listing, or stopped through `emulator.stop`, is released.
  - `device.list` rows gain `occupiedBy {name?, since, stale?}`; `DEVICE_OCCUPIED` in the `errors` of each `Occupies` capability but `emulator.start`.
- Implements: Q1 §1–§4 for devices; owner 3; row 107.
- Check: `TestDeviceOccupation` (`internal/domain/device` with the fake adb of `fakeadb_test.go`, two registered MCP sessions; the emulator cases in `internal/domain/emulator` with its fakes): A's `device.launch` on `emulator-5554` → held, `device.list` shows A; B's `device.install` → `DEVICE_OCCUPIED` naming A, and the fake's log has no `install` (seen failing with the check removed); B's `device.uiTree` → ok; a `client=ui` `device.tap`, a CLI call, and a CLI `device.install` joined to B (step 3) → ok, nothing held (the last seen failing with the check reading `Joined`); B's `emulator.wipe` of the AVD running on A's serial → `DEVICE_OCCUPIED`, no kill in the fake's log; A enters `main` → released, B's install runs and B holds; B ends → free; the serial gone from the listing → free; A's `emulator.start` of a fresh boot on a serial B held → A holds; idle → taken over; `allow` → B proceeds, A holds. Under `-race`: A and B act on one free serial at once → one holds, one refused.
- Schema: `DEVICE_OCCUPIED` in the `errors` of the seven that can refuse (not `emulator.start`); `occupiedBy` on `device.list`'s rows; `devices` on `session.watch`'s rows and `session.state`. Nothing else.
- Needs: steps 2, 3 (`Origin.Client`).
- Not here: holding on a read; an explicit claim tool (Q1 §2); remote devices (C4); the real emulator (step 9).
- Rests on: the check runs before any adb command that changes the device, level 4 (the fake's log).
- Review: the Opus verifier (a new code, a refusal in the preamble, concurrency), effort `high`.
- [ ] Done. Evidence:

### 7. The panel: who occupies what, idle holders, and the owner's release

- Builds:
  - `session.release` (command, global, audience `human`, idempotent; not destructive: it removes a mark, no data): params `{workspace?, device?}`, exactly one (else `REQUEST_INVALID_PARAMS`); it ends that occupation, publishes `session.state` for the holder, and never changes a binding. A free one answers as done.
  - Web (load `svelte-core-bestpractices` and `svelte-code-writer` first; the autofixer gate on each `.svelte` file): `sessions.ts`, `Projects.svelte`: a session row names what it occupies and shows "idle" when `stale`; a worktree row held by a session names it; Release on an occupied row calls `session.release`. `devices.ts`, `Devices.svelte`: a held device shows "in use by <name>" (and idle), with Release.
- Implements: Q1 §4; owner 4.
- Check: `TestSessionRelease` (real `serve`, shims, the fake adb): A holds `wt1` and `emulator-5554`; `session.release {workspace}` over the WebSocket → A's row loses `occupies`, A's `workspace_describe` still `wt1` (seen failing with a release that rebinds), B enters `wt1`; `{device}` → B acts on it; over MCP → `REQUEST_NOT_ALLOWED_HERE`; both or neither field → `REQUEST_INVALID_PARAMS`. Vitest: holder and idle labels, Release's params. Level 4 in headless Chrome against an isolated daemon with two scripted shims: the panel shows A on `wt1`, Release clears it, A's row still names `wt1`.
- Schema: `session.release`. Nothing else.
- Needs: steps 2, 6.
- Not here: the look (the owner's review); moving a session (never: ADR-0082 §3); collapsing a run of edit records into one Activity row (not asked; a register row if Activity floods at step 9).
- Rests on: —
- Review: the Opus verifier (a capability), default effort.
- [ ] Done. Evidence:

### 8. Activity: Restore for a delete made before the tab opened

- Builds: `file.trash` (query, workspace, audience `human`): the workspace's trash entries newest first, at most 200: `{id, path, type, deletedAt, size, by?}`; an `entry.json` that cannot be read is skipped. `file.delete` writes `by {client, name?}` into `entry.json` (an additive key), from the call's origin: `name` is the MCP session's name when the call carries one (step 3's join). Activity calls `file.trash` at load and after a reconnect, and shows each entry deleted before its first operation as a "deleted" row with Restore, under the agent or You by `by`; a Restore of an entry already gone says so.
- Implements: Q3; row 334 (Restore).
- Check: `TestTrashListing` (`internal/domain/file`): two deletes listed newest first, the bound, `by` from the origin, an old `entry.json` without `by` listed without it, an unreadable one skipped (seen failing with the skip removed: the listing fails). Vitest: an entry from before load offers Restore, one matching an operation in the store is not shown twice. Level 4 in headless Chrome: `droidyard trash` with chat X (step 3) while no tab is open, then the tab: the row under A, Restore, the file back.
- Schema: `file.trash`. Nothing else.
- Needs: step 3.
- Not here: emptying the trash and a trash view (register row of ADR-0054 §Consequences); Cursor's deletes stay under You (Q2 §1).
- Rests on: —
- Review: the Opus verifier (a capability, a state-file key), default effort.
- [ ] Done. Evidence:

### 9. Level 5 with devices and the UI, the documents, and the close

- Builds: no code but what the run finds. The main session's run under K1 and K2: an isolated daemon, `$B/repo` trusted, this lane's emulator, the UI in headless Chrome.
  - (a) two Claude Code chats: A enters `repo-a`, B is refused; the panel shows A occupying, B on main.
  - (b) A's `device_launch` on the lane's emulator holds it; B's `device_install` gets `DEVICE_OCCUPIED`; B's `device_uiTree` answers; the UI's tap acts.
  - (c) A's command `cd <repo-a> && ./gradlew tasks` recorded under `repo-a` (row 220).
  - (d) `staleAfterMinutes: 1`: A idle in the panel; B's edit in `repo-a` allowed with the warning in its transcript.
  - (e) Release from the panel: A's mark goes, A's describe still `repo-a`.
  - (f) A's client killed: its occupations end within step 1 (f)'s time.
  - (g) A resumed after B entered `repo-a`: A bound at main, its edit there refused.
  - (h) A's `rm` through `droidyard trash` with no tab open, then the tab: Restore under A.
  - (i) cursor-agent: one chat's edit in a worktree A holds, as step 5 found.
- Documents: `workspace-state.md` §What a workspace is (occupation) and §Devices are not workspace-owned (held by a session, still global); `adb-device.md` §Targeting; `mcp.md` §Transport and session (occupation, `WORKSPACE_OCCUPIED`) and §Features beyond tools (the edit hooks); `glossary.md` (Occupation); the register: rows 107, 220, 221, 334 closed or narrowed by what was proven, 171 narrowed (`ifRevision`, shell writes), 217 and 219 narrowed (subagents, Cursor), 234 by step 1 (c), new rows for Q1 §3, §4's limits (a resumed chat not told; freshness only from the hooks that dial; a Cursor holder idle after editing only) and for what the run finds; `docs/README.md` §Where the work stands; the plans index. `doccheck`'s `liveBudget` raise stated.
- Implements: A2, A4, A6, A7, A8, A10.
- Check: each item's decisive lines under `$E`; the owner's adb server pid unchanged; no email in the evidence.
- Schema: none.
- Needs: steps 1 to 8.
- Not here: the Cursor IDE (the owner's trial).
- Rests on: —
- Review: the Opus verifier at the close.
- [ ] Done. Evidence:

## Not in this milestone

- The daemon-hosted terminal (row 361): its own milestone and security ADR (brief).
- More assistant hooks, a shell redirect or a session-start context (row 246): ADR-0037.
- `ifRevision` for an agent's own write, and writes by shell commands (`sed -i`, redirects): Q2 §4; row 171 narrowed.
- Joining Cursor's hooks: Q2 §1; row 219 narrowed.
- A subagent's own identity on MCP calls: Q1 §8; row 217 narrowed.
- A claim or release MCP tool, and a new instructions line: no room in either budget (Starting point); the skill and `workspace.enter`'s description carry the release.
- Asking from the daemon (`ask` on `workspace.enter` and on devices): Droidyard cannot ask; elicitation is not built (`mcp.md` §Features beyond tools). Q1 §6.
- Editing `occupation` from the UI or through `policy.update`: hand-edited (Q1 §7).
- Emptying the trash, and a trash view beyond Activity: ADR-0054 §Consequences' row.
- Remote devices and remote users: C4 (Q1 §10).
- Linux.

## Design questions

Decided 2026-10-18, as drafted, into accepted ADRs (the owner's decisions 1 to 4; the rest Delegated):

- Q1, occupation by a session of a worktree and of a device: [ADR-0094](../adr/0094-occupation-by-a-session.md).
- Q2, hooks joined to their session and the agent's own edits: [ADR-0095](../adr/0095-hooks-joined-and-agent-edits.md).
- Q3, Activity's Restore for a delete made before the tab opened: [ADR-0096](../adr/0096-trash-listed-for-activity.md).

The ADRs keep the drafts' numbering: a clause of Q1, Q2 or Q3 cited in this plan is the clause of the same number in ADR-0094, ADR-0095 or ADR-0096. Step 1's stop rule stands: if Claude Code's deny does not stop an edit, ADR-0095 §3 returns to the owner.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| Claude Code's `deny` on an edit tool stops the write, and the model reads the reason | 4, 5, 9 | step 1 (b), step 5 |
| `ask` on an edit is honoured in `-p` and interactively, per client | 4, 5 | step 1 (b), (e); interactive needs a terminal, else a register row |
| Cursor has a pre-edit event whose deny is honoured | 4, 5 | step 1 (e) |
| `CLAUDE_CODE_SESSION_ID` is in the Bash tool's environment | 3, 8 | step 1 (d); else the `--chat` flag |
| The variable stays, undocumented, in later Claude Code releases | 3 to 9 | not settleable here (ADR-0088) |
| The shim exits when its client dies | 2, 6, 9 | step 1 (f) |
| A warning given as context reaches the model | 4, 9 | step 1 (b) |
| Agents take the way out when refused | 5, 9 | one run per client; not a measurement |
| 120 minutes of idleness suits the owner's chats | 2 | the owner's use |
| The Cursor IDE behaves as cursor-agent | 4, 5 | the owner's trial |
| Linux | all | not run (macOS only) |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | [contract] B1 (blocking): Q1, Q2 change ADR-0088 §1, §4, ADR-0083 §1, ADR-0089 §1, ADR-0010 without naming them | Q2's Supersedes gains ADR-0088 §1, §4; Q1's gains ADR-0083 §1, ADR-0089 §1, ADR-0010; Q1 §9 no longer cites ADR-0088 §4 as unchanged |
| 1 | revise | [safety][bug] B2 (blocking): a joined CLI connection would hold devices through `Origin.ID`; `checkWrite` had no way to read the hello chat | step 3: `Origin.ID` stays the connection's, `Origin` gains `Client`, `Chat`, `Joined`, read only for attribution, `Seen` and `checkWrite`; step 6 holds by `Client == mcp` and `ID`, with a joined CLI `device.install` row seen failing with `Joined` read; `droidyard call` dropped from the join |
| 1 | revise | [contract][test] B3 (blocking): the added sentence breaks ADR-0036 §1 (2 sentences, 400 characters) | step 2: the release folded into the description's first sentence (about 393 characters), `TestDescriptions` in the Check |
| 1 | revise | [bug] two live sessions of one chat; [test] nested worktree, new folder, `/tmp` spelling; [test] the race aimed below `enter.go` | own = same session or same chat, rows in steps 2 and 4; longest held root wins, canonical by nearest existing ancestor, three rows; one `Occupy` deciding and binding under the lock, the race driven through `workspace.enter` with an idle-takeover row |
| 1 | revise | [contract] seven, not eight, codes lists; wipe and start have no preamble; the registry's injection; [plan] the restore's `Forget` in `mcp.go`, a hello inside a held worktree; owner 4's limits | step 6: seven, wipe checked in its handler before `stopSerial`, start holds at resolution, an injected interface, a wipe row; step 2 names `mcp.go`'s restore and the hello row; Q1 §3, §4 state the limits, register rows at step 9 |
| 1 | revise | [plan] cuts: edit-row collapse, `droidyard call`, size; nits: Activity filters, holder naming, cost per edit, main-from-main releases devices, ADR-0082's rejected alternative answered | collapse cut (step 7's Not here), `call` cut, Q1–Q3 shrink to links once the ADRs exist; all nits taken (steps 2, 3, 6; Q1 Alternatives; Q2 §3) |
| 2 | approve | B1, B2, B3 resolved | — |
| 2 | approve | [test] step 3's joined-`workspace.enter` row named a mutation that cannot fail (`Registered(Origin.ID)` still refuses) | the mutation is now `Origin.ID` set to the joined session with the client check removed |
| 2 | approve | nits: the test is `TestDescriptionsFollowTheRule`; Q1's ADR-0088 §3 line omits the kept memory; Q1 §9's reference | all taken |

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.
