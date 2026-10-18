# ADR-0095: A hook is joined to its chat's session, and the agent's own edits pass through the hooks

- Status: Accepted
- Date: 2026-10-18
- Supersedes: ADR-0088 §Decision (in part: §1, the hook and `droidyard trash` also send `chat`; §4, a chat id also identifies an occupation's holder); ADR-0085 §Decision (in part: §1's root, refined in §5 below); ADR-0063 §Decision (in part: §7, `init` also writes the edit-tool entries); ADR-0048 §Decision (in part: §1, `session` on a call joined to an MCP session); ADR-0062 §Decision (in part: §4, the replacement names `--chat` when the shell has no chat id); ADR-0060 §Consequences (in part: an agent's edits are recorded)
- Relates to: ADR-0061, ADR-0081 §2, ADR-0082, ADR-0094; `docs/research/spike-c2-sessions-2026-10.md` (c); plan m2s Q2
- Decided by the owner, 2026-10-18: an agent's own edits are recorded in Activity under its session and refused only in a worktree another live session occupies (ADR-0094); idleness needs hooks joined to their session. The join's shape and the hooks' place rule were drafted by the planner and decided by the main session under the owner's standing instruction, Delegated.

## Context

ADR-0060 took the file tools off MCP: agents edit with their own tools, which bypass the
daemon, so their edits are not recorded, not contained and seen by the tree only by
polling. The hooks `init` writes see shell commands only (ADR-0061, ADR-0063), and each
dials as `client=cli` with no chat, so nothing ties a hook record to its chat's MCP
session (ADR-0088 §Alternatives considered). Claude Code gives the hook's `session_id` and
the MCP server's `CLAUDE_CODE_SESSION_ID` the same value [obs, spike C2]; cursor-agent
sets no chat id on its MCP server. ADR-0094 needs both: a writer's identity for its
verdict, and hooks that keep a working holder from going idle.

## Decision

1. **The join is the chat id.** A hook's connection names the event's chat (Claude Code
   `session_id`, Cursor `conversation_id`), as does `droidyard trash` run from Claude
   Code's shell, from `CLAUDE_CODE_SESSION_ID`, or from `--chat` where the shell does not
   carry it (plan m2s step 1 (d) settles which). `droidyard call` does not. A connection
   whose chat is a live MCP session's chat is joined to it, the most recently seen when two
   sessions share the chat. Cursor's hooks stay unjoined.
2. **A joined call gains attribution and freshness only.** It keeps the session from going
   idle (ADR-0094 §4), its operations carry the session's ref so Activity shows them under
   the agent, and `agent.checkWrite` reads it as the writer's identity. The call's own
   origin (its connection's id and kind) is unchanged: it moves no binding and holds no
   device.
3. **The agent's edits.** `init` adds, for each client, an entry before and after its edit
   tools. Before: `agent.checkWrite` gives ADR-0094 §5's verdict, answered as the client's
   deny, ask or pass within ADR-0061's 2 s, failing open. After: `agent.recordEdit` records
   the file under its place (a worktree or the main tree by §5; else the hook's root,
   marked outside the project, with the absolute path) and makes the tree walk at once.
   Both have audience human, the hook's channel. The record holds the client, the paths and
   created or updated, nothing of the content.
4. **What is not covered**: `ifRevision` needs a precondition on the write and the client's
   tool writes with none; a shell command's writes cannot be read lexically (ADR-0063 §1).
5. **The hook's place** refines ADR-0085 §1: with `--root` a linked worktree, its main
   tree counts as the project too; the upward walk passes a `.git` that is not a linked
   worktree of the project and goes on; and, if plan m2s step 1 (c) shows Claude Code
   reports the project's directory after `cd <dir> && …`, a leading `cd <dir>` gives the
   command's directory.

## Consequences

- Capabilities `agent.checkWrite` and `agent.recordEdit` (audience human); `Origin` gains
  the client, the chat and the join.
- Each edit starts two short processes and records three operations (`agent.checkWrite`,
  `workspace.open`, `agent.recordEdit`), as ADR-0063's Consequences count for commands.
- Cursor's hooks stay unjoined, and `ifRevision` and shell writes stay open: register rows.
- The join rests on `CLAUDE_CODE_SESSION_ID`, undocumented for MCP servers (ADR-0088).
- Documents that change with it: `mcp.md` §Features beyond tools, `files.md` (an agent's
  edits recorded).

## Alternatives considered

- **Joining by the client process** (the parent chain of the shim and of the hook): one
  Cursor process carries many chats, and a pid can be reused.
- **A `PreToolUse` hook on every droidyard MCP tool, matching Claude Code's `toolUseId`**:
  a hook process per MCP call, for subagent identity only (ADR-0094 §8).
- **Recording edits from the file watch** (a change the daemon did not make is an
  agent's): it cannot say whose, and the user's own editor is not an agent.
- **The verdict computed in the hook from `workspace.describe`**: two places for one rule,
  and the hook cannot see the join.
- **Widening `agent.recordCommand` to carry files**: one capability with two record shapes,
  and every rendering would branch on which.
- **ADR-0037's shell redirect and session-start context**: still rejected; these entries
  observe and gate the client's edit tools, neither of those.
