# ADR-0121: Lag counts from the first read, not on a relayed Hub

- Status: Accepted
- Date: 2026-10-21
- Supersedes: ADR-0118 §1 (in part: a queue ages from the viewer's first read, and a relayed source's Hub has no age bound), §2 (in part: on a static screen only the request, not the GOP, bounds the wait), ADR-0119 §2 (in part: the fast preset is a working default, unproven on a 2.5 Mbit/s link)
- Relates to: ADR-0118 §3, ADR-0100 §3; plan m2y step 9 (the close verifier)
- Decided by the main session under the owner's standing instruction, Delegated, on the close verifier's findings.

## Context

The close verifier found that ADR-0118 §1, as built, ages a viewer's queue from its join. A
view joins inside `device.watchScreen` but reads only when the browser attaches its socket,
up to 10 s later: an attach more than 500 ms late dropped the joined key frame, and on a
static screen, where the encoder sends nothing, the view never drew. A relayed source's Hub,
on the client, applied the same bound but cannot ask the host's encoder for a key frame, so a
reset there could leave a view stale until the screen changed. ADR-0118 §2's "the GOP bounds
the wait" holds only while the screen changes. And ADR-0119 §2 stated that the fast preset fits
a 2.5 Mbit/s link, when two of the four runs on that link drew nothing and Lag was not measured.

## Decision

1. A viewer's packets age from its first read: until then a join's GOP waits for it, and no
   key frame is asked for on its account.
2. A relayed source's Hub has no age bound; its byte and packet bounds stay. The host's own Hub
   and the peer window (ADR-0118 §3) bound the link, and the client's socket to its browser is
   on the same machine.
3. The fast preset (1024 px, 2 Mbit/s, 30 fps) stays a working default: on L2 both qualities
   kept up, on L3 two of four runs drew nothing and the cause was not found.

## Consequences

- A slow browser attach no longer loses the first picture; a reader that falls behind after
  its first read still rejoins at a key frame it asks for.
- A client-side viewer of a remote device that falls behind is bounded only by its queue's
  bytes and packets; the register records it, with L3's runs.

## Alternatives considered

- **Forwarding a client viewer's lag to the host as a key-frame request**: a new peer message
  for a case the host's window already bounds on the link; it wins if a client's local viewers
  are shown to fall behind in use.
- **Keeping ADR-0119 §2's claim**: it records as decided a fit observed failing half the time.
