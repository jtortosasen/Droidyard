---
name: ADR
description: Use when a Droidyard design decision must be recorded or changed. That happens when a plan hits a question the documents do not decide, when implementation forces a deviation from an accepted ADR, or when new evidence challenges one. Covers when to write a new ADR and when to amend, the format and `Amended:` line, and how to write Alternatives considered so they are worth reading.
---

# Writing or amending an ADR

The format is in `docs/adr/README.md` §Format. Read two ADRs before you write one: ADR-0020, which has dense Alternatives and a precise `Amended:` line, and ADR-0017, which corrects its own rationale after new evidence without hiding the original.

## When

- **Write an ADR** for a decision that could reasonably have gone another way and that code or other documents will depend on: a new capability or error-code prefix, a new third-party dependency that shapes a contract, a file format or path, a default a consumer observes. A private implementation detail that is cheap to change is not an ADR.
- **Amend** when the decision stands but its details, scope or evidence change. Add an `Amended:` line in the header and edit the body.
- **Supersede** when the decision itself is reversed. Write a new ADR with `Supersedes:`, set the old one to `Superseded by ADR-XXXX`, and keep the old file.
- **Never rewrite silently.** Every substantive edit to an accepted ADR adds an `Amended:` line, one per pass (ADR-0017 has two).

An agent writes `Status: Proposed`; only the owner accepts. A proposed ADR binds nothing, so no code is built on it until it is accepted.

## Writing it

1. **Number.** Take the next free number in `docs/adr/`, use `NNNN-short-title.md`, and add a row to the index in `docs/adr/README.md`.
2. **Context.** The forces and the evidence, with sources, and what breaks or stays undecided without a decision. Label every assumption; cite what was observed, as `docs/research/spike-s1-error-formats.md` does.
3. **Decision.** Specific enough that code can be checked against it: names, codes, numbers, states. Label working defaults as such, as ADR-0020 does with "20 s (working default, configurable)".
4. **Consequences.** Include the costs, and list every document that must change to follow the decision. That list is a checklist for the same change, not a to-do for later.
5. **Alternatives considered.** Each alternative is one someone would actually propose, including the one you expect to be asked about. For each, say what it is and the concrete way it fails ("breaks on ~60 s client timeouts, and a timeout cancels the build"). "More complex" is not a reason. Where it applies, add what would make the alternative win (ADR-0020 keeps MCP Tasks "when clients catch up"): that tells a future reader when to reopen the question and when not to.

## Amending one

- Header line: `- Amended: YYYY-MM-DD — <what changed, concretely>`, citing the evidence that forced it.
- If the change reverses a claim someone may already have acted on, keep the old text visible with a dated correction, as ADR-0017 §Decision does. Otherwise edit in place.
- Search `docs/` for every restatement of what changed, including the tables in `capability-api.md`, `mcp-tool-surface.md` and `roadmap/mvp.md`, and update them in the same change. A fix to one document that contradicts another is the failure the verifier finds most often.

## From a plan

The planner cannot write ADR files. It puts the draft (question, what the documents say, the proposed decision) under the plan's Design questions. The session that owns the workflow turns it into a `Proposed` ADR for the owner.
