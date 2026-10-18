# ADR-0092: A Gradle root switched away from closes its connection

- Status: Accepted
- Date: 2026-10-18
- Supersedes: ADR-0075 §Decision 8 (in part: "a root switched away from keeps its connection until the close or the sidecar's idle timeout")
- Relates to: ADR-0026, ADR-0027, ADR-0075 §2, §8; plan m2o; plan m2r
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated. The owner asked for a cleanup of the register, 2026-10-18 (plan m2r §Not in this milestone, draft decision).

## Context

ADR-0075 §8 has the daemon remember, per open workspace, every Gradle root its calls
resolved, and close a connection at the workspace's close when no other open workspace
uses it. A workspace whose `gradleRoot` is changed from the UI resolves the new root at
its next call, and the old root's sidecar connection, with its Gradle daemon behind it,
stays until the workspace closes or the idle timeout (register row 39). On the owner's
project a Gradle daemon is several hundred megabytes.

## Decision

1. Each open workspace remembers its **current** Gradle root: the one its latest
   resolution gave. When a resolution gives a different root (or none), the workspace
   forgets the previous one.
2. A forgotten root whose connection no open workspace remembers is closed as the
   workspace's close closes one (ADR-0075 §8), once no operation holds it: a build or a
   model call in flight on that root finishes first, and the close follows its end.
3. The crash rule of ADR-0075 §8 now reads over the current roots: a sidecar crash
   degrades the open workspaces whose current root is the crashed one.

## Consequences

- Row 39 closes.
- `internal/domain/build` (or the package that holds the per-workspace roots): the
  remembered set becomes one current root per workspace, with tests for a switch that
  closes, a switch while a build runs (closed after), and a root another workspace still
  uses (kept).
- `docs/architecture/workspace-state.md` or `gradle.md`, wherever ADR-0075 §8's rule is
  stated, changes its sentence.

## Alternatives considered

- **Keep it and rely on the idle timeout** (ADR-0075 §8 as written). Holds a Gradle daemon
  for the timeout after every switch; the switch is rare, but it is exactly the moment the
  owner is fixing a wrong root.
- **Close at once, cancelling what runs on the old root.** A switch from another tab would
  cancel a build the user started; waiting for it costs nothing.
