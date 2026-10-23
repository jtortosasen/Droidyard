# Plan: Milestone 3d — Graphite Console: a type scale, two faces and a palette that reads

- Status: Approved
- Date: 2026-10-23
- Milestone: "Improve the UX and legibility of the UI: fonts are far too small, every text uses the same typeface, the colours are ugly. A facelift, in short." (the owner, 2026-10-23, translated). Scope "visual + contrast": a type scale, distinct faces for UI and for code, logs and figures, a palette for dark and light with spacing to match, and the audit's colour P1s; keyboard and ARIA deferred. The owner chose the direction "Graphite Console" from nine sketches. `docs/README.md` §Where the work stands says "next, the owner's choice".
- Phase: none. Phase 3 is use and polish; the Scope table is the boundary.
- Contract: the direction contract `/Users/jorgetortosa/Documents/AioAndroidDev/.claude/worktrees/m3d/.impeccable/surfaces/web-src-app-svelte.md` and its sketch `…/m3d/.impeccable/mocks/decision/challenger-console.html` (local, git-excluded, present only in the m3d worktree: cited by absolute path, never copied into a shipped file; its CSS is the dark target); ADR-0039 §1–§3, §Consequences (libraries are the plan's); ADR-0040 §1; ADR-0050 §1, ADR-0052 (the sessions column); m2a D1, D2, D6 (no `data:`, exact pins, `/assets/` serves `.js` and `.css` only, the CSP never widened); `http-websocket.md` §Security baseline rule 7; m2d Style C and D24 (native chrome and system font, replaced by the owner's 2026-10-23 choice; D24's one tokens file kept); m2i D2, D12 (the editor's colours are tokens). Q1: [ADR-0128](../adr/0128-ui-own-faces-and-contrast-floor.md).
- Register: the commit that adds this plan splits row 515 (3c review round) into its visual clauses (the column's "Sessions" title above both lists; titles cut short in the narrow column; Release wrapping below a connected row), Work `3d`, and its Copy clause, which keeps Work `review`. Row 490 (the Git tab's look) is already closed ("2o, reviewed, fine as it is"): not gathered; the Git tab is swept anyway (step 8). Rows 504 and 510 stay out: neither is purely visual.
- Parallel: steps 5–10 once step 4 has landed. Each runs in its own worktree (`pnpm -C web install --frozen-lockfile` there first), touches only its own components, never `tokens.css`, `base.css`, `fonts.css` or the `ui/*.test.ts` files; a token it lacks goes back to the main session, which adds it on `m3d`. In the worktrees each step's check is its unit tests, `verify` and the gate; the level-5 probe runs once on the merged tree.

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| Type scale: body 13.5 px sans, code 13.5/22 mono, logs 12.5/19 mono, headers 11.5 px uppercase .09em on raised bars, no text under 11.5 px, tabular figures | yes | step 4; steps 5–10 per area |
| Two faces, Geist and Geist Mono, in the bundle from pinned packages; the daemon serving them | yes | Q1; step 3 |
| A dark palette (the sketch's world) and a light reading with the same roles and seams, accents darkened | yes | step 2 |
| Every text pair at WCAG 2.2 AA (4.5:1), marks at 3:1, both themes, measured from `tokens.css` | yes | step 2 |
| Spacing and sizes to match (44 px toolbar, 34–36 px bars, 24/28 px rows), both densities kept | yes | step 4; steps 5–10 |
| Colour P1s: Logcat's light-only colours; `var(--bg)` undefined; Console's colourless border; `.soon` at 9 px; LiveView's Bootstrap banner; `--text-3` at 3.0:1 dark, 2.4–2.6:1 light; light `--accent` 3.69:1 | yes | step 2 (colours), step 4 (sizes), steps 9, 10 (restyled) |
| Every hard-coded colour, font size and line height through tokens, kept so by a test | yes | steps 2, 4 |
| Row 515: column title, title truncation, Release wrapping | yes | steps 5, 6 |
| Row 515: Copy on a connected chat's row | no | a function, not a look; ADR-0095 §1 joins only the most recently seen session of a chat, and a resume command for a chat a client holds starts a second client on it; stays `review` |
| Sessions inside the left panel above the tree, as the sketch draws them | no | the docked column is ADR-0050 §1 and ADR-0052; moving it needs a superseding ADR, outside "visual + contrast"; the column takes the sketch's styling |
| Keyboard, focus order, ARIA | no | owner: deferred; register rows at step 11 |
| A theme switch, more themes, italics, a display face | no | not asked; dark and light follow the system |

## Starting point

- Branch `m3d` at `8bb9062` (main, after 3c). No `web/node_modules` in this worktree yet.
- **Tokens**: `web/src/lib/ui/tokens.css` (m2d D24): dark default, light under `prefers-color-scheme: light`; `system-ui` and `ui-monospace` stacks; `--fs-base` 13px, `--fs-small` 11px (100 uses), `--fs-mono` 12px; `--row` 22px, `--tab-height` 28px; `[data-density=compact|comfortable]` (switched in `Shell.svelte:234`); `--syn-*` (`editor/cm/highlight.ts`), `--lane-0..7` (`git/graph.ts:31`). `base.css`: body `13px / 1.4`, `h2` 11px uppercase .04em. Both imported by `main.ts`. `--tab-height` is read by `Shell.svelte:521, 615` and `Editor.svelte:157`.
- **Literals** (`web/src`, tests excluded, counted 2026-10-23, confirmed by the reviewer): 9 hex colours (Logcat ×5, LiveView ×3, HistoryPanel ×1), 5 dialog scrims, 1 shadow, 3 `hsl()` with literal channels, 2 `var(--bg)` (undefined), 1 colourless `border: 1px solid` (`Console.svelte:179`); 13 font sizes (9px in `Shell:639` and `SessionsToggle:84`, 8px in `HistoryPanel:907`, …), 5 line heights (`SessionsToggle:85`, `HistoryPanel:676, 718`, `theme.ts:23` `'1.5'`, `base.css:13` `1.4`), 8 `letter-spacing: 0.04em`; text dimmed by opacity in `HistoryPanel.svelte:870` (.55), `cm/review.ts:253` (.55), `FileTree.svelte:759` (.5), `LiveView.svelte:312` (.75). Spacing is mostly tokens (313 uses against 17 px literals).
- **Row heights in script**: `Logcat.svelte:35` and `Console.svelte:16` `ROW_HEIGHT = 18`, with `height: 18px` at `:326` and `:189`; `git/graph.ts:15` `ROW = 24` matching `HistoryPanel.svelte:825, 877` (left alone); `FileTree.svelte:90-98` measures `--row`, only when its model changes.
- **Serving**: `adapters/httpapi/httpapi.go:217` `assetTypes` = `.js`, `.css`; `TestUIIsServedBehindTheFence` (`ui_test.go:60`) asserts `.svg`, `.map` 404. CSP `default-src 'self'` (`:179`) admits same-origin fonts; `vite.config.ts` `assetsInlineLimit: 0`, flat `assets/`.
- **Vitest**: environment node; a raw `import.meta.glob` of a `.css` file yields `""` (vitest 5.0.3's `vitest:css-disable`, default `test.css.include: []`; read in its source by the reviewer, not run). `screen/golden.ts` reads a file with `readFileSync(new URL(…, import.meta.url))` through `screen/node-fs.d.ts`.
- **The fonts, observed 2026-10-23 (npm registry, jsDelivr):** `@fontsource-variable/geist` 5.3.0 and `@fontsource-variable/geist-mono` 5.3.0, published 2026-07-19 (past `minimumReleaseAge`), `license` OFL-1.1 with a `LICENSE`, no dependencies, 181 KB and 172 KB unpacked, woff2 only in unicode-range subsets; `wght.css` declares `'Geist Variable'` and `'Geist Mono Variable'`, weight 100–900, `font-display: swap`, relative `url(./files/…)`; latin normal 29,400 B and 23,128 B. Chosen over Vercel's `geist` 1.8.2 (peer-depends on `next`, ships no CSS) and `@fontsource/geist` (static weights, about ten times the files); ADR-0039 §Consequences leaves the library to the plan. Geist's TTF has `tnum`; Geist Mono needs none (monospaced). Whether the subsets keep `tnum` is not observed.
- **Baseline, 2026-10-23 at `8bb9062`:** `go vet ./...` clean; `go test ./...` ok in every package. `pnpm -C web run verify`, the probe and the detector: step 1.

## Acceptance

- [ ] A1. **Contrast**: in dark and in light, every text token on every surface it is used on reaches 4.5:1 and every mark (state dots, focus ring, selection edge, graph lanes) 3:1, computed by a Vitest from `tokens.css`, which fails on today's tokens; and no text in the running UI is dimmed by opacity outside a disabled control. Source: owner (colour P1s, WCAG 2.2 AA); Q1. Proven by: steps 2, 11.
- [ ] A2. **No literal**: no colour, font size, line height or font family outside `tokens.css` in `web/src` (tests excepted), and every `--fs-*` token ≥ 11.5 px, asserted by Vitests; `var(--bg)` and the colourless border gone. Source: audit P1s; D24. Proven by: steps 2, 4.
- [ ] A3. **Two faces, bundled**: the UI in Geist; code, logs, paths and figures in Geist Mono, tabular; from the two pinned packages; served as `font/woff2`; no request off the page's origin and no CSP violation; the built CSS carries the OFL text. Source: owner; Q1. Proven by: step 3.
- [ ] A4. **The scale**: body 13.5 px, code 13.5/22, logs 12.5/19, headers 11.5 px uppercase .09em on raised bars, no visible text under 11.5 px, at 1440×900 and 720×450 at scale 2 (200 % zoom). Source: owner; contract. Proven by: steps 4, 11.
- [ ] A5. **The look per area**: each area's own assertions (steps 5–10) hold on the merged tree in both themes, and screenshots of each beside the sketch go to the owner. Source: owner; contract. Proven by: steps 5, 6, 7, 8, 9, 10, 11.
- [ ] A6. **Row 515's visual clauses**: no heading titles both lists; a chat title wraps to two lines before it is cut and shows whole on hover; Release stays on its row's line at the column's 220 px minimum. Source: row 515. Proven by: steps 5, 6.
- [ ] A7. **Nothing else changes**: both densities switch; `pnpm -C web run verify` and `go test ./...` pass; the main session's functional pass works as on `main`; no schema change. Source: ADR-0039 §3; owner. Proven by: steps 4, 11.

## Steps

### 1. Baselines (main session)

- Builds: no product code. `pnpm -C web install --frozen-lockfile`; `pnpm -C web run verify` on `main`'s tree. A `verify-daemon` §Launch and §Doctor with a binary built after `pnpm -C web build`, `$R/proj` holding the sketch's Kotlin files and a Git history; headless Chrome with its own profile over CDP; the owner's daemon and adb server snapshotted (memory: compare to this baseline). **The probe**, a script kept under `$E` and reused by steps 3–11 (not committed): for a root selector it returns the smallest computed `font-size` of any element with a visible text node; elements with a visible text node whose effective opacity (the product over ancestors) is below 1 outside a `:disabled` control; the font families in use; and the named per-area assertions of steps 5–10. Run on `main`'s build in both themes (`Emulation.setEmulatedMedia`) at 1440×900 and 720×450 scale 2; screenshots of the project view, Git, Logcat, Devices. `~/.claude/skills/impeccable/scripts/impeccable detect web/src`: its count.
- Implements: `milestone-plan` (risk first: the "seen failing" claims of steps 3–11 compare against these numbers).
- Check: the evidence lists: `verify` green; the probe's smallest size (9 px expected: `.soon`), its opacity list, its families (system ones), and each area assertion of steps 5–10 failing; the detector's count.
- Schema: no change.
- Needs: nothing.
- Not here: no product change.
- Rests on: —
- [x] Done. Evidence: 2026-10-23, `$R=/private/tmp/dy.6UYdEk`, at `3f2376c`:
  - `verify` was green. The owner's daemon was pid 70217 and the adb server pid 72901, recorded as the baseline.
  - The probe is `$E/probe/` (`cdp.mjs` drives Chrome over CDP and `probe.js`/`areas.js` run in the page, shadow roots included because the editor lives in one). It ran on the golden sample with a five-commit history, `$E/step1/summary.txt`.
  - Results:
    - The smallest text was 9 px (`.soon`), or 8 px once History shows (the avatar initials).
    - Text dimmed by opacity: History's subject, author, sha and time at .55.
    - Families in use were `system-ui` and `ui-monospace`.
  - Area assertions failing:
    - step 5: toolbar 38 px, no tab underline, status bar not mono, the column head has a heading;
    - step 7: file-tab edge, active line, gutters;
    - step 8: History head, sha not in Geist Mono.
  - Area assertions not run:
    - Step 5's "Run and Build reachable at 720 px" already holds; it stays as a regression guard.
    - Steps 9 and 10 need log lines and a running emulator, which this launch has not set up; they are set up at those steps.
  - Detector: 2 findings, the two kept 3 px status edges (`Act.svelte:243`, `Activity.svelte:414`).

### 2. The palette and the contrast floor

- Builds: load `svelte-core-bestpractices` and `svelte-code-writer` first.
  - `web/src/lib/ui/contrast.test.ts` (new), written first. It reads `tokens.css` with `readFileSync(new URL('./tokens.css', import.meta.url))` (the `screen/golden.ts` precedent; the `node:fs` declaration moved from `screen/node-fs.d.ts` to `web/src/node-fs.d.ts` if needed) and fails when the text is empty; takes `:root` as dark and the `@media (prefers-color-scheme: light)` `:root` over it as light; resolves `var(--x)` between tokens; accepts only `#rgb`, `#rrggbb` and `rgb(r g b / a)` (anything else fails, naming the token); composites alpha over the surface tested; WCAG 2.2 luminance and ratio. Pairs in each theme: `--text`, `--text-2`, `--text-3`, `--accent`, `--success`, `--warning`, `--danger` on `--bg-window`, `--bg-panel`, `--bg-raised`, `--bg-hover` and `--accent-soft` over `--bg-panel` ≥ 4.5; the same roles on `--warning-soft` and `--danger-soft` over `--bg-window` and over `--bg-panel` ≥ 4.5; `--text`, `--text-2` on `--bg-active` ≥ 4.5; `--accent-text` on `--accent`, `--danger-text` on `--danger` ≥ 4.5; every `--syn-*` on `--bg-window`, `--bg-panel`, `--bg-raised` and on `--success` and `--danger` at 12 % over `--bg-window` (Review's `mix`, `cm/merge.ts:28`) ≥ 4.5; marks (`--accent`, `--success`, `--warning`, `--danger`, `--text-3`, `--lane-0..7`) on `--bg-window`, `--bg-panel`, `--bg-raised` ≥ 3; `--avatar-text` on `hsl(h, --avatar-s, --avatar-l)` for every hue 0–359 ≥ 4.5. Light overrides every colour token dark defines, lanes included. A failure names theme, pair and ratio.
  - `web/src/lib/ui/literals.test.ts` (new): `.ts` and `.svelte` files through `import.meta.glob('/src/**/*.{ts,svelte}', { query: '?raw', eager: true })`; `.css` files by `readFileSync`, each non-empty, their list asserted equal to `Object.keys(import.meta.glob('/src/**/*.css'))` (not eager) less `tokens.css`; tests excluded. Rules: no hex colour in a CSS context (a `<style>` block, a `.css` file) or a quoted string (a `this.#add` field is not a colour); no `rgb(`/`rgba(`/`hsl(`/`hsla(` with a literal channel; no named colour as a CSS value (`transparent`, `currentColor`, `inherit` allowed); no `var(--x)` whose name no scanned file defines (`--x:` or `style:--x`; `var(${…})` skipped); no `border` or `border-*` shorthand with a style and no colour. Checked against known bad and good lines (as `TestNoHTMLSinks` is): a scanner matching nothing, or matching `this.#dead`, fails.
  - `tokens.css`, the same names (125 `--fs-*` uses and every colour use keep working), new values. Dark from the sketch: `--bg-window` #1a1b1e, `--bg-panel` #1f2024, `--bg-raised` #23252a, `--border` #2e3036, `--text` #e4e4e7, `--text-2` #a1a1aa, `--text-3` #8a8a94, `--accent` #7aa2f7, `--accent-text` #14161b, `--success` #9ece6a, `--warning` #e0af68, `--danger` #f7768e, `--syn-keyword` #c3a6f2, `--syn-type` #8fd3dc, `--syn-function` #e4e4e7, `--syn-string` #b8c99a, the other `--syn-*` in that family; lanes mid-tones per theme (3:1 on both raised surfaces means luminance about 0.16–0.25 in dark's set and its own darker set in light). Light starting values (hand-computed; the test is the arbiter): `--bg-window` #f4f4f5, `--bg-panel` #fafafa, `--bg-raised` #ececee, `--border` #d4d4d8, `--text` #18181b, `--text-2` #52525b, `--text-3` #64646c, `--accent` #2b59c3, `--accent-text` #ffffff, `--success` #36701b, `--warning` #8a5a00, `--danger` #c0304f, syntax darkened to pass. New: `--warning-soft`, `--danger-soft`, `--danger-text`, `--danger-border`, `--scrim`, `--avatar-s`, `--avatar-l`, `--avatar-text`, `--sel-edge: inset 2px 0 0 var(--accent)`. A sketch value that fails (dark `--text-3` on `--bg-raised` is about 4.49:1) moves in lightness only until the test passes; final and moved values go in the evidence for the owner. The header comment says why, not the values.
  - The literals: Logcat `.p-warn` → `--warning`, `.p-error, .p-fatal` → `--danger`, `.p-verbose` → `--text-3`, `.p-debug` → `--text-2`, `.marker` → `--bg-raised`, `--text-2`; LiveView `.secure` → `--warning-soft`, `--text`, its border colour `--warning` on all sides (step 10 makes it a left edge); the History avatar → `--avatar-text` on `hsl(var(--hue) var(--avatar-s) var(--avatar-l))`; the ref dots (`HistoryPanel:686`, `RefsPanel:539`) → `laneColor(ri)` through `style:`; `var(--bg)` → `--bg-window` (`CommitView`), `--bg-panel` (`RefsPanel`'s ring); `Console.svelte:179` → `var(--border)`; scrims → `var(--scrim)`; `Shell.svelte:462` → `var(--shadow)`.
- Implements: owner (palette, colour P1s); contract OWN-WORLD and its light reading; Q1 §3; D24.
- Check: `contrast.test.ts` seen failing on `main`'s tokens (at least dark `--text-3` on `--bg-panel` about 3.0, light `--text-3`, light `--accent` on `--bg-window` 3.69, light `--syn-comment`), then passing; failing again with dark `--text-3` back to #6e6e76 and with light `--accent` #007aff. `literals.test.ts` seen failing on `main` (the 9 hex, 5 scrims, 1 shadow, 3 hsl, 2 `var(--bg)`, 1 border), then passing; failing with `base.css` given a `color: #fff` (the CSS loader is real). `verify`; the gate. Main session: screenshots in both themes; a Logcat `W` row's computed colour equals `--warning`'s value in dark.
- Schema: no change.
- Needs: step 1.
- Not here: fonts, sizes, spacing (steps 3, 4); area restyles; control boundaries at 3:1 (WCAG 1.4.11): buttons carry text and inputs sit on a contrasting surface; a register row at step 11.
- Rests on: —
- [x] Done. Evidence: 2026-10-23, `5933661`.
  - `contrast.test.ts` failed on `main`'s tokens: 5 of 10 tests. Dark `--text-3` measured 3.02 on the panel and 2.75 on raised. Light `--accent` measured 3.69 on the window. Light `--text-3` measured 2.36–2.57, and lanes 1, 2, 4 and 6 were under 3:1.
  - `literals.test.ts` failed with the plan's counts: 9 hex, 5 scrims, 1 shadow, 3 `hsl`, 2 `var(--bg)` and 1 colourless border.
  - Both tests now pass, and both were seen failing again: on `base.css` given `color: #fff`, and on dark `--text-3` #6e6e76 with light `--accent` #007aff.
  - `verify` passed: 117 files, 1346 tests. `go test ./...` passed.
  - Level 5 (`$R=/private/tmp/dy.ulwJii`, `$E/step2`): screenshots in both themes and at both sizes. The probe's sizes and families are unchanged from step 1, as expected.
  - Values that moved:
    - dark `--text-3` #9a9aa4: the sketch's #8a8a94 fails on the tinted rows;
    - dark `--text-2` #b1b1b9 (Changes);
    - light `--danger` #b82a48: the plan's #c0304f fails on the tinted rows;
    - light `--avatar-l` 30%.
  - The Logcat `W` colour is checked in the CSS only: `.p-warn` is `var(--warning)`. No device ran, so there were no log lines to read in the page; step 9 checks it there.

### 3. The faces, served

- Builds:
  - `pnpm -C web add -E @fontsource-variable/geist@5.3.0 @fontsource-variable/geist-mono@5.3.0` (never `npx` or `pnpm dlx`). `web/src/lib/ui/fonts.css` (new), imported by `main.ts` before `tokens.css`: a `/*!` comment with both fonts' copyright lines and the OFL 1.1 text verbatim from the packages' `LICENSE` (Q1 §4), then `@import` of both packages' `wght.css` (normal only, every subset; the browser fetches what a page uses).
  - `adapters/httpapi/httpapi.go` `assetTypes` gains `".woff2": "font/woff2"`, nothing else; its comment names the fonts (Q1 §2). `ui_test.go` `TestUIIsServedBehindTheFence`: a `/assets/geist-latin-wght-normal-abc123.woff2` served with `font/woff2`, `immutable`, `nosniff`; `.woff`, `.ttf`, `.svg`, `.map` 404; a wrong `Host` on a font 403 before any read.
  - `tokens.css`: `--font-ui: 'Geist Variable', system-ui, sans-serif`; `--font-mono: 'Geist Mono Variable', ui-monospace, Menlo, monospace`. `base.css`: `font-variant-numeric: tabular-nums` on body.
- Implements: owner (two faces); Q1 §1, §2, §4; m2a D2, D6 as amended by Q1.
- Check: `TestUIIsServedBehindTheFence` seen failing (404 on the `.woff2`) before the entry; `go test ./adapters/httpapi ./web`; `verify` (pins pass). Level 5 (step 1's launch, rebuilt): `dist/app/assets/*.woff2` exist and the built CSS contains "SIL Open Font License"; the Network log shows each font `200 font/woff2` from `127.0.0.1:<port>`, no other origin; no CSP violation; `document.fonts.check('13px "Geist Variable"')` and `"Geist Mono Variable"` true; `"111111"` and `"000000"` in Geist with `tabular-nums` measure equal widths; the probe's families are the two Geists (system ones in step 1).
- Schema: no change.
- Needs: step 2; Q1's ADR (the main session writes it).
- Not here: sizes (step 4); italics; `font-display: swap` kept (a cold load may flash the fallback; the files are local).
- Rests on: the browser accepts the font as served under `nosniff` and the CSP: level 5, the Network log (the `MapFS` test shows the header only).
- Review: the main session's diff review (one map entry; the fence's tests unchanged and first); the close verifier covers it.
- [x] Done. Evidence: 2026-10-23.
  - `TestUIIsServedBehindTheFence` failed on the `.woff2` (404) before the `assetTypes` entry. The `.woff` and `.ttf` 404s and the wrong-`Host` 403 already held, and are now pinned. It passes now.
  - `verify` passed: pins, 1346 tests and the build. `go test ./...` passed.
  - The two packages are exact in `package.json`. The lockfile gained 16 lines and nothing else changed.
  - The build has 11 `.woff2` files, latin 29.4 kB and 23.1 kB. The built CSS keeps the `/*!` OFL text, so Q1 §4 holds as written.
  - Level 5 (`$R=/private/tmp/dy.YZRVBd`, `$E/step3`):
    - Network: `200 font/woff2` for the two latin subsets, from `127.0.0.1` only, and every request on the page's origin.
    - The console has no CSP violation; its one 404 is `/favicon.ico`, as at step 1.
    - `document.fonts.check` is true for both families.
    - Under `tabular-nums`, `111111` and `000000` both measure 46.81 px, against 27.36 px for proportional figures, so the subsets keep `tnum`.
    - The probe's families are `Geist Variable` and `Geist Mono Variable` (system fonts at step 1).
  - Italic text, a preview tab's name and code comments, is synthesised by the browser: no italic face ships (Not here).

### 4. The scale and the shared styles

- Builds: load the two Svelte skills.
  - `tokens.css`: `--fs-label` 11.5px, `--fs-small` 12px, `--fs-base` 13.5px, `--fs-title` 14.5px, `--fs-large` 16px, `--fs-display` 18px, `--fs-mono` 13.5px, `--fs-log` 12.5px, `--lh-base` 1.45, `--lh-code` 22px, `--lh-log` 19px, `--tracking-label` .09em, `--toolbar` 44px, `--head` 34px; per density (compact default, comfortable): `--row` 24/28px, `--tab-height` 36/40px (centre and bottom tab bars), `--ftab-height` 34/38px (file tabs; `Editor.svelte:157` moves to it).
  - `base.css`: body `--font-ui` at `--fs-base`/`--lh-base`; `h2` and a shared `.panel-head` as the raised bar (`--bg-raised`, `--head` tall, `--fs-label`, 600, uppercase, `--tracking-label`, `--text-2`, a `--border` seam below); `.mono`; `.selected` (`--accent-soft` with `var(--sel-edge)`); controls at the sketch's 28 px, radius 5 px, weight 500; `.btn-danger` (transparent, `--danger-border`, `--danger`).
  - The literal sizes and line heights of Starting point → tokens (`.soon` and the `SessionsToggle` badge → `--fs-label`, the badge 16 px; the History avatar 20 px with `--fs-label` initials; `App` h1 → `--fs-display`; Palette's input → `--fs-large`; Problems, AppLogs, LiveView → `--fs-small`/`--fs-base`; `letter-spacing: 0.04em` → `--tracking-label`; `theme.ts` → `var(--fs-mono)`, `var(--lh-code)`). Logcat and Console: rows in `--font-mono` at `--fs-log`; `LOG_ROW_PX = 19` in `web/src/lib/ui/metrics.ts` (new) replaces both `ROW_HEIGHT`s and reaches the CSS as `style:--log-row="{LOG_ROW_PX}px"` on the list, `height` and `line-height: var(--log-row)` (one source).
  - `literals.test.ts` extended: no `font-size`, `font` shorthand size, `line-height`, or camelCase `fontSize`/`lineHeight` in TypeScript with a literal value, and no `font-family` but `var(--font-ui)`, `var(--font-mono)`, `inherit`. `scale.test.ts` (new, reading `tokens.css` as step 2 does): every `--fs-*` ≥ 11.5px.
- Implements: owner (the scale, spacing); contract (11.5 px floor, raised headers, tabular figures); D24 (density kept).
- Check: `literals.test.ts` seen failing on step 3's tree (the size literals listed); `scale.test.ts` failing with `--fs-small` 11px; `verify`. Level 5: the probe's smallest size ≥ 11.5 px in both themes and sizes (9 px at step 1); the centre tab bar is 36 px; the selected tree row carries the accent edge and a background other than `--accent`; a Logcat-sized list scrolled to its end shows its last row whole; the main session records each named assertion of steps 5–10 individually as failing on this tree; switching density changes `--row` and the tree's row height after a reload (the tree measures `--row` only when its model changes: pre-existing, a register row at step 11).
- Schema: no change.
- Needs: step 3.
- Not here: area restyles (steps 5–10).
- Rests on: —
- [x] Done. Evidence: 2026-10-23.
  - Seen failing on step 3's tree, then passing:
    - `scale.test.ts` failed 4 of 4, the floor test with `--fs-small is 11px`.
    - `literals.test.ts` named 17 type literals, among them 9 px in `Shell` and `SessionsToggle`, 8 px in `HistoryPanel`, `cm/theme.ts` `lineHeight: 1.5`, and `base.css` `/ 1.4`.
  - `verify` passed: 118 files, 1369 tests. `go test ./...` passed.
  - Level 5 (`$R=/private/tmp/dy.vkzAkR`, `$E/step4`):
    - The probe's smallest text is 11.5 px in both themes and at both sizes; it was 9 px and 8 px at step 1.
    - The centre tab bar is 36 px plus its 1 px seam.
    - The selected tree row has `inset 2px` accent on `--accent-soft`, not `--accent`.
    - Comfortable density gives 28 px tree rows and a 40 px tab bar after a reload; compact gives 24 px and 36 px.
    - Two area assertions were already true on this tree: the 44 px toolbar (step 5) and the sha in Geist Mono (step 8, from step 3). Their steps keep only the others.
  - The last-log-row check waits for log lines (step 9).

### 5. Shell, toolbar and start

- Builds: load the two Svelte skills. `ui/Shell.svelte` (toolbar `--toolbar` on `--bg-raised`; region heads as `.panel-head`; tab bars `--tab-height` on raised bars, the current tab with an inset 2 px `--accent` underline, disabled tabs `--text-3`; the right head "Device" a header bar; the seams of the sketch's one-pixel gaps), `ui/Status.svelte` (24 px, mono `--fs-small`), `ui/Splitter.svelte`; `toolbar/*` (project name `--fs-title` with a state dot and word, Run primary as the sketch's, figures in mono, 28 px controls); `projects/SessionsToggle.svelte`; `start/*`; `build/Workspace.svelte`; `App.svelte`. The column head (`Shell.svelte:225-237`) loses its "Sessions" `h2`; the pin and density switch stay; `aria-label`s unchanged (ARIA deferred).
- Implements: contract FIRST VIEWPORT; row 515 (title clause).
- Check: the probe's step-5 assertions, failing at step 1 and on step 4's tree: the toolbar's height is 44 px; the current centre tab's `box-shadow` holds a 2 px accent underline; the status bar's text computes to Geist Mono; the column head holds no heading element; at 720 px Run and Build stay reachable. Worktree: `verify`, the gate.
- Schema: no change.
- Needs: step 4. Runs beside steps 6–10.
- Not here: the column's lists (step 6).
- Rests on: —
- [ ] Done. Evidence:

### 6. Left column: projects, sessions, file tree, palette

- Builds: load the two Svelte skills. `projects/Projects.svelte`: Projects and Sessions headers as `.panel-head`; rows on the sketch's session grid (dot or icon, title, state at the right; a second line in mono `--fs-small` `--text-2` for worktree, holds, age); `.selected`. Row 515: a chat title clamps at two lines (`-webkit-line-clamp: 2`), its `title` attribute the whole text (a text node; untrusted); Release and Copy in the row's last column, `flex: none`, never below it. `projects/Spinner.svelte`; `files/FileTree.svelte` (rows from `--row`, folders `--text-2`, chevrons `--text-3` mono, `.selected`, the filter on `--bg-window`; the `.5` opacity at `:759` → a token colour); `files/CollisionDialog.svelte`; `palette/Palette.svelte` (on `--bg-raised`, matches `--accent`, paths mono).
- Implements: row 515 (truncation and wrap); contract (selection is an edge).
- Check: step-6 assertions: at the 220 px column with a 90-character title, the title element spans two line boxes and its `title` equals the text; a connected row's Release `top` lies within its row's first line (it wraps below at step 1); no opacity-dimmed text in the column (FileTree's `.5` today). Synthetic chats and a connected session as `m3c` step 5 (a) builds them. Worktree: `chats.test.ts`, `sessions.test.ts`, `tree.test.ts`, `verify`, the gate.
- Schema: no change.
- Needs: step 4. Runs beside steps 5, 7–10.
- Not here: Copy on a connected row (Scope); tree behaviour.
- Rests on: —
- [ ] Done. Evidence:

### 7. Editor and diff

- Builds: load the two Svelte skills. `editor/Editor.svelte` (file tabs `--ftab-height`, mono names, the current one on `--bg-window` with a 2 px top `--accent` edge), `Viewer.svelte`, `UnsavedDialog.svelte`; `cm/theme.ts` (code on `--bg-window`, gutter `--text-3` right-aligned, the active line `--bg-raised` and its number `--text`), `cm/merge.ts`, `cm/review.ts` (the `.55` opacity at `:253` → `--text-3`), `cm/diagnostics.ts`; `git/DiffPanel.svelte`, `git/CompareView.svelte`.
- Implements: contract (code 13.5/22 mono); m2i D2, D12.
- Check: step-7 assertions, the sketch's Kotlin file open: the current file tab's `box-shadow` holds a 2 px top accent edge on `--bg-window`; `.cm-activeLine` computes to `--bg-raised`; `.cm-gutters` background is `--bg-window`; no opacity-dimmed text in Review. Worktree: `cm/*.test.ts`, `verify`, the gate.
- Schema: no change.
- Needs: step 4. Runs beside steps 5, 6, 8–10.
- Not here: syntax roles, languages, behaviour.
- Rests on: —
- [ ] Done. Evidence:

### 8. Git

- Builds: load the two Svelte skills. `git/GitToolbar.svelte`, `GitTree.svelte`, `HistoryPanel.svelte` (hashes and dates mono, lanes `--lane-*`, relative times `--text-2`; the `.55` opacity at `:870` → a token colour), `RefsPanel.svelte`, `CommitView.svelte`, `SubmodulesSection.svelte`, `SyncSection.svelte`, `BranchMenu.svelte`, `CheckoutDialog.svelte`, `CreateBranchDialog.svelte`: section headers as `.panel-head`, `.selected`, destructive actions (delete branch, discard, force) as `.btn-danger` set apart.
- Implements: contract (colour is state; destructive actions apart).
- Check: step-8 assertions, on a synthetic repository with three branches, a merge and a submodule as m2n's level 5 builds one: the History tab's `.head` computes to `--bg-raised` (the side panel's `.sec-head` already does); every destructive button's border is `--danger-border`; hashes in Geist Mono; no opacity-dimmed text. Worktree: `git/*.test.ts`, `verify`, the gate.
- Schema: no change.
- Needs: step 4. Runs beside steps 5–7, 9, 10.
- Not here: graph layout; the disabled filter buttons (row 516, closed: as they are).
- Rests on: —
- [ ] Done. Evidence:

### 9. Bottom panels

- Builds: load the two Svelte skills. `logcat/Logcat.svelte` (time `--text-2`, pid `--text-3`, the level letter in a 16 px column, weight 500; an error or fatal row on `--danger-soft`; the query bar as the sketch's filter row, mono); `devices/AppLogs.svelte`; `build/Console.svelte`, `Problems.svelte`, `Build.svelte`, `BuildResult.svelte`; `ops/Activity.svelte` (actor and duration mono; its 3 px left status edge kept: it is state).
- Implements: audit P1 (Logcat in dark); contract (logs 12.5/19).
- Check: step-9 assertions, Logcat streaming: an `E` row's background is `--danger-soft` over `--bg-window`; the level column is 16 px wide. Lines come from the lane's own scratch AVD on its own adb port (memory: `lane-own-avd-and-adb-port`); without one, the error-row and colour assertions run on the build console and the gap stays in Unverified. Worktree: `virtual.test.ts`, `console.test.ts`, `activity.test.ts`, `verify`, the gate.
- Schema: no change.
- Needs: step 4. Runs beside steps 5–8, 10.
- Not here: Logcat's query or format options (row 497); Problems' scrolling header (row 480).
- Rests on: —
- [ ] Done. Evidence:

### 10. Device panel and live view

- Builds: load the two Svelte skills. `devices/Devices.svelte` (sections with the sketch's `.lbl` headers, a row per device with its dot and state word, serials and packages mono, Start and Stop small, Stop `.btn-danger` set apart), `Act.svelte` (its 3 px status edge kept), `Tree.svelte`, `UiEvents.svelte`, `UiRecording.svelte`; `screen/LiveView.svelte` (the secure notice a quiet strip with a `--warning` edge, the meta mono, the Fast toggle at `--fs-small` with no opacity: `:312`'s .75 goes).
- Implements: audit P1 (the banner); contract (destructive actions apart).
- Check: step-10 assertions, an emulator listed: Stop's border is `--danger-border` and at least 8 px from Start; the secure notice (forced `view.secure`) has a `--warning` left edge and no border on its other sides; the Fast toggle's effective opacity is 1. Worktree: `liveview.test.ts`, `devices.test.ts`, `uievents.test.ts`, `verify`, the gate.
- Schema: no change.
- Needs: step 4. Runs beside steps 5–9.
- Not here: live view behaviour.
- Rests on: —
- [ ] Done. Evidence:

### 11. The merged tree, the finish and the register (main session)

- Builds: no product code unless a finding needs it (a fix with its check, under Changes). On the merged tree, step 1's launch rebuilt: (a) the probe over the whole page and every area's assertions, both themes, both sizes; (b) screenshots as step 1's, beside the sketch's render, for the owner; (c) the functional pass: open the project, edit and save, Review and History, a build's output and problems, Logcat streaming, the scratch emulator started and stopped, the live view, density; (d) the detector against step 1's count, the 3 px status edges kept as true indicators; (e) the impeccable finish reviewer, then its documenter writing the local `DESIGN.md` (never committed); (f) the register per `milestone-workflow` §Close: row 515's visual clauses closed, rows for the audit's keyboard and ARIA findings, control boundaries at 3:1, the tree's stale `--row`, and what Unverified leaves open; (g) `go test ./...`, `verify`, `verify-daemon` §Cleanup with 0 orphans; the Opus verifier over the Acceptance items.
- Implements: A1, A4, A5, A7; `prove-change` (level 5); `milestone-workflow` §Close.
- Check: (a) smallest size ≥ 11.5 px, no opacity-dimmed text, families the two Geists, every area assertion true; (c) every item works; (d) no new finding but the kept ones; the verifier without blocking findings.
- Schema: no change.
- Needs: steps 1–10.
- Not here: the aesthetic verdict, which is the owner's.
- Rests on: —
- [ ] Done. Evidence:

## Not in this milestone

- Keyboard reach, focus order, ARIA: deferred by the owner; register rows at step 11.
- Copy on a connected chat's row; Sessions inside the left panel; rows 504 and 510: see Scope.
- Non-text contrast of control boundaries (WCAG 1.4.11); the tree's `--row` re-measure on a density switch: register rows at step 11.

## Design questions

The main session may apply the recommendation under the owner's standing instruction (Delegated) and write the ADR before step 3; steps 1 and 2 do not depend on it.

### Q1. The UI's own faces, served by the daemon, under the Graphite rules

- Blocks: step 3.
- The documents say: m2d Style C and D24 (an owner decision recorded in a plan) chose the system font and a fixed system blue; m2a D6 serves `/assets/` as `.js` and `.css` only and never widens the CSP; nothing on fonts, their licence or a contrast rule. The choice of package is the plan's (ADR-0039 §Consequences; Starting point).
- Draft decision, as a new ADR, "The UI ships its own faces and holds every text pair to WCAG AA": (1) the owner's Graphite Console direction (2026-10-23) replaces Style C: colour only for state, Geist for the UI and Geist Mono for code, logs and figures, no text under 11.5 px, selection as an edge, destructive actions apart; the faces come from pinned packages through the app's CSS, emitted as hashed files; (2) `/assets/` also serves `.woff2` as `font/woff2`, same name rules and headers, no other font format, the CSP unchanged; (3) every text token 4.5:1 and every mark 3:1 in both themes, held by the tokens' test; (4) the OFL text travels in the built CSS as a preserved `/*!` comment. Rejected: **a CDN** (the CSP; offline use); **inlined `data:` fonts** (the CSP, m2a D1); **system fonts with a better scale** (the owner's complaint is one face for everything); **the OFL as a file in the release archive** (changes the archive and the installer for a notice the bundle carries).

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| A raw glob of a `.css` file is empty under Vitest (read in vitest 5.0.3's source, not run) | 2, 4 | step 2's `base.css` mutation; the tests read CSS with `readFileSync` either way |
| The fontsource subsets keep Geist's `tnum` | 3 | step 3's width check |
| Vite's CSS minifier keeps the `/*!` comment | 3 | step 3's grep of the built CSS; if stripped, Q1 §4 is redrafted under Changes before step 4 |
| The light values suit the owner as well as AA; the hand-computed ratios | 2 | the test decides AA; the owner's look at step 11 |
| Logcat's level colours on a real stream | 9 | a device for the lane; else the build console only |
| Chrome only: Safari and Firefox with variable fonts and `line-clamp` | 3, 6 | not run (row 477's browsers) |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | B1 [test] a raw `import.meta.glob` of `.css` is `""` under Vitest 5.0.3, so the contrast, metrics and literal tests would read nothing | CSS read with `readFileSync` (golden.ts precedent), non-empty asserted, the list checked against a non-eager glob; a `base.css` mutation in step 2's Check; Unverified row |
| 1 | | B2 [plan] the probe and baselines were built in the last step but used from step 1 | New step 1 (main session): verify, the probe, its per-area assertions failing, the detector's count, on `main`'s build |
| 1 | | B3 [test] area checks already true after the token steps | Each of steps 5–10 names assertions only it makes true; the size and opacity probe is the regression guard |
| 1 | | N1 lanes shared by both themes cannot reach 3:1 on both | Light overrides the lanes; the exception gone |
| 1 | | N2 pair gaps (syntax on raised and Review's mixes, soft surfaces over the window, opacity-dimmed text) | Pairs added; the four opacities replaced in steps 6–10; the probe flags dimmed text; A1 says so |
| 1 | | N3 `height: 18px` on log rows unnamed | `LOG_ROW_PX` reaches the CSS as `--log-row`; `metrics.test.ts` and `--lh-log`'s parse dropped |
| 1 | | N4 parallel worktrees lack the sketch and could touch shared files | Absolute sketch path; the Parallel line's rules; the probe once on the merged tree |
| 1 | | N5 step 2 too big | Split into step 3 (faces, served) and step 4 (scale) |
| 1 | | N6 A4's floor had no static test | `scale.test.ts`: every `--fs-*` ≥ 11.5px |
| 1 | | N7 Q1: ADR for §2 and the Style C record; the package is the plan's | Q1 narrowed, the direction's rules in §1; the package rationale in Starting point |
| 1 | | Nits: tab heights; `.p-debug`; the ADR-0095 paraphrase; missing literals; the `name`-table row; size | One token per bar (`--tab-height`, `--ftab-height`); D → `--text-2`; reworded; `theme.ts:23`, `base.css:13`, camelCase and `border-*` covered; row removed; trimmed |
| 2 | approve | B1, B2, B3 resolved. Non-blocking: five sub-assertions of steps 5, 6, 8, 9, 10 were already true on step 4's tree (36 px bar, status 24 px, the tree's edge, the side panel's headers, the `W` colour) | Re-homed: the bar and the tree's edge to step 4's level 5, the `W` colour to step 2's, the side panel dropped (History's `.head` kept), the status bar asserted by its font; step 2 recolours `.secure`'s border only; step 4 records each area assertion failing |

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.

- 2026-10-23, step 2: dark `--text-2` moved from the sketch's #a1a1aa to #b1b1b9. The contrast floor pushed `--text-3` to #9a9aa4, which left the two text levels about 0.02 apart in luminance, so `--text-2` was lifted to keep the hierarchy. The change is lightness only. Delegated.
- 2026-10-23, step 4: `h2` keeps the label type only (11.5 px, uppercase, tracked, `--text-2`), and only `.panel-head` is the raised bar. Applied to every `h2`, the bar boxed the headings inside padded sections (Projects, Devices, UI recording) as separate tiles. A region's head adopts `.panel-head` in its area step. Delegated.
