# Plan: Milestone 2v — Tests that hold: fewer flaky tests, faster, more solid

- Status: Approved
- Approved: the owner, 2026-10-20 ("ponte con ello", with as many parallel subagents as it needs).
- Date: 2026-10-19
- Milestone: the owner, 2026-10-19: "Reducir los tests flaky, que vayan mejor, sean más sólidos, más rápidos." Two register rows join it, gathered by the main session (Delegated): "Load-sensitive tests: …" and "`TestTimeoutKillsTheGroup` … failed once under the pre-commit hook's load".
- Phase: none. The MVP is done (2h); the owner's goal and the two rows are the requirement.
- Contract: no capability, schema or error code changes. What bounds the work: ADR-0068 §3 and ADR-0093 (what CI runs, and that the hook runs no race); `AGENTS.md` §Build and test and `.githooks/pre-commit` (a failing commit is refused); `milestone-workflow` §Testing, §Verification by risk; `prove-change` §Grade what you proved. For step 2 only, the clause each failing test names (ADR-0099 §3, ADR-0100 §3).
- Lane: `2v`, branch `m2v` from `main` at `e0f3c5a` (the plan, with `be9caf9`'s MCP test client waits, was merged to `main` Reviewed, for later), touching `build`. `board.go claim` refuses an id already in the plans index, so the branch and worktree were made with `git worktree add` and the lane is not recorded (Delegated; a register row at the close). The steps edit tests across the tree, a new test-only package, `internal/daemon` and the code step 2's bugs name, and two skills; no step edits `.github/workflows/ci.yml` or `.githooks/pre-commit` (Q4, Q5). Lane `fix-wait-focus` (`devices`) is fixing `TestWaitFocusRejectsAFocusedNodeOffThePoint`'s fake in `internal/domain/device/tap_test.go` and `devicetest`: no step here edits `tap_test.go` or that test's fake; they meet in `internal/domain/device` only through step 3's swap of `screensource_test.go`'s `eventually`, a textual merge for whichever closes second, which reruns the device package.
- Size: nine steps. If the owner wants 2v smaller, the cut is step 7 (speed): it becomes its own milestone, with step 1's timing as its baseline.

## Scope

| Item (the brief, 2026-10-19, and the rows) | Here? | Where, and why |
| --- | --- | --- |
| 1. Measure first: each package under load, `-cpu`, two runs at once, `-race`; the suite's and CI's timings | yes | step 1 |
| 2. Classify each flaky test: bug in code or fake, racy assertion, wall-clock bound or window, resource contention | yes | step 1 classifies; steps 2, 4, 5, 6 fix by class |
| 3. One time scale for tests; events instead of sleeps where the code can expose one; which bounds are product claims and where level 5 proves them | yes, for the tests on step 1's list | step 3 (the helper), steps 4–6; sleeps in tests not on the list are not swept (Not in this milestone) |
| 4. Speed: what dominates, `t.Parallel`, shared setups, the hook's packages and `-p`, CI caching and jobs | part | step 7 for the critical-path package; the hook and CI unchanged (Q4) |
| 5. Keep it from coming back: a repeat-run check, the register row shrinking | yes | step 9 (the tool in `prove-change`, a rule at each step and at the close); no CI job (Q5) |
| Row "Load-sensitive tests: …" (Area process) | yes | steps 2, 4, 5, 6, proven together in step 8; at the close it is closed by 2v, or narrowed to the tests step 1 could not reproduce |
| Row "`TestTimeoutKillsTheGroup` (`internal/domain/git`) failed once under the pre-commit hook's load" | yes | step 4 |
| CI `race` on `main`, `TestForegroundShare` (run 39322290639) | done | fixed by `7774e0b` (`raceTimeScale`); step 3 moves it onto the shared scale |
| CI `race`, `TestRemoteArtifactsAndLogs` (an MCP screenshot relayed through two daemons past the client's 10 s) | done on `m2v` | `be9caf9` (`raceTimeScale` in `mcp_test.go`); step 3 moves it onto the shared scale |
| The size of each fixing step | bounded after step 1 | the main session writes steps 2 and 4–6's concrete test lists under Changes; a list that adds a package or class outside steps 4–6, or clearly grows the milestone, goes to the owner before step 2, with step 7 the named cut |
| CI on `main`, `TestWaitFocusRejectsAFocusedNodeOffThePoint` (run 39296252835) | no | lane `fix-wait-focus`: a test fake's bug, another session's |
| 2t's fake reading a stop file mid-write (`TestScreenSourceDeviceGone`) | done | `devicetest.Write` writes beside and renames (m2t step 10); step 1 looks for any other fake input written in place |

## Starting point

- `main` at `980361c`, 45 packages. Baseline 2026-10-19, the owner's Mac with other sessions running, one run: `go vet ./...` clean; `go test -count=1 ./...` green. Package wall times: `internal/domain/git` 277 s, `internal/domain/device` 144 s, `cmd/droidyard` 76 s, `release` 71 s, `internal/domain/file` 43 s, `internal/workspace` 32 s, `sidecar` 27 s, `adapters/peer` 22 s; the rest under 21 s. With the default `-p` the suite's wall time is about the git package's. In `go test -v ./internal/domain/...` the slowest git tests ran 178 s (`TestFetchQuietKeepsTheFatalLine`), 137 s (`TestRootRefused`), 116 s (`TestRefsCaps`), 98 s, 90 s, 81 s, 78 s: parallel tests contending for the CPU, each building its repositories with tens of git commands, so the package is bound by the git processes it spawns, not by any one test.
- Time in tests today: `raceTimeScale` (`cmd/droidyard/race_on_test.go`, ×3 under `-race`, used by four test files: `serve_test.go`, `live_peer_test.go`, `foreground_share_test.go`, `mcp_test.go`); `followRaceOverhead` (`internal/domain/device`, +5 s under `-race`, measured); six polling helpers, each its own: `eventually` in `cmd/droidyard/call_test.go` and `internal/daemon/daemon_test.go` (a fixed 10 s), `internal/domain/device/screensource_test.go` (`ssWait`, 60 s), `internal/process/supervisor_test.go` and `adapters/peer/peer_test.go` (the caller's bound, often a product claim: an idle stop, a stalled upload cut, a client gone), and `within` in `cmd/droidyard/live_peer_test.go` (3 s for ADR-0109 §3's 2 s reconcile, scaled). No rule says which bounds may grow; `7774e0b`'s message ("the 2 s claim stays level 5's") is the only statement of the house rule.
- Shape: `cmd/droidyard` has 246 tests, nearly all parallel, most re-executing the test binary as `droidyard` (`installTestSeams` runs in the re-executed binary only); three of its test files use `lsof`. In `internal/domain/git`, 7 tests are not parallel, three of them because git must reach a point inside a 2–3 s limit.
- Hook: one `go test -count=1` over the staged packages and every importer, at the default `-p`; a change under `internal/capability` runs most of the tree. In 2u it refused four commits on tests that passed alone; `GOFLAGS=-p=2` got them through. CI: `checks` (ADR-0068 §3, 20 min) and `race` (ADR-0093, `-race -timeout 30m`, 40 min), `ubuntu-24.04`, `setup-go`'s default cache.
- The evidence list, from the brief and the register; classes are provisional until step 1 measures them:

| Test | Seen | Provisional class | Step |
| --- | --- | --- | --- |
| `cmd/droidyard` `TestServeStartsThroughAProbe` | hook 2u, twice; register | window: the test sees serve's open of `daemon.lock` only through `lsof`, which on a loaded host can outlast `daemon.probeGrace` (100 ms), so serve gives up while the test still holds the probe | 5 |
| `cmd/droidyard` `TestRemoteInstall` "a cancel leaves nothing on the host" | hook 2u | suspected bug: the upload still on the host 10 s after both operations ended cancelled; 0.02 s alone | 2 |
| `cmd/droidyard` `TestRelayedScreenLinkCut` | hook 2u | suspected bug: a cut link reported `failed`, not `deviceGone` | 2 |
| `cmd/droidyard` `TestDetachedLogRotates` | CI once (2r) | suspected racy assertion: a second rotation during `fixture.stray` would carry the stray line into `daemon.log.1` | 5 |
| `cmd/droidyard` `TestCtrlCDuringAutoStart` | two full runs at once (2r) | window: a 500 ms sleep stands for "the handler is installed" before the SIGINT; then a 3 s bound against the 10 s retry | 5 |
| `cmd/droidyard` `TestCloseBoundsADrainALeaseHolderIgnores`, `TestMCPSessionBindsAndOpensItsRoot`, `TestScreenEndpointFence` | two full runs at once (2r) | bounds | 5 |
| `internal/domain/git` `TestTimeoutKillsTheGroup` | hook (row) | window: git must reach the filter inside a 3 s runner limit | 4 |
| `internal/domain/git` `TestRefsOutputCap` | hook 2u | bound: the product's 10 s `runTimeout` met by 30,000 refs under load; the claim is the cap, not the time | 4 |
| `internal/domain/git` `TestHistoryFence`, `TestWriteSpike3NoGcOutlivesAFetch`, `TestUpdateTimeout` | two full runs at once (2r) | bounds and windows | 4 |
| `TestShareAndJoin`, `TestJoinWithAnUnusableConfigurationDialsNothing` | hook 2u | racy assertion, fixed in 2u (join lines only) | — |

## Acceptance

- [ ] A1. A stress tool runs packages repeatedly under chosen load (`-count`, `-cpu`, concurrent copies, background full suites, `-race`) and reports each failing test with its rate and first failure lines, and the slowest tests and packages; shown to catch a test that fails only on one CPU and one that fails only beside a copy of itself. Source: brief §1, §5. Proven by: step 1.
- [ ] A2. Every test on step 1's measured list has a class and a step, and none is left without one. Source: brief §2. Proven by: step 1.
- [ ] A3. Each bug step 1 traces to product code or a fake is fixed, with a test that reproduces it without load, seen failing before the fix. Source: brief §2, the constraint on product changes. Proven by: step 2.
- [ ] A4. One time scale and one `Eventually` serve every package: `raceTimeScale` and the six helpers are gone, the scale grows under `-race` and with an explicit variable, `Eventually` scales nothing itself, and every scaled bound that is a product claim names where level 5 proved it; an unproven claim stays unscaled. Source: brief §3. Proven by: steps 3, 4, 5, 6.
- [ ] A5. Every test on the list passes the configuration that failed it (at least `-count=10` beside two full suites, and `-race -count=3`), and each still fails with the decision it guards reversed. Source: brief §2, §3; both rows. Proven by: steps 4, 5, 6.
- [ ] A6. Two full `go test -count=1 ./...` at once pass three times in a row on the Mac, the hook's whole-tree run passes beside one of them with no `GOFLAGS`, and `go test -race ./...` passes. Source: the owner's goal; both rows. Proven by: step 8.
- [ ] A7. The suite's critical path is shorter: the slowest package's CPU time and median wall time over three runs drop by at least a third from step 1's (the target step 1's profile confirms), with no test removed or bound weakened. Source: brief §4. Proven by: step 7.
- [ ] A8. `prove-change` holds the stress tool, the classes of timing in tests and the rule for a new or changed test; `milestone-workflow` runs the tool over a milestone's packages at its close. Source: brief §5. Proven by: step 9.

## Steps

### 1. The stress tool and the measured baseline

- Builds: `.claude/skills/prove-change/stress.go`, a `go run` tool as `work-board/board.go` is (outside `./...`, so outside vet and the hook), and its fixture module `.claude/skills/prove-change/testdata/flaky/` (its own `go.mod`): `TestNeedsTwoCPUs` (fails when `runtime.GOMAXPROCS(0) == 1`), `TestExclusive` (takes a non-blocking `flock` on a fixed file in `os.TempDir()` and holds it 200 ms, so it fails beside a copy of itself, and the kernel releases it if a run is killed), `TestSlow` (sleeps 1 s), `TestBurns` (waits for a child process that burns about 1 s of CPU), `TestStable`. The tool runs `go test -json -count=N [-race] [-cpu L] [-run RE] <packages>` as K concurrent copies (`-copies K`) while B background loops of `go test -count=1 ./...` run in the module root (`-load B`, their results discarded). Every copy and background loop runs in its own process group (`Setpgid`), and on exit, on SIGINT and on SIGTERM the tool signals those groups by their pids (SIGTERM, then SIGKILL after 10 s), never anything by name (`guard-bash.sh implement`; m2k's `pkill` stopped the owner's daemon); it sets `GOTMPDIR` to a directory of its own, so every test binary, and every `droidyard` re-executed from one (which `dieWithTheTest` ties to its test), runs from a path under it. It prints: each failing test with failures/runs per `-cpu` value and its first five output lines; the 20 slowest tests (max and median elapsed), each package's elapsed, and each copy's CPU time (user plus system of the waited `go test`, which includes the test binary and the processes it waited for); exit 1 when a measured test failed. Then the baseline, recorded in §Starting point in place of the provisional table and timings: (a) `go test -count=1 -json ./...` three times alone, the median per package and the 20 slowest tests; for the slowest package, each of its 20 slowest tests run alone (`-run '^Name$'`) with its CPU time, and the share of it spent building fixtures, so step 7 aims at what costs; (b) the whole tree with `-copies 2`, three times (2r's reproduction); (c) `-race` over the tree once; (d) `-cpu 1,4 -count=5` over `cmd/droidyard`, `internal/domain/git`, `internal/domain/device`, `internal/process`, `internal/daemon`, `adapters/peer`; (e) each evidence test by name, `-count=10 -load 2`; (f) CI: `gh run list --workflow ci.yml --limit 50`, the failure rate and median duration of each job, and the failing tests of each failed run; whether `go test` in CI prints `(cached)`. One line per failing test: package, test, the configuration and rate, its class (bug in code, bug in a fake, racy assertion, bound, window, contention), the step that takes it. A test from the evidence that never fails is listed "not reproduced" and kept with its provisional class.
- Implements: brief §1 and §2; Q1, Q2.
- Check: in the fixture module, `go run ../../stress.go -count 5 -cpu 1,4 -copies 2 ./...` reports `TestNeedsTwoCPUs` 10/10 at `-cpu 1` and 0/10 at `-cpu 4`, `TestExclusive` failing, `TestStable` 0/20, `TestSlow` among the slowest, a copy's CPU time at least 0.9 s per `TestBurns` run and `TestSlow` alone (`-run`) near 0, and exits 1; `-copies 1 -cpu 4` reports no failure and exits 0; `-race -copies 1 -cpu 4` exits 0. After a run with `-load 1`, and after one interrupted by SIGINT, `ps -axo pid,pgid,command` shows no process of the run's groups and none whose command line holds the run's `GOTMPDIR` (a run of `cmd/droidyard` with `-load 1`, so real daemons were up). That `go run` of a file outside the fixture's module works is settled by the first run. And §Starting point's table holds every failing test of (a)–(f) with a class and a step.
- Schema: no change.
- Needs: real `go`, `git`, `lsof`; the fake adb and fake Gradle the tests already use; `gh` for (f), run by the main session if the implementer's limits refuse it.
- Not here: no fix (steps 2–7); no CI job (Q5); no change to the hook (Q4).
- Rests on: that two concurrent full suites on this Mac approximate the hook's load of 2u, L5 by reproduction; a test the baseline does not reproduce is fixed on its traced cause (L3) and a mutation, the gap in Unverified.
- [ ] Done. Evidence:

### 2. The bugs the baseline traced to code or to a fake

- Builds: for each test step 1 classed "bug in code" or "bug in a fake", the code it names, its root cause traced in this step's evidence, and a test that reproduces it deterministically, ordering the race through a seam (a hook variable set by `installTestSeams` or the package's tests), never by load. The suspects: the host's upload left after a cancelled `device.installUpload` (`internal/hosting/uploads.go`, the host's handler, `internal/domain/device/installremote.go`); the reason a relayed screen gets when the link is cut (`internal/domain/device`'s relayed source, `adapters/peer`). A suspect step 1 finds to be a racy assertion or a bound moves to step 5 with a line under Changes. A fix that would change behaviour a document specifies stops the step: an ADR first (`adr` skill).
- Implements: the clause each test names: ADR-0099 §3 (an upload is kept only until installed or 10 minutes old; a cancel leaves none, `TestRemoteInstall`'s comment), ADR-0100 §3 (`deviceGone` for a cut link, `TestRelayedScreenLinkCut`'s comment).
- Check: per bug, the new deterministic test, seen failing on the code before the fix and passing after; the original test `-count=20 -load 2` with 0 failures. If step 1 traces no bug, the step is recorded as empty under Changes, with its evidence.
- Schema: no change.
- Needs: step 1; fakes only (the two-daemon lab of `cmd/droidyard`'s peer tests).
- Not here: no new behaviour; no device run (a level-5 run of the host's cleanup is not what the test claims: it claims the core's, which a lab of two real daemons reaches).
- Rests on: each fix's root cause, L3 traced and L4 tested by the deterministic reproduction; concurrency and process lifetime, so the Opus verifier reviews it (`milestone-workflow` §Verification by risk) and the implementer runs at effort `high`.
- [ ] Done. Evidence:

### 3. One time scale and one `eventually`

- Builds: `internal/testtime` (imported only by tests, so it is linked into no binary): `Scale()` is 3 when the binary is built with `-race` (a `race`-tagged file, as `race_on_test.go` is today) and 1 otherwise, multiplied by the positive integer in `DROIDYARD_TEST_TIMESCALE` when it is set (a malformed value panics at the first call, naming the variable); `Bound(d)` is `d * Scale()`; `Eventually(t testing.TB, within, what, cond)` polls every 10 ms until exactly `within`, scaling nothing itself, and fails naming `what` and `within`: a caller whose bound is a hang guard passes `testtime.Bound(d)`. Its package comment states the four kinds of time in a test (Q2): a hang guard (scaled freely); a product claim (scaled only with its level-5 proof named beside it, else unscaled); a window the test itself must meet (redesigned, not scaled: steps 4, 5); an absence waited for (`nothing within X`, never a failure under load, only a weaker test). A scaled bound that separates two product numbers (3 s against a 10 s retry) is held below the larger. Then: `raceTimeScale` replaced in its four files and its two files deleted; the six helpers replaced by `testtime.Eventually`. Each converted call site is classed guard or claim, in one evidence line per file: the 10 s copies and `ssWait` are guards (`testtime.Bound`); a caller's product-claim bound (`supervisor_test.go:183`'s idle stop, `deathsignal_test.go:182`'s client gone, `adapters/peer` `upload_test.go:187`, `:198`, `:324`, `screen_test.go:196`, `live_peer_test.go`'s 3 s for ADR-0109 §3) stays as it is today: unscaled where it is unscaled, scaled where `raceTimeScale` scaled it only with the level-5 proof named. `TestForegroundShare`: `lossBound` (2 s, `foreground_share_test.go:192`) cites m2u's level-5 runs 7 (b) and 8 (a), which timed SIGINT only (190 ms, 89 ms); it stays scaled for SIGHUP, SIGTERM, `kill -9` and `unshare` only if the step points (L2) at the one stop path they share with SIGINT, else those cases are unscaled; the 3 s `joined` bound (`:171`) is a guard (no document claims a join latency). `within` (3 s for ADR-0109 §3's 2 s reconcile): no level-5 run timed the reconcile (m2u steps 2 and 3 did not), so it becomes unscaled with a register row; if `-race` then fails it (Check), the step times the reconcile at level 5 instead (two isolated daemons, `verify-daemon`, no device: a peer configuration change and its effect within 2 s), names that run beside `within`, and scales it. `followRaceOverhead` stays: it is a measured addition, not a scale.
- Implements: brief §3; Q3.
- Check: `internal/testtime`'s tests: `Scale()` is 1 plain, 3 under `-race` (`go test -race ./internal/testtime`), 6 under `-race` with `DROIDYARD_TEST_TIMESCALE=2`, a panic naming the variable for `0`, `-1` and `x`; `Eventually` calls `Fatalf` with `within` in the message on a condition that never holds, after `within` and not `Bound(within)` under `-race` (timed), and returns at once on one that holds (a fake that embeds `testing.TB` and overrides `Helper` and `Fatalf`; `Eventually` returns right after `Fatalf`, so the fake need not stop the goroutine). Under `-race`, one product-claim call site still fails at its unscaled bound: `supervisor_test.go:183`'s idle stop, with the supervisor's idle stop delayed in the product through `go test -overlay` to about 1.5 s past the timeout (between the unscaled 1.3 s and a scaled 3.9 s), fails, and would pass were the call site scaled. `grep -rn --include='*_test.go' -e raceTimeScale -e 'func eventually' -e 'func within(t ' cmd internal adapters` prints nothing. The users' packages pass with `-race`, and `go test -race -count=5 -run 'TestLivePeerConfig|TestReconcileFollowsInterfaces' ./cmd/droidyard` passes (the tests that call `within`).
- Schema: no change.
- Needs: nothing; independent of step 2 (may run in parallel, `milestone-workflow` §Parallel steps). Two isolated daemons (`verify-daemon`) only if `within`'s level-5 branch is taken.
- Not here: no product-claim bound changes value; a guard grows only under `-race` or the variable; no CI or hook setting of the variable (Q3: it is for a person on a loaded machine; the tests must hold without it); no product environment variable (ADR-0024 is untouched: the variable is read only by test binaries).
- Rests on: —
- [ ] Done. Evidence:

### 4. `internal/domain/git`, `internal/process`, `internal/daemon`: windows and bounds

- Builds: the tests step 1 lists in these packages, by class. Known windows: `TestTimeoutKillsTheGroup` (row 214, `run_test.go`, no retry), `TestUpdateTimeout` (`update_test.go`) and `TestFetchTimeout` (`write_test.go`, "retries five times, and still failed on a loaded machine"): git must reach the filter, the hung remote or the upload-pack inside a 2–3 s limit. One helper for the three: the limit under test stays short, an attempt whose precondition never appeared (the filter's pid file, the remote's start mark) is inconclusive and retried with the limit doubled, up to five attempts, then fails naming them; an attempt with the precondition met is decisive and must end in `ErrTimeout` with the group gone. `TestRefsOutputCap` (bound): its fixture's runner gets `testtime.Bound(60 s)`, since the claim is the 4 MiB cap and its marks, not git's speed; the product's 10 s `runTimeout` stays. `TestWriteSpike3NoGcOutlivesAFetch` (contention): it counts gc processes of the whole machine with `ps`, so another session's git can fail it; `gcProcesses` keeps a new gc or maintenance process only when its working directory (`lsof -a -p <pid> -d cwd -Fn`) is under the test's temporary directory (compared after `filepath.EvalSymlinks`, since `lsof` on macOS reports `/private/var/…`), and the test becomes parallel; a test of the filter starts a long-lived process whose command line `gcProcesses` matches (a `sleep 30` whose `exec.Cmd.Args[0]` is `git gc`) once in the test's directory and once in another, and sees only the first kept, so the filter cannot hide the repository's own gc. `TestHistoryFence` and any new: per step 1's class. Each change says in the test why the bound is safe. A test whose "Not parallel" reason is gone becomes parallel.
- Implements: brief §2, §3; row 214; the git rows of the load-sensitive row.
- Check: each listed test, `-count=10 -load 2` and `-race -count=3`, 0 failures, where step 1 recorded its failure rate; each still fails with its decision reversed (`go test -overlay`): `TestTimeoutKillsTheGroup` with the group kill removed and with `endOf` returning the context error; `TestRefsOutputCap` with the cap's mark dropped; the others as their comments name.
- Schema: no change.
- Needs: step 3; real git. Steps 4 and 5 both may touch `internal/daemon`: they run in sequence.
- Not here: no change to `runTimeout` or any product limit; no sweep of passing tests in the package.
- Rests on: that a retried inconclusive attempt cannot turn a real failure into a pass: a retry happens only when the precondition (the filter's pid file, the remote's start) is absent, and a run with the precondition met is decisive; L4, aimed at by a mutation that makes every attempt inconclusive (it must fail after five).
- [ ] Done. Evidence:

### 5. `cmd/droidyard`: windows, bounds and racy assertions

- Builds: the tests step 1 lists in `cmd/droidyard`, by class. `TestServeStartsThroughAProbe`: `lsof` polling replaced by a blocking seam. `internal/daemon` gains a hook variable (nil in the product) that `lockExclusive` calls after a busy attempt that passed the deadline check and before its `time.Sleep`; in the re-executed binary `installTestSeams` sets it, only when the test's FIFO variable is set (so no other re-executed serve that meets a busy lock blocks), to write one line to standard error and block until the test, having read the line and released the probe, writes to a FIFO the test names in a test environment variable. The next `Flock` then succeeds whatever the load, since `lockExclusive` returns on a nil error without looking at the deadline; with the attempts and the skip gone, nothing in the test races the grace. `TestCtrlCDuringAutoStart`: the 500 ms sleep replaced by a wait on a line the re-executed call prints through the same kind of seam once its interrupt handler is installed; its 3 s bound is a separation from the 10 s retry, so it is scaled but held below 10 s (a scaled bound that discriminates against a product number never reaches it). `TestDetachedLogRotates`: its stray-line assertions hold only when no second rotation ran (the test reads the rotation count it can see, or raises the log bound for the stray part), per step 1's diagnosis. The bounds step 1 lists (`TestCtrlCDuringAutoStart`, `TestCloseBoundsADrainALeaseHolderIgnores`, `TestMCPSessionBindsAndOpensItsRoot`, `TestScreenEndpointFence`, and any new): hang guards through `testtime.Bound`; a product-claim bound names its level-5 proof or, lacking one, keeps its value and is listed under Unverified. A whole-output comparison that step 1 lists (2u's class, a late trace line) is narrowed to the lines the claim is about, and what the whole comparison also asserted absent (no extra line, nothing logged for a refusal) stays as an explicit negative assertion, seen failing with the unwanted line injected; comparisons no run has seen fail are not touched.
- Implements: brief §2, §3; the `cmd/droidyard` part of the load-sensitive row; ADR-0019 §5 (the probe grace, unchanged).
- Check: each listed test `-count=10 -load 2` and `-race -count=3`, 0 failures; reversals: `TestServeStartsThroughAProbe` with `probeGrace` set to 0 fails at once (the deadline check returns before the hook, so no line arrives and serve gives up; the test watches serve's exit as well as the line, so this is a failure, not a hang until the guard); `TestCtrlCDuringAutoStart` with the interrupt ending no wait (the call retries its 10 s) fails; `TestDetachedLogRotates` without the `dup2` repoint fails; each bound test with its decision reversed as its comment names. `TestAcquireOutlastsAHolderProbe` unchanged and passing.
- Schema: no change.
- Needs: step 3.
- Not here: no change to `probeGrace` or to rotation; the seam does nothing in a `droidyard` binary (it is set only by test code).
- Rests on: that the hook runs only after a busy attempt the grace allowed, and that serve's next attempt after it is a `Flock` whose success ends the loop without a deadline check. What remains timed is the first attempt alone: under 100 ms between computing the deadline and the first `Flock` returning, a stall of that width being the only way left to fail; L2 pointed at `lock.go`'s loop, L4 by the `probeGrace = 0` mutation and by `-count=20 -load 2` with 0 failures. Process lifetime and the daemon lock: the Opus verifier reviews this step.
- [ ] Done. Evidence:

### 6. The other packages on the list

- Builds: the tests step 1 lists outside steps 4 and 5 (expected: `internal/domain/device` beyond `tap_test.go`, `adapters/peer`, `internal/workspace`, `internal/domain/file`), by the same classes and rules; fake inputs written in place where a fake polls them go through `devicetest.Write` or its equivalent. If step 1 lists none, the step is recorded as empty under Changes.
- Implements: brief §2, §3; the rest of the load-sensitive row.
- Check: as step 4's, per test: `-count=10 -load 2` and `-race -count=3` with 0 failures, and the reversal each names.
- Schema: no change.
- Needs: step 3.
- Not here: `TestWaitFocusRejectsAFocusedNodeOffThePoint` and its fake (lane `fix-wait-focus`).
- Rests on: —
- [ ] Done. Evidence:

### 7. The critical path: the slowest package

- Builds: in the package step 1 measures slowest (today `internal/domain/git`, 277 s; `internal/domain/device` next), from step 1's profile of where its CPU goes: the fixture repositories the tests build with tens of git commands each (`newWfx`, `newRemote`, `newReadFx` and the like) are built once per package and copied per test, where step 1 shows fixtures dominate; only fixtures without worktrees, submodules or alternates are copied (those keep being built), remote URLs are rewritten in the copy, and the copy's index is refreshed (`git update-index --refresh`) so no test observes stale stat data; a not-parallel test whose reason steps 4–6 removed becomes parallel; a fixed `time.Sleep` that waits for something the code can signal becomes a wait on that signal. A test keeps every assertion and bound; one whose speed-up needs either is left as is.
- Implements: brief §4; Q4.
- Check: `stress.go -count 1` over the package after one warm-up build (`go test -count=1 -run '^$'`, so no run carries compile time), three runs before (step 1's numbers) and after: the CPU time of the run (user plus system of the `go test` process tree, which the tool reports from the waited process's usage) drops by at least a third, and the median wall time with it (A7); the test count is equal; `go test -race` and `-count=3 -cpu 1,4` of the package pass. The third is provisional: step 1's profile confirms or replaces it, under Changes, before this step starts.
- Schema: no change.
- Needs: steps 4–6 (a test made load-proof may become parallel).
- Not here: no hook or CI change (Q4); no split of a package.
- Rests on: that a copied fixture is equivalent to a built one: nothing in it points back at the template; L4 by a test that copies the template, finds no file of the copy holding the template's path (every file read, `.git` included), writes in the copy and finds the template unchanged and the copy's remotes resolving to the copy's own peers, seen failing with the rewrite dropped.
- [ ] Done. Evidence:

### 8. The whole suite under load, and the hook as it is

- Builds: nothing new unless a run below fails: then the failing test joins its class's step under Changes and this step reruns.
- Implements: the owner's goal; both rows; Q4 (the hook keeps its packages and `-p`).
- Check: `stress.go -copies 2 ./...` three times in a row, 0 failures; the hook's own command (`go test -count=1 ./...` with the hook's environment) beside one background full suite (`-load 1`), 0 failures; `go test -race -timeout 30m ./...` passes. And after the merge, `main`'s CI green on both jobs (the close's watch).
- Schema: no change.
- Needs: steps 2–7.
- Not here: no `-p` cap, retry or package filter in the hook (Q4).
- Rests on: that the Mac's load stands in for CI's runner; CI's own run after the merge settles it (Unverified).
- [ ] Done. Evidence:

### 9. Keep it from coming back

- Builds: `prove-change` gains a short section: the stress tool and its flags; the four kinds of time (pointing to `internal/testtime`'s comment, not restating it); the rule: a new or changed test that spawns a process, polls, or bounds time passes `stress.go -copies 2 -cpu 1,4 -count=10 -run <its name>` on its package and `-race -count=3` before its step is ticked, and a bound that is a product claim names its level-5 proof; no background suites per step, since on a Mac shared by lanes that load is what refused 2u's commits. `milestone-workflow` §Testing gains one line: at the close, the stress tool over the packages the milestone changed, `-count=5 -load 1` and `-race -count=2`. The close (not this step) closes row 214 and closes or narrows the load-sensitive row to what step 1 could not reproduce, citing this plan.
- Implements: brief §5; Q5.
- Check: `go test ./internal/doccheck` passes over the two edited skills (links to `stress.go` and `internal/testtime` resolve; no duplicated passage of `internal/testtime`'s comment); the rule's command, run as written on one test changed in steps 4–7, reports it and exits 0.
- Schema: no change.
- Needs: steps 1, 3.
- Not here: no CI job (Q5).
- Rests on: —
- [ ] Done. Evidence:

## Not in this milestone

- A repeat-run or flake-hunting CI job: Q5; reconsidered if `main`'s `race` job fails on a test again after 2v (a register row at the close).
- A `-p` cap, a retry or fewer packages in the hook, and a CI job split or cache change: Q4.
- Sweeping the sleeps and bounds of tests no run has seen fail: the rule of step 9 covers tests as they are written or changed.
- `TestWaitFocusRejectsAFocusedNodeOffThePoint`: lane `fix-wait-focus`.
- Product behaviour beyond what step 2's bugs need, and any product limit (`runTimeout`, `probeGrace`, upload timeouts).

## Design questions

Each has a recommendation; the steps are written against it. None needs an ADR: none touches a capability, an error code, the state directory or what CI runs (ADR-0068 §3, ADR-0093). The approval settles them.

### Q1. Measure before fixing

- Blocks: steps 2–7.
- The documents say: nothing; m2r's step 1 fixed a named list and found five more under a heavier load.
- Recommendation: step 1 measures under five configurations and the later steps take the measured list by class; the evidence of 2u and the register is the starting set, not the scope.

### Q2. The classes and what each gets

- Blocks: steps 2, 4–6.
- The documents say: nothing; the house rule that a bound is evidence only when the claim is proven elsewhere is in `7774e0b`'s message alone.
- Recommendation: bug in code or in a fake: fixed, with a deterministic reproduction (step 2). Racy assertion: narrowed to the lines the claim is about. Bound: a hang guard is scaled; a product claim is scaled only beside its level-5 proof. Window the test must meet: redesigned with an event or a retry of inconclusive attempts, never scaled. Contention: a unique resource per test.

### Q3. One time scale

- Blocks: steps 3–7.
- The documents say: nothing; `raceTimeScale` and five `eventually` exist.
- Recommendation: `internal/testtime` with `Scale`, `Bound`, and an `Eventually` that scales nothing itself (the caller says, by passing `Bound(d)`, that its bound is a guard); ×3 under `-race`; `DROIDYARD_TEST_TIMESCALE` multiplies it, set by a person on a loaded machine, never by the hook or CI, so the suite must hold without it. No detection of CI by its `CI` variable: CI's failures were under `-race`, which the scale already covers.

### Q4. Speed, and the hook

- Blocks: steps 7, 8.
- The documents say: the hook tests the staged packages and their importers (its comment, `milestone-workflow` §Testing); ADR-0093 §2 keeps race out of it.
- Recommendation: the hook keeps its packages and the default `-p`. A cap (2u's `GOFLAGS=-p=2`) hides load sensitivity that CI and other sessions still meet, and with the git package at 277 s it lengthens every core commit; a retry would pass a flake the commit introduced. Speed comes from the critical-path package (step 7). CI unchanged: `setup-go` already caches, and step 1's (f) says whether a split would pay; if it would, that is a later change with its own ADR (ADR-0068 §3).

### Q5. Keeping it from coming back

- Blocks: step 9.
- The documents say: ADR-0068 §3 and ADR-0093 fix what CI runs; a new job would supersede them in part.
- Recommendation: the stress tool and a rule (step 9), at each step's tick and at each close, on the Mac. No CI job now: changes reach CI only when a close merges, so a CI repeat-run reports after the fact what the close's run reports before it; the `race` job stays the CI-side check. Reconsidered, with an ADR, if `race` fails on `main` again after 2v.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| Two concurrent full suites on the Mac reproduce the hook's 2u load and CI's runner closely enough | 1, 4–6, 8 | step 1's rates; `main`'s CI on both jobs after the close, and the next milestones' hook runs |
| A test from the evidence that step 1 does not reproduce is fixed on its traced cause | 2, 4–6 | its reversal mutation (L4) proves the test still guards; only a later failure, or its absence over the next closes, proves the flake gone |
| Each product-claim bound has a level-5 proof to name | 3–6 | the bound's comment; a claim without one stays unscaled and gets a register row at the close |
| A killed copy leaves no daemon: `dieWithTheTest` takes down every `droidyard` re-executed from a test binary, a detached one (its own session) included | 1, 8 | step 1's interrupted run and its `ps` check |
| `go test`'s waited usage includes the test binary and the processes it waited for, on macOS (a detached gc is not counted) | 1, 7 | step 1's `TestBurns` fixture |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | [test] [blocking] B1: `Eventually` scaled every bound, product claims included (`supervisor_test.go:183`, `deathsignal_test.go:182`, `adapters/peer` uploads and screen, `within`) | Step 3: `Eventually` scales nothing; guards pass `testtime.Bound`; every call site classed per file; a claim stays as today unless its level-5 proof is named; a `-race` check that a claim still fails at its unscaled bound; A4 rewritten |
| 1 | revise | [test] [blocking] B2: the seam's line still raced `probeGrace`, and the attempts and skip were dropped | Step 5: the hook runs inside `lockExclusive` after a busy attempt the grace allowed, and blocks on a FIFO until the test has released; Rests on rewritten; the `probeGrace = 0` mutation still bites |
| 1 | revise | [plan] N1 stale starting point (`be9caf9`, a fourth `raceTimeScale` user, `TestRemoteArtifactsAndLogs`, `within`) | Header, Scope and Starting point updated; `within` folded into step 3 |
| 1 | revise | [safety] N2 the stress tool's background suites had no end | Step 1: process groups signalled by pid, never by name; `GOTMPDIR` of its own; a `ps` check after a full and an interrupted run; an Unverified row |
| 1 | revise | [test] N3 CPU reporting unchecked; N7 fixture copies not proven equivalent | Step 1's `TestBurns`; step 7 warms the build and copies only fixtures without worktrees, submodules or alternates, refreshes the index, and greps the copy for the template's path |
| 1 | revise | [plan] N4 step 9's rule was itself load; N6 fixing steps unbounded | Step 9's per-step rule without `-load`; a Scope row: the main session writes the lists under Changes after step 1, growth goes to the owner |
| 1 | revise | [test] N5 the whole-output sweep could weaken tests | Step 5: only listed tests, absences kept as negative assertions seen failing |
| 1 | revise | [nit] counts 10/10; `flock` not `O_EXCL`; `EvalSymlinks`; steps 4 and 5 in sequence; step 9's check | All applied |
| 2 | revise | B1 and B2 resolved (checked against `lock.go:124-133` and the call sites) | — |
| 2 | revise | [test] [blocking] N1 (from B1): step 3's grep matched the product's `func within` (`internal/capability/sessions_write.go:133`) | The grep is limited to `*_test.go` and matches `func within(t ` |
| 2 | — | [test] N2 the idle-timeout mutation moved the bound with it; N3 `within` has no timed level-5 run; [doc] N4 `TestForegroundShare`'s proof is m2u 7 (b) and 8 (a), SIGINT only | Step 3: the mutation delays the product's idle stop through `-overlay`; `within` unscaled with a register row, or timed at level 5 if `-race -count=5` of its two tests fails; `lossBound` scaled for the other signals only where they share SIGINT's path, the `joined` 3 s a guard |
| 2 | — | [nit] step 5: the residual first-attempt window stated; the hook only with the FIFO variable set; the reversal watches serve's exit; the fake `testing.TB` described | Applied |

## Changes

- 2026-10-20, steps 1 and 3 (Delegated, the owner's word on parallel subagents): step 3 runs at once with step 1, in its own worktree (its Needs line names nothing), and is merged after step 1 lands. Step 1's measurements (a) then run beside step 3's implementer, so step 7 does not take its "before" from them: before step 7 starts, its package is measured again three times alone on the branch as it then stands, and those numbers are A7's baseline.
- 2026-10-20, step 1: the evidence gains four tests that failed one run of the whole tree on 2026-10-20 (the hook's run for an unrelated commit, refused): `cmd/droidyard` `TestRemoteInstall`, `TestCtrlCDuringAutoStart`, `TestRemoteLogsManyStreams` and `internal/workspace` `TestDialogIsRecordedWhileItRuns`; the last two were not on the list.
