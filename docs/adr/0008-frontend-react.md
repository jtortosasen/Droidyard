# ADR-0008: Frontend built with React and TypeScript

- Status: Accepted
- Date: 2026-09-27

## Context

The web UI ([ADR-0006](0006-web-ui-no-electron.md)) needs a frontend framework.
Two considerations dominate:

1. The developer's AI tooling is most reliable at generating React, which matters
   because a significant share of the UI will be written by AI agents.
2. The capability API ([ADR-0003](0003-unified-capability-api.md)) is typed, so a
   typed client can be generated from the same schema and reused by the UI.

## Decision

Build the frontend with **React + TypeScript**, compiled to static assets served
by the core.

- TypeScript is mandatory (not plain JS) so the capability client is type-checked.
- Generate the TypeScript client types from the capability API schema where
  possible, so UI and agents stay aligned with the core contract.
- The build toolchain is an implementation detail; a modern bundler (e.g. Vite)
  is assumed and can be revisited without an ADR.

## Consequences

- Large ecosystem and best-in-class codegen support from AI models.
- Strong typing shared with the core contract.
- A frontend build step becomes part of the project (also noted in
  [ADR-0006](0006-web-ui-no-electron.md)).
- Bundle size must be managed deliberately, since "lean" is a project goal.
- React does not constrain the editor choice: both CodeMirror and Monaco have
  first-class React wrappers ([ADR-0009](0009-editor-component.md)).

## Alternatives considered

- **Svelte/Solid/Vue** — smaller bundles in some cases, but weaker AI-codegen
  ergonomics for this developer and smaller ecosystems for editor wrappers.
- **Plain HTML/TypeScript, no framework** — viable for a very small UI but poor
  fit for the panel/state complexity of an IDE-like tool.
