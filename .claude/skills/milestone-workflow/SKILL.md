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
| 4 | Implement | the Claude `implementer` on Sonnet (Agent tool), one step per run, its effort set by the main session; `ds-implement.sh` (DeepSeek) only when the owner asks to spare the quota | code, left uncommitted; the main session ticks each step with its evidence and commits | every step and every Acceptance item ticked |
| 5 | Verify | per step, at the depth §Verification by risk sets; once more at close, over the whole milestone | a verdict | no blocking findings after the last fix round |
| 6 | Close | the session that ran the workflow | on the branch, one close commit: plan status `Done`; its row in `docs/plans/README.md`; one line in `docs/README.md` §Where the work stands; new ADRs indexed; `docs/roadmap/known-limits.md` rows added or closed. Then the branch merged with `git merge --no-ff`, `main` pushed to `origin`, and the milestone's worktree and branch removed, all without asking (owner, 2026-10-12); the merge commit is the evidence of a merge item, which stays unticked in the record with that note. Nothing else: Done plans, research notes, reviews and ADRs are records, untouched and not reconciled | the next milestone is named in §Where the work stands, and `go test ./...` passes with the plans index in step |

## Rules between the steps

- **Resuming.** Read the plan first: its status and ticked steps say where the work is. No plan means step 1. Never implement from the milestone sentence alone.
- **Blocked.** A design question goes to the owner. Once decided, it becomes an ADR, new or superseding (`adr` skill), and the planner revises the plan from step 1. Nobody downstream answers it on the owner's behalf.
- **Implementing.** One step at a time, in order. After each step, run its check and the four commands, then tick it with the evidence (`prove-change` skill). Plan changes during implementation are recorded under the plan's Changes, as `milestone-plan` §Following one says.
- **Running the implementer.** The Agent tool, `subagent_type: implementer`, `model: sonnet`, `run_in_background: true`; the prompt carries `implementer-run.md`'s rules and the path of the step's brief. Effort (owner, 2026-10-08: the main session chooses): the model's default, no override; `high` for a step §Verification by risk sends to the Opus verifier, or for a step whose first run under-delivered. The main tree for a sequential step, `isolation: "worktree"` for parallel steps (merge with `git diff` / `git apply`, then remove the worktree and its branch). A review's fix round is sent to the same agent with SendMessage. The main session reviews the diff, commits through the hook, and runs the verifiers. `ds-implement.sh <dir> <brief-file> <out-dir>` (DeepSeek through OpenCode Go, `claude -p --agent implementer`, effort `high` by default, `max` with `CLAUDE_CODE_EFFORT_LEVEL=max`) is kept for when the owner asks to spare the Claude quota; run it in the background, never with `&`.
- **Briefing.** An implementer or verifier gets its step through `plan-step.sh <plan> <n>` (this skill's directory): the plan header, the step, the acceptance items it proves and the §Changes entries naming it, about a tenth of the plan. It reads the rest of the plan only when the step points there.
- **Verifying.** The verifier gets the step brief and the diff, so it can check the step's Acceptance items against the code and anything the step put in scope that the change silently dropped. Brief it with a timebox: read and reason first, the package tests once, at most two or three `go test -overlay` mutations each narrowed with `-run` and `-timeout 120s`, and the report as soon as it has one. A verifier with no progress for 10 minutes is stopped and relaunched once with a narrower brief; it is not waited on.
- **Testing.** The implementer runs `go test ./...` once when its step is done. The pre-commit hook then tests only the packages a commit touches and their importers; `go test ./...` runs in full at each step's close and at the milestone's close.
- **Committing.** The `.githooks/pre-commit` hook (`git config core.hooksPath .githooks`) refuses a commit the checks would fail. Never bypass it with `--no-verify` to land a step.
- **Branch per milestone.** At approval the main session creates `m<id>` from `main`; steps commit there through the pre-commit hook; the close merges it with `git merge --no-ff` (one merge commit per milestone), pushes `main` and deletes the branch. A milestone run in its own worktree (a parallel milestone) has its worktree removed at the close; when the session runs inside that worktree, the removal is the session's last act, since it deletes the session's directory. The implementer never switches or merges.
- **Parallel steps.** Two steps whose Needs lines do not name each other and that touch disjoint packages may be implemented at once, each implementer in its own worktree (`isolation: "worktree"`), merged in plan order. Steps that share a package, the golden file or the plan text run in sequence.
- **Documents.** During a step, only the code, `schema/capabilities.json` and the one area document that states what code cannot are edited in the same change; a changed decision is a new superseding ADR. Records (Done plans, research notes, reviews, ADRs) are never reconciled.
- **Delegated decisions.** A decision the main session takes under the owner's standing instruction is labelled `Delegated` where it is recorded (plan, superseding ADR). The close report lists every one, one line each, so the owner can revert any.
- **Measuring.** Each verifier and plan-reviewer finding carries a tag (`[bug]`, `[contract]`, `[safety]`, `[test]`, `[plan]`, `[doc]`, `[nit]`). The plan's Review table, and the close report, give the count per tag and per layer (plan review, step verifier, close verifier, level-5 run), so a layer that finds nothing of value over three milestones can be cut (owner, 2026-10-08).
- **Closing.** Report what the implementer and the verifier actually established. An acceptance item proven only in part stays unticked, with a note saying which part was proven. A plan marked `Done` stays in `docs/plans/` as the record.

## Verification by risk

Every step gets its check, the four commands and the main session's review of the diff. On top of that (owner, 2026-10-06: fewer rounds, same bar; the implementer writes code only and never reviews):

| The step | Review |
| --- | --- |
| changes what a capability means (a new capability, error code or field, or a changed one's semantics), the security fence, authorization or untrusted marking, concurrency, or process lifetime | the Opus `verifier` alone |
| anything else: description text, an adapter change its tests fully cover, fixtures, a parser, internal plumbing, tests, documents | the main session's review of the diff; for a code diff over about 200 lines, also the `code-review` skill at `low` (few, high-confidence findings), in the background |
| the milestone's close | the Opus `verifier`, over the Acceptance items |

- **Fix rounds.** Small fixes (a test, a bound, a doc line, a few lines of code) the main session makes itself and checks with the tests; only a blocking finding whose fix is not small goes back to the implementer and then to the reviewer that found it, on the diff only.
- **Reversal evidence.** "Seen failing with the decision reversed" is required for the decisions a blocking finding would be about (correctness, bounds, safety), not for every test.
- **Grouping.** Adjacent steps that share a package and are each small may be implemented, reviewed and run at level 5 together.

When unsure, take the deeper row.

## When not to use it

A fix that changes no contract (a bug inside one package, a test gap, a typo) does not need a plan: make it, prove it (`prove-change`), and run the verifier if it touches a descriptor, an error code or an adapter. If a "small fix" needs more than one step or touches an ADR, it is a milestone after all.
