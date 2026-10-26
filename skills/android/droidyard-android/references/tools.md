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
| `device_forceStop` |  | changes state | Force-stops an application, which ends its processes and loses its unsaved in-memory state, and reports whether it was running. |
| `device_inputText` | core | changes state | Types text into the focused field, or into target after tapping it once, and verifies the effect in the same call; clear empties the field first and ... |
| `device_install` | core | changes state | Installs a build's APK on the device with adb install -r, which keeps the app's data, and returns its applicationId and versionCode. |
| `device_launch` | core | changes state | Starts an application's launcher activity, or with url opens that link in the app, and returns launchedAt and the process id observed afterwards ... |
| `device_list` | core | read-only | Lists the connected emulators and the physical devices the policy admits, with serial (the value of every device argument), state, kind and model ... |
| `device_logs` | core | read-only | Reads the device's log once as a snapshot, or follows it for followMs: structured lines, the app's crash blocks, how lines were selected, and a ... |
| `device_pressKey` |  | changes state | Presses and releases a system key on the device, such as back, home, power or a volume key. |
| `device_recordUi` |  | changes state | Records the device's UI from now until device_stopUiRecording: the full dump (device_uiDump's document) each time the screen changes, with the events ... |
| `device_rotate` |  | changes state | Pins the screen to an angle with auto-rotate off, or turns auto-rotate back on, and returns the previous setting. |
| `device_screenshot` | core | read-only | Captures the device's screen as a PNG and returns an ephemeral artifact reference with its size, not the bytes. |
| `device_stopUiRecording` |  | changes state | Ends the device's UI recording after a last dump and returns its summary; stopped is false when none runs or it was already ending, and recording is ... |
| `device_swipe` |  | changes state | Drags a finger along a path of points, as fractions of the screen device_uiTree reports, replayed with the points' timing. |
| `device_systemPanel` |  | changes state | Opens the notification shade or quick settings, or closes the open panel, with the device's status-bar command. |
| `device_tap` |  | changes state | Touches the screen at a point, as a fraction of the screen device_uiTree reports, and lifts after durationMs (a long press). |
| `device_tapElement` | core | changes state | Taps one UI element once and verifies the effect in the same call: it resolves target with the device_uiTree selector, taps it, then polls the ... |
| `device_uiDump` |  | read-only | Returns the active window's full accessibility tree, uncut and without refs, as an XML artifact in uiautomator dump's format plus extra attributes, ... |
| `device_uiEvents` |  | read-only | Returns what happened on the screen since a cursor, oldest first: windows, content, focus, scrolls, clicks, toasts with their text and notifications ... |
| `device_uiRecordings` |  | read-only | Lists the UI recordings kept, newest first and at most 50, a running one with its live counts and growing timeline (save a timeline with droidyard ... |
| `device_uiTree` | core | read-only | Returns the current UI as a compact accessibility tree: snapshot id, foreground package, and a flat list of nodes with ref, class, id, text, desc and ... |
| `device_uninstall` |  | changes state | Uninstalls an application and reports whether it removed one; an app that is not installed completes with uninstalled false. |
| `emulator_create` |  | changes state | Creates one AVD from a device profile (sdk_profiles) and an installed system image (sdk_packages) with avdmanager, where the launcher lists it, with ... |
| `emulator_list` | core | read-only | Lists every AVD the emulator launcher knows, with its configuration and, when it is running, its device serial. |
| `emulator_start` | core | changes state | Boots one AVD and returns {device, avd, adopted} once its device has finished booting. |
| `emulator_stop` | core | changes state | Stops one running emulator with adb emu kill and returns once its serial leaves adb devices. |
| `emulator_wipe` |  | changes state | Factory-resets one AVD's user data by booting it again with -wipe-data, stopping it first if it is running, and returns {device, avd} once it has ... |
| `operation_cancel` | core | changes state | Asks a running operation of your session or chat to stop; cancelling a finished one does nothing. |
| `operation_wait` | core | read-only | Waits for an operation to finish, up to timeoutMs, and returns its state, progress and, once completed, its result. |
| `project_modules` | core | read-only | Lists the Gradle projects of the workspace's Gradle root (the root and container projects included) with path, name and directory, from one cached ... |
| `project_variants` | core | read-only | Reports the build variants of each module, or of one with module: source probe is exact, source task-names is inferred from the task names and exact ... |
| `sdk_install` |  | changes state | Installs SDK packages and their dependencies from Google's repository, dependencies first, updates an installed one that has a newer revision, and ... |
| `sdk_packages` |  | read-only | Lists SDK packages: those installed in the host SDK (size on disk, the newer revision available) and those Google's repository offers for this host ... |
| `sdk_profiles` |  | read-only | Lists the device profiles AVDs are created from (id, name, maker, kind); use an id as emulator_create's profile. |
| `sdk_requirements` |  | read-only | Reports what the project's build requires of the SDK (platforms and build-tools) against the SDK it would use, listing what is missing and, for each, ... |
| `sdk_status` |  | read-only | Reports the host SDK: its root, where it was found, the candidates rejected before it and what is installed (platforms, build-tools, platform-tools, ... |
| `shell_exec` |  | changes state | Runs a shell command as the user with `/bin/sh -c` in the project root, or in cwd inside it; stdout and stderr are each the last 16 KiB, and a longer ... |
| `system_capabilities` |  | read-only | Lists every capability the core exposes, with its scope, statefulness, annotations and availability: the discovery entry point. |
| `system_health` |  | read-only | Reports whether the core is alive, its version, uptime and operations in flight, where adb and the JVM were found, any state file it could not use, ... |
| `workspace_close` |  | changes state | Closes the workspace for every session: cancels its running operations, waits up to 10 s for them, and stops its processes. |
| `workspace_describe` | core | read-only | Describes the open workspace: its id, root, Gradle root, state, whether it is trusted, when degraded why, and the project's git worktrees with their ... |
| `workspace_enter` | core | changes state | Moves this session into a git worktree of its project, occupying it, or back to the project's root, which frees what it occupies: later calls act on ... |
| `workspace_open` | core | changes state | Opens the workspace for a project root, or returns its state if it is already open. |
