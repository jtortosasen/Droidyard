# ADR-0027: Workspace close and reopen — when close completes

- Status: Accepted
- Date: 2026-10-03
- Relates to: ADR-0020 §Scope and workspace addressing, `workspace-state.md` §Lifecycle

## Context

Two clients share one workspace ([ADR-0019](0019-process-topology-daemon-and-shims.md)
§Sessions), so a close and an open of the same workspace can overlap.
[`workspace-state.md`](../architecture/workspace-state.md) §Open questions, which leaves
it open: "Whether a workspace can be *reopened* while operations are still draining, or
must fully close first". If `close` returns at `closing` and drains in the background,
Phase 0's "no orphans after `workspace.close`" cannot be checked when it returns. If it
waits without a bound, a handler that ignores cancellation holds a tool call past the
client's timeout: `workspace.close` is not flagged long-running
([`mcp-tool-surface.md`](../interfaces/mcp-tool-surface.md) §The MVP tool surface), so
it stays under 60 s ([ADR-0020](0020-operation-contract.md) §Adapter projections).

## Decision

- `workspace.close` completes when the workspace is `closed`.
- Entering `closing` rejects every workspace-scoped call on it except `open` and `close`
  at `Start` with `WORKSPACE_NOT_OPEN`.
- Its operations are cancelled and waited on for up to 10 s (working default). An
  operation still running then is logged and left to end `cancelled` when its handler
  returns; the workspace then stops its sidecars (shutdown timeout, then `SIGKILL` of
  the group).
- A close is not cancellable once begun.
- `open` and `close` of one workspace are serialised: an `open` during `closing` waits
  for `closed` and then opens; a second `close` waits and completes as a no-op.

## Consequences

- "No orphans after `workspace.close`" holds when close returns, so the Phase 0
  acceptance item is checkable as written.
- [`workspace-state.md`](../architecture/workspace-state.md) §Lifecycle, which states
  the rule, drops its open question.
- Sidecar shutdown timeouts must keep close under 60 s — a constraint on the Gradle
  sidecar's descriptor later.
- The 10 s bound is a working default, to move to the configuration file when it exists
  (Phase 1).

## Alternatives considered

- **Return at `closing`, drain in the background.** "No orphans after close" holds only
  eventually, and an `open` during the drain either fails (needs a code and a retry
  loop) or revives a half-closed workspace.
- **Reject an `open` during `closing` with a new code.** The same retry loop, for a wait
  that is normally milliseconds.
- **No bound on draining.** One handler that ignores `ctx` keeps `workspace_close` past
  client timeouts, and the timeout's `notifications/cancelled` would cancel the close
  ([ADR-0020](0020-operation-contract.md) §Adapter projections).
- **Flag `workspace.close` long-running.** Changes a normally instant tool to the
  operation view, and with it the tool's published output schema, for a case the bound
  removes.
