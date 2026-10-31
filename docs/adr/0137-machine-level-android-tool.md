# ADR-0137: Droidyard is a machine-level Android tool; the project is named per call, and the UI, Git, files and sessions leave

- Status: Proposed
- Date: 2026-10-29
- Relates to: ADR-0003; ADR-0015 §5; ADR-0031; ADR-0075; ADR-0081; ADR-0094 (the device's occupation stays, the worktree's goes); ADR-0104; ADR-0122; [plan m3j](../plans/m3j-machine-level-android-tool.md)

## Context

Droidyard grew from an Android runtime for agents into a workbench: a web UI, Git, a file
tree with a trash, sessions and chats, worktree occupation, and assistant hooks that guard
edits and redirect `rm`. The owner (2026-10-28): "this droidyard is covering too much";
Droidyard is to be purely Android, at machine level, on top of adb and SDK management, with
the UI and Git as a separate project; Build and Gradle stay.

Observed, `daemon.log` from 2026-10-15 to 2026-10-28 (calls per client and domain):

- agents over MCP: `device` 155, `workspace` 55 (almost all the binding's automatic
  `workspace.open`), `build` 6, `emulator` 2, `project` 1, `shell` 1; `file` and `git` 0.
- the UI: `git` 2339, `session` 1853, `file` 283, `device` 239, `build` 126.
- the CLI: `agent` 406 and `workspace` 271, all from the assistant hooks.

Agents use the Android part; Git, files and sessions exist for the UI, and the hooks exist
to keep sessions bound. By size (non-test lines): `web/src` about 72,000; `git`, `file`,
`chats`, `assistant` and `workspace` about 18,700; `device`, `emulator` and `sdk` about
40,100; `build` 6,600. Of the register's open rows, about 88 are in Area `web UI`, 28 `git`,
25 `files`, and most of the 92 `MCP/agents` concern sessions, binding and hooks.

The binding broke in use (2026-10-28, observed): the Cursor app runs one `droidyard` user
server for every chat and starts it once per value `${workspaceFolder}` takes, including
the literal string; the chat of an Android project received the instance started from a
closed non-Android folder (`No Android project at …/chromebook-protection`) and had no
tools, so its agent drove the device with `adb` in the shell. A session bound at start
(ADR-0019 §Sessions) assumes one process per chat started from the chat's folder; a client
that shares one process breaks it, and nothing in the protocol names the calling chat
(Cursor's documentation lists MCP roots as supported; its forum reports `roots/list`
answering "Method not found" [not verified here]).

The capability core already takes an explicit scope (ADR-0020 §Scope): the binding only
fills `workspace` on the agent's behalf. Of 101 capabilities, 64 are global; the device,
emulator and SDK tools agents used need no project.

## Decision

1. **Scope.** Droidyard is one machine-level daemon with a CLI, MCP and an HTTP/WebSocket
   API for Android work:
   - stays: `device.*`, `emulator.*`, `sdk.*`, `build.*`, `project.modules`,
     `project.variants`, `artifact.*`, `operation.*`, `events.*`, `system.*`, `policy.*`
     (minus its `shell` section), `hosting.*` and the peer link (Delegated: lending a
     device is device work);
   - leaves: the web UI (`web/` and the assets the daemon embeds), `git.*`, `file.*`
     (the trash and `droidyard trash` with it), `session.*` and the chats readers,
     `workspace.*` (open, enter, describe, close, show, forget, chooseRoot, watch), `agent.*`,
     `shell.exec`, the assistant hooks, the project file `.droidyard/project.json`
     (ADR-0046), and a worktree's occupation (ADR-0094 §Decision for worktrees; a device's
     occupation and the lease of ADR-0104 stay).
2. **No binding.** An MCP session is the machine's, not a project's. `droidyard mcp` lists
   the same tools wherever it starts and binds nothing; `--root` and `--from` are accepted
   and ignored, with one line on stderr, for one minor version (Delegated: existing client
   entries keep working), then refused. The server instructions say a project tool takes
   `project`.
3. **The project per call.** Every capability of scope `workspace` takes `project`: an
   absolute path inside the project (a worktree is its own path). The daemon resolves it
   by ADR-0075's rules (the nearest ancestor holding `settings.gradle(.kts)` is the Gradle
   root), canonicalised. A relative path, a path that does not exist, or one with no Gradle
   root is refused before anything runs, with an error that says what to pass. `project`
   replaces `workspace` in the contract; the per-root state (the Gradle connection and
   sidecar, the project model, the last build, the build registry) is opened by the first
   call that names the root and released after an idle period (working default 30 min).
   Trust is unchanged: running project code needs a trusted root (ADR-0015 §5, ADR-0031);
   a linked worktree of a trusted repository is trusted (ADR-0081).
   - *What it names:* the nearest ancestor of `project`, itself included, holding a
     settings file; else `project` itself when exactly one Gradle build lies one or two
     levels below (ADR-0075 §2 (c)); home or `/` only by its own settings file. `project`
     must be an existing directory. Trust, the id, state and published paths are that
     root's (plan m3j Q2).
   - *On the wire:* a parameter, not an envelope field. `internal/schema` adds it to every
     workspace-scoped input schema from the descriptor's scope (required unless the
     descriptor marks it optional), and the core takes it off before strict decoding, so
     no handler changes and MCP lists it. This reverses the parameter ADR-0020 §Scope
     rejected "so schemas stay clean": an MCP tool has no envelope (plan m3j Q3).
   - *When:* resolved, and the root opened, at Start: bounded local file reads (ADR-0075
     (c)'s at most 1,000 directory reads) and the first open's small writes; never a
     process, a device or the network. ADR-0020's "Start does no I/O" narrows to that, so
     a refused call records no operation and builds of one root queue in start order
     (plan m3j Q1).
4. **`device.install`** takes `apk` (an absolute path to an APK file) or `project` with
   `build` (a build's output, as today); exactly one of the two.
5. **The CLI** takes `--project`, defaulting to the current directory, for people
   (ADR-0122). MCP has no default: one process may serve several chats.
6. **The HTTP/WebSocket API stays the way out for UIs.** The live view (`GET /v1/screen`,
   ADR-0078), the event topics and capability calls over the WebSocket, behind the token
   of http-websocket.md §Security baseline. The daemon serves no page and has no `ui`
   subcommand; any UI is a client like any other (ADR-0003 holds: one API).
7. **The separate project** takes the web UI, Git, files and sessions from this
   repository's history into a repository of its own before they are removed here. Its
   design is its own; Droidyard owes it the published contract only.
8. **Version.** Removing capabilities and replacing `workspace` with `project` is a major
   version of the API (capability-api.md §Discovery and versioning).
9. **Supersession, applied on acceptance** (the header's `Supersedes:` line and each old
   file's one header line, in the plan's first step): ADR-0006 whole; ADR-0015 §7 in part
   (the shell); ADR-0019 §Processes in part (1, the daemon serves no web UI; 3, no `ui`
   subcommand) and §Sessions in part (the workspace binding and its open); ADR-0020
   §Scope in part (the workspace scope is named by a `project` parameter) and §Core
   primitives in part (Start's bounded file reads); ADR-0032 in part (the `shell` keys);
   ADR-0037 in part (the hooks); ADR-0075 §2 (a), §4 and §7 in part; ADR-0094 in part (a
   worktree's occupation); ADR-0112 §Decision in part (the panel; the stream stays);
   ADR-0124 §Decision in part (2, no session without tools; 3, no hook); and whole, as
   their subsystems leave: ADR-0009, 0039, 0040, 0045, 0046, 0047, 0049, 0050, 0052,
   0053, 0054, 0058, 0059, 0060, 0061, 0062, 0063, 0064, 0065, 0066, 0067, 0069, 0070,
   0071, 0072, 0073, 0074, 0082, 0083, 0084, 0085, 0086, 0088, 0089, 0095, 0096, 0105,
   0125, 0126, 0127, 0128, 0134.

## Consequences

- An agent's tools no longer depend on where its client started the server, on whether
  `${workspaceFolder}` was expanded, or on whether processes are shared between chats.
  Project tools cost the agent one argument per call, which it always knows.
- What a session carried is lost: an agent's worktree occupation, a resumed chat's
  binding (ADR-0088), the edits guard and the `rm` redirect. Two chats building one root
  queue as two builds of one root already do (`blockedBy`).
- Every chat whose client lists the server starts the daemon and pays the tools' tokens,
  Android or not, which ADR-0124 §Alternatives considered rejected. Accepted: device tools
  are the product wherever a chat starts (the Cursor chat that broke got none), the
  idle daemon costs little (ADR-0019 §Lifetime), and `--tools core` stays for a smaller
  list (plan m3j Q6). A shim answering `tools/list` itself would put MCP logic in the
  shim, against ADR-0019 §Processes 2.
- Clients already set up keep working for one minor version: `droidyard hook` passes
  every event, and `init` removes the hook entries it wrote (plan m3j Q4).
- With no binding, the shim holds no project state; a shim that survives a daemon restart
  becomes possible and is left to a register row, not decided here.
- About 100,000 lines and about 40 ADRs' subsystems leave; the register's rows of the
  leaving Areas move with them to the separate project and are closed here.
- Documents that change with this decision: `AGENTS.md`; `docs/README.md`;
  `docs/roadmap/mvp.md` (a header line: the scope since 3j); `docs/vision/vision.md`;
  `docs/architecture/overview.md`, `capability-api.md`, `workspace-state.md`,
  `process-lifecycle.md`; `docs/interfaces/mcp.md` and `http-websocket.md`;
  `docs/subsystems/files.md` (leaves); `docs/roadmap/known-limits.md`; the skills that
  name `web`, `git` or the hooks (`work-board`, `milestone-workflow`, `prove-change`,
  `verify-daemon`, `svelte-*`, the shipped agent skill); `.githooks/pre-commit`;
  `internal/doccheck`.

## Alternatives considered

- **Keep the workbench and add a machine mode beside the binding** (proposed in this
  session first). It fixes Cursor's chats but keeps every cost the owner named: two
  session models to test and document, and the UI, Git and sessions still in the core.
  It would win if the workbench were the product; the counts above say the agents'
  product is the Android part.
- **Bind per chat through Cursor's `beforeMCPExecution` hook** (the hook receives the
  chat's `workspace_roots`). It matches a call to its chat by timing and input, unproven,
  per client, and keeps the hooks this decision removes.
- **Bind from MCP roots.** Not reliable in Cursor [reported, not verified], and one shared
  connection would still not tell chats apart; ADR-0019 kept roots out of binding for
  determinism, which still holds.
- **A per-project server entry with a fixed path** (`.cursor/mcp.json`). Works only where
  someone writes it, per project and per worktree; ADR-0124 moved to user-level entries
  because worktrees lack untracked files.
- **Drop Build and Gradle too, leaving no project at all.** Rejected by the owner
  (2026-10-28): structured build errors and the Gradle model are Android work. It would
  win if agents kept building in the shell; 6 MCP builds in two weeks is a sign to watch,
  not a reason.
- **Keep the web UI in this repository as a separate binary.** One repository would still
  carry two products' documents, register and release; the owner asked for a separate
  project.
