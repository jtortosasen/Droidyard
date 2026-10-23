# ADR-0085: The hook records where the command ran

- Status: Accepted
- Superseded in part by: ADR-0095 (§Decision)
- Superseded in part by: ADR-0124 (§Decision)
- Date: 2026-10-18
- Supersedes: ADR-0063 §3 (in part: which workspace opens and records); ADR-0062 §Decision (in part: the root the `rm` route resolves against)
- Relates to: ADR-0061, ADR-0063 §4, ADR-0075, ADR-0081, ADR-0082; plan m2q Q5
- Decided by the main session under the owner's standing instruction to apply the recommended option, Delegated. The owner chose the register row ("A command an agent runs in another project is recorded in Activity under the hook's root") for milestone 2q, 2026-10-17.

## Context

ADR-0063 §3 opens and records under the hook's `--root`, the root `init` wrote. Its Consequences name the defect: a command an agent runs in another directory is recorded under that root anyway. With worktrees (ADR-0082) the common case becomes a command run in a worktree of the project, which belongs under that worktree. Claude Code's after-command event carries the command's `cwd`; Cursor's, as observed in 2j, carries none.

## Decision

1. The hook's root comes from the command's directory. Both `cwd` and `--root` are canonicalised first. From `cwd` upwards, take the nearest directory holding a `.git` entry:
   - if it is a linked worktree (ADR-0081 §2) of `--root`'s project, the root is **that worktree**;
   - otherwise, if `cwd` is `--root` or lies under it, the root is **`--root`** (a Gradle build in `dev/`, any subfolder);
   - otherwise there is **none**: the record is dropped (the hint still goes), and the `rm` route keeps `--root`'s rules.
2. A client that sends no directory (Cursor, as observed in 2j) keeps `--root`.
3. Recording and the `rm` route use the same rule, within ADR-0061's 2 s.

## Consequences

- `assistant.DecodeAfterEvent` returns `cwd`; `cmd/droidyard/hook.go` gains `hookRoot`. Activity shows an agent's command under the worktree it ran in.
- A command in an unrelated project is no longer recorded at all; it used to be recorded in the wrong project.
- A record is still not tied to its chat's session (nothing links a hook process to an MCP connection). That is a register row.

## Alternatives considered

- **ADR-0019's settings-file rule applied from the directory.** A `cd dev` would leave the project, which is ADR-0075's layout.
- **Opening whatever project a command ran in.** Every directory an agent enters would become a recent project.
- **Recording under another project that is already open.** One project's agent would record in another project's Activity.
- **Keeping `--root`.** That is today's wrong project.
