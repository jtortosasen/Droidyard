# ADR-0089: `workspace.describe` lists the project's worktrees

- Status: Accepted
- Superseded in part by: ADR-0094 (§Decision)
- Date: 2026-10-18
- Relates to: ADR-0081 §2, ADR-0082 §2, §6; plan m2q step 5
- Decided by the owner, 2026-10-18 ("Que el daemon enseñe los worktrees"), after step 5's chat 2. The shape is Delegated.

## Context

ADR-0082 §2 leaves the choice of a worktree to the agent's workflow, guided by the skill and the server instructions. In step 5, a new Claude Code chat told to "continue task `fix-greeting`: check that the debug build still passes" called `workspace_describe` first. It never ran `git worktree list`, never loaded the skill, and built on the main tree, without the task's change. The instructions' rule spoke of "a task that changes code", and checking a build did not read as one. The agent's first call is the one place Droidyard is sure to be read.

## Decision

1. **`workspace.describe`'s result gains `worktrees`**: the linked worktrees of the project's repository, each `{root, branch?}`, sorted by `root`, at most 50 (working default).
   - `root` is absolute and canonical.
   - `branch` is the short name from the worktree's `HEAD` when it is `ref: refs/heads/<name>`. It is absent when the worktree is detached.
   - The list is the same whether the described root is the main tree or one of its worktrees, and the described root appears in it when it is a worktree.
2. **It is read as ADR-0081 §2 reads**, with no git run. The daemon lists `C/worktrees/<name>/gitdir` under the repository's common directory `C`, and keeps an entry only when that worktree passes `platform.LinkedWorktree` with this main tree. A stale entry is left out: a removed directory, or a broken or moved back-link. Every file read is bounded and non-blocking, as there.
3. **Only `workspace.describe` carries it.** `workspace.open`, `workspace.watch` and `workspace.state` do not, so the panel's streams and the bound-open path read nothing more. The field is omitted when the project has no linked worktree, or is not a git repository.
4. **The instructions say what to do with it**: work on a task that already has a worktree (its branch names the task) happens there, so the agent enters it first.

## Consequences

- An agent sees the project's worktrees and their branches in the first call it usually makes, without git.
- The UI's Git panel keeps its own list (`git.refs`, from `git worktree list`), which includes entries git would prune. The two can differ on stale entries, and that difference is by design.
- `docs/interfaces/mcp.md` §Server instructions follows (verbatim), within the instructions' 2,000-character budget.

## Alternatives considered

- **A stronger rule in the instructions only.** It depends on the agent reading the rule as covering "check" or "continue". Chat 2 did not read the first rule that way, and the text budget leaves little room to enumerate cases.
- **Running `git worktree list` in the daemon.** It runs git in a root whose trust is not yet known (ADR-0081 §Alternatives), and it lists stale entries that ADR-0081 would not accept.
- **`worktrees` on `workspace.open` and `workspace.watch` too.** More reads on every open and every panel refresh, for a field only the agent's choice needs.
- **Droidyard choosing the worktree from the task's name.** ADR-0082 §2 leaves the choice to the agent's workflow, and Droidyard is never told the task.
