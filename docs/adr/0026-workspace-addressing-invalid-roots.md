# ADR-0026: Workspace addressing — invalid roots and newer state files

- Status: Accepted
- Date: 2026-10-03
- Relates to: ADR-0020 §Scope and workspace addressing, ADR-0021 §Format, `workspace-state.md` §Lifecycle

## Context

Milestone 1c ships `workspace.open` and the MCP session binding, so a mistyped `--root`
reaches `open` at once. The documents name the accepted targets — an absolute project
root or a workspace id ([ADR-0020](0020-operation-contract.md) §Scope and workspace
addressing) — and require `open` to validate that the target "is a readable directory"
([`workspace-state.md`](../architecture/workspace-state.md) §Lifecycle) — but they give
no outcome when that validation fails. The error model forbids reusing a published code
with a new meaning ([`capability-api.md`](../architecture/capability-api.md) §Error
model), and its cross-cutting table has only `WORKSPACE_NOT_OPEN` and
`WORKSPACE_UNTRUSTED`; neither describes a target that cannot be opened. Separately,
[ADR-0021](0021-persistence-format-and-location.md) §Format, which requires a state file
of a newer `schemaVersion` to be left untouched and the workspace to open "degraded with
a clear error", names no code either.

## Decision

Two codes in the `WORKSPACE` domain; each descriptor that can return one lists it.

- **`WORKSPACE_ROOT_INVALID`** — the target of `workspace.open` is an absolute path that
  does not exist, is not a directory or cannot be read, or an id that no workspace index
  holds. The operation ends `failed`; validation is the handler's work in
  [`workspace-state.md`](../architecture/workspace-state.md) §Lifecycle, so
  [ADR-0020](0020-operation-contract.md) §Core primitives, whose `Start` rejection list
  is unchanged. `details {target}`; nextStep "call `workspace.open` with the absolute
  path of a readable project directory".
- **`WORKSPACE_STATE_NEWER`** — `ws/<wsid>/workspace.json` carries a `schemaVersion`
  this daemon does not read. The workspace opens `degraded`; the file is never
  overwritten; `workspace.describe` and `workspace.open` carry the error in
  `degradedBy`; nextStep "upgrade droidyard, or delete `<state>/ws/<wsid>/` to reset
  this workspace" ([ADR-0021](0021-persistence-format-and-location.md) §Consequences).

An MCP binding to a bad root degrades to `WORKSPACE_NOT_OPEN` on every workspace tool
until `workspace_open` shows the cause. [`workspace-state.md`](../architecture/workspace-state.md)
§Lifecycle and [ADR-0021](0021-persistence-format-and-location.md) §Format name the
codes, and the golden file lists them on `workspace.open` and `workspace.describe`.

## Consequences

- Both codes are published in the golden file; after publication they cannot be renamed
  or given a new meaning ([`capability-api.md`](../architecture/capability-api.md) §Error
  model).
- An agent that opens a bad root gets one actionable error instead of borrowing a code
  with another meaning; the MCP binding's failure path names `workspace_open` as the
  cause, not the fix ([ADR-0020](0020-operation-contract.md) §Adapter projections).
- A newer state file stays visible and untouched: the workspace degrades, nothing is
  migrated, and a downgrade deletes no state.
- The workspace object gains `degradedBy`, the error shape, in milestone 1c's schema
  change.

## Alternatives considered

- **`REQUEST_INVALID_PARAMS` for a bad path or id.** Gives a published code a second
  meaning, which [`capability-api.md`](../architecture/capability-api.md) §Error model,
  which forbids it for a target that is not a parameter.
- **`WORKSPACE_NOT_OPEN` for a failed open.** Its nextStep is the call that just failed,
  so an agent loops on `workspace.open`.
- **Reject at `Start` with a typed error and no operation.** Adds a filesystem check to
  every `Start` of `workspace.open` and amends [ADR-0020](0020-operation-contract.md)
  §Core primitives, whose rejection list is left alone here, for no consumer-visible
  gain.
- **Three codes (not found, not a directory, unreadable).** Every consumer reacts the
  same way, by fixing the path; `cause` already says which, so the split buys nothing.
- **Treat a newer state file as corrupt.** Renames a newer version's state away on a
  downgrade, which [ADR-0021](0021-persistence-format-and-location.md) §Format, which
  requires it to be left untouched, forbids.
- **Defer `WORKSPACE_STATE_NEWER` until a binary writes version 2.** Fewer codes now; no
  released binary writes a newer file, but `open` still must refuse to overwrite one and
  then has no way to say why. This alternative wins if the owner prefers fewer codes.
