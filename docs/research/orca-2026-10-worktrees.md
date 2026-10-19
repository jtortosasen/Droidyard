# Orca deep-dive: worktrees, review, source control, awake (for a Droidyard design note)

Source: Orca at commit f48123e8 (2026-10-18), clone at `/private/tmp/claude-501/-Users-jorgetortosa-Documents-AioAndroidDev/3e86660f-f178-48b7-86aa-f98931091805/scratchpad/orca-src` (abbreviated `$O` below). Paths are relative to `$O`. Docs are under `docs/site/content/docs/` (abbreviated `D/`). Nothing was run; read only.

Labels: **[read]** = I read it in code/docs and cite the line. **[inferred]** = my deduction. **[web]** = read on code.claude.com. A docs claim that the code does not back is flagged **DOC/CODE MISMATCH**.

Scope note: Orca is an Electron desktop app that owns worktrees AND the agent terminals (it launches agents). Droidyard does neither (ADR-0082 §2, ADR-0004). Almost every Orca feature below therefore needs translating, not copying.

---

## 0. Does Claude Code read `.worktreeinclude`? YES [web]

Page: https://code.claude.com/docs/en/worktrees, section "Copy gitignored files into worktrees". Verbatim points:
- "To copy them automatically when Claude creates a worktree, add a `.worktreeinclude` file to your project root."
- "The file uses `.gitignore` syntax. Only files that match a pattern and are also gitignored are copied, so tracked files are never duplicated."
- Applies to "every worktree Claude Code creates with git: `--worktree` worktrees, subagent worktrees, and parallel sessions in the desktop app."
- With a `WorktreeCreate` hook (non-git VCS or custom creation) `.worktreeinclude` is NOT processed; the hook must copy files itself.
- A `**/` pattern into a wholly-ignored directory only reaches files if the directory itself matches or the first name after `**/` is in the dir path (v2.1.239 rule). Example given: `**/.claude/skills/*.md`.
- Example file: `.env`, `.env.local`, `config/secrets.json`.

Also from the same page (useful for Droidyard, since its agents make their own worktrees):
- Default location `.claude/worktrees/<name>/`, branch `worktree-<name>`; `claude --worktree <name>`; PR form `claude --worktree "#1234"` (fetches `pull/<n>/head` or `merge-requests/<n>/head` from `origin`, dir `.claude/worktrees/pr-<n>`).
- Base: `worktree.baseRef` `"fresh"` (default: remote default branch; fetches `origin/HEAD` if not fetched in 24h, 5 s cap, falls back to cached/local HEAD) or `"head"`.
- Cleanup on exit: clean unnamed worktree auto-removed (with branch); worktree with work prompts keep/remove; unverifiable state prompts. `-p` runs never clean up and leave a `git worktree lock`.
- Periodic sweep removes subagent/background worktrees older than `cleanupPeriodDays` unless they hold changed/untracked files, unpushed commits, dirty submodules, are not marked as created by Claude Code (a marker is written into git metadata), or are locked. Claude Code holds `git worktree lock` while an agent runs; sweep releases locks of dead sessions, never user-set locks.
- Isolation enforcement: blocks Edit/Write/NotebookEdit into main checkout, Bash cwd in main checkout, `git -C`/`--git-dir`/`GIT_DIR`/`GIT_WORK_TREE` redirects. Not tracked: `cp` or shell redirects into main.
- Skips repo-local git filter drivers (LFS pointer files) when creating a worktree.
- Implication for Droidyard [inferred]: because `.worktreeinclude` is a Claude Code convention (gitignore syntax, cross-tool; Orca's source cites "Cross-session convention (see issue #7549)", `src/main/git/worktree-include-file.ts:7-8`), Droidyard can treat it as READ-ONLY data: show which gitignored files an agent's worktree will/did receive, never copy anything itself.

Important divergence: Claude Code = gitignore syntax with globs. Orca = literal paths only (section 1.1). A `.worktreeinclude` written for Claude Code with globs is mostly skipped by Orca.

---

## 1. Worktree preparation

### 1.1 `.worktreeinclude` in Orca

Docs: `D/model/orca-yaml.mdx:117-142`, `D/model/worktrees.mdx` (the "Shared directories & gitignored files" section, ~lines 38-66 of that file).
Code: `src/main/git/worktree-include-file.ts` (146 lines).

Syntax [read]:
- One literal path per line, anchored at repo root; blank lines and lines starting `#` ignored; inline comments NOT stripped (`worktree-include-file.ts:28-44`, doc `orca-yaml.mdx:138`).
- Normalised: `\` -> `/`, leading `./` and trailing `/` stripped, de-duplicated (`:36`).
- Skipped with `console.warn`: entries starting `!` or containing `*` or `?` (`:46-48`, `:98-104`). Globs and negation unsupported "yet" (`:14-16`).
- Unsafe paths skipped: absolute, any `..` segment, empty segment, first segment `.git` (`:50-56`).
- File must be a regular file (lstat) <= 256 KiB (`:17`, `:58-69`); otherwise treated as absent. At most 1,000 valid candidates (`:19`, `:92-97`).

What is copied [read]:
- Only entries that exist in the PRIMARY checkout (lstat, concurrency 8, `:115-136`) AND are gitignored per `checkIgnoredPaths` (`:138-141`). Rationale quoted: "never duplicate tracked files or surface unignored ones as spurious worktree diffs". Output sorted.
- Never throws: any failure resolves to `[]` so creation is never blocked (`:78-79`, `:142-145`).
- Config source: the primary checkout's `.worktreeinclude` and filesystem, not the new worktree's (`orca-yaml.mdx:65-73`). A feature branch edit of the file does not take effect until the primary has it.

When [read]: during worktree creation, "materialization" step after `git worktree add`: `src/main/runtime/runtime-local-worktree-materialization.ts` imports `resolveWorktreeIncludePaths`, `createWorktreeCopiedPaths`, `createWorktreeLinkedPaths`, `createWorktreeSharedPaths`. Order per docs: Settings shared paths, then YAML shared dirs, then include copies; existing destinations preserved; a path already shared is not re-copied (`orca-yaml.mdx:119`).

How copied [read]:
- Mode `'copy'` in `src/main/ipc/worktree-symlinks.ts:31-36`: APFS clone when possible, else real copy; "per-worktree copies by cross-tool convention — edits must not leak back". Never falls back to a symlink (`orca-yaml.mdx:125`).
- Real copy = `fs.cp(source, target, {recursive:true, force:false, errorOnExist:false})` (`worktree-symlinks.ts:83-88`): does not clobber a racing file.
- APFS clone = `/bin/cp -c <src> <tmp>` for files and `/bin/cp -n -c -R <src>/. <target>` for directories (`src/main/ipc/worktree-apfs-clone.ts:153,191`; the file has two near-duplicate code paths at lines ~84/122 and ~153/191). The volume is probed first with `/bin/df -P` and `/usr/sbin/diskutil info -plist`, each 5 s timeout, cached per `stat().dev` (`:14-15, :56-70, :32-36`). Reason quoted: "cp's non-APFS full-copy fallback cannot surprise users" (`:142-143`).

Symlinks [read]: "Copying a top-level source symlink uses its target's contents. Nested symlinks remain links, so editing through one can still affect its referent" (`orca-yaml.mdx:142`).

Size limits [read] (`src/main/ipc/worktree-include-copy-budget.ts:18-21`): 2 GiB total bytes and 50,000 filesystem entries per new worktree, cumulative over all entries, measured BEFORE copying; sizing walk gets 5x headroom (`:26`). APFS clones do not consume bytes but do consume entries (`:47-50`, doc `orca-yaml.mdx:140`). Over-budget entry is skipped and creation returns a warning string (`formatWorktreeIncludeCopyWarning`, used in materialization file); entries abandoned mid-copy are flagged `mayBePartial` (`:41-44`). They are admission limits, not quotas. Stated reason: a repo listing `node_modules` froze creation for minutes (`:12-17`).

Claude Code comparison: no documented size limit, glob support, copies at create time for every git-created worktree [web].

### 1.2 `worktree.sharedDirectories` and Settings "Worktree Shared Paths"

Docs: `D/model/orca-yaml.mdx:117-127`; `D/model/worktrees.mdx` shared section.
Code: `src/main/ipc/worktree-symlinks.ts`, `src/main/git/worktree-shared-directories.ts` (imported by materialization; not read in full).

- `orca.yaml` `worktree.sharedDirectories`: list of repo-relative **gitignored directories** that exist in the primary checkout; ALWAYS a link, "including on APFS" (so one `node_modules` serves all worktrees); Windows tries a directory junction then symlink (`worktree-symlinks.ts:38-55`, mode `'share'` comment `:34-35`). Files, missing dirs, non-ignored dirs are skipped; missing dirs are skipped silently with no warning (`orca-yaml.mdx:127,149`). Max first 100 entries (`orca-yaml.mdx:58`). Path rules: backslash normalised, absolute/drive/empty/`.`/`..`/`.git` segments rejected (`:127`).
- Settings -> Repository -> "Worktree Shared Paths" (per user, not in repo): files or directories; APFS clone-copy on macOS if available (independent contents) otherwise link (shared edits) (`orca-yaml.mdx:123`, mode `'link'`). YAML paths ADD to the Settings list, never replace.
- Risks [read/inferred]:
  - Link means edits in one worktree change the primary: a package-manager install from a worktree mutates the shared `node_modules` for all [inferred].
  - Git sees a symlink as untracked, which blocks `git worktree remove`. Orca therefore unlinks the known links before delete and passes `ignoredUntrackedPaths` to its clean check (`src/main/ipc/worktree-symlinks.ts:353-405` doc comment "Why: `git worktree remove` refuses..."; `runtime-registered-local-worktree-removal.ts:72-86`). Only removes entries that ARE symlinks, leaves real files/dirs (`worktree-symlinks.ts:363-384`).
  - With `--force` removal, the linked-path list ignoring is skipped (`...:73-75`, `ignoredLinkedPaths = args.force ? [] : ...`) but links are still removed (`:97-99`) [read].
  - Windows: Claude Code docs note that removing a worktree deletes only a junction/symlink, not its target [web]; Orca equivalent only unlinks (above).

### 1.3 `orca.yaml` `scripts.setup` / `scripts.archive`

Schema/limits: `D/model/orca-yaml.mdx:32-60`, parser `src/shared/orca-yaml.ts:198-225`.

Duplicate-key refusal [read]: `parseDocument(content, {uniqueKeys: true, ...})`; any `document.errors` (which includes duplicate keys), failed conversion limit, non-mapping root, alias budget (`maxAliasCount` 100), or oversize returns `null` = no configuration at all (`orca-yaml.ts:207-224`; doc `orca-yaml.mdx:50`: "Orca does not keep the last duplicate value... A parser failure can disable setup, tabs, and shared directories together" `:146`). Limits: whole file 256 KiB (bytes AND UTF-16 units), each string 64 KiB (field dropped), `defaultTabs` 256 entries (whole list dropped), `sharedDirectories` first 100. Invalid fields skipped individually; unknown keys ignored (`:34`).

Which checkout supplies what [read] (`orca-yaml.mdx:61-73`):
| thing | read from |
|---|---|
| setup script, setup policy, defaultTabs | the NEW worktree's `orca.yaml` (so branch-specific) |
| sharedDirectories, `.worktreeinclude` | PRIMARY checkout |
| archive script | PRIMARY checkout's `orca.yaml`, run with the worktree as cwd |
| issueCommand | primary's `.orca/issue-command`, then `orca.yaml` |

Setup [read]:
- Runs in a visible "Setup" terminal tab in the desktop app when a setup runner is used (`createSetupRunnerScript`, `src/main/runtime/runtime-local-worktree-setup.ts:62-71`). On macOS/Linux a Bash runner with `set -e`; leading `#!` shell options replayed; Windows `.cmd` runner calling each non-empty line and stopping on failure (`orca-yaml.mdx:89`).
- Otherwise (CLI/headless, `shouldUseSetupRunner` false) it runs "in-process" fire-and-forget: `void runHook('setup', ...)`, failure only `console.error`'d, creation not failed (`runtime-local-worktree-setup.ts:76-80`, 113-121 in the 35-offset listing: lines ~71-80).
- A failure of the policy decision (new branch adds a hook the caller never approved) skips setup rather than fail the create, with a warning string "orca.yaml setup hook skipped ... pass --setup run to run it." (`:82-86`).
- Policy: Settings -> Repository: run automatically / ask / skip (new repo default: run); CLI `--setup run|skip|inherit`. "A valid file does not bypass Orca's command approval" (`orca-yaml.mdx:77`); shared YAML commands go through an approval flow that can reuse saved trust, local overrides are treated as user-authored (`:110`).
- Command source (`shared-only` / `local-only` / `run-both` joined into one script) (`:79-87`).
- Environment [read] (`orca-yaml.mdx:91-98`): `ORCA_ROOT_PATH` (primary), `ORCA_WORKTREE_PATH`, `ORCA_WORKSPACE_NAME` (dir basename), aliases `CONDUCTOR_ROOT_PATH`, `GHOSTX_ROOT_PATH`. Hook runner also merges `process.env` and drops "incoherent conda activation env" (`src/main/hooks.ts:69-70`) and sets unattended-git guards `promptGuardShellEnv` (`:98`, issue #7652: block Git Credential Manager prompts; GIT_TERMINAL_PROMPT=0 style, per the WSL branch lines `:40-48`).
- `setupAgentStartupPolicy`: `start-immediately` (default) or `wait-for-setup` (agent waits for successful setup). Only controls ordering.
- Timeout [read]: the in-process `runHook` has `HOOK_TIMEOUT = 120_000` ms (`hooks.ts:24`). The visible-terminal Setup runner: no timeout found [inferred: it is an interactive terminal tab]. OPEN.

Archive [read]:
- Runs ONLY when removal is requested with hooks enabled: `if (hooks?.scripts.archive && args.runHooks) { runHook('archive', canonicalPath, repo, ...) }` else a warning "orca.yaml archive hook skipped for <path>; pass --run-hooks to run it." (`src/main/runtime/runtime-registered-local-worktree-removal.ts:88-102`). CLI: `orca worktree rm --worktree <sel> [--force] [--run-hooks] [--allow-failed-archive-hook] [--json]` (`src/cli/specs/core.ts:143`).
- It is a PRECONDITION, evaluated before any PTY stop, deregistration or delete (`src/shared/worktree/archive-hook-removal-gate.ts:1-4`, `src/main/worktree-archive-hook-gate.ts:9-14`). Failure throws `WorktreeArchiveHookFailedError` code `worktree_archive_hook_failed` and message: "Archive hook failed for worktree: <path> — exited <n>. Nothing was stopped, deleted or deregistered. Fix the hook and retry, or delete anyway with an explicit waiver — "Delete Anyway" in the app, or --allow-failed-archive-hook on the CLI." followed by the hook output (`archive-hook-removal-gate.ts:25-31, 69-76`).
- `exited` (non-zero code) vs `unverifiable` (spawn failure, timeout, lost contact, signalled): both block; "Loss of contact is never evidence that the hook passed" (`:33-39`). `force` never implies the waiver ("Never implied by `force` — see #19334", `runtime-registered...:60`).
- Timeout `ARCHIVE_HOOK_TIMEOUT_MS = 120_000`; the host owns the deadline and settles AT it even if the script traps SIGTERM (`hooks.ts:72-77`); spawned `detached` as a group leader on POSIX so the group can be signalled (`:94-107`); shell `/bin/bash` (POSIX) or `ComSpec`/`cmd.exe` (`hooks.ts:106-112`). Client wait for `worktree.rm` reply = 120 s (if hook) + 180 s (`archive-hook-removal-gate.ts:18-20`).
- Output: stdout/stderr each captured to a bounded sink of 10 MiB, text appended "[output truncated at N bytes]" (`hooks.ts:72-78, 108-111`). Shown to the user in the failure toast (with a "Delete Anyway" button, `src/renderer/src/components/sidebar/delete-worktree-failure-toast.tsx` exists) and logged via `console.error('[hooks] ...')` (`worktree-archive-hook-gate.ts:26`). A waived failure returns `archiveHookOverride` to the caller (`...gate.ts:29-33`).

### 1.4 Create details (needed for section 2)

- Background: dialog closes, `git fetch` + `git worktree add` continue in background with a progress row, cancellable, retry on error (`D/model/worktrees.mdx` "Creation runs in the background"). Progress is read from git's own stderr meter (`src/main/git/worktree-add.ts:267-268`).
- Command [read] (`worktree-add.ts:241-252`): `git [-c long paths on Windows] worktree add [--no-checkout for sparse] --no-track -b <branch> <path> <base>`. `--no-track` so `git status` does not say "behind by N" pre-publish; `push.autoSetupRemote` is set so the first plain push creates upstream (`:251, :299`).
- Base/fetch: the start-from picker accepts base ref, local branch, commit SHA, or remote branch (`worktrees.mdx` "Start-from picker"). A remote fetch is cached: `FETCH_FRESHNESS_MS = 30_000` (reuse recent fetch), `REMOTE_FETCH_TIMEOUT_MS = 60_000`, cache max 512 entries, coalesced by in-flight map, `git fetch <remote>` (`src/main/runtime/runtime-remote-fetch-controller.ts:22-25, 102-135`). The worktree is created from the remote-tracking commit so the refresh "can overlap the checkout" (`worktree-add.ts:30` comment).
- Branch naming: sanitised name (keeps Unicode letters/digits and `._-`, other runs -> `-`, `..` collapsed, leading/trailing `.-` trimmed; emoji -> shortcode; `workspace` if only emoji) (`src/main/ipc/worktree-logic.ts:28-62`); then optional prefix from settings strategy (git username / custom / none) `prefix/name`; a garbage git-username prefix is silently skipped, a bad custom prefix errors (`src/main/ipc/worktree-branch-name.ts:44-62`). Explicit "Branch name" override in an Advanced drawer; for a linked PR/Linear/Jira/GitLab item the branch derives from the item (Linear's own branch name) (`worktrees.mdx` "Naming the branch").
- Location: `workspaceDir` setting joined with `[repoName/]<name>` depending on `nestWorkspaces` (`worktree-logic.ts:87-122`). Default dir from `getDefaultWorkspaceDir(homedir)` (`src/shared/constants.ts:112`). Not `.claude/worktrees` (that is Claude Code's default [web]).
- Orca recognises worktrees created outside it (`git worktree add`, Claude Code worktrees, "GSD" worktrees, custom roots): hidden by default behind a "hidden worktrees" card / "Non-Orca worktrees" dialog with per-project override and global source defaults in Settings -> General -> Workspace (`worktrees.mdx` "Using plain git"). If you `git worktree remove` from the CLI "Orca will notice and clean up its own state the next time it refreshes" (same section). [read] This is the direct analogue of Droidyard's situation (agents create their own worktrees).
- Orca watches for externally-created worktrees via watchers and pollers: `src/main/ipc/worktree-base-directory-watcher.ts`, `worktree-git-common-watch.ts`, `worktree-git-common-polling.ts` (names only; contents not read). OPEN: exact watch/poll cadence.

---

## 2. Lifecycle and cleanup

### 2.1 Archive vs delete

- Docs conflate: "Archive or delete — one click removes the worktree and branch" (`worktrees.mdx` line 23 of file) and "right-clicking a worktree exposes archive / sleep / delete actions" (`worktrees.mdx` ~line 113 of file). `isArchived` is a metadata flag on every worktree/folder workspace (`src/shared/worktree/types.ts:146`).
- Cleanup classifier uses it: an archived workspace idle >= 7 days gets reason `archived`; any workspace idle >= 30 days gets `idle-clean` (`src/shared/workspace-cleanup.ts:7-8, 245-259`).
- I did NOT find, by `isArchived: true` search under `src`, the code that sets the flag, nor whether "archive" removes the directory or only hides/flags. The only code path that deletes is `git worktree remove` in `src/main/git/worktree-removal.ts:77-90`. OPEN — investigate when building: Archive = flag-only or delete-with-hook? The archive HOOK runs "before archive/removal" (`orca-yaml.mdx:39`) which suggests archive = removal.
- "Sleep" = close terminals/browser tabs of a workspace (and with descendants), keeping the checkout (`worktrees.mdx` sidebar section; `src/main/runtime/terminal/stop/worktree-terminal-sleep.ts` exists). Hibernation of agents: `D/agents/hibernation.mdx` (not read).

### 2.2 Delete flow [read] (`runtime-registered-local-worktree-removal.ts:55-165`, `src/main/git/worktree-removal.ts`)

Order:
1. Archive hook (if enabled), as precondition.
2. Re-list worktrees (`git worktree list` strict) and verify the registration is unchanged; error "Worktree registration changed during deletion... Retry deletion." (`:61-65` in the 54-offset listing).
3. Refuse if the worktree is `git worktree lock`ed (`assertWorktreeUnlockedForRemoval`).
4. `assertWorktreeCleanForRemoval(path, force)` unless `force`; orphan-compatible errors (not a git repo, ENOENT) tolerated.
5. Stop PTYs, remove shared-link symlinks.
6. `git worktree remove [--force] <path>` with no timeout ("prod p90 29 s", `worktree-removal.ts:68-69`), under a delete concurrency limit (host-side "two-at-a-time", `archive-hook-removal-gate.ts:14`), then `removeCheckoutLeftByGit`.
7. Delete the branch unless `deleteBranch === false`.

### 2.3 Preserved branches [read] (`src/main/git/worktree-branch-removal.ts`)

Detection is exactly: try `git branch -d -- <branch>` (safe delete) (`:66-78`; `-D` only with `forceBranchDelete`, used for failed-creation rollback `:24-25`).
- If refused because the branch is still "checked out in a worktree" (stale admin record): `git worktree prune`, retry once (`:79-104`).
- If `-d` fails for another reason (not fully merged): second chance `deleteAlreadyMergedBranchAfterSafeDeleteFailure` (`:37-47, 107-141`) — because "squash merges rewrite commit IDs, so `branch -d` rejects already-merged branches" (`:124`). It deletes only if git proves "no unmerged tree changes" via `branchHasNoUnmergedChangesWithLazyTargetRefresh` (`src/shared/git-branch-cleanup.ts:288`). Mechanisms visible in that file (`git-branch-cleanup.ts` lines 61-65, 130, 173, 201-242): target refs = `branch.<name>.base` config then `refs/remotes/origin/HEAD` (symbolic-ref); possible `fetch --prune <remote>` refresh (`:92`); `git merge-tree --write-tree <target> <branch>` (clean merge result equals target tree => nothing unmerged; exit 1 on conflict, objects written in a quarantine, `:115-137`); `git cherry -v <target> <branch>` (patch-equivalence); `git patch-id --stable`, `git merge-base`, `git diff <base> <branch>`, `git rev-list`, `git show --format=` (per-commit patch-id comparison). I did not read the exact decision ordering. OPEN.
- If still not deletable: the branch is KEPT and the result carries `preservedBranch: {branchName, head}` (`worktree-branch-removal.ts:57-62`): "deleting a worktree must never silently discard commits". UI: toast "Review N Branches" -> dialog listing kept branches to force-delete selectively (`D/model/worktrees.mdx` "Preserved branches"; components `PreservedBranchBatchReviewDialog.tsx`, `preserved-branch-toast.tsx`). Workspace folders are NOT restored.
- Force delete later uses `git update-ref -d refs/heads/<b> <expectedHead>` (compare-and-delete: only deletes if the branch is still at the recorded head), checks not checked out elsewhere, restores the ref if a concurrent checkout appears, then removes `branch.<name>` config section (`worktree-branch-removal.ts:143-190`). Stale toast actions cannot delete a branch that moved (`:162`).
- Contrast with Claude Code: it keeps/removes whole worktree with a prompt based on changed files/new commits; sweep keeps anything with unpushed commits [web].

### 2.4 Resource Manager: what it measures

UI: status bar "Resource Manager" (`D/settings.mdx:28`: "CPU/memory/sessions, daemon controls, workspace disk scans"). Code: `src/main/memory/collector.ts`, `src/renderer/src/components/status-bar/use-resource-usage-status-controller.ts`, `src/main/workspace-space-*.ts`.

CPU and memory [read]:
- One snapshot = (a) Orca's own Electron processes via `getAppMetrics()` bucketed main/renderer/other, (b) each registered PTY's process SUBTREE out of a single host-wide process sweep, attributed to its worktree (`collector.ts:1-15`).
- Sweep on Unix: `ps -eo pid=,ppid=,pcpu=,rss=` with `LC_ALL=C` (locale decimal comma bug), 5 s timeout, 10 MiB buffer (`collector.ts:85-86, 135-150`). CPU = percent of one core (may exceed 100). Memory = RSS bytes; Windows adds "private (committed) bytes" via PowerShell CIM with Typeperf fallback, `hasPrivateMemory` is data-driven (`:92-110`).
- Per-key sample ring (one per worktree + app total) for sparkline (`:10-11`); concurrent callers coalesce on one in-flight sweep (`:13, 63-80`).
- Cadence: polled every `POLL_MS = 2_000` ms ONLY while the popover is open (`use-resource-usage-status-controller.ts:36, 192-205`). Session inventory (`listSessions`) is NOT polled: on open/action only, "since it's expensive with many terminals" (`:191-200`).

Sessions and daemon controls [read from i18n strings, not code]: "Manage Sessions" lists terminal sessions, can kill sessions, "force-quits every running terminal pane across all workspaces... The daemon itself keeps running", and "Restart daemon" which closes all local terminals (`src/renderer/src/i18n/locales/en.json` lines 6867, 6892, 251, 8029-8033). The terminal daemon is the process that hosts the PTYs and survives app restarts [read: en.json:1421].

Disk size per workspace [read] (`src/main/workspace-space-analysis.ts`, `workspace-space-local-scan.ts`):
- On demand (a "scan", cancellable via AbortSignal; progress events), not periodic [inferred from absence of any interval in these files; OPEN verify].
- Local worktree: depth-1 `du -k -d 1 <root>` on a worker thread, timeout `DU_TIMEOUT_MS = 120_000`, buffer 16 MiB (`workspace-space-analysis.ts:22-23, 66`); plus a JS directory walk with `lstat`/`opendir` (`workspace-space-local-scan.ts:1, 65-113`), a scan budget (`src/shared/workspace-space-scan-budget.ts`), compaction. Concurrency: repos 2 at a time, local worktrees 1 at a time (`workspace-space-analysis.ts:20-21`).
- A snapshot is persisted to a sidecar JSON (`orca-workspace-cleanup-scan.json`, version 2, keyed by a classifier-version fingerprint; older classifier snapshot "must read as absent") (`src/main/workspace-cleanup-scan-snapshot.ts:16-17, 36-40`).

### 2.5 Cleanup list ("Clean up workspaces") [read]

Doc: `worktrees.mdx` "Resource Manager cleanup": lists local worktrees, main worktrees, folder workspaces, workspaces on disconnected SSH hosts; search/filter/sort by status, recent activity, size, Git state, linked review.

Candidate record (`src/shared/workspace-cleanup.ts:54-86`): branch, path, `reasons`, `blockers`, `lastActivityAt`, `createdAt`, `localContext` {terminalTabCount, cleanEditorTabCount, browserTabCount, diffCommentCount, newestDiffCommentAt, retainedDoneAgentCount}, `git` {clean, upstreamAhead, upstreamBehind, checkedAt}, `fingerprint`. The legacy `tier` (ready/review/protected) and `selectedByDefault` are deprecated: "Current renderers ignore this legacy verdict" (`:63-66`) — i.e. Orca moved from an auto-suggested selection to a flat list with evidence; nothing is preselected by the current UI [read; the replacement filter model is in `shared/workspace-cleanup-filter-model.ts`, not read].

Blockers (`:19-35`): main-worktree, folder-repo, pinned, active-workspace, running-terminal, terminal-liveness-unknown, dirty-editor-buffer, volatile-local-context, recent-visible-context, live-agent, ssh-disconnected, git-status-error, dirty-files, unpushed-commits, unknown-base, dismissed. A dismissal is stored per `worktreeId` + `fingerprint` + `classifierVersion` (+ host) so a changed workspace re-surfaces (`:37-46`; fingerprint = version|branch|head|clean/dirty|activity bucket, `:235-241`).

Git evidence (`src/main/ipc/workspace-cleanup-git-evidence.ts:36-117`):
- `getStatus(worktree.path, {includeLineStats:false})` under an 8 s timeout (`workspace-cleanup-scan-primitives.ts:6`); any failure or missing `upstreamStatus` => blocker `git-status-error`.
- `clean = entries.length === 0` else `dirty-files`.
- ahead/behind from upstream status; `ahead > 0` => `unpushed-commits`.
- If clean and NO upstream: `git rev-list --count HEAD --not --remotes` (8 s timeout); count > 0 => `unpushed-commits`; unparsable => `unknown-base` (`:119-143`). This one command ("commits reachable from HEAD but from no remote ref") is the cheapest portable "would deleting lose work" test and is a good Droidyard primitive.
- Shared-link paths are excluded from dirty status (`:60, 68`).
- SSH / host-mismatch refuse to read the local checkout ("An unreachable remote host is an error, never a licence to read this machine's checkout", `:53`).

Activity: `lastActivityAt` = newest of Orca's persisted stamps (createdAt, lastActivityAt, lastTerminalUseAt...) `shared/workspace-cleanup.ts:262-`, refreshed via re-stat for targeted removal preflight (`refreshActivity`, `:104-105`). Batch limit 500 ids per targeted scan (`:108`).

Before removal: a preflight re-scan with fresh evidence (see `refreshActivity` comment "removal preflight needs fresh, batched evidence scans do not", `:104-105`), removal-snapshot pruning so deleted workspaces vanish from the stored snapshot (`src/main/workspace-cleanup-removal-snapshot-prune.ts`, not read).

### 2.6 Automatic sweeps and safety rules

- Age-based automatic deletion of ordinary worktrees: NOT FOUND [read: searched `src/main`+`src/shared` for auto-archive/auto-cleanup/"automatic sweep"; only hit unrelated]. The 7-day/30-day thresholds only populate the manual list as `reasons`. [inferred] Orca never auto-deletes a user's worktree.
- Automatic sweeps that do exist, all about Orca's OWN half-made worktrees:
  - Stale creation preparation cleanup: `src/main/worktree-create-preparation-stale-cleanup.ts` discards preparations whose owner PID is dead on the same machine (`process.kill(pid, 0)`, ESRCH), with owner-machine identity check so another machine sharing the repo (network disk) is not trusted (`:14-24`), concurrency 4, in-flight de-dupe, retry of pending discards (`worktree-preparation-discard-retry`). Also `worktree-preparation-discard.ts` uses `branch -D`.
  - Orphan directory cleanup: `src/main/local-orphaned-worktree-cleanup.ts` removes a directory only if `canSafelyRemoveOrphanedWorktreeDirectory` (path inside workspace root, not home, etc.; `CLIENT_REMOVAL_HOME` guard).
- Safety rules visible: lock check; clean check; archive-hook precondition; `-d` not `-D`; compare-and-delete of branch; registration recheck; refuse remote-host mismatch; never touches the main worktree (blocker `main-worktree`); pinned/active/live-agent/running-terminal blockers; bulk delete still prompts ("the normal confirmation dialog still appears", `worktrees.mdx` sidebar section) with `Cmd-Shift-Backspace`.

---

## 3. Review

### 3.1 Annotate AI Diff

Docs: `D/review/annotate-ai-diff.mdx`.
Storage [read]: comments are saved on `WorktreeMeta.diffComments`, persisted to `orca-data.json` by the existing metadata layer (`src/shared/diff-comment-types.ts:1-5`; update path `updateMeta({worktreeId, updates:{diffComments}})` seen in `src/renderer/src/store/slices/diffComments.test.ts:487-490`; folder workspaces have their own `src/main/folder-workspace-diff-comments.ts`). So the comments live in Orca's app data, NOT in the repo and not in git.

Record (`diff-comment-types.ts:27-48`): `id, worktreeId, filePath, source?('diff'|'markdown'), selectedText?, startLine?, lineNumber, body, createdAt, updatedAt?, sentAt?, scope?('unstaged'|'staged'|'branch'), oldPath?, diffIdentity?, side:'modified'`. `lineNumber === 0` means whole-file scope. Comments are only on the modified side ("Reserved for future comments on the original side").

Format sent to the agent — VERBATIM, `src/shared/diff-comments-format.ts` (34 lines):
```ts
function isMarkdownComment(comment: Pick<DiffComment, 'source'>): boolean {
  return comment.source === 'markdown'
}

// Why: the pasted format is the contract between review notes and whichever
// agent consumes them. Keep it deterministic and quote-safe across clients.
export function formatDiffComment(c: DiffComment): string {
  const escaped = c.body
    .replace(/\\/g, '\\\\')
    .replace(/"/g, '\\"')
    .replace(/\r/g, '\\r')
    .replace(/\n/g, '\\n')
  const locationLabel =
    c.lineNumber === 0
      ? 'Scope: file'
      : c.startLine !== undefined && c.startLine !== c.lineNumber
        ? `Lines: ${c.startLine}-${c.lineNumber}`
        : `Line: ${c.lineNumber}`
  if (!isMarkdownComment(c)) {
    return [`File: ${c.filePath}`, locationLabel, `User comment: "${escaped}"`].join('\n')
  }
  return [
    `File: ${c.filePath}`,
    'Source: markdown',
    locationLabel,
    `User comment: "${escaped}"`
  ].join('\n')
}

export function formatDiffComments(comments: readonly DiffComment[]): string {
  return comments.map(formatDiffComment).join('\n\n')
}
```
So a batch is plain text blocks separated by a blank line, e.g.
```
File: src/a.kt
Lines: 12-14
User comment: "rename this; it shadows the param\nand add a test"

File: src/b.kt
Line: 3
User comment: "..."
```
No preamble or instruction text is added by this function (the agent just receives the notes as a prompt) [read]; whether the launch path prepends anything: not seen in `NotesSendMenu.tsx`. OPEN. There is also a copy at `src/renderer/src/lib/diff-comments-format.ts` (presumably a re-export; not read).

Batching and delivery [read] (`src/renderer/src/components/editor/DiffNotesSendMenu.tsx`, `NotesSendMenu.tsx`):
- Scopes: "This file" (file-scoped menu, default when shown) and "All unsent notes" (`DiffNotesSendMenu.tsx:56-82`); notes with `sentAt` set are excluded; a "send in flight" registry prevents the same note being included by two concurrent sends (`NotesSendMenu.tsx:23, 32, 125-129`).
- Delivery: the prompt is handed to a generic "agent send popover" (`openAgentSendPopoverTargetMode`, `promptDelivery="submit-after-ready"`, `launchSource="notes_send"`, `NotesSendMenu.tsx:135-145, 278-293`): the user picks an existing agent terminal of the worktree or launches a new agent; "submit-after-ready" = text is typed/pasted into that agent's TUI input and submitted once it is ready [inferred from name; the PTY write code was not read]. There is NO MCP/CLI pull channel for diff comments: grep of `src/cli/specs` found no diff-comment command; `src/main/runtime/rpc/methods/worktree.ts:198` only passes `diffComments` through worktree metadata update. [read: absence]. So the agent receives it as a user prompt, once.
- After delivery `onDelivered -> clearDeliveredDiffComments(worktreeId, notes)` REMOVES the delivered notes from storage, keeping any note that changed during delivery (`DiffNotesSendMenu.tsx:102`; test `diffComments.test.ts:476-508`: "clears delivered notes and persists the remaining pending notes", "keeps a note that changed while delivery was pending"). **DOC/CODE MISMATCH**: the docs say comments "stay pinned after the agent revises" and "Unresolved comments are part of the next batch" (`annotate-ai-diff.mdx:35-37`), but the code deletes delivered notes (the `sentAt` field is documented as "Set after the note has been handed to an agent. Edits clear it", `diff-comment-types.ts:41-42`, so there may be two behaviours in different surfaces). OPEN: which is current for the combined-diff view (`use-combined-diff-notes-actions.ts`, 143 lines, not read).
- "Comments pin to the exact line; Orca tracks them across edits so they follow the line if the diff shifts" (docs line 21). Code evidence of line tracking: `diffIdentity` field and `lastSeenDiffIdentity`; actual re-anchoring algorithm not found. OPEN.
- Multi-file: yes, the "All unsent notes" scope covers every file of the worktree; each note carries its own `File:` line.
- Mobile has its own per-file review state (`MobileDiffReviewState`: `reviewedAt`, `lastSeenDiffIdentity`) (`diff-comment-types.ts:9-25`).

### 3.2 Per-line attribution (`D/review/attribution.mdx`)

Claim [read] (`attribution.mdx:9`): "When an agent writes to a file through its tooling, Orca records the ranges. The diff viewer renders AI-originated lines with a subtle marker in the gutter. Human edits on top of AI code flip the attribution back to human." Scope (`:19`): "local to Orca — it doesn't get committed to git. ... export the diff metadata from the diff toolbar."

Code search: **NOT FOUND.** I searched `src`, `docs`, `skills` for: `attribution` (hits are all unrelated: macOS TCC attribution, crash-report sibling attribution, "agent attribution" of worktree creation `createdWithAgent`, API-budget attribution), `provenance` (workspace/plugin/CLI provenance only), `ai-written|ai-authored|ai-generated`, `aiRanges|aiLines|lineProvenance`, `glyphMargin|gutter.*(ai|agent)`, `exportDiff|diff.*metadata`, `human` under `components/editor`. No range-recording store, no gutter marker, no export action. `docs/site/content/docs/model/worktrees.mdx:21` just lists "Attribution" in the Review step. [inferred] The documented feature is not implemented in this commit (or is under a name I did not search); treat the doc as aspirational. What DOES exist: `createdWithAgent` on worktrees (which agent created the workspace; `runtime-local-worktree-materialization.ts` takes `effectiveCreatedWithAgent`) and the agent-status/"OSC 7501" program status, but not per-line.
OPEN — investigate when building: run the app and look at the diff gutter; check `src/renderer/src/components/editor/combined-diff/` and Monaco decoration code for decorations keyed by agent writes.

---

## 4. Source control rules

Docs: `D/review/commit-push.mdx`. Code: `src/main/git/source-control/commit-changes.ts`, `src/main/git/remote.ts`, `src/shared/git-upstream-status.ts`, `src/shared/source-control-primary-action-decision.ts`, `src/renderer/src/lib/source-control-remote-error.ts`.

Commit [read]:
- Implementation is exactly `git commit -m <message>` in the worktree, read caches invalidated before and after (`commit-changes.ts:6-33`). No `--no-verify`, no `-a`, no `--amend`. Hooks run as usual ("Pre-commit hooks from the repo run as usual. If a hook fails Orca surfaces the output inline." `commit-push.mdx:13`).
- Failure output: `stderr ?? stdout ?? error.message ?? 'Commit failed'`, returned as `{success:false, error}` "because useful message may be on stderr (hook/GPG failures) or stdout ('nothing to commit')" (`commit-changes.ts:16-30`). So the hook output is the raw git output string.
- "Fix with AI": starts the default agent in the worktree with hook output, attempted message and staged file list; "The agent gets a repair prompt only — it is not asked to bypass hooks, commit, push, or open a review" (`commit-push.mdx:15`). Orca still has agent recipes (agent, CLI args, prompt template with `{stagedFiles}`, `{stagedPatch}`, `{branch}`, `{linkedIssue}`...) per action (`:31-39`).

Amend [DOC/CODE MISMATCH]: docs: "Amending is explicit — **Commit → Amend**. Orca won't amend commits that have already been pushed unless you confirm." (`commit-push.mdx:41-43`). Code: grep for `amend` under `src/main`, `src/shared`, `src/renderer`, `src/cli` finds nothing relevant (only crash-report/watcher files), and the only commit function is plain `git commit -m`. So the quoted guard rail is documented but I found no implementation. OPEN: maybe lives in a not-yet-searched native/mobile/cloud tree.

Push [read]:
- `git push [--force-with-lease] --set-upstream <remote> <refspec>`; remote/refspec = explicit push target (`HEAD:<branch>`), else the configured upstream, else `origin HEAD` (`remote.ts:22-61`). Comment: PR-created worktrees may track a contributor fork remote, hardcoding origin "would send review commits to the upstream repository instead" (`:36-39`).
- Plain push never forces: "If the branch is behind, Orca will not silently force-push" (`commit-push.mdx:19`).
- Force with lease is a SEPARATE action offered ONLY when: has upstream AND ahead > 0 AND behind > 0 AND `behindCommitsArePatchEquivalent === true` (`git-upstream-status.ts:39-48`; decision `source-control-primary-action-decision.ts:153-164` sets `labelIntent:'force_push'`, `requiresForceWithLease:true`). I.e. the "behind" commits on the remote are just older versions of your own local commits (rebase/amend/squash). Button title: "Remote only has older copies of local commits. Force push <N commits> with lease to update <upstream>." (`source-control-primary-action-titles.ts:13-19`). Otherwise diverged => `sync` (pull then push).
- Lease rejection message: "Force push rejected — remote changed since last fetch. Fetch first, then try again." (`source-control-remote-error.ts:199-206`). Telemetry/labels distinguish `push`, `publish`, `force_push` (`git-remote-push-pull.ts:32`).
- Each git op runs under `runWithGitWorktreeOperationLock(worktreePath)` plus a pause of background ref maintenance (pull/ff shown at `remote.ts:109-114, 123-129`).
- Pull = plain `git pull` with divergence fallback (`runPullWithDivergenceFallback`); "Conflicts surface through the existing conflict-resolution flow" (`remote.ts:106-108`). Fast-forward = `git pull --ff-only`. Fetch = `fetch --prune`.

Hosted review (PR/MR) [read: docs]: after push, Source Control "hosted review action": confirm base branch, title, description, draft state (`commit-push.mdx:23-25`). "Generate pull request details with AI" rejects empty descriptions, keeps selected base (`:29`). If Create PR needs a follow-up commit and it fails (hooks reject), the dialog shows failure summary with hook output (`:27`). Providers: GitHub, GitLab, Bitbucket Cloud (no draft), Azure DevOps and Gitea directories exist in `src/main` (`bitbucket/`, `azure-devops/`). Stacked PRs for GitHub (`review/github.mdx`, not read). Not read: the actual `gh`/API call code. OPEN.

Conflicts [read: docs]: "When a merge, rebase, cherry-pick, or revert leaves conflicts, Source Control shows **Resolve with AI** next to **Review conflicts**... For an in-progress merge or rebase you no longer want, use **Abort merge** or **Abort rebase**" (`commit-push.mdx:57`). Primary-button ladder: Stage Files -> Commit -> Push/Pull/Sync (`:55`).

Guard-rail summary (quote-ready): never silent force; force only when remote-only commits are patch-equivalent; lease not bare force; no `--no-verify`; AI repair agents "not asked to bypass hooks, commit, push, or open a review"; amend-after-push requires confirmation (documented, unverified in code).

---

## 5. Activity feed and notifications (worktree-related)

Docs: `D/activity.mdx`, `D/notifications.mdx`.
- Feed ("Agents" sidebar entry, on by default, unread badge) event kinds [read: docs]: agent finished a turn (idle or blocked on a question), new worktree created, agent waiting long enough to be "blocking", short preview of the last response. Threads grouped by status; running agents pinned at top. Clicking jumps to worktree and pane (`activity.mdx:12-21`).
- No worktree archive/delete/push/commit events are listed [read: docs]. Only creation.
- Retention [read] (`src/renderer/src/components/activity/activity-event-cap.ts:3-40`): `EVENTS_PER_PANE_CAP = 5`, global cap 80 non-working events, newest-first, each pane's newest event reserved before the cap so every agent stays visible; "working" turns are uncapped and do not evict history. Response preview rendered max 320 chars (`activity-thread-presentation.ts:25`). Search query max 2 KiB (`activity-thread-grouping.ts:128`). "Clear completed" exists (`activity-clear-completed.ts`) and a `retentionSuppressedPaneKeys` map. Persistence of the feed itself across restarts: `worktrees-activity-persistence.test.ts` and `folder-workspace-activity-persistence.ts` exist; not read. OPEN.
- Notifications [read: docs]: agent working->idle fires system notification + sound + chip; header bell with unread across worktrees; macOS Dock badge; mark unread; per-category toggles (system, sound, chip-only); custom sound (MP3/WAV/OGG/M4A/AAC/FLAC), one file for all desktop notifications. State transitions come from "Agent status hooks" (working/waiting/done), toggle `orca agent hooks on|off|status` (`D/settings.mdx:64`). Orca also reads status via an OSC 7501 terminal escape ("show OSC 7501 program status", HEAD commit f48123e8).
- Worktree-directory change notifications (external worktree appear/disappear): `src/main/ipc/worktree-base-directory-notifications.ts`, not read.

---

## 6. "Keep computer awake"

Setting [read] (`D/settings.mdx:65`): **On** (continuous), **Agent** (while an agent is working), **Off**; same control on the status bar as "Caffeinate"; hidden on paired web clients. Mode type `ComputerAwakeMode` = `'off'|'auto'|'on'` ('Agent' = `auto`) (`src/main/agent-awake-service.ts:46, 91-102`).

Implementation, `src/main/agent-awake-service.ts` (+ helpers):
- Decision: `shouldBlock = mode==='on' || (mode==='auto' && runningStatusCount > 0)` (`:151`). `runningStatusCount` = agent pane statuses with `state === 'working'`, `observedInCurrentRuntime`, and `receivedAt` within `AGENT_AWAKE_STATUS_STALE_AFTER_MS = 2 h` (a lease that auto-expires if the hook stops reporting) (`agent-awake-status-lease.ts:3, 36-60`). Hold is re-evaluated on: settings change, status change, status freshness renewal, lease expiry timer, and `powerMonitor` `resume` event (`:63, 83, 101, 106, 112`).
- macOS: spawns `/usr/bin/caffeinate -i -s` (idle-sleep and system-sleep-on-AC assertions) as a child and holds the child; on child error/exit it records failure with a retry-not-before backoff (`src/main/macos-system-sleep-assertion.ts:70`, `:57-66`). If that works, Electron's blocker is stopped ("macos-assertion-active"); if not, falls back to Electron `powerSaveBlocker.start('prevent-display-sleep')` (`agent-awake-service.ts:153-158, 193`). So on macOS the screen may still sleep? The fallback is display-sleep prevention; the caffeinate flags `-i -s` do not include `-d`. [inferred] On macOS with caffeinate working the display is not kept awake, the system is.
- Linux: `systemd-inhibit --what=sleep:handle-lid-switch --who=Orca --why="Agents are working" --mode=block cat` with stdin piped: "EOF releases the inhibitor even when Orca is killed without running cleanup" (`src/main/linux-lid-sleep-assertion.ts:75-90`); needs logind, handles lid close; failure retried/backed off, "systemd-inhibit-missing" logged once-ish (`:188-234`).
- Windows: only Electron `powerSaveBlocker('prevent-display-sleep')`; "Windows lid close is intentionally not modeled as an assertion here: keeping it awake requires mutating the user's global power plan" (`agent-awake-service.ts:64-65`).
- Release: mode off, no working agents, dispose (`stopBlocker`, `stopMacosAssertion`, `stopLinuxAssertion`, `:160-164, 137-143`). A status publish happens only on change of {mode, active} (`:168-180`).
- Droidyard relevance [inferred]: an Android build/emulator run by an agent is the "agent working" signal Orca gets from agent hooks; Droidyard has its own signal (running Gradle build, emulator boot, active MCP calls).

---

## For Droidyard

Assumptions (from the task statement, ADRs not re-read): ADR-0082 §2 Droidyard never creates/deletes a worktree; ADR-0095 §3 records file paths of an agent's edits, "nothing of the content"; ADR-0004 no in-house agent harness / no launching agents; MCP tool-list budget 29,000 bytes with ~145 B left, so assume NO new MCP tool unless one is removed or merged; web UI + human-only capabilities are free.

Cross-cutting design rule from Orca [inferred]: Orca can delete safely because it also owns the terminals. Droidyard does not, so any "cleanup" must be READ-ONLY EVIDENCE plus a copyable command, never an action.

### F1. Worktree preparation (`.worktreeinclude`, shared dirs, setup/archive scripts)
- Feature: a read-only "Worktree readiness" panel for a registered workspace: parse the repo's `.worktreeinclude` (Claude Code gitignore syntax, section 0) and list (a) entries present in the primary checkout and gitignored, (b) which of those exist in the agent's worktree, (c) the ones missing (e.g. `local.properties`, `google-services.json`, `keystore.properties`, `.env`). Detects the classic Android failure "SDK location not found" when `local.properties` is missing in a fresh worktree. Orca's checks to reuse: lstat existence + `git check-ignore` (`worktree-include-file.ts:115-141`), safety filter (`:50-56`). Show a suggested `.worktreeinclude` line for the human to commit; Droidyard never copies.
- Capability: human-only read, e.g. `workspace.worktree_readiness` (no MCP exposure, or fold the result into an existing status payload only if it costs 0 B in the tool list; descriptors' text is what costs bytes, so probably a UI-only HTTP capability).
- ADR touched: none violated; cite ADR-0082 §2 (read-only). If it is registered as a capability it is a contract addition => schema regeneration (`go test ./internal/schema -update`).
- Size: small-medium (parser ~100 lines Go, `git check-ignore --stdin -z` call, one UI card).
- Open questions: does Droidyard have a notion of "primary checkout" vs agent worktree? How does it learn the agent's worktree path (ADR-0095 edit paths)? Should globs be honoured (Claude Code does, Orca does not)? Use `git check-ignore` for globs by feeding expanded matches [inferred].
- Do NOT copy: Orca's copy budget (2 GiB / 50k entries), APFS clone, link-sharing of `node_modules` — they require creating worktree content. Good Android analogue worth documenting only: the Gradle/Android build caches (`.gradle`, `build/`) are per-worktree and a shared one would be unsafe [inferred].
- `scripts.setup`/`scripts.archive`: out of scope (Droidyard launches nothing). The duplicate-key lesson is reusable if Droidyard ever reads a config: reject the whole file on duplicate keys and say so (Orca: `uniqueKeys:true`, `orca-yaml.ts:207-213`), and report skipped fields instead of silent drop (Orca admits "not every skipped field/path has a visible error", `orca-yaml.mdx:149`, a weakness).

### F2. Worktree lifecycle and cleanup (preserved branches, Resource Manager, cleanup list)
- Feature A, "Worktree inventory" (human-only): list `git worktree list --porcelain` for each registered repo with, per worktree: branch, head, dirty (porcelain status empty?), ahead/behind of upstream, "commits on no remote" (`git rev-list --count HEAD --not --remotes`, Orca `workspace-cleanup-git-evidence.ts:119-143`), locked?, last activity (newest mtime of Droidyard's own records for that path: last build, last install, last edit path from ADR-0095), disk size (`du -sk` with timeout, on demand only, Orca uses 120 s and 1-at-a-time concurrency), and Droidyard-specific holdings (device/emulator/session bound to it, running Gradle daemon). Blockers shown as chips mirroring Orca's vocabulary (dirty-files, unpushed-commits, unknown-base, git-status-error, live session).
- Feature B, "Preserved branch" check: for a worktree the human is about to remove, show whether its branch would survive `git branch -d` (merged?) — Orca's two-stage test: `branch -d` semantic then squash-merge-aware `merge-tree --write-tree` / `cherry` (section 2.3). Output a copyable command, e.g. `git worktree remove <path>` and `git branch -d <b>`; never run it (ADR-0082 §2).
- Feature C, disk/space card: Android worktrees are large (Gradle `build/`, `.gradle/`, AVD not inside). Show size split top-level dirs (`du -k -d 1`) exactly as Orca does.
- Feature D: "stale" suggestions reusing Orca's thresholds as defaults (7 d archived, 30 d idle) but only as sort/filter, since Orca itself never auto-deletes.
- Capability: new human-only read capability(ies); none on MCP. Use `git -C <path>` read-only commands only; add timeouts (Orca: 8 s status, 8 s rev-list, 5 s ps).
- ADR touched: ADR-0082 §2 (reads only; explicitly state "inventory never mutates"); ADR-0095 §3 (use paths/timestamps only, no content). Possibly a new ADR if Droidyard decides to expose a "worktree" noun in the capability API.
- Size: medium (Go git probes ~200 lines + UI table + tests with synthetic repos; do not test on the owner's repos).
- Open questions: how is "last activity" defined without Orca's terminals? Is a worktree "owned" by an agent session (Droidyard has `workspace` and session notions)? Does the sweep belong in the daemon (background) or only on demand (recommended on demand; Orca polls memory only while the popover is open, `use-resource-usage-status-controller.ts:192-205`)?
- Resource usage per workspace: Orca attributes CPU/RSS by PTY subtree it owns. Droidyard can only attribute processes it spawns or can find: Gradle daemons (cwd or `--project-dir` => worktree), the sidecar, adb, emulator. A human-only "processes by workspace" view via `ps -eo pid=,ppid=,pcpu=,rss=` (Orca's exact invocation, `LC_ALL=C`) filtered to Droidyard-owned subtrees is feasible; attributing to arbitrary agent processes is not [inferred]. Keep only for processes Droidyard started.

### F3. Review: line-anchored notes and attribution
- Feature A, "Review notes" (human-only in the web UI): the human anchors a comment to `file:line(-line)` in a diff shown by Droidyard, and Droidyard stores it. Delivery to the agent has three possible shapes; Orca's shape (typing into the agent terminal) is NOT available (ADR-0004). Options:
  1. Copy-to-clipboard of Orca's exact format (`File:`, `Line:`/`Lines: a-b`/`Scope: file`, `User comment: "<escaped>"`, notes separated by a blank line; section 3.1) for the human to paste into the agent. Zero MCP cost, zero ADR change; the format is a proven contract ("deterministic and quote-safe across clients").
  2. Pull: expose the pending notes through an EXISTING MCP read capability (e.g. attach `notes` to a status/diagnostics response when non-empty) so the agent sees them on its next call. Costs 0 bytes of new tool names only if no new schema text is added; any new field in a descriptor does cost bytes — check budget. Orca has no pull channel, so this would exceed Orca.
  3. Dedicated MCP tool: not affordable (145 B left) without trimming others.
- Storage: Droidyard-side (its data dir), keyed by workspace + file path + line + `diffIdentity` (Orca stores a hash of the diff to detect staleness, `diff-comment-types.ts:45`), never in the repo. Content of the NOTE (human text) is not "content of the agent's edits", but ADR-0095 §3 says it records edit PATHS and "nothing of the content": the diff view itself needs the file content from git at view time, so the note's line anchor should be re-validated against `git diff` on open rather than storing code; storing `selectedText` (Orca does, for markdown notes) would store content => avoid or hash it. Needs checking against ADR-0095 §3 wording. OPEN.
- Batch rule to copy: send once, then mark/remove (Orca's per-note `sentAt`, in-flight registry to avoid double-send, keep a note edited during delivery, `NotesSendMenu.tsx:23,125`; `diffComments.test.ts:493-508`). Resolve decision on docs-vs-code mismatch: keep sent notes visible as "sent" (docs) vs delete (code); recommend keep with `sentAt` so the human can see what was asked.
- Feature B, attribution: Orca documents it but I found no implementation (section 3.2). Droidyard CAN do a weaker, honest version: ADR-0095 §3 already records WHICH FILES an agent edited; show per-file "touched by the agent in session X, N times" badges in the changes view. Per-LINE attribution needs content/range recording => conflicts with ADR-0095 §3 ("nothing of the content"); do not build. Alternative that stays within the ADR: after the fact, `git blame`-style by commit author/trailer (`Co-Authored-By:`) read from git at view time, no recording at all [inferred].
- Capability: human-only HTTP capabilities (create/list/delete note; format/export); optional MCP pull via an existing tool (option 2).
- ADR touched: ADR-0004 (no launching/typing into agents => Option 1/2 only), ADR-0095 §3 (content), maybe new ADR "review notes".
- Size: medium (store + UI gutter in the web diff view + formatter port ~40 lines; line re-anchoring is the hard part: Orca's docs promise it, the algorithm was not found).
- Open questions: does Droidyard have a diff viewer at all? How to re-anchor after edits (map via `git diff -U0` hunk offsets between the stored diffIdentity and current)? MCP-visible or copy-only?

### F4. Source control rules
- Droidyard is not a git client (an agent commits/pushes). Features worth taking are as ADVISORY read-outs for the human and as constraints in docs for agents:
  - "Push safety" indicator per workspace: ahead/behind/diverged and the Orca predicate "diverged but behind commits are patch-equivalent" (`git cherry <upstream> HEAD` or `git rev-list --left-right --cherry-pick`), to tell the human "a force-with-lease is the right push" vs "do not force". Human-only read capability.
  - Pre-commit hook output inline: Droidyard only sees it if it observes the agent's command output; skip, unless Droidyard starts to run git (it does not).
  - Docs-level rules to state in Droidyard's agent guidance (AGENTS.md style, not code): plain push never forced; `--force-with-lease` not `--force`; never `--no-verify`; amend only before first push or after explicit human OK; abort merge/rebase explicitly. These are Orca's published guard rails (sections 4) and Claude Code convention; they cost nothing.
  - PR/MR creation: out of scope (needs credentials, provider APIs; agent does it via `gh`/`glab`, which Claude Code already links sessions to [web, common-workflows]).
  - Conflict handling: show "operation in progress" (merge/rebase/cherry-pick/revert) by reading `.git/MERGE_HEAD`, `rebase-merge/`, etc. in the worktree's git dir, as a banner. Small, read-only.
- ADR touched: none, (reads). Possibly note in the Android docs. Size: small.
- Open questions: does Droidyard already read git status for any capability? Where would the banner live?

### F5. Activity feed and notifications
- Feature: a Droidyard "Activity" list per workspace/worktree, built from events Droidyard already knows (build started/finished/failed, install, device/emulator state change, session opened/closed, agent's edited paths from ADR-0095, new worktree appeared on disk). Orca's retention numbers are a sane default: keep newest 5 per source ("pane"), global cap 80, reserve each source's newest event first, never let in-flight ("working") items evict history (`activity-event-cap.ts:3-40`). Notifications: browser Notification / tab-title badge for build finished/failed while the tab is hidden; unread count; click focuses the item (Orca: `notifications.mdx`).
- "New worktree detected" event: Droidyard can detect (poll `git worktree list` on registered repos, cheap) but must label it "observed", not "created by Droidyard".
- Capability: human-only SSE/event stream or an extension of an existing event channel; no MCP change.
- ADR touched: none new; consistent with ADR-0095 (paths only).
- Size: small-medium. Open questions: persistence across daemon restarts (Orca's feed persistence not read, OPEN); event granularity.

### F6. Keep computer awake
- Feature: a daemon-side "keep awake while Droidyard has work in flight" option, default off (Orca: Off/Agent/On). Triggers: a Gradle build/test, an emulator boot or a `device` long operation in progress; plus an explicit "On" toggle in the web UI. macOS: spawn `/usr/bin/caffeinate -i -s` as a child of the daemon and kill it on release (child dies with the daemon => no stale lock; Orca's Linux variant uses stdin-EOF with `systemd-inhibit ... cat` for the same reason: `linux-lid-sleep-assertion.ts:75-90`). Linux: `systemd-inhibit --what=sleep ... cat` (optionally `handle-lid-switch`). Windows: out of scope (MVP says Windows is out). Add a staleness lease like Orca's 2 h `AGENT_AWAKE_STATUS_STALE_AFTER_MS` so a missed "done" cannot hold the machine awake forever, and re-evaluate on resume.
- Caution: this launches a (tiny) OS child process — not an agent, so ADR-0004 is untouched; but the verify-daemon/guard hooks forbid killing by name in implementer runs; implement with exact PIDs. Mention in a design note that the daemon currently decides to hold nothing; "agent working" is not knowable to Droidyard except via its own in-flight operations (agent loop is external).
- Capability: human-only toggle + status; optionally surface `awake: true` in an existing status payload.
- ADR touched: possibly a new ADR (a new host side effect). Size: small-medium (assertion helper ~100 lines + lease + UI toggle; macOS and Linux paths testable with an injected spawn, as Orca does).
- Open questions: should "agent working" ever count (cannot be known)? Does the owner want `-d` (display) as well? On macOS laptops lid-close sleep is not prevented by `caffeinate -s` on battery [inferred; Orca's docs do not claim lid-close on macOS].

---

## OPEN — investigate when building

1. Orca "Archive" semantics: flag only or removal? (`isArchived` setter not found.)
2. Whether Orca's Setup-terminal runner has any timeout; only the in-process `runHook` has 120 s (`hooks.ts:24`).
3. Exact decision order inside `branchHasNoUnmergedChangesWithLazyTargetRefresh` (`src/shared/git-branch-cleanup.ts:256-330` not read).
4. Orca's diff-comment line re-anchoring algorithm (docs promise it; not found) and which UI (combined diff vs editor diff vs source-control notes) deletes vs keeps sent notes (DOC/CODE MISMATCH, section 3.1).
5. Whether text is prefixed with an instruction when launching an agent with the notes (`openAgentSendPopoverTargetMode` / PTY write path not read).
6. Per-line attribution: not found anywhere; docs only. Run the app to confirm or ask upstream.
7. Amend + "confirm if already pushed": documented, no implementation found (`commit-changes.ts` is `git commit -m` only). Search native/mobile/cloud dirs.
8. Hosted-review (PR/MR) creation code path, stacked PRs, Bitbucket/GitLab differences: only docs read.
9. Resource Manager: whether disk scans ever run on a timer; where the sidecar snapshot's TTL is enforced; per-session memory attribution for remote hosts.
10. Activity feed persistence across restarts and its storage format.
11. Orca watcher/poller cadence for externally created worktrees (`worktree-base-directory-*` and `worktree-git-common-*` files).
12. Claude Code `.worktreeinclude` details unverified: behaviour when the file is in a worktree rather than main root; whether it is read from the main checkout only [web page says "project root"]; exact gitignore negation support (gitignore syntax implies `!`).
13. Droidyard side: current wording of ADR-0082 §2, ADR-0095 §3 and ADR-0004 was taken from the task brief, not re-read; check before writing the design note. Also confirm the MCP byte budget cannot absorb even one optional field.
14. Orca `git worktree lock` use: Orca refuses removal of locked worktrees (`assertWorktreeUnlockedForRemoval`) but I did not read whether Orca itself ever locks (Claude Code does lock while an agent runs [web]); a Droidyard inventory should show "locked (reason)" because Claude Code agent worktrees will carry a lock.
