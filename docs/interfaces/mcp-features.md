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
| **Resources** (+ `subscribe`) | The client reads server-held data as context | 🟡 Candidate, after the MVP |
| **Prompts** | Server-provided prompt templates | 🟡 Candidate, only where needed |
| **Logging** (`notifications/message`) | Structured log messages to the client | 🟢 Cheap, do it |
| **Roots** | The client declares which directories are in scope | 🟢 Cheap, optional input |
| **Progress** | Progress notifications for long operations | ✅ Already used |
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

This also strengthens [ADR-0015](../adr/0015-untrusted-data-authorization.md): today,
destructive operations are only *hinted* at. Elicitation is the first mechanism that
makes the confirmation real.

## 2. Resources — a natural fit with our artifacts

MCP resources let the client read data the server holds, and `resources/subscribe`
notifies it when that data changes.

We already decided that screenshots and logs are **opaque references**
([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)). Those references map directly
to resource URIs:

- `device_screenshot` returns a reference; a client that supports resources can read
  it natively instead of receiving base64 inside a tool result.
- A live `logcat` becomes a subscribable resource instead of a bounded follow call.
- `build_lastResult` could be read as context without a tool call.

**Design constraint this creates now, cheaply:** artifact identifiers should be
**URI-shaped from the start** (e.g. `mydev://artifact/<id>`), so that exposing them as
resources later is a mapping rather than a redesign.

Not for the MVP: the UI already has WebSocket, and this is value for the agent side
only.

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

The client declares which directories are in scope. Useful as an **optional input** to
the policy of allowed project roots ([ADR-0015](../adr/0015-untrusted-data-authorization.md))
— never as the only source, because policy must be live-configurable and independent
of any one client.

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

Already part of the design: long calls stream progress notifications and return their
result ([`mcp-tool-surface.md`](mcp-tool-surface.md)).

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

## Effect on existing documents

- [`subsystems/sdk.md`](../subsystems/sdk.md): the licence-consent question has a
  candidate answer (elicitation) with an explicit fallback.
- [`mcp-tool-surface.md`](mcp-tool-surface.md): destructive annotations gain a real
  counterpart where the client supports elicitation.
- [ADR-0012](../adr/0012-visual-artifacts-as-refs.md): artifact identifiers are
  URI-shaped so they can become resources without a redesign.

## Open questions

- Which clients we care about actually support elicitation, and how they degrade.
- Whether prompts are worth dual-shipping alongside skills.
- When resources become worth the surface — after the MVP, driven by the agent side
  rather than the UI.
- Whether elicitation should be adopted as an ADR (it is a decision with real
  consequences if it becomes load-bearing).
