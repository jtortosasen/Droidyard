# ADR-0047: One tab for every project: the projects panel replaces the picker

- Status: Accepted
- Date: 2026-10-10
- Supersedes: ADR-0045 §1 (in part: the page without `w` is no longer the picker)
- Supersedes: ADR-0045 §2 (in part: several projects are not several tabs)
- Superseded in part by: ADR-0050 (§1, §2)
- Relates to: ADR-0043, ADR-0045 §3–§6, ADR-0046, ADR-0048; plan m2d Q1
- Decided by the owner, 2026-10-10: one instance and no picker on open; a left panel lists
  every project and its agent sessions, and selecting one switches the whole UI. The shape
  below is Delegated (plan m2d Q1, reviewed in two rounds).

## Context

ADR-0045 gave each project its own tab and made the page without `w` a picker of recents.
The owner wants one window in which every project is a click away, the way Orca lists
its worktrees and their agents, and no selection screen in front of the work.

## Decision

1. **A projects panel** behind the left rail lists `workspace.watch`'s rows, newest first,
   each with its agent sessions (ADR-0048). Selecting a project opens it if it is closed
   (`workspace.open`) and selects it. The fragment's `w` still names the selection, so a
   reload or a copied link stays on it; the project view is mounted again per selection.
2. **No picker view.** Without `w`, the newest open workspace is selected. With none open,
   or with a `w` the daemon refuses, the panel opens pinned with the recents, "Open
   folder…" and the reason.
3. Everything else in ADR-0045 stands: `droidyard` in a project opens a tab on it (the
   daemon cannot raise an open page), `workspace.watch`, `workspace.chooseRoot`, trust from
   the UI. No capability or HTTP rule changes.

## Consequences

- Two tabs remain possible (each `droidyard` opens one); each switches on its own.
- The picker component and its texts in the CLI and `http-websocket.md` go.

## Alternatives considered

- **Keep the picker view**: the owner's "no picker on open".
- **One tab per project, enforced**: the owner switches in place.
- **The selection in `sessionStorage`**: the address is the state (reload, a copied link).
- **A capability to focus an open tab**: no page can raise itself.
