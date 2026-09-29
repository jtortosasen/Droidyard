# Research: `codex-android-mcp` (zifanersuotang)

- Repo: <https://github.com/zifanersuotang/codex-android-mcp>
- License: MIT (dual copyright: ZSeven-W and zifanersuotang)
- Language: TypeScript (ESM), Node >= 20.11, official MCP SDK
- Analyzed: 2026-09-27 (version 0.1.0)
- Local clone: `.refs/codex-android-mcp`

## What it is

A **local-first, STDIO-only MCP server** that exposes Android device control over
`adb` to an LLM agent. It is a **derivative of `dsh-android`**: the same Android
domain logic, with the DSH/Cordis plugin registration and web panel replaced by
the official MCP SDK plus a startup policy layer.

That lineage is the perfect illustration of our thesis: the *same* capability
library, re-packaged for a different consumer. It proves the Android logic is
portable and that the packaging layer is replaceable.

19 tools by default (+1 opt-in `android_build_run`). No raw `adb`, no shell, no
arbitrary command, no HTTP endpoint, no web panel. The README states outright that
it does **not** reproduce dsh-android's live sidebar.

## Architecture

- `index.ts` → `serveStdio(factory)`; a **fresh `McpServer` per connection**
  supports both modern (2026) and legacy (2025) protocol negotiation.
- Clean seam: `defineTool()` produces a plain `ToolDefinition`; a separate adapter
  (`server.ts` + `mcp-schema.ts`) converts it to JSON Schema and registers it.
- `policy.ts`: authorization **re-checked immediately before each call**
  (re-discovers devices, validates the exact target — mitigates TOCTOU),
  explicitly separated from annotations ("annotations are usability hints, not an
  authorization boundary").
- Default policy: **emulator-only**; physical devices require an enable flag + an
  allowlisted serial + the serial on every call, and are hidden from listings.
- Single stream slot + consumer refcount + 5-min idle timeout + keep-alive;
  all writes serialized through one promise queue.

## Tool surface (19 + 1)

Core/app: `devices`, `boot`, `shutdown`, `screenshot`, `interact`, `list_apps`,
`launch_app`, `build_run` (opt-in).
UI tree: `ui_tree`, `tap_element`.
Rows: `ui_rows`, `tap_row`.
OCR (macOS only): `find_text`, `wait_for`, `tap_text`.
Logs: `logs`.
Debug: `processes`, `backtrace`, `meminfo`, `app_info`.

Coordinates are **normalized 0..1 of the live frame**. Target arg naming is
**inconsistent** (`device` for core/log/debug, `serial` for UI/row/OCR).

## Reusable ideas (high value for Droidyard)

1. **Capability definition decoupled from the transport adapter.** Exactly the
   seam our "one capability API, two consumers" needs. Write capabilities once,
   add MCP and HTTP/WS adapters over the same definitions.
2. **Strict, limit-hardened input schemas**: `additionalProperties:false`, derived
   `required`, and limits that match what the runtime enforces.
3. **Conservative MCP annotations + a dense server-level `instructions` block**
   (pick device first, emulator by default, treat device data as untrusted, no raw
   adb). Cheap and portable.
4. **Authorization as a distinct boundary, re-checked right before execution**,
   separate from annotations.
5. **Emulator-by-default device policy** with explicit allowlisting.
6. **"Empty is a fact, not a failure"**: failed listings throw; only a successful
   listing can return `count:0`; zero matches carry candidates + a hint.
7. **Bounded output with `truncated` + narrowing hints**, enforced *while*
   capturing (40 KB trees/rows/OCR, 300-line/30 KB log ring, 200-line backtraces).
8. **Cause-attributed, actionable hints** (filter matched nothing vs. cap pruned vs.
   genuinely no accessibility info — only the last routes to OCR).
9. **Pure parsers as standalone modules** (`uitree.ts`, `list-rows.ts`,
   `debug-parse.ts`, `app-list.ts`) driven by fixtures with no device. Maps cleanly
   to Go and is highly testable.
10. **Interaction closes the verify loop in one call**: every tap returns a
    post-settle screenshot and can assert `expect_text`/`expect_gone` or
    `expect_count` (with an anti-probe guard that refuses before tapping an unknown
    counter).
11. **Identity-first element resolution** with candidate lists, chain folding,
    off-screen/disabled gates and relative row taps.
12. **Process-lifecycle discipline**: process-group reaping, combined client signal
    + per-tool timeout (`AbortSignal.any`), bounded child output, one slow consumer
    must not stall fan-out.
13. **Real STDIO protocol tests** (spawn the binary; assert protocol eras, tool
    count/order, annotations) and an in-memory image round-trip test.
14. **Build/run specifics**: wrapper-first Gradle; applicationId from
    `output-metadata.json` (never guess, no `aapt2`); filtered Gradle tail on
    failure; depth-bounded APK search; `monkey` launch; `adb install`
    `Failure [...]` detection even on exit 0; Windows `taskkill /T /F`.

## Things to avoid

1. **Orphaned code from the previous harness** (`MultipartFrameWriter`, the
   `vision.ts` attachment/`presentationMeta` DSL) left half-wired. We want a stream
   *and* a UI, so define one capability API with two clean adapters — not two
   half-wired presentation layers.
2. **Inconsistent target naming** (`device` vs `serial`). Unify to one.
3. **Overloaded tools** (`android_interact` with 5 actions, conditional fields and a
   `json` blob). Prefer discriminated tools / tagged unions with per-action
   schemas.
4. **Output schemas declared but never advertised** to MCP clients. If we promise
   typed results, register them.
5. **Very long tool descriptions**; keep them concise plus shared instructions.
6. **Duplicated, drift-prone metadata** (`READ_ONLY_TOOLS`/`DESTRUCTIVE_TOOLS`
   duplicated in tests; `isConcurrencySafe` declared but unused). Compute once.
7. **macOS-only OCR** (runtime-compiled Swift/Vision) as a hard portability wall.
   Make OCR an optional, pluggable backend that degrades gracefully.
8. **Path-based screenshot plumbing** (write temp file, encode, delete, leak `path`
   in text). Prefer in-memory bytes with a stable opaque reference.
9. **Env-var-only, startup-frozen policy**. Expose policy via the capability
   API/config store with live updates.
10. **Single stream slot + global write serialization**. Plan for concurrent
    devices with per-device locking.
11. **STDIO-only transport**. Design transports as adapters over the capability
    API, not a single-transport assumption.
12. **Branding/lineage leakage** in internal errors. Clean naming from day one.

## Legal note

MIT. The Android domain modules are legally reusable; we would port the logic to Go
and keep attribution in a third-party notices file.

## Verdict for Droidyard

The repo validates the thesis: a **standard-MCP STDIO server plus adb/Gradle
tooling is portable across assistants today** — "Codex-specific" is essentially
branding and docs. Along with `dsh-android` (its ancestor), it is the best source
of *Android domain logic and safety patterns* (authorization boundary, bounded
output, cause-attributed hints, one-call verify loop), while its
transport/presentation seam is exactly what we should rebuild cleanly.
