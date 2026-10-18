# ADR-0103: A shared serial is refused; a product makes an emulator only on an emulator's serial

- Status: Accepted
- Date: 2026-10-18
- Relates to: ADR-0015 §4, ADR-0032 §2, ADR-0099 §1, ADR-0100 §1; `adb-device.md` §Device model, §Policy and safety; plan m2t Q7
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated (plan m2t Q7, reviewed in two rounds). The owner kept both register rows for 2t (2026-10-18).

## Context

Two register rows weaken the physical-device policy, and lending (ADR-0099) builds on it:
devices sharing a serial (`0123456789ABCDEF`, common on cheap phones) are admitted or
refused together, since the allowlist names serials; and off USB a device reporting
`product:sdk_gphone*` is classified an emulator, bypassing the policy.

## Decision

1. **A serial listed twice** by `adb devices -l` is refused with its reason
   (`DEVICE_NOT_ALLOWED`, cause naming the duplicate) for both devices, and is never lent.
2. **Classification.** A `usb:` field makes a device physical, first, as today.
   Otherwise a product of the emulator list makes an emulator only on an
   `emulator-<port>` or a loopback serial (`127.0.0.1:<port>`, `localhost:<port>`);
   anything else is physical.

## Consequences

- `adb-device.md` §Device model and §Policy and safety change accordingly; the shared
  serial row closes, the `sdk_gphone` row narrows to a loopback serial.
- Two identical phones on one host cannot be used until one is unplugged or its serial
  changed.

## Alternatives considered

- **Identity by USB path.** It changes with the port and is absent off USB, so an
  allowlist entry would not survive a replug.
- **Classification by serial alone.** Some local emulators attach as `127.0.0.1:<port>`
  with no `emulator-` serial; the product is still needed there.
