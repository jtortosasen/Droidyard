# ADR-0063: The hook observes an agent's Android commands: a hint, and a record in Activity

- Status: Accepted
- Superseded in part by: ADR-0085 (§3)
- Superseded in part by: ADR-0065 (§Decision)
- Date: 2026-10-12
- Supersedes: ADR-0061 §Decision (in part: §5, `init` also writes the after-command entries)
- Relates to: ADR-0004, ADR-0015 §1, §6, ADR-0037 (the redirect stays rejected), ADR-0043 §2, ADR-0048; `docs/research/cli-vs-mcp-hooks-2026-10.md` §4, §5; plan m2k Q2
- Decided by the owner, 2026-10-12 ("lo que veas más indicado. Procede"): monitor rather than intercept. The shape is Delegated.

## Context

Intercepting `./gradlew` or `adb` would block what droidyard does not cover (ADR-0037; the owner). Nothing shows the owner when an agent goes around droidyard, and nothing tells the agent the equivalent tool.

The clients' after-command events (plan m2k step 1):
- Claude Code: `PostToolUse` (matcher `Bash`) after a command that exits 0, and `PostToolUseFailure` after one that exits non-zero, with `error` starting `Exit code N`. Both carry `tool_input.command` and take `hookSpecificOutput.additionalContext`, which reaches the model beside the tool result. A permission denial fires neither. [doc, code.claude.com/docs/en/hooks; run at plan m2k step 9]
- Cursor ([obs] cursor-agent 2026.10.01): `postToolUse` (matcher `Shell`) carries `tool_input.command` and `tool_output` `{"output","exitCode"}`; a non-zero exit fires `postToolUseFailure` with `failure_type: "error"`; a hook's deny fires `postToolUseFailure` with `failure_type: "permission_denied"`. `additional_context` from either reaches the model. A command refused by the project's `cli.json` fires `postToolUse` with `exitCode: 0` and empty output, the same as a run that printed nothing. Each event also carries the user's email.

## Decision

1. `droidyard hook` reads the command after a shell command: Claude Code `PostToolUse` and `PostToolUseFailure` (`Bash`); Cursor `postToolUse` and `postToolUseFailure` (`Shell`), skipping `failure_type: "permission_denied"`.
   - It finds simple commands whose program's base name is `gradlew`, `gradle`, `adb`, `emulator`, `sdkmanager` or `avdmanager`, at most four per command line.
   - It splits at unquoted `;`, `&&`, `||`, `|`, `&` and newlines, and skips assignments and `command`, `exec`, `env`, `time` and `nohup`.
   - The reading is lexical, not a boundary: `sh -c`, scripts and aliases are not seen.
2. It never denies, rewrites, or delays past ADR-0061's 2 s. For an observed command it adds a hint naming the droidyard MCP tool that does the same: a table in code, tested against `tools/list`. The hint is on with the entry.
3. If a daemon answers (never auto-started), it opens the workspace and calls `agent.recordCommand` within the same 2 s. Otherwise nothing is recorded, and the hint still goes.
4. The record holds the client and, per command: the program; at most three positional words matching `^[A-Za-z0-9][A-Za-z0-9:._-]{0,63}$`, and for `adb shell` only the shell's command word; the suggested tool. Flags, flag values, assignments, output, the email and the command line are never read into it. A positional word can still carry text the agent typed; the pattern and the cap bound it.
5. `agent.recordCommand`, in a new domain `agent`:
   - kind command, because its effect is the record it leaves; scope workspace; stateless;
   - audience human: it is the hook's channel, never an MCP tool;
   - destructive false (it changes nothing in the project or on a device), not idempotent, not open-world;
   - result: the record; invalid params `REQUEST_INVALID_PARAMS`;
   - not untrusted: the words are the agent's own, no device or project text, within the pattern;
   - API `0.22.0`.
6. Activity reads a record's result once (`operation.wait`, `timeoutMs` 0) and shows "<client> ran `<program words>` outside droidyard" with the tool, attributed to the client, under Agents, not You.
7. `init --client claude|cursor` writes the after-command entries with the same command line, under ADR-0037 §2's rules.

## Consequences

- Each shell command starts one more short process after it.
- Activity shows records only while a tab is open; `daemon.log` names the capability without the words.
- A command in another project is recorded under the hook's root. A Cursor command refused by `cli.json` is recorded as run (register).
- The records show the owner, live, when an agent goes around droidyard; they are not a durable measurement.
- The rules live in `internal/assistant/observe.go`, `internal/domain/agent` and `web/src/lib/ops`; documents: `mcp.md`, `mvp.md`, the register.

## Alternatives considered

- **Deny and redirect**: blocks what droidyard lacks (ADR-0037; the owner).
- **Record every command, or whole lines**: secrets in flags and values.
- **Record before the command runs**: it records commands the user then refuses.
- **Cursor's `afterShellExecution`**: carries the command and output but no documented output field, so no hint.
- **A log file under the state root**: invisible in the UI, and a new state file.
- **A `hook` client kind in the hello**: a protocol change that rewrites every actor rule. Wins with the agent-sessions milestone.
- **Parameters on `operation.state`**: ADR-0043 §2 rejected them.
