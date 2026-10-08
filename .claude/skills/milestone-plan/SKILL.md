---
name: milestone-plan
description: Use when writing, revising, reviewing or following an implementation plan for a Droidyard milestone (docs/plans/*.md). Defines what a plan must contain here (pinned scope, ordered steps each with an acceptance check, the ADR clauses each step implements, what it does not do) and provides the template and the map of which document decides what.
---

# Milestone plan

A plan turns the one-sentence milestone in `docs/README.md` §Where the work stands into ordered steps, each proven by a check. It lives at `docs/plans/<id>-<slug>.md` (for example `docs/plans/m1b-daemon-and-mcp-adapter.md`) and is written from `templates/plan.md`. `references/sources.md` says which documents decide each area.

## Writing one

1. **Pin the milestone.** Quote its sentence. Milestones (1, 1b, …) and roadmap phases (0, 1, 2) do not map one to one: milestone 1b is part of Phase 0, which also holds the supervisor, the workspace model and the HTTP skeleton. So list every deliverable and acceptance item of the phase in the Scope table, and mark each as in this milestone, in a named later one, or out of scope. That table is the scope boundary; it is the decision most often left implicit.
2. **Collect the contract.** From `references/sources.md`, list every ADR section and document the milestone touches. Read them in full, following any `Supersedes:` chain to the ADR now in force, and Alternatives considered. For each open item the milestone touches (a row in `docs/roadmap/known-limits.md`), cite the ADR that answers it, or make it a design question.
3. **Establish the starting point from the code.** Name the packages, descriptors and tests that exist, with paths. Run `go vet ./...` and `go test ./...` and record the result with the date.
4. **State the milestone's acceptance.** Each item is observable and traced to a source: a phase acceptance item or an ADR clause. If only part of a phase item is reachable now, say which part.
5. **Decompose into steps** (rules below), then fill in Not in this milestone and Unverified.
6. **Design questions stop the plan.** If any step needs a decision no document makes, write it under Design questions with a draft decision, set the status to `Blocked`, and hand it back. Do not write the steps that depend on it as if it were settled.
7. **Review.** `plan-reviewer` attacks the plan; each finding and its resolution goes in the Review table. One round by default; a second only when the first returned blocking findings, and it checks only those. After that, every blocking finding answered in the plan is enough. The status becomes `Reviewed` then, and `Approved` when the owner (or the main session under the owner's standing instruction) says so.

## Light plan

A milestone that changes no published contract (no schema, error code or capability semantics) and no security, concurrency or process-lifetime behaviour may use a light plan: the header, a Scope list, the steps (each with all six fields the plan test requires: Builds, Implements, Check, Schema, Needs, Not here; one line each) and Acceptance; no Design questions, Unverified or Rests on unless one is real. If a change can be described in one sentence, it needs no plan at all: the main session does it with its check and commits.

## What makes a step

- **One check that can fail.** Name the test (new or existing) and the decision it asserts, or the exact command and its expected output. It fails before the step and would fail if the decision were reversed. "Compiles" and "tests pass" are not checks.
- **Green after every step.** The four commands in `AGENTS.md` §Build and test pass at the end of each step, so the milestone can stop at any step boundary.
- **Ordered by dependency, then by risk.** A step uses only what earlier steps built. Among the rest, the first observable end-to-end result comes as early as possible, and a step that exposes an unverified assumption comes before the steps that rest on it.
- **Schema impact declared.** A step that touches a descriptor or the generator states the expected diff of `schema/capabilities.json`; every other step states "no schema change". An unexpected diff is then detectable.
- **Real or fake, said explicitly.** A check that needs a real `adb`, emulator, Gradle build or MCP client says so and says how it is run; otherwise it is a unit test with a fake, as `nothingInstalled` in `internal/schema/schema_test.go` does. A check against a running daemon names the `verify-daemon` row it runs.
- **The fact it rests on, when there is one.** Where the step's safety rests on a single fact, name it under `Rests on` with the level its proof needs (`prove-change` §Grade what you proved), and aim the Check at that fact. When the Check cannot reach that level, for example a fake where only a real process settles it, put the gap in Unverified now rather than discovering it at the tick.
- **Its own "not here".** Where a reader would reasonably expect something the step does not do, say so and cite why (for example: no idle exit for the daemon, owner decision in `docs/reviews/2026-09-29-ratifications.md`).
- **Sized for one sitting.** If a milestone needs more than about ten steps, propose a split instead. This is a working heuristic, not a rule.
- **A self-contained brief.** The step is what the implementer receives (`plan-step.sh`): it names the files and functions to touch, the behaviour, the tests with what each asserts and how it is seen failing, and the limits (no adb, no schema change). A step the main session would have to rewrite into a brief is not finished.
- **Short.** Aim for about 25 KB. A step says what to build, what proves it and which clause it implements, and cites documents instead of restating them; the Review table keeps one line per finding.

## Following one

The implementer builds the steps in order. When a step's check passes and the four commands are green, tick it and write the evidence on its line, in under about 800 characters: the level reached, the tests that prove it and the decisive output, the mutations seen failing (names only), what stayed unproven. Detail belongs in the commit message and the reviewers' reports, not the plan. If that level is below what `Rests on` needs, the gap stays in Unverified, and any Acceptance item the step proves stays unticked. When a step proves wrong, stop and record the change under Changes before continuing. If the change alters only order or split, continue; if it alters behaviour a document specifies, it is a deviation and needs an ADR (`adr` skill). When the plan and an accepted ADR disagree, the ADR wins and the plan is amended.
