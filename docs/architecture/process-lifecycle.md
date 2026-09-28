# Process Lifecycle

How the core supervises the external processes it depends on. Every long-lived
process is owned by the core and has an explicit lifecycle; **no orphan
processes**.

> Status: draft. Complements `workspace-state.md` (sibling document).

## Why this exists

The core is a supervisor. Its correctness depends on how well it starts, health-
checks, restarts and stops the tools underneath it — and on how much RAM it lets
them keep while idle. This document is the reference for that behaviour.

## Kinds of processes

| Kind | Examples | Ownership | Lifetime |
| --- | --- | --- | --- |
| **Shared daemon** | `adb` server | global (core) | long-lived, shared across workspaces |
| **Workspace sidecar** | Kotlin LSP (JVM), **Gradle TAPI sidecar** (JVM) | workspace | long-lived while workspace is open |
| **Per-operation** | `adb` client commands, `git`, `uiautomator`, `screenrecord` | operation | short, tied to an operation |
| **Device process** | emulator (qemu), the app under test | global / device | managed via emulator/adb, **not** a child of the core |

Note the last row: the emulator and app processes are **not** children of the
core. We start/stop them through their own tools and observe them; we do not own
their process tree.

## Model

A **process descriptor** defines everything the supervisor needs:

```text
ProcessDescriptor
├── id            stable identifier
├── kind          shared-daemon | workspace-sidecar | per-operation
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
  - scrollback/emulator: device reports `sys.boot_completed`.
- Failures during startup put the owning workspace in `degraded`, not `failed`.

## Health and restart

- Health is checked by liveness (process alive) plus a cheap protocol probe where
  available.
- Restart policy is per descriptor. Default: `on-failure` with exponential
  backoff and a capped retry count; `never` for stateful processes where a
  restart would lose work unexpectedly.
- Repeated failure surfaces as `degraded` and as a structured error to consumers,
  not as silent retry loops.

## Idle shutdown

The main lever for the "lean" goal. The Kotlin LSP is the biggest sidecar; it
should stop after a configurable idle period and restart on demand.

- Idle is measured by absence of in-flight requests and events, not wall clock
  alone.
- Idle shutdown must be safe: never interrupt an in-flight operation.
- Auto-restart on next use is transparent to consumers (the capability waits for
  readiness).

## Shutdown, cancellation and no-orphans

- Graceful shutdown order: stop accepting new work → cancel/drain operations →
  send the graceful signal to sidecars → wait → force kill on timeout.
- On Unix, sidecars are launched in their **own process group** so that killing
  the group reaps children (Gradle and LSP both fork).
- The core installs signal handlers (`SIGTERM`/`SIGINT`) to run this sequence on
  exit.
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

On start, the core reconciles stale state: detects processes recorded as running
that are provably gone (dead pid, missing socket) and clears their state. It must
not double-start a sidecar that is actually still alive.

## Resource budget

- The core sets a soft budget for sidecars and enforces it through lazy start +
  idle timeout.
- Per-workspace caps prevent opening many heavy workspaces from multiplying JVM
  sidecars without bound.

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
