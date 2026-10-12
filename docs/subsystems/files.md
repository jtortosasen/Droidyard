# Subsystem: files

Reading, writing, watching and changing project files: `file.list`, `file.read`,
`file.write`, `file.watch`, and the tree's operations (§Operations). All are **workspace-scoped** and run against a workspace's project root
without running project code. Decisions D2-D9 of [`plans/m1j-files.md`](../plans/m1j-files.md).
Parameters, results, bounds and error lists are the descriptors' (`internal/domain/file`).

## Why this exists

`file.*` serves the UI and the CLI; it is not on the agent surface
([ADR-0060](../adr/0060-file-capabilities-not-mcp-tools.md)). Agents use their own tools, so
their edits bypass the daemon: the tree sees them by polling, and the editor's `ifRevision`
catches them. `file.*` needs no JVM, device or Gradle.

## Paths and containment

`path` is slash-separated and **relative to the project root**; `""` and `"."` are the root,
and an absolute path is accepted and treated the same. Every path is contained by an `os.Root`
opened on the workspace's **canonical** root for the call (D2). Containment is the root's, not
a check-then-open, so no race separates deciding a path is inside from using it.

- A relative path that `filepath.IsLocal` rejects after cleaning, an absolute path outside the
  root, `".."`, and any symlink whose target would leave the root are `FILE_OUTSIDE_ROOT`. A
  symlink with an **absolute** target is refused even when it points inside the root (the
  standard library refuses those).
- An `os.Root` refusal is recognised by the standard library's escape error text; a test pins it,
  so a Go release that changes it fails loudly rather than turning an escape into an I/O failure.
- `file.read` follows an in-root **relative** symlink. `file.write` never follows a symlink as
  its final component: a rename would replace the link, not its target, so it is
  `FILE_WRONG_TYPE`.

## The capabilities

- **list** is one level, never recursive; it reports a symlink, never follows it.
- **read** is regular files only, opened `O_RDONLY|O_NONBLOCK` and `fstat`ed, so a FIFO is
  `FILE_WRONG_TYPE` at once and never blocks. A window holding a NUL or invalid UTF-8 (a rune
  cut by the window's end excepted) is `binary: true`, `text` omitted. Paging is
  `artifact.read`'s.
- **write** is atomic by [ADR-0021](../adr/0021-persistence-format-and-location.md)'s recipe:
  a `.droidyard-<16 hex>.tmp` in the target's directory, written, `fsync`ed, given the
  target's mode, renamed over it, the directory `fsync`ed. The target **keeps its on-disk name**
  (so `src/a.kt` replaces `src/A.kt` on a case-insensitive volume without renaming it). Writes
  under `.git/` are allowed. A daemon killed between the temporary file and the rename leaves
  it in the project: a transient file, not state, named so a human recognises it and ignored by
  `file.watch`.
- **Conditional write** ([ADR-0058](../adr/0058-file-write-revision-precondition.md)): `file.read`
  returns the whole file's SHA-256 as `revision` on every page, and `file.write`'s `ifRevision`
  writes only if the file still has it. A hash, not mtime and size: coarse clocks and `cp -p`
  hide a change. Daemon writes on one root hold a mutex from the compare to the rename; a
  writer outside the daemon landing there is still overwritten; `file.move`, `copy`, `delete`
  and `restore` take no mutex.

## Operations

`file.create`, `file.move` and `file.copy` change the tree and replace nothing
([ADR-0053](../adr/0053-file-operations.md)). A rename is a move inside one folder. A move
opens each item's two parent folders through the `os.Root` and renames on their descriptors
with the kernel's no-replace rename (`RENAME_EXCL` on macOS, `RENAME_NOREPLACE` on Linux), so
containment stays the root's, the final name is never followed, and a writer that takes the
target first keeps its file: the item is `FILE_EXISTS`. `os.Root`'s `Rename` is not used,
because `rename(2)` replaces a file and an empty folder. A volume that cannot refuse a replace
fails the item `FILE_IO_FAILED`; there is no fallback. The one plain rename is a case-only or
normalisation-only rename of the entry itself on a volume that folds names.

A move is a batch: what the request's text alone decides (a path outside the root, the root
itself, a source equal to its target or holding it) fails the whole call; what only the
filesystem can say fails its item, and the call completes with every item's outcome.

`file.delete` moves to a trash, `file.restore` puts back ([ADR-0054](../adr/0054-trash.md)).

## The watch

`file.watch` creates a **subscription** (so it is a command, and streaming: WebSocket only,
`SESSION_REQUIRED` elsewhere, [ADR-0010](../adr/0010-capability-statefulness.md)). It **polls**
(D7): the handler walks the directory through the root's `fs.FS` for a baseline, then a
goroutine walks again **every second** and publishes the differences. No OS notification API is
used: FSEvents needs cgo on macOS or one kqueue descriptor per file, and inotify is Linux-only;
a second's latency suits a file explorer. The baseline is also the result's `entries`, the map the
first tick diffs against and never a second walk, so a client builds the tree from one call and the
events ([ADR-0049](../adr/0049-file-watch-baseline.md)).

A change is an entry's **type, size or modification time** differing; a directory's own size
and mtime are not compared (a child change already names itself). A tick with no change
publishes nothing. A walk:

- records at most 20 000 entries and never descends a symlink (recorded as one entry);
- excludes `.git/`, `.gradle/`, `.idea/`, `.kotlin/`, `.cxx/`, a `build` directory whose parent
  holds a `build.gradle`/`build.gradle.kts` (a source package named `build` is kept), and
  `file.write`'s and `file.copy`'s temporary files and folders;
- on a tick that reaches the cap, reports **no deletions at all** and marks its event
  `truncated`: it cannot tell a deleted entry from one pushed past the cap, so the client
  re-lists; one that later falls under the cap is reported `created`;
- on a tick, a subdirectory that cannot be read keeps its last known entries, reported
  unchanged, and an entry gone between the listing and its stat is skipped. A failed walk is
  never "nothing changed": if the watched directory itself is gone (or no longer a directory),
  every entry is reported `deleted` and the watch stops with reason `pathGone`; if it can no
  longer be read, it stops with reason `unreadable`.

### The topic `file.changes`

`file.changes` **takes a `seq` but never enters the replay ring** (D8): a busy watch would
otherwise flush the 4096 events and 4 MiB that other subscribers resume from through `since`.
It is offered to the subscription the watch created and to **no other**, is not in
`events.subscribe`'s topic enum, and has **no `since`**: a watch that was not running produced
nothing to replay, so after a reconnect the client watches again and re-lists
([`capability-api.md`](../architecture/capability-api.md) §Subscriptions,
[`http-websocket.md`](../interfaces/http-websocket.md) §`GET /v1/ws`).

### A watch's lifetime

A watch's poller ends when its **subscription** ends (`events.unsubscribe` of its id, or the session's
end), when its **workspace closes**, or when its path is gone or unreadable, whichever comes first
(D9). On a workspace close, or a path gone or unreadable, the poller publishes `stopped` as its own
last act, so nothing follows it; the subscription stays open until the session or the client ends it,
so `stopped` is delivered. The handler closes its subscription and poller on every path that does not
return an id and re-checks `ctx.Err()` as its last act; the close hook signals the poller and never
waits for it while holding the bus's lock. A watcher belongs to the **session's subscription**, not to
the workspace ([`workspace-state.md`](../architecture/workspace-state.md) §State model). One narrow
window remains, a stated limit: a close's forced cancel landing after the handler's last check ends
the operation `cancelled` with the subscription created, which then holds `stopped` until the session
ends.

## Trust

`file.*` runs no trust check and marks no result: project content is not
[untrusted data](../adr/0015-untrusted-data-authorization.md) (ADR-0015 §1), and §5 lets
`file.*`, `git.*` and `workspace.describe` work on an untrusted root (`git.*` barring
[ADR-0066](../adr/0066-daemon-git-watch-and-read.md) §3), as agents read the same
bytes.
