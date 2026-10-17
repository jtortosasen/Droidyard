# ADR-0078: `device.watchScreen`, the `device.screen` topic and `GET /v1/screen`

- Status: Accepted
- Date: 2026-10-17
- Relates to: ADR-0010, ADR-0015 §2, §4, ADR-0016, ADR-0028, ADR-0041 §4, §6, ADR-0077; `http-websocket.md` §Security baseline; plan m2p Q3, Q4, Q7, Q8; [`spike-screen-stream-2026-10.md`](../research/spike-screen-stream-2026-10.md)
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated (plan m2p Q3, Q4, Q7, Q8, reviewed in two rounds).

## Context

The source (ADR-0077) yields H.264 packets at about 2–4 Mbit/s and 37 per second while
the screen moves. The session's `/v1/ws` is JSON text only (ADR-0028) with a 1,024-event
queue whose overflow is `events.gap`; a decoder that loses a packet shows garbage until the
next key frame. The spike showed the config (SPS/PPS) is sent only when the encoder
starts or restarts, never with a later key frame, so a viewer that joins late needs it
kept apart; and that Chrome decodes a late join at a key frame with the config prepended
(67 of 67 packets).

## Decision

1. **Capability** `device.watchScreen {device}` → `{subscriptionId, device, streamId,
   width, height}`: kind command, global, streaming (never an MCP tool, ADR-0010),
   audience `all`, not long-running. The device policy is checked at start. Errors:
   the preamble's, `DEVICE_STREAM_FAILED` (a push, start, connect or protocol failure at
   start; its cause names the phase and exit status, never device output),
   `SESSION_REQUIRED`, `REQUEST_INVALID_PARAMS`.
2. **Events**, topic `device.screen`, published to the call's subscription only
   (ADR-0041 §4): `stopped {device, reason: deviceGone | exited | failed | detached,
   cause?}`, and `state {device, secure}` (ADR-0080).
3. **`streamId`**: 128 random bits, base64url, mapped to the call's viewer until the
   subscription ends, the stream socket closes, or 10 s pass without an attach; each of
   these releases the viewer and publishes `stopped` (`detached` for the last two).
4. **`GET /v1/screen`**, a WebSocket of its own, under the security baseline (Host
   allowlist, mandatory `Origin`, no CORS, 4 KiB read limit). The first text frame,
   within 5 s, is `{"auth": <token>, "stream": <id>}`, compared in constant time. Close
   codes: 4401 a bad token, 4404 an unknown id, 4409 a second attach, 4410 the stream
   ended, 1003 any client frame after `auth`. Then binary messages
   `[flags u8: 1 key, 2 config][pts u64 BE][Annex B]`, each under a write deadline. The
   adapter holds only `Attach(id) (Viewer, error)`: nothing it receives reaches the
   device.
5. **One source per device**, shared by every viewer and reference-counted. It keeps the
   current config apart from the current GOP (the latest key frame and what follows,
   at most 16 MiB of device bytes). A join starts at the GOP's key frame with the config
   prepended once; a viewer whose queue (20 MiB) overflows is emptied and rejoins at the
   **next** key frame, carrying the current config. The bytes a viewer receives are the
   device's, unchanged but for that prepend.
6. **Lifetimes.** The source ends with its last viewer, with its device (`deviceGone`,
   `emulator.stop` included), with the daemon (`StopAll`, from `AfterOps`, as the log
   streams), or on a protocol error (`failed`). The UI watches only while its panel is
   visible.
7. **Decoding** is the browser's WebCodecs `VideoDecoder` (Annex B, `optimizeForLatency`)
   to a canvas. A browser without H.264 in WebCodecs gets a message, never stills.

## Consequences

- A live view costs one server on the device whatever the number of tabs, and a slow tab
  only drops its own packets until the next key frame (about 3.2 s apart on the emulator).
- The endpoint is the unit a remote host (C4) would relay: the bytes are the device's.
- `DEVICE_STREAM_FAILED` joins the codes; one minor API version with ADR-0076 and
  ADR-0079 (plan m2p: `0.27.0`).
- Documents to change: `capability-api.md` §Events and subscriptions (`device.screen`),
  `http-websocket.md` §Endpoints (`GET /v1/screen`); in plan m2p step 10.

## Alternatives considered

- **Base64 packets as events on `/v1/ws`**: a third more bytes, and the shared 1,024-event
  queue with `events.gap` would break the decoder on any burst until the next key frame.
- **Binary frames on `/v1/ws`**: video would share one TCP send buffer with every response
  of the session, and ADR-0028's session is text only.
- **SSE or chunked HTTP**: not binary, and no key-frame-aware backpressure per viewer.
- **A server per viewer**: an encoder each on the device. **Refusing a second tab**: the
  owner opens several.
- **Asking the encoder for a key frame on join**: a scrcpy-specific request a relay could
  not forward; the kept GOP works for any source.
- **MSE with a remux to fragmented MP4, or a WASM decoder**: more code and latency, or a
  dependency, where WebCodecs decodes the device's Annex B as is.
