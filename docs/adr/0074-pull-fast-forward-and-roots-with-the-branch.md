# ADR-0074: Pull is a fast-forward only, and a synchronised branch applies to the roots that have it

- Status: Accepted
- Date: 2026-10-14
- Supersedes: ADR-0071 §Decision (in part: item 1's list of commands, which left pull for later; item 6's default of the synchronise toggle)
- Relates to: ADR-0071 §2, §3, §4, §5; ADR-0073; plan m2n part 3
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated, after the owner's first check of 2026-10-14: the toolbar's Pull showed the incoming dot after a Fetch and could not be used ("no puedo bajar nada aunque haga fetch"), and a checkout should move "only the submodule that has that branch; the rest stay on theirs", as IntelliJ does.

## Context

ADR-0071 §1 built fetch, checkout, a new branch and the submodule update, and left pull with push, merge, rebase and stash for later. The toolbar shows Pull with a dot when the current branch is behind its upstream (plan m2n step 6), so after a Fetch the UI says there is something to bring in and gives no way to bring it. A pull that merges or rebases can stop half-way with conflicts the UI cannot resolve; a fast-forward cannot: it moves the branch and the work tree to a descendant, or git refuses before it writes, as a checkout does.

ADR-0071 §6 turns the synchronise toggle on only when every initialised root has the branch, and the daemon refuses a root that lacks it (`TestSyncBranchMissingInARoot`). IntelliJ's synchronised branches list the union of the roots' branches, mark each with the roots that have it, and check a branch out in those roots only. The owner works with submodules whose branches exist in one of them.

## Decision

1. **`git.pull`**, a command beside ADR-0071 §1's: the current branch of the workspace's repository fast-forwarded to its upstream, `git pull --ff-only --no-rebase --no-recurse-submodules --no-autostash` under ADR-0071 §3's invocation for a network command (5 minutes, never a prompt, hooks off) and ADR-0073's environment, on a trusted root only, through the repository's one write key (§4). It takes no parameter. The result is the branch, the oid before and the oid after; equal oids mean it was up to date.
2. **What it refuses, before any write**: a detached HEAD, or a branch with no upstream, is `GIT_FAILED` with a cause that says which and a nextStep. A branch git cannot fast-forward (it has commits the upstream lacks) and local changes in the way are `GIT_FAILED` with git's cause (ADR-0071 §5); nothing is merged, rebased or stashed. The UI refuses while an editor buffer is dirty, naming the files, as for a checkout.
3. **Synchronised branches apply to the roots that have the branch.** The side panel lists the branches of every root once, each marked with the colour of each root that has it. A checkout or a new branch from a branch, with Synchronise submodules on, passes `roots` = the roots that have that branch (for the toolbar's Create Branch, every initialised root); a root without it keeps its branch. The toggle is on by default whenever a submodule has the branch. The daemon's rules are unchanged: every root it is given must have the branch.

## Consequences

- The toolbar's Pull works for the workspace's repository; a diverged branch says so and the user merges or rebases in a terminal. Push, merge, rebase and stash stay later.
- A pull of a submodule's branch is not offered: the submodule update brings submodules to the commits the repository records.
- The current rule lives in `internal/domain/git` (`pull.go`, the package comment), `schema/capabilities.json` and the web UI's branch model; `docs/interfaces/mcp.md`'s not-exposed row gains `git.pull`.

## Alternatives considered

- **Pull with merge or rebase, git's default**: a conflict stops it half-way, with the work tree full of markers and a merge or rebase in progress the UI can neither show nor finish nor abort.
- **Disabling Pull's dot until pull exists**: hides that the branch is behind, which the dot exists to say.
- **Fetch then a fast-forward `merge --ff-only @{upstream}` as two calls**: two writes, and a second fetch from elsewhere between them; one git does both under one key.
- **Refusing a synchronised checkout when a root lacks the branch, as before**: the owner's submodules have branches in one root only, so the toggle was off for the case they need.
