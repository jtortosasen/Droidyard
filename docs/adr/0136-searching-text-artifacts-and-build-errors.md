# ADR-0136: Searching text artifacts and build errors

- Status: Accepted
- Date: 2026-10-27
- Supersedes: ADR-0012 §Reading artifacts (in part: "one capability serves every artifact type"; a second one searches text artifacts)
- Relates to: ADR-0012 §Durability classes, ADR-0015 §1, ADR-0036 §1, ADR-0060 §2, §4, ADR-0099 §2, ADR-0100 §3, ADR-0135; `capability-api.md` §Discriminated tools; [plan m3i](../plans/m3i-agent-filters.md) Q6–Q7
- Decided by the owner, 2026-10-27: an agent never falls back to a terminal to filter what droidyard returns. The shape below is the main session's under the owner's standing instruction to apply the recommended option, Delegated (plan m3i, reviewed in two rounds).

## Context

Text artifacts are where droidyard puts what is too large to return inline: the
`device.logs` capture (≤ 4 MiB), the build console (≤ 32 MiB), `shell.exec` output, the UI
dump, a recording's timeline. `artifact.read` pages them by byte offset (16 KiB default,
256 KiB at most), so finding one line in a 32 MiB console means up to 128 reads, or a
terminal `grep`. `build.errors` returns up to 200 errors and 50 warnings with no way to
narrow them to the file the agent is fixing.

## Decision

1. **`artifact.search`**, a second capability over text artifacts: `{ref, match (1–20,
   required), exclude, context (0–10), maxLines (1–500, default 50), maxLineBytes
   (16–4,096, default 512), keep (oldest default, newest), offset}` →
   `{ref, mediaType, lines: [{line, text, context?, cut?}], matched, returned, scanned,
   size, nextOffset?, growing, truncated}`. Patterns are ADR-0135 §1's (RE2, the same
   bounds, validated before the reference is resolved). A call scans line by line, never
   the whole artifact in memory, numbers lines from the artifact's start, and ends after
   8 MiB scanned or ADR-0135 §2's `FilterBudget`, with `nextOffset`; a growing artifact's
   unterminated last line is held back, as `artifact.read` holds it. An image is
   `REQUEST_INVALID_PARAMS`. The result carries the artifact's untrusted mark.
2. Query, global, stateless, audience `all`, in the MCP core profile, not lendable (a
   borrower searches relayed artifacts in its own store, ADR-0100 §3). The server
   instructions are unchanged (their budget is full); `artifact.read`'s description and
   every `capture` and `consoleRef` description point to `artifact_search`.
3. **`build.errors`** gains `file` (a prefix of the record's published `file`), `match` and
   `exclude` over the message, applied within the 200/50 caps, and `filtered`, the count
   they dropped.
4. API `0.41.0`, with ADR-0135.

## Consequences

- One more MCP tool in the core profile, held by ADR-0060 §4's `tools/list` rule.
- A 32 MiB console takes up to four calls to search whole.
- To change with this decision: `internal/artifact`, `internal/domain/artifacts`,
  `internal/domain/build`, `adapters/mcp`, the schema, and `docs/interfaces/mcp.md`
  §Images and artifacts.

## Alternatives considered

- **`match` on `artifact.read`**: one tool with two result shapes, against
  `capability-api.md` §Discriminated tools; the agent cannot tell from the call which it gets.
- **Search only the logs capture**: the console and `shell.exec` output are what agents grep
  as often.
- **HTTP `Range` for agents**: paging, not searching; the UI's route stays as it is.
- **Lend `artifact.search` to peers**: a borrower already holds what it fetched; a host's
  own artifacts are not the peer's to read.
- **`build.errors` by module or severity**: a record holds a path, not a Gradle project, and
  severity has two values, which `includeWarnings` already chooses between.
