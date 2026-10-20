---
name: work-board
description: Use at the start of a fresh Droidyard session, and whenever the owner asks what is in flight, what can start now or in parallel, or to see the board. Says how to survey what other sessions are working on (lanes), pick work that does not collide with them, claim it in its own worktree, close it in turn, and publish the board page.
---

# Work board

The owner runs several Claude Code sessions at once, one per piece of work, each in its own worktree. This skill is how a session learns what the others are doing and takes its own lane without colliding. `board.go` (this directory) is the tool; run it from any worktree of the repository with `go run .claude/skills/work-board/board.go <command>`.

- **The record** is read from `main`: the register's `Work` column (`docs/roadmap/known-limits.md`, its intro defines the values) and the plans index.
- **Lanes** are the in-flight work, one per session, in `<git common dir>/droidyard-lanes`: shared by every worktree, never committed, edited only through the tool. A lane names its id (a milestone such as `2p`, or a slug such as `fix-init` for work without a plan), its branch, worktree, what it touches, the ADR numbers it reserved, and its state (`working`, `closing`).

## 1. Survey (a fresh session's first act)

Run `board.go status` and tell the owner, briefly and in Spanish:

- what is in flight (each lane: what, state, commits ahead, plan status) and anything marked PROBLEM or UNTRACKED;
- what could start now: the plans index's open plans, then the `Work` groups by size, each with whether it collides with a lane (§Touches);
- the next free milestone id.

Recommend one, and wait for the owner's choice: starting work is theirs.

## 2. Gather (before the plan)

For the chosen work, list the open register rows it could close on the way: its own `Work` value, plus the rows of the same Area with an empty `Work` or `quick`. Show them to the owner as a short list (one line each), and the ones they keep go to the planner's brief as in scope. A row a milestone means to close gets its `Work` set to the milestone id in the plan's first commit.

## 3. Claim

`board.go claim <id> <touches> "<what>"` creates branch `m<id>` (or the slug) from `main` and its worktree under `.claude/worktrees/`, and records the lane. Then switch the session into it with `EnterWorktree` (`path`: the worktree the tool printed), and follow `milestone-workflow` from there; every command and edit happens in the worktree. `board.go adr <id>` reserves the next ADR number whenever the work needs one; never take a number from `docs/adr/` alone. Republish the board (§5).

The main tree stays on a clean `main` while any lane is open: it is where merges land, not where work happens. A worktree has no `web/node_modules`; run `pnpm -C web install --frozen-lockfile` there before the first web commit.

## Touches

| Touch | Means | Rule |
| --- | --- | --- |
| `devices` | adb, an emulator or a phone | shared (owner, 2026-10-17): each lane boots its own emulator and acts on its serial only, stops only what it started, never runs `adb kill-server` or `adb start-server` (the adb server is everyone's), and asks the owner before using a phone |
| `contract` | `schema/capabilities.json` and the API version | the second to close merges `main`, regenerates the schema and bumps the version once more |
| `web` | `web/` | conflicts are textual; merge `main` early when both touch one component |
| `docs` | area documents and the README's status line | the second to close rewrites its status line over the first's |
| `git`, `build`, `process` | those domains and the process layer | meeting is a warning, not a block |
| `release` | CI, the release workflow, the installer | one at a time in practice |

The register is touched by almost every close; its rows are ordered by Area, so two lanes in different Areas rarely conflict. Usual touches: C3 and C5 `devices,contract,web`; C2 `contract,web,process,git`; Git work `git,contract,web`; the UI review `web`; `v1-device` and `v1-sdk` `devices,contract`.

**Each session runs its own daemon** for checks, through `verify-daemon`'s isolated launch, and its own fixtures under `~/Library/Caches/droidyard-dev/<id>/`. Never the owner's daemon, never another lane's.

## 4. Close, in turn

1. `board.go closing <id>`: refused while another lane is closing; wait for it.
2. In the worktree: `git merge main`, resolve, rerun `go test ./...` (and `pnpm -C web run verify` if `web/` changed), regenerate the schema when `contract` met another lane.
3. The close commit, as `milestone-workflow` step 6 says.
4. Merge from the main tree: `git -C <main tree> merge --no-ff <branch>`, push `main`, watch `main`'s CI.
5. `ExitWorktree` with `action: "keep"`, then `board.go release <id>`: it removes the worktree, the branch and the lane, and refuses an unmerged branch.
6. Republish the board (§5).
7. Free the disk once `main`'s CI is green (owner, 2026-10-20: the Mac is short of space): delete the lane's fixtures (`~/Library/Caches/droidyard-dev/<id>/`), its `/tmp` runs and the binaries and logs in the session's scratchpad, run `go clean -cache` (other open lanes rebuild once), and tell the owner what was freed.

Abandoning work is the owner's call: then remove the worktree and the branch by hand, and `board.go release <id>` drops the lane.

## 5. The board page

`board.go page <scratchpad>/board.html` writes the page; publish it with the Artifact tool to its fixed URL, https://claude.ai/artifact/D77M8VWETF3fwMkWzF8ZkF (pass it as `url`; a publish refused for an unread artifact is answered by reading it and publishing the regenerated file, which replaces it whole). Republish at every claim and every close, and when the owner asks. The page's labels are in Spanish, for the owner; its template is `board.html` in this directory.
