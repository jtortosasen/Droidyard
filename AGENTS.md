# Working in this repository

**Droidyard** — a lean, AI-agnostic Android development runtime. One Go daemon
(`droidyard`) exposes a single **capability API** to two consumers: a lightweight web
UI and AI coding agents over MCP. There is deliberately **no in-house agent harness**;
the agent loop belongs to the external assistant.

## Read these first, in this order

1. **`docs/README.md`** — the documentation map, the status of every document, and
   §Where the work stands. Start here.
2. **`docs/roadmap/mvp.md`** — what the first version is, its phases, its definition of
   done, its metric, and what is explicitly out of scope.
3. **`docs/architecture/capability-api.md`** — the contract everything else hangs off.
4. **`docs/adr/README.md`** — 35 decisions, all accepted. **Before proposing a change
   to something an ADR covers, read that ADR: it records what was rejected and why.**
   A rejected option with a recorded reason is not an invitation to re-propose it.
5. **`docs/reviews/2026-09-29-pre-implementation-review.md`** and
   **`docs/reviews/2026-09-29-ratifications.md`** — the findings that shaped the
   current design, and the nineteen decisions the owner made. Both are closed records.

## Starting a milestone

`docs/plans/` holds one plan per milestone, written before the code. The
`milestone-workflow` skill says what happens in what order — plan, plan review, owner
approval, implementation, verification, close — and which agent does each step. If a
plan exists for the milestone you are about to work on, **follow it**; if the work
forces a departure from it, say so and update the plan rather than drifting from it
silently.

## Agents and models

The project runs on Claude Code. Agents are in `.claude/agents/`, skills in
`.claude/skills/`, and their tool limits in each agent's frontmatter: `disallowedTools`,
plus `PreToolUse` hooks in `.claude/hooks/` (`guard-bash.sh observe` is an allowlist of
observation commands, `readonly` a denylist of tree-changing ones;
`guard-plans-only.sh` keeps the planner to `docs/plans/*.md`). The `milestone-workflow`
skill says who does what.

- **Main session** (Opus): runs the workflow, briefs the implementer one step at a time,
  reviews the diff. Review depth follows the step's risk (`milestone-workflow`
  §Verification by risk): the Opus verifier for contract, security and concurrency
  changes and at a milestone's close; the main session's diff review otherwise.
- **Implementer: the Claude `implementer` on Sonnet** (owner's choice, 2026-10-08, after
  milestone 1p ran on it; replaces DeepSeek, the choice of 2026-10-04): launched by the main
  session through the Agent tool (`subagent_type: implementer`, `model: sonnet`), in the
  background, one plan step per run, in the main tree (`isolation: "worktree"` only for
  parallel steps). **The main session sets each run's reasoning effort** (owner,
  2026-10-08): `medium` by default, `high` for a step the Opus verifier reviews (contract,
  security, untrusted text, concurrency, process lifetime). A review's fix round goes to the
  same agent (SendMessage). Limits: the `implementer-run.md` rules in its prompt (no
  verifier or other agent, no staging or committing, no adb, emulator or device). The main
  session reviews the diff and commits. `ds-implement.sh` (DeepSeek through OpenCode Go;
  effort `high` unless `CLAUDE_CODE_EFFORT_LEVEL` says otherwise) stays usable when the
  owner asks to spare the Claude quota.
- **`planner`, `plan-reviewer`, `verifier`** (Opus).
- **The implementer only writes code.** It implements; it does not review, verify or plan
  (owner, 2026-10-06). Every check is Opus's.

## Conventions

- **Documents and commit messages are in English.**
- **Decisions go to ADRs.** One file per decision, numbered, with Context / Decision /
  Consequences / Alternatives considered. Never bury a decision in prose. Never
  silently rewrite an accepted ADR — add an `Amended:` line saying what changed.
- **Verify what can be verified.** The house rule, learned the hard way: a claim that
  can be checked should be checked, and an assumption must be *labelled* as an
  assumption. `docs/research/spike-s1-error-formats.md` is the worked example — it
  states plainly what it observed and what it did not.
- **Density over volume.** A tight document beats a thorough one. The corpus is already
  large; do not add to it without a reason.
- **Do not overstate.** If the second spike has not run, the document says so.

## Build and test

`.githooks/pre-commit` refuses a failing commit: `go build ./...`, then `gofmt`, `go vet`
and `go test` on the packages the commit touches and their importers (a document-only
commit runs the document checks). Enable it once per clone with
`git config core.hooksPath .githooks`. The full `go test ./...` runs at each step's close.

```sh
go build ./...
go vet ./...
gofmt -l .          # must print nothing
go test ./...
```

After changing any descriptor, regenerate the published schema and review the diff:

```sh
go test ./internal/schema -update
```

**`schema/capabilities.json` is the contract consumers see.** A change there is a
change to the published API, and it should be a conscious one.

## Scope discipline

The MVP is deliberately narrow. `docs/roadmap/mvp.md` §Out of scope lists what was
deferred and why — including code intelligence, the live screen stream, OCR, SDK
provisioning, multi-device concurrency and Windows. Do not add what the MVP does not
need; if you think something should be added, write an ADR proposing it.

## Before touching the Gradle sidecar

Read `docs/subsystems/gradle.md` §Spikes before the sidecar is written and
`docs/research/spike-s1-error-formats.md` first. The second spike run has not happened,
and it is what settles whether the Tooling API sidecar keeps its justification: on a
real project (Kotlin plugin 2.0.21), compiler errors exist only as console text, so the
console extractor is the primary path today, not a fallback.
