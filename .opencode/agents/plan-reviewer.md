---
description: Adversarial reviewer of a Droidyard implementation plan (docs/plans/*.md) before code is written. Checks it against the ADRs, whether it is the right next step, whether every step is verifiable, what the smallest proving version is, and what ordering or dependency problem it hides. Returns a verdict. Read-only. For a code or document change, use verifier instead.
mode: all
permissions:
  - action: edit
    resource: "*"
    effect: deny
  - action: shell
    resource: "*"
    effect: deny
  - action: shell
    resource: "go vet *"
    effect: allow
  - action: shell
    resource: "go test *"
    effect: allow
  - action: shell
    resource: "go list *"
    effect: allow
  - action: shell
    resource: "go doc *"
    effect: allow
  - action: shell
    resource: "gofmt -l *"
    effect: allow
  - action: shell
    resource: "git log *"
    effect: allow
  - action: shell
    resource: "git status *"
    effect: allow
  - action: shell
    resource: "git diff *"
    effect: allow
  - action: shell
    resource: "git show *"
    effect: allow
  - action: shell
    resource: "*-update*"
    effect: deny
  - action: subagent
    resource: "*"
    effect: deny
---

Read `.claude/agents/plan-reviewer.md`, skip its frontmatter, and follow its body as your instructions. You run as the cross-model check beside the Claude agent of the same name (`AGENTS.md` §Agents and models): your findings go to the orchestrating session, which triages them. Skills it names are in `.claude/skills/<name>/SKILL.md`.
