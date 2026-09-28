# ADR-0015: Untrusted device data and an explicit authorization boundary

- Status: Accepted
- Date: 2026-09-27

## Context

Everything read from a device or app (UI tree labels, log lines, clipboard,
screenshots, package metadata) is **attacker-controllable** if the app under test
is hostile. `codex-android-mcp` handles this with a mature policy layer: it
re-checks authorization immediately before executing each call (mitigating TOCTOU)
and explicitly separates that boundary from MCP annotations, which it treats as
usability hints only, not enforcement. It also defaults to emulators and requires
an explicit allowlist for physical devices.

## Decision

1. **Device/app output is untrusted data.** It is tagged as such at the API
   boundary, and the MCP `instructions` tell agents not to treat it as commands.
   We do not attempt heuristic "anti-prompt-injection" sanitisation of content.
2. **Authorization is a distinct boundary**, re-validated immediately before
   execution, separate from annotations.
3. **Annotations are hints**, for client approval UX, never enforcement.
4. **Default device policy: emulator-preferred.** Physical devices require an
   enable flag, an exact allowlist, and the target on every call; device listings
   are policy-filtered.
5. Policy is **live-configurable** through the capability API/config store, not
   frozen at startup via environment variables.

## Consequences

- Safer defaults for agents operating on real devices.
- A per-call re-validation cost (cheap, and worth it).
- Consumers must be able to distinguish trusted control data from untrusted device
  payloads in results.
- Policy changes do not require restarting the core.

## Alternatives considered

- **Annotations as enforcement** — they are client-side hints; unsafe as a
  boundary. Rejected.
- **Content sanitisation / injection heuristics** — unreliable and a false sense of
  safety. Rejected in favour of tagging + instructions + policy.
- **Startup-frozen env-only policy** — the codex-android-mcp limitation; rejected in
  favour of live configuration.
