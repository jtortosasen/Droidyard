# Architecture Decision Records

Each ADR captures one decision: the context, the choice, and its consequences.
Decisions that could reasonably have gone another way belong here rather than
buried in prose.

## Format

```text
# ADR-NNNN: <title>

- Status: Proposed | Accepted | Superseded by ADR-XXXX | Deprecated
- Date: YYYY-MM-DD
- Supersedes: <optional>: ADR-NNNN (whole) or ADR-NNNN §Section (in part)
- Superseded in part by: <optional>: ADR-XXXX (§Section)   (header only; whole supersession shows in Status)
- Relates to: <optional>

## Context
## Decision
## Consequences
## Alternatives considered
```

An accepted ADR is not edited again (`AGENTS.md` §Conventions). A changed decision is a
new ADR that supersedes the old one in whole or in part, and the old file gains only its
one header line. The `Amended:` lines that older ADRs carry are historical: they record
edits made before this rule and are not added to.

## Index

| ADR | Title | Status |
| --- | --- | --- |
| [0001](0001-core-runtime-go.md) | Core runtime implemented in Go | Accepted |
| [0002](0002-unix-first.md) | Unix-first (macOS + Linux), Windows deferred | Accepted |
| [0003](0003-unified-capability-api.md) | One capability API shared by UI and agents | Accepted |
| [0004](0004-no-own-harness.md) | No in-house agent harness in v1 | Accepted |
| [0005](0005-reuse-platform-tools.md) | Use Gradle and ADB directly; no Android CLI | Accepted |
| [0006](0006-web-ui-no-electron.md) | Web UI served by the core, no Electron | Accepted |
| [0007](0007-kotlin-lsp-sidecar.md) | Kotlin LSP as an external sidecar | Accepted |
| [0008](0008-frontend-react.md) | Frontend built with React and TypeScript | Superseded by ADR-0039 |
| [0009](0009-editor-component.md) | Editor component — CodeMirror 6 | Accepted |
| [0010](0010-capability-statefulness.md) | Statefulness declared per capability | Accepted |
| [0011](0011-skills-agent-skills-standard.md) | Skills follow the Agent Skills standard | Accepted |
| [0012](0012-visual-artifacts-as-refs.md) | Artifacts as opaque references, in two durability classes | Accepted |
| [0013](0013-ocr-optional-pluggable.md) | OCR is optional and pluggable | Accepted |
| [0014](0014-agent-distribution-model.md) | Agent distribution model | Accepted |
| [0015](0015-untrusted-data-authorization.md) | Untrusted device data + authorization boundary | Accepted |
| [0016](0016-capability-schema-in-go.md) | Capability schema defined in Go, published as JSON Schema | Accepted |
| [0017](0017-gradle-execution-strategy.md) | Gradle execution strategy — TAPI sidecar | Accepted |
| [0018](0018-gradle-version-support-policy.md) | Gradle version support policy | Accepted |
| [0019](0019-process-topology-daemon-and-shims.md) | Process topology — one daemon, attached by stdio and CLI shims | Accepted |
| [0020](0020-operation-contract.md) | Invocation contract — Start, Wait, Cancel, and explicit scope | Accepted |
| [0021](0021-persistence-format-and-location.md) | Persistence — JSON files in a per-user state directory | Accepted |
| [0022](0022-build-error-extraction.md) | Build error extraction — structured sources plus a core console extractor | Accepted |
| [0023](0023-android-project-facts-and-build-outputs.md) | Android project facts and build outputs | Accepted |
| [0024](0024-project-name-and-identifiers.md) | Project name and identifiers | Accepted |
| [0025](0025-mcp-adapter-implementation.md) | MCP adapter implementation — mark3labs/mcp-go | Accepted |
| [0026](0026-workspace-addressing-invalid-roots.md) | Workspace addressing — invalid roots and newer state files | Accepted |
| [0027](0027-workspace-close-and-reopen.md) | Workspace close and reopen — when close completes | Accepted |
| [0028](0028-websocket-library.md) | WebSocket library — coder/websocket | Accepted |
| [0029](0029-ui-reader-dex.md) | UI reads through a one-shot dex reader that keeps accessibility services running | Accepted |
| [0030](0030-persistent-ui-reader.md) | The UI reader stays running on the device | Accepted |
| [0031](0031-configuration-file-trust-first.md) | The configuration file starts with trusted roots only | Accepted |
| [0032](0032-configuration-file-v1.md) | The configuration file, version 1: policy and tool keys | Accepted |
| [0033](0033-emulator-launch-owned-until-serial.md) | An emulator launch is droidyard's until its serial is resolved | Accepted |
| [0034](0034-device-calls-report-busy-crash-dialog.md) | Device calls report a busy app, an app crash and a system dialog | Accepted |
| [0035](0035-reader-acts-fenced-by-peer-uid.md) | The reader acts: taps, keys and set-text, fenced by its peer's uid | Accepted |
| [0036](0036-agent-surface-token-budget.md) | The agent surface's token budget | Accepted |
| [0037](0037-assistant-integration-by-init.md) | Optional assistant integration, written by `droidyard init` | Accepted |
| [0038](0038-no-agents-md-block.md) | `droidyard init` writes no `AGENTS.md` or `CLAUDE.md` block | Accepted |
| [0039](0039-frontend-svelte.md) | Frontend built with Svelte 5 and TypeScript | Accepted |
| [0040](0040-built-ui-not-committed.md) | The built UI is not committed; Go never needs Node | Accepted |
| [0041](0041-device-watchlogs-stream.md) | `device.watchLogs`, a session-owned Logcat stream | Accepted |
| [0042](0042-watchlogs-device-gone-online.md) | `device.watchLogs`: `deviceGone` means not listed online; the no-uid-column next step | Accepted |
| [0043](0043-operation-watch.md) | `operation.watch`, the activity feed and the state at load | Accepted |
| [0044](0044-build-watchoutput.md) | `build.watchOutput`, the build console while a build runs | Accepted |
| [0045](0045-project-start-flow.md) | The project start: `droidyard` alone, one tab per project, recents and a folder dialog | Accepted |
| [0046](0046-project-file.md) | The project file, `.droidyard/project.json`, for the UI's choices | Accepted |
| [0047](0047-projects-panel-no-picker.md) | One tab for every project: the projects panel replaces the picker | Accepted |
| [0048](0048-operation-session.md) | `session` on the operation events, for the agents panel | Accepted |
| [0049](0049-file-watch-baseline.md) | `file.watch` returns its baseline and publishes its events, for the file tree | Accepted |
| [0050](0050-sessions-column.md) | The sessions panel is a column docked left of the tree, toggled from the toolbar | Accepted |
| [0051](0051-logcat-line-package.md) | `device.watchLogs` lines carry their application's package, from the line's uid | Accepted |
| [0052](0052-sessions-overlay.md) | The sessions column also shows as an overlay, on hover and at the window's left edge | Accepted |
| [0053](0053-file-operations.md) | The tree's file operations are five batch capabilities, open to agents, that never replace | Accepted |
| [0054](0054-trash.md) | A deleted file goes to its workspace's trash in the state directory | Accepted |
| [0055](0055-event-subscription-wire-shape.md) | An event frame names its subscription, and subscribing is a command | Accepted |
| [0056](0056-newer-daemon-wide-state-file.md) | A daemon-wide state file of a newer version degrades no workspace | Accepted |
| [0057](0057-probe-unread-field-omitted.md) | A probe field that could not be read is omitted, with a note | Accepted |
| [0058](0058-file-write-revision-precondition.md) | `file.write` takes a revision precondition | Accepted |
| [0059](0059-file-write-revision-null-and-absent.md) | `ifRevision` null is refused, and `absent` is settled by existence | Accepted |
| [0060](0060-file-capabilities-not-mcp-tools.md) | The file capabilities are not MCP tools | Accepted |
| [0061](0061-agent-rm-to-trash-hook.md) | An agent's `rm` is redirected to the trash by an optional assistant hook | Accepted |
| [0062](0062-agent-rm-handed-droidyard-trash.md) | An agent's `rm` is handed `droidyard trash`, which resolves operands in the agent's shell | Accepted |
| [0063](0063-hook-observes-android-commands.md) | The hook observes an agent's Android commands: a hint, and a record in Activity | Accepted |
| [0064](0064-trash-replacement-dashdash-as-typed.md) | The `trash` replacement carries `--` only when the `rm` did | Accepted |
| [0065](0065-observed-word-leading-colon.md) | A recorded word may start with one `:` | Accepted |
| [0066](0066-daemon-git-watch-and-read.md) | The daemon's git: `git.watch` and `git.read`, human-only, and a hardened invocation | Accepted |
| [0067](0067-browser-diff-review-mode-git-tree.md) | The diff in the browser, its Classic and Review modes, and the Git tree | Accepted |
| [0068](0068-release-builds-and-installer.md) | Release builds in GitHub Actions, and a one-line installer that uses `gh` while the repository is private | Accepted |
| [0069](0069-git-history-reads.md) | The history reads: `git.log`, `git.refs` and `git.show`, addressed by oid, with the history fence | Accepted |
| [0070](0070-history-graph-in-the-browser.md) | The history graph laid out in the browser, paged by oid, and a commit's file in the Diff tab | Accepted |
| [0071](0071-git-writes-for-the-ui.md) | The UI's Git writes: fetch, checkout, new branch and submodule update, on a trusted root | Accepted |
| [0072](0072-compare-two-commits.md) | Compare with current: `git.log` excludes, `git.show` takes a base, and the Diff tab compares two commits | Accepted |
| [0073](0073-git-writes-never-ask.md) | A Git write never asks: no askpass program either, and the user's own credential helpers on a trusted root | Accepted |
| [0074](0074-pull-fast-forward-and-roots-with-the-branch.md) | Pull is a fast-forward only, and a synchronised branch applies to the roots that have it | Accepted |
| [0075](0075-project-root-and-gradle-root.md) | The project root and the Gradle root | Accepted |
| [0076](0076-operation-target.md) | The operation's target on `operation.state` | Accepted |
| [0077](0077-screen-stream-source.md) | The live view's source is scrcpy's server, vendored and pinned | Accepted |
| [0078](0078-screen-watch-and-endpoint.md) | `device.watchScreen`, the `device.screen` topic and `GET /v1/screen` | Accepted |
| [0079](0079-screen-input-capabilities.md) | Input from the live view: `device.tap`, `device.swipe`, `device.pressKey` | Accepted |
| [0080](0080-flag-secure-detection.md) | `FLAG_SECURE` is read from the focused window | Accepted |
| [0081](0081-worktree-inherits-trust.md) | A linked worktree of a trusted repository is trusted | Accepted |
| [0082](0082-session-enters-a-worktree.md) | A session enters a worktree of its project; a worktree is a workspace of its project | Accepted |
| [0083](0083-session-watch-and-panel-by-project.md) | `session.watch`, and the sessions panel grouped by project | Accepted |
| [0084](0084-one-tab-workspace-show.md) | One tab: `droidyard` switches an open tab instead of opening another | Accepted |
| [0085](0085-hook-root-from-command-directory.md) | The hook records where the command ran | Accepted |
| [0086](0086-workspace-forget.md) | A recent project can be removed: `workspace.forget` | Accepted |
| [0088](0088-chat-id-recovers-binding.md) | A session carries its chat's id, and a resumed chat gets its worktree back | Accepted |
| [0089](0089-describe-lists-worktrees.md) | `workspace.describe` lists the project's worktrees | Accepted |
| [0090](0090-init-recognises-its-own-entries.md) | `init` recognises its own stale entries by their shape | Accepted |
| [0091](0091-leftover-temporary-files-swept.md) | Leftover temporary files are swept, and asides get their own name | Accepted |
| [0092](0092-switched-gradle-root-closes.md) | A Gradle root switched away from closes its connection | Accepted |
| [0093](0093-race-detector-in-ci.md) | CI also runs the race detector | Accepted |
| [0097](0097-local-first-with-peers.md) | Local-first, with peers that lend devices | Accepted |
| [0098](0098-peer-transport-identity-pairing.md) | Peers: mutual TLS between daemons, pinned identities, pairing by hand | Accepted |
| [0099](0099-what-a-host-lends.md) | What a host lends, to whom, and what a peer may call | Accepted |
| [0100](0100-remote-devices-on-the-client.md) | Remote devices on the client, the same capabilities relayed | Accepted |
| [0101](0101-waits-reach-own-operations.md) | `operation.wait` and `operation.cancel` reach one's own operations | Accepted |
| [0103](0103-shared-serial-and-emulator-class.md) | A shared serial is refused; a product makes an emulator only on an emulator's serial | Accepted |

## Open decisions (not yet ADRs)

These were raised in the original sketch or in review and still need an ADR:

- Authentication/exposure model when the web UI is reachable on a LAN.

Settled since: the project name and its identifiers in
[ADR-0024](0024-project-name-and-identifiers.md); MCP transport (stdio shim to a daemon for the MVP; streamable HTTP later
as another adapter on it) and the CLI adapter (`droidyard call`) in
[ADR-0019](0019-process-topology-daemon-and-shims.md); persistence in
[ADR-0021](0021-persistence-format-and-location.md).
