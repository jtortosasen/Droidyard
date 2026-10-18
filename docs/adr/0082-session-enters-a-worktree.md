# ADR-0082: A session enters a worktree of its project; a worktree is a workspace of its project

- Status: Accepted
- Superseded in part by: ADR-0088 (§Decision)
- Date: 2026-10-18
- Supersedes: ADR-0019 §Sessions (in part: the binding moves within its project through `workspace.enter`); ADR-0010 §Decision (in part: a stateless capability may act on the calling MCP session's binding)
- Relates to: ADR-0004, ADR-0020 §Scope, ADR-0021 §Layout, ADR-0036, ADR-0048 §1, ADR-0060 §2, ADR-0071, ADR-0075, ADR-0081; `docs/vision/beyond.md` (C2); plan m2q Q2
- Decided by the owner, 2026-10-17:
  - a session is a chat;
  - a chat that does no development creates nothing and works on what the project has checked out;
  - the agent's workflow decides whether a task is new, then reuses or creates a worktree for it;
  - the UI never moves a session;
  - a worktree is another root of the same project, under one daemon;
  - on 2026-10-18, owning a worktree is not exclusive.

  The shape (`workspace.enter`, the `binding` flag, the registry) is Delegated.

## Context

ADR-0019 §Sessions binds an MCP session to one root, found from the shim's directory, for the session's life. A chat that takes a development task moves into a worktree halfway through, so its builds and tools must follow it there. Droidyard has no agent harness (ADR-0004): the chat and its context are the assistant's. Making a worktree is a git write that every agent and client already does with its own tools (ADR-0060 §2).

## Decision

1. **A session is one MCP connection**, which is Droidyard's view of a chat. Three cases are limits, each a register row:
   - A client that carries several chats on one connection makes them one session.
   - A subagent on its parent's connection is part of it, so a worktree-isolated subagent that enters its worktree moves its parent.
   - A resumed chat is a new connection, bound to the main tree again; the tool's description says to enter again.
2. **The agent's workflow creates or reuses the worktree**, with its own git or its client's worktree feature, wherever that puts it. The skill states the rule: reuse the worktree whose branch names the task, else create one. Droidyard creates, names and deletes no worktree.
3. **`workspace.enter {root}`** (command, global, stateless, audience all, idempotent) rebinds the **calling** session to `root`. `root` is absolute and must be either the session's project root (the main tree of its hello's root) or a linked worktree of that main tree by ADR-0081 §2. The call opens the workspace if it is closed, grants no trust, and returns the workspace. Any other root, including a genuine worktree of another repository, fails `WORKSPACE_NOT_A_WORKTREE`. No parameter names another session, so neither the UI nor another client can move one.
4. **The `binding` descriptor flag**: "acts on the calling MCP session's binding". The binding is ADR-0019's addressing, which the MCP session already holds, not session-owned state that needs a push channel, so `workspace.enter` stays `stateless` and remains an MCP tool. Start refuses a `binding` capability from a connection that has no binding (the WebSocket, the CLI, plain HTTP) with `REQUEST_NOT_ALLOWED_HERE`, and creates no operation. That code's meaning widens to "a capability this connection may not call".
5. **The binding lives in the core's session registry**, which the MCP adapter reads on each call, so ADR-0048's `session.workspace` follows a move. The binding ends with the connection and is never persisted.
6. **A worktree is a workspace**, with its own root, id (ADR-0021), Gradle root (ADR-0075), builds, watches and state under `ws/<wsid>/`. That is the "context per worktree" Droidyard keeps; the chat's own context stays the assistant's. A worktree belongs to its project: `workspace.open`, `describe`, `watch` and `workspace.state` carry `mainRoot`, and the index entry keeps it as an additive key. `workspace.watch` leaves out a worktree whose directory is gone. A worktree is never shown as a project of its own.
7. **Owning a worktree is being bound to it, not exclusively.** Two sessions may be in one worktree, for example when a new chat continues the same task; builds in it queue as in any workspace.

## Consequences

- `workspace.enter`, the `binding` flag, `WORKSPACE_NOT_A_WORKTREE` and `mainRoot` enter the contract (schema, one minor version for milestone 2q). The tool joins the core profile within ADR-0036's budget.
- The MCP instructions' first line and the Android skill say how development moves into a worktree. `docs/interfaces/mcp.md` §Transport and session, `workspace-state.md` §What a workspace is, the glossary's Session and `overview.md`'s binding sentence follow this ADR.
- Whether an agent actually enters before it builds depends on its following the skill. That is unmeasured and has a register row.

## Alternatives considered

- **A capability that creates the worktree** (`workspace.acquire {task}` running `git worktree add`). It puts a git write on the agent surface (ADR-0071 keeps the daemon's git writes human-only) for something every agent and client already does.
- **A `droidyard worktree` CLI command.** The same write, through the shell.
- **Adopting the worktree from the hook's `cwd`.** Nothing links a hook process to an MCP connection, and Cursor's hook carries no `cwd`.
- **MCP roots as the binding source.** ADR-0019 rejected them: a binding must be deterministic and visible.
- **A second MCP server per worktree** (`--root <wt>`). A chat would need a new server in the middle of the chat.
- **A `root` argument on every tool.** Tokens on every call and every schema (ADR-0036).
- **The worktree as a second root of one workspace** (like ADR-0074's submodule roots). Every capability would take a root, and two worktrees could not build at once, since the queue is per workspace.
- **`workspace.enter` declared `stateful`.** ADR-0010 keeps stateful capabilities off MCP, which is the one consumer that needs this one.
- **An exclusive owner per worktree.** A second chat on the same task would be refused the worktree its task lives in. The owner chose non-exclusive (2026-10-18).
