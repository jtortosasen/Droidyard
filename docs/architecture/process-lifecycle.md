# Process Lifecycle

How the core supervises the external processes it depends on. Every long-lived
process the core spawns is owned by it and has an explicit lifecycle; **no orphan
processes**. Processes that belong to other tools (the `adb` server, the Gradle
daemon, emulators) are observed, not owned.

> Status: draft. Complements `workspace-state.md` (sibling document).

## Why this exists

The core is a supervisor. Its correctness depends on how well it starts, health-
checks, restarts and stops the tools underneath it — and on how much RAM it lets
them keep while idle. This document is the reference for that behaviour.

The core itself is the `droidyard serve` daemon; how it is started, found and kept single
is [ADR-0019](../adr/0019-process-topology-daemon-and-shims.md). The `droidyard mcp` and
`droidyard call` shims are clients, not supervised processes.

## Kinds of processes

| Kind | Examples | Ownership | Lifetime |
| --- | --- | --- | --- |
| **Shared daemon** | `adb` server | global (core) | long-lived, shared across workspaces; not ours to kill |
| **Shared sidecar** | **Gradle TAPI sidecar** (JVM) | global (core) | one per core, serving every open root; idles out when no root is active |
| **Workspace sidecar** | Kotlin LSP (JVM, post-MVP) | workspace | long-lived while the workspace is open |
| **Per-operation** | `adb` client commands, `git`, `uiautomator`, `screenrecord`; a `shell.exec` command (recorded, kind `shell-command`) | operation | short, tied to an operation; a `shell.exec` command's whole process group is killed when its call ends |
| **Device process** | emulator (qemu), the app under test | global / device | managed via emulator/adb, **not** a child of the core |
| **Tool-owned daemon** | Gradle daemon, Kotlin compile daemon | Gradle | spawned and detached by Gradle; never a child of ours |

Note the last two rows: those processes are **not** children of the core. We
start/stop them through their own tools and observe them; we do not own their
process tree.

**One TAPI sidecar for all roots.** The sidecar protocol already passes `root` on every
call, and each extra JVM costs hundreds of megabytes for nothing: per-root isolation
comes from the core serialising TAPI work per root
([core-runtime](core-runtime.md) §Concurrency), not from separate processes. Closing a
workspace closes that root's connection (`connection.close`), not the sidecar. The
cost: a sidecar crash fails in-flight operations on every root. The choice is
reversible without a protocol change — spawning one sidecar per root speaks the same
protocol.

## Model

A **process descriptor** defines everything the supervisor needs:

```text
ProcessDescriptor
├── id            stable identifier
├── kind          shared-daemon | shared-sidecar | workspace-sidecar | per-operation
├── command/args/cwd/env
├── scope         global | workspace:<id> | operation:<id>
├── readiness     how to detect it is up (see below)
├── health        how to detect it is alive/healthy
├── restart       never | on-failure | always (+ backoff)
├── idleTimeout   stop after N of inactivity (if applicable)
├── logSink       where stdout/stderr go (structured events)
└── shutdown      graceful signal + timeout before force kill
```

## Lifecycle states

```text
starting -> ready -> (busy | idle) -> stopping -> stopped
                \                         ^
                 +------ failed ----------+
```

- `starting`: spawned, waiting for readiness.
- `ready`: passed the readiness probe.
- `busy` / `idle`: for processes whose idleness drives auto-shutdown (e.g. LSP).
- `failed`: exited unexpectedly or never became ready.
- `stopped`: deliberately terminated.

Transitions are published on the `process.lifecycle` topic.

## Startup

- **Lazy by default.** Sidecars start on first use, not at core start, except
  where a workspace open genuinely requires them.
- **Readiness detection is per process**, never a fixed sleep:
  - `adb`: `adb start-server` completes / server socket reachable.
  - Kotlin LSP: JSON-RPC `initialize` handshake completes.
  - Gradle TAPI sidecar: the `hello` handshake completes.
  - emulator: device reports `sys.boot_completed`.
- Failures during startup put the workspaces that need the process in `degraded`, not
  `failed`; other workspaces and global capabilities are unaffected.

## Health and restart

- Health is checked by liveness (process alive) plus a cheap protocol probe where
  available.
- Restart policy is per descriptor. Default: `on-failure` with exponential
  backoff and a capped retry count; `never` for stateful processes where a
  restart would lose work unexpectedly.
- Repeated failure surfaces as `degraded` and as a structured error to consumers,
  not as silent retry loops.

## Idle shutdown

The main lever for the "lean" goal. The JVM sidecars — the TAPI sidecar now, the
Kotlin LSP later — should stop after a configurable idle period and restart on demand.

- Idle is measured by absence of in-flight requests and events, not wall clock
  alone. The shared TAPI sidecar is idle only when **no** root has TAPI work in
  flight or queued.
- Idle shutdown must be safe: never interrupt an in-flight operation.
- Auto-restart on next use is transparent to consumers (the capability waits for
  readiness).

## Shutdown, cancellation and no-orphans

- Graceful shutdown order: stop accepting new work → cancel/drain operations →
  send the graceful signal to sidecars → wait → force kill on timeout.
- On Unix, sidecars are launched in their **own process group** so that killing
  the group also reaps helpers a sidecar forks (the Kotlin LSP may). This is not what
  keeps the Gradle daemon alive: Gradle **detaches its daemon by design**, so it is
  never in our group whatever we do — which is what we want.
- The core installs signal handlers (`SIGTERM`/`SIGINT`) to run this sequence on
  exit.
- **Surviving a core that dies without running it** (`SIGKILL`, OOM, panic). Own
  process groups mean the kernel will not take sidecars down with the core, so three
  layers apply:
  1. **Our sidecars exit on stdin EOF.** We write the TAPI sidecar, so this is a hard
     requirement of its protocol: when the core's end of the pipe closes, the sidecar
     cancels in-flight TAPI work and exits. Tested like any other protocol rule.
  2. **Linux: `Pdeathsig: SIGTERM`** on every child **except the emulator launcher**,
     which is deliberately unsupervised and must outlive the daemon
     ([emulator](../subsystems/emulator.md) §Design rule), **and a `shell.exec`
     command** (kind `shell-command`, milestone 1k D5): its leader must survive a killed
     daemon so the next one's reconcile can find its pid record and kill its whole group;
     while the daemon lives, the group is killed at every end of the call (exit, timeout,
     cancel, workspace close, shutdown). A process that detaches into its own group or
     session escapes both, by design (Gradle's daemon does). Go delivers it when the *thread*
     that forked the child exits, so children are spawned from one goroutine locked
     to its OS thread for the daemon's lifetime.
  3. **Pid records** ([ADR-0021](../adr/0021-persistence-format-and-location.md)): the
     next daemon reaps anything the first two missed — third-party sidecars that ignore
     EOF, and macOS, which has no `Pdeathsig`.
- **Shared `adb` server policy**: it is used by other tools too, so the core does
  **not** kill it by default. It may be left running across core restarts; an
  explicit capability can stop it.
- The **Gradle daemon** is owned by Gradle, not by us. Builds go through it via our
  TAPI sidecar ([ADR-0017](../adr/0017-gradle-execution-strategy.md)); we may request
  `--stop` explicitly, but we never kill it implicitly.

## Output handling

- `stdout`/`stderr` are captured into **structured events** (`process.output`)
  with bounded buffers, so a noisy process cannot exhaust memory.
- Output is line-framed where the tool is line-oriented, and streamed to
  subscribers (UI terminal, build panel, logcat).
- Parsers live with the owning subsystem (ADB, Gradle, Logcat), not here.

## Crash recovery

Every sidecar and every long-running child (a `logcat` or `screenrecord` stream) has a
durable **pid record** `{id, kind, workspace?, pid, pgid, startTime, command}`, written
at spawn and removed at reap — the one piece of process state that is persisted
([ADR-0021](../adr/0021-persistence-format-and-location.md)). Short per-operation
commands are not recorded: they are bounded by their own timeouts, and a write per
`adb` call would cost more than it saves.

On start, before spawning anything, the daemon reconciles each record:

- the pid is alive **and** its start time matches: our orphan. Its stdio is gone, so it
  cannot be adopted; terminate its process group (graceful signal, then force);
- otherwise (dead pid, or pid reused by another process): drop the record.

The stale socket is removed by the new lock holder
([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)). Devices and emulators
are not reconciled here: they are re-adopted through `adb`.

## Resource budget

- The core sets a soft budget for sidecars and enforces it through lazy start +
  idle timeout.
- The TAPI sidecar is shared, so opening more workspaces does not multiply it. Caps
  apply to workspace sidecars (the LSP, later), so many open workspaces cannot
  multiply JVMs without bound.

## Concurrency and devices

- The core supports **multiple devices concurrently**, each with its own lock.
  Single-stream-slot designs (as in the prior art) are a v1 limitation we do not
  inherit.
- Writes to a given device are serialised; reads may proceed in parallel.
- The frame pipeline is **per device**, with its own lifecycle, backpressure and
  idle shutdown.
- A slow consumer must not stall other consumers of the same stream (fan-out with
  latest-wins backpressure).

## Open questions

- Precise idle-timeout defaults per sidecar (LSP, Gradle).
- Whether the adb server should ever be auto-stopped, and under what policy.
- Backoff parameters and retry caps per descriptor.
- How to detect readiness for the emulator cheaply and reliably across API
  levels.
- Whether per-operation processes should be pooled (e.g. repeated small `adb`
  invocations) or always spawned fresh.
