# ADR-0031: The configuration file starts with trusted roots only

- Status: Accepted
- Date: 2026-10-06
- Relates to: ADR-0015 §5–6 (trusted roots, human-only policy), ADR-0019 §3–4
  (`droidyard trust`), ADR-0021 §Location, ADR-0024 (directory names),
  `docs/plans/m1g-gradle-sidecar-and-build.md` D1–D3
- Decided by the main session under the owner's standing instruction to apply the
  recommended option (Delegated, milestone 1g plan D1 and D2); the owner can revert it.

## Context

Milestone 1g is the first to run project code (Gradle configures and builds the
project), so ADR-0015 §5's trusted roots must exist: without them every `project.*` and
`build.*` call fails `WORKSPACE_UNTRUSTED`. ADR-0021 places user configuration outside
the state directory and leaves its format "to the configuration spec", which does not
exist yet. ADR-0019 §3 says `droidyard trust` goes over `policy.update`, which does not
exist either, and §4 auto-starts the daemon for it.

## Decision

1. **One file, `config.json`, holding trust only** until the configuration
   specification defines the rest:
   `{"schemaVersion": 1, "trust": {"roots": [], "prefixes": []}}`. Paths are absolute
   and canonical. A root is trusted when it equals a `roots` entry or lies under a
   `prefixes` entry at a path-component boundary (`/a/b` trusts `/a/b/c`, not `/a/bc`).
   Unknown keys are kept on rewrite and ignored. A relative entry makes the whole file
   unparseable (below): canonicalised, `.` would mean `/` and trust every root.
2. **Location.** `DROIDYARD_CONFIG_DIR`; else, when `DROIDYARD_STATE_DIR` is set,
   `<state>/config/` (one variable isolates a run, ADR-0021 §Location); else Linux
   `$XDG_CONFIG_HOME/droidyard` (default `~/.config/droidyard`), macOS
   `~/Library/Application Support/droidyard-config/`, a sibling of the state root and
   never inside it, so deleting state cannot grant or revoke trust.
3. **Failure modes.** A missing file trusts nothing. An unparseable one trusts nothing,
   and the `WORKSPACE_UNTRUSTED` cause names the file and the parse error; it is user
   configuration, so it is never renamed, reset or rewritten by us.
4. **`droidyard trust <root>` writes the file itself**: it canonicalises the root as
   `workspace.open` does, takes an exclusive `flock` on `config.json.lock`, re-reads,
   adds the root, and writes atomically. It needs no daemon and starts none. The daemon
   re-reads the file (size and mtime) at each trust check, so a `trust` or a hand edit
   takes effect without a restart (ADR-0015 §6). There is no `untrust`: edit the file.
5. **Trust is checked at the authorization re-check** (ADR-0020), before anything is
   spawned; the nextStep is the exact command, `droidyard trust '<root>'`.

## Consequences

- No capability can trust a root: the only writers are a human at the CLI and the file
  itself (ADR-0015 §5–6 hold). An agent's own shell runs as the same user and can run
  `droidyard trust` or edit the file; that is ADR-0015 §6's stated residual risk. The
  same goes for an MCP client configuration that sets `DROIDYARD_CONFIG_DIR` or
  `DROIDYARD_STATE_DIR` for an auto-started daemon: it grants no more than the server
  `command` that configuration already runs.
- A test or scratch run under `DROIDYARD_STATE_DIR`, with `DROIDYARD_CONFIG_DIR` unset,
  never reads or writes the user's real trust list.
- When `policy.get`/`update` land, `droidyard trust` may move onto them; the file
  format stays.

## Alternatives considered

- **`droidyard trust` over a minimal `policy.update` now.** Needs the daemon running
  (auto-start) and an authorization path for a human-only capability before the policy
  model exists; it adds a capability for one key. Rejected for this milestone.
- **The configuration inside the macOS state root** (`…/droidyard/config/`). Simpler
  path, but deleting state would revoke trust, against the ratified "separate from
  state". Rejected.
- **Trust in `workspace.json`.** That file is state, written by the daemon on behalf
  of any client, including agents. Rejected: trust would no longer be human-only.
