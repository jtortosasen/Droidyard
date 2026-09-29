# Owner decisions — closed 2026-09-29

Nineteen decisions that the review process left to the product owner: fifteen
operational and security ones from the pre-implementation review, plus four about what
the project is and who it is for. All are closed. This is the record of *what was
decided and why*, not a list of open items.

Full review: [`2026-09-29-pre-implementation-review.md`](2026-09-29-pre-implementation-review.md).

## Security

| Decision | Outcome | Where |
| --- | --- | --- |
| `shell.exec` | **Off by default.** The tool stays listed so the surface is stable; a call returns `POLICY_SHELL_DISABLED`. | [ADR-0015](../adr/0015-untrusted-data-authorization.md) §7 |
| Project-root trust | **Trust is explicit.** An untrusted root opens restricted; capabilities that run project code fail with `WORKSPACE_UNTRUSTED`. Trust comes from a configured path prefix or a human act, **never** from an MCP binding — a repository can ship its own `.mcp.json`. The friction on the first session in a new project is accepted. | [ADR-0015](../adr/0015-untrusted-data-authorization.md) §5 |
| Policy over MCP | **Never exposed**, not even read-only. The core rejects those calls from an MCP session: enforcement, not omission. | [ADR-0015](../adr/0015-untrusted-data-authorization.md) §6 |

## Operational defaults

| Decision | Outcome | Where |
| --- | --- | --- |
| Daemon lifetime | **Does not idle-exit.** A daemon the user expects to find there is the point of persistence, and sidecars idle out so the cost is small. | ADR-0019 §Lifetime |
| MCP wait budget | **20 s**, configurable per server. Provisional until spike S2 measures real client timeouts. | ADR-0020 §Adapter projections |
| A short call queued behind a long one | **Fails after 20 s** with `OPERATION_QUEUE_TIMEOUT`, before running, so retrying is safe. Chosen over waiting, because the alternative reintroduces the client-timeout-cancels-the-operation path. | ADR-0020 §States |
| The MCP adapter opening its bound root | **Yes, once at session start.** If another client closes it, tools return `WORKSPACE_NOT_OPEN` and the adapter does not reopen it. | ADR-0020 §Scope |
| `emulator.wipe` | **Leaves the AVD running**, factory-fresh, and returns the new device. | `subsystems/emulator.md` |
| adb client/server version mismatch | **Warns and proceeds**, rather than refusing. The risk is recorded: restarting the shared server can interrupt another tool mid-operation. | `subsystems/adb-device.md` |

## Tooling

| Decision | Outcome | Where |
| --- | --- | --- |
| The probe | **The probe** (init script plus a small jar), over the task-name heuristic: exact variant names, AGP and Kotlin plugin versions, and `compileSdk` are what decide whether errors arrive structured. Used only for `project.model`, never injected into a user's build. | ADR-0023 |
| Sidecar JVM discovery | configured path → `JAVA_HOME` → `java` on `PATH` → macOS `java_home -v 17+` → Android Studio's JBR. `PATH` sits high so the sidecar matches `./gradlew` and is more likely to reuse its daemon. | ADR-0017 §Environment |
| CLI device fallback | `--device`, else **`ANDROID_SERIAL`** — adb's own convention. | `subsystems/adb-device.md` |
| The Gradle work's self-declared "ratified" items | **Confirmed** by the owner: sidecar ownership, protocol, output transport and JVM order. | ADR-0017, `subsystems/gradle.md` |

## Measurement and layout

| Decision | Outcome | Where |
| --- | --- | --- |
| The hypothesis threshold | **A pilot run first, then the threshold** — with one guard: the pilot exists to *size* the test (run-to-run variance, and therefore how many runs each arm needs). The threshold itself is fixed **before the comparison runs**, from "is this difference worth building on", and **never from the pilot's observed difference**, which would be grading on a curve. The pilot is not a measurement. | `roadmap/mvp.md` §Metrics |
| User configuration location | `$XDG_CONFIG_HOME/droidyard` (macOS: beside the state directory), **separate from state** so that deleting state cannot grant or revoke trust. | ADR-0021 |

## Product decisions, same date

Four questions about *what this is and who it is for*, asked and answered after the
fifteen above.

| Decision | Outcome | Where |
| --- | --- | --- |
| Project name | **Droidyard**, with `droidyard` as the binary and CLI, and a fixed set of derived identifiers: state and config directories, env overrides, socket name, artifact URI scheme, MCP `_meta` key and HTTP header. Recorded once so it is not decided again. | [ADR-0024](../adr/0024-project-name-and-identifiers.md) |
| Which assistants matter | **Cursor and OpenCode.** They are what the owner uses, and they are what spike S2 measures — the MVP's "verify against at least two" now has names. | `roadmap/mvp.md` |
| Validation target | **DopamineControl**, the owner's real multi-module project with `build-logic` convention plugins, replaces the sample app as the acceptance target. CI keeps small synthetic fixtures, including two Kotlin-plugin versions. An acceptance run needs a clean checkout, because the project has uncommitted work. | `roadmap/mvp.md` |
| Remote access | **After v1.** It is where the project started, but the MVP stays on loopback and the design already permits adding it without rework. | `roadmap/mvp.md` |

## Still to settle before Phase 1, and not a ratification

- **AAPT2 merged-resource paths.** A resource-linking error names AGP's *merged*
  resources, not the source file. Working assumption: always report the virtual path
  labelled `merged: true`, and quote the merged line where the file can be located.
  *Must be settled before `build.errors` is implemented.*
  [ADR-0022](../adr/0022-build-error-extraction.md) §Known open item.
