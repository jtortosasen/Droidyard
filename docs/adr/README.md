# Architecture Decision Records

Each ADR captures one decision: the context, the choice, and its consequences.
Decisions that could reasonably have gone another way belong here rather than
buried in prose.

## Format

```text
# ADR-NNNN: <title>

- Status: Proposed | Accepted | Superseded by ADR-XXXX | Deprecated
- Date: YYYY-MM-DD
- Amended: YYYY-MM-DD        (substantive edits after acceptance)
- Supersedes: <optional>
- Relates to: <optional>

## Context
## Decision
## Consequences
## Alternatives considered
```

## Index

| ADR | Title | Status |
| --- | --- | --- |
| [0001](0001-core-runtime-go.md) | Core runtime implemented in Go | Accepted |
| [0002](0002-unix-first.md) | Unix-first (macOS + Linux), Windows deferred | Accepted |
| [0003](0003-unified-capability-api.md) | One capability API shared by UI and agents | Accepted |
| [0004](0004-no-own-harness.md) | No in-house agent harness in v1 | Accepted |
| [0005](0005-reuse-platform-tools.md) | Use Gradle and ADB directly; no Android CLI | Accepted |
| [0006](0006-web-ui-no-electron.md) | Web UI served by the core, no Electron | Accepted |
| [0007](0007-kotlin-lsp-sidecar.md) | Kotlin LSP as an external sidecar | Accepted |
| [0008](0008-frontend-react.md) | Frontend built with React and TypeScript | Accepted |
| [0009](0009-editor-component.md) | Editor component — CodeMirror 6 | Accepted |
| [0010](0010-capability-statefulness.md) | Statefulness declared per capability | Accepted |
| [0011](0011-skills-agent-skills-standard.md) | Skills follow the Agent Skills standard | Accepted |
| [0012](0012-visual-artifacts-as-refs.md) | Artifacts as opaque references, in two durability classes | Accepted |
| [0013](0013-ocr-optional-pluggable.md) | OCR is optional and pluggable | Accepted |
| [0014](0014-agent-distribution-model.md) | Agent distribution model | Accepted |
| [0015](0015-untrusted-data-authorization.md) | Untrusted device data + authorization boundary | Accepted |
| [0016](0016-capability-schema-in-go.md) | Capability schema defined in Go, published as JSON Schema | Accepted |
| [0017](0017-gradle-execution-strategy.md) | Gradle execution strategy — TAPI sidecar | Accepted |
| [0018](0018-gradle-version-support-policy.md) | Gradle version support policy | Accepted |
| [0019](0019-process-topology-daemon-and-shims.md) | Process topology — one daemon, attached by stdio and CLI shims | Accepted |
| [0020](0020-operation-contract.md) | Invocation contract — Start, Wait, Cancel, and explicit scope | Accepted |
| [0021](0021-persistence-format-and-location.md) | Persistence — JSON files in a per-user state directory | Accepted |
| [0022](0022-build-error-extraction.md) | Build error extraction — structured sources plus a core console extractor | Accepted |
| [0023](0023-android-project-facts-and-build-outputs.md) | Android project facts and build outputs | Accepted |
| [0024](0024-project-name-and-identifiers.md) | Project name and identifiers | Accepted |

## Open decisions (not yet ADRs)

These were raised in the original sketch or in review and still need an ADR:

- Authentication/exposure model when the web UI is reachable on a LAN.

Settled since: the project name and its identifiers in
[ADR-0024](0024-project-name-and-identifiers.md); MCP transport (stdio shim to a daemon for the MVP; streamable HTTP later
as another adapter on it) and the CLI adapter (`droidyard call`) in
[ADR-0019](0019-process-topology-daemon-and-shims.md); persistence in
[ADR-0021](0021-persistence-format-and-location.md).
