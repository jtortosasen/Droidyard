# ADR-0058: `file.write` takes a revision precondition

- Status: Accepted
- Date: 2026-10-12
- Relates to: ADR-0032 §5 (`policy.update`'s `ifRevision`, the precedent); ADR-0021 §Format
  (the atomic write); ADR-0036 Decision 1; plan m2i Q1
- Decided by the main session under the owner's standing instruction (apply the recommended
  option), Delegated. The owner asked for the precondition on 2026-10-11/12 (plan m2i O1);
  its shape is the delegated part.

## Context

The editor (m2i) and agents write the same project files through `file.write`, and the last
writer wins: a save can erase an agent's write, and an agent a user's save. `files.md`
deferred conditional writes (register row 143). `policy.update` already guards its file
with `ifRevision`, a SHA-256 of the bytes or `absent` (ADR-0032 §5).

## Decision

1. `file.read` returns `revision`, the lowercase SHA-256 hex of the whole file, read to its
   end through the page's own descriptor, on every page; it is omitted for a file over
   8 MiB.
2. `file.write` takes an optional `ifRevision`: a revision, or `absent` (the file must not
   exist). Immediately before the rename the daemon opens the target without blocking and
   hashes it. A target over 8 MiB is `FILE_TOO_LARGE`, unhashed. A mismatch is
   `FILE_REVISION_CONFLICT`: nothing is written, `details` holds only `path`, the current
   revision appears in `cause` only, and the next step is "call file.read and apply the
   change again". A conditional write whose target already holds exactly `content`
   succeeds without writing, so a retried write stays idempotent.
3. `file.write` returns `revision`, the hash of `content`, never a re-read.
4. Every `file.write` on one project root holds one mutex from the compare to the rename.
5. Without `ifRevision` nothing changes. The API moves to `0.20.0`, additively.

## Consequences

- Agents get the protection by passing back `file_read`'s revision; a `file_read` result
  grows by about 80 bytes (ADR-0036).
- A writer outside the daemon that lands between the hash and the rename is still
  overwritten; `file.move`, `copy`, `delete` and `restore` do not take the mutex. Both are
  register rows.
- A page's hash costs reading up to 8 MiB; m2i step 2 benchmarks it.
- The rule lives in `internal/domain/file` and `files.md`.

## Alternatives considered

- **`modTime` and `size`**, no `file.read` change: coarse-mtime volumes and mtime-preserving
  tools (`cp -p`, `rsync -t`, `touch -r`) make a changed file look unchanged, the very loss
  to prevent, and a model would have to echo a nanosecond timestamp exactly.
- **An opaque token of inode, size, mtime and ctime**: cheap, but identical content
  conflicts, whether a rename updates ctime differs by OS (unverified), and it departs from
  `policy`'s revision.
- **A lease or lock held by the editor**: writers outside the daemon ignore it.
- **A client-side re-read before saving**: a round-trip window instead of microseconds, and
  agents get nothing.
- **A revision of the page only**: says nothing about the whole file a write replaces.
- **`policy.update`'s order, the revision checked before the no-op**: a retried write whose
  first attempt landed would conflict with itself; identical bytes lose nothing, so the
  no-op comes first here.
- **The current revision in `details`**: a blind retry with it would overwrite without a
  read, and `details` only grow (`capability-api.md` §Error model).
