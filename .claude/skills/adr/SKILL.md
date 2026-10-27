---
name: adr
description: Use when a Droidyard design decision must be recorded or changed. That happens when a plan hits a question the documents do not decide, when implementation forces a deviation from an accepted ADR, or when new evidence challenges one. Covers when to write a new ADR and when to supersede, the format and `Supersedes:` lines, and how to write Alternatives considered so they are worth reading.
---

# Writing or superseding an ADR

The format is in `docs/adr/README.md` §Format. Read two ADRs before you write one: ADR-0020, which has dense Alternatives, and ADR-0017, whose `Amended:` lines show what a superseding ADR now records instead (they are historical: no ADR gets a new one).

## When

An ADR costs a number, a file, an index row and a review; spend it only where reversing the decision later would be expensive (owner, 2026-10-27: too many ADRs slowed every change).

- **Write an ADR** for: a new subsystem, long-lived process or third-party dependency; the security fence, authorization or the untrusted-text model; an on-disk format or path other tools or versions read; a principle the whole capability contract follows (naming, the error shape, versioning); or when the owner asks for one.
- **Not an ADR**: a new capability, parameter, field, error code or default; a UI layout; a bound or a working value; a change to a detail an older ADR set. The descriptor, `schema/capabilities.json`, the area document and the commit message are its record. A commit that departs from an ADR says `Changes ADR-NNNN §X: <why>` in its message; no file changes.
- **Supersede** only when the new decision itself meets the first bullet. Write a new short ADR with `Supersedes: ADR-NNNN` (whole) or `Supersedes: ADR-NNNN §Section` (in part). The old file keeps its text and gains one header line (see Superseding one).
- **Never edit an accepted ADR.** No new `Amended:` line and no rewritten body; the existing `Amended:` lines are history. The current rule lives in the code and the area document, which the new ADR's Consequences name.

An agent writes `Status: Proposed`; only the owner accepts. A proposed ADR binds nothing, so no code is built on it until it is accepted.

## Writing it

1. **Number.** Reserve it with `board.go adr <lane>` (`work-board`): other lanes hold numbers `docs/adr/` does not show yet, so the index may have gaps. Use `NNNN-short-title.md`, and add a row to the index in `docs/adr/README.md`.
2. **Context.** The forces and the evidence, with sources, and what breaks or stays undecided without a decision. Label every assumption; cite what was observed, as `docs/research/spike-s1-error-formats.md` does.
3. **Decision.** Specific enough that code can be checked against it: names, codes, numbers, states. Label working defaults as such, as ADR-0020 does with "20 s (working default, configurable)".
4. **Consequences.** Include the costs, and list every document that must change to follow the decision. That list is a checklist for the same change, not a to-do for later.
5. **Alternatives considered.** Each alternative is one someone would actually propose, including the one you expect to be asked about. For each, say what it is and the concrete way it fails ("breaks on ~60 s client timeouts, and a timeout cancels the build"). "More complex" is not a reason. Where it applies, add what would make the alternative win (ADR-0020 keeps MCP Tasks "when clients catch up"): that tells a future reader when to reopen the question and when not to.

## Superseding one

- New ADR: `Supersedes: ADR-NNNN` or `Supersedes: ADR-NNNN §Section`, with the evidence that forced the change and Alternatives considered, including why the old decision lost.
- Old ADR, the only edit: whole supersession sets Status to `Superseded by ADR-XXXX` (the index's Status column shows it); partial supersession adds `- Superseded in part by: ADR-XXXX (§Section)` to the header. `internal/doccheck` checks the pointer both ways.
- Update the code and the one area document that states the rule, in the same change. Do not hunt for duplicates of the rule: there should be none, and a duplicate that must stay is checked by `internal/doccheck`.

## From a plan

The planner cannot write ADR files. It puts the draft (question, what the documents say, the proposed decision) under the plan's Design questions, and the answer stays there as its record. Only an answer that meets §When becomes an ADR, which the session that owns the workflow writes.
