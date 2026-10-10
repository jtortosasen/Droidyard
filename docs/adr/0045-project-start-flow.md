# ADR-0045: The project start: `droidyard` alone, one tab per project, recents and a folder dialog

- Status: Accepted
- Superseded in part by: ADR-0047 (§1, §2)
- Date: 2026-10-10
- Supersedes: ADR-0019 §Processes (in part: item 3's `ui` opens a project and the picker)
- Supersedes: ADR-0015 §Decision (in part: item 6's human audience gains a second use)
- Relates to: ADR-0006, ADR-0010, ADR-0015 §5, ADR-0021 §Layout, ADR-0026, ADR-0032 §5,
  ADR-0036, ADR-0043 §1, §3; plan m2c Q2
- Decided by the owner, 2026-10-10: a traditional start, either a project picker with the
  recent projects and "open a folder", or `droidyard` typed in a project folder; no paths
  typed, no native app for now. The shape below is Delegated (plan m2c Q2, reviewed in two
  rounds; the folder dialog observed at plan m2c step 2 D).

## Context

One daemon per user hosts every workspace (ADR-0019), and `workspaces.json` already lists
the recent ones (ADR-0021 §Layout), but nothing exposes them: 2b's UI types a root and
keeps it in `localStorage`, and a workspace opened before the tab is not shown. A page
cannot learn a native path. Trust is a human act, never granted on open (ADR-0015 §5).

## Decision

1. **Entry points.** `droidyard` with no subcommand runs `ui`. `ui` looks for a project
   from its working directory by the MCP binding's rule (the nearest ancestor holding
   `settings.gradle(.kts)`); found, it opens it (`workspace.open`, client `cli`: no
   project code, no trust) and the browser at `/#token=<t>&w=<wsid>`; not found, at
   `/#token=<t>`, the picker. Auto-start as ADR-0019 §4; no new subcommand.
2. **One daemon, one tab per project.** A tab's project is the fragment's `w`, an id never
   sent to a server; the token moves to `sessionStorage` and `w` stays, so a reload stays
   on the project. The picker is the page without `w`; several projects are several tabs.
3. **`workspace.watch`**: command, global, streaming, audience all. Result
   `{subscriptionId, workspaces: [{id, name, root, lastOpenedAt, state, trusted}]}`: the
   index merged with live states, newest first, at most 50, taken after the subscription
   opened. Events on a new ring-less topic `workspace.state`, type `state`, payload
   `{state, degradedBy?, name, root, trusted}`, published beside every
   `workspace.lifecycle` publish, in order under the workspace's transition lock, and
   offered to every `workspace.watch` subscription (ADR-0043 §3). `workspace.lifecycle` is
   unchanged. `lastOpenedAt` is refreshed on every open, an open workspace's included.
   Streaming keeps it off MCP (ADR-0010, ADR-0036).
4. **`workspace.chooseRoot`**: the macOS folder dialog (`osascript` `choose folder`) run by
   the daemon; on any other platform, or without `osascript`, unavailable with the reason
   (a requirement `folderDialog`), and the UI falls back to a typed field. Query, global,
   long-running, at most 5 minutes, one at a time; `{root}` (the binding's rule applied to
   the choice) or `{dismissed: true}`; error `WORKSPACE_DIALOG_FAILED`. **Audience human**:
   it puts a window on the user's screen and its answer is a human's choice, so an agent may
   not raise it. ADR-0015 §6's "one deliberate exception" gains this second use; policy
   remains the only thing it guards against self-authorisation.
5. **Trust from the UI**: an untrusted project says what trusting means and offers Trust, a
   `policy.update` of `trust.roots` with `ifRevision`; the UI then reads
   `workspace.describe` again. Opening, the CLI entry and the project file never trust.
6. No HTTP route or rule changes; every action is a capability.

## Consequences

- The register rows "a workspace opened before the tab loaded is not shown" and the
  `localStorage` root of m2b D10 close.
- A remote browser (ADR-0006's tablet, once LAN access exists) would raise the dialog on the
  host; there the directory browser below wins.
- Linux has no folder dialog yet; the typed field remains.

## Alternatives considered

- **A directory-browser capability** (any directory's subdirectories, human-only):
  cross-platform and testable, but a read surface over the whole disk and a file-manager
  UI. It wins on Linux or for a remote browser.
- **Linux `zenity`/`kdialog`**: no host here runs them; a later minor change.
- **A typed path with completion**: the owner's "no paths".
- **The root in the URL**: a path in history and in every copied link; the id resolves
  through the index (ADR-0026).
- **One daemon per project**: ADR-0019's rejection stands.
- **A `workspace.list` query**: an MCP tool or a stretched audience (ADR-0043).
- **Trust on open or from the CLI entry**: rejected by ADR-0015 §5.
- **A native app**: the owner, not now.
