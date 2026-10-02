---
name: Milestone workflow
description: Use when starting, resuming or closing a Droidyard milestone ("do milestone 1b", "what is next", "continue the daemon work"), or when implementing from a plan. Says what happens in what order (plan, plan review, owner approval, implementation, verification, close), which agent does each step, and what passes between them.
---

# Milestone workflow

One milestone at a time, and no code before an approved plan. The plan file is the handoff between every step, so a new session can resume from it alone.

| # | Step | Who | Produces | Done when |
| --- | --- | --- | --- | --- |
| 1 | Plan | `planner` | `docs/plans/<id>-<slug>.md`, status `Draft` | the template is complete, or the status is `Blocked` |
| 2 | Review the plan | `plan-reviewer`, launched by the planner | a verdict, recorded in the plan's Review table | verdict `approve`; status `Reviewed` |
| 3 | Approve | the owner | status `Approved` | the owner has read the Scope table, the step list, Not in this milestone and Unverified, and said go |
| 4 | Implement | `implementer`, given the plan path | code; each step ticked with its evidence | every step and every Acceptance item ticked |
| 5 | Verify | `verifier`, launched by the implementer, given the plan path as well as the change | a verdict | no blocking findings after the last fix round |
| 6 | Close | the session that ran the workflow | plan status `Done`; its row in `docs/plans/README.md` updated to match; `docs/README.md` §Where the work stands updated; new or amended ADRs indexed in `docs/adr/README.md` | the next milestone is named in §Where the work stands, and `go test ./...` passes with the plans index in step |

## Rules between the steps

- **Resuming.** Read the plan first: its status and ticked steps say where the work is. No plan means step 1. Never implement from the milestone sentence alone.
- **Blocked.** A design question goes to the owner. Once decided, it becomes an ADR or an amendment (`adr` skill), and the planner revises the plan from step 1. Nobody downstream answers it on the owner's behalf.
- **Implementing.** One step at a time, in order. After each step, run its check and the four commands, then tick it with the evidence (`prove-change` skill). Plan changes during implementation are recorded under the plan's Changes, as `milestone-plan` §Following one says.
- **Verifying.** The verifier gets the plan so it can check each Acceptance item against the code, and anything the plan put in scope that the change silently dropped.
- **Closing.** Report what the implementer and the verifier actually established. An acceptance item proven only in part stays unticked, with a note saying which part was proven. A plan marked `Done` stays in `docs/plans/` as the record.

## When not to use it

A fix that changes no contract (a bug inside one package, a test gap, a typo) does not need a plan: make it, prove it (`prove-change`), and run the verifier if it touches a descriptor, an error code or an adapter. If a "small fix" needs more than one step or touches an ADR, it is a milestone after all.
