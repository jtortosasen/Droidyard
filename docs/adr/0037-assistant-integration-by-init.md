# ADR-0037: Optional assistant integration, written by `droidyard init`

- Status: Accepted
- Superseded in part by: ADR-0038 (§Decision)
- Date: 2026-10-08
- Supersedes: ADR-0014 §Decision (in part: per-assistant configuration is written into the project, not only printed)
- Supersedes: ADR-0019 §Processes (in part: items 3 and 4, the subcommand set gains `init`, which never auto-starts the daemon)
- Supersedes: ADR-0024 §Decision (in part: the subcommands row gains `init`)
- Relates to: ADR-0004, ADR-0011 §Decision, ADR-0015 §5, §6, `mcp.md` §Features beyond tools,
  `mvp.md` §Out of scope, plan m1o Q2 and §Changes
- Decided by the main session under the owner's standing instruction (Delegated,
  2026-10-08, plan m1o Q2, narrowed by step 2's evidence); the owner can revert it.

## Context

The owner's brief of 2026-10-07 (item E) asked for droidyard's integration with the
assistants it serves: an `AGENTS.md` block, one skill, the read-only tools allowlisted,
an `init`-style command, and a Cursor shell redirect through hooks. ADR-0014 decided
generated per-assistant MCP snippets and one optional skill, and `mvp.md` kept skills and
distribution out of the MVP. Setting a project up by hand today means writing three JSON
files per client with the binary's absolute path (a GUI-launched client has no shell
`PATH`, ADR-0019 §Processes 8) and approving every tool call once.

The redirect hook was to be built only if a need appeared (`mcp.md` §Features beyond
tools). Plan m1o step 2 measured it: in five runs of Cursor with both its own shell and
droidyard available, the agent ran no shell command at all, so a redirect would have
changed nothing [obs, plan m1o step 2]. Hooks are not adopted here.

## Decision

1. **Optional.** Every capability works without these files (ADR-0004).
2. **`droidyard init`**, run by the user in a project, never through MCP, writes for
   `--client cursor|claude|opencode|all`: a marked block of at most five lines in
   `AGENTS.md` (`CLAUDE.md` for Claude Code), the skill (ADR-0011's format) under
   `.agents/skills/droidyard-android/` (and `.claude/skills/` for Claude Code), and per
   client the MCP server entry and an allowlist of the read-only tools, each naming the
   binary by its absolute path. It merges by key, appends to arrays only what is absent,
   keeps an existing entry under its key and reports it, refuses a file it cannot parse
   (or a JSONC file whose comments it would lose) and prints the entry to add instead, and
   `--uninstall` removes exactly what it would write now. `--dry-run` prints the changes.
3. **The allowlist follows `readOnlyHint`**, so it also approves the queries that run
   project code on a trusted root (`project_*`, `build_tasks`, `sdk_requirements`); `init`
   prints the list it writes. Trust stays the user's (ADR-0015 §5): `init` trusts nothing.
4. **Generated from one source.** The allowlist and the skill's tool map come from the
   registry, with tests that fail on drift.
5. **`init` joins the subcommands** (ADR-0019 §Processes 3, ADR-0024) and, like `status`
   and `stop`, never auto-starts the daemon (ADR-0019 §Processes 4).

## Consequences

- `mvp.md` §Out of scope moves one skill and `droidyard init` into the MVP; binaries, an
  installer, registries, `llms-install.md` and telemetry stay out.
- Three clients' configuration formats live in `internal/assistant`; a client that changes
  its format breaks `init` for that client until updated, never the tools.
- These are files the user asked for, in the user's project; ADR-0021's "never inside the
  project tree" is about droidyard's state, which `init` does not write.
- Assistant-side hooks stay "not adopted" in `mcp.md`, now with a measurement behind it;
  the redirect and a session-start context are deferred in the register.

## Alternatives considered

- **A shell-redirect hook (`droidyard hook`)**, one fail-open Go handler for Cursor, Claude
  Code and an OpenCode plugin: measured unnecessary (zero redirectable commands in five
  runs), and it would put three clients' hook formats and a command-line parser in the
  core. It wins if a client or model is shown falling back to `adb` or Gradle while
  droidyard is configured; plan m1o step 1 recorded how each client's hooks behave.
- **Printing snippets for the user to paste** (ADR-0014's original form): no merge, no
  idempotence, no uninstall, and the absolute path is easy to get wrong by hand.
- **Plugins or marketplace packages per assistant**: ADR-0014's rejection stands, the
  coupling ADR-0004 avoids.
- **Writing the files from `droidyard mcp` on first start**: it would change the user's
  project unasked, from a process the client launches.
- **Allowlisting by hand-picked tools instead of `readOnlyHint`**: a second list to keep
  true; the annotations already say which tools change nothing.
