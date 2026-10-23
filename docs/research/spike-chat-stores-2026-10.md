# Spike: the clients' chat stores, 2026-10-23

Plan m3c step 1. Run by the main session on the owner's Mac, on this repository's chats only
(never another repository's, not even their structure, but the Cursor IDE counts over its
whole table noted below), keys and counts only: no title or message text was printed. Claude
Code 2.1.309, cursor-agent with `--resume [chatId]`, `sqlite3` 3.51.0 (`/usr/bin`).

## (a) Claude Code: the chat id is the transcript's name

- **Observed.** This session's `CLAUDE_CODE_SESSION_ID` (`a9bde9a3-…`) is the stem of the only
  transcript under `~/.claude/projects/-Users-jorgetortosa-Documents-AioAndroidDev--claude-worktrees-m3c/`,
  with its side directory of the same name. The session started in the main tree and entered
  the m3c worktree: the file moved to the worktree's directory.
- **Not observed:** `custom-title` (`/rename`): no transcript of this repository holds one.

## (b) cursor-agent

- **Observed.** `~/.cursor/chats/<md5 of the root path>/` exists for this repository (`md5 -qs`
  of `/Users/jorgetortosa/Documents/AioAndroidDev`), with one chat directory.
- Each chat directory holds `store.db` (WAL: `PRAGMA journal_mode` is `wal`, with `-wal` and
  `-shm` beside it) and, in 69 of the 81 chat directories on the machine, `meta.json` with the
  keys `schemaVersion`, `createdAtMs`, `updatedAtMs`, `hasConversation`, `cwd` (no title). This
  repository's one chat (2026-10-07) has no `meta.json`.
- **The title** is in `store.db`: tables `meta (key TEXT PRIMARY KEY, value TEXT)` and `blobs`;
  `meta` has one row, key `0`, whose value is hex-encoded JSON with the keys `agentId`,
  `latestRootBlobId`, `name` (the title), `mode`, `createdAt`, `lastUsedModel`.
- **Resume:** `cursor-agent --resume [chatId]`.

## (c) Cursor IDE

- **Observed.** `globalStorage/state.vscdb` is WAL (its `-wal` was 0 bytes, `-shm` 32 KiB).
  `composerHeaders (composerId TEXT PRIMARY KEY, workspaceId, createdAt, lastUpdatedAt, isArchived,
  isSubagent, recency, checkpointAt, value, subagentTypeName)` (`.schema`, before the lane's
  claim) has 601 rows, 31 distinct `workspaceId`; the keys of one row's `value`, read in that
  same first probe, include `composerId`, `lastUpdatedAt`, `createdAt`, `isArchived`, `isDraft`
  and `workspaceIdentifier`; at the close, `value.composerId` equals the column in all 601 rows.
  Columns `isArchived` (109 set) and
  `isSubagent` (214 set); in `value`: `name` (the title, in 438 rows; in 203 of the 270 rows
  neither archived, subagent nor draft), `isDraft`, `isWorktree` (never set here),
  `workspaceIdentifier {id, uri {fsPath, path, scheme, external, $mid}}`; `id` equals the
  `workspaceId` column in all 601 rows; `uri.fsPath` is present in 454 rows.
- **The place is in the row**: `json_extract(value, '$.workspaceIdentifier.uri.fsPath')`, so the
  candidate paths filter in SQL and `workspaceStorage/*/workspace.json` (keys `folder`, only 13
  of the 31 ids have a directory) is not needed.
- No row and no `workspace.json` names this repository: the IDE was never opened on it.
- No resume command (the IDE has no CLI resume).

## (d) clientInfo.name

- **Observed:** cursor-agent sends `{"name":"Cursor","version":"1.0.0"}` (a recorder as its MCP
  server, `mcp list`, isolated `HOME`), capital C. Claude Code sends `claude-code` (earlier runs).
- **Not observed:** the Cursor IDE's name.

## (e) sqlite3

- **Observed:** 3.51.0 here (≥ 3.37, so `-json` and `-safe` exist).
- **A `mode=ro` read writes the `-shm`**: reading cursor-agent's `store.db` moved its `-shm`
  mtime; `store.db` and `-wal` kept size and mtime. Every reader of a WAL database maps and
  updates `-shm`; only `immutable=1` avoids it, and that misses the rows still in `-wal`.
- **Not observed:** the CI image (`ubuntu-24.04`); its first run with the tests says.

## (f) What Claude Code calls a chat

- **Observed**, over this repository's 12 store directories, 80 transcripts: `entrypoint` `cli`
  25 (20 titled), `sdk-cli` 49 (none titled: the headless `claude -p` runs of tools and
  probes), 6 with no `cwd` line and no entrypoint. The last `ai-title` lies 1.1–44.1 KiB from
  the file's end in all 20 titled files.
- **Not observed:** whether `claude --resume`'s own list shows the `sdk-cli` runs.
