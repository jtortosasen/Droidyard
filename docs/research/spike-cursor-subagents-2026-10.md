# Spike: what a Cursor subagent's droidyard calls could carry (2026-10)

- Asked by the owner, 2026-10-19: whether idea E of
  [subagents-worktrees-2026-10.md](subagents-worktrees-2026-10.md) §5 (the daemon tells each
  subagent apart and gives it its own binding) can work in Cursor. Like that note, this one
  decides nothing.
- Method: four `cursor-agent -p --trust --output-format stream-json` runs (2026.10.16-e3cb207,
  model `cursor-grok-4.6-high`, scratch `CURSOR_DATA_DIR` with only the probe server enabled), in
  a scratch git repository. The MCP server was a 30-line Python probe that logs every message and
  echoes its arguments; a hook on `sessionStart`, `preToolUse`, `postToolUse`,
  `beforeMCPExecution`, `afterMCPExecution`, `subagentStart`, `subagentStop` and `afterFileEdit`
  logged its input, and on `preToolUse` for the probe tool answered `updated_input` with the
  arguments plus a `caller` field holding its `conversation_id`. The probe's schema declares
  `caller`. Evidence: `~/Library/Caches/droidyard-dev/e-cursor/log/r1` to `r4`. Droidyard was not
  run. The Cursor app was not tried.
- Runs: r1, the parent and one subagent each call the tool; r2, the same with the subagent asked
  for isolation (the model chose `subagent_type: best-of-n-runner`); r3, the parent started with
  `-w probe-wt`; r4, an isolated subagent that writes a file and calls the tool.

## Observed [obs]

1. **The subagent rides the parent's connection**: both calls of every run reached one probe
   process (one pid).
2. **No `_meta`**: Cursor's `tools/call` carries only `name` and `arguments` (also in the m1l and
   m1o wires), so a call cannot be matched to a hook by an id on the MCP side.
3. **A subagent's hooks carry its own `conversation_id`**, not its parent's (r1 to r4). The
   parent's `postToolUse` for `Task` returns `{"agentId": <that id>, "status": "success", ...}`,
   so the parent's hook can link parent and child.
4. **`preToolUse` fires for MCP tools** (`tool_name` `MCP:whoami`, with a `tool_use_id`, which
   `beforeMCPExecution` and `afterMCPExecution` also carried, though Cursor's documentation does
   not list it there), and **its `updated_input` reaches the server**: the probe received the
   `caller` the hook added, in the parent and in the subagent.
5. **`subagentStart` and `subagentStop` did not fire** in any run.
6. **No working directory per subagent**: `preToolUse`'s `cwd` was empty (and the shell tool's
   `cwd` argument); `workspace_roots` and the hook's own directory were the parent's.
7. **The isolated subagent got no worktree in `cursor-agent`**: in r4 the `best-of-n-runner`
   subagent wrote its file into the parent's checkout and reported that directory; `git worktree
   list` showed none made.
8. **A parent started with `-w`** runs with its worktree (`~/.cursor/worktrees/repo/probe-wt`)
   as `workspace_roots`, the hooks' directory and the MCP server's cwd (r3), and its subagent
   likewise.

## What it means for idea E [inferred]

- **Telling the callers apart works in Cursor**, by another road than Claude Code's: the hook
  writes the caller's `conversation_id` into each droidyard call through `updated_input`, so the
  daemon can keep a sub-session per conversation and link it to its parent, with no id to match.
- **The worktree does not come from Cursor**: nothing in the hook says where a subagent works,
  and `cursor-agent`'s subagents share the parent's checkout anyway. A Cursor subagent that makes
  a worktree with its own git would `workspace_enter` it, and with a sub-session that moves only
  it. In Claude Code the hook's `cwd` gives the worktree without that call.
- The argument the hook fills must be one every tool's schema declares: whether Cursor keeps an
  `updated_input` key the schema does not have was not tried.

## Not established

- The Cursor app (IDE and Agents window), where the documentation places subagent isolation.
- Whether Cursor drops an `updated_input` key the tool's schema does not declare.
- Whether the parent link holds for a background subagent (`run_in_background: true`).
