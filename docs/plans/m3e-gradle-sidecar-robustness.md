# Plan: Milestone 3e — The Gradle sidecar's robustness: a distribution that installs, a root that stays coherent, the unmeasured costs measured

- Status: Approved
- Approval: main session, Delegated (2026-10-25, the owner's standing instruction to apply the recommended option): step 7 stays in, its check a draft PR's run on the pushed `m3e`, its Actions minutes on the private repository accepted; no waiting for another installer past Gradle's 2-minute lock wait, which would need a new ADR.
- Date: 2026-10-25
- Milestone: "the Gradle build sidecar's robustness: the register's `gradle` group" (owner, 2026-10-25). The 17 open rows of [`known-limits.md`](../roadmap/known-limits.md) whose Work is `gradle` joined (main session, Delegated, the owner's standing instruction); the two other open `build` rows with an empty Work stay out (Gradle 7.x below the floor: not wanted, owner 2026-10-08; the probe fields with no consumer). `docs/README.md` §Where the work stands says "next, the owner's choice".
- Phase: none. The MVP is done (2h); the Scope table is the boundary.
- Contract: ADR-0017 §Decision (TAPI only, no CLI path), §Resolved design (the core owns state); ADR-0018 rule 1, rule 4, §Consequences (the matrix and the probe canary); ADR-0019 §8 (the daemon's environment is fixed at start); ADR-0020 (cancel, progress); ADR-0021 §Pid records; ADR-0092 §2, §3 (a forgotten root closes once no operation holds it); [`gradle.md`](../subsystems/gradle.md) §Protocol, §Concurrency, §Lifecycle, §Failure modes, §Testing, §Spikes; plan m1g §Changes (2026-10-07, the ignored cancel) and D8 (the fingerprint).
- Register: the commit that adds this plan sets Work `3e` on the nine rows Scope marks yes or part (G2, G3, G4, G5, G6, G7, G8, G9, G12); G1's Work becomes empty (a producer's limit no work can close, Delegated); the seven other rows keep `gradle`. The commit is the main session's (the planner writes only this file), with this plan's row in `docs/plans/README.md`.
- Contract changes: **none**. No descriptor, schema, error code or API version changes; the sidecar protocol stays at version 2 (step 1 changes the text of an existing field). 3f (SDK provisioning, in parallel) owns every contract change of the two lanes; the only file both may touch is `cmd/droidyard/main.go` (step 5 wires one option).
- Parallel: steps 1, 2 and 7 (disjoint: `sidecar/`, `internal/domain/build/gradle/fingerprint.go`, `.github/workflows/`). Steps 3, 4, 5 share `internal/domain/build/gradle` and run in order after 1 and 2.
- Review: the Opus verifier once over steps 3–5 (process lifetime, concurrency; implementer effort `high`), and once at the close; steps 1, 2, 7 by the main session's diff review. One level-5 pass (step 6).

**The owner should see** (on synthetic projects; the owner's own cause is unknown, see Unverified): a project whose wrapper distribution is not on disk lists its tasks once a slow (0.8 MB/s) download ends; when the install fails, the error says why in Gradle's inner words, and names the way out: `./gradlew --version` in a terminal, or, when another Gradle process holds the install, waiting for it and retrying. The cause and the nextStep help whatever the owner's cause was; nothing else in 3e is built on a guess about it.

## Scope

Rows are named by their first words in the register (all Area `build`).

| Row | Here? | What 3e does, or why not |
| --- | --- | --- |
| G1. "`build.errors` fields depend on the producer" (limit) | no | KSP prints no column and AAPT2 no source file; ADR-0022 (D12) already settled the merged path. No droidyard work adds what a producer does not emit. Work emptied |
| G2. "The model cache's size-and-mtime fingerprint misses `~/.gradle/init.d`…" (limit) | part | step 2: the Gradle user home's init scripts and `gradle.properties` join the fingerprint. Environment variables: closed by argument, the daemon's (and so the sidecar's) environment is fixed at start (ADR-0019 §8) and the cache is in memory. Remote dynamic plugin versions stay (no fingerprint sees a repository): the row narrows to them |
| G3. "After a cancel the sidecar ignores, the root's Tooling API lock is released…" (limit) | yes | step 1 measures how long Gradle lets such a build run; step 4 closes the window, or is cut if Gradle closes it itself |
| G4. "A project whose wrapper distribution is not downloaded yet…" (limit) | yes | steps 1, 3, 6 |
| G5. "A call that resolved the old Gradle root just before a switch can reopen its connection…" (limit) | part | step 5 closes the reopened connection (ADR-0092 §2). The crash clause stays: ADR-0092 §3 degrades the workspaces whose current root crashed, and none holds this one; the call itself fails `BUILD_SIDECAR_CRASHED`. Changing that is a new ADR nobody asked for |
| G6. "Build retention by age, the 32 MiB console cap, and a crash mid-build proven at level 3 only" (unverified) | yes | step 6 (level 5) |
| G7. "Gradle below the floor, a missing wrapper (its ~150 MB download) and a call-time `UnsupportedMethodException`…" (unverified) | part | Below the floor: already observed at level 5 in 1k (Gradle 7.6.4, `gradle.md` §Failure modes); closed by that citation. A missing wrapper: step 6 runs the `connector` source for real; its download is the same `DistributionInstaller` as the wrapper's, proven by steps 1 and 6. A call-time `UnsupportedMethodException`: no known real Gradle throws it at a gated call, so only a fake reaches it; the row narrows to it |
| G8. "Sidecar memory (spike S4): measured idle only" (unverified) | yes | step 6 measures the sidecar's RSS under a build |
| G9. "Gradle daemon reuse with the terminal and Android Studio (spike S5)" (unverified) | part | step 6 measures the terminal case. Android Studio is the owner's: the row narrows to it, Work `trial` |
| G10. "Telling a probe failure from the project's own by the failure chain rests on two broken-script cases" (unverified) | no | The cases that would matter come from AGP's lazy APIs on real projects; a third synthetic script exercises the branch the two existing ones do (`TestGradleProbeBrokenInitScriptRetriesOnce`, `…ApplyFailureRetriesOnce`). Keeps `gradle` |
| G11. "Problem summaries (`event.build.problemSummary`)…" (deferred) | no | a new event topic is a contract change with no consumer; 3f owns the lanes' contract changes. Keeps `gradle` |
| G12. "The CI version matrix…" (deferred) | part | step 7: the **probe canary** in CI (ADR-0018 §Consequences names it apart from the matrix), the smallest piece that runs real Gradle in CI. The matrix rows stay out: ADR-0018's Tier A and blocking B rows are six Gradle minors (9.8, 9.7, 8.14, 8.0, 9.0, 9.1) times a KGP axis and an AGP axis, each a distribution plus AGP's dependencies per run, and `validation: tested` per row would need a generated list kept in step with CI. `validation` stays `untested` (the canary is not a matrix row). The row narrows to the matrix |
| G13. "Extractor forms not recorded: kapt, older Kotlin, AGP SDK signatures…" (deferred) | no | each needs a real recording from a project using that tool (kapt and KGP 1.x: an AGP project and ~1 GB of dependencies on a disk that is short); no consumer has hit one. Keeps `gradle` |
| G14. "`BUILD_QUEUE_FULL` for a second concurrent client…" (deferred) | no | a new error code (only "planned" in `internal/doccheck`), a contract change; no harm observed from the unbounded queue. Keeps `gradle` |
| G15. "Split APKs, App Bundles, dynamic features and instant apps…" (deferred) | no | a feature, not robustness (`mvp.md` §Out of scope). Keeps `gradle` |
| G16. "A raw 'arbitrary Gradle arguments' escape hatch…" (open question) | no | a contract question, and `--init-script` through it would break ADR-0018 rule 4. Keeps `gradle` |
| G17. "Included (composite) builds in `project.model`; whether the probe survives Isolated Projects" (open question) | no | composite builds change `project.modules`' shape (contract); Isolated Projects is a measurement on AGP that no Gradle default forces yet. Keeps `gradle` |
| Download progress for the distribution (operation progress, the UI) | no | cut: whether TAPI reports the distribution's download as progress events is unverified, and the UI does not show `build.tasks`' progress; a new register row at the close |

## Starting point

- Branch `m3e` at `50621d0` (main). API version unchanged by this plan.
- **Sidecar** (`sidecar/src/Session.kt`): one dispatcher thread (`pool`) serves every request but `build.cancel`, `ping`, `shutdown` (`control`), in arrival order, all roots together. `openConnection` uses `useBuildDistribution()` when `gradle/wrapper/gradle-wrapper.properties` exists, then fetches `BuildEnvironment`, which is where the Tooling API installs the distribution; a failure replies `RpcException.application("BuildEnvironment failed", e.message)`: **the outermost message only**, so the owner's error ("Could not install Gradle distribution") carried no cause. `projectModel` already sends the chain (`failureMessages`, m1h D5). A root already open is refused ("root is already open"), on purpose: the fake mirrors it so a missed close shows (`gradletest.go`). Jars are rebuilt by `sidecar/build.sh` (TAPI 9.8.0, digests pinned in `sidecar/sidecar.go`, `TestBuildScriptReproducesTheJars`).
- **Client** (`internal/domain/build/gradle/client.go`): `fetch` and `BeginBuild` (`build.go`) open the root when `!r.isOpen(pid)`; `call` on a cancelled context `forget`s the request, so a `connection.open` whose caller went away can still succeed on the sidecar while the client believes the root closed, and the next open is refused "already open". `rpcFailure` maps an application error to `PROJECT_MODEL_FAILED`. `CloseWhenFree` (ADR-0092 §2) runs once per switch from `cmd/droidyard/main.go:497`; a call that took the root's lock after it reopens the root and nothing closes it (m2r step 9 §Unverified).
- **Builds** (`internal/domain/build/build.go`): on cancel, `run` asks the sidecar and waits `cancelWait` (10 s); then `defer br.Close()` releases the root's TAPI lock and the lease whatever Gradle is doing.
- **Fingerprint** (`gradle/fingerprint.go`): files under the root only (m1g D8).
- **Registry** (`internal/domain/build/registry.go`): `consoleCap` 32 MiB, `retainBuildFor` 7 days by `index.json`'s `finishedAt`, 50 builds.
- **Real-Gradle tests**: `sidecar/sidecar_gradle_test.go`, opt-in with `DROIDYARD_GRADLE_TESTS=1` (21 top-level tests; the probe canary cells C1 8.13/AGP 8.13.2, C2 9.6.0/9.4.1, C4 8.0.2/8.0.2 from build.sh's cached zip). Nothing in `internal/` runs real Gradle; the client is tested against `gradletest` (a fake sidecar with `HoldOn`, `DieOn`, file-gated builds, `WaitForCancel`, an event log of `opened`/`closed <root>`).
- **CI** (`.github/workflows/ci.yml`): ubuntu-24.04, no JVM, no Gradle.
- **Host** (read-only, for fixtures): `~/.cache/droidyard-sidecar-build/gradle-8.0.2-bin.zip` (124 116 070 bytes, the official zip build.sh fetched with a pinned SHA-256); `~/.gradle/wrapper/dists/gradle-9.8.0-bin/3m7h6ceboy5k31n8kzwzuxssm/` unpacked, its zip deleted, `.lck` dated 2026-10-13 14:48 and `.ok` 2026-10-14 09:38: the owner's first attempt and the terminal `./gradlew --version` that finished it, a day apart. Nothing completed the install in those 19 h, which argues against a concurrent installer (Android Studio) that finished; the terminal used the same wrapper `networkTimeout` and succeeded.
- **The Tooling API** (Gradle source, read by the plan review): every install failure is rethrown as "Could not install Gradle distribution from '…'." (`DistributionFactory`'s `ZippedDistribution`), so the owner's message names no cause; the install runs under `ExclusiveFileAccessManager(120000, 200)` on the zip's `.lck`; the directory is `wrapper/dists/<name>/<base36 of the MD5 of the URL>`; the wrapper's `networkTimeout` is passed to the installer, with no network retries; a cancel Gradle's build ignores is ended by the Gradle daemon itself after a 10 s grace, by stopping that daemon (`DaemonStateCoordinator`, `cancelTimeoutMs`; master source, not checked in 8.0.2 or 9.8).
- **Baseline, 2026-10-25 at `50621d0`:** `go vet ./...` clean; `go test ./...` ok in every package.

## Acceptance

- [ ] A1. A distribution install that fails says why: `PROJECT_MODEL_FAILED`'s cause is Gradle's failure chain (de-duplicated, bounded as m1h D5), not the outermost message, and its nextStep names `./gradlew --version` at the Gradle root and retrying. Source: G4; `gradle.md` §Failure modes. Proven by: steps 1, 3.
- [ ] A2. The synthetic slow download is recorded: a wrapper distribution served from loopback at the owner's 0.8 MB/s, with a stall past the wrapper's `networkTimeout`, and with Gradle's install lock held by another process, each with its outcome and its **inner** chain verbatim (the outer message is the same for every install failure), and the time from a cancel to `finished` for a task that ignores interruption, with whether the Gradle daemon survived. Source: G4, G3. Proven by: step 1.
- [ ] A3. Another process holding the same distribution's install (Gradle's install lock past its own 2-minute wait) fails the call with a nextStep that says so and to retry when it finishes; once the holder is gone, a retry installs and answers. Source: G4. Proven by: steps 3, 6.
- [ ] A4. A call that went away during `connection.open` leaves the root usable: the next call answers instead of "root is already open", and a close at the workspace's close still reaches that connection. Source: G4 (found while planning). Proven by: step 3.
- [ ] A5. After a cancel whose build outlives `cancelWait`, the root's TAPI lock is held until the sidecar reports the build finished or exits: a model call meanwhile answers from the cache, a new build waits for the lock cancellably, and the sidecar can still idle out; or, if step 1 shows the sidecar's `finished` arriving before `cancelWait` ends (timed from the reply to `build.cancel`) in every run, G3 is closed by that measurement and step 4 is cut (Changes). Source: G3; `gradle.md` §Concurrency. Proven by: steps 1, 4, 6.
- [ ] A6. A connection a stale call reopened on a root no open workspace holds is closed when that call releases the root (ADR-0092 §2). Source: G5. Proven by: step 5.
- [ ] A7. An edit to the Gradle user home's `init.d/*`, `init.gradle(.kts)` or `gradle.properties` changes the fingerprint, so the next model call refetches. Source: G2. Proven by: step 2.
- [ ] A8. At level 5, through an isolated daemon: the console capped at 32 MiB with its marker; a record older than 7 days dropped at the next finish; a sidecar killed mid-build failing the build `BUILD_SIDECAR_CRASHED` with the workspace degraded, then recovering; a project with no wrapper built with `gradle.source: connector`; the sidecar's RSS under a build; whether a terminal build reuses the sidecar's Gradle daemon. Source: G6, G7, G8, G9. Proven by: step 6.
- [ ] A9. The probe canary runs in CI on changes to `sidecar/` or the build domain, on the three pinned cells (the newest Gradle not among them), and fails when a cell skips. Source: G12; ADR-0018 §Consequences. Proven by: step 7.

## Steps

### 1. The distribution install, reproduced on loopback and named

- Builds:
  - `sidecar/src/Session.kt` `openConnection`: a failed `connect()` or `BuildEnvironment` fetch replies with `failureMessages(e)` joined as the cause (the bounds of m1h D5), as `modelFailed` does; the message strings stay ("Gradle connection failed", "BuildEnvironment failed"). Jars rebuilt with `sidecar/build.sh`; digests in `sidecar/sidecar.go`.
  - `sidecar/sidecar_gradle_test.go`: `procOpts.env` (extra environment for the sidecar process, for `GRADLE_USER_HOME`); `distServer(t, zip, rate, stallAt, stallFor)` on `httptest` at 127.0.0.1, writing the zip at `rate` bytes/s and pausing once; `distFixture(t, url)`, a copy of `testdata/plain` whose wrapper's `distributionUrl` is the server's, and whose `gradle.properties` keeps a short daemon idle timeout; `distDir(guh, url)`, Gradle's install directory for a URL (`wrapper/dists/<name>/<base36 of the URL's MD5>`, the wrapper's `PathAssembler`); a temporary `GRADLE_USER_HOME` per test. The zip is `DROIDYARD_GRADLE_DIST_ZIP`, else build.sh's cached `gradle-8.0.2-bin.zip`; neither: skip, saying so.
  - `TestGradleDistributionInstallsFromASlowServer`: rate `DROIDYARD_GRADLE_DIST_RATE` (default 16 MB/s): `connection.open` answers `gradleVersion` 8.0.2, `source: wrapper`; `distDir` holds the `.ok` marker (which proves `distDir` is right for the lock test); the elapsed time and the sidecar's stderr are logged.
  - `TestGradleDistributionStallCarriesTheChain`: a 15 s stall after 8 MB (past the fixture's `networkTimeout=10000`): `connection.open` fails, its data holds "Could not install Gradle distribution" and at least one inner message; both logged verbatim.
  - `TestGradleDistributionLockedByAnotherProcess`: the test creates `distDir(...)` (a fresh user home has none), takes an `fcntl` write lock (`unix.FcntlFlock`) on its `<zip>.lck` before the open and holds it 130 s: the open fails after about 120 s with a chain naming Gradle's lock wait; the test logs the exact inner text step 3 matches, and the elapsed time. About 2.5 min.
  - `TestGradleIgnoredCancelTiming`: on the installed 8.0.2, a task that ignores interruption (a 40 s busy loop), `build.cancel` after 2 s: logs the time from the cancel to `finished`, its outcome, and whether the Gradle daemon survived (the pids of the Gradle daemons whose command line names the test's user home, by `ps`, before and after). Asserts only that `finished` arrives. This is the fact step 4 rests on.
- Implements: G4's first need, the cause; `gradle.md` §Failure modes ("Gradle's failure chain as the cause"), extended to `connection.open`.
- Check: the three tests. The chain test seen failing on the old jar (one message). Run by the implementer at the default rate; the main session reruns the three (1g S5: an opt-in test that skipped proves nothing) and the first once more with `DROIDYARD_GRADLE_DIST_RATE=800000`, the owner's rate (about 155 s). The evidence line records, per scenario, pass or fail, the elapsed time and the inner chain (every install failure shares the outer "Could not install Gradle distribution", so the outer message tells nothing), and the ignored-cancel timing. Two rules before step 3: a scenario that fails other than as the test expects (a fourth cause) is recorded under Changes and step 3's nextSteps are checked against it; and step 4 is cut, with G3 closed by this measurement (Changes), only if in every run the cancel's `finished` arrives before the core's `cancelWait` (10 s) would end, timed from the reply to `build.cancel` as `build.go:338-346` times it; otherwise step 4 stands (a `finished` a few hundred ms after `cancelWait` is the window G3 names, so it does not cut the step).
- Schema: no change. Protocol version unchanged (the `data` field's text only).
- Needs: none. Real: a JDK 17 (Gradle 8.0.2 runs on 17–19), the cached zip, no internet (the plain fixture has no plugins). Cost: each test installs Gradle 8.0.2 into its own temporary user home (the 124 MB zip while it downloads, about 130 MB unpacked) and starts one Gradle daemon; `t.TempDir` removes the files, the fixture's idle timeout ends the daemon.
- Not here: the remedies (step 3); a daemon (step 6); progress (Scope: cut); the owner's own cause (Unverified).
- Rests on: the Tooling API 9.8.0 installs a wrapper distribution from an `http://127.0.0.1` URL (level 4, the first test). If it refuses plain HTTP, the server goes to HTTPS with a self-signed certificate and the test's sidecar JVM gets `-Djavax.net.ssl.trustStore`, recorded under Changes. Also: Java's `FileChannel` lock and Go's `fcntl` lock exclude each other on macOS (level 4, the lock test fails visibly if not).
- [x] Done. Evidence: `connectionFailed` sends `failureMessages(e)` for both `connection.open` failures; jar rebuilt (`SHA256` 8f34da56…), deps and probe jars unchanged. With `DROIDYARD_GRADLE_TESTS=1`, the implementer's run and the main session's rerun (2026-10-25) pass all four. Slow server: 16 MB/s in 17.1 s and 9.1 s; at the owner's 800000 B/s it **installs, in 157.2 s**, so the rate alone does not fail (the owner's cause is not the rate). Stall (15 s after 8 MB): fails at 10.7 s, chain "Could not install Gradle distribution from '…'." / "Downloading from … failed: timeout (10000ms)" / "Read timed out". Lock held through `fcntl`: fails at 120.3 s, chain "Could not install …" / "Timeout of 120000 reached waiting for exclusive access to file: …/gradle-8.0.2-bin.zip" (Java's lock and `fcntl` exclude each other on macOS). The chain test seen failing on the old jar (one message). Ignored cancel: the reply in 5.3 and 5.6 ms; `finished` after **227.8 ms** (implementer) and **10.02 s** (rerun), outcome `cancelled`, the Gradle daemon alive after both. Not before `cancelWait` in every run, so step 4 stays (A5's rule). TAPI 9.8.0 takes plain HTTP on loopback: no HTTPS fallback.

### 2. The fingerprint sees the Gradle user home's scripts and properties

- Builds: `internal/domain/build/gradle/fingerprint.go`: the fingerprint also hashes, by size and mtime as D8, `<GRADLE_USER_HOME>/gradle.properties`, `init.gradle`, `init.gradle.kts` and every file directly under `init.d/`, where the user home is `GRADLE_USER_HOME` from the client's environment (`Options.Env`), else `$HOME/.gradle`; nothing else under the user home (caches, daemons, wrapper dists). `Client.Model` passes the client's user home; a function signature change stays inside the package. `gradle.md` §Project model: one clause on what the fingerprint covers outside the root, and that the environment needs none (ADR-0019 §8).
- Implements: G2; m1g D8 widened.
- Check: `TestFingerprintSeesTheUserHome` (a temporary root and user home): adding `init.d/a.gradle`, editing it, editing `gradle.properties`, adding `init.gradle.kts` each change the fingerprint; a file under `caches/` or `wrapper/dists/` does not; `GRADLE_USER_HOME` unset reads `$HOME/.gradle` (a `HOME` in the test's environment). Seen failing with the user home left out. A client test: with a cached model, an `init.d` edit makes the next `Model` reach the fake (`served project.model` in its log).
- Schema: no change.
- Needs: none (parallel with step 1).
- Not here: remote dynamic plugin versions (Scope G2); the Gradle installation's own `init.d` (inside a distribution directory, which changes only with the version, and the version is in `gradle-wrapper.properties`, already hashed).
- Rests on: —
- [x] Done. Evidence: `TestFingerprintSeesTheUserHome` and `TestUserHomeEditRefetchesTheModel` pass (`served project.model` 1 to 2 after an `init.d` edit), both seen failing with `hashUserHome` left out (implementer). The daemon gives the client `gradleEnviron()` (`os.Environ`) as `Env` (`cmd/droidyard/main.go:311`), so `UserHome` reads its `GRADLE_USER_HOME` or `$HOME/.gradle` (main session).

### 3. A distribution install failure that names its way out, and an open that survives its caller

- Builds: in `internal/domain/build/gradle/client.go`:
  - **The way out.** `open`: a `connection.open` failure whose chain holds "Could not install Gradle distribution" (the pinned Tooling API's outer text, step 1) gets a nextStep: when the chain also holds Gradle's install-lock text (step 1's recorded inner message; one constant, its source step 1's evidence), "another Gradle process (Android Studio, a terminal) is installing this project's Gradle distribution; retry when it finishes"; otherwise "run `./gradlew --version` once in a terminal in the project's Gradle root (it downloads the distribution), then retry". No path in either (ADR-0075: published paths are relative to the project root, which the client does not know). **No retry**: the call fails at Gradle's own 2-minute lock wait (plan review B3: a retry would hold the sidecar's one dispatcher thread, so every root's calls, for up to its bound, and no document decides that).
  - **The abandoned open.** When `open`'s context ends before the reply, the root is marked `openUnknown` for that sidecar pid; the next `fetch` or `BeginBuild` on that pid sends `connection.close` first (harmless when nothing is open: `closeConnection` returns), then opens. `openUnknown` counts as open everywhere "open" is asked: `closeLocked`'s `isOpen` guard (`client.go:348`, so a workspace's close still reaches it), `fail`'s degraded mark (`client.go:861`), and step 5's "leaves the root open". The sidecar keeps refusing a second open (the fake's guard against a missed close stays meaningful).
  - `gradletest`: `connection.open` can be failed with a given error text (`OpenFailure`), and held until a file exists (`OpenGate`), so the client's paths are reachable without Gradle.
  - `gradle.md` §Failure modes: one sentence for the two nextSteps and why there is no retry.
- Implements: G4 (the remedies); `gradle.md` §Failure modes; `gradle.md` §Concurrency (the open stays serialised per root).
- Check: fake sidecar, level 3:
  - `TestDistributionFailureNamesTheWayOut`: a chain with "Could not install Gradle distribution" and a read timeout: the nextStep names `./gradlew --version`; the same with step 1's lock text: the nextStep names another Gradle process and retrying; an unrelated `connection.open` failure gets neither. Seen failing without the mapping (no nextStep).
  - `TestAbandonedOpenLeavesTheRootUsable`: the open gated, the caller's context cancelled, the gate opened: the next `Model` answers (fake log: `closed`, then `opened`), and before it a workspace close sends `connection.close`. Seen failing without `openUnknown` ("root is already open").
- Schema: no change (`PROJECT_MODEL_FAILED` and its nextStep text; no new code).
- Needs: step 1 (the lock text; and a Changes entry if it found a fourth cause), step 2 (same package). Review: the Opus verifier with steps 4–5 (the abandoned open is process lifetime).
- Not here: a retry or a wait for another installer (B3; a new register row at the close, with the dispatcher's cost, if the owner wants it); one root's install blocks every other root's sidecar calls, since the sidecar serves them on one thread (a register row at the close; changing the dispatch is sidecar concurrency no row asked for); progress (Scope: cut); downloading the distribution ourselves or running `./gradlew` (ADR-0017 §Decision: no CLI path; the nextStep asks the person to).
- Rests on: an install failure and Gradle's install-lock failure are recognisable in the chain the sidecar now sends, in the pinned Tooling API's words (level 4 from step 1; the client's match is level 3 against the fake, level 5 in step 6).
- [ ] Done. Evidence:

### 4. A cancel that outlives `cancelWait` keeps the root locked until the build ends

Cut, with G3 closed by step 1's measurement, only if step 1 shows `finished` before `cancelWait` ends, timed from the reply to `build.cancel`, in every run (step 1 §Check; A5's rule).

- Builds:
  - `internal/domain/build/build.go` `run`: when the cancel's `cancelWait` ends without the sidecar's `finished`, the operation ends `cancelled` as today, and the `BuildRun` goes to a drain instead of `Close`: the lease is released at once (so the idle stop can still end a sidecar whose build never finishes), the root's TAPI lock is kept until the sink's `finished` arrives or the sidecar exits (`br.Done()`), then released. The sink stays registered for that build: its console writes are no-ops after `end` (`console.go:100`), and a late `event.build.progress` reaching the ended operation's `call.Progress` must be harmless (the test asserts the operation's state and progress do not change after it ended). One daemon log line when a drain starts and when it ends. `cancelWait` becomes a package variable, so the tests shorten it.
  - `internal/domain/build/gradle/build.go`: `BuildRun` gains the split release (lease now, lock later) the drain needs; `Close` keeps releasing both.
  - `gradle.md` §Lifecycle: the idle-shutdown sentence ("never while a build is running") amended to "never while a call or a build that has not been cancelled holds a lease; a build drained after its cancel holds none", and one sentence for the drain.
- Implements: G3; `gradle.md` §Concurrency ("all TAPI operations for one root are serialised"; "while a build runs, `project.model` answers from the cache"); `gradle.md` §Lifecycle (amended as above); m1g §Changes 2026-10-07 reversed.
- Check: fake sidecar, a build that ignores `build.cancel` and finishes when a file appears:
  - `TestIgnoredCancelKeepsTheRootLocked`: the operation ends `cancelled` within `cancelWait`; a `project.modules` meanwhile answers from the cache (no `served project.model`), `stale` after a `build.gradle.kts` edit; a second `build.run` waits and starts only after the file releases the first (the fake's log orders `finished b1` before `served build.run b2`); cancelling that second build while it waits ends it at once. Seen failing with the old `Close`.
  - `TestDrainEndsWhenTheSidecarExits`: the fake killed during the drain: the lock is released and the next call restarts the sidecar.
  - `TestDrainHoldsNoLease`: with an injected 2 s idle timeout and a draining build, the sidecar is stopped by the idle timer.
  - In the first test, a progress notification after the operation ended changes neither its state nor its progress.
- Schema: no change.
- Needs: step 1 (the measurement that keeps or cuts this step), step 3 (same package).
- Not here: a second build waiting on the lock shows `running`, not `queued` with `blockedBy` (the operation's scheduler key ended with the cancelled build; it waited invisibly inside the sidecar before, so this is no regression, and a `blockedBy` on a drain would be a contract field); killing the Gradle build (`gradle.md` §Lifecycle: never kill the Gradle daemon).
- Rests on: a build that ignores a cancel outlives `cancelWait` on real Gradle, so there is a window to close (level 4, step 1's `TestGradleIgnoredCancelTiming`; Gradle's 10 s grace, read in master source only, may close it, and then this step is cut). Also: the sidecar sends exactly one `finished` per accepted build, also after a cancel, or exits (m1g step 1 fix round; level 3 here, level 5 in step 6).
- [ ] Done. Evidence:

### 5. A connection reopened on a forgotten root closes when its call ends

- Builds: `internal/domain/build/gradle/client.go`: `Options.InUse func(root string) bool`; every release of a root's TAPI lock that leaves the root open (the end of `Model`'s fetch, `BuildRun.Close`, step 4's drain) asks `InUse`, and when no open workspace holds the root, runs `CloseWhenFree(root, InUse)` in a goroutine, as the switch does. `CloseWhenFree`'s own release does not ask again (its close leaves the root not open). `cmd/droidyard/main.go`: `InUse: ws.GradleRootInUse`, set where the client is built (the workspace registry is created after the client; a closure over `ws`). Nil `InUse` (tests, the schema-only client) changes nothing.
- Implements: ADR-0092 §2 ("a forgotten root whose connection no open workspace remembers is closed … once no operation holds it"), for the connection a stale call reopened; G5.
- Check: fake sidecar: `TestStaleCallReopenedRootCloses`: root A forgotten and closed (`closed A`), then a call on A opens it again (`opened A`): when that call ends, `closed A` follows; with `InUse(A)` true, nothing closes; a build on a forgotten A closes after the build's end, not during it. Seen failing without the release hook. `go test -race` on the package.
- Schema: no change.
- Needs: step 4 (the drain's release is one of the paths).
- Not here: the crash clause of G5 (Scope); closing at once (ADR-0092 §Alternatives: never cancel what runs).
- Rests on: every release of a root's lock passes through the hook (level 3: the test drives each path; the verifier reads that no other path releases the lock).
- [ ] Done. Evidence:

### 6. One level-5 pass: the slow distribution, the lock, the ignored cancel, the caps, the costs

Run by the main session against an isolated daemon (`verify-daemon` §Launch, §Doctor, §Cleanup), never the owner's, on synthetic projects only. About 20 minutes.

- Builds: no code. Scratch, outside the repository:
  - `$R`, `$E`, `$DY` and `$PID` as `verify-daemon` §Launch sets them (the socket under `/tmp`); `F=~/Library/Caches/droidyard-dev/3e`; `GUH=$F/gradle-home` (the daemon is launched with `GRADLE_USER_HOME=$GUH` and `JAVA_HOME` a JDK 17); two plain Java projects without plugins: `$F/slow`, whose wrapper's `distributionUrl` is `http://127.0.0.1:$PORT/gradle-8.0.2-bin.zip`, with tasks `spam` (prints 40 MiB), `sleepy` (`Thread.sleep` 60 s) and `busy` (a 40 s busy loop that ignores interruption); `$F/connector`, without `gradle/wrapper/`. Both trusted with `droidyard trust` under the scratch state.
  - A scratch server (a short Go program in `$F`, or `python3 -I`) serving build.sh's cached 8.0.2 zip read-only at 800 000 bytes/s; a scratch lock holder taking an `fcntl` write lock on step 1's `distDir` `.lck` for 150 s.
  - For the connector: first compute step 1's `distDir` for `https://services.gradle.org/distributions/gradle-9.8.0-bin.zip` (the Tooling API 9.8.0's own distribution); if it is `gradle-9.8.0-bin/3m7h6ceboy5k31n8kzwzuxssm`, `cp -R ~/.gradle/wrapper/dists/gradle-9.8.0-bin "$GUH/wrapper/dists/"` (no internet download; a copy, so the owner's cache is only read); if not, item 7 downloads it from the internet (about 130 MB, minutes at the host's rate) or is dropped, recorded either way.
- Implements: A3, A5 and A8 at level 5.
- Check: through the CLI (`--no-autostart`), each output kept in `$E`:
  1. Lock, then the slow download: the holder starts; `build.tasks` on `$F/slow` (long-running, `operation.wait`) fails after about 120 s with `PROJECT_MODEL_FAILED`, the inner lock text in its cause and the "another Gradle process" nextStep; the holder released, `build.tasks` again completes with the tasks after the ~155 s download at 0.8 MB/s. A second `project.modules` answers in under 1 s.
  2. `build.run {tasks: [":spam"]}`: the console artifact is 32 MiB plus the cap marker (`artifact.read` of its tail); the sidecar's RSS sampled each second (`ps -o rss= -p <sidecar pid>`) and the Gradle daemon's, peak and idle recorded (G8).
  3. `build.run :busy`, `operation.cancel` after 3 s: the operation is `cancelled` within `cancelWait`; `project.modules` answers at once; a `build.run :help` started then starts only after the sidecar's `finished` for `busy` (Gradle's forced stop after its grace, or `busy`'s end), in a new Gradle daemon if Gradle stopped the old one: the time and the daemon pids before and after are recorded (G3 at level 5; skipped with step 4 if step 4 is cut, the timing then being step 1's).
  4. `build.run :sleepy`, then `kill -9` of the sidecar's pid (the daemon's child `java`, from `ps -o pid,ppid,command`, never by name): the build fails `BUILD_SIDECAR_CRASHED`, `workspace.describe` says `degraded`; `build.tasks` then answers and the workspace is `ready`.
  5. Retention: `droidyard stop`; one `index.json` entry's `finishedAt` set 8 days back (`jq`); a new daemon; `build.run :help`: that record and its console are gone, the others kept.
  6. Terminal reuse (S5): `GRADLE_USER_HOME=$GUH JAVA_HOME=<the sidecar's JVM, from system.health> <the 8.0.2 distribution in $GUH>/bin/gradle -p $F/slow help`: the count of Gradle daemons whose command line names `$GUH`, before and after (equal: reused); once more with another installed JDK if there is one (one more: not reused).
  7. Connector: `build.run :help` on `$F/connector`: `gradle.source: connector`, the droidyard notice line in its console.
  - Cleanup: `GRADLE_USER_HOME=$GUH .../bin/gradle --stop` with each distribution in `$GUH` (only that user home's daemons), `droidyard stop`, the server and holder by their pids, `du -sh $F` recorded, then `rm -rf "$F" "$R"`; zero processes naming `$GUH` or `$R`.
- Schema: no change (`"$DY" schema | cmp - schema/capabilities.json`).
- Needs: steps 3, 4, 5 (and step 1's `distDir`).
- Cost: disk peak about 600–700 MB (estimate: 8.0.2 installed in `$GUH`, ~150 MB of the 9.8.0 copy, two Gradle daemons' caches, 32 MiB of console), measured by the `du` before cleanup; RAM: two Gradle daemons and the sidecar while it runs.
- Not here: Android Studio (G9's remainder, the owner's `trial`); an AGP project (the pass needs none, and AGP's dependencies would cost disk and network); the owner's own project (never, even read-only).
- Rests on: —
- [ ] Done. Evidence:

### 7. The probe canary in CI

The owner's call (Actions minutes on a private repository); dropping it leaves G12 out whole.

- Builds: `.github/workflows/gradle.yml`, a workflow of its own (so `release.yml`, which calls `ci.yml`, does not grow): on `pull_request` and `push` to `main` with paths `sidecar/**`, `internal/domain/build/**` and the file itself, plus `workflow_dispatch`; ubuntu-24.04, actions pinned by SHA as `ci.yml` pins them, Go from `go.mod`, the runner's JDK 17 (`JAVA_HOME_17_X64`) and Android SDK (`ANDROID_HOME`); build.sh's 8.0.2 zip fetched into `BUILD_CACHE` and checked against build.sh's `GRADLE_SHA`, so cell C4 runs; then `set -o pipefail; DROIDYARD_GRADLE_TESTS=1 go test -v -run 'TestGradleProbe' ./sidecar | tee "$RUNNER_TEMP/canary.log"`, then failing when the log holds `--- SKIP`. `timeout-minutes: 30`. `gradle.md` §Testing: "and is not yet in CI" becomes where it runs.
- Implements: ADR-0018 §Consequences (the probe canary), in part: the cells the tests pin (Gradle 8.0.2, 8.13, 9.6.0; AGP 8.0.2, 8.13.2, 9.4.1), so the floor and AGP's floor and latest, but not the newest Gradle (9.8); a 9.8 cell stays in G12's narrowed row. G12 in part.
- Check: `workflow_dispatch` needs the file on the default branch, and `push` runs on `main` only, so the check is a draft pull request from `m3e` (its `pull_request` run, the paths filter matching the file): green with the three cells passing and no skip; seen failing once with a temporary commit pointing `BUILD_CACHE` nowhere (C4 skips), reverted. Opening the draft pull request needs the branch pushed: the main session's call (owner, Actions minutes). Without it, the first run on `main` after the merge is the check, labelled as such.
- Schema: no change.
- Needs: none (parallel with steps 1–6).
- Not here: the version matrix (Scope G12); caching Gradle's downloads in CI (`actions/cache`, a later saving); macOS runners (ten times the minutes).
- Rests on: the runner's SDK has what the cells need (platform 36, build-tools 35.0.0, 36.0.0 and 30.0.3, or AGP may fetch them) (level 5, the first run; unverified until then).
- [ ] Done. Evidence:

## Not in this milestone

- G1, G10, G11, G13–G17: Scope says why; each keeps `gradle` but G1 (Work emptied).
- The remainders after 3e, rewritten at the close as narrowed rows: remote dynamic plugin versions (G2); the crash clause (G5); the call-time `UnsupportedMethodException` (G7); Android Studio's daemon reuse, Work `trial` (G9); the CI version matrix (G12).
- New rows at the close: one root's distribution install holds every other root's sidecar calls (one dispatcher thread); the distribution download shows no progress; a call fails at Gradle's 2-minute install-lock wait instead of waiting for the other installer (a retry would hold the one dispatcher thread for every root: plan review B3; a new ADR if the owner wants it).
- The owner's own project: whether 3e ends the owner's failure is the owner's trial after the merge (Work `trial`), since no agent opens it.

## Design questions

None. Every remedy stays inside a decision already made: the cause and the nextSteps are text of an existing code (`gradle.md` §Failure modes); there is no retry (B3 cut it rather than decide it); the drain keeps `gradle.md` §Concurrency's serialisation and amends one sentence of §Lifecycle that described a lease, not a policy; step 5 is ADR-0092 §2; step 7 is ADR-0018's canary.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| The Tooling API 9.8.0 installs from `http://127.0.0.1` | 1, 6 | step 1's first test (fallback named there) |
| Java's `FileChannel` lock and an `fcntl` lock exclude each other on macOS | 1, 6 | step 1's lock test |
| The owner's cause is unknown: every install failure shares the outer message, and the host's `.lck`/`.ok` timeline (19 h with no install completed) argues against a concurrent installer that finished. 3e's remedies (the inner chain, the nextSteps) help whatever it was | 3 | the inner chain in the owner's next failure, or their trial after the merge |
| Gradle's install-lock text is stable for the pinned Tooling API | 3 | step 1 records it; a Tooling API bump re-runs step 1's lock test |
| A build that ignores a cancel outlives `cancelWait` on real Gradle (Gradle's 10 s grace read in master source only) | 4, 6 | step 1's `TestGradleIgnoredCancelTiming` |
| `distDir` of the official 9.8.0 URL is the owner's cached `3m7h6ceboy5k31n8kzwzuxssm` | 6 | computed before step 6 item 7 |
| The CI runner's Android SDK serves the canary cells | 7 | step 7's first run |
| The disk cost of step 6 (600–700 MB) | 6 | `du` before its cleanup |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | B1 [test][plan] Gradle ends an ignored cancel itself after a 10 s grace by stopping its daemon, racing `cancelWait`; step 6 item 3's "after `busy` ends" could not tell the drain from today's code; step 4 rested on the wrong fact | step 1 gains `TestGradleIgnoredCancelTiming`; step 4 rests on its result and is cut (G3 closed by the measurement) if `finished` arrives within ~1 s of `cancelWait`; step 6 item 3 rewritten (after `finished`, possibly a new daemon, pids recorded) |
| 1 | revise | B2 [plan] the outer message is shared by every install failure, so A2 and step 1's stop rule could not fire; the `.ok` timeline argues against the lock hypothesis | step 1 compares inner chains; A2, the owner-should-see line and the Unverified row rewritten (the owner's cause unknown; the remedies are the ones that help whatever it was) |
| 1 | revise | B3 [plan][design] the bounded install-lock retry was undocumented behaviour and held the sidecar's one dispatcher thread for every root | retry cut (option a): the lock failure gets its own nextStep; A3 and step 6 item 1 rewritten; a register row at the close |
| 1 | — | N1 [test] `workflow_dispatch` needs the file on the default branch | step 7's check is a draft pull request's run, red once by a temporary commit |
| 1 | — | N2 [contract] the cells stop at Gradle 9.6.0, not the newest | step 7 and A9 claim the pinned cells only; a 9.8 cell stays in G12's row |
| 1 | — | N3 [test] the copied 9.8.0 directory's hash unchecked | computed first in step 6; an Unverified row |
| 1 | — | N4 [bug] the drain's sink and late progress; `cancelWait` a const | step 4 names both, a test asserts late progress is harmless, `cancelWait` a variable |
| 1 | — | N5 [doc] §Lifecycle misquoted | step 4 amends "never while a build is running" |
| 1 | — | N6 [plan] `openUnknown` must count as open everywhere | step 3 names `closeLocked`, `fail`, step 5 |
| 1 | — | nits: 21 tests; `$R` undefined; `distDir` must exist before the lock; `pipefail` | all taken |
| 2 | revise | B2, B3 resolved. B1 in part: step 1 and step 4 cut step 4 on "within about 1 s of `cancelWait`", A5 on "within `cancelWait`"; with Gradle's 10 s grace starting after the core's, the likely result is `finished` a few hundred ms after the lock's release, which the first rule would call closed | one rule in step 1, step 4 and A5: cut only if `finished` arrives before `cancelWait` ends, timed from the reply to `build.cancel` as `build.go:338-346` does, in every run; otherwise step 4 stands. Answered in the plan after the second round (`milestone-plan` §Writing one, item 7) |

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.
