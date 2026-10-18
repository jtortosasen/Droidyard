# ADR-0079: Input from the live view: `device.tap`, `device.swipe`, `device.pressKey`

- Status: Accepted
- Date: 2026-10-17
- Relates to: ADR-0010, ADR-0015 §2, §4, ADR-0035, ADR-0036, ADR-0060 §2, ADR-0077, ADR-0078; `ui-automation.md` §Input primitives, §Design rules; plan m2p Q5, Q6; [`spike-screen-stream-2026-10.md`](../research/spike-screen-stream-2026-10.md)
- Decided by the owner, 2026-10-17: every input from the view is a capability call, with the policy, the audit and the operation trace an agent's call has, so a later recorder (C5) sees each action. The path behind the calls is Delegated (plan m2p Q5, Q6, reviewed in two rounds).

## Context

The view needs taps, long presses, swipes and the system keys at a person's pace. Today's
input is `device.tapElement` (by selector) and `device.inputText`, through the UI reader
(ADR-0035: 23–435 ms quiet, blocked 11.2 s on a frozen app), with `adb shell input`
(a JVM per call, ~0.65 s) as fallback. `ui-automation.md` §Input primitives reserved
coordinate capabilities "normalised 0..1". scrcpy's control socket (ADR-0077) is open
whenever a view is: in the spike, 20 of 20 taps landed on both API levels, a write took
10–110 µs, and the next video bytes followed the touch-up at p50 22–42 ms, p95 141–195 ms.

## Decision

1. **Three capabilities**, kind command, global, audience `human` (ADR-0060 §2: refused
   from an MCP session with `REQUEST_NOT_ALLOWED_HERE`), resource the device's write key,
   `authorize` the device policy (ADR-0015 §2, §4), result `{delivered: true,
   warnings?}`:
   - `device.tap {device, x, y, durationMs?}`: `x`, `y` in 0..1; `durationMs` 0–2,000
     (a long press);
   - `device.swipe {device, path}`: 2–64 points `{x, y, t}`, `t` in ms from the first,
     strictly increasing, at most 5,000;
   - `device.pressKey {device, key: back | home | appSwitch | enter | delete}`.
2. **Coordinates** are normalised against the stream's frame size, which follows
   rotation (a new size comes with each encoder restart, ADR-0078).
3. **Delivery** is the device's running source (`Source.Inject`), never a new start: with
   no live view, `DEVICE_NO_STREAM` (nextStep "open the device's live view"), nothing
   sent. A failure before the first byte is `DEVICE_STREAM_FAILED`; after it, the call
   completes with a warning (`ui-automation.md` §Design rules).
4. **Text** stays `device.inputText` (Unicode through the reader).
5. **Agents get nothing new**: the three are not MCP tools (ADR-0036's budget unchanged);
   coordinate input for agents is decided with C5's exploration.

## Consequences

- Each input costs, above a bare socket write, the loopback HTTP round trip, the
  operation bookkeeping and trace line, and `authorize`'s `adb devices -l` (p50 49 ms,
  p95 56 ms in the spike). A tap queues behind an agent's `device.tapElement` on the same
  device's write key, up to `OPERATION_QUEUE_TIMEOUT`. A drag reaches the device only
  when released, replayed with its timing. The measured path is in `ui-automation.md`
  §Input primitives after plan m2p step 7.
- A recorder later hooks the operation path, where every input already passes.
- `DEVICE_NO_STREAM` joins the codes.
- Documents to change: `ui-automation.md` §Input primitives; in plan m2p step 10.

## Alternatives considered

- **Through the UI reader**: synchronous and blocked by a frozen app (11.2 s), and new
  swipe and key verbs with a dex rebuild; the control socket is already open.
- **`adb shell input`**: a JVM per call, ~0.65 s or more.
- **A side channel from the UI straight to the control socket**: the lowest latency, but
  the input would bypass the policy, the audit and the trace, against the owner's rule.
- **Live drag tracking** (a stream of touch events, each an operation): too many
  operations for one gesture; a later minor change on this ADR's shape if a person needs
  it.
- **Pixels instead of normalised coordinates**: the client would need the device's size
  and rotation, which the stream already carries.
