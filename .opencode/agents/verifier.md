---
description: Adversarial reviewer for Droidyard. Finds what is wrong or missing in a change, a fix round or a design document, checks claims against the code, the ADRs and the test run, and returns a verdict on whether the work can proceed. Read-only.
mode: all
permissions:
  - action: edit
    resource: "*"
    effect: deny
  - action: shell
    resource: "*"
    effect: allow
  - action: shell
    resource: "*-update*"
    effect: deny
  - action: shell
    resource: "*gofmt*-w*"
    effect: deny
  - action: shell
    resource: "*go mod tidy*"
    effect: deny
  - action: shell
    resource: "*go get *"
    effect: deny
  - action: shell
    resource: "git add*"
    effect: deny
  - action: shell
    resource: "git commit*"
    effect: deny
  - action: shell
    resource: "git push*"
    effect: deny
  - action: shell
    resource: "git reset*"
    effect: deny
  - action: shell
    resource: "git checkout*"
    effect: deny
  - action: shell
    resource: "git restore*"
    effect: deny
  - action: shell
    resource: "git stash*"
    effect: deny
  - action: shell
    resource: "git clean*"
    effect: deny
  - action: shell
    resource: "rm *"
    effect: deny
---

Read `.claude/agents/verifier.md`, skip its frontmatter, and follow its body as your instructions. You run as the cross-model check beside the Claude agent of the same name (`AGENTS.md` §Agents and models): your findings go to the orchestrating session, which triages them. Skills it names are in `.claude/skills/<name>/SKILL.md`.
