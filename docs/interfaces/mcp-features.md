# Interface: MCP features beyond tools

What the MCP server exposes or uses **besides** tool calls, with a verdict on each.
The tool surface itself is in
[`mcp-tool-surface.md`](mcp-tool-surface.md).

> Status: draft. This document records decisions and candidates so they are not
> re-litigated. Accepted items become ADRs; candidates are marked as such.

## Summary

| Feature | What it does | Verdict |
| --- | --- | --- |
| **Elicitation** | The server asks the **user**, through the client | ⭐ **Candidate — highest value** |
| **Resources** (+ `subscribe`) | The client reads server-held data as context | 🟡 Candidate, after the MVP (`artifact_read` covers the MVP) |
| **Prompts** | Server-provided prompt templates | 🟡 Candidate, only where needed |
| **Logging** (`notifications/message`) | Structured log messages to the client | 🟢 Cheap, do it |
| **Roots** | The client declares which directories are in scope | 🟢 Cheap, optional input that can only narrow |
| **Progress** | Progress notifications for long operations | ✅ Used during bounded waits |
| **Sampling** | The server asks the **client's model** to generate | ❌ Rejected |
| **Assistant-side hooks** | `PreToolUse`/`SessionStart`-style extension points | ❌ Not part of MCP; not adopted |

## 1. Elicitation — the one that closes real open questions

The server can ask the client for user input: a confirmation, or a small form. It
solves problems we already have written down and unanswered:

| Our open problem | What elicitation provides | Fallback without it |
| --- | --- | --- |
| **SDK licence consent** ([`subsystems/sdk.md`](../subsystems/sdk.md)) | The server asks; the human accepts | An explicit capability call, and an error that names it |
| **Confirming destructive operations** | A real confirmation step, not a hint | `destructiveHint` only, which clients may ignore |
| **Resolving ambiguity** | "Which of these two devices?" / "Which variant?" | An error that names the candidates and requires an explicit parameter |

Rules:

1. **Always optional.** Elicitation is an *improvement when available*, never a
   requirement. Every capability that could use it must work without it.
2. **Support varies** by client and version. Detect, and degrade to the fallback
   column above.
3. Not in the MVP core loop: the golden path needs none of it. It becomes worth
   building with the SDK provisioning stage, where the licence question actually
   arises.
4. **Never a policy channel.** Elicitation does not enable devices or `shell.exec`,
   and it does not trust a project root. A client in auto-approve mode would let the
   agent grant itself access
   ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §6).

This also strengthens [ADR-0015](../adr/0015-untrusted-data-authorization.md): today,
destructive operations are only *hinted* at. Elicitation is the first mechanism that
makes the confirmation real.

## 2. Resources — a natural fit with our artifacts

MCP resources let the client read data the server holds, and `resources/subscribe`
notifies it when that data changes.

Artifacts are **opaque, URI-shaped references** (`droidyard://artifact/<id>`,
[ADR-0012](../adr/0012-visual-artifacts-as-refs.md)). In the MVP, agents read them
through the `artifact_read` tool, and screenshots arrive inlined as images
([`mcp-tool-surface.md`](mcp-tool-surface.md) §Images and artifacts). A tool is needed
either way: many clients put resources under the user's control, not the model's.

Resources would add:

- `resources/read` on the same URI, as a second projection of `artifact.read`, for
  clients that attach resources as context;
- a live `logcat` as a subscribable resource, instead of a bounded follow call;
- `build_lastResult` read as context without a tool call.

Not for the MVP: `artifact_read` already closes the gap for agents, and the UI has its
own route.

## 3. Prompts — a second channel for the same knowledge

MCP prompts are server-provided templates. They overlap with the Agent Skills we
adopted in [ADR-0011](../adr/0011-skills-agent-skills-standard.md): the same
"how to run the build → run → inspect → debug loop" guidance, delivered two ways.

- A client that supports prompts can pull the playbook from the server; a client that
  supports skills installs a file.
- **The risk is drift.** If we ship both, they are **generated from one source** —
  exactly the discipline we already apply to the tool table
  ([`mcp-tool-surface.md`](mcp-tool-surface.md)).
- Verdict: only if we find a client we care about that supports prompts but not
  skills. Otherwise the skills format already covers the need.

## 4. Logging notifications

`notifications/message` carries structured, levelled log messages to the client. It
complements `stderr` (which stays the transport-safe default) and lets clients surface
what the core is doing without scraping text.

Cheap, no design conflict. Do it when convenient.

## 5. Roots

The client declares which directories are in scope. Roots are an **optional input
that can only narrow**: a root the client does not declare may be refused, but a
declared root is never trusted because of it
([ADR-0015](../adr/0015-untrusted-data-authorization.md) §5). They are never the
session's binding source either: binding comes from `--root` or the cwd, so it is
deterministic and visible in the server config
([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)).

## 6. Sampling — rejected

Sampling lets the server ask the client's model to generate something. It sounds
attractive and is the wrong direction:

- It moves intelligence into the core's workflow and edges us toward being a harness,
  which [ADR-0004](../adr/0004-no-own-harness.md) rejects. The agent already has the
  model; our job is to give it data.
- It adds coupling to a feature with uneven support, for work the agent does better
  anyway. The classic example — summarising a huge log before returning it — is
  better done by the agent, which has the model and the context.

## 7. Progress

Sent during any wait when the client supplied a `progressToken`. It is a nicety, never
relied on to keep a call alive: long calls return a running operation after a bounded
wait ([ADR-0020](../adr/0020-operation-contract.md),
[`mcp-tool-surface.md`](mcp-tool-surface.md) §Long-running work).

## 8. Assistant-side hooks — not adopted

`PreToolUse`, `PostToolUse`, `SessionStart`-style hooks belong to individual
assistants and are **not part of MCP**. They are:

- **per-assistant** (Claude's hooks are not Cursor's or Codex's), which is the coupling
  [ADR-0004](../adr/0004-no-own-harness.md) forbids as a dependency;
- **largely redundant** with what we already have: "device data is not instructions"
  lives in `instructions`; "open the workspace" is a call the agent makes; "build after
  editing" should not be automatic.

They may return later as **optional adapters** for a specific assistant, if a real
need appears. Our internal equivalent already exists: the authorization boundary
re-checked immediately before execution.

## Cost discipline

Every feature is surface to implement, test and degrade gracefully. A feature
supported badly is worse than one not advertised at all. So each item above is either
implemented **with** a documented degradation path, or not advertised.

## Transports

The MVP transport is stdio, through the shim
([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)). Streamable HTTP, when
added, is another adapter on the daemon's loopback listener. It inherits the Host,
Origin and loopback rules of
[`http-websocket.md`](http-websocket.md) §Security baseline, which the MCP
specification also requires for that transport.

## Effect on existing documents

- [`subsystems/sdk.md`](../subsystems/sdk.md): the licence-consent question has a
  candidate answer (elicitation) with an explicit fallback.
- [`mcp-tool-surface.md`](mcp-tool-surface.md): destructive annotations gain a real
  counterpart where the client supports elicitation.
- [ADR-0012](../adr/0012-visual-artifacts-as-refs.md): artifact identifiers are
  URI-shaped, so resources become a second projection of `artifact.read`.

## Open questions

- Which clients we care about actually support elicitation, and how they degrade.
- Whether prompts are worth dual-shipping alongside skills.
- When resources become worth the surface — after the MVP, driven by the agent side
  rather than the UI.
- Whether elicitation should be adopted as an ADR (it is a decision with real
  consequences if it becomes load-bearing).
