# Plan: Milestone 2o — A project whose Gradle build is in a subfolder

- Status: Approved
- Approved: 2026-10-15, Delegated (the main session under the owner's standing instruction to apply the recommended option; the owner approved the design, "Sí porfa"). Q1 is ADR-0075.
- Date: 2026-10-15
- Milestone: "2o — A project whose Gradle build is in a subfolder" (owner, 2026-10-15; `docs/README.md` §Where the work stands says "next, each milestone by discussion", and the main session adds this one at approval).
- Phase: `docs/roadmap/mvp.md` §Phase 2, after its acceptance; no phase item is this milestone (see Scope).
- Contract: ADR-0075 (Q1, Delegated). Also ADR-0015 §5, ADR-0019 §Sessions, ADR-0020 §Scope and workspace addressing, ADR-0022, ADR-0026, ADR-0027, ADR-0036, ADR-0037 §2, ADR-0045 §1, §4, ADR-0046 §1, §2, §3; `workspace-state.md` §Identity and reuse, §Lifecycle; `capability-api.md` §Error model, §Discovery and versioning; `gradle.md` §Error model.

**The owner should see:**
- You open the repository's top. Droidyard finds `dev/` by itself when it is the only Gradle build one or two levels below (hidden folders, `build` and `node_modules` skipped). With several, the UI lets you choose, and the choice is saved in `.droidyard/project.json` as `gradleRoot`.
- Trust belongs to the folder you open. Your trust of `dev/` does not carry over to the top; trust the top once (the UI's Trust button or `droidyard trust`).
- If you ran `droidyard init --root dev`, run `droidyard init --client <yours>` at the top, and `droidyard init --uninstall --client <yours> --root dev` to remove the old entries under `dev/`.
- Started inside the build (a terminal in `dev/app`), `droidyard` still opens `dev/`, as it does today (Q1 §6).
- Started in a folder that holds exactly one Gradle project one or two levels down (a projects folder with one app in it), `droidyard` opens that folder as the project and `init` writes its files there; `init` prints the Gradle root it found. Never in your home directory or `/`.
- The API gains one field (`gradle` on `workspace.open` and `workspace.describe`) and one error code (`PROJECT_NO_GRADLE_ROOT`).
- No agent may look at your repository, so whether it has exactly one Gradle build within two levels is untested until you open it (Unverified).

## Scope

The owner's brief (2026-10-15) is the requirement; its design was approved ("Sí porfa"), and its details are Delegated.

| Item | Here? | Where, or why not |
| --- | --- | --- |
| Two roots: the project root (files, editor, git, trust, init, project file, MCP binding, Activity) and the Gradle root (model, tasks, builds, sidecar, fingerprint, `sdk.requirements`) | yes | steps 1–4; Q1 §1 |
| Rule 1: the project root holds `settings.gradle(.kts)`, unchanged | yes | steps 1, 2 (every existing test, unchanged) |
| Rule 2: exactly one within two levels, skipping hidden folders, `build`, `node_modules` | yes | step 1; Q1 §2 adds: symlinks skipped, no descent into a found build (`build-logic`) |
| Rule 3: `gradleRoot` in the project file, inside the project root | yes | steps 1, 6 |
| No Gradle root: an error with a nextStep naming the key, never a crash | yes | step 2 |
| The contract: a field on `workspace.open`/`describe`, one API minor version, schema regenerated | yes | steps 2, 3 |
| A capability that sets the key | no | Q1 §7: the UI writes it through `file.write` (ADR-0046 §2) |
| Entry points from a top: `droidyard`/`ui`, `init`; `droidyard mcp` | yes | step 5 (`mcp`: no change needed, Q1 §6) |
| Started inside `dev/app` | yes, as today | Q1 §6: the upward walk wins |
| The UI shows and changes the Gradle root (functional) | yes | step 6; the look is the owner's at the UI revamp |
| Agents' observed `gradlew` commands | no change | `observe.go` reads the program by `path.Base` (`cd app && ../gradlew` is already a `TestObserveTable` case); a record carries no cwd and goes to the hook's `--root`, which `init` writes as the project root |
| Phase 2 acceptance, "same-primitive check" | part | the UI and an agent learn the Gradle root from the same `workspace.describe` (step 3) |
| Several Gradle builds in one project used at once | no | one Gradle root at a time; a register row at step 7 |
| Linux | no | not run (macOS only) |

## Starting point

`main` at `8516cf0`; API `0.25.0` (`internal/domain/system/system.go`); ADRs up to 0074.

- **One root everywhere.** `internal/platform/project.go` `GradleRoot` (the upward walk for `settings.gradle(.kts)`) is the project root for `cmd/droidyard/root.go` `resolveRoot` (the hello root of `mcp`, `call`, `trash`; else cwd), `cmd/droidyard/client.go` `ui` (opens a project only when the walk finds one), `cmd/droidyard/init.go` (refuses unless the walk finds one or `--root`), and `internal/workspace/dialog.go` `chooseRoot`.
- **The trust hook hands one root.** `cmd/droidyard/main.go` `registry`: `authorize` (`pol.Authorize` over `ws.Describe(...).Root`) returns the canonical root as `Call.Authorized` to `build.run`, `build.assemble`, `build.tasks`, `build.errors`, `build.lastResult` (`internal/domain/build/build.go`, `tasks.go`), `project.modules`, `project.variants` (`internal/domain/project/project.go`), `sdk.requirements` (`internal/domain/sdk/requirements.go`), the git writes and `shell.exec`. The Gradle client keys everything on it: `Model` and its `Fingerprint`, `BeginBuild`, `LockRoot`, `Close` (`internal/domain/build/gradle/client.go`, `build.go`, `fingerprint.go`). The build queue's `Resource` is `gradle.root:<workspace id>` (`build.go` `resource`).
- **Crash and close map by root.** `OnCrash(root)` calls `ws.Degrade(root)`, which turns the root into a workspace id (`internal/workspace/workspace.go`); `CloseRoot` is called with the workspace's root.
- **Paths.** `extract.Input.Root` (the Authorized root) relativises build errors' `file`; the UI opens a record only when its path is relative to the project root (`web/src/lib/build/locate.ts`). `project.modules`' `dir` is "relative to the root", as the model reports it.
- **The workspace object** `workspace.Info` is `open`'s and `describe`'s result; no Gradle field.
- **The project file** is read and written only by the UI (`web/src/lib/start/projectfile.ts`: `build`, `device`, `applicationId`); no Go code reads it.
- **Fake sidecar** `internal/domain/build/gradle/gradletest` logs `started <pid>`, `served <method>`, `eof`, not the root it opens.
- **Sample** `samples/golden`: Kotlin DSL, a `build-logic` with its own `settings.gradle.kts`, wrapper Gradle 8.13, whose distribution is in `~/.gradle/wrapper/dists` (checked 2026-10-15).
- **Baseline, 2026-10-15 at `8516cf0`:** `go vet ./...` clean; `go test ./...` ok in every package. This file fails `internal/doccheck` `TestPlansIndexMatchesFiles` until the main session adds its row to `docs/plans/README.md` (the planner writes only this file).

## Acceptance

- [ ] A1. Opened at a top holding `.cursor/`, loose files and one Gradle build in `dev/`, a trusted workspace lists `dev`'s tasks, modules and variants and builds it, while its file tree, git and trust are the top's. Source: brief; Q1 §1, §2. Proven by: steps 2, 7.
- [ ] A2. A project whose root holds `settings.gradle(.kts)` behaves as before: its Gradle root is its root, and the daemon-level tests on such roots (`TestProjectModulesRequiresTrustThenSharesOneSidecar`, `TestSidecarCrashDegradesTheWorkspace`) pass untouched; unit tests change only the type of `Call.Authorized` they pass. Source: brief, rule 1. Proven by: steps 1, 2.
- [ ] A3. A `gradleRoot` from the project file is used only when, symlinks resolved, it stays inside the project root and holds a settings file; otherwise there is no Gradle root, with the reason. Source: brief, rule 3; Q1 §2, §3. Proven by: step 1.
- [ ] A4. With none or several, the Gradle capabilities fail `PROJECT_NO_GRADLE_ROOT` naming the key and the candidates, an untrusted root still fails `WORKSPACE_UNTRUSTED` first, and nothing is spawned. Source: brief; Q1 §4. Proven by: step 2.
- [ ] A5. Build errors' `file` and modules' `dir` are relative to the project root: the UI opens the errors, and an agent, whose cwd is the project root, uses both as they are. Source: `gradle.md` §Error model ("project-relative"); Q1 §5. Proven by: steps 2, 7.
- [ ] A6. `workspace.open` and `workspace.describe` report the Gradle root and its candidates; the schema diff is one minor version: the field, the code, and the descriptions of `workspace.describe`, `project.modules` and its `dir`. Source: Q1 §5. Proven by: steps 2, 3.
- [ ] A7. A sidecar crash during a build of `dev/` degrades the top's workspace, and closing it closes `dev/`'s connection unless another open workspace uses it. Source: `workspace-state.md` §Lifecycle; Q1 §8. Proven by: step 4.
- [ ] A8. `droidyard` and `droidyard init` started at the top find the top; started in `dev/app`, `dev/`; started in the home directory or in a folder of several projects, none. Source: brief; Q1 §6. Proven by: step 5.
- [ ] A9. The UI shows the Gradle root in use, offers the candidates (also after a choice, so it can switch back), a choice is saved and followed by the task list without a reopen, and no later UI write drops a `gradleRoot` it did not change. Source: brief, rule 3; Q1 §7. Proven by: steps 6, 7.
- [ ] A10. A real Gradle build, a real compiler error and a real MCP session from the top of a synthetic repository. Source: brief. Proven by: step 7.

## Run rules (step 7, and step 6's level-4 run)

- **K1.** Fixtures only under `B=~/Library/Caches/droidyard-dev/m2o` or `t.TempDir()`; an isolated daemon by the `verify-daemon` recipe (state under `/tmp`, never the default state root). No owner repository is named, opened or read, and no test data names one.
- **K2.** No adb, emulator or device; no process killed by name. Headless Chrome with its own profile over CDP for the UI.

## Steps

### 1. The Gradle root, resolved: rules, containment, candidates

- Builds: `internal/workspace/gradleroot.go`: `ResolveGradleRoot(projectRoot string, withCandidates bool) GradleRoot`, `GradleRoot{Abs string; Rel string; Candidates []string; Err *capability.Error}` (`Abs` canonical, empty when none; `Rel` `/`-separated, `"."` for the project root), in Q1 §2's order. `withCandidates` false (the Gradle capabilities' hook) skips (c) when (a) decides; `open`, `describe` and the error's `details` ask for them.
  - (0) a project root that is the home directory (`os.UserHomeDir`, canonicalised as the project root is) or `/`: rule (b) still applies (a project at home keeps today's behaviour); (a) and (c) are skipped, so without a settings file it has none, cause "not searched", and nothing else is read (an MCP binding may fall back to either, ADR-0019 §Sessions, and its open must not walk `~/Documents` or `/Volumes`).
  - (a) `gradleRoot` from `<projectRoot>/.droidyard/project.json`, read through `os.OpenRoot(projectRoot)` (a `.droidyard` or file that leaves the root is not read), at most 256 KiB, `schemaVersion` 1 only; a newer, unparseable or non-string value is absent (ADR-0046 §1). The value must be relative, without a `..` segment or a leading `/`; joined, `filepath.EvalSymlinks`, it must lie inside the canonicalised project root at a path-component boundary and be a directory holding a regular `settings.gradle` or `settings.gradle.kts`. A value that fails any test gives no root and `Err` with that reason: no fallback. Whether the key is valid or not, `Candidates` are still filled by (b) or (c), so the UI can switch back or repair; the key's folder is added when it is valid and not among them.
  - (b) the project root holds a regular settings file: `Rel "."`, `Candidates ["."]`, no scan.
  - (c) the scan: the root's entries and each subdirectory's, skipping names that start with `.`, `build`, `node_modules` and every symlink (`DirEntry.Type()`); a directory holding a settings file is a candidate and is not descended into; at most `maxScanDirs = 1000` directories examined (read or checked for a settings file), past which it stops: without a key, no root whatever it found, and the cause says so; a stopped scan only limits the candidates, and a valid key still decides; candidates sorted, at most 50; exactly one is the root.
  - `PROJECT_NO_GRADLE_ROOT` in `internal/capability/error.go` beside `PROJECT_MODEL_FAILED`, with its meaning; one constructor `noGradleRoot(projectRoot, cause, candidates)` with Q1 §4's message, cause, nextStep and `details {candidates}`. No descriptor lists it yet.
- Implements: Q1 §2, §3 (containment), §4 (the error's text).
- Check: `TestResolveGradleRoot`, a table on real directories in `t.TempDir()`:
  - rule 1 → `"."`, also with a `dev/settings.gradle` beside it (no scan);
  - a top with `.cursor/`, `README.md`, `dev/settings.gradle.kts`, `dev/build-logic/settings.gradle.kts`, `dev/app/` → `dev` (seen failing with descent into a candidate allowed: two candidates);
  - `a/b/settings.gradle` → `a/b`; `a/b/c/settings.gradle` → none; `dev` and `tools/x` → none, both candidates, cause "several"; nothing → none, no candidates;
  - `.hidden/`, `build/x/`, `node_modules/p/` holding settings → none; a settings that is a directory → none; `link -> <outside>/` holding settings → none (seen failing with the symlink skip removed);
  - key `tools/x` among two → `tools/x`, `Candidates ["dev","tools/x"]` (seen failing with (a) returning before the scan); key `a/b/c` (three deep) → used, `a/b/c` among the candidates; a bad key among two → no root, both candidates;
  - a project root equal to `HOME` (pointed at a temporary directory under `/var`, holding one build two levels down) → none, "not searched", no candidate (seen failing without (0), and with `HOME` left uncanonicalised); the same `HOME` holding `settings.gradle` → `"."`; key `"."` on a root without settings → error; keys `../sib`, `/abs`, `dev/../..` → error, not `dev` though `dev` is unique (seen failing with a fallback); key `inside` where `inside -> ../outside` holds settings → error (seen failing with the containment check made before `EvalSymlinks`); key to a folder without settings → error; key `7`, `schemaVersion` 2, unparseable JSON → absent → `dev`; `.droidyard -> <outside>/` whose file names `tools/x` → not read, so `dev` (seen failing with `os.ReadFile`);
  - `t.TempDir()` is under `/var`, `/private/var` canonically: a valid key resolves (seen failing with the project root left uncanonicalised in the comparison);
  - 1,001 empty directories with `maxScanDirs` lowered in the test → stops, the cause names the bound; the same with one build among the first directories → still none; with a valid key → the key's root and the candidates found so far; `withCandidates` false with a valid key → no directory read beyond the key's (a counting seam).
- Schema: no change.
- Needs: nothing; real filesystem.
- Not here: no caller; the daemon still uses one root.
- Rests on: a `gradleRoot` from the project file, which is untrusted repository content, never resolves outside the canonical project root, at level 4.
- Review: the Opus verifier (untrusted text deciding where project code runs), effort `high`.
- [x] Done. Evidence: `go test ./internal/workspace/` ok (2026-10-16); `TestResolveGradleRoot` (30 rows on `t.TempDir()`), `...KeyResolvesUnderACanonicalisedTempDir`, `...WithoutCandidates`, `...ScanBound`, `...CapsCandidates`, `...KeyAtTheCap`, `...ProjectFileFIFO`, `TestNoGradleRootError`. Seen failing, each through `go test -overlay`: descent into a candidate (two candidates); (a) returning before the scan (`[tools/x]`); (0) removed and `HOME` uncanonicalised (`x/y` resolved; the second rerun by the verifier); a bad key falling back (`dev`, ten rows); containment before `EvalSymlinks` (`../outside`); `os.Open` for `os.OpenRoot` (the outside file read); the project root uncanonicalised (20+ rows); `withKey` past the cap (51); a blocking open of the project file (hung on a FIFO). The symlink-skip reversal as planned does not fail: `DirEntry.IsDir()` is already false for a link, so the explicit test is redundant; the property is pinned by the `link -> OUTSIDE` rows against a scan that would follow links (verifier N1). Opus verifier: proceed, 0 blocking, 1 [test] (N1, this note), 5 [nit]; the main session fixed the nextStep text to Q1 §4's, the cap, and the project file opened `O_NONBLOCK`; a symlinked settings file still counts, as `platform.GradleRoot` does.

### 2. The Gradle capabilities run on the Gradle root: the first end to end

- Builds:
  - `internal/domain/build/gradle`: `type Roots struct{ Project, Gradle string }`, both canonical: the `Call.Authorized` of the Gradle capabilities.
  - `cmd/droidyard/main.go`: `authorizeGradle` runs `authorize` (trust on the project root, unchanged), then `workspace.ResolveGradleRoot(projectRoot, false)`; no root → the `Err` of a second resolution with candidates (its `details`); else `gradle.Roots`. It goes to `build.run`, `build.assemble`, `build.tasks`, `project.modules`, `project.variants`, `sdk.requirements`. `build.errors`, `build.lastResult`, the git writes and `shell.exec` keep `authorize`: they read history or run in the project root. `builddomain.Options` gains `AuthorizeGradle` beside `Authorize` (`build.go`'s five descriptors take one or the other; `tasks.go` the new one); `internal/schema/schema_test.go`'s registration passes `trustNothing` for both.
  - Handlers read `call.Authorized.(gradle.Roots)`: the client, `LockRoot`, `BeginBuild`, the fingerprint and `sdk.requirements`' `local.properties` take `.Gradle`; `extract.Input.Root` takes `.Project`; the build record's `Root` is `.Gradle`; `project.modules`' `dir` is `path.Clean(path.Join(Rel, dir))` (`"."` stays `"."` when the roots are equal; a module outside the Gradle root, `../lib`, comes out project-relative), its description "relative to the project root"; `project.modules`' own description says "the workspace's Gradle root" where it says "the workspace's root" and "the root".
  - The unit tests that pass a string `Call.Authorized` pass `gradle.Roots` instead, a mechanical edit: `internal/domain/build/build_test.go`, `tasks_test.go`, `internal/domain/project/project_test.go`, `internal/domain/sdk/requirements_test.go`.
  - The code in the six descriptors' `Errors`; `system.Version` one minor up (the milestone's one version, as plan m2c D17); `go test ./internal/schema -update`.
  - `gradletest`: the fake appends `opened <root>` on `connection.open` and `closed <root>` on `connection.close`.
- Implements: Q1 §1, §3, §4, §5; ADR-0015 §5 (trust first); ADR-0022 (a project-relative `file`).
- Check:
  - `TestGradleRootBelowTheProjectRoot` (`cmd/droidyard`, a daemon and the fake sidecar): a top with `.cursor/`, `README.md` and `dev/settings.gradle.kts`, opened at the top. Untrusted: `build.tasks` is `WORKSPACE_UNTRUSTED`, no sidecar started. After `droidyard trust <top>`: the fake's tasks are listed, and the log has `opened <canonical top>/dev` and no `opened <top>` (seen failing on `main`).
  - `TestNoGradleRoot`: a trusted top with `dev/` and `tools/x/` → `project.modules`, `build.tasks`, `build.run` and `sdk.requirements` each fail `PROJECT_NO_GRADLE_ROOT`, `details.candidates` `["dev","tools/x"]`, the nextStep naming `gradleRoot`, no sidecar started; untrusted, the same calls fail `WORKSPACE_UNTRUSTED` (seen failing with the two checks swapped); trusted, `build.lastResult` answers `BUILD_NOT_FOUND`, not `PROJECT_NO_GRADLE_ROOT` (seen failing with `build.lastResult` given `AuthorizeGradle`). Then `.droidyard/project.json` `{"schemaVersion":1,"gradleRoot":"tools/x"}` is written and the next `build.tasks` logs `opened <top>/tools/x` with no reopen (seen failing with the resolution cached at open).
  - `TestBuildErrorsRelativeToTheProjectRoot` (`internal/domain/build`, fake `BuildClient`): a Kotlin error line in a form `extract`'s golden tests recognise, naming `<top>/dev/app/src/main/kotlin/A.kt`, with `Roots{top, top/dev}` → `file` `dev/app/src/main/kotlin/A.kt`, not `external` (seen failing with `.Gradle` as `extract.Input.Root`).
  - `TestModuleDirRelativeToTheProjectRoot` (`internal/domain/project`): `.`, `app` and `../lib` under `Rel` `dev` → `dev`, `dev/app`, `lib`; equal roots → `.`, `app`.
- Schema: `version` one minor up; `PROJECT_NO_GRADLE_ROOT` in the `errors` of `build.run`, `build.assemble`, `build.tasks`, `project.modules`, `project.variants`, `sdk.requirements`, and in the codes list; the descriptions of `project.modules` and of its `dir` (and the generated `tools.md` row for `project.modules`, by `go test ./cmd/droidyard -update`, if `TestSkillToolMapMatchesTheRegistry` reads it). Nothing else.
- Needs: step 1; the fake sidecar; no JVM, no Gradle.
- Not here: the `gradle` field (step 3). Until step 4, a crash during a build of `dev/` does not degrade the top's workspace and a close leaves `dev/`'s connection to the sidecar's idle timeout; the commit says so. The queue's `Resource` stays per workspace: two open workspaces sharing one Gradle root (the top and an old `--root dev` one) are serialised by the client's per-root `LockRoot`, not the queue.
- Rests on: trust is decided on the project root before any Gradle root is resolved or anything spawned, and the Gradle capabilities receive only step 1's contained root, at level 4.
- Review: the Opus verifier (contract, trust), effort `high`.
- [x] Done. Evidence: `TestGradleRootBelowTheProjectRoot` (a real `serve` and the fake sidecar: untrusted `build.tasks` WORKSPACE_UNTRUSTED with no sidecar started; trusted, `build.tasks`, `project.modules`, `project.variants`, `sdk.requirements` and `build.run` on the top open `<top>/dev` and never the top), `TestNoGradleRoot` (six capabilities: untrusted first, then PROJECT_NO_GRADLE_ROOT with candidates `["dev","tools/x"]`; `build.lastResult` BUILD_NOT_FOUND; a written key takes effect on the next call with one `opened <top>/tools/x`), `TestBuildErrorsRelativeToTheProjectRoot`, `TestModuleDirRelativeToTheProjectRoot` (also `../lib` with equal roots, passed through). Seen failing through `go test -overlay`: the roots equal (`opened <top>`); the checks swapped (PROJECT_NO_GRADLE_ROOT before trust, six calls); `build.lastResult` on the Gradle hook; the resolution cached; `.Gradle` as the extract root (`app/...`); `dir` passed through. The verifier's mutation (build.run/assemble, project.variants and sdk.requirements on the project root) passed every test at first; the main session added those four calls to the end-to-end test, which then failed under it ("the sidecar connected to the project root"). Schema: 0.26.0; PROJECT_NO_GRADLE_ROOT on exactly the six; `project.modules` and its `dir` described; `tools.md` regenerated. Also `git_read_test.go`'s pinned API version (not in the step). Opus verifier: proceed, 0 blocking, 1 [test] (fixed as above), 3 [nit] (dead `hasLine` removed, `projectModel` comment fixed; `ProjectRel` recomputing the relative path from two canonicalisations left: equal unless a parent is renamed while open).

### 3. `workspace.open` and `workspace.describe` report the Gradle root

- Builds: `internal/workspace`: `Info.Gradle GradleInfo` (`json:"gradle"`, required) with `root` (relative, `"."` for the project root, absent when none), `candidates` (required, possibly empty) and `error` (the error shape, present when none), filled by the `open` and `describe` handlers (`capabilities.go`) from step 1. `Registry.Describe`, which every `RootOf` and `file`/`git` root lookup calls, resolves nothing. `describe`'s description adds "and its Gradle root"; the code in both descriptors' `Errors` (ADR-0026's precedent for an error carried in a field). Step 2's API version. `go test ./internal/schema -update` and `go test ./cmd/droidyard -update` (the skill's `skills/android/droidyard-android/references/tools.md` row for `workspace_describe` is generated from the description).
- Implements: Q1 §5.
- Check: `TestOpenAndDescribeReportTheGradleRoot` (`internal/workspace`, real files): a top with `dev/` → `{root: "dev", candidates: ["dev"]}`; rule 1 → `{root: ".", candidates: ["."]}`; two → no `root`, both candidates, `error.code` `PROJECT_NO_GRADLE_ROOT`; a project file written after the open changes `describe`'s answer with no reopen. `TestToolListModelFacingBytes` stays under its budget, the delta recorded. The schema test and `TestSkillToolMapMatchesTheRegistry` fail before `-update`.
- Schema: `workspace.open` and `workspace.describe` results gain the required `gradle` object; `describe`'s description; the code in both `errors`. Outside the schema: the `tools.md` row.
- Needs: step 1; step 2 for the version.
- Not here: `workspace.watch` rows and `workspace.state` events carry no Gradle root: the projects panel does not show it.
- Rests on: —
- Review: the Opus verifier (contract), effort `high`.
- [x] Done. Evidence: `TestOpenAndDescribeReportTheGradleRoot` (real files, through the capability layer; open and describe agree: a top with `dev/` → `dev`, `["dev"]`; rule 1 with another build below → `.`, `["."]`; two → no root, `[a b]`, PROJECT_NO_GRADLE_ROOT; none → `candidates` `[]` and no `root` on the wire; a project file written after the open decides at the next describe, a bad key then the error), `TestRegistryDescribeResolvesNoGradleRoot`. Seen failing through `go test -overlay`: the resolution cached per root; `Registry.Describe` resolving; `Root` absolute; and the schema and `tools.md` golden tests before `-update`. Schema: `gradle {root?, candidates, error?}` required on both results, with field descriptions; PROJECT_NO_GRADLE_ROOT in both `errors`; describe's description; version 0.26.0 unchanged. Tool-list budget: 28,059 of 29,000 bytes after the description change (about 28,047 before; inferred). Opus verifier (rerun after a network cut): proceed, 0 blocking, 1 [plan] (this evidence and the budget delta), 5 [nit]; the main session filled an empty candidate list when an info has no root, described the `gradle` fields, and reworded describe's description.

### 4. Crash and close follow the Gradle root

- Builds: `internal/workspace`: each workspace records the canonical Gradle roots its calls resolved (`NoteGradleRoot(id, abs)`, called by `authorizeGradle` after a successful resolution). The rules:
  - `NoteGradleRoot` takes `r.mu` and is a no-op unless the workspace is `ready` or `degraded`, so an operation that outlives a close's drain cannot leave a root on a closed workspace; the set is cleared at close and when a closed workspace opens (in `load`), never by an `Open` of an open one (every new tab and MCP session calls it).
  - `Degrade(root, cause)` copies the workspaces under `allMu`, as `Recover` does, and degrades each ready one whose recorded roots hold `root` (an ordinary project records its own root, so today's case is one of these).
  - `Close` snapshots, under `r.mu`, the recorded roots that no other open workspace records, and calls `CloseRoot` for each outside every lock: `gradle.Client.Close` waits up to 5 s for the root's lock, while a crashing build's path takes `r.mu` through `Degrade`.
- Implements: Q1 §8; `workspace-state.md` §Lifecycle (`degraded`), §Cleanup and crash recovery, and `gradle.md` §Protocol (a close releases the root's connection).
- Check:
  - `TestSidecarCrashDegradesTheTopWorkspace` (`cmd/droidyard`, as `TestSidecarCrashDegradesTheWorkspace`, the fake's `DieOn: build.run`): the top's workspace goes `degraded` with `BUILD_SIDECAR_CRASHED` and returns to `ready` after the next start (seen failing at step 2's state).
  - `TestCloseClosesTheGradleRootConnection`: after `build.tasks` on the top, `workspace.close` → `closed <top>/dev` in the fake's log. With a second workspace opened at `<top>/dev` that also ran `build.tasks`, closing the top writes no `closed` line and closing both writes one (seen failing with the "no other open workspace" test removed).
  - `TestDegradeByGradleRoot` (`internal/workspace`, unit): two open workspaces, one recorded root; only that one degrades; a closed one is untouched.
  - `TestNoteRacingClose` (`internal/workspace`, `-race`): `NoteGradleRoot` in a loop while the workspace closes and reopens; after the reopen its set holds no root noted while it was closing or closed (seen failing with the state test removed from `NoteGradleRoot`); a second `Open` of a ready workspace keeps its set (seen failing with the clear moved into `Open`).
- Schema: no change.
- Needs: step 2.
- Not here: a Gradle root the user switched away from keeps its connection until the close or the sidecar's idle timeout (Q1 §8; a register row at step 7). Two workspaces sharing a Gradle root both degrade on its crash (Q1 §8).
- Rests on: a crash's root maps back to the workspaces whose calls resolved it, read and written under the registry's lock, at level 4 (with `-race`).
- Review: the Opus verifier (concurrency, process lifetime), effort `high`.
- [ ] Done. Evidence:

### 5. Entry points from a repository's top

- Builds: `cmd/droidyard/root.go`: `projectOf(cwd string) (string, bool)`: the upward walk `platform.GradleRoot(cwd)` when it finds one (unchanged); else `cwd` when `workspace.ResolveGradleRoot(cwd, false)` has a root (a valid key or exactly one candidate; step 1's (0) already gives none for the home directory and `/`); else not found. `ui` (`client.go`) and `init` (`init.go`) use it. `init` prints the Gradle root it found, and its refusal names the three places it looked; `init.go`'s comment ("does not guess one") and `main.go`'s usage of `ui` and `init` say what it does now. `resolveRoot` (`mcp`, `call`, `trash`) is unchanged: its cwd fallback already gives the top (Q1 §6).
- Implements: Q1 §6; ADR-0045 §1 (superseded in part); ADR-0037 §2.
- Check:
  - `TestProjectOf`, a table with `HOME` pointed at a temporary directory: a top with `dev/` → the top; `dev/app` → `dev`; a folder with `a/` and `b/` each a Gradle build → not found; the same with a key → found; a `HOME` holding exactly one build two levels down → not found (seen failing without the home exclusion); `/` → not found with no directory read.
  - `TestUIOpensTheTopOfARepository` (the `TestUIOpensTheTokenisedURL` harness): `ui` started at the top opens `w=<the top's id>`; at a folder of two, no `w`.
  - `TestInitOnTheCommandLine` gains: `init --client cursor --dry-run` at the top names the top as the root; at a folder of two, exit 2 with the new text.
- Schema: no change.
- Needs: step 1.
- Not here: `workspace.chooseRoot` keeps its rule (the nearest ancestor with a settings file, else the folder): a top chosen in the dialog is already the top. The MCP binding does not change.
- Rests on: —
- Review: the main session's diff review.
- [ ] Done. Evidence:

### 6. The UI: show and change the Gradle root

- Builds: Svelte 5 (load `svelte-core-bestpractices` and `svelte-code-writer` first; regenerate `web/src/lib/api/capabilities.gen.ts`):
  - `web/src/lib/build/workspace.ts` `WorkspaceModel`: keeps `gradle` from `open` and `describe`; `refresh()` describes again.
  - `web/src/lib/start/projectfile.ts`: `ProjectPatch.gradleRoot?: string`; `applyPatch` sets it and keeps every other key. **Every write reads the file again first** (Q1 §7): `#write` calls `file.read`, applies the pending patches to what it read, and writes that with `ifRevision` the read's `revision` (`"absent"` when the read found no file, which then starts from `{schemaVersion: 1}`; ADR-0058, ADR-0059); on `FILE_REVISION_CONFLICT` it reads and patches again, at most three times, then shows the failure, as `trust.ts` retries the policy. A read that finds the file newer, unparseable or unreadable writes nothing and shows why. A write's settlement is observable (a listener or a promise from `update`), and settles also when the model refuses to write (its `BLOCKED` states), with the notice, so the select never waits forever. The UI never takes `gradleRoot` from the file for display: `describe` is the one source.
  - `web/src/lib/toolbar/GradleRootSelect.svelte`, beside the task selector: "Gradle: `<root>`" when there is one; a select of `candidates` whenever there is more than one, or the root is absent; with none, the error's message. A choice writes `gradleRoot`, and once the write settles the workspace describes again. The states come from a pure `gradleChoice(gradle)` in a `.ts` module.
  - `web/src/lib/build/tasks.ts` `TasksModel`, and the modules and variants loader, reload when `gradle.root` changes, as they do on a trust grant; `PROJECT_NO_GRADLE_ROOT` shows its message rather than a generic failure.
- Implements: Q1 §7; ADR-0046 §2 (the UI writes the project file).
- Check: Vitest: `applyPatch` sets `gradleRoot` and keeps unknown keys; a `gradleRoot` written by hand after the model loaded survives a later device choice, and so does one written by a second model on the same file (seen failing with today's rewrite of the copy read at load); a file the fake changes between the read and the write is read and patched again (seen failing without `ifRevision`); `refresh` carries `gradle`; `TasksModel` reloads once on a `gradle.root` change and not on an unrelated describe (seen failing with the comparison removed); `gradleChoice` covers one, several with none chosen, a root with two candidates (a select with the root chosen), none, and a key's error. `pnpm -C web run verify` clean; `node web/scripts/autofixer-gate.mjs` on the new `.svelte`. Level 4 (the main session, K2): headless Chrome on `$B/two`: both candidates listed; choosing `dev` writes the file and the task list fills; switching to `tools/x` and back to `dev` works.
- Schema: no change (the generated client follows step 3).
- Needs: step 3.
- Not here: the look (owner, the UI revamp); no typed path: a build deeper than two levels is named in the file by hand.
- Rests on: —
- Review: the main session's diff review and its level-4 run.
- [ ] Done. Evidence:

### 7. Level 5 and the close

- Builds: no code. The main session's run under K1 and K2:
  - (a) Fixtures: `$B/top` (`git init`, `.cursor/rules/x.mdc`, `README.md`, `notes.txt`, and `dev/` a copy of `samples/golden`); `$B/two` (`dev/` and `tools/x/`, each a minimal settings file); `$B/none`.
  - (b) `droidyard trust $B/top`; `call workspace.describe` → `gradle.root` `dev`; `build.tasks` lists `:app:assembleDebug`; `build.run` `assembleDebug` succeeds on real Gradle 8.13; a copy with one Kotlin error → `build.errors` `file` starts with `dev/app/`, and the UI opens it in the editor (headless Chrome).
  - (c) A scripted MCP stdio session `droidyard mcp --root $B/top`, as `init` writes it: `workspace_describe` shows `gradle.root`; `build_tasks` lists `dev`'s tasks.
  - (d) `droidyard init --client cursor --dry-run` at `$B/top` names `$B/top`; at `$B/two`, refused; `droidyard ui --no-autostart` at `$B/top` opens `w` of the top.
  - (e) `$B/two` and `$B/none` in the UI: the choice, the file, the tasks; the message.
  - (f) Documents: `workspace-state.md` §What a workspace is gains one paragraph on the two roots, linking Q1's ADR, and three sentences that become false are corrected: §Lifecycle's "What `open` does, completely" (it now reads the project file and searches), §Durable vs ephemeral state's "only ever preselecting", §Cleanup and crash recovery's "the root's connection"; `gradle.md` says the sidecar connects to the Gradle root; `mcp.md` §Transport and session's binding sentence checked (unchanged); `known-limits.md`: the project file's "last writer wins" row rewritten for step 6's read-before-write, and new rows (several Gradle builds at once; an old Gradle root's connection until close; the owner's repository untested); `docs/README.md` §Where the work stands; the index row.
- Implements: A1, A5, A9, A10.
- Check: each run's output under `$E`, the decisive lines quoted in the evidence: `gradle.root` `dev`, the task list, the build's `success`, the error's `dev/app/` path, the MCP `build_tasks` result, the `init` root line.
- Schema: no change.
- Needs: steps 1–6; real Gradle 8.13 (downloaded), a JDK 17, `samples/golden`'s dependencies cached; no device.
- Not here: the owner's repository (the owner opens it); `build.install` and devices (unchanged by this milestone).
- Rests on: Gradle and the sidecar need nothing from the project root, at level 5.
- Review: the Opus verifier at the close.
- [ ] Done. Evidence:

## Not in this milestone

- Several Gradle builds in one project used at once: one Gradle root at a time; a register row (step 7).
- A capability that sets `gradleRoot`: Q1 §7.
- The Gradle root in `workspace.watch` and the projects panel: not needed to show or change it (step 3).
- A search deeper than two levels, or from the cwd's ancestors: Q1 §2, §6.
- Started in `dev/app`, opening the top: Q1 §6.
- Included builds outside the Gradle root in the model's fingerprint: unchanged since 1g (`fingerprint.go`'s doc comment).
- Moving an existing `--root dev` setup (its `init` entries under `dev/`, the trust of `dev/`): the owner's, listed above.
- The control's look: the owner's, at the UI revamp.

## Design questions

### Q1. The project root and the Gradle root

- Blocks: steps 1–7. Delegated: the owner approved the design in the brief; the details below are the main session's under the owner's standing instruction. The main session writes it as an ADR at approval.
- The documents say: ADR-0019 §Sessions binds a session to `--root`, else the nearest ancestor with `settings.gradle(.kts)`, else the cwd; ADR-0045 §1 has `ui` look for a project by that rule; `init` refuses without one (code, `init.go`); ADR-0046 §2 says no capability interprets the project file and §3 that it only preselects; ADR-0015 §5 trusts a project root before its code runs; `gradle.md` §Error model makes `file` project-relative. Every one of them assumes the project root is the Gradle build's root.
- Draft decision, a new ADR. Supersedes: ADR-0045 §1 (in part: `ui` also finds a project at the cwd when its Gradle build is below); ADR-0046 §1 (in part: the shape gains `gradleRoot`), §2 (in part: the daemon reads `gradleRoot`, and the UI reads the file again before each write instead of "last writer wins"), §3 (in part: `gradleRoot` decides where Gradle runs, inside the trusted root). Relates to: ADR-0015 §5, ADR-0019 §Sessions (unchanged), ADR-0022, ADR-0026, ADR-0027, ADR-0037 §2, ADR-0045 §4.
  1. **Two roots.** The *project root* is the workspace's root: its id, `file.*`, `git.*`, `shell.exec`, trust, `init`, the project file, the MCP binding, Activity. The *Gradle root* is a directory inside it, the project root itself in the common case, holding `settings.gradle(.kts)`: the sidecar's connection, the model and its fingerprint, `build.run`, `build.assemble`, `build.tasks`, `project.*` and `sdk.requirements`.
  2. **Resolution**, at every call that needs it and in every `open` and `describe`, nothing cached. A project root that is the home directory or `/` gets only (b): no project file read, no search. (a) `gradleRoot` in `<project root>/.droidyard/project.json`, a relative `/`-separated path (`"."` is the project root), used when, symlinks resolved, it lies inside the project root and holds a regular settings file; otherwise there is no Gradle root and the reason says why, with no fallback. (b) Else the project root, when it holds a settings file. (c) Else the directories one or two levels below holding one, skipping hidden directories, `build`, `node_modules` and symlinks, and not descending into a directory that holds one (its subdirectories are its build's, as `build-logic`); at most 1,000 directories examined, past which (c) gives none (a valid key still decides). Exactly one is the Gradle root. The candidates of (b) or (c) are listed even when (a) decides or fails, so a choice can be changed or repaired.
  3. **Trust stays the project root's** (ADR-0015 §5): a Gradle capability checks the project root's trust first, then resolves. Containment makes trusting the project root what lets its Gradle build run; a repository's project file can only point at a directory inside the root it already controls. The moment between resolving and the sidecar opening the path matters only to a process already writing inside the trusted root, which can change its build files anyway.
  4. **No Gradle root:** the Gradle capabilities fail `PROJECT_NO_GRADLE_ROOT`: the cause says none, several, or the key's problem; `details {candidates}`; nextStep "choose the Gradle build in the Droidyard UI, or set `gradleRoot` in `<root>/.droidyard/project.json` to the folder, relative to the project root, that holds settings.gradle(.kts)".
  5. **The contract:** `workspace.open` and `workspace.describe` gain `gradle {root?, candidates, error?}`, `root` relative to the project root. Every published path stays relative to the project root: build errors' `file` (now with the Gradle root's prefix) and `project.modules`' `dir`. One minor API version.
  6. **Entry points.** The MCP binding and `droidyard call` keep ADR-0019 §Sessions' rule: at a top with no settings file above it, its cwd fallback already binds the top. `droidyard`/`ui` and `init` without `--root` find a project as: the nearest ancestor holding a settings file (unchanged); else the cwd itself, when (a) or (c) gives it a Gradle root, never the home directory or `/`; else none. Started inside the build (`dev/app`), the upward walk wins and the project is `dev/`. `workspace.chooseRoot` is unchanged.
  7. **The UI** shows the Gradle root in use and offers the candidates; a choice writes `gradleRoot` through `file.write`, as ADR-0046 §2 writes the rest, then describes again. No capability writes it. Because the file now decides where Gradle runs, every UI write reads the file again, patches what it read and writes with that read's revision as `ifRevision` (ADR-0058), reading again on a conflict, so a key written by hand or by another tab is not dropped; a file the read finds newer or unparseable is not written.
  8. **The daemon remembers**, per open workspace, the Gradle roots its calls resolved, so a sidecar crash degrades every open workspace that resolved the crashed root and a close closes each connection no other open workspace uses (`gradle.md` §Protocol; `workspace-state.md` §Cleanup and crash recovery). A root switched away from keeps its connection until the close or the sidecar's idle timeout.
- Alternatives:
  - The Gradle root in droidyard's state (`ws/<wsid>/workspace.json`), set by a capability: it would not travel with the project; the owner chose the project file.
  - A `workspace.setGradleRoot` capability: a contract for a write the UI already makes through `file.write`, and a tool that lets an agent redirect builds through droidyard.
  - Resolving once at open and caching it: a choice would need a reopen or an invalidation path; resolution is a stat and two directory reads.
  - An invalid key falling back to (b) or (c): a stale explicit choice would silently build another build.
  - The key consulted only when (b) fails: a user's choice could not override a unique (c), which the brief's rule 3 allows.
  - A deeper search, or the downward rule at every ancestor of the cwd: a folder of projects or the home directory would become a project.
  - Started in `dev/app`, the project root an ancestor whose project file names `dev/`: a walk above the build reading untrusted files, and a top without a project file (rule (c)) would still not be found; whoever wants the top starts there or passes `--root`.
  - Requiring `.git` at the cwd for the entry points: a project that is not a repository would not be found; "exactly one" and "never home" cover the folder of projects.
  - Trust per Gradle root: two human acts for one project.
  - Paths relative to the Gradle root: the UI and the agent, whose cwd is the project root, open files by project-relative paths.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| The owner's repository has exactly one Gradle build within two levels of its top (no agent may look at it) | 5, 7 | the owner's own open; several or a deeper one is the UI's choice (step 6) or the key |
| Gradle and the sidecar need nothing from the project root (the owner's `--root dev` already runs Gradle with `dev/` as its only root, by the brief) | 2, 7 | step 7 (b), real Gradle |
| The producers whose errors the UI opens report absolute paths (Kotlin, the Problems API), so relativising them to the project root stays right; one reporting paths relative to the Gradle root would be mis-rooted | 2 | step 7 (b), one Kotlin error; other producers not run |
| `samples/golden` builds on this host from cached dependencies | 7 | step 7 (b); if not, (b) stops at `build.tasks` and the build stays in this row |
| Linux | all | not run (macOS only) |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | [blocking] B1: the UI rewrites the whole project file from the copy it read at load, so a `gradleRoot` set by hand or by another tab is dropped and Gradle silently runs another build | Q1 §7 and the Supersedes line: every UI write reads the file again first; step 6's `#write` and a Vitest seen failing with today's rewrite; step 7 (f) rewrites the register row |
| 1 | revise | [blocking] B2: with a key deciding or failing, no candidates, so the UI could neither switch back nor repair | Q1 §2 and step 1: (b)/(c) fill the candidates even when (a) decides or fails, with table cases; step 6 shows the select whenever there is more than one |
| 1 | revise | [should] N1: `open`/`describe` would scan `/` or the home directory of a cwd-bound MCP session | Q1 §2 and step 1 (0): none, nothing read; a test; step 5 relies on it |
| 1 | revise | [should] N2: the recorded roots' lifetime and locks were unspecified | step 4: Note only while open, cleared at close and open; `Degrade` copies under `allMu`; `CloseRoot` outside every lock; `TestNoteRacingClose` |
| 1 | revise | [should] N3 A2's "unchanged" was false for unit tests; N4 step 3 not green (the skill's `tools.md`, the test name); N5 false sentences in `workspace-state.md` and `project.modules`' description; N6 no check that history reads keep the project-root hook; N7 a folder with one project now opens | A2 reworded; step 2 lists the test edits; step 3's `-update` and `TestToolListModelFacingBytes`; step 2's description and step 7 (f); step 2's `build.lastResult` check; an owner bullet and `init` printing the Gradle root |
| 1 | revise | nits: the scan bound and a stopped scan; the second hook field; ADR-0027 not the source of the release; A5's reason for `dir`, `../lib`; `TestObserveTable` | all taken (step 1, step 2, step 4 and Q1 §8, A5 and step 2, Scope) |
| 2 | proceed | B1 resolved (read before write, its Vitest fails on today's `#write`); B2 resolved (candidates under (a), the select's condition) | — |
| 2 | proceed | [should] S1 two tabs could still race between read and write; S2 the scan bound could be read as overriding a key; S3 rule (0) before (b) broke a project at home; S4 "cleared at open" could clear on every tab's `Open` | S1: `ifRevision` from the read (`"absent"` for none), three retries on `FILE_REVISION_CONFLICT`, a Vitest; S2: the bound limits candidates only, a table case; S3: (0) skips only (a) and (c), a `HOME` with settings gives `"."`; S4: cleared at close and in `load`, never by an `Open` of an open one, a test |
| 2 | proceed | nits: switch-back case and level-4 run; the write settles when blocked; `HOME` canonicalised; candidates scanned on every build call | all taken (step 6's `gradleChoice` and level-4 run; step 6's settlement; step 1 (0) and its case; `withCandidates`, step 1, 2, 5) |
## Changes
