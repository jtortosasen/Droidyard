# ADR-0127: The sessions panel lists chats read from the clients' own stores

- Status: Accepted
- Superseded in part by: ADR-0134 (§Decision 4)
- Date: 2026-10-23
- Supersedes: ADR-0083 §Decision (in part: §2, the panel's agent rows under each project); ADR-0048 §Decision (in part: §3, agent rows from operations, and plan m2d D30's "You" row); ADR-0088 §Decision (in part: §4, the chat id is never published)
- Relates to: ADR-0005, ADR-0010, ADR-0015 §1, ADR-0021 §Alternatives, ADR-0060 §2, ADR-0068 §1, ADR-0089 §2, ADR-0094, ADR-0095 §1; [plan m3c](../plans/m3c-sessions-by-chat.md) Q1–Q4
- Decided by the owner, 2026-10-23: the panel lists every chat by client, project and title, with no "You" row, and droidyard may read the clients' local chat stores for it (locally, nothing sent, formats undocumented, never message content). The shape is the main session's under the owner's standing instruction to apply the recommended option, Delegated.

## Context

The panel listed live MCP sessions, labelled by `session.name` (`claude-code 3`), and one
"You" row for the user's own commands. The owner wants what the clients themselves show: each
chat with its title, grouped Client → Project → chat, connected or not. Only the clients know
their chats: Claude Code keeps one JSONL transcript per chat under
`~/.claude/projects/<encoded cwd>/` (an `ai-title` line, transcripts up to 124 MB); Cursor keeps
SQLite databases (`~/.cursor/chats/…/store.db` for cursor-agent, `globalStorage/state.vscdb`
for the IDE). The formats are undocumented and change with the clients' versions. The daemon
builds with `CGO_ENABLED=0` (ADR-0068 §1).

## Decision

1. **`session.chats`**, a new capability: read-only, stateless, global, audience human (off
   MCP), no parameters. Each chat carries `client`, `store`, `id`, `project`, `worktree?`,
   `title?` (untrusted), `updatedAt`, and `session?`, the live session joined by chat id
   (ADR-0095 §1); the result has `sources` (each store read, absent or failed, with a cause)
   and `truncated`. The UI reads it on start, after `session.watch` changes and every 30 s
   (working default). The chat id is published: it is a file or directory name in the user's
   own store, and occupation is not a security boundary (ADR-0094 §9).
2. **What is read.** Only the chats of the projects the daemon knows (the index's main trees,
   at most 50) and their live linked worktrees (ADR-0089 §2); only the keys that give id,
   title, place and time, never a message field; a Claude Code transcript only in a 64 KiB
   window at its head and its tail; a chat is one the client's resume list would show. Title:
   `custom-title`, else the last `ai-title`, else `agent-name`, else none; at most 120
   characters, control and format characters removed. Working defaults: 30 chats per client
   and project, 200 file reads and 2 s a call, a cache on size and mtime.
3. **SQLite through the `sqlite3` command**, as `git` and `adb` are run (ADR-0005): `-init
   /dev/null -safe -readonly -json`, a query built from constants and quoted literals, the
   database opened `mode=ro` (`immutable=1` only on a closed WAL database), its process group
   killed at 3 s, its output capped at 4 MiB. Without `sqlite3` the Cursor stores are a failed
   source.
4. **The panel.** Projects keeps its project and worktree rows, without agent rows. A Sessions
   section below groups Client → Project → chat, newest first; a connected chat is one row with
   its session's state, holds and Release; a live session no chat claims is a row of its own
   (a name no client claims under "Other"). No "You" row and no rows from operations: the
   user's operations stay in Activity. The panel numbers repeated names among the rows listed
   now; Activity, the build status and the devices keep a per-tab label that is never
   renumbered. A row of Claude Code or cursor-agent copies its resume command.

## Consequences

- One contract change, API 0.38.0. A changed store format reads as an untitled or empty group,
  never an error; the clients' formats are now a dependency the register tracks.
- Cursor chats are never shown connected: Cursor's connection carries no chat id (row 287).
- OpenCode's chats, Linux's `sqlite3`, and the encoding of non-ASCII paths are register rows.
- To follow it: `internal/domain/chats` (its package comment states the bounds),
  `web/src/lib/projects/sessions.ts`'s header comment, `docs/architecture/process-lifecycle.md`
  (`sqlite3` per call).

## Alternatives considered

- **Chats inside `session.watch`.** A stream of live connections would carry rows read from
  files that change on no event of the daemon's, mixing two lifetimes, as ADR-0083 rejected for
  workspaces.
- **A streaming capability over an OS watch of the stores.** Directories growing by megabytes
  an hour, watched for a panel a 30 s read serves.
- **Joining in the browser** by publishing the chat id on `session.watch`: every live session's
  chat id on a stream, for the same result.
- **Every chat in the stores.** The other projects' chats include repositories that must not be
  read, and the owner asked for their projects.
- **A full parse of the transcripts** (as Orca does): 124 MB read on every refresh.
- **The first prompt as a fallback title**: message content, outside the owner's condition.
- **`mattn/go-sqlite3`**: cgo, against ADR-0068's cross-compile. **`modernc.org/sqlite`**: a
  multi-megabyte dependency to read a few keys, ADR-0021's objection. Either wins if
  `sqlite3` turns out missing where users run droidyard. **Copying the database first**:
  hundreds of MB, and a copy without its `-wal` misses live rows.
- **The whole column as Client → Project → chat**: a project with no chat could not be opened,
  and worktree rows, Release and Remove would lose their place. **A resume button that launches
  the client**: droidyard launches no client (ADR-0004).
