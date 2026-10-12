# ADR-0064: The `trash` replacement carries `--` only when the `rm` did

- Status: Accepted
- Date: 2026-10-12
- Supersedes: ADR-0062 §Decision (in part: §4's `--` before the operands)
- Relates to: ADR-0061 §2 (flags end at the first operand, BSD-style); plan m2k step 3, the verifier's N1
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated.

## Context

With Cursor's `cwd: ""` the hook cannot expand a relative glob (ADR-0062 §5). In a directory holding `-f` and `A.kt`, BSD `rm *` gets `-f A.kt`: it deletes `A.kt` and keeps `-f`. ADR-0062 §4's replacement `trash … -- *` would trash both; with `-x.kt` beside `A.kt`, `rm *.kt` is a usage error that deletes nothing, while the replacement would trash both. `trash` cannot tell, after the shell has expanded, that a word was a flag to `rm`.

## Decision

The replacement writes `--` before the operands only when the typed `rm` had it. `trash` then stops reading flags where `rm` does: an expanded `-f` is a flag, and an expanded `-x.kt` is a usage error that moves nothing. A name such as `--root` expanded in that place is a usage error too, since those options take an absolute value.

## Consequences

- The rule lives in `internal/assistant/hook.go` (`Route.endOptions`) and `TestHookReplacementReadsAGlobsDashNameAsRmWould`.

## Alternatives considered

- **`--` always, as ADR-0062 had it**: trashes a file `rm` would have kept or refused, as above.
- **Pass any relative glob under Cursor**: loses the commonest form, `rm *.kt`, to protect a rare name.
