# ADR-0104: A lent device is leased: one peer at a time, by 2s's occupation

- Status: Accepted
- Superseded in part by: ADR-0115 (§Decision 3)
- Date: 2026-10-19
- Supersedes: ADR-0094 §Decision (in part: §2, a peer's call of any kind holds a lent device; §4, a peer's lease is idle after 15 minutes and an open stream counts as activity; §5, a peer is refused whatever `conflict` says; §3, a lease also ends when the device is no longer lent)
- Relates to: ADR-0015 §6, ADR-0048 §1, ADR-0097, ADR-0098, ADR-0099, ADR-0100; plan m2t Q3 §5
- Decided by the owner, 2026-10-18: one person at a time on a lent device, which the host sees and can take back. The lease as 2s's occupation, its idle time and what ends it were drafted by the planner and decided by the main session under the owner's standing instruction, Delegated (plan m2t Q3 §5, reviewed in two rounds).

## Context

ADR-0099 lets a host lend a phone to named peers; the owner asked that one person use it
at a time, and that the host see who and take it back. ADR-0094 already holds a device for
one live session, never refuses the host's UI or CLI, and ends on close, release and the
device leaving; its §10 says remote users fit it. Two of its rules do not fit a person on
another machine: reads never hold (a second person could watch the first's screen), and
`conflict: allow` lets another session through.

## Decision

1. **The lease is ADR-0094's occupation** with a peer's link as the holder: one session
   in the registry per linked peer, client `peer`, named by `hosting.peers`.
2. **Any call of a peer holds**, reads included: listing aside, a peer's first call on a
   free lent device takes it. While it is held, another peer's calls and a host agent's
   acting calls fail `DEVICE_OCCUPIED` naming the holder, whatever `occupation.conflict`
   says; a peer whose first call finds a host agent holding the device is refused the same
   way. The host's UI and CLI are never refused (ADR-0094 §5), so the device's owner can
   always act.
3. **Idle** after 15 minutes (a constant) with no call and no open stream of the holder;
   the next caller then takes the lease over.
4. **It ends** with the peer's link, when the device is no longer lent to it (seen within
   the 2 s check), when the device leaves, and on the host's `session.release`. Its end
   stops the holder's streams on the device (`stopped deviceGone`, cause "the host ended
   the lease").
5. **On the client**, a remote id is an occupation key like a serial, so the client's own
   sessions coordinate on it as on a local device; the host's `DEVICE_OCCUPIED` passes
   through with its message.

## Consequences

- `DEVICE_OCCUPIED` joins the `errors` of every Lendable device capability ADR-0094 did
  not cover (reads, screen and input included); `peer` joins the client kinds of the
  session rows.
- A peer holds a device for up to 15 idle minutes after it stops using it; a client cannot
  hand a lease back early (plan m2t §Not in this milestone).
- `adb-device.md` §Targeting names the lease beside the occupation.

## Alternatives considered

- **A lease table of its own.** Two concepts for one hold, where the owner chose one in
  2s; the release, the idle rule and `occupiedBy` would be built twice.
- **A lease taken only by acting calls**, as ADR-0094 §2. ADR-0094 rejected "held from any
  call" because a watching session would block the acting one; for people on other
  machines that block is the owner's rule: one person at a time.
- **Honouring `occupation.conflict` for peers.** `allow` would let two people drive one
  phone, which the owner refused.
- **ADR-0094's 120 idle minutes.** A colleague's phone left held for two hours by someone
  who has gone home; 15 minutes is a working default, the owner's use will tell.
