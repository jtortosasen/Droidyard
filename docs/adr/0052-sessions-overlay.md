# ADR-0052: The sessions column also shows as an overlay, on hover and at the window's left edge

- Status: Accepted
- Date: 2026-10-11
- Supersedes: ADR-0050 §1 (in part: "No hover reveal")
- Relates to: ADR-0050 §2, §3, ADR-0047 §2; plan m2f Q1, D43–D45
- Decided by the owner, 2026-10-11, after trying 2e: a hover on the sessions column that
  overlays and displaces nothing, and a "viscous" left edge that stimulates the toggle, then
  feints, then brings the column out. The shape below is Delegated (plan m2f Q1, reviewed in
  two rounds).

## Context

ADR-0050 docked the column left of the tree and rejected hover reveal because a docked
column would shift the layout under the pointer. The owner wants the column at hand without
docking it, and without disturbing work on the tree.

## Decision

1. While the column is not docked, resting on the toolbar toggle or reaching the window's
   left edge shows it as an overlay over the left of the middle row. Nothing in the layout
   moves, the right region is not collapsed, and nothing is remembered.
2. The edge is graded by the pointer's distance: approaching stimulates the toggle; nearer, a
   drawn feint of the column's edge that takes no clicks; at the last pixels, the overlay.
   Mouse pointers only, never with a button held. The thresholds live in code (`ui/edge.ts`).
3. The overlay hides on leaving it, on Escape or on a click outside. A click on the toggle, or
   the overlay's Dock control, docks the column; docking keeps ADR-0050 §1 ("no rail"), §2
   and §3.

## Consequences

- The tree's first pixels stay usable: the feint takes no clicks, and only the last few pixels
  pop the overlay.
- How the edge feels rests on the owner's headed trial (`roadmap/known-limits.md`).

## Alternatives considered

- **Click only (ADR-0050 as it was)**: the owner asked for hover.
- **Hover that docks the column**: the layout would shift under the pointer, ADR-0050's reason.
- **An ungraded hot zone at the edge**: it would open while the user works on the tree's first
  pixels, which the feint exists to prevent.
- **A dwell-time hot zone**: no warning before it opens, and the owner's "boom" is immediate.
- **A visible rail as the hover target**: removed by the owner in 2e.
