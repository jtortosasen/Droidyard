# ADR-0128: The UI ships its own faces and holds every text pair to WCAG AA

- Status: Accepted
- Superseded in part by: ADR-0134 (§Decision 1)
- Date: 2026-10-23
- Relates to: ADR-0006, ADR-0039 §Consequences, ADR-0040 §1; [plan m2a](../plans/m2a-minimal-web-ui.md) D1, D6; [plan m2d](../plans/m2d-redesign-file-tree.md) Style C, D24; [plan m3d](../plans/m3d-graphite-facelift.md) Q1
- Decided by the owner, 2026-10-23: the facelift's direction, Graphite Console, chosen from nine sketched directions, with distinct faces for the interface and for code, and contrast fixed in both themes. The shape below is the main session's under the owner's standing instruction to apply the recommended option, Delegated.

## Context

The owner's complaint about the UI: text is too small (13 px body, 11 px labels, 9 px in
places), every text is one face, and the colours are stock greys and system blue. That look is
m2d's Style C (native macOS chrome, the system font, the user's accent), an owner decision
recorded in a plan. An audit on 2026-10-23 measured `--text-3` at 3.0:1 in dark and 2.4–2.6:1 in
light, the light accent at 3.69:1, and Logcat's hard-coded light colours unreadable in dark.

Distinct faces mean font files. The UI's CSP is `default-src 'self'` and is never widened (m2a
D6, `http-websocket.md` §Security baseline rule 7). `data:` URLs are refused (m2a D1). The
daemon serves `/assets/` as `.js` and `.css` only (m2a D6; `assetTypes` in
`adapters/httpapi/httpapi.go`). Geist and Geist Mono are under the SIL Open Font License 1.1,
which requires the licence text to travel with the fonts.

## Decision

1. **Graphite Console replaces Style C.**
   - Colour means state, nothing else.
   - Geist sets the interface; Geist Mono sets code, logs, paths and every figure, with tabular numerals.
   - No text is set under 11.5 px.
   - Selection is shown as an edge, not a filled block.
   - Destructive actions are styled apart.
   - Dark and light both follow the system.
   - The faces come from pinned packages (ADR-0040 §1; the package itself is the plan's, per ADR-0039 §Consequences). They are imported by the app's CSS and emitted by the build as hashed files.
2. **`/assets/` also serves `.woff2` as `font/woff2`.** It keeps the same name rules and headers (`immutable`, `nosniff`). No other font format is served, and the CSP is unchanged.
3. **A contrast floor in both themes.** Every text token reaches 4.5:1 on each surface it is used on, and every mark (state dots, focus ring, selection edge, graph lanes) reaches 3:1. A Vitest computes these ratios from `tokens.css`, and no colour lives outside it.
4. **The OFL text travels in the built CSS,** as a preserved `/*!` comment with each font's copyright line.

## Consequences

- The bundle grows by the font subsets a page uses: Latin normal is about 29 KB and 23 KB, each fetched once and cached as immutable.
- On a cold load `font-display: swap` may show the fallback face briefly; the files are local.
- `assetTypes` gains one entry, and `TestUIIsServedBehindTheFence` asserts it along with the 404s for other font formats.
- `tokens.css` keeps m2d D24's single file and density switch. Its values and the shared styles in `base.css` change, and two tests hold the rules: the contrast floor, and no literal outside the tokens.
- If Vite strips the `/*!` comment, §4 is redrafted before the faces ship (plan m3d step 3 checks the built CSS).
- No schema change.

## Alternatives considered

- **Load the faces from a CDN.** The CSP refuses it, and the UI must work offline. Widening the CSP for a typeface is what m2a D6 rules out.
- **Inline the fonts as `data:` URLs.** The CSP and m2a D1 refuse them, and inlining would put about 200 KB into every CSS load, without caching per face.
- **Keep the system fonts and only enlarge the scale.** That answers "too small" but not "every text is the same face". `ui-monospace` beside `system-ui` differs by platform, and on macOS SF Mono beside SF Pro reads as one family.
- **Ship the OFL as a separate file in the release archive.** It changes the archive and the installer for a notice the bundle can carry itself, and a binary built from source would ship the fonts without the file.
- **A contrast check by eye or by an audit at each review.** Today's 2.4:1 tokens passed every review so far. A test over the token file fails the commit that breaks the floor.
