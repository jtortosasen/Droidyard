# ADR-0038: `droidyard init` writes no `AGENTS.md` or `CLAUDE.md` block

- Status: Accepted
- Date: 2026-10-09
- Supersedes: ADR-0037 §Decision (in part: item 2's marked block in `AGENTS.md`, `CLAUDE.md` for Claude Code)
- Relates to: ADR-0036 §Decision 1, `mcp.md` §Server instructions, plan m1o step 2
- Decided by the owner, 2026-10-09.

## Context

ADR-0037 has `init` write a marked block of at most five lines into the project's
`AGENTS.md` (`CLAUDE.md` for Claude Code), telling the agent to prefer droidyard's tools
over adb, Gradle or uiautomator in a shell and to load the `droidyard-android` skill. The
same text reaches the agent twice more: in the server instructions (ADR-0036 §Decision 1),
which every client tested passes to the model, and in the skill, which a client loads from
its description. In fifteen runs with droidyard configured (plan m1o step 2 and §Changes)
no agent ran a shell command droidyard would have replaced. The block edits a file the
project's owners keep, often tracked in git, for a nudge with no measured effect.

## Decision

`init` writes no block into `AGENTS.md` or `CLAUDE.md`, and its `--uninstall` no longer
looks for one. Everything else in ADR-0037 §Decision stands: the skill, the MCP entry and
the read-only allowlist, in the project, merged into existing files.

## Consequences

- `init` touches only files that configure the assistant (`.cursor/`, `.claude/`,
  `.agents/`, `.mcp.json`, `opencode.json`), never the project's own instructions.
- A block written by the 1o `init` stays until removed by hand; no project used it
  (owner, 2026-10-09).
- A client that ignores server instructions and skills gets no hint to prefer droidyard.
  None of the three tested does.

## Alternatives considered

- **Keep the block behind a flag**: a second path to keep tested for text the
  instructions and the skill already carry; it wins if a client is shown to read
  `AGENTS.md` but neither of the other two.
- **Write everything at user level** (the skill, the MCP entry and the allowlist in each
  client's home configuration): rejected by the owner on 2026-10-09 in favour of keeping
  the integration per project. A user-level entry would run droidyard in every session,
  Android or not, and would need an empty-session mode outside a project and per-client
  evidence to stay cheap.
