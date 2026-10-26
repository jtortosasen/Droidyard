# ADR-0131: Setuid processes, the emulator's lock and a licences-only root, as step 1 found them

- Status: Accepted
- Date: 2026-10-26
- Supersedes: ADR-0129 §5, §7, §8 (in part)
- Relates to: ADR-0130; [spike m3f step 1](../research/spike-sdk-provisioning-2026-10.md) §(d), §(f); [plan m3f](../plans/m3f-sdk-provisioning.md) Q10
- Decided by the main session under the owner's standing instruction to apply the recommended option, Delegated (2026-10-26), from the plan's Q10 draft and the redraft's review.

## Context

Three clauses of ADR-0129 do not survive the spike's evidence and ADR-0130:

- **§8, the in-use check.** It counts any unreadable live process as a doubt. Every terminal
  session runs setuid `login` processes, and `kern.procargs2` fails on them with `EINVAL`
  (`P_svuid` 0). Under §8 as written, every update and uninstall on a Mac with a terminal open
  would be refused.
- **§7, a running AVD.** It counts "the emulator's lock" as running. No emulator was running, so
  the lock was not observed, and its name is unknown.
- **§5, the target root.** It refuses an existing, non-empty directory that is not an SDK. On a
  host with no SDK, `sdk.acceptLicense` creates `<root>/licenses/` before the first install,
  which §5 would then refuse. Setting up from nothing would deadlock.

## Decision

1. **Setuid processes are skipped.** A process whose `kern.procargs2` read fails (on Linux,
   whose `/proc/<pid>/exe` or `cmdline` read fails) and whose saved uid is not the user's is
   skipped, not counted as a doubt. An executable inside the user's SDK cannot be setuid to
   another user. Every other unreadable live process is still a doubt.
2. **The emulator's lock is an open file.** It counts only as a file held open inside the
   `.avd` directory, found by §7's open-file check over the candidate processes. Lock files are
   not matched by name.
3. **A licences-only root is empty.** A directory holding nothing but `licenses/` and `.temp/` is
   an empty target for §5.

## Consequences

- Blind spot: a setuid wrapper of another saved uid that runs an SDK executable is not seen. It
  becomes a register row.
- An AVD whose emulator crashed and left a lock file is not held as running. It is held only
  while a process still has a file open there.
- The rules live in the code of plan m3f steps 3, 4, 5 and 6, and in `sdk.md` §Provisioning
  and `emulator.md` §Capabilities at the close.

## Alternatives considered

- **Count setuid processes as doubts.** Every update and uninstall would be refused on any Mac
  with a terminal open.
- **Match lock files by name.** A crashed emulator's stale lock would block delete and edit
  until the user found it, and the name was never observed.
- **Keep the licence file somewhere else until the first install.** It would be a second
  place, one that AGP and Android Studio do not read.
