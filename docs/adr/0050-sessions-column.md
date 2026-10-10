# ADR-0050: The sessions panel is a column docked left of the tree, toggled from the toolbar

- Status: Accepted
- Date: 2026-10-11
- Supersedes: ADR-0047 §1 (in part: the left rail)
- Supersedes: ADR-0047 §2 (in part: the panel opening pinned)
- Relates to: ADR-0047 §3, ADR-0048; plan m2e Q1, D32, D33
- Decided by the owner, 2026-10-11: one toggle where the brand was, the rail gone, the panel
  to the left of the tree and not over it, the right panel collapsed while it is open. The
  shape below is Delegated (plan m2e Q1, reviewed in two rounds).

## Context

In 2d the projects and agents panel sat behind a vertical rail: hover showed it over the
tree, a click pinned it. After trying it the owner asked for one toggle in the toolbar and a
panel that takes its own place in the layout.

## Decision

1. The sessions panel is a column docked left of the left region, with its own width
   (remembered per browser, with the other region sizes). One toolbar toggle, in the
   brand's place, opens and closes it and carries the running-agent count and spinner. No
   hover reveal and no rail.
2. Opening it collapses the right region and records that it did; closing it restores the
   right region only while that record stands. Any user action on the right region while
   the column is open clears the record.
3. With no project open, or a `w` the daemon refuses, the column is the page (ADR-0047 §2's
   "opens pinned"). ADR-0047's content and selection rules, the rest of §2 and §3 stand.

## Consequences

- The column, the tree and the centre share the width; at 1280 px with every region open
  the centre keeps at least 240 px (plan m2e step 5).
- The density switch lives at the top of the column.

## Alternatives considered

- **Keep hover reveal**: with a docked column the layout would shift under the pointer.
- **An overlay over the tree**: the owner's "not over the tree".
- **Always restore the right region on close**: it would override the user's own collapse.
