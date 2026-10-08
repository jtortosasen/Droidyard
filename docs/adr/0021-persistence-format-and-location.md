# ADR-0021: Persistence — JSON files in a per-user state directory

- Status: Accepted
- Date: 2026-09-29
- Amended: 2026-09-29. What changed: the `run/` directory and the HTTP token; user
  configuration located outside the state directory; an explicit `DROIDYARD_STATE_DIR`
  now wins over the XDG runtime location for the socket.
- Amended: 2026-10-03 — the newer-state-file error is named `WORKSPACE_STATE_NEWER`
  ([ADR-0026](0026-workspace-addressing-invalid-roots.md)).
- Amended: 2026-10-03 (second pass) — the filesystem root's workspace name is `root`
  (`root-<12 hex>`), because its basename is `/`; owner decision.
- Amended: 2026-10-03 (third pass) — runtime files (`daemon.lock`, `run/`) carry no
  `schemaVersion`: their contents mean something only while the daemon holds the lock,
  and `run/http-token` is deleted on exit (milestone 1d, owner-delegated decision D8).
- Amended: 2026-10-06 — the configuration directory decided (ADR-0031): under an
  explicit `DROIDYARD_STATE_DIR` it is `<state>/config/`; on macOS without overrides
  `~/Library/Application Support/droidyard-config/`. Milestone 1g plan D1, Delegated.
- Amended: 2026-10-06 (second pass) — the derived cache's first tenant: the Gradle
  sidecar's jars, under `<cache>/gradle/` named by digest, written atomically and
  rewritten when their bytes differ from the binary's; under an explicit
  `DROIDYARD_STATE_DIR` the cache is `<state>/cache/` (one variable isolates a run).
  Milestone 1g plan D5, Delegated.
- Amended: 2026-10-07 — the derived cache also holds the probe's jar and init script, in
  `<cache>/gradle/probe-<digest>/`, written and checked by their bytes like the sidecar's
  jars (milestone 1h D4, Delegated).
- Amended: 2026-10-08 — §Pid records: an emulator launch is recorded as kind
  `emulator-launch`, with its AVD and adb server port, from spawn until its serial is
  resolved (removed before the serial is returned; a failed removal ends the launch and
  fails the boot). Reconcile signals such a record's group only on a positive answer from
  the recorded port's adb server, read over its socket within one 2 s deadline: it answers,
  its version matches, every listed serial but `usb:` devices, offline included, names
  another AVD on `emu avd name`, and every emulator console on 5554–5584 that accepts a
  connection names another AVD too (a server started moments earlier may not have scanned
  its emulators). Any doubt drops the record and signals nothing. "Before spawning
  anything" now allows, only when such a record exists and within that 2 s deadline, the
  socket reads plus `adb version` and `adb -s emulator-N emu avd name` clients, which
  contact no server (step 6 review, N2 and N3). A resolved
  emulator is still never recorded. Plan m1p Q1, Delegated.

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
  A **newer** version is never overwritten: the workspace opens `degraded` with
  `WORKSPACE_STATE_NEWER` ([ADR-0026](0026-workspace-addressing-invalid-roots.md)).
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

**An explicit `DROIDYARD_STATE_DIR` wins over the XDG location for everything, the
socket included.** Without that rule, on Linux the socket would land in
`$XDG_RUNTIME_DIR/droidyard/` even when the state root was overridden, so a scratch or
test instance would share a socket with the real daemon, and one variable would not be
enough to isolate it. Isolating a run has to be a single, obvious act.
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

Runtime files — `daemon.lock` and everything under `run/` — are not versioned state:
they carry no `schemaVersion`, and their contents mean something only while the
daemon holds the lock. A clean exit deletes `run/http-token` and leaves `daemon.lock`
empty; after a crash both may remain, stale, until the next start replaces them
(2026-10-03, milestone 1d).

The workspace id is `<basename>-<12 hex of sha256(canonical root)>`: deterministic,
and still readable when someone lists the directory. The canonical root is the
symlink-resolved absolute path, using the on-disk case on case-insensitive volumes.
The filesystem root's name is `root` (`root-<12 hex>`): its own basename is `/`, which
would put a slash into `ws/<wsid>/` (amended 2026-10-03).

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
recorded: they are not our children and are re-adopted through `adb` (except a launch
without a serial yet; amended 2026-10-08).

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
