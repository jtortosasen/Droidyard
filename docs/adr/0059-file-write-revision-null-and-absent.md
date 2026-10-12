# ADR-0059: `ifRevision` null is refused, and `absent` is settled by existence

- Status: Accepted
- Date: 2026-10-12
- Supersedes: ADR-0058 §Decision (in part: item 2's "a target over 8 MiB is
  `FILE_TOO_LARGE`" and its silence on null; item 4's mutex scope as stated in §Consequences)
- Relates to: ADR-0032 §5 (`policy.update` refuses a null `ifRevision`); plan m2i step 2
  (the verifier's round), §Changes
- Decided by the main session under the owner's standing instruction (apply the recommended
  option), Delegated, before 0.20.0 shipped: tightening either rule later would break callers.

## Context

Step 2's verifier found two gaps in ADR-0058 §2. An explicit `"ifRevision": null` decoded as an
omitted field, so a client that sent null got an unconditional write: `policy.update` refuses
null for exactly that reason. And `ifRevision: "absent"` against an existing file over 8 MiB
answered `FILE_TOO_LARGE`, whose message ("file.write compares files up to 8 MiB") invites the
caller to drop the check and retry, which overwrites the file; existence alone settles `absent`.

## Decision

1. An explicit `"ifRevision": null` is refused as invalid parameters ("omit it to write without
   the check"); omitting the field still means no precondition.
2. `ifRevision: "absent"` against an existing target is `FILE_REVISION_CONFLICT` whatever the
   target's size; `FILE_TOO_LARGE` remains only for a hash compared with a target over 8 MiB.
3. The mutex of ADR-0058 §4 is `file.write`'s only: `file.create`, `move`, `copy`, `delete` and
   `restore` do not take it (ADR-0058 §Consequences named four of the five).

## Consequences

- The rules live in `internal/domain/file/write.go` (`WriteParams.UnmarshalJSON`, the locked
  compare) and its tests; the register row on the foreign-writer window names all five.

## Alternatives considered

- **Null as omitted** (every other optional pointer in the API): the one field whose omission
  disables a safety check should not be switched off by a value a client may send by accident.
- **`FILE_TOO_LARGE` for `absent` too** (ADR-0058 as written): its next step leads a model to
  drop the guard; a conflict's next step is a read, which is right for an existing file.
