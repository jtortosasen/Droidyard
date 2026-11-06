# Plan: Milestone 3j — A machine-level Android tool: the project per call, the workbench out

- Status: Approved
- Date: 2026-10-29
- Milestone: the owner, 2026-10-28: "this droidyard is covering too much"; Droidyard becomes purely an Android tool at machine level, a CLI/MCP on adb and SDK management; Build and Gradle stay; the web UI, Git, files, sessions and chats, the binding, the assistant hooks, `shell.exec` and the trash leave to a separate project. `docs/README.md` §Where the work stands: "next, the owner's choice".
- Phase: none (Phase 3). The Scope table is the boundary.
- Contract: ADR-0137 (Proposed; accepted with this plan); ADR-0015 §5, ADR-0020 §Core primitives, §Scope, ADR-0027, ADR-0037, ADR-0043 §2, ADR-0075 §2, §5, §6, ADR-0081, ADR-0092, ADR-0094, ADR-0101, ADR-0104, ADR-0122, ADR-0124; `workspace-state.md`; `capability-api.md` §Discovery and versioning; `mcp.md` §Server instructions; `http-websocket.md` §Security baseline.
- Before approval (main session, ADR-0137 still Proposed): Q1, Q3 and Q6's answers folded into ADR-0137 §3 and §Consequences (Q3 reverses an alternative ADR-0020 rejected, a principle of the contract, so it belongs in the ADR, not a commit line); Decision 9 completed: ADR-0085 whole; in part ADR-0015 §7 (the shell), ADR-0020 §Core primitives (Q1), ADR-0032 (the `shell` keys), ADR-0037 (hooks), ADR-0075 §2 (a), §4, §7, ADR-0094 (worktrees).
- Approved: the owner, 2026-11-06 ("continua por donde ibas", then "en local digo": the whole milestone runs locally, nothing pushed before the close); ADR-0137 accepted with it.
- Track: Full. Opus verifier after step 5 (lifetime, admission) and step 6 (the contract switch), and at the close; implementer effort `high` for steps 5, 6, 8.
- The plan's commit adds its row to `docs/plans/README.md` (until then `TestPlansIndexMatchesFiles` fails on this file).
- API `0.41.0` on main; this milestone takes `1.0.0` (`capability-api.md` §Discovery and versioning: a breaking change is a major; ADR-0137 §8). No other lane on 2026-10-29; checked again at approval.
- Devices: step 10 only, on the lane's scratch AVD (`B=~/Library/Caches/droidyard-dev/3j`, own adb and emulator ports); synthetic projects only.
- Order: the export; the web first, because while `web/` exists every schema change runs `pnpm -C web run verify` on a generated client (`.githooks/pre-commit:38`); the other removals, so the per-root lifetime (step 5, the risky core) lands only on what stays; then the contract switch (step 6), the first end-to-end MCP result. Every step that removes a capability also edits what names it (`mcp.md`'s exposure table and its §Server instructions, `Instructions`, `CoreTools`, the shipped skill and its tool map, the pinned tool count and `maxToolListBytes`), so each stays green alone. A removed error code that live documents or the register still name goes to a retired list in `internal/doccheck/duplicates_test.go`, so `TestErrorCodesInDocsExist` holds without editing closed rows.

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| ADR-0137 accepted, Decision 9's header lines | yes | step 1 |
| The leaving paths exported with history, outside this repository | yes, a verified bundle | step 1 (Q5); the repository is the owner's |
| The web UI, its embedded assets, `droidyard ui` | yes | step 2 |
| `agent.*`, the hooks, `droidyard trash`, `init`'s hook writing | yes | step 3 (Q4) |
| `git.*`, `file.*`, `shell.exec` and policy's `shell`, `session.*`, the chats readers | yes | step 4 |
| Per-root state opened by the first call, released after 30 min idle | yes | step 5 (Q1, Q2) |
| `project` per call; `workspace.*`, the binding, worktree occupation, `.droidyard/project.json`; CLI `--project`; API `1.0.0` | yes | step 6 (Q2, Q3) |
| `droidyard mcp` with one tool list everywhere, `--root`/`--from` ignored; `init`'s entries | yes | step 7 (Q6) |
| `device.install` with `apk` | yes | step 8 |
| Documents, skills, the register | yes | step 9 |
| Level 5 | yes | step 10 |
| The separate project's design, build, repository; a shim that survives daemon restarts; MCP roots | no | ADR-0137 §7, §Consequences, §Alternatives |

## Starting point

- `m3j` at `ebbbc37`, API `0.41.0`, 101 capabilities, 37 of scope `workspace`: `workspace.{open,close,describe}` (`open`, `close` with `RawTarget`), `build.*` (6), `project.{modules,variants}`, `sdk.requirements`, `device.install`, `shell.exec`, `agent.record{Command,Edit}`, `file.*` (10), `git.*` (11).
- Scope: `RequestEnvelope.Workspace` (`internal/capability/envelope.go:28`), checked in `prepare` (`operation.go:1087`), admitted at Start by `Workspaces.Admit` (`operation.go:529`, else `WORKSPACE_NOT_OPEN`) = `workspace.Registry.Admit` (`workspace.go:311`); `sessionRef` also calls `Admit` on the bound root (`operation.go:483`). Handlers get the id in `Call.Workspace` and resolve the root with `ws.Describe`. The build lock key `gradle.root:<wsid>` is computed at Start (`build.go:117`, `operation.go:545`).
- Trust: `policy.Authorize(rootOf)` on the workspace root, then `authorizeGradle` resolves and records the Gradle root (`cmd/droidyard/main.go:470–510`); ADR-0081 in `Policy.Trusted` (`policy.go:134`).
- Per-root state: `ws/<wsid>/{workspace.json,builds/,artifacts/}` (build history read from disk per call, `build/registry.go:231`), `workspaces.json`; the Gradle client's `roots` map (`gradle/client.go:121`, never pruned); one sidecar, idle after 10 min with no lease. `Registry.Close` takes `w.tx`, drains, `CloseRoot`s roots no open workspace uses (ADR-0092), stops pollers (`workspace.go:819`).
- Adapters: MCP binds at hello (`adapters/mcp/mcp.go` `Serve`, `openBound`, `request`); the shim's `--root`/`--from`, `projectOf`, `serveNoTools` (`cmd/droidyard/client.go:232`, `root.go:35`); CLI `--workspace` and the hello root (`adapters/cli/tree.go:510`, `client.go:197`, `internal/daemon/native.go:194`); HTTP the envelope verbatim (`httpapi.go:319`); pages from `web.Assets()` (`serve.go:80`, `httpapi.go:91`).
- Couplings that fix the removal order: `internal/domain/git` imports `file`; `shell` uses `file.ResolveDir`; `chats` uses `ws.Known` (`workspace/watch.go:82`) and `assistant.ResolveUserDirs`; `assistant` imports `agent`; `trash.go` uses `assistant.PlaceTarget`; `init --exclude` uses `git.Tracked`; the device half of occupation shares `Holder`, `Holder.Who` (`sessions_write.go:19`), the claim enum and `idleLocked` with the worktree half; `release/build.sh:31,52` refuses a binary without the UI; `release/workflows_test.go:64` pins ci.yml's pnpm lines.
- **Baseline, 2026-10-29 at `ebbbc37` plus ADR-0137 uncommitted:** `go vet ./...` clean; `go test ./...` green except `internal/doccheck` `TestRelativeLinksResolve` (ADR-0137 links to this file, now present).

## Acceptance

- [ ] A1. **Device work needs no project**: a shim started in a folder with no Android project lists every tool and runs `device_list`, `device_uiTree`, `emulator_list`. Source: ADR-0137 §2. Proven by: steps 7, 10.
- [ ] A2. **The project per call**: every workspace-scoped capability takes `project`; a relative, missing, nonexistent or Gradle-less one is refused at Start, no operation, naming what to pass; no envelope carries `workspace`. Source: ADR-0137 §3. Proven by: steps 6, 10.
- [ ] A3. **Per-root lifetime**: a root opens once on the first calls naming it, concurrent or not; it is released after 30 min with no call and no running operation; a release never cancels a call; a call during a release waits and runs. Source: ADR-0137 §3. Proven by: steps 5, 10 (release at level 4).
- [ ] A4. **Trust unchanged**: an untrusted root fails `WORKSPACE_UNTRUSTED` before anything spawns; a linked worktree of a trusted repository is trusted. Source: ADR-0015 §5, ADR-0081. Proven by: steps 6, 10.
- [ ] A5. **One shim, two projects**: one shim serves calls naming two roots in turn, each building its own; `--root`/`--from` are accepted with one stderr line. Source: ADR-0137 §2. Proven by: steps 7, 10.
- [ ] A6. **`device.install`**: `apk`, or `project` with `build`, exactly one. Source: ADR-0137 §4. Proven by: steps 8, 10.
- [ ] A7. **Removed**: no page served, `ui` and `trash` unknown; no `git.`, `file.`, `session.`, `workspace.`, `agent.`, `shell.` capability; an installed hook passes; `/v1/call`, `/v1/ws`, `/v1/screen`, the topics answer behind the token. Source: ADR-0137 §1, §6. Proven by: steps 2–7, 10.
- [ ] A8. **Contract**: API `1.0.0`; the schema diff exactly the steps'. Source: ADR-0137 §8. Proven by: steps 3, 4, 6, 8.
- [ ] A9. **The export**: a bundle outside the repository holds only the leaving paths, with their full history, and the register rows that leave; those rows are closed here. Source: ADR-0137 §7, §Consequences. Proven by: steps 1, 9.

## Steps

### 1. Accept ADR-0137 and export the leaving project (main session)

- Builds: no product code.
  - ADR-0137 `Accepted` with its `Supersedes:` line from Decision 9 (as completed before approval); each old ADR's one header line (whole: `Status: Superseded by ADR-0137`; in part: `Superseded in part by: ADR-0137 (§…)`); the `docs/adr/README.md` statuses.
  - The export (Q5) in `X=$B/export`: `git clone --no-local --single-branch --branch m3j` at this step's commit, the branch renamed `main`; `git filter-branch --prune-empty --index-filter` keeping only `X/paths.txt`: `web/`; `internal/domain/{git,file,chats,agent,shell}/`; `internal/assistant/`'s hook files (`hook`, `edit*`, `observe`, `projectdir`, `hookentry`, `testdata/hooks`); `internal/capability/session{watch,release}*`, `sessions_write*`; `internal/workspace/{capabilities,enter,show,watch,dialog}*`; `adapters/httpapi`'s page code (`ui_test.go`); `cmd/droidyard/{hook,trash,ui,open,show,git_*,session_*,mcp_enter,worktree*}*`; `docs/subsystems/files.md`; the ADRs Decision 9 supersedes whole; `release/build.sh`. Then `refs/original/*` and `refs/remotes/*` deleted, and `git bundle create X/droidyard-workbench.bundle main`. `X/register-rows.md`: the open rows step 9 closes, verbatim, with the rule that chose them (step 9).
- Implements: ADR-0137 §7, §9.
- Check: `TestADRsAreWellFormed`, `TestADRIndexMatchesFiles` pass (seen failing with one old ADR's line left out). `git bundle list-heads` names one branch; a fresh clone of it succeeds; per top path in `paths.txt`, `git log --oneline -- <path> | wc -l` equals the count here; `git log --all --name-only --format=` in the clone lists no path outside `paths.txt` (seen failing on a bundle made with `--all` before the refs are deleted).
- Schema: no change.
- Needs: the owner's acceptance of ADR-0137 with this plan.
- Not here: the separate repository (the owner clones the bundle and pushes; outward-facing); its build (it imports packages that stay, ADR-0137 §7); history from before a rename into a leaving path (filter-branch keeps paths, not renames).
- Rests on: the bundle holds each leaving path's history and nothing else (L3, the counts and the path list).
- [x] Done. Evidence: `93985b4`; `TestADRsAreWellFormed`, `TestADRIndexMatchesFiles` pass, seen failing with one header line out. The export in `~/Library/Caches/droidyard-dev/3j/export/` (`verify.txt`): `droidyard-workbench.bundle` 1.9 MB; `list-heads` names `HEAD` and `refs/heads/main` only (the first run carried the `v0.*` tags and no `HEAD`, so its clone failed: every ref but `main` is now deleted and `HEAD` bundled); its clone has 309 commits; 101 of 101 paths `ok` (the same commit count as here), none `DIFF`; 563 paths in its history, 0 outside `paths.txt`. `register-rows.md`: 96 open rows of `web UI`, `git`, `files` and 49 of the other Areas, verbatim, with the rule; 9 `web UI` rows about what stays change Area at step 9 (lines 543, 544, 560, 562, 598 to `MCP/agents`; 552, 583, 586 to `device/UI`; 573 to `device`); 4 binding rows close at step 9 as resolved, not moved (344, 345, 346, 373).

### 2. Remove the web UI

- Builds: delete `web/` and `serve.go`'s `web.Assets()`; `httpapi`'s page routes, `placeholder`, `page`, `assets` and their tests (`/v1/call`, `/v1/ws`, `/v1/screen`, `/v1/artifacts/{id}` and the peer's handlers stay); `droidyard ui`, bare `droidyard` (the help now), `status`'s UI URL (it prints the API address); `release/build.sh`'s UI build and asset check; the frontend part and schema trigger of `.githooks/pre-commit`; the web steps of `ci.yml`, `release.yml` and `release/workflows_test.go`'s `hookChecks`; `.claude/skills/svelte-*` and doccheck's `thirdPartyDirs` (its self-test given a fixture of its own); the frontend lines of `AGENTS.md` §Build and test and of `milestone-workflow`'s `implementer-run.md`.
- Implements: ADR-0137 §1, §6.
- Check: `TestNoPageIsServed` (httpapi): `GET /` and `/index.html` → 404 with the fence's headers, `POST /v1/call` with the token answers (seen failing before the routes go); `TestUIIsNotACommand` (exit 2, unknown); `TestOldSpellings` without `ui`; `go list -deps ./cmd/droidyard` names no `web` package; `release/build.sh` builds a binary.
- Schema: no change.
- Needs: step 1.
- Not here: the `workspace.*` capabilities the UI used (step 6).
- Rests on: —
- [x] Done. Evidence: `TestNoPageIsServed` (`/`, `/index.html`, `/assets/app.js`, `/favicon.ico` → 404 with nosniff and no-referrer; a foreign Host 403; `/v1/call` 401 without the token, an envelope with it), seen failing before the routes went (`GET /: 200 "<!doctype html>…"`); `TestUIIsNotACommand` (exit 2, unknown, the help names no web UI), seen failing with a stub `ui` entry put back; `TestBareDroidyardPrintsTheHelp` (exit 0, no `daemon.lock`; not seen failing, written after the change); `TestOldSpellings` with 21 names; `go list -deps ./cmd/droidyard` names no `web` package; `release/build.sh v0.0.0` built the four tarballs offline. `go test ./...`: one run failed `TestRemoteRecordingResumesTheCopy` (20 s, load), passing alone and in the package's rerun; the commit's hook ran the whole suite (it touches the hook).

### 3. Remove `agent.*`, the hooks and `droidyard trash`

- Builds: `internal/domain/agent`; `internal/assistant`'s hook files (the observe hint table, `RouteRm`, `PlaceTarget`) and `TestHintToolsAreExposed`; `cmd/droidyard/trash.go`; `Sessions.CheckWrite` (`Holder.Who` stays for devices); the `agent.*` row of `mcp.md`'s exposure table (`mcp.md:84`). `droidyard hook <event>` stays one minor as a pass-through (Q4). `init` and `init --user` write no hook and remove the hook entries a previous `init` wrote (ADR-0037's uninstall rules), reporting each.
- Implements: ADR-0137 §1 (agent.*, the hooks, the trash command); Q4.
- Check: `TestInstalledHookPasses` (each client's event shape → exit 0, the pass output, no socket dialled, nothing written); `TestInitRemovesItsHooks` (a fixture `settings.json` and `hooks.json` holding init's hook and a user's own → init's goes, the user's stays); `droidyard trash` exits 2.
- Schema: `agent.checkWrite`, `agent.recordCommand`, `agent.recordEdit` removed.
- Needs: step 2.
- Not here: a replacement for the edits guard and the `rm` redirect (ADR-0137 §Consequences); files already in the state directory's trash stay where they are, unread (a register row and a release-note line, step 9).
- Rests on: —
- [x] Done. Evidence: schema 339 lines out, none in: the three `agent.*` (audience `human`, so `Instructions`, `CoreTools`, the skill, the tool count 48 and `maxToolListBytes` unchanged; no error code retired). `TestInstalledHookPasses` (46 event shapes of both clients → exit 0, the pass output, no stderr, 0 dials of a live fake daemon, the project and state root unchanged; a stdin that never ends holds it at most `hookDrainLimit`), seen failing with the hook made to dial (46 of 46) and to read stdin to the end (killed at 10 s); the main session added the bounded drain (1 s, 4 MiB), so a client's write of the event never meets a closed pipe. `TestInitRemovesItsHooks` (the real binary on fixture `settings.json` and `hooks.json`: init's entries, two binary paths, one with the state root inline, go; the user's own stay; the removal reported), and `assistant`'s `TestInitRemovesItsHooksFromTheProject`, `TestUserRunRemovesItsHooks`, seen failing with the cleanup stubbed; `TestInitWritesNoHook` not seen failing (the old writers are gone); `TestTrashIsNotACommand` (exit 2, unknown), seen failing with a stub `trash`; `TestOldSpellings` 20 names. The four commands clean; `go test ./...` 41 packages ok.

### 4. Remove Git, files, `shell.exec`, sessions and chats

- Builds: `internal/domain/{git,file,shell,chats}` and their wiring (`StopPollers` nil, `gitWriteStarter`, `shellStarter`, `RequiresGit`, `proc.GitWrite`, `proc.ShellCommand`); `session.{watch,chats,release}` and `Sessions.ReleaseWorkspace`; policy's `shell` section, `ShellAllowed` and `policy.update`'s `shell` path (an existing file's `shell` key becomes an unknown key, kept and ignored, `policy.go:15`). `init --exclude|--gitignore` keep working: `git.Tracked`, its errors and `internal/domain/git/run.go`'s hardening (fsmonitor and hooks off) move into `internal/assistant` with their tests. What names them: their rows in `mcp.md`'s exposure table (`session.*` included); `shell` out of `Instructions`' policy line and its `mcp.md` copy (`TestInstructionsCarryTheManual`'s point with it); `shell_exec` out of the shipped skill, `references/tools.md` regenerated; `untrusted_test.go`'s tagged set; `call_test.go`'s harness; the tool count and `maxToolListBytes`.
- Implements: ADR-0137 §1.
- Check: `system.capabilities` lists none of `git.`, `file.`, `shell.`, `session.`; `TestConfigWithShellSectionStillLoads` (a `config.json` with `shell` → trust, devices, occupation read as before); `TestInitExcludeStillWorks` and `TestInitExcludeIgnoresFsmonitor` (a repository with `core.fsmonitor` set → no marker written, as `history_harden_test.go` asserts today).
- Schema: those 25 capabilities removed; `shell` out of `policy.get`'s result and `policy.update`'s input.
- Needs: step 3.
- Not here: an owner's release of a held device (`session.release` left; the device's other ends stay, ADR-0094 §3, and the CLI is never refused): a register row.
- Rests on: —
- [x] Done. Evidence: schema exactly the 25 capabilities out and `shell` out of `policy.get`'s result and `policy.update`'s input (compared parsed against HEAD); 13 error codes retired into `duplicates_test.go`'s `retiredCodes`, guarded by `TestRetiredCodesAreNoConstants`; tool count 48 → 47, `maxToolListBytes` 54,000 (measured 53,933). `TestConfigWithShellSectionStillLoads` (trust, devices, occupation read as before; the key kept on rewrite; a `shell` patch refused as unknown), seen failing with `decode` rejecting the key; `TestInitExcludeStillWorks`, seen failing with `Tracked` returning `ErrNotRepository`; `TestInitExcludeIgnoresFsmonitor` (no marker; plain git as the control makes it), seen failing with `core.fsmonitor=false` dropped; `TestSystemCapabilitiesListsNoLeftDomain`, seen failing with a `git.probe` registered. `proc.ShellCommand`, `proc.GitWrite` gone; an old record of them is reaped by `Reconcile`'s default branch. Left for step 6: `TestWorktreeOccupation` deleted (it read rows through `session.watch`; the worktree half keeps its unit tests until step 6 removes it), `session.state` rows with no consumer. The four commands clean; `go test ./...` every package ok.

### 5. Per-root lifetime: opened by the first call, released after idle

- Builds: `internal/workspace`, `internal/capability/operation.go`, `serve` wiring. The target still comes in the envelope; the capabilities it reaches are the 10 Gradle and project ones and `workspace.*`.
  - **Lock order** `w.tx` → `r.mu` → `o.mu`, stated in the package doc.
  - `Registry.Enter(target, record) error` replaces `Admit` in `start` for scope `workspace` without `RawTarget`, at Start (Q1): a root target is resolved (`ResolveProject`, Q2) and canonicalised; `Open` (under `w.tx`; on an open root it writes nothing, no `recordOpen`, only the `lastUsed` stamp; one during a close waits for it, ADR-0027); then the admission under `r.mu`, as `Admit` does. An admission that finds it `closing` (a release decided in between) opens once more and admits. Refusals at Start, no operation: `REQUEST_INVALID_PARAMS` (not absolute, missing, not a directory), `PROJECT_NO_GRADLE_ROOT` with candidates, `WORKSPACE_STATE_UNREADABLE`. An id target resolves through the index, as today. `Admit` stays, for `sessionRef`'s stamp only, until step 6 removes it.
  - `Registry.RunIdle(ctx)`, started by `serve`, every minute (an injectable clock; `IdleAfter` 30 min, a constant): per open workspace, `w.tx.TryLock()` (a busy one is skipped this round); under `r.mu`, released when the store has no non-terminal operation of it (`Operations.ActiveIn(id)`) and its `lastUsed` is older than `IdleAfter`, entering `closing` in that hold; then, `tx` still held, `Close`'s path from its drain on (`CloseRoot`, pollers, `closed`). `lastUsed` is the registry's, stamped under `r.mu` by every `Open`, the no-op on an open root included (so a fresh or just-reopened root cannot be judged idle before its admission, and one retry suffices, whether the admission finds it `closing` or `closed`), at each admission and at each of its operations' end (`Workspaces.Ended(id)`, called by `endLocked`'s caller after `o.mu` is released), since the store prunes terminal operations after 15 min. `ws/<wsid>/` stays.
  - Implicit open also reopens a root that `workspace.close` closed, while `close` lasts (step 6).
- Implements: ADR-0137 §3 (the per-root lifetime), ADR-0027; ADR-0020 §Core primitives as ADR-0137 §3 now says (Q1).
- Check: with `-race` and a fake clock,
  - `TestEnterOpensOnceUnderConcurrency`: 20 goroutines, one root by three spellings (trailing slash, a symlink, a subdirectory) → one `OnOpen`, one id, 20 admissions. Seen failing with `tx` not taken.
  - `TestIdleNeverReleasesARunningOperation`: an operation held, +31 min → open; it ends, +29 → open; +31 → closed, `CloseRoot` once. Seen failing with `ActiveIn` dropped, and with `lastUsed` stamped at admission only.
  - `TestReleaseNeverCancelsAnAdmittedCall`: `Enter` racing the sweep at the 30-minute edge, 1,000 iterations → every `Enter` admitted, no call ends `OPERATION_CANCELLED`, and `CloseRoot` count equals reopen count. Seen failing with the release taking `tx` after its decision, and with `Open`'s stamp removed.
  - `TestEnterDuringRelease`: a hook holds the release between `closing` and `closed`; a concurrent `Enter` waits on `tx`, then admits on the reopened root.
  - `TestRootRemovedWhileOpen`: the directory deleted → a call naming it refused, nothing opened; the sweep releases the old entry.
  - `TestLastResultSurvivesRelease`; `cmd/droidyard` `TestCLIOpensItsProject` (`droidyard call build.tasks` in a fixture project, no `workspace.open` first → runs).
  - The tests that assert `WORKSPACE_NOT_OPEN` after a close or with no open are rewritten to the new rule: `cmd/droidyard/{start,workspace,mcp_binding}_test.go`, `internal/workspace/{gradleswitch,workspace}_test.go`, `internal/capability/registry_test.go`, `adapters/cli/cli_test.go` (those of hooks and git went in steps 3–4).
- Schema: no change (`WORKSPACE_NOT_OPEN` stays declared until step 6).
- Needs: step 4.
- Not here: `project` on the wire (step 6); pruning the Gradle client's `roots` at release (register row).
- Rests on: admission and the release decision are serialised by `w.tx` and `r.mu`, and an admitted operation counts until it ends (L4 with `-race`; L5 in part at step 10).
- [x] Done. Evidence: with `-race` and a fake clock: `TestEnterOpensOnceUnderConcurrency` seen failing with `tx` not taken ("OnOpen ran 20 times; want once"); `TestIdleNeverReleasesARunningOperation` (30 min not released, 31 released) seen failing with `ActiveIn` dropped and with the `Ended` stamp dropped ("29 minutes after the operation ended: state closed"); `TestReleaseNeverCancelsAnAdmittedCall` (1,000 rounds, 5 interleavings, about 2.6 s) seen failing with the release taking `tx` after its decision ("round 4") and with Open's stamp removed ("round 2"); `TestEnterDuringRelease` seen failing with the same late `tx`; `TestRootRemovedWhileOpen`, `TestFolderGoneBetweenResolveAndOpen` (`REQUEST_INVALID_PARAMS`, seen failing as `WORKSPACE_ROOT_INVALID`), `TestLastResultSurvivesRelease` (through `build.lastResult`, seen failing with a release deleting `builds/`), `TestCLIOpensItsProject` (real daemon, two spellings, one sidecar). `Ended` is told outside the store's locks (`TestEndedIsToldOncePerOperationOutsideTheStoreLock`, seen deadlocking inside `endLocked`). Opus verifier: proceed, no blocking finding; it reran three mutations by overlay. Its N1 taken here: `PROJECT_NO_GRADLE_ROOT`'s nextStep now "pass the absolute path of one of the candidates … as the project", compared literally. `start_test.go`, `capability/registry_test.go`, `adapters/cli/cli_test.go` needed no rewrite (their `WORKSPACE_NOT_OPEN` comes from fakes); four `cmd/droidyard` tests outside the list were rewritten for Q2 (`TestNoGradleRoot`, `TestSwitchedGradleRootCloses`, two in `trust_test.go`). Departures, Delegated: an explicit `workspace.open` of an open root still records it (`workspace.watch`'s published description relies on it until step 6); `PROJECT_NO_GRADLE_ROOT` now precedes `WORKSPACE_UNTRUSTED` (nothing spawns; the candidates are folder names); a sweep between an operation's end and its `Ended` may release a root whose long operation just ended (nothing cancelled; the next call reopens); `Enter` during a close waits for it (bounded by the drain), the caller's context not interrupting.

### 6. The project per call; `workspace.*` and the binding removed

- Builds:
  - `internal/capability`: `Workspace` gone from `RequestEnvelope` and `Request`; for scope `workspace` the core takes `project` from the params (Q3), removes it before strict decoding and hands it to `Enter`, which returns the root and its resolved Gradle root on the `Call`; `REQUEST_MISSING_WORKSPACE`, `REQUEST_UNEXPECTED_WORKSPACE`, `WORKSPACE_NOT_OPEN` retire (a missing `project`, or one sent to a global capability, is `REQUEST_INVALID_PARAMS`). `internal/schema` adds one required `project` property to every workspace-scoped input schema. The build lock key becomes `gradle.root:<canonical Gradle root>`, so two spellings of one project (its top, its `dev/`) queue with `blockedBy` (ADR-0137 §Consequences).
  - `internal/workspace`: `workspace.{open,describe,close,enter,show,forget,chooseRoot,watch}` and their files; rule (a) of `ResolveGradleRoot` (`.droidyard/project.json`, `gradleroot.go:243`); `PROJECT_NO_GRADLE_ROOT`'s nextStep (`gradleroot.go:131`) now "pass the absolute path of one of the candidates"; `workspace.lifecycle`'s payload gains `root` (the one way a client maps an event's workspace id); the worktree half of `Sessions` (`Opened`, `Occupy`, `HolderOf`, `Binding`, `Remembered`, ADR-0088's restore, the device release on entering the main tree), `Origin.Root`, `Admit` and `SessionRef.Workspace`; the device half and the lease unchanged.
  - Adapters: MCP `Serve` binds nothing (`openBound`, `request`'s filling gone; the hello's chat stays for ADR-0101's reach); the hello's `root` gone; the CLI's `--workspace` becomes `--project`, the cwd's absolute path when absent (ADR-0137 §5), on `call` and the derived commands (ADR-0122); HTTP and WebSocket take `project` in params.
  - What agents read: the `workspace.*` rows of `mcp.md`'s exposure table; `Instructions` and `mcp.md` §Server instructions together (≤ 2,000 characters with `CoreNote`): the binding and worktree lines replaced by one ("Project tools take `project`: the absolute path of the project's folder or worktree you work in; device, emulator and SDK tools take none"); `TestInstructionsCarryTheManual`'s points and `TestMCPTranscript`'s golden file; `CoreTools` without `workspace_*`; the shipped skill without "Development in a worktree", with the project argument, `droidyard.version` 2, `references/tools.md` regenerated.
  - API `1.0.0`; `go test ./internal/schema -update`; the tool count and `maxToolListBytes`.
- Implements: ADR-0137 §2 (daemon side), §3, §5, §8.
- Check:
  - `TestProjectIsRequiredAndAbsolute`: missing, relative, `~/x`, nonexistent, a file → each refused at Start naming `project` with the nextStep "pass the absolute path of the project's folder"; a folder with two builds below → `PROJECT_NO_GRADLE_ROOT` with the new nextStep; home or `/` only by a settings file of its own (ADR-0075 §2); no operation recorded.
  - `TestEnvelopeHasNoWorkspace`: a native and an HTTP envelope with `workspace` → refused as an unknown field.
  - `TestUntrustedProjectSpawnsNothing`, `TestLinkedWorktreeInheritsTrust` through `project` (seen failing with the gate on the passed path, a subdirectory of a worktree, instead of the resolved root).
  - `TestBuildByProject` (dummy sidecar): `build.assemble {project}` as the first call completes; `build.lastResult` by another spelling of that root returns it; `TestTwoSpellingsQueue`: builds from a project's top and its `dev/` → the second `queued`, `blockedBy` the first.
  - `TestCLIProjectDefaultsToCwd`; `release/workflows_test.go` accepts the tag `v1.0.0`; the schema diff as below.
- Schema: `workspace.*` removed; every workspace-scoped input gains required `project`; the three codes out of the error lists; `SessionRef.workspace` out of the operation views; `workspace.lifecycle` gains `root`; `version` `1.0.0`.
- Needs: step 5.
- Not here: the shim (step 7); absolute paths in results (Q2).
- Rests on: trust is decided on the resolved root, never on the path passed (L4; L5 at step 10).
- [ ] Done. Evidence:

### 7. `droidyard mcp` without a binding

- Builds: `client.go`: `--root`, `--from` accepted and ignored with one stderr line; `projectOf`'s use and `serveNoTools` gone, so every shim dials and auto-starts the daemon (Q6); one tool list wherever it starts. `init`'s Cursor entry `mcp` without `--from`, a stale one replaced (ADR-0090 §1); `init` reports an installed skill older than the shipped one (`droidyard.version`).
- Implements: ADR-0137 §2; ADR-0124 §2 superseded in part.
- Check: `TestMCPListsAllToolsAnywhere`: shims started in an empty folder, with `--from '${workspaceFolder}'`, and with `--root /nonexistent` → one `tools/list`, the stderr line (seen failing on step 6's tree: no tools); `TestCursorEntryHasNoFrom`; `TestInitReportsAStaleSkill`.
- Schema: no change.
- Needs: step 6.
- Not here: refusing `--root`/`--from` (the next minor, ADR-0137 §2); a shim that answers `tools/list` itself (Q6).
- Rests on: —
- [ ] Done. Evidence:

### 8. `device.install` with `apk`

- Builds: `InstallParams{Device, APK, Build}`; `validate`: exactly one of `apk` and `build`; `apk` absolute, symlinks resolved before the check, a regular file named `.apk` starting with the zip magic, within `installUpload`'s size bound, so a link cannot send another host file to a peer; `Descriptor.ProjectOptional` for this capability alone (`project` required iff `build`; the core resolves it only when present); a lent device gets the file as a build's output is sent today (`installUpload`).
- Implements: ADR-0137 §4.
- Check: `TestInstallApkOrBuild` (both, neither, relative `apk`, a directory, a symlink to a non-APK, a `.apk` that is not a zip, `build` without `project` → `REQUEST_INVALID_PARAMS`, nothing on the device fake or the peer); `TestInstallApkLocalAndLent` (device fake, peer fake).
- Schema: `device.install` gains optional `apk`; `build`, `project` no longer required, the rule in their descriptions.
- Needs: step 6.
- Not here: split APKs and `.aab`.
- Rests on: —
- [ ] Done. Evidence:

### 9. Documents, skills and the register

- Builds: ADR-0137 §Consequences' list: `AGENTS.md`; `docs/README.md`; `mvp.md`'s header line (its tables stay the MVP's record); `vision.md`; `overview.md`, `capability-api.md`, `workspace-state.md` (the per-root lifetime and why no binding), `process-lifecycle.md`; `mcp.md`; `http-websocket.md` (no pages; a page from another origin is refused by rules 3 and 5, so a UI brings its own server or shell); `docs/subsystems/files.md` deleted; `work-board` and `board.go`'s `touchesKnown`, `milestone-workflow`, `prove-change`, `verify-daemon` (MCP, CLI and Workspaces recipes with `project`), `milestone-plan`'s `sources.md`; the release notes of `1.0.0` (rerun `droidyard init --user`; the trash's files and where they lie). **The register**: each row of `X/register-rows.md` closed "3j: moved to the separate project (ADR-0137)": the open rows of Areas `web UI` (61), `git` (25), `files` (19), and of `MCP/agents` (74 open), `policy/security`, `process`, `CLI` those about sessions, the binding, `workspace.*`, worktree occupation, the hooks, chats, the trash, `file.*`, `shell.exec` or the UI; a `web UI` row about what stays (`known-limits.md:543` `operation.wait` resume, `:552` live-view latency, `:586` the view on sleep or reboot, and any like them) moves to the Area of its code instead. The Cursor `init --user` row (Work `trial`) closed "3j: no binding; the same tools wherever the server starts (step 10 (c), scripted)", with one new `trial` row for the Cursor app's shared server. New rows for §Not in this milestone and §Unverified's open items.
- Implements: ADR-0137 §Consequences.
- Check: `go test ./internal/doccheck` green; every row of `register-rows.md` closed, and no other row changed but the moved and the new ones (the open-row diff against the file, recorded).
- Schema: no change.
- Needs: steps 1–8.
- Not here: Done plans, research, reviews, ADR bodies (records).
- Rests on: —
- [ ] Done. Evidence:

### 10. Level 5 and the close

- Builds: no product code. `verify-daemon` Launch, Doctor, Cleanup; an isolated daemon from the tree; a trusted synthetic Gradle project `P`, its linked worktree `W`, an untrusted copy `U`; the scratch AVD; evidence in `$B/evidence/l5/`.
  - (a) A1: a shim in an empty folder: `tools/list` complete; `device_list`, `emulator_start`, `device_uiTree` with no `project`.
  - (b) A2, A4, A6: `build_assemble {project: P}`, `device_install {project: P, build}`, `device_install {apk}` of the same file; a relative `project` and `U` refused with no sidecar pid recorded; `W` builds (trusted through `P`).
  - (c) A5: one shim with `--from '${workspaceFolder}'`: `build_assemble` on `P`, then `W`: two builds, each of its root; the stderr line once.
  - (d) A3 in part: two shims' first calls on a fresh root at once → one `opening`/`ready` pair on `workspace.lifecycle`, with `root`.
  - (e) A7: `GET /` 404; `/v1/screen` streams behind the token; `droidyard hook` with a Claude `PreToolUse` event passes.
  - (f) The version: this tree's daemon borrows the AVD from a host built from main (`0.41.0`): `device_list`, `device_uiTree` on `<remote>/<serial>`.
  - Zero orphans; the owner's daemon and adb server unchanged.
- Implements: A1–A7 at level 5.
- Check: each sub-run's decisive output in the evidence line.
- Schema: no change.
- Needs: steps 1–9.
- Not here: the Cursor app and Claude Code themselves (the owner's `trial` row); idle release (no shipped override of 30 min: level 4, step 5).
- Rests on: —
- [ ] Done. Evidence:

## Not in this milestone

Each a register row at step 9 unless said otherwise.

- **The separate project**: its design, build, repository (ADR-0137 §7). No row.
- **A shim that survives daemon restarts** (ADR-0137 §Consequences); **MCP roots** (§Alternatives, no row).
- **A held device released by its owner**: `session.release` left with sessions (step 4).
- **A project that chose one of several builds** through `.droidyard/project.json` now names that build's folder as `project`, a new workspace id; its old builds stay in the old `ws/<wsid>/`.
- **Absolute paths in results**: they stay relative to the resolved root (Q2). No row.
- **The Gradle client's `roots` pruned at release**; **old `ws/<wsid>/` directories** of projects no longer named, kept and never swept.
- **Refusing `--root`/`--from`; removing `droidyard hook`**: the next minor (Q4).

## Design questions

Each has a recommended option. **Answered 2026-10-31 by the main session under the standing instruction (Delegated): the recommended option of each, Q1–Q6.** Q1, Q2, Q3 are folded into ADR-0137 §3, Q4 and Q6 into its §Consequences, and Decision 9 is completed as the header asked; the owner accepts or reverts them with the ADR.

- **Q1. Where `project` is resolved and the root opened** (steps 5, 6). ADR-0020's 2026-10-04 amendment (owner, 1e Q2): "`Start` still does no I/O". Recommended: at Start: the resolution (a stat per ancestor, at most ADR-0075 (c)'s 1,000 directory reads, which `authorizeGradle` already does per call) and a first open (`workspace.json`, the index, the console sweep): bounded local file reads and two small writes, never a process, a device or the network; "no I/O" narrowed to that. Start keeps its guarantees: a refused call records no operation, the lock key is known at Start so the build queue's order is the start order, the start event precedes every transition (ADR-0043 §2). The owner's call, as 1e Q2 was. Rejected: resolving in the operation's goroutine (a bad `project` becomes a failed operation; the key moves after Start, so builds of one root may queue out of start order and show `queued` after `running`); in each adapter (three copies; MCP has nothing to default from).
- **Q2. What `project` names** (steps 5, 6, 8). ADR-0137 §3 says "by ADR-0075's rules"; ADR-0075 §5 makes published paths relative to the project root. Recommended: today's `projectOf` without the project file: the nearest ancestor of `project`, itself included, holding a settings file; else `project` itself when exactly one Gradle build lies one or two levels below (ADR-0075 §2 (c)); home or `/` only by its own settings file; `project` an existing directory. Trust, the id, `ws/<wsid>/` and every published path are that root's, so an agent passing its cwd gets what its bound session got. Rejected: the upward walk alone (the owner's repository with its build in `dev/` would need `…/dev`); keying by the Gradle root (paths relative to `dev/`; the trust gate moved); a file path.
- **Q3. `project` on the wire** (step 6). ADR-0020 §Scope and workspace addressing made `workspace` an envelope field "so schemas stay clean" and rejected a parameter; ADR-0137 §3: every such capability "takes `project`". Recommended: a parameter, added by `internal/schema` to every workspace-scoped input schema from the descriptor's scope (one property, one description; required unless `ProjectOptional`) and taken off by the core before strict decoding: no handler's struct changes, no descriptor can omit it, and MCP lists it because the input schema has it. Rejected: an envelope field (an MCP tool has none; the adapter would splice every schema); a field per params struct (ten copies, drift).
- **Q4. Installed clients after the upgrade** (steps 3, 7). `init` wrote `droidyard hook …` into users' client settings; removed outright, every tool call in every chat would run a failing hook. Recommended: `droidyard hook` passes every event (exit 0, the client's pass output, nothing dialled or written) for one minor, as `--from`; `init` and `init --user` remove the hook entries they wrote and say so; `init` reports a stale skill. Rejected: removing it now (broken hooks until the user reruns `init`); the daemon cleaning client files (it never writes them, ADR-0037).
- **Q5. The export** (step 1). Recommended: a bundle of the filtered `main` from `git filter-branch --index-filter` on a scratch clone (stdlib git; `git filter-repo` is not installed here), outside the repository; the removals start once it verifies, not once the owner creates the repository, since this repository's history keeps every removed file. Rejected: `git subtree split` (one prefix per run; there are a dozen); waiting for the remote (blocks on an outward step that changes nothing here).
- **Q6. A daemon for every chat** (step 7). ADR-0124 §Alternatives considered rejected auto-starting an unbound session: "a daemon would stay resident for every chat on the machine", and every chat would pay the tools' tokens; ADR-0137 §2 lists the tools everywhere without saying so. Recommended: accept both, stated in ADR-0137 §Consequences: device tools are the product wherever a chat starts (the Cursor case is a chat that got none), the daemon's idle cost is small (sidecars idle out; ADR-0019 §Lifetime), and `--tools core` stays for a smaller list. Rejected: a shim that answers `initialize` and `tools/list` from the embedded schema and dials at the first call (MCP logic in the shim, against ADR-0019 §Processes 2, and a second place the tool list is built).

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| Agents pass the right `project` (their cwd or worktree) with no binding | 6, 7 | step 10 scripted; real agents: the owner's `trial` row |
| The Cursor app's shared server gets every tool in an Android chat | 7 | step 10 (c) simulates it; the app not run |
| Idle release on a real daemon | 5 | level 4 only; a register row |
| Start-side resolution stays fast on a slow filesystem (a network home) | 5, 6 | not measured |
| A `0.41.x` host and a `1.0.0` client lend devices (and the reverse) | 6, 8 | step 10 (f), one direction |
| Each client's pass output for every hook event | 3 | `TestInstalledHookPasses` against the shapes `internal/assistant` decodes today; the clients not run |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise: 5 blocking, 9 non-blocking, 5 nits | B1 [concurrency]: the idle release decided under `r.mu` but `Close` takes `w.tx` after it, so an `Enter` could reopen and admit in between and the release cancel it | Lock order `tx` → `r.mu` → `o.mu`; `RunIdle` `TryLock`s `tx`, decides and enters `closing` inside it, keeps it to `closed`; `TestReleaseNeverCancelsAnAdmittedCall` (step 5) |
| 1 | | B2 [green]: steps 4–6 removed tools still named by `Instructions`, `CoreTools`, the shipped skill and `mcp.md:84` until step 7 | Each removal step edits what names it (header §Order; steps 3, 4, 6); step 7 keeps the shim and `init` only |
| 1 | | B3 [order]: step 2's implicit open and Gradle-root refusal reached 27 leaving capabilities (hooks in any folder), and eleven tests assert `WORKSPACE_NOT_OPEN` | Order 1, web, hooks, git/file/shell/sessions, then the lifetime (step 5), then the switch; the rewritten tests named in step 5 |
| 1 | | B4 [ADR]: removing `serveNoTools` auto-starts a resident daemon for every chat, an alternative ADR-0124 rejected | Q6: accepted and stated in ADR-0137 §Consequences before approval; the lazy shim rejected with its reason |
| 1 | | B5 [export]: `filter-branch` without the refs removed bundles `refs/original` and `refs/remotes`, the whole repository | The filtered `main` only, refs deleted; `list-heads` one branch, `log --all --name-only` no stray path (step 1) |
| 1 | | N6 two spellings don't queue; N7 `PROJECT_NO_GRADLE_ROOT`'s nextStep; N8 last end pruned after 15 min, `sessionRef`'s `Admit`; N9 trash files, stale skills; N10 ids nothing resolves | N6: lock keyed by the Gradle root, `TestTwoSpellingsQueue`; N7: nextStep rewritten, tested, §Not in this milestone; N8: `lastUsed` stamped by `Ended`, `Admit` kept to step 6; N9: release notes, a row, `TestInitReportsAStaleSkill`; N10: `root` on `workspace.lifecycle`, `SessionRef.workspace` out, declared (step 6) |
| 1 | | N11 rows of staying parts closed as moved; N12 Decision 9 incomplete, Q1/Q3 belong in ADR-0137; N13 the git runner's hardening; N14 a symlinked `apk` to a peer | N11: moved to their Area (step 9); N12: header §Before approval; N13: moved with its tests, `TestInitExcludeIgnoresFsmonitor`; N14: symlinks resolved, zip magic (step 8) |
| 1 | | Nits: `workspace.*` scope count; the version asked though the documents decide it; the tag shape is a check; home by rule (b); the export list | All taken |
| 2 (B1–B5 only) | revise: B2, B3, B5 resolved; B4 resolved provided the fold into ADR-0137 happens before approval; B1 in part | R2-B1: `lastUsed` stamped only at admission and end, so a fresh or reopened root could be released between `Open` and its admission and the one retry lose again (also on `closed`); the stress test passed with a refused `Enter`. Not blocking: `Enter`'s open of an open root rewrote `workspaces.json` per call | Every `Open`, the no-op included, stamps `lastUsed` under `r.mu`; one retry covers `closing` and `closed`; `TestReleaseNeverCancelsAnAdmittedCall` asserts every `Enter` admitted, seen failing without the stamp; an open root's `Open` writes nothing (step 5) |
| 2 | | Nits: the `session.*` and `workspace.*` rows of `mcp.md`'s exposure table not named; the export clone's ref | Named in steps 4 and 6; `--single-branch --branch m3j`, renamed `main` (step 1) |

No third round: R2-B1 is answered with round 2's own fix (`milestone-plan` §Writing one, item 7); the main session or the owner judges it, and the step 5 verifier checks it in code.

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.
