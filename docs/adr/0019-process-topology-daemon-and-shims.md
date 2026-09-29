# ADR-0019: Process topology — one daemon, attached by stdio and CLI shims

- Status: Accepted
- Date: 2026-09-29
- Amended: 2026-09-29. What changed: the complete subcommand list (`ui`, `trust`); the
  MCP session opens its bound root at start (verification pass); and an explicit
  `DROIDYARD_STATE_DIR` wins over the XDG runtime location for the socket
  ([ADR-0021](0021-persistence-format-and-location.md) §Location), because otherwise a
  scratch instance would share a socket with the real daemon.

## Context

The vision calls the core a **persistent local process**; the web UI, the CLI and
agents are its clients. MCP clients, however, spawn a stdio server per session and
kill it when the session ends. If that spawned process were the core:

- the web UI and any running build would die with the agent session;
- two agent windows on one project would be two cores contending for one workspace;
- the CLI (`droidyard call`) would have nothing to talk to.

Separately, devices, emulators and the `adb` server are **global** resources shared by
every workspace ([workspace-state](../architecture/workspace-state.md)). Per-device
write serialisation, emulator boot (the new serial is found by diffing `adb devices`)
and frame pipelines are only correct if exactly one process owns them.

Two shapes were evaluated seriously: one daemon hosting every workspace, and one
daemon per project root (`droidyard serve --root <path>`). See *Alternatives*.

## Decision

```text
 MCP client ──stdio──▶ droidyard mcp ──┐
 MCP client ──stdio──▶ droidyard mcp ──┤  Unix socket              ┌─ TAPI sidecar (JVM, shared)
 terminal  ──────────▶ droidyard call ─┼────────────▶ droidyard serve ─┼─ Kotlin LSP (per workspace, later)
 browser   ──HTTP/WS (loopback)────┘                           └─ adb server, emulators (not children)
```

### Processes

1. **`droidyard serve` is the core**: one long-lived daemon per user — precisely, per state
   root ([ADR-0021](0021-persistence-format-and-location.md)). It owns all state, every
   workspace, every supervised process, and serves the web UI on loopback HTTP/WebSocket.
2. **`droidyard mcp` is a stdio shim.** It connects to the daemon's socket, sends a hello,
   then relays MCP JSON-RPC in both directions. It holds no state and no MCP logic: the
   MCP adapter runs inside the daemon. When a client kills the shim, only the session
   ends.
3. **`droidyard call` is the CLI adapter** over the same socket, speaking the native
   capability envelope ([capability-api](../architecture/capability-api.md)). The
   complete subcommand set is `serve`, `mcp`, `call`, `ui`, `trust`, `status`, `stop`:
   - `droidyard ui` opens the browser at the tokenised URL
     ([http-websocket](../interfaces/http-websocket.md) §Security baseline);
   - `droidyard trust <root>` is a human channel over `policy.update` that adds the root
     to the trusted roots ([ADR-0015](0015-untrusted-data-authorization.md) §5);
   - `droidyard status` and `droidyard stop` report on and stop the daemon.
4. **Auto-start.** Every subcommand except `serve`, `status` and `stop` tries the
   socket; if nothing answers it spawns `droidyard serve --detach` and retries with
   backoff for up to 10 s. Detached means a new
   session (`setsid`), stdio redirected to `daemon.log`, cwd `/` — so a client that
   kills the shim's process group cannot reach the daemon. `--no-autostart` disables
   this. Running `droidyard serve` in the foreground is equally supported; launchd/systemd
   units are an optional post-MVP convenience.
5. **Single instance.** Before binding anything the daemon takes an exclusive `flock`
   on `<state>/daemon.lock` and writes `{pid, startTime, version, socket, http}` into
   it. The kernel releases the lock when the daemon dies, so a crashed daemon never
   blocks the next one; the file's contents only identify the holder (for `status`,
   `stop`, and error messages). Racing auto-starts resolve themselves: the loser exits,
   its shim connects to the winner. The lock holder removes a stale socket file before
   binding.
6. **Socket.** Linux: `$XDG_RUNTIME_DIR/droidyard/droidyard.sock`, else `<state>/run/`. macOS:
   `<state>/run/droidyard.sock`. **An explicit `DROIDYARD_STATE_DIR` wins over the XDG
   location**, the socket included, so that one variable isolates an instance
   ([ADR-0021](0021-persistence-format-and-location.md) §Location); without that rule a
   scratch instance would share a socket with the real daemon on Linux. Directory `0700`, socket `0600`, and the daemon checks the
   peer uid (`SO_PEERCRED` / `getpeereid`). If the path exceeds `sun_path` (104 bytes
   on macOS), fall back to `/tmp/droidyard-<uid>/`. Framing: newline-delimited JSON.
7. **Handshake.** The client's first line is
   `{"hello": {protocol, version, client: "mcp" | "cli", root?, cwd}}`. The daemon
   replies with its version and a session id, or with `DAEMON_VERSION_MISMATCH`
   (nextStep: `droidyard stop`, then retry). Shim and daemon are the same binary, so a
   mismatch means an upgrade with a stale daemon still running.
8. **Environment is the daemon's, fixed at start.** Shims do not forward their
   environment: the daemon's behaviour must not depend on which window called. A
   daemon auto-started by a GUI-launched client therefore lacks the shell's
   `JAVA_HOME`/`PATH`; the Gradle subsystem's explicit environment source
   ([gradle](../subsystems/gradle.md) §Environment) is the fix, and `system.health`
   reports the environment in use.

### Sessions

- A **session** is one connection: one shim (= one MCP session), one `droidyard call`
  invocation, or one WebSocket. Plain HTTP requests have no session. This is the
  session of [ADR-0010](0010-capability-statefulness.md): stateful and streaming
  capabilities (subscriptions, watches) are owned by a session and released when it
  ends.
- **Workspace binding (MCP).** At hello the shim resolves a root: `--root <path>` if
  given, else the nearest ancestor of its cwd containing `settings.gradle(.kts)`, else
  its cwd. The session is bound to that root for its lifetime, and the MCP adapter
  fills the `workspace` of every workspace-scoped call from it
  ([ADR-0020](0020-operation-contract.md)); agents never pass it. One MCP session works
  on one project; an agent that needs two configures two servers. MCP roots remain a
  policy input only ([mcp-features](../interfaces/mcp-features.md) §5), never a binding
  source: binding must be deterministic and visible in the server config.
- **The binding opens its root, once.** When the MCP session initializes, the adapter
  calls `workspace.open` on the bound root (no project code runs, and binding grants
  no trust). If any client later closes that workspace, the binding stays and
  workspace tools return `WORKSPACE_NOT_OPEN` until someone reopens it; the adapter
  never reopens it by itself ([ADR-0020](0020-operation-contract.md) §Scope).
- **Operations belong to the daemon, not the session.** When a session disconnects,
  the operations it started run to completion and land in their registries (a build
  finishes and becomes `build.lastResult`); only session-owned state is released. Every
  long-running capability is bounded (a timeout, a follow window, or a natural end), so
  nothing runs unattended forever. Only an explicit cancel stops an operation early.
- The UI and agents see the same operations: a build an agent started is visible and
  cancellable from the UI, and vice versa.

### Lifetime

The daemon runs until `droidyard stop`, a signal, or a crash. It does **not** exit when the
last session leaves: persistence is the point, and its idle cost is small because
sidecars idle out ([process-lifecycle](../architecture/process-lifecycle.md)).
**Ratified (2026-09-29): it does not idle-exit.** A daemon the user expects to find
there is the point of persistence, and the cost of staying alive is small because
sidecars idle out.

If the daemon crashes, shims see EOF and exit with a message on stderr (the client
shows the server as failed). The next shim auto-starts a fresh daemon, which reaps
orphans from its pid records (ADR-0021). Transparent shim reconnection is a later
improvement.

## Consequences

- The web UI and running builds survive agent sessions. Several agent windows share
  one core, one set of workspace, device and build-root locks, one event bus.
- All serialisation is in-process. Two cores cannot manage one workspace within a
  state root, so no per-workspace lock file is needed.
- The capability layer needs explicit workspace addressing (the `workspace` envelope
  field); the MCP adapter hides it behind session binding.
- A daemon crash takes down every open workspace at once. Accepted: it is one user's
  local tool, and restart reconciliation exists anyway.
- The daemon becomes something users see and stop; `droidyard status` must be good.
- Streamable-HTTP MCP, when added, is another adapter on the same daemon — no topology
  change.
- The environment is captured per daemon, not per client (review risk R3, made
  explicit).

## Alternatives considered

- **The core is the stdio process.** Simplest. Rejected for the reasons in *Context*.
- **One daemon per project root**, with the shim attaching to (and auto-starting) the
  daemon for the root the agent works in. Genuinely attractive: workspace addressing
  disappears and a crash is contained to one project. Rejected because the resources
  that most need a single owner are global:
  - per-device write serialisation, frame pipelines and `adb track-devices` would be
    duplicated per daemon, and correctness would need cross-process locks per serial;
  - `emulator.start` identifies its serial by diffing `adb devices` — two daemons
    booting concurrently would misattribute serials;
  - device policy is live-configurable ([ADR-0015](0015-untrusted-data-authorization.md))
    and would have to be propagated to N processes;
  - the web UI would need one port per project, and "recent workspaces" has no home;
  - N Go processes and N TAPI sidecars instead of one of each.

  What it buys — no `workspace` field for agents — the chosen design also delivers,
  through session binding, and agents are the only consumer for which that field has a
  real cost.
- **launchd/systemd as the only way to run the core.** Robust, but installation-heavy,
  platform-specific, and opaque when misconfigured. Kept as an optional convenience.
- **Loopback TCP for the shims.** Reachable by any local process and, through DNS
  rebinding, by web pages. A `0600` socket with a peer-uid check is stronger for free;
  the HTTP listener exists for the browser only.
- **Cancel a session's operations on disconnect.** A closed agent window or a client
  restart would silently kill a ten-minute build. Rejected: explicit cancel only.
