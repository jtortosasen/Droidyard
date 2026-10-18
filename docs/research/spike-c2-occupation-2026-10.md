# Spike: what the hooks see of an agent's edits, and what each client does with the answer (m2s step 1)

Run 2026-10-18 by the main session, macOS (Darwin 25.6.0), against an isolated daemon built from
`m2s` at `88c26a1` (`$R` = `/private/tmp/dy2s.gKln`, evidence in `$R/evidence`, hook lines in
`~/Library/Caches/droidyard-dev/2s/hooklog/hooks.jsonl`). The scratch repository was a copy of
`samples/golden` with one commit (`~/Library/Caches/droidyard-dev/2s/repo`) and a linked worktree
beside it (`../wt`, branch `t`). Every hook entry ran a recorder that kept the event (`user_email`
removed), its environment's variable names, whether `CLAUDE_CODE_SESSION_ID` was set and equal to
the event's id, and its parent chain; it answered from a file, filtered by path and tool. The shim
ran behind a recorder that kept its cwd and environment. Clients: Claude Code 2.1.302
(`claude -p --model sonnet`, `--strict-mcp-config`, `--allowedTools` naming the edit tools) and
cursor-agent 2026.10.16-e3cb207 (`-p --trust`, model `cursor-grok-4.6-high`, scratch
`CURSOR_DATA_DIR`, `cli.json` allowing `Write(**)`).

`[obs]` means seen in a hook line, on the wire or on disk. "Not observed" means it was not run.

## (a) Claude Code's edits

- **`Write` and `Edit` fire `PreToolUse` and `PostToolUse`; `tool_input.file_path` is absolute**
  [obs, run a1], for the main tree, `../wt`, and `/tmp` alike. 2.1.302 offers no `MultiEdit` (it
  is absent from the init event's tool list).
- **Create versus update:** `Write`'s `tool_response.type` is `create` or `update`; `Edit`'s
  response has no `type` (`oldString`, `newString`, `originalFile`) and is always an update [obs].
- **`cwd` is the session's directory**, not the file's [obs]: the edits to `../wt` and `/tmp` came
  with `cwd` = the repository.
- **Subagents' events carry `agent_id` and `agent_type`** and the parent's `session_id` [obs]. A
  worktree-isolated subagent's worktree is `repo/.claude/worktrees/agent-<id>`, inside the main
  tree, and its events' `cwd` is that worktree.
- **A `-p` run wrote outside its directory without `--add-dir`** (`../wt`, `/tmp`) [obs]: with
  `Write` in `--allowedTools`, Claude Code did not refuse it.

## (b) Claude Code's answers on an edit

- **`deny` stops the write, and the model reads the reason** [obs, b1]: the file was not written,
  the tool result was `PreToolUse:Write hook error: <reason>`, and the model followed the reason's
  way out (it wrote the file under the directory the reason named). Step 1's stop rule did not
  fire.
- **`ask` under `-p` is a refusal** [obs, b2]: no prompt, the file not written, the tool result the
  reason alone; the model did not try another route. Interactive `ask`: not observed (no terminal).
- **`allow` with `additionalContext` writes and the context reaches the model** [obs, b3]; so does
  `PostToolUse`'s `additionalContext` [obs, b4]. Both models quoted the context verbatim.

## (c) The shell's directory

- **`cd ../wt && <cmd>` as one command: `cwd` is the repository** in both `PreToolUse` and
  `PostToolUse`, though the command ran in `wt` (its `pwd` printed `wt`) [obs, c1]. Without
  `--add-dir`, Claude Code resets the shell to the repository after every command outside it, so a
  plain `cd ../wt` does not persist. Row 220's inference holds: the leading `cd` must be read.
- **With `--add-dir ../wt`, a `cd ../wt` persists and later events' `cwd` follow it** [obs, c2].
- **`claude -w t1`** made `repo/.claude/worktrees/t1` and started there: the shim's cwd was that
  worktree, so the hello binds it, and `workspace_describe` answered its root [obs, c3]. **No hook
  fired in that run**: the worktree was made from the commit, and `.claude/settings.json` (where
  the hook entries live) was untracked, so the worktree had none [obs]. The isolated subagent of (a)
  and `EnterWorktree` below did fire hooks: their settings came from the session's start directory.
- **`EnterWorktree` is offered under `-p`** [obs, c4]. After it, the hooks' `cwd` and the edits
  were in `repo/.claude/worktrees/ew1`, but no new MCP connection was opened and
  `workspace_describe` still answered the main root [obs]: the MCP binding does not follow it until
  the agent calls `workspace_enter` (row 234).

## (d) The chat id

- **`CLAUDE_CODE_SESSION_ID` is in the Bash tool's environment**, equal to the hooks' `session_id`
  and to the shim's [obs, c1, c3]. A `droidyard trash` run by Claude Code can carry its chat; no
  `--chat` flag is needed for it.
- **cursor-agent's shell carries `CURSOR_CONVERSATION_ID`**, equal to the hooks'
  `conversation_id` [obs, e1]; its MCP shim's environment carries no `CURSOR_*` variable and no
  `CLAUDE_*` one [obs], as m2q's spike saw. Cursor's hooks still cannot be joined to its session.
- **A client started from a Claude Code shell inherits that session's `CLAUDE_CODE_SESSION_ID`**
  [obs]. In this run cursor-agent's shell environment held the id of the Claude Code session that
  launched it. cursor-agent's MCP environment did not. A `droidyard trash` run from such a shell
  that read `CLAUDE_CODE_SESSION_ID` would name the wrong chat; `CURSOR_AGENT=1` marks the shell.

## (e) cursor-agent's edits

- **Its edit tool is `Write` for creates and edits alike**, with the whole content; `preToolUse`
  and `postToolUse` carry `tool_input.file_path`, absolute, and `afterFileEdit` carries
  `file_path` [obs, e1]. `cwd` is absent on these events; `workspace_roots` names the repository.
- **Before writing a file it reads it**, with its own `preToolUse` (tool `Read`) for the same path
  [obs]. A path-only rule would refuse that read; the verdict must look at the tool.
- **`deny` on `Write` is honoured, with and without `--force`** [obs, e6, e7, e8]. **The model sees
  `user_message`, not `agent_message`**, followed by Cursor's own "Agent note: Do not suggest
  workarounds to the blocked tool." With the way out only in `agent_message` the model gave up
  (e6); with it in `user_message` it took it (e7, e8).
- **`ask` is not honoured under `-p`: the write went through**, with and without `--force` [obs,
  e4, e5]. A Cursor `ask` must be answered as `deny` (ADR-0094 §6).

## (f) The end signal

- **`kill -9` of a running `claude -p` and of a `cursor-agent -p`: the shim exited within 0.06 s**
  [obs, f1, f2], the socket closing with it. The daemon's `ended` on `session.watch` was not
  observed in this run; that a closed socket ends the session is ADR-0082 §5's rule and m2q's tests.
- After every normal exit no shim was left [obs].
