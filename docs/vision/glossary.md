# Glossary

Terms used consistently across the documentation. When a term has a common but
ambiguous meaning, the definition here wins.

## Core

**Core** — the persistent native process that owns toolchain
integration, state and the capability API. Written in Go
([ADR-0001](../adr/0001-core-runtime-go.md)). It is not an agent and not the UI.
Concretely, it is the `aiodev serve` daemon, one per user
([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)).

## Shim

A stateless process that relays a client to the daemon over its Unix socket:
`aiodev mcp` (stdio MCP) and `aiodev call` (CLI). Killing a shim ends a session, never
the core ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)).

## Session

One adapter connection: one shim, one `aiodev call` invocation, or one WebSocket. It
owns subscriptions and watches. An MCP session is bound to one project root. It does
not own operations.

## Operation

The record of one invocation: id, state (`queued | running | completed | failed |
cancelled`), result. Every call is one. Callers wait on it for a bounded time, and any
session can cancel it ([ADR-0020](../adr/0020-operation-contract.md)).

## Artifact

A byte payload handed out as an opaque `aiodev://artifact/<id>` reference, never a path:
a screenshot, a log capture, a build console. It is either *ephemeral* (memory, TTL) or
*durable* (kept as long as its owning record), and it is read through `artifact.read`
([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)).

## Policy

The user's authorization rules: device allowlists, trusted project roots, and whether
`shell.exec` is enabled. It is re-checked before every execution and changed only
through human channels (UI, CLI, config file), never by an agent
([ADR-0015](../adr/0015-untrusted-data-authorization.md)).

## MVP and v1

The *MVP* is the first release, which tests the hypothesis
([roadmap/mvp](../roadmap/mvp.md)). *v1* is the first release meant for daily use: the
MVP plus code intelligence and the other items marked v1 in [scope](scope.md).

## Capability API

The internal, typed command/query contract exposed by the core. Everything else —
HTTP, WebSocket, MCP, CLI — is an adapter over it
([ADR-0003](../adr/0003-unified-capability-api.md)).

## Capability domain

A namespace of related capabilities, e.g. `workspace`, `project`, `build`, `device`,
`emulator`, `sdk`, `file`, `git`, `shell`, `code`. The authoritative list is the
capability registry ([capability-api](../architecture/capability-api.md) §Naming).

## Adapter

A protocol edge that translates between an external protocol and the capability
API. MVP adapters: HTTP/WebSocket (UI), MCP (agents), CLI (humans and scripts). All of
them run inside the daemon.

## Workspace

The unit of state in the core: a project root plus its build state, watchers and
workspace-owned sidecars. Devices and emulators are global, not part of a workspace.
See [workspace-state](../architecture/workspace-state.md).

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

## LLM

The model that consumes token input and produces token output. Distinct from the
harness, which drives it.

## MCP

Model Context Protocol — the protocol by which `AioDevAndroid` exposes capabilities to an
agent. It is an *adapter* over the capability API, not the core's internal design.

## Tool

An executable capability presented to an agent (over MCP) or a human (over the
API), e.g. `device.tapElement`.

## Skill

Knowledge/procedure that teaches an agent *how to combine* tools for a workflow
(e.g. build → install → launch → inspect → logs → diagnose → edit → rebuild).
Separate from tools.

## Hook

An extension point in an **agent harness**'s lifecycle (e.g. `preToolUse`,
`sessionStart`). It belongs to the harness, not to `AioDevAndroid`.

## Plugin

A distribution/extension package for some host. Depending on the host it may
bundle tools, skills, runtime state and UI integration. `AioDevAndroid` is not a plugin
host in v1.

## Kotlin LSP

The official Kotlin language server (JVM). Runs as a sidecar; the core is its
client/proxy ([ADR-0007](../adr/0007-kotlin-lsp-sidecar.md)).

## Delta install

Incremental deployment that transfers only changed artifacts to a device.
Distinct from Gradle incremental *build* (which avoids recompiling unchanged
code).

## UI tree

The structured hierarchy of on-screen elements of a running app, as exposed by the
platform (accessibility / view hierarchy inspection). Used for semantic element
resolution and input.
