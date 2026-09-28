# Research: `mobile-mcp` (Mobile Next)

- Repo: <https://github.com/mobile-next/mobile-mcp>
- License: Apache-2.0
- Language: TypeScript / Node.js (CommonJS, Node >= 20)
- Analyzed: 2026-09-27 (version observed: 1.0.5)
- Local clone: `.refs/mobile-mcp`

## What it is

An MCP server that lets an agent drive real and simulated **iOS/Android** devices
through one platform-agnostic tool surface. It is **accessibility-first**: it
reads the native UI/accessibility tree and acts on element refs, using
screenshots and coordinates only as a fallback (games, canvas, visual checks).

It is **not** a harness and makes no LLM calls — a pure MCP server. Since
v1.0.0 it is a thin TypeScript wrapper over an external Go binary (`mobilecli`).

This is the closest prior art to the "mydev as a portable MCP" goal: same
multi-assistant packaging problem, similar domain (devices, UI, screenshots,
logs, crashes).

## Architecture (summary)

- 32 flat tools, all prefixed `mobile_`.
- Platform specifics hidden behind a `Robot` interface with backends for
  Android (`adb`), iOS (`go-ios` + WebDriverAgent) and the default `mobilecli`.
- Robots cached per device id in-process.
- Explicit `device` argument on every tool: **no implicit current device**.
- `SERVER_INSTRUCTIONS` in the MCP initialize payload encodes operating policy.
- Optional single skill: `skills/mobile-automation/SKILL.md`.

## Tool surface (shape, not exhaustive)

Device/app lifecycle, screen interaction (tap by ref or coordinates, swipe,
type, hardware buttons, open URL), screenshots (to model or disk), screen
recording, UI element listing (text or JSON), orientation, GPS, clipboard,
device logs, crash reports, and a **batch** tool that runs a sequence in one
round trip.

## Reusable ideas (high value for mydev)

1. **Explicit target everywhere.** No hidden mutable "current device"; every call
   takes the device id. Makes multi-device and concurrent-agent use correct and
   debuggable. Strongly aligns with our `device.*` design.
2. **Accessibility tree first, refs over coordinates.** `listElements` then
   `tap(ref)` is deterministic, token-cheap and survives layout shifts. This is
   the model for our `device.uiTree` / `device.tapElement`.
3. **Compact text serialization of the tree** (~5x smaller than JSON) with a
   self-describing header. Directly informs our structured-vs-raw and
   token-economy choices in `capability-api.md`.
4. **MCP `instructions` in the initialize payload.** A cheap, assistant-agnostic
   way to encode policy (pick target first, prefer the tree over screenshots,
   batch, release resources) with no skill required. Cheap and portable.
5. **Batch tool** that bypasses transport per step and returns one combined
   result — useful to cut round trips. Note its deliberate exclusions (no
   nesting, no image-returning tools) and per-step schema validation.
6. **Coordinate-mapping hint appended to screenshots** (downscale / point-vs-pixel
   mismatch): one sentence of plain language prevents a classic tap-miss.
7. **Tool annotations as a tested contract** (`readOnly`/`destructive`/`openWorld`).
8. **Cross-assistant packaging kit**: one canonical `npx -y pkg@latest` string,
   per-assistant snippets (Claude, Cursor, Codex, opencode, Goose, Copilot,
   Gemini…), deeplinks, `llms-install.md`, MCP registry `server.json`, MCPB and
   Smithery bundles, plus one optional skill. This is exactly our distribution
   problem; it is worth copying the *shape*.
9. **Thin adapter over a capability interface.** Mirrors our capability API:
   keep platform specifics behind one interface, expose via a thin MCP adapter.
10. **Actionable, typed errors** with "how to fix" guidance, distinguishing
    recoverable from fatal.

## Things to avoid / adapt

1. **Black-box external binary.** `mobilecli` is pulled in transitively via
   `mobilewright` (never imported directly) — confusing and fragile. We invoke
   our own Go core through a clean internal API, not an assumed CLI.
2. **Per-call process spawn.** `execFileSync` per tool call is slow and blocks the
   event loop. A resident core with an internal socket/IPC (or the UI's HTTP/WS
   layer) is a better fit — and is what we already plan.
3. **Stateless HTTP that silently breaks stateful features.** In streamable HTTP
   mode it creates a fresh server per request, so cross-request state (screen
   recording) *cannot* work. We must decide statefulness explicitly per
   capability; our workspace model and `operation.*` handle this, but we should
   document which capabilities are stateful.
4. **Legacy/dead backends left in-tree** (`Simctl`, `WebDriverAgent` unreachable;
   `AndroidRobot`/`IosRobot` deprecated). Extra maintenance and security surface.
   Keep one live path.
5. **Telemetry on by default** with hard-coded keys and hashed host identity. If
   we ever send telemetry, make it **opt-in**.
6. **Version patches via `sed`** across several manifests, which disagree in-repo.
   Generate all manifests from a single source of truth.
7. **Tool-name sprawl** (32 flat tools). Fine at this size; as we grow, namespace
   and keep the exposed set small — large tool lists degrade many clients.
8. **Naive gesture emulation** (double-tap = two taps, long-press = zero-distance
   swipe). Implement real primitives in the core.
9. **Platform naming leaks** (`packageName` reused for iOS bundle ids; Android-only
   button names). Define platform-neutral capability names and map internally.
10. **Batch annotation accuracy** (`destructiveHint:true` even for read-only
    sequences). If we build batching, derive accurate semantics.

## Verdict for mydev

Adopt the *shape*, not the implementation:

- Keep a `Robot`-equivalent internal interface (our capability API) with
  per-backend implementations.
- Expose an MCP adapter that takes explicit targets, prefers the accessibility
  tree with stable refs and compact text output, appends screenshot coordinate
  mapping, supports an internal batch call, and publishes `instructions`.
- Package as one self-contained binary with per-platform release artifacts and a
  one-line installer, plus assistant-specific config snippets, `llms-install.md`
  and one optional skill.
- Do **not** adopt a per-call external CLI dependency or default-on telemetry.

Related: our planned `subsystems/ui-automation.md` and
`interfaces/mcp-tool-surface.md` should incorporate these findings.
