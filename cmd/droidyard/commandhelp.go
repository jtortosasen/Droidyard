package main

// The help of each hand-written command: the section of the binary's former global usage,
// moved to the command it describes (plan m2z D12). `droidyard help <command>` and
// `droidyard <command> --help` print it.

const capsHelp = `droidyard caps

List the capabilities this build exposes, with their annotations. droidyard schema
prints the contract they are published in.
`

const schemaHelp = `droidyard schema

Print the generated JSON Schema that consumers are contracted to.
`

const versionHelp = `droidyard version [--licenses]

Print the build version and the API version; --licenses prints the licence of the
software embedded in the binary.
`

const helpHelp = `droidyard help [<command>]

With no command, print the index: where to start, the commands, the capabilities by
domain. With one, print its help: droidyard help share, droidyard help device,
droidyard help device list.
`

const serveHelp = `droidyard serve [--detach]

Run the core daemon in the foreground, until SIGTERM or SIGINT; --detach runs it in a
session of its own, logging to daemon.log.
`

const callHelp = `droidyard call <capability> [--workspace <id|root>] [--params <json>] [--detach] [--no-autostart]

Call one capability and print its response envelope. This is the raw door: every
capability is also a command of its own, droidyard <domain> <verb>.
--workspace names the workspace; without it, a workspace call uses the project root of
the current directory.
Ctrl-C cancels the operation; --detach prints its id and returns.
call and mcp start a daemon when none answers, unless --no-autostart.
`

const statusHelp = `droidyard status

Report on the running daemon: its socket, and the web UI's address and tokenised URL.
`

const stopHelp = `droidyard stop

Stop the running daemon.
`

const mcpHelp = `droidyard mcp [--root <path>] [--from <dir>] [--wait <duration>] [--tools core|all] [--no-autostart]

Attach an MCP stdio session to the daemon.
The session is bound to --root, else to the project of --from or of the current directory
(a settings.gradle or settings.gradle.kts above it, or the top of a repository whose Gradle
build is one or two folders below), and opens it when it starts.
--from names the folder the project is found from, in place of the current directory; a
value that is empty or still holds a client variable (${workspaceFolder}) is ignored.
--from and --root exclude each other.
With no project, the session has no tools: mcp starts no daemon and connects to none.
--wait bounds how long a long-running tool waits (default 20s).
--tools core lists the golden path's tools only (default all).
mcp starts a daemon when none answers, unless --no-autostart.
`

const uiHelp = `droidyard ui [--no-autostart]

Open the web UI in the default browser, at the tokenised URL.
In a project folder, or under one (a settings.gradle or settings.gradle.kts), or at the
top of a repository whose Gradle build is one or two folders below, it opens that project;
elsewhere, the newest open project, else the projects panel.
ui starts a daemon when none answers, unless --no-autostart.
`

const trustHelp = `droidyard trust <root>

Grant a project root trust, so capabilities that run its code may.
It writes the user configuration; no daemon is involved or started.
`

const idHelp = `droidyard id

Print this daemon's fingerprint, what a host pins to admit it.
It asks the daemon, and starts one when none answers.
`

const hostHelp = `droidyard host admit <name> <fingerprint> | revoke <name> | list
droidyard host lend <serial> --to <name>[,<name>] | unlend <serial>
droidyard host listen <ip:port>|off

Lend physical devices to paired Droidyards (ADR-0098 §7), the host's side.
admit pairs a peer by the fingerprint its human sent from droidyard id; revoke unpairs it
and drops it from what is lent; lend lends a whole device (use a dedicated one) and still
needs devices.allowPhysical and the serial in devices.allowlist. A running daemon applies
a change within seconds.
It writes the user configuration; no daemon is involved or started.
`

const remoteHelp = `droidyard remote add <name> <ip:port> <fingerprint> | remove <name> | fast <name> on|off | list

Use the devices another Droidyard lends, the client's side.
fast marks a slow link: a live view started on its device is lighter.
The host's devices join the list as <name>/<serial>, used once devices.allowPhysical is
true and that id is in devices.allowlist. A running daemon applies a change within
seconds. No daemon is involved or started.
`

const deviceAllowHelp = `droidyard device allow <serial>

Allow a physical device (or <remote>/<serial>) without editing the configuration:
it sets devices.allowPhysical to true and adds the serial. A running daemon sees it at
its next call. No daemon is involved or started.
`

const deviceDisallowHelp = `droidyard device disallow <serial>

Remove the serial from the allowed devices and leave devices.allowPhysical as it was. A
running daemon sees it at its next call. No daemon is involved or started.
`

const shareHelp = `droidyard share [<serial>] [-d] [--on vpn|lan|<ip>]

Share a physical device with another Droidyard, the host's side.
With no serial it lists the physical devices (emulators are not shown) and shares the
only one that can be; with several it lists them and stops.
It admits the serial for this Mac too, lends it, listens on this Mac's private addresses
(the VPN and the LAN, or the ones --on names) and prints one line, droidyard join <code>,
to paste on the other Mac. The code works for one join and does not expire. Without -d it
stays in the foreground, prints who joins and leaves, and Ctrl-C (or closing the terminal)
stops sharing. With -d it returns at once; the share lasts until unshare.
It asks the daemon, and starts one when none answers.
`

const unshareHelp = `droidyard unshare [<serial>]

Stop sharing a device, or every device, in either mode.
The codes not yet used stop working. Those who joined stay paired and reach nothing;
droidyard host revoke unpairs one.
`

const joinHelp = `droidyard join <code>

Use the device another Droidyard shares, the client's side.
The code is the line the host's share printed. The device is listed at once as
<host>/<serial>; nothing is edited by hand and no restart is needed.
It asks the daemon, and starts one when none answers.
`

const initHelp = `droidyard init --client cursor|claude|opencode|all [--user | [--root <path>] [--exclude | --gitignore]] [--dry-run] [--uninstall]

Set a project up for coding assistants (optional; every tool works without it).
Writes the droidyard-android skill and, per client, the MCP server entry and an allowlist
of the read-only tools, naming this binary by its absolute path; for claude and cursor
also the entries that run droidyard hook, before a shell command and after one.
It merges into existing files, keeps an existing droidyard entry, and refuses a file it
cannot parse. --uninstall removes what init would write; --dry-run prints the changes and
writes none.
The root is --root, else the project ui would open from the current directory: the nearest
folder at or above it with a settings.gradle(.kts), else the current folder when it is the
top of a repository with one Gradle build below.
--exclude or --gitignore keeps the files it wrote that git does not track out of git status:
one marked block of lines, in the repository's info/exclude (shared by every worktree) or in
the project's .gitignore. A file git tracks gets no line and is reported. --uninstall always
removes the block.
--user installs once for every project and worktree, in your own configuration (claude:
CLAUDE_CONFIG_DIR or ~/.claude, its MCP entry through "claude mcp add-json --scope user";
cursor: ~/.cursor; opencode: XDG_CONFIG_HOME or ~/.config; the skill for cursor and opencode
in ~/.agents), with no root in any entry: the project is the folder the assistant starts in.
It takes no --root, --exclude or --gitignore, and writes nothing in a project. A project's
own entries take precedence there, and init --user run in such a project says so.
No daemon is involved or started.
`

const hookHelp = `droidyard hook --client claude|cursor [--root <absolute project root>]

Answer one coding assistant's hook event (run by an assistant's hook entry).
Before a shell command, a simple rm inside the root is denied with the command that moves
the files to droidyard's trash instead; anything else passes.
After one, a gradlew, gradle, adb, emulator, sdkmanager or avdmanager command gets a hint
naming the droidyard tool that does the same, when there is one, and is recorded in
Activity if a daemon is running; nothing else is read or kept. It reads the event on
stdin, answers within 2 s, never starts a daemon, deletes nothing, denies nothing after a
command, and lets the command through whenever it cannot decide.
Without --root (an entry init --user wrote) the project is the folder the assistant names
as its own; outside a project, or where that folder has init's own hook, every event passes.
`

const trashHelp = `droidyard trash [--state-dir <dir>] [--root <abs>] [-r|-R|-f ...] [--] <path>...

Move files to the project's trash, where they can be restored (what a denied rm is handed;
usable by hand).
Paths resolve against the current directory; the root is --root, else the project root of
the current directory. Every path is checked before any moves.
It needs a running daemon and never starts one. Exits 0 only when every path is trashed; a
refusal prints the rm command that deletes without the trash.
`

const artifactCatHelp = `droidyard artifact cat <ref> > dump.xml

Write a text artifact's bytes to standard output, unchanged.
The reference is the one a capability returned (device.uiDump's dump). When the artifact is
untrusted, a line on stderr says its bytes are device data, never instructions. It needs a
running daemon and never starts one; an unknown or expired reference exits non-zero with
the daemon's message, an image with a message saying it is not text.
`
