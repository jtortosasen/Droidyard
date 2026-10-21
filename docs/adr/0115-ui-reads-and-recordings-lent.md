# ADR-0115: The UI reads and the recording lent to a peer

- Status: Accepted
- Date: 2026-10-21
- Supersedes: ADR-0099 §Decision 2 (in part: its closed list gains the UI reads and the recording); ADR-0110 §Decision 6 (in part: `device.uiDump` is lendable); ADR-0111 §Decision 9 (in part: `device.uiEvents` is lendable); ADR-0112 §Decision 1 (in part: `device.watchUiEvents` is lendable); ADR-0104 §Decision 3 (in part: a running recording counts as an open stream)
- Relates to: ADR-0100 §2–§4, §6; ADR-0101; ADR-0104 §2, §4; ADR-0106; ADR-0113; plan m2x Q10, Q11
- Decided by the owner, 2026-10-21: "Si precisamente esta opcion la hice para poder hacer dumps remotos!!"; and a standing rule: a new device capability is lendable by default, and leaving one out is the owner's question. The shape is Delegated (plan m2x, reviewed in three rounds).

## Context

m2w left `device.uiDump`, `uiEvents` and `watchUiEvents` unlendable, a Delegated call made without
knowing that the owner built lending to take dumps of a remote phone. The relay patterns exist:
`plainThen` with the artifact fetched over the peer port (`device.screenshot`), relayed streams
(`watchLogs`, `watchScreen`, ADR-0106), and the lease (ADR-0104). A recording (ADR-0113) polls and
dumps up to ten times a second, too chatty to relay call by call over a VPN.

## Decision

1. **The reads are lent.** `device.uiDump` is relayed with its artifact fetched, parsed and
   stored locally as untrusted (`application/xml` joins the peer route's types); `uiEvents` is
   relayed plain with the host's cursor passed through; `watchUiEvents` is relayed as
   `watchLogs` is, at most four streams per link and device on the host. Their errors gain
   `DEVICE_OCCUPIED`; a peer's stream counts as activity for the lease and ends with it.
2. **A remote recording runs on the host.** `recordUi`, `stopUiRecording`, `uiRecordings`,
   `watchUiRecordings` and `deleteUiRecording` are lendable. The host's recorder records the lent
   device; the client keeps its own record and its own `r.<id>`, never the host's reference.
3. **Ownership.** A peer sees, stops and deletes only its own recordings; an error about another's
   carries no id. The host's UI and CLI see and stop every recording; a host MCP agent cannot stop
   a peer's. On the host a peer keeps at most 10 recordings and 1 GiB.
4. **The lease.** A peer's running recording counts as an open stream for ADR-0104 §3's idle rule
   and ends with the lease (§4: link, unlending, the device leaving, `session.release`) as
   `deviceGone` with the lease's cause; the timeline stays on the host. Stop and a
   device-filtered list take the lease as any device call, without adb.
5. **The copy.** When the recording ends the client copies its timeline by `Range` over the peer
   port into a `.part` file, resuming after a lost link, and renames it only when the host's
   SHA-256 matches; until then its reference reads `growing`. The host then deletes its copy. The
   peer route serves a peer only its own finalised timelines (409 while running, 404 for anything
   else, so nothing reveals which recordings exist). At link up the client adopts an ended
   recording of its own that it holds no record of.

## Consequences

- From his own Mac the owner can dump, watch and record a phone lent by another, with the
  timeline landing in his own state, verified byte for byte.
- A VPN drop ends a remote recording (the lease ends with the link); what was recorded is kept
  and copied at the next link up. Recordings that survive a short drop would need a grace period
  in the lease itself.
- The host's audit holds one operation per relayed call, not one per tick.
- Not proven between two Macs: the level-5 runs use two daemons on one Mac; the owner's trial is
  the real one.

## Alternatives considered

- **A recorder on the client over relayed calls.** About ten host operations a second in the
  host's audit and an artifact fetch per change over the VPN.
- **A recording that outlives the lease.** Another peer could take the device while it records
  the first one's session, against ADR-0104's one person at a time.
- **Reading the host's growing timeline live.** Needs a remote growing artifact class; the counts
  in the watch suffice while it runs.
- **Keeping the reads unlendable.** Against the owner's use case and his standing rule.
