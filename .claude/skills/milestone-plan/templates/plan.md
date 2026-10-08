# Plan: Milestone <id> — <title>

- Status: Draft | Blocked | Reviewed | Approved | Done
- Date: YYYY-MM-DD
- Milestone: "<the sentence, quoted>" (`docs/README.md` §Where the work stands)
- Phase: `docs/roadmap/mvp.md` §Phase <n> (in part; see Scope)
- Contract: ADR-00XX §…, ADR-00YY §…; `docs/…/….md` §…

## Scope

| Phase item (deliverable or acceptance) | Here? | If not, where and why |
| --- | --- | --- |
| … | yes / part / no | milestone <id> / §Out of scope / … |

## Starting point

What exists, with paths. Baseline on YYYY-MM-DD: `go vet ./...` <result>, `go test ./...` <result>.

## Acceptance

- [ ] A1. <observable result>. Source: <phase item or ADR clause>. Proven by: step <n>.

## Steps

### 1. <title>

- Builds: <packages and files>
- Implements: ADR-00XX §…
- Check: <test name and the decision it asserts> | <command and expected output>
- Schema: no change | <expected diff of `schema/capabilities.json`>
- Needs: <earlier steps; real tools, or which fake>
- Not here: <what a reader might expect that this step does not do, and why>
- Rests on: <the one fact this step's safety depends on, and the level (1 to 5) its proof needs> | —
- [ ] Done. Evidence: L<n>, <command and decisive output>

## Not in this milestone

- <item>: <where it lands, and the source that puts it there>

## Design questions

Empty unless the plan is `Blocked`.

### Q1. <question>

- Blocks: step <n>
- The documents say: <citation, or "nothing">
- Draft decision: <…>, as a new ADR | an ADR superseding ADR-00XX

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| … | … | spike S<n> / a real run of … |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.
