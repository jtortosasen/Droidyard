# ADR-0096: The trash is listed, so Activity restores a delete made before the tab opened

- Status: Accepted
- Date: 2026-10-18
- Supersedes: ADR-0054 §Decision (in part: §1, `entry.json` gains `by`); ADR-0054 §Consequences (in part: the trash can be listed)
- Relates to: ADR-0043 §1, ADR-0048, ADR-0095 §2; plan m2s Q3
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated; the register row was kept for 2s by the owner, 2026-10-18.

## Context

Activity lists operations from the tab's load on: `operation.watch` returns non-terminal
operations only (ADR-0043 §1), so a delete made before the tab opened has no row and no
Restore, though `file.restore` works on it after a restart too (ADR-0054 §4). No
capability lists the trash (ADR-0054 §Consequences), and `entry.json` does not say who
deleted.

## Decision

1. `file.trash` (query, scope workspace, audience human) lists the workspace's trash
   entries newest first, at most 200 (a working default); an unreadable entry is skipped.
2. `file.delete` writes who deleted into `entry.json` as `by {client, name?}`; entries
   written before have none.
3. Activity shows the entries deleted before its first operation as rows with Restore,
   under the agent or You by `by`.

## Consequences

- One capability and one `entry.json` field; older entries show under You.
- Emptying the trash and a trash view beyond Activity stay open (ADR-0054's row).
- Documents that change with it: `files.md` (the trash).

## Alternatives considered

- **`operation.watch`'s snapshot carrying recent terminal operations**: only those the
  store still keeps (15 minutes, at least 200), and only while the daemon lives.
- **A trash view with emptying**: more than the row asks; its register row stays.
