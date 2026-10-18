# ADR-0062: An agent's `rm` is handed `droidyard trash`, which resolves operands in the agent's shell

- Status: Accepted
- Superseded in part by: ADR-0085 (§Decision)
- Superseded in part by: ADR-0064 (§Decision)
- Date: 2026-10-12
- Supersedes: ADR-0061 §Decision (in part: §2's clause on Cursor's relative operands; §3's replacement command)
- Supersedes: ADR-0061 §Consequences (in part: the replacement naming the state root as an inline variable)
- Supersedes: ADR-0019 §Processes (in part: item 3, the subcommand set gains `trash`; item 4, `trash` never auto-starts)
- Supersedes: ADR-0024 §Decision (in part: the subcommands row gains `trash`)
- Relates to: ADR-0015 §5, ADR-0054, ADR-0060; `docs/research/cli-vs-mcp-hooks-2026-10.md` §6; plan m2k Q1
- Decided by the owner, 2026-10-12: Cursor supported as well as Claude Code. The shape is Delegated.

## Context

Cursor's `beforeShellExecution` carries `cwd: ""` while its shell keeps a `cd` ([obs] m2j step 6), so ADR-0061 routes only Cursor's absolute operands, and agents mostly type relative ones. ADR-0061's replacement takes root-relative paths in JSON, which only the hook can compute, and its inline `DROIDYARD_STATE_DIR=…` stops a Claude Code allow rule from matching (research note §6). A command that runs in the agent's own shell resolves relative paths and globs where the agent typed them.

What reaches the model from Cursor's deny ([obs] plan m2k step 1 c1, cursor-agent 2026.10.01, re-checking m1o): `user_message` does, prefixed "Command execution was blocked by a hook:" and followed by Cursor's "Agent note: Do not suggest workarounds to the blocked tool."; `agent_message` does not.

## Decision

1. `droidyard trash [--state-dir <dir>] [--root <abs>] [-r|-R|-f, combined as rm takes them] [--] <path>…` resolves operands against its own working directory.
   - The root is `--root`, else the project root of its working directory (ADR-0019 §Sessions).
   - The state root is `--state-dir`, else the environment's (ADR-0021).
   - It never auto-starts a daemon.
2. It checks every operand before moving any, by ADR-0061 §2's target rules: inside the root; existing unless `-f`; a directory only with `-r`; not the root, `.` or `..`; not at or under `build`, `.gradle`, `.kotlin` or `.cxx`; a link that does not leave the root. On any refusal, or with no daemon, it moves nothing, exits 1, and prints the reasons and `command rm <flags> -- <operands>`, which the hook passes (a prefix is not routed).
3. Otherwise it opens the workspace and calls `file.delete` as `client=cli`, prints one line per item, and exits 0 only when every item is trashed.
4. For a routed `rm`, and for Cursor's `Delete`, the hook's message names `<abs droidyard> trash [--state-dir '<dir>'] --root '<root>' [-r] [-f] -- <operands>`.
   - Each operand is re-rendered from the typed word: a `*` or `?` that was unquoted there stays bare, so the shell expands it again as typed; every other character is single-quoted.
   - `--state-dir` appears only when the hook's own state root came from `DROIDYARD_STATE_DIR`.
   - The prefix `<abs droidyard> trash` can take an allow rule.
5. With `cwd: ""`, a relative operand routes on lexical checks only: no `..` component, no build-output component, not `.`. `trash` checks the rest. Where the hook can place a target (an absolute `cwd` or operand), ADR-0061's checks stay.
6. Cursor's deny carries the text in both `user_message` (the field that reaches the model) and `agent_message`.

## Consequences

- When `trash` refuses, the agent spends a turn. Cursor's "Do not suggest workarounds" note may keep its agent from running the replacement; plan m2k step 4 runs it.
- `trash` is usable by hand. Its deletes show as "You" in Activity (register).
- `init` writes no allow rule for it (ADR-0037 §3).
- The rules live in `cmd/droidyard/trash.go` and `internal/assistant/hook.go`; documents: `mcp.md` §Features beyond tools, the register.

## Alternatives considered

- **Keep `call file.delete`, with `"$PWD"` spliced into its JSON**: fragile to quote and to approve. Wins if Cursor's event ever carries the real cwd.
- **Ask the agent for `pwd` first**: a turn on every delete.
- **Rewrite the command**: `beforeShellExecution` cannot (ADR-0061).
- **Root-relative paths in the `trash` command**: they would resolve against the shell's cwd, not the root.
- **`agent_message` only**: it does not reach the model in `cursor-agent` ([obs] above).
