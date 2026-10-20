# Research: subagents in other worktrees, and the connection they inherit (2026-10)

- Asked by the owner, 2026-10-19: what happens when a session working in a worktree launches
  subagents that work in other worktrees, in Claude Code and in Cursor; whether Orca does
  anything about it; and several solutions, beyond what had been discussed, so that a subagent
  working on its own in a worktree is not limited and nothing acts on the parent's worktree only
  because the subagent inherited its state.
- **The owner's choice, 2026-10-19: none of the ideas for now** (idea G below): the main session
  builds and tests, the subagents only edit. This note keeps the rest for later. **None of the
  ideas in §5 is decided**; each is inspiration to adapt or drop when the work is chosen.
- Method: the main session read lane 2u's transcript (2026-10-19, two implementers launched
  into hand-made worktrees), Claude Code's documentation (worktrees, subagents, the
  `EnterWorktree` tool's description, read 2026-10-19), Cursor's (subagents, worktrees), the
  Orca notes of 2026-10, and the MCP wire recorded in lane 2t's step 9. Nothing new was run.
- Labels: [obs] seen in a Droidyard run or transcript; [doc] in the client's documentation;
  [read] in Orca's code or docs, through [orca-2026-10-agents.md](orca-2026-10-agents.md);
  [inferred] is judgement. Every open point has a register row (Source: this note).

## 1. What happened in lane 2u [obs]

The main session had entered its lane worktree `m2u` with `EnterWorktree`. For two parallel
steps it made `m2u-s2` and `m2u-s6` with `git worktree add` and launched one implementer in
each, without `isolation`. Claude Code refused every write there: "This session is isolated in
the worktree …/m2u, so its writes are limited to that folder", and every Bash command that
`cd`'d there before git, or used a variable where git could stand. One implementer tried
`EnterWorktree` to `m2u-s6` and was refused ("would not give this agent write access there").
The session then ran both steps in `m2u` at once, each in its own files. Nothing was lost; the
steps were no longer isolated.

## 2. The client layer

**Claude Code** [doc]:

- "When the main conversation itself runs isolated in a worktree, Claude Code applies the same
  checks to the session and to every subagent it spawns, including subagents without
  `isolation: worktree`." The checks: edits to, commands run in, and git redirected into the
  main checkout, and any command whose git it cannot verify from the text.
- `EnterWorktree` with a path switches "agents whose working directory was pinned at launch
  (subagent isolation or explicit cwd)", and "the switch only affects this agent". 2u's
  implementers were not pinned, which fits their refusal [inferred].
- `isolation: worktree` makes the subagent's worktree from `worktree.baseRef`: `fresh` (the
  default) is the remote's default branch, `head` the current `HEAD`, which inside a worktree is
  that worktree's. The project sets no `baseRef`, and `milestone-workflow` §Rules names
  `isolation: "worktree"` for parallel steps, so from a lane such a subagent would start without
  the lane's commits [inferred]. Not tried: whether a subagent with `isolation: worktree`
  launched from an isolated session writes its own worktree, and where that worktree lands.
- A subagent's `mcpServers` frontmatter takes inline servers, "connected when the subagent
  starts … and disconnected when it finishes", or names that "share the parent session's
  connection". The working directory an inline server starts in is not documented.

**Cursor** [doc]: subagents "share the parent agent's checkout by default"; on request each runs
"an isolated Git worktree with a separate working directory", and its changes stay on its branch
"until the parent agent merges the results". "Subagents inherit all tools from the parent,
including MCP tools from configured servers" (cloud subagents use the team's). A custom
subagent's frontmatter has `name`, `description`, `model`, `readonly`, `is_background`: no MCP
field.

## 3. The Droidyard layer

- One MCP connection is one session, bound to one root (ADR-0019 §Sessions, ADR-0094 §8). A
  subagent rides its parent's connection in both clients, so its build, tests and installs act on
  the parent's worktree, its `workspace_enter` moves the parent and releases what the parent
  occupies, and siblings share one occupation: no `WORKSPACE_OCCUPIED` or `DEVICE_OCCUPIED`
  between them. Its hooks' writes are its chat's own
  ([spike-c2-occupation-2026-10.md](spike-c2-occupation-2026-10.md)).
- **Each Claude Code `tools/call` carries `_meta."claudecode/toolUseId"`** [obs: lane 2t step 9's
  `wire.c2s.jsonl`, Claude Code 2.1.303], beside `clientCapabilities.roots.listChanged: true`. A
  `PreToolUse` hook's input carries `tool_use_id`, `agent_id` (in a subagent only) and `cwd`
  [doc, through the Orca annex §2; `cwd` is the subagent's worktree, obs in the spike above]. So a
  hook on the droidyard tools and the MCP call it precedes can be matched exactly [inferred].
  ADR-0095 rejected a hook process per MCP call for subagent identity before this was known.
- What Cursor's MCP calls and its `beforeMCPExecution` hook carry for a subagent is not known.
- **Nothing Droidyard says to an agent warns a subagent**, and what it says leads one into the
  problem: the server instructions ([mcp.md](../interfaces/mcp.md) §Server instructions) and the
  `droidyard-android` skill (§Development in a worktree) tell every agent that a task changing
  code gets a worktree and `workspace_enter`s it, and that the builds that follow act on it. A
  subagent that follows them moves its parent [read: the text at 2026-10-19, `main` 19f6025].
  Option G holds in this repository because its implementers do not use the droidyard tools.

## 4. Orca [read]

- Orca joins a hook to a pane by the environment it injects into the pty (`ORCA_PANE_KEY`,
  `ORCA_WORKTREE_ID`). Subagents inherit it: they are child rows of their parent (from
  `SubagentStart`/`SubagentStop`), and nothing is routed per subagent.
- Orca met the inherited-state problem itself (#9236): a background session inherited the
  dispatching pane's environment, so its events named a pane it did not run in. Its answer was
  to skip hooks when `CLAUDE_JOB_DIR` is set: it silences the inheritance, it does not resolve it.
- For parallel work Orca does not use subagents: its orchestration launches each worker as an
  agent of its own, in its own worktree and pty, under a worker contract (`heartbeat`, `ask`,
  `worker_done`). [orca-2026-10.md](orca-2026-10.md) keeps that under "Not to copy" (a harness,
  ADR-0004).

## 5. Ideas, none decided

| | Idea | A subagent on its own | Cursor | Cost, and what would have to change |
| --- | --- | --- | --- | --- |
| A | Say it: the server's `instructions`, the skill and the agents tell a subagent that the connection is bound to the parent's worktree and not to `workspace_enter`; each workspace-scoped result names the root it acted on | no; surprises avoided | yes | small; relies on the model |
| B | Refuse: a hook on the droidyard tools (`PreToolUse`, Cursor's `beforeMCPExecution`) denies a call whose `cwd` is another worktree than the connection's, and says what to do | no; never the wrong worktree | likely, to check | small; a hook process per droidyard call |
| C | A `worktree` argument on workspace-scoped tools, limited to worktrees of the bound project | yes, if the model passes it | yes | supersedes ADR-0019 in part (agents never pass a workspace); without B a forgotten argument acts on the parent |
| D | C without the model: the hook fills `worktree` from its `cwd` (Claude Code's `updatedInput`), and adds the `agent_id` | yes | to check | `updatedInput` on an MCP tool is not verified |
| E | Route by `toolUseId`: the hook tells the daemon which agent and `cwd` a call comes from, and the daemon keeps a sub-session per subagent with its own binding and occupation | yes, siblings told apart | not until Cursor's equivalent is known | medium; supersedes ADR-0095 and ADR-0094 §8 in part |
| F | A connection per subagent: Claude Code's inline `mcpServers` in our agents, so each subagent has its own session, occupation and release (ADR-0094's lifetime) | yes | no | only defined agents, not ad-hoc ones; if the server does not start in the worktree, the subagent `workspace_enter`s on its own connection |
| G | The main session builds and tests; subagents only edit (today) | no | yes | none: **the owner's choice, 2026-10-19** |
| H | Workers as real sessions, as Orca's orchestration: Droidyard makes the worktree and starts `claude -p` or `cursor-agent` there with its own `--root` | fully | yes | large; C2's deferred terminal row, and against ADR-0004 |

[inferred] When the work is taken up: A and B are cheap and stop the unwanted effect; D or E give
Claude Code subagents their autonomy, exactly because of the `toolUseId`; F is an alternative
for our own agents; Cursor stays on B and C until a spike says what it sends.

## 6. To investigate when built

- Whether `updatedInput` applies to an MCP tool in Claude Code, and whether Cursor's
  `preToolUse` or `beforeMCPExecution` can change an MCP call's arguments.
- The working directory of an inline `mcpServers` server for a worktree-isolated subagent.
- What a Cursor subagent's MCP calls and hooks carry: a subagent id, its worktree as `cwd` or
  `workspace_roots`.
- Whether a subagent with `isolation: worktree` launched from an isolated session writes its own
  worktree, and where it is made (under the lane's worktree or the main tree's
  `.claude/worktrees/`).
- The latency a hook adds to each droidyard call.
- Whether Claude Code sends `notifications/roots/list_changed` for a subagent.
