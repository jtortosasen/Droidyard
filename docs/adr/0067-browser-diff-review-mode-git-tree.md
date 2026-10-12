# ADR-0067: The diff in the browser, its Classic and Review modes, and the Git tree

- Status: Accepted
- Date: 2026-10-12
- Relates to: ADR-0009; ADR-0066; plan m2i D8; plan m2l Q2
- Decided by the owner, 2026-10-12, for the two modes, the Review layout, the elastic anchored scroll and the Git tree's views (prior art for Review: matklad, "Unified vs split diff", 2023-10-23, a wish never implemented there). The engine, the toggle's form and the scroll's details are Delegated (the main session under the owner's standing instruction).

## Context

ADR-0009 chose CodeMirror with `@codemirror/merge` for the Git diff and kept a dedicated diff component as the escape hatch. The owner asked for two modes: the classic side-by-side, and a Review mode of their own, where the file on disk stays editable on the left with no gaps and a magit-style unified rendering sits on the right, its scroll anchored to the left's. Unsaved edits must show in both. In m2i, Compare mirrored its editable side into the buffer, which was safe only because Compare replaced the editor in its host.

## Decision

1. **The browser computes the diff**: HEAD's text (`git.read`) against the editor's buffer, with `@codemirror/merge`'s `Chunk.build` and `Chunk.updateB` and one shared diff configuration in both modes. Unsaved edits show, there is one engine, and there is no new dependency. ADR-0009's escape hatch is not taken.
2. **The Diff tab** (centre) shows the Editor's active file against HEAD. A file opened from the Git tree opens as the Editor's preview tab, so a file has one buffer. The Editor and Diff tabs stay mounted together, so the diff's editable side **follows the buffer**: a change it did not make is applied to it as the smallest change over document positions, and only its own edits mirror back (m2i D8's guarantee kept, CRLF files included). A segmented **Classic | Review** control in the header, remembered per browser, default Review.
3. **Classic**: MergeView, HEAD read-only, the buffer editable, labelled `HEAD` and `Working tree`.
4. **Review**:
   - Left: the buffer, editable, with no spacer or widget; gutter marks for added and modified lines and a marker where lines were deleted.
   - Right: read-only rows. Unchanged rows are dimmed, added and modified ones green, deleted ones red above their hunk's green. Each side is highlighted from its own parse.
   - **Elastic anchored scroll**: the anchor is the cursor line while it is visible, else the viewport's centre. A monotone, piecewise-linear map sends each left line onto its row plus the red rows after it, and the right pane is placed so the anchor's point sits at the same height. A line's top meets its row's top exactly, and crossing a deletion block slides the right pane through it instead of jumping. The right pane is padded so any row can align. The pane the user last scrolled drives, and the other follows by the inverse map; a change of anchor eases over 120 ms. A click on a right row puts the cursor on its line (for a red row, where its block was deleted).
5. **The Git tree** (the Git side of the left panel's Editor | Git switch), with an expansion state of its own, in memory, never the file tree's:
   - **Tree**: the whole project, plus status paths the file watch does not list (deleted files), with only the folders holding changes expanded.
   - **Changes**: grouped by folder, or flat with each file's directory greyed.
   - Letters M, A, U, D, R, C; a deleted file struck through. The view and grouping are remembered per browser and project.

## Consequences

- The Diff tab and the Editor tab share one buffer, and the follow rule is what keeps a stale side from reverting it.
- Review's feel is the owner's to judge; until they have, it is unverified (plan m2l step 7).
- Word-level marks, next and previous change, per-hunk actions and Git marks in the normal tree are not here.

## Alternatives considered

- **The daemon diffs (`git diff`)**: no unsaved edits, two engines, and a hunk format to publish.
- **Monaco's diff editor**: ADR-0009.
- **`unifiedMergeView` in the editor**: deleted chunks become widgets inside the file, the gaps the owner refused.
- **Classic only**: the owner asked for Review.
- **A buffer of the Diff tab's own**: two buffers for one file, and two saves that can disagree.
