# Plan: Milestone 2a — the web UI's foundation and a streaming Logcat

- Status: Approved
- Approved: 2026-10-09, Delegated (owner: "hasta completar el primer hito"). The owner can revert any Delegated choice.
- Date: 2026-10-09
- Milestone: "the next milestone is Phase 2, the minimal web UI" (`docs/README.md` §Where the work stands), split by the owner's decisions of 2026-10-09 (Delegated split): **2a** (this plan) = the toolchain and gates, serving with the generated client and the WebSocket, the capability trace, a minimal shell, and Logcat streaming end to end, usable on its own; 2b and 2c under Not in this milestone.
- Phase: `docs/roadmap/mvp.md` §Phase 2 (in part; see Scope).
- Contract: ADR-0039 §1–§5; ADR-0006; ADR-0003; ADR-0010; ADR-0016; ADR-0015 §1, §6; ADR-0019 §Sessions; ADR-0020 §Core primitives; ADR-0021 §Layout, §Pid records; ADR-0028; `docs/interfaces/http-websocket.md` §Security baseline, §Endpoints; `docs/architecture/capability-api.md` §Events and subscriptions, §Discovery and versioning; `docs/architecture/overview.md` §Cross-cutting concerns.

**The owner's decisions of 2026-10-09 this plan follows:** immediacy and lightness chose the framework, they are not budgets (no size or memory item, nothing cut for size); Logcat **streams** (Q2), polling rejected; **no screenshots** in the UI (2b's devices panel has no stills; a live view stays Phase 2's stretch, which 2b decides on cost); **no aesthetics now**: a plain functional UI with native controls, restyled later at the owner's request; the split into 2a, 2b, 2c.

**The owner should see:** Q1 (the built UI is not committed; a binary built without it serves a placeholder; a draft ADR); Q2 (the new capability `device.watchLogs`, a contract change: a draft ADR, the schema's minor version moves); Q3 (the trace is one daemon log line per operation); the working defaults in D8 and A6–A8.

## Scope

| Phase 2 item, or Definition of done item | Here? | If not, where and why |
| --- | --- | --- |
| Svelte 5 + TypeScript app the daemon serves (ADR-0039, ADR-0006) | yes | steps 1, 2 |
| TypeScript client generated from the schema | yes | steps 2, 4 (ADR-0016's "standard generator") |
| Loopback HTTP baseline | exists | 1d; served under it unchanged; Phase 2 adds no rule |
| Panel: Logcat | yes | steps 4–6, streaming (Q2) |
| Same-primitive check: the trace it reads | part | step 3 builds the trace; 2b runs the check |
| Panels: build / Gradle tasks, problems; devices (no screen) | no | 2b |
| Panels: Git, project explorer | no | 2c |
| CodeMirror 6 editor | no | `mvp.md` §Phase 2: deferred to code intelligence (v1) |
| Acceptance: golden path from the browser; DoD item | no | 2b |
| DoD: every deviation recorded | part | 2a's own: Q1's and Q2's ADRs; the screen's removal recorded in `mvp.md` at step 7 |
| `policy.*` from the UI | no panel | already authorised: HTTP requests are client `ui` (ADR-0015 §6) |

## Starting point

Code at `09cda54`. `adapters/httpapi/httpapi.go`: the fence (rules 2–7) before every route, `POST /v1/call`, `GET /v1/artifacts/{id}` (client `ui`), `GET /` a static placeholder under `default-src 'self'; frame-ancestors 'none'`; `ws.go`: one session per connection, auth frame, envelopes served concurrently, `{subscriptionId, event}` frames. `droidyard ui` opens `http://127.0.0.1:<port>/#token=<token>`. The pattern Q2 follows exists: `file.watch` (`internal/domain/file/watch.go`) opens a session subscription with an end hook and publishes with `Bus.PublishTo`, outside the replay ring; `device.logs`' bounded follow runs `adb logcat` through `RunLinesRecorded` with a `device-stream` pid record, killed by pid (never its group), reaped by the next daemon's `Reconcile`; `devicetest`'s fake adb already serves follows. The MCP adapter lists only stateless capabilities (`adapters/mcp/mcp.go`), so a streaming one never reaches agents. Event payloads are not in `schema/capabilities.json`. Operation records hold no client kind. Schema version `0.13.0` (`internal/domain/system/system.go`). The `@sveltejs/mcp` CLI (`svelte-mcp`) prints an inspected object and always exits 0; its package export `.` offers `svelteAutofixer` (`.refs/svelte-ai-tools/packages/mcp-stdio/src/handlers.ts`) returning `{issues, suggestions}`; ESLint errors and compiler warnings go to `issues`, ESLint warnings to `suggestions`.

Baseline on 2026-10-09 at `09cda54` (macOS): `go vet ./...` and `gofmt -l .` printed nothing; `go test ./...` ok in every package. Green: no repair step.

## Run rules

- K1. `B=~/Library/Caches/droidyard-dev/m2a`. Every daemon runs with a scratch `DROIDYARD_STATE_DIR` (`.claude/skills/verify-daemon/SKILL.md` §Launch); `~/Library/Application Support/droidyard` is absent or unchanged before and after.
- K2. Step 6 (a device) follows `m1l-phase1-acceptance.md` §Owner's constraints and §Emulator rules (E1–E5, E7) with `m2a` for `m1l`: the guard as `ADB`, `-tags droidyard_nodump`, the scratch AVD `droidyard_m2a` the only one booted or stopped, 5037's pid unchanged, no `uiautomator dump` of an owner emulator, the SDK stamp unchanged; the Samsung not used; DopamineControl not read.
- K3. Browser runs use Claude in Chrome on a new tab of the owner's Chrome at the URL `droidyard status` prints. Snippets run through its JavaScript tool and are kept as files. **Probe first** (step 2): one snippet's result recorded, and a deliberate violation (a `<script>` element with inline text appended by a snippet) seen in the console reader; until both are seen, "no violation" and every in-page measurement are not evidence.

## Delegated decisions

- D1. **Layout.** `web/` at the root: `package.json`, `pnpm-lock.yaml`, `.node-version` (24.13.1), `vite.config.ts` (`outDir: dist/app`, `assetsInlineLimit: 0`, so nothing becomes a `data:` URI the CSP refuses), `svelte.config.js`, `tsconfig.json`, `index.html`, `src/`, `scripts/`, `embed.go` (Go package `web`). Git-ignored: `src/lib/api/capabilities.gen.ts`, `dist/app/`, `node_modules/`. No `dev` script: the fence ties `Origin` to the daemon's port.
- D2. **Package manager and pins.** pnpm pinned in `packageManager`; exact versions only; the lockfile committed, installed with `--frozen-lockfile`; dependency lifecycle scripts not run (pnpm's default since 10); `minimumReleaseAge` of one day if the host's pnpm has it; `engines.node` with `engine-strict`; nothing through `npx` or `pnpm dlx`. Pins: svelte 5.57.2, svelte-check 4.7.6, @sveltejs/vite-plugin-svelte 7.3.1, vite 8.3.4, @sveltejs/mcp 0.1.26 (step 1's frozen install confirms them); `typescript` ≥ 5.8 and `json-schema-to-typescript`, exact at the latest on the day added. npm runs dependency install scripts by default; bun's lockfile and script handling are less established here.
- D3. **No component library.** Native controls only (`select`, `input`, `button`, checkbox, `<dialog>`), plain styles. A headless library is added only when a needed behaviour has no native control; none in 2a.
- D4. **Transport.** One WebSocket session per tab carries every call; `POST /v1/call` is unused. A `longRunning` capability is sent with `waitMs: 0` and followed with `operation.wait`; others without `waitMs`. Reconnect with backoff (0.25 s doubling to 5 s); a stream is started again with `since` = its last line's time (Q2 ¶2), lines at that instant deduplicated; close 4401 shows "the daemon restarted: run `droidyard ui`".
- D5. **Inert by construction** (rule 8). `TestNoHTMLSinks` (`web`) fails on `{@html`, `innerHTML`, `outerHTML`, `insertAdjacentHTML`, `document.write`, `srcdoc`, and any `href=`/`src=` whose value is an expression. No autolinking. Every field is then inert.
- D6. **Static serving and the CSP.** `GET`/`HEAD /` → `index.html` with the CSP and `Cache-Control: no-cache`; `/assets/<hashed name>` (`.js`, `.css` only) → `Cache-Control: public, max-age=31536000, immutable`; any other path 404. A CSP violation is fixed in the code (no static `style="…"`; `style:` directives set properties through the CSSOM), **never by widening the CSP**.
- D7. **Frontend tests with Vitest** (Svelte's recommendation with Vite; it also runs `.svelte.ts` modules that use runes), in Node, on the logic modules (ring, batcher, stream client, WebSocket client, token bootstrap) with injected sockets, clocks and frame schedulers; `.svelte` files stay thin over them. `vitest` pinned exact like the rest (D2).
- D8. **Working defaults** (constants with a comment): the stream's batch interval 50 ms, at most 500 lines or 256 KiB per event, a pending cap of 10,000 lines; `tail` default 1,000 (max 5,000); the UI's ring 50,000 lines; A6–A8's thresholds.

## Acceptance

- [ ] A1. The core serves the built app under the unchanged baseline: D6's headers and 404s; the placeholder when nothing is embedded; a clean checkout with no `node_modules`, no build and no Node on `PATH` passes `go build ./...`, `go vet ./...`, and the `web` and `httpapi` tests; over step 6's run the console (K3's probe passed) shows no CSP violation; a tab without a token shows "run `droidyard ui`"; the address bar never keeps the token. Source: ADR-0006; `http-websocket.md` §Security baseline; Q1. Proven by: step 2, step 6
- [ ] A2. The TypeScript client, event payloads included, is generated from `schema/capabilities.json` by every `check` and `build`; with a field the UI reads renamed in the schema, `check` fails, and a commit staging only the schema is refused with the frontend gate named. Source: ADR-0016; ADR-0039 §5. Proven by: step 2, step 4
- [ ] A3. The hook refuses each seeded violation, naming the gate: Svelte 4 syntax, a `svelte-check` warning (`check` alone shown failing), an autofixer issue `svelte-check` accepts, a version range; a Go-only commit passes with no Node on `PATH`. Source: ADR-0039 §2, §3. Proven by: step 1
- [ ] A4. `svelte-core-bestpractices` and `svelte-code-writer` are vendored at `6b5d0dab3c9c083387247ab20dc684573481076b` with the MIT licence and provenance, beside Svelte's `llms-small.txt`; no vendored instruction fetches from the network or runs `npx`. Source: ADR-0039 §4. Proven by: step 1
- [ ] A5. Every recorded operation writes one trace line naming its client kind and never its data; step 6's UI calls trace as `client=ui`. Source: ADR-0020 §Core primitives; Q3. Proven by: step 3, step 6
- [ ] A6. `device.watchLogs` behaves as Q2 decides: the schema diff is the new capability with its `events` and the version `0.14.0`, nothing else (`device.logs` and the MCP tool list unchanged); a CLI call fails `SESSION_REQUIRED`; lines reach the subscription within one batch interval of logcat writing them; drops are counted, never silent; on a real emulator the `adb logcat` child ends on unsubscribe, on tab close, on a second stream of the same device in the session, when the device goes, and at `droidyard stop` (its record removed before the daemon exits); a daemon killed with SIGKILL leaves a child the next daemon reaps (step 4, fake follow; on the emulator the reconcile log shows it reaped or already gone). Source: Q2; ADR-0010; ADR-0015 §1; ADR-0021 §Pid records. Proven by: step 4, step 6
- [ ] A7. The Logcat panel shows lines as they arrive, appended once per animation frame; filters (priority, tag, text), an application filter (server-side), pause, follow, clear; it resumes after a reconnect to the same daemon without duplicates, and after a daemon restart shows D4's instruction (the token is per launch); every device field rendered as text. Lines appear within 250 ms p95 of emission at 200 lines/s (working default; the functional meaning of "as they arrive"). Source: owner 2026-10-09 (Q2). Proven by: step 5, step 6
- [ ] A8. **Fluid under a burst** (usability, not a budget; working defaults): following a 5,000 lines/s stream for 15 s, at most 5 % of frames run longer than 1.5 × the idle median frame and no long task reaches 50 ms; the ring stops at its cap counting drops, and the list's DOM is the same size at 1,000 lines and at the cap. Source: owner 2026-10-09 (correction). Proven by: step 5
- [ ] A9. The register, `mvp.md` (§Phase 2 without the screen; the split), `docs/README.md` and the plans index record what 2a closed and left open. Source: `milestone-workflow` step 6; the owner's decisions. Proven by: step 7

## Steps

### 1. Toolchain, gates and the briefing material

- Builds: `web/` per D1–D2: `App.svelte` rendering a title; `compilerOptions.runes: true`; `tsconfig.json` strict with `verbatimModuleSyntax`; scripts `check` (gen, then `svelte-check --fail-on-warnings`), `test` (`vitest run`), `build` (gen, then Vite), `verify` (pins, check, test, build); `scripts/check-pins.mjs` (non-exact spec fails); `scripts/autofixer-gate.mjs` (imports `svelteAutofixer` from the pinned package, reads each file itself, a missing file fails, calls it with `{code, desired_svelte_version: 5, filename}` (the extension selects component or module), fails on non-empty `issues`, prints suggestions). `.gitignore` per D1. `.githooks/pre-commit`, **before** its Go part: when a staged path is under `web/`, is `schema/capabilities.json` or under `.githooks/`, require `pnpm` and `web/node_modules` (else fail naming `pnpm -C web install --frozen-lockfile`), run `pnpm -C web install --frozen-lockfile --offline` if `package.json` or the lockfile is staged, `pnpm -C web run verify`, and the gate on staged `.svelte`, `.svelte.ts`, `.svelte.js`, each gate named on failure. Vendored: `.claude/skills/svelte-core-bestpractices/` verbatim; `.claude/skills/svelte-code-writer/SKILL.md` edited to run `node web/scripts/autofixer-gate.mjs <path>` from the root and to read the vendored `references/svelte-llms-small.txt` instead of `list-sections`/`get-documentation`; each with `LICENSE` and `SOURCE` (upstream, commit, edits). `internal/doccheck` skips the two directories as third-party, one self-test case. `AGENTS.md` §Build and test gains the frontend commands; `implementer-run.md` tells a frontend step to load the two skills and run `verify` and the gate.
- Implements: ADR-0039 §1–§4; D1, D2.
- Run by: the implementer; seeded commits by the main session; review: the main session's diff review (vendored text excluded).
- Check: five seeded commits, the refusing gate read from the output: (a) `export let x` → refused; (b) `<img src="/x.png">` without `alt` → refused, and `pnpm -C web run check` alone fails on it; (c) a component `svelte-check` accepts and the handler reports under `issues`, e.g. `{#each items as item}` without a key (`svelte/require-each-key`; if not an error at the pinned plugin, seed one that is and record it) → refused by the autofixer gate; (d) `"vite": "^8.3.4"` → refused; (e) a Go-only change with no node or pnpm on `PATH` → passes. A clean `pnpm -C web install --frozen-lockfile` succeeds, then `gofmt -l .` prints nothing; `grep -rn -e npx -e 'pnpm dlx'` over `web/` (outside `node_modules`), `.githooks/` and the vendored skills prints nothing; `go test ./internal/doccheck` passes.
- Schema: no change.
- Needs: nothing in this plan.
- Not here: serving the app; the client (step 2).
- Rests on: the installed `svelteAutofixer` reports seed (c) under `issues` while `svelte-check` passes it (level 5, the seeded commit).
- [ ] Done. Evidence:

### 2. The daemon serves the app; the generated client; one live call

- Builds: `web/embed.go`: `//go:embed all:dist` over a committed `web/dist/.keep`; `Assets() fs.FS` is `dist/app` when it holds `index.html`, else nil. `adapters/httpapi`: `New(…, ui fs.FS)` serves D6, the CSP unchanged; nil serves the placeholder, naming `pnpm -C web build` and a rebuild. `cmd/droidyard/serve.go` passes `web.Assets()`. `web/scripts/gen-client.ts`: one schema composed from `schema/capabilities.json`, compiled with `json-schema-to-typescript` into `capabilities.gen.ts` (params from `input`; result from `output`, or `output.properties.result` when `longRunning`; a `const` map of `scope`, `longRunning`, `statefulness`). `src/lib/api/client.ts` (D4), `token.ts` (fragment → `sessionStorage`, stripped with `history.replaceState`; none → the instruction view). `App.svelte`: connection state, `system.health`'s version and uptime.
- Implements: ADR-0006; ADR-0039 §1, §5; ADR-0016; `http-websocket.md` §Security baseline, §Endpoints; D4–D6; Q1.
- Run by: the implementer (effort high); review: **Opus verifier** (the HTTP edge, the token).
- Check: `TestUIIsServedBehindTheFence` (`httpapi`, `fstest.MapFS`): `/` with the CSP, `nosniff`, `no-cache`; an asset with `immutable` and its type; `/x` and a `.map` or `.svg` in the FS → 404; a raw path `/assets/../index.html` → 404 (or the mux's 301 to a path that 404s, asserted as such); `POST /` 405; a wrong `Host` 403 before any file is read; `/` needs no token. `TestUIPlaceholderWhenNotBuilt` (nil) and `TestAssetsNilWithoutABuild` (`web`, only `.keep`). `TestNoHTMLSinks` seen failing on `{@html x}` and on `href={x}`. Vitest: `client.test.ts` (auth frame first; replies by id; events by `subscriptionId`; an error's `code` surfaced; 4401 → `unauthorized`; reconnect) and `token.test.ts`. A2 (main session): `system.health`'s uptime field renamed in the working schema → `pnpm -C web run check` fails on the field `App.svelte` reads, and a commit staging only the schema is refused with the frontend gate named; the file restored. A1, after the step's commit: `git archive HEAD | tar -x` into a scratch directory, then, no Node on `PATH`, `go build ./... && go vet ./... && go test ./web ./adapters/httpapi`. Level 5 (K1, K3): K3's probe; `droidyard ui --no-autostart`; health shown; `location.hash` empty; no CSP violation; reload works; a tab without the fragment shows the instruction; `curl -sI` of `/` and an asset show D6's headers.
- Schema: no change.
- Needs: step 1.
- Not here: Logcat (steps 4–5); `POST /v1/call` from the UI (D4).
- Rests on: the static routes sit behind the same fence and reach no capability (level 4); the CSP admits the same-origin WebSocket and the built app (level 5, Chrome only).
- [ ] Done. Evidence:

### 3. The capability trace (Q3)

- Builds: `internal/capability/operation.go`: `operation` records `client` from `Request.Client` at `Start`; at its terminal state `Operations` writes, **after releasing `Operations.mu`**, one line through an injectable logger (default `log.Printf`): `droidyard: op <id> client=<cli|mcp|ui> capability=<name> workspace=<wsid|-> state=<state> ms=<n>[ code=<CODE>]`; never params, results, messages or causes. The artifact route's test gains its `client=ui capability=artifact.read` line. `overview.md` §Cross-cutting concerns: one sentence naming the line as the trace.
- Implements: ADR-0020 §Core primitives; Q3.
- Run by: the implementer; review: the main session's diff review.
- Check: `TestEveryOperationIsTracedOnce` under `-race`: each client kind ending completed, failed (its code on the line), cancelled, and `OPERATION_QUEUE_TIMEOUT` gives exactly one line of that shape; `operation.wait`/`operation.cancel` none; a sentinel in a param and in a result never appears; seen failing with the client unrecorded and with params on the line.
- Schema: no change.
- Needs: step 2 (both touch `httpapi`).
- Not here: a trace file, a switch or an operation listing (Q3); `daemon.log` rotation (register).
- Rests on: the line carries no request or device data (level 4).
- [ ] Done. Evidence:

### 4. `device.watchLogs` in the core (Q2)

- Builds: Q2's ADR first (main session). Then `internal/domain/device/watchlogs.go`: the descriptor and handler per Q2; a long-lived variant of the recorded runner (no window; on the stream's own context, Q2 ¶6); the batcher (D8, one event per tick) publishing with `PublishTo`; one stream per (session, device); the stream set with `StopAll(ctx)`, called from the daemon's `AfterOps`; the end paths of Q2 ¶6. `devicetest`'s fake: `-T <digits>` prints the newest N lines then follows (today any `-T` is an epoch), and a scripted exit partway through a follow. `internal/capability` descriptors gain an optional `Events` (topic, and per type the payload's Go type); `internal/schema` publishes it as `events: {topic, types: {<type>: <schema>}}` with `x-droidyard-untrusted` from the payload's tags; `system.Version` → `0.14.0`; `go test ./internal/schema -update`. `gen-client.ts` emits the event payload types. `capability-api.md` §Events and subscriptions: `device.logcat` joins `file.changes` as a capability's own stream.
- Implements: Q2 (ADR); ADR-0010; ADR-0015 §1; ADR-0016; ADR-0021 §Pid records; `capability-api.md` §Discovery and versioning.
- Run by: the implementer (effort high); review: **Opus verifier** (a new capability and published shape, process lifetime, untrusted text).
- Check: with `devicetest`'s fake adb follow, `TestWatchLogsStreamsBatches` (lines that come due **after** the call completed are published within one interval, ≤ 500 per event, `untrusted: true`; seen failing with the batcher removed and with the child on the operation's context); `TestWatchLogsCountsDrops`, two mechanisms apart: a backlog over 10,000 lines (`since` before them) gives `dropped` in the next event, and a subscription that does not drain (`Session.SetQueue`) gives `events.gap`; `TestWatchLogsEndsItsChild` (unsubscribe, session close, a second stream of the same device, a scripted exit with the serial gone → `stopped: deviceGone`, otherwise `exited`, operation cancel during start: each leaves no child and no pid record); `TestWatchLogsNeedsAWebSocketSession` (CLI and HTTP: `SESSION_REQUIRED`; not in the MCP tool list); `TestWatchLogsApplicationFilter` (uid-filtered; no uid column, and an application `pm` does not list, each fail `DEVICE_LOGS_FAILED`); `TestWatchLogsTailAndSince` (`tail` N gives the newest N, `since` an epoch). `cmd/droidyard`: `TestStopAllWaitsForTheReap` (an injected recorded starter whose `Wait` blocks until released: `StopAll` has not returned before the release, both for a stream it ends and for one whose end hook fired first; seen failing with removal from the set at the hook and with a `StopAll` that does not wait); `TestWSWatchLogsStopped` (after `TestWSFileWatch`: a client gets `lines` frames then `stopped: replaced`; seen failing with a `Close` after the publish, the writer held with `SetWriteHook` while `stopped` is queued so the failure is deterministic); `TestGracefulStopEndsAStream` (after `TestGracefulStopCancelsABoundedFollow`, level 4 end to end: a stream open over a WebSocket, `Shutdown` leaves the fake's follow dead and no `device-stream` record); a daemon killed with SIGKILL mid-stream, the next one reaps the fake's follow (it writes nothing once its lines are out, so it survives its reader). `internal/schema`'s untrusted test walks `events` payloads. The schema diff reviewed line by line: the new capability with `events`, the version, nothing else.
- Schema: `device.watchLogs` added (with `events`); `version` `0.13.0` → `0.14.0`; no other line.
- Needs: step 2 (the generator), step 1 (the hook's gate on the schema).
- Not here: the panel (step 5); crash summaries in the stream (`device.logs` keeps them); event schemas for `file.watch` (2c).
- Rests on: a stream's child never outlives its subscription or the daemon unrecorded (level 4 here, level 5 at step 6).
- [ ] Done. Evidence:

### 5. The minimal shell and the Logcat panel

- Builds: a plain shell: a device `select` (`device.list`), the panel, the connection state. `src/lib/logcat/ring.ts` (circular, D8's cap, counting drops); `batcher.ts` (events queued, moved into the ring once per animation frame; while the tab is hidden the queue is capped and drops are counted); `stream.ts` (`device.watchLogs {device, tail, applicationId?}`; on reconnect, `since` = the last line's time with boundary dedup; on `stopped`, its reason shown and `events.unsubscribe` sent; `dropped` and `events.gap` shown as markers); `filter.ts` (priority, exact tags, case-insensitive substring, on the client; the application filter restarts the stream). `Logcat.svelte`: a fixed-row virtual list (visible rows plus overscan, scroll once per frame), follow at the bottom, a "N new lines" pill, pause, clear; a footer with lines held and dropped; every device field as text. `web/testdata/burstadb/` (Go `main`, outside `./...`): a fake `adb` answering `version`, `devices -l` (one emulator), `getprop`, `emu avd name`, the uid-column probe, and a following `logcat` at the rate `burst.json` sets, each message stamped with its host-clock emission time.
- Implements: owner 2026-10-09 (Q2; plain UI); D3, D5, D7, D8.
- Run by: the implementer (effort high); review: **Opus verifier** (untrusted text, the stream's lifetime in the client).
- Check: Vitest: the ring's cap and drop count; one flush per frame however many events arrive; the hidden-tab cap; reconnect restarts with `since` and dedups the boundary; markers for `dropped`, `events.gap`, `stopped`; filters. Level 4 (K1, K3) against a daemon with `ADB` = `burstadb` and an empty `ANDROID_AVD_HOME`: A8's burst (a `requestAnimationFrame` loop recording frame deltas plus a `longtask` observer, 2 s idle, then 5,000 lines/s for 15 s; the footer's counts; `querySelectorAll('*').length` in the list at 1,000 lines and at the cap); A7's latency (200 lines/s for 2 minutes, a `MutationObserver` comparing each line's stamp with `Date.now()`, p95).
- Schema: no change.
- Needs: steps 3, 4.
- Not here: other panels (2b, 2c); styling beyond legibility (owner).
- Rests on: per-frame batching keeps the list fluid whatever the event rate (level 4, `burstadb`).
- [ ] Done. Evidence:

### 6. Logcat on a real emulator, and the stream's lifetime

- Builds: nothing unless a defect is found (each its own step before step 7, with a failing test first).
- Implements: A1, A5, A6, A7 at level 5.
- Run by: the main session under K1–K3.
- Check: `droidyard_m2a` booted by `droidyard call emulator.start`; the panel streams it (a known line from `adb -s <scratch> shell log -t DyCheck hello` appears); application filter on a system app; pause, follow, clear. Lifetime, each followed by `processes.json` and `ps -p <the recorded pid>`: unsubscribe (switching device), tab closed, a second stream of the same device, `emulator.stop` (→ `stopped`), `droidyard stop`; after a SIGKILLed daemon, the next start's reconcile log says the child was reaped or was already gone (a real logcat dies of SIGPIPE without its reader; the reap itself rests on step 4). Trace lines `client=ui` for the UI's calls; no CSP violation (K3). 5037's pid unchanged; zero orphans after `verify-daemon` §Cleanup.
- Schema: no change.
- Needs: step 5.
- Not here: an agent's `device.logs` (unchanged).
- Rests on: the child is ours to end in every path (level 5).
- [ ] Done. Evidence:

### 7. Close: documents and register

- Builds: `docs/roadmap/known-limits.md`: `device.logcat` removed from the "event topics with no producer" row; event schemas for `file.watch`; Safari and Firefox unchecked; the capability-trace row closed in part; the `daemon.log` rotation row noting the trace's growth; the WKWebView shell and the `.git/info/exclude` option (owner's ideas); 2b and 2c named. `docs/roadmap/mvp.md` §Phase 2: the devices panel without stills (owner, 2026-10-09), the live view still a stretch that 2b decides on cost, and the split, citing this plan. The close commit is `milestone-workflow` step 6.
- Implements: `milestone-workflow` step 6; A9.
- Run by: the main session; review: **Opus verifier** over A1–A9.
- Check: `go test ./internal/doccheck` passes; every Not-in-this-milestone item has a register row or a named milestone.
- Schema: no change.
- Needs: step 6.
- Not here: 2b's plan.
- Rests on: —
- [ ] Done. Evidence:

## Not in this milestone

- **2b**: build and Gradle tasks with problems; devices (list, start, stop, install, launch, tap and type, no screen); the golden path from the browser with the same-primitive check, closing Phase 2's acceptance; an operation activity feed — every operation's start and end with capability and client kind as events (a new topic, built on 2a's capability trace) and a panel listing them, so the UI shows live what an agent does through the shared daemon (owner, 2026-10-09, confirmed); **controls that mirror shared state** — the UI's controls reflect operations any client runs (the build button shows "building" and offers cancel while an agent's build runs; emulator start and stop reflect an agent's), built on the same activity events plus a query for the state at load (owner, 2026-10-09). Its G6 in the browser is logs (the stream, or `device.logs` since launch) and the UI tree, which tap and type need to pick a target; no screenshot (owner) — the deviation from `mvp.md` §Phase 2, recorded at step 7.
- **2b, if cheap enough (owner): the live emulator screen** (Phase 2's stretch). Cost estimate for 2b to decide: *exists* — `device.screenshot` (`screencap -p` to an artifact; `artifact.read` resizes with `maxEdge`), the artifact route, 2a's session-owned stream pattern (recorded child, `PublishTo`, published event payloads), pid records for a `screenrecord` child (ADR-0021 §Pid records), and the decision that a smooth view means an H.264 path behind a `StreamSource` seam (`docs/subsystems/adb-device.md` §Screen viewing); *missing* — the seam itself, a new streaming capability and topic (an ADR, a minor version), a frame transport (the WebSocket carries text only, so frames would be base64 in JSON or a change to binary frames), and the image display the owner's no-screenshot decision removed (the CSP refuses `blob:`; a canvas would be needed). A `screencap` loop on 2a's pattern is a few steps and a slideshow (2–8 fps per the prior art), and §Screen viewing rejects exactly that ("not a PNG-per-frame loop"), so choosing it needs an ADR superseding that decision; the H.264 path (an on-device encoder, binary frames, WebCodecs) is a milestone of its own.
- **2c**: Git with `git.*`, and the project explorer.
- The code editor (CodeMirror 6): v1 with `code.*`. Styling, layout and a component library: when the owner asks.
- A policy or trust panel; the `.git/info/exclude` option for `init`'s files; a WKWebView shell (owner's ideas): register.
- DoD items left after Phase 2: `build.errors` on both KGP fixtures and the second Gradle spike; the per-process idle footprint (register).
- Size, memory or paint budgets, and any cut for size (owner). Safari, Firefox; LAN access.

## Design questions

Each has a recommendation; the main session may apply it as Delegated, and Q1 and Q2 become ADRs before the steps that build them.

### Q1. Must `go build ./...` work without Node, and where does the built UI live?

Decided: [ADR-0040](../adr/0040-built-ui-not-committed.md) (Delegated).

### Q2. How does Logcat stream to the UI? (the owner chose streaming)

Decided: [ADR-0041](../adr/0041-device-watchlogs-stream.md) (the owner chose streaming; the shape Delegated). Its numbered paragraphs are the "Q2 ¶n" this plan cites.

### Q3. What is the capability trace the same-primitive check reads?

- Blocks: step 3
- The documents say: ADR-0020 §Core primitives (one record, one trace); `overview.md` §Cross-cutting concerns; the format is a register row.
- Draft decision, plan-level: step 3's line, always on, in the daemon's existing log (ADR-0021 §Layout), which is not rotated (register row, updated at step 7). Alternatives: a trace file (a new path for ADR-0021); an opt-in variable (a new override in ADR-0024's table); a capability listing operations (a contract change for a debugging need). **Option for 2b, not in 2a's scope:** the point where step 3 writes the line already holds the whole record and `Operations` already has its publisher (`UsePublisher`, as progress uses), so the same record could be published as an event at little cost; a start event needs one more call at `Start`. It is still a new topic with a published payload (a minor version and an ADR), which is why it waits for 2b's activity feed.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| The unchanged CSP admits the same-origin WebSocket and the DOM Svelte 5 produces | 2, 5 | the console at steps 2 and 6 after K3's probe (Chrome); Safari and Firefox not checked |
| The installed `@sveltejs/mcp` 0.1.26 exports `svelteAutofixer`, and `svelte/require-each-key` is an error at its plugin | 1 | step 1 |
| `json-schema-to-typescript` renders the schema's shapes, event payloads included, into types `svelte-check` accepts | 2, 4 | `verify` at steps 2 and 4 |
| Long-lived `logcat` clients behave on the scratch image (API 31) as `device.logs`' bounded follow did: no stall, exit when the emulator stops | 4, 6 | step 6 |
| Claude in Chrome's JavaScript tool and console reader behave as K3 needs | 2, 5, 6 | K3's probe at step 2 |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| earlier | approve with changes | Two rounds reviewed the earlier nine-step draft (polling, screen, look). Carried here: B1/C1 (autofixer handler, seed), B3 (K3's probe), B4/C3 (A1's and A2's checks), N2 (hidden tab), N3 (`data:` inlining, CSP fixed in code), N4 (no `dev`), N5 (trace outside the lock, after step 2), N6 (gates named), nits | Kept in steps 1–3, D1, D6, K3 |
| 1 | revise (approve with changes) | B1 [safety][test] blocking: nothing ends or waits for the stream's child at `droidyard stop` | Q2 ¶6: a stream set whose `StopAll(ctx)` the daemon's `AfterOps` calls, waiting for each reaping; step 4: `TestGracefulStopEndsAStream`, seen failing without it |
| 1 | | B2 [contract][bug] blocking: what follows `stopped`; closing the subscription discards it | Q2 ¶4: the subscription stays open and empty until unsubscribe or session end; step 5 unsubscribes on `stopped`; step 4: `TestWSWatchLogsStopped` |
| 1 | | B3 [test] blocking: `PublishTo` never blocks, so the drop test cannot fill the pending buffer | Q2 ¶5: one event per tick (ceiling near 10,000 lines/s); the test drives the pending cap (a backlog) and the gap (`SetQueue`) apart |
| 1 | | B4 [doc] blocking: three § references failed doccheck | Rewritten; doccheck passes but for the index row |
| 1 | | N1 [bug] the child on the operation's context dies when the call completes | Q2 ¶6: its own context; the test needs lines due after completion |
| 1 | | N2 [test] the fake reads any `-T` as an epoch and cannot exit mid-follow | Step 4 Builds extends `devicetest` |
| 1 | | N3 [bug] `tail: 0` | `tail` is 1–5,000 |
| 1 | | N4 [plan] no resume across a daemon restart (per-launch token) | A7 reworded |
| 1 | | N5 [contract] `buffers` unused; `LogLine`'s `crash` would leak into the payload | `buffers` cut; `StreamLine` its own type |
| 1 | | N6 [contract] an application not yet installed | Q2 ¶2: `DEVICE_LOGS_FAILED`, next step "install it, then start again" |
| 1 | | N7 [test] SIGPIPE masks step 6's reap and `pgrep` misses adopted children | Step 6 reads the recorded pid and accepts reaped or gone; the reap rests on step 4 |
| 1 | | Nits [doc]/[test]/[contract]: D4's `since`; register row for `device.logcat`; `destructiveHint`; untrusted test walks `events`; policy re-check at start only | All applied |
| 2 | approve with changes | B1 answered with a new gap; B2, B3, B4 answered (doccheck's section test passes) | — |
| 2 | | N-R2-1 [safety][test] (important): the session end hook races `StopAll`; when a stream leaves the set was unstated; the daemon-level mutation could not fail | Q2 ¶6: a stream leaves the set only once reaped, and `StopAll` waits for hook-ended streams too; step 4: `TestStopAllWaitsForTheReap` with a blocking injected `Wait`; `TestGracefulStopEndsAStream` kept as level 4 without the mutation claim |
| 2 | | Nits [test]/[contract]/[doc]: `TestWSWatchLogsStopped`'s mutation races the drain; which ends publish `stopped`; `maxEdge` belongs to `artifact.read`; the screen line vs §Screen viewing and "post-MVP" | `SetWriteHook` holds the writer; only `replaced` and an uncaused exit publish `stopped`; cost line corrected and names the superseding ADR a `screencap` loop needs; header, Not in this milestone and step 7 say "stretch, 2b decides on cost" |

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.

- 2026-10-09, at approval (Delegated): D7 moves from `node --test` to Vitest, Svelte's documented recommendation with Vite, which also runs rune modules; step 1's `tsconfig` drops the type-stripping options it needed. Q1 and Q2 became ADR-0040 and ADR-0041; their drafts are replaced by pointers (the reviewed text is in the ADRs).
