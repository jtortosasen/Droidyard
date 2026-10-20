# Plan: Milestone 2m — Release builds and the one-line installer

- Status: Done
- Approved: 2026-10-12 by the main session under the owner's standing instruction (apply the recommended option), Delegated. Step 7's tag push still waits for the owner's explicit yes.
- Date: 2026-10-12
- Milestone: "2m — release builds and the one-line installer". The owner, 2026-10-12: a GitHub CI that produces a release build the owner can run on their Mac, and a one-line installer like other tools have; through `gh` while `jtortosasen/Droidyard` is private, a plain `curl | sh` once public, only the command changing. Owner addition, same day: updating is re-running the same one-liner, no separate updater.
- Phase: none. ADR-0068 pulls part of `mvp.md` §Out of scope's Distribution line in (ADR-0014 §Decision items 1 and 2).
- Contract: ADR-0068 (all of it); ADR-0014 §Decision 1–2; ADR-0002; ADR-0040 §1, §3, §Consequences; ADR-0037 §2 (absolute binary path); ADR-0019 §Processes 4 (`status`, `stop` never auto-start); `AGENTS.md` §Build and test; `.githooks/pre-commit`.

**The owner should see:**
- `droidyard version` changes from `0.23.0` to `droidyard v0.1.0 (API 0.23.0)`, or `dev` in place of the tag when the release script did not build it. The schema, the API version and `system.health` do not change.
- Step 2 pushes the `m2m` branch and opens a draft pull request, so CI runs before the close. Every push to it spends Linux minutes.
- Step 7 needs your yes before it pushes a tag. The plan proposes `v0.1.0`, a real release. A tag is never moved: if a release run fails, the next try is `v0.1.1`.
- Until the close merges `install.sh` into `main`, step 7 fetches it with `?ref=m2m`. The exact one-liner runs after the merge.
- The curl path cannot be exercised for real while the repository is private. Only fakes prove it.

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| ADR-0068 §1: four targets, `CGO_ENABLED=0` from one Linux runner, the archive names, `checksums.txt` | yes | step 4 |
| §2: `vX.Y.Z` tags start the release; the build version is stamped and shown beside the API version | yes | steps 3, 6 |
| §3: CI on `main` and pull requests runs the hook's whole-tree checks on Linux, with toolchains and actions pinned | yes | steps 1, 2 |
| §3: the release runs the same checks first, and publishes nothing when one fails | yes | step 6 |
| §4: the build is a script in the tree that the Mac can run | yes | step 4 |
| §5: `install.sh`; owner addition: a re-run updates, or does nothing when current | yes | step 5 |
| §6: the one-liner, private and public | part | private: steps 7, 8; public: fakes only (Unverified) |
| §7: no signing, notarization, Homebrew or `.pkg` | no | ADR-0068 §7 |
| §Consequences: the three documents | yes | step 8 (the close) |
| ADR-0014 items 3–6 and `init` for Codex | no | they stay in `known-limits.md`'s Distribution row (ADR-0068 §Consequences) |
| Tests on macOS in CI | no | ADR-0068 §Consequences: the pre-commit hook is the macOS run |

## Starting point

Read at `3eec3e4` (`main`).

**Precondition (main session, before branching `m2m`).** Commit ADR-0068 and its index row. Give ADR-0068 the header line `Supersedes: ADR-0002 §Consequences (in part: CI runs the tests on Linux only; the pre-commit hook is the macOS run)`, and give ADR-0002 its `Superseded in part by: ADR-0068 (§Consequences)` line. ADR-0068 weighed this in §Alternatives considered (the macOS runner) but did not record it as superseding.

- `cmd/droidyard/main.go`: `version` prints `system.Version`, the API version (`const Version = "0.23.0"`).
  - A grep over `cmd/`, `internal/`, `web/scripts/`, `.claude/`, `docs/` and `skills/` finds nothing that parses that output.
  - The hello compares API versions only (`internal/daemon/daemon.go:376`, `DAEMON_VERSION_MISMATCH`).
  - `status` exits 0 only when the socket answers, so a daemon of another API version gives exit 1 (`client.go:312`).
  - `stop` works across versions, by the lock and SIGTERM (`client.go:465`).
- `web/embed.go`: without `pnpm -C web build`, the daemon serves ADR-0040 §2's placeholder, so a release without the UI fails silently.
- Pins: `web/.node-version` is `24.13.1`, `web/package.json` has `packageManager: pnpm@12.10.1+sha512…`, and `go.mod` says `go 1.27`, with no `toolchain` line.
- The embedded artefacts (jars, `reader.dex`, `skills/android`) are checked in. There is no `.github/` and no tag.
- **Tests that need host tools.**
  - They skip with a reason when the tool is absent: kotlinc and javac (the sidecar skips on a version mismatch too), build-tools 37.0.0 with javac (`reader`, `formapp`, which check presence only), `DROIDYARD_GRADLE_TESTS`, git, lsof, root.
  - The `ubuntu-24.04` runner is not bare: it ships JDKs, an Android SDK (`ANDROID_HOME`), lsof and `gh`. Tests that skip in a bare container can therefore run there.
  - `reader/build.sh` only warns when javac is not 17.x, so its byte comparison can fail on another javac.
  - The git tests set their own identity. `cmd/droidyard` empties `PATH` for adb and the JVM.
  - Linux has never run.
- The habit for testing shell is Go tests that drive `sh` (`trash_cmd_test.go`, `hook_test.go`). The hook maps a staged non-Go file to the nearest directory holding Go files, so a root `install.sh` maps to none (`.githooks/pre-commit:85`).
- The host has `docker`, `colima`, `gh` and `go` in `/opt/homebrew/bin`. The planner could not check whether colima is running. `~/.local/bin` exists, with no `droidyard` in it.
- Baseline, 2026-10-12 at `3eec3e4`, with ADR-0068 in the tree: `go vet ./...` is clean, `go test ./...` is ok in all 33 packages with tests, and `gofmt -l .` prints nothing (the reviewer's rerun).

## Acceptance

- [x] A1. CI on a Linux runner runs `pnpm -C web install --frozen-lockfile`, `pnpm -C web run verify`, `go build ./...`, `gofmt -l .` (empty), `go vet ./...` and `go test ./...` on the pull request, and passes. Its log shows Go from `go.mod`, Node `v24.13.1` and pnpm `12.10.1`. Source: ADR-0068 §3. Proven by: steps 1, 2.
- [x] A2. On the runner, every test passes or skips with its reason. Each Linux-only or runner-only failure is fixed, or is skipped with a reason and a register row. Source: ADR-0068 §Consequences. Proven by: steps 1, 2.
- [x] A3. `droidyard version` prints `droidyard <build> (API <system.Version>)`. `<build>` is the stamped tag, or `dev`. The schema, the API version and `system.health` do not change. Source: ADR-0068 §2. Proven by: steps 3, 7.
- [x] A4. `release/build.sh vX.Y.Z <out>` on the Mac does the following:
  - It builds the UI, then the four targets, as `droidyard_vX.Y.Z_<os>_<arch>.tar.gz`, each holding only `droidyard`, and a `checksums.txt` that verifies.
  - The native binary prints the stamped version, and every binary embeds the built UI.
  - A second run gives byte-identical binaries.
  - It refuses a malformed version, and a missing UI build.
  - Source: ADR-0068 §1, §4; ADR-0040 §Consequences. Proven by: step 4.
- [x] A5. `install.sh` does the following:
  - It maps `uname` to the four pairs, and refuses any other pair before any download.
  - It uses `gh` when that is authenticated, and `curl` otherwise.
  - It shape-checks every tag, given or discovered.
  - It checks the SHA-256 before installing. On a mismatch or a missing line it refuses, and leaves the old binary.
  - It refuses a binary whose `version` does not name the tag.
  - It replaces atomically into `$DROIDYARD_INSTALL_DIR`, by default `~/.local/bin`.
  - It installs the latest release, or `DROIDYARD_VERSION` when that is set.
  - It edits no profile. It prints the `PATH` line only when the directory is not on `PATH`.
  - It says `droidyard stop` when a daemon answers to the old binary or the new one, and stops nothing.
  - It is idempotent. When the version is already there, it prints `already at Y` and exits 0 without downloading. An update prints `installed X, installing Y`.
  - Source: ADR-0068 §5; owner, 2026-10-12. Proven by: step 5.
- [x] A6. The workflows meet these conditions:
  - Third-party actions are pinned by a 40-hex SHA.
  - Every checkout sets `persist-credentials: false`.
  - Only the publish job holds `contents: write`, and it runs no code from the repository or npm.
  - Every job runs on Linux.
  - The release runs the CI checks first, and publishes only after them.
  - Source: ADR-0068 §3. Proven by: steps 2, 6, 7.
- [x] A7. A tag pushed to GitHub produces a release with five assets, built by CI. Installed by the `gh` path on the owner's Mac into a scratch directory, it gives the following:
  - `version`, `serve`, `status` and `stop` work against an isolated state.
  - The UI served is the built app.
  - The darwin/arm64 binary is ad-hoc signed and carries no `com.apple.quarantine`.
  - A second run says `already at`.
  - Source: ADR-0068 §2, §5, §6, §Context (both assumptions). Proven by: steps 1, 7.
- [ ] A8. After the merge, the exact private one-liner of ADR-0068 §6 says `already at`, and the three documents of §Consequences are changed. These are post-merge observations, reported at the close, and this item stays unticked in the record with that note (`milestone-workflow`, the close row). Source: ADR-0068 §6, §Consequences. Proven by: step 8.

## Steps

### 1. Linux and the ad-hoc signature, before anything is built on them

- Builds:
  - (a) The main session measures. The source is `git archive HEAD | tar -x -C "$W"` (`W=$(mktemp -d /tmp/dylx.XXXXXX)`); the Mac's `node_modules` is never mounted.
    - **Go.** In `golang:1.27-bookworm` (trixie has no `openjdk-17`), add `apt-get install -y lsof openjdk-17-jdk-headless`. As a non-root user made with `useradd -m` (the runner is non-root), run `go vet ./...`, `go test ./...` and `go test -v ./... | grep -- '--- SKIP'`.
    - **Web.** In `node:24.13.1-bookworm`, as user `node`, run `cd web && corepack pnpm install --frozen-lockfile && corepack pnpm run verify`.
    - **The ADR-0068 §Context signature assumption.** In the Go container, build `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-s -w -buildid=" ./cmd/droidyard`. On the Mac, run `codesign -dv` (expect `Signature=adhoc`) and `./droidyard version`.
    - Outputs stay in the scratchpad.
    - If `docker info` fails and `colima start` does not fix it, (a) is skipped. Step 2's first run then measures Linux, and step 4's build (on the Mac) checks the signature.
  - (b) The rule for failures, here and in step 2's runner run:
    - A *small* fix is test-only, or a platform adapter (`*_linux.go`, `internal/platform`) of about 30 lines or fewer. The implementer makes it in this step.
    - A test that rebuilds an artefact and fails on another javac gets the sidecar's gate: skip when the host's javac is not the recorded one.
    - Anything else gets `t.Skip("linux: <reason>")` behind `runtime.GOOS`, a §Changes line, and a register row at the close.
    - More than five non-small failures stop the milestone for the owner, with a proposed split.
- Implements: risk first. ADR-0068 §Consequences ("the plan finds out first"), §Context (the signature); ADR-0002.
- Check:
  - `go test ./...` and `pnpm run verify` pass in the containers after (b).
  - The `SKIP` list is recorded. Each skip that depends on a tool names that tool.
  - The cross-compiled binary shows `Signature=adhoc` and runs on the Mac.
  - Each fix is seen failing before the step, as logged in (a).
- Schema: no change.
- Needs: Docker through colima, or step 2.
- Not here:
  - amd64: the containers run linux/arm64, and the runner is the authority (step 2).
  - No `-race`: the hook does not run it.
- Rests on: `go test ./...` passing on the GitHub runner, at level 5. Only step 2 reaches that level. This step is the cheap proxy.
- Review: the main session's diff review. A domain fix goes to the Opus verifier.
- [x] Done. Evidence: (a) linux/arm64 containers, 2026-10-12: `pnpm run verify` passes (75 files, 785 tests, build); the darwin/arm64 cross-build shows `Signature=adhoc` and `version` runs on the Mac; `go vet` passes, `go test` had six failures. (b) all test-only: `unix.Getsid` for `syscall.Getsid` (cmd/droidyard, emulator); EOF or ECONNRESET for a refused peer (daemon); the `/var` alias row only where `/var` is a symlink (file); start time not after the first child's, Linux ticks are 10 ms (platform); the rewrite's mtime moved on by hand (policy); the dummy sidecar ignores Pdeathsig SIGTERM in the two killed-daemon tests; a 300 ms wait for the launch record (emulator, a 1-in-4 flake); one skip, `TestKilledDaemonLeavesTheFollowForTheNextStart` on Linux (Pdeathsig). Each was seen failing before. After: Linux `go test -v ./...` no FAIL, 33 skips (build-tools 37.0.0: 3; Gradle level 5: 22; case-sensitive volume: 4; `/tmp` not a symlink: 2; Pdeathsig: 1, plus one subtest); Mac green. `TestReaderClientSerialisesPerDeviceAndNotTwoDevices` failed once in five Linux runs, untouched.

### 2. CI on pull requests and `main`: the first run on GitHub

- Builds:
  - `.github/workflows/ci.yml`:
    - Triggers: `push: branches: [main]`, `pull_request` and `workflow_call`.
    - `permissions: contents: read`.
    - One job, `checks`, on `ubuntu-24.04`, with `timeout-minutes: 20`.
    - Setup steps:
      - `actions/checkout` with `persist-credentials: false`;
      - `actions/setup-go` with `go-version-file: go.mod`;
      - `pnpm/action-setup` with `package_json_file: web/package.json`;
      - `actions/setup-node` with `node-version-file: web/.node-version`.
    - It prints `go version`, `node --version` and `pnpm --version`, then runs ADR-0068 §3's six commands. The gofmt one is `test -z "$(gofmt -l .)"`.
    - Each action is written `owner/repo@<40-hex SHA> # vX.Y.Z`. The SHA is resolved from the action's current release tag with `gh api repos/<o>/<r>/git/ref/tags/<tag>`, dereferencing annotated tags.
  - `release/doc.go`: `package release`. Its comment says the package holds the release script and its tests, and nothing the binary imports.
  - `release/workflows_test.go` reads the workflow files as text. It uses no YAML module, because `go.yaml.in/yaml/v4` is only an indirect dependency and a new direct one needs a document. Its tests:
    - `TestActionsPinnedBySHA`: every `uses:` is `./…` or `owner/repo@[0-9a-f]{40}`.
    - `TestLinuxRunnersOnly`: every `runs-on:` is `ubuntu-…`.
    - `TestCIRunsTheHookChecks`: the six commands are present.
    - `TestCheckoutDropsCredentials`: every `actions/checkout` is followed by `persist-credentials: false`.
    - `TestNoWriteInCI`: `ci.yml` holds no `: write`.
  - `.githooks/pre-commit`: the `case` that maps `schema/*` also maps `install.sh` and `.github/*` to `release`.
  - The main session pushes `m2m` and runs `gh pr create --draft --base main`. That is the trigger: a `workflow_dispatch` workflow has to be on the default branch first.
- Implements: ADR-0068 §3; ADR-0002.
- Check:
  - (a) The five tests pass. Each is seen failing on a mutated copy: a tag-pinned `uses:`, a `macos-latest` runner, a missing `go vet`, a checkout without the line, and `contents: write`.
  - (b) With an `install.sh` placeholder staged, `sh -x .githooks/pre-commit 2>&1 | grep 'go test -count=1.*/release'` matches, and it matches nothing without the mapping. Then unstage the placeholder, and commit nothing.
  - (c) The pull request's `checks` run is green. Its log prints Node `v24.13.1`, pnpm `12.10.1` and a Go 1.27 patch release. Every test that fails only on the runner goes through step 1(b). Level 5: the run URL. The `SKIP` list is recorded from `go test -v` in a one-off step, or else from the summary.
- Schema: no change.
- Needs: step 1; `gh` authenticated on the Mac.
- Not here:
  - No macOS job (ADR-0068 §Consequences).
  - No caching or concurrency tuning.
  - The build dry run comes in step 4.
- Rests on: the runner takes Node and pnpm from `web/`'s pins and Go from `go.mod`, at level 5. Only the run log proves it.
- Review: the main session's diff review.
- [x] Done. Evidence: (cut to `ci.yml`, §Changes) run 38281599901 green on `ubuntu-24.04` (PR #1), logging go1.27.2 linux/amd64, Node v24.13.1, pnpm 12.10.1. Run 38280846547 had two runner-only failures, both test premises, fixed by step 1(b): `TestLongStateRootsDoNotShareAFallbackSocket` (the runner sets `XDG_RUNTIME_DIR`; the test hides it) and `TestBuildScriptReproducesTheDex` (the runner's javac is not 17.0.11; the test skips, as the sidecar's does). (a), added after the owner's second word: the five `release/` tests pass, each seen failing on a mutated copy of `ci.yml` (a tag-pinned `uses:`, `macos-latest`, no `go vet`, no `persist-credentials`, `contents: write`). (b): with a placeholder `install.sh` staged, `sh -x .githooks/pre-commit` ran `go test -count=1 github.com/jtortosasen/droidyard/release`; placeholder unstaged and removed.

### 3. The build version: stamped at link time, shown beside the API version

- Builds:
  - In `cmd/droidyard/main.go`: `var build = "dev"`, which the release sets with `-ldflags "-X main.build=vX.Y.Z"`.
  - `versionLine(build, api string) string` returns `droidyard <build> (API <api>)`.
  - `version` prints `versionLine(build, system.Version)`.
  - The usage line becomes "Print the build version and the API version."
  - `cmd/droidyard/version_test.go`:
    - `TestVersionLine` checks the format for `v1.2.3` and for `dev`.
    - `TestVersionCommand` re-executes the binary through the `reexec` seam, runs `version`, and expects `droidyard dev (API <system.Version>)`.
- Implements: ADR-0068 §2, which leaves the display to the plan. The API version is untouched, so a shim and a daemon of two builds that share one API version still talk.
- Check: both tests pass. `TestVersionCommand` fails against today's output. Step 4's native-binary check proves that the `-X` symbol takes effect.
- Schema: no change. `system.health`, `daemon.lock` and MCP `serverInfo` keep the API version.
- Needs: none.
- Not here:
  - The build version is not added to `system.health` or `daemon.lock`. That would change the schema, and ADR-0068 asks for the CLI only.
  - No `--version` flag.
  - No `git describe` stamp on a dev build.
- Rests on: —
- Review: the main session's diff review.
- [x] Done. Evidence: `TestVersionLine`, `TestVersionCommand` pass; `TestVersionCommand` seen failing with `version` printing `system.Version` alone ("0.23.0\n"). The `-X` symbol's effect is step 4's check.

### 4. `release/build.sh`: the release, built anywhere

- Builds:
  - `release/build.sh <version> <outdir>`, POSIX sh, which finds the repository from its own path. It does the following:
    - It refuses a version not shaped `^v[0-9]+\.[0-9]+\.[0-9]+$` (ADR-0068 §2), and refuses an `<outdir>` that exists and is not empty.
    - It runs `pnpm -C web build`, then refuses if `web/dist/app/index.html` is absent.
    - For each of `darwin/arm64 darwin/amd64 linux/amd64 linux/arm64`, it builds with `CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "-s -w -buildid= -X main.build=$V"`.
    - It refuses a binary that lacks the name of one hashed file of `web/dist/app/assets/` (checked with `grep -c`).
    - It refuses when the host pair's binary does not print `droidyard $V (API …)`.
    - It packs `COPYFILE_DISABLE=1 tar -czf "$out/droidyard_${V}_${os}_${arch}.tar.gz" -C <dir> droidyard`. The variable keeps macOS from adding `._` entries.
    - It writes `checksums.txt` with `sha256sum`, or with `shasum -a 256` when the first is missing.
    - Its header comment says what the script does and points to ADR-0068 for why.
  - `ci.yml` gains one step after the checks: `release/build.sh v0.0.0 "$RUNNER_TEMP/release"`. A broken script then fails a pull request, not a tag.
- Implements: ADR-0068 §1, §4; ADR-0040 §Consequences ("must build the UI first").
- Check: a level-3 command on the Mac, `release/build.sh v0.0.0 "$S/a"`, run by the implementer and rerun by the main session. It must show the following:
  - (i) Five files, named as §1 says.
  - (ii) `shasum -a 256 -c checksums.txt` reports every file OK.
  - (iii) `tar -tzf` of each archive lists exactly `droidyard`.
  - (iv) `file` reports Mach-O arm64, Mach-O x86_64, ELF x86-64 and ELF aarch64.
  - (v) The darwin/arm64 binary prints `droidyard v0.0.0 (API 0.23.0)`, and `codesign -dv` shows `Signature=adhoc`.
  - (vi) A second run, into `$S/b`, extracts binaries with the same hashes as the first.
  - (vii) The script refuses each of `v0.0`, a non-empty `<outdir>`, and a `pnpm` shim that exits 0 without writing `index.html` (with `web/dist/app` moved aside, then restored).
  - CI's dry run is green on Linux, and its native-binary check runs linux/amd64.
- Schema: no change.
- Needs: steps 2, 3; Node and pnpm on the Mac (ADR-0040).
- Not here:
  - Reproducible archives: GNU tar and bsdtar disagree on flags. The binaries are the reproducible unit.
  - No GoReleaser (ADR-0068 §Alternatives considered).
  - No Go test of the script, because a Go test may not need Node (ADR-0040 §3). The CI dry run is its regression check.
- Rests on: the binaries carry the built UI and the stamped version, at level 3. The script itself checks both on every run.
- Review: the main session's diff review.
- [x] Done. Evidence: on the Mac, (i)–(vii) by the implementer; (i), (ii), (v) rerun by the main session (four archives and `checksums.txt`, every sum OK, `droidyard v0.0.0 (API 0.23.0)`, `Signature=adhoc`). CI run 38284160051 green, its dry run built on linux/amd64 (the native-version check ran there). The script removes `web/dist/app` before building, so a stale UI cannot pass its check. (vi) and (vii) rerun by the main session at the close: four binaries byte-identical between two runs; `v0.0` and a non-empty outdir refused (the pnpm-shim refusal rests on the implementer's run).

### 5. `install.sh`: install, update, or nothing to do

- Builds:
  - `install.sh` at the root, POSIX sh with `set -eu`. The body is a `main` function called on the last line, so a truncated `| sh` runs nothing. It reads nothing from stdin. `R=jtortosasen/Droidyard`. In order:
    1. **Pair.** `uname -s` must give `Darwin` or `Linux`. `uname -m` must give `arm64` or `aarch64` (arm64), or `x86_64` or `amd64` (amd64). Any other pair is refused with the supported list, before any download.
    2. **Transport.** It uses `gh` if `gh auth status --hostname github.com` succeeds, else `curl`. With neither on `PATH`, it says it needs `gh` or `curl` and exits non-zero. When `curl` fails, it says that a private repository needs `gh` installed and authenticated, and exits non-zero.
    3. **Target.** It uses `DROIDYARD_VERSION`, or the latest release: `gh release view --repo R --json tagName -q .tagName`, or the last path element of `curl -fsSLI -o /dev/null -w '%{url_effective}' https://github.com/R/releases/latest`. Either way, a tag not shaped `vX.Y.Z` is refused. A repository with no release redirects to `/releases`.
    4. **Current.** `dir=${DROIDYARD_INSTALL_DIR:-$HOME/.local/bin}`. If `$dir/droidyard` is executable, `current` is the second word of its `version` output when that word is shaped `vX.Y.Z`. Otherwise it is unknown, as for a pre-2m build that prints `0.23.0`.
    5. **Idempotence.** When `current` equals the target, pinned or not, it prints `droidyard: already at <target>` and exits 0 without downloading. Otherwise it prints `droidyard: installed <current>, installing <target>`, or `droidyard: installing <target>` when no binary is there.
    6. **Download.** Into `mktemp -d`, it fetches `droidyard_<target>_<os>_<arch>.tar.gz` and `checksums.txt` with `gh release download <target> --repo R --pattern … --pattern checksums.txt --dir <tmp>`, or with `curl -fsSL -o` from `https://github.com/R/releases/download/<target>/…`. One `trap` removes `<tmp>` and any `$dir/.droidyard.new.$$`.
    7. **Verification.** The expected sum is the line of `checksums.txt` whose name field equals the archive's name. A missing line or a mismatch is refused, naming both sums. The sum comes from `sha256sum`, else `shasum -a 256`; with neither it refuses. Only then does it run `tar -xzf <archive> -C <tmp> droidyard`, which extracts that one member. It refuses unless `<tmp>/droidyard version` says `droidyard <target> (API …)`, which also catches a wrong architecture.
    8. **Daemon, before.** If the old binary exists and its `status` exits 0, it remembers that a daemon answers. The old binary most likely started that daemon. A new binary with another API version gets `DAEMON_VERSION_MISMATCH`, so its `status` exits 1.
    9. **Install.** It runs `mkdir -p "$dir"`, copies to `$dir/.droidyard.new.$$` with `chmod 755`, then `mv -f` onto `$dir/droidyard`. The rename is atomic, and a running daemon keeps its old inode.
    10. **After.** If `$dir` is not on `PATH`, it prints `export PATH="<dir>:$PATH"` and the profile that line belongs in, and writes nothing. If (8) remembered a daemon, or the new binary's `status` exits 0, it prints that the running daemon still runs the previous binary, and to run `droidyard stop`. It stops nothing.
  - `release/install_test.go` drives `sh ../install.sh`. When `dash` is on `PATH`, it reruns the cases under `dash`. The environment is hermetic:
    - `PATH` is a fake `bin` directory, then a tools directory of symlinks to an allowlist (`sh`, `tar`, `gzip`, `mktemp`, `cp`, `mv`, `chmod`, `mkdir`, `rm`, `cat`, `grep`, `sed`, `awk`, `cut`, `tr`, `head`, `dirname`, `ln`, `sha256sum` or `shasum`, plus whatever `install.sh` uses, found by `command -v` at test start). Nothing else is on `PATH`. A case with an absent `gh` or `curl` asserts first that `command -v` fails for it.
    - The fakes are written by the test:
      - `uname` gives answers set by the environment.
      - `gh` covers `auth status`, `release view` and `release download`.
      - `curl` covers `-w '%{url_effective}'` and `-o`, and fails on demand.
      - Both serve one fixture directory and log every call.
    - The fixture archives are built in Go (`archive/tar`, `compress/gzip`). Each holds a fake `droidyard` script whose `version` line is chosen and whose `status` exits by scenario.
    - `HOME` is a temporary directory with `.zshrc`, `.bashrc` and `.profile` of known bytes.
  - The cases:
    - Fresh install through `gh`, and through `curl` with no `gh`. `gh` unauthenticated falls back to `curl`. With neither, the script fails with the `gh` or `curl` wording, not the curl-failure one, and installs nothing.
    - `DROIDYARD_VERSION` pinned: `latest` is not queried. A malformed pin is refused. A discovered tag of `releases` is refused.
    - An unsupported pair (`FreeBSD`, `riscv64`): the script fails and the transport logs are empty.
    - A checksum mismatch, a missing line, and an archive whose `version` says `v9.9.9`: each is refused, the old binary is byte-identical, and no `.droidyard.new.*` is left.
    - Update X to Y: the output says `installed X, installing Y`, and a hard link to the old file keeps X's bytes, so the file was renamed, not overwritten.
    - Already at Y, latest and pinned: exit 0, no download call in either log, and the inode is unchanged.
    - An unknown current version (a binary printing `0.23.0`): it installs.
    - The `export` line is printed only when the directory is not on `PATH`. The profiles are byte-identical in every case.
    - Daemon: the old `status` exits 0 and the new one exits 1, then the old exits 1 and the new exits 0. Each prints the advice, and no `stop` is logged.
    - A truncated script, its first half piped to `sh`: no install, and no fake is called.
- Implements: ADR-0068 §5, §6 (the same URLs public or private); owner, 2026-10-12 (re-running updates).
- Check: every case passes. Each of these mutations fails a case:
  - no checksum comparison, which fails the mismatch case;
  - `cp` onto the target instead of a rename, which fails the update case's hard link;
  - no equality short-circuit, which fails the `already at` case;
  - no version check after extraction, which fails `v9.9.9`;
  - no tag shape check on a discovered tag, which fails `releases`;
  - only the new binary's `status` consulted, which fails the first daemon case;
  - a `main` call placed before the function's end, which fails the truncated case;
  - the `neither` branch removed, which fails `neither`: the fall-through to a missing `curl` prints the curl-failure wording instead.
- Schema: no change.
- Needs: step 2 (the `release` package and the hook mapping); step 3 (the `version` line).
- Not here:
  - No Rosetta detection: an amd64 binary runs under Rosetta.
  - No uninstall (ADR-0068 §7).
  - No retries or timeouts.
  - No signature beyond the release's own checksums (ADR-0068 §7). The transport is HTTPS from the same repository.
- Rests on: the script refuses before any write when the sum does not match, at level 2 (fakes). Step 7 adds a real download.
- Review: the Opus verifier, at effort `high`: this is a downloader that replaces an executable.
- [x] Done. Evidence: `release/install_test.go` passes under `sh` and `dash` on the Mac (~30 s) and on Linux (25 tests, none skipped, in the container; CI run 38287594429 green). The plan's eight mutations each fail a case. Opus verifier: no blocking finding; its six non-blocking ones fixed in the same step, each seen failing on a mutated copy: no binary runs before its sum is checked (the reorder now fails four cases), `gh auth status --active`, `mktemp` beside the target, a symlink member refused and only `droidyard` extracted, `--repo github.com/…`, the PATH advice also when already current, trailing slashes stripped, fresh-install daemon wording. Not tested: INT/TERM through the trap.

### 6. The release workflow

- Builds:
  - `.github/workflows/release.yml`: it triggers on `push: tags: ['v*.*.*']`, with top-level `permissions: contents: read`. It has three jobs:
    - `checks`: `uses: ./.github/workflows/ci.yml`.
    - `build`: `needs: checks`, `ubuntu-24.04`, `contents: read`, with the same setup as `ci.yml` and the same SHAs. `env: TAG: ${{ github.ref_name }}`. The tag reaches the shell only as `"$TAG"`. The job refuses a `$TAG` not shaped `vX.Y.Z`; `v1.2.3-rc.1` matches the glob, so it publishes nothing. It then runs `pnpm -C web install --frozen-lockfile` and `release/build.sh "$TAG" "$RUNNER_TEMP/release"`, and `actions/upload-artifact` uploads the five files.
    - `publish`: `needs: build`, `ubuntu-24.04`, `contents: write`. No checkout runs, and no code from the repository or npm. `actions/download-artifact` fetches the files, `sha256sum -c checksums.txt` checks them, and `gh release create "$TAG" --repo "$GITHUB_REPOSITORY" --verify-tag --title "$TAG" --notes "…" <files>` publishes them. The job declares its own `env: TAG: ${{ github.ref_name }}`, since `build`'s does not reach it. `GH_TOKEN: ${{ github.token }}` is set on that one step only. A dependency compromised in `build` therefore never shares a job, a `PATH` or a `GITHUB_ENV` with the write token.
  - `release/workflows_test.go` gains three tests:
    - `TestOnlyPublishWrites`: the single `contents: write` sits in `publish`, and `publish` holds no `actions/checkout` and no `pnpm` or `release/build.sh`.
    - `TestTokenOnOneStep`: `GH_TOKEN` appears once, in a step's `env`, never in a job's or the workflow's.
    - `TestNoExpressionInRun`: no `run:` block contains `${{`, and every job whose `run:` uses `$TAG` declares `TAG` in its own `env`.
- Implements: ADR-0068 §2 and §3: the same checks first, nothing published on a failure, minimal permissions.
- Check:
  - The static tests pass. Each is seen failing on a mutated copy: `contents: write` at the top, a checkout in `publish`, `GH_TOKEN` on the job, and `${{ github.ref_name }}` inside `run:`.
  - CI stays green.
  - The workflow first runs for real in step 7, because a tag needs the owner's yes.
- Schema: no change.
- Needs: steps 2, 4.
- Not here:
  - No drafts, prereleases or generated changelog: ADR-0068 §2 takes `vX.Y.Z` only.
  - No check that the tag is on `main`.
  - No artifact attestation.
- Rests on: `gh release create`, run with the job token, publishes on a private repository, at level 5 (step 7).
- Review: the Opus verifier, for the supply chain: permissions, the token, injection.
- [x] Done. Evidence: the eight static tests pass, the plan's four mutations each fail one, and CI run 38289477093 is green. Opus verifier: no blocking finding. Its non-blocking ones were fixed in the same step: quoted and spaced `write` spellings, the token matched by value and `secrets: inherit` refused, `publish` restricted to an allowlist (the pinned download-artifact and `run:` steps), job keys with comments, the tag shape checked in a first `tag` job before `checks`, and no setup-go or setup-node cache in the release build. A limit was accepted for the register: the split guards the token, not the binaries' integrity, since npm code runs in `build`.

### 7. Level 5: a real release, installed on the owner's Mac

- Builds: nothing. Steps 1–6 are committed and pushed to `m2m`. This step is run by the main session, in this order, keeping its evidence in `$E`.
  - (a) **The owner's yes is needed here** for pushing `v0.1.0` (proposed) on `m2m`'s head and publishing its release. Then `git tag v0.1.0 && git push origin v0.1.0`, `gh run watch`, and `gh release view v0.1.0 --json assets`, which should list five assets. A failed run publishes nothing; the fix goes on `m2m`, and the next try is `v0.1.1`. No tag is moved or deleted without the owner.
  - (b) Download one asset on its own, `gh release download v0.1.0 --pattern '*darwin_arm64*' --dir "$E/dl"`, then `xattr -l` it: no `com.apple.quarantine`.
  - (c) Run `verify-daemon` §Launch to get `$R` and the isolated state. Then install: `gh api -H 'Accept: application/vnd.github.raw' 'repos/jtortosasen/Droidyard/contents/install.sh?ref=m2m' | DROIDYARD_INSTALL_DIR="$R/bin" sh`, and set `DY="$R/bin/droidyard"`, used in place of a built binary.
  - (d) Check the installed binary:
    - `xattr -l "$DY"` lists no quarantine.
    - `codesign -dv "$DY"` shows `Signature=adhoc`.
    - `"$DY" version` prints `droidyard v0.1.0 (API 0.23.0)`.
    - The foreground `serve` passes §Doctor, and `status` exits 0.
    - `curl` on the tokenised URL that `status` prints returns the built app, not ADR-0040 §2's placeholder.
  - (e) With that daemon still answering, rerun (c) with `DROIDYARD_VERSION=v0.1.0` after deleting `"$DY"`. The advice line is printed.
  - (f) Rerun (c) unchanged. It prints `already at v0.1.0`, and `ls -i` shows the same inode.
  - (g) Run `"$DY" stop`, then §Cleanup.
  - (h) If Docker is available, the release's linux/arm64 binary prints its version in `debian:bookworm-slim`. If Rosetta is installed, `arch -x86_64` runs the darwin/amd64 binary's `version`.
- Implements: ADR-0068 §2, §5, §6, and the two assumptions in §Context.
- Check:
  - (a)–(g) give what each says. Any failure leaves A7 unticked.
  - A quarantine flag or a missing signature stops the milestone for the owner as a design question. The answer would be the signing branch or the macOS-runner branch of ADR-0068 §Alternatives considered.
- Schema: no change.
- Needs: steps 1–6; the owner's yes; real `gh`. No adb, emulator or Gradle. The default state and `~/.local/bin` are not touched.
- Not here:
  - The real update path: it needs two releases.
  - The real curl path: it needs a public repository.
  - Pointing the owner's `init` entries at the installed binary: that is the owner's choice (ADR-0037 §2).
- Rests on: the darwin/arm64 binary, cross-compiled on Linux, runs on Apple Silicon with no prompt, at level 5.
- Review: the main session runs it. The close verifier rereads `$E`.
- [x] Done. Evidence: `$E` = `/private/tmp/dy.rjIRqE/evidence`, 2026-10-12. (a) Run 38290235257 succeeded with jobs `tag`, `checks`, `build` and `publish` all green; release `v0.1.0` (not a draft, not a prerelease) lists the five assets. (b) `gh release download` of the darwin_arm64 archive carries `com.apple.provenance` and no `com.apple.quarantine`. (c) The one-liner with `?ref=m2m` and `DROIDYARD_INSTALL_DIR` exited 0: `installing v0.1.0`, `now at v0.1.0`, and the PATH line, with no profile touched. (d) The installed binary has no quarantine, shows `Signature=adhoc`, and `version` prints `droidyard v0.1.0 (API 0.23.0)`. The Doctor passed (`pre-health` exit 1; the lock, socket and HTTP listener are all held by the launched pid). The UI port fell back to 53804 because another session's daemon held 7317. The tokenised URL returned 200 with the built app (`/assets/index-CGMjD6Wr.js`), not the placeholder. (e) With the binary deleted and the daemon answering, the run printed `a droidyard daemon is running; run: droidyard stop, …`. (f) A rerun printed `already at v0.1.0` with the same inode. (g) `stop` exited 0, 0 orphans, no token file left; the owner's adb server and daemon list are unchanged. (h) The release's linux/arm64 binary prints `droidyard v0.1.0 (API 0.23.0)` in `debian:bookworm-slim`, as does darwin/amd64 under Rosetta.

### 8. The close: documents, then the exact one-liner

- Builds: on the branch, in the close commit:
  - In `docs/roadmap/mvp.md` §Out of scope, binaries and the installer leave the Distribution line, which cites ADR-0068.
  - In `docs/roadmap/known-limits.md`:
    - The Distribution row keeps `llms-install.md`, registries, telemetry and Codex `init`.
    - New `unverified` rows for the curl path, the real update path and darwin/amd64 on an Intel Mac.
    - Any step 1 skip.
  - `docs/README.md` gets the install line: ADR-0068 §6's private command, and a clause saying the public one replaces it.
  - After the merge and the push of `main`, the main session runs the exact one-liner, with no `?ref`, and `DROIDYARD_INSTALL_DIR="$R/bin"`.
- Implements: ADR-0068 §Consequences, §6.
- Check:
  - `go test ./internal/doccheck` passes.
  - After the merge, the one-liner prints `droidyard: already at v0.1.0` and exits 0.
  - CI on `main` is green.
  - The close report holds the post-merge results (A8's note).
- Schema: no change.
- Needs: step 7.
- Not here: no release procedure in `AGENTS.md`. ADR-0068 §Consequences gives the two commands, and `release/build.sh`'s header holds the rest.
- Rests on: —
- Review: the close verifier, over the Acceptance items.
- [x] Done. Evidence: the close commit changes `mvp.md` §Out of scope, `known-limits.md` (the rows closed and added) and `docs/README.md` (the install line and §Where the work stands); `go test ./internal/doccheck` passes. The post-merge one-liner and CI on `main` are in the close report (A8).

## Not in this milestone

- Signing, notarization, Homebrew, `.pkg`, deb or rpm: ADR-0068 §7, §Alternatives considered.
- `llms-install.md`, registries, telemetry, generated snippets, `init` for Codex: they stay in the Distribution row (ADR-0068 §Consequences).
- Tests on macOS in CI: ADR-0068 §Consequences.
- The build version in `system.health` or `daemon.lock`: it would change the schema, and nothing asks for it. It would need an ADR.
- Making the repository public: the owner, 2026-10-12.

## Design questions

None. The ADR-0002 bookkeeping is a precondition (Starting point), not an open decision: ADR-0068 already decides Linux-only CI.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| The tests pass or skip on the amd64 `ubuntu-24.04` image, with its JDK, SDK and javac patch | 2, 6, 7 | step 1 (the proxy), step 2 (the runner) |
| `setup-node` and `pnpm/action-setup` take `web/.node-version` and the `packageManager` value, `+sha512` included; `setup-go` takes `go 1.27` | 2, 6 | step 2's log |
| Go ad-hoc signs darwin/arm64 when cross-compiling on Linux; `gh` and `tar` set no quarantine | 7 | step 1 (the signature), step 7 (b) and (d) |
| `gh release create` publishes with the job token; the repository's Actions policy allows the pinned actions | 2, 7 | step 2's run, step 7 (a) |
| `/releases/latest` redirects to `/releases/tag/<tag>`, and the curl URLs work once public | 5 | fakes only; a public repository (register row) |
| The update path between two real releases | 5 | the next release (register row) |
| darwin/amd64 on an Intel Mac | 4 | Rosetta in step 7 (h) at best (register row) |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | [test] B1: the runner ships JDKs, an SDK, lsof and `gh`, so step 1's bare container cannot see runner-only runs. `reader`/`formapp` check presence only, and `reader/build.sh` only warns on another javac. A2 cannot hold as worded | A2 is now "passes or skips"; step 1's container adds lsof and JDK 17; step 1(b) covers runner-only failures in step 2 with the sidecar's javac gate; Unverified row |
| 1 | revise | [test] B2: the installer test's `PATH` keeps `/usr/bin`, which holds a real `curl` (and `gh` on Ubuntu), so the cases without a tool are not hermetic | `PATH` is the fake `bin` plus an allowlisted tools directory; the cases assert `command -v` fails; `neither` is in the mutation list |
| 1 | revise | [safety] B3: the publish job exposed the write token to `pnpm install` and `vite build` (job `env`, `persist-credentials`) | `build` (read) and `publish` (write, no checkout, no npm) are separate jobs, linked by artifacts; `GH_TOKEN` is set on one step; `persist-credentials: false` everywhere; three static tests |
| 1 | revise | [contract] N1: ADR-0002 §Consequences against ADR-0068, with no `Supersedes:` | a precondition in Starting point, before `m2m` is branched |
| 1 | revise | [bug] N2: the new binary's `status` exits 1 under an API mismatch, so the advice was lost | step 5 (8) runs the old binary's `status` before the rename, and either one triggers the advice; two daemon cases and a mutation |
| 1 | revise | [test] N3: the hook check could not fail | an `sh -x` trace grep, seen not matching without the mapping |
| 1 | revise | [plan] N4: the archive's xattr could not be observed after the installer's cleanup | step 7 (b) downloads separately |
| 1 | revise | [plan] N5: the ad-hoc signature could be checked far earlier | step 1(a) cross-compiles in the container and runs `codesign` on the Mac; step 4 (v) also checks it |
| 1 | revise | [safety] N6: a discovered tag was not shape-checked | step 5 (3) checks every tag; a `releases` case and a mutation |
| 1 | revise | [plan] N7: A8 and step 8 happen after the merge | labelled post-merge, unticked in the record, reported at the close |
| 1 | revise | [nit] ×5: a stale "private" message, `gh auth status --hostname`, an orphan `.droidyard.new`, step 7's order, the size | message made conditional; `--hostname github.com`; the `trap` covers it; step 7 reordered ((e) before (g)); text tightened |
| 2 | approve | B1, B2, B3 resolved; no blocking finding | — |
| 2 | approve | [bug] `publish` never defined `$TAG` | `publish` declares its own `TAG`; `TestNoExpressionInRun` asserts it per job |
| 2 | approve | [test] the `neither` mutation was equivalent (same message as a curl failure) | its own wording, asserted in the case |
| 2 | approve | [plan] `golang:1.27` may be trixie, with no `openjdk-17` | `golang:1.27-bookworm` |
| step 5 verifier | proceed | [test][safety] no test caught a downloaded binary run before its sum; [bug] `gh auth status` fails on any bad github.com account; [safety] a predictable name beside the target; [test] three extraction guards untested; [nit] ×5 | all fixed in step 5, each seen failing on a mutated copy |
| step 6 verifier | proceed | [test] the static rules bypassed by spelling (quoted `write`, the token by another name, any action in `publish`, `secrets: inherit`, a commented job key); [safety] the job split guards the token, not the binaries; [nit] ×4 | fixed in step 6; the integrity limit is a register row |
| close verifier | close | [doc] ×2 (`-race` left the register; the Linux row's successor dropped Pdeathsig); [plan] ×2 (departures only in Evidence; the runner SKIP list unrecorded); [test] A4 (vi)–(vii) not rerun; [nit] ×5 | register rows added and reworded; a §Changes line; A4 rerun at the close; the SKIP list stated as not done |
| level 5 (step 7) | pass | no finding | — |

## Changes

- Step 1: on Linux, Pdeathsig ends a killed daemon's follow client, so `TestKilledDaemonLeavesTheFollowForTheNextStart` is skipped there: the reap-by-record half of that path is tested on macOS only (register row at the close). `TestReaderClientSerialisesPerDeviceAndNotTwoDevices` failed once in five Linux runs; if CI shows it, it is skipped on Linux with a row.
- Owner, 2026-10-12, after step 1: "solo me interesa ahora que se ejecute en la CI". Step 2 is cut to `ci.yml` and its first runner run; the `release/` package of static workflow tests and the hook mapping move out of step 2 (whether they come back with step 6 is decided there).
- Owner, 2026-10-12, later the same day: the milestone is done in full and closed only when complete, nothing broken or partial merged into `main`. Step 2's (a) and (b), the `release/` static tests and the hook mapping, come back, implemented with step 3.
- Step 3: `cmd/droidyard/main.go` imported `internal/domain/build` as `build`, which collides with `var build`; the import is aliased `builddomain` there and in `watchoutput_test.go`. The `-X main.build` symbol is as planned.
- Step 5: wording the plan left open: `installed an unknown version, installing <tag>`; `now at <tag> (<dir>/droidyard)`; on a fresh install the daemon advice names no previous binary. The truncated-script mutation is the body run unwrapped (a call inside `main` would recurse). The sum-tool check runs just before the download, after the already-at short-circuit.
- Owner, 2026-10-12, at step 7: yes to `v0.1.0`. If the run fails, the tag (and a release, if one was made) is deleted, the fix goes on `m2m`, and `v0.1.0` is pushed again, replacing the plan's "never moved, next is `v0.1.1`".
- Departures recorded in their steps' Evidence: step 4's `build.sh` removes `web/dist/app` before building; step 5 uses `gh auth status --hostname github.com --active`, `--repo github.com/…` and `mktemp` beside the target (verifier findings); step 6 adds a first `tag` job (the shape check moves out of `build`) and builds with no setup-go or setup-node cache. Not done: step 2(c)'s runner SKIP list was not recorded (plain `go test` shows no skips); step 1's container list stands as its proxy. `-race` runs nowhere (register row).
