# ADR-0084: One tab: `droidyard` switches an open tab instead of opening another

- Status: Accepted
- Date: 2026-10-18
- Supersedes: ADR-0047 §3 (in part: `droidyard` in a project switches an open tab instead of opening one); ADR-0045 §3 (in part: `workspace.state` gains the event type `show`)
- Relates to: ADR-0045 §1, ADR-0083; `docs/interfaces/http-websocket.md` §Security baseline; plan m2q Q4
- Decided by the owner, 2026-10-17: "Una única pestaña o app desktop (cuando esté hecha). No más." The shape is Delegated.

## Context

ADR-0047 §3 kept "`droidyard` in a project opens a tab on it", because no page can raise itself. The owner wants a single tab. An open tab already holds a `workspace.watch` subscription, so the daemon knows whether a tab is there and can tell it what to show, even though it cannot bring the window to the front.

## Decision

1. **`workspace.show {workspace?}`** (command, global, stateless, audience human) offers every `workspace.watch` subscription a `show` event on `workspace.state` for the workspace, with an empty payload. The tab selects that workspace and opens it if it is closed. The result is `{tabs}`, the number of subscriptions offered.
2. **`droidyard` / `droidyard ui`** opens the project, then calls `workspace.show`. It opens a browser only when no tab answered (`tabs` 0) or the call failed.
3. **The window is not raised.** No page can raise itself (ADR-0047). Activating the browser application from the CLI would need to know which browser is the user's default, so it is left out.
4. **One tab per daemon run.** A tab from before a restart holds a token the new daemon does not know, so it cannot answer, and a second tab opens. This is a limit with a register row. The fix, a token kept across launches, is a security decision of its own.

## Consequences

- `workspace.show` and the `show` event type enter the contract; `cmd/droidyard`'s `ui` changes.
- After `droidyard`, the user finds the existing tab switched, not brought forward. What the owner then sees in practice is unverified, and goes to the owner's trial.
- The desktop app, when it exists, takes the tab's place under the same rule.

## Alternatives considered

- **Raising the tab.** A page cannot raise itself.
- **`osascript` activating a named browser.** Which browser is the user's default is not known to us, and activating the wrong one is worse than none.
- **A lock file for the tab.** New state for what the live subscriptions already say, and it goes stale on a crash.
