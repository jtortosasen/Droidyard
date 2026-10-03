# Which documents decide what

Read the whole ADR, not only the section named. Every area also has the owner decisions in `docs/reviews/2026-09-29-ratifications.md`, and each spike S1 to S9 is defined in `docs/reviews/2026-09-29-pre-implementation-review.md` §Assumptions that need a spike.

| Area | Documents |
| --- | --- |
| Every milestone | `docs/README.md` §Where the work stands · `docs/roadmap/mvp.md` (the phase, §Out of scope, §Definition of done) · `docs/architecture/capability-api.md` · `docs/architecture/core-runtime.md` §Package layout, §Adapter boundary, §What comes first · ADR-0003, ADR-0016 |
| Descriptors, schema, golden file | ADR-0016 · ADR-0010 (statefulness) · ADR-0015 §1 (untrusted derived from tags) · `capability-api.md` §Naming, §Classification, §Adding a capability · `internal/capability/`, `internal/schema/` |
| Invocation, operations, errors | ADR-0020 · `capability-api.md` §Invocation model, §Error model, §Idempotency · `core-runtime.md` §Invocation, §Concurrency |
| Daemon, socket, shims, sessions | ADR-0019 · ADR-0021 §Location, §Layout, §Locking · ADR-0024 (binary, subcommands, paths, env overrides) · ADR-0010 (what a session is) |
| MCP adapter | `docs/interfaces/mcp-tool-surface.md` · `docs/interfaces/mcp-features.md` · ADR-0020 §Adapter projections · ADR-0015 §6 (policy never over MCP) |
| HTTP / WebSocket | `docs/interfaces/http-websocket.md` §Security baseline · ADR-0006 |
| Workspaces and trust | `docs/architecture/workspace-state.md` · ADR-0015 §5 · ADR-0020 §Scope and workspace addressing · ADR-0021 |
| Processes and supervision | `docs/architecture/process-lifecycle.md` · ADR-0001 (process ownership, amended) · ADR-0021 §Pid records |
| Events | `capability-api.md` §Events and subscriptions |
| Artifacts | ADR-0012 §Durability classes, §Reading artifacts |
| Devices and UI automation | `docs/subsystems/adb-device.md` · `docs/subsystems/ui-automation.md` · ADR-0005, ADR-0012, ADR-0015 |
| Emulator | `docs/subsystems/emulator.md` |
| SDK | `docs/subsystems/sdk.md` |
| Gradle and `build.*` | `docs/subsystems/gradle.md` (§Spikes before the sidecar is written, §Testing) · ADR-0017, ADR-0018, ADR-0022 (§Known open item, before `build.errors`), ADR-0023 · `docs/research/spike-s1-error-formats.md` |

## Open questions that can block a plan

Most architecture, interface and subsystem documents end in §Open questions. Check the ones in your area before writing steps. Two are known to sit on the critical path:

- `capability-api.md` §Open questions: the config file (policy, trusted roots, `shell.exec`, JVM discovery, timeouts) has no specification, and "the format is needed before Phase 1".
- ADR-0022 §Known open item: AAPT2 merged-resource paths must be settled before `build.errors` is implemented.
