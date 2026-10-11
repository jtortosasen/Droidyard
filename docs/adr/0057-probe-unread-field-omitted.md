# ADR-0057: A probe field that could not be read is omitted, with a note

- Status: Accepted
- Date: 2026-10-11
- Supersedes: ADR-0023 §Decision (in part: item 2's "a failure makes it `unknown`")
- Relates to: ADR-0017; plan m1h D6; plan m2h §Audit ledger
- Decided by the main session under the owner's standing instruction (apply the recommended
  option), Delegated. Milestone 1h shipped it as D6, marked "a departure: the owner should
  see it"; ADR-0023's 1h `Amended:` line did not record it, and the m2h audit found it.

## Context

ADR-0023 §Decision 2 isolates each probe read: "a failure makes it `unknown` and never fails
the fetch". Most per-project fields are numbers, version strings or lists, where a literal
`"unknown"` would be a value of the wrong type that every consumer must test for; and a bare
`unknown` says nothing about why.

## Decision

1. A per-project field the probe could not read is omitted, and a note
   `"<project path>: <field>: <exception class>: <message>"` is added (at most 50 notes per
   project, each at most 512 characters); `probe.status` is `partial`.
2. AGP and KGP, which are build-wide, still read `"unknown"` when no project reports them.
3. A failed read never fails the fetch (unchanged).

## Consequences

- The rule lives in the probe (`sidecar/probe/`, `ProbeState`) and `gradle.md` §Project
  model; `sdk.requirements` answers `complete: false` when a field it needs is missing.
- A consumer tells "not read" from "absent in the project" only by the notes.

## Alternatives considered

- **`"unknown"` in every field** (ADR-0023's text): a string where a number or a list is
  declared, so the schema would lie or every field would become a union.
- **Fail the fetch**: one odd module would hide the whole model, which ADR-0023 rejects.
