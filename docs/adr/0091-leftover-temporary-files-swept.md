# ADR-0091: Leftover temporary files are swept, and asides get their own name

- Status: Accepted
- Date: 2026-10-18
- Supersedes: ADR-0054 §Decision (in part: a cross-volume delete's aside is named `.droidyard-aside-<16 hex>.tmp`)
- Relates to: ADR-0021 §Format (the atomic write recipe), ADR-0053, ADR-0054; `files.md` §The capabilities, §The watch; plan m2g §Not in this milestone; plan m2r
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated. The owner asked for the `quick` rows and a cleanup of the register, 2026-10-18; the planner judged this row not quick (plan m2r §Not in this milestone, draft decision).

## Context

`file.write` and `file.copy` write a `.droidyard-<16 hex>.tmp` file or folder in the
target's directory and rename it into place (ADR-0021's recipe). A daemon killed between
the two leaves it in the project; `files.md` calls it "a transient file, not state", and
`file.watch` hides it, so nobody sees it and nothing removes it (register rows 163, 166).

A sweep cannot simply remove every such name:

- A cross-volume delete (ADR-0054) renames the item aside under the same pattern
  (`trash.go` `asidePattern`) while it copies it to the trash; until the copy is synced
  and the aside removed, the aside is the only copy, and after a crash the trash's
  `Sweep` restores or removes it by its entry.
- Another state root's daemon (a second `DROIDYARD_STATE_DIR`) may be writing in the same
  project; its in-flight temporary file is young.

## Decision

1. **Asides are named `.droidyard-aside-<16 hex>.tmp`.** New cross-volume deletes use it.
   The trash's restore and sweep accept both the new name and the old
   `.droidyard-<16 hex>.tmp` in an entry's `aside`, so entries written by an older binary
   still resolve.
2. **The watch sweeps.** When a `file.watch` tick's walk meets an entry named exactly
   `.droidyard-<16 hex>.tmp` (not an aside), file or folder, whose modification time is
   more than 24 hours old (working default), it removes it, through the workspace's
   `os.Root`, without following a link. The walk already visits each watched directory
   and already recognises the name; nothing else walks the project for it.
3. **An aside is never swept by the watch**, whatever its age: only the trash's `Sweep`,
   which holds its entry, removes an aside. An old-named aside that an entry of this state
   root records is skipped by the watch too.
4. The sweep is silent: no event, no entry in Activity. A failure to remove is ignored and
   retried on a later tick.

## Consequences

- Rows 163 and 166 close. A leftover in a directory no tab watches stays until one does.
- `internal/domain/file`: the aside name in `trash.go`, the sweep in `watch.go`, tests for
  the age bound, an aside left alone, an old-named recorded aside left alone, and a
  symlink named like a temporary file removed as a link, never followed.
- `files.md` §The capabilities' "a transient file, not state" sentence gains the sweep;
  §The watch says the walk sweeps.

## Alternatives considered

- **Sweep at workspace open over the whole project.** A full walk of a large project
  (5,460 directories took 0.46 s per watch tick on the owner's measure) at every open, for
  files that are almost never there.
- **Record each temporary file in the state directory and remove it at the next start.**
  Exact, but a write per write in the hot path, and a record that a second state root
  cannot see.
- **Keep one name and skip every name an entry of this state root records.** An aside of
  another state root's trash, older than 24 hours after a crash, would be swept while it
  is the only copy; a distinct name rules that out for every aside written from now on.
- **A shorter age.** A large `file.copy` of a folder can legitimately run for minutes; a
  day is far past any write and still clears leftovers within a day of use.
