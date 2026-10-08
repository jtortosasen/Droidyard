# ADR-0014: Agent distribution model

- Status: Accepted
- Superseded in part by: ADR-0037 (§Decision)
- Date: 2026-09-27

## Context

`mobile-mcp` demonstrates a portable packaging kit: one canonical
`npx -y pkg@latest` command, per-assistant snippets (Claude, Cursor, Codex,
opencode, Goose, Copilot, Gemini…), deeplinks, an `llms-install.md`, registry
manifests and one optional skill. Our core is a Go binary, not an npm package, so
the delivery vehicle differs but the *shape* of the kit should be copied.

## Decision

Distribute for agents as:

1. **Per-OS/arch binaries** with checksums (macOS and Linux; see
   [ADR-0002](0002-unix-first.md)).
2. A **one-line installer**.
3. **Per-assistant MCP configuration snippets** (Claude, Codex, Cursor, generic
   JSON), generated from a single source of truth.
4. An **`llms-install.md`** aimed at AI installers.
5. **One optional skill** in the Agent Skills format
   ([ADR-0011](0011-skills-agent-skills-standard.md)).
6. **Telemetry off by default**; if ever added, strictly opt-in and documented.

An npm wrapper (for `npx` convenience) is a *possible* add-on, not the primary
vehicle.

## Consequences

- Works across assistants without per-assistant forks.
- Requires release automation for multi-platform artifacts and generated snippets.
- Documentation must stay aligned with the generated snippets.
- No default data leaving the user's machine.

## Alternatives considered

- **npm-only** — mismatched with a Go binary, and pulls in a Node runtime.
- **Per-assistant plugins** — the exact coupling we are avoiding.
- **Default-on telemetry** — rejected on principle.
