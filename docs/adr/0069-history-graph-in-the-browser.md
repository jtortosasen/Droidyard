# ADR-0069: The history graph laid out in the browser, paged by oid, and a commit's file in the Diff tab

- Status: Accepted
- Date: 2026-10-12
- Supersedes: ADR-0067 §Decision (in part: items 2–4, that the Diff tab shows only the active file against HEAD and that its buffer side is always editable)
- Relates to: ADR-0067 §1; ADR-0068; plan m2n Q2
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated. The owner set the look on 2026-10-12: vscode-git-graph's table and in-place expansion, SourceGit's side panel and toolbar, and a commit's file opening in the Diff tab against the commit before it.

## Context

A commit graph needs each commit's lane and the edges between lanes. The survey (`docs/research/git-history-survey-2026-10.md`) found no Svelte library; the React ones (react-git-log, commit-graph) and GitKraken's component are rejected as dependencies, and vscode-git-graph's and BeardGit's licences forbid reuse. The lane algorithms in use are one newest-first pass over each commit's parents (pvigier 2019, VS Code's `scmHistory.ts`, BeardGit), or git's own `--graph` text parsed into flows (Gitea). ADR-0067 §2–§4 made the Diff tab show the active file against HEAD with the buffer side editable, so a commit's file, which has no buffer, has no place there.

Step 1 of plan m2n measured a page of 500 commits in `--date-order` at 65–108 ms with a commit-graph and 327–415 ms without, on 100,000 commits, at any skip (ADR-0068 §Context): paging by `skip` over fixed tips costs no more deep in the history than at its top.

## Decision

1. **Lanes are laid out in the browser** from each commit's parents, in one newest-first pass: a row's incoming lanes are the previous row's outgoing ones; a commit takes the first lane waiting for it, else the first free column; its first parent inherits that lane; another parent joins a lane already waiting for it, else opens one; lanes waiting for the same commit close where it is drawn. The layout extends page by page, and laying pages out one at a time equals laying them all out at once. No lane cap. Each row is drawn as an inline SVG in a virtualised table, so text stays selectable and readable by a screen reader. No lane data enters the published contract.
2. **Order and paging**: `--date-order` (children before parents, otherwise by committer date), pages by `skip` over fixed tips (ADR-0068 §2).
3. **Freshness**: the refs reload when `git.watch`'s `head.oid` changes, when the History tab is shown and when the window regains focus. When the reloaded tips differ from those the loaded history started from, the history reloads from its first page, keeping the selection, the scroll position and an open expansion at the same oid if it is still loaded, else returning to the top. A ref moved by another git while none of the three happens is seen at the next one.
4. **The Diff tab has two sources**: the active file against HEAD (ADR-0067 §2, unchanged), and a commit's file against its first parent (ADR-0068 §5), both sides read-only, in Classic and in Review. With the second source, Review's left side and Classic's right side are the commit's text, with no buffer behind them.

## Consequences

- The lane pass is code of ours, in `web/src/lib/git/`, kept by a property test that every parent edge ends at its parent's node and that paged and whole layouts agree.
- Without a lane cap a repository with many long-lived branches draws a wide graph; a cap or collapsed long edges (BeardGit, IntelliJ) is the later answer if the owner's project needs it.
- A ref moved without HEAD moving, while the tab stays shown and focused, is stale until the next refresh signal.
- `mountCompare` and `mountReview` gain a read-only text side; the editor's buffer contract is unchanged for the first source.
- The current rule lives in the code and in the web UI's area document, both changed by the plan steps that build it (m2n steps 3, 4 and 9).

## Alternatives considered

- **Lanes computed by the daemon** (BeardGit computes its layout in Rust): presentation in the published contract, and a schema change for every layout change.
- **Parsing `git log --graph`** (Gitea): git's ASCII art is wide, its state does not carry across pages, and the parse rebuilds what one pass over the parents gives directly.
- **A library**: none for Svelte; the React ones would bring React; GitKraken's is proprietary.
- **Canvas** (BeardGit): loses native text selection and needs a hidden mirror for accessibility.
- **`--topo-order`**: either order serves the lane pass; date order interleaves branches by time, as the clients the owner chose do, and the owner's check judges the look.
- **A refs fingerprint published by `git.watch` every tick**: a contract change and a `for-each-ref` a second, for a case that focus and HEAD changes mostly cover.
