# ADR-0086: A recent project can be removed: `workspace.forget`

- Status: Accepted
- Date: 2026-10-18
- Supersedes: ADR-0045 §3 (in part: `workspace.state` gains the event type `forgotten`)
- Relates to: ADR-0021 §Layout, ADR-0054, ADR-0084; plan m2q Q6
- Decided by the main session under the owner's standing instruction to apply the recommended option, Delegated. The owner chose the register row ("a recent project cannot be removed from the projects panel") for milestone 2q, 2026-10-17.

## Context

`workspace.watch` lists the index's 50 newest entries (ADR-0045 §3), and nothing removes one. A project the user no longer wants keeps its row until 50 newer ones push it out.

## Decision

1. **`workspace.forget {workspace}`** (command, global, stateless, audience human, idempotent) removes the index entry of a workspace that is not open, and publishes `forgotten` on `workspace.state`.
2. `ws/<wsid>/` stays (builds, the trash), so reopening the root later restores its history.
3. An open workspace fails the new `WORKSPACE_OPEN`. An unknown id answers as done.

## Consequences

- `workspace.forget`, the `forgotten` event type and `WORKSPACE_OPEN` enter the contract.
- A closed row in the projects panel gets Remove.
- A forgotten project's state stays on disk. Clearing it is not offered.

## Alternatives considered

- **Hiding the row in the browser.** Other tabs, and the next load, still list it.
- **Deleting `ws/<wsid>/` too.** The trash holds the user's deleted files (ADR-0054).
- **Closing an open workspace first, then removing it.** A removal would stop running builds.
