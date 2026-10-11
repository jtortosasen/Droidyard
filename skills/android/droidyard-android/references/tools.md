# Tools

Generated from droidyard's registry by `go test ./cmd/droidyard -update`; do not edit.
A session started with `--tools core` lists only the tools marked core.

| Tool | Core | Effect | What it does |
|---|---|---|---|
| `artifact_read` | core | read-only | Reads an artifact by the reference another tool returned (a screenshot, a build console, shell output): an image can be resized or cropped, text is ... |
| `build_assemble` | core | changes state | Assembles one module's variant with Gradle and returns the build record, as build_run does. |
| `build_errors` | core | read-only | Returns a finished build's merged errors (severity, file, line, message, problem id), and its warnings on request; buildId absent means the newest ... |
| `build_lastResult` | core | read-only | Returns the newest finished build of the workspace in build_run's shape, without running anything. |
| `build_run` | core | changes state | Runs 1 to 20 Gradle tasks and returns the finished build's compact record; a failed build is a completed result with outcome failed, never an error. |
| `build_tasks` |  | read-only | Lists the tasks that exist in the project, filtered by module, group or a name substring, from the cached project model (it never runs Gradle). |
| `device_inputText` | core | changes state | Types text into the focused field, or into target after tapping it once, and verifies the effect in the same call; clear empties the field first and ... |
| `device_install` | core | changes state | Installs a build's APK on the device with adb install -r, which keeps the app's data, and returns its applicationId and versionCode. |
| `device_launch` | core | changes state | Starts an application's launcher activity and returns launchedAt and the process id observed afterwards (null with a reason when no process appeared, ... |
| `device_list` | core | read-only | Lists the connected emulators and the physical devices the policy admits, with serial (the value of every device argument), state, kind and model ... |
| `device_logs` | core | read-only | Reads the device's log once as a snapshot, or follows it for followMs: structured lines, the app's crash blocks, how lines were selected, and a ... |
| `device_screenshot` | core | read-only | Captures the device's screen as a PNG and returns an ephemeral artifact reference with its size, not the bytes. |
| `device_tapElement` | core | changes state | Taps one UI element once and verifies the effect in the same call: it resolves target with the device_uiTree selector, taps it, then polls the ... |
| `device_uiTree` | core | read-only | Returns the current UI as a compact accessibility tree: snapshot id, foreground package, and a flat list of nodes with ref, class, id, text, desc and ... |
| `device_uninstall` |  | changes state | Uninstalls an application and reports whether it removed one; an app that is not installed completes with uninstalled false. |
| `emulator_list` | core | read-only | Lists every AVD the emulator launcher knows, with its configuration and, when it is running, its device serial. |
| `emulator_start` | core | changes state | Boots one AVD and returns {device, avd, adopted} once its device has finished booting. |
| `emulator_stop` | core | changes state | Stops one running emulator with adb emu kill and returns once its serial leaves adb devices. |
| `emulator_wipe` |  | changes state | Factory-resets one AVD's user data by booting it again with -wipe-data, stopping it first if it is running, and returns {device, avd} once it has ... |
| `file_copy` |  | changes state | Copies up to 1000 files or folders, a folder whole and a symlink as a link; nothing is replaced, and a failed or cancelled copy leaves nothing. |
| `file_create` |  | changes state | Creates an empty file or a folder in the project, making missing parent folders; it never replaces, and an existing path is FILE_EXISTS. |
| `file_delete` |  | changes state | Deletes up to 1000 files or folders into the workspace's trash, kept up to 7 days within 2 GiB per project; an item's trashId restores it with ... |
| `file_list` | core | read-only | Lists one level of a directory in the project, never recursively, with name, type, size and modification time per entry. |
| `file_move` |  | changes state | Moves or renames up to 1000 files or folders, a rename being a move inside one folder; nothing is replaced. |
| `file_read` | core | read-only | Reads a byte range of a text file in the project, paging as artifact_read does: text ends at a complete line, nextOffset continues, a negative offset ... |
| `file_restore` |  | changes state | Puts trashed items back at their paths, making missing folders and replacing nothing: a taken path is FILE_EXISTS, and an id this workspace's trash ... |
| `file_write` | core | changes state | Writes a whole UTF-8 text file (at most 512 KiB) into the project, atomically replacing any existing file and creating missing parent directories. |
| `operation_cancel` | core | changes state | Asks a running operation to stop, whichever session started it; cancelling a finished one does nothing. |
| `operation_wait` | core | read-only | Waits for an operation to finish, up to timeoutMs, and returns its state, progress and, once completed, its result. |
| `project_modules` | core | read-only | Lists the Gradle projects of the workspace's root (the root and container projects included) with path, name and directory, from one cached ... |
| `project_variants` | core | read-only | Reports the build variants of each module, or of one with module: source probe is exact, source task-names is inferred from the task names and exact ... |
| `sdk_requirements` |  | read-only | Reports what the project's build requires of the SDK (platforms and build-tools) against the SDK it would use, listing what is missing and the ... |
| `sdk_status` |  | read-only | Reports the host SDK: its root, where it was found, the candidates rejected before it and what is installed (platforms, build-tools, platform-tools, ... |
| `shell_exec` |  | changes state | Runs a shell command as the user with `/bin/sh -c` in the project root, or in cwd inside it; stdout and stderr are each the last 16 KiB, and a longer ... |
| `system_capabilities` |  | read-only | Lists every capability the core exposes, with its scope, statefulness, annotations and availability: the discovery entry point. |
| `system_health` |  | read-only | Reports whether the core is alive, its version, uptime and operations in flight, where adb and the JVM were found, any state file it could not use, ... |
| `workspace_close` |  | changes state | Closes the workspace for every session: cancels its running operations, waits up to 10 s for them, and stops its processes. |
| `workspace_describe` | core | read-only | Describes the open workspace: its id, root, state, whether it is trusted, and, when degraded, why. |
| `workspace_open` | core | changes state | Opens the workspace for a project root, or returns its state if it is already open. |
