# Plan: Milestone 2n — Git history

- Status: Approved
- Approved: 2026-10-12, Delegated (the main session under the owner's standing instruction to apply the recommended option), part 1: the Scope table, the ten steps, Not in this milestone, Unverified, and Q1 and Q2 as drafted. Q4 as drafted: one milestone in two parts, `m2n` may merge into `main` at the part boundary. Q3 as drafted, with the owner questions taken at their drafts: (a) hooks stay off; (b) git's message is enough for a refused checkout until stash exists; (c) synchronised operations stop at the first failing root, no rollback; (d) mixed roots in one log, as item 7 asks; (e) the UI refuses a checkout while a buffer is dirty, naming the files; (f) Fetch on an untrusted root shows, refused with a nextStep to trust the project. Q3's ADR is written, and part 2's steps written and reviewed, at the part boundary.
- Date: 2026-10-12
- Milestone: "2n — Git history". Picks up the rows 2l left open in `docs/roadmap/known-limits.md` (history, the bottom Git panel; `log` and `branches`; in part the Git actions row) and the owner's requirements below.
- Phase: `docs/roadmap/mvp.md` §Phase 2, the **Git** milestone ("branches and commits later"), in part; see Scope.
- Contract: Q1–Q3 below, Delegated; Q1's and Q2's ADRs written at step 1, Q3's at the part boundary. Also: ADR-0066 (§1 widening `base`, §2 invocation, §3 untrusted drivers, §6 no untrusted mark), ADR-0067 §2–§4, ADR-0060 §2, ADR-0015 §1, §2, §5, ADR-0020 §Retention, ADR-0053 §2 (per-item results); `capability-api.md` §Error model, §Discovery and versioning; `overview.md` §Concurrency; `http-websocket.md` rule 7 (CSP `default-src 'self'`); plan m2l.

**The owner should see:**
- The work is cut in two parts of about ten steps: part 1 is the history you read (graph, side panel, the expanded commit, its diff); part 2 is fetch, checkout, new branch, submodule update, submodules as roots and synchronised branches (Q4).
- The writes contradict ADR-0066 ("the daemon never fetches"), so they need a new ADR; Q3 drafts it, and its six questions were taken at their drafts (see Approved).
- Part 1 adds three human-only capabilities, `git.log`, `git.refs` and `git.show`, and lets `git.read` read a file at a commit. No MCP tool, no tokens (your item 11).
- The graph is laid out in the browser from each commit's parents, as the Review diff's maps are; no lane data enters the published contract (Q2).
- A merge commit's files are shown against its first parent (Q1 §5).

## The owner's requirements

Gathered in conversation, 2026-10-12. Items 6–11 are the owner's decisions; the rest are requirements the plan turns into scope.

1. **A commit history**, so that Git is "at least reasonably complete" next to 2l's Tree, Changes and Diff.
2. **A branch visualiser**: the commit graph with its lanes and the refs on each commit.
3. **Submodule support.**
4. **A layout like Android Studio's Git Log tool window**, from the owner's screenshot:
   - left: a branch tree (HEAD (current branch), Local grouped in folders by prefix such as `bugfix/`, `chore/`, `feature/`, Remote, Tags) with a "Branch or tag" filter;
   - centre: the commit list, with a filter bar (text or hash, regex and case toggles; Branch, User, Date and Paths filters), a coloured branch-label column, the graph, subject, author, date and short hash;
   - right: the selected commit's changed files ("Select commit to view changes") and, below, its details.
   Refined after the survey (2026-10-12), replacing the centre and left of the layout above:
   - **Graph and commits like vscode-git-graph** ([demo](https://raw.githubusercontent.com/mhutchie/vscode-git-graph/master/resources/demo.gif)): one table using the full width and height (graph and subject, author, SHA, commit time); clicking a commit expands it in place to show its file tree, message, author and the rest. The look only: its licence bans derivative works, so none of its code.
   - **Side panel like SourceGit**: a "Filter Branches/Tags/Submodules" box above collapsible sections with counts, Local branches (current marked), Remotes, Tags, Submodules, Worktrees, each with its own small actions (sort, tree or list, add, update).
   - **An operations toolbar above, like SourceGit**: fetch, pull, push, stash and branch operations, and the current branch at the right end.
   - **The branch and submodule workflow like IntelliJ's.**
5. **Learn from existing clients and libraries without copying them.** A friend's project, [BeardGit](https://github.com/The3eard/BeardGit), is a reference for its strategy, not for its code. Nothing is downloaded until the owner says so.
6. **Scope: reading plus the safe writes** (owner, 2026-10-12, chosen over "read only, writes in 2o" and "every operation in 2n"). 2n takes fetch, checkout of a branch, creating a branch and updating submodules. Pull, push, merge, rebase and stash wait for a later milestone; their buttons show and stay disabled until then.
7. **The IntelliJ workflow, all four parts** (owner, 2026-10-12):
   - **A menu per branch:** Checkout, New branch from, Compare with current, Merge into current, Rebase onto, Push, Rename, Delete. Under item 6 the writes past checkout and new branch stay disabled. Compare with current only reads, so it can be in 2n.
   - **Branches in folders** by prefix (`feature/`, `bugfix/`, `chore/`) as a tree, favourites first.
   - **Submodules as roots:** each submodule is one more repository; the log can mix them with a colour stripe per root and filter by root.
   - **Synchronised branches:** checkout and new branch apply to the repository and its submodules at once (IntelliJ's synchronous branch control).
8. **Rename and Delete** in the branch menu wait for the later milestone with the other writes (owner, 2026-10-12).
9. **A changed file of an expanded commit opens in the Diff tab** (the centre, above), comparing the file at that commit with the commit before it, to show what the commit changed (owner, 2026-10-12).
10. **The expanded commit and the top of the view** (owner, 2026-10-12, from two screenshots; the descriptions are the main session's):
    - From a vscode-git-graph screenshot, only for how a commit expands: the panel opens in place under the commit row, across the table's full width, and the rows below move down; a close (×) at its top right and a small vertical strip of view buttons (tree or list; one more, perhaps find or compare, unverified). Left half: Commit (full hash), Parents (full hashes), Author (name and email), Date (full, with the time zone), Committer, a blank line, the full message. Right half: the changed files as a folder tree (folders collapsible), each with its counts as "(+26 | -2)"; a click opens item 9's diff.
    - Graph details from the same screenshot, kept unless they clash with SourceGit's look: an "Uncommitted Changes (n)" row at the top; ref badges on the subject, a branch joined with its remote ("master | origin"), tags with a tag icon; the HEAD commit's node hollow; merge commits in dimmer text.
    - From a SourceGit screenshot, the top of the view: one toolbar row. Its buttons, identified from SourceGit's `src/Views/RepositoryToolbar.axaml` (develop at `9071940`, read on the web, matched by order and icon path data, not rendered): left, Open in External Tools, View Logs (the git commands run), Statistics; centre, Fetch, Pull (a dot when behind), Push (a dot when ahead), Stash, Apply Patch, a separator, Create Branch, Git-Flow, Git LFS, Bisect, Custom Actions (a newer build adds Cleanup); right, the current branch's name (a display, with an ahead and behind badge when it has an upstream) and Navigate to HEAD (selects the current branch in the side panel and scrolls the history to HEAD). Under the row, left, the side panel's filter box; right, above the list, a search button and a filter-settings button, then the columns GRAPH & SUBJECT, AUTHOR (with an avatar), SHA, COMMIT TIME (relative, "12 days ago").
    - **The toolbar's scope** (delegated, 2026-10-12: the main session under the owner's standing instruction; the owner may override): active, Fetch, Create Branch, Navigate to HEAD and the current-branch display; shown disabled, Pull, Push, Stash; not shown, Apply Patch, Git-Flow, Git LFS, Bisect, Custom Actions, Cleanup and the whole left group.
11. **Every new history and Git write capability is human-only, off the MCP surface** (owner, 2026-10-12: "esto es para la UI, la IA ya usa el git por su cuenta"), as `git.read` and `git.watch` (ADR-0060 §2's third reason, ADR-0066 §1). Decided; Q1 and Q3 carry it.

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| Commit history with graph, lanes and refs (items 1, 2) | yes | part 1, steps 2–4, 6 |
| Side panel: Local in folders, favourites first, Remotes, Tags, Worktrees, counts, filter (items 4, 7) | yes | part 1, steps 5, 6 |
| Side panel: Submodules section and its update action (items 3, 4); register row on submodules ignored by the status | yes | part 2 (the status keeps `--ignore-submodules=all`) |
| Toolbar (item 10's scope): Navigate to HEAD, the branch display with ahead and behind | yes | part 1, step 6 |
| Toolbar: Fetch, Create Branch | yes | shown disabled in part 1 (step 6); enabled in part 2 |
| Toolbar: Pull, Push, Stash shown disabled (item 6) | yes | part 1, step 6 |
| Expanded commit in place with numstat file tree (item 10) | yes | part 1, steps 7, 8 |
| A commit's file in the Diff tab against its parent (item 9) | yes | part 1, steps 7, 9; Q1 §5, Q2 §4 |
| Branch menu: all eight items shown; Checkout, New branch from, Compare with current enabled (items 7, 8) | yes | shown disabled in part 1 (step 6); the three enabled in part 2 |
| Fetch, checkout, new branch, submodule update (item 6) | yes | part 2; Q3 |
| Submodules as roots, colour stripe, filter by root (item 7) | yes | part 2; Q3 §7 |
| Synchronised branches (item 7) | yes | part 2; Q3 §6 |
| Pull, push, merge, rebase, stash, rename, delete (items 6, 8) | no | a later milestone; shown disabled |
| Android Studio's filter bar (User, Date, Paths, regex) (item 4) | no | replaced by item 4's refinement; server-side filters later |
| `git.*` over MCP (item 11) | no | owner, item 11 |
| Register: `git.*` `log` and `branches` remain; Git actions row's history and bottom Git panel | yes | part 1 (`branches` becomes `refs`, Q1 §1) |
| Register: Git actions row's stage, unstage, discard, commit, a base other than HEAD for the working-tree diff, blame | no | stays open |

## Starting point

`m2n` from `main` at `4c5514b` (API `0.23.0`, ADRs up to 0067). Another session is writing m2m on branch `m2m`; it takes the next ADR number and may take API `0.24.0` first, so this plan names numbers at writing time ("the next free").

- `internal/domain/git/`: `run.go` (the hardened runner, ADR-0066 §2, §3: fixed prefix, scrubbed environment, untrusted driver overrides, own process group, 10 s, 4 MiB, `stdin` supported), `git.go` (`Register`, `Options`, `mapError`, the three `GIT_*` codes), `read.go` (`git.read`, `base` enum `HEAD`, `cat-file --batch` on stdin, `Slim`), `status.go` (`prefix`, porcelain v2, `Head.Oid`), `watch.go` (`git.watch`, `git.changes`), tests with real git.
- Web: `web/src/lib/git/` (`watch.ts` `GitWatch`, created in `start/Project.svelte` for the project tab's whole life; `changes.ts`, `tree.ts`, `base.ts` `HeadBase`, `review.ts`, `diffmode.ts`, `DiffPanel.svelte`, `GitTree.svelte`). `editor/cm/merge.ts` `mountCompare` requires a `CmBuffer` (its side b is always editable); `editor/cm/review.ts` `mountReview` takes `ReviewOptions {buffer: EditorBuffer | null, base, path?, onSave}`, where `null` means a deleted file and a buffer is always editable. `ui/panels.ts` has the bottom `git` panel disabled with milestone "Git history".
- CSP `default-src 'self'` (`adapters/httpapi/httpapi.go`): no remote avatar can load; inline SVG is fine.
- Host: git 2.51.2 (Homebrew) and 2.50.1 (Apple), as m2l recorded.
- Baseline, 2026-10-12 at `4c5514b` plus the earlier requirements draft of this file: `go vet ./...` clean; `go test ./...` ok in every package except `internal/doccheck` (`TestPlansFollowTheTemplate`, `TestPlansIndexMatchesFiles`), which failed only on that draft (no Status value, no Steps, no index row); this rewrite and its index row fix both.

## Acceptance

Part 1's items; part 2's are written with its steps.

- [ ] A1. On an untrusted root no history read runs a signature program, an external or command diff, a textconv, a filter, fsmonitor or a hook. Source: ADR-0066 §2, §3; Q1 §3. Proven by: steps 1, 2, 5, 7.
- [ ] A2. `git.log` pages, joined, equal one walk of the whole history over the tip set the first page returned, children before parents, even when a commit lands between pages, and also when the repository has more refs than the cap. Source: Q1 §2, Q2 §2. Proven by: step 2.
- [ ] A3. Every parent edge of the graph ends at its parent's node, and laying pages out one at a time equals laying out all of them at once. Source: Q2 §1. Proven by: step 3.
- [ ] A4. The bottom Git tab shows GRAPH & SUBJECT, AUTHOR with an avatar, SHA and relative COMMIT TIME; an Uncommitted Changes row; a hollow HEAD node and dimmed merges; more pages on scroll; a commit made outside the UI appears within 2 s. Source: items 1, 2, 10. Proven by: steps 4, 10.
- [ ] A5. The side panel lists Local (folders by prefix, starred favourites first, current marked), Remotes, Tags and Worktrees with counts and a filter, and a click on a ref selects its commit, also after an external fetch moved it. Source: items 4, 7; Q2 §3. Proven by: steps 5, 6.
- [ ] A6. The toolbar shows item 10's scope and the branch menu item 7's eight entries; Navigate to HEAD and the branch display work; every write is disabled and no write capability exists. Source: items 6, 8, 10. Proven by: step 6.
- [ ] A7. A clicked commit expands in place: hashes, parents, author, committer, full date with zone, message, and a file tree whose counts equal `git diff --numstat` against the first parent. Source: items 9, 10; Q1 §5. Proven by: steps 7, 8.
- [ ] A8. A file of the expanded commit opens in the Diff tab, the file at the commit against its first parent, both sides read-only, in Classic and Review. Source: item 9; Q2 §4. Proven by: steps 7, 9.
- [ ] A9. The schema diff is one minor version, `git.log`, `git.refs`, `git.show` and `git.read`'s widened `base`; none is an MCP tool, and an MCP session gets `REQUEST_NOT_ALLOWED_HERE`. Source: item 11; Q1. Proven by: steps 2, 5, 7, 10.
- [ ] A10. The owner confirms the look and the workflow on their own project. Source: items 4, 10. Proven by: step 10.

## Run rules (steps 1, 4, 6, 8, 9, 10)

- **K1.** m2l's K1 with `B=~/Library/Caches/droidyard-dev/m2n`, the root left untrusted. Two more fixtures: `$B/hist`, made by step 1's `gittest.History` (branches in `feature/`, `bugfix/`, `chore/` folders, merges, an octopus, annotated and lightweight tags, a tag on a tree, a bare `origin` with remote-tracking refs and the current branch ahead and behind, a linked worktree, a binary file, a rename); and `$B/big`, `gittest.Synthetic` with 100,000 commits and 40 merging branches. Never the default state root; the owner's project only as a `git clone` into `$B`.
- **K2, K3.** As m2l: no process killed by name, no adb, emulator or Gradle; headless Chrome with its own profile over CDP.

## Steps

Part 1. Part 2 is outlined after these.

### 1. Spike: the history reads' fence and cost; then the ADRs

- Builds:
  - (a) `internal/domain/git/history.go`: Q1 §3's argv builders only (`logArgs`, `refsArgs`, `showArgs`, `worktreeArgs`) and `tips`, the one function that resolves the tip set (Q1 §2); no capability.
  - (b) `internal/domain/git/gittest/`: `History(t, dir)` (K1's `$B/hist` shape) and `Synthetic(t, dir, commits, branches, tags)`, both through `git fast-import` (or `commit-tree` with `update-ref --stdin`); `TestMakeFixture`, skipped unless `-fixture=<dir>`, writes one for the level-4 runs.
  - (c) `history_harden_test.go`, real git, `HOME` and `XDG_CONFIG_HOME` temporary, an untrusted root; each attack a marker script, run through the runner with each builder's argv and with `cat-file --batch` on a commit oid: `log.showSignature=true` with `gpg.program` on a commit carrying a `gpgsig` header; `diff.external`; `diff.<d>.command` and `diff.<d>.textconv` selected by `.gitattributes`; `core.pager` and `pager.log`; a filter driver; `core.fsmonitor`; `notes.displayRef`; `log.mailmap` with a `.mailmap` (the author must stay raw). No marker appears; the raw author is returned.
  - (d) `BenchmarkLogPage` on `Synthetic` 100,000: a page of 500 at skip 0, 50,000 and 99,500, with and without `git commit-graph write`, on both gits; `BenchmarkRefs` on the same repository. The main session also times both on a clone of the owner's project and records its commit and ref counts.
  - (e) The main session writes Q1 and Q2 as ADRs, amended by (c) and (d), Accepted, Delegated, with index rows; Q3 stays for the owner.
- Implements: risk first; Q1 §3 and ADR-0066 §Consequences ("the guard is a list of what git executes on a read").
- Check: every attack passes; the signature one is seen failing with `--no-show-signature` removed, the mailmap one with `--no-use-mailmap` removed, and each other one either fails under its flag's removal or is recorded as not run by git's plumbing (a guard that cannot discriminate, as m2l step 1's lock test). Timings recorded; a page at skip 50,000 over 1 s without a commit-graph changes Q2 §2's paging, and `refs` over 1 s changes Q1 §1's ahead and behind, before step 2 (a Changes entry).
- Schema: no change.
- Needs: real git; no daemon.
- Not here: no capability.
- Rests on: `log`, `for-each-ref`, `diff-tree`, `worktree list` and `cat-file`, with Q1 §3's flags, run nothing of the repository's on an untrusted root, at level 3 (real git, both builds).
- Review: the Opus verifier (security fence), effort `high`.
- [x] Done. Evidence: level 3, real git 2.51.2 (Homebrew) and 2.50.1 (Apple), `go test -count=1 ./internal/domain/git/...` ok on both. `TestHistoryFence`, 14 attacks each with a control that fires: the signature programs (openpgp, x509, ssh) and the replace ref discriminate on their flag, grafts and the replace ref on the runner's `GIT_GRAFT_FILE` and `GIT_NO_REPLACE_OBJECTS`, the UTF-16 output encoding on `--encoding=UTF-8`, fsmonitor on the runner's `core.fsmonitor=false`; mailmap and notes cannot discriminate (raw `%an`/`%ae`, no `%N`; the raw author is asserted); external and command diffs, textconv and filters are not run by the plumbing, the pager not with a pipe for stdout. `replace_test.go`: `git.read` and the status unforged by a replace ref; the graft hint kept off stderr (`TestGraftHintDoesNotReachStderr`, seen failing without `advice.graftFileDeprecated=false`). `TestTips*` (order, peeling, dedupe, exactly 5,000 vs 5,001, unborn, detached), `TestBuildersAgainstARealRepository`, `TestShowFilesFromASubdirectory`, the merge's first-parent files. Timings in ADR-0068 §Context: a 500-commit page 65–108 ms with a commit-graph, 327–415 ms without, at any skip; refs 158–325 ms; the owner's project (29 commits, 3 refs) 16–34 ms; no threshold crossed, Q1 §1 and Q2 §2 unchanged. ADR-0068 and ADR-0069 written, Accepted, Delegated. Verifier: round 1 revise (replace refs), round 2 revise (the graft hint), fixed and rerun by the main session on both gits; `go build`, `go vet`, `gofmt -l`, `go test ./...` clean. See §Changes.

### 2. `git.log`, API one minor up

- Builds: `internal/domain/git/log.go` by Q1 §1, §2, §4, §6: descriptor (query, workspace, `human`, `RequiresGit`); `from` validated in Go (full hex oids, at most 5,000) before any git starts; with no `from`, the tips by step 1's `tips` (Q1 §2), returned as `tips` with `tipsTruncated`; the walk `logArgs` with `--stdin --date-order --skip --max-count`, the tips written to its stdin; records parsed by a fixed field count; at the 4 MiB cap the last partial record is dropped and `more` is true (the next `skip` counts the records returned); text through `strings.ToValidUTF8`; `Slim` drops `commits` once delivered. `Register` adds it; the golden registry; `system.Version` the next minor; `mvp.md`'s git row, `mcp.md`'s not-exposed row, `liveBudget`.
- Implements: Q1 §1, §2, §4, §6; ADR-0020 §Retention; item 11.
- Check:
  - `TestLogFirstPage` (`gittest.History`): tips, every parent present, every child before its parents; a tag on a tree is not among the tips.
  - `TestLogPagesJoin`: pages of 7 joined equal one page of everything; a commit made between pages changes nothing when `tips` are passed back. Seen failing with the tips re-resolved per page.
  - `TestLogTipsCapped`: 5,001 tags on distinct commits give 5,000 `tips`, `tipsTruncated: true`, HEAD and every branch among them; page 2 with those tips as `from` succeeds. Seen failing with the cap removed (page 2 refused).
  - `TestLogTipsOnStdin`: the counting `Starter` sees no oid in argv.
  - `TestLogFromRefused`: a short oid, a ref name, `-x`, 5,001 oids are `REQUEST_INVALID_PARAMS` with zero starts.
  - `TestLogOutputCap`: a page whose subjects pass 4 MiB returns whole records and `more: true`.
  - `TestLogUnborn` (no commits, no error), `TestLogNotARepository`, `TestLogAwkwardText` (bidi controls, invalid UTF-8, a 10 KiB subject: valid JSON).
  - `TestLogSlim`, modelled on `read_test.go`'s retention test.
  - `TestLogRunsNoSignatureProgram` through the capability on an untrusted root.
  - MCP: `REQUEST_NOT_ALLOWED_HERE`; `TestToolListModelFacingBytes` unchanged.
  - Level 5 (K1): `droidyard call git.log` on `$B/hist` equals `git log --date-order --format=%H HEAD --branches --remotes --tags` there.
- Schema: `version` one minor up; a `git.log` block (params `from`, `skip`, `limit`; result `commits [{oid, parents, authorName, authorEmail, commitTime, subject}]`, `tips`, `tipsTruncated`, `more`; its errors; audience `human`). Nothing else.
- Needs: step 1.
- Not here: no server-side search, author, date or path filter (Not in this milestone); no prefix limit (Q1 §2).
- Rests on: no parameter reaches argv but validated integers; oids go on stdin; at level 3.
- Review: the Opus verifier (contract, security), effort `high`.
- [x] Done. Evidence: `internal/domain/git/log.go`, API 0.24.0; the schema diff is `version` and the `git.log` block only. Every Check test exists and passes (`TestLogFirstPage`, `TestLogPagesJoin` seen failing with the tips re-resolved per page, `TestLogTipsCapped` seen failing with the cap removed, `TestLogTipsOnStdin`, `TestLogFromRefused` (16 shapes, zero starts; seen failing by the verifier with the oid check removed), `TestLogFromNotACommit`, `TestLogOutputCap`, `TestLogUnborn`, `TestLogNotARepository`, `TestLogAwkwardText`, `TestLogSlim`, `TestLogRunsNoSignatureProgram` (seen failing by the verifier with `--no-show-signature` removed)); the MCP refusal in `cmd/droidyard`; `TestToolListModelFacingBytes` unchanged. Level 5 (K1, the main session): a scratch daemon at 0.24.0 answered `git.log` on `$B/hist` in pages of 7 (7, 7, 2), joined equal to `git log --date-order --format=%H HEAD --branches --remotes --tags` (16 commits); `from: ["main"]` is `REQUEST_INVALID_PARAMS`; 0 orphans; the owner's adb server untouched. Both gits for the git package; `go build`, `go vet`, `gofmt -l`, `go test ./...` and `pnpm -C web run verify` clean. Verifier: proceed; its contract nit (`skip` bounded) and description nits applied. See §Changes.

### 3. The lanes, pure

- Builds: `web/src/lib/git/lanes.ts`, no Svelte and no `@codemirror`: `Lanes.extend(rows: {oid, parents}[])` returns, per row, the node's column, the lanes passing through, the edges from this row to the next (straight, branch-out, merge-in) and a colour index stable per lane. The rule is Q2 §1's, written from the survey's description and pvigier's article; no code read from vscode-git-graph or BeardGit.
- Implements: Q2 §1.
- Check: Vitest `lanes.test.ts`:
  - linear; a branch and its merge; an octopus; two roots; a parent not yet loaded (its lane runs to the bottom edge);
  - a property test over 2,000 random DAGs of up to 300 commits: every parent edge ends at its parent's node column, and no two lanes share a column in a row;
  - `extend(page1); extend(page2)` equals `extend(all)`, seen failing with a layout that restarts per page;
  - 100,000 rows of `Synthetic`'s shape laid out, the time recorded, not asserted.
- Schema: no change.
- Needs: nothing (pure); may run beside step 2.
- Not here: no rendering; no lane cap (Q2 §1).
- Rests on: —
- Review: the main session's diff review.
- [ ] Done. Evidence:

### 4. The History tab: the first end to end

- Builds: the `svelte-core-bestpractices` and `svelte-code-writer` skills loaded first. Then:
  - `ui/panels.ts`: the bottom `git` panel enabled.
  - `git/history.ts`: a store per project tab; the first page, more pages near the end of the scroll with `tips` and `skip`; reloads by Q2 §3 (here on `GitWatch`'s `head.oid`; step 6 adds the refs' tips), keeping the selected oid and the scroll at the same oid when still loaded, else the top; `Lanes` extended per page.
  - `git/rows.ts`, pure: the visible window of a virtualised table of fixed row height, with room for one taller expanded row (step 8).
  - `git/HistoryPanel.svelte`: columns GRAPH & SUBJECT (one inline `<svg>` per row from the lanes), AUTHOR (an initials avatar, coloured from the email, drawn locally: the CSP admits no remote image), SHA (short), COMMIT TIME (`commitTime`, relative, absolute in the title); the Uncommitted Changes (n) row from `GitWatch`'s entries when n > 0; HEAD's node hollow; merges dimmed. Every text a text node in a cell with `unicode-bidi: isolate`.
- Implements: Q2 §1–§3; Q1 §6; items 1, 2, 10.
- Check:
  - Vitest `history.test.ts` on a fake client: the first call has no `from`; the next passes `tips` and `skip`; a new `head.oid` reloads; a selected oid still loaded keeps its place; the Uncommitted row's count. `rows.test.ts`: the window at the top, the middle and the end, with and without an expanded row.
  - `pnpm -C web run verify`, the autofixer gate.
  - Level 4 (K1, K3): on `$B/hist`, the rows' oids equal step 2's level-5 list; on `$B/big`, the first rows within 1 s of opening the tab, and after scrolling past three pages each row's incoming lane ends meet the previous row's outgoing ones (the SVG paths' end points, read over CDP); an external commit appears within 2 s.
- Schema: no change.
- Needs: steps 2, 3.
- Not here: no ref badges, side panel or toolbar (step 6); no expansion (step 8).
- Rests on: —
- Review: the main session's diff review.
- [ ] Done. Evidence:

### 5. `git.refs`

- Builds: `internal/domain/git/refs.go` by Q1 §1, §2: HEAD (`oid`, `branch`, `detached`, and the current branch's `upstream`, `ahead`, `behind`), local branches (`name`, `oid`, `upstream`), remote-tracking branches (`remote`, `name`, `oid`), tags (`name`, `oid` peeled to its commit; a tag on a tree or blob has none), worktrees (`path`, `branch`, `oid`, `current`), each list at most 5,000 with `truncated`; and `tips`, `tipsTruncated` from step 1's `tips`, the same function `git.log` uses. `Slim` drops the lists once delivered. `mcp.md` row; `liveBudget`.
- Implements: Q1 §1, §2, §4; ADR-0020 §Retention; item 11.
- Check: `TestRefsOfARealRepository` (`gittest.History`: folders, an annotated and a lightweight tag, a tag on a tree, a remote, ahead and behind, a detached HEAD, a linked worktree); `TestRefsTipsEqualLogTips` (a fixture with an annotated tag and over 5,000 refs: `git.refs`' tips equal `git.log`'s first-page tips; seen failing with tags not peeled, or the cap on one side only); `TestRefsAwkwardNames` (Unicode, a deep folder); `TestRefsCaps`; `TestRefsSlim`; step 1's fence through the capability; MCP refused; level 5 (K1): the counts equal `git for-each-ref` on `$B/hist`.
- Schema: a `git.refs` block (no params; the result above; its errors; audience `human`).
- Needs: steps 1, 2 (the shared `tips`).
- Not here: no ahead and behind for other branches (Q1 §1); no submodules (part 2); no stashes (a later milestone with stash).
- Rests on: —
- Review: the Opus verifier (contract), effort default.
- [ ] Done. Evidence:

### 6. The side panel, the ref badges, the toolbar and the branch menu

- Builds: the two Svelte skills first. Then:
  - `git/refs.ts`, pure: the folder tree by `/`, starred favourites first (a star on each branch row, per browser and project, checked on the way in), current marked, counts, a case-insensitive filter over branches, tags and worktrees; badges per oid, a local branch joined with its remote when both point at it ("main | origin"), tags with an icon.
  - `git/history.ts` gains Q2 §3's refs reload (on `head.oid`, the tab shown, the window focused) and reloads the history when the refs' `tips` differ from its own.
  - `git/RefsPanel.svelte` at the panel's left, collapsible sections Local, Remotes, Tags, Worktrees; a click selects the ref's commit in the table, loading at most 5 more pages, else saying "not in the loaded history; scroll to load more".
  - The toolbar row by item 10's scope: Fetch and Create Branch shown disabled until part 2; Pull, Push, Stash shown disabled; the current branch's name with its ahead and behind badge; Navigate to HEAD, which selects the current branch in the side panel and HEAD's row in the table. Above the list, search finds over the loaded rows (subject, author, SHA prefix) with next and previous; filter settings is shown disabled (its function unverified).
  - A context menu per branch with item 7's eight entries, all disabled.
- Implements: items 4, 6, 7 (menu, folders, favourites), 8, 10; Q2 §3.
- Check: Vitest `refs.test.ts` (folders, starred first, counts, filter, joined badges, a tag badge); `history.test.ts` gains: refs whose tips differ reload the history, equal tips do not; a fake-client test that clicking every disabled toolbar and menu entry calls no capability; `verify` and the autofixer gate. Level 4 (K1, K3) on `$B/hist`: section counts equal step 5's; a click on `bugfix/x` selects its commit; after another clone pushes a commit to `$B/hist`'s bare `origin` and `git fetch` runs in `$B/hist` from a terminal, the window regaining focus reloads, and a click on `origin/<branch>` selects the new commit; Navigate to HEAD selects HEAD's row after scrolling away; the branch display shows the fixture's ahead and behind; every write control has `disabled`.
- Schema: no change.
- Needs: steps 4, 5.
- Not here: no Submodules section, no enabled write (part 2); no keyboard shortcuts for fetch, pull or push.
- Rests on: —
- Review: the main session's diff review.
- [ ] Done. Evidence:

### 7. `git.show`, and `git.read` at a commit

- Builds:
  - `internal/domain/git/show.go` by Q1 §1, §5: params `oid` (full hex, validated in Go); result `oid`, `parents`, `author` and `committer` (`name`, `email`, `time` in RFC 3339 with its offset), `message` (at most 64 KiB, `messageTruncated`), `files [{path, origPath?, status, added?, removed?, binary}]` against the first parent (a root commit against the empty tree), workspace-relative, at most 5,000 with `truncated` (also at the 4 MiB cap). One `diff-tree --raw --numstat -M -z --literal-pathspecs -- <prefix>` gives status and counts. `Slim` drops `message` and `files` once delivered.
  - `read.go`: `base` accepts `HEAD` or a full commit oid (ADR-0066 §1's widening); the object name becomes `<oid>:<prefix><path>`; the result's `base` widens the same way.
- Implements: Q1 §1, §3–§5; ADR-0066 §1; ADR-0020 §Retention; item 9.
- Check: `TestShowMergeAgainstFirstParent` (a file the second parent brought is listed; seen failing against the combined diff); `TestShowRootCommit`; `TestShowNumstat` (counts equal `git diff --numstat`; a binary file has none); `TestShowRename`; `TestShowPrefix` (a subdirectory workspace sees only its files, a path with `*` in it matched literally); `TestShowCaps` (message and files); `TestShowUnknownCommit` (`REQUEST_INVALID_PARAMS`); `TestShowSlim`; `TestReadAtCommit` (an older commit's bytes; a path absent there is `exists: false`); `TestReadBaseRefused` (`main`, a short oid, `HEAD~1` refused with zero starts); m2l's `git.read` tests unchanged; the fence through `git.show`; MCP refused.
- Schema: a `git.show` block; `git.read`'s `base` (param and result) from enum `HEAD` to a pattern admitting `HEAD` or a 40- or 64-digit oid. Nothing else.
- Needs: step 1.
- Not here: no diff against another parent (Q1 §5); no line diff from the daemon (ADR-0067 §1).
- Rests on: no caller parameter reaches argv but a validated oid (the prefix is the root's, matched literally), at level 3.
- Review: the Opus verifier (contract, security), effort `high`.
- [ ] Done. Evidence:

### 8. The expanded commit

- Builds: the two Svelte skills first. `git/CommitView.svelte` under the clicked row, across the table's width, rows below moved down (`rows.ts`'s taller row); × closes it; a vertical strip with tree or list (the second button of item 10 shown disabled, unverified). Left: Commit, Parents (a click selects the parent), Author, Date (full, with zone), Committer, a blank line, the message as preformatted text. Right: `git/files.ts`, pure, a folder tree (collapsible, folders compacted as `changes.ts` does) or a flat list, each file with "(+a | -r)" or "binary". A commit with no file under the workspace says so. One `git.show` per expansion, kept while the row stays loaded.
- Implements: item 10; Q1 §5, §6.
- Check: Vitest `files.test.ts` (tree, list, counts text, binary, rename shown with its origin, the empty case); the window with an expanded row (step 4's `rows.test.ts` case, now used); `verify`, the autofixer gate. Level 4 (K1, K3): on a merge of `$B/hist`, the files and counts shown equal `git diff --numstat <commit>^1 <commit>`; the rows below move by the panel's height; × restores them.
- Schema: no change.
- Needs: steps 4, 7.
- Not here: no compare with another commit (part 2's Compare with current is between branches).
- Rests on: —
- Review: the main session's diff review.
- [ ] Done. Evidence:

### 9. A commit's file in the Diff tab

- Builds: the two Svelte skills first. `DiffPanel` gains a second source by Q2 §4: a commit's file, `git.read` at the first parent (at `origPath` for a rename) and at the commit, both read-only; header `<path> · <short> vs <parent short>`. `surface.ts`: `CompareOptions` and `ReviewOptions` each gain a read-only text (`{text, path}`) in place of the buffer, distinct from `ReviewOptions.buffer: null` (a deleted file, unchanged); with it, `merge.ts`'s side b and `review.ts`'s left are read-only views with no `mirror` and no `follow`. Added file: an empty base; deleted: the right pane alone, all red, as 2l; binary or too large: a message. Opening a file from the editor or the Git tree returns the tab to the working-tree source. Counts in step 8 are git's; the Diff tab's rows are CodeMirror's (ADR-0067 §1) and may differ on the same change.
- Implements: item 9; Q2 §4.
- Check: Vitest: `diffsource.test.ts` (the two reads and their bases; a rename reads `origPath` at the parent; a merge reads the first parent; switching back to the working tree); `merge.test.ts` and `cm/review.test.ts` gain the read-only text (no buffer touched; the document is the commit's text; an edit transaction is refused); `verify`, the autofixer gate. Level 4 (K1, K3): from step 8's merge, a file opens in Review with the first-parent change; a keystroke on either side changes nothing; Classic's captions are the two short hashes; the working-tree diff still follows the buffer after returning (m2l A10 rerun once).
- Schema: no change.
- Needs: steps 7, 8.
- Not here: no next or previous change; no per-parent choice.
- Rests on: —
- Review: the main session's diff review.
- [ ] Done. Evidence:

### 10. Level 5 and the owner's check

- Builds: nothing. Under K1–K3, the History tab open in headless Chrome:
  - (a) A script commits twice on `$B/copy` and creates a branch: each commit appears at the top within 2 s with its lane; the new branch appears at the next refs reload (Q2 §3).
  - (b) On an untrusted copy of `$B/hist` given step 1's hostile configuration, opening the tab, expanding a commit and opening its diff create no marker.
  - (c) `droidyard mcp`: no `git_log`, `git_refs`, `git_show`; `tools/call` on each is tool not found.
  - (d) The owner's checklist, in Spanish, on their own project: the table and graph, the side panel, the toolbar, the expansion, the diff, the filter-settings button's expected function. Each item not confirmed becomes a register row.
- Implements: A1, A4, A9, A10 at level 5.
- Check: (a)–(c) observed with their evidence; (d) answered or recorded as not confirmed.
- Schema: no change.
- Needs: steps 6, 9.
- Not here: part 2.
- Rests on: —
- [ ] Done. Evidence:

## Part 2 (outline; its steps are written when Q3 and Q4 are settled)

About nine steps, each later written in full and reviewed: P1, a spike of the writes from the daemon (credentials through the daemon's environment, SSH with no terminal, a cancelled fetch leaving no `ssh`, a checkout over a dirty tree, a partial clone's checkout); P2, the write runner and `git.fetch`, the toolbar's Fetch; P3, `git.checkout` and `git.createBranch`, the toolbar's Create Branch and the menu's Checkout and New branch from; P4, `git.submodules` and the Submodules section; P5, `git.updateSubmodules`; P6, submodules as roots (a `root` parameter on the reads, the mixed log, the stripe, the root filter); P7, synchronised branches; P8, Compare with current (read-only: commits each way and the files between the two tips); P9, level 5 and the owner's check.

## Not in this milestone

- Pull, push, merge, rebase, stash, rename, delete; force or smart checkout; tag creation: a later milestone (items 6, 8).
- SourceGit's Apply Patch, Git-Flow, Git LFS, Bisect, Custom Actions, Cleanup, and its left group (Open in External Tools, View Logs, Statistics); its Ctrl-click and keyboard shortcuts for fetch, pull and push: in none of the owner's requirements (item 10's scope, delegated).
- Server-side search, author, date and path filters, regex (item 4's first layout, replaced): later; part 1's search covers the loaded rows.
- A lane cap or collapsed long edges (BeardGit, IntelliJ): later, if the owner's project needs it (step 10).
- Ahead and behind for branches other than the current one: later (Q1 §1).
- Stashes in the side panel: with stash.
- Signatures (`%G?`), mailmap, notes: not read (Q1 §3).
- Remote avatars (Gravatar): the CSP; initials instead.
- A per-parent or combined diff of a merge: later (Q1 §5).
- Stage, unstage, discard, commit, blame, a working-tree base other than HEAD: the register's Git actions row.
- `git.*` over MCP: item 11.

## Design questions

### Q1. The history reads: capabilities, fence, untrusted text, the merge parent

- Blocks: steps 1, 2, 5, 7 (part 1). Draft for the main session (Delegated), except §1's audience, which is the owner's item 11.
- The documents say: `mvp.md` names `log` and `branches`; ADR-0066 §1 promises that commits widen `base` "and break nothing", §2 the invocation and its 4 MiB and 10 s limits, §6 that paths and text are project content; ADR-0015 §1 does not mark project content; ADR-0020 §Retention keeps terminal results. Nothing on refs, commit details, a merge's parent, the commands a history read runs, or a workspace below the repository's top.
- Draft decision, a new ADR (next free number):
  1. **Three queries**, domain `git`, workspace-scoped, audience `human` (owner, item 11), `RequiresGit`: `git.log` (one page of commits), `git.refs` (HEAD with the current branch's ahead and behind, local and remote-tracking branches, tags, worktrees, and the tip set), `git.show` (one commit's details and files). `git.read`'s `base` widens to a full commit oid. Each drops its bulk once delivered (ADR-0020 §Retention). `mvp.md`'s row becomes `watch`, `read`, `log`, `refs`, `show`.
  2. **Addressing by oid.** Every read takes full hex oids, validated in Go before git starts; no ref name ever reaches argv in a read. The tip set is HEAD and every branch, remote-tracking branch and tag, peeled to commits (a tag on a tree or blob dropped), deduplicated, capped at 5,000: HEAD and branches first, then remote-tracking branches, then tags by newest date; past the cap the rest are dropped and `tipsTruncated` says so. One function builds it for `git.log` (with no `from`) and `git.refs`. A later page passes `tips` back as `from` (at most 5,000, about 215 KB of JSON, inside the 1 MiB request limit) with `skip`, so pages are deterministic while refs move. The tips reach git on stdin (`git log --stdin`). Pages of at most 1,000 commits (default 500). `git.log` walks the whole repository even when the workspace is a subdirectory; `git.show` lists only the workspace's files.
  3. **The fence**: ADR-0066 §2's invocation unchanged, plus on every history read `--no-show-signature --no-notes --no-color --no-ext-diff --no-textconv --no-use-mailmap --literal-pathspecs`, raw author fields (`%an`, `%ae`), plumbing `diff-tree` for files, no format that verifies a signature. Step 1's attacks keep the list.
  4. **Limits and codes**: at the 4 MiB output cap `git.log` returns whole records with `more: true` and `git.show` sets `truncated`; ADR-0066 §4's codes; an unknown or non-commit oid is `REQUEST_INVALID_PARAMS`. No new code.
  5. **A merge commit's files and diff are against its first parent**: what the merge brought into the branch, `git diff <merge>^1 <merge>`; a root commit against the empty tree. `git.show`'s counts are git's.
  6. **Commit text, names, e-mails and ref names are project content** (ADR-0015 §1, ADR-0066 §6), unmarked: only the UI reads them (item 11). The UI renders them as text nodes, each cell bidi-isolated. `git.log` carries the committer time (`commitTime`, the order `--date-order` sorts by); `git.show` carries both.
- Alternatives: a combined diff (`--cc`) for merges: hides what the branch brought, and a hunk format the browser cannot reuse; a parent selector: later. Ref names as `from`: argv injection to guard and moving targets between pages. Tips on argv: `ARG_MAX`. A daemon-held cursor: state per client. `git.log -- <prefix>` with history simplification: rewrites parents, so the graph is no longer the repository's. Ahead and behind for every branch: its cost grows with branches (step 1 times it). Marking commit text untrusted: the agent never receives it.

### Q2. The graph, paging, freshness and the commit diff in the browser

- Blocks: steps 3, 4, 6, 9.
- The documents say: ADR-0067 §1 puts the diff in the browser; §2 has the Diff tab show only the active file against HEAD; §3 and §4 make the buffer side editable in both modes. Nothing on lanes, paging or freshness of refs.
- Draft decision, a new ADR (Delegated), superseding ADR-0067 §Decision in part (items 2–4: the Diff tab's single source and the always-editable buffer side):
  1. **Lanes are laid out in the browser** from each commit's parents, by one newest-first pass: a row's incoming lanes are the previous row's outgoing ones; a commit takes the first lane waiting for it, else the first free column; its first parent inherits that lane; another parent joins a lane already waiting for it, else opens one; lanes waiting for the same commit close where it is drawn. Extended page by page. No lane cap. Drawn as one inline SVG per row in a virtualised table, so text stays selectable and readable by a screen reader.
  2. **Order and paging**: `--date-order` (children before parents, else by committer date), pages by `skip` over fixed `tips` (Q1 §2). Step 1's timings may change this.
  3. **Freshness**: the refs reload when `git.watch`'s `head.oid` changes, when the History tab is shown and when the window regains focus. When the reloaded `tips` differ from those the loaded history started from, the history reloads from its first page, keeping the selection, the scroll position and an open expansion at the same oid if still loaded, else at the top. A ref moved by another git while none of the three happens is seen at the next one.
  4. **The Diff tab has two sources**: the active file against HEAD (ADR-0067 §2, unchanged), and a commit's file against its first parent, both sides read-only, in Classic and Review; Review's left and Classic's right are then the commit's text, with no buffer.
- Alternatives: lanes from the daemon: presentation in the published contract, a schema change for every layout change. Parsing `git log --graph` (Gitea): git's ASCII art, wide, and its state breaks across pages. A library: none in Svelte; the React ones are rejected in the survey; vscode-git-graph's and BeardGit's licences forbid reuse. Canvas (BeardGit): loses selection and needs an accessibility mirror. `--topo-order`: either serves the lane pass; date order interleaves branches by time, and the owner's check at step 10 judges the look. A refs fingerprint published by `git.watch` every tick: a contract change and a `for-each-ref` a second, for a case focus and HEAD already cover mostly.

### Q3. The Git writes (owner's answers needed)

- Blocks: part 2.
- The documents say: ADR-0066 §2, "fetching is off on every root, because the daemon never fetches", 10 s per git, `GIT_ALLOW_PROTOCOL=none`; the register's "Git actions" row deferred; `overview.md` §Concurrency, per-resource locks; ADR-0058, the editor's revision precondition. Nothing on any write.
- Draft decision, a new ADR superseding ADR-0066 §Decision in part (§2's "the daemon never fetches" and its timeout, for the write capabilities only):
  1. **Four commands and one query**, audience `human` (owner, item 11, decided): `git.fetch` (all remotes, or one; long-running, cancellable; no prune, no submodule recursion), `git.checkout` (a local branch; a remote-tracking one creates its local tracking branch), `git.createBranch` (`name`, `from` oid, `checkout`), `git.updateSubmodules` (`submodule update --init --recursive`, all or given paths), and the query `git.submodules` (path, recorded and checked-out oid, state uninitialised, clean, outdated or dirty).
  2. **A trusted root only**; otherwise `WORKSPACE_UNTRUSTED`. A fetch runs the repository's transport and credential configuration (`core.sshCommand`, `remote.<n>.uploadpack`, a credential helper), an update may run `submodule.<n>.update = !cmd`, and a checkout runs smudge filters: on a trusted root that is the user's own git, as `build.*` is the user's own build (ADR-0015 §5).
  3. **Invocation**: ADR-0066 §2's prefix, but network commands use git's own protocol policy; no prompt ever (`GIT_TERMINAL_PROMPT=0`, no terminal), so a credential that needs one fails with `GIT_FAILED` and a nextStep to run the command once in a terminal; 5 minutes for network commands, 60 s for the rest; the group killed on cancel; `--no-recurse-submodules` on checkout. **Hooks stay off** (owner question a).
  4. **One write at a time per repository**, queued FIFO (`overview.md` §Concurrency); reads never wait. A lock held by the agent's own git is `GIT_FAILED` with git's line.
  5. **Names** validated in Go (no leading `-`) and by `git check-ref-format --branch`, passed after `--end-of-options`. A checkout git refuses over local changes is `GIT_FAILED` with git's cause; no force, no stash (owner question b). No new code.
  6. **Synchronised branches**: a `roots` parameter (`.` and submodule paths); applied in order, the repository first, then submodules depth-first, stopping at the first failure; a `completed` result with one outcome per root (ADR-0053 §2's pattern), no rollback (owner question c). The UI's toggle defaults on when every root has the branch.
  7. **Submodules as roots**: the reads take an optional `root`, a submodule path inside the workspace, contained lexically and checked against the gitlinks; git runs there under the workspace's trust. The browser merges each root's pages by commit time with a colour stripe and a root filter (owner question d).
- Owner questions: (a) on a trusted root, should a checkout run the repository's hooks (`post-checkout`), as the git CLI does? Draft: no, as every git the daemon runs. (b) A refused checkout only reports git's message: acceptable until stash exists? (c) Synchronised operations stop at the first failing root with no rollback: acceptable? (d) Mixed roots in one log, or one root at a time first? (e) A checkout rewrites files that may be open with unsaved edits: refuse the checkout while a buffer is dirty, or leave it to the editor's revision precondition (ADR-0058), which refuses the later save? Draft: refuse in the UI with the dirty files named. (f) Fetch needs a trusted root: are your projects trusted, or should Fetch show as refused with "trust this project" on an untrusted one? Draft: shown, refused with the nextStep.
- Alternatives: writes on an untrusted root with the configuration neutralised: a list of transport, credential and update commands too long to keep, and an LFS checkout broken by empty smudge filters. A daemon that shells out to the user's terminal: no process ownership. Rollback of a synchronised checkout: another checkout that can fail too.

### Q4. The split

- Blocks: approval.
- The documents say: `milestone-plan` §What makes a step, about ten steps a sitting; the owner's item 6 chose "reading plus the safe writes" in 2n over "read only, writes in 2o"; `milestone-workflow` merges a milestone's branch at its close.
- Draft decision: milestone 2n stays one milestone, as item 6 says, in two parts of this plan. Part 1 (ten steps above) is approved and built now; part 2's steps are written into this plan once Q3 is answered, reviewed, and built before 2n closes. The branch `m2n` may merge into `main` at the part boundary, since part 1 leaves every write visibly disabled (a departure from `milestone-workflow`'s single merge at the close, for the owner to accept).
- Alternatives: one plan of about nineteen steps; two milestones (2n reading, 2o writes), which item 6 rejected unless the owner reads it only as a deferral question.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| The history commands execute nothing beyond step 1's list on an untrusted root | 2, 5, 7 | step 1 on both gits; a new git that grows one escapes until a test names it (ADR-0066 §Consequences) |
| A `--date-order` page with `skip`, and `refs` with the current branch's ahead and behind, stay fast on large histories with no commit-graph | 2, 4, 5 | step 1 (d); the owner's own counts at step 10 |
| `git log --stdin` skips, rather than fails on, a non-commit; the tips never contain one (peeled and filtered) | 2 | step 2's tag-on-a-tree fixture |
| The graph stays readable at the owner's lane width with no cap | 4 | step 10's check |
| SourceGit's toolbar names come from its source read on the web, not rendered; the filter-settings button's function and the expansion strip's second button are unknown | 6, 8 | the owner at step 10 |
| An initials avatar and relative time are what the owner expects | 4 | step 10's check |
| A ref moved by another git while the window keeps focus and HEAD does not move is seen late | 6 | stated in Q2 §3; the owner at step 10 |
| Part 2: credentials and SSH from the daemon's environment work with no prompt and never hang | part 2 | P1 |
| Linux | all | not run (macOS only) |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | [blocking] B1: the tip set was unbounded while `from` was capped at 1,000; tips on argv near `ARG_MAX` | Q1 §2: tips capped at 5,000 (the `from` cap), ordered, `tipsTruncated`; on stdin; `TestLogTipsCapped`, `TestLogTipsOnStdin` |
| 1 | revise | [blocking] B2: refs moving without HEAD left badges on unloaded commits; the ref click walked the whole history | Q2 §3: refs reload on `head.oid`, tab shown, focus; the history reloads on changed tips; the click loads at most 5 pages; step 6's level-4 external fetch |
| 1 | revise | [should] S1 ADR-0067 §3–§4 also contradicted; S2 no retention slimming; S3 output cap and timeout unstated | Q2 supersedes items 2–4 in part; `Slim` and a test in steps 2, 5, 7; Q1 §4 limits, `TestLogOutputCap`, `TestShowCaps`, `--literal-pathspecs -- <prefix>` |
| 1 | revise | [should] S4 ahead/behind cost; S5 prefix for the log; S6 author time under COMMIT TIME; S7 fields; S8 no favourite control; S9 owner questions on dirty buffers and untrusted Fetch | current branch only, `BenchmarkRefs`; Q1 §2 whole repository, alternative recorded; `commitTime`; every step has the six fields; a star per branch row; Q3 (e), (f) |
| 1 | revise | nits: level-5 tips; `diff.<d>.command`; mailmap; git's counts vs the engine's; scroll on reload; name the commands | all taken (step 2, step 1 (c), Q1 §3, §5, step 9, Q2 §3, step 1's Rests on) |
| 2 | proceed | B1 resolved; B2 resolved; [should] the "tips differ" comparison must use one tip function (annotated tags, the cap) | `tips` shared by `git.log` and `git.refs`; `TestRefsTipsEqualLogTips`; fixtures through `fast-import`; tag-on-a-tree case in step 2 |

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.

- 2026-10-12, step 1 (Delegated): ADR numbers. Q1 and Q2 are ADR-0068 and ADR-0069 on this branch: `internal/doccheck` refuses a gap, and m2m, on its own branch from the same `main`, also writes an ADR-0068. Whichever of the two merges into `main` second renumbers its ADRs at that merge.
- 2026-10-12, step 1 (Delegated): the Check's "the mailmap one seen failing with `--no-use-mailmap` removed" cannot hold: a format of raw `%an` and `%ae` never shows the mailmap, and one without `%N` never shows notes, so both flags are recorded belts; the attack rows still assert the raw author. `showArgs` takes the first parent and uses the two-tree `diff-tree <parent> <commit>`, since `-m --first-parent` listed every parent's differences on a merge (Q1 §5 unchanged in meaning).
- 2026-10-12, step 1, the verifier's two rounds (Delegated): replace refs and `.git/info/grafts` could show another commit's author, message, parents or files under an oid, in the history reads and in m2l's `git.read` and status. The runner's environment gains `GIT_NO_REPLACE_OBJECTS=1` and `GIT_GRAFT_FILE=/dev/null` for every git the daemon runs (only the variable stops grafts), with `-c advice.graftFileDeprecated=false` so the deprecation hint git prints for any graft file never stands in for git's error; every history command also carries the global `--no-replace-objects`; `log` carries `--encoding=UTF-8` (a UTF-16 `i18n.logOutputEncoding` broke the records). ADR-0068 supersedes ADR-0066 §Decision item 2 in part. `gittest.History`'s merges carry their branches' files.
- 2026-10-12, step 2 (Delegated): `git.log`'s `Slim` drops `tips` as well as `commits` (both bulk, ADR-0020 §Retention); `tips` is returned only by a page without `from` (an empty `from` is absent); a page with `from` checks the oids with one `cat-file --batch-check` on stdin, so an unknown oid or one that is not a commit is `REQUEST_INVALID_PARAMS` (ADR-0068 §4); `skip` is capped at 2147483647, git's own integer limit, so a larger one is `REQUEST_INVALID_PARAMS`, not `GIT_FAILED`. A first record past the 4 MiB cap is `GIT_FAILED`, and `git.log` runs no prefix check, so a root inside `.git/` walks the repository: both registered as limits. ADR-0068 §1's list of `git.log`'s fields is corrected to Q1 §6's (no author time, no refs per commit: the refs come from `git.refs`), an edit to an Accepted ADR made on this branch before any merge.
