# ADR-0068: Release builds in GitHub Actions, and a one-line installer that uses `gh` while the repository is private

- Status: Accepted
- Date: 2026-10-12
- Supersedes: ADR-0002 §Consequences (in part: "CI runs on two operating systems from the start"; CI runs on Linux only, and macOS is tested by the owner's pre-commit hook)
- Relates to: ADR-0014 §Decision (items 1 and 2), ADR-0002, ADR-0040, ADR-0037
- The owner set the frame on 2026-10-12: a GitHub CI that builds a release the owner can run on their Mac, and a one-line installer of the kind other tools have; while the repository is private the installer goes through `gh`, and becomes a plain `curl | sh` when it is made public. The rest was decided by the main session under the owner's standing instruction (apply the recommended option), Delegated.

## Context

ADR-0014 chose per-OS/arch binaries with checksums and a one-line installer, and
`mvp.md` §Out of scope deferred them. The owner pulls them in now (2026-10-12). The
repository `jtortosasen/Droidyard` is private, so an anonymous `curl` cannot fetch its
release assets or its files; an authenticated `gh` can.

What a release must contain is already settled by the tree: the sidecar jars, the
reader dex and the skill are checked in and embedded (`sidecar/sidecar.go`,
`internal/domain/device/reader/reader.go`, `skills/embed.go`); the web UI is embedded
from `web/dist/app`, which is built by `pnpm -C web build` and not committed
(ADR-0040), so a release build runs that first. No package imports `"C"`, so
`CGO_ENABLED=0` cross-compiles every target from one runner (observed: `grep` over the
tree, 2026-10-12). Go links darwin/arm64 binaries with an ad-hoc signature, which Apple
Silicon requires, including when cross-compiled (assumption, to be observed on the
owner's Mac). Neither `curl` nor `gh` sets `com.apple.quarantine` on what they write, so
an unsigned, unnotarized binary they install runs without a Gatekeeper prompt
(assumption, to be observed).

## Decision

1. **Targets**: `darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`
   (ADR-0002), built with `CGO_ENABLED=0` from one Linux runner. Each is one archive,
   `droidyard_<version>_<os>_<arch>.tar.gz`, holding the `droidyard` binary, plus one
   `checksums.txt` of SHA-256 sums for the release.
2. **Versions** are git tags `vMAJOR.MINOR.PATCH`. Pushing such a tag runs the release
   workflow, which builds, checks and publishes a GitHub Release on this repository. The
   version is stamped into the binary at link time; how the CLI shows it beside the API
   version (`droidyard version`) is the plan's. The API version is unchanged by a release.
3. **CI**: one workflow on pushes to `main` and on pull requests runs what
   `.githooks/pre-commit` runs on the whole tree: `pnpm -C web install
   --frozen-lockfile`, `pnpm -C web run verify`, `go build ./...`, `gofmt -l .`,
   `go vet ./...`, `go test ./...`, on Linux. The release workflow runs the same checks
   before it builds, and publishes nothing when one fails. Toolchains are pinned: Go
   from `go.mod`, Node and pnpm as `web/` pins them, third-party actions by commit SHA.
4. **The build is a script in the tree**, plain `go build` per target, run by the
   release workflow and runnable on the owner's Mac, so a release never depends on
   GitHub Actions being available.
5. **The installer** is `install.sh` at the repository root, POSIX `sh`:
   - it picks the OS and architecture from `uname`, and refuses any other pair;
   - it downloads the archive and `checksums.txt` with `gh release download` when `gh`
     is installed and authenticated, else with `curl` from the release URL; the URLs are
     the same for a private and a public repository, so making it public changes
     nothing in the script;
   - it verifies the SHA-256 before anything is installed, and stops on a mismatch;
   - it installs `droidyard` into `$DROIDYARD_INSTALL_DIR`, default `~/.local/bin`
     (working default), replacing the file atomically (write beside it, then rename);
   - the latest release by default, `DROIDYARD_VERSION=vX.Y.Z` for one in particular;
   - **updating is running it again** (owner, 2026-10-12): it reads the installed
     binary's version, says what it replaces, and does nothing when that version is
     already installed;
   - it edits no shell profile, and prints the line to add when the directory is not on
     `PATH`; it never stops a running daemon, and says to run `droidyard stop` when one
     answers, since the daemon keeps running the old binary.
6. **The one-line command** while the repository is private:
   `gh api -H 'Accept: application/vnd.github.raw' repos/jtortosasen/Droidyard/contents/install.sh | sh`.
   Once it is public: `curl -fsSL https://raw.githubusercontent.com/jtortosasen/Droidyard/main/install.sh | sh`.
   Only the command changes.
7. **Not signed or notarized**, not in Homebrew, no `.pkg`. Uninstalling is deleting the
   binary (and `droidyard init --uninstall` per project).

## Consequences

- A release is `git tag vX.Y.Z && git push origin vX.Y.Z`; the owner installs it with
  the line in item 6.
- GitHub Actions minutes on a private repository are spent on Linux runners only
  (macOS runners cost ten times as much). Nothing in CI runs the tests on macOS: the
  pre-commit hook on the owner's Mac stays the only macOS test run. Whether
  `go test ./...` passes on Linux has not been observed; the plan finds out first.
- `droidyard init` writes the binary's absolute path (ADR-0037), so a stable install
  directory keeps the assistants' entries valid across upgrades.
- A browser download of the archive is quarantined, and Gatekeeper refuses it
  unsigned; the documented ways are `gh` and `curl`.
- Documents to change in the same milestone: `docs/roadmap/mvp.md` §Out of scope
  (binaries and the installer leave the Distribution line); the Distribution row of
  `docs/roadmap/known-limits.md` (binaries and the installer close; `llms-install.md`,
  registries, telemetry and Codex stay deferred); the root of the documentation map
  gets the install line.

## Alternatives considered

- **GoReleaser.** It builds, archives, checksums and publishes from one config. Here it
  is a third-party tool and a config to pin for four `go build`s and a checksum file,
  and it does not build the web UI, which still needs its own step. It wins when
  signing, notarization, a Homebrew tap or deb/rpm packages arrive.
- **Make the repository public now**, for a plain `curl | sh` and no `gh`. The owner
  chose to keep it private for now (2026-10-12); the installer is ready for it.
- **A separate public repository for the releases**, with a token in CI. It gives the
  anonymous `curl` today, at the cost of a second repository and a long-lived secret
  that can write to it; the owner preferred `gh` until the source is public.
- **A Homebrew tap.** The formula would download from a private repository, which
  needs a token Homebrew does not carry. It wins once the repository is public.
- **Build the macOS binaries on a macOS runner.** It would also run the tests on
  macOS, at ten times the minutes. Cross-compiling needs no cgo, and the owner's
  pre-commit hook already tests on macOS. It wins if cgo comes in, or if a macOS-only
  failure reaches a release.
- **Sign and notarize.** It needs an Apple Developer ID and the notarization round
  trip, for a binary that `gh` and `curl` install without a quarantine flag. It wins
  with a browser download, a `.pkg`, or a public audience.
