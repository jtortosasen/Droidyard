# Vision

## One-liner

A lean, AI-agnostic Android development runtime that replaces most day-to-day
Android Studio usage, exposing one capability API to both a lightweight web UI
and external coding agents.

## The problem

Android development today is effectively locked to Android Studio:

- IDE memory and startup cost are high, even for tasks that need no IDE (build,
  install, launch, inspect, read logs, navigate code).
- The IDE is a heavyweight, GUI-bound, single-machine experience. It is awkward
  to drive from a tablet, from a script, or from an AI agent.
- Coding agents are forced to work through raw `adb`, `gradlew` and shell output:
  unstructured stdout that the model must discover and interpret on every run.
  This is slow, error-prone and expensive in tokens and steps.

## Origin

The project did not start as an AI tool. It started as a **UI**: a workflow tuned
to one developer's Android needs — logs, Gradle tasks, install and run, Git.
Wanting that UI to be reachable **remotely, from another machine**, forced a
separation between the UI and the thing doing the work.

```text
A UI tuned to a personal Android workflow
        ↓
Wanted remotely, from another machine
        ↓
UI and engine must separate (a browser cannot be the engine)
        ↓
An independent, persistent engine
        ↓
If the engine is already separate, why not give agents the same capabilities?
        ↓
Engine = the product.  UI and agent = two consumers of the same engine.
```

This is why the **core is the deliverable** and the UI is a client, and why the
agent is a first-class consumer rather than an add-on. It also explains the
priority order: the engine first, the human conveniences built on top of it as
they are needed.

The emphasis that follows: **the agent's tooling surface is the priority.** The
MCP surface aims to be *complete* — every operation the Android development loop
needs, exposed semantically and returning structured data — while the UI
deliberately exposes a useful subset and grows on demand. A capability that only
the agent uses is still worth building.

## What we are building

A persistent local process (the **core**, codename `mydev`): one daemon per user,
which the web UI reaches over loopback HTTP and agents and scripts reach through thin
stdio and CLI shims
([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)). It:

1. Manages the real Android toolchain: filesystem, processes, Git, Gradle (through
   the Tooling API), ADB, the emulator, and later the Kotlin LSP.
2. Exposes a single, typed **capability API** — semantic operations that return
   **structured data**, not raw stdout.
3. Serves a **lightweight web UI** over HTTP/WebSocket for humans (desktop and
   tablet).
4. Exposes the same capabilities to AI agents over **MCP**.

The core is a *development environment runtime*, not an IDE shell and not an
agent.

## Who it is for

Two first-class consumers of the exact same primitives:

- **A human developer** who wants a light, responsive, browser-reachable
  workspace that can run on a PC and be used from a tablet.
- **An AI coding agent** that needs reliable, structured operations instead of
  rediscovering the Android toolchain through shell on every task.

## The core hypothesis

> A stable semantic layer over the existing Android toolchain lets general-purpose
> agents work more efficiently and more reliably than shell + CLI alone, **without
> building a proprietary AI harness.**

This hypothesis is the reason the project exists and should be the first thing
validated. If it fails, the value proposition collapses to "yet another ADB
wrapper", which we explicitly reject.

## What "lean" means here

- The core must stay small in memory and fast to start.
- No embedded JVM in the core. The JVM-only tools we drive (the Gradle Tooling API,
  later the Kotlin LSP) run as **our own sidecars**: supervised, started on demand and
  stopped when idle. The Gradle daemon is Gradle's, not ours: Gradle starts it, reuses
  it and idles it out.
- The UI is not a desktop app: no Electron, no bundled Chromium.
- Modules are separable; a feature that is not needed does not have to run.

## Success criteria (falsifiable)

Design-phase targets. The MVP must meet 2, 3 and 4; criterion 1 is met in v1
([scope](scope.md) §Terms):

1. Open a real, non-trivial Android project and get go-to-definition, references
   and diagnostics **without launching Android Studio**.
2. Drive **build → install → launch → logs → UI inspection** from an agent over
   MCP, receiving structured results at every step.
3. Idle resource footprint stays within a defined budget (to be set in the MVP
   doc), dominated by the unavoidable JVM sidecars, not by the core.
4. The UI and the agent reach a given capability through **the same** core
   primitive (no parallel implementations).

## Non-goals

- A full Android Studio clone.
- A proprietary LLM/agent harness or agent loop.
- A cloud service. `mydev` is local-first.
- An embedded emulator engine (we drive the existing one).
