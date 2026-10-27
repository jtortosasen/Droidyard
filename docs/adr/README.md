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

Only decisions costly to reverse get an ADR (`AGENTS.md` §Conventions, owner,
2026-10-27); a change to a detail an ADR set is recorded in its commit message as
`Changes ADR-NNNN §X`, and the code is then current. An accepted ADR is not edited
again. A changed decision that itself meets that bar is a new ADR that supersedes the
old one in whole or in part, and the old file gains only its one header line. The `Amended:` lines that older ADRs carry are historical: they record
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
| [0094](0094-occupation-by-a-session.md) | A session occupies its worktree and the device it acts on | Accepted |
| [0095](0095-hooks-joined-and-agent-edits.md) | A hook is joined to its chat's session, and the agent's own edits pass through the hooks | Accepted |
| [0096](0096-trash-listed-for-activity.md) | The trash is listed, so Activity restores a delete made before the tab opened | Accepted |
| [0097](0097-local-first-with-peers.md) | Local-first, with peers that lend devices | Accepted |
| [0098](0098-peer-transport-identity-pairing.md) | Peers: mutual TLS between daemons, pinned identities, pairing by hand | Accepted |
| [0099](0099-what-a-host-lends.md) | What a host lends, to whom, and what a peer may call | Accepted |
| [0100](0100-remote-devices-on-the-client.md) | Remote devices on the client, the same capabilities relayed | Accepted |
| [0101](0101-waits-reach-own-operations.md) | `operation.wait` and `operation.cancel` reach one's own operations | Accepted |
| [0102](0102-local-channels-to-the-device.md) | Forwards to the device are sockets in `run/`, and the reader fences its reads | Accepted |
| [0103](0103-shared-serial-and-emulator-class.md) | A shared serial is refused; a product makes an emulator only on an emulator's serial | Accepted |
| [0104](0104-lease-of-a-lent-device.md) | A lent device is leased: one peer at a time, by 2s's occupation | Accepted |
| [0105](0105-trash-rows-outside-delete-spans.md) | Activity shows a trash entry no restorable delete row accounts for | Accepted |
| [0106](0106-peer-logcat-streams.md) | A peer's link holds up to four Logcat streams per lent device | Accepted |
| [0107](0107-pairing-by-single-use-code.md) | Pairing by a single-use code | Accepted |
| [0108](0108-share-lifetime-and-consent.md) | A share's lifetime, its capabilities and its consent | Accepted |
| [0109](0109-private-networks-live-peer-config.md) | Private networks and live peer configuration | Accepted |
| [0110](0110-full-ui-dump.md) | A full UI dump for parsers, as an artifact in `uiautomator dump`'s format | Accepted |
| [0111](0111-ui-events-between-reads.md) | The UI's events kept on the device, read since a cursor and waited on | Accepted |
| [0112](0112-ui-events-stream-and-panel.md) | The UI events stream and its panel in the web UI | Accepted |
| [0113](0113-ui-recording.md) | A UI recording: every distinct screen of a span, with its events | Accepted |
| [0114](0114-patient-ui-events-reach.md) | A patient events reach for the UI events stream and the recorder | Accepted |
| [0115](0115-ui-reads-and-recordings-lent.md) | The UI reads and the recording lent to a peer | Accepted |
| [0116](0116-remote-recording-adoption.md) | The client adopts only a remote recording whose start it lost | Accepted |
| [0117](0117-live-view-input-over-the-stream.md) | The live view's input travels its own stream, authorized when the view opened | Accepted |
| [0118](0118-a-view-never-far-behind.md) | A view never far behind, and a lighter stream for a remote marked fast | Accepted |
| [0119](0119-the-live-view-stays-on-tcp.md) | The live view stays on TCP; a datagram transport waits for a measured loss | Accepted |
| [0120](0120-remote-view-input-on-the-peer-socket.md) | A remote device's view sends its input on the peer screen socket | Accepted |
| [0121](0121-lag-bound-from-the-first-read.md) | Lag counts from the first read, not on a relayed Hub | Accepted |
| [0122](0122-a-cli-for-people.md) | A CLI for people: commands per domain from the descriptors, and CLI connections that carry streams | Accepted |
| [0123](0123-a-fast-change-renews-the-view.md) | A fast change travels in the open session and renews the view | Accepted |
| [0124](0124-init-once-per-user.md) | `init` once per user, a session with no tools outside a project, and init's files kept out of git | Accepted |
| [0125](0125-submodule-changes-in-the-git-watch.md) | The Git watch reads inside submodules: git flags them on a trusted root, the daemon reads them on an untrusted one | Accepted |
| [0126](0126-ignore-block-shared-by-runs.md) | The ignore block is shared by every run in a repository | Accepted |
| [0127](0127-chats-from-the-client-stores.md) | The sessions panel lists chats read from the clients' own stores | Accepted |
| [0128](0128-ui-own-faces-and-contrast-floor.md) | The UI ships its own faces and holds every text pair to WCAG AA | Accepted |
| [0129](0129-sdk-provisioning-by-droidyard.md) | Droidyard provisions the SDK and AVDs, with consent kept human and nothing in use replaced | Accepted |
| [0130](0130-droidyard-installs-sdk-packages.md) | Droidyard installs SDK packages itself; `avdmanager` stays for AVDs | Accepted |
| [0131](0131-sdk-provisioning-narrowings.md) | Setuid processes, the emulator's lock and a licences-only root, as step 1 found them | Accepted |
| [0132](0132-linux-processes-the-kernel-hides.md) | On Linux, a process whose executable the kernel hides is skipped by the in-use check | Accepted |
| [0133](0133-agents-act-by-coordinates-keys-and-system-verbs.md) | Agents act on the device by coordinates, keys and system verbs | Accepted |
| [0134](0134-panel-by-worktree-sessions-by-folder.md) | The column shows the tab's project as its worktrees, and the sessions by folder with their client | Accepted |
| [0135](0135-host-side-filters-for-device-logs.md) | Host-side filters for device logs, local and lent | Accepted |
| [0136](0136-searching-text-artifacts-and-build-errors.md) | Searching text artifacts and build errors | Accepted |

## Open decisions (not yet ADRs)

These were raised in the original sketch or in review and still need an ADR:

- Authentication/exposure model when the web UI is reachable on a LAN.

Settled since: the project name and its identifiers in
[ADR-0024](0024-project-name-and-identifiers.md); MCP transport (stdio shim to a daemon for the MVP; streamable HTTP later
as another adapter on it) and the CLI adapter (`droidyard call`) in
[ADR-0019](0019-process-topology-daemon-and-shims.md); persistence in
[ADR-0021](0021-persistence-format-and-location.md).
