# ADR-0056: A daemon-wide state file of a newer version degrades no workspace

- Status: Accepted
- Date: 2026-10-11
- Supersedes: ADR-0021 §Format (in part: a newer daemon-wide file)
- Relates to: ADR-0026; plan m1c R10, R16; plan m2h §Audit ledger
- Decided by the main session under the owner's standing instruction (apply the recommended
  option), Delegated. It records what milestone 1c shipped from its readings R10 and R16,
  which R16 itself left "the owner's to choose"; the m2h audit found it with no ADR.

## Context

ADR-0021 §Format says a newer state file "is never overwritten: the workspace opens
`degraded` with `WORKSPACE_STATE_NEWER`". That fits `ws/<wsid>/workspace.json`, which
belongs to one workspace (ADR-0026). Two files belong to none: `workspaces.json`, the index
of known roots, and `processes.json`, the record of every child the daemon starts. For them
"the workspace" has no referent. Degrading every workspace for an unreadable index would
stop work that does not need it; starting children that cannot be recorded would let them
outlive a crash unseen (`process-lifecycle.md` §Shutdown).

## Decision

1. A newer file is still never read or written, and `system.health` lists it in
   `stateProblems` as `newerVersion`.
2. A newer `workspaces.json`: no workspace degrades. An open by root proceeds without
   updating the index; an open by an id this daemon has not opened ends
   `WORKSPACE_ROOT_INVALID`, as for an id no index holds.
3. A newer `processes.json`: the supervisor starts no supervised or recorded child (a
   sidecar, a recorded stream such as a logcat follow, an emulator launch), so the
   capabilities that need one fail and the workspace states are unchanged. Short
   per-operation commands, which are never recorded (ADR-0021 §Pid records), such as adb's,
   still run.
4. A newer `workspace.json` keeps ADR-0026's rule: that workspace opens `degraded` with
   `WORKSPACE_STATE_NEWER`.

## Consequences

- The rules live in `internal/workspace/workspace.go` (`loadIndex`, `recordOpen`) and
  `internal/process/supervisor.go` (`Load`, `Use`, `SpawnRecorded`, `RecordLaunch`); `workspace-state.md`'s `degraded` row names only
  the workspace's own file.
- With a newer `processes.json` a build fails although every workspace reads `ready`; the
  health problem is the only pointer to why.

## Alternatives considered

- **Degrade every workspace** (ADR-0021's literal reading): an index the daemon only needs
  for open-by-id and recents would block every capability.
- **Overwrite the newer file**: loses a newer daemon's state, which ADR-0021 forbids.
- **Refuse to start the daemon**: the UI and the CLI could not even report the problem.
