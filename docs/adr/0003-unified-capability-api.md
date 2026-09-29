# ADR-0003: One capability API shared by UI and agents

- Status: Accepted
- Date: 2026-09-27
- Amended: 2026-09-29 — the invocation model is now fixed by
  [ADR-0020](0020-operation-contract.md) (Start / Wait / Cancel, explicit workspace
  scope); references updated to the documents that now exist.

## Context

The project has two first-class consumers: a human web UI and an AI agent. The
naive approach is to build a REST API for the UI and a separate MCP server with
its own logic for the agent. That duplicates behaviour, drifts over time, and
guarantees the two consumers see different capabilities and different bugs.

The original sketch identified this ("UI and AI should use the same internal
primitives") but did not model it as a layer.

## Decision

Define a single, typed **capability API** inside the core. Every consumer-facing
protocol is an **adapter** over it:

```text
              +------------------ capability API ------------------+
              |  workspace.*  code.*  build.*  device.*  git.* ...   |
              +-----------------------------------------------------+
                 ^                    ^                     ^
                 | HTTP / WebSocket   | MCP                 | CLI
                 | (web UI)           | (agents)            | (scripts)
              Browser            Cursor/Codex/Claude       Terminal
```

Capabilities are expressed as commands (mutating, long-running) and queries
(reads), both returning structured results and emitting structured events.

## Consequences

- A capability is implemented exactly once; adapters only translate.
- The UI and agents get identical semantics; a fix or feature reaches both.
- Adapters stay thin and protocol-focused.
- Requires an explicit capability contract (typing, versioning, events) to be
  designed early. See [capability-api](../architecture/capability-api.md).
- Raw shell is modelled as a capability too (`shell.exec`), so even the escape
  hatch flows through the same layer.
- One deliberate exception: policy changes are human-only. They are capabilities for
  the UI and CLI, and they are never exposed over MCP, so an agent cannot authorize
  itself ([ADR-0015](0015-untrusted-data-authorization.md) §6).

## Alternatives considered

- **Separate UI API and MCP server** — duplication and drift. Rejected; this is
  the standard MCP-server shape we are explicitly improving on.
- **MCP as the internal interface** (core logic written as MCP tools) — couples
  the core to one protocol and to agent-oriented ergonomics. Protocols must stay
  at the edge.
