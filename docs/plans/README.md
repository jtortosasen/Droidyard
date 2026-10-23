# Plans

One plan per milestone, written **before** the code and kept after it lands as the
record of what was decided, in what order, and how each step was proved.

Named `<milestone>-<slug>.md`, for example `m1b-daemon-and-mcp-adapter.md`.

The procedure — what a plan must contain, who writes it, who attacks it, and how it is
followed — is the `milestone-plan` and `milestone-workflow` skills. This directory holds
the output.

## Index

| Plan | Milestone | Status |
| --- | --- | --- |
| [m1b-daemon-and-mcp-adapter.md](m1b-daemon-and-mcp-adapter.md) | 1b — the daemon and the MCP adapter | Done |
| [m1c-process-supervisor-and-workspace.md](m1c-process-supervisor-and-workspace.md) | 1c — the process supervisor and the workspace model | Done |
| [m1d-http-websocket-and-events.md](m1d-http-websocket-and-events.md) | 1d — the HTTP/WebSocket skeleton, the event bus and `events.*` | Done |
| [m1e-jvm-free-device-slice.md](m1e-jvm-free-device-slice.md) | 1e — the JVM-free device slice | Done |
| [m1f-device-actions.md](m1f-device-actions.md) | 1f — device actions on the persistent UI reader | Done |
| [m1g-gradle-sidecar-and-build.md](m1g-gradle-sidecar-and-build.md) | 1g — the Gradle sidecar and `build.*` | Done |
| [m1h-android-probe-and-sdk.md](m1h-android-probe-and-sdk.md) | 1h — the Android probe, exact variants and `sdk.*` | Done |
| [m1i-device-logs-and-emulator.md](m1i-device-logs-and-emulator.md) | 1i — `device.logs` and `emulator.*` | Done |
| [m1j-files.md](m1j-files.md) | 1j — `file.*` | Done |
| [m1k-policy-and-shell.md](m1k-policy-and-shell.md) | 1k — `policy.*`, `shell.exec` and the configuration file | Done |
| [m1l-phase1-acceptance.md](m1l-phase1-acceptance.md) | 1l — the Phase 1 acceptance run | Done |
| [m1m-emulator-robustness.md](m1m-emulator-robustness.md) | 1m — emulator boot robustness and the MCP-vs-shell comparison | Done |
| [m1n-daily-use-core.md](m1n-daily-use-core.md) | 1n — the daily-use core: crashes, ANRs, typing and speed | Done |
| [m1o-tokens-and-harness.md](m1o-tokens-and-harness.md) | 1o — tokens, assistant integration, and the comparison with Cursor | Done |
| [m1p-functional-gaps.md](m1p-functional-gaps.md) | 1p — the functional gaps of Phase 1, and three ideas from earlier plans | Done |
| [m1q-one-source-of-truth.md](m1q-one-source-of-truth.md) | 1q — one source of truth, fewer documents | Done |
| [m2a-minimal-web-ui.md](m2a-minimal-web-ui.md) | 2a — the web UI's foundation and a streaming Logcat | Done |
| [m2b-build-devices-golden-path.md](m2b-build-devices-golden-path.md) | 2b — build and devices panels, the activity feed, and the golden path from the browser | Done |
| [m2c-fixes-live-console-project-start.md](m2c-fixes-live-console-project-start.md) | 2c — a round of fixes, the build console live, and a traditional project start | Done |
| [m2d-redesign-file-tree.md](m2d-redesign-file-tree.md) | 2d — a redesign, the projects and agents panel, and a read-only file tree | Done |
| [m2e-ux-round.md](m2e-ux-round.md) | 2e — a UX round: collapsible panels, the sessions column, a status bar, a run toolbar and a Logcat query bar | Done |
| [m2f-sessions-overlay-and-fixed-controls.md](m2f-sessions-overlay-and-fixed-controls.md) | 2f — the sessions column as an overlay on hover and at the left edge, and bottom panels whose controls do not scroll away | Done |
| [m2g-file-operations.md](m2g-file-operations.md) | 2g — the file tree's operations | Done |
| [m2h-mvp-done.md](m2h-mvp-done.md) | 2h — the MVP's three open Definition of done items | Done |
| [m2i-editor.md](m2i-editor.md) | 2i — the editor | Done |
| [m2j-file-tools-off-mcp.md](m2j-file-tools-off-mcp.md) | 2j — the file capabilities leave the MCP surface, and an agent's `rm` goes to the trash | Done |
| [m2k-cursor-parity-and-observation.md](m2k-cursor-parity-and-observation.md) | 2k — Cursor at Claude Code's level, and the agent's Android commands observed | Done |
| [m2l-git-review-diff-and-tree.md](m2l-git-review-diff-and-tree.md) | 2l — Git: the review diff and the Git tree | Done |
| [m2m-release-and-installer.md](m2m-release-and-installer.md) | 2m — release builds and the one-line installer | Done |
| [m2n-git-history.md](m2n-git-history.md) | 2n — Git history | Done |
| [m2o-gradle-root-below.md](m2o-gradle-root-below.md) | 2o — A project whose Gradle build is in a subfolder | Done |
| [m2p-live-device-view.md](m2p-live-device-view.md) | 2p — C3, a live device view: the screen streamed to the UI, with input | Done |
| [m2q-sessions-own-worktrees.md](m2q-sessions-own-worktrees.md) | 2q — C2, sessions that own worktrees | Done |
| [m2r-register-cleanup.md](m2r-register-cleanup.md) | 2r — Register cleanup: the quick rows, a block of fixes without devices, and an audit of the open rows | Done |
| [m2s-occupation-of-worktrees-and-devices.md](m2s-occupation-of-worktrees-and-devices.md) | 2s — C2 completed: a session occupies its worktree and its device | Done |
| [m2t-device-hosting.md](m2t-device-hosting.md) | 2t — C4a, device hosting: lend physical devices to a paired Droidyard over the VPN | Done |
| [m2u-share-and-join.md](m2u-share-and-join.md) | 2u — C4b, share a phone with one command, join with one paste | Done |
| [m2v-tests-that-hold.md](m2v-tests-that-hold.md) | 2v — Tests that hold: fewer flaky tests, faster, more solid | Done |
| [m2w-ui-dump-and-changes.md](m2w-ui-dump-and-changes.md) | 2w — A full UI dump for parsers, and the UI's changes between reads | Done |
| [m2x-ui-recording.md](m2x-ui-recording.md) | 2x — A UI recording: every distinct screen of a span, with its events, local or lent | Done |
| [m2y-live-view-that-responds.md](m2y-live-view-that-responds.md) | 2y — A live view that responds: input over the open stream, live drag, a remote view that keeps up | Done |
| [m2z-cli-for-people.md](m2z-cli-for-people.md) | 2z — A CLI for people: commands per domain from the descriptors, help in levels, output to read | Done |
| [m2za-view-quality-from-the-client.md](m2za-view-quality-from-the-client.md) | 2za — The live view's quality from the client: a toggle, and a view that comes back by itself | Done |
| [m3a-init-once-per-user.md](m3a-init-once-per-user.md) | 3a — `init` once per user: every project and worktree, with no `init` per project | Done |
| [m3b-submodule-changes.md](m3b-submodule-changes.md) | 3b — Uncommitted changes inside submodules, in the Git tab and Review | Done |
