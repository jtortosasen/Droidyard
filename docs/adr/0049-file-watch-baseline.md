# ADR-0049: `file.watch` returns its baseline and publishes its events, for the file tree

- Status: Accepted
- Date: 2026-10-10
- Relates to: ADR-0010, ADR-0020 §Retention, ADR-0036, ADR-0041 §4; `files.md` §The watch;
  plan m1j D7, D8; plan m2d Q3
- Decided by the owner, 2026-10-10: a rich file tree whose external changes (the agent's)
  show at once. The shape below is Delegated (plan m2d Q3, reviewed in two rounds).

## Context

`file.list` lists one level. `file.watch` walks the tree each second but returns only a
count, and its `file.changes` events are not in the schema, so the client is not generated.
A browser walking `file.list` folder by folder would record one operation per directory:
two `operation.state` events each to every watching tab, and a flooded trace.

## Decision

1. `file.watch`'s result carries `entries [{path, type}]`, always: the baseline walk the
   first tick diffs against, sorted, at most 20,000 (the existing `truncated`). It is
   slimmed from the stored result after delivery (ADR-0020 §Retention), as `file.list`.
2. `file.changes` is published through the descriptor's `events` (types `changes` and
   `stopped`, today's payloads), so the client is generated (ADR-0041 §4).
3. One root watch per tab feeds the tree, its refresh and the palette's index; `file.list`
   serves hidden entries and a truncated baseline. A tick cut at 1,000 changes
   (`truncated`) makes the client watch again. No parameter, no new error code; streaming,
   so no MCP change.

## Consequences

- A tree with every visible folder current within a second; the walk's cost is measured
  (plan m2d step 4) and gates the design.

## Alternatives considered

- **The client walks `file.list`**: no contract change, but one recorded operation per
  directory.
- **A recursive `file.list` or a `file.index` query**: an MCP tool (ADR-0036) for what an
  agent's own glob does.
- **An OS watcher**: m1j D7's rejection stands.
- **A daemon that never cuts a tick**: unbounded events; the watch again costs one walk.
