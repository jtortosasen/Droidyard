# Plan: Milestone 2a — the minimal web UI: Logcat, build and devices, driven from the browser

- Status: Reviewed
- Date: 2026-10-09
- Milestone: "the next milestone is Phase 2, the minimal web UI" (`docs/README.md` §Where the work stands); the owner's brief of 2026-10-09 (a macOS look, panels in `mvp.md`'s order, evidence early) and its correction of the same day: immediacy and lightness were criteria for choosing the framework and libraries, **not budgets**; no bundle-size or tab-memory acceptance item, no feature or library cut for size alone, a lighter option preferred only between otherwise equal ones; the one check kept is that the UI stays fluid while Logcat streams a burst (A7).
- Phase: `docs/roadmap/mvp.md` §Phase 2 (in part; see Scope). Proposed split: **2a** (this plan) closes Phase 2's acceptance; **2b** holds the Git panel with `git.*`, the project explorer and a read-only viewer.
- Contract: ADR-0039 §1–§5; ADR-0006; ADR-0009; ADR-0003; ADR-0016; ADR-0015 §1, §6; ADR-0020 §Core primitives; ADR-0019 §Sessions; ADR-0012 §Durability classes; ADR-0021 §Layout; ADR-0028; `docs/interfaces/http-websocket.md` §Security baseline, §Endpoints; `docs/architecture/capability-api.md` §Events and subscriptions; `docs/architecture/overview.md` §Cross-cutting concerns; `docs/roadmap/mvp.md` §The golden path, §Definition of done.

**The owner should see:** Q1 (the built UI is not committed; a binary built without it serves a placeholder; a draft ADR); Q2 (Logcat polls `device.logs`; no streaming capability in 2a); Q3 (the capability trace is one daemon log line per operation); Q4 (screenshots drawn on a canvas, so the CSP stays as it is); A7's working defaults; **the Git panel moves from third place to 2b**, after devices, because `git.*` does not exist and may slip (`mvp.md` §In scope) while the acceptance needs devices; step 4's shell screenshots come to the owner before step 5 starts.

## Scope

| Phase 2 item, or Definition of done item | Here? | If not, where and why |
| --- | --- | --- |
| Svelte 5 + TypeScript app the daemon serves (ADR-0039, ADR-0006) | yes | steps 1, 2 |
| CodeMirror 6 editor | no | deferred by `mvp.md` §Phase 2, until code intelligence (v1); a read-only viewer is 2b's call |
| Panels: Logcat; build / Gradle tasks; devices + screen (stills) | yes | steps 5, 6, 7 |
| Panel: Git | no | 2b with `git.*` (see above) |
| Panels: project explorer, problems | part | problems = `build.errors` in the build panel (the MVP has no other source); explorer to 2b |
| TypeScript client generated from the schema | yes | step 2 (ADR-0016's "standard generator") |
| Loopback HTTP baseline | exists | 1d; step 2 serves the app under it unchanged; Phase 2 adds no rule |
| Acceptance: golden path from the browser; same-primitive check | yes | steps 3, 8 |
| DoD: golden path from the browser | yes | step 8 |
| DoD: every deviation recorded as an ADR | part | this milestone's own (Q1's ADR), at the close |
| `policy.*` from the UI | no panel | already authorised: HTTP requests are client `ui` (ADR-0015 §6; `http-websocket.md` §Endpoints); G1's trust uses `droidyard trust` |
| Stretch; other DoD items; owner's deferred ideas | no | Not in this milestone |

## Starting point

Code at `09cda54`. `adapters/httpapi/httpapi.go`: the fence (rules 2–7) before every route, `POST /v1/call`, `GET /v1/artifacts/{id}` (an `artifact.read` operation of client `ui`), `GET /` a static placeholder under `default-src 'self'; frame-ancestors 'none'`; `ws.go`: one session per connection, auth frame, envelopes served concurrently, `{subscriptionId, event}` frames. `droidyard ui` opens `http://127.0.0.1:<port>/#token=<token>` (`TestUIOpensTheTokenisedURL`); fence and WebSocket tests in `cmd/droidyard/http_test.go`, `ws_test.go`. Subscribable topics: `workspace.lifecycle`, `operation.progress`; builds publish progress. Operation records hold no client kind and nothing prints them (the trace format is a register row). `device.logs` has no `Slim`, so its results stay whole for the 15-minute retention. `daemon.log` is never rotated (register). No `git.*`, no `web/`. Svelte material in `.refs/` (git-ignored); there, the `@sveltejs/mcp` CLI (binary `svelte-mcp`) prints an inspected object, not JSON, always exits 0, and treats a missing path as code; its package export `.` offers the handler as `svelteAutofixer` (`mcp-stdio/src/handlers.ts`), returning `{issues, suggestions}`; ESLint errors and compiler warnings go to `issues`, ESLint warnings and `assign_in_effect` to `suggestions`.

Baseline on 2026-10-09 at `09cda54` (macOS): `go vet ./...` and `gofmt -l .` printed nothing; `go test ./...` ok in every package (rechecked by the round-1 reviewer). Green: no repair step.

## Run rules

- K1. `B=~/Library/Caches/droidyard-dev/m2a`. Every daemon runs with a scratch `DROIDYARD_STATE_DIR` (`.claude/skills/verify-daemon/SKILL.md` §Launch); `~/Library/Application Support/droidyard` is absent or unchanged before and after each run.
- K2. Steps 6, 7 and 8 (Gradle or a device) follow `m1l-phase1-acceptance.md` §Owner's constraints, §Scratch rules and §Emulator rules (E1–E7) with `m2a` for `m1l`: the guard as `ADB`, `-tags droidyard_nodump`, the scratch AVD `droidyard_m2a` the only one booted or stopped, 5037's pid unchanged, no `uiautomator dump` of an owner emulator, the SDK stamp unchanged, no Gradle task matching `install*`, `uninstall*`, `connected*` or `device*`. The physical Samsung is not used; DopamineControl is not read.
- K3. Browser runs use Claude in Chrome on a new tab of the owner's Chrome at the URL `droidyard status` prints for the scratch daemon. Snippets run through its JavaScript tool and are kept as evidence files. **Probe first** (step 2): one snippet's result recorded (it ran), and one deliberate violation (a `<script>` element with inline text appended by that snippet) seen in the console reader; until both are seen, "no violation" and every in-page measurement are not evidence.

## Delegated decisions

- D1. **Layout.** `web/` at the root: `package.json`, `pnpm-lock.yaml`, `.node-version` (24.13.1), `vite.config.ts` (`outDir: dist/app`, `assetsInlineLimit: 0` so nothing becomes a `data:` URI the CSP refuses), `svelte.config.js`, `tsconfig.json`, `index.html`, `src/`, `scripts/`, `embed.go` (Go package `web`). Git-ignored: `web/src/lib/api/capabilities.gen.ts`, `web/dist/app/`, `web/node_modules/`. No `dev` script: the fence ties `Origin` to the daemon's port, so a dev server on another port cannot reach it.
- D2. **Package manager and pinning.** pnpm, pinned in `packageManager`; exact versions only (no ranges, tags or URLs); the lockfile committed and installed with `--frozen-lockfile`; dependency lifecycle scripts not run (pnpm's default since 10, allowlist empty); `minimumReleaseAge` of one day if the host's pnpm has it; `engines.node` with `engine-strict`; nothing through `npx` or `pnpm dlx`. Pins: svelte 5.57.2, svelte-check 4.7.6, @sveltejs/vite-plugin-svelte 7.3.1, vite 8.3.4, @sveltejs/mcp 0.1.26 (the brief's registry reading; step 1's frozen install confirms them); `typescript` (≥ 5.8, the newest svelte-check accepts), `json-schema-to-typescript` and `bits-ui` exact at the latest on the day they are added. npm runs dependency install scripts by default; bun's lockfile and script handling are less established here.
- D3. **Primitives.** Native controls where they look native on macOS (`select`, `input`, checkbox, `<dialog>`), styled by the tokens; **Bits UI** for what native HTML lacks (menus, tooltips, segmented tabs with roving focus, alert dialog): runes-native and built for Svelte 5, where Ark UI's Svelte adapter drives each component through a Zag state machine, a second model beside runes. Weight would only break a tie.
- D4. **Transport.** One WebSocket session per tab carries every call; `POST /v1/call` is unused. A `longRunning` capability is sent with `waitMs: 0` and followed with `operation.wait {timeoutMs: 15000}` and an `operation.progress` subscription; others without `waitMs`. Reconnect with backoff (0.25 s doubling to 5 s), resubscribe with `since`, re-wait every known operation id; close 4401 shows "the daemon restarted: run `droidyard ui`". Artifacts through `GET /v1/artifacts/{id}` with the `Authorization` header.
- D5. **Inert by construction** (rule 8). No markup from data anywhere in `web/src`: `TestNoHTMLSinks` (`web`) fails on `{@html`, `innerHTML`, `outerHTML`, `insertAdjacentHTML`, `document.write`, `srcdoc`, and on any `href=`/`src=` attribute whose value is an expression (`={…}`) rather than a literal. No autolinking. Every field is then inert without per-field machinery.
- D6. **Static serving and the CSP.** `GET`/`HEAD /` → `index.html` with the CSP and `Cache-Control: no-cache`; `/assets/<hashed name>` (`.js`, `.css`, `.png` only) → `Cache-Control: public, max-age=31536000, immutable`; every other path 404 (no SPA fallback). A CSP violation is fixed in the code (no static `style="…"` attributes: classes, or `style:` directives, which set properties through the CSSOM), **never by widening the CSP**; a component that cannot comply is replaced.
- D7. **Frontend logic tests** run with `node --test` and Node 24's type stripping on plain `.ts` modules (ring, batcher, poller, client, token bootstrap) with injected sockets, clocks and frame schedulers; `.svelte` files stay thin over them.
- D8. **Working defaults** (constants with a comment): Logcat ring 50,000 lines; poll period 1 s; re-poll after a truncated answer no sooner than 250 ms; `maxLines` 1000; devices refresh 5 s while visible; A7's thresholds.

## Acceptance

- [ ] A1. The golden path G1–G8 of `m1l-phase1-acceptance.md` §The golden-path run is completed from the browser on a fresh copy of `samples/golden/` and the scratch AVD, each step's structured result visible in the UI: modules; a debug build; after a compile error, `build.errors`' file, line, column and message in the problems list, then a successful rebuild; `emulator.start`; install and launch; logs with `GoldenApp: started`, a screenshot, the UI tree; the four taps and inputs with `matched: true`; the crash's exception and message. G3's edit is made with `droidyard call file.write` (the editor is deferred). Source: `mvp.md` §Phase 2 (Acceptance), §Definition of done. Proven by: step 8
- [ ] A2. Same-primitive check over A1's trace: every UI action is an operation of client `ui`; per G row the UI's capabilities are those m1l's G table names, plus only, from this closed list, `workspace.open` (an MCP session opens its root itself, ADR-0019 §Sessions), `workspace.describe`, `events.subscribe`, `operation.*`, `artifact.read` (the route), `project.variants`, `build.tasks`, `build.lastResult`, `emulator.list`, `device.list`, `system.health`; any other `client=ui` capability is a defect. The tab's network log shows no request outside `/`, `/assets/`, `/v1/ws`, `/v1/artifacts/`. Source: `mvp.md` §Phase 2 (Acceptance); ADR-0020 §Core primitives. Proven by: step 3, step 8
- [ ] A3. The core serves the built app under the unchanged baseline: D6's headers and 404s; the placeholder when no build is embedded; a clean checkout with no `node_modules` and no build passes `go build ./...`, `go vet ./...` and the `web` and `httpapi` tests with no Node on `PATH`; over A1's run the console (K3's probe passed) shows no CSP violation; a tab without a token shows "run `droidyard ui`"; the address bar never keeps the token. Source: ADR-0006; `http-websocket.md` §Security baseline; Q1. Proven by: step 2, step 8
- [ ] A4. The TypeScript client is generated from `schema/capabilities.json` by every `check` and `build`; a commit that changes the schema runs the frontend gate, and a schema in which a field the UI reads is renamed fails `check`. Source: ADR-0016; ADR-0039 §5. Proven by: step 2
- [ ] A5. The pre-commit hook refuses each seeded violation, with the gate that refused it named: Svelte 4 syntax (runes enforced), a `svelte-check` warning (`--fail-on-warnings` shown alone), an autofixer issue, a version range; a Go-only commit passes with no Node on `PATH`. Source: ADR-0039 §2, §3. Proven by: step 1
- [ ] A6. `svelte-core-bestpractices` and `svelte-code-writer` are vendored at `6b5d0dab3c9c083387247ab20dc684573481076b` with the MIT licence and provenance, beside Svelte's `llms-small.txt`; no vendored instruction fetches from the network or runs `npx`. Source: ADR-0039 §4. Proven by: step 1
- [ ] A7. **Fluid under a Logcat burst** (usability, not a budget; working defaults): following a 5,000 lines/s burst until the ring is full (at least 13 s: delivery tops out at `maxLines` per 250 ms floor, 4,000 lines/s), at most 5 % of frames run longer than 1.5 × the idle median frame and no long task reaches 50 ms (M1); the ring stops at its cap counting drops, and the list's DOM is the same size at 1,000 lines and at the cap (M2). Source: owner 2026-10-09 (correction). Proven by: step 5
- [ ] A8. The UI looks like a current macOS app: one tokens file; the system font stack; a translucent sidebar (`backdrop-filter`); a restrained accent; light and dark following the system, with an override; screenshots of the shell (step 4) and of each panel (step 8) in both appearances accepted by the owner. Source: owner 2026-10-09. Proven by: step 4, step 8
- [ ] A9. The panels do what their steps list beyond the golden path: Logcat filters, pause and follow; build tasks, `build.run`, progress, cancel and the console; emulator list, start and stop. Source: `mvp.md` §Phase 2 (Deliverables). Proven by: step 5, step 6, step 7
- [ ] A10. The register, `mvp.md`'s Definition of done, `docs/README.md` and the plans index record what 2a closed and left open. Source: `milestone-workflow` step 6. Proven by: step 9

**Measurements** (step 5, main session, K3):

- M1. A `requestAnimationFrame` loop recording frame deltas and a `longtask` observer: 2 s idle, then the burst, the tab visible.
- M2. The footer's held and dropped counts after the burst; `querySelectorAll('*').length` inside the list at 1,000 lines and at the cap.
- M3. Q2's trigger (not an acceptance item): at 200 lines/s for 24 minutes, each message stamped with its host-clock emission time and compared with `Date.now()` by a `MutationObserver` on insertion (p95); the daemon's `ps -o rss=` at 1, 16, 20 and 24 minutes (operations are retained `retainFor` = 15 minutes and captures live `artifact.DefaultTTL` = 15 minutes, so the retained set grows by design until minute 15 and a bounded core plateaus after it); the first poll's capture `ref` read with `artifact.read` at minute 12, before its TTL.

## Steps

### 1. Toolchain, gates and the briefing material

- Builds: `web/` per D1–D2: `App.svelte` rendering the window title only; `compilerOptions.runes: true`; `tsconfig.json` strict with `erasableSyntaxOnly`, `allowImportingTsExtensions`, `verbatimModuleSyntax`; scripts `check` (gen, then `svelte-check --fail-on-warnings`), `test`, `build` (gen, then Vite), `verify` (pins, check, test, build). `scripts/check-pins.mjs` fails on any non-exact spec. `scripts/autofixer-gate.mjs` imports `svelteAutofixer` from the pinned `@sveltejs/mcp`, reads each file given itself (a missing file fails), calls it with `{code, desired_svelte_version: 5, filename: <basename>}` (the extension selects component or module compilation), and fails when `issues` is non-empty; suggestions are printed and pass. `.gitignore` per D1. `.githooks/pre-commit`: **before** its Go part (a web-only commit has no Go package until step 2 and would exit early), when a staged path is under `web/`, is `schema/capabilities.json` or is under `.githooks/`, require `pnpm` and `web/node_modules` (else fail naming `pnpm -C web install --frozen-lockfile`), run `pnpm -C web install --frozen-lockfile --offline` if `package.json` or the lockfile is staged, then `pnpm -C web run verify` and the autofixer gate on staged `.svelte`, `.svelte.ts`, `.svelte.js`, each gate's name printed on failure. Vendored: `.claude/skills/svelte-core-bestpractices/` verbatim and `.claude/skills/svelte-code-writer/SKILL.md` edited to run `node web/scripts/autofixer-gate.mjs <path>` from the repository root (the raw CLI reads a path it cannot find as code and reports nothing) and to read the vendored `references/svelte-llms-small.txt` instead of `list-sections`/`get-documentation`; each directory with `LICENSE` and a `SOURCE` file (upstream, commit, edits). `internal/doccheck` skips the two directories as third-party, with one self-test case. `AGENTS.md` §Build and test gains the frontend commands; `implementer-run.md` tells a frontend step to load the two skills and run `verify` and the gate.
- Implements: ADR-0039 §1–§4; D1, D2.
- Run by: the implementer; the seeded commits by the main session; review: the main session's diff review (vendored text excluded).
- Check: five seeded commits, each one file, the refusing gate read from the hook's output: (a) `export let x` → refused; (b) `<img src="/x.png">` without `alt` → refused, and `pnpm -C web run check` alone exits non-zero on it; (c) a component `svelte-check` accepts but the handler reports under `issues`, for example `{#each items as item}` with no key (`svelte/require-each-key`, assumed an error at the pinned plugin; if not, seed one that is and record it) → refused by the autofixer gate, `check` alone passing on it; (d) `"vite": "^8.3.4"` → refused; (e) a Go-only change with `PATH` lacking node and pnpm → passes. A clean `pnpm -C web install --frozen-lockfile` succeeds; then `gofmt -l .` prints nothing (no `.go` file under `node_modules` is walked); `grep -rn -e npx -e 'pnpm dlx'` over `web/` (outside `node_modules`), `.githooks/` and the vendored skills prints nothing; `go test ./internal/doccheck` passes.
- Schema: no change.
- Needs: nothing in this plan.
- Not here: serving the app; the TS client (step 2).
- Rests on: the installed `svelteAutofixer` returns `{issues, suggestions}` and reports seed (c) under `issues` while `svelte-check` passes it (level 5, the seeded commit).
- [ ] Done. Evidence:

### 2. The daemon serves the app; the generated client; one live call

- Builds: `web/embed.go`: `//go:embed all:dist` over a committed `web/dist/.keep`; `Assets() fs.FS` is `dist/app` when it holds `index.html`, else nil. `adapters/httpapi`: `New(…, ui fs.FS)` serves D6 from `ui`, the CSP string unchanged; nil `ui` serves the placeholder, its text naming `pnpm -C web build` and a rebuild. `cmd/droidyard/serve.go` passes `web.Assets()`. `web/scripts/gen-client.ts` compiles one schema composed from `schema/capabilities.json` with `json-schema-to-typescript` into `capabilities.gen.ts` (per capability: params from `input`; result from `output`, or `output.properties.result` when `longRunning`) plus a `const` map of `scope`, `longRunning`, `statefulness`. `src/lib/api/client.ts` (D4), `src/lib/api/token.ts` (fragment → `sessionStorage`, stripped with `history.replaceState`; none → the instruction view). `App.svelte` shows the connection state and `system.health`'s version and uptime.
- Implements: ADR-0006; ADR-0039 §1, §5; ADR-0016; `http-websocket.md` §Security baseline, §Endpoints; D4–D6; Q1.
- Run by: the implementer (effort high); review: **Opus verifier** (the HTTP edge, the token).
- Check: `TestUIIsServedBehindTheFence` (`adapters/httpapi`, `fstest.MapFS`): `/` with the CSP, `nosniff`, `no-cache`; an asset with `immutable` and its type; `/x` and a `.map` or `.svg` file in the FS → 404; a raw request path `/assets/../index.html` → 404 (or the mux's 301 to a path that then 404s, asserted as such); `POST /` 405; a wrong `Host` 403 before any file is read; `/` needs no token. `TestUIPlaceholderWhenNotBuilt` for nil, and `TestAssetsNilWithoutABuild` (`web`) for an embed holding only `.keep`. `TestNoHTMLSinks` seen failing on a seeded `{@html x}` and on `href={x}`. Node: `client.test.ts` (auth frame first; replies by id; events by `subscriptionId`; an error envelope's `code` surfaced; 4401 → `unauthorized`; reconnect resubscribes with `since` and re-waits a known id) and `token.test.ts`. A4 (main session): with `system.health`'s uptime field renamed in the working `schema/capabilities.json`, `pnpm -C web run check` fails naming the field `App.svelte` reads, and a commit staging only that file is refused with the frontend gate named (it runs before the Go gate, whose golden test would also refuse it); the file is then restored. A3, after the step's commit: `git archive HEAD | tar -x` into a scratch directory, then, with no Node on `PATH`, `go build ./... && go vet ./... && go test ./web ./adapters/httpapi`. Level 5 (K1, K3): a launched daemon with the built app; K3's probe; `droidyard ui --no-autostart`; health shown; `location.hash` empty; no CSP violation or error; reload works; a tab without the fragment shows the instruction; `curl -sI` of `/` and one asset show D6's headers.
- Schema: no change.
- Needs: step 1.
- Not here: the shell and the panels; `POST /v1/call` from the UI (D4).
- Rests on: the static routes sit behind the same fence and reach no capability (level 4, `TestUIIsServedBehindTheFence`); the CSP admits the same-origin WebSocket and the built app (level 5, Chrome only).
- [ ] Done. Evidence:

### 3. The capability trace (Q3)

- Builds: `internal/capability/operation.go`: `operation` records `client` from `Request.Client` at `Start`; when it reaches a terminal state, `Operations` writes one line through an injectable logger (default `log.Printf`), **after releasing `Operations.mu`**: `droidyard: op <id> client=<cli|mcp|ui> capability=<name> workspace=<wsid|-> state=<state> ms=<n>[ code=<CODE>]`. Never params, results, messages or causes. `httpapi`'s artifact-route test gains the `client=ui capability=artifact.read` line. `docs/architecture/overview.md` §Cross-cutting concerns: one sentence naming the line as the trace.
- Implements: ADR-0020 §Core primitives ("one record type gives one trace format"); Q3.
- Run by: the implementer; review: the main session's diff review. After step 2 (both touch `httpapi`).
- Check: `TestEveryOperationIsTracedOnce` under `-race`: operations of each client kind ending completed, failed (its code on the line), cancelled, and `OPERATION_QUEUE_TIMEOUT` each give exactly one line of that shape; `operation.wait`/`operation.cancel` give none; a sentinel string in a param and in a result never appears; seen failing with the client not recorded and with params added to the line.
- Schema: no change.
- Needs: step 2.
- Not here: a trace file, a switch or a capability listing operations (Q3); `daemon.log` rotation (register).
- Rests on: the line carries no request or device data (level 4, this test).
- [ ] Done. Evidence:

### 4. The macOS shell

- Builds: `web/src/styles/tokens.css`, the only place colours, type, spacing, radii and materials are defined: the system font stack (`-apple-system, BlinkMacSystemFont, system-ui`; mono `ui-monospace, "SF Mono", Menlo`), 13 px body, a 4 px grid, hairline separators, `color-scheme: light dark`, light and dark sets under `prefers-color-scheme` with an override (System, Light, Dark) in `localStorage`, one accent and one destructive colour, the log-priority colours, a sidebar material with `backdrop-filter`. Layout: a sidebar (workspace, device, the panels, the daemon's state), a toolbar, the panel area, a status bar; ⌘1–⌘3 switch panels. Workspace: an absolute path → `workspace.open`, then `workspace.describe`; `WORKSPACE_UNTRUSTED` shows its `nextStep`. Device: `device.list` in a native `select`. `bits-ui` added (D2, D3) for the toolbar menu, tooltips, the panel tabs and the alert dialog.
- Implements: owner 2026-10-09 (look); ADR-0039 §Consequences (component libraries); D3, D6.
- Run by: the implementer; review: the main session's diff review.
- Check: `verify` green. Level 5 (K3): with each Bits UI component open, no CSP violation (D6: fixed in code, the component replaced if it cannot comply); the keyboard reaches every control (Tab, arrows in menus and tabs, Escape closes); ⌘1–⌘3; the override switches the tokens; Light and Dark screenshots of the shell **sent to the owner before step 5 starts**, the answer under Changes.
- Schema: no change.
- Needs: step 2.
- Not here: panel contents; window vibrancy (the material blurs page content only; a native shell is post-MVP).
- Rests on: —
- [ ] Done. Evidence:

### 5. Logcat (Q2), with the burst measured

- Builds: `src/lib/logcat/ring.ts` (a circular buffer of D8's capacity counting drops); `batcher.ts` (answers queued, moved into the ring once per animation frame); `poller.ts` (Q2: `device.logs {device, maxLines, since?, applicationId?}` every period, again after a `truncated: true` answer no sooner than D8's floor, inserting a "lines skipped" marker; the `since` boundary deduplicated; stops when the panel is hidden, paused, the device gone, on a typed error (shown with its code), and while `document.visibilityState` is `hidden`, resuming with `since` and a gap marker); `filter.ts` (priority, exact tags, case-insensitive substring, crash-only, on the client; the application filter is `applicationId`, server-side). `Logcat.svelte`: a fixed-row virtual list (visible rows plus overscan, scroll handled once per frame), follow at the bottom, a "N new lines" pill when scrolled up, pause, clear; a footer with lines held and dropped; crash lines and `crashes[]` highlighted; every device field as text. `web/testdata/burstadb/` (Go `main`, outside `./...`): a fake `adb` answering `version`, `devices -l` (one emulator), `getprop`, `emu avd name`, and `logcat -d -v threadtime,epoch,usec,uid` honouring `-b` and `-T <epoch>` over a log that grows with the clock at the rate `burst.json` sets, each message stamped per M3; no `pm` or `pidof` answers.
- Implements: `mvp.md` §Phase 2 (Logcat first); ADR-0015 §1 (D5); Q2; D8.
- Run by: the implementer (effort high); review: **Opus verifier** (untrusted text, the poll loop's lifetime).
- Check: node tests: the ring never exceeds its capacity and counts drops; one flush per frame however many answers arrive; the poller passes `since`, dedups the boundary, re-polls after `truncated` no sooner than the floor, stops on hide, on `hidden` visibility and on error, and resumes with a marker; filters. Level 4 (K1, K3): a daemon with `ADB` = `burstadb`, an empty `ANDROID_AVD_HOME`: M1, M2 (A7), then M3. **Decisive** (Q2's trigger, about whether polling is live and keeps the core bounded, not a UI budget): latency p95 over 1.5 s, the daemon's RSS at minute 24 more than 10 % above minute 16 (working default), or the first capture gone before its TTL (evicted) stops the step and goes to the owner; the plateau's level is recorded either way.
- Schema: no change.
- Needs: step 4.
- Not here: a streaming capability (Q2); filling a skipped burst from the capture; the `applicationId` mode under load (Unverified); a real device (step 8).
- Rests on: polling keeps lines within 1.5 s and the core bounded (level 4, fake device).
- [ ] Done. Evidence:

### 6. Build and Gradle tasks, with problems

- Builds: `Build.svelte` and plain-`.ts` helpers: modules (`project.modules`) and variants (`project.variants`); Assemble (`build.assemble {module, variant}`); a filterable task list (`build.tasks`) whose Run is `build.run`; progress from `operation.progress`; Cancel (`operation.cancel`); the result (outcome, duration, task summary, outputs); `build.lastResult` on open; problems (`build.errors`): file:line:column, severity, message, tool, the `merged` mark, a click copying `path:line`; the console after the build (`artifact.read {ref: consoleRef, offset: -16384}`) as text.
- Implements: `mvp.md` §Phase 2 (build / Gradle tasks; problems); `docs/subsystems/gradle.md` §Output (no console until the build finishes).
- Run by: the implementer; review: the main session's diff review.
- Check: node tests for the progress reducer and the problem formatting. Level 4 (K1, K2, K3) on a `git archive` copy of the sample trusted with `droidyard trust`, no device: G1–G3 from the browser (G3's edit by `droidyard call file.write`), a `build.run` of `:app:tasks`, a cancelled assemble; `client=ui` trace lines for each.
- Schema: no change.
- Needs: steps 3, 4.
- Not here: a live console while building (`build.output` has no producer); an editor.
- Rests on: —
- [ ] Done. Evidence:

### 7. Devices and screen

- Builds: `Devices.svelte` and helpers: `device.list` (D8) and `emulator.list` with Start (`emulator.start`, progress) and Stop (`emulator.stop`, behind an alert dialog); Install (`device.install {device, module, variant}` from the build panel's selection) and Launch (`device.launch {device, applicationId}`); the screen (`device.screenshot`, fetched per D4, drawn per Q4, refreshed on demand and after each action); the UI tree (`device.uiTree`); a selected node's bounds drawn over the screen; Tap (`device.tapElement`) and Type (`device.inputText`) on the selected node, the target from its id, else its text, with an optional expectation (appears or gone), showing `matched` and `afterMs`. `docs/interfaces/http-websocket.md` §Endpoints: the UI draws artifact bytes on a canvas (Q4).
- Implements: `mvp.md` §Phase 2 (devices + screen, stills); ADR-0015 §1 (D5); Q4.
- Run by: the implementer (effort high); review: **Opus verifier** (device actions, untrusted text).
- Check: node tests for the target built from a node and the bounds-to-canvas mapping. Level 5 (K2, K3), a rehearsal: `droidyard_m2a` started from the UI; the sample (built in step 6) installed and launched; screenshot and tree shown; Tap on `submit` expecting `nameError` → `matched: true`; Stop; no CSP violation.
- Schema: no change.
- Needs: steps 5, 6.
- Not here: `emulator.wipe`, `device.uninstall` (not in Phase 2's panels); a live stream.
- Rests on: —
- [ ] Done. Evidence:

### 8. The golden path from the browser

- Builds: nothing unless a defect is found (`m1l-phase1-acceptance.md` §The golden-path run, "Defects": each its own step before step 9).
- Implements: `mvp.md` §Phase 2 (Acceptance); A1, A2.
- Run by: the main session under K1–K3, on a fresh copy of the sample, a fresh state, the app built from the commit under test.
- Check: G1–G8 through the UI only, except G3's edit (`droidyard call file.write`, client `cli`) and the setup and cleanup calls (`droidyard trust`, `emulator.wipe`, `emulator.stop`); each G row's result seen in the UI, a screenshot kept. A2 from the trace lines, grouped per G step against m1l's table and A2's closed list. A3's console check over the run. A8's panel screenshots, Light and Dark, to the owner. Run hygiene as m1l's; zero orphans after `verify-daemon` §Cleanup.
- Schema: no change.
- Needs: step 7.
- Not here: an agent in the loop (Phase 1 accepted it).
- Rests on: the UI reaches the golden path through the published capabilities only (level 5, the trace).
- [ ] Done. Evidence:

### 9. Close: documents and register

- Builds: `docs/roadmap/known-limits.md`: rows for Q2's polling limits and the streaming capability deferred; Git, explorer and viewer to 2b; the WKWebView shell and the `.git/info/exclude` option; Safari and Firefox unchecked; the capability-trace row closed in part; the `daemon.log` rotation row updated with the trace's growth (about 0.5 MB per hour of open Logcat, estimated). `mvp.md` §Definition of done: the browser item ticked with the plan cited. The close commit is `milestone-workflow` step 6.
- Implements: `milestone-workflow` step 6; A10.
- Run by: the main session; review: **Opus verifier** over A1–A10.
- Check: `go test ./internal/doccheck` passes; every Not-in-this-milestone item has a register row or a named milestone.
- Schema: no change.
- Needs: step 8.
- Not here: 2b's plan.
- Rests on: —
- [ ] Done. Evidence:

## Not in this milestone

- Git panel and `git.*`; project explorer (`file.list`, `file.watch`); a read-only viewer: **2b**.
- The code editor (CodeMirror 6): v1, with `code.*` (`mvp.md` §Phase 2; ADR-0009 stands).
- A streaming Logcat capability or `device.logcat` topic; a live build console (`build.output`): register; Q2's trigger reopens the first.
- Live screen stream: Phase 2's stretch; register.
- A policy or trust panel: when the owner asks.
- A UI option adding `droidyard init`'s project files to `.git/info/exclude`: the owner's deferred idea; register row at the close.
- A thin native WKWebView shell: post-MVP (owner); register row.
- `emulator.wipe`, `device.uninstall`, `sdk.*`, `shell.exec` in the UI: not among Phase 2's panels.
- DoD items left after 2a: `build.errors` on both KGP fixtures and the second Gradle spike; the per-process idle footprint and the sidecar's idle stop (register rows).
- Size, memory, paint-time or latency budgets for the UI, and any cut of a feature or library for size (owner's correction, 2026-10-09).
- Safari and Firefox; LAN access (after v1).

## Design questions

Each has a recommendation, so the plan is not `Blocked`; the main session may apply it as Delegated.

### Q1. Must `go build ./...` work without Node, and where does the built UI live?

- Blocks: steps 1, 2
- The documents say: ADR-0006 "the core serves the built static assets"; ADR-0039 §1 "built by Vite into static assets the daemon serves"; nothing on committing the output or building without it.
- Draft decision, as a new ADR (next free number), written by the main session before step 1: the build output is **not committed**; `web` embeds `dist/` over a committed `.keep`, and a binary built without `pnpm -C web build` serves a placeholder saying how to build the UI. `go build`, `go vet` and `go test` never need Node; the hook needs it only when `web/`, the schema or the hook changes. The TypeScript client is generated at every check and build, not committed. Alternatives: **commit `dist/`** (every binary carries the UI, at the cost of minified diffs and a staleness check; wins once binaries are distributed without a release build); **build in the hook or `go generate`** (Node on every Go change, against ADR-0039 §3's "when a commit touches the frontend"); **a build tag** (the default binary lacks the UI and the tag is easy to forget).

### Q2. How does the Logcat panel stay live?

- Blocks: step 5
- The documents say: `device.logs` is a snapshot or a bounded follow returning at its end (`capability-api.md` §Statefulness is declared); `device.logcat` is a topic with no producer and no shape (register); `overview.md` §Events: no adapter polls (the core's adapters; the UI is a consumer).
- Draft decision, plan-level (no contract change): the panel polls `device.logs` with `since` (D8). It is G6's own primitive, so the same-primitive check covers it. Costs: about a second of latency; at most `maxLines` per call (a longer burst is marked); each call is an operation retained whole for 15 minutes (no `Slim`) and an ephemeral capture in the 64 MiB budget agents' screenshots share (ADR-0012 §Durability classes): estimated near 55 MB at 200 lines/s once steady (reviewer's estimate), which M3 measures. Trigger: step 5's decisive check. Alternative: a session-owned streaming capability like `file.watch` (a supervised `adb logcat` child with a pid record, ADR-0021 §Pid records; batched untrusted line events): real time and cheaper per line, but a new capability, event payload and process lifetime needing an ADR; it wins if the trigger fires or the owner prefers it now. A smaller mitigation if only memory trips: a `Slim` on `device.logs` (a contract change, ADR-0020's retention rule).

### Q3. What is the capability trace the same-primitive check reads?

- Blocks: steps 3, 8
- The documents say: ADR-0020 §Core primitives (one record type, one trace); `overview.md` §Cross-cutting concerns (structured logs, one operation record per call); the format is a register row.
- Draft decision, plan-level: step 3's line, always on, in the daemon's existing log (ADR-0021 §Layout), which is not rotated (register row; the growth is recorded there at step 9). Alternatives: a trace file under the state root (a new path for ADR-0021); an opt-in variable (a new override in ADR-0024's table); a capability listing operations with their client (a contract change for a debugging need).

### Q4. How does the UI show an image artifact under the CSP?

- Blocks: step 7
- The documents say: `http-websocket.md` rule 7 sets `default-src 'self'`, which refuses `blob:` images; §Endpoints says the UI displays artifacts "through `blob:` URLs". The two conflict.
- Draft decision, plan-level: `createImageBitmap` on the fetched bytes, drawn on a `<canvas>`, which no CSP directive governs; §Endpoints corrected at step 7. Alternative: `img-src 'self' blob:`, a change to the security baseline that would want its own ADR, for no gain.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| The unchanged CSP admits the same-origin WebSocket and the DOM Svelte 5 and Bits UI produce | 2, 4 | the console at steps 2 and 4 after K3's probe (Chrome); Safari and Firefox not checked |
| Polling keeps latency ≤ 1.5 s, the core bounded and agents' artifacts unevicted | 5 | M3 at level 4; step 8 on a real device (latency only) |
| Polling with `applicationId` (`pm`, `pidof` per call) under load | 5, 8 | not measured: `burstadb` has no app; step 8 exercises it at golden-path rates only |
| The installed `@sveltejs/mcp` 0.1.26 exports `svelteAutofixer` as the `.refs` source does, and `svelte/require-each-key` is an error in the ESLint plugin it resolves | 1 | step 1's frozen install and seed (c) |
| `json-schema-to-typescript` renders the schema's shapes into types `svelte-check` accepts | 2 | step 2's `verify` |
| Claude in Chrome's JavaScript tool runs under the app's CSP and its console reader sees CSP reports | 2, 4, 5, 8 | K3's probe at step 2 |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise (approve with changes) | Reviewed the draft written before the owner's correction (budgets). | The correction applied first: A7–A8's budgets, M1–M2, M3, M6–M7 and the measurement step removed; A7 is the burst check only. |
| 1 | | B1 [test] the autofixer CLI prints an inspected object, always exits 0, reads a missing path as code; binary `svelte-mcp`; seed (c) is an ESLint issue | Step 1: the gate imports `svelte_autofixer_handler`, reads files itself, fails on a missing file; the skill edit names `svelte-mcp`; Rests on rewritten; Starting point records it (checked in `.refs`) |
| 1 | | B2 [test] Q2's memory check ends before the 15-minute retention; re-poll has no floor; eviction unobserved | M3 runs 16 minutes with RSS at 1, 8, 16; D8's 250 ms floor with a node test; the first capture read at minute 12; Q2 states the no-`Slim` retention and the estimate |
| 1 | | B3 [test] in-page measurements and "no CSP violation" may be unable to fail; M3 (Event Timing) vacuous | K3's probe (a snippet runs; a deliberate violation is seen) gates all of them; the Event Timing and paint measurements were removed with the budgets |
| 1 | | B4 [test] A3 and A4 had no check | Step 2: the renamed-field schema copy fails `check` (A4); `git archive` checkout built, vetted and tested with no Node (A3); `TestAssetsNilWithoutABuild` |
| 1 | | N1 [plan] Git moved after devices | Said under "The owner should see" |
| 1 | | N2 [bug] hidden tab: rAF stops, the queue grows | The poller stops while hidden and resumes with `since` and a marker, tested |
| 1 | | N3 [safety] `data:` inlining, static `style` attributes, CSP fixes | `assetsInlineLimit: 0` (D1); D6: fixed in code, never by widening; the step-4 size gate removed with the budgets |
| 1 | | N4 [plan] `dev` cannot reach the daemon | Dropped (D1) |
| 1 | | N5 [plan] step 3 shares `httpapi` with step 2; lock; log growth | Step 3 after step 2; the line written outside `mu`, `-race`; Q3 cites the rotation row, updated at step 9 |
| 1 | | N6 [test] seeds do not isolate the gates | The refusing gate named per seed; `check` run alone on (b) |
| 1 | | N7 [test] `burstadb` must honour `-b`/`-T`; `applicationId` not measured | Brief says so; an Unverified row |
| 1 | | N8 [test] cold load unobtainable | Moot: the paint measurement was removed (owner's correction) |
| 1 | | N9 [plan] the owner sees the look late | Step 4 sends the shell screenshots before step 5 |
| 1 | | N10 [test] A2's "explained" escape hatch | A2 lists the allowed extra capabilities; anything else is a defect |
| 1 | | Nits [test]/[doc]: `../` gets 301; Go-only commit with no Node on `PATH`; K2 names its steps; Contract adds ADR-0012, ADR-0019; D5's `href`/`src` rule | All applied |
| 1 | | Size [plan] 38 KB | Measurements folded, Scope rows merged into Not in this milestone, Unverified trimmed |
| 2 | approve with changes | B1–B4 answered; B3 answered cleanly; the correction applied consistently; no dangling step reference | — |
| 2 | | C1 [test] the export is `svelteAutofixer`; seed (c) (`prefer-writable-derived`, `assign_in_effect`) lands in `suggestions`; `filename` selects the compiler (not blocking) | Starting point and step 1 corrected (checked in `.refs/.../handlers.ts`); seed (c) is one `svelte-check` passes and the handler reports as an issue (`require-each-key`, fallback kept); Rests on rewritten |
| 2 | | C2 [test] blocking: RSS rises until minute 15 by design, so "rising between 8 and 16" always fires | M3 runs 24 minutes, RSS at 1, 16, 20, 24; the trigger is minute 24 over minute 16 by more than 10 %; `retainFor` and `DefaultTTL` cited |
| 2 | | C3 [test] blocking: `check` regenerates from the real schema, so the scratch-copy test cannot fail; the hook half had no check | Step 2: the working schema edited and restored; `check` fails, and a schema-only commit is refused with the frontend gate named (it runs before the Go gate) |
| 2 | | N-a [test] the ring cannot fill in 10 s at 4,000 lines/s | A7: the burst runs until the ring is full (≥ 13 s) |
| 2 | | N-b [test] the vendored skill's raw CLI call reads a wrong path as code | The skill runs `web/scripts/autofixer-gate.mjs` |
| 2 | | Nits [test]/[doc]: frontend gate before the hook's early exit; A3's archive after the commit; K3's "under the CSP" over-claims | All applied |

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.
