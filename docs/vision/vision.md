# Vision

A lean, AI-agnostic Android development runtime that replaces most day-to-day Android
Studio usage, exposing one capability API to both a lightweight web UI and external
coding agents.

## The problem

Android development is effectively locked to Android Studio:

- IDE memory and startup cost are high, even for tasks that need no IDE (build,
  install, launch, inspect, read logs, navigate code).
- The IDE is a heavyweight, GUI-bound, single-machine experience, awkward to drive from
  a tablet, a script or an AI agent.
- Coding agents work through raw `adb`, `gradlew` and shell output: unstructured stdout
  the model must discover and interpret on every run. That is slow, error-prone and
  expensive in tokens and steps.

## Origin

The project started as a **UI**: a workflow tuned to one developer's Android needs
(logs, Gradle tasks, install and run, Git). Wanting it reachable **remotely, from
another machine**, forced the UI apart from the thing doing the work: a browser cannot
be the engine. Once the engine is separate and persistent, agents can have the same
capabilities. So the **core is the deliverable**, the UI and the agent are two consumers
of it, and the agent is a first-class consumer, not an add-on. The priority follows:
the engine first, human conveniences on top as needed.

The agent's tooling surface is the priority. The MCP surface aims to be *complete*:
every operation the Android development loop needs, semantic and structured. The UI
exposes a useful subset and grows on demand. A capability only the agent uses is still
worth building.

## What we are building

One persistent local process per user, the **core**, which:

1. Manages the real Android toolchain: filesystem, processes, Git, Gradle (through the
   Tooling API), ADB, the emulator, and later the Kotlin LSP.
2. Exposes a single typed **capability API**: semantic operations returning structured
   data, not raw stdout.
3. Serves a lightweight **web UI** over loopback HTTP/WebSocket, and the same
   capabilities to agents over **MCP**, through thin stdio and CLI shims
   ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)).

It is a *development environment runtime*, not an IDE shell and not an agent. Its two
first-class consumers use the exact same primitives: a human developer who wants a
light, browser-reachable workspace (a PC, used from a tablet), and an AI coding agent
that needs reliable, structured operations.

## The core hypothesis

> A stable semantic layer over the existing Android toolchain lets general-purpose
> agents work more efficiently and more reliably than shell + CLI alone, **without
> building a proprietary AI harness.**

This is the reason the project exists and the first thing to validate. If it fails, the
value proposition collapses to "yet another ADB wrapper", which we explicitly reject.

## What "lean" means

- The core stays small in memory and fast to start.
- No embedded JVM in the core. JVM-only tools (the Gradle Tooling API, later the Kotlin
  LSP) run as **our own sidecars**: supervised, started on demand, stopped when idle.
  The Gradle daemon is Gradle's, not ours: Gradle starts, reuses and idles it out.
- The UI is not a desktop app: no Electron, no bundled Chromium.
- Modules are separable; a feature that is not needed does not have to run.

## Success criteria (falsifiable)

The MVP must meet 2, 3 and 4; criterion 1 is met in v1 ([mvp](../roadmap/mvp.md)
§Beyond the MVP):

1. Open a real, non-trivial Android project and get go-to-definition, references and
   diagnostics **without launching Android Studio**.
2. Drive **build → install → launch → logs → UI inspection** from an agent over MCP,
   with structured results at every step.
3. Idle resource footprint within a defined budget ([mvp](../roadmap/mvp.md)
   §Metrics), dominated by the unavoidable JVM sidecars, not by the core.
4. The UI and the agent reach a capability through **the same** core primitive (no
   parallel implementations).

## Non-goals

- A full Android Studio clone.
- A proprietary LLM/agent harness or agent loop.
- A cloud service. Droidyard is local-first.
- An embedded emulator engine (we drive the existing one).

## Principles

When two options conflict, prefer the one that better satisfies these, in roughly this
order.

1. **One capability API, two consumers.** The UI and the agent reach every capability
   through the same internal primitive; never a human API and a separate agent API for
   the same thing ([ADR-0003](../adr/0003-unified-capability-api.md)). The one
   deliberate exception: the human-audience capabilities (policy, the project chooser, the
   file operations) are never exposed to agents ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §6,
   [ADR-0045](../adr/0045-project-start-flow.md) §4, [ADR-0060](../adr/0060-file-capabilities-not-mcp-tools.md)).
2. **The core is AI-agnostic.** Fully useful with no agent attached. The AI is a
   consumer, not a dependency; the core does not know what a "prompt" is.
3. **Lean by default.** Small footprint, fast startup, no unnecessary long-lived
   processes. JVM tools are sidecars, started when needed and shut down when idle.
4. **Reuse the platform, don't reimplement.** Gradle, ADB, the emulator and Kotlin LSP
   evolve on their own. We orchestrate and elevate them; we do not fork or replace them
   ([ADR-0005](../adr/0005-reuse-platform-tools.md)).
5. **Semantic over shell, structure over stdout.** Frequent operations become semantic
   calls returning structured data (`device.tapElement(resourceId=...)`, not
   `adb shell input tap 540 1220` plus interpretation). Raw shell stays an escape
   hatch, not the primary interface.
6. **Protocols are adapters, not the core.** HTTP/WebSocket and MCP are edges; the
   capability API is the center. No business logic in an adapter.
7. **Explicit state and lifecycle.** Every persistent thing has an explicit owner, and
   the owner is not always us. *Our* processes (the TAPI sidecar, the LSP, watchers, log
   streams) start lazily, stop when idle and leave no orphans. Processes that belong to
   *other tools* (the adb server, the Gradle daemon, emulators) are started or used
   through those tools and never killed implicitly; stopping one is an explicit call.
   State lives in the workspace model, not scattered across adapters.
8. **Progressive disclosure with an escape hatch.** Model the common, high-value
   operations first; expose raw shell for the rest, one policy toggle away and off by
   default. Never make an unmodelled operation impossible.
9. **Modules are separable.** A capability domain can be absent, disabled or replaced
   without breaking the rest; the UI adapts to the capabilities the running core
   advertises.
10. **Observable by nature.** Every long-running operation emits structured events
    (progress, output, result). The UI subscribes; agents wait on the operation for a
    bounded time ([ADR-0020](../adr/0020-operation-contract.md)). Nobody re-runs a
    command or scrapes output to find out what happened.
11. **Bounded output and honest emptiness.** Every result is bounded, and truncation is
    reported (`truncated` plus a hint to narrow). An empty result is a fact, never a
    disguised failure; a failure is an error with a cause and a next step.
12. **Authorization is a boundary, not an annotation.** Tool annotations are usability
    hints for client approval UX. Enforcement is separate, re-validated immediately
    before execution. Policy is changed by humans, never by the agent it governs
    ([ADR-0015](../adr/0015-untrusted-data-authorization.md)).
13. **Treat device data as untrusted.** Anything read from a device or app is
    attacker-controllable: marked per field in the schema and per result on every wire;
    agents are told not to treat it as instructions; the UI never renders it as markup.
14. **Artifacts are references.** Screenshots, captures and build consoles are opaque
    references, never filesystem paths, and every reference handed out is readable
    through the API ([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)).
