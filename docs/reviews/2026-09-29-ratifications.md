# Pending owner ratification

The pre-implementation review produced decisions that agents could not settle on their
own: they are product, security or measurement calls. Each is already written into the
documents with a working default and marked *needs ratification*.

Full review: [`2026-09-29-pre-implementation-review.md`](2026-09-29-pre-implementation-review.md).

## Security — recommended: yes to all three

| # | Decision | Where |
| --- | --- | --- |
| 1 | **`shell.exec` is off by default.** The tool stays listed so the surface is stable, and a call returns `POLICY_SHELL_DISABLED`. | [ADR-0015](../adr/0015-untrusted-data-authorization.md) §7 |
| 2 | **Project roots are trusted explicitly.** An untrusted root opens restricted; capabilities that run project code fail with `WORKSPACE_UNTRUSTED`. Trust comes from a configured path prefix or a human act, **never** from an MCP binding — a repository can ship its own `.mcp.json`. Cost: friction on the first agent session in a new project. | [ADR-0015](../adr/0015-untrusted-data-authorization.md) §5 |
| 3 | **Policy is never exposed over MCP, not even read-only.** The core rejects those calls from an MCP session; it is enforcement, not omission. | [ADR-0015](../adr/0015-untrusted-data-authorization.md) §6 |

## Operational defaults

| # | Decision | Default in the docs | Where |
| --- | --- | --- | --- |
| 4 | Does an **auto-started daemon** exit after a long quiet period? | No: it runs until `mydev stop`, a signal or a crash. Persistence is the point, and sidecars idle out. | ADR-0019 §Lifetime |
| 5 | The **MCP wait budget** before a long tool returns an operation id | 20 s, configurable per server, always below the client timeout. Pending spike S2. | ADR-0020 §Adapter projections |
| 6 | A **queued short call** behind a long one | Fails after 20 s with `OPERATION_QUEUE_TIMEOUT`, before running (retrying is safe). Alternative: wait, at the risk of the client timeout cancelling the operation. | ADR-0020 §States |
| 7 | The **MCP adapter opens the bound root** at session start | Yes. If another client closes it, tools return `WORKSPACE_NOT_OPEN` and the adapter does not reopen it. Alternative: never open implicitly, and make the agent call `workspace.open`. | ADR-0020 §Scope |
| 8 | **`emulator.wipe`** leaves the emulator running afterwards | Yes, and returns the fresh device. Alternative: stop it after the wipe. | `subsystems/emulator.md` |
| 9 | **adb client/server version mismatch** | Refuse device calls rather than let adb restart the shared server. Safer, more annoying. | `subsystems/adb-device.md` |

## Tooling and product

| # | Decision | Default in the docs | Where |
| --- | --- | --- | --- |
| 10 | **The probe**: an init script plus a small jar, or a task-name heuristic | The probe (exact variants, plugin versions, `compileSdk`). Heavier, but used only for `project.model` and never injected into a user's build. | ADR-0023 |
| 11 | **JVM discovery order** for the sidecar | configured path → `JAVA_HOME` → `java` on `PATH` → macOS `java_home -v 17+` → Android Studio's JBR. `PATH` moved up, unlike the review's proposal, to match `./gradlew` and maximise Gradle daemon reuse. | ADR-0017 §Environment |
| 12 | **CLI falls back to `ANDROID_SERIAL`** when `--device` is absent | Yes. | `subsystems/adb-device.md` |
| 13 | Confirm the items the Gradle work **declared ratified** (sidecar ownership, protocol, output transport, JVM order) — written by an agent, not by you | — | ADR-0017, `subsystems/gradle.md` |

## Measurement and layout

| # | Decision | Default in the docs | Where |
| --- | --- | --- | --- |
| 14 | The **hypothesis success threshold**: our arm uses ≥ 30 % fewer tool calls with no more failures than the shell baseline | As stated, over N ≥ 5 runs with a pinned model. | `roadmap/mvp.md` §Metrics |
| 15 | **User configuration location**: `$XDG_CONFIG_HOME/mydev` (macOS: beside the state directory), separate from state so deleting state cannot grant or revoke trust | As stated. | ADR-0021 |

## Not a ratification, but a decision to settle before Phase 1

- **AAPT2 merged-resource paths.** A resource-linking error names AGP's merged
  resources, not the source file. Working assumption: report the virtual path labelled
  `merged: true` always, and quote the merged line where the file can be located.
  *Must be settled before `build.errors` is implemented.*
  [ADR-0022](../adr/0022-build-error-extraction.md) §Known open item.
