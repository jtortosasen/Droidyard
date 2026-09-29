# ADR-0004: No in-house agent harness in v1

- Status: Accepted
- Date: 2026-09-27

## Context

It is tempting to build the whole AI loop: prompt assembly, context building,
session state, the agent loop, tool dispatch. But this is a large, fast-moving
space where external tools (Cursor, Codex, Claude, and others) already excel and
compete. Building a harness would split the project's focus and tie it to a model
or vendor.

An MCP server can maintain state, threads, event loops, watchers, sockets and
child processes. What it does not control is a harness's internal lifecycle
(prompt assembly, loop, sessions). That is exactly the part we do not need to
own.

## Decision

Do **not** build an agent harness or agent loop in v1. Expose the core's
capabilities via **MCP** and let an external agent provide the harness.

## Consequences

- `AioDevAndroid` stays AI-agnostic and useful with no agent attached.
- The project is model- and vendor-neutral: any MCP-capable agent can use it.
- We accept that some harness-level integrations (hooks, custom UI) are out of
  reach; they can be added later as non-essential adapters/plugins.
- The MCP tool surface and its ergonomics become a primary design surface.

## Alternatives considered

- **Full harness** — out of scope, huge cost, competes with better-resourced
  tools. Rejected.
- **Harness plugin for one agent** (e.g. mirroring the `dsh-android` approach) —
  couples the project to one host. Rejected as the primary path; may be revisited
  as an optional adapter.
