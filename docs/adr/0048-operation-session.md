# ADR-0048: `session` on the operation events, for the agents panel

- Status: Accepted
- Superseded in part by: ADR-0095 (§Decision)
- Superseded in part by: ADR-0083 (§3)
- Superseded in part by: ADR-0098 (§1)
- Date: 2026-10-10
- Supersedes: ADR-0043 §2 (in part: the payload names the calling session)
- Relates to: ADR-0010, ADR-0015 §1, ADR-0036, ADR-0043 §1, §3, ADR-0047; plan m2d Q2
- Decided by the owner, 2026-10-10: the projects panel shows one row per agent session
  with what it is running, as Orca shows its agents. The shape below is Delegated (plan m2d
  Q2, reviewed in two rounds).

## Context

`operation.state` carries `client`, a kind (`cli`, `mcp`, `ui`): two Cursor windows are
both `mcp`, so the UI cannot tell agents apart. Each adapter already holds a session id,
and an MCP client names itself in `initialize`'s `clientInfo`.

## Decision

1. `operation.state`'s payload and `operation.watch`'s rows gain an optional
   `session {id, name?, workspace?}`: `id` the daemon session's opaque id; `name`, for
   MCP, `clientInfo.name` (at most 64 runes, control and format characters removed,
   absent before `initialize`); `workspace` the session's bound workspace while open.
   Omitted for a call recorded without a session (plain HTTP, the artifact route,
   internal calls).
2. A session id grants nothing: no capability takes one. Not untrusted: a local client
   names itself, and no device or project text is carried.
3. Rows come from operations, so a connected agent that has run nothing since the tab
   loaded is not listed. Streaming, so no MCP change; `0.16.0` → `0.17.0`.

## Consequences

- The panel names each agent and its running operations; idle agents wait for the agent
  sessions milestone, where the daemon hosts the sessions.

## Alternatives considered

- **A `session.watch` capability** (connected sessions and their open and close): lists
  idle agents, but a second stream and lifetime for a panel the agent sessions milestone
  rebuilds; it wins there.
- **Group by client kind**: merges two agents.
- **A target summary on the event**: ADR-0043 rejected parameters on events.
