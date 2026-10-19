# Glossary

Terms used consistently across the documentation. When a term has a common but
ambiguous meaning, the definition here wins. MVP and v1 are defined in
[mvp](../roadmap/mvp.md) §Terms; the capability API, adapter, workspace and policy in
[overview](../architecture/overview.md).

## Core

**Core** — the persistent native process that owns toolchain
integration, state and the capability API. Written in Go
([ADR-0001](../adr/0001-core-runtime-go.md)). It is not an agent and not the UI.
Concretely, it is the `droidyard serve` daemon, one per user
([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)).

## Shim

A stateless process that relays a client to the daemon over its Unix socket:
`droidyard mcp` (stdio MCP) and `droidyard call` (CLI). Killing a shim ends a session, never
the core ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)).

## Session

One adapter connection: one shim, one `droidyard call` invocation, or one WebSocket. It
owns subscriptions and watches. An MCP session is bound to one root at a time, which
`workspace.enter` moves within its project ([ADR-0082](../adr/0082-session-enters-a-worktree.md)).
It does not own operations.

## Occupation

A live MCP session holding a linked worktree or a device, so that another chat does not write
in it or act on it ([ADR-0094](../adr/0094-occupation-by-a-session.md)). The main tree is never
occupied.

## Host, peer, lease

A **host** is a Droidyard that lends devices; a **peer** is a Droidyard it paired with, by
certificate fingerprint ([ADR-0098](../adr/0098-peer-transport-identity-pairing.md)). A peer's
link holds a lent device through any call, one peer at a time: the **lease**
([ADR-0104](../adr/0104-lease-of-a-lent-device.md)).

## Operation

The record of one invocation: id, state (`queued | running | completed | failed |
cancelled`), result. Every call is one. Callers wait on it for a bounded time; an agent or
a peer waits on and cancels only its own, the UI and the CLI any
([ADR-0101](../adr/0101-waits-reach-own-operations.md)).

## Artifact

A byte payload handed out as an opaque `droidyard://artifact/<id>` reference, never a path:
a screenshot, a log capture, a build console. It is either *ephemeral* (memory, TTL) or
*durable* (kept as long as its owning record), and it is read through `artifact.read`
([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)).

## Sidecar

An external long-lived process **of ours**, supervised by the core but not embedded in
it, with its own lifecycle: started on demand, health-checked, and stopped when idle.
Examples are the Gradle Tooling API sidecar (JVM, one for all roots) and, later, the
Kotlin LSP. The Gradle daemon is *not* a sidecar: Gradle starts it and keeps it alive,
and we never kill it implicitly. The `adb` server and emulators are not sidecars
either.

## Agent harness

The external runtime that builds prompts, maintains conversation state, executes
tool calls and runs the agent loop against an LLM. **Not built by us**
([ADR-0004](../adr/0004-no-own-harness.md)). Also called *agent runtime*.

## Tool

An executable capability presented to an agent (over MCP) or a human (over the
API), e.g. `device.tapElement`.

## Skill

Knowledge/procedure that teaches an agent *how to combine* tools for a workflow
(e.g. build → install → launch → inspect → logs → diagnose → edit → rebuild).
Separate from tools.

## Hook

An extension point in an **agent harness**'s lifecycle (e.g. `preToolUse`,
`sessionStart`). It belongs to the harness, not to Droidyard.

## Plugin

A distribution/extension package for some host. Depending on the host it may
bundle tools, skills, runtime state and UI integration. Droidyard is not a plugin
host in v1.

## Delta install

Incremental deployment that transfers only changed artifacts to a device.
Distinct from Gradle incremental *build* (which avoids recompiling unchanged
code).

## UI tree

The structured hierarchy of on-screen elements of a running app, as exposed by the
platform (accessibility / view hierarchy inspection). Used for semantic element
resolution and input.
