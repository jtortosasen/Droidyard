# ADR-0101: `operation.wait` and `operation.cancel` reach one's own operations

- Status: Accepted
- Date: 2026-10-18
- Supersedes: ADR-0020 §Operation capabilities (in part: "any session — or none — can wait on or cancel any operation"); ADR-0019 §Sessions (in part: an agent's operation is no longer cancellable by another agent)
- Relates to: ADR-0015 §6, ADR-0048, ADR-0088, ADR-0098, ADR-0099; plan m2t Q5
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated (plan m2t Q5, reviewed in two rounds). The owner kept the register row for 2t (2026-10-18).

## Context

ADR-0020 made operation ids daemon-wide and let any session wait on or cancel any
operation. Ids are random and no MCP tool returns another session's, so the register
called it a limit, not a hole. A peer (ADR-0098) is another person's daemon: reading or
cancelling the host's or another peer's operations would cross the line ADR-0099 draws.

## Decision

- An **MCP session** waits on and cancels only operations of its own session or of its
  chat's sessions (ADR-0088); a **peer** only its own. The **UI and the CLI** reach any
  (Activity, and resuming after a reconnect).
- Another's id is `OPERATION_NOT_FOUND`, as an unknown one.
- The check is in the core, from the call's origin, not in the adapter.
- `operation_cancel`'s MCP description says so, no longer than today.

## Consequences

- An MCP client with no chat id loses its waits across a shim restart;
  `build.lastResult` still answers for builds.
- `capability-api.md` §Operations names the rule.

## Alternatives considered

- **An audience check alone.** It keeps another agent's operation readable by id.
- **The adapter as the fence.** The core is the fence for every authorization
  (ADR-0015 §6); an adapter that forgot it would reopen the hole.
