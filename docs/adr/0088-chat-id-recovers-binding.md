# ADR-0088: A session carries its chat's id, and a resumed chat gets its worktree back

- Status: Accepted
- Superseded in part by: ADR-0095 (§Decision)
- Superseded in part by: ADR-0094 (§Decision)
- Superseded in part by: ADR-0127 (§Decision 4)
- Date: 2026-10-18
- Supersedes: ADR-0082 §Decision (in part: §1's "a resumed chat is bound to the main tree again", and §5's "never persisted" gains a memory by chat id for the daemon's life)
- Relates to: ADR-0019 §7 (the hello), ADR-0048 §2, ADR-0083 §1; `docs/research/spike-c2-sessions-2026-10.md` (a), (b); plan m2q step 1
- Decided by the owner, 2026-10-18: a session tied to its chat, and a resumed chat back in its worktree while the daemon lives, are in 2q. Tying hook records to the session is not. The shape is Delegated.

## Context

ADR-0082 §1 takes a session to be one MCP connection, and a resumed chat comes back on a new connection bound to the main tree. Step 1's spike observed that Claude Code 2.1.302 sets `CLAUDE_CODE_SESSION_ID` in the MCP server's environment. That value is the chat's own id: it is the same in the stream's `init` and in every hook line, and it is unchanged by `--resume`, which does open a new connection. cursor-agent sets no such variable. The variable is not documented as an interface for MCP servers [assumption: it can change or vanish with a release].

## Decision

1. **The hello carries `chat`**, an optional string. `droidyard mcp` fills it from `CLAUDE_CODE_SESSION_ID` when the value matches `^[A-Za-z0-9-]{1,64}$`, and leaves it empty otherwise. Nothing else sets it.
2. **The core's session registry keeps each chat's last binding**: chat id → workspace root, for the daemon's life. It is never written to disk and is bounded to 256 chats; the oldest is dropped first (working default).
3. **A new session whose `chat` has a remembered binding is bound there at connect**, before `initialize` or `server/discover` is answered. Two conditions apply: the root must still be the session's project root or a linked worktree of it (ADR-0082 §3's rule, checked again now), and its directory must still exist. Otherwise the session binds as ADR-0019 §Sessions says, and the memory entry is dropped. Being bound grants no trust.
4. **A chat id grants nothing more than `workspace.enter` would.** A local process that presents another chat's id gets at most a binding inside its own project, which it could have entered itself. The id is not published: `session.watch` does not carry it (ADR-0048 §2's rule for session ids).
5. Without the variable (cursor-agent, another client, a future Claude Code), ADR-0082 holds as written: a resumed chat binds to the main tree and enters again.

## Consequences

- `internal/daemon` `Hello` gains `chat`. The hello is not part of `schema/capabilities.json`, so the published contract does not change.
- A resumed Claude Code chat keeps building in its worktree with no new `workspace_enter`. After a daemon restart the memory is gone, and the chat enters again.
- Tying a hook record to its session through the hooks' `session_id` stays open, with a register row.
- `docs/interfaces/mcp.md` §Transport and session names the variable and the fallback.

## Alternatives considered

- **Keep ADR-0082 §1 as written** (resume binds to the main tree). The owner wants a resumed chat to stay where its task is, and the agent may not re-enter before its first build.
- **Persisting the memory across daemon restarts.** A file of chat ids and paths that outlives every chat, and a stale entry could point a resumed chat at a worktree reused for another task. Live memory covers the common case: a chat resumed the same day.
- **Taking the chat id from MCP `_meta`.** Claude Code 2.1.302 sends no chat id there (only `claudecode/toolUseId`), and Cursor sends no `_meta` at all.
- **Taking it from the hook's `session_id`.** No link joins a hook process to an MCP connection: that join is what this ADR's id would make possible, and it is left for later.
