# Scope

The coarse line between what we build and what we deliberately do not. The MVP is
elaborated in [`../roadmap/mvp.md`](../roadmap/mvp.md).

## Terms

- **MVP**: the first release. It is defined in `roadmap/mvp.md` and exists to test
  the core hypothesis. Where this file and `mvp.md` disagree about the MVP, `mvp.md`
  wins.
- **v1**: the first release meant for daily use. It is the MVP plus everything marked
  *v1* below. v1 is where [vision](vision.md) success criterion 1 (code intelligence
  without Android Studio) is met.
- **Later**: after v1, with no commitment.

## In scope

### Core runtime (MVP)

- One persistent daemon per user, which agents and the CLI reach through shims
  ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)), with workspaces,
  sessions and operations.
- Supervision of our own sidecars: the Gradle Tooling API sidecar
  ([ADR-0017](../adr/0017-gradle-execution-strategy.md)), and the Kotlin LSP in v1.
  The `adb` server, the Gradle daemon and emulators belong to their tools: we start or
  use them, and never own them.
- Filesystem access and file watching.
- Structured logging, events, and one operation record per call.
- Durable state outside the project tree
  ([ADR-0021](../adr/0021-persistence-format-and-location.md)).

### Capability domains

The authoritative list is the capability registry
([capability-api](../architecture/capability-api.md) §Naming).

| Domain | MVP | v1 adds |
| --- | --- | --- |
| `system` | capabilities, health | |
| `workspace` | open, describe, close | |
| `project` | modules, variants | |
| `build` | assemble, run, tasks, errors, lastResult, all through the TAPI sidecar | test |
| `device` | list, install, launch, uninstall, logs (structured, snapshot + bounded follow), screenshot, uiTree, tapElement, inputText | processes, app info, backtrace, wireless pairing |
| `emulator` | list, start, stop, wipe | create, delete |
| `sdk` | status, requirements | package installation and licences |
| `file` | list, read, write, watch | |
| `git` | status, diff, log, branches (scheduled last, may slip) | |
| `code` | — | definition, references, symbols, diagnostics (Kotlin LSP) |
| `shell` | exec (off unless policy enables it) | |
| `operation`, `events`, `artifact` | wait, cancel; subscribe; read | |
| `policy` | get, update, **human-only** ([ADR-0015](../adr/0015-untrusted-data-authorization.md)) | |

### Interfaces

- HTTP + WebSocket server for the UI, loopback-only in the MVP
  ([http-websocket](../interfaces/http-websocket.md)).
- MCP server for agents, over stdio in the MVP.
- CLI (`aiodev call`) for scripts and testing.
- A raw shell escape hatch for operations not yet modelled.

### UI (minimal)

- MVP: Logcat, build and Gradle tasks, Git, devices and screen, then project explorer
  and problems.
- v1: a light editor with diagnostics, once code intelligence exists.

## Out of scope for v1

- **Own agent harness.** No prompt assembly, no agent loop, no session lifecycle of an
  LLM. The external agent provides this.
- **Android Studio feature parity.** No full refactoring engine, no visual layout
  editor, no APK analyser, and so on.
- **Deep debugger integration.** JDWP stepping and breakpoints.
- **Embedded emulator.** We orchestrate the platform emulator; we do not build one.
- **Multi-user, remote or cloud.** Local-first and single user.
- **Windows.** Unix-first targets are macOS and Linux
  ([ADR-0002](../adr/0002-unix-first.md)); Windows is a deferred seam.
- **A dependency on Android CLI.** We use Gradle, through the Tooling API, and ADB
  directly ([ADR-0005](../adr/0005-reuse-platform-tools.md)).
- **A Gradle CLI execution path.** Every Gradle interaction goes through the TAPI
  sidecar ([ADR-0017](../adr/0017-gradle-execution-strategy.md)).

## Later

- LAN and tablet access to the UI: TLS and authentication, which need their own ADR.
  The engine is designed for this, but it is not scheduled.
- UI automation at scale: the row abstraction for unlabelled lists, and OCR
  ([ADR-0013](../adr/0013-ocr-optional-pluggable.md)).
- Crash and ANR analysis beyond structured logs.
- Live screen streaming to the UI. This is a stretch goal inside the MVP, not required.
- Deeper code intelligence: call hierarchy, rename, code actions.
- MCP resources, prompts, elicitation and Streamable HTTP
  ([mcp-features](../interfaces/mcp-features.md)).
- Distribution as an installable MCP server plus optional skills per agent
  ([ADR-0011](../adr/0011-skills-agent-skills-standard.md),
  [ADR-0014](../adr/0014-agent-distribution-model.md)).
- Optional agent-specific adapters, plugins or hooks, where they add real value.
