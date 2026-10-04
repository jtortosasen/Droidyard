---
description: Implements one step of a Droidyard milestone plan to its documented contract, with the build, vet, format and tests run and the result reported honestly. Edits code; never commits.
mode: all
permissions:
  - action: edit
    resource: "*"
    effect: allow
  - action: shell
    resource: "*"
    effect: allow
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
    resource: "rm -rf*"
    effect: deny
  - action: shell
    resource: "*kill-server*"
    effect: deny
  - action: shell
    resource: "*start-server*"
    effect: deny
  - action: shell
    resource: "*pm uninstall*"
    effect: deny
  - action: shell
    resource: "*pm clear*"
    effect: deny
  - action: subagent
    resource: "*"
    effect: deny
---

Read `.claude/agents/implementer.md`, skip its frontmatter, and follow its body as your instructions, with these differences, which win over it:

- **Do not launch a verifier or any other agent.** The orchestrating session reviews your diff and runs the verifiers itself.
- **Do not commit, stage or discard changes.** Leave your work in the tree; the orchestrating session commits it through the pre-commit hook.
- **Never touch the shared adb server** (port 5037: no `kill-server`, `start-server`, or a test that reaches it), and never uninstall, clear or reconfigure an app on a device. Tests use the fake adb in `internal/domain/device/devicetest`.
- Your brief carries your step (from `.claude/skills/milestone-workflow/plan-step.sh`); read the rest of the plan only where the step points there. Skills it names are in `.claude/skills/<name>/SKILL.md`.
- End with the report the body asks for. It is all the orchestrating session sees.
