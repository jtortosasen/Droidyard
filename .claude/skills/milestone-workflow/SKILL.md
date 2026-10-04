---
name: milestone-workflow
description: Use when starting, resuming or closing a Droidyard milestone ("do milestone 1b", "what is next", "continue the daemon work"), or when implementing from a plan. Says what happens in what order (plan, plan review, owner approval, implementation, verification, close), which agent does each step, and what passes between them.
---

# Milestone workflow

One milestone at a time, and no code before an approved plan. The plan file is the handoff between every step, so a new session can resume from it alone.

| # | Step | Who | Produces | Done when |
| --- | --- | --- | --- | --- |
| 1 | Plan | `planner` | `docs/plans/<id>-<slug>.md`, status `Draft` | the template is complete, or the status is `Blocked` |
| 2 | Review the plan | `plan-reviewer`, launched by the planner | a verdict, recorded in the plan's Review table | verdict `approve`, or two rounds done with every blocking finding answered; status `Reviewed` |
| 3 | Approve | the owner, or the main session under the owner's standing instruction to apply the recommended option | status `Approved` | the Scope table, step list, Not in this milestone and Unverified are settled; a delegated approval says so in the plan header |
| 4 | Implement | `implementer`, given the plan path | code; each step ticked with its evidence | every step and every Acceptance item ticked |
| 5 | Verify | per step, at the depth §Verification by risk sets; once more at close, over the whole milestone | a verdict | no blocking findings after the last fix round |
| 6 | Close | the session that ran the workflow | plan status `Done`; its row in `docs/plans/README.md` updated to match; `docs/README.md` §Where the work stands updated; new or amended ADRs indexed in `docs/adr/README.md` | the next milestone is named in §Where the work stands, and `go test ./...` passes with the plans index in step |

## Rules between the steps

- **Resuming.** Read the plan first: its status and ticked steps say where the work is. No plan means step 1. Never implement from the milestone sentence alone.
- **Blocked.** A design question goes to the owner. Once decided, it becomes an ADR or an amendment (`adr` skill), and the planner revises the plan from step 1. Nobody downstream answers it on the owner's behalf.
- **Implementing.** One step at a time, in order. After each step, run its check and the four commands, then tick it with the evidence (`prove-change` skill). Plan changes during implementation are recorded under the plan's Changes, as `milestone-plan` §Following one says.
- **Verifying.** The verifier gets the plan so it can check each Acceptance item against the code, and anything the plan put in scope that the change silently dropped. Brief it with a timebox: read and reason first, the package tests once, at most two or three `go test -overlay` mutations each narrowed with `-run` and `-timeout 120s`, and the report as soon as it has one. Long mutation loops have stalled it.
- **Committing.** The `.githooks/pre-commit` hook (`git config core.hooksPath .githooks`) refuses a commit the checks would fail. Never bypass it with `--no-verify` to land a step.
- **Parallel steps.** Two steps whose Needs lines do not name each other and that touch disjoint packages may be implemented at once, each implementer in its own worktree (`isolation: "worktree"`), merged in plan order. Steps that share a package, the golden file or the plan text run in sequence.
- **Delegated decisions.** A decision the main session takes under the owner's standing instruction is labelled `Delegated` where it is recorded (plan, ADR `Amended:` line). The close report lists every one, one line each, so the owner can revert any.
- **Closing.** Report what the implementer and the verifier actually established. An acceptance item proven only in part stays unticked, with a note saying which part was proven. A plan marked `Done` stays in `docs/plans/` as the record.

## Verification by risk

Every step gets its check, the four commands and the main session's review of the diff. On top of that:

| The step | Review |
| --- | --- |
| changes `schema/capabilities.json`, an error code, an adapter, the security fence, authorization or untrusted marking, concurrency or process lifetime | Opus `verifier` and the DeepSeek cross-check, in parallel |
| anything else: fixtures, a parser, internal plumbing, tests, documents | the DeepSeek cross-check alone |
| the milestone's close | Opus `verifier` and the DeepSeek cross-check, over the Acceptance items |

When unsure, take the deeper row. A step whose review found a blocker gets its fix round reviewed by the reviewer that found it, on the diff only.

## When not to use it

A fix that changes no contract (a bug inside one package, a test gap, a typo) does not need a plan: make it, prove it (`prove-change`), and run the verifier if it touches a descriptor, an error code or an adapter. If a "small fix" needs more than one step or touches an ADR, it is a milestone after all.
