# ADR-0021: Persistence — JSON files in a per-user state directory

- Status: Accepted
- Date: 2026-09-29
- Amended: 2026-09-29. What changed: the `run/` directory and the HTTP token; user
  configuration located outside the state directory.

## Context

Phase 0 needs durable state — workspace configuration, recent workspaces — and
Phase 1 needs more: the build registry and `build.lastResult`, which must survive
restarts ([ADR-0017](0017-gradle-execution-strategy.md) §Recorded sub-decisions),
and the build console ([gradle](../subsystems/gradle.md) §Persistence refers to a
"workspace state directory" that was never defined).

There was also a contradiction. `process-lifecycle.md` §Crash recovery reconciles
"processes recorded as running", which requires persisted pids, while
`workspace-state.md` said processes are ephemeral and "never persisted".

Scale is small: one writer (the daemon, [ADR-0019](0019-process-topology-daemon-and-shims.md)),
a few KB per workspace, a few hundred build records.

## Decision

### Format

**JSON files, one per concern**, human-inspectable.

- Every file carries `"schemaVersion": N`. Older versions are migrated in code on read.
  A **newer** version is never overwritten: the workspace opens `degraded` with a clear
  error.
- Writes are atomic: write `*.tmp`, `fsync`, `rename`, `fsync` the directory.
- A file that fails to parse is renamed to `*.corrupt-<timestamp>`, treated as absent,
  and reported in `system.health`. Corrupt state never stops the daemon.
- Single writer: only the daemon holding `daemon.lock` writes.

### Location

**Never inside the project tree**: no `.droidyard/` directory, no `.gitignore` pollution,
read-only checkouts work.

| | Linux | macOS |
| --- | --- | --- |
| State root | `$XDG_STATE_HOME/droidyard` (default `~/.local/state/droidyard`) | `~/Library/Application Support/droidyard` |
| User configuration (policy incl. trusted roots, JVM discovery, timeouts) | `$XDG_CONFIG_HOME/droidyard` (default `~/.config/droidyard`) | beside the state directory |
| Socket | `$XDG_RUNTIME_DIR/droidyard/`, else `<state>/run/` | `<state>/run/` |
| Derived caches (deletable any time) | `$XDG_CACHE_HOME/droidyard` | `~/Library/Caches/droidyard` |

`DROIDYARD_STATE_DIR` overrides the state root; it is how tests get an isolated daemon.
User configuration — policy, including trusted roots (ADR-0015 §5–6), JVM discovery,
timeouts — is **not state**. It is written only by human channels (`policy.update`,
`droidyard trust`) or by hand, and is never migrated or reset with the state.
`DROIDYARD_CONFIG_DIR` overrides its location; its format belongs to the configuration
spec.

### Layout

```text
<state>/
├── daemon.lock          flock held by the running daemon; {pid, startTime, version, socket, http}
├── daemon.log           detached daemon's stdout/stderr, size-rotated
├── processes.json       pid records of supervised children (the durable exception, below)
├── workspaces.json      index: wsid → {root, name, lastOpenedAt}  ("recent workspaces")
├── emulator/<avd>.log   detached emulator launchers' output, read for the stderr tail
├── run/                 0700; per-launch runtime files
│   ├── droidyard.sock       the socket, where the location table puts it
│   └── http-token       0600; HTTP bearer token, regenerated each start, deleted on exit
└── ws/<wsid>/
    ├── workspace.json   durable workspace config: name, selected variant, preferences
    ├── builds/
    │   ├── index.json   registry of compact build records, retention applied here
    │   └── <buildId>.json   compact build result; the newest finished one is lastResult
    └── artifacts/       durable artifacts (e.g. build consoles), handed out as refs, never paths
```

The workspace id is `<basename>-<12 hex of sha256(canonical root)>`: deterministic,
and still readable when someone lists the directory. The canonical root is the
symlink-resolved absolute path, using the on-disk case on case-insensitive volumes.

### Pid records: the durable exception

Processes are ephemeral; the **record of what we spawned** is not. For every sidecar
and every long-running child (a `logcat` or `screenrecord` stream) the daemon writes
`{id, kind, workspace?, pid, pgid, startTime, command}` when it spawns it, and removes
the entry when it reaps it. Short per-operation commands are not recorded: they are
bounded by their own timeouts.

On start, before spawning anything, the daemon reconciles every record:

- a live process whose **start time matches** the record is our orphan: `SIGTERM` its
  process group, wait, then `SIGKILL`. Sidecars are restartable by design; their stdio
  is gone, so adoption is impossible anyway;
- anything else — no such pid, or a start-time mismatch (pid reuse) — is dropped.

Start time comes from `/proc/<pid>/stat` (field 22) on Linux and
`sysctl kern.proc.pid` (`p_starttime`) on macOS. Devices and emulators are not
recorded: they are not our children and are re-adopted through `adb`.

### Locking

`daemon.lock` is an exclusive `flock` (ADR-0019). The kernel releases it when the
holder dies, so reclamation needs no pid check; the pid and start time in the file
identify the holder for humans and for `droidyard stop`. There are no per-workspace lock
files: one daemon per state root owns every workspace in it.

## Consequences

- The state directory is defined; `gradle.md`, ADR-0017 and `workspace-state.md` now
  point to one place.
- Crash recovery is concrete: stale sockets are removed by the lock holder, orphans are
  reaped from pid records.
- Everything is inspectable with `cat` and `jq`, and the binary gains no database
  dependency.
- Querying history beyond "list and read" (e.g. searching old builds) means reading
  files; acceptable at a retention of 50 builds.
- Durable artifacts need refs that stay valid across restarts; their ref semantics are
  settled with the artifact store, not here.
- Deleting `<state>/ws/<wsid>/` resets a workspace; nothing in the project is affected.

## Alternatives considered

- **SQLite.** Transactions and queries we do not need yet, at the cost of cgo or a
  multi-megabyte pure-Go driver, and state that is no longer readable with `cat`.
  Revisit if the build registry grows query needs.
- **A `.droidyard/` directory in the project.** Pollutes the tree, needs ignoring,
  breaks on read-only checkouts, and would scatter the global state (recent workspaces,
  pid records) anyway. Rejected.
- **An embedded key-value store (bbolt, Badger).** Opaque files and a dependency, for no
  gain at this size. Rejected.
- **PID files only, no `flock`.** Reclamation would depend on pid-reuse checks racing
  against a starting daemon. `flock` makes the kernel the arbiter; the pid is kept for
  identification only.
