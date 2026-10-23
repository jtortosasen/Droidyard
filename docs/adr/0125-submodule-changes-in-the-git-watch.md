# ADR-0125: The Git watch reads inside submodules: git flags them on a trusted root, the daemon reads them on an untrusted one

- Status: Accepted
- Date: 2026-10-23
- Supersedes: ADR-0066 §Decision (in part: item 1's one status a tick, which becomes the project's status plus a submodule pass; item 5's entries, which gain a submodule's records and files)
- Relates to: ADR-0066 §2, §3; ADR-0071 §1, §7; ADR-0067 §2, §5; ADR-0015 §2; plan m3b Q1–Q3
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated (plan m3b Q1–Q3, reviewed in two rounds). The scope is the owner's (2026-10-22): the status stops ignoring submodules, a second status only in the submodules with changes, `git.read` from the submodule's HEAD, the UI grouping by submodule. The trusted/untrusted split in item 1 is the main session's, over the plan's draft (every submodule read on every root), to keep the owner's "only in the submodules with changes" where the fence allows it.

## Context

`git.watch` runs `status --ignore-submodules=all` once a tick, so a submodule's new commits and
its uncommitted files never reach the Git tab or Review. git can report them itself with
`--ignore-submodules=none`: it then starts a `git status` in every populated submodule, with
that submodule's own configuration. On an untrusted root that is outside ADR-0066 §3's fence:
the daemon reads and switches off the filter drivers of the repository it runs git in, and a
driver configured only in a submodule's config is not among them. `git.submodules` (ADR-0071
§1) already runs its own git in each submodule's directory under the workspace's trust, which
reads and switches off that submodule's drivers. Without an explicit flag,
`diff.ignoreSubmodules` and `submodule.<name>.ignore` (config or `.gitmodules`) decide what the
status shows.

## Decision

1. **The flag, by trust.** The project's status, and each submodule's, carries an explicit
   `--ignore-submodules`, which overrides the configuration's and `.gitmodules`' `ignore`:
   - **trusted root: `none`.** git looks inside each populated submodule itself and flags it in
     its record's `S<c><m><u>` field; the root's trust covers the submodules below it, and git's
     children inherit ADR-0066 §2's `-c` overrides and protocol variables.
   - **untrusted root: `dirty`.** git reports a gitlink only when its commit differs, and starts
     nothing in a submodule; the daemon's pass (item 2) finds the contents.
   `git.submodules` keeps `all` for its own `dirty` state.
2. **The submodule pass.** After the project's status, and only when the repository's top has a
   `.gitmodules`, the daemon runs one status in a submodule's own directory, through the runner
   with the workspace's trust (its drivers read and switched off there on an untrusted root),
   after `git.submodules`' containment checks, rerun each tick: on a trusted root only in the
   submodules git flagged `m` or `u`; on an untrusted one in every initialised submodule. Nested
   submodules by the same rule, to eight levels.
3. **The entries.** Marks unchanged. Every entry inside a submodule carries `root`, the
   submodule's path as `git.read` takes it, its own path staying the workspace's. A gitlink's
   own entry carries `submodule {commits, modified, untracked, checkedOut?, unread?}`, the bits
   as git's `S<c><m><u>`; a gitlink changed only in content gets an entry marked `modified`.
   `submodulesUnread` on the status says what no gitlink can carry.
4. **Bounds and failure.** At most 32 submodules read per tick, a 5 s deadline for the pass, the
   5000 entries and each git's 4 MiB cap shared with the project (`truncated`). A git that fails
   in a submodule marks that gitlink `unread` with a cause and the rest is published; a failure
   to list the submodules, the cap or the deadline set `submodulesUnread`. Only the project's own
   status failing stops the watch `failed`, as before.
5. **Review** reads a submodule's file at that submodule's HEAD: `git.read` with `root`
   (ADR-0071 §7), unchanged.

## Consequences

- A trusted project pays git's own child status per populated submodule, and the daemon's
  status only where there are changes. An untrusted project (the default) pays 2 gits per
  initialised submodule and per level with a `.gitmodules`, doubled by the driver reads: about
  4× git's own processes; with tens of submodules a tick takes seconds, and the project's own
  changes, published once per tick after the pass, reach the UI that much later. Trusting the
  root is the way to the cheap path.
- On a trusted root, what a submodule's child status runs is the submodule's own configuration
  under the parent's overrides: the child is git's, not the daemon's.
- A submodule near the deadline can flicker between listed and `unread` under load.

## Alternatives considered

- **`none` on every root**: a filter configured only in a submodule would run on an untrusted
  root, outside the driver read.
- **`dirty` on every root, the daemon reading every submodule** (the plan's draft): one code
  path, but every initialised submodule read every tick on trusted roots too, where git can
  flag them at a quarter of the processes; the owner asked for a second status only where there
  are changes.
- **Flag submodules from `file.watch` or a cache across ticks**: `file.watch` excludes
  `.git/modules/<x>`, where a submodule's index changes, and a cache is a second freshness for
  one view.
- **A seventh mark `submodule`**, **a separate `submodules` list**, **paths relative to the
  submodule**: plan m3b Q2; each loses information or duplicates it.
- **A failure in a submodule fails the tick**: one malformed `.gitmodules` would blank a Git tab
  that today ignores submodules.
