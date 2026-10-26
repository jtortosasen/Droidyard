# ADR-0132: On Linux, a process whose executable the kernel hides is skipped by the in-use check

- Status: Accepted
- Date: 2026-10-26
- Supersedes: ADR-0131 §1 (in part: Linux)
- Relates to: ADR-0129 §8; [plan m3f](../plans/m3f-sdk-provisioning.md) step 5
- Decided by the main session under the owner's standing instruction to apply the recommended option, Delegated (2026-10-26), on main's CI after the m3f merge.

## Context

ADR-0131 §1 skips a process whose read fails and whose saved uid is not the user's. On main's
Linux CI after m3f, `readlink /proc/<pid>/exe` failed with "permission denied" for a process of the
runner's own real and saved uid. The test of the in-use check found every package "could not
check", and so would every update and uninstall on that host.

Linux refuses `/proc/<pid>/exe` to the process's own user when ptrace read access is denied: the
process is not dumpable (it gained privileges, as a binary with file capabilities does, or opted out
with `prctl`), or it lives in another user namespace. Which of these the CI process was is not known.

## Decision

On Linux, a process of the user whose `/proc/<pid>/exe` read fails with `EACCES` or `EPERM` is
skipped, as a setuid process is. Any other read failure of a live, non-zombie process is still a
doubt.

## Consequences

- Blind spot, beside ADR-0131 §1's: a non-dumpable process, or one in another user namespace, that
  runs an executable of the user's SDK is not seen. An SDK tool the user runs (adb, the emulator,
  Java) is dumpable and in the user's namespace.
- A register row records it. The rule lives in `internal/platform/procs*.go`.

## Alternatives considered

- **Keep such a process as a doubt.** Every update and uninstall is refused on a host that runs one,
  as on main's CI, with nothing the user can stop to clear it.
- **Fall back to `/proc/<pid>/cmdline`'s first argument as the executable.** It is the name the
  process chose, not the file it runs; and `lsof` cannot read that process's files either, so a
  later check would turn it back into a doubt.
