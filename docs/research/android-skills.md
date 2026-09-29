# Research: `android/skills` (Google)

- Repo: <https://github.com/android/skills>
- License: Apache-2.0
- Format: markdown skills following the open **Agent Skills** standard
  (<https://agentskills.io>)
- Analyzed: 2026-09-27 (25 skills across 17 categories)
- Local clone: `.refs/skills`

## What it is

A curated, portable **markdown knowledge base** of Android skills, plus a
plugin/marketplace packaging layer and a lightweight executable layer attached to
only 2–3 skills. It is the reference for **what our optional skills should look
like**, minus its Android CLI dependency.

Selection heuristic worth stealing: skills target **workflows where evaluations
show LLMs underperform**, not well-established areas like basic Compose best
practices.

## The skill format (adopt this)

**Agent Skills** standard: a folder with a `SKILL.md` (YAML frontmatter +
instructions) and optional `references/`, `scripts/`, `assets/`.

```text
<category>/<skill-name>/
├── SKILL.md        # required
├── references/     # optional, often mirrors doc URL paths
├── scripts/        # optional (only 1–2 skills here)
└── resources/      # optional
```

Canonical frontmatter:

```yaml
---
name: r8-analyzer
description: Analyzes Android build files and R8 keep rules ... Use when developers want to optimize app size ...
license: Complete terms in LICENSE.txt
metadata:
  author: Google LLC
  last-updated: '2026-09-22'
  keywords: [R8, proguard, keep rules, app size, optimization]
---
```

Only `name` and `description` are mandatory; `license`/`metadata` are repo
conventions. There is **no per-skill `version`** and no `allowed-tools` binding.

**Progressive disclosure** is the core mechanic:

1. **Discovery** — only `name` + `description` are loaded.
2. **Activation** — when a task matches, the full `SKILL.md` is read.
3. **Execution** — referenced files are loaded / bundled code run on demand.

## Skills vs. tools

Predominantly **pure knowledge/procedure** that composes existing capabilities
(Gradle, ADB, shell, the `android` CLI). A skill is not an MCP tool: it defines
no JSON schema, no validation, no return contract.

Only 2–3 skills bundle executables, and the pattern is not worth copying:

- `play-policy-insights`: Python orchestrator/scanner/report scripts + templates,
  with a sub-agent-vs-sequential fallback.
- `android-profiler`: downloads the Perfetto `trace_processor` binary at runtime.
- `r8-analyzer`: references `.py` scripts that **do not exist in the repo** — a
  latent inconsistency.

Takeaway: executable code is a bundled convenience, not the contract. Our
capabilities belong in the core/MCP; skills stay procedural.

## Discovery and packaging

- Discovery is **filesystem + description matching**; no registry.
- Three assistant manifests, generated from the folder tree:
  - `.claude-plugin/marketplace.json` (marketplace + plugin listing all skill paths)
  - `.codex-plugin/plugin.json` (name, `version`, `skills[]`, interface block)
  - `.agents/plugins/marketplace.json` (URL source + install policy)
- **No Cursor manifest**: Cursor consumes `SKILL.md` folders natively via the open
  standard. This is the key portability win.
- Releases are git tags + a `android-skills.zip`; the public repo is a **generated
  downstream artifact** of an internal pipeline (`update-skills.yml` wipes and
  recopies all content).

## Versioning, testing, validation

- No per-skill semantic version or changelog; only `metadata.last-updated`.
- Plugin-level version exists only for Codex (`1.0.13`), bumped from a git tag.
- **No automated tests, no frontmatter/schema validation, no link checking.**
- Quality gate is social/eval-based (issue templates require demonstrating a gap
  against both a fast and a reasoning model). Public contributions not accepted.
- Runtime "validation" is pushed into the skill as instructions (build commands,
  screenshot parity, checklists, output contracts).

## Workflows encoded

- **Build → run → inspect → debug** (`android run`; `android layout` flat JSON UI
  tree with a `--diff` mode to keep context small; screenshot+annotate fallback;
  `input tap/swipe/text/keyevent`; XML "journeys" as sources of truth).
- AppFunctions build → list → invoke → toggle → debug.
- Profile: setup → record → analyze → verify (Perfetto).
- Analyze-and-report, read-only (r8-analyzer, play-policy-insights).
- Multi-phase migrations (analyze → plan → migrate → build → validate → clean up).
- Greenfield integrations (client-side only, with backend reminders).
- Testing strategy meta-workflow.

## Reusable ideas (high value for Droidyard)

1. **Adopt the agentskills.io format verbatim.** It is the de-facto cross-assistant
   standard; shipping `<name>/SKILL.md` gives Claude + Codex + Cursor support
   essentially for free. Namespace our own fields under `metadata:`.
2. **Progressive disclosure as a rule.** Keep `SKILL.md` short and routing-oriented;
   push detail into `references/`. Keeps tokens cheap with many skills installed.
3. **Mirror canonical doc paths in `references/`** for traceability (we mirror our
   doc/URL or capability IDs).
4. **Separate knowledge skills from orchestrator skills**: a thin SKILL.md for
   intent disambiguation + routing, and workflow files with their own
   `name/description/keywords` discovered by frontmatter scan.
5. **Invest in discriminative `description` triggers** ("Use when…"), since
   activation is description-matching.
6. **Generate per-assistant manifests from the folder tree** (single source of
   truth).
7. **Degrade gracefully when a capability/assistant feature is absent** (their
   sub-agent-vs-sequential fallback is the model for our AI-agnostic skills).
8. **Portability hygiene**: `$SKILL_ROOT` anchoring and a contained scratch dir.
9. **Explicit output contracts** ("output ONLY the markdown report"; "reply exactly
   `SUCCESS`") to protect context and make skills composable.
10. **End procedural skills with a verification step backed by a real command**
    (for us: a capability call such as build + run + UI diff).
11. **Conservative cleanup default**: remove legacy files only when asked.
12. **Use their category taxonomy** (`build-system`, `performance`, `security`,
    `testing`, …) for our skill IDs.

## Things to avoid

1. **Do not bundle scripts for anything our Go core can expose as a tool.** Their
   Python orchestrators / runtime-downloaded binaries hurt determinism, security
   and cross-platform behaviour.
2. **Avoid hardcoded assistant/install paths inside skills** (their r8-analyzer
   path is assistant-specific and points at scripts that are not shipped).
3. **Avoid network downloads during skill execution.**
4. **Avoid unversioned skills.** `last-updated` is weak; add a real `version` (or
   digest) and a changelog so consumers can pin and audit.
5. **Avoid duplicating shared setup prose** across many skills; factor shared
   prerequisites.
6. **Do not depend on the Android CLI.** Skills must call **our** capabilities
   (Gradle/ADB via our core), not `android ...`.
7. **Avoid over-broad/overlapping trigger descriptions** that cross-activate.
8. **Do not rely on sub-agent orchestration as the primary design**; make it an
   optional branch.
9. **Do not vendor official docs verbatim at scale**; link to sources and keep
   only our opinionated delta.
10. **Avoid an opaque generated-content pipeline**; keep skills in normal
    PR-reviewed source with meaningful diffs.

## Legal note

Apache-2.0. Adaptation is permitted with attribution and a "modified from
android/skills" note, retention of notices, and no implied Google/Android
endorsement (no trademark grant). Prefer linking canonical sources over vendoring.

## Verdict for Droidyard

Adopt the **format and the split** (knowledge vs. orchestrator, progressive
disclosure, generated per-assistant manifests, graceful degradation). Replace the
**Android CLI dependency** with calls to our own capabilities, and drop the
bundled-script pattern. Start with the **build → run → inspect → debug** loop,
where our tools add the most value.

Related: `interfaces/mcp-tool-surface.md` and a future
`docs/skills/README.md` (format + packaging) should incorporate this.
