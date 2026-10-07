# ADR-0015: Untrusted device data and an explicit authorization boundary

- Status: Accepted
- Date: 2026-09-27
- Amended: 2026-09-29. What changed: how the untrusted mark travels; the project-root
  trust gate; policy changes limited to human channels; `shell.exec` off by default;
  the local transport baseline (review N4, N14, N15). Items 5–7 were **ratified on
  2026-09-29**.
- Amended: 2026-10-05 (clarification of §1, plan m1f D5, Delegated). A failed call can
  carry device text too (a resolution error's candidates). It is marked only when its
  handler marks it, never by the result's derived flag: `meta.untrusted` on the native,
  HTTP and WebSocket error envelopes, `_meta` over MCP with the error JSON alone as the
  text (ADR-0020 §Adapter projections unchanged), and the capability layer appends a
  fixed warning to the error's `message`. The decision of §1 is unchanged.
- Amended: 2026-10-07 (Delegated, plan m1k Q1, [ADR-0032](0032-configuration-file-v1.md)).
  §6's defence, "our agent-facing surface offers no path to it", holds only while
  `shell.exec` is off for the root: once a human enables it, the agent has the user's
  reach, policy included, and the defence is the human's informed choice.
- Amended: 2026-10-07 (Delegated, m1k step 2 review; corrects the line above). The
  defence did not hold even with `shell.exec` off: on a **trusted** root `build.*` runs
  the project's build scripts, which an agent can edit with `file.write` (1j), so
  trusting a root already lets an agent run code as the user there, droidyard's
  configuration included. `shell.exec` makes that direct; it does not create it. For
  both, the defence is the human's choice, trusting a root and enabling the shell; an
  untrusted root runs no project code. The human is told at each: `droidyard trust`
  prints what trusting means, and `POLICY_SHELL_DISABLED` what enabling the shell means. No fence on `file.write` is attempted
  (the configuration lives outside every root, and a build script needs no file tool).

## Context

Everything read from a device or an app is **attacker-controllable** if the app under
test is hostile. That covers UI tree labels, log lines, the clipboard, screenshots and
package metadata. `codex-android-mcp` handles this with a mature policy layer:

- it re-checks authorization immediately before each call, which mitigates TOCTOU;
- it keeps that boundary separate from MCP annotations, which it treats as usability
  hints only, never as enforcement;
- it defaults to emulators and requires an explicit allowlist for physical devices.

The first version of this ADR left four holes:

- **How untrusted data is marked.** It never said how the mark travels.
- **Project code.** It covered devices only, but Gradle executes the project's build
  logic. Opening a repository you have not vetted is a code-execution decision.
- **Policy changes.** It called policy "live-configurable through the capability
  API", but no capability existed. Adding one would expose it over MCP, so an agent
  could grant itself physical devices or `shell.exec`.
- **The HTTP listener.** It said nothing about a loopback HTTP listener that any web
  page can reach.

## Decision

1. **Device and app output is untrusted data.** It is declared **per field** and
   signalled **per result**:
   - Fields derived from the device or app are tagged in their Go result struct. The
     generated JSON Schema, including the MCP `outputSchema`, carries
     `"x-droidyard-untrusted": true` on them.
   - A descriptor's `Untrusted` flag is **derived**: it is true if any field of the
     result is tagged.
   - Each adapter signals the mark per result. MCP uses `_meta` plus a fixed header
     and quoted strings in the text rendering
     ([mcp-tool-surface](../interfaces/mcp-tool-surface.md) §Untrusted data). HTTP
     uses `meta.untrusted` in the response envelope, and the UI renders tagged fields
     inert ([http-websocket](../interfaces/http-websocket.md) §Security baseline).
   - An artifact inherits the mark of the capability that produced it.
   - We do not attempt heuristic "anti-prompt-injection" sanitisation.
   - Project content (source files, build output) is not marked. It is the user's own
     code, which the agent already reads through its own tools.
2. **Authorization is a distinct boundary.** It is re-validated immediately before
   execution, separately from annotations.
3. **Annotations are hints.** They serve the client's approval UX and are never
   enforcement.
4. **Default device policy: emulator-preferred.** A physical device requires an enable
   flag, an exact allowlist entry, and its serial on every call. Device listings are
   filtered by policy.
5. **A project root must be trusted before its code runs.** Capabilities that make
   Gradle configure or run the project (`project.*`, `build.*`, `sdk.requirements`)
   need a trusted root. They are re-checked before execution like any other
   authorization, and otherwise fail with `WORKSPACE_UNTRUSTED`, whose nextStep names
   the human action.
   - `workspace.open` itself **runs no project code**. An untrusted root opens in a
     restricted mode: `file.*`, `git.*` and `workspace.describe` work, and `describe`
     reports `trusted: false`.
   - Git runs on an untrusted root with the repository's executable configuration
     neutralised: `-c core.fsmonitor=false -c core.hooksPath=/dev/null`.
   - A root becomes trusted if it falls under a prefix in the user's configuration,
     or through an explicit human act (the UI, or `droidyard trust <root>`). The decision
     is recorded in the user configuration, as policy
    ([ADR-0021](0021-persistence-format-and-location.md)).
   - An MCP binding never grants trust, whether it comes from `--root` or from the cwd.
     A server configuration can be written by the repository itself (`.mcp.json`).
   - MCP roots may **narrow** what is allowed and never widen it
     ([mcp-features](../interfaces/mcp-features.md) §5).
6. **Only humans change policy, and the trust boundary is the user account.**
   - Policy covers the device rules, trusted roots and `shell.exec`. It lives in the
     user configuration and changes without a restart, through human channels only:
     - the UI and the CLI, through `policy.get` / `policy.update`;
     - hand edits to the file, picked up on reload.
   - These capabilities have **audience `human`**. The MCP adapter does not list them,
     and the core rejects them from an MCP session by the session's client kind. This
     is enforcement, not just omission from the list.
   - This is the one deliberate exception to "every capability reaches both consumers"
     ([principles](../vision/principles.md) #1): the agent must not be able to
     authorize itself.
   - **Residual risk.** A process running as the same user can connect to the socket
     or read the HTTP token. That includes an agent's own shell, which could run
     `droidyard call policy.update`. Same-uid code is inside the trust boundary. The
     defence is that our agent-facing surface offers no path to it, and that an
     agent's own shell commands pass through its host's approval.
7. **`shell.exec` is disabled by default.** Policy enables it globally or per
   workspace. The tool stays listed when disabled, so the tool list is stable across
   policy changes and prompt caches. A call fails with `POLICY_SHELL_DISABLED`.
8. **Local transports have a baseline.** The Unix socket (`0600`, peer-uid check) is
   defined in [ADR-0019](0019-process-topology-daemon-and-shims.md). The HTTP listener
   is loopback-only and checks Host and Origin, with a per-launch bearer token and no
   cookies ([http-websocket](../interfaces/http-websocket.md) §Security baseline).

## Consequences

- Safer defaults for agents operating on real devices and on unvetted repositories.
- Each call pays for re-validation. The cost is small and worth it.
- Consumers can tell trusted control data from untrusted payloads at both levels: the
  schema marks the fields, and every result carries a mark.
- Policy changes do not require restarting the core, and the agent cannot make them.
- Trusting a project once adds friction to the first agent session in it. The
  mitigations are a configured trusted prefix (e.g. `~/src`), and an error whose
  nextStep is the exact `droidyard trust <root>` command. Elicitation is deliberately not a
  mitigation, for the reason given in *Alternatives*.
- Descriptors gain an `Audience` (`all | human`), and `Untrusted` becomes derived
  ([core-runtime](../architecture/core-runtime.md) §The capability registry).

## Alternatives considered

- **Annotations as enforcement.** They are client-side hints and unsafe as a boundary.
  Rejected.
- **Content sanitisation or injection heuristics.** Unreliable, and a false sense of
  safety. Rejected in favour of marking, instructions and policy.
- **A policy frozen at startup, set only through environment variables.** This is the
  codex-android-mcp limitation. Rejected in favour of live configuration.
- **Per-result marking only.** The UI could not tell which fields to render inert.
  Rejected.
- **Per-field wrapping inside payloads** (`{"untrusted": "…"}` on every string).
  Noisy in every payload and schema, and it changes no behaviour. Rejected.
- **Policy mutation over MCP, gated by elicitation**, including trusting a root.
  Elicitation support varies, and a client in auto-approve mode would let the agent
  approve itself. Rejected.
- **Trust on first open.** Opening *is* the act an injected instruction would trigger.
  Rejected.
- **`shell.exec` enabled by default** for a single-user setup. It makes the widest
  capability the default. It also contaminates the MVP comparison, because the agent
  falls back to shell instead of semantic tools. Rejected; one policy toggle away.
