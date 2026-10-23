# ADR-0066: The daemon's git: `git.watch` and `git.read`, human-only, and a hardened invocation

- Status: Accepted
- Date: 2026-10-12
- Supersedes: ADR-0015 §Decision (in part: item 5's bullet "Git runs on an untrusted root with the repository's executable configuration neutralised")
- Supersedes: ADR-0015 §Decision (in part: item 5's restricted-mode bullet, that `git.*` works on an untrusted root; it does unless the root's git configuration cannot be neutralised, §3 here)
- Superseded in part by: ADR-0069 (§Decision)
- Superseded in part by: ADR-0071 (§Decision)
- Superseded in part by: ADR-0125 (§Decision)
- Relates to: ADR-0015 §1, §2; ADR-0010; ADR-0049; ADR-0060 §2; ADR-0067; plan m2l Q1
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated. The owner set the frame on 2026-10-12: read-only, working tree against HEAD with the base a parameter, git not offered to agents, and the UI's reads never taking `.git/index.lock`.

## Context

`mvp.md` planned `git.status`, `diff`, `log` and `branches` as a thin wrapper over the git CLI for the human UI. Nothing said how the UI learns of an agent's commit: `file.watch` excludes `.git/` (ADR-0049), and in a linked worktree `.git` is a file. ADR-0015 §5 neutralised fsmonitor and hooks on an untrusted root but said nothing of repository filters, which are commands too, nor of a partial clone's lazy fetch, which runs the repository's `core.sshCommand` from a plain `status` or `cat-file` and writes into `.git`. The agent runs its own git beside the daemon's, so a daemon read that took `.git/index.lock` would fail the agent's commit.

Step 1 of plan m2l measured, on git 2.51.2 (Homebrew) and 2.50.1 (Apple), with tests in `internal/domain/git/run_test.go`: `--no-optional-locks` leaves a stat-dirty index unwritten, and a plain `git status` rewrites it; an empty `clean`/`smudge`/`process` override with `required=false` stops a repository filter, a process filter and a `required` driver's fatal exit; the same holds for a driver defined only in the global config; an empty driver name (`[filter ""]`) and a driver listed past the output cap ran until the reading failed closed; a partial clone's lazy fetch ran the repository's `core.sshCommand` until `GIT_NO_LAZY_FETCH=1` (git 2.45 and later) stopped it; on an older git `protocol.allow=never` alone does not, since the repository's `protocol.ssh.allow = always` overrides it, and `GIT_ALLOW_PROTOCOL=none` does; `status --find-renames` runs no `diff.<d>.textconv`; a group kill, and not a pid kill, leaves no filter child behind. The verifier's attacks are kept as tests. The status command took p50 18–43 ms, p95 under 90 ms with every tracked file stat-dirty, on the sample and on a clone of the owner's project; 23 ms on a generated 5,000-file repository.

## Decision

1. **Two capabilities**, domain `git`, workspace-scoped, audience `human`, never MCP tools (ADR-0060 §2: every agent has git), answering `GIT_NOT_FOUND` when git is missing:
   - **`git.watch`**, a streaming command as `file.watch`. It runs the status once a tick (1 s, ticks never overlapping) and publishes on its own ring-less `git.changes` subscription a `status` event only when the status differs from the last one sent, and `stopped {workspaceClosed | failed}`. Because the status is git's own, HEAD, the index, refs, packed refs and linked worktrees are all covered, and `.git/` stays out of `file.watch`.
   - **`git.read`**, a query: a path's text at the base, with `exists`, `binary` and `tooLarge` (over 512 KiB, the editor's save limit).
   - Both take `base`, an enum holding only `HEAD`, absent meaning HEAD. Commits and branches later widen the enum and break nothing.
   - No `git.status` query and no `git.diff`: the watch carries the status, and the browser computes the diff (ADR-0067).
2. **Every git the daemon runs** is `git --no-optional-locks -c core.fsmonitor=false -c core.hooksPath=/dev/null`, with the daemon's environment minus `GIT_*` plus `GIT_OPTIONAL_LOCKS=0`, `GIT_TERMINAL_PROMPT=0` and `LC_ALL=C`, plus `GIT_NO_LAZY_FETCH=1`, `GIT_ALLOW_PROTOCOL=none` and `-c protocol.allow=never`, started in its own process group, its whole group killed on cancel or after 10 s, stdout capped at 4 MiB. fsmonitor is off on every root, because `true` would start a daemon Droidyard does not own; fetching is off on every root, because the daemon never fetches and a fetch writes `.git`.
3. **On a root that is not trusted**, read before every invocation (each read, each tick; ADR-0015 §2), every configured filter driver in any scope is overridden empty and not required. The reading fails closed: a `filter.*` key it cannot map to a driver it can override (an empty or unwritable name), or a list cut by the output cap, refuses the call with `WORKSPACE_UNTRUSTED` (trusting the root is the way on) rather than leaving a driver running. So on an untrusted root a stat-dirty Git LFS file shows as modified.
4. **Codes**: `GIT_NOT_FOUND`, `GIT_NOT_A_REPOSITORY`, `GIT_FAILED` (cause: git's first stderr line); `FILE_OUTSIDE_ROOT` (containment is the file domain's rule, applied lexically, before git starts), `FILE_WRONG_TYPE`, `REQUEST_INVALID_PARAMS`.
5. **Marks against HEAD**: `modified`, `added`, `untracked`, `deleted`, `renamed` (with `origPath`), `conflicted`, mapped from porcelain v2. A rename in either column (`R.`, `RM`, and `.R`, an intent-to-add file equal to a deleted one) is `renamed`; `RD` is its origin `deleted`; `AD` is nothing; a `D.` with an untracked file at the same path is `modified`. A plain `mv` is git's own `deleted` plus `untracked`. The table lives in the status parser (plan m2l step 2).
6. **No untrusted mark**: paths and text are project content (ADR-0015 §1); the UI renders them as text.

## Consequences

- One `git status` per second per open project tab, more while files stay stat-dirty, since the daemon never refreshes the index.
- A config change between the driver read and the git that follows (a `.git/config` write, an `includeIf "onbranch:"` across a checkout) is a window, a stated limit.
- The guard is a list of what git executes on a read, kept by tests that run each attack against real git; a git that grows a new executable read path is not covered until a test names it.
- On a trusted root a filter child that outlives a git that exited 0 is the user's own filter, and is left to finish.
- A repository created after the watch started is not noticed until the UI's Retry; submodules are ignored.
- `mvp.md`'s git row becomes `watch`, `read`, `log`, `branches`.

## Alternatives considered

- **`.git/` in `file.watch`**: the object store churns into the tree's stream, and a linked worktree's `.git` is a file.
- **Stat HEAD, the index and refs, then a `git.status` call**: a layout that varies (packed refs, worktrees), an operation per refresh, and working-tree edits still need a second signal.
- **The UI polls a `git.status` query**: one recorded operation a second.
- **go-git**: a new dependency whose status ignores filters and index extensions; `mvp.md` says the git CLI.
- **Override only repository-scoped drivers**, keeping the user's LFS on an untrusted root: rests on `--show-scope` and include semantics; rejected for now.
- **Refuse an untrusted root that configures a filter**: kept as the fallback had the override not held; step 1 showed it holds for every driver it can name.
- **Refuse any untrusted root whose config holds a key outside an allowlist**: the strictest fence, but most real repositories (LFS, `pull.rebase`, user settings through includes) would need trusting before the Git tab shows anything.
- **A daemon `git.diff`**: ADR-0067.
