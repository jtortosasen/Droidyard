# Plan: Milestone 2d — a redesign and the file tree

- Status: Approved
- Approved: 2026-10-10, Delegated (owner: "con lo que tienes para 2d, ponte a hacer un plan e implementalo"). The split, Q1–Q4 and D22–D30 applied as recommended; the owner can revert any Delegated choice.
- Date: 2026-10-10
- Milestone: "the next is 2d, a redesign and the file tree" (`docs/README.md` §Where the work stands), as the owner scoped it on 2026-10-10 (below).
- Phase: `docs/roadmap/mvp.md` §Phase 2 (in part; see Scope). Phase 2's acceptance is met (2b); 2d adds none.
- Contract: ADR-0045 §1, §2, §6; ADR-0043 §1, §2, §3; ADR-0041 §4; ADR-0020 (§Retention); ADR-0039 §Consequences; ADR-0040; ADR-0015 §1, §5, §6; ADR-0046; ADR-0010; ADR-0016; ADR-0036; `files.md` §The watch; `http-websocket.md` §Security baseline (rule 7, the CSP); `capability-api.md` §Subscriptions; m1j D7, D8; m2a D2, D3, D5–D7, m2b D11–D16, m2c D17–D21 in force.

**The owner's decisions (2026-10-10, translated; the main session's brief):** 2d is the redesign (m2a's "no aesthetics now" lifted). Style C, "blocks and timeline": native-macOS chrome (system font, `ui-monospace`, the user's accent where the browser gives it, solid surfaces, translucency only as an extra), every action a Warp-style block (status, actor, duration, collapsible output); dark first, light with the system; one token file with a compact/comfortable density; Lucide-style icons. The inspector on the right: approved. The tree: "do what you consider best for a rich and usable file tree". Layout: one instance, **no picker on open**; a hidden but recognisable left rail opens a panel of **all projects and their agent sessions** (hover shows, click pins), and selecting one switches the UI; the tree is the default left panel under an **Editor | Git** switch (Git disabled); centre tabs **Terminal | Editor | Diff** (two disabled), opened files as second-level tabs; bottom tabs Logcat, app logs, build output, Problems, **Activity**, Git (disabled); a toolbar with build, module, variant, a task selector and an Xcode-style status; a right panel with the device, start/stop, the inspector and its actions, the live view reserved (no stills). From Orca: agent rows with a spinner or idle dot and their live operations, running first, unread marks; Copy for the agent (text); natural sort, folders first; external changes at once; copy path and relative path; a name filter; ⌘P for files, `>` for commands. Out: terminal and chats, worktrees, the live view, Git, the editor, an Android view, git decorations, content search, Reveal in Finder, Finder drops.

**The owner should see:** the split (below); Q1 (the projects panel replaces ADR-0045's picker); Q2 (agent sessions from `session` on `operation.state`, so idle connected agents are not listed); Q3 (`file.watch` returns its baseline and publishes its events: one walk feeds the tree and ⌘P); Q4 (no image preview in 2d: the CSP refuses it). All applied as recommended, Delegated.

**Split (recommended, Delegated).** All of it is about eighteen steps. **2d** = the redesign, the projects and agents panel, and a **read-only** tree with the viewer and the palette (twelve steps, three the main session's); **2e** = the file operations (create, rename, move by drag and ⌘X/⌘V, duplicate, delete to a trash with Undo, collisions, multi-select) and their new `file.*` capabilities. The cut isolates the risk: 2d's contract changes are additive fields on streaming capabilities; 2e's are new capabilities that delete project files. For 2e's plan: `file.list` already gives type, size and mtime (no `file.stat`); a human audience for file writes would be a third exception to `vision.md` §Principles #1 and ADR-0015 §6, to be argued in an ADR; a trash under the state directory is usually on another volume than the project (`EXDEV`), so copy then delete.

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| Style C, tokens, density, dark and light, icons; regions resizable and remembered | yes | steps 1, 2 |
| Read-only file tree, viewer tabs, palette | yes | steps 4–6, 9 (Q3, Q4) |
| Projects and agents panel, no picker | yes | steps 3, 7 (Q1, Q2) |
| Toolbar and Activity blocks | yes | step 8 |
| Right panel with Copy for the agent | yes | step 10 |
| File operations (create, rename, move, copy, duplicate, delete with trash and Undo, collisions, drag and drop, multi-select) | no | 2e (split) |
| Image preview; a target in agent rows; idle connected agents | no | Q4; register row 196; Q2 |
| Terminal and chats, worktrees, live view, Git, editor, Android view, decorations, search, Reveal in Finder, Finder drops | no | owner, 2026-10-10; `mvp.md` §Phase 2 |

## Starting point

Code at `c21045f` (2c merged) plus the main session's uncommitted edit to `mvp.md` §Phase 2. API `0.16.0`, 34 MCP tools. `web/`: `App.svelte` shows the picker without `w` (and on a refused `w`, `missing()`), else `Project.svelte` keyed on `w` (`{#key}`, so a switch re-mounts and disposes every model), which stacks every 2c panel (`start/`, `build/` with `Workspace.svelte`'s "All projects", `devices/` with `selectorFor`, `ops/activity.ts`, `logcat/virtual.ts`'s `rowWindow`). No runtime dependency. `internal/domain/file`: `file.list` one level, `{name, type, size, modTime}`, at most 2,000, slimmed after delivery; `file.watch` walks recursively each second, at most 20,000 entries, returns `{subscriptionId, watched, truncated, excluded}` (`excluded` is display text), and cuts a tick's changes at 1,000 with `truncated` after advancing its baseline (`watch.go` 205–212, 238). `capability.Request` has a `Session` (the event channel) but no caller identity, though each adapter has a session id (`daemon.Session.ID`, `sessionIDOf` in `adapters/mcp`, `newSessionID` in `adapters/httpapi/ws.go`); `POST /v1/call` and the artifact route call with none; the MCP binding opens its root before `initialize`. `cmd/droidyard/main.go`'s usage names the picker.

Baseline on 2026-10-10 at `c21045f` with the uncommitted edit (macOS): `go vet ./...` printed nothing; `go test ./...` green (live corpus 25,936 words, budget 26,067), confirmed by the reviewer. With this draft present `TestPlansIndexMatchesFiles` fails (no index row): step 1. `pnpm -C web run verify` not run (the planner's command guard).

## Run rules

- K1–K6 are m2c's (`m2c-fixes-live-console-project-start.md` §Run rules) with `m2d` for `m2c`: `B=~/Library/Caches/droidyard-dev/m2d`, fresh state and configuration per run exported on every `droidyard` invocation, the adb guard at `$B/bin/adb`, `ANDROID_SERIAL=droidyard-m2d-none`, the scratch AVD `droidyard_m2a` reused through `$B/avd`, no owner device writes, no adb server restart, nothing in `~/Documents/DopamineControl`, the m2c demo daemon untouched.
- K7. Headless pages may be captured (`Page.captureScreenshot`); the owner's screen never.
- K8. Front-end steps load `svelte-core-bestpractices` and `svelte-code-writer` first, pass `pnpm -C web run verify` and `node web/scripts/autofixer-gate.mjs` on every changed `.svelte`/`.svelte.ts`, keep logic in `.ts` with Vitest beside it (m2a D7), `TestNoHTMLSinks` green, and every model that subscribes unsubscribes on dispose.

## Delegated decisions

- D22. **One version**: step 3 moves `0.16.0` → `0.17.0`; step 4's additions ride on it.
- D23. **Libraries.** No component library (m2a D3 stands): `<dialog>`, the Popover API and `title` serve menus, overlays and tooltips; Chrome is the browser run. Two runtime dependencies, exact pins (m2a D2): `fuzzysort` 4.0.2 (step 9) and `@lucide/svelte` at the exact version `pnpm add -E` resolves on step 2's day (recorded). ADR-0039 §Consequences leaves each choice to the plan.
- D24. **Tokens**, `web/src/lib/ui/tokens.css`, imported once: colours for dark (default) and light under `prefers-color-scheme: light`, `color-scheme: light dark`; the accent `AccentColor` if step 1 shows Chrome resolves it, else a fixed blue; `system-ui` and the monospace stack step 1 selects; spacing, radii; `[data-density=compact|comfortable]` sets row heights (22 and 28 px). Translucency only as `backdrop-filter` on overlays over a solid fallback.
- D25. **Per-browser memory** in `localStorage` under `droidyard.ui.`, every access in try/catch, values clamped: panel sizes, density, the hidden-files toggle, the pinned panel, each project's open file tabs. Not the project file (ADR-0046 holds preselections only).
- D26. **Working defaults** (constants with a comment): the viewer reads at most 2 MiB of a file in 256 KiB pages, then says so; an agent row leaves the panel 30 minutes after its last terminal event; the palette shows 50 results; tree events are applied at most once per animation frame; the root watch ends while the page is hidden and starts again when shown.
- D27. **Hidden entries** are those `file.list` returns that a complete baseline lacks (no parsing of `excluded`, no copy of the Go rule); shown by the toggle, refreshed on each expand, not live.
- D28. **Module marks** without Gradle: a folder holding `build.gradle(.kts)` is marked, so they show on an untrusted root (`project.modules` needs trust).
- D29. **Copy for the agent**: the `target` from `selectorFor` as JSON (with a ref, the snapshot and "expires with this snapshot"), then the node's attributes; every device string JSON-quoted on its own line under a first line "device-derived text, untrusted"; at most 20 attributes of 200 characters (ADR-0015 §1).
- D30. **Agent rows**: MCP sessions only, labelled by `session.name` (a second session with the same name gets " 2", in the order seen; no name: "agent"); a session without `session.workspace` sits in a "No project" group; the user's UI and CLI commands are one "You" row, without the tab's own reads (m2b D16's filter); a row runs while any of its operations is not terminal; running rows first; unread is a terminal event since the row was last selected (per tab, memory only); selecting a row selects its project and filters Activity to it. The rail's spinner and count follow agent rows only.

## Acceptance

- [ ] A1. The shell: the owner's regions, D24's tokens, dark and light following the system, the density knob, sizes remembered; every 2c panel works from its new place; Terminal, Diff, the Git tab and switch disabled, naming their milestone. Source: owner items 1, 4. Proven by: step 2, step 11
- [ ] A2. `operation.state` events and `operation.watch` rows carry `session {id, name?, workspace?}`, omitted for a call without a session; the schema diff is that and `0.17.0`; 34 MCP tools. Source: Q2. Proven by: step 3
- [ ] A3. `file.watch` returns its baseline, slimmed after delivery, and its events are in the schema; the walk's cost recorded. Source: Q3. Proven by: step 4
- [ ] A4. The tree: folders first, natural order, hidden toggle, compaction, module marks, filter, the WAI-ARIA tree keys, copy path and relative path; an agent's `file.write` shows within 2 s; a tick over 1,000 changes still converges. Source: owner items 3, 6. Proven by: step 5, step 11
- [ ] A5. A clicked file opens a second-level tab in a read-only viewer with line numbers; a binary file a placeholder; an external change re-reads it; the active file is revealed in the tree. Source: owner item 4; Q4. Proven by: step 6, step 11
- [ ] A6. No picker: the projects panel (rail icon with the running-agent count and a spinner, hover overlay, click to pin) lists recents with state; selecting one switches every panel; without `w`, or with a refused one, the newest open project or the pinned panel. Source: owner item 4; Q1. Proven by: step 7, step 11
- [ ] A7. Two scripted agents show as two named rows with their live operations and durations, running first, unread marked. Source: owner item 5; D30. Proven by: step 7, step 11
- [ ] A8. Toolbar status ("Building :app · 12 s", any client's build) and the task selector; Activity as blocks with status, actor, duration, output. Source: owner items 1, 4. Proven by: step 8, step 11
- [ ] A9. ⌘P finds files, `>` runs commands. Source: owner item 6. Proven by: step 9, step 11
- [ ] A10. The right panel: device, start/stop, inspector, actions, Copy for the agent as D29. Source: owner items 2, 4, 6. Proven by: step 10, step 11
- [ ] A11. Same primitive: every `client=ui` call in the trace is a published capability; the CDP log only `/`, `/assets/…`, `/favicon.ico`, `/v1/ws`; no CSP violation. Source: `mvp.md` §Phase 2; `http-websocket.md` §Security baseline. Proven by: step 11
- [ ] A12. The register, `docs/README.md` and the plans index record what 2d closed and left, and 2e. Source: `milestone-workflow` step 6. Proven by: step 12

## Steps

### 1. Baseline, probes and the three ADRs

- Builds: the main session commits the `mvp.md` edit with **2e** (the file operations) named after 2d, this plan's index row, `liveBudget` raised by at most 100 words with the reason (steps 4 and 7 change `files.md` and `http-websocket.md`); Q1, Q2 and Q3's ADRs with their index rows. **Probe P** (K4, `$B/l4-probe-<stamp>/`): on a K1 daemon's page in headless Chrome over CDP, `CSS.supports('color', 'AccentColor')` and its computed value; the platform font `ui-monospace` resolves to (`CSS.getPlatformFontsForNode`); `prefers-color-scheme` under `Emulation.setEmulatedMedia`; `isSecureContext` and `navigator.clipboard.writeText` after a dispatched click, without then with `Browser.grantPermissions`; a meta+P `keydown` reaching a page listener.
- Implements: `milestone-plan` §Writing one 3; risk first for D24.
- Run by: the main session.
- Check: `go test ./internal/doccheck` (index, ADR mentions, budget) and `go test ./...` green; P's answers on the Done line, choosing D24's accent and monospace stack.
- Schema: no change.
- Needs: nothing.
- Not here: code.
- Rests on: —
- [x] Done. Evidence: ADR-0047–0049 indexed, ADR-0043/0045 headers; `mvp.md` names 2e; the index row; no budget raise needed (25,942 of 26,067). Probe P (headless Chrome 7317, K1 daemon): `CSS.supports('color','AccentColor')` true but computes `rgb(0, 117, 255)`, Chrome's own blue in dark and light, not the system accent → D24 takes a fixed accent token; `ui-monospace`, `"SF Mono"` and `SFMono-Regular` all render **Menlo**, `system-ui` renders `.SF NS` → mono stack `ui-monospace, "SF Mono", Menlo, monospace` (SF Mono in Safari, Menlo in Chrome); `prefers-color-scheme` follows `Emulation.setEmulatedMedia`; `isSecureContext` true and `navigator.clipboard.writeText` resolves after a user-gesture eval without a grant; a meta+P `keyDown` reaches a page `keydown` listener (headless only).

### 2. The tokens and the shell

- Builds: `web/src/lib/ui/`: `tokens.css` (D24); `layout.ts` (region sizes, clamping, D25) with `Splitter.svelte`; `Shell.svelte`: toolbar, left rail and left panel, centre with first-level tabs, bottom panel with tabs, right panel; `panels.ts`, the registry (id, region, disabled-with-milestone) `Shell` mounts from. The 2c panels move as they are: Workspace, Trust, ProjectFile and Build to the toolbar; Logcat, AppLogs, Console, Problems, Activity to bottom tabs, Tasks as a bottom tab until step 8; Devices, Tree, Act to the right panel; the picker stays the centre's content without `w` until step 7. Disabled: Terminal, Diff, the bottom Git tab, the left panel's Git side, each naming its milestone (`mvp.md` §Phase 2); a reserved live-view area. `@lucide/svelte` (D23). Styles through classes and `style:` directives only (m2a D6).
- Implements: owner items 1, 4; D23–D25.
- Run by: the implementer; review: the main session's diff review, `code-review` at `low` if over about 200 lines. If the run under-delivers, the main session splits it (tokens and layout first, the move second).
- Check: Vitest `layout.test.ts`: a size outside its bounds clamped; sizes and density restored; a storage that throws on read and on write leaves defaults and no exception (seen failing without the try/catch). `panels.test.ts`: every 2c panel id in exactly one region; each disabled entry names a milestone and cannot be activated. Existing Vitest green; K8. Level 4 (main session, no device): captures dark and light, compact and comfortable; each 2c panel's main control used once from its new place (Build, the console, Logcat's filter, Trust, a task run); no CSP violation.
- Schema: no change.
- Needs: step 1. May run in parallel with steps 3–4 (web only).
- Not here: the tree (5), the projects panel (7), the task selector and blocks (8), the inspector's new parts (10).
- Rests on: no 2c panel loses behaviour in the move (level 4; the models' suite cannot see a lost component).
- [ ] Done. Evidence:

### 3. `session` on the operation events (Q2)

- Builds: `capability.Request` gains `Origin {id, name, root}` (beside the existing `Session`), set by `adapters/mcp` (the session id; `name` from `initialize`'s `clientInfo` through mcp-go's `SessionWithClientInfo`, absent before it; `root` the binding), the WebSocket (its session id) and the CLI shim (`daemon.Session.ID` threaded to the native handler). The store records it; `OperationState` and `ActiveOperation` gain `session {id, name?, workspace?}`, `workspace` the bound root's workspace id while open; `session` omitted for a call recorded without an origin (`POST /v1/call`, the artifact route, internal). A name keeps at most 64 runes, with control and format (Cc, Cf) characters removed. `system.Version` `0.17.0`; `go test ./internal/schema -update`.
- Implements: Q2 (its ADR); ADR-0043 §1, §3 unchanged; ADR-0010.
- Run by: the implementer (effort high); review: **Opus verifier** (a contract change, session identity).
- Check: `TestOperationStateCarriesTheSession` (`cmd/droidyard`, two real shims with `clientInfo` "agent-a" and "agent-b" bound to two roots, one WebSocket session, one `POST /v1/call`): each shim's `file.list` and global `system.health` events carry its own id, name and bound workspace; the WebSocket's carry its id and no name; `POST /v1/call`'s carry no `session`; the bound open before `initialize` carries no name. `TestActiveOperationCarriesTheSession`. `TestSessionNameIsBounded` (300 runes with `\n`, `\x1b`, U+202E). `TestNoCapabilityTakesASessionID` (registry params). Seen failing with the adapters' wiring removed. `TestMCPTranscript` 34 tools; the schema diff line by line.
- Schema: `operation.watch`: `ActiveOperation` and the `operation.state` payload gain optional `session` (`id` required inside it, `name`, `workspace`); `version` `0.16.0` → `0.17.0`; nothing else.
- Needs: step 1.
- Not here: a session list or idle sessions (Q2); parameters or a target on the events (ADR-0043 §2); the trace line.
- Rests on: a session id grants nothing, no capability takes one (level 3).
- [ ] Done. Evidence:

### 4. `file.watch` for the tree (Q3)

- Builds: `internal/domain/file/watch.go`: `WatchResult.entries [{path, type}]`, always, the baseline map the first tick diffs against, sorted, at most 20,000 (the existing `truncated`); a `Slim` that empties `entries` after delivery (ADR-0020 §Retention, as `file.list`); the descriptor's `Events` (topic `file.changes`, types `changes` and `stopped`, today's payloads). `BenchmarkWatchWalkNested` on a generated tree, depth 6, 20,000 entries. `files.md` §The watch: one clause.
- Implements: Q3 (its ADR); m1j D7, D8; ADR-0041 §4.
- Run by: the implementer (effort high); review: **Opus verifier** (a contract change).
- Check: `TestWatchEntriesAreTheBaseline`: entries equal the walk, excluded entries absent; a file created through a hook after the baseline arrives `created` in an event and is not in `entries` (seen failing with a second walk for `entries`); 20,001 entries → 20,000 and `truncated`; 1,001 changes in a tick → an event with `truncated: true`. `TestWatchEntriesAreSlimmed`: an `operation.wait` after delivery returns no entries (seen failing without the `Slim`). The generated client types `file.changes` (`pnpm -C web run check`). The benchmark on the Done line: **above 100 ms per walk the plan stops before step 5**; the fallback (recorded under Changes) watches expanded folders only and ⌘P indexes loaded folders only.
- Schema: `file.watch`: result `entries`, `events` (`file.changes`: `changes`, `stopped`); no version change.
- Needs: step 3 (version and golden file).
- Not here: an OS watcher, configurable exclusions (register row 132); the 1,000-change cut (the client re-watches, step 5).
- Rests on: entries plus later events equal the tree while a tick holds at most 1,000 changes (level 3).
- [ ] Done. Evidence:

### 5. The file tree (Q3)

- Builds: `web/src/lib/files/`: `watch.ts` (the root `file.watch`; again after a reconnect, a `stopped`, or a `truncated` event while the baseline was complete; with an incomplete baseline, where every event says `truncated`, its changes are applied and the expanded folders re-listed only when an event holds 1,000 changes (the cut, `watch.go` 238); ended by `events.unsubscribe` on dispose and while hidden, D26; events applied per frame); `tree.ts` (children from a complete baseline, else and for hidden entries from `file.list` on expand; rows of expanded folders only; `Intl.Collator` numeric order, folders first; single-child folder chains compacted; D27, D28; a name filter keeping ancestors of matches; one selected row; type-ahead; `reveal(path)`); `FileTree.svelte` over `rowWindow` (`role="tree"`, `aria-activedescendant`, `aria-expanded`, `aria-level`, `aria-selected`), the filter field, the hidden toggle, a context menu (right click, Shift+F10) with Copy path and Copy relative path.
- Implements: Q3; owner items 3, 6; D26–D28.
- Run by: the implementer; review: the main session's diff review, `code-review` at `low`.
- Check: Vitest `tree.test.ts`: `file2` before `file10`, folders first; `src/main/java/com/x` one row expanding to the leaf; a `created` under an expanded folder adds a row, under a collapsed one none; deleting the selected row selects its neighbour; a truncated baseline lists on expand; the toggle shows an expanded folder's `file.list` entries the baseline lacks; the filter keeps ancestors; Right, Left, Home, End, type-ahead "ma"; relative and absolute paths; 10,000 entries to rows under 50 ms in Node. `watch.test.ts`: `truncated` with a complete baseline → the old subscription ended, then one re-watch; with an incomplete one, an event of 10 changes → applied, no `file.list`, and one of 1,000 → re-lists, no re-watch; dispose and hide unsubscribe. Seen failing reversed. Level 4 (main session): the sample's tree; a `droidyard call file.write` shows within 2 s.
- Schema: no change.
- Needs: steps 2, 4.
- Not here: file operations, multi-select (2e); git decorations, search (owner).
- Rests on: the root watch keeps every baseline folder current; hidden folders and folders past the cap are current as of their last expand (level 4).
- [ ] Done. Evidence:

### 6. Centre tabs and the read-only viewer (Q4)

- Builds: `web/src/lib/editor/`: `tabs.ts` (second-level tabs per project, D25, a vanished path dropped on restore; opening an open file focuses it); `viewer.ts` (`file.read` pages to D26's cap; a binary page → "binary file, size" and Copy path; a `modified` event for its path re-reads; `deleted` marks the tab); `Viewer.svelte` over `rowWindow` with line numbers, monospace, no highlighting. Selecting a tab reveals its file in the tree.
- Implements: owner item 4; Q4; D25, D26.
- Run by: the implementer; review: the main session's diff review.
- Check: Vitest `viewer.test.ts`: pages joined at their offsets; the cap stated; binary → placeholder, no text; `modified` re-reads, `deleted` marks. `tabs.test.ts`: restore drops a missing path; Terminal and Diff cannot open. Seen failing reversed. Level 4 (main session): a CLI `file.write` to an open file re-renders it within 2 s.
- Schema: no change.
- Needs: step 5.
- Not here: editing, highlighting (the editor milestone); image preview (Q4).
- Rests on: —
- [ ] Done. Evidence:

### 7. The projects and agents panel (Q1, Q2)

- Builds: `web/src/lib/projects/`: `projects.ts` (from `PickerModel`: `workspace.watch` rows, "Open folder…", Trust); `sessions.ts` (D30, from `operation.watch` with `session`); `Rail.svelte` (icon, running-agent count, a spinner while an agent runs, hover overlay, click to pin, Escape closes); `Projects.svelte` (projects newest first, each with its agent rows and their running operations, capability and live duration; the "No project" group). Selecting routes to `#w=<id>` after `workspace.open {workspace}` when closed. `route.ts` and `App.svelte`: no picker view; without `w`, the newest open workspace, else the panel pinned; a refused `w` → the panel pinned with the reason (`missing()`); `Workspace.svelte`'s "All projects" opens the panel. `Picker.svelte` removed. `cmd/droidyard/main.go`'s usage line, `client.go`'s three picker messages (356, 403, 408) and `start_test.go`'s test name, and `http-websocket.md`'s "without `w` is the project picker" sentence.
- Implements: Q1, Q2 (their ADRs); owner items 4, 5; D30.
- Run by: the implementer; review: the main session's diff review.
- Check: Vitest `projects.test.ts`: a closed row's click opens then routes (seen failing routing first); without `w` the newest open is chosen, none open pins the panel; a refused `w` pins it with the reason. `sessions.test.ts`: two MCP sessions → two rows, the same name → " 2"; `ui` and `cli` commands → one "You", reads absent; a global operation under its `session.workspace`, none → "No project"; running first; unread set on an unselected row and cleared on select; a row gone 30 minutes after its last terminal event (fake clock); the rail count and spinner ignore "You". Seen failing reversed. Level 4 (main session): two named K5 shims → two live rows; hover and pin.
- Schema: no change.
- Needs: steps 2, 3.
- Not here: idle connected agents (Q2); removing a recent; reusing an open tab (register row 201).
- Rests on: a session's rows follow its events alone (level 3).
- [ ] Done. Evidence:

### 8. The toolbar and Activity blocks

- Builds: `web/src/lib/toolbar/`: `status.ts` ("Building :app · 12 s" from the build mirror, any client's, actor named; a clock seam), the task selector over `build.tasks` running `build.run` (from `Tasks.svelte`, whose bottom tab goes), module, variant, Build. `web/src/lib/ops/blocks.ts` and `Activity.svelte` as blocks: status, actor (D30's label or "You"), duration ticking then `durationMs`, collapsible output: a build's console lines by `buildId` while `ConsoleModel` holds that build, else "open in Build output"; the tab's own failed call its code and message; another client's non-build operation none (ADR-0043 §2).
- Implements: owner items 1, 4; m2b D11, D16.
- Run by: the implementer; review: the main session's diff review.
- Check: Vitest `status.test.ts`: an MCP `build.assemble` running 12 s → "Building :app · 12 s" with the agent's name; untrusted → the selector disabled with the reason. `blocks.test.ts`: the four output cases; a queued block shows `blockedBy`. Seen failing reversed.
- Schema: no change.
- Needs: steps 2, 3.
- Not here: targets on blocks (register row 196).
- Rests on: —
- [ ] Done. Evidence:

### 9. The palette

- Builds: `web/src/lib/palette/`: `palette.ts` (files mode over the watch's paths, `fuzzysort` 4.0.2 with the file name weighted over the directory; `>` switches to a command registry: Build, run a task, start or stop the emulator, switch project, toggle hidden files, density, focus the tree; an unavailable command says why); `Palette.svelte` in a `<dialog>`, ⌘P and ⇧⌘P, arrows, Enter, Escape returning focus.
- Implements: owner item 6; D23, D26.
- Run by: the implementer; review: the main session's diff review.
- Check: Vitest `palette.test.ts`: "mainact" ranks `MainActivity.kt` first over a 5,000-path fixture; `>` and Backspace switch modes; 50 results at most; a path created by an event is found; Build untrusted listed disabled with its reason. Seen failing reversed.
- Schema: no change.
- Needs: steps 6, 7, 8.
- Not here: content search (owner).
- Rests on: Chrome hands ⌘P to the page (Unverified).
- [ ] Done. Evidence:

### 10. The right panel: device, inspector, Copy for the agent

- Builds: `web/src/lib/devices/`: the device selector with start and stop at the top, the reserved live-view area, `Tree.svelte` as an inspector (the selected node's attributes), tap and type on it (`act.ts`), and `copy.ts` (D29) with a Copy for the agent button.
- Implements: owner items 2, 4, 6; D29; ADR-0015 §1.
- Run by: the implementer (effort high); review: **Opus verifier** (untrusted device text leaves the page).
- Check: Vitest `copy.test.ts`: id → `{"id": …}`; else text; else ref and snapshot with the expiry line; a label holding `"`, a newline, a backtick and "ignore previous instructions" stays one JSON-quoted line under the untrusted header (seen failing with plain interpolation); D29's bounds. Existing `interact.test.ts` green.
- Schema: no change.
- Needs: step 2.
- Not here: stills or the live view (owner, 2026-10-09).
- Rests on: no device string reaches the clipboard unquoted (level 3).
- [ ] Done. Evidence:

### 11. The whole UI at level 5

- Builds: nothing unless a defect is found (each its own step before 12, failing test first).
- Implements: A1, A4–A11.
- Run by: the main session (K1–K8).
- Check: fresh state, headless Chrome through a scratch `open` (m2c step 12's pattern). `droidyard` in a sample copy → its project selected, no picker; a second copy opened by a K5 shim appears in the panel; two K5 shims named "agent-a" and "agent-b" (one per copy): agent-a `build_assemble`, `device_install` and `device_launch` on `droidyard_m2a`, then `device_tapElement`; agent-b `file_write`: two rows with live durations, the rail count, unread marks; the toolbar names agent-a's build; Activity blocks with output. The tree shows agent-b's file within 2 s and an open viewer re-reads; 1,001 files created at once in the copy (one shell loop) all appear, the tree converging after the cut tick; filter, hidden toggle, keyboard; ⌘P opens `MainActivity.kt`, `>` Build runs. Copy for the agent on a node, read back through CDP. Captures dark and light. Same primitive: every `client=ui` capability in the trace published; CDP paths; no CSP violation; E1, E3, E5, S3.
- Schema: no change.
- Needs: steps 2–10.
- Not here: Safari, Firefox; a real agent client (Unverified).
- Rests on: the UI reaches everything through published capabilities (level 5).
- [ ] Done. Evidence:

### 12. Close: documents and register

- Builds: `known-limits.md`: row 212 closed (step 4); rows added: image preview, a target in agent rows, idle agent sessions, the file operations (2e), Unverified rows not settled; `docs/README.md` names 2e; the plans index `Done`.
- Implements: A12.
- Run by: the main session; review: **Opus verifier** over A1–A12.
- Check: `go test ./internal/doccheck` passes; every Not-in-this-milestone item has a register row or a named milestone.
- Schema: no change.
- Needs: step 11.
- Not here: 2e's plan.
- Rests on: —
- [ ] Done. Evidence:

## Not in this milestone

- File operations, multi-select and their capabilities: 2e (the split above).
- Image preview: the editor milestone (Q4).
- A target in agent rows and Activity blocks: ADR-0043's alternative, register row 196.
- Idle connected agents: the agent sessions milestone (Q2).
- Terminal and chats, worktrees, the live view, Git, the editor, an Android grouped view, git decorations, content search, Reveal in Finder, Finder drops: owner, 2026-10-10.
- Reusing an open tab, removing a recent: register row 201.

## Design questions

Each has a recommendation applied as Delegated; Q1–Q3 become ADRs at step 1.

### Q1. How does one tab show every project?

- Blocks: steps 7, 11
- The documents say: ADR-0045 §1: outside a project the browser opens "at `/#token=<t>`, the picker"; §2: one tab per project, the picker is the page without `w`, several projects are several tabs. The owner: one instance, no picker on open, a panel of all projects switches the UI.
- Draft decision, an ADR superseding ADR-0045 §1 (in part: the picker) and §2 (in part): (1) A left-rail panel lists `workspace.watch`'s rows with their agent sessions (Q2); selecting one opens it if closed and selects it; `w` still names the selection, so a reload stays; the project view is re-mounted per selection (today's `{#key}`). (2) No picker view: without `w` the newest open workspace is selected; with none open, or a `w` the daemon refuses, the panel opens pinned with recents, "Open folder…" and any reason. (3) The rest of ADR-0045 §1 and §4–§6 stand: `droidyard` in a project opens a tab on it, since the daemon cannot raise a page. No capability changes.
- Alternatives considered: **keep the picker view** — the owner's "no picker on open"; **one tab per project, enforced** — the owner switches in place; **the selection in `sessionStorage`** — the address is the state (reload, a copied link); **a capability to focus an open tab** — no page can raise itself; register row 201.

### Q2. How does the UI know an agent session?

- Blocks: steps 3, 7, 8
- The documents say: ADR-0043 §2: `operation.state` carries `client` (a kind), never parameters; nothing names a session. Two Cursor windows are both `mcp`.
- Draft decision, an ADR superseding ADR-0043 §2 in part: `operation.state`'s payload and `operation.watch`'s rows gain `session {id, name?, workspace?}`, omitted for a call recorded without a session (plain HTTP, the artifact route, internal): the daemon session's opaque id; for MCP, `clientInfo.name` from `initialize` (at most 64 runes, control and format characters removed; absent before `initialize`); `workspace` the session's bound workspace when open. Not untrusted (a local client names itself, no device text). No capability takes a session id. Rows come from operations, so a connected agent that has run nothing since the tab loaded is not listed, and a disconnected one ages out (D26). `0.17.0`; streaming, so no MCP change (ADR-0036).
- Alternatives considered: **a `session.watch` capability** (connected sessions, connect and close events) — lists idle agents, but a second stream and lifetime for a panel the agent sessions milestone rebuilds when the daemon hosts the sessions; it wins there; **group by client kind** — merges two agents; **a target summary** — ADR-0043 rejected parameters on events.

### Q3. Where does the file tree's data come from?

- Blocks: steps 4, 5, 9
- The documents say: `file.list` is one level; `file.watch` walks the tree every second but returns only a count (m1j D7) and its events are unpublished (register row 212); recursive listing is deferred (register row 131).
- Draft decision, a new ADR: `file.watch`'s result carries the baseline walk's `{path, type}` entries (at most 20,000, the existing `truncated`), slimmed after delivery (ADR-0020 §Retention), and `file.changes` is published through its descriptor (ADR-0041 §4). One root watch per tab feeds the tree's structure, its refresh and ⌘P's index; `file.list` serves hidden entries and a truncated baseline; a tick cut at 1,000 changes (`truncated`) makes the client re-watch. Always, not behind a parameter: the UI is the only caller (streaming), so no MCP change. No new error code.
- Alternatives considered: **the client walks `file.list`** — no contract change, but one recorded operation per directory: two `operation.state` events each to every watching tab, thousands at once against a subscription's 1,024-event queue, and the trace flooded; **a recursive `file.list` or `file.index` query** — an MCP tool (ADR-0036) for what agents' own glob does; **an OS watcher** — m1j D7's rejection stands; **a daemon that never cuts a tick** — unbounded events; the re-watch costs one walk.

### Q4. Does the viewer preview images?

- Blocks: step 6
- The documents say: the brief chose a viewer "with image preview, since `file.read` exists", but `file.read` returns no text and no bytes for a binary page, and the CSP `default-src 'self'` refuses `data:` and `blob:` images; `http-websocket.md` §Security baseline adds no rule in Phase 2 and m2a D6 forbids widening it.
- Draft decision (no ADR: no decision changes): no image preview in 2d; a binary file shows its size and Copy path. The editor milestone, which needs bytes anyway, decides a byte read and `img-src`.
- Alternatives considered: **`file.read` with base64 plus `img-src data:`** — a CSP change and a field on an MCP tool for a placeholder; **an artifact route for project files** — a new HTTP route in Phase 2.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| Chrome resolves `AccentColor` and `ui-monospace` to the system's | 2 | step 1 P; D24's fallbacks |
| Headed Chrome hands ⌘P to the page (headless shows the listener only) | 9 | the owner's trial; else a register row and another binding |
| A 20,000-entry nested walk each second per visible tab is cheap | 4, 5 | step 4's benchmark; register row 130 |
| Real clients' `clientInfo.name` (Cursor, OpenCode) reads well in a row | 7 | step 11 uses scripted names; a register row if not seen |
| `navigator.clipboard.writeText` works from the loopback page with a click, without a granted permission | 5, 10 | step 1 P; the owner's trial |
| `fuzzysort` 4.0.2 and `@lucide/svelte` exist at those pins and behave as their tests assume | 2, 9 | each step's install and tests |
| The hover-to-reveal rail is recognisable and comfortable | 7 | the owner's trial |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | B1 [contract] blocking: step 4's `entries` never slimmed, against ADR-0020 §Retention | Step 4: a `Slim`, `TestWatchEntriesAreSlimmed` seen failing without it; Q3 says so |
| 1 | | B2 [bug] blocking: a tick over 1,000 changes is cut after the baseline advanced (`watch.go` 205–212, 238); nothing re-syncs | Step 5: `truncated` with a complete baseline re-watches, with an incomplete one re-lists; tests; step 4 tests the cut and qualifies Rests on; step 11 creates 1,001 files at once |
| 1 | | B3 [contract] blocking: ADR-0045 §1 also names the picker; refused `w`, "All projects" and the usage text missed | Q1 supersedes §1 and §2 in part; step 7 names all three with a test for a refused `w` |
| 1 | | B4 [contract] blocking: `session.id` required but `POST /v1/call`, the artifact route and internal calls have no session | `session` omitted then; a `POST /v1/call` case; schema line says optional |
| 1 | | N1 [bug]: the root watch never unsubscribed across re-mounts | Step 5 and K8: dispose (and hide, D26) unsubscribes, tested |
| 1 | | N2 [contract]: `excluded` is display text | D27: hidden = `file.list` entries a complete baseline lacks |
| 1 | | N3 [plan]: step 7's Rests on contradicted D27 | Qualified (now step 5) |
| 1 | | N4 [test]: step 2's Rests on at level 3 cannot see a lost panel | Level 4, each panel's control used once |
| 1 | | N5 [plan]: steps 2 and 7 large; the tree could come earlier | Tree and viewer moved to steps 5–6; step 2 kept whole with a stated split if it under-delivers |
| 1 | | N6 [plan]: the UI's own reads keep "You" and the rail spinning | D30: rail follows agent rows; "You" without reads |
| 1 | | N7 [plan]: duplicate names, unbound sessions | D30: " 2" suffix, "No project" group |
| 1 | | N8 [plan]: the 100 ms gate's cost and ⌘P under the fallback | D26 stops the watch while hidden; step 4 states ⌘P's fallback |
| 1 | | N9 [doc]: 2e not in the `mvp.md` edit | Step 1 adds it |
| 1 | | N10 [plan]: `withEntries` and multi-select beyond the smallest proof | Entries always; multi-select to 2e |
| 1 | | Nits: three main-session steps; the queue is per subscription; `Request.Session` exists; Cf characters; step 11's install | All applied (`Origin`; U+202E in the test) |
| 2 | approve | B1–B4 answered, checked against the code; no new blocking finding | — |
| 2 | | Important: past 20,000 entries every event says `truncated`, so step 5 would re-list expanded folders each second, the flood Q3 rejects | Step 5: changes applied; re-list only on an event of 1,000 changes (the visible cut); tested |
| 2 | | Nits: `client.go`'s three picker messages and a test name; "as `client` is" loose; assert the old watch ends before the re-watch | Step 7 names them; phrase dropped; `watch.test.ts` asserts it |

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.
