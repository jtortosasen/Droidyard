# Research: `dsh-android` (ZSeven-W)

- Repo: <https://github.com/ZSeven-W/dsh-android>
- License: MIT
- Language: TypeScript (host + React/TSX client), Swift for OCR helper
- Analyzed: 2026-09-27 (version observed: 0.1.0-rc.8)
- Local clone: `.refs/dsh-android`

## What it is

A **DeepSeek Harness (DSH) plugin**, not an MCP server and not a standalone app.
Installed into a DSH profile, it puts a live Android device into a DSH
conversation and gives the agent 20 `android_*` tools plus a streamed device
panel in the DSH web UI. There is **no MCP anywhere** in the codebase.

It is the concrete example of the approach we are deliberately *not* taking: rich
Android capability, but delivered as a plugin for one harness.

Its most important property for us: **roughly half the repo is a host-agnostic
Android capability library** that happens to be wrapped in a DSH plugin. The
`./driver` export and the builtins-only modules prove the isolation.

## Architecture (summary)

- Host glue (`index.ts` + tool families) registers tools/skill/stream routes
  through the cordis plugin runtime.
- `AndroidHostController` owns **one stream slot**: one persistent
  `adb exec-out 'while :; do screencap -p; done'` child, a PNG frame splitter, a
  latest-frame buffer and a `multipart/x-mixed-replace` writer (~8 fps on an
  emulator, 2–5 fps over USB).
- Server-side frame fan-out with consumer refcount + idle timeout (5 min) +
  keep-alive.
- Web UI (`src/client/`, 31 React modules) mounts into DSH's module loader and
  renders the device panel and tool cards.
- Visual bytes cross the DSH webserver origin under signed routes protected by
  HMAC capabilities + a triple loopback fence.

## Tool surface (20 tools)

Device/stream: `devices`, `boot`, `shutdown`, `screenshot`, `interact`,
`list_apps`, `launch_app`, `build_run`.
UI tree: `ui_tree`, `tap_element`.
Lists/rows: `ui_rows`, `tap_row`.
OCR (macOS only): `find_text`, `tap_text`, `wait_for`.
Logs: `logs`.
Debug: `processes`, `backtrace`, `meminfo`, `app_info`.

Interaction coordinates are **normalized 0..1 of the live frame**, which removes
rotation inverse-math entirely.

## The skill

Exactly one skill, `android-ui-automation`, and it is **not a file**: it is a
TypeScript string constant registered via DSH's skills service. Content is
~80 lines of markdown that teach the *workflow between tools* (observe once →
act with an assertion → observe again if needed), a cost table per reader,
"resource-id is the most stable handle", "never guess a package name", confirming
taps with `expect_text`/`expect_gone` and list actions with `expect_count`.

This prose is harness-independent and is the perfect candidate for an optional
skill shipped by us across assistants.

## Host coupling vs portable

**DSH-specific (would be discarded):** cordis plugin registry/lifecycle
(`apply`/`effect`/`inject`), the `@deepseek-ai/dsh-tools` schema DSL, the
`presentationMeta` projection, the React client and its slots, `DSH_HOME` /
`DSHPLUGIN_` config model, packaging.

**Portable (only Node builtins):** `adb.ts`, `frame-source.ts`,
`android-host.ts`, `stream-access.ts`, `uitree.ts`, `list-rows.ts`,
`debug-parse.ts`, `build-run.ts`, `app-list.ts`, `qa-driver.ts`, and the skill
prose.

`qa-driver.ts` is effectively a ready-made capability-API shape:
`discover / foregroundApp / launchApp / observe / tap / type / scroll / key /
screenshot / dispose`, explicit serial, `coordinateSpace: "display-pixels"`.

## Reusable ideas (high value for AioDevAndroid)

1. **Serial is the only device identity.** One adb code path; no emulator vs.
   real-device split.
2. **In-process frame pipeline, no helper, no inner port.** Persistent screenrecord
   child + streaming PNG splitter + latest-frame buffer + multipart writer. Maps
   cleanly to Go and avoids the per-call spawn cost.
3. **Normalized 0..1 coordinates tied to the live frame** (not `wm size`). Zero
   rotation math.
4. **Pluggable stream source** behind an interface from day one (leaves room for a
   future H.264/scrcpy path).
5. **Semantic UI tree as default observer; identity-first taps** (resource-id >
   text/content-desc > coordinates), with exact→contains matching, nested
   duplicate/containment folding, outermost-control selection, off-screen/disabled
   gates and ambiguity candidate lists.
6. **Observe → act-with-assertion → observe**: `expect_text` / `expect_gone` (one
   round trip) and `expect_count` (±1 verification with a probe-guard that refuses
   before tapping an unknown key). Safe-by-construction primitives.
7. **List/row abstraction** with generic counter parsing (no app vocabulary) for
   feeds whose controls are unlabeled.
8. **Honest degradation + self-describing errors**: tools always register; every
   call returns a real fact or throws with the fix; empty vs. failed never
   conflated; backtrace reports `engine` + `note`.
9. **Optional skill as portable markdown** consumable by Codex/Claude/Cursor alike.
10. **Security posture for visual bytes**: HMAC capabilities with short TTL, loopback
    + Host + Origin fence, directory containment (`lstat`/`realpath`/`O_NOFOLLOW`),
    latest-wins backpressure.
11. **Build/run specifics worth copying**: applicationId resolution via
    `output-metadata.json` (never guess, no `aapt2`); wrapper-first Gradle; skip
    `.git/.gradle/src` in APK search; detect `adb install` exit-0-with-
    `Failure[…]`; `monkey` for launcher resolution; `logcat -T` from the **device
    clock**; process-group kill for logcat children.
12. **Output budgeting**: ~40 KB caps that prune deepest / drop lowest-confidence,
    with a hint explaining the truncation.

## Things to avoid

1. **Do not couple to a harness plugin registry / event bus / lifecycle.** Invert
   dsh-android's ratio (its `index.ts` is ~93% host glue): our capabilities are the
   product, the adapters are the edge.
2. **Do not adopt its non-standard tool-schema DSL.** MCP requires standard JSON
   Schema; keep schemas standards-compliant.
3. **Do not bundle a host-specific web UI** in the capability layer. Our UI is a
   separate consumer of the same API.
4. **Do not depend on a runtime-compiled macOS OCR helper** (`swiftc`) for a
   cross-platform product; OCR must be optional and separately distributed.
5. **Do not use a host-reserved env prefix or its home dir** for state; own our
   namespace and state root.
6. **Do not write captures to the OS temp dir** (can be reaped); use a durable
   app-owned root.
7. **Do not duplicate core stores** (it has two `ScreenshotStore`s plus a route
   counter sharing one directory).
8. **Do not let docs diverge from schemas**; generate docs from the schema.
9. **Do not promise `kill -3`/`/data/anr/` or unrooted `uiautomator`**; design the
   degradation path.
10. **Do not inherit the PNG-per-frame ceiling** if smooth UI is a goal (2–5 fps
    over USB); plan a faster source behind the seam.
11. **Do not make boot an incidental UI action**; long/destructive operations stay
    explicit capabilities.
12. **Do not assume icon-only controls are reachable**; keep a row/relative-
    position primitive.

## Legal note

The repo is **MIT**, so the host-agnostic domain modules are legally reusable. We
would **port** the logic to Go, not copy TypeScript; attribution would be kept in
a third-party notices file if we lift specific algorithms.

## Verdict for AioDevAndroid

dsh-android is a rich reference for *what Android operations should look like* and
a cautionary tale for *how not to package them*. It also validates our core bet:
the Android capability library is cleanly separable from the harness. We make that
isolation the primary architectural fact and treat MCP/HTTP/web as thin adapters —
the inverse of this repo's organization.

Related: `subsystems/ui-automation.md`, `subsystems/adb-device.md` and
`interfaces/mcp-tool-surface.md` should incorporate these findings.
