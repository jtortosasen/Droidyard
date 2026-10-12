# ADR-0065: A recorded word may start with one `:`

- Status: Accepted
- Date: 2026-10-12
- Supersedes: ADR-0063 §Decision (in part: §4's word pattern)
- Relates to: plan m2k step 5
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated.

## Context

ADR-0063 §4's pattern `^[A-Za-z0-9][A-Za-z0-9:._-]{0,63}$` refuses a Gradle task path written from the root, `:app:installDebug`, the usual way agents name a task in a multi-module project. Such a command would be recorded with no words, so the owner would see `gradlew` and not what it ran.

## Decision

A recorded positional word matches `^:?[A-Za-z0-9][A-Za-z0-9:._-]{0,63}$`: ADR-0063's pattern with one optional leading `:`. `agent.recordCommand` validates the same pattern. `adb exec-out`, like `adb shell`, keeps only its command word.

## Consequences

- The pattern lives in `internal/assistant/observe.go` and the `agent` domain; one test pins it in each.

## Alternatives considered

- **Strip the leading `:`**: `app:installDebug` reads as a different task path to Gradle.
- **Keep ADR-0063's pattern**: the commonest Gradle form records no task.
