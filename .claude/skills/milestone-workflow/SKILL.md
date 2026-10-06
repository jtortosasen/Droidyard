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
| 4 | Implement | DeepSeek through pi (`.pi/roles/implementer.md`), one step per run; the Claude `implementer` (Sonnet) is the fallback | code, left uncommitted; the main session ticks each step with its evidence and commits | every step and every Acceptance item ticked |
| 5 | Verify | per step, at the depth §Verification by risk sets; once more at close, over the whole milestone | a verdict | no blocking findings after the last fix round |
| 6 | Close | the session that ran the workflow | plan status `Done`; its row in `docs/plans/README.md` updated to match; `docs/README.md` §Where the work stands updated; new or amended ADRs indexed in `docs/adr/README.md` | the next milestone is named in §Where the work stands, and `go test ./...` passes with the plans index in step |

## Rules between the steps

- **Resuming.** Read the plan first: its status and ticked steps say where the work is. No plan means step 1. Never implement from the milestone sentence alone.
- **Blocked.** A design question goes to the owner. Once decided, it becomes an ADR or an amendment (`adr` skill), and the planner revises the plan from step 1. Nobody downstream answers it on the owner's behalf.
- **Implementing.** One step at a time, in order. After each step, run its check and the four commands, then tick it with the evidence (`prove-change` skill). Plan changes during implementation are recorded under the plan's Changes, as `milestone-plan` §Following one says.
- **Running the implementer.** `pi -p -a --no-session --model opencode-go/deepseek-v4.1-flash --append-system-prompt .pi/roles/implementer.md "<brief>"` in the step's worktree, in the background with its output redirected to a file; the end of the file is its report. The project policy of `@gotgenes/pi-permission-system` (`.pi/`) denies staging, committing, discarding, `adb` and writes outside the tree; pi runs headless, so an `ask` is a deny. A run that edits nothing in its first 8 minutes is killed and relaunched once (DeepSeek runs have stalled at start). The main session reviews the diff, commits through the hook, and runs the verifiers. If a run fails twice on the same step, that step goes to the Claude `implementer`, and the switch is recorded in the plan's Changes.
- **Briefing.** An implementer or verifier gets its step through `plan-step.sh <plan> <n>` (this skill's directory): the plan header, the step, the acceptance items it proves and the §Changes entries naming it, about a tenth of the plan. It reads the rest of the plan only when the step points there.
- **Verifying.** The verifier gets the step brief and the diff, so it can check the step's Acceptance items against the code and anything the step put in scope that the change silently dropped. Brief it with a timebox: read and reason first, the package tests once, at most two or three `go test -overlay` mutations each narrowed with `-run` and `-timeout 120s`, and the report as soon as it has one. A verifier with no progress for 10 minutes is stopped and relaunched once with a narrower brief; it is not waited on.
- **Testing.** The implementer runs `go test ./...` once when its step is done. The pre-commit hook then tests only the packages a commit touches and their importers; `go test ./...` runs in full at each step's close and at the milestone's close.
- **Committing.** The `.githooks/pre-commit` hook (`git config core.hooksPath .githooks`) refuses a commit the checks would fail. Never bypass it with `--no-verify` to land a step.
- **Parallel steps.** Two steps whose Needs lines do not name each other and that touch disjoint packages may be implemented at once, each implementer in its own worktree (`isolation: "worktree"`), merged in plan order. Steps that share a package, the golden file or the plan text run in sequence.
- **Documents.** During a step, only documents that state the contract (`schema/capabilities.json`, the interface documents, an ADR the step amends) are edited in the same change. Other prose (research notes, status tables, cross-references) is reconciled once, at the milestone's close, in one pass.
- **Delegated decisions.** A decision the main session takes under the owner's standing instruction is labelled `Delegated` where it is recorded (plan, ADR `Amended:` line). The close report lists every one, one line each, so the owner can revert any.
- **Closing.** Report what the implementer and the verifier actually established. An acceptance item proven only in part stays unticked, with a note saying which part was proven. A plan marked `Done` stays in `docs/plans/` as the record.

## Verification by risk

Every step gets its check, the four commands and the main session's review of the diff. On top of that (owner, 2026-10-06: fewer rounds, same bar; DeepSeek writes code only and never reviews):

| The step | Review |
| --- | --- |
| changes what a capability means (a new capability, error code or field, or a changed one's semantics), the security fence, authorization or untrusted marking, concurrency, or process lifetime | the Opus `verifier` alone |
| anything else: description text, an adapter change its tests fully cover, fixtures, a parser, internal plumbing, tests, documents | the main session's review of the diff |
| the milestone's close | the Opus `verifier`, over the Acceptance items |

- **Fix rounds.** Small fixes (a test, a bound, a doc line, a few lines of code) the main session makes itself and checks with the tests; only a blocking finding whose fix is not small goes back to the implementer and then to the reviewer that found it, on the diff only.
- **Reversal evidence.** "Seen failing with the decision reversed" is required for the decisions a blocking finding would be about (correctness, bounds, safety), not for every test.
- **Grouping.** Adjacent steps that share a package and are each small may be implemented, reviewed and run at level 5 together.

When unsure, take the deeper row.

## When not to use it

A fix that changes no contract (a bug inside one package, a test gap, a typo) does not need a plan: make it, prove it (`prove-change`), and run the verifier if it touches a descriptor, an error code or an adapter. If a "small fix" needs more than one step or touches an ADR, it is a milestone after all.
