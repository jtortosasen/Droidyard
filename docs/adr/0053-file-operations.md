# ADR-0053: The tree's file operations are five batch capabilities, open to agents, that never replace

- Status: Accepted
- Date: 2026-10-11
- Superseded in part by: ADR-0060 (§Consequences)
- Superseded in part by: ADR-0060 (§Decision)
- Relates to: ADR-0015 §1, §5, §6, ADR-0020 §States, ADR-0036 Decision 4, ADR-0054; plan m2g Q1, D47–D50
- Decided by the main session under the owner's standing instruction (apply the recommended
  option), Delegated; the owner asked for the operations in 2d (plan m2d, the split). The
  shape is plan m2g's Q1, reviewed in two rounds.

## Context

`file.*` held list, read, write and watch; delete, rename and mkdir were deferred. The tree
needs create, move, copy and delete with Undo, over many items at once, and someone must
decide name collisions. `vision.md` §Principles #1 lets a capability be human-only for two
reasons: an agent must not authorise itself (ADR-0015 §6), and a window opens on the user's
screen (ADR-0045 §4).

## Decision

1. Five workspace-scoped capabilities: `file.create` (a file or a folder), `file.move` (a
   rename is a move inside one folder), `file.copy`, `file.delete` (to the trash, ADR-0054)
   and `file.restore` (from it, by trash id). Audience all: MCP tools under `--tools all`,
   not in `core`. No trust check, nothing marked untrusted, no build lock (as `file.write`).
2. **Batches.** A call takes up to 1,000 items. A refusal decided from the text alone
   (outside the root, the root itself, a target inside its source, a malformed trash id)
   fails the whole call before anything changes. Anything only the filesystem can say fails
   its item; the call completes with every item's outcome, failed ones included, and its text
   rendering leads with the failures. A batch with failed items is a successful MCP call.
3. **Never replace.** Renames use the kernel's no-replace rename on folders opened through
   the workspace's `os.Root`; a volume that cannot refuse a replace fails the item. On a
   collision the item fails `FILE_EXISTS`, or, with `onCollision: "keepBoth"`, the daemon
   picks a free name. There is no replace.
4. **Cancel** stops between items. With no item attempted the operation ends `cancelled`;
   otherwise it completes and the rest are `OPERATION_CANCELLED`. This extends ADR-0020
   §States, as m1f D21 did, without superseding it.
5. New codes: `FILE_EXISTS`, `FILE_NOT_IN_TRASH`, `FILE_TOO_LARGE`.

## Consequences

- An agent's moves and deletes go through the daemon: contained, recorded, visible in
  Activity and undoable by the human from the trash.
- The UI composes Undo from the done items a result lists.
- `tools/list` grows by five entries under `all`.

## Alternatives considered

- **Human audience (a third exception)**: neither reason behind the two exceptions applies,
  and the agent's shell already moves files, uncontained.
- **One item per call, the UI looping**: an operation and an Activity block per file, and no
  unit to undo.
- **The first failure as the call's error**: the done items, which Undo needs, would sit in
  schemaless `details`.
- **A check, then `os.Root.Rename`**: a racing writer is overwritten, and `rename(2)`
  silently replaces an empty folder.
- **`onCollision: "replace"`**: an overwrite destroys; the UI can compose one undoably later.
- **A separate `file.rename`, or `file.write` with an exclusive flag for create**: the same
  primitive; and `file.write` makes no folders.
