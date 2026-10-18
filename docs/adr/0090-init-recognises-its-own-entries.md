# ADR-0090: `init` recognises its own stale entries by their shape

- Status: Accepted
- Date: 2026-10-18
- Supersedes: ADR-0037 §Decision 2 (in part: "removes exactly what it would write now" gains the stale hook entries and retired allow rules below)
- Relates to: ADR-0037 §Decision 2, §3, ADR-0060 §Consequences, ADR-0061 §3; plan m2r
- Decided by the main session under the owner's standing instruction (apply the recommended option), Delegated. The owner asked for the `quick` rows and a cleanup of the register, 2026-10-18; the planner judged these rows not quick because they change ADR-0037 (plan m2r §Not in this milestone, draft decision).

## Context

ADR-0037 §Decision 2 has `init` merge by key, append only what is absent, and have
`--uninstall` remove "exactly what it would write now". Three register rows follow from
"now":

- After the binary's path changes (a reinstall elsewhere, a Homebrew upgrade), the hook
  command differs, so a later `init` appends a second hook entry beside the stale one, and
  `--uninstall` cannot remove the stale one, which runs a binary that may be gone.
- ADR-0060 retired the `file_list` and `file_read` tools; the allow rules an earlier `init`
  wrote for them stay forever, since neither `init` nor `--uninstall` writes them now
  (ADR-0060 §Consequences recorded it).
- Cursor ignores a `cli.json` without a `deny` array (m1o §Changes 2026-10-09 (1)), so
  `init` adds `"deny": []` when it is absent; `--uninstall` cannot tell that empty array
  from one the user had, and leaves it. Row 200 states the general case: without a
  record of what `init` wrote, an element equal to init's is taken as init's.

## Decision

1. **A stale hook entry is init's.** A hook entry for this client whose command has
   init's shape, `[DROIDYARD_STATE_DIR=<dir> ]<path>/droidyard hook --client <client>
   --root <this root>` with any absolute `<path>` (ADR-0061 §3's `HookCommand`, parsed
   with the same quoting), is init's entry. `init` replaces such an entry when its command
   differs from the one it would write now, instead of appending a second; `--uninstall`
   removes every such entry. An entry naming another root or another client is not
   init's for this run and is left alone.
2. **A retired allow rule is init's.** An allow rule naming a droidyard tool that an
   earlier `init` wrote and the registry no longer ships, the list kept in the code
   beside the allowlist (today `file_list` and `file_read`, ADR-0060), is removed by both
   `init` and `--uninstall`. A rule naming a tool that still ships follows ADR-0037 §3.
3. **The empty `deny` stays.** `--uninstall` leaves an empty `deny` array in Cursor's
   `cli.json`: Cursor needs it whenever the file holds permissions, and it grants nothing.
   Row 200's general case is accepted: an element equal to init's is init's.
4. `--dry-run` shows the replacements and removals of 1 and 2 like any other change.

## Consequences

- Rows 202 and 205 close; row 201 closes as decided here and folded into row 200, which
  stays open as an accepted limit.
- `internal/assistant` gains the command parser and the retired-tool list, with tests that
  a stale path is replaced, another root's entry is kept, and a retired rule goes.
- No area document states init's merge rule; `internal/assistant`'s package comment
  gains the shape rule. The skill's tool map is unaffected.

## Alternatives considered

- **A record of what `init` wrote** (a manifest under `.droidyard/` or the state
  directory). It would settle row 200 exactly, but it is a new file per project that a
  user copying a project or editing by hand breaks, and an uninstall without it falls back
  to the shape anyway. Wins if users report `init` removing their own identical entries.
- **Match any hook command containing `droidyard hook`.** Catches an entry a user wrote
  by hand to wrap droidyard's hook; the full shape with this root and client does not.
- **Remove the empty `deny` when nothing else is left in the file.** Cursor then ignores
  a `cli.json` the user may refill later with `allow` only, the trap m1o recorded.
