# ADR-0105: Activity shows a trash entry no restorable delete row accounts for

- Status: Accepted
- Date: 2026-10-18
- Supersedes: ADR-0096 §Decision (in part: §3, which entries Activity shows as rows with Restore)
- Relates to: ADR-0043 §1, ADR-0054; plan m2s step 8
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated, on step 8's implementation and its verifier.

## Context

ADR-0096 §3 shows "the entries deleted before [Activity's] first operation". That cutoff
hides an entry deleted while the tab's connection was lost (the tab holds operations from
before the loss, so the entry is after its first), and an entry of a delete whose row
cannot restore it: one that ended `unseen`, `cancelled` or `failed` with items already
moved (register rows on a workspace close's forced cancel). Those are the entries whose
only way back would otherwise be `entry.json`.

## Decision

Activity shows a trash entry as a row with Restore unless a `file.delete` operation of the
workspace that its store holds accounts for it: one `completed`, whose row offers Restore,
with the entry's `deletedAt` within the operation's span (its start to its end, 1 s of
slack each side), or one `queued` or `running` started before it. Both times come from the
daemon's one clock.

## Consequences

- An entry deleted within 1 s of a restorable delete the store holds is hidden; that
  operation's row restores only its own items.
- The rule lives in `web/src/lib/ops/trash.ts`.

## Alternatives considered

- **ADR-0096 §3's cutoff at the first operation**: hides entries deleted during a
  disconnect and those of deletes whose row cannot restore, with no Restore anywhere.
- **Matching entries to operations by id**: `entry.json` carries no operation id; adding one
  changes the state file for a dedup the time span already gives.
