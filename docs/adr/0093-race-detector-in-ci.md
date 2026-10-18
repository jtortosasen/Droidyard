# ADR-0093: CI also runs the race detector

- Status: Accepted
- Date: 2026-10-18
- Supersedes: ADR-0068 §Decision 3 (in part: CI runs one check the pre-commit hook does not)
- Relates to: ADR-0040, ADR-0068 §3; plan m2m step 1; plan m2r
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated. The owner asked for a cleanup of the register, 2026-10-18 (plan m2r §Not in this milestone, draft decision).

## Context

ADR-0068 §3 has CI run exactly what `.githooks/pre-commit` runs, so plan m2m left
`go test -race` out, and nothing runs it (register row 251). The daemon is concurrent
throughout (sessions, the supervisor, the hub of plan m2p), and a data race shows only
under the detector. Plan m2r's baseline ran `go test -race -timeout 40m ./...` on the
owner's Mac, loaded by other sessions: every package passed, no race, the device package
in 536 s and the git package in 292 s; with the default 10-minute timeout the git package
timed out once.

## Decision

1. CI gains one job, on Linux, beside the existing one: `go test -race -timeout 30m
   ./...`, on the same triggers (pushes to `main`, pull requests), with its own job
   timeout of 40 minutes (working default).
2. The pre-commit hook does not run it: a race run of the tree takes minutes, and the hook
   runs on every commit.
3. The release workflow does not run it either; it keeps ADR-0068 §3's checks.

## Consequences

- Row 251 closes once the job has passed on `main`.
- `.github/workflows/ci.yml` gains the job, its actions pinned by commit SHA as the
  existing job's are.
- A race found later fails `main`'s CI, not a commit: whoever closes a milestone watches
  `main`'s CI, as the close already does.

## Alternatives considered

- **Race in the pre-commit hook.** Minutes per commit; the hook would be bypassed.
- **Race only on the packages a commit touches, in the hook.** The races that matter cross
  packages (a domain and the core's operation table), and the importer closure of a core
  change is most of the tree anyway.
- **Race in the existing job.** It would run every test twice in sequence and push the
  job past its 20 minutes; a separate job runs in parallel and fails on its own.
