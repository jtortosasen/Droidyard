# Research: Orca (Stably AI)

- Repo: <https://github.com/stablyai/orca>
- License: MIT
- Language: TypeScript; Electron (electron-vite), React, Radix and shadcn, Tailwind, Monaco,
  xterm, `@tanstack/react-virtual`, dnd-kit; `node-pty` for terminals
- Analyzed: 2026-10-10 (shallow clone at `2eb6a92`, about 89k stars, created 2026-03)
- Local clone: `.refs/orca`
- Kept at the owner's request (2026-10-10): "we will copy things from it in the near
  future".

## What it is

An "agent development environment": a desktop app that runs many CLI agents (Claude
Code, Codex, Cursor CLI, OpenCode and others) in its own terminals, each in its own git
worktree, with a mobile companion, GitHub and Linear panels, an embedded browser and diff
review. It is a layer above Droidyard: an agent in an Orca terminal could use Droidyard
over MCP unchanged. It hosts terminals; it has no agent loop of its own.

## What the owner liked, and where it is

- **Projects and sessions in a side panel**: one row per agent session with status, branch
  and its subagents with title and age; selecting a session switches the worktree and so
  the file tree. Docs: `docs/site/content/docs/model/agents-sessions.mdx`, `activity.mdx`
  (the agents feed: running first, unread, ⌘F filter, jump to the pane).
- **Terminal and chat in the centre**, tabs and split panes:
  `docs/site/content/docs/model/tabs-panes-splits.mdx`; terminals on `node-pty` in the main
  process, xterm in the renderer, scrollback kept across restarts.
- **File explorer**: `src/renderer/src/components/right-sidebar/FileExplorer*.tsx`
  (virtual rows, name filter, natural order, live external changes, drops from Finder,
  copy path and relative path); docs `docs/site/content/docs/editing/file-explorer.mdx`.
- **Design Mode** (an element of a web page sent to the agent with its HTML, styles and a
  crop): `docs/site/content/docs/browser/design-mode.mdx`.
- **Quick open** over files, commands and agents: `docs/site/content/docs/model/quick-open.mdx`.
- Also present: diff review with comments back to the agent, session history read from the
  agents' own files ("AI vault", `right-sidebar/ai-vault-*`), usage tracking, worktree
  checkpoints, SSH worktrees.

## Where it lands in Droidyard

Milestone 2d took the agent rows (from operations), the feed's ordering, "Copy for the
agent" and the palette ([plan m2d](../plans/m2d-redesign-file-tree.md)). The terminal,
the chats, the subagents and a worktree per session are the agent sessions milestone;
diffs are the Git milestone; Monaco is a candidate for the editor milestone
(`docs/roadmap/mvp.md` §Phase 2).
