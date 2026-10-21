# ADR-0119: The live view stays on TCP; a datagram transport waits for a measured loss

- Status: Accepted
- Superseded in part by: ADR-0121 (§2)
- Date: 2026-10-21
- Relates to: ADR-0117, ADR-0118, ADR-0097, ADR-0109; plan m2y Q8, step 8
- Decided by the main session under the owner's standing instruction, Delegated. The owner asked whether "a mode that switches to UDP or WebRTC" would win something over the internet or a VPN (2026-10-21).

## Context

Measured in m2y step 8 on the owner's phone (a Samsung SM-A256B, its hardware encoder) lent
through a loopback peer pair and step 1's link emulator, with steps 2–7 built (input on the
view's socket, the 500 ms viewer bound, the peer window, the fast mark), a tap from pointer to
drawn frame:

```text
link                              quality   tap p50   drag per frame p50 / p95
none (pair on loopback)           full       81 ms     67 / 100 ms
L2  100 ms RTT, 6 Mbit/s, 1 %     full      248 ms    200 / 317 ms
L2                                fast      219 ms    182 / 317 ms
L3  150 ms RTT, 2.5 Mbit/s, 2 %   full      382 ms    did not draw within 45 s
L3                                fast      no view   384 / 917 ms
```

On L2 a tap costs about the round trip plus what it costs on loopback: TCP adds little when
the link is not saturated. L3's 2.5 Mbit/s is under the full stream's 4 Mbit/s, and its runs
were unstable in both qualities on a host at load 30–90; it was not rerun. The emulator's
stalls are stalled 16 KiB chunks, not lost packets, and no real VPN was measured.

## Decision

1. **No UDP or WebRTC path in this milestone, nor a milestone named for one.** A datagram
   transport removes TCP's head-of-line blocking after a loss, but a lost video datagram costs
   a key frame (188–217 ms on the phone, step 6) or a retransmission (one round trip), about
   what TCP's own recovery costs at these round trips, and it would need its own encryption,
   pairing and NAT handling beside the peer link's TLS (ADR-0097).
2. **The fast preset stays 1024 px, 2 Mbit/s, 30 fps** (ADR-0118 §4): it fits a link of about
   2.5 Mbit/s, where the full stream does not; on L2 the full stream already keeps up, so a
   remote is marked fast only for a link that cannot carry 4 Mbit/s.

## Consequences

- A remote view over a VPN costs about its round trip more than a local one, and a link
  slower than the stream needs the fast mark.
- Reopen when a real VPN or internet link shows staleness the 500 ms bound and the window do
  not explain, with packet loss measured (a register row records it).

## Alternatives considered

- **WebRTC for the browser and the peer**: the browser talks to its own daemon on loopback,
  where there is no loss; between daemons it adds ICE, DTLS and SRTP for a gain the figures
  above do not show.
- **QUIC between daemons**: streams without head-of-line blocking between video and input,
  over TLS 1.3; the input already travels its own direction of the socket and arrives in
  milliseconds (T1 through the pair p50 1.9 ms), so the gain is the video's after a loss only.
- **Adapting the bit rate to the link**: restarting the encoder blanks every viewer (ADR-0118
  Alternatives); the fast mark is the owner's per-remote choice instead.
