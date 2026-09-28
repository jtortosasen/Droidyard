# ADR-0016: Capability schema defined in Go, published as JSON Schema

- Status: Accepted
- Date: 2026-09-27

## Context

Every capability has a shape: name, parameters, result, errors, statefulness. That
shape is needed by **three consumers**:

- the **Go core** (the structs that implement it),
- the **TypeScript client** of the web UI,
- the **MCP tool definitions** the agent sees.

Hand-writing it three times guarantees drift: a field added in Go but missing from
the MCP definition makes a capability unusable to the agent; a parameter the UI
sends but the core rejects is a silent break. See
[capability-api.md](../architecture/capability-api.md).

Alternatives considered: JSON Schema as the source, a custom IDL, Protocol Buffers,
and gRPC as a transport (a separate question, evaluated here too because it is
usually raised together).

## Decision

**Define capabilities in Go** (structs plus a JSON Schema generation library) and
**publish the generated JSON Schema as a versioned artifact**.

Concretely:

```text
Go structs + tags
        │  generate
        ▼
JSON Schema  ──► committed golden file (reviewable, diffable)
        │
        ├──► MCP tool definitions (the same schema, at runtime)
        └──► TypeScript client types (phase 2, via a standard generator)
```

Rules:

1. The Go definition lives **next to the implementation** — no separate schema
   language to keep in sync.
2. The generated JSON Schema is **committed and reviewed**. A change in Go that
   alters the agent-facing surface shows up as a diff and must be a conscious
   decision.
3. The JSON the consumer sees is **native**, not a translation dialect.
4. Validation limits advertised in the schema must be the same limits the runtime
   enforces.
5. If Go's type system later proves too limiting for the contract, switching the
   *authoring source* to JSON Schema is contained: the boundary does not move.

## Consequences

- One source of truth; no duplicate definitions and no drift by construction.
- Definitions are written in the language the core is written in, which also makes
  them easy for AI agents to edit correctly.
- A small generation step exists and must be part of the build.
- Go's type system constrains expressiveness (optional vs zero value, unions). Where
  it is insufficient, use tags and library features, or a dedicated descriptor type —
  not a second schema language.
- The MCP surface stays under our control: no third-party JSON mapping rules leak
  into what the agent sees.

## Alternatives considered

- **Protocol Buffers as the source.** Genuinely strong: explicit presence, `oneof`
  (which maps well to discriminated tools), formal evolution rules, and
  `protovalidate` for declarative validation. Rejected because MCP requires JSON
  Schema, so choosing protobuf forces a `proto → JSON Schema` step and exposes
  **proto3 JSON mapping quirks** (int64 as string, camelCase renaming, base64 bytes,
  flattened `oneof`) at the most important boundary of the project. It also adds a
  `buf`/`protoc` step and generates verbose Go (pointers for presence) that is less
  pleasant in domain code.
- **gRPC as a transport.** Rejected: browsers cannot speak gRPC — it needs
  grpc-web plus a proxy (Envoy) and still lacks full bidirectional streaming — while
  our UI is a browser app. MCP is JSON-RPC regardless of what we use internally, and
  the core is a single process with no internal network to optimise. Protobuf would
  become the right answer only if a high-volume **internal binary** link appeared
  (for example a frames or LSP channel), which is a local problem, not the project
  contract.
- **A custom IDL.** Perfect fit conceptually (it could express commands, queries,
  errors and events), but it means writing and maintaining a parser plus generators —
  real work and a real source of bugs for a one-person project.
- **JSON Schema as the source.** Rejected *for v1*: authoring JSON Schema by hand is
  verbose, and generating idiomatic Go from it needs a code generator of uncertain
  quality. Kept as the fallback if Go proves too restrictive (see rule 5).
- **OpenAPI / TypeSpec.** OpenAPI targets HTTP APIs and fits MCP poorly; TypeSpec is
  powerful but adds a Node toolchain to a Go project for a problem we do not have.
