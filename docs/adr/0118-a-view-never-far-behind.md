# ADR-0118: A view never far behind, and a lighter stream for a remote marked fast

- Status: Accepted
- Date: 2026-10-21
- Supersedes: ADR-0078 §5 (in part: a viewer is also reset by its queue's age, and the source asks for a key frame), ADR-0077 §1 (in part: a source started for a remote marked fast uses a lighter preset), ADR-0117 §3 (in part: the peer screen socket also carries the client's acknowledgements)
- Relates to: ADR-0100 §3, ADR-0109 §4, ADR-0120; plan m2y Q6, Q7; [`spike-live-input-2026-10.md`](../research/spike-live-input-2026-10.md) §S3, §S7, §S8
- Decided by the owner, 2026-10-21, for the remote quality: a remote the client marks fast gets a lighter stream, every other view keeps full quality ("Ajuste por remoto"). The rest is Delegated (plan m2y Q6, Q7, and step 1's results).

## Context

A viewer is reset today only when its queue passes 20 MiB, which on a 4 Mbit/s stream is
about 40 s of video: a viewer that falls behind stays behind. On a slow link the backlog can
sit outside the Hub altogether, in the kernel's socket buffers, where no daemon rule sees it.
Step 1 could not measure a socket option against that (§S7: macOS grows a loopback receive
buffer past `SO_RCVBUF`, so the queue moved into the lab's relay). scrcpy 4.1's reset-video
message asks the encoder for a key frame: 83–156 ms on an API 31 emulator, 0.5–2.0 s on API 36
(§S3). The owner asked that a view over a VPN keep up, and chose that a remote marked fast
trades picture for latency while every other view keeps full quality.

## Decision

1. **A viewer's lag is its queue's age.** Each packet carries the time it entered that
   viewer's queue (a join's GOP enters at the join, so it is not lag). A viewer whose oldest
   queued packet entered more than 500 ms ago (working default) is emptied and rejoins at the
   next key frame, as an overflow does today; the byte and packet bounds stay.
2. **A lag asks for a key frame.** The source writes one reset-video on its control line when
   one of its viewers lags, at most one every 2 s; where the encoder is slow to answer, the
   GOP (`i-frame-interval=2`) still bounds the wait.
3. **The peer link bounds what is in flight.** The client daemon acknowledges the video bytes
   it has received on the peer screen socket, a binary frame `[4][u32 bytes received, mod
   2^32]` at least every 64 KiB and every 100 ms while bytes arrive. The host keeps at most
   256 KiB (working default) unacknowledged per peer socket: past it, it stops taking from that
   viewer's queue, so a slow link's backlog forms in the Hub, where rule 1 bounds it. A client
   that never acknowledges (an older one) is sent as today. The local `/v1/screen` has no
   window: its browser is on the same machine.
4. **A remote marked fast gets a lighter stream.** The client's `remotes` entry gains `fast`
   (`droidyard remote fast <name> on|off`), sent in the client's hello; a source the host starts
   for a peer whose hello says fast uses the fast preset, working default `max_size=1024`,
   2 Mbit/s, 30 fps, replaced at step 8 by the largest preset whose Lag on L2 stays under 1 s.
   Every other start keeps ADR-0077 §1's options. A source keeps its options for its life,
   shared by every later viewer: the host's own view of a source a fast peer started is light
   too, and a fast peer joining a source the host started gets full quality.
5. **H.265 is not built** in this milestone; a trial on a phone at step 8 decides whether a
   later milestone does. scrcpy's low-latency codec options are not adopted (§S2).

## Consequences

- A viewer that falls behind loses at most about 0.5 s plus the wait for a key frame, and
  never replays a stale backlog; a slow peer link cannot hide seconds of video in kernel
  buffers.
- The peer socket's client-to-host direction carries acknowledgements beside the input frames.
  An older host closes the socket 1003 on a binary frame it does not know, so the client sends
  them only to a host whose hello names API 0.33.0 or later.
- A fast mark lowers the picture for everyone watching that source, the host's own user
  included, while the source lives.
- Documents: `adb-device.md` §Screen viewing (the lag rule), `peer.md` §The peer port (the
  acknowledgements, the fast mark), in steps 6 and 7.

## Alternatives considered

- **A larger queue, or only the byte bound**: a viewer behind stays behind; the bound is
  latency, not memory.
- **Dropping delta frames**: the decoder shows corruption until the next key frame, so the
  rejoin at a key frame costs no more and shows nothing broken.
- **Only `TCP_NOTSENT_LOWAT` or a small `SO_SNDBUF`**: it bounds the sender's socket, but not a
  VPN's or a router's buffers, and step 1 could not verify it; it may stay as a cheap
  addition (step 7), not as the bound.
- **Adapting the bit rate to the measured throughput**: restarting the server blanks every
  viewer for 0.5–2.2 s (§S3), and one source serves every viewer.
- **A per-call quality parameter**: one source serves every viewer of a device, and it would
  change `device.watchScreen`'s contract for a per-link choice.
- **A preset chosen by the host's listener tag (`vpn`)**: the owner chose a per-remote mark on
  the client; a VPN may be fast and a LAN slow.
- **A UDP or WebRTC transport**: step 8 measures what TCP costs after these rules and records
  the decision (number 0119).
