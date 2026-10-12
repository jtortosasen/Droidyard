# Plan: Milestone 2l — Git: the review diff and the Git tree

- Status: Approved
- Approved: 2026-10-12, Delegated (the main session under the owner's standing instruction to apply the recommended option): the Scope table, the nine steps, Not in this milestone, Unverified, and Q1 and Q2 as drafted. `m2l` fast-forwarded to `main` at `8e7c0c8` at approval.
- Date: 2026-10-12
- Milestone: "2l — Git: the review diff and the Git tree". The owner, 2026-10-12, in a discussion session (the brief that handed it over is not a repository document; this plan replaces it): two diff modes behind a toggle, Classic (`@codemirror/merge`) and Review (the owner's own: the file on disk editable on the left, a magit-style unified rendering on the right, an elastic anchored scroll between them); a Git tree in the left panel with a Full view and an Affected-only view (grouped or flat); working tree against HEAD, the base kept a parameter; read-only; git not offered to agents over MCP; the daemon's git reads never take `.git/index.lock`.
- Phase: `docs/roadmap/mvp.md` §Phase 2, the **Git** milestone after the Definition of done (in part: no branches or commits, see Scope).
- Contract: Q1 (ADR-0066) and Q2 (ADR-0067), drafted below, Delegated under the owner's standing instruction; written at step 1. Also: ADR-0009, ADR-0010, ADR-0015 §1 and §5, ADR-0020 §Retention, ADR-0049, ADR-0055, ADR-0060 §2; `capability-api.md` §Naming and namespaces, §Error model, §Discovery and versioning; `files.md` §Paths and containment, §The watch; `process-lifecycle.md`; plan m2i D3, D7, D8.

**The owner should see:**
- Two new capabilities, `git.watch` and `git.read`, human-only like `file.*`: no MCP tool, no tokens. API `0.23.0`.
- The daemon learns of an agent's commit by running `git status` itself once a second, not by watching `.git/`.
- The diff is computed in the browser, against the editor's buffer, so unsaved edits show in both modes.
- On a root you have not trusted, the repository's own filters and fsmonitor never run.
- The elastic scroll's feel can only be judged by you: step 7 ends with a short checklist, before the rest is built on it.
- On a root you have not trusted, a Git LFS file you touched shows as modified (its filter does not run there).

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| Phase 2 Git: the tree's Git view | yes | Q2 §5; steps 5, 8 |
| Phase 2 Git: a Diff tab | yes | Q2 §1–§4; steps 5–7 |
| Phase 2 Git: branches and commits; the bottom Git panel | no | owner, 2026-10-12: read-only first, each action with its ADR |
| Base = working tree vs HEAD, kept a parameter | yes | Q1 §1; steps 3, 4 |
| `.git` changes (HEAD, index, refs) reach the UI | yes | Q1 §1 (`git.watch` polls `git status`); step 4 |
| Reads that never take `.git/index.lock` | yes | Q1 §2; steps 1, 4, 9 |
| `git.*` over MCP | no | Q1 §1, ADR-0060 §2's third reason |
| Phase 2 acceptance, same-primitive check | yes | the UI's git calls are operation records (ADR-0020); nothing new to prove |
| jj; code intelligence | no | owner; `mvp.md` §Out of scope |

## Starting point

m2k merged into `main` during the review (`8e7c0c8`, API `0.22.0`, ADRs up to 0065, `liveBudget` 26,377). `m2l` has no commits yet, so at approval the main session fast-forwards it to `main` and reruns the baseline there; the brief's "merge `main` once m2k lands" is that fast-forward, and no merge step is left. Read at `e3bb62f`, unchanged by m2k unless said:

- No `git` domain. `capability.RequiresGit` exists; `cmd/droidyard/main.go` `requirements.Missing` answers it with the default "not found".
- `internal/domain/file/watch.go`: the poller lifecycle to copy (subscription first, baseline, last-act `ctx` check, `Watchers.Stop` wired as `workspace.Options.StopPollers`), and `.git` in `excludedDirs`.
- `internal/domain/device/runner.go`: `Starter` (the supervisor's spawner), own process group, output capped as read; its `Run` kills only the pid, for the adb server's sake, which git must not copy (Q1 §2).
- `policy.Policy.Trusted(root)`; `event.Bus.PublishTo` (ring-less, one subscription).
- Web: `panels.ts` has `git-side` (left), `diff` (centre) and `git` (bottom) disabled. `editor/surface.ts` is the editor seam (`cm/` alone imports `@codemirror/*`, `boundary.test.ts`); `cm/merge.ts` `mountCompare` (MergeView; the buffer side mirrors into the buffer, never back, which was safe only because Compare replaced the editor in its host); `@codemirror/merge` 6.12.2 exports `Chunk.build`, `Chunk.updateB` and `DiffConfig` (read in the main tree's `node_modules/@codemirror/merge/dist/index.d.ts`, same lockfile). `Shell.svelte` keeps every pane mounted, so the Editor and Diff tabs coexist on one buffer. `files/watch.ts` `FileWatch.listen` serves a second reader with no second `file.watch`. Activity hides UI queries and streams (`ops/blocks.ts` `hidden`), so git calls add no rows.
- Host: `git` 2.51.2 (Homebrew, first on `PATH`) and Apple's `/usr/bin/git`. git's manual (2.51.2) says `--no-optional-locks` equals `GIT_OPTIONAL_LOCKS=0` and "will prevent `git status` from refreshing the index as a side effect" [doc, not run here: the planner cannot run git]. Step 1 runs it.
- Baseline, 2026-10-12 at `e3bb62f`: `go vet ./...` clean; `go test ./...` ok in all 31 packages with tests; live corpus 26,300 words. At `8e7c0c8`: not run here (the planner cannot check it out); the fast-forward's rerun records it. `web/node_modules` is absent in this worktree: `pnpm -C web install --frozen-lockfile` once.
- What m2k changed that 2l builds on: `cmd/droidyard/main.go` (the `agent` registration), `system.Version` `0.22.0`, the golden registry, `mcp.md`'s not-exposed table, `mvp.md`'s surface table, `liveBudget`, and `web/src/lib/start/Project.svelte` (Activity's `records`), which step 5 also edits.

## Acceptance

- [ ] A1. No git the daemon runs takes `.git/index.lock`: a stat-dirty index is not rewritten, a held lock does not fail the read, and 300 commits by another process beside a watching tab all succeed. Source: owner; Q1 §2. Proven by: steps 1, 4, 9.
- [ ] A2. On an untrusted root no repository-configured filter or fsmonitor runs. Source: ADR-0015 §5; Q1 §2. Proven by: steps 1, 3.
- [ ] A3. `git.read` returns a path's text at HEAD, `exists: false` for a new file, `binary` and `tooLarge` without text, and refuses a path outside the root without starting git. Source: Q1 §1, §3. Proven by: step 3.
- [ ] A4. `git.watch` reports the status against HEAD (modified, added, untracked, deleted, staged renames with `origPath`, conflicted) and publishes a new one within three ticks of an edit, a `git add` or a commit; a workspace close stops it with nothing left running. Source: Q1 §1. Proven by: steps 2, 4.
- [ ] A5. The schema diff is `0.23.0`, `git.read`, `git.watch` and its `git.changes` events; neither is an MCP tool and an MCP session gets `REQUEST_NOT_ALLOWED_HERE`. Source: Q1 §1. Proven by: steps 3, 4.
- [ ] A6. The Git tab shows Tree, Changes grouped and Changes flat, with marks and deleted files, and its expansion never changes the file tree's. Source: owner; Q2 §5. Proven by: steps 5, 8.
- [ ] A7. The Diff tab shows the active file against HEAD in Classic or Review behind a remembered toggle; an unsaved edit shows in both at once. Source: owner; Q2 §1–§3. Proven by: steps 5, 7.
- [ ] A8. Review: the left is the editable buffer with gutter marks and no spacer; the right is dim, green and red rows; a line's top meets its row's top within 1 px at the anchor, crossing a deletion block moves the right pane continuously, the right pane drives the left by the inverse map, and a click on a right row puts the cursor on its line. Source: owner; Q2 §4. Proven by: steps 6, 7.
- [ ] A9. A real `git commit` in the project refreshes the Git tab and the open diff within 2 s (level 5). Source: owner. Proven by: step 9.
- [ ] A10. The Diff tab follows its buffer: an edit made in the Editor tab, or a clean buffer's silent reload, reaches the diff, and a later edit in the diff keeps both. No path loses the buffer's or the disk's text without the user's choice. Source: plan m2i D8 and A5; Q2 §2. Proven by: steps 5, 7.

## Run rules (steps 1, 5, 7, 8, 9)

- **K1.** `B=~/Library/Caches/droidyard-dev/m2l`. The project is `git archive HEAD samples/golden` extracted into `$B/copy`, then `git init -q && git add -A && git commit -qm base` there. `DROIDYARD_STATE_DIR` and `DROIDYARD_CONFIG_DIR` fresh under `/tmp/dyl.*` (the root stays untrusted, so the hardened path is the one exercised); the binary built after `pnpm -C web build`; `verify-daemon` §Launch, §Doctor, §Cleanup. Never the default state root; never `~/Documents/DopamineControl` itself (step 1 times a `git clone` of it into `$B`).
- **K2.** Every daemon this run did not start keeps its pid and start time; nothing is killed by name. No adb, emulator or Gradle is used.
- **K3.** Headless Chrome with its own `--user-data-dir`, driven over CDP; never a capture of the owner's screen.

## Steps

### 1. Spike: the hardened git runner, the lock, the filters, the cost; then ADR-0066 and ADR-0067

- Builds:
  - (a) `internal/domain/git/run.go` (package doc comment: why the daemon polls git, the invocation), per Q1 §2: `run(ctx, root, trusted, stdin, args...)` with the fixed prefix, the scrubbed environment, the untrusted filter override (drivers re-read by `git config --name-only --get-regexp '^filter\.'` on every untrusted call), a `Starter` as `device.Starter` (own process group), a 10 s timeout, stdout capped at 4 MiB as read, and the **whole process group** killed on `ctx` end or timeout.
  - (b) `run_test.go`, real git, each test skipped with a message when `git` is not on `PATH`, and with `HOME` and `XDG_CONFIG_HOME` set to a temporary directory so the host's `~/.gitconfig` (signing, fsmonitor, LFS) cannot change the result:
    - `TestStatusLeavesTheIndexAlone`: a committed file touched to a new mtime (stat-dirty, same bytes). After our status, `.git/index` has the same bytes and mtime and no `index.lock` exists. Seen failing with `--no-optional-locks` and `GIT_OPTIONAL_LOCKS=0` removed (the index is rewritten).
    - `TestStatusBesideAHeldIndexLock`: a hand-made `.git/index.lock`, as a commit in progress holds it. Our status exits 0 with the right entries, and the lock is still there, byte-identical.
    - `TestUntrustedRootRunsNoRepositoryFilter`: `.git/config` defines `filter.dy.clean` and `core.fsmonitor` as scripts that create marker files; `.gitattributes` selects `filter=dy`; a stat-dirty file. Untrusted: no marker, status exits 0 with the right entries. Trusted: the filter marker appears (the user's filters run on a trusted root), the fsmonitor one does not. A second driver with `required = true`, as `git lfs install` writes: untrusted status still exits 0. Seen failing with the override removed, and the `required` case with `filter.<d>.required=false` removed (git dies on a required filter that produced nothing).
    - `TestCancelKillsTheGroup`: a trusted root whose filter sleeps (a stand-in for `git-lfs filter-process`); a cancel mid-status leaves no process of git's group. Seen failing with a pid-only kill.
    - `TestGitEnvironmentIgnored`: `GIT_DIR` and `GIT_INDEX_FILE` in the daemon's environment, pointing at another repository, change nothing.
    - `BenchmarkStatus` on a generated 5,000-file repository.
  - (c) The main session reruns (b) once with `/usr/bin` first on `PATH` (Apple's git) and records its version.
  - (d) The main session times Q1's status command, 20 runs, p50 and p95, on `$B/copy` and on a `git clone` of DopamineControl in `$B`, each twice: fresh, and after `touch`ing every tracked file. With no optional locks the daemon never refreshes the index, so every stat-dirty file is hashed again each second until some other git refreshes it; the second number is that cost.
  - (e) The main session writes ADR-0066 and ADR-0067 from Q1 and Q2, amended by (b)–(d), Accepted, Delegated, with index rows and the back-pointer line in ADR-0015, then replaces the letters in this plan with the numbers.
  - (f) The spike's porcelain v2 outputs (`-z`) are saved as `internal/domain/git/testdata/status/*.z` for step 2.
- Implements: risk first. Q1 §2 and ADR-0015 §5 rest on these facts.
- Check: the five tests pass, and each is seen failing under its mutation. If the empty override does not stop the filter, ADR-0066 takes Q1 §2's refusal branch and step 3 adds its test. Timings recorded; a p50 over 100 ms, fresh or touched, on the clone becomes a register row.
- Schema: no change.
- Needs: `m2l` fast-forwarded to `main` (`8e7c0c8`); real git; no daemon.
- Not here: no descriptor or capability.
- Rests on: git's optional-lock switch keeps `status` from writing the index, at level 3 (real git, both builds).
- Review: the Opus verifier (security fence), effort `high`.
- [x] Done. Evidence: `internal/domain/git/run.go`, `run_test.go`, `harden_test.go`, nine `testdata/status/*.z`; 16 tests pass on Homebrew 2.51.2 and Apple 2.50.1 (`/usr/bin` first on `PATH`). Each test seen failing under its mutation (implementer's report; the main session reran the `GIT_ALLOW_PROTOCOL` one: `untrusted_older_git` fails with `status fetched: core.sshCommand ran`), except `TestStatusBesideAHeldIndexLock`, which git's status passes even without the switches (it guards, it cannot discriminate; step 4's commit stress does). Removing only one lock switch, or only `clean=`, fails nothing: redundant by design. (d): p50 18–43 ms, p95 ≤ 87.5 ms, fresh and touched, both gits, the sample and a DopamineControl clone (108 files); 23 ms/op on 5,000 files; no register row. (e): ADR-0066, ADR-0067. Verifier: round 1 fix first (B1 empty driver name ran, B2 driver past the cap ran, B3 lazy fetch ran `core.sshCommand`: [safety] ×3, B4 [doc]); round 2 fix first (R1 [safety]: `protocol.ssh.allow` overrides `protocol.allow=never` on a pre-2.45 git; [plan] the refusal branch unrecorded; 2 [nit]); R1 and the rest fixed by the main session (`GIT_ALLOW_PROTOCOL=none`, per-guard test cases, §Changes, `files.md` §Trust). Unverified: a real pre-2.45 git; Linux.

### 2. The status: parse and map

- Builds: `internal/domain/git/status.go`. `parseStatus(raw, prefix, max)` turns porcelain v2 `-z` with `--branch` into `head {oid?, branch?}` and entries `{path, mark, origPath?}`, workspace-relative, sorted, at most 5,000 (`truncated`), by Q1 §4's table. `status(ctx, …)` runs `rev-parse --show-prefix`, then Q1's status command. The types carry their JSON tags and descriptions now; step 4 publishes them.
- Implements: Q1 §1, §4.
- Check:
  - `TestMarksFromPorcelain`: every row of Q1 §4, from step 1's fixtures and hand-written lines; seen failing with `AD` mapped to added.
  - `TestPrefix`: a workspace in a subdirectory drops entries outside it; a rename from outside is `added`, one to outside reports its origin `deleted`.
  - `TestAwkwardPaths`: spaces, a newline, Unicode.
  - `TestStatusOfARealRepository` (real git): an unborn HEAD (no `oid`, every file untracked); a staged `git mv` (`renamed`, `origPath`); a deleted file; a linked worktree (`.git` is a file); a non-repository (`GIT_NOT_A_REPOSITORY`); a nested repository, which git reports as an untracked `dir/` (an `untracked` entry whose path has no trailing slash); 5,001 untracked files (`truncated`).
- Schema: no change.
- Needs: step 1.
- Not here: no capability. A plain `mv` is git's own `deleted` plus `untracked` (Unverified row 3).
- Rests on: —
- [x] Done. Evidence: `internal/domain/git/status.go`, `status_test.go`; `statusArgs` has one definition. `TestMarksFromPorcelain` (every Q1 §4 row and ADR-0066 §5's `.R`, from the real fixtures; `T`, `C.`, `CD` hand-written), `TestHeadFromPorcelain`, `TestPrefix`, `TestAwkwardPaths`, `TestStatusCaps`, `TestStatusOfARealRepository` (unborn, staged `git mv`, linked worktree, non-repository, nested repository, subdirectory workspace, 5,001 untracked → 5,000 and `truncated`) pass on Homebrew 2.51.2 and Apple 2.50.1. Mutation: `AD` not special-cased fails `AD must be nothing, got "deleted"` (the `D` rule matches first, so the plan's "mapped to added" shows as deleted). Implementer's decisions, kept (Delegated): `CD` is nothing as `AD`; a status cut at the byte cap drops its last record and is `truncated`; any duplicate path other than deleted plus untracked keeps the first entry. Main session's review: a record with a malformed `XY` panicked the parser; guarded, `TestMalformedRecordIsSkipped` seen failing (index out of range) without the guard. Full `go test ./...` passes.

### 3. `git.read`, the domain's registration, API `0.23.0`

- Builds:
  - `internal/domain/git/git.go` (`Register`, `Options{Starter, Trusted, Bus, Watchers, Tick}`) and `read.go` by Q1 §1, §3. Containment is lexical, by the rules of `files.md` §Paths and containment, and touches no file. The object name `HEAD:<prefix><path>` goes on `cat-file --batch`'s stdin; a path holding a line break or NUL is `REQUEST_INVALID_PARAMS`. Binary is `file.read`'s rule, shared rather than copied.
  - `cmd/droidyard/main.go`: register; `RequiresGit` resolves `exec.LookPath("git")`; pass `pol.Trusted` and the supervisor's spawner.
  - `internal/schema/schema_test.go`: `git.Register` in the golden registry. `system.Version` `0.23.0`.
  - Documents: `mvp.md`'s `git` row becomes `watch`, `read` (human-only; never over MCP), `log`, `branches`; a `git.read` row in `mcp.md`'s not-exposed table (`git.watch` joins it at step 4, when it is published: `checkCapabilityNames` refuses an unpublished name); `liveBudget` raised by what they add, with the reason.
- Implements: Q1 §1, §3, §5; ADR-0060 §2.
- Check:
  - `TestReadBaseText`: HEAD's bytes exactly, also after the worktree copy changed.
  - `TestReadAbsent`: an untracked file and an unborn HEAD give `exists: false`.
  - `TestReadBinaryAndTooLarge`: no `text`.
  - `TestReadOutsideRoot`: `../x`, an absolute path outside, `a/../../x` are `FILE_OUTSIDE_ROOT` with zero starts on a counting `Starter`.
  - `TestReadDirectoryIsWrongType`; `TestReadOtherBaseRefused`.
  - A call from an MCP session is `REQUEST_NOT_ALLOWED_HERE`; `TestToolListModelFacingBytes` is unchanged; no `git` on `PATH` gives `GIT_NOT_FOUND`, and `system.capabilities` reports it unavailable with the reason.
  - An untrusted root whose driver read fails closed (an empty driver name) is `WORKSPACE_UNTRUSTED` (§Changes, step 1).
  - Level 5 (K1): `droidyard call git.read` after an edit returns HEAD's text; `system.health` says `0.23.0`.
- Schema: `version` `0.23.0`, and a `git.read` block (params `path`, `base` enum `HEAD`; result `path`, `base`, `exists`, `size`, `binary`, `tooLarge`, `text`; its errors; audience `human`). Nothing else.
- Needs: steps 1, 2.
- Not here: no `git.status` query, no `git.diff` (Q1 §1).
- Rests on: no path parameter reaches git's argv or leaves the root, at level 3.
- Review: the Opus verifier (contract, security), effort `high`.
- [ ] Done. Evidence:

### 4. `git.watch` and `git.changes`

- Builds: `internal/domain/git/watch.go` by Q1 §1, on `files.md` §A watch's lifetime:
  - the subscription first, then the baseline, and a last-act `ctx` check;
  - a close hook that signals and never waits;
  - one goroutine, ticks never overlapping (`Options.Tick`, default 1 s), trust and the filter drivers read again on every tick (ADR-0015 §2);
  - a `status` event only when the status differs from the last one sent;
  - `stopped {failed}` after a failed tick, `{workspaceClosed}` on a close;
  - the result slimmed after delivery (ADR-0020 §Retention).

  `Watchers.Stop` joins `file`'s in `StopPollers` (`main.go`). `mcp.md`'s not-exposed row gains `git.watch`.
- Implements: Q1 §1, §2; ADR-0010; ADR-0049's pattern.
- Check: at a 20 ms tick, real git:
  - `TestWatchBaselineIsStatus`.
  - `TestWatchPublishesACommit`: an edit gives `modified`; the test's `git commit -am` gives no entries and a new `head.oid`, each within three ticks.
  - `TestWatchPublishesAnIndexChange`: `git add` of an untracked file gives `added`.
  - `TestWatchQuietWhenUnchanged`: ten ticks, no event.
  - `TestWatchStopsOnWorkspaceClose`: `stopped`, `Live() == 0`, no git child left on the counting `Starter`.
  - `TestWatchCancelledDuringBaseline`: nothing outlives the operation.
  - `TestWatchFailedTickStops`.
  - `TestWatchRevokedTrustRunsNoFilter`: a watch started on a trusted root with a marker filter; trust revoked mid-watch; after the next tick no new marker. Seen failing with trust read once at the start.
  - `TestCommitsNeverFailBesideTheWatch`: 200 add-and-commit cycles beside a 5 ms watch, every one exits 0 and none mentions `index.lock`. The count with the switch removed is recorded, not asserted.
  - `go test -race ./internal/domain/git`.
- Schema: a `git.watch` block (command, streaming, audience `human`) and its `events`: `git.changes` types `status` and `stopped`, with payload schemas.
- Needs: steps 2, 3.
- Not here: no repository appearing later is noticed (`GIT_NOT_A_REPOSITORY`; the UI offers Retry).
- Rests on: no git process outlives a stop or a cancel, at level 3 here and level 5 at step 9.
- Review: the Opus verifier (concurrency, process lifetime), effort `high`.
- [ ] Done. Evidence:

### 5. The Git tab's Changes view and the Diff tab in Classic: the first end-to-end

- Builds: first the implementer loads the `svelte-core-bestpractices` and `svelte-code-writer` skills. Then:
  - `web/src/lib/git/watch.ts` `GitWatch`, one per project tab beside `FileWatch` (`start/Project.svelte`), with its lifecycle: restart on reconnect and on `stopped {failed}` after 2 s, end while the page is hidden; `GIT_NOT_A_REPOSITORY` and `GIT_NOT_FOUND` as text with Retry.
  - `git/changes.ts`, pure: grouped rows (folders compacted as `tree.ts` does, numeric order) and flat rows (name, then the directory as greyed text); letters M A U D R C.
  - `GitTree.svelte` in the left `git-side` tab (Changes only; step 8 adds the selector).
  - `DiffPanel.svelte` in the centre `diff` tab, for the Editor's active file: header with path, mark and "vs HEAD"; the base by `git.read` (`origPath` for a rename), read again when `head.oid` or the file's entry changes. Classic is `mountCompare` with two new optional `CompareOptions`: `labels` (`HEAD` / `Working tree`) and the read-only side's `aria-label`, so HEAD's text is not announced as "File contents on disk". A deleted, binary or too-large file, or no repository, is a message.
  - `cm/merge.ts`: the buffer side **follows the buffer** (Q2 §2), by a pure `follow(side, buffer)` exported beside `mirror`:
    - `CmCompare` calls it from a `buffer.onChange` subscription.
    - It computes `minimalChange` over the two documents' `doc.toString()` (document positions, where a line break counts once) and takes the insert from the buffer's document, as `CmBuffer.#replace` does; never `sliceDoc()`, whose string offsets break a CRLF file.
    - A change of line separator, or of read-only, remounts the side instead.
    - Its transaction carries an annotation that the `mirrored` listener skips, and `Transaction.addToHistory.of(false)`.

    Without it, an edit in the Editor tab or a clean buffer's silent reload never reaches the side, and the side's next keystroke, through `mirror`'s drift branch, writes its stale text back into the buffer.
  - A Git row opens its file as the Editor's preview tab and shows the Diff tab (Q2 §2). `panels.ts`: `git-side` and `diff` enabled; the bottom `git` waits for "Git history".
- Implements: Q2 §1–§3, §5.
- Check:
  - Vitest: `changes.test.ts` (compaction, flat directory text, marks, order, deleted rows, the truncated note); `git/watch.test.ts` on a fake client (baseline, replacement, restart after `failed`, no retry loop on not-a-repository, hidden ends it); `panels.test.ts` and `tabs.test.ts` (Diff can be activated); `merge.test.ts`: labels and `aria-label`; and `follow` on `EditorState`s with the existing `sideB` and `edit` helpers (Vitest runs in `node`, with no view; `buffer.load` and `buffer.dispatch` fire `onChange` without one). After `buffer.load(newText)` and a `buffer.dispatch` edit, `follow`, then a keystroke on the side through `edit`: the buffer holds the load, the edit and the keystroke, for an LF and a CRLF buffer. Seen failing without `follow` (the stale side reverts the buffer) and with `sliceDoc` offsets (the CRLF case).
  - Level 4 addition (K3): type in the Editor tab, switch to the Diff tab, type, save: the disk holds both edits; once more on a CRLF file.
  - `pnpm -C web run verify` and the autofixer gate.
  - Level 4 (K1, K3): a file edited in `$B/copy` outside the UI is listed `M` within 2 s; clicking it shows Classic with the change; typing on the right updates the diff before any save.
- Schema: no change.
- Needs: step 4.
- Not here: no Review (step 7), no Tree view (step 8), no Git marks in the normal editor or file tree.
- Rests on: —
- Review: the main session's diff review.
- [ ] Done. Evidence:

### 6. Review mode's maps, pure

- Builds: `web/src/lib/git/review.ts`, with no `@codemirror` import (`boundary.test.ts`). Its input is line hunks `{fromA, toA, fromB, toB}` (0-based, half-open, sorted) and the two line counts. Q2 §4 defines:
  - `rows`: `same`, `add`, `del`, each with its lines;
  - `gutter`: added and modified lines, and deleted markers between lines;
  - `rowOfLine` and `lineOfRow`;
  - `ElasticMap` (`toRight`, `toLeft`) over measured tops, and `alignRight` / `alignLeft` with the padding;
  - `anchorOf(cursor, viewport)`;
  - `Driver`: the pane the user last scrolled drives, and the follower's echo is ignored.
- Implements: Q2 §4.
- Check: Vitest,
  - `rows` for a pure add, a deletion mid-file, at the top and at the end, a modification (red above green), adjacent hunks, an empty base, an empty buffer.
  - `rowOfLine` exact for every line; `lineOfRow` of a red row is its block's line.
  - `toRight` maps each line's top exactly onto its row's top.
  - Across a ten-row deletion block, in 1 px steps, the right offset is continuous and monotone: no step above the block's slope, which is (block + line) / line. Seen failing with a step map that jumps at a line boundary.
  - `toLeft(toRight(y)) == y` within 1e-6.
  - The `Driver` ignores the follower's echo, and a user scroll there switches the driver.
- Schema: no change.
- Needs: nothing built here (pure).
- Not here: no view.
- Rests on: —
- Review: the main session's diff review.
- [ ] Done. Evidence:

### 7. Review mode's view and the toggle

- Builds: the two Svelte skills loaded first, as in step 5. Then:
  - `web/src/lib/editor/cm/review.ts`, reached by `view.ts` `mountReview`, a dynamic import as Compare is; `surface.ts` gains `EditorModule.mountReview` and `ReviewOptions {buffer, base, onSave}`.
  - Left: the buffer side built as Compare's (`buffer.extensions()`, `mirror`, and step 5's following of the buffer), plus `review.ts`'s gutter marks and `scrollPastEnd`.
  - Right: a read-only view whose document is the rows' text. Line classes come from tokens (as `mergeTheme`). Highlighting is per side: `highlightTree` over the buffer's tree for same and added rows, over the base's parse for red rows. A gutter shows the disk line, blank on red rows.
  - Chunks come from `Chunk.build` at mount and `Chunk.updateB` on each edit, coalesced to one frame, with one `DIFF_CONFIG` exported from `merge.ts` and also given to MergeView. Scroll sync applies step 6's maps to CodeMirror's geometry (`lineBlockAt`). A click on a right row reveals its line on the left.
  - `DiffPanel`: the Classic | Review toggle, remembered by Q2 §2. A deleted file shows the right pane alone, all red, with the toggle disabled.
- Implements: Q2 §1, §2, §4.
- Check:
  - Vitest `cm/review.test.ts`: chunk-to-hunk conversion (a last line with no newline included); the right document equals the rows; the class of each row; a red row highlighted from the base's tree; one `DIFF_CONFIG` object in both modes; an edit changes the rows; the left uses step 5's `follow`, whose test covers it. `diffmode.test.ts`: the stored value is checked on the way in.
  - Level 4 (K1, K3, CDP), on a file with a ten-line deletion at line 50 and an addition at 80:
    - (a) with the cursor off-screen (the centre anchor), in 1 px left scroll steps across the deletion: at each step where the centre crosses a line's top, that line's row on the right is within 1 px of it; at every step the right offset is continuous and no step exceeds the block's slope;
    - (b) a wheel on the right moves the left by the inverse;
    - (c) a click on a red row puts the cursor on line 50;
    - (d) typing on the left updates the right within 50 ms;
    - (e) the left DOM holds no `.cm-mergeSpacer`, and its line count is the file's;
    - (f) the toggle survives a reload;
    - (g) no CSP violation in the console.
  - `verify` and the autofixer gate.
  - The owner's check, in Spanish, before step 8: the elastic scroll on a real file of theirs, both anchors, and the toggle. An answer that rejects the feel stops the milestone for a Changes entry; an item not confirmed becomes a register row.
- Schema: no change.
- Needs: steps 5, 6.
- Not here: no next or previous change, no per-hunk action, no word-level marks in Review.
- Rests on: —
- Review: the main session's diff review.
- [ ] Done. Evidence:

### 8. The Git tree's Tree view

- Builds: the two Svelte skills loaded first, as in step 5. Then:
  - `web/src/lib/git/tree.ts`, a model fed by `FileWatch.listen` (no second `file.watch`) and the status. Its rows are the whole project. At first only the ancestors of changed paths are expanded. Its expansion set is its own, in memory. A newly changed path expands its ancestors once. A folder the user collapsed stays collapsed until a new change lands beneath it. Status paths the watch does not list (deleted files, files under `.idea/`) are inserted with their folders.
  - `GitTree.svelte`: the Tree | Changes selector and the Group toggle, remembered by Q2 §5.
- Implements: Q2 §5.
- Check:
  - Vitest `git/tree.test.ts`: only the ancestors of changes are expanded; a deleted file sits under its folder; a change under `.idea/` adds that folder; the two expansion rules above; stored values checked. Expanding in the Git model leaves a `FileTreeModel` built on the same `FileWatch` unchanged; seen failing with one shared expansion set.
  - Level 4 (K1, K3): in the Tree view only the changed folders are open, and toggling them leaves the Editor tree's folders as they were.
  - `verify` and the autofixer gate.
- Schema: no change.
- Needs: step 5.
- Not here: no Git decoration in the Editor's file tree.
- Rests on: —
- Review: the main session's diff review.
- [ ] Done. Evidence:

### 9. Level 5: an external commit, the lock, MCP, and the owner's check

- Builds: nothing. The main session runs under K1–K3, with the Git tab and a Review diff of a changed file open in headless Chrome:
  - (a) A script edits two files, `git add`s one, `git mv`s a third, deletes a fourth and commits. Each state shows in the Git tab within 2 s, an empty list after the commit, and the open diff shows no change (the base read again).
  - (b) 300 edit-add-commit cycles run beside the watching tab: every exit is 0, and `index.lock` appears in no stderr.
  - (c) A sampler runs `ps -A -o pid,ppid,pgid,comm` every 50 ms for 3 s before the close, and must see a git child of the daemon at least once (the probe works), noting each one's pgid. After the tab and the workspace close it runs again for 3 s and must see no process in the daemon's children or in any pgid noted.
  - (d) An MCP stdio session (`droidyard mcp`) lists no `git_*` tool, and `tools/call git_read` is tool not found.
  - (e) The owner's checklist, in Spanish: the three tree views, and a commit from their own agent. Each item not confirmed becomes a register row.
- Implements: A1, A9 at level 5.
- Check: (a)–(d) observed with their evidence; (e) answered or recorded as not confirmed.
- Schema: no change.
- Needs: steps 4, 7, 8.
- Not here: a real agent driving git; any `git` binary works the same, and the owner's (e) covers it.
- Rests on: the lock fact of step 1, at level 5.
- [ ] Done. Evidence:

## Not in this milestone

- Stage, unstage, discard, commit, branches, history, the bottom Git panel: the owner (read-only first), each with its ADR.
- A base other than HEAD: later; `base` is the parameter (Q1 §1).
- `git.*` for agents over MCP: not planned (Q1 §1, ADR-0060 §2).
- A `git.status` query for the CLI, a daemon `git.diff`: when a consumer needs one (Q1 §1, Q2 §1).
- Git marks in the normal editor or the Editor's file tree, blame, next or previous change, per-hunk actions: later.
- Submodules (`--ignore-submodules=all`), an unstaged rename (git reports it as deleted and untracked), a repository created after the watch started: limits, register rows at the close.
- jj, code intelligence: the owner; `mvp.md` §Out of scope.

## Design questions

Both drafted and Delegated under the owner's standing instruction (apply the recommended option); the ADR texts come from them at step 1.

### Q1. The daemon's git: what it serves, how it learns of a commit, and its fence

- Blocks: steps 1–4
- The documents say: `mvp.md` plans `git.status`, `diff`, `log` and `branches`, "a thin wrapper over the `git` CLI", the value "for the human UI". ADR-0015 §5: `git.*` works on an untrusted root with `core.fsmonitor` and `core.hooksPath` neutralised. ADR-0049 and `files.md` exclude `.git/` from the walk. Nothing says how the UI learns of a commit, or anything about repository filters.
- Draft decision, ADR-0066, superseding ADR-0015 §Decision in part (item 5's git bullet):
  1. **Two capabilities**, domain `git`, workspace-scoped, audience `human` (ADR-0060 §2's third reason: every agent has git), API `0.23.0`, both answering `GIT_NOT_FOUND` when git is missing (`RequiresGit`):
     - `git.watch`: a command, streaming, as `file.watch` (ADR-0049, ADR-0010).
       - Params: `base` (enum `HEAD`; absent means HEAD).
       - Result: `subscriptionId`, `base`, `head {oid?, branch?}`, `entries [{path, mark, origPath?}]` (at most 5,000), `truncated`; entries slimmed after delivery.
       - It runs Q1 §2's status once per tick (1 s, never overlapping) and publishes on `git.changes`: `status` (the whole status, only when it differs from the last one sent) and `stopped {reason: workspaceClosed | failed}`. The topic is ring-less, offered to its own subscription only, and takes no `since`, as `file.changes`.
       - The status is git's own, so HEAD, the index, refs, packed refs and a `.git` file (linked worktrees) are all covered, and `.git/` stays out of `file.watch`.
     - `git.read`: a query, stateless.
       - Params: `path`, `base`.
       - Result: `path`, `base`, `exists`, `size`, `binary`, `tooLarge` (over 512 KiB, the editor's save limit), `text` (absent unless `exists` and neither flag is set).
     - `base` accepts only `HEAD` now, so accepting commits and branches later widens an enum and breaks nothing (`capability-api.md` §Discovery and versioning).
     - No `git.status` query and no `git.diff`: the watch carries the status, and the diff is the browser's (Q2 §1). `mvp.md`'s row becomes `watch`, `read`, `log`, `branches`.
  2. **Every git the daemon runs**:
     - is `git --no-optional-locks -c core.fsmonitor=false -c core.hooksPath=/dev/null …`, on all roots: fsmonitor `true` would start a `git fsmonitor--daemon` droidyard does not own (`process-lifecycle.md`);
     - gets the daemon's environment without `GIT_*`, plus `GIT_OPTIONAL_LOCKS=0`, `GIT_TERMINAL_PROMPT=0` and `LC_ALL=C`;
     - is started through the supervisor's spawner in its own process group, and the whole group is killed on cancel or after 10 s (a filter's child, such as `git-lfs filter-process`, must not outlive it); its stdout is capped at 4 MiB as read.

     On a root that `policy.Trusted` says is not trusted, read before every invocation (each `git.read`, each tick: ADR-0015 §2), every configured filter driver, in any scope, is overridden empty and not required (`-c filter.<d>.clean= -c filter.<d>.smudge= -c filter.<d>.process= -c filter.<d>.required=false`; `required` because git dies on a required filter that produced nothing, and `git lfs install` sets it globally). So on an untrusted root a stat-dirty LFS file shows as modified. Overriding only drivers with a repository-scope key would keep the user's LFS working there, at the price of trusting `--show-scope` and include semantics; rejected for this milestone. If step 1 shows the override does not stop a filter, the branch is instead: `git.watch` and `git.read` refuse an untrusted root that configures any driver, with `WORKSPACE_UNTRUSTED`, and ADR-0066 then also supersedes ADR-0015 §5's restricted-mode bullet ("`git.*` … work") in part and corrects `files.md` §Trust. The commands are `rev-parse --show-prefix`; `status --porcelain=v2 -z --branch --no-ahead-behind --untracked-files=all --ignore-submodules=all --find-renames -- .` (paths from the repository's top, the workspace's prefix stripped); and `cat-file --batch`, which runs no filter. No parameter reaches argv.
  3. **Codes**:
     - `GIT_NOT_FOUND`;
     - `GIT_NOT_A_REPOSITORY` (the root is in no work tree);
     - `GIT_FAILED` (any other non-zero exit; `cause` is git's first stderr line);
     - `FILE_OUTSIDE_ROOT` for a path outside the root, since containment is the file domain's rule, applied lexically;
     - `FILE_WRONG_TYPE` when HEAD holds a directory or a submodule there;
     - `REQUEST_INVALID_PARAMS` for another base or a line break in a path.
  4. **Marks against HEAD** (enum: `modified`, `added`, `untracked`, `deleted`, `renamed`, `conflicted`), from porcelain v2's XY:
     - `?` is untracked; `u` is conflicted.
     - `R.` and `RM` are renamed, with `origPath`; `RD` is the origin deleted.
     - `A.`, `AM`, `.A` (intent to add) and a `C` copy are added; `AD` is nothing.
     - `D.`, `.D` and `MD` are deleted; a `D.` with an untracked file at the same path is modified.
     - Any other `M` or `T` is modified.
     - A nested repository, which git lists as an untracked `dir/`, is an `untracked` entry without the slash.
  5. **No untrusted mark**: paths and text are project content (ADR-0015 §1). The UI renders them as text nodes.
- Consequences: one `git status` per second per open project tab, as `file.watch`'s walk, and more while files are stat-dirty, since the daemon never refreshes the index (register row on its cost); measured at step 1. On an untrusted root a stat-dirty LFS file shows as modified. A `.git/config` written between the driver read and the status is a window, a stated limit.
- Alternatives:
  - **`.git/` in `file.watch`**: the object store churns into the tree's stream, and a linked worktree's `.git` is a file.
  - **Stat HEAD, the index and refs, then the UI calls `git.status`**: an operation per refresh, a layout that varies (packed refs, worktrees), and working-tree edits still need a second signal.
  - **The UI polls a `git.status` query**: one recorded operation a second.
  - **An OS watcher**: m1j D7.
  - **go-git**: a new dependency whose status ignores filters and index extensions; `mvp.md` says the git CLI.
  - **A daemon `git.diff`**: Q2 §1.

### Q2. The diff in the browser, its two modes, and the Git tree

- Blocks: steps 5–8
- The documents say: ADR-0009, CodeMirror with `@codemirror/merge` "adequate" for the Git diff, and a dedicated diff component as the escape hatch; m2i D8, Compare mirrors the buffer. Nothing on Review, the toggle or the Git tree.
- Draft decision, ADR-0067:
  1. **The browser computes the diff**: HEAD's text (`git.read`) against the editor's buffer, by `@codemirror/merge`'s `Chunk.build` and `Chunk.updateB` with one `DIFF_CONFIG`, in both modes. So unsaved edits show, there is one engine, and there is no new dependency. ADR-0009's escape hatch is not taken.
  2. **The Diff tab** (centre) shows the Editor's active file against HEAD; opening a file from the Git tree opens it as the Editor's preview tab and shows the Diff tab, so a file has one buffer. The Editor and Diff tabs stay mounted together, so the diff's buffer side **follows the buffer**: a change it did not make (an edit in the Editor tab, a clean buffer's silent reload) replaces its text by the smallest change, and only then can its own edits mirror back (m2i D8's guarantee kept). A segmented **Classic | Review** in its header, remembered per browser (`droidyard.ui.diffMode`, checked on the way in), default Review. A deleted file: Review's right pane alone, all red. Binary, too large or no repository: a message.
  3. **Classic**: MergeView, HEAD read-only, the buffer editable (m2i D8's mechanism), labelled `HEAD` and `Working tree`.
  4. **Review**:
     - Left: the buffer, editable, no spacer, with gutter marks for added and modified lines and a deletion marker between lines, and scroll past the end.
     - Right: read-only rows. Unchanged rows are dimmed; added and modified ones green; deleted ones red, above their hunk's green. Each side is highlighted from its own parse. A gutter shows the disk line, blank on red rows.
     - Recomputed on each edit, coalesced to a frame.
     - **Elastic anchored scroll**:
       - The anchor is the cursor line while it is visible, else the viewport's centre.
       - A monotone, piecewise-linear map sends each left line's span onto its row plus the red rows after it, and the right pane is placed so the anchor's point maps onto the same height. A line's top therefore meets its row's top exactly (the cursor anchor is exact); inside the line before a deletion block, the right pane slides through the block instead of jumping.
       - The right pane is padded top and bottom by its height.
       - Two-way: the pane the user last scrolled drives, and the other follows by the inverse map. A switch of anchor eases over 120 ms.
       - A click on a right row puts the cursor on its line; for a red row, the line where its block was deleted.
  5. **The Git tree** (left panel, the `Git` side of the Editor | Git switch), with its own expansion state in memory:
     - **Tree**: the whole project from the root `file.watch`, plus status paths it does not list, with only the folders holding changes expanded.
     - **Changes**, grouped by folder, or flat with each file's directory greyed. The view and the grouping are remembered per browser and project.
     - Letters M, A, U, D, R, C; a deleted file struck through. A `git.watch` per project tab, under `FileWatch`'s lifecycle.
- Alternatives:
  - **The daemon diffs (`git diff`)**: no unsaved edits, two engines, and a hunk contract to publish.
  - **Monaco's diff editor**: ADR-0009.
  - **`unifiedMergeView` in the editor**: deleted chunks become widgets inside the file, the gaps the owner refused.
  - **Classic only**: the owner asked for Review.
  - **A Review mode inside the Editor tab**: the Diff tab already exists in the registry.
  - **A buffer of the Diff tab's own**: two buffers for one file.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| `--no-optional-locks` keeps `status` from writing the index, in Homebrew's and Apple's git | 1, 4, 9 | step 1 (b), (c) |
| An empty driver override stops a repository filter | 1, 3 | step 1; otherwise Q1 §2's refusal branch |
| A plain `mv` shows as deleted plus untracked, not renamed | 2 | git's own rule; a register row if the owner wants detection |
| `git status` once a second stays cheap on the owner's project, fresh and with every tracked file stat-dirty | 4, 5 | step 1 (d) timings |
| A `required` driver overridden with `required=false` lets status finish (git's `convert.c`, read, not run) | 1, 4 | step 1 (b), both builds |
| A `.git/config` changed between the driver read and the status | 4 | not settled: a stated limit |
| The elastic scroll feels right; Safari, Firefox and touch | 7 | step 7's owner check; not run otherwise |
| Per-side highlighting of a buffer the parser has not finished | 7 | step 7's level 4 on the sample; a large file stays unproven |
| Linux | all | not run (macOS only) |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | [blocking] B1: the Diff tab's buffer side never follows the buffer; mounted beside the Editor tab, a stale side reverts it through `mirror`'s drift branch (m2i A5) | Q2 §2 "follows the buffer"; step 5 builds it in `merge.ts` with a Vitest seen failing without it; step 7 reuses it; A10 |
| 1 | revise | [blocking] B2: step 3 lists unpublished `git.watch` in `mcp.md`, which `checkCapabilityNames` refuses | step 3 adds `git.read` only; step 4 adds `git.watch` |
| 1 | revise | [blocking] B3: an empty override of a `required` driver (git-lfs) makes status die | Q1 §2 adds `required=false`, LFS consequence recorded; step 1's test gains a `required` case seen failing without it |
| 1 | revise | [should] m2k merged into `main` (`8e7c0c8`): collisions and the merge step out of date; m2k edited `Project.svelte` | Starting point rewritten: fast-forward at approval and rebaseline; ADRs numbered 0066, 0067 at step 1; step 10 dropped |
| 1 | revise | [should] trust read once per watch; [should] pid-only kill; [should] 9(c) cannot fail; [should] timing misses stat-dirty; [should] 7(a) conflicting assertions; [should] the owner judges the scroll last | per tick (`TestWatchRevokedTrustRunsNoFilter`); group kill (`TestCancelKillsTheGroup`); 9(c) a 50 ms sampler that must first see git; 1(d) timed after `touch`; 7(a) exact at line tops only; the scroll check moved to step 7 |
| 2 | revise | B1 not resolved: the described Vitest cannot run (`environment: 'node'`, no view), and a follower over `sliceDoc()` corrupts CRLF files; a separator change on reload does too | step 5: a pure `follow` over `doc.toString()` positions, an annotation the `mirrored` listener skips, a remount on a separator or read-only change; tested on `EditorState`s, LF and CRLF, seen failing without `follow` and with `sliceDoc`; a level-4 two-tab save; step 7 reuses it. No third round (`milestone-plan` §Writing one, item 7) |
| 2 | — | B2, B3 resolved | — |
| 1 | revise | nits: A4 ticks; nested repository; host `~/.gitconfig` in tests; refusal branch's other supersessions; Classic's `disk` and `aria-label` | all taken (A4 three ticks; Q1 §4 and step 2; `HOME`/`XDG_CONFIG_HOME`; Q1 §2; step 5) |

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.

- 2026-10-12, step 1 (Delegated): Q1 §4 had no rule for `.R` (an intent-to-add file equal to a deleted tracked one, the `intent-rename` fixture): it is `renamed` with `origPath`, as `R.`; ADR-0066 §5. Step 2's `TestMarksFromPorcelain` covers it. The Unverified row on a plain `mv` is settled: deleted plus untracked (the `rename` fixture).
- 2026-10-12, step 1, the verifier's two rounds (Delegated): Q1 §2's invocation gains `GIT_NO_LAZY_FETCH=1`, `GIT_ALLOW_PROTOCOL=none` and `-c protocol.allow=never` on every root (a partial clone's lazy fetch ran the repository's `core.sshCommand`; `protocol.allow` alone is overridden by a repository's `protocol.ssh.allow`). The driver read fails closed (`ErrUnsafeDriver`: an empty or unwritable driver name, a key without `filter.`, a list past the cap). Q1 §2's refusal branch is thereby taken in part: **step 3 maps `ErrUnsafeDriver` to `WORKSPACE_UNTRUSTED`** and tests it, and `git.watch` stops `failed` on it (step 4). ADR-0066 supersedes ADR-0015 §5's restricted-mode bullet in part; `files.md` §Trust says so. `diff.<d>.textconv` does not run in `status --find-renames` (tested, no override).
