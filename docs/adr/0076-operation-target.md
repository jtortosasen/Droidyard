# ADR-0076: The operation's target on `operation.state`

- Status: Accepted
- Date: 2026-10-17
- Supersedes: ADR-0043 §2 (in part: the payload carries the call's target, a bounded exception to "never parameters")
- Relates to: ADR-0010, ADR-0036, ADR-0043 §1, §3, ADR-0048; plan m2p Q10
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated (plan m2p Q10, reviewed in two rounds). The owner chose the register rows this closes, 2026-10-17.

## Context

ADR-0043's Alternatives deferred a target on `operation.state` "if a mirror needs it", and
ADR-0048's Alternatives repeated the rejection. Two mirrors now need it (the register rows
plan m2p closes as R2 and R5):

- The UI tree and app-logs panels re-read on a terminal operation on any device, and the
  devices panel cannot tell which emulator an `emulator.stop` targets or which AVD an
  `emulator.start` boots. With the live view's taps (plan m2p step 6), every tap on one
  device would reload every other device's tree.
- The toolbar names this tab's selected module for any UI build, another tab's included,
  and names none for an agent's `build.assemble`.

The UI cannot derive the target itself: it does not see other clients' parameters.

## Decision

1. `operation.state`'s payload and `operation.watch`'s `ActiveOperation` rows gain an
   optional `target {device?, avd?, module?, variant?}`. Each field is a string of at
   most 200 bytes, omitted when empty; `target` is omitted when every field is.
2. The target is declared per descriptor (`Descriptor.Target`, a function of the call's
   validated parameters), recorded when the operation starts, and never changes. Set on:
   every `device.*` capability that takes a `device` (`device`); `emulator.stop`
   (`device`); `emulator.start` and `emulator.wipe` (`avd`); `build.assemble` (`module`,
   `variant`); `device.install` (`device`, `module`, `variant`). Every other capability
   has none, `build.run` included (its tasks are not a target).
3. The values are the caller's own validated parameters, never device or project text, so
   the field is not untrusted. ADR-0043 §2 otherwise holds: no other parameter, no
   result, message or cause on the event.
4. Streaming, so no MCP change (ADR-0010, ADR-0036). A minor version, in plan m2p's.

## Consequences

- The UI tree and app logs reload only on their own device's operations, or on one with
  no target; the devices panel names the device being stopped and the AVD being booted;
  the toolbar names the module of any client's `build.assemble`.
- A new capability that acts on a device, an AVD or a module declares its target, or its
  operations mirror as untargeted (a reload for everyone, as before).
- Documents to change: `capability-api.md` §Events and subscriptions, if it names the
  payload's fields; the register (R2, R5 closed by 2p), in plan m2p step 9 and the close.

## Alternatives considered

- **Every parameter on the event**: carries paths, text typed into a device and selectors
  to every watcher, which ADR-0043 rejected for good reason; the mirrors need four fields.
- **Deriving the target in the UI from its own calls**: blind to other tabs, the CLI and
  agents, which is exactly the case R2 and R5 describe.
- **A target only for `device`**: closes R2 but not R5; `module` and `variant` cost two
  optional fields on the same object.
- **A free-form `target` string**: each consumer would parse it; named fields are typed in
  the schema and in the generated client.
