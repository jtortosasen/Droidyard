# Token cost of the Droidyard MCP surface: measurements, prior art, ranked changes

2026-10-07. Repo at `61bf3b2`. Labels: **[obs]** observed (measured or read in code), **[inf]** inferred,
**[asm]** assumption. Nothing in the repo was edited; the wire was captured with a test file injected through
`go test -overlay` (scratchpad `measure/zz_tokmeasure_test.go`), which builds the server exactly as
`cmd/droidyard/serve.go:57-65` does and sends `initialize` + `tools/list`.

## 1. What Droidyard sends

### 1.1 tools/list on the wire [obs]

`adapters/mcp/mcp.go:190-191` registers each tool with the generated `inputSchema` **and** `outputSchema`
(ADR-0025 §Decision 1: "passed through unmodified").

| Part | Bytes (34 tools) |
| --- | ---: |
| whole `tools/list` response line | **130,942** |
| `outputSchema` | 70,111 (54 %) |
| tool `description` strings | 33,588 |
| `inputSchema` | 20,918 |
| annotations | 3,022 |
| **everything except outputSchema** | **60,532** |
| `initialize` result (instructions 860 B) | 1,040 |

Per tool, ranked (bytes; "model-facing" = without outputSchema):

| Tool | wire total | description | input | output | model-facing |
| --- | ---: | ---: | ---: | ---: | ---: |
| device_inputText | 16,615 | 2,759 | 4,999 | 8,677 | **7,938** |
| device_tapElement | 15,960 | 1,685 | 4,640 | 9,454 | **6,506** |
| device_logs | 8,339 | 2,314 | 1,865 | 3,985 | **4,354** |
| artifact_read | 3,217 | 1,268 | 1,087 | 686 | 2,531 |
| emulator_start | 3,584 | 1,939 | 359 | 1,109 | 2,475 |
| device_uiTree | 3,500 | 1,597 | 695 | 1,033 | 2,467 |
| device_install | 3,113 | 1,147 | 915 | 873 | 2,240 |
| emulator_wipe | 2,892 | 1,610 | 258 | 848 | 2,044 |
| shell_exec | 5,106 | 1,296 | 504 | 3,133 | 1,973 |
| file_read | 2,867 | 1,211 | 572 | 908 | 1,959 |
| project_variants | 3,795 | 1,505 | 196 | 1,916 | 1,879 |
| build_run | 7,970 | 1,248 | 399 | 6,149 | 1,821 |
| file_write | 2,110 | 1,128 | 392 | 415 | 1,695 |
| build_tasks | 2,735 | 950 | 550 | 1,062 | 1,673 |
| file_list | 2,543 | 1,063 | 357 | 947 | 1,596 |
| sdk_requirements | 4,621 | 1,331 | 62 | 3,046 | 1,575 |
| device_launch | 2,107 | 972 | 401 | 555 | 1,552 |
| … 17 more, each < 1.5 KB model-facing | | | | | |

Why the top two are big [obs]: the selector object (`id/text/desc/class/within/index/ref/snapshot`, each with
a description) is **inlined three times** in `inputText` (target, expect[].appears, expect[].gone) and likewise in
`tapElement`; "resource-id, without its package prefix; exact." occurs 13 times and "Short class name…" 13 times
across the list. Property descriptions total 10.0 KB (67 unique strings). Tool descriptions are manuals: the
inputText one is 2,759 chars (delivery paths, ime, chunk-late timing bounds 37 s/14 s/24 s/15 s, error list). This
**contradicts the project's own rule**, `docs/interfaces/mcp-tool-surface.md:108-115` ("One or two sentences … Not a
manual … They cost tokens on every session"); 31 of 34 descriptions exceed two sentences.

### 1.2 What actually reached the model in the comparison runs [obs + inf]

Source: `~/Library/Caches/droidyard-dev/m1m/runs/{dy,sh}{1..5}/out.jsonl` (OpenCode 2.0.18 `step_finish.tokens`)
and `wire.{c2s,s2c}.jsonl`.

- Every droidyard run: step 0 context = **10,39x tokens**, step 1 = **24,88x–25,03x** (all 7 dy runs; the shell arm
  has 12.0k → 12.6–12.9k). OpenCode logged `mcp connected … tools=34` before step 0 (`dy1/err.log:66`). The ~14.4k
  jump is the droidyard tool list entering the prompt [inf: mechanism not established — step 0 called OpenCode's
  code-mode `execute({search({query:"droidyard"})})`, which returned `items: []`].
- 60.5 KB + 0.86 KB instructions ≈ 14.3k tokens ⇒ **≈ 4.3 B/token** for the tool list. If OpenCode forwarded
  outputSchema the jump would be ~30k; so **outputSchema does not reach the model in OpenCode** [inf, strong].
- The tool list's first appearance is uncached: dy1 step 1 = 18,753 uncached of 24,897. That alone is ~14k of the
  dy arm's 42.1k median uncached input, i.e. **more than the whole 7.6k uncached gap to the shell arm**.
- Then it is re-sent every step: dy runs have 19–32 steps (median 21) ⇒ **~14.3k × 20 ≈ 286k cached tokens per run
  are tool definitions**, about half of dy1's 590k total context. Removing them entirely would put the dy arm at
  ~300k vs the shell arm's ~550k.
- Results: OpenCode's tool `output` equals our text block (untrusted line + compact JSON); `structuredContent` is not
  shown to the model [obs for OpenCode 2.0.18]. The MCP spec says a structured result SHOULD also carry the JSON as
  text, so this duplication is wire-only for OpenCode; Claude Code/Cursor not checked.

Bytes per token for results, calibrated from context deltas between steps [obs]: JSON log lines **≈ 2.45 B/token**
(dy1–5, the 6–7.5 KB `device_logs` step: 2.39–2.50); prose-heavy JSON (`system_capabilities`) 3.78; shell text 2.2–3.9.
Dense JSON with ISO timestamps tokenizes badly.

### 1.3 Result sizes [obs]

Per tool, dy1–5 (OpenCode `output` bytes; "once" and "resent" are averages over the 5 runs; resent = bytes × later steps, i.e. what stays in context):

| Tool | calls | median B | once/run | resent/run | ≈ cached tok/run |
| --- | ---: | ---: | ---: | ---: | ---: |
| device_logs | 10 | 12,860 (max 21,725) | 23.5 KB | 72 KB | ~29k (2.45 B/t) |
| system_capabilities | 1 (dy4 only) | 42,931 | 8.6 KB | 103 KB | ~27k avg; **in dy4 itself: +11.4k context at step 20 (35.9k → 47.4k), resent 12 steps ≈ 137k** |
| file_read | 5 | 2,720 | 2.7 KB | 53 KB | ~15k |
| build_assemble | 12 | 1,003 | 2.5 KB | 48 KB | ~13k |
| device_uiTree | 15 | 843 (max 1,876) | 3.0 KB | 29 KB | ~12k |
| device_tapElement | 23 | 446 (2,274 with returnTree) | 4.9 KB | 24 KB | ~10k |
| inputText / launch / install / screenshot | | 397 / 91 / 78 / 361 | small | | |

- **device_logs**: each line is `{"time":"2026-10-07T14:24:10.739922Z","pid":…,"tid":…,"priority":"debug","tag":…,
  "message":…}`: ~120 B of keys/timestamp per line. Over the 10 calls: JSON 117.4 KB → threadtime-style text 69.3 KB
  (−41 %) → same text with consecutive same pid/prio/tag lines grouped 52.2 KB (−56 %). The crash read (dy3,
  `minPriority:error`) is 15.3 KB: 68 crash lines = 15.56 KB, while the result's own `crashes[]` summary is 133 B;
  headline + `Caused by`/`Suppressed` + app frames (`dev.droidyard…`) = 7 lines, 707 B (**−95 %**). Defaults:
  `maxLines` 100.
- **device_uiTree** golden files (`internal/domain/device/testdata/*-uitree.golden.json`) and the dy3 tree, compact
  JSON vs a line-per-node rendering with JSON-quoted strings (scratchpad `treefmt2.py`):

  | tree | JSON | text + bounds | text, no bounds | distilled + bounds | distilled, no bounds |
  | --- | ---: | ---: | ---: | ---: | ---: |
  | dy3 golden-app list (15 nodes) | 1,788 | 905 | 585 | 669 | **455 (−75 %)** |
  | pixel9a Settings (41 nodes) | 4,499 | 2,819 | 2,066 | 2,075 | **1,465 (−67 %)** |
  | pixeltablet (54 nodes) | 5,988 | 3,936 | 2,856 | 2,838 | **1,954 (−67 %)** |

  "Distilled" = drop `statusBarBackground`/`navigationBarBackground`, inline a lone text-only leaf into its parent
  (`n3 View #like_alpha "Like" clickable`), unwrap unnamed flagless single-child containers. Example output:
  `n0 FrameLayout #content` / `  n1 TextView #greeting "Hello, Ana"` / `  n3 View #like_alpha "Like" clickable`.
- **build result** (success): 1,003 B, of which `gradle.daemonJavaHome`, `expectedSources` (6 keys), `profile`,
  `validation`, `extraction:{}`, `request` echo ≈ 55 %.
- **system_capabilities**: 43 KB = every descriptor's full description again; the one call (dy4, step 19 of 32) added
  11.4k tokens to every later step, ~137k cached tokens: a large part of why dy4 reached 1.18M total context.
- **Errors**: `{code,message,cause?,nextStep?,details?}`, typically 200–500 B (`internal/capability/error.go:91-100`).
- **Screenshot**: text 361 B + an image block (1080×2340 → 473×1024 PNG, 17 KiB); ≈ 646 image tokens by Anthropic's
  w·h/750 [asm: DeepSeek's cost unknown; one screenshot per run, minor].

## 2. How others keep the cost down

| Project | Tools / size (model-facing) | Levers |
| --- | --- | --- |
| **Droidyard** | 34 / **60.5 KB ≈ 14.3k tok**, 1.78 KB/tool | none; all tools always listed |
| mobile-mcp (`.refs/mobile-mcp`, 18d0e8c) | 32 / ~18 KB [estimate from source: descriptions + `.describe()` + 35 B/param] ≈ 570 B/tool | one-line descriptions; `mobile_batch_commands` (`src/server.ts:1162-1180`); instructions say tree before screenshot (`:78-85`) |
| Playwright MCP (microsoft/playwright 16a8826; playwright-mcp f183dad) | 25 default (README "Core automation" 14.2 KB of markdown); **47 more opt-in** via `--caps` (README:415, groups at :1175-1605) | capability groups; `--snapshot-mode full|none` (README:450); boxes opt-in (`--snapshot-boxes`, :449); `--image-responses omit` (:436); `browser_find` instead of a full snapshot (:994-995); `browser_snapshot {filename, depth}` (:1103-1110) |
| github-mcp-server (55edd58) | 141 tools, median **1.6 KB/tool, median description 72 chars** (`pkg/github/__toolsnaps__`); default toolsets context+repos+issues+pull_requests+users = 45 tools ≈ 67.6 KB | `--toolsets`, `--tools` allowlist, `--read-only` (README:444-497, 549-557); consolidated `*_read/*_write` with a method enum; `fields` selection enums and Minimal* types (`pkg/github/minimal_types.go`); CSV for `list_*` behind a flag (`pkg/github/csv_output.go:45-81`); explicitly **does not** advertise `listChanged` (`pkg/github/server.go:91-102`) — the old dynamic-toolsets mode is not in the current tree [obs: grep finds none] |
| appium-mcp (f97fa9e) | 34 tools, descriptions ~6.2 KB total [estimate] | `action`-enum consolidation (`appium_app_lifecycle`: "Manage app lifecycle, installation, state, data, and deep links.", `src/tools/app-management/app.ts:67-68`); screenshot saved to a file, path returned, base64 only with `returnRawBase64` (`src/tools/interactions/screenshot.ts:124-140`); `NO_UI` and docs tools opt-in (README:152,166,393-425) |
| gradle-mcp (`.refs`, 493af5a) | **10 tools** (`docs/tools/*.md` 32 KB incl. prose) | few tools with `kind` modes (`query_build kind=CONSOLE|TESTS…`); pagination default 20 lines; `outputFile` to write big output to disk (`GradleBuildLookupTools.kt:52-56, 459-461`) |
| codex-android-mcp (`.refs`) | ~20 tools, ~25 KB description literals [estimate] | none notable; text is **pretty-printed** JSON (`JSON.stringify(…, null, 2)`, `src/server.ts:243-246`) — the opposite lever |
| droidrun (1935ada) | harness, not MCP-first | indexed indented text `N. Class: "id", "text" - (x1,y1,x2,y2)` (`mobilerun/tools/formatters/indexed_formatter.py`); drops offscreen and < 5 px nodes (`tools/filters/concise_filter.py`); optional bounds normalised 0–1000 |
| tester-army-e2e (`.refs`) | harness | **screen diffs**: first look whole, later looks only `added`/`changed (was: …)`/`removed` lines keyed by stable node ids, whole again past 60 lines or 50 % changed (`packages/e2e/src/agent/screen-update.ts:1-35, 360-388`); lesson recorded at :366: prefix a word, not `-`, because a model read `- #n19 button "Delete"` as present and tapped it twice; observation byte budget (`observation-budget.ts`) |

Client-side deferral [obs, docs fetched 2026-10-07]:
- Anthropic tool search (`platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool`): `defer_loading`
  keeps tools out of the prompt until searched; quoted ">85 %" reduction for ~55k-token multi-server setups;
  "selection accuracy degrades once you exceed 30–50 tools"; recommended when "10 or more tools" or ">10k tokens";
  search runs over names, descriptions, argument names and argument descriptions; keep the 3–5 most used non-deferred;
  prefix names by service. For MCP: `mcp_toolset.default_config.defer_loading`.
- Claude Code (`code.claude.com/docs/en/mcp`): MCP tool search on by default (v2.1+), defers when MCP tools would take
  ~10 % of context; `MAX_MCP_OUTPUT_TOKENS` default 25,000, warning above 10,000; results above 50,000 chars saved to a
  file unless the tool sets `_meta["anthropic/maxResultSizeChars"]` (≤ 500,000). Droidyard's 14k-token list crosses
  the 10 % line on a 128k-context model, so Claude Code would defer it [inf]; OpenCode 2.0.18 did not.
- MCP `notifications/tools/list_changed` would allow server-side progressive disclosure, but a changing list breaks the
  client's prompt cache prefix and support is uneven; GitHub removed/does not use it [obs above].

## 3. Output compaction patterns worth copying

1. **Line-per-node trees with refs, defaults omitted** (Playwright `ariaSnapshot({mode:'ai'})`: refs only on
   interactable nodes, `injected/src/ariaSnapshot.ts:58-70`; distiller plugins mergeStringChildren,
   removeNamelessImages, removeRedundantNames, inlineTextIntoGeneric, removeNameRepeatingChild,
   unwrapSingleChildGenerics, `ariaSnapshotDistiller.ts:256-263`, with removed nodes' refs still resolvable;
   mobile-mcp `src/format-elements.ts:9-55,97-104` emits one line per element with a one-line header and
   "only emit non-default states").
2. **Coordinates opt-in** (Playwright boxes off by default; Droidyard taps resolve by ref/selector, so bounds are
   rarely needed by the agent).
3. **Big output to a file/ref, pointer inline**: Playwright writes the post-action snapshot to a `.yml` file and
   inlines only the link unless `browser_snapshot` is called explicitly (`tools/backend/response.ts:289-322`
   [obs in shared backend code; not run]); console as `file#L10-L24` (`response.ts:327-330`, `logFile.ts`); appium
   screenshots as paths; gradle-mcp `outputFile`. Droidyard already has the mechanism (`capture` and `consoleRef`
   artifacts + `artifact_read` paging).
4. **Diffs instead of repeats** (tester-army). Droidyard's `tapElement` already returns no tree unless
   `returnTree` and verifies via `expect`, which is the cheaper design.
5. **Screenshots**: JPEG q75, max edge 1024 (mobile-mcp `src/server.ts:29, 863`); Droidyard PNG ≤ 1024, JPEG q85 only
   above 512 KiB; appium compression "50–80 %".
6. **Logs**: count limits with small defaults (mobile-mcp 100, advises 20–50, `src/server.ts:24, 994`; gradle-mcp 20
   lines), file sink for large captures.

## 4. Ranked changes for Droidyard

Units: model-facing tokens. Per turn = every agent step. Per run = the m1m golden path (median 21 steps, dy1–5).
Tool-list tokens at 4.3 B/t (calibrated), JSON results at 2.5 B/t, other JSON 3.6 B/t [asm for the last].
Baseline per run: ~42k uncached, ~550–600k total context.

| # | Change | Saving / turn | Saving / run | Risk |
| --- | --- | ---: | ---: | --- |
| 1 | **Descriptions to the documented rule** (`mcp-tool-surface.md` §Descriptions): ≤ 2 sentences / ~400 chars; keep one safety sentence where a retry is harmful ("a retry would type/tap twice; read the field first"); move timing bounds, delivery paths and error lists to the server instructions, a skill and the errors' `nextStep`; keep property descriptions on the first selector copy only (nested copies bare) and ≤ 100 chars. Mechanical cut measured: 60.5 → **30.3 KB** | **−7.0k** (−50 %) | −7k uncached, **−140k cached** | Medium: agents lose caveats (two-read rule, vacuous `expect`, partial typing). Mitigate: the caveat lives where it bites (result `warnings`, error `nextStep`, already present); keep full text in `schema/capabilities.json` by adding a short MCP `summary` field to descriptors instead of shortening `description` (contract change either way: regenerate schema, review diff). `$defs/$ref` dedup gives the same bytes (53.1 KB alone) but some clients mishandle `$ref`; not needed. |
| 2 | **`device_logs` compaction**: crash lines collapsed into `crashes[]` with headline, `Caused by`/`Suppressed` and app frames (full block stays in `capture`); text rendering one line per entry (`HH:MM:SS.mmm P/tag: msg`, consecutive same-header lines grouped), strings quoted per the untrusted rule; default `maxLines` 100 → 50 | — | **−6 to −8k uncached, −20 to −25k cached** (logs ≈ 23.5 KB once, 72 KB resent per run; −56 % text, −95 % on the crash read) | Low–medium: the text loses `tid`/µs; `structuredContent` keeps the full JSON. The untrusted rule (`mcp-tool-surface.md:249-251`, item 2) requires device strings JSON-quoted, not the whole result as JSON: keep message/tag quoted; `adapters/mcp/mcp.go:759-768` (single-line compact JSON check) must change. |
| 3 | **Tool profile** (`droidyard mcp --tools core|all`, GitHub/Playwright style) with ~21 golden-path tools (workspace_describe, project_modules, build_assemble/run/errors, file_read/write/list, emulator_start/list, device_list/install/launch/uiTree/tapElement/inputText/logs/screenshot, operation_wait/cancel, artifact_read). After #1: 20.3 KB | **−9.4k** vs today (−2.4k on top of #1) | −9k uncached, −190k cached (vs today) | Medium: a needed tool absent (sdk_requirements, emulator_wipe, shell_exec) reads as "cannot". Needs an ADR: `mcp-tool-surface.md:90-92` says exclusions are principled, "none made just to save tokens", and :347 leaves the minimal surface open. Keep `all` the default; recommend `core` for small-context models. |
| 4 | **uiTree / returnTree text rendering, distilled**: line per node, `ref Class #id "text" desc="…" flags`, bounds opt-in (`bounds:true`), system-bar views and unnamed single-child wrappers folded, lone text child inlined into its clickable parent (its ref still resolvable server-side) | — | **−2k uncached, −12 to −15k cached** on the toy app (tree bytes −67 to −75 %); proportionally more on real screens (41–54 nodes: 4.5–6 KB → 1.5–2 KB) | Medium: agents that read bounds for swipes need the flag; a hidden child ref can confuse selectors (`text:"Like"` matched n4, the agent now sees it on n3: resolution must keep matching the inlined text to the parent or the child consistently). Word-prefix lesson (tester-army) applies if diffs are added later. |
| 5 | **`system_capabilities` compact by default**: names + one-line summary; full descriptor only with `{name}` | — | 0 in 4 of 5 runs; **−11k uncached, ~−130k cached** in a run that calls it (dy4) | Low. |
| 6 | **Success results without boilerplate**: build (`daemonJavaHome`, `expectedSources`, `profile`, `validation`, empty `extraction`, `request` echo) ≈ −55 %; omit empty/default fields generally | — | −0.5k uncached, **−7k cached** | Low–medium: fields are in the contract; either drop them from the result (schema change) or let the text rendering omit them while `structuredContent` keeps them (ADR-0020 says the text is the plain result — amend). |
| 7 | **Server instructions as the shared manual** (selector vocabulary once, untrusted rule once, write-lock and "never retry an action call" once): enables #1 | +0.3k (paid once per turn, replaces ~7k) | — | Low; instructions are not always shown by every client [asm: OpenCode and Claude Code do show them]. |
| 8 | **Keyword-rich short descriptions** so client tool search finds tools (OpenCode's code-mode `search` returned nothing for "droidyard" in every dy run; Claude Code defers above ~10 % of context) | client-dependent: up to −14k | client-dependent | Low; complements #1. |
| — | Not recommended: dropping `outputSchema` from tools/list (70 KB wire, ~0 model tokens in OpenCode [inf]; contradicts ADR-0025 §1); `tools/list_changed` dynamic toolsets (breaks prompt-cache prefix, uneven client support); `$ref` dedup (#1 gets the same without it). | | | |

**Combined estimate**, #1 + #2 + #4 + #6 on the golden path: uncached ~42k → **~26–28k** (below the shell arm's 34.5k);
total context ~590k → **~400k** (shell arm ~550k). Adding #3: another ~−50k cached. These are estimates from
byte counts and two calibrated ratios, not a rerun; the m1m kit (`~/Library/Caches/droidyard-dev/m1m/kit/run.sh`)
can confirm with N = 5 after the changes.

## 5. Not verified

- Why OpenCode's step 0 lacks the droidyard tools (10.4k) and step 1 has them; and whether OpenCode's code-mode can
  defer MCP tools if their descriptions match its search.
- Whether Claude Code or Cursor forward `outputSchema` or `structuredContent` to the model (OpenCode does neither).
- Tool-list sizes for mobile-mcp, appium-mcp and codex-android-mcp are source estimates (no downloaded code was run);
  github-mcp-server's are exact (toolsnaps); Playwright's are README markdown, not wire bytes.
- DeepSeek's cached-token price relative to uncached [asm: much cheaper], so the cached savings matter less in money
  than in context length.
- Playwright's older "incremental" snapshot mode: the current README offers only `full|none` (README:450); whether
  it was removed was not checked in history.
