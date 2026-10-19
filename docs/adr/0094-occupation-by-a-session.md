# ADR-0094: A session occupies its worktree and the device it acts on

- Status: Accepted
- Superseded in part by: ADR-0104 (§Decision)
- Date: 2026-10-18
- Supersedes: ADR-0082 §Decision (in part: §7, one session holds a worktree); ADR-0088 §Decision (in part: §3, a remembered worktree another session holds is not restored, and its memory is kept); ADR-0032 §Decision (in part: the file gains `occupation`); ADR-0083 §Decision (in part: §1, the rows and the payload gain `occupies`, `devices`, `stale`); ADR-0089 §Decision (in part: §1, the items gain `occupiedBy`); ADR-0010 §Decision (in part: a stateless capability may hold a worktree or a device for the calling MCP session)
- Relates to: ADR-0015 §2, §6, ADR-0019 §Sessions, ADR-0036, ADR-0048 §2, ADR-0095; `docs/vision/beyond.md` (C2); plan m2s Q1
- Decided by the owner, 2026-10-18: Droidyard coordinates and does not secure; a write is refused only where another live session works, with a way out, and every other write is allowed and recorded; one concept for worktrees and devices; the main tree is never occupied; an occupation ends on close, on release, on idleness (then a warning, not a refusal) and on the owner's release; a resumed chat does not get back an occupation lost meanwhile. A device's start, the configuration's shape, the idle default and the release without a new tool were drafted by the planner and decided by the main session under the owner's standing instruction, Delegated.

## Context

ADR-0082 §7 made owning a worktree being bound to it, not exclusively, and devices belong
to no workspace (`workspace-state.md`, `adb-device.md` §Targeting): one chat can write in
another chat's worktree and drive another chat's emulator (register rows, 2c step 13).
The owner wants Droidyard to keep sessions apart by organisation, not by security: the
agent client's own permissions stay the security, and a session that would cross another
is told to work in its own worktree and integrate through git. The MCP tool list has
189 B left and the server instructions none (plan m2s §Starting point), so no claim or
release tool fits.

## Decision

1. **Occupation** is a live MCP session holding a linked worktree or a device, so that
   another session does not write in that worktree or act on that device. One holder per
   worktree and per device; a worktree may still hold several bound sessions (ADR-0082
   §7) but one holder. **The main tree is never occupied**: it is everyone's.
2. **It starts** when a session binds to a free linked worktree (the hello's root,
   `workspace.enter`, ADR-0088's restore), and when an MCP session's acting call reaches a
   free device: install, uninstall, launch, tap or type through an element, emulator stop
   or wipe; `emulator.start` holds the serial it boots. Reads never hold.
3. **It ends** when the connection closes; when the holder's binding moves (entering the
   main tree, the skill's "task done", also releases its devices); when an emulator stops
   or its serial leaves `adb devices`; and when the owner releases it (`session.release`,
   audience human), which never moves the session. Nothing is persisted. A resumed chat
   whose worktree another session now holds is bound at the main tree; it learns why from
   `workspace.describe`'s `occupiedBy` or its first refused edit.
4. **Idle.** A holder with no MCP request and no joined hook (ADR-0095) for
   `staleAfterMinutes` (120, a working default) is shown idle. Another session is then
   warned instead of refused, and entering or acting takes the occupation over. Any call
   or joined hook ends idleness.
5. **Conflicts** are another session's write into a held, non-idle worktree (its edit
   tools and a routed `rm`, through ADR-0095's hooks), its `workspace.enter` there, and its
   acting call on a held device. The verdict follows `conflict`: `refuse` (the default),
   failing `WORKSPACE_OCCUPIED` or `DEVICE_OCCUPIED` with a message naming the holder and
   the way out (a worktree of its own, or the user's release); `ask`, the client's own
   prompt where a hook can raise one; `allow`, recorded. A writer that cannot be told from
   the holder (no chat id tells them apart) is warned, never refused. The UI and the CLI
   are never refused.
6. **`ask` where Droidyard cannot ask** (`workspace.enter`, devices, a client that ignores
   a hook's ask) is `refuse`: the user chose to be consulted, not to let it through.
7. **Configuration**: `config.json` gains `"occupation": {"conflict":
   "refuse"|"ask"|"allow", "staleAfterMinutes": 120}`, every key optional, re-read by size
   and mtime; a wrong value makes the file unusable, as ADR-0032 §4 says of any key. It is
   edited by hand; no capability changes it (Delegated).
8. **Subagents.** Occupation is per connection, as the session is (ADR-0082 §1). A
   subagent's hooks carry its chat's id, so its writes are its session's own and never
   refused in its parent's worktree. A worktree-isolated subagent's `workspace.enter` still
   moves its parent and releases what the parent held: MCP calls carry no subagent
   identity.
9. **Not a security boundary.** A session id grants nothing (ADR-0048 §2); a same-uid
   process can present any chat id. The client's own permissions are the security.
10. **Remote devices.** The holder is a session and the device a key (a serial today), so
    C4's remote devices and users fit this rule unchanged.

## Consequences

- Error codes `WORKSPACE_OCCUPIED` and `DEVICE_OCCUPIED`; `occupiedBy` on
  `workspace.describe`'s worktrees and on `device.list`; `occupies`, `devices` and `stale`
  on `session.watch`; `session.release`; one minor API version.
- `workspace.enter`'s description names the release (entering the main tree) within
  ADR-0036 §1; the skill says to enter the main tree when a task is done.
- Hooks dial the daemon only for edits, a routed `rm` and Android commands, so a holder
  that only reads or runs other shell commands goes idle; a Cursor holder that only edits
  does too (ADR-0095 §1). Both are register rows.
- A resumed chat's shell builds in a worktree it no longer holds are not refused.
- Documents that change with it: `workspace-state.md` (devices and worktrees held by
  sessions), `adb-device.md` §Targeting, `mcp.md` §Transport and session.

## Alternatives considered

- **Refusing only at `workspace.enter`, no hooks**: an agent writes with its own tools by
  absolute path, so the worktree would not be protected.
- **Exclusive binding** (a second session cannot even read there): the owner refused only
  writes, and a question about another worktree's state harms nothing.
- **The main tree occupied too**: it blocks the shared checkout every session starts from.
- **A device held from `emulator.start` only**: a session using a running emulator would
  never hold it. **Held from any call, reads included**: a watching session would block the
  acting one. **An explicit claim tool**: 189 B left in the tool list, and an agent forgets
  to call it.
- **A dedicated release tool**: the same budget; entering the main tree already says the
  task is done. It wins if the tool list gains room and agents are seen holding worktrees
  after their task.
- **Idleness ending the occupation outright**: a person at lunch would lose the worktree
  to the next chat; a warning first and a takeover on entry let both proceed.
- **`ask` falling back to `allow`**: the user who chose `ask` did not choose to let writes
  through.
- **Persisting occupations across a restart**: sessions do not survive one (ADR-0010), so
  a persisted holder would be no one.
- **Keeping ADR-0082 §7**, which rejected an exclusive owner because a second chat on the
  same task would be refused its task's worktree: the owner's decision refuses exactly
  that while the first chat is live and not idle, and leaves the way out.
