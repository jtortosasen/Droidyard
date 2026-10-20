# The CLI, with or without assistant hooks, against MCP as the agents' surface

2026-10-12. Repo at `f46d528` on `m2j` (step 9 work uncommitted). An analysis for the owner,
checked by the Opus verifier against the code, the ADRs and primary web sources. Labels:
**[obs]** read in code or a primary source, or run; **[inf]** inferred; **[unv]** not verified.
Nothing here is a decision; any change it suggests needs an ADR.

## 1. Corrections made during the analysis

The first pass got five things wrong; the comparison in §2 is the corrected one.

- **Tool definitions are not a fixed cost in Claude Code or Cursor.** Claude Code defers every
  MCP tool by default and loads only names and server instructions at start; the ~10 % threshold
  applies only to the opt-in `auto` modes [obs, code.claude.com/docs/en/mcp §Configure tool search].
  Cursor syncs descriptions to a folder and shows the agent only the names [obs,
  cursor.com/blog/dynamic-context-discovery, 2026-01-06]. The ~7k tokens of `tools/list`
  (28,032 B, 31 tools, `cmd/droidyard/toollist_bytes_test.go`) are paid in full only by clients
  that load the list eagerly, as OpenCode 2.0.18 did (`daily-use-tokens-2026-10.md` §1.2).
  Codex tool search: third-party sources only [unv].
- **The CLI does mark untrusted data**: `"meta":{"untrusted":true}` in the envelope
  (`internal/capability/envelope.go:60-72`, `internal/daemon/native.go`). It lacks MCP's
  plain-language first line (`adapters/mcp/mcp.go:842`).
- **Capability errors go to stdout** in the JSON envelope, exit 1 (`adapters/cli/cli.go` `emit`);
  stderr carries only dial, usage and interrupt messages (`cmd/droidyard/client.go:118-134`).
- **The CLI has a bounded wait**: `--detach` prints the operation id, then `operation.wait` with
  `timeoutMs`. Without `--detach`, a call killed by the Bash timeout (Claude Code: 120 s default,
  600 s max, `BASH_DEFAULT_TIMEOUT_MS` / `BASH_MAX_TIMEOUT_MS`) never printed its id, and there is
  no `operation.list`; on SIGINT the CLI cancels the operation. The signal Claude Code sends [unv].
- **MCP streams nothing to agents.** The watch and event capabilities are not exposed
  (`docs/interfaces/mcp.md:57`); they are WebSocket-only. MCP 2026-07-28 still returns one result
  per tool call; progress carries a counter and a message; `subscriptions/listen` carries change
  notices only [obs, modelcontextprotocol.io changelog]. The CLI cannot stream either today (no
  session: `SESSION_REQUIRED`), but a long-lived CLI process read in the background could, with
  new code.

## 2. Comparison

| | MCP | CLI, no hooks | CLI + hooks |
| --- | --- | --- | --- |
| Fixed context | ~0 in Claude Code and Cursor; ~7k tokens in eager clients | Skill description (≤ 1,536 chars), the skill on first use | same |
| Per call | Compact renderings (uiTree −67–75 %) | Raw JSON; a text flag is small (`d.Text` is in the core descriptor) | same |
| Screenshots | Inline image | Ref with no path (ephemeral artifacts are in memory); `artifact.read` returns base64 to pipe and decode | same |
| Policy boundary (ADR-0015 §6) | Core refuses `AudienceHuman` to `ClientMCP` (`operation.go:862`) | `policy.update`, `trust` reachable; client kind is self-declared, no TTY check; only the host's Bash approval remains | A deny hook is bypassable (`call --workspace X policy.update`) |
| Approvals | Exact per tool, plus annotations | Bash prefix rules: `Bash(droidyard call *)` approves `policy.update`; an allow rule does not match past a leading `VAR=…` | same |
| Attribution | Agent and session in Activity | `client=cli`, shown as "You" | same |
| Long work | Bounded wait, `operation_wait` | `--detach` + `operation.wait` | same |
| Streaming | none | none today; possible with a CLI session | same |
| Composability | none | pipes, `jq`, redirect to a file | same |
| Clients | those with MCP | any with a shell, scripts, CI | Claude Code, Cursor, Codex (official hooks); OpenCode by plugin |
| The agent's own commands | not seen | not seen | `rm` redirected (ADR-0061); `SessionStart` context; `Stop` can block |
| Parameters | structured JSON | `--params '<json>'` in shell quoting | same |

Hooks are orthogonal to the transport: they run in the client and combine with MCP too
(a Claude Code `PreToolUse` matcher also takes `mcp__droidyard__.*`). A deny's reason reaches
the model in Claude Code [obs, hooks reference] and Cursor [obs, m1o step 1 (a)]; in Codex [unv].

**Reading.** MCP stays the better primary surface for agents: in the clients that defer tools its
cost is small, and it keeps the policy boundary, attribution, images, exact approvals and the
renderings. The CLI's gains are reach, composability and possible streaming. If the CLI were ever
offered to agents, it would first need a text format reusing the renderings, a distinguishable
(self-declared) agent client kind for attribution, and `--detach` by default for builds.

## 3. The file tools (ADR-0060)

ADR-0060 removed them for tokens; with §1's first point, that saving is real only in eager
clients. The stronger reason is redundancy: no data shows an agent choosing `file_read` with its
native Read available (the five `file_read` calls in `daily-use-tokens-2026-10.md` §1.3 were not
measured against native tools). What was given up: containment, attribution, `ifRevision` on the
agent's side, and trash for deletes and moves. Claude Code's checkpoints already undo its
Write/Edit changes but not Bash's `rm`, `mv`, `cp` [obs, code.claude.com/docs/en/checkpointing],
which is the gap ADR-0061's trash covers.

ADR-0061 covers it in part. These pass untouched: compound forms (`&&`, `find -delete`, `xargs rm`,
`sh -c`, `/bin/rm`, `git rm`), build outputs (on purpose), Cursor's relative operands, no daemon or
no hook (OpenCode), and losses that are not `rm`: `mv`/`cp` over an existing file, `> file`,
scripts.

## 4. Other hooks considered

None is justified today.

1. **`./gradlew` → `build.*`**: live console, structured errors, no unseen concurrent build.
   ADR-0037 rejected it on evidence (zero shell commands in five Cursor runs with droidyard
   configured) and names the condition that would change that. With MCP present the agent has
   no observed reason to fall back; possible reasons are hypotheses [inf]: deferred tools and a
   strong `./gradlew` prior, a droidyard failure, an uncovered task, a project `AGENTS.md`, a
   weaker model.
2. **Destructive `adb`** (`uninstall`, `pm clear`, `emulator -wipe-data`): raw `adb` bypasses the
   device policy. A guard, not a boundary.
3. **Destructive `git`** (`reset --hard`, `restore .`, `clean -fd`): real loss, but not Android;
   out of scope.
4. **`mv`/`cp` over an existing file**: same gap as `rm`; rare; no data.

## 5. Observability today

- `droidyard hook` sees every Claude Code Bash command and every Cursor `beforeShellExecution`,
  and records nothing for a pass-through.
- `daemon.log` records each operation, `op … client=… capability=… state=…`
  (`internal/capability/operation.go:549`): droidyard's own failures are visible. On this machine
  it held only the start lines on 2026-10-12.
- Claude Code transcripts hold every Bash command and MCP call. Checked on the m2j step 11
  transcript:

  ```sh
  jq -r 'select(.type=="assistant") | .message.content[]? | select(.type=="tool_use")
         | if .name=="Bash" then "Bash: "+.input.command else .name end' \
     ~/.claude/projects/<project>/*.jsonl | grep -E 'gradlew|adb|emulator|mcp__droidyard'
  ```

  No transcript of real Android work with droidyard exists on this machine yet. Cursor
  (`~/.cursor/chats`) and OpenCode (`opencode.db`) stores were not examined.
- Suggested order: real use plus this recipe first; only if it falls short, an opt-in log line
  from the hook for `gradlew|adb|emulator|sdkmanager` (an ADR, since it widens ADR-0061).

## 6. Findings for open work

- m2j step 11: ADR-0061's replacement `DROIDYARD_STATE_DIR=… /abs/droidyard call …` is not
  approved by an allow rule, which does not match past a non-safe variable assignment [obs,
  code.claude.com/docs/en/permissions]; a deny rule does. Only a custom state root is affected.
- ADR-0061 hands agents a human-audience capability over the CLI, logged `client=cli` and shown
  as "You" in Activity (plan m2j, register).
- MCP 2026-07-28 removes the `initialize` handshake; whether `droidyard mcp` handles that
  revision is not recorded anywhere [unv].

## 7. Not verified

OpenCode's current MCP loading and plugin hooks; Codex tool search; whether clients show MCP
progress to the model; the signal of Claude Code's Bash timeout; whether Cursor's deny reason
reaches the model outside `-p`, and Codex's at all.
