# ADR-0134: The column shows the tab's project as its worktrees, and the sessions by folder with their client

- Status: Accepted
- Date: 2026-10-27
- Supersedes: ADR-0127 §Decision (in part: §4, the panel); ADR-0083 §Decision (in part: §2, the panel by project); ADR-0047 §Decision (in part: §1, a panel listing every project); ADR-0128 §Decision (in part: §1, "colour means state, nothing else", for client tags only)
- Relates to: ADR-0015 §1, ADR-0050 §3, ADR-0052 §3, ADR-0083 §3, ADR-0086, ADR-0088 §Alternatives considered, ADR-0089 §1, §3, §Alternatives considered, ADR-0094 §3, ADR-0095 §1, ADR-0104 §1, ADR-0128 §3; [plan m3h](../plans/m3h-worktrees-and-sessions-panel.md) Q1–Q4
- Decided by the owner, 2026-10-27: no Projects section; the current project's worktrees at the top; the sessions by project folder with the client as a visible tag and no client header; a clicked live session stays marked and its move selects its worktree; a chat with nothing to do with Droidyard shown apart; colours and icons that carry information (Orca as the reference); the tab titled `Droidyard - <project>`; the status bar naming the worktree; reviewed by `impeccable`. The shape below is the main session's under the owner's standing instruction to apply the recommended option, Delegated.

## Context

The owner found the column confusing: a Projects section that repeated what Sessions showed,
a worktree row drawn with a branch icon, a client header ("Cursor") above a folder, and two
more "Cursor" rows that were live MCP connections no chat claims (Cursor's connection carries
no chat id, ADR-0095 §1), so ADR-0127 §4 drew each as a row of its own. Clicking a chat
selected a workspace without saying so, and nothing marked which session the tab followed.

The daemon knows which chats used it only through the live join (ADR-0095 §1): chat ids live
in memory, are lost at a restart, and persisting them was rejected (ADR-0088 §Alternatives
considered). `workspace.describe` is the one read that lists every linked worktree, opened or
not (ADR-0089 §1, §3).

## Decision

1. **The top of the column is the tab's project, as its trees.** Its main tree first, tagged
   `primary`, then the linked worktrees `workspace.describe` lists, merged by root with the
   project's `workspace.watch` rows. Each tree shows its name, its branch (linked ones), its
   open state, the live sessions in it nested with their holds and Release (ADR-0094 §3). A
   tree's click selects it, opening it first when closed. `describe` is read for the tab's
   project only, on the chats' cadence (one read after a burst of `session.state`, every
   30 s, on reconnect); no stream gains a field.
2. **The project switch is a menu on the project's name**, inside the column: the recents
   with their state, Remove on a closed one (ADR-0086), Open folder… or the typed root, and
   the panel's notes. It is open by itself when no project is selected. Nothing ADR-0047 §2
   gives is lost.
3. **Sessions are grouped by project folder**, the tab's project first, then by newest chat.
   Each chat carries a client tag from its store (Claude Code, Cursor, cursor-agent); there
   is no client header. A live session no chat claims is never a chat row: in the tab's
   project it is nested on its tree as a connected session with its numbered label; in
   another project it is a tag on that folder's head; with no workspace it is a tag on a
   "No project" folder.
4. **"Droidyard" means connected now.** A chat joined to a live session has full contrast,
   its state, holds, Release and selection. A chat with no joined session, in a project with
   no unclaimed session of its client, is on a quiet surface with a "not connected" tag. A
   chat whose project has an unclaimed session of its client (Cursor's case) carries neither
   mark: the panel never guesses which chat a connection is.
5. **The selection follows and is shown.** The session the selection follows (ADR-0083 §3)
   carries the selection edge wherever it is shown; its move into another worktree selects
   that tree, listed even before `describe` reads it again. Any navigation to another
   workspace (a tree row, the menu, the address bar, `workspace.show`) ends the follow.
6. **A client's tag carries its own hue**, always beside the client's name, so colour is
   never the only carrier; every hue meets ADR-0128 §3's floor in both themes. Elsewhere
   colour still means state.
7. **The tab is titled `Droidyard - <project>`**, the main tree's name (else the base name of
   its root) on the main tree and on every worktree, `Droidyard` with none. The status bar
   names the tree the tab is on, beside the connection state.

## Consequences

- No contract change: `web/` only. Every chat title, tree and branch name stays a text node
  (ADR-0015 §1).
- A Cursor chat is never marked connected nor "not connected" while a Cursor connection is
  live in its project; the join itself stays a register row.
- A live chat appears twice: nested under its tree and as its chat row.
- The main tree shows no branch: `describe` lists linked worktrees only. A field on it is a
  minor contract change, left to a register row.
- `describe` is now polled for the tab's project; its cost on a large repository is not
  measured (plan m3h §Unverified).

## Alternatives considered

- **Every project's trees in the column**: the redundancy the owner named.
- **Only the opened worktrees** (as before): a worktree an agent made and has not entered is
  invisible.
- **`git.refs`'s worktrees**: they come from running git, include entries git would prune
  (ADR-0089 §Consequences) and live in the project view, not the column.
- **`worktrees` on `workspace.watch`**: ADR-0089 rejected it for reads on every open and
  refresh; polling `describe` for one project keeps every other open unread.
- **A popover for the project menu**: the overlay hides on a pointer outside it (ADR-0052 §3).
- **Past use from the in-memory chat map**: daemon-life and Claude Code only, a contract
  change for a mark that resets at a restart; **a persisted record of chat ids**: rejected by
  ADR-0088.
- **Dimming every Cursor chat**: it would mark the live one "not connected"; **matching a
  Cursor connection to its newest chat**: a guess.
- **A row for an unclaimed connection** (ADR-0127 §4): the rows the owner could not read.
- **Neutral client tags** (ADR-0128 §1 as written): the owner asked for colours that carry
  information, and the name already carries the meaning beside the hue.
- **Marking the session by `selectedKey`**: it outlives the follow and would mark a session
  the tab no longer follows.
- **The worktree in the title**: the owner's correction; the status bar says where.
