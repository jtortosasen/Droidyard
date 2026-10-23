# ADR-0075: The project root and the Gradle root

- Status: Accepted
- Date: 2026-10-15
- Superseded in part by: ADR-0092 (§Decision)
- Superseded in part by: ADR-0124 (§Decision)
- Supersedes: ADR-0045 §1 (in part: `ui` also finds a project at its cwd when the Gradle build is below it); ADR-0046 §1 (in part: the shape gains `gradleRoot`), §2 (in part: the daemon reads `gradleRoot`, and the UI reads the file again before each write), §3 (in part: `gradleRoot` decides where Gradle runs, inside the trusted root)
- Relates to: ADR-0015 §5, ADR-0019 §Sessions (unchanged), ADR-0022, ADR-0026, ADR-0027, ADR-0037 §2, ADR-0045 §4, ADR-0058; plan m2o
- Decided by the owner (the design: "Sí porfa", 2026-10-15) and, for its details, by the main session under the owner's standing instruction (apply the recommended option), Delegated. The owner's repository keeps its git root, its Cursor configuration and loose files at the top and the Gradle build in `dev/`; opening the top gave no tasks, and opening `dev/` cut the files, git and the assistants' configuration down to `dev/`.

## Context

Every entry point finds a project as the nearest ancestor holding `settings.gradle(.kts)` (ADR-0019 §Sessions, ADR-0045 §1), and every capability takes that directory as both the workspace's root and the Gradle build's. A repository whose build is a subfolder has to be opened at the subfolder, and then its top is out of reach.

## Decision

1. **Two roots.** The *project root* is the workspace's root: its id, `file.*`, `git.*`, `shell.exec`, trust, `init`, the project file, the MCP binding, Activity. The *Gradle root* is a directory inside it, the project root itself in the common case, holding `settings.gradle(.kts)`: the sidecar's connection, the model and its fingerprint, `build.run`, `build.assemble`, `build.tasks`, `project.*` and `sdk.requirements`.
2. **Resolution**, at every call that needs it and in every `open` and `describe`; nothing is cached. A project root that is the home directory or `/` gets (b) only.
   - (a) `gradleRoot` in `<project root>/.droidyard/project.json`, a relative `/`-separated path (`"."` is the project root), used when, symlinks resolved, it lies inside the project root and holds a regular settings file; otherwise there is no Gradle root, and the reason says why. An invalid key never falls back.
   - (b) Else the project root, when it holds a settings file.
   - (c) Else the directories one or two levels below that hold one, skipping hidden directories, `build`, `node_modules` and symlinks, and not descending into a directory that holds one; at most 1,000 directories examined, past which (c) gives none. Exactly one is the Gradle root.
   - The candidates of (b) or (c) are listed even when (a) decides or fails, so a choice can be changed or repaired.
3. **Trust stays the project root's** (ADR-0015 §5): a Gradle capability checks the project root's trust first, then resolves. A project file can only point inside the root that is already trusted.
4. **No Gradle root:** the Gradle capabilities fail `PROJECT_NO_GRADLE_ROOT`, whose cause says none, several, or the key's problem, with the candidates in its details and a nextStep naming the UI's choice and the `gradleRoot` key.
5. **The contract:** `workspace.open` and `workspace.describe` gain `gradle {root?, candidates, error?}`, `root` relative to the project root. Every published path stays relative to the project root, build errors' `file` and `project.modules`' `dir` included. One minor API version.
6. **Entry points.** The MCP binding and `droidyard call` keep ADR-0019 §Sessions' rule; at a top with no settings file above it, the cwd fallback already binds the top. `droidyard`/`ui` and `init` without `--root` find a project as the nearest ancestor holding a settings file (unchanged), else the cwd itself when (a) or (c) gives it a Gradle root (never the home directory or `/`), else none. Started inside the build (`dev/app`), the upward walk wins and the project is `dev/`.
7. **The UI** shows the Gradle root in use and offers the candidates; a choice writes `gradleRoot` through `file.write`. No capability writes it. Every UI write of the project file reads it again, patches what it read and writes with that read's revision as `ifRevision` (ADR-0058), reading again on a conflict; a file it cannot parse is not written.
8. **The daemon remembers**, per open workspace, the Gradle roots its calls resolved, so a sidecar crash degrades every open workspace that resolved the crashed root, and a close closes each connection no other open workspace uses. A root switched away from keeps its connection until the close or the sidecar's idle timeout.

## Consequences

- A repository with its build in a subfolder opens at its top, with the files, git and assistants' configuration of the whole repository, and its builds run in the subfolder.
- Every project whose root holds a settings file behaves as before.
- Trust given to the subfolder does not carry over to the top: the top is trusted once.
- A folder holding exactly one Gradle project one or two levels down opens as a project from `droidyard`/`ui` and `init`.
- Several Gradle builds used at once in one project stay out (one Gradle root at a time).
- The current rule lives in `internal/workspace` (the resolution), the Gradle capabilities' descriptors, `schema/capabilities.json`, and `workspace-state.md`.

## Alternatives considered

- **The Gradle root in droidyard's state, set by a capability**: it would not travel with the project; the owner chose the project file.
- **A `workspace.setGradleRoot` capability**: a contract for a write the UI already makes through `file.write`, and a tool that lets an agent redirect builds through droidyard.
- **Resolving once at open and caching it**: a choice would need a reopen or an invalidation path; resolution is a stat and two directory reads.
- **An invalid key falling back to (b) or (c)**: a stale explicit choice would silently build another build.
- **The key consulted only when (b) fails**: a user's choice could not override a unique (c).
- **A deeper search, or the downward rule at every ancestor of the cwd**: a folder of projects or the home directory would become a project.
- **Started in `dev/app`, the project root an ancestor whose project file names `dev/`**: a walk above the build reading untrusted files, and a top without a project file would still not be found; whoever wants the top starts there or passes `--root`.
- **Requiring `.git` at the cwd**: a project that is not a repository would not be found; "exactly one" and "never home" cover the folder of projects.
- **Trust per Gradle root**: two human acts for one project.
- **Paths relative to the Gradle root**: the UI and the agent, whose cwd is the project root, open files by project-relative paths.
