# ADR-0072: Compare with current: `git.log` excludes, `git.show` takes a base, and the Diff tab compares two commits

- Status: Accepted
- Date: 2026-10-13
- Supersedes: ADR-0069 §Decision (in part: item 1's parameters of `git.log` and `git.show`)
- Supersedes: ADR-0070 §Decision (in part: item 4's two sources of the Diff tab)
- Relates to: ADR-0069 §2, §3, §5; ADR-0070 §4; ADR-0071; plan m2n Q5
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated. The owner put Compare with current in milestone 2n, in IntelliJ's branch menu (plan m2n item 7: "only reads").

## Context

IntelliJ's Compare with current shows the commits one branch has that the current one lacks, each way, and the files that differ between the two tips. ADR-0069 §1 gives `git.log` a walk from tips with nothing excluded, and `git.show` a commit against its first parent (§5); ADR-0070 §4 gives the Diff tab two sources, the working tree against HEAD and a commit's file against its first parent. Nothing returns the commits of one tip not in another, nor the files between two tips, and the browser cannot derive either without walking both whole histories and listing trees no capability returns.

## Decision

1. **`git.log` gains `exclude`**: full commit oids, at most 5,000, validated as `from` is (ADR-0069 §2) and written to the same stdin as `^<oid>`, so `from: [B], exclude: [A]` is `A..B`, paged by `skip` as before.
2. **`git.show` gains `base`**, a full commit oid of the repository: its files are then `diff-tree <base> <oid>` under ADR-0069 §3's fence, and the commit's details are unchanged. Absent, the first parent, as before.
3. **The Diff tab's second source** is a file at one commit against the same file at another, both read-only, in Classic and Review. A commit's file against its first parent is the case where the base is the first parent.
4. Read-only, audience `human`, no new error code.

## Consequences

- Compare with current is two `git.log` pages (`from` the branch's tip excluding HEAD, and the reverse) and one `git.show` of the branch's tip with HEAD as `base`.
- `git.log` and `git.show` gain optional parameters, an additive schema change; the current rule lives in `internal/domain/git` (`log.go`, `show.go`) and the web UI's Diff source.

## Alternatives considered

- **A `git.compare` query**: one more capability for what two optional parameters give the existing ones.
- **The three-dot diff from the merge base** (what a pull request shows): not what Compare with current shows in IntelliJ, which is the two tips' trees.
- **Moving Compare with current to the later milestone with the other branch-menu items**: item 7 put it in 2n.
