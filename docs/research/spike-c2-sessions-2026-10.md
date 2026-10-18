# Spike: what a chat is to Claude Code and cursor-agent (m2q step 1)

Run 2026-10-18 by the main session, macOS (Darwin 25.6.0), against an isolated daemon built from
`m2q` (`$R` = `/private/tmp/dy2q.Kgmn`, evidence in `$R/evidence`). The scratch repository was a copy
of `samples/golden` with one commit (`~/Library/Caches/droidyard-dev/2q/repo`). The shim ran behind
a recorder that kept each connection's wire, its cwd, its environment's variable names and three
id values. A hook recorder kept the after-command events, with `user_email` removed. Clients:
Claude Code 2.1.302 (`claude -p --model sonnet`, `--strict-mcp-config`) and cursor-agent
2026.10.01-e373342 (`-p --trust`, model `cursor-grok-4.6-high`, scratch `CURSOR_DATA_DIR`).

`[obs]` means seen on the wire or in a hook line. `[doc]` means read in a client's or library's
source or documentation. "Not observed" means it was not run.

## (a) Claude Code

- **One connection per `claude -p` run** [obs]. Runs a1 to a4 each started one shim process.
- **A subagent rides its parent's connection** [obs]. In a1, the parent's `system_health` (id 1)
  and a general-purpose subagent's `workspace_describe` (id 2) went over the same connection.
- **A worktree-isolated subagent does too** [obs]. In a2 (`isolation: "worktree"`) no new
  connection was opened, so its MCP calls went over the parent's binding. Its hook lines carry
  `agent_id` and a `cwd` in the subagent's worktree, `repo/.claude/worktrees/agent-<id>`. A
  `workspace_enter` from such a subagent would move its parent, as ADR-0082 §1 says.
- **Protocol `2026-07-28`, with no `initialize`** [obs]. The client opens with `server/discover`
  (id `server-discover-probe-1`), then `tools/list` and `tools/call`. Every request carries `_meta`
  with `io.modelcontextprotocol/protocolVersion`, `clientInfo` (`claude-code`, 2.1.302),
  `clientCapabilities` (`roots.listChanged`, `elicitation`), and on `tools/call` also
  `claudecode/toolUseId` and `progressToken`. mcp-go 1.1.1 answered every request, and it sets the
  session's client info from `server/discover` as well as from `initialize` [doc:
  `server/discover.go:113`]. No `roots/list` was exchanged.
- **The MCP server's environment carries the chat id** [obs]. `CLAUDE_CODE_SESSION_ID` in the
  shim's environment equalled the run's `session_id`, in the stream's `init` and in every hook
  line, in a2, a3 and a4. It is not documented as an interface for MCP servers [assumption: it may
  change without notice]. `CLAUDE_PID` was inherited from the session that launched the runs, so it
  says nothing about the run.
- **`--resume` opens a new connection with the same chat id** [obs]. a3 resumed a2's chat: a new
  shim, `CLAUDE_CODE_SESSION_ID` unchanged (`ce634906…`), and the shell's cwd back at the
  repository root, not at the worktree a2 had `cd`'d into.
- Interactive `/clear`: not observed (no terminal).

## (b) cursor-agent

- **One connection per run** [obs], with classic `initialize` (`protocolVersion` `2025-11-25`,
  `clientInfo` `Cursor` 1.0.0) and no `_meta` on requests.
- **No chat id reaches the MCP server** [obs]. No `CURSOR_*` or session variable in its
  environment, and nothing on the wire.
- **`--resume` opens a new connection** [obs] (b2), and the hooks' `conversation_id` /
  `session_id` stay the same (`091250ff…`). `sessionStart` fired on the first run only.

## (c) The hooks' input

- **Claude Code `PostToolUse` carries a physical `cwd` that follows `cd`** [obs]. In a2, after
  `cd ../wt-a2` the events of that command, `pwd` and `git status` had `cwd`
  `…/2q/wt-a2`. Every event carries `session_id`, the chat id, stable through the chat and across
  `--resume`. A subagent's events also carry `agent_id` and `agent_type`.
- **Cursor `postToolUse` and `beforeShellExecution` carry `cwd` `""`**, and `tool_input.cwd` is
  `""` too, even after `cd ../wt-b1`, which `pwd` then printed [obs]. They carry
  `conversation_id` = `session_id` = `generation_id`, `workspace_roots`, and the user's email
  (removed from the evidence). So Cursor gives the hook no directory: ADR-0085 §2's fallback
  applies.

## (d) Claude Code's own worktrees

- `claude -w <name>` makes `repo/.claude/worktrees/<name>` on branch `worktree-<name>` and starts
  the MCP server **with its cwd in that worktree** [obs] (a4). The session's hello is bound there
  from the start, and today the worktree opens as an unrelated project (`spike-a4-…`, `trusted:
  false`). The hook's `cwd` is the worktree. No `notifications/roots/list_changed` was sent.
- `EnterWorktree` (the in-session tool) was not exercised in `-p`. Not observed.

## What this settles for plan m2q

- Step 1's stop rule did not fire: no connection carried two chats. ADR-0082 §1 holds for both
  clients run here; the Cursor app is still the owner's trial.
- Step 6 sets the session's name from `server/discover` as well as `initialize`: Claude Code
  2.1.302 sends no `initialize`.
- Step 7 keeps Cursor on `--root` (no directory). Claude Code's `cwd` is the physical path and
  follows `cd`.
- New, and not in the plan: through `CLAUDE_CODE_SESSION_ID`, Claude Code gives Droidyard the
  chat's id on the MCP connection and in every hook line, stable across `--resume`. A resumed
  chat could recover its binding, and a hook record could be tied to its session. Cursor gives
  the id to hooks only.
