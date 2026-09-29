# Research: Prior Art Synthesis

Cross-cutting conclusions from four external repositories, and what they imply for
Droidyard. Per-repo detail: [`dsh-android.md`](dsh-android.md),
[`codex-android-mcp.md`](codex-android-mcp.md),
[`mobile-mcp.md`](mobile-mcp.md), [`android-skills.md`](android-skills.md).

> Status: draft. Feeds ADRs and the `capability-api`, `ui-automation`,
> `adb-device`, `mcp-tool-surface` and `skills` documents.

## The four references

| Repo | What it is | Packaging model | License |
| --- | --- | --- | --- |
| `dsh-android` | Android capability + live device panel | **Plugin** for one harness (DSH) | MIT |
| `codex-android-mcp` | Same lineage, re-packaged | **STDIO MCP server** | MIT |
| `mobile-mcp` | Mobile automation (Android+iOS) | **MCP server**, stdio + stateless HTTP | Apache-2.0 |
| `android/skills` | 25 Android skills | **Agent Skills** (markdown) + manifests | Apache-2.0 |

`dsh-android` and `codex-android-mcp` share lineage: the *same* Android domain
logic, packaged once as a harness plugin and once as an MCP server. This is the
strongest single piece of evidence for our core bet — **the capability layer is
separable and the packaging layer is replaceable**.

## Convergent findings (multiple repos agree)

1. **Accessibility/UI tree first, identity refs over coordinates** (mobile-mcp,
   dsh-android, codex-android-mcp).
2. **Assert the effect of an interaction in the same call**
   (`expect_text`/`expect_gone`/`expect_count`) to cut round trips.
3. **Bounded, structured output** with a `truncated` flag and narrowing hints.
4. **Honest degradation; "empty is a fact, not a failure"**; cause-attributed
   errors that tell the agent what to do next.
5. **Device identity is explicit** on every call; no hidden "current device".
6. **Coordinates normalized against the live frame** (rotation-safe, no math).
7. **One persistent `screencap` child** beats per-frame spawning by a wide margin.
8. **Skills are portable markdown**, separate from tools; the de-facto standard is
   `agentskills.io` (`SKILL.md` + frontmatter).
9. **Cross-assistant packaging = one canonical command + per-assistant snippets +
   optional skill + MCP `instructions`.**
10. **Build/run details**: wrapper-first Gradle; applicationId from
    `output-metadata.json`; `monkey` launch; detect `adb install` failure on exit 0.
11. **Process discipline**: process groups, explicit timeouts, no orphans.

## Divergent choices (where they differ)

| Axis | dsh-android | codex-android-mcp | mobile-mcp |
| --- | --- | --- | --- |
| Transport | plugin HTTP on host origin | STDIO only | stdio + stateless HTTP |
| State | host-owned, refcount + idle | in-process, single stream slot | per-request in HTTP (state lost) |
| Packaging | harness plugin | source + Node | npm `npx`, MCPB, Docker |
| Multi-device | one stream | one stream, serialized writes | explicit per call |
| Policy | harness-managed | emulator-default allowlist | none |

## What this validates in our design

- **Capability API vs. their seams.** Their `Robot` interface / `ToolDefinition` /
  `qa-driver` is the same idea we formalized in
  [`capability-api.md`](../architecture/capability-api.md).
- **Workspace + `operation.*` vs. their stream slots.** Their refcount + idle
  timeout + keep-alive is a special case of our process-lifecycle and operations.
- **Adapters.** Their transport seams confirm our "protocols are edges" rule.
- **Go core.** `mobile-mcp` delegates the heavy lifting to a Go binary
  (`mobilecli`) while `dsh-android`'s domain layer is host-agnostic — both point to
  a lean core with thin adapters.

## Decisions this research implies (candidates)

Each item below is a concrete change/addition to make, with a proposed home.

### New ADRs

1. **Statefulness is explicit per capability.** Bounded/streaming capabilities
   (logcat, screen, build follow) declare they need a live session; stateless HTTP
   must not silently break them. Hosts: `architecture/capability-api.md`.
2. **Skills follow the Agent Skills standard** (`agentskills.io`), generated
   per-assistant manifests, no bundled scripts, no Android CLI dependency.
   Hosts: a new `docs/skills/` area + ADR.
3. **Screenshots/streams as in-memory bytes with opaque refs**, not filesystem
   paths.
4. **OCR is an optional, pluggable backend**, never a runtime-compiled macOS wall.
5. **Distribution model**: per-platform artifact + one-line install + per-assistant
   snippets + `llms-install.md` + one optional skill; telemetry off (or strictly
   opt-in).
6. **Untrusted device data** is tagged at the API boundary (MCP instructions +
   explicit result marking).

### Updates to existing documents

- `capability-api.md`: add cross-cutting conventions — bounded output +
  `truncated` + narrowing hints; "empty is a fact"; typed, cause-attributed errors;
  advertise output schemas; unified `device` parameter naming; discriminated tools
  instead of overloaded ones.
- `principles.md`: add "empty is a fact", "bounded output", and "authorization is a
  boundary, not an annotation".
- `process-lifecycle.md`: add per-device locking and concurrent-device support
  (reject single stream slot as a v1 limitation).
- `subsystems/ui-automation.md`: encode identity-first resolution,
  one-call verify, row abstraction, pure parsers.
- `interfaces/mcp-tool-surface.md`: annotations, server `instructions`,
  explicit targets, output schemas.

## Safety and policy model worth adopting

`codex-android-mcp`'s `policy.ts` is the most mature model seen:

- **Emulator by default**; physical devices require enable + exact allowlist +
  per-call serial.
- **Authorization is a boundary**, re-checked immediately before execution
  (TOCTOU-safe), explicitly distinct from tool annotations (which are hints for
  client approval UX, not enforcement).
- Device listing is filtered by policy; unauthorized rows degrade gracefully.

## Licensing summary

- `dsh-android`, `codex-android-mcp`: MIT.
- `mobile-mcp`, `android/skills`: Apache-2.0.

Porting logic to Go is permitted in all cases with attribution and licence
retention; keep third-party notices and avoid implying endorsement.

## Open questions

- Do we adopt MCP `outputSchema` fully (typed structured results) even though some
  clients ignore it?
- Which capabilities are stateful, and how do we express that in discovery?
- Do we ship OCR at all in v1, and via which portable backend?
- How early do we support multiple concurrent devices?
- Do we publish to npm (a wrapper) or ship a native Go binary with per-platform
  releases (or both)?
