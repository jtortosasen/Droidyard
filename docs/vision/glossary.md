# Glossary

Terms used consistently across the documentation. When a term has a common but
ambiguous meaning, the definition here wins.

## Core

**Core** (codename `mydev`) — the persistent native process that owns toolchain
integration, state and the capability API. Written in Go
([ADR-0001](../adr/0001-core-runtime-go.md)). It is not an agent and not the UI.

## Capability API

The internal, typed command/query contract exposed by the core. Everything else —
HTTP, WebSocket, MCP, CLI — is an adapter over it
([ADR-0003](../adr/0003-unified-capability-api.md)).

## Capability domain

A namespace of related capabilities, e.g. `workspace`, `code`, `build`, `device`,
`emulator`, `git`, `runtime`, `shell`.

## Adapter

A protocol edge that translates between an external protocol and the capability
API. v1 adapters: HTTP/WebSocket (UI), MCP (agents), CLI (humans/scripts).

## Workspace

The unit of state in the core: a project root plus the processes, devices, build
state and watchers associated with it. See `architecture/workspace-state.md`
(planned).

## Sidecar

An external long-lived process **supervised by the core but not embedded in it**,
with its own lifecycle: started on demand, health-checked, and stopped when idle
(e.g. the Kotlin LSP and the Gradle Tooling API bridge, both JVM). The Gradle daemon
is *not* one of ours — it is started and kept alive by Gradle, and we never kill it
implicitly.

## Agent harness

The external runtime that builds prompts, maintains conversation state, executes
tool calls and runs the agent loop against an LLM. **Not built by us**
([ADR-0004](../adr/0004-no-own-harness.md)). Also called *agent runtime*.

## LLM

The model that consumes token input and produces token output. Distinct from the
harness, which drives it.

## MCP

Model Context Protocol — the protocol by which `mydev` exposes capabilities to an
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
`sessionStart`). It belongs to the harness, not to `mydev`.

## Plugin

A distribution/extension package for some host. Depending on the host it may
bundle tools, skills, runtime state and UI integration. `mydev` is not a plugin
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
