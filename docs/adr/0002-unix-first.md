# ADR-0002: Unix-first (macOS + Linux), Windows deferred

- Status: Accepted
- Date: 2026-09-27
- Revised: 2026-09-27 (previously "macOS-first, portable by design")
- Superseded in part by: ADR-0068 (§Consequences)

## Context

The first version of this decision targeted macOS only, keeping portability as a
design constraint. On review, the marginal cost of including Linux from the start
is small, because both are Unix and the core does little platform-specific work:
it orchestrates external binaries and uses processes, sockets and the filesystem.

The real differences are narrow and fit behind small adapters:

- **File watching**: FSEvents (macOS) vs inotify (Linux), abstracted by a
  portable library.
- **SDK/binary discovery**: `~/Library/Android/sdk` vs `~/Android/Sdk`, and
  `ANDROID_HOME` / `ANDROID_SDK_ROOT`.
- **Process groups and signal semantics**: effectively equivalent on both.
- **Packaging/signing**: notarization vs distro packaging.

**Windows is the genuinely different platform** (non-POSIX process model,
different path semantics, different signal handling) and is where the cost
actually lives.

## Decision

Support **macOS and Linux as first-class targets for v1**. **Defer Windows** to a
later effort.

Keep platform-specific code behind small adapters (file watching, SDK/binary
discovery, process group/signal handling, packaging) so that Windows later
becomes a plug-in seam rather than a rewrite.

## Consequences

- CI runs on two operating systems from the start.
- Path and binary discovery must handle both macOS and Linux layouts and env
  vars.
- No Windows-only or macOS-only shortcuts in the domain layer.
- The Go core cross-compiles to both without a runtime dependency.
- Windows support stays a defined, deferred seam.

## Alternatives considered

- **macOS only** — needlessly narrow given the small marginal cost of Linux.
- **Cross-platform including Windows from day one** — Windows is the expensive
  platform; supporting it before the core hypothesis is validated is premature.
