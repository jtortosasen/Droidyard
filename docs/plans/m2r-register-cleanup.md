# Plan: Milestone 2r — Register cleanup: the quick rows, a block of fixes without devices, and an audit of the open rows

- Status: Approved
- Approved: 2026-10-18, Delegated (the main session under the owner's standing instruction to apply the recommended option): the block of rows and ADR-0090 to ADR-0093 as drafted; review round 4 asked for one fix, applied without a fifth round. The owner may revert any.
- Date: 2026-10-18
- Milestone: the owner, 2026-10-18: "Hagamos la tanda quick, y de paso mira todas las líneas pendientes del known-limits a ver si puedes aprovechar y meter un buen bloque de tareas, y limpiamos." The seven `quick` rows, a block of other open rows fixable without devices, and a pass over the whole register. The block was chosen by the main session under the owner's standing instruction (Delegated); `docs/README.md` §Where the work stands says "next, the owner's choice".
- Phase: none. The MVP is done; the register rows below are the requirement.
- Lane: `2r`, branch `m2r` from `main` at `f52803d`, touches `process,build,docs,release` (`build`: step 9's Gradle root; `release`: step 10's `ci.yml`). Lane 2q (`m2q`, Approved) holds `devices` and edits `internal/workspace/{watch,workspace,capabilities,enter}.go`, `internal/capability/{operation,descriptor,error,registry}.go`, `internal/daemon/{daemon,hello}.go`, `internal/policy/policy.go`, `adapters/mcp/mcp.go`, `cmd/droidyard/{serve,client,hook}.go`, `internal/assistant/observe.go` (`DecodeAfterEvent`), `internal/domain/git`, `web/`, and in the register three rows' `Work` and `liveBudget`. No step here edits those files; the expected merges are the register and `liveBudget` at the second close, where 2q's hook-root ADR (on `m2q`, superseding ADR-0062 §Decision in part) is noted beside step 3, which it does not affect.
- Contract: no change. `schema/capabilities.json` and the API version stay as they are. ADR-0090, ADR-0091, ADR-0092, ADR-0093 (Delegated, the main session, 2026-10-18); ADR-0021 §Pid records; ADR-0037 §Decision 2; ADR-0054; ADR-0056; ADR-0061 §3; ADR-0062 §1–§3; ADR-0063 §4; ADR-0068 §3; ADR-0075 §8; ADR-0020 §Core primitives; `files.md` §The capabilities, §The watch; `workspace-state.md` §Lifecycle, §Cleanup; `process-lifecycle.md` §Kinds of processes.

**The owner should see:**
- Six of the seven `quick` rows are fixed: `agent.recordCommand`'s missing `words`, the folder dialog's pid record, the leftover temporary files (swept after a day by the watch, never an aside, which now has its own name: ADR-0091), and `init`'s stale hook entries and retired allow rules (ADR-0090; Cursor's empty `deny` stays, as decided). The seventh, the error message in the trace line, stays out: no rule says which messages hold no project or device text.
- The block adds: four load-sensitive tests made robust, a watch that notices the project deleted or moved, `init` writing a custom state root into Cursor's entry, `droidyard trash` giving up on a daemon that hangs, a Gradle root you switch away from releasing its Gradle connection once its build ends (ADR-0092), and the race detector in CI as a job of its own (ADR-0093; no race found today).
- The audit closes, with evidence, the open rows that later work already made untrue. It rewrites no row and closes nothing on opinion.

## Scope

| Register row (line on `main` at `f52803d`) | Here? | Where, or why not |
| --- | --- | --- |
| 277 `TestSpawnRecordedKillsTheShellCommandsGroupWhenTheRecordWriteFails` flaked under load | yes | step 1 |
| 278 `lock_test.go:110` and `TestServeStartsThroughAProbe` flaked under load | yes | step 1 (`lock_test`); `TestServeStartsThroughAProbe` counts only conclusive attempts since `ddd6c62` (2026-10-15), cited at the close |
| 270 the folder dialog is spawned unrecorded (`quick`) | yes | step 2 |
| 210 `droidyard trash`: no deadline; no `command rm` line after a transport failure | yes | step 3 |
| 160 watching the root never reports `pathGone` | yes | step 4 |
| 199 Cursor's server entry needs a custom `DROIDYARD_STATE_DIR` by hand | yes | step 5 |
| 211 `agent.recordCommand` accepts a command whose `words` is missing or `null` (`quick`) | yes | step 6 |
| 202 old `file_list`/`file_read` allow rules stay (`quick`); 205 a changed binary path leaves a stale hook entry (`quick`) | yes | step 7 (ADR-0090 §1, §2) |
| 201 `init --uninstall` leaves Cursor's `"deny": []` (`quick`) | yes, as decided | ADR-0090 §3: closed at the close, folded into 200 |
| 200 uninstall cannot tell a user's identical element from init's | no | ADR-0090 §3 accepts it: stays open, an accepted limit |
| 166 + 163 leftover `.droidyard-*.tmp` never swept (`quick`); a crash leaves the temporary file | yes | step 8 (ADR-0091) |
| 39 a Gradle root switched away from keeps its connection | yes | step 9 (ADR-0092) |
| 251 `go test -race` runs neither in CI nor in the hook | yes | step 10 (ADR-0093); closed once the job passes on `main` |
| Every open row, against the code and the Done plans | yes | step 11 |
| 284 the trace line gains the error's message (`quick`, the owner's idea) | no | Not in this milestone |
| 204, 164, 168 | no | Not in this milestone |

## Starting point

`main` at `f52803d`, API `0.27.0`, ADRs to 0080 (lane 2q holds 0081–0089). On `m2r`: `db0a3f9`, 2q's gap-tolerant doccheck (`0e060f4`) cherry-picked, so ADR-0090 to ADR-0093 can sit above 2q's unmerged numbers; ADR-0090 to ADR-0093, their index rows, the superseded ADRs' header lines and this plan's index row, written by the main session and not yet committed (doccheck passes with them).

- **277.** `internal/process/deathsignal_test.go:44`: `eventually(…, 2*time.Second, …)` for the background child and the group; `dead` is `kill(pid, 0) == ESRCH`, so an orphan zombie not yet reaped by launchd reads as alive; `beforeRecord` waits at most 5 s for the pid file.
- **278.** `internal/daemon/lock_test.go:109`: the reader must read at least 10 times while the writer makes exactly 1000 rewrites. `go test -cpu 1 -count=50 -run TestSetHTTPNeverShowsAnEmptyLock ./internal/daemon` fails on `main` ("the reader read the lock only 1 times across 1000 rewrites", planner, 2026-10-18).
- **270.** `internal/workspace/dialog.go` `choose` runs osascript through `Dialog.Spawn`, the supervisor's unrecorded `Spawn` (`cmd/droidyard/main.go:400`; `dialogSeam` swaps the binary in tests, `sidecar_test.go:139`), for up to 5 min, killing its group on a cancel or the bound. `process.SpawnRecorded(cmd, kind)` records a child in `processes.json` until its `Recorded.Wait`; `Reconcile`'s `default` branch reaps any matching record but `emulator-launch` by its group, so a new kind needs no `RecordsVersion` change, and an older binary reaps it alike.
- **210.** `cmd/droidyard/trash.go` `trashCmd` dials twice through `connectOrStart` (no deadline; `connectWith`'s hello can block), opens the workspace, then runs `file.delete` (long-running) as one `cli.Call`: a start with `waitMs 0`, then `operation.wait` on the same connection. A transport error after the start exits 1 without the `command rm` line (line 195). `cli.Call{Detach: true}` returns at the first answer with the operation id; `operation.wait` without `timeoutMs` waits without bound.
- **160.** `internal/domain/file/watch.go` `tickOnce` walks `w.rel` through the `os.Root` opened on the canonical root (`file.go` `openRoot`); the descriptor keeps an unlinked or moved directory readable, so a deleted project reports its entries deleted once and polls on (m1j §Changes, close).
- **199.** `internal/assistant/assistant.go` `plan` builds one `serverSpec` (`stdioServer{command, args}`) for `.cursor/mcp.json` and `.mcp.json`. `Options.StateDir` (`cmd/droidyard/init.go:153`, from `DROIDYARD_STATE_DIR`) already reaches the hook command inline (`hookentry.go` `HookCommand`, ADR-0061 §3). Cursor honours an `env` object in the entry: m1o's kit added `env: {DROIDYARD_STATE_DIR}` for its runs and `cursor-agent mcp list` showed droidyard ready ([obs] m1o §Changes 2026-10-09 (2), step 9).
- **211.** `internal/domain/agent/agent.go` `validate` lets a nil `Words` (absent or `null`) through and normalises it to `[]`; the core's `decodeParams` (`internal/capability/operation.go:1100`) checks `required` on top-level fields only; the hook always sends `[]` (`cmd/droidyard/hook.go` `commandRecord`).
- **CI.** `.github/workflows/ci.yml` runs on pushes to `main` and on pull requests the hook's checks over the tree, without `-race` (ADR-0068 §3; plan m2m step 1 "Not here"), in a 20-minute job.
- **Baseline, 2026-10-18 at `f52803d`, on a host other sessions were loading:** `go vet ./...` clean. `go test ./...` red in three packages over three runs, each a timing bound; each test passes alone (`-count=3`, `-cpu 1 -count=5`):
  - `internal/domain/shell` `TestRunTimesOutAndKillsTheGroup` (twice): `signal = "SIGTERM", want SIGKILL` and no `pid` file, at 1.03 s: the 1 s timeout's SIGTERM landed before the shell ran `trap '' TERM`.
  - `internal/workspace` `TestChooseRootBoundDismisses` (once, 7.02 s, message not captured): the 2 s bound plus `waitGone`'s 5 s (`dialog_test.go:84`, `gone` is `kill(pid, 0) == ESRCH`).
  - `internal/domain/git` `TestHistoryFence` ("git did not finish in time") and `TestWriteSpike3NoGcOutlivesAFetch`, once each: lane 2q's package.
  - `go test -race -timeout 40m ./...`: every package ok, no `DATA RACE` (device 536 s, git 292 s). A first run with the default 10-minute timeout timed out in `internal/domain/git` (602 s) under heavier load.
  - This plan's row in `docs/plans/README.md` was added by the main session.
- **ADR-0090 to ADR-0093's areas.** `internal/assistant/edit.go` merges arrays by equality only (`mergeInto`, `uninstallJSON`); `hook.go` `splitWords` parses `shellArg`'s quoting. `trash.go:47` `asidePattern` equals `write.go:395`'s temporary name; `watch.go:378` `tempWatchName` hides both from the walk; `trash.go:59` removes through `os.Root.RemoveAll`. `internal/workspace/workspace.go` keeps a growing set of resolved roots per workspace (`NoteGradleRoot`, `:362`), read by `degradeOne` and `releasableRoots`; `gradle.Client.Close` (`client.go:298`) takes the root's lock, and the next call reopens a cleared connection (`client.go:442`); the fake sidecar logs `opened`/`closed <root>` and can hold a build until a file exists. `release.yml:32` calls `ci.yml`.
- **Words.** The live documents stand at 27,967 words, `liveBudget` 27,967; 2q raises the constant on its branch.

## Acceptance

- [ ] A1. The flaky tests of rows 277 and 278 (`lock_test`) and the baseline's two load failures outside 2q's packages pass repeatedly on a loaded host, each still failing when the decision it guards is reversed, with the reason for its bound or retry in the test. Source: rows 277, 278; the baseline. Proven by: step 1.
- [ ] A2. While the folder dialog is up its process is in `processes.json` as kind `folder-dialog`; the record is gone after an answer, a cancel, the bound or a call's cancel; a daemon killed with SIGKILL during a dialog leaves a record the next daemon's reconcile reaps, group included. Source: row 270; ADR-0021 §Pid records. Proven by: step 2.
- [ ] A3. `droidyard trash` against a daemon that accepts and never answers exits 1 within its bound, says nothing was moved and prints the `command rm` line; after a transport failure or a hang once the delete was sent it says the outcome is unknown and prints the line; a delete longer than the bound completes. Source: row 210; ADR-0062 §2. Proven by: step 3.
- [ ] A4. A `file.watch` of any path stops with `pathGone`, after reporting its entries deleted, when the project directory is deleted, moved away or replaced. Source: row 160; `files.md` §The watch. Proven by: step 4.
- [ ] A5. `droidyard init --client cursor` with `DROIDYARD_STATE_DIR` set writes it into `.cursor/mcp.json`'s droidyard entry as `env`; without it the entry is as today; `--uninstall` under the same variable removes the entry. Source: row 199; ADR-0037 §Decision 2; ADR-0061 §3. Proven by: step 5.
- [ ] A6. `agent.recordCommand` refuses a command whose `words` is absent or `null` with `REQUEST_INVALID_PARAMS` naming the field, accepts `[]`, and the published schema is unchanged. Source: row 211; ADR-0063 §4. Proven by: step 6.
- [ ] A7. Each row the audit closes carries `Closed by` `2r (audit: <evidence>)` with a file and line, a test or a commit that the close verifier reproduced as showing its whole claim untrue; the evidence states, per area, the rows read, closed and given a `Work` value; the register tests pass. Source: the owner's "y limpiamos". Proven by: step 11.
- [ ] A8. A later `init` replaces a droidyard hook entry of init's shape whose binary path changed instead of adding a second; `--uninstall` removes every such entry and the retired `file_list`/`file_read` allow rules; entries of another root, client or shape, and Cursor's empty `deny`, are left. Source: ADR-0090; rows 202, 205, 201. Proven by: step 7.
- [ ] A9. A `file.watch` tick removes a non-aside `.droidyard-<16 hex>.tmp` file or folder older than 24 h, a link as a link; it never removes an aside (new name, or old name recorded by any workspace's trash under this state root) or a younger one; new asides are `.droidyard-aside-<16 hex>.tmp` and old-named entries still restore. Source: ADR-0091; rows 163, 166. Proven by: step 8.
- [ ] A10. A workspace that resolves a new Gradle root (or none) forgets the old one; the old root's connection is closed once its in-flight operation ends, unless another open workspace remembers it; a sidecar crash degrades only the workspaces whose current root crashed. Source: ADR-0092; row 39. Proven by: step 9.
- [ ] A11. CI's `race` job, `go test -race -timeout 30m ./...` on Linux, passes on the branch's pull-request run; a tag release does not run it, and the pre-commit hook is unchanged. Source: ADR-0093; row 251 (closed when the job passes on `main` after the merge). Proven by: step 10.

## Steps

### 1. A green baseline: the load-sensitive tests made robust

- Builds: tests only; each change carries its reason in a comment.
  - `internal/daemon/lock_test.go` `TestSetHTTPNeverShowsAnEmptyLock`: the writer rewrites at least 1000 times and keeps rewriting until the reader has read at least 200 times, within a 30 s deadline (passing it is a failure naming the reads), yielding (`runtime.Gosched`) after each rewrite; the final assertion (`lock_test.go:114`) compares with the last port written, no longer a fixed `11000`. Why: with one CPU or on a loaded host the 1000 rewrites end before the reader is scheduled, and the property is "never empty", not a rate.
  - `internal/process/deathsignal_test.go` `killsTheGroupWhenTheRecordWriteFails`: both `eventually` bounds and the `beforeRecord` wait 10 s. Why: a killed orphan stays a zombie until launchd reaps it and `dead` reads a zombie as alive; load delays both. `eventually` returns as soon as the condition holds.
  - `internal/workspace/dialog_test.go` `waitGone`: 10 s, the same reason (most likely `TestChooseRootBoundDismisses`'s failure; if a run beside a full `go test ./...` shows another message, the cause goes under Changes before any edit).
  - `internal/domain/shell` `TestRunTimesOutAndKillsTheGroup`: an attempt counts only when the `pid` file exists (written after the `trap`, so the SIGTERM was ignored as the test assumes); otherwise it is made again, at most five times, then skipped as inconclusive, as `TestServeStartsThroughAProbe` does since `ddd6c62`.
- Implements: rows 277, 278 (the `lock_test` half); the baseline's two failures outside 2q's packages.
- Check:
  - `go test -cpu 1 -count=50 -run TestSetHTTPNeverShowsAnEmptyLock ./internal/daemon` fails on `main` and passes after the step.
  - Reversals, through `go test -overlay`: `SetHTTP` truncating before it writes fails `TestSetHTTPNeverShowsAnEmptyLock` at the default GOMAXPROCS; a `sleep 2;` before the `trap` makes `TestRunTimesOutAndKillsTheGroup` skip, not fail (the retry path is reachable); a shell command's leader killed by its pid fails `TestSpawnRecordedKillsTheShellCommandsGroupWhenTheRecordWriteFails`; the bound's group kill removed fails `TestChooseRootBoundDismisses`; the timeout's SIGKILL removed fails `TestRunTimesOutAndKillsTheGroup` (a conclusive attempt, not a skip).
  - `go test ./...` twice, each beside another `go test ./...`: none of the four fails.
- Schema: no change.
- Needs: nothing.
- Not here: `TestReaderClientSerialisesPerDeviceAndNotTwoDevices` (row 98, device area, 2q's touch); `TestHistoryFence` and `TestWriteSpike3NoGcOutlivesAFetch` (`internal/domain/git`, 2q's package): one new register row at the close (unverified, process) with the messages above.
- Rests on: —
- [x] Done. Evidence: level 4, 2026-10-18.
  - The lock test under one CPU: `go test -cpu 1 -count=50 -run TestSetHTTPNeverShowsAnEmptyLock ./internal/daemon` passes twice (152 s). The original test, through an overlay, fails every run ("the reader read the lock only 0 times across 1000 rewrites").
  - Reversals through `-overlay`:
    - `SetHTTP` truncating first fails 3 of 3 runs at the default GOMAXPROCS ("the reader saw an empty daemon.lock after 670 reads").
    - A `sleep 2;` before the `trap` skips with "inconclusive" after 5 attempts.
    - Killing the leader by its pid fails the record-write test ("not within 10s: the background child … gone"), in both kinds.
    - The bound's group kill turned into a pid kill fails `TestChooseRootBoundDismisses` ("what the dialog forked … is still running").
    - Removing the timeout's SIGKILL fails `TestRunTimesOutAndKillsTheGroup` on a conclusive attempt (`signal = ""`, the group survived).
  - `go build`, `vet`, `gofmt -l` clean; `go test ./...` ok.
  - Two `go test -count=1 ./...` runs at once: none of the four fails. Five other tests failed under that doubled load, recorded under Changes.

### 2. The folder dialog has a pid record

- Builds:
  - `internal/process/reconcile.go`: `FolderDialog Kind = "folder-dialog"`, documented as recorded for the life of a `workspace.chooseRoot` dialog, with the death signal (`deathSignal` unchanged), reaped by Reconcile's default branch; `RecordsVersion` stays 2.
  - `internal/workspace/dialog.go`: `Dialog.Spawn` becomes `func(cmd *exec.Cmd) (wait func() error, err error)`; `choose` reaps through the returned `wait` (which removes the record); `takeDown` still kills the group first. A spawn refused because `processes.json` cannot be written is `WORKSPACE_DIALOG_FAILED` with that cause (ADR-0056 §Decision 3: under a newer `processes.json` no recorded child starts, only unrecorded short commands; a dialog is now a recorded child).
  - `cmd/droidyard/main.go:400`: `NewDialog` gets a function over `sup.SpawnRecorded(cmd, proc.FolderDialog)` returning the `Recorded`'s `Wait`; the doubles in `dialog_test.go` and `start_test.go` take the new signature.
  - `process-lifecycle.md` §Kinds of processes: the per-operation row names the folder dialog (kind `folder-dialog`), with words cut in the same row so the count does not grow.
- Implements: row 270; ADR-0021 §Pid records ("every long-running child"); `process-lifecycle.md` §Crash recovery.
- Check:
  - `TestDialogIsRecordedWhileItRuns` (`internal/workspace`, a fake spawn): the dialog reaps through `wait` on an answer, a cancel, the bound and a call's cancel (seen failing with `cmd.Wait` kept).
  - `TestFolderDialogRecordAndReap` (`cmd/droidyard`, a real `serve`, `dialogSeam` pointing at a script that writes its pid, forks a child and sleeps): during `workspace.chooseRoot`, `processes.json` holds one `folder-dialog` record with that pid; after the call is cancelled, none (both platforms). Then, on darwin only, a second dialog, the daemon killed with SIGKILL, a new `serve` on the same state: its reconcile log names the record reaped, and the script and its child are gone (seen failing on `main`: no record, both survive). On Linux that half is `t.Skip`ped citing Pdeathsig, as `TestKilledDaemonLeavesTheFollowForTheNextStart` (`logs_follow_test.go:127`) is: the kind keeps the death signal, so the leader dies with the daemon and Reconcile drops the record as "no such process" (row 268's limit); the real dialog exists on darwin only (`Dialog.Missing`).
  - `TestReconcileReapsAFolderDialog` (`internal/process`): a `folder-dialog` record of a live `sleep` in its own group is reaped by its group (passes on `main` too: it pins that the default branch covers the kind without a version change).
- Schema: no change (only a cause text is new).
- Needs: step 1 (`dialog_test.go`'s `waitGone`).
- Not here: a real osascript panel (Unverified).
- Rests on: a dialog this daemon started is in `processes.json` before `choose` waits on it, at level 4 (the real `serve` test); that the real panel goes with its process is level 5, not run.
- Review: the Opus verifier (process lifetime), effort `high`.
- [ ] Done. Evidence:

### 3. `droidyard trash` bounds its wait on the daemon

- Builds: `cmd/droidyard/trash.go` only.
  - Everything but the wait itself (two dials and hellos, `workspace.open`, `file.delete` with `Detach: true`, which returns at the first answer, and the third connection's dial and hello) runs in a goroutine raced against `trashDeadline = 10 * time.Second` (Delegated, a working default with `DialogBound`'s precedent: each of these answers at once on a live daemon). Expired before the delete was sent: ADR-0062 §2's "no daemon" refusal (nothing moved, the `command rm` line, exit 1). Expired after: "the outcome is unknown: some paths may already be in the trash", the `command rm` line, exit 1. The process exits, and the abandoned goroutine with it.
  - "Sent" is decided by one atomic compare-and-swap: the goroutine marks the delete sent before writing its request, only if the main goroutine has not given up; the main goroutine gives up (and prints) only if it is not marked; the abandoned goroutine never prints. So "nothing was moved" is printed only when no request was written.
  - Only `operation.wait` on that third connection, without `timeoutMs`, is unbounded: a long delete is waited for as today (the client's own timeout still bounds the agent's command).
  - A transport failure during that wait prints the same "outcome unknown" line and the `command rm` line (Delegated: an operand already trashed makes `rm` report it missing; one not trashed is deleted as the agent first asked).
  - `plainRm()` unchanged.
- Implements: row 210; ADR-0062 §1 (never auto-starts), §2 (a refusal and no daemon print the plain `rm`), §3 (one line per item). The `rm` line after an unknown outcome goes beyond §2, which ties it to "moves nothing": it rests on row 210's wording and m2k step 2's evidence (the line "left (the call may have reached the daemon)"), Delegated.
- Check: in `cmd/droidyard`, the built binary as `runTrash` runs it:
  - `TestTrashGivesUpOnADaemonThatNeverAnswers`: a listener on the state root's socket that accepts and never writes → exit 1 within 15 s, "nothing was moved" and `command rm -- <operand>`, the file still there (seen failing on `main`: the test's 30 s timeout).
  - `TestTrashAfterATransportFailureHandsBackRm`: a fake daemon that answers the hellos, `workspace.open` and the delete's start, then closes the wait connection → exit 1, "outcome is unknown" and the `command rm` line (seen failing on `main`: no `command rm`).
  - `TestTrashGivesUpOnAHelloAfterTheStart`: a fake that answers the delete's start, then accepts the third connection and never answers its hello → exit 1 within 15 s, "outcome is unknown" and the `command rm` line (seen failing with the third dial outside the deadline).
  - `TestTrashWaitsForALongDelete`: the deadline lowered to 1 s, a fake whose `operation.wait` answers after 3 s → exit 0, "trashed <operand>" (seen failing with the deadline applied to the wait).
  - The fakes reuse or extend `hook_test.go`'s `fakeDaemon` (a socket server speaking the hello and the envelopes, `hook_test.go:178`); the deadline is lowered by a test-only variable the re-executed test binary reads in `TestMain` (`serve_test.go:31`), never a production environment variable (ADR-0024's overrides unchanged). The existing `TestTrash…` tests pass unchanged.
- Schema: no change.
- Needs: nothing.
- Not here: the hook's own 2 s bound (ADR-0061 §1); cancelling an abandoned delete (operations belong to the daemon, ADR-0020 §Adapter projections).
- Rests on: a live daemon answers everything the deadline covers at once (the hellos; `workspace.open`, which registers a root and runs no project code, `internal/workspace/capabilities.go:37-50`; the delete's start with `waitMs 0`, `adapters/cli` package doc), so the deadline never cuts a legitimate delete, at level 3 (the fake's long wait).
- [ ] Done. Evidence:

### 4. A watch stops when the project directory is gone

- Builds: `internal/domain/file/watch.go`: each tick first compares the root by name (`os.Stat(w.root.Name())`, the canonical root) with the root the descriptor holds (`w.root.Stat(".")`) by `os.SameFile`; missing, not a directory, or another directory ends the tick as a gone path does today (every baseline entry reported deleted, then `stopped` with `pathGone`). One `stat` and one `fstatat` per tick.
- Implements: row 160; `files.md` §The watch ("if the watched directory itself is gone … the watch stops with reason `pathGone`"), now true for the root, and for any path under it when the project moves.
- Check: `TestWatchStopsWhenTheProjectGoes` (`internal/domain/file`, real directories, a short tick); each of the first three seen failing on `main` (no `stopped` within 20 ticks): a watch of `"."` with the project removed → its entries `deleted`, then `stopped` `pathGone`; the same with the project renamed; a watch of `app` with the project renamed; and a project left in place → no `stopped` in 20 ticks. The existing `TestWatch…` tests pass unchanged.
- Schema: no change.
- Needs: nothing.
- Not here: 164's forced-cancel window (Not in this milestone); a path that reappears (m1j's recorded race, unchanged).
- Rests on: —
- [ ] Done. Evidence:

### 5. `init` writes a custom state root into Cursor's server entry

- Builds: `internal/assistant/assistant.go`: Cursor's `.cursor/mcp.json` entry gets its own spec, `{command, args, env: {"DROIDYARD_STATE_DIR": <StateDir>}}` when `Options.StateDir` is set, `{command, args}` otherwise (`stdioServer.Env`, `omitempty`). `.mcp.json` and `opencode.json` unchanged. Uninstall compares with what this run writes (ADR-0037 §Decision 2), so an `--uninstall` without the variable keeps an entry written with it and says it differs, as the hook entries already do.
- Implements: row 199; ADR-0037 §Decision 2 (the entry reaches droidyard from a client with no shell environment, as the absolute binary path does); ADR-0061 §3, ADR-0062 §4 (the state root travels with what `init` writes when init's own came from `DROIDYARD_STATE_DIR`). Delegated: Cursor only, the one client observed to rebuild the server's environment.
- Check: `TestCursorEntryCarriesACustomStateRoot` (`internal/assistant`): with `StateDir` `/s`, `.cursor/mcp.json`'s entry has `"env": {"DROIDYARD_STATE_DIR": "/s"}` and `.mcp.json`'s none (seen failing on `main`); a second run unchanged; `--uninstall` with `/s` restores the tree; without `StateDir`, `TestInitWritesEachClientsFiles`' golden trees unchanged; `--uninstall` without it on a tree written with it keeps the entry with a note. `cmd/droidyard` `TestInitHookEntriesNameTheBinaryTheRootAndTheStateRoot` (`init_test.go:415`, which sets the variable) also finds it in `.cursor/mcp.json`.
- Schema: no change.
- Needs: nothing.
- Not here: Claude Code's `.mcp.json` (not observed to lose the variable); 205's stale entries (Not in this milestone).
- Rests on: Cursor passing an entry's `env` to the server it starts, level 5 in m1o step 9, not rerun (Unverified).
- [ ] Done. Evidence:

### 6. `agent.recordCommand` requires `words`

- Builds: `internal/domain/agent/agent.go` `validate`: a command whose `Words` is nil (absent or `null`; `[]` decodes non-nil) is `REQUEST_INVALID_PARAMS`, `commands[<i>].words is required (an empty list when there is no word)`. No descriptor, tag or core change: the schema already marks `words` required.
- Implements: row 211; ADR-0063 §4.
- Check: `TestRecordCommandRequiresWords` (`internal/domain/agent`, through a registry and `Operations`, so `decodeParams` runs first): `{"program":"adb"}` and `{"program":"adb","words":null}` end failed `REQUEST_INVALID_PARAMS` naming `commands[0].words` (seen failing on `main`: completed); `"words":[]` completes with `words: []`; the hook tests pass unchanged; `go test ./internal/schema` passes without `-update`.
- Schema: no change.
- Needs: nothing.
- Not here: a nested `required` check in the core's `decodeParams` (`internal/capability/operation.go`, 2q's file); the evidence lists any other nested parameter field tagged `required` (a grep), each a candidate register row.
- Rests on: —
- [ ] Done. Evidence:

### 7. `init` recognises its own stale entries (ADR-0090)

- Builds: `internal/assistant` only (not `observe.go`, which 2q edits).
  - `hookentry.go`: `initHookShape(command string, c Client, root string) bool`: the command split with `splitWords` (`hook.go`, called, not edited) is an optional `DROIDYARD_STATE_DIR=<abs>` word, then an absolute path whose base is `droidyard`, then exactly `hook --client <c> --root <root>`; anything else (a wrapper, `sh -c`, another root or client, extra words) is not init's. A Claude element counts when its `matcher` is `Bash` and every command in its `hooks` has the shape; a Cursor element when its `command` has it and its `matcher` is the one init writes for that event.
  - `edit.go`: `arrayEntry` gains `stale func(json.RawMessage) bool`. Install removes the elements `stale` matches that differ from the element it writes, then appends what is absent (a replacement, not a second entry); uninstall removes the elements it writes and every element `stale` matches. Notes say "replaced a stale entry" and "removed N stale entries".
  - `assistant.go`: the hook arrays carry the shape test; the allow arrays carry a test for the retired rules, `retiredTools = []string{"file_list", "file_read"}` kept beside the allowlist with a comment citing ADR-0060 and ADR-0090 §2, rendered by `AllowRule`. The empty `deny` keeps its `sole` rule (ADR-0090 §3: it stays).
  - The package comment gains the shape rule (ADR-0090 Consequences).
- Implements: ADR-0090 §1–§4; rows 202, 205; row 201 closes as decided and folds into 200.
- Check: in `internal/assistant` (real files in `t.TempDir()`):
  - `TestStaleHookEntryIsReplaced`: `init` with `Command` `/old/droidyard`, then with `/opt/bin/droidyard` → each event's array holds one droidyard entry, the new one (seen failing on `main`: two), for Claude and Cursor; a `DROIDYARD_STATE_DIR=…` prefixed stale entry is replaced too.
  - `TestUninstallRemovesStaleEntries`: a tree written by `/old/droidyard`, `--uninstall` with `/opt/bin/droidyard` → no droidyard entry left, the file deleted when nothing else remains (seen failing on `main`: the stale entry kept).
  - `TestHookEntriesThatAreNotInitsAreKept`: entries for another root, another client, `sh -c '/opt/bin/droidyard hook …'`, a base name other than `droidyard` and an extra flag survive both install and uninstall (seen failing with the shape test reduced to "contains `droidyard hook`").
  - `TestRetiredAllowRulesGo`: `mcp__droidyard__file_list` and `Mcp(droidyard:file_read)` in the allow lists are removed by `init` and by `--uninstall`; a user's `mcp__other__file_list` stays (seen failing on `main`).
  - `TestUninstallRestoresTheTree` and the golden trees pass unchanged (the empty `deny` rule untouched); `--dry-run` reports the replacements and writes nothing.
- Schema: no change.
- Needs: step 5 (the Cursor entry's own spec, same files).
- Not here: the keyed MCP server entry after a path change ("kept, yours differs", ADR-0037 §2 unchanged; ADR-0090 covers the hook entries and allow rules only); row 200 (accepted). After this step an `--uninstall` without `DROIDYARD_STATE_DIR` removes the hook entries carrying the prefix (their shape) but keeps Cursor's MCP entry with its `env` (step 5), so step 5's "as the hook entries already do" no longer holds; the step's evidence says so.
- Rests on: no element a user wrote is matched by the shape test, at level 3 (the kept-entries table).
- [ ] Done. Evidence:

### 8. Leftover temporary files are swept, and asides get their own name (ADR-0091)

- Builds: `internal/domain/file` and `files.md`.
  - `delete.go`: a new cross-volume delete's aside is `asideName()`, `.droidyard-aside-<16 hex>.tmp`; `trash.go` `asidePattern` accepts that and the old `.droidyard-<16 hex>.tmp` (restore and `Sweep` resolve entries an older binary wrote).
  - `trash.go`: `(*Trash).RecordedAsides() (map[string]bool, error)`, an error whenever a trash folder or an entry cannot be read or parsed (the tick then sweeps nothing: ADR-0091 §3's skip is a hard rule), and otherwise the aside base names recorded by the entries of every workspace's trash under the state root (ADR-0091 §3: "this state root", and workspaces nest, a top and its `dev/`); the 16 random hex characters make a name match enough. It reads the entry files without taking the trash locks (an entry is written atomically and names its aside before the rename, `delete.go:256-260`), because `deleteOne` holds its workspace's lock through a whole cross-volume copy (`delete.go:194`).
  - `watch.go`: on a tick (never the baseline walk), an entry named exactly `^\.droidyard-[0-9a-f]{16}\.tmp$` whose `Lstat` modification time is more than `tempMaxAge = 24 * time.Hour` old, and which `RecordedAsides` (read only when a tick has found such a candidate) does not name, is removed with the watcher's `os.Root` (`RemoveAll`, which removes a link as a link); a failure is ignored and retried next tick; no event, no Activity row. New-style aside names never match. The entry stays hidden from events as today.
  - `files.md` §The capabilities and §The watch: the sweep in one sentence each, words cut in the same sections so the document's count does not grow (`liveBudget` is in 2q's `register_test.go`).
- Implements: ADR-0091 §1–§4; rows 163, 166.
- Check: in `internal/domain/file`, real directories, the age read through a seam (`now`) or set with `os.Chtimes`:
  - `TestWatchSweepsOldTemporaryFiles`: a 25-hour-old `.droidyard-<hex>.tmp` file and folder (with contents) are gone after one tick and no event names them; a 1-hour-old one stays (seen failing with the age test removed).
  - `TestWatchNeverSweepsAnAside`: a 30-day-old `.droidyard-aside-<hex>.tmp` stays (seen failing with the sweep matching `tempWatchName` instead of the exact pattern); a 30-day-old old-named one that a trash entry of the workspace records stays, and so does one recorded by another workspace's entry under the same state root (each seen failing with the `RecordedAsides` test removed, the second also with it reading only the watching workspace's trash); an old-named one no entry records is swept.
  - `TestWatchSweepRemovesALinkNotItsTarget`: an old symlink named `.droidyard-<hex>.tmp` pointing at an outside directory is removed and the target and its file are intact (seen failing with a removal of `filepath.EvalSymlinks(joined)`, which follows the link).
  - `TestWatchSkipsTheSweepWhenTheTrashCannotBeRead`: an unreadable or corrupt `entry.json` (or trash folder) that records an old-named, 30-day-old aside → the aside stays and nothing is swept on that tick (seen failing with read errors ignored).
  - `TestCrossVolumeDeleteUsesTheAsideName` (through the existing cross-volume seam): the aside is `.droidyard-aside-…`; `TestRestoreResolvesAnOldNamedAside`: an entry written with the old name restores and sweeps as before.
  - The existing trash and watch tests pass unchanged.
- Schema: no change.
- Needs: step 4 (same `watch.go`).
- Not here: directories no tab watches (ADR-0091 Consequences); `file.write`'s temporary name, unchanged.
- Rests on: the watch removes only a day-old `.droidyard-<16 hex>.tmp` that no trash entry of this state root records and that is not a new-style aside, never through a link, at level 4 (real files and links).
- Review: the Opus verifier (deletes files inside the user's project), effort `high`.
- [ ] Done. Evidence:

### 9. A Gradle root switched away from closes its connection (ADR-0092)

- Builds: without editing `internal/workspace/workspace.go` (2q's file).
  - `internal/workspace/gradleswitch.go` (new; its doc comment says it replaces `NoteGradleRoot`'s growing-set semantics): `(*Registry).UseGradleRoot(target, abs string) (forgotten []string, err error)`: the checks of `NoteGradleRoot` (not open → `notOpen`), then, with `r.mu` held as `releasableRoots`' comment requires, the workspace's remembered set becomes `{abs}`, or empty when `abs` is `""`; it returns, from the roots it dropped, those `releasableRoots` (called, not edited) says no other open workspace remembers. `Degrade` and `Close`, which read the same set, then follow the current root without change (ADR-0092 §1, §3).
  - `internal/domain/build/gradle/client.go` (not 2q's): `(*Client).CloseWhenFree(canonical string)`: waits for the root's lock without a deadline (ADR-0092 §2: a build or model call in flight finishes first); after the lock, the rest is `Close`'s body under a 5 s context, as `closeRoot` bounds a close today, its not-running guard included (`client.go:309-316`: it never starts the sidecar to close a connection that is gone); `Close` itself unchanged. So no holder of the root's lock is left that nothing can end.
  - `cmd/droidyard/main.go` `authorizeGradle`: `UseGradleRoot` replaces `NoteGradleRoot`, also on a resolution that gives no root (before returning its error); each forgotten root goes to `CloseWhenFree` in its own goroutine. The workspace close's 5 s `closeRoot` is unchanged.
  - `workspace-state.md` §Lifecycle's `degraded` row ("every open workspace that used the crashed Gradle root" → its current Gradle root) and §Cleanup's close sentence (a switched-away root's connection is closed once its operations end), and `process-lifecycle.md`'s matching sentence; ADR-0075 §8's keep-until-close rule is stated in no area document, so these are the sentences it changes. Words cut in the same paragraphs.
- Implements: ADR-0092 §1–§3; row 39.
- Check:
  - `TestSwitchedGradleRootCloses` (a new file `cmd/droidyard/gradle_switch_test.go`, a real `serve` and the fake sidecar, as `TestNoGradleRoot`): a trusted top with `dev/` and `tools/x/`, `gradleRoot` `dev`, `build.tasks` → `opened <top>/dev`; `gradleRoot` `tools/x`, `build.tasks` → `opened <top>/tools/x` and `closed <top>/dev` (seen failing on `main`: no `closed`). With a second open workspace at `dev` itself that has made a Gradle call, the same switch → no `closed <top>/dev`. A build held running on `dev` by the fake for longer than 5 s while the switch happens → `closed <top>/dev` only after the build ends (seen failing with `closeRoot`'s 5 s bound reused). Switching back to `dev` → `opened <top>/dev` again and tasks listed.
  - `TestCloseWhenFreeBoundsTheCall` (`internal/domain/build/gradle`, the fake with `holdOn: connection.close`): the root's lock is free again within about 5 s of the call (seen failing with the call under the lock-wait's context); with the sidecar stopped, no sidecar is spawned (seen failing without the guard).
  - `TestUseGradleRootFollowsTheCurrentRoot` (a new file `internal/workspace/gradleswitch_test.go`; `workspace_test.go`, `gradleinfo_test.go` and `cmd/droidyard/sidecar_crash_test.go` are 2q's): after `UseGradleRoot(ws, A)` then `(ws, B)`, `Degrade(A)` leaves the workspace ready and `Degrade(B)` degrades it (seen failing with the set kept growing); `UseGradleRoot(ws, "")` forgets `B` and returns it; a closing workspace → `notOpen`, nothing recorded.
  - The existing `TestSidecarCrashDegradesTheWorkspace` and close tests pass unchanged.
- Schema: no change.
- Needs: nothing (no step before it touches these files).
- Not here: `NoteGradleRoot`, left with no production caller, and the comments in `workspace.go` that describe a growing set (`:144`, `:210-211`, `:354-361`, `:383-387`) stay, since `workspace.go` is 2q's file; the close adds a register row (limit, process, `Work` empty) for removing them after both lanes merge, whichever closes second.
- Rests on: a close that lands after a switch back only costs a reconnect (`connection.open` at the next call, `client.go:442`), at level 4 (the switch-back case). Not closed by this step (Unverified): a call that resolved the old root just before the switch takes the root's lock after the close and reopens a connection no workspace remembers; it falls back to the sidecar's idle timeout, and a crash in that build degrades no workspace.
- Review: the Opus verifier (process lifetime: a connection closed under a workspace that may use it), effort `high`.
- [ ] Done. Evidence:

### 10. CI runs the race detector (ADR-0093)

- Builds: `.github/workflows/ci.yml`: a second job, `race`, on `ubuntu-24.04`, `timeout-minutes: 40`, with the existing job's pinned checkout and setup-go (same SHAs; no Node: the web build is not needed for Go tests, unless a Go test reads `web/dist`, checked by the run) and one step `go test -race -timeout 30m ./...`. `ci.yml` is also called by `release.yml` (`uses: ./.github/workflows/ci.yml`), so the job carries `if: github.event_name == 'pull_request' || github.ref == 'refs/heads/main'`, which a tag release does not meet (ADR-0093 §3): a called workflow sees its caller's `github` context (event `push`, ref `refs/tags/…`), per GitHub's reusable-workflow documentation. `.githooks/pre-commit` unchanged (§2).
- Implements: ADR-0093 §1–§3; row 251 (closed at the close once the job passes on `main`).
- Check: the job's run on the branch. CI does not run on a branch push (only `main` and pull requests), so the main session pushes `m2r` and opens a draft pull request, then `gh run watch`: both jobs green, the `race` job's log showing `-race` and every package `ok`; the run's URL under Changes. Reversal, optional and on a throwaway branch, never on `m2r`: a test with a deliberate data race turns the `race` job red. The release's dry run is unaffected (`release/build.sh` in the existing job).
- Schema: no change.
- Needs: steps 1–9 (the job runs their tests).
- Not here: `-race` in the hook (ADR-0093 §2). A race the run finds goes under Changes before any fix; one in a file lane 2q edits cannot be fixed here, so step 10 is deferred, row 251 stays open, and the race becomes a register row.
- Rests on: the tree is race-free and fits 40 minutes on the runner, at level 5 (the run itself).
- [ ] Done. Evidence:

### 11. The audit of the open rows

- Builds: `docs/roadmap/known-limits.md` only. Run by the main session (Opus), not the implementer: it checks claims against code (AGENTS.md: the implementer only writes code).
  - Every row with an empty `Closed by` (265 at `f52803d`, device areas included, by reading code only) is checked against the code at the step's commit and the Done plans after its `Recorded` milestone.
  - A row is closed only when its whole claim is now untrue: `Closed by` `2r (audit: <evidence>)`, the evidence a `file:line`, a test or a commit that shows it. A row partly true, or stale in wording but true, stays as it is. Nothing is closed on opinion or reworded.
  - An empty `Work` cell is filled only where a Done plan's Not in this milestone or an ADR names the owning work, cited in the evidence; no new kind of work.
  - Untouched: the rows steps 1–10 close (closed at the close with their evidence), every row whose `Work` is `2q` on `m2q` or `C2` (2q is the C2 lane and its step 10 edits the register), every row's order and format.
  - The evidence lists every closure, one line each, and per area the rows read, closed and given a `Work` value.
- Implements: the owner's "y limpiamos"; AGENTS.md §Where each fact lives (open items live in the register only).
- Check: `go test ./internal/doccheck` passes (`TestRegisterIsWellFormed`: the `Closed by` shape and the order); the close verifier re-opens every audit closure's evidence and reverts any that does not show the claim untrue.
- Schema: no change.
- Needs: steps 1–10 (their rows are not audited while a step changes them).
- Not here: closing a row by judgement (a "won't fix" is the owner's or an ADR's); any device run.
- Rests on: —
- [ ] Done. Evidence:

## Not in this milestone

Each row keeps its line in the register. At the close, row 284 loses `Work` `quick` (left empty), citing this section; row 200 stays open as ADR-0090 §3's accepted limit.

- **284, the error message in the trace line.** The trace never carries a message (plan m2a Q3; `overview.md` §Cross-cutting concerns); messages interpolate paths, serials and parameters, and nothing marks which are free of project or device text. Draft: an opt-in, unserialised log-safe summary on `capability.Error` that the core appends as `msg=<quoted>`, first set by the core's fixed-text errors (`internal/capability/error.go`, 2q's file now).
- **204.** Its first half (a complex `rm`, `/bin/rm`, build outputs) is ADR-0061 §2's design; its state-root half is inherent: the hook command already carries `DROIDYARD_STATE_DIR` whenever `init` saw it (`hookentry.go` `HookCommand`).
- **164 and 168.** A forced cancel ends the operation `cancelled`, which carries an error, never a result (ADR-0020 §Core primitives): 168's trash ids need a partial result on a cancelled operation (a contract change), 164's subscription a core hook releasing what a discarded result holds (`internal/capability/operation.go`, 2q's).
- **Device rows** (98 included): 2q holds `devices`; the audit reads their code only.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| The real osascript panel goes when Reconcile signals its group (SIGTERM, then SIGKILL after 5 s), as a cancel's SIGKILL takes it down (m2c §Changes) | 2 | a level-5 run: a real `chooseRoot`, the daemon killed with SIGKILL, a new one started (a window on the owner's screen) |
| Cursor still honours `env` in `.cursor/mcp.json` (level 5 in m1o, cursor-agent 2026.10.01) | 5 | a Cursor run with a custom state root (`trial`) |
| A user's own hook entry never has init's exact shape (ADR-0090 §1's premise) | 7 | reports from users; the kept-entries table covers the forms known |
| The race job fits 40 minutes on the GitHub runner and the tree has no race on Linux (macOS only, 2026-10-18) | 10 | the branch's run (step 10) |
| A tag release skips the `race` job (the `if:` reads the caller's context) | 10 | the next tag's release run showing `race` skipped |
| A call that resolved the old Gradle root before a switch may reopen its connection after the close; nobody remembers it then | 9 | a later change to the Gradle client (register row at the close if the owner wants it tracked) |
| The cause of 277's flake (a zombie's slow reaping) is inferred from the m1n note's "not within 2s"; it does not reproduce on demand (`-cpu 1 -count=10` and `-count=5` under load passed on `main`) | 1 | repeated full runs; CI on Linux |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | B1 [test] blocking: step 2's SIGKILL half fails on Linux CI (the kind keeps Pdeathsig; Reconcile drops a dead leader's record) | that half darwin-only, `t.Skip` on Linux citing Pdeathsig, as `logs_follow_test.go:127` |
| 1 | | B2 [bug] blocking: step 3's third dial and hello outside the deadline, row 210's own defect | inside the deadline; only `operation.wait` unbounded; `TestTrashGivesUpOnAHelloAfterTheStart` |
| 1 | | B3 [plan] blocking: A7's "every row checked" unprovable | A7 and step 7: closures the close verifier reproduced, plus per-area counts read, closed, given `Work` |
| 1 | | [bug] the abandoned goroutine may send the delete after the timer, making "nothing was moved" false | step 3: one atomic compare-and-swap decides "sent" |
| 1 | | [plan] rows judged not quick keep `Work` `quick` | Not in this milestone: cleared at the close, citing it |
| 1 | | nits: last port in `lock_test`; truncate reversal at default GOMAXPROCS; retry path reachable; cite row 210 and m2k step 2 for the `rm` line; exclude every `2q`/`C2` row; 2q's hook-root ADR at the merge | all taken |
| 1 | | moved-out rows, baseline, code claims, lane collisions: checked, solid | — |
| 2 | approve | B1, B2, B3 resolved; no new Important finding | — |
| 2 | | nits: step 3's Rests on names only the start; A3 omits a hang after the delete was sent | both taken |
| 3 | revise | B1 [bug] blocking: step 9's close under a context without deadline holds the root's lock and the lease forever if `connection.close` never answers | `CloseWhenFree` in `client.go`: unbounded lock wait, 5 s lease and call; `TestCloseWhenFreeBoundsTheCall`; the held build outlasts 5 s |
| 3 | | B2 [safety] blocking: step 8 read only the watching workspace's trash; nested workspaces could lose an only copy | `RecordedAsides` over every workspace's trash under the state root, lock-free, read only on a candidate; a test case for another workspace's entry |
| 3 | | B3 [test] blocking: the link mutation (`os.RemoveAll` on the joined path) cannot fail | a mutation that follows the link (`joined + "/"`, or `EvalSymlinks`) |
| 3 | | [test] the PR run cannot show a tag release skips `race`; [plan] `workspace.go`'s stale comments and `NoteGradleRoot` need an owner; [plan] a call resolved before a switch reopens a forgotten connection; [plan] a race in 2q's files | Unverified row; a register row at the close and new test file names; Rests on and Unverified; step 10 deferred, row 251 open |
| 3 | | nits: `tempWatchName` mutation; the `dev` workspace makes a Gradle call; `r.mu` held; step 7's note on step 5's phrase; cite the caller's context | all taken |
| 4 | revise | B1, B2 resolved; B3 partly: `os.RemoveAll(joined + "/")` cannot fail either (`RemoveAll` strips the slash and unlinks the link) | that mutation deleted; `EvalSymlinks` is the one; the reviewer: one line, no fifth round needed |
| 4 | | [bug] `CloseWhenFree` without `Close`'s not-running guard would start a sidecar after a crash; [safety] `RecordedAsides` failing silently could sweep a recorded only copy | the guard kept, a no-spawn case; `RecordedAsides` returns an error and the tick sweeps nothing, `TestWatchSkipsTheSweepWhenTheTrashCannotBeRead` |

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.

- 2026-10-18, step 1. Two full `go test -count=1 ./...` runs at once on this host failed five tests step 1 does not name. Each is a timing bound under a doubled load, a heavier load than the plan's single loaded run. They are not fixed here: the close adds them to the new load-sensitive tests row, with the git ones.
  - `cmd/droidyard` `TestCtrlCDuringAutoStart`: "exited before printing interrupted".
  - `cmd/droidyard` `TestCloseBoundsADrainALeaseHolderIgnores`: the close took 3.67 s.
  - `cmd/droidyard` `TestMCPSessionBindsAndOpensItsRoot`: an MCP read timed out.
  - `cmd/droidyard` `TestScreenEndpointFence`: the fake device's `getprop` took over 3 s.
  - `internal/domain/git` `TestUpdateTimeout`: five inconclusive attempts (2q's package).

- 2026-10-18, before approval (Delegated). The main session took the plan's draft decisions as ADR-0090 (rows 202, 205; 201 as decided, 200 accepted), ADR-0091 (rows 163, 166), ADR-0092 (row 39) and ADR-0093 (row 251), and cherry-picked 2q's gap-tolerant doccheck (`db0a3f9` from `0e060f4`) so their numbers can sit above 2q's. Steps 7–10 added, one per ADR; the audit is now step 11; Scope, Acceptance (A8–A11), the lane's touches and Not in this milestone follow. Reviewed in round 3.
