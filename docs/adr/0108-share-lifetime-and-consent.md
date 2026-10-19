# ADR-0108: A share's lifetime, its capabilities and its consent

- Status: Accepted
- Date: 2026-10-19
- Supersedes: ADR-0099 §1 (in part: a foreground share lends without a `hosting.lend` entry); ADR-0015 §4 (in part: the human-only share listing is not filtered by the physical policy and reads each listed device's manufacturer); ADR-0019 §Sessions and ADR-0010 §Decision (in part: a native CLI connection owns one piece of state, a foreground share, released when the connection ends)
- Relates to: ADR-0015 §6, ADR-0020 §Adapter projections, ADR-0021 §Layout, ADR-0024, ADR-0103 §1, ADR-0107, ADR-0109; plan m2u Q2, Q4
- Decided by the owner, 2026-10-19: `droidyard share [<serial>]` in the foreground until Ctrl-C, or with `-d` until `droidyard unshare [<serial>]`; a detached share survives a restart; with no serial it lists physical devices (emulators not even shown) and shares the only one; no configuration edited by hand. How a share is held, ends and is persisted was drafted by the planner and decided by the main session under the owner's standing instruction, Delegated (plan m2u Q2 and Q4, reviewed in two rounds).

## Context

ADR-0099 §1 lends what `hosting.lend` names, and ADR-0015 §4 admits a physical device
only by `devices.allowPhysical` and the allowlist, both edited by a human. The owner
wants one command to do it, tied to a terminal or detached. ADR-0019 §Sessions and
ADR-0010 give session state to the WebSocket only; a CLI connection holds none.

## Decision

1. **Capabilities** `hosting.share`, `hosting.unshare`, `hosting.watchShare` and
   `hosting.join`: global, audience `human` (never MCP, ADR-0015 §6), not `Lendable`.
   `share` and `join` write the physical policy (`allowPhysical`, the serial or
   `<remote>/<serial>`): the human running the command is the consent ADR-0015 §4 asks
   for. The rule is unchanged, the writer is new. `share` with no device lists the
   physical devices whatever the policy says, emulators left out, `unauthorized`,
   `offline` and a duplicated serial (ADR-0103 §1) shown as unshareable.
2. **Detached** (`-d`): a `hosting.lend` entry, plus the hashes of its unused codes in
   `<state>/hosting/shares.json`. A consumed code is written there before the join is
   answered. It lasts until `unshare`, across daemon restarts.
3. **Foreground**: in memory, owned by the native connection that made it, as a
   subscription is owned by its session. It ends when that connection closes, so Ctrl-C, a
   closed terminal and a killed CLI end it alike. No operation is cancelled, so ADR-0020
   §Adapter projections holds. Other client kinds share detached only, until the UI brings
   its own hook. Both capabilities stay `Stateless` (no event-carrying session is needed);
   their descriptions state the tie.
4. **Stopping** (`unshare [<serial>]`, or the foreground's end): the lend goes and its
   codes are void. The peers stay paired and reach nothing; `host revoke` unpairs.
   `unshare` with no serial stops every share.
5. **Again**: `share` of a shared serial issues a fresh code on that share, its mode
   unchanged. A new share lends only to those who join it; a known client joins under its
   old name.
6. **The watch**: `hosting.watchShare` is allowed only on the caller's own foreground
   share, so it ends with that connection (ADR-0019 §Sessions: nothing long-running is
   unattended).
7. **After a Mac restart** a detached share resumes when the daemon next starts (any
   command that starts it); `share -d` says so. Starting droidyard at login is not part
   of this decision (ADR-0019 §4).
8. **Subcommands** `share`, `unshare`, `join` and `device allow|disallow` join ADR-0024's;
   `device` writes the policy directly, as `trust`.

## Consequences

- ADR-0021 §Layout gains `hosting/shares.json` (0600, atomic, quarantined when corrupt).
- `internal/daemon` tells the core when a native connection ends.
- Sharing a serial also admits it for the host's own agents: `share` says so, and that
  turning `allowPhysical` on admits every serial already in the allowlist.
- One minor API version: four capabilities.
- Register: starting droidyard at login; re-lending a re-shared device to its earlier
  peers without a code.

## Alternatives considered

- **A foreground share as an operation the CLI cancels**: a CLI killed with `kill -9`
  leaves it running (ADR-0020 §Adapter projections).
- **Persisting foreground shares**: a crashed terminal would leave a phone lent.
- **Unpairing the peers when a share stops**: their links redial into refusals, and a new
  share needs a code anyway.
- **Re-lending a re-shared device to its earlier peers**: the host's human would not see
  who gets the phone.
- **The CLI writing the share to the configuration itself**: the daemon that answers
  joins must hold the code.
- **A launchd item so a detached share resumes at login**: a new host side effect, wanted
  only if the owner asks.
