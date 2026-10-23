# ADR-0126: The ignore block is shared by every run in a repository

- Status: Accepted
- Date: 2026-10-23
- Supersedes: ADR-0124 §Decision (in part: §4, a rerun replaces the block and an uninstall removes it)
- Relates to: ADR-0090 §1; [plan m3a](../plans/m3a-init-once-per-user.md) §Changes (the close verifier's B1)
- Decided by the main session under the owner's standing instruction to apply the recommended option, Delegated (the close verifier of plan m3a).

## Context

ADR-0124 §4 puts init's untracked paths in one marked block, in the common git directory's
`info/exclude` or the root's `.gitignore`, which a rerun replaces and an uninstall removes.
The close verifier showed that one `info/exclude` block is shared by every client, worktree
and project root of a repository: `init --client cursor --exclude` then `--client claude
--exclude` dropped Cursor's lines; `init --uninstall --client cursor` with Claude still
installed, or an uninstall in one worktree, brought the other run's files back into `git
status`; a second Android root in the repository dropped the first's lines.

## Decision

An install writes the union of the block's lines and the run's untracked paths. An uninstall
removes only the lines of the paths it removed itself, and keeps one whose path still exists
in its root or, for `info/exclude`, in another work tree of the repository (read from the
main tree and `<commondir>/worktrees/*/gitdir`, without running git): a line anchored at the
top hides that path in every tree. The block, and a file it leaves empty, goes only when no
line is left. Tracked paths are still reported and never added. The
common directory's `info` must resolve below the common directory, or the block is refused.

## Consequences

- A line outlives its file when the file goes by other means than `init --uninstall`; it
  then ignores nothing anyone sees. A line whose path became tracked stays, harmlessly.
- `internal/assistant/ignore.go` and `cmd/droidyard/init.go` follow; the plan's §Changes
  records it.

## Alternatives considered

- **Keeping "replaces" and recording the scenarios as a limit**: the owner's request was a
  clean `git status`, which two clients, two worktrees or two roots would lose.
- **One block per client and root**: the same union, with more marker lines to parse and
  more ways for a hand edit to break them.
- **Recomputing the block from the files present in every tree**: init would have to walk
  every worktree of the repository, which it does not know.
