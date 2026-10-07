# Subsystem: files

Reading, writing and watching project files: `file.list`, `file.read`, `file.write`
and `file.watch`. All four are **workspace-scoped** and run against a workspace's
project root without running project code. Decisions D2–D9 of
[`plans/m1j-files.md`](../plans/m1j-files.md); API `0.9.0`.

> Status: draft. The watch polls; it uses no OS notification API.

## Why this exists

An agent that can read and write project files itself already edits a project; the
value here is that the edits go through **one daemon**, are **contained** to the
project root, and are **observable** — the UI's file panel and any agent can watch a
directory change. `file.*` is the JVM-free half of that: it works on a device-less,
Gradle-less daemon, and on trusted and untrusted roots alike.

## Paths and containment

`path` is slash-separated and **relative to the project root**; `""` and `"."` are the
root. An absolute path is accepted and treated the same.

Every path is contained by an `os.Root` opened on the workspace's **canonical** root
for the call (D2). Containment is the root's, not a check-then-open: an open, stat,
read, mkdir or rename goes through it, so there is no race between deciding a path is
inside and using it.

- `""` where a file is required, and a NUL byte anywhere, are `REQUEST_INVALID_PARAMS`.
- A relative path that `filepath.IsLocal` rejects after cleaning, an absolute path
  outside the root, `".."`, and any symlink whose target would leave the root are
  `FILE_OUTSIDE_ROOT`. A symlink with an **absolute** target is refused even when it
  points inside the root (the standard library refuses those).
- An `os.Root` refusal is recognised by the standard library's escape error
  (`"path escapes from parent"`); a test pins that text, so a Go release that changes
  it fails loudly rather than turning an escape into an I/O failure.
- `file.read` follows an in-root **relative** symlink. `file.write` never follows a
  symlink as its final component: a rename would replace the link, not its target, so
  it is `FILE_WRONG_TYPE`.

## The capabilities

| Capability | Kind | Result |
| --- | --- | --- |
| `file.list {path?, limit?}` | Q | one level (never recursive), `{path, entries [{name, type, size?, modTime}], truncated, hint?}`; sorted by name; `limit` 1–2000, default 500 |
| `file.read {path, offset?, limit?}` | Q | a byte range, paging exactly as `artifact.read`; `{path, size, modTime, offset, nextOffset, text?, truncated, binary}` |
| `file.write {path, content}` | C | a whole file, written atomically; `{path, size, created}` |
| `file.watch {path?}` | C (streaming) | a subscription id and then `file.changes` events; `{subscriptionId, watched, truncated, excluded}` |

- **list** reports a symlink, never follows it; a directory of more than 20 000 names
  yields a sorted subset with `truncated`. A slimmed (retained) result drops the
  entries.
- **read** is regular files only, opened `O_RDONLY|O_NONBLOCK` and `fstat`ed, so a
  FIFO is `FILE_WRONG_TYPE` at once and never blocks. A window holding a NUL or invalid
  UTF-8 (a rune cut by the window's end excepted) is `binary: true`, `text` omitted.
  A slimmed result drops the text.
- **write** is atomic by [ADR-0021](../adr/0021-persistence-format-and-location.md)'s
  recipe: a `.<name>.droidyard-<8 hex>.tmp` in the target's directory, written,
  `fsync`ed, given the target's mode, renamed over it, the directory `fsync`ed. The
  target **keeps its on-disk name** (so `src/a.kt` replaces `src/A.kt` on a
  case-insensitive volume without renaming it). Content is UTF-8, at most 512 KiB.
  Writes under `.git/` are allowed. A daemon killed between the temporary file and the
  rename leaves it in the project — a transient file, not state, named so a human
  recognises it and ignored by `file.watch`.

## The watch

`file.watch` creates a **subscription** (so it is a command, and streaming: WebSocket
only, `SESSION_REQUIRED` elsewhere — [ADR-0010](../adr/0010-capability-statefulness.md)).
It **polls** (D7): the handler walks the directory through the root's `fs.FS` for a
baseline, then a goroutine walks again **every second** and publishes the differences.
No OS notification API is used: FSEvents needs cgo on macOS or one kqueue descriptor
per file, and inotify is Linux-only; a second's latency suits a file explorer.

A change is an entry's **type, size or modification time** differing; a directory's own
size and mtime are not compared (a child change already names itself). A tick with no
change publishes nothing. A walk:

- records at most **20 000 entries** (`watched`, `truncated`; the field's description
  says to watch a subdirectory, there is no runtime hint) and never descends a symlink — the symlink is recorded as one entry;
- excludes `.git/`, `.gradle/`, `.idea/`, `.kotlin/`, `.cxx/`, a `build` directory
  whose parent holds a `build.gradle`/`build.gradle.kts` (a source package named
  `build` is kept), and `file.write`'s temporary files;
- on a tick that reaches the cap, reports **no deletions at all** and marks its event
  `truncated`: it cannot tell a deleted entry from one pushed past the cap, so the
  client re-lists; one that later falls under the cap is reported `created`;
- on a tick, a subdirectory that cannot be read keeps its last known entries, reported
  unchanged, and an entry gone between the listing and its stat is skipped. A failed
  walk is never "nothing changed": if the watched directory itself is gone (or is no
  longer a directory), every entry is reported `deleted` and the watch stops with
  `stopped` reason `pathGone`; if it can no longer be read, it stops with reason
  `unreadable`.

## The topic `file.changes`

Type `changes` carries `{path, changes [{path, kind: created|modified|deleted, type}],
truncated}`, at most 1000 changes per event; type `stopped` carries `{path, reason}`,
reason `workspaceClosed`, `pathGone` or `unreadable`.
The event's `workspace` is the workspace id and its `operation` is the `file.watch`
call's id.

`file.changes` **takes a `seq` but never enters the replay ring** (D8): a busy watch
would otherwise flush the 4096 events and 4 MiB that other subscribers resume from
through `since`. It is offered to the subscription the watch created and to **no
other**, is not in `events.subscribe`'s topic enum, and has **no `since`**: a watch that
was not running produced nothing to replay, so after a reconnect the client watches
again and re-lists.
([`architecture/capability-api.md`](../architecture/capability-api.md) §Subscriptions,
[`interfaces/http-websocket.md`](../interfaces/http-websocket.md) §`GET /v1/ws`.)

## A watch's lifetime

A watch's poller ends when its **subscription** ends (`events.unsubscribe` of its id,
or the session's end), when its **workspace closes**, or when its path is gone or
unreadable, whichever comes first (D9). On a workspace close, or a path gone or
unreadable, the poller publishes `stopped` as its own last act, so nothing follows
it; the subscription stays open until the session or the client ends it, so `stopped` is
delivered. The handler closes its subscription and poller on every path that does not
return an id (an error, a cancel during the baseline walk), re-checks `ctx.Err()` as its
last act, and cleans up on a cancel. The close hook signals the poller and never waits
for it while holding the bus's lock.

A watcher belongs to the **session's subscription**, not to the workspace
([`architecture/workspace-state.md`](../architecture/workspace-state.md) §State model):
the workspace's close only knows which pollers to stop. One narrow window remains, a
stated limit: a close's forced cancel landing after the handler's last check ends the
operation `cancelled` with the subscription created, which then holds `stopped` until
the session ends.

## Errors

`FILE_NOT_FOUND` (a missing path; next step: `file.list` the parent); `FILE_OUTSIDE_ROOT`
(a path that leaves the root; `details {path, root}`); `FILE_WRONG_TYPE` (a directory
where a file is needed or the reverse, a FIFO, socket or device, a write to a symlink;
`details {path, type}`); `FILE_IO_FAILED` (permissions, a read-only file, no space,
anything else; `cause` is the OS error). `WORKSPACE_NOT_OPEN` and `SESSION_REQUIRED`
come from `Start`, not from a handler. `file.read`, `file.list` and `file.watch` never
return `FILE_IO_FAILED` for a missing path; `file.write` has no `FILE_NOT_FOUND` (it
creates a missing target, and any `ENOENT` during the write is `FILE_IO_FAILED`).

## Trust

`file.*` runs no trust check and marks no result: project content is not
[untrusted data](../adr/0015-untrusted-data-authorization.md) (ADR-0015 §1), and §5
lets `file.*`, `git.*` and `workspace.describe` work on an untrusted root — the agent's
own file tools read the same bytes. No `file.*` field is tagged.

## Not here

Recursive listing, globbing, search, a list cursor, delete, rename, mkdir, conditional
writes, writes through symlinks; an OS notification watcher; a configurable interval,
caps or exclusions; `since` and filters on the watch. Deferred to later milestones or
the Phase 2 file panel.
