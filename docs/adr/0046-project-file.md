# ADR-0046: The project file, `.droidyard/project.json`, for the UI's choices

- Status: Accepted
- Date: 2026-10-10
- Supersedes: ADR-0021 §Location (in part: the UI's choices for a project live in the project)
- Relates to: ADR-0015 §5, ADR-0031, ADR-0032, ADR-0037 §Consequences, ADR-0045; plan m2c Q3,
  D19, D20; plan m2b D10
- Decided by the owner, 2026-10-10: each project keeps a file in its folder with what the UI
  needs, so nothing is typed again, "as other IDEs do"; that git will see it does not matter
  for now. The shape below is Delegated (plan m2c Q3, reviewed in two rounds).

## Context

ADR-0021 §Location keeps all state out of the project tree and rejected `.droidyard/`; the
per-workspace `ws/<wsid>/workspace.json` was to hold "selected variant, preferences", but
nothing writes it, and 2b's UI keeps its root in `localStorage` (m2b D10) and forgets the
installed application in a new tab. The owner wants the choices to travel with the project.

## Decision

1. **`<root>/.droidyard/project.json`**, `schemaVersion: 1`: `{build: {module, variant},
   device: {avd} | {serial}, applicationId}`. Unknown keys are kept. A newer version, or a
   file that does not parse, is treated as absent and never overwritten, and the UI says so.
   A directory, as `.idea/` and `.vscode/`.
2. **Read and written by the UI** through `file.read` and `file.write`, so the daemon stays
   the only writing process; written on the first change, never on open, so opening leaves
   the tree untouched. Last writer wins; no capability interprets it. A failed write (a
   read-only checkout, ADR-0021's concern) keeps the choice for the tab and shows the failure.
3. **It only preselects.** A module, variant, AVD or serial the daemon's lists lack is
   ignored. `applicationId`, which no list holds, is used only for launch and logs after a
   click, below the newest build's output (m2c D20). Nothing runs from the file without a
   click, and it never carries trust, policy, device admission or tool keys (ADR-0015 §5,
   ADR-0031, ADR-0032): a repository that ships one gains nothing.
4. Everything else ADR-0021 keeps out of the tree stays out. Whether git sees the file is the
   user's business. It replaces m2b D10's `localStorage` root.

## Consequences

- A project opened from the picker or by `droidyard` comes back with its module, variant,
  device and application chosen, in any tab.
- The register row "the application a tab installed is that tab's" closes.
- The file appears as untracked in git; ignoring it is the user's choice (the register keeps
  the owner's `.git/info/exclude` idea).

## Alternatives considered

- **`ws/<wsid>/workspace.json`** in the state directory: the choices would not travel with
  the project.
- **A typed capability**: a contract and an agent tool for UI preferences with one consumer.
- **`localStorage`**: per browser profile and origin, m2b D10's limit.
- **One root file `.droidyard.json`**: valid, but no room for a second file.
