# ADR-0124: `init` once per user, a session with no tools outside a project, and init's files kept out of git

- Status: Accepted
- Date: 2026-10-23
- Supersedes: ADR-0037 §Decision (in part: 2, `init` may also run at user level); ADR-0038 §Decision (in part: user level, rejected on 2026-10-09, is taken); ADR-0061 §Decision (in part: §1, the hook may run without `--root`; §5, user-level files); ADR-0019 §Processes (in part: 2 and 4, a shim with no project serves an empty session and starts nothing) and §Sessions (in part: no cwd fallback for `mcp` without `--root`); ADR-0075 §Decision (in part: §6, the MCP binding without `--root`); ADR-0085 §Decision (in part: §1–2, the root without `--root`)
- Relates to: ADR-0004; ADR-0015 §5; ADR-0082 §3, §6; ADR-0090 §1 (extended); ADR-0095 §1, §3, §5; [plan m3a](../plans/m3a-init-once-per-user.md) Q1–Q4 and step 1
- Decided by the owner, 2026-10-22: install the assistant integration once, at user level, so every project and worktree has it without `init` per project; per-project `init` gains `--exclude` and `--gitignore`. The details are Delegated (plan m3a Q1–Q4, reviewed in two rounds; step 1's probes).

## Context

`init` writes per project, each entry naming the root (`mcp --root`, `hook --root`). A
worktree carries only tracked files, so a worktree made by `git worktree add` or `claude -w`
has no entries, and a copy of the main tree's would bind the main tree. The owner also saw
init's files in `git status`. ADR-0038 rejected user level on 2026-10-09 until there were "an
empty-session mode outside a project and per-client evidence to stay cheap"; ADR-0061 kept
hooks out of user files because "they act in every project". The owner reversed both.

Step 1 of plan m3a, run 2026-10-23 with isolated `HOME` and config directories (claude
2.1.308, cursor-agent 2026.10.18; evidence `/private/tmp/dy3a.YtnjOv`):

- [obs] `claude mcp add-json --scope user` writes `$CLAUDE_CONFIG_DIR/.claude.json` (else
  `$HOME/.claude.json`), storing the entry as given; `claude mcp list` from a worktree
  started the shim there, which bound the worktree. `mcp list` and `mcp get` start every user
  server as a health check.
- [obs] Claude passes `CLAUDE_PROJECT_DIR` (the launch directory) to stdio servers, whose
  cwd is the launch directory. cursor-agent passed `${workspaceFolder}` literally and no
  `CURSOR_*` variable; its server's cwd was the launch directory.
- [obs] A server that declares tools and lists none shows "✔ Connected" in Claude and
  "ready" in cursor-agent, with no warning.
- [obs] cursor-agent left a user `cli-config.json` without `deny` as it was in every
  unauthenticated run; a file from 2026-10-22's research had been expanded to its defaults
  by some run [inferred: live state].
- Not run: neither client authenticated in isolation, so whether Claude's user and project
  hooks both fire, and what Cursor's user hooks receive, rest on the clients' documents.

## Decision

1. **`init --user`** (`--client cursor|claude|opencode|all`, with `--dry-run` and
   `--uninstall`) writes at user level, and no entry names a root:

   | Client | MCP entry | Hooks | Allowlist | Skill |
   | --- | --- | --- | --- | --- |
   | claude | `<bin> mcp`, through `claude mcp add-json --scope user` | `<claude>/settings.json` | `<claude>/settings.json` | `<claude>/skills/` |
   | cursor | `~/.cursor/mcp.json`: `<bin> mcp --from ${workspaceFolder}` | `~/.cursor/hooks.json` | `~/.cursor/cli-config.json` | `~/.agents/skills/` |
   | opencode | `<xdg>/opencode/opencode.json`: `<bin> mcp` | none | none | `~/.agents/skills/` |

   `<claude>` is `CLAUDE_CONFIG_DIR`, else `~/.claude`. Claude's entry goes through Claude's
   CLI, never by editing `.claude.json`. Every other file follows ADR-0037's merge, keep,
   refuse and uninstall rules; in a user file the rootless hook shape is init's (ADR-0090
   §1). Writing `cli-config.json`, `init` says to close cursor-agent first. A project's own
   entries take precedence there; `init --user` run in a project reports them.
2. **`droidyard mcp` without `--root`** binds the project of `--from`, else of its cwd
   (`projectOf`). A `--from` that is empty or still holds `${` is ignored. With no project,
   the shim serves a session itself: the tools capability, an empty list; it dials nothing
   and starts no daemon. `--root` keeps its meaning; `call` and `trash` keep the cwd
   fallback (ADR-0075 §6 distinguishes them).
3. **`droidyard hook` without `--root`** takes the project of the directory the client names
   as its project: `CLAUDE_PROJECT_DIR`; Cursor's `workspace_roots[0]`, else
   `CURSOR_PROJECT_DIR`; else the event's `cwd`. ADR-0085 and ADR-0095 §5 then apply with it
   as the root. With no project, every event passes with nothing dialled. Where that
   directory holds init's own per-project hook for the client, the user hook passes, so a
   project set up both ways records once.
4. **`--exclude` and `--gitignore`**, per project only: the run's untracked paths become one
   marked block, in the common git directory's `info/exclude` (read by every worktree,
   anchored at the top) or in the root's `.gitignore` (anchored at the root). Tracked paths
   are reported, never listed. A rerun replaces the block; an uninstall removes it and
   deletes a file it leaves empty. Git is read only through `internal/domain/git`'s runner,
   untrusted.

## Consequences

- A chat started outside an Android project, or in a subfolder that is no project, has no
  droidyard for its life (one session, one project); `mcp` without `--root` no longer binds
  a non-project folder.
- Every chat on the machine starts the shim; outside a project it costs a process and no
  daemon. Claude's `mcp list` health check starts it too.
- The user `settings.json` carries `PostToolUseFailure`: an older Claude Code that discards a
  file with an unknown event would lose the user's settings, not one project's (register).
- Unverified until an authenticated isolated run: Claude's user and project hooks firing
  together; Cursor's user hooks and what they receive; whether an authenticated cursor-agent
  rewrites `cli-config.json`. The Cursor IDE's server cwd and expansion are untested.
- `docs/interfaces/mcp.md` §Transport and session and the register follow.

## Alternatives considered

- **Copying the per-project files into worktrees** (`.worktreeinclude`, a `post-checkout`
  hook): each copy names the main tree's root, and `.worktreeinclude` covers only worktrees
  Claude creates.
- **Editing `.claude.json` directly**: it is Claude's live state, rewritten by every running
  session, and over a megabyte is ordinary.
- **`--global`**: Cursor's word, not Claude's or OpenCode's.
- **`mcp` exiting outside a project**: every other chat would show a failed server.
- **Attaching unbound, or auto-starting unbound**: the tool list would depend on whether a
  daemon runs, every chat would pay the tools' tokens, or a daemon would stay resident for
  every chat on the machine.
- **`--root ${workspaceFolder}` for Cursor**: once a client expands it, every Cursor
  window's folder, Android or not, would be bound and opened.
- **The hook's root from the event's `cwd` alone**: an agent that `cd`s into another project
  would open it (ADR-0085 §Alternatives considered).
- **Ignore lines without a block, or `init` running `git` itself**: an uninstall could not
  tell its lines from the user's, and a second git invocation would lack the runner's
  hardening against a repository's configured commands.
