# ADR-0011: Skills follow the Agent Skills standard

- Status: Accepted
- Date: 2026-09-27

## Context

Google's `android/skills` ships skills in the open **Agent Skills** format
(`agentskills.io`): a folder with `SKILL.md` (YAML frontmatter + instructions) and
optional `references/`. Claude, Codex, Cursor, Gemini CLI, Copilot and others
consume it; Cursor reads `SKILL.md` natively with **no manifest of its own**. This
is the de-facto cross-assistant standard, and it matches our goal of shipping
skills that work across assistants.

## Decision

Ship optional skills in the **Agent Skills** format:

```text
skills/<category>/<skill-id>/
├── SKILL.md        # name + description (+ our namespaced metadata)
└── references/     # stable procedural docs, mirroring our doc/capability ids
```

Rules:

- Skills are **procedural knowledge** that composes our capabilities; a skill is
  **not** a tool and defines no schema or return contract.
- **No bundled executables.** Anything executable is a capability in the core.
- **No Android CLI dependency**; skills call our capabilities
  (Gradle/ADB via the core).
- **Version each skill** (do not rely on `last-updated` alone).
- Generate per-assistant manifests (e.g. Claude, Codex, generic) from the folder
  tree as a single source of truth.
- Target workflows where LLMs measurably underperform (start with
  build → run → inspect → debug), not generic best-practice guides.

## Consequences

- Near-free support across Claude, Codex, Cursor and others.
- Progressive disclosure keeps token cost low with many skills installed.
- We own a small manifest-generation step and a skill-review process.
- Skills must degrade gracefully when a capability is unavailable.

## Alternatives considered

- **A custom skill format** — rejects portability for no gain.
- **Bundled scripts / runtime downloads** (as some Google skills do) — hurts
  determinism, security and cross-platform behaviour.
