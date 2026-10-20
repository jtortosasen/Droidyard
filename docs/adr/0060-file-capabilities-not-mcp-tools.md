# ADR-0060: The file capabilities are not MCP tools

- Status: Accepted
- Superseded in part by: ADR-0095 (§Consequences)
- Date: 2026-10-12
- Supersedes: ADR-0053 §Decision (in part: item 1's "MCP tools under `--tools all`"; item 2's text rendering and "a successful MCP call")
- Supersedes: ADR-0053 §Consequences (in part: the first bullet, an agent's moves and deletes through the daemon; the third, `tools/list` growing by five)
- Supersedes: ADR-0036 §Decision (in part: item 4, the core profile without `file_list`, `file_read`, `file_write`)
- Supersedes: ADR-0058 §Consequences (in part: the agents' protection through `file_read`'s revision)
- Relates to: ADR-0015 §6, ADR-0045 §4, ADR-0037 Decision 4, ADR-0054; plan m2j Q1
- Decided by the owner, 2026-10-12: "No quiero gastar tokens en definiciones para algo que todos los agentes a día de hoy soportan"; "quítalos, sí". The mechanism is Delegated.

## Context

Eight of the nine `file.*` are MCP tools (`file.watch` is streaming). They take about 6.4 KB of the 34,786 B of `tools/list` a model reads (`TestToolListModelFacingBytes` at `b5e8dd9`), paid on every session. The agents the owner uses read, write, edit and delete with built-in tools (Claude Code's Read, Write, Edit, Bash; Cursor's; OpenCode's). That is the owner's statement, not measured. No usage data on agents and these tools exists. ADR-0053 kept them for agents because a delete through the daemon is contained and undoable. The owner puts the tokens first. An agent's own `rm` reaching the trash is the delete hook's concern (plan m2j Q2).

## Decision

1. All nine `file.*` have audience `human`. No MCP profile lists them, mcp-go answers a call "tool not found", and the core refuses them from an MCP session (`REQUEST_NOT_ALLOWED_HERE`, checked before statefulness, so `file.watch` gets it too). The UI (`client=ui`) and the CLI (`client=cli`, an agent's shell included) call them as before, with the same parameters, results and codes.
2. Audience `human` means "not on the agent surface", for any of three reasons: an agent must not authorise itself (ADR-0015 §6); a window on the user's screen (ADR-0045 §4); every agent has an equivalent already (here). No new audience value.
3. The renderings of `file.move`, `copy`, `delete` and `restore` go, since only MCP read them. The core profile is the golden path without file tools: 22 tools.
4. API `0.21.0`. `capability-api.md` says a breaking change is "a new name or a major version". Before 1.0 the major is the minor, as ADR-0036's changed `maxLines` default went out as 0.13.0. There is no `deprecated` period: no capability is removed or renamed, only the MCP projection of eight. `maxToolListBytes` drops to the new size rounded up to the next 1,000 B, so the saving is held.

## Consequences

- An agent's edits and deletes bypass the daemon: no containment, no Activity record, no `ifRevision`. The tree still sees them (`file.watch` polls), and the editor's `ifRevision` still refuses a save over an agent's write. The delete hook (plan m2j Q2) routes an agent's simple `rm` to the trash.
- The "droidyard only" comparison arm (m1l C2, m1o D1) cannot be run as defined.
- Allow rules from an earlier `init` stay (`init` only adds; `--uninstall` removes what it would write now). They name no tool.
- Code: `internal/domain/file`, `adapters/mcp` `CoreTools`, the `cmd/droidyard` tests and budget, the skill map. Documents: `mcp.md` §What is exposed, §Results, §Annotations; `files.md` §Why this exists; `vision.md` §Principles #1; `overview.md`; `mvp.md` §MVP capability surface; the register.

## Alternatives considered

- **Keep them** (ADR-0053's reasoning): containment and Undo for an agent's delete, at about 6.4 KB per session for work agents do with their own tools. Wins if usage data shows agents calling `file_*`.
- **Only out of `core`, or keep `file_read` and `file_write`**: the default `all` still pays, and the owner asked for all nine.
- **An exclusion list in the MCP adapter, audience left `all`**: the published schema would say `all` for capabilities no session lists. `init`, the skill map and the schema would then need a second source.
- **A new audience value (`ui`, `local`)**: every adapter would treat it exactly as `human`. Wins if a capability must one day reach the UI and not the CLI, or the reverse.
