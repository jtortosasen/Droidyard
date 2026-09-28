# ADR-0009: Editor component — CodeMirror 6

- Status: Accepted
- Date: 2026-09-27

## Context

The UI needs a code editor for viewing, navigating and editing source, plus a
diff view for Git. The two realistic candidates were **CodeMirror 6** and
**Monaco**.

Two framing points decided the question:

- **React does not decide this.** Both have first-class React wrappers
  (`@uiw/react-codemirror`, `@monaco-editor/react`).
- **The editor does not bring the language intelligence.** Diagnostics,
  definition, references and completion are served by our own capability API over
  the Kotlin LSP (see [ADR-0003](0003-unified-capability-api.md),
  [ADR-0007](0007-kotlin-lsp-sidecar.md)), not by the editor's built-in language
  services. This removes Monaco's single biggest advantage.

Comparison:

| Dimension | CodeMirror 6 | Monaco |
| --- | --- | --- |
| Bundle size | Small, tree-shakeable | Heavy (MBs + web workers) |
| Touch / tablet | Designed for it | Desktop-oriented, weak on touch |
| Built-in IDE features | Must be wired | Rich out of the box |
| AI codegen familiarity | Lower | Very high (VS Code lineage) |
| Diff view | `@codemirror/merge` (adequate) | Superior diff editor |

The project explicitly targets tablets ([ADR-0006](0006-web-ui-no-electron.md))
and a lean footprint.

## Decision

Use **CodeMirror 6** as the editor component. Rationale:

1. **Tablet/touch is a first-class target**; CodeMirror is designed for it,
   Monaco is not.
2. **Lean footprint** matches the project goal; Monaco adds megabytes and web
   workers.
3. **We supply the language intelligence ourselves**, so Monaco's built-in
   language services — its strongest argument — would go unused.

Known trade-off: the **Git diff view**. Monaco's diff editor is better than
`@codemirror/merge`. If diff quality turns out to be critical, the escape hatch is
to keep CodeMirror for editing and evaluate a dedicated diff component for the
Git view only.

## Consequences

- Smaller frontend bundle; better touch behaviour on tablets.
- Language features are wired by us through the capability API (which is the plan
  regardless of editor).
- The Git diff view is acceptable but may need extra design if
  `@codemirror/merge` proves insufficient.
- Migration to Monaco later stays contained if the editor is isolated behind a
  small frontend interface.

## Alternatives considered

- **Monaco** — much larger bundle and weaker touch support, and its main
  advantage (built-in language services) is not used because language
  intelligence comes from the capability API. Its stronger diff editor was the
  only meaningful remaining argument.
- **Ace** — legacy, less momentum, weaker typing. Not considered further.
