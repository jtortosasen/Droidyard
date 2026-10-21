# ADR-0117: The live view's input travels its own stream, authorized when the view opened

- Status: Accepted
- Date: 2026-10-21
- Supersedes: ADR-0079 §Decision 3 (in part: the live view no longer delivers its input as calls; the three capabilities keep their contract); ADR-0078 §Decision 1 (in part: the result gains `control`), §Decision 4 (in part: binary client frames after `auth` are input)
- Relates to: ADR-0015 §2, §4, ADR-0043 §2, ADR-0060 §2, ADR-0094 §5, ADR-0099 §2, ADR-0100 §3, ADR-0104 §2–§4, ADR-0109 §3; `ui-automation.md` §Input primitives; `http-websocket.md` §`GET /v1/screen`; plan m2y Q1–Q5; [`spike-live-input-2026-10.md`](../research/spike-live-input-2026-10.md)
- Decided by the owner, 2026-10-21: the view is authorized when it opens and its input then goes straight to the device, "a direct interaction tunnel … speed and response come first"; every input is still recorded. The shape is Delegated (plan m2y Q1–Q5, reviewed in two rounds).

## Context

ADR-0079 made every input from the live view a capability call, so the policy, the audit and
the trace see it. Its cost, measured in m2y step 1 on emulators: T1 (the client's send → the
control-socket write) p50 50–65 ms locally, 71 ms through a loopback peer pair, before the
device's own floor (F, p50 60–90 ms on a loaded host). Most of it is the call path:
`authorize`'s `adb devices -l` (p50 49 ms in m2p's spike), the HTTP round trip and the
operation bookkeeping. A drag reaches the device only when released, because a live drag would
be an operation per move. The owner judged the view laggy on taps and drags and changed the
rule: the view's admission is the input's admission.

Facts the shape rests on (step 1, scrcpy v4.1): the scroll message and `ACTION_CANCEL` work on
both levels, and a down after a cancel lands; a touch whose screen size is not the current video
size is dropped without a reply (`PositionMapper.map`), a lift included.

## Decision

1. **The grant.** `device.watchScreen`'s authorization (the device policy, ADR-0015 §4; for a
   remote id the client's policy and, on the host, the lending check and the lease, ADR-0104 §2)
   is the input's authorization for the view's life. The view's input travels its own stream
   socket, never as a call: no per-input policy read, `adb devices -l`, HTTP round trip or write
   key. `device.watchScreen`'s result gains `control: boolean`, true when the stream accepts
   input (an older host's result lacks it, which reads as false).
2. **Bounded revocation.** A view, input included, ends within 2.5 s when the policy stops
   admitting its physical device or remote id (a 2 s sweep, `stopped deviceGone`), when the
   device is no longer lent to the peer (the host's lease sweep), when the peer is unpaired (the
   listener's sweep; the client reads its close as `deviceGone`), and, as before, when the
   session, the daemon, the device or the source ends. An ended stream accepts nothing.
3. **The wire.** After `auth`, client binary frames, big endian, coordinates normalised to the
   frame (0..65535): touch `[1][action: 0 down, 1 up, 2 move, 3 cancel][x u16][y u16]`; key
   `[2][key: 1 back, 2 home, 3 appSwitch, 4 enter, 5 delete]` (down and up); scroll
   `[3][x u16][y u16][h i16][v i16]` (notches × 256). The daemon parses, validates and encodes
   scrcpy's messages with the current frame size; client bytes never reach the control socket,
   so scrcpy's other messages are unreachable. A text frame or a malformed one closes the socket
   1003. At most 1,000 frames a second per socket and a queue of 256 per source with moves
   coalesced; past either, only the sending socket is closed (1008). The same wire runs from a
   client daemon to its host on `/peer/v1/screen`.
4. **One finger per device**, owned by the source. A holder (a stream, or an `Inject` from a
   call, classed by the call's origin) keeps it from down to lift. A local holder takes it from a
   peer holder with a cancel; a peer never takes it from a local holder (ADR-0094 §5); between two
   local holders the first keeps it. A gesture whose down was refused is dropped whole.
5. **Every lift the daemon writes is `ACTION_CANCEL`**, never `ACTION_UP`, and carries the
   current frame size: at every end of a view, a pre-emption, a revocation, and a size change
   while a finger is held (a lift for the old size would be dropped and leave the app's finger
   down). Only a client's own `up` writes `ACTION_UP`. Ending a view never completes a click or a
   drop its user began.
6. **Records.** Each gesture is one operation of the capability that replays it: `device.tap`
   (a down…up within 1 % travel and 2,000 ms), `device.swipe` (at most 64 points; a longer
   gesture's times scaled into 5,000 ms), `device.pressKey`, or the new `device.scroll`
   (human only, not an MCP tool, shaped as the other three); state `completed`, `cancelled` (a
   cancel) or `failed` (`DEVICE_STREAM_FAILED`), with the view's origin and target, its Activity
   row and its trace line. It is recorded after the gesture, off the input's path: a stalled
   trace writer or publisher delays no input. The one loss is a record dropped when 256 gestures
   wait on the store's lock, counted and logged at most once a minute.
7. **The three capabilities stay** unchanged for the CLI, scripts and C5; the UI stops calling
   them.

## Consequences

- An input costs a frame on an open socket and a socket write: T1's targets are p50 ≤ 3 ms
  locally and ≤ 8 ms through a peer pair (plan m2y A1, A2). A drag and a wheel reach the device
  as they happen.
- A revoked device keeps receiving a view's input for up to 2.5 s after the policy changes,
  where ADR-0079 checked each call. The sweep is the bound.
- An input no longer queues behind an agent's `device.tapElement` on the write key; the finger's
  owner, not the queue, keeps two gestures from interleaving.
- Activity shows one row per gesture, not per input event; a record can be lost only under the
  store's lock contention, and the loss is counted.
- The schema gains `device.watchScreen`'s `control` and `device.scroll`, one minor version.
- Documents to change, in the steps that build them: `ui-automation.md` §Input primitives;
  `http-websocket.md` §`GET /v1/screen`; `peer.md` §The peer port.

## Alternatives considered

- **Keep each input a call, made cheaper** (cache the device list, skip `adb devices -l`): still
  an HTTP round trip and an operation per event, so a live drag stays a stream of operations; it
  removes about half of T1 and none of the drag's delay.
- **A separate control socket** beside the stream: a second attach, token check and lifetime for
  what the open socket already carries, and two sockets whose ends must be kept in step.
- **A per-input policy read**: a file read per move to close a window the 2 s sweep already
  bounds; it would win if a revocation had to take effect within a frame.
- **Revoking only the input** while the video runs on: the view was admitted by the same policy,
  so a device the policy no longer admits should not be shown either.
- **JSON frames**: a parse per move for no reader; the frames are fixed-size and the daemon is
  their only reader.
- **Passing scrcpy's own bytes through**: every control message (clipboard, power, app start)
  would be reachable from a browser.
- **A lift as `ACTION_UP`**: a click or a drop at revocation, done by the daemon, not the user.
- **Several fingers** (multi-touch): out of the milestone (register row); the wire's one finger
  is the rule it would extend.
- **A record per event**: a 2 s drag would be about 240 rows and as many trace lines.
