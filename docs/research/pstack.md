# Research: `pstack` (poteto)

- Repo: <https://github.com/cursor/plugins/tree/main/pstack>
- License: MIT (`LICENSE`, `.cursor-plugin/plugin.json`)
- Form: a Cursor plugin: markdown skills and agents, plus TypeScript and shell helpers
- Analyzed: 2026-09-29 (version observed: 0.15.5, `.cursor-plugin/plugin.json`)
- Read over HTTP (GitHub tree API, `raw.githubusercontent.com`). There is no local
  clone. Paths below are relative to `pstack/`.

> Status: draft. This is process research, not product research. It concerns
> `.opencode/agents/`, `AGENTS.md` and how Droidyard verifies its own work. It does not
> touch the capability API, and it proposes no ADR.
>
> Droidyard's process layer changed while this was being written. At the start,
> `.opencode/` held two agents (`implementer` and `verifier`) and no skills. By the time
> of writing it held:
>
> - four agents: `planner`, `plan-reviewer`, `implementer` and `verifier`;
> - four skills: `adr`, `milestone-plan`, `milestone-workflow` and `prove-change`.
>
> This document compares pstack with that later state. `implementer.md` and
> `verifier.md` have since been given a line each about the plan files, so they now
> refer to them.

## What it is

pstack packages one engineer's working method (Lauren Tan, "poteto") for Cursor
agents. The README makes two claims. The first is quality: "pstack helps you write
less, but higher quality code". The second is "fearless parallelism": if one agent
can be trusted to write verifiable code, you can run many of them. It also states a
premise that shapes everything else: "i don't believe in planning. the best spec is
code" (`README.md` §why are there no planning skills). Droidyard makes the opposite bet,
a closed design corpus written before the code. **pstack's process is built to stand in
for a written design, not to sit beside one.** Read every mechanism below with that
in mind.

### What was read

- **Read in full.**
  - Top level: `README.md`, both files in `agents/`, `plugin.json`.
  - Skills: `poteto-mode`, `architect` with its three references, `interrogate` with
    its four references, `blast-radius`, `create-verification-skill` with the
    feature-map example (`README.md`, `search.md`), `maintain-verification-skill`,
    `show-me-your-work` with its template and `log.sh`, `figure-it-out`, `tdd`,
    `reflect`, `how`, `arena`, `swarm`, `no-comments`, and
    `why/references/epistemics.md`.
  - 17 of the 23 principles: attack-the-premise, boundary-discipline, build-the-lever,
    encode-lessons-in-structure, exhaust-the-design-space, fix-root-causes,
    foundational-thinking, guard-the-context-window, laziness-protocol,
    make-operations-idempotent, minimize-reader-load, model-the-domain,
    never-block-on-the-human, prove-it-works, sequence-verifiable-units,
    subtract-before-you-add, test-behavior-not-implementation.
  - Playbooks: feature, bug-fix, refactoring, prototype, investigation, session-pickup,
    pause-safely, autonomous-run, authoring-a-skill, eval, multi-phase-plan,
    opening-a-pr.
  - `scripts/check-plan.mjs`, guide pages README and 02 to 10, and from
    `automations/benny/`: `README.md`, `FOR_AGENTS.md` and the three templates.
- **Not read** (known only from the README table or the guide):
  - The remaining six principles.
  - Skills: `automate-me`, `bro`, `make-bot-ui`, `recall`, `setup-pstack`, `teach`,
    `technical-writing`, `typescript-best-practices`, `unslop`, `why/SKILL.md`.
  - Playbooks: babysit, shipping, autopilot-full, autopilot-stack, orchestrate,
    hillclimb, perf-issue, runtime-forensics, trace-forensics, visual-parity,
    worktree-cleanup.
  - The three `SKILL.md` files in benny, `scripts/orch/` and `scripts/watch-pr/` (file
    names and sizes only), and guide page 01.

Every file was reachable.

## The structure

| Layer | Count | Role | Evidence |
| --- | --- | --- | --- |
| Agents | 2 | Give a subagent an identity. Contain no procedure. | `agents/poteto-agent.md` is 641 bytes and says only "Read the `poteto-mode` skill's `SKILL.md` in full before doing any work" |
| Router skill | 1 | Trigger rules, principles index, autonomy rules, subagent defaults, reply style, playbook table | `skills/poteto-mode/SKILL.md`, 19 KB |
| Playbooks | 23 | One step list per task type. The steps are copied verbatim into the todo list, and a skipped step stays visible as `skip: <reason>` | `poteto-mode/SKILL.md` §Playbooks |
| Workflow skills | 23 besides the router | Techniques that playbook steps call: `how`, `why`, `architect`, `arena`, `swarm`, `interrogate`, `blast-radius`, `show-me-your-work`, and others | `skills/*` |
| Principles | 23 | One rule per skill, 1 to 5 KB each, each with a trigger saying when it applies | `skills/principle-*` |
| Helpers | 20 files, about 206 KB | Mostly TypeScript for PR watching and orchestration, plus `check-plan.mjs`, `worktree-audit.sh` and `log.sh` | `skills/poteto-mode/scripts/` |
| Automations | 1 pack | Dormant. The files are not registered as skills | `automations/benny/` |

**Two agents against 47 skills.** Behaviour lives in skills. An agent is only a name a
parent can spawn, and pstack has two reasons to create one:

- **To force a read.** `poteto-agent` exists so that a subagent is sure to load the
  router. The README says "substituting `generalPurpose` skips that read and drifts".
- **To separate review from authorship.** Comment Sicko is read-only because "an author
  defends its comments the way you'd defend yours" (guide 05). The shipping rule
  follows the same logic: "the agent that judges a change is never the one that wrote
  it" (guide 06).

Droidyard started with the inverse shape: two agents, no skills, and the whole process
inside the agent prompts. The files added during this analysis move it towards
pstack's division of labour:

- **Skills hold procedure.** `planner.md` says `milestone-plan` "holds the procedure, the
  template and the map of which document decides what". `milestone-workflow` holds the
  order of steps and the handoffs between agents.
- **Agents hold role, judgment and permissions.**

At four agents and four skills the ratio is not a problem. The split is the right
one, and pstack's much larger ratio is a symptom of its scale, not a goal to aim for.
One duplication is left over from the earlier shape: the four build commands and the
"read the ADR first" rule are still stated in `AGENTS.md`, `implementer.md` and
`verifier.md`.

Two further structural ideas are worth understanding even though Droidyard should not
copy them:

- **Task type is decided before technique.** A playbook ("bug fix", "refactoring")
  fixes the steps up front, and the steps pull in techniques (`arena`, `blast-radius`).
  In the feature, bug-fix and refactoring playbooks, step 1 is `how` or reproduction.
- **Principles are loaded on demand.** The router carries a one-line index, and a leaf
  is read only when it is applied. The reply must "name each principle that shaped a
  decision… Cite only principles whose leaf SKILL.md you read this session"
  (`poteto-mode` §Non-negotiables). This saves context: 23 rules are not loaded on
  every turn.

## Mechanisms worth stealing

Five survive translation to a solo Go project with no PR workflow.

### 1. The proof ladder and the one load-bearing fact

**Source:** `skills/blast-radius/SKILL.md` §How sure are you, and §Steps 2 and 5.

For each fact a change's safety depends on, record how far down this list it got:

1. You said so.
2. You pointed at the line.
3. You walked the failure path and it does not reach.
4. You ran a test that calls the real code.
5. You reproduced it in the running app.

Most risky-looking changes are safe because of a single fact. Find that fact and
prove it; do not list maybes. In the skill's words, "A blast-radius writeup that sounds
right is worthless."

**Translation.** `prove-change` §Label what you did not prove sorts every claim into
Verified, Not verified or Assumed. The ladder splits Verified into grades. That split
matters most at Milestone 1b. "Killing a shim does not stop an operation it started"
(`docs/roadmap/mvp.md` Phase 0, ADR-0020) is level 4 against a fake connection, and
level 5 only when a real `droidyard mcp` process is killed. Both count as Verified
today. `prove-change` §Beyond unit tests already recognises the difference, but its
labels do not record it. The plan template's per-step `Evidence:` line is where the
level would go. The "one load-bearing fact" idea is the part with no counterpart in
Droidyard: for each step, name the single fact its safety rests on, and aim the
highest level of proof at that fact. Cost: a sentence in `prove-change` and a word in
each `Evidence:` line.

### 2. A project verification skill with a Doctor and a Cleanup that keeps evidence

**Source:** `skills/create-verification-skill/SKILL.md` §1 to §4,
`references/feature-map-example/`, `skills/maintain-verification-skill/SKILL.md`.

The generated skill has five sections:

- **Launch:** start the app for verification.
- **Doctor:** a read-only check that answers "is this instance worth driving?"
- **Drive:** exercise features through the real surface.
- **Evidence:** capture proof.
- **Cleanup:** "Never kill by process name; kill what you started." Cleanup never
  deletes the evidence.

A feature map sits alongside, one file per user-facing feature, with four fixed H2
headings. The generator must run the skill end to end once before handing it over: "A
generated skill that was never executed is a draft, not a deliverable." The
maintenance pass ends in one of three outcomes (`clean`, `changed`, `blocked`) and
never edits product code. If the map describes something the app no longer does, that
is doc drift or a product regression, and it is reported rather than hidden.

**Translation.** Milestone 1b is the first code that unit tests cannot fully reach:
the socket, `flock`, shim auto-start and killed processes. `prove-change` §Beyond unit
tests already sets the principles: build outside the tree, isolate the state with
`DROIDYARD_STATE_DIR`, prefer a durable form such as a Go test over pipes or a recorded
MCP transcript, and record what ran on what. pstack adds three things on top:

- **a named recipe that anyone can rerun**, instead of one improvised per run;
- **a Doctor check and a Cleanup rule**;
- **a first end-to-end run as the condition for the recipe to count at all.**

For Droidyard the recipe would be:

- **Launch:** `droidyard serve` with `DROIDYARD_STATE_DIR` and `DROIDYARD_CONFIG_DIR`
  pointing at a scratch directory.
- **Doctor:** `droidyard call system.health`, plus a check that the socket belongs to
  that state directory.
- **Drive:** `droidyard call <capability>` and a scripted `droidyard mcp` stdio
  session.
- **Evidence:** operation records, stdout and exit codes.
- **Cleanup:** stop the daemon the run started, then count orphans. The orphan count
  is itself a Phase 0 acceptance item.

The feature map is the table in `mvp.md` §MVP capability surface, with one file per
domain, grown phase by phase. In Phase 1 it holds the golden path against
DopamineControl. The natural home is a section of `prove-change`, or a sibling skill
that it points to. It should not be a new agent.

The skill's "Isolate" question already has something to find. ADR-0021's table places
the Linux socket under `$XDG_RUNTIME_DIR/droidyard/` when that variable is set, but
ADR-0024 gives the socket as `<state>/run/droidyard.sock` with no condition. If
ADR-0021 is right, `DROIDYARD_STATE_DIR` alone does not isolate the socket on Linux.
macOS is `<state>/run/` in both. *I observed this in the two tables and did not check
whether another document reconciles them. It should go to the verifier.*

This recipe would be a process artefact for the repository. It is separate from the
user-facing skills covered by ADR-0011.

### 3. Turn repeated review checks into tests

**Source:**

- `principle-encode-lessons-in-structure`: when an instruction is written a second
  time, make it "a lint, metadata flag, runtime check, or script", and "pick the
  strongest mechanism".
- `principle-build-the-lever`: "The tool is the artifact a reviewer can rerun", and "if
  you cited it and there is no codemod, script… you didn't apply it".
- The worked example is `skills/poteto-mode/scripts/check-plan.mjs`, run from
  `playbooks/multi-phase-plan.md` step 6. It is a 200-line linter over a prose plan. It
  requires headings in order, the same rule sentence at the top of every verification
  block, and ten numbered lanes, each with a screenshot and a "Pass when" predicate.

**Translation.** `verifier.md` §Things that must agree in more than one place lists
cross-document invariants, and an LLM re-checks them by reading on every run. Some are
exact and can be checked by code:

- capability names in `mvp.md` §MVP capability surface against the registry and
  `schema/capabilities.json`;
- the ADR index in `docs/adr/README.md` against the files present;
- every ADR has a Status line and the four sections, with a non-empty Alternatives
  considered.

A Go test turns these into failures of `go test ./...`. With no CI and no PR, that
command, run by the implementer and the verifier, is the only gate the project has, so
a check becomes structural only if it lives there. Keep semantic agreement with the
verifier and move only exact checks.

The plan files are an even closer match to `check-plan.mjs`. `milestone-plan` defines a
template whose rules are partly mechanical:

- every step has `Builds`, `Implements`, `Check`, `Schema`, `Needs`, `Not here` and a
  `Done. Evidence:` line;
- the status is one of five values;
- every Acceptance item names the step that proves it;
- a ticked step has non-empty evidence.

Nothing checks these mechanically today. `plan-reviewer.md` treats a missing template
section as a finding "only when its absence hides something". That is right for a
reviewer, but it means a ticked step with an empty `Evidence:` line can pass unremarked
(*inferred*). A small checker would cover the mechanical rules and leave the reviewer
free for what only judgment can decide: whether each step is the right one. Going
forward, when a reviewer reports the same class of problem twice, the next step is a
check, not another sentence in an agent prompt. A test like this will break when
someone restructures a markdown table. That is the point, as with the golden file: the
edit becomes a conscious one.

### 4. Blinding for the MVP comparison

**Source:** `skills/poteto-mode/playbooks/eval.md` §Non-negotiables for blinding and
steps 6 and 7. Guide 09 gives the reason: "An agent that knows it's being evaluated
behaves differently."

- Candidates never see words such as `eval`, `benchmark` or `compare` in any path or
  prompt.
- The prompt reads like an ordinary request, and directory names are sanitized.
- Grading comes from what the transcript shows the candidate actually did, not from
  its own report.
- The human reads every output.

**Translation.** `mvp.md` §How the comparison is run already fixes N ≥ 5 runs, a pinned
agent and model, a scripted reset, and a pilot before the threshold. It does not say
what the agent is told. The two arms differ in available tools, so the agent can see
which arm it is in and full blinding is impossible (*inferred*). Three pieces still
transfer:

1. The prompt is identical in both arms, phrased as an ordinary request, with no
   mention of measurement.
2. Tool calls, failures and tokens are counted from the client's transcript or log, not
   from the agent's summary.
3. Neither the workspace path nor the project name signals a benchmark.

This belongs in `mvp.md` as an amendment when Phase 1 closes, not now.

### 5. Show dismissed findings, and try a reviewer from another model family

**Source:** `skills/interrogate/SKILL.md` §Step 5, `references/lead-judgment.md`,
`references/reviewer-prompt.md`.

Reviewers are told to be aggressive and that "an empty review is a valid outcome". The
lead then sorts every finding into Act on, Consider, Noted or Dismissed. The dismissed
list is shown, as "a trust mechanism… lets the user override your judgment". The lead
filters for named failure modes: "nitpick gravity" (reviewers inflate nits to fill
space), hypothetical findings the call site rules out, and "I would have done it
differently". The skill says adversarial signal "comes from model diversity, not
assigned personas".

**Translation.** On the plan side this is already done: `milestone-plan` step 7 records
"each finding and its resolution" in the plan's Review table. On the code side,
`verifier.md` §Report has blocking, non-blocking, unverified and solid sections, and
`implementer.md` dismisses a finding "only by citing the ADR or evidence". The gap is
in the implementer's report, which lists deviations but not dismissed findings.
Recording "dismissed verifier findings, each with the ADR or evidence that answers it"
in the implementer's report, or in the plan's Changes section, closes the loop for an
owner who is the only person able to overrule it.

On model diversity: all four agents are Claude. `implementer` runs on
`claude-sonnet-5.5`, and `planner`, `plan-reviewer` and `verifier` run on
`claude-opus-5.5`. The planner and its reviewer are even the same model. pstack asserts
that different families catch different bugs (`interrogate` intro, `blast-radius` step
6), but no file I read gives evidence for it. Run it as an experiment: for one plan,
and for one change to `schema/capabilities.json`, run the reviewer a second time on a
non-Claude model and compare the findings. Do not make it a rule.

## What is GitHub-and-PR-centric, and irrelevant here

Most of pstack's volume is irrelevant to Droidyard:

- **The PR lifecycle.** The babysit, shipping, autopilot-full, autopilot-stack,
  orchestrate and opening-a-pr playbooks are about PRs, stacks, CI and merging
  (`README.md` playbook table, guide 06 and 07). Droidyard has none of these.
- **The PR scripts.** `scripts/watch-pr/` and `scripts/orch/` hold about 200 KB of
  TypeScript for GitHub polling policy and an orchestration store.
  `references/bugbot-triage.md` handles Cursor Bugbot comments (not read beyond its
  name).
- **The multi-phase plan template.** It has one section per PR, ten live verification
  lanes per PR on cloud machines, a 30-minute audit tick, and review videos. The idea
  behind `check-plan.mjs` survives as mechanism 3. The template does not.
- **The benny automation.** Here an "automation" is an event-triggered Cursor agent,
  created with the built-in `/automate`. Its instructions are committed markdown in the
  target repository, not registered skills (`automations/benny/README.md`,
  `FOR_AGENTS.md` §for the agent). benny triages Slack bug reports, deduplicates them in
  a tracker, reproduces them through the UI and opens draft PRs. It needs Slack, a
  tracker and other people filing bugs. Its one portable rule, "fail closed when… the
  control adapter, or the feature map are missing", is Droidyard's degraded-mode stance
  already.
- **Fleet tooling.** Worktree cleanup, `swarm`'s cloud workers, `setup-pstack`'s model
  roles and the overnight `/loop` contract all serve many agents running at once.
- **`why`'s source adapters.** `why/references/sources/` covers Slack, Linear, Notion,
  Datadog, Databricks and Sentry: digging for rationale across an organisation's
  tools. Droidyard's rationale is its ADRs.
- **Transcript tooling.** `recall`, `session-pickup`, `reflect` and the transcript audit
  in `show-me-your-work` read Cursor's `agent-transcripts/` directory. The ideas carry
  over to OpenCode; the mechanics as written do not.

## What to avoid

- **Personas.** Comment Sicko is "a deranged comment-hater" whose first output is "Yes…
  Ha ha ha… Yes!" (`agents/comment-sicko.md`). pstack's own `interrogate` says the
  signal comes from model diversity, not personas. Its keep-list would also delete
  comments Droidyard requires. `implementer.md` asks each test to say "which decision in
  a comment", and the verifier treats a comment citing an ADR section as a claim to
  check. Those comments are load-bearing. Do not adopt `no-comments`.
- **"Never block on the human" and the autonomy defaults**
  (`principle-never-block-on-the-human`, `poteto-mode` §Autonomy: "Just do it"). Inside a
  plan step this is already how the implementer works. For anything an ADR covers, it
  is exactly wrong. Droidyard's owner decisions are a named artefact (19
  ratifications). `milestone-workflow` step 3 makes the owner's approval of the plan a
  gate, and a design question sets a plan to `Blocked` and goes to the owner.
- **Design exploration without a checkpoint.** `architect` §Phase C defaults to "proceed
  directly to implementation… No human checkpoint", and it runs a three-model `arena`
  for every change that crosses a function boundary. In Droidyard that reopens
  decisions the ADRs closed. `AGENTS.md` forbids that without new evidence.
- **Principle names as ritual.** The router requires naming each principle that shaped
  a decision. The guide names the failure itself: "A principle citation with no
  decision behind it is the tell that it name-dropped" (guide 08). Citing an ADR clause
  can be checked; citing a generic principle name cannot.
- **House prose rules.** No long dashes and no mid-sentence colons
  (`poteto-mode` §Writing the reply, enforced by `check-plan.mjs`). These are taste, not
  engineering.
- **Machinery sized for a fleet.** 23 playbooks, three-model panels by default
  (`architect` Phase B, `interrogate` Step 3), ten live lanes per PR, and the
  parallel-workstream "throughput checkpoint" (`playbooks/feature.md` step 3). Model
  defaults also go stale: the README already has a migration note for rules "written
  before 0.15.3".

## Where Droidyard is stronger, and where pstack is better

**Droidyard is stronger in seven places:**

- **Decisions are durable records.** pstack's `architect/references/rationale-template.md`
  requires an "Alternatives considered" section, which is the ADR idea. But the
  rationale has no location, status, index or amendment rule; it "ships alongside" the
  sketch (`architect` §Outputs). *Inferred:* it ends up in a PR body, and opening-a-pr
  caps the squash commit at about 40 lines. That is why pstack needs `/why` and a 7.7 KB
  guide on epistemics to rebuild rationale from chat, trackers and commits after the
  fact. Droidyard writes the rationale first and amends it with `Amended:` lines. pstack
  has nothing like "a rejected option with a recorded reason is not an invitation to
  re-propose it" (`AGENTS.md`). In its lead-judgment framework, "what was already tried
  and rejected" is context that only the lead's chat happens to hold.
- **The reviewer reads the design.** pstack's reviewer gets a one-paragraph intent and
  is told "Do NOT question the intent itself" (`reviewer-prompt.md`). Droidyard's verifier
  checks code against the ADRs in both directions and hunts "fixes that broke
  something" (`verifier.md`). No pstack review file names that failure mode. Droidyard's
  record shows it matters: the fixes introduced four contract-level blockers
  (`docs/reviews/2026-09-29-pre-implementation-review.md`, Status).
- **Tests are tied to decisions.** Droidyard's rule is "A test that would still pass if
  the decision were reversed is not a test" (`implementer.md`). pstack's rule is "would it
  still pass if every imported function returned `undefined`"
  (`principle-test-behavior-not-implementation`). Droidyard's criterion is stronger for a
  contract project, because a test can observe behaviour and still not pin the
  decision. The golden-file guards (a permission prompt on `-update`, and a verifier
  that may not regenerate) already block what pstack calls "fixture asserts fixture".
- **Honesty labels live in the documents.** Examples are the spike S1 status block and
  the correction in `gradle-mcp.md`. pstack's labels live in replies and in local logs
  that are not committed.
- **The execution record is committed.** A plan stays in `docs/plans/` as the record of
  its milestone (`milestone-workflow` §Closing). Each ticked step carries its evidence,
  and changes made during implementation go under Changes. pstack's equivalent, the
  `show-me-your-work` TSV, is local by default ("leave it out of git") and committed
  only for "ambitious" work. The plan file also covers what pstack spreads across
  session-pickup, pause-safely and the decision log: "a new session can resume from it
  alone" (`milestone-workflow`).
- **Roles are enforced, not described.** pstack's agent files carry only a name, a
  description and prose, plus `is_background` on `poteto-agent`. Comment Sicko is
  read-only because it says "I never write application code". Droidyard's roles are permission rules. The planner can edit only
  `docs/plans/*.md`, the verifier and the plan-reviewer cannot edit at all, and no
  agent may regenerate the golden file without a prompt. By pstack's own
  `encode-lessons-in-structure`, which prefers structure over text, this is the
  stronger mechanism.
- **Principles are already concrete decisions.**
  - boundary-discipline corresponds to `core-runtime.md` §Adapter boundary and ADR-0015.
  - model-the-domain and "derive from authoritative schemas" correspond to the registry
    as single source of truth and ADR-0016.
  - make-operations-idempotent corresponds to `flock`, no-orphans and ADR-0020.

  A decision can be checked; a principle cannot.

**pstack is better in three places:**

- **Verification is executable** (mechanisms 2 and 3). Droidyard enforces cross-document
  consistency and the shape of its plans by having an LLM read them. pstack's instinct
  is to write a script.
- **Evidence and inference are graded.** Mechanism 1 grades evidence. `epistemics.md`
  grades inference in five tiers: Direct, Supported, Inferred, Speculative, Unknown. It
  also warns that output with no "What we don't know" section is suspicious.
  `prove-change` has three grades.
- **A catalogue of vacuous tests.** `principle-test-behavior-not-implementation` lists
  five test shapes that cannot fail, useful as a checklist for the verifier beside
  Droidyard's decision criterion. In Go, "returns `undefined`" becomes "returns its
  zero value". `prove-change`'s "see a new test fail once", and its warnings about empty
  `-run` filters and `(cached)` results, catch the same defect from the other side and
  are more specific to Go. Also relevant later: `playbooks/refactoring.md` step 1 pins
  behaviour before any refactor, because "type check and lint are not a pin".

One advantage pstack had when this analysis started is now covered: `milestone-plan`
lays out the steps before any code, with a check for each.

## Verdict

Take, in priority order:

1. **A rerunnable daemon verification recipe** (Launch, Doctor, Drive, Evidence,
   Cleanup), as a section of `prove-change` or a sibling skill, run once end to end
   before Milestone 1b is declared done (mechanism 2). The milestone's acceptance items
   are the ones unit tests cannot reach, and the socket-isolation question has to be
   settled first.
2. **Structural checks** for the exact cross-document invariants and for the mechanical
   rules of the plan template (mechanism 3). They are cheap, and `go test ./...` is the
   only gate the project has.
3. **The proof ladder in `prove-change`**, recorded on each plan step's `Evidence:`
   line, plus the one load-bearing fact per step (mechanism 1).
4. **Dismissed findings with reasons** in the implementer's report, plus one
   cross-family review as an experiment (mechanism 5).
5. **Blinding rules in `mvp.md` §How the comparison is run**, when Phase 1 closes
   (mechanism 4).

Do not take the router, the playbooks, the principle skills, the personas, the autonomy
defaults, the PR machinery or benny. The new agent-and-skill split already follows
pstack's useful division of labour. Going further, towards dozens of small skills,
would add pstack's cost without its reason, which is scale.

Items 1 to 4 change `.opencode/` or add process tests. In my reading they are not ADR
matters, but that is the owner's call. Item 5 amends a roadmap document.
