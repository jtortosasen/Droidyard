# ADR-0024: Project name and identifiers

- Status: Accepted
- Date: 2026-09-29

## Context

The project ran its entire design phase under the provisional codename `mydev`, used
across 24 ADRs, six subsystems, three interface documents and a research corpus. The
name was listed as an open decision from the first day.

By this point the name had already leaked into a set of derived identifiers that are
expensive to change once code exists: the CLI and its subcommands, the state and
configuration directories, the environment variables that override them, the artifact
URI scheme, the MCP `_meta` key and the HTTP header that carries the untrusted mark.
Renaming after implementation means migrating user state, not just rewriting prose.

## Decision

- **Project name: AioDevAndroid.** It is the name in the repository, the documents and
  the about text.
- **CLI and binary: `aiodev`.** The full name is awkward to type daily and the binary
  appears in every shell command; `aiodev` is the short form and matches the leading
  token of the project name.

Derived identifiers, fixed here so they are decided once:

| Thing | Value |
| --- | --- |
| Project | `AioDevAndroid` |
| Binary and CLI | `aiodev` |
| Subcommands | `serve`, `mcp`, `call`, `ui`, `trust`, `status`, `stop` |
| State root | `$XDG_STATE_HOME/aiodev` · `~/Library/Application Support/aiodev` |
| Configuration | `$XDG_CONFIG_HOME/aiodev` · beside the state directory on macOS |
| Cache | `$XDG_CACHE_HOME/aiodev` · `~/Library/Caches/aiodev` |
| Env overrides | `AIODEV_STATE_DIR`, `AIODEV_CONFIG_DIR` |
| Socket | `<state>/run/aiodev.sock` |
| Artifact URI scheme | `aiodev://artifact/<id>` |
| MCP `_meta` key | `aiodev/untrusted` |
| Untrusted HTTP header | `x-aiodev-untrusted` |
| Probe init script | `aiodev-probe.init.gradle` |

## Consequences

- The name decision is closed; no further renaming is expected before implementation.
- The identifier set above is the single reference for names that appear in more than
  one place. A new identifier that derives from the project name belongs in this table.
- The Codename "mydev" is retired. Documents written before this ADR have been renamed
  in place; there was no code, so no migration was needed.

## Alternatives considered

- **Keep `mydev`.** Short and neutral, and free. Rejected: it reads as a placeholder,
  and the owner preferred a name that says what the project is.
- **`AioAndroidDev`** (matching the directory and repository created during the design
  phase). Rejected in favour of the ordering the owner chose.
- **Renaming later, before distribution.** Considered and rejected: the identifiers
  above would have to change in user state, and the cost only grows.
