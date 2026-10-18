# Process Lifecycle

How the core supervises the external processes it depends on. Every long-lived process
the core spawns is owned by it and has an explicit lifecycle; **no orphan processes**.
Processes that belong to other tools (the `adb` server, the Gradle daemon, emulators once
their serial is resolved) are observed, not owned. The supervisor is `internal/process`;
the core is the `droidyard serve` daemon, started, found and kept single by
[ADR-0019](../adr/0019-process-topology-daemon-and-shims.md), and the `droidyard mcp` and
`droidyard call` shims are clients, not supervised processes. Sibling:
[workspace-state.md](workspace-state.md).

## Kinds of processes

| Kind | Examples | Ownership | Lifetime |
| --- | --- | --- | --- |
| **Shared daemon** | `adb` server | global (core) | long-lived, shared across workspaces; not ours to kill |
| **Shared sidecar** | **Gradle TAPI sidecar** (JVM) | global (core) | one per core, serving every open root; idles out when no root is active |
| **Workspace sidecar** | Kotlin LSP (JVM, post-MVP) | workspace | long-lived while the workspace is open |
| **Per-operation** | `adb` client commands, `git` (a `git.watch`'s per tick, its group killed at the watch's stop), the live view's `adb shell` running the screen server (kind `device-stream`, ended with its last viewer); a `shell.exec` command (kind `shell-command`); a UI write git (kind `git-write`, [ADR-0071](../adr/0071-git-writes-for-the-ui.md)), recorded | operation | short, tied to an operation; a `shell.exec` command's or a write git's whole process group is killed when its call ends |
| **Device process** | emulator (qemu), the app under test | global / device | managed via emulator/adb, **not** a child of the core once its serial is resolved; a launch the core started is ended if its boot fails before then ([ADR-0033](../adr/0033-emulator-launch-owned-until-serial.md)), and is pid-recorded until then (kind `emulator-launch`, ADR-0021) |
| **Tool-owned daemon** | Gradle daemon, Kotlin compile daemon | Gradle | spawned and detached by Gradle; never a child of ours |

The last two rows are **not** children of the core: we do not own their process tree.

**One TAPI sidecar for all roots.** The sidecar protocol passes `root` on every call, and
each extra JVM costs hundreds of megabytes for nothing: per-root isolation comes from the
core serialising TAPI work per root ([overview](overview.md) §Concurrency), not from
separate processes. Closing a workspace closes the connection of each Gradle root no other
open workspace uses (`connection.close`), not the sidecar. The cost: a sidecar crash fails in-flight
operations on every root. The choice is reversible without a protocol change, since one
sidecar per root speaks the same protocol.

## Model

`process.Descriptor` (`internal/process/supervisor.go`): id, kind, command, scope (`global`,
`workspace:<id>`), readiness, idle timeout, log sink, shutdown policy. Health and restart are
designed but not built; they come with the first sidecar that needs them: health is liveness plus a
cheap protocol probe where available; restart is per descriptor, default `on-failure` with
exponential backoff and a capped retry count, `never` for stateful processes where a restart would
lose work unexpectedly; repeated failure surfaces as `degraded` and as a structured error, never as
a silent retry loop.

## Lifecycle states

`process.State`: `starting -> ready -> (busy | idle) -> stopping -> stopped`, with `failed` reachable
from `starting` and `ready`. Transitions are published on the `process.lifecycle` topic.

## Startup

**Lazy by default**: sidecars start on first use, not at core start, except where a workspace open
genuinely requires them. **Readiness is per process**, never a fixed sleep: `adb start-server`
completes; the Kotlin LSP's JSON-RPC `initialize`; the Gradle sidecar's `hello`; an emulator's
`sys.boot_completed`. A failure during startup puts the workspaces that need the process in
`degraded`, not `failed`; other workspaces and global capabilities are unaffected.

## Idle shutdown

The main lever for the "lean" goal: the JVM sidecars (the TAPI sidecar now, the Kotlin
LSP later) stop after a configurable idle period and restart on demand.

- Idle is measured by absence of in-flight requests and events, not wall clock alone. The
  shared TAPI sidecar is idle only when **no** root has TAPI work in flight or queued.
- Idle shutdown never interrupts an in-flight operation.
- Auto-restart on next use is transparent to consumers: the capability waits for
  readiness.

## Shutdown, cancellation and no-orphans

- Graceful order, run on `SIGTERM`/`SIGINT`: stop accepting new work, cancel and drain
  operations, send the graceful signal to sidecars, wait, force kill on timeout.
- On Unix, sidecars are launched in their **own process group**, so killing the group also
  reaps helpers a sidecar forks (the Kotlin LSP may). This is not what keeps the Gradle
  daemon alive: Gradle **detaches its daemon by design**, so it is never in our group.
- **Surviving a core that dies without running the sequence** (`SIGKILL`, OOM, panic).
  Own process groups mean the kernel does not take sidecars down with the core, so three
  layers apply:
  1. **Our sidecars exit on stdin EOF.** A hard requirement of the TAPI sidecar's
     protocol: when the core's end of the pipe closes, it cancels in-flight work and
     exits.
  2. **Linux: `Pdeathsig: SIGTERM`** on every child **except the emulator launcher**,
     which is deliberately unsupervised and must outlive the daemon once its serial is
     resolved ([emulator](../subsystems/emulator.md) §Design rule; a launch still without
     a serial is ended on the daemon's graceful shutdown, ADR-0033, and a killed daemon's
     successor ends it at reconcile when adb proves it is not a device), **and a
     `shell.exec` command** (kind `shell-command`, milestone 1k D5) **and a UI write git**
     (kind `git-write`, plan m2n P2; its own session, no terminal): its leader must
     survive a killed daemon so the next one's reconcile finds its pid record and kills
     its whole group (a fetch's `ssh` or `uploadpack` too); while the daemon lives, the
     group is killed at every end of the call (exit, timeout, cancel, workspace close,
     shutdown), a write git's by SIGTERM, then SIGKILL after 2 s: git removes its lock
     files only on a signal it can catch. A process that detaches into its
     own group or session escapes both, by design. Go delivers `Pdeathsig` when the
     *thread* that forked the child exits, so children are spawned from one goroutine
     locked to its OS thread for the daemon's lifetime.
  3. **Pid records** ([ADR-0021](../adr/0021-persistence-format-and-location.md)): the
     next daemon reaps anything the first two missed: third-party sidecars that ignore
     EOF, and macOS, which has no `Pdeathsig`.
- **Shared `adb` server**: used by other tools too, so the core does **not** kill it by
  default; it may be left running across core restarts, and an explicit capability can
  stop it.
- The **Gradle daemon** is Gradle's. Builds go through it via our TAPI sidecar
  ([ADR-0017](../adr/0017-gradle-execution-strategy.md)); we may request `--stop`
  explicitly, never implicitly.

## Output handling

`stdout`/`stderr` are captured with bounded buffers (the sink's last lines,
`process.DefaultLogLines`), so a noisy process cannot exhaust memory, and streamed to
subscribers as `process.output` events, line-framed where the tool is line-oriented.
Parsers live with the owning subsystem (ADB, Gradle, Logcat), not here.

## Crash recovery

Every sidecar and every long-running child (a `logcat` stream or the live view's screen server) has a
durable **pid record** `{id, kind, workspace?, pid, pgid, startTime, command}`, written at
spawn and removed at reap: the one piece of process state that is persisted
([ADR-0021](../adr/0021-persistence-format-and-location.md)). Short per-operation
commands are not recorded: their own timeouts bound them, and a write per `adb` call would
cost more than it saves.

On start, before spawning anything, the daemon reconciles each record: the pid alive
**and** its start time matching is our orphan (its stdio is gone, so it cannot be
adopted): terminate its process group, graceful signal then force; otherwise (dead pid, or
pid reused) drop the record. The stale socket is removed by the new lock holder
([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)). Devices and resolved
emulators are not reconciled here: they are re-adopted through `adb`.

An emulator launch is recorded as kind `emulator-launch`, with its AVD and adb server port,
from spawn until its serial is resolved; the record is removed before the serial is returned
(a failed removal ends the launch and fails the boot), and a resolved emulator is never
recorded. A record that survives (a launch whose serial never resolved) is signalled
only on a positive answer within 2 s: the recorded port's adb server answers over its
socket at the client's version, every listed serial but `usb:` devices, offline included,
names another AVD, and every emulator console on 5554-5584 that accepts a connection names
another AVD too; any doubt drops the record and signals nothing. The clients it runs, `adb
version` and `adb -s emulator-N emu avd name`, contact no server.

## Resource budget and devices

The core keeps a soft budget for sidecars through lazy start and idle timeout. The TAPI sidecar is
shared, so more workspaces do not multiply it; caps apply to workspace sidecars (the LSP, later), so
many open workspaces cannot multiply JVMs without bound. **Multiple devices run concurrently**, each
with its own lock (single-stream-slot designs, as in the prior art, are a v1 limitation we do not
inherit): writes to a device are serialised, reads may proceed in parallel, and the frame pipeline is
**per device**, with its own lifecycle, backpressure and idle shutdown; a slow consumer must not stall
other consumers of the same stream (fan-out with latest-wins backpressure).
