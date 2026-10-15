# Git history: clients, graph libraries and BeardGit (survey, 2026-10)

- Date: 2026-10-12
- Method: web pages only (READMEs, docs, source viewed on GitHub); nothing cloned, downloaded or run. Licences and dates marked verified come from the GitHub API, the project page or the source file read; anything not confirmed is marked **(unverified)**.
- For: milestone 2n ([plan](../plans/m2n-git-history.md)). The target layout is Android Studio's Git Log tool window.

## Clients

"AS-like" is how close the layout is to the Android Studio Log: branch tree left, filtered commit table centre, files and details right.

| Client | Look | Screenshot / page | Submodules | Licence | AS-like |
|---|---|---|---|---|---|
| IntelliJ / Android Studio Log | The target: branches pane; filter bar (text with regex and case, Branch, User, Date, Paths); coloured ref labels; graph; Changed Files above Commit Details on the right (verified, docs 2026.2) | https://resources.jetbrains.com/help/img/idea/2026.2/Git_log_tab_legend.png · https://www.jetbrains.com/help/idea/log-tab.html | Each submodule a separate VCS root; the log mixes roots with a colour stripe per commit and filters by root through Paths (stripe verified; submodules as roots from community answers) | IDE proprietary; graph engine `platform/vcs-log/graph` in intellij-community, Apache-2.0 | — |
| GitKraken Desktop | Refs panel left; graph with branch/tag, message, author, date, sha columns; commit panel right | https://www.gitkraken.com/features/commit-graph | Opt-in Submodules section in the left panel; a submodule opens as its own repo; prompts to save the new pointer (verified, help docs) | Proprietary, freemium | High |
| GitLens Commit Graph (VS Code) | Rebuilt in 19.0 (2026-08-12); movable columns, minimap, search with regex and case, inline column filters | https://help.gitkraken.com/wp-content/uploads/gl-commit-graph-01-v4@2x.png | Not in the docs | Extension MIT; graph component `@gitkraken/gitkraken-components` 11.3.0 proprietary; Pro for private repos (verified) | High (no branch tree) |
| Fork | Sidebar of branches, remotes, tags, stashes; commit list with graph; commit view | https://git-fork.com/images/carousel/carousel_mainMac.jpg | Listed as a feature (verified); placement (unverified) | Proprietary, $59.99 | High (details below, not right) |
| Sublime Merge | Location bar; list with graph; details and files right; as-you-type search over message, author, path, contents | https://www.sublimemerge.com/ | "Submodule Management" (verified); details (unverified) | Proprietary | High |
| SourceGit | Avalonia (C#): sidebar, history with graph, detail pane | https://github.com/sourcegit-scm/sourcegit/raw/develop/screenshots/theme_dark.png | Listed (verified); placement (unverified) | MIT, pushed 2026-10-12 | High; closest open-source match |
| SmartGit | Log window with Branches, Graph, Files, Details panes, close to IntelliJ (unverified) | https://www.smartgit.dev/home/header-dark.webp | Yes (detail unverified) | Proprietary; free for open source and education | High (unverified) |
| SourceTree | Sidebar of Branches, Tags, Remotes, Submodules; graph table; details below (unverified) | https://www.sourcetreeapp.com/ | Sidebar section (unverified) | Proprietary, free | Medium-high |
| Tower | Sidebar, History view, details | https://www.git-tower.com/help/guides/commit-history/overview/mac | At the bottom of the sidebar with the checked-out revision; double-click opens it (verified) | Proprietary | Medium-high |
| BeardGit | Svelte + Tauri; branch folder tree with favourites; canvas graph, dashed local-only and remote-only lanes, ref and PR badges | https://github.com/The3eard/BeardGit | Nested list with status (below) | CC BY-NC-SA 4.0 (README) | Medium-high |
| VS Code Source Control Graph | Compact swimlanes, one SVG per row; incoming/outgoing rows; a commit expands to its files | https://code.visualstudio.com/assets/docs/sourcecontrol/staging-commits/source-control-graph.png | Separate repositories in the SCM view (unverified) | MIT | Low |
| vscode-git-graph | One SVG table; branch dropdown with globs; find widget; inline details | https://github.com/mhutchie/vscode-git-graph/raw/master/resources/demo.gif | Repo dropdown lists submodules (unverified) | Custom MIT banning derivative works (verified); last push 2023-07-08 | Medium-low |
| gitk | Graph and subject, author, date on top; diff and files below | https://git-scm.com/docs/gitk | None dedicated (unverified) | GPL-2.0 | Medium |
| tig / lazygit | Terminal UIs; lazygit draws the graph in its commits panel | https://github.com/jesseduffield/lazygit | lazygit: a submodules panel with init and update (verified) | GPL-2.0 / MIT (unverified) | Low |
| Gitea / Forgejo graph | Server-rendered SVG lanes, branch filter | https://codeberg.org/forgejo/forgejo/graph (unverified live) | No | Gitea MIT (verified); Forgejo GPL-3.0+ (unverified) | Low |
| GitLab repository graph | Horizontal network graph | https://gitlab.com/gitlab-org/gitlab/-/network/master | No | MIT (CE) | Low |
| ungit | Node server and browser UI on localhost, Droidyard's architecture | https://github.com/FredrikNoren/ungit | Not mentioned | MIT, pushed 2026-09-03 | Low |
| GitHub Desktop | Linear history, no graph (unverified) | — | — | MIT | No |

The clients near the target share a left sidebar of refs (Local, Remote, Tags, often Submodules, Stashes, Worktrees), a centre table with ref labels and graph, and a detail pane. IntelliJ's own piece is the filter bar with one dropdown per field; GitLens comes nearest with search prefixes and column filters. Submodules are either a side list opened as a separate repo (GitKraken, Tower, lazygit, BeardGit) or separate roots merged into one log with a colour stripe (IntelliJ only). None draws submodule history inside the parent graph.

## Libraries and algorithm sources

| Source | Licence | Last activity | Rendering | Large histories | Coupling | Verdict |
|---|---|---|---|---|---|---|
| gitgraph.js | MIT | Archived 2024-07-13 (verified) | SVG | No | Vanilla, React | Reject: illustrative API, dead |
| Mermaid gitGraph, Schematex | MIT (unverified) | — | SVG | No | Vanilla | Reject: diagrams, not repos |
| @tomplum/react-git-log | Apache-2.0 | v3.5.1, 2026-10-08 (verified) | HTML grid | Client paging; no virtualisation stated | React | Reject as a dependency; its lane code credits pvigier |
| commit-graph (liuliu-dev, DoltHub) | Apache-2.0 | v2.3.9, 2026-09-14 (verified) | SVG, Bézier | Infinite scroll | React | Reject as a dependency; DoltHub's write-up is useful |
| @gitkraken/gitkraken-components | Proprietary | 11.3.0 | (unverified) | Yes | React | Reject: licence |
| vscode-git-graph `web/graph.ts` | MIT with derivatives banned | 2023-07-08 | SVG | "Load more" | Vanilla TS | Reject: licence; ideas only |
| VS Code `src/vs/workbench/contrib/scm/browser/scmHistory.ts` | MIT | Active | One `<svg>` per row (22 px, 11 px lane step) | Recomputes; pages through `scm.graph.pageSize` | None | Best portable reference |
| Gitea `services/repository/gitgraph/{graph.go,parser.go}` | MIT | Active | Server SVG | Pages by commit count | Go | Parses `git log --graph` ASCII: git lays out, Gitea colours |
| lazygit `pkg/gui/presentation/graph/graph.go` | MIT (unverified) | Active | Text | Eager, rendered in parallel | Go | Clean Go lane algorithm |
| IntelliJ `platform/vcs-log/graph` | Apache-2.0 | Active | Swing | Collapses long edges | Java | Reference for collapsing |
| mlange-42/git-graph | MIT | 2026-08-06 (verified) | Terminal | — | Rust | Branching-model layout; niche |
| gitamine (pvigier) | GPL-3.0 | 2019-05-10 | — | Virtualised list, interval tree | TS | Reference only |

No Svelte commit-graph library was found. The lane algorithms reused: pvigier, ["Commit graph drawing algorithms"](https://pvigier.github.io/2019/05/06/commit-graph-drawing-algorithms.html) (2019; temporal topological sort, straight branches, forbidden columns); VS Code's swimlanes (a row's input lanes are the previous row's output; the first parent inherits the lane); Gitea's parse of git's own graph; IntelliJ's collapsing graph. All are one newest-first pass, O(N × lanes), cheap in Go; the browser then needs only row windows.

## BeardGit

- **What:** a desktop client by one person; Tauri 2, Rust backend in 22 crates, Svelte + CodeMirror 6 frontend; macOS, Linux, Windows; CC BY-NC-SA 4.0, so ideas only. Created 2026-03-27, last push 2026-09-21.
- **History:** reads through libgit2 (`git2`; verified in `crates/git-engine/src/commits.rs`), writes through the `git` binary. Walks every direct ref, `TOPOLOGICAL | TIME`. Filters are in-process linear scans (author, summary, SHA prefix). Offset paging; an anchored mode for first-parent history only.
- **Layout** (`crates/graph-builder/src/{dag.rs,layout.rs,viewport.rs}`): one newest-first pass; the first parent inherits the lane; lanes waiting for the same commit close where they meet; a lane cap (default 8, 4–16) reclaims a stale lane and marks it with an arrow. Output: lane segments (with local-only / remote-only / synced state) and merge curves. Close to VS Code's swimlanes; cites no source.
- **Serving** (`crates/app-core/src/commands/{graph.rs,graph_cache.rs}`): first layout up to 20,000 commits, older pages on demand; cached on disk under a key of path, HEAD and refs; an incremental prepend when one ref moved along a chain (≤ 512 commits), else a full rebuild; `viewport(offset, limit)` returns only what crosses the window.
- **Drawing** (`src/lib/components/graph/{GitGraph.svelte,graph-renderer.ts}`): one Canvas 2D surface for lanes, curves, nodes, badges and text; 28 px rows, 22 px lanes; a hidden `<ol>` mirror for tests and accessibility; manual hit-testing; colours from CSS tokens.
- **Branches and submodules:** `branch-tree.ts` splits names on `/` into folders, favourites first; `submodule.rs` lists submodules depth-first (≤ 5) with status Uninitialized / Clean / Outdated / Dirty; a separate panel, not in the graph.
- **Caveats:** canvas text loses native selection and needs the mirror for accessibility; the lane cap trades correctness for width; filters cover no regex, date or paths.
