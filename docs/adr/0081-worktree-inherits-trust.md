# ADR-0081: A linked worktree of a trusted repository is trusted

- Status: Accepted
- Date: 2026-10-18
- Supersedes: ADR-0015 §5 (in part: a root also becomes trusted as a linked worktree of a trusted repository); ADR-0031 §1 (in part: the trust rule gains the worktree clause); ADR-0032 §2 (in part: `shell.roots` matches by the same rule); ADR-0045 §5 (in part: Trust on a worktree trusts its main tree)
- Relates to: ADR-0015 §2, §6, ADR-0066 §3, ADR-0075 §3, ADR-0082; plan m2q Q1
- Decided by the owner, 2026-10-17: "El trust aplica al proyecto, si hay un worktree nuevo, el trust ya viene de serie … Aquí ya no tiene sentido preguntar esto." How a worktree is proven to be one is Delegated (the main session, under the owner's standing instruction).

## Context

Trust is matched by path: a root equal to a `trust.roots` entry or under a `trust.prefixes` entry (ADR-0031 §1). A git worktree an agent makes for a task lives at a path of its own, often beside the main tree, so every worktree would need a human `droidyard trust`, and a session working in one would stop at `WORKSPACE_UNTRUSTED`. The owner's rule is that trust is the project's: a worktree is a checkout of the same repository, as a branch checked out in the main tree already is, which nobody re-trusts.

What has to be decided is how the daemon tells a genuine worktree from a directory whose `.git` file merely points at a trusted repository's data.

## Decision

1. A root that is not trusted by path is trusted when it is a **linked worktree** of a non-bare repository whose main tree is trusted by path. Trust flows from the main tree to its worktrees only, never from a worktree to its main tree or to a sibling. It is checked before every call, as now; nothing is cached.
2. Root `R` is a linked worktree of main tree `M` when, canonically:
   - (i) `R/.git` is a regular file, not a link, of at most 4 KiB, holding `gitdir: <P>`;
   - (ii) `P/commondir` names the common directory `C`, and `P` is `C/worktrees/<name>`;
   - (iii) `P/gitdir` names `R/.git`: the back-link;
   - (iv) `C` is named `.git`, `M` is its parent, and `M/.git` is that directory.

   A relative path resolves against the directory holding the file that names it, as git resolves it. Anything else is not a linked worktree: a submodule, a bare repository, `core.worktree`, a worktree moved by hand. The daemon reads these files itself and runs no git: a root's configuration is not read before its trust is known.
3. **The threat** is a directory planted with a `.git` file naming a trusted repository's data. Only a write inside the trusted repository's `.git` makes the back-link, and a process that can write there already runs code in the trusted root (ADR-0015 §6's same-uid boundary). **Accepted residual risk:** a worktree directory deleted by hand, without `git worktree remove`, leaves its administrative entry naming that path until `git worktree prune` (or git's gc, three months by default). A directory later made at exactly that path, with a `.git` file naming the entry, inherits trust. The path is one the user chose, and `git worktree remove` leaves no such entry. A test pins this behaviour.
4. The same rule matches `shell.roots` for `shell.exec`. Enabling the shell is the project's choice too; ADR-0032 §2 is otherwise unchanged.
5. The UI's Trust, on a worktree, trusts its main tree. `WORKSPACE_UNTRUSTED` on a worktree names the main tree in its message and nextStep. `droidyard trust <path>` still trusts the path it is given.

## Consequences

- `internal/platform` gains `LinkedWorktree`, and `internal/policy` `Trusted` and `ShellAllowed` use it. The package doc comment states the rule; `workspace-state.md` §Identity and reuse links here.
- An agent never asks for trust in a worktree of a trusted project. One `droidyard trust` on the main tree covers every worktree it will have.
- Worktrees of bare or submodule repositories, those with `core.worktree` set, and a project root below its repository's top (`M/dev`) inherit nothing: they fail closed. The last one is a register row.

## Alternatives considered

- **Containment by path** (trusted when under the main tree). Worktrees often live beside the main tree, so they would miss. And any directory planted under a trusted prefix already passes, so containment proves nothing about the repository.
- **`git rev-parse --git-common-dir` in the root.** It runs git with the candidate's configuration before the candidate's trust is known. As far as we know it also follows a `.git` file's `gitdir` and `commondir` without checking the back-link [assumption, not run], so a planted file would pass.
- **A trust act per worktree.** This is the owner's "never asked again", refused.
- **Recording a worktree in `config.json` when a session enters it.** An agent's act would then write trust, which ADR-0015 §6 and ADR-0031 forbid.
