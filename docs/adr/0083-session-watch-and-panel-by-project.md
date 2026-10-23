# ADR-0083: `session.watch`, and the sessions panel grouped by project

- Status: Accepted
- Superseded in part by: ADR-0094 (§Decision)
- Superseded in part by: ADR-0127 (§Decision 2)
- Date: 2026-10-18
- Supersedes: ADR-0048 §3 (in part: agent rows also come from `session.watch`); ADR-0047 §1 (in part: worktrees nest under their project)
- Relates to: ADR-0010, ADR-0045 §3, ADR-0048 §1–§2, ADR-0050, ADR-0052, ADR-0082; plan m2q Q3
- Decided by the owner, 2026-10-17:
  - one tab in all;
  - the sessions panel is split by project, with each project's sessions and their worktrees;
  - the UI follows a session and never moves it;
  - the Git side panel shows worktrees by session, read-only.

  The shape is Delegated.

## Context

The panel's agent rows come from operations (ADR-0048 §3), so an agent that has connected but run nothing is not listed. ADR-0048 named `session.watch` as the alternative that wins once agent sessions exist, and ADR-0082 now gives the core a registry of them. Worktrees opened as workspaces (ADR-0082 §6) would show as unrelated projects unless they are grouped under their project.

## Decision

1. **`session.watch`** (streaming, global, audience all; off MCP by ADR-0010) answers `{subscriptionId, sessions: [{id, name?, workspace?, connectedAt}]}`, oldest first, read after the subscription opens. Its events are `state` on a new ring-less topic `session.state`, with payload `{id, name?, workspace?, ended}`. One is published on connect, on the name learned from `initialize` (bounded as ADR-0048 §1), on every binding and at the end, in that order per session, to every subscription. Only MCP sessions are listed; a CLI call and the hook are one-shot and are not chats. Ids grant nothing (ADR-0048 §2).
2. **The panel**: for each project, its sessions (each naming its worktree, or main), then its worktrees that have no session. A worktree whose main tree is not listed sits under a group row for that main tree. A worktree is never a top-level row.
3. **The UI follows and never moves.** A selection made from a session's row follows that session's moves. A selection made from a project or worktree row stays where it is.
4. **The Git panel's worktree rows** name the sessions in each worktree and add no action.

## Consequences

- `session.watch` and the `session.state` topic enter the contract.
- The panel model reads two streams: `workspace.watch` for the projects and their worktrees, and `session.watch` for who is where. Operations still give the running state, the result and the unread mark.
- `web-ui.md`'s panel description follows this ADR.

## Alternatives considered

- **Rows from operations only, as today.** An idle connected agent stays invisible; that is the register row this closes.
- **Sessions inside `workspace.watch`'s rows.** It mixes two lifetimes in one stream: a session moves between workspaces and outlives none of them.
- **A selection that never follows.** It breaks the owner's "the UI follows".
- **A selection that always follows.** A user looking at main would be pulled away whenever any session moved.
