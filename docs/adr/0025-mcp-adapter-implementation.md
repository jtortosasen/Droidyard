# ADR-0025: MCP adapter implementation — mark3labs/mcp-go

- Status: Accepted
- Date: 2026-09-30
- Amended: 2026-09-30 — criterion 7's role made explicit: it is recorded for the
  owner, not a gate at milestone 1b; criteria 1 to 6 are the gate.
- Amended: 2026-09-30 (second pass) — references to the plan's step numbers dropped,
  because the plan reordered its steps; no decision changed.

## Context

The MCP adapter runs inside the daemon; `droidyard mcp` is a byte relay over the Unix
socket, and one shim connection is one MCP session
([ADR-0019](0019-process-topology-daemon-and-shims.md) §2, §Sessions). The projection is
already fixed: tool names and schemas, annotations, server instructions, results and
errors by [`mcp-tool-surface.md`](../interfaces/mcp-tool-surface.md); the bounded wait,
`operation_wait` and `operation_cancel` by
[ADR-0020](0020-operation-contract.md) §Adapter projections; the schemas themselves are
generated from the Go registry and published as a golden file
([ADR-0016](0016-capability-schema-in-go.md)).

No document chose an implementation. `docs/README.md` §Where the work stands, and
`docs/reviews/2026-09-29-session-retrospective.md` §Carried forward, recorded that as
the decision blocking milestone 1b; the milestone plan raised it as its Q1, and the
owner decided on 2026-09-30.

What the implementation has to allow, beyond the frozen projection:

1. our generated `inputSchema` and `outputSchema`, passed through unmodified;
2. an `isError` result with **no** `structuredContent`
   ([ADR-0020](0020-operation-contract.md) §Adapter projections);
3. `_meta` on a result — the untrusted mark, from the device slice on
   ([ADR-0015](0015-untrusted-data-authorization.md) §1);
4. a session served over the **daemon's end of the socket**, not the process's stdio;
5. inside a tool handler, telling a `notifications/cancelled` for *that request* from
   the connection closing — the first cancels the operation, the second must not
   ([ADR-0020](0020-operation-contract.md) §Adapter projections;
   [ADR-0019](0019-process-topology-daemon-and-shims.md) §Sessions);
6. `instructions` in the initialize payload;
7. an idle daemon inside its memory budget (under 50 MB RSS,
   [`roadmap/mvp.md`](../roadmap/mvp.md) §Metrics).

Observed on 2026-09-30 by reading the source of `mark3labs/mcp-go` v1.1.1 (level 2:
nothing compiled), criteria 1 to 4 and 6 are reachable:

- `mcp.NewToolWithRawSchema(name, description, schema json.RawMessage)` and
  `mcp.WithRawOutputSchema(schema json.RawMessage)` take the schemas as bytes
  (`mcp/tools.go`);
- `mcp.CallToolResult` carries `Content`, `StructuredContent` — with a
  `RawStructuredContent` byte path — `IsError` and `Meta *Meta` (`json:"_meta"`)
  (`mcp/tools.go`);
- `(*server.StdioServer).Listen(ctx, stdin io.Reader, stdout io.Writer)` takes both
  streams as parameters, so it can run over the socket's ends (`server/stdio.go`);
- `server.WithInstructions` fills the initialize payload (`server/server.go`), and
  `notifications/cancelled` cancels the per-request handler context
  (`server/request_handler.go`).

Criterion 5 is documented for neither candidate: the notification and the stream
closing both surface in the library's context handling, and whether the adapter can
tell them apart through the public API is the fact the milestone plan gates on.

## Decision

**Use `github.com/mark3labs/mcp-go`, pinned in `go.mod`, through its raw schema
helpers.** Concretely:

- Tools are built with `mcp.NewToolWithRawSchema` and `mcp.WithRawOutputSchema` from
  the bytes `internal/schema` generates. The typed helpers that infer schemas from Go
  handler types are **not** used ([ADR-0016](0016-capability-schema-in-go.md)
  §Consequences: no third-party mapping rules leak into what the agent sees).
- The adapter constructs every result itself: `Content` (the text rendering),
  `RawStructuredContent` (the capability's result bytes; absent on errors),
  `IsError`, `Meta`. The library does not build error results for it.
- The session runs on the daemon's socket:
  `NewStdioServer(server).Listen(ctx, connReader, connWriter)`, with the shim still
  relaying bytes. Newline-delimited JSON is both the MCP stdio framing and the socket
  framing.
- `WithInstructions` carries the block of
  [`mcp-tool-surface.md`](../interfaces/mcp-tool-surface.md) §Server instructions.
- Upgrades are deliberate: bumping the module runs the milestone's transcript tests
  first.

**Acceptance gate.** The milestone 1b plan tests criteria 1 to 6 on a real socket
and a real MCP harness; criterion 7 is **recorded**, not asserted — the 50 MB budget
belongs to the MVP (§Metrics, §Definition of done), and an over-budget measurement
reaches the owner as evidence rather than failing this milestone. If the library fails
one of criteria 1 to 6, milestone 1b stops there and the replacement is a new owner
decision recorded as an amendment to this ADR; no fallback is pre-authorised here.

## Consequences

- New module requirements at v1.1.1 (`go.mod`): `github.com/google/jsonschema-go`,
  `github.com/google/uuid`, `github.com/rogpeppe/go-internal`,
  `github.com/santhosh-tekuri/jsonschema/v6`, `github.com/spf13/cast`,
  `github.com/yosida95/uritemplate/v3`. How many are linked into the shipped binary
  is measured during implementation, not assumed. The module is MIT-licensed.
- The library owns protocol-version negotiation and the wire details; a change in the
  specification moves with its releases. The transcript tests are the guard.
- If criterion 5 is reachable only by working around the library's stream handling,
  the adapter needs a thin layer of its own — which the raw-API rule above keeps
  possible.
- The adapter stays inside `adapters/` and imports `capability`, `event` and
  `artifact` only, never `domain/*`
  ([core-runtime](../architecture/core-runtime.md) §Adapter boundary).
- Documents changed with this decision: `docs/README.md` §Where the work stands (the
  blocker sentence). The milestone 1b plan is revised separately by the planner.

## Alternatives considered

- **The official `github.com/modelcontextprotocol/go-sdk`.** Its raw
  `Server.AddTool` with `json.RawMessage` schemas plus an `IOTransport` over an
  arbitrary stream meets criteria 1, 2, 3, 4 and 6 on paper (documentation read,
  2026-09-30). It was not chosen; its requirement set is heavier — `go.mod` at
  v1.8.0 adds `golang.org/x/oauth2`, `github.com/golang-jwt/jwt/v5`,
  `github.com/segmentio/encoding` and `golang.org/x/time` as direct requirements
  over mark3labs's set. It wins if mark3labs fails a criterion that it passes.
- **A hand-written JSON-RPC adapter.** No dependency, and full control of criterion
  5, at the cost of tracking a specification that was still adding protocol pieces
  during 2026 and negotiating it against Cursor and OpenCode ourselves while spike
  S2 has not run. It wins if both libraries fail criterion 5 or the memory budget.
- **Schema inference from Go handler types (either library's typed API).** Rejected
  by [ADR-0016](0016-capability-schema-in-go.md) §Consequences: the published schema
  would be the library's mapping of our types rather than the committed golden
  surface.
