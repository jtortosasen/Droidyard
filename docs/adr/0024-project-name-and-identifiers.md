# ADR-0024: Project name and identifiers

- Status: Accepted
- Superseded in part by: ADR-0037 (§Decision)
- Date: 2026-09-29
- Superseded in part by: ADR-0061 (§Decision)
- Superseded in part by: ADR-0062 (§Decision)
- Amended: 2026-09-29 — the name was revised from `AioDevAndroid` to `Droidyard`
  before any code existed. See §Revision.
- Amended: 2026-10-06 — "beside the state directory on macOS" named:
  `~/Library/Application Support/droidyard-config/` (ADR-0031). Milestone 1g plan D1,
  Delegated.

## Context

The project ran its entire design phase under the provisional codename `mydev`, used
across every document. The name was listed as an open decision from the first day.

By the time it was closed, the name had already leaked into a set of derived
identifiers that are expensive to change once code exists: the CLI and its
subcommands, the state and configuration directories, the environment variables that
override them, the socket, the artifact URI scheme, the MCP `_meta` key and the HTTP
header that carries the untrusted mark. Renaming after implementation means migrating
user state, not just rewriting prose.

## Decision

- **Project name: Droidyard.** A yard is where ships are built, fitted out and put to
  sea; a droid is what this tooling builds for. The name describes **the loop** —
  build, install, launch, observe, repeat — rather than the platform, which is what
  distinguishes it from the many tools named after Android itself.
- **CLI and binary: `droidyard`.** The project name and the command are the same
  string, so there is no mismatch to explain.

Derived identifiers, fixed here so they are decided once:

| Thing | Value |
| --- | --- |
| Project | `Droidyard` |
| Binary and CLI | `droidyard` |
| Subcommands | `serve`, `mcp`, `call`, `ui`, `trust`, `status`, `stop` |
| State root | `$XDG_STATE_HOME/droidyard` · `~/Library/Application Support/droidyard` |
| Configuration | `$XDG_CONFIG_HOME/droidyard` · beside the state directory on macOS |
| Cache | `$XDG_CACHE_HOME/droidyard` · `~/Library/Caches/droidyard` |
| Env overrides | `DROIDYARD_STATE_DIR`, `DROIDYARD_CONFIG_DIR` |
| Socket | `droidyard.sock` under `run/`; the full rule is in [ADR-0021](0021-persistence-format-and-location.md) |
| Artifact URI scheme | `droidyard://artifact/<id>` |
| MCP `_meta` key | `droidyard/untrusted` |
| Untrusted HTTP header | `x-droidyard-untrusted` |
| Probe init script | `droidyard-probe.init.gradle` |

## Revision (2026-09-29)

The first name decided was **`AioDevAndroid`**, with `aiodev` as the binary. Two
problems surfaced on review:

1. It is an acronym nobody can pronounce, and it is long for something typed daily.
2. The binary (`aiodev`) did not follow from the project name, so every document had
   to carry two names for one thing.

A name search followed, and it produced a finding worth recording: **every single
dictionary word tested was already taken on both GitHub and npm** — `gantry`,
`shuttle`, `tether`, `hoist`, `lathe`, `bench`, `bay`, `reel`, `grasp`, `kiln`,
`mill`, `jig`, `spindle`. Only compounds were free, which is what made the choice a
question of composition rather than of vocabulary.

The shortlist, with what was checked:

| Candidate | Binary | Available | Note |
| --- | --- | --- | --- |
| **`Droidyard`** | `droidyard` | every registry | Chosen: the yard metaphor covers build, fitting and launch |
| `Droiddock` | `droiddock` | every registry | Close second: shorter, and *dock* is literally what one does with a device |
| `Droidgantry` | `droidgantry` | every registry | The most precise metaphor (a launch gantry), but the longest to type |
| `Droidrig` | `droidrig` | every registry | A test rig: apt, less evocative |
| `Anddock`, `Anddev`, `Andloop` | — | npm free | Shortest, but they read as two words ("and dock"), which costs more than the length saves |
| `Aioandroid` | `aioandroid` | every registry | Kept the original acronym, so it kept the original problem |

Two naming traps were noted and avoided: **"droid" is saturated** in the Android
ecosystem, so the second element has to carry the meaning; and **the `and-` family is a
readability trap**, because the eye splits it.

## Consequences

- The name decision is closed. No further renaming is expected before implementation.
- The identifier table above is the single reference for names that appear in more than
  one place. A new identifier derived from the project name belongs in that table.
- The codename `mydev` and the interim name `AioDevAndroid` are retired. There is no
  code, so neither migration nor compatibility shim is needed.

## Alternatives considered

See the shortlist in §Revision. Rejected on principle: keeping `mydev` (reads as a
placeholder), and renaming later (the identifiers above would have to change in user
state, and the cost only grows).
