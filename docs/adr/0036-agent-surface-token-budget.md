# ADR-0036: The agent surface's token budget

- Status: Accepted
- Date: 2026-10-08
- Superseded in part by: ADR-0060 (§Decision)
- Supersedes: ADR-0020 §Adapter projections (in part: the text a completed operation renders)
- Supersedes: ADR-0019 §Processes (in part: item 7, the hello gains an optional `tools` field)
- Relates to: ADR-0015 §1, ADR-0025 §Decision, `mcp.md` (§Descriptions, §Server instructions,
  §Results, §What is exposed, §Schemas on the wire), plan m1o Q1
- Decided by the main session under the owner's standing instruction (Delegated,
  2026-10-08, plan m1o Q1); the owner can revert it.

## Context

The owner's brief of 2026-10-07 (item D) asked for an agent surface that spends fewer
tokens. Measured at `1e736e6` (plan m1o §Starting point): `tools/list` is 142,483 B,
66.7 KB of it what a model can read (descriptions 39,227 B, `inputSchema` 21,913 B) and
75,000 B of `outputSchema`; 31 of 34 descriptions break `mcp.md` §Descriptions' own rule
of one or two sentences, which no test enforced; the selector object is inlined, with its
property descriptions, three times in `device_inputText` and `device_tapElement`. A result's
text block is its compact JSON (`adapters/mcp`), so a UI tree or a log read costs the model
every field name of every node or line (`docs/research/daily-use-tokens-2026-10.md` §1, §2).

OpenCode shows the model the text block and not `structuredContent` [obs, tokens note §1.2].
Cursor 2026.10.01 shows the text block too, and at a session's start its `getMcpTools`
call hands the model every tool's description and `inputSchema` (80,301 B for 34 tools),
without `outputSchema`, and the server instructions as the namespace description [obs,
`cursor-agent -p` stream, plan m1o step 1 (b)].

## Decision

1. **Descriptions follow the documented rule, enforced by a test**: at most two sentences
   and 400 characters, with a named list of one-sentence safety exceptions (a repeat repeats
   an effect; `expect` can be vacuous; `gone` needs two reads; `emulator_start`'s cancel;
   `shell_exec`'s reach); property descriptions at most 120 characters; a repeated object
   (the selector) described once per tool, its nested copies bare. The workflow manual moves
   into the server instructions (at most 2,000 characters) and the skill.
2. **Rendering.** A capability may declare a compact text rendering. The MCP text block is
   that rendering, for the tool and through `operation_wait`; `structuredContent` stays the
   full result. Every untrusted-tagged string in a rendering is JSON-quoted through one
   helper, and the untrusted first line stays (ADR-0015 §1). ADR-0020 §Adapter projections'
   "a completed operation renders as the plain result" becomes "as the plain result or its
   declared rendering".
3. **Results stay additive** (`capability-api.md` §Discovery and versioning). `device.logs`'s
   `crashes[]` entries gain `causes` and up to five app `frames`; `maxLines` defaults to 50.
   Folding a crash's lines into its entry and a compact `system.capabilities` happen in the
   renderings (2), not the schema.
4. **Profile.** `droidyard mcp --tools core` lists only the golden path's tools (plan m1o
   D3), carried in the hello as an optional `tools` field (ADR-0019 §Processes item 7);
   `all` stays the default, so the default surface is unchanged and stable for prompt caches.
   A call to a tool outside the profile gets MCP's "tool not found".

## Consequences

- API 0.13.0 (3 adds fields and changes a default; 1, 2 and 4 change no field).
- `mcp.md` §Descriptions, §Server instructions, §Results and §What is exposed state the rule;
  the code and its tests (`internal/schema`, `adapters/mcp`) hold it.
- A client that shows the model `structuredContent` instead of the text gains nothing from 2.
- The safety text that leaves the descriptions lives where it bites: in result `warnings`,
  error `nextStep`s, the instructions and the skill. A client that shows none of these loses
  it; the named exceptions keep the caveats whose absence repeats an action.
- The renderings are a second view of each result to keep true; golden tests over recorded
  results hold them, and the quoting helper is the one place untrusted text is guarded.

## Alternatives considered

- **Drop `outputSchema` from `tools/list`** (75 KB on the wire): about zero model tokens in
  OpenCode and none in Cursor, which leaves it out of what the model reads; and it contradicts ADR-0025 §Decision (schemas passed through unmodified) and
  MCP's rule that `structuredContent` conforms to a declared schema. It wins if a client is
  shown to pay model tokens for it.
- **`$ref`/`$defs` deduplication**: forbidden by `mcp.md` §Schemas on the wire (some clients
  mishandle `$ref`), and 1 gets the same bytes.
- **A separate short `summary` field**, keeping the long text in the schema: two texts to keep
  true, and the manual already has homes (instructions, skill, area documents).
- **`tools/list_changed` progressive disclosure**: a changing list breaks the client's
  prompt-cache prefix, and support is uneven.
- **Removing fields from the build result's schema instead of rendering them away**: a breaking
  change for the coming web UI, for tokens only the model pays.
