# ADR-0061: An agent's `rm` is redirected to the trash by an optional assistant hook

- Status: Accepted
- Date: 2026-10-12
- Supersedes: ADR-0037 §Decision (in part: item 2, `init` also writes a hook entry for `claude` and `cursor`)
- Supersedes: ADR-0037 §Consequences (in part: assistant-side hooks "not adopted")
- Supersedes: ADR-0019 §Processes (in part: item 3, the subcommand set gains `hook`; item 4, `hook` never auto-starts)
- Supersedes: ADR-0024 §Decision (in part: the subcommands row gains `hook`)
- Relates to: ADR-0004, ADR-0015 §6, ADR-0054, ADR-0060; plan m1o step 1 (a); plan m2j Q2
- Decided by the owner, 2026-10-12 ("analiza cómo es con cursor para meterlo también"): an agent's deletes reach droidyard's trash with no MCP tool. The shape is Delegated.

## Context
 After ADR-0060, an agent deletes with its own tools. Claude Code has no delete tool, so a delete is a Bash `rm`. Cursor deletes through its shell or its `Delete` tool. Each client runs a project hook before a shell command, and a deny's text reaches the model ([obs] Cursor, m1o step 1 (a); Claude Code, step 6 (a)). ADR-0037 rejected a hook that redirects shell commands to tools for two reasons. No agent ran a redirectable command. And it would put three clients' formats and a command-line parser in the core. A delete is a different need: the owner asks for it, and no measurement says how often agents `rm`. The parser recognises one command word with three flags, and it lives in `internal/assistant` beside `init`, outside the core and the adapters.

## Decision

1. `droidyard hook --client claude|cursor --root <abs>` reads one hook event on stdin and answers within 2 s. It never auto-starts the daemon. It is not a security boundary: `/bin/rm`, `sh -c` or `find -delete` pass.
2. It routes only a simple `rm` whose every target would be deleted by `rm` itself and lies inside `--root`:
   - the command word exactly `rm`;
   - flags among `-r`, `-R`, `-f`, and `--`;
   - operands as plain or quoted words, or `*` and `?` globs expanded as `sh` does (no dot-names unless asked for);
   - relative operands only where the event's `cwd` follows the shell's `cd`: Claude Code's reference says `cwd` is "the new directory after Claude runs `cd`" (not yet run, m2j step 11); Cursor's `beforeShellExecution` carries `cwd: ""` while its shell keeps a `cd` ([obs] m2j step 6), so for Cursor only absolute operands route.
   - Cursor's own `Delete` tool (`preToolUse`, matcher `Delete`) carries `tool_input.file_path`, absolute ([obs] m2j step 6); it routes by the same rules.

   Anything else passes untouched:
   - any shell operator or expansion, another flag, a prefix (`sudo`, `command`, a path);
   - a target outside the root, matching nothing, `.`, ending in `..`, or the root itself;
   - a directory without `-r`;
   - a target that is or lies under `build`, `.gradle`, `.kotlin` or `.cxx`. These are regenerable outputs, and trashing them would evict the user's older items under ADR-0054 §3's 2 GiB cap.
3. For a routed `rm`, if a daemon answers on the socket, the hook opens the workspace and denies the command with one message: run `<abs droidyard> call --no-autostart --workspace '<root>' file.delete --params '{"paths":[…]}'` instead, which moves them to droidyard's trash, where the user can restore them. `DROIDYARD_STATE_DIR` is inline when the hook's root came from it, so the replacement reaches the daemon the hook dialled and never starts another.
   - **The hook deletes nothing.** The agent's replacement goes through the client's own approval like any shell command, and the agent reads each item's outcome in its result.
   - The hook's `workspace.open` happens before approval. It runs no project code (ADR-0015 §5).
   - With no daemon, an error, a timeout or malformed input, the command passes (fail open).
4. Pass-through:
   - Claude Code: exit 0 with no output.
   - Cursor: `{}`. [obs] m2j step 6, cursor-agent 2026.10.01: `{}`, an empty output and exit 1 each let an allowed command run; `{"permission":"allow"}` did not run a command the project's `cli.json` denies (Cursor's own rules still apply).
   - A usage error exits 1 and a panic is recovered. The hook never exits 2, which Claude Code reads as a deny.
5. `droidyard init --client claude|cursor` writes the entry into the project's `.claude/settings.json` or `.cursor/hooks.json` only, never a user-level file, with ADR-0037's merge, keep and uninstall rules. It is on by default with the client's other entries.

## Consequences

- In `cursor-agent -p` without `--force`, the `Delete` tool is refused by Cursor itself whatever the hook answers ([obs] m2j step 6); the hook's `Delete` branch matters in interactive Cursor.

- A routed `rm` costs the agent one more turn and one approval for the replacement. A complex form, `/bin/rm` or a build output bypasses the trash. ADR-0054's caps apply to what is routed.
- The replacement must name the binary and the state root, so it reaches the daemon the hook dialled. The in-hook delete would have needed neither.
- A client hook's environment lacks `DROIDYARD_STATE_DIR` [obs, m1o]: a custom state root needs it written into the command (register).
- Two clients' hook formats live in `internal/assistant`. A client that changes its format loses the redirect silently, never the agent's work (fail open).
- Documents: `mcp.md` §Features beyond tools; the register.

## Alternatives considered

- **The hook trashes the files itself, then denies** (the coordinator's sketch): one turn fewer. But `PreToolUse` runs before the permission prompt (Claude Code's reference: exit 0 "continues through the normal permission flow"), so an `rm` the user would have refused still happens (to the trash), in any permission mode. The hook must also own a partial failure and a long-running delete inside a hook's timeout. Wins if the owner prefers the turn saved and accepts the approval skipped.
- **Rewriting the command** (Claude Code's `updatedInput`, Cursor `preToolUse`'s `updated_input`): a reported Claude Code bug ignores it in some versions, and `beforeShellExecution` cannot modify. Wins when both clients honour it.
- **A `file_delete` MCP tool only**: the token cost ADR-0060 removes, and agents reach for `rm` anyway.
- **A shell function or a `rm` shim on `PATH`**: it alters the user's shell for every program, invisibly.
- **User-level hook files**: they act in every project, and the house rule forbids them.
- **An OpenCode plugin**: not asked; a register row.
