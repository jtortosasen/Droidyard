# ADR-0054: A deleted file goes to its workspace's trash in the state directory

- Status: Accepted
- Date: 2026-10-11
- Superseded in part by: ADR-0091 (§Decision)
- Supersedes: ADR-0021 §Layout (in part: adds `ws/<wsid>/trash/`)
- Relates to: ADR-0021 §Location, §Format, ADR-0046 §4, ADR-0053; plan m2g Q2, D48, D50
- Decided by the main session under the owner's standing instruction (apply the recommended
  option), Delegated; the owner asked for a delete to a trash with Undo (plan m2d, the split).
  The shape is plan m2g's Q2, reviewed in two rounds.

## Context

`file.delete` (ADR-0053) must be undoable, and the undo must not depend on the tab or the
daemon staying up. State lives outside the project (ADR-0021 §Location, ADR-0046 §4), usually
on the same volume, but not always.

## Decision

1. A deleted item goes to `<state>/ws/<wsid>/trash/<id>/item`, its `entry.json` (path, type,
   time, size) written first. The id is 16 lowercase hex characters, checked before any path
   is built.
2. On the state directory's volume it is one rename, any size. Across volumes the item is
   renamed aside in its own folder, copied with every file and folder synced, and the aside
   removed only once the trash holds it whole; that copy is bounded, else `FILE_TOO_LARGE`
   and the item stays.
3. Entries are kept 7 days and at most 2 GiB per workspace, oldest first, never the newest;
   swept at each delete and when the workspace opens.
4. `file.restore` puts an item back at its recorded path, resolved again inside the root,
   never replacing. It works after a daemon restart; only the tab's Undo stack does not.

## Consequences

- Deleting a workspace's state empties its trash.
- There is no trash view and no emptying by hand yet (`roadmap/known-limits.md`).

## Alternatives considered

- **The system Trash**: macOS guards `~/.Trash` from a shell ("Operation not permitted"),
  Finder's Put Back needs Finder's metadata, and Linux's trash is another specification.
- **A trash inside the project**: every delete a same-volume rename, but ADR-0046 §4 keeps
  everything but the project file out of the tree, and the watch, Git and the build would
  see it.
- **Refusing cross-volume deletes**: smaller, but a project on an external disk could not
  delete from the tree at all.
- **Permanent delete with a confirmation**: the owner asked for a trash with Undo.
- **An undo copy in memory**: lost with the daemon, and unbounded.
