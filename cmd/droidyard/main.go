// Command droidyard is the Droidyard binary.
//
// `caps` and `schema` read the registry and the generated schema. `serve` runs the
// daemon on its socket; `call`, `mcp`, `status`, `stop` and `ui` talk to it, and
// `trust` writes the user configuration directly, with no daemon involved, and so does
// `init`, which writes the assistant integration into a project (ADR-0037). `hook`
// answers an assistant's hook event and dials the daemon only if one is running
// (ADR-0061). `trash` moves files to the trash through a running daemon and starts none
// (ADR-0062).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/agent"
	artifactcap "github.com/jtortosasen/droidyard/internal/domain/artifacts"
	builddomain "github.com/jtortosasen/droidyard/internal/domain/build"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen/scrcpyserver"
	"github.com/jtortosasen/droidyard/internal/domain/emulator"
	"github.com/jtortosasen/droidyard/internal/domain/file"
	gitdomain "github.com/jtortosasen/droidyard/internal/domain/git"
	"github.com/jtortosasen/droidyard/internal/domain/project"
	"github.com/jtortosasen/droidyard/internal/domain/sdk"
	"github.com/jtortosasen/droidyard/internal/domain/shell"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
	proc "github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/schema"
	"github.com/jtortosasen/droidyard/internal/store"
	"github.com/jtortosasen/droidyard/internal/workspace"
)

// build is the release's version, set at link time with -ldflags "-X main.build=vX.Y.Z"
// (ADR-0068 §2). A build made any other way is "dev". It is not the API version, which
// system.Version holds and which health, the lock and MCP serverInfo keep.
var build = "dev"

// versionLine is what `version` prints: the build, then the API version.
func versionLine(build, api string) string {
	return "droidyard " + build + " (API " + api + ")"
}

// versionCommand prints the one version line, or with --licenses the licence of the
// third-party software embedded in the binary (scrcpy's server, Apache-2.0). The bare
// command stays one line: install.sh parses it.
func versionCommand(w io.Writer, args []string) int {
	switch {
	case len(args) == 0:
		fmt.Fprintln(w, versionLine(build, system.Version))
	case len(args) == 1 && args[0] == "--licenses":
		fmt.Fprintf(w, "scrcpy server %s, embedded as the screen stream's source (sha256 %s).\n\n%s",
			scrcpyserver.Version, scrcpyserver.SHA256, scrcpyserver.License())
	default:
		fmt.Fprintln(os.Stderr, "usage: droidyard version [--licenses]")
		return 2
	}
	return 0
}

const usage = `Droidyard — a lean Android development runtime for humans and agents.

Usage:
  droidyard [<command>]

With no command, droidyard runs ui.

Commands:
  caps       List the capabilities this build exposes, with their annotations.
  schema     Print the generated JSON Schema that consumers are contracted to.
  version    Print the build version and the API version;
             --licenses prints the licence of the software embedded in the binary.
  serve      Run the core daemon in the foreground, until SIGTERM or SIGINT;
             --detach runs it in a session of its own, logging to daemon.log.
  call       Call one capability and print its response envelope:
             droidyard call <capability> [--workspace <id|root>] [--params <json>] [--detach] [--no-autostart]
             --workspace names the workspace; without it, a workspace call uses
             the project root of the current directory.
             Ctrl-C cancels the operation; --detach prints its id and returns.
             call and mcp start a daemon when none answers, unless --no-autostart.
  mcp        Attach an MCP stdio session to the daemon:
             droidyard mcp [--root <path>] [--wait <duration>] [--tools core|all] [--no-autostart]
             The session is bound to --root, else to the project root of the
             current directory, and opens it when it starts.
             --wait bounds how long a long-running tool waits (default 20s).
             --tools core lists the golden path's tools only (default all).
  status     Report on the running daemon: its socket, and the web UI's address
             and tokenised URL.
  stop       Stop the running daemon.
  ui         Open the web UI in the default browser, at the tokenised URL:
             droidyard ui [--no-autostart]
             In a project folder, or under one (a settings.gradle or settings.gradle.kts),
             or at the top of a repository whose Gradle build is one or two folders
             below, it opens that project; elsewhere, the newest open project, else the
             projects panel.
             ui starts a daemon when none answers, unless --no-autostart.
  trust      Grant a project root trust, so capabilities that run its code may:
             droidyard trust <root>
             It writes the user configuration; no daemon is involved or started.
  init       Set a project up for coding assistants (optional; every tool works without it):
             droidyard init --client cursor|claude|opencode|all [--root <path>] [--dry-run] [--uninstall]
             Writes the droidyard-android skill and, per client, the MCP server entry and
             an allowlist of the read-only tools, naming this binary by its absolute path;
             for claude and cursor also the entries that run droidyard hook, before a
             shell command and after one.
             It merges into existing files, keeps an existing droidyard entry, and refuses
             a file it cannot parse. --uninstall removes what init would write; --dry-run
             prints the changes and writes none.
             The root is --root, else the project ui would open from the current directory:
             the nearest folder at or above it with a settings.gradle(.kts), else the
             current folder when it is the top of a repository with one Gradle build below.
             No daemon is involved or started.
  hook       Answer one coding assistant's hook event (run by an assistant's hook entry):
             droidyard hook --client claude|cursor --root <absolute project root>
             Before a shell command, a simple rm inside the root is denied with the command
             that moves the files to droidyard's trash instead; anything else passes.
             After one, a gradlew, gradle, adb, emulator, sdkmanager or avdmanager command
             gets a hint naming the droidyard tool that does the same, when there is one, and is recorded in
             Activity if a daemon is running; nothing else is read or kept. It reads the
             event on stdin, answers within 2 s, never starts a daemon, deletes nothing,
             denies nothing after a command, and lets the command through whenever it
             cannot decide.
  trash      Move files to the project's trash, where they can be restored (what a denied
             rm is handed; usable by hand):
             droidyard trash [--state-dir <dir>] [--root <abs>] [-r|-R|-f ...] [--] <path>...
             Paths resolve against the current directory; the root is --root, else the
             project root of the current directory. Every path is checked before any moves.
             It needs a running daemon and never starts one. Exits 0 only when every path
             is trashed; a refusal prints the rm command that deletes without the trash.
`

func main() {
	// No subcommand is `ui`: a user types `droidyard` in a project folder (ADR-0045 §1).
	// The usage is `help`'s.
	if len(os.Args) < 2 {
		os.Exit(ui(nil))
	}

	cmd := os.Args[1]
	switch cmd {
	case "caps":
		if err := printCaps(); err != nil {
			fail(err)
		}
	case "schema":
		if err := printSchema(); err != nil {
			fail(err)
		}
	case "version":
		os.Exit(versionCommand(os.Stdout, os.Args[2:]))
	case "-h", "--help", "help":
		fmt.Print(usage)
	case "serve":
		os.Exit(serve(os.Args[2:]))
	case "call":
		os.Exit(call(os.Args[2:]))
	case "status":
		os.Exit(status())
	case "stop":
		os.Exit(stop())
	case "mcp":
		os.Exit(mcpShim(os.Args[2:]))
	case "ui":
		os.Exit(ui(os.Args[2:]))
	case "trust":
		os.Exit(trust(os.Args[2:]))
	case "init":
		os.Exit(initCmd(os.Args[2:], os.Stdout, os.Stderr))
	case "hook":
		os.Exit(hook(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "trash":
		os.Exit(trashCmd(os.Args[2:], os.Stdout, os.Stderr))
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

// requirements reports what is missing: adb through the device package, which
// resolves it afresh on every question, the emulator launcher through the emulator
// package (plan m1i, D19), gradle through the sidecar client, which
// answers from the JVM discovery result it cached at startup (plan m1g D22), and every
// other external tool as missing until its own check exists, with the reason, rather
// than the capability vanishing.
type requirements struct {
	adb      *device.ADB
	emulator *emulator.Emulator
	gradle   *gradle.Client
	// dialog answers folderDialog: workspace.chooseRoot's macOS folder dialog
	// (ADR-0045 §4).
	dialog workspace.Dialog
}

func (r requirements) Missing(req capability.Requirement) (string, bool) {
	switch req {
	case capability.RequiresADB:
		res := r.adb.Resolve()
		if res.Found {
			return "", false
		}
		return res.NotFound(), true
	case capability.RequiresEmulator:
		if r.emulator == nil {
			return "no emulator launcher subsystem is configured", true
		}
		res := r.emulator.Resolve()
		if res.Found {
			return "", false
		}
		return res.NotFound(), true
	case capability.RequiresGradle:
		if r.gradle == nil {
			return "no Gradle sidecar client is configured", true
		}
		return r.gradle.Missing(req)
	case capability.RequiresFolderDialog:
		return r.dialog.Missing()
	case capability.RequiresGit:
		// Looked up afresh, as git.read does, so the two cannot disagree (ADR-0066 §1).
		if _, err := exec.LookPath("git"); err != nil {
			return "git was not found on the PATH droidyard runs with (" + err.Error() + ")", true
		}
		return "", false
	default:
		return string(req) + " was not found on this machine", true
	}
}

// Seams for this package's tests only, set by TestMain in a re-executed test binary
// before it calls main (plan m1c step 7, R17). The shipped binary never sets them.
var (
	// testCapabilities registers test-only capabilities beside the shipped ones.
	testCapabilities func(reg *capability.Registry, sup *proc.Supervisor)
	// testDrain replaces workspace.close's drain bound; zero keeps the default.
	testDrain time.Duration
	// adbEnviron is the environment adb is resolved in and its clients run with: the
	// daemon's own. Tests replace it so none finds or runs this machine's adb or
	// reaches the shared server on 5037.
	adbEnviron = os.Environ
	// adbExec, when set, runs every adb client in this process instead of starting it
	// (device.NewADBWithExec): the fake adb of a test whose daemon runs in the test's
	// own process. The shipped binary never sets it.
	adbExec device.Exec
	// adbStreamWrap, when set, wraps the recorded starter of a Logcat stream: a test
	// holds a child's reaping to see what waits for it. The shipped binary never sets it.
	adbStreamWrap func(device.StreamStarter) device.StreamStarter
	// gradleEnviron is the environment the sidecar's JVM is discovered in and the
	// sidecar inherits: the daemon's own. Tests replace it with a fake JAVA_HOME so
	// discovery does not depend on this machine (plan m1g step 5).
	gradleEnviron = os.Environ
	// gradleFinder, when set, replaces discovery's host probes (the macOS
	// /usr/libexec/java_home and the Android Studio globs) for the environment it is
	// given, so a test's discovery is deterministic. Nil probes the host.
	gradleFinder func(env []string) *gradle.Finder
	// gradleLaunch, when set, starts the sidecar with this command instead of the
	// embedded jars on the discovered JVM: the fake sidecar of a test. Nil runs the
	// real one.
	gradleLaunch gradle.Launch
	// watchTick, when > 0, is the file.watch and git.watch pollers' interval for the daemons this
	// process starts: the in-process WebSocket test shortens it to 50 ms so a change is
	// seen at once (plan m1j step 4). Set under adbSeamMu, like adbEnviron.
	watchTick time.Duration
	// dialogSeam, when set, adjusts the folder dialog before the daemon uses it: a test
	// points Binary at a fake osascript and GOOS at darwin. The shipped binary never
	// sets it.
	dialogSeam func(d *workspace.Dialog)
	// sessionClock, when set, is the clock the session registry reads (a session's last
	// request and the idle sweep): a test advances it to make a holder idle. The shipped
	// binary never sets it.
	sessionClock func() time.Time
	// sessionSweep, when > 0, is how often the idle sweep runs, in place of the minute
	// (capability.SweepInterval). The shipped binary never sets it.
	sessionSweep time.Duration
)

// registry builds the shipped registry, the operation store that dispatches from it
// and the daemon's event bus, over the state store st and the supervisor sup, logging
// to log. internal/schema's test builds the same set for the golden file. caps and
// schema, which run no capability, pass nil for all three. The device ADB is returned
// so the daemon's shutdown can stop the UI readers and remove their forwards
// (plan m1f step 5).
//
// env is the daemon's environment: it decides where the state root and, with it, the
// user configuration live, so an in-process test daemon reads its own configuration
// and not this machine's (plan m1g step 3).
func registry(env platform.Env, st *store.Store, sup *proc.Supervisor, log io.Writer) (*capability.Registry, *capability.Operations, *event.Bus, *artifact.Store, *device.ADB, *file.Watchers) {
	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	// The daemon's one event bus (overview.md §Events).
	bus := event.New(event.Options{})
	ops.UsePublisher(bus)
	ops.UseStatePublisher(bus)
	var problems system.StateProblems
	if st != nil {
		problems = st.Problems
	}
	// The daemon's one policy: it decides trust, shell.exec and physical devices, reports
	// the configuration file's state, and reads the tool keys once, here, so they take
	// effect at this start and not before the next (ADR-0032 §3, §6; plan m1k D1, D8). A
	// daemon that reads no configuration still starts: a bad file is reported, not fatal.
	pol := policy.New(env)
	startup := pol.Startup()
	// The session registry reads the occupation section at each decision (ADR-0094 §7);
	// capability cannot import policy, so it is handed the reader here.
	ops.Sessions().UseOccupation(pol.Occupation)
	if sessionClock != nil {
		ops.Sessions().UseClock(sessionClock)
	}
	// Every adb client is started through the supervisor's locked spawner and given
	// the daemon's whole environment (plan m1e, R8). Without a supervisor (caps,
	// schema) adb can be resolved and reported but not run.
	var spawn device.Starter
	var recorded device.StreamStarter
	if sup != nil {
		spawn = sup.Spawn
		// A logcat follow's client is recorded for its life, so a daemon that dies
		// mid-window leaves it findable for the next start's reconciliation (plan m1i
		// D9). The supervisor's error is mapped to the device's, so the follow can tell
		// a record it could not write from a client it could not start.
		recorded = func(cmd *exec.Cmd, kind string) (device.Stream, error) {
			r, err := sup.SpawnRecorded(cmd, proc.Kind(kind))
			if errors.Is(err, proc.ErrUnrecordable) {
				return nil, fmt.Errorf("%w: %v", device.ErrStreamUnrecorded, err)
			}
			return r, err
		}
	}
	var adb *device.ADB
	if adbExec != nil {
		adb = device.NewADBWithExec(adbEnviron(), adbExec)
	} else {
		adb = device.NewADB(adbEnviron(), spawn)
		if recorded != nil && adbStreamWrap != nil {
			recorded = adbStreamWrap(recorded)
		}
		adb.SetStreamStarter(recorded)
	}
	// device.watchLogs publishes its streams to the daemon's bus (ADR-0041).
	adb.SetBus(bus)
	// `tools.adb` is the first adb candidate, fixed at this start (ADR-0032 §3, §6;
	// plan m1k D8).
	adb.SetToolKey(startup.ADBKey())
	// Reader restarts, fallbacks and slow warm reads go to daemon.log (plan m1n step 2).
	adb.SetLog(log)
	// The one Gradle sidecar client (ADR-0019): it discovers the JVM in the daemon's
	// environment, extracts the jars to the cache directory and supervises the shared
	// sidecar lazily (plan m1g step 5). It holds no process until a call needs one.
	// A sidecar crash degrades the workspace whose build was running, and the next
	// successful start returns it to ready (workspace-state.md §Lifecycle; plan m2c,
	// D21). The workspace registry is built below, before any call reaches the client.
	var ws *workspace.Registry
	gradleOpts := gradle.Options{
		OnCrash:    func(root string, e *capability.Error) { ws.Degrade(root, e) },
		OnRecover:  func() { ws.Recover(capability.ErrBuildSidecarCrashed) },
		Supervisor: sup, Log: log, Env: gradleEnviron(), Launch: gradleLaunch, JavaHome: startup.JavaHomeKey(), AllowBelowFloor: startup.BelowFloorKey()}
	if sup != nil {
		if cache, err := env.CacheDir(); err == nil {
			gradleOpts.CacheDir = cache
		}
	}
	if gradleFinder != nil {
		gradleOpts.Finder = gradleFinder(gradleEnviron())
	}
	gradleClient := gradle.New(gradleOpts)
	// The daemon's one artifact store, for the bus's epoch: a reference from an earlier
	// daemon run is ARTIFACT_EXPIRED (ADR-0012). Its state root is the daemon's, so a
	// durable build console survives a restart; without one (caps, schema) it serves
	// ephemeral references only. The caller closes it on shutdown.
	var stateRoot string
	if st != nil {
		stateRoot = st.Root()
	}
	// The emulator subsystem (plan m1i steps 4–5): it resolves the launcher in the
	// daemon's environment, shares adb for the device side, runs `-list-avds` through the
	// same locked spawner adb clients use, and boots detached under `emulator/<avd>.log`
	// in the daemon's state root. It owns no device logic (emulator package doc).
	emulatorClient := emulator.New(adbEnviron(), adb, spawn, stateRoot)
	// `tools.emulator` is the first launcher candidate and `tools.adb` the adb the
	// launcher derived from adb's location uses, both fixed at this start (ADR-0032 §3,
	// §6; plan m1k D8).
	emulatorClient.SetToolKeys(emulator.Keys{Launcher: startup.EmulatorKey(), ADB: startup.ADBKey()})
	if sup != nil {
		// A launch is pid-recorded from its spawn until its serial resolves, and a daemon
		// that dies in between has the record judged by its successor's reconcile: the
		// launch is ended only if adb proves it is not a device (plan m1p Q1).
		emulatorClient.SetLaunchRecorder(emulator.LaunchRecorder{Record: sup.RecordLaunch, Drop: sup.DropLaunch})
		sup.SetJudge(func(ctx context.Context, r proc.Record) (bool, string) {
			return emulatorClient.JudgeLaunch(ctx, r.AVD, r.AdbPort)
		})
	}
	policy.Register(reg, pol)
	// The device policy seam (plan m1k D7): the ADB consults the one Policy for a
	// physical device's admission, re-reading the configuration at each check, so a hand
	// edit decides the next call without a restart. The emulator subsystem shares this
	// ADB, so its policy check uses the same configuration.
	adb.SetPolicy(pol)
	// workspace.chooseRoot's folder dialog is started through the supervisor's recorded
	// spawner (ADR-0045 §4; ADR-0021 §Pid records), so a daemon killed while it is up leaves
	// a record the next start reaps; a test replaces the osascript it runs and the platform
	// it asks on. Without a supervisor (caps, schema) the dialog has no spawner.
	var dialogSpawn func(cmd *exec.Cmd) (func() error, error)
	if sup != nil {
		dialogSpawn = func(cmd *exec.Cmd) (func() error, error) {
			r, err := sup.SpawnRecorded(cmd, proc.FolderDialog)
			if err != nil {
				return nil, err
			}
			return r.Wait, nil
		}
	}
	dialog := workspace.NewDialog(dialogSpawn)
	if dialogSeam != nil {
		dialogSeam(&dialog)
	}
	system.Register(reg, requirements{adb: adb, emulator: emulatorClient, gradle: gradleClient, dialog: dialog}, adb, ops, problems, gradleClient, pol)
	artifacts, err := artifact.New(artifact.Options{Epoch: bus.Position().Epoch, StateRoot: stateRoot})
	if err != nil {
		panic(err) // the bus mints a well-formed epoch
	}
	// The daemon's one build history (plan m1g step 7): the records, their index and
	// the consoles they own. Without a state store it registers capabilities whose
	// handlers report that nothing is configured.
	history := builddomain.NewRegistry(builddomain.RegistryOptions{Store: st, Artifacts: artifacts, Log: log, Bus: bus})
	device.Register(reg, adb, artifacts, history)
	emulator.Register(reg, emulatorClient)
	artifactcap.Register(reg, artifacts)
	ops.RegisterCapabilities()
	var stopSidecars func(id string)
	if sup != nil {
		// workspace.close stops the workspace's sidecars after its drain and before it
		// marks the workspace closed (ADR-0027 §Decision).
		stopSidecars = func(id string) { sup.Stop(proc.WorkspaceScope(id)) }
	}
	// A closing workspace releases its root's Gradle connection, so Gradle's daemon
	// connection is not left to the sidecar's idle timeout (gradle.md §Protocol). Any
	// failure here is not the close's business: the sidecar is stopped with the daemon.
	closeRoot := func(root string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		gradleClient.Close(ctx, root)
	}
	// The daemon's live file.watch and git.watch pollers: a workspace close ends that
	// workspace's watches, each publishing its stopped event (plan m1j, D9; ADR-0066 §1).
	watchers := file.NewWatchers()
	gitWatchers := gitdomain.NewWatchers()
	// The trash of file.delete, under the state root, swept when a workspace opens beside
	// the build history's sweep (ADR-0054 §3). Without a state store there is none, and
	// file.delete and file.restore answer FILE_IO_FAILED.
	var trash *file.Trash
	if st != nil {
		trash = file.NewTrash(st)
	}
	ws = workspace.New(workspace.Options{Store: st, Ops: ops, Policy: pol, Log: log, Drain: testDrain,
		StopSidecars: stopSidecars, StopPollers: func(id string) { watchers.Stop(id); gitWatchers.Stop(id) }, CloseRoot: closeRoot, Publisher: bus, States: bus, Dialog: dialog,
		OnOpen: func(id, root string) { history.Sweep(id); trash.Sweep(id, root) }})
	// The trust hook: a call's root comes from its workspace, and an untrusted root
	// fails WORKSPACE_UNTRUSTED before the handler runs (plan m1g step 3, R4).
	authorize := pol.Authorize(func(call *capability.Call) (string, bool) {
		info, err := ws.Describe(call.Workspace)
		if err != nil {
			return "", false
		}
		return info.Root, true
	})
	// The Gradle capabilities run on the Gradle root, a directory inside the project root
	// (ADR-0075 §3): trust is decided on the project root first, before anything is
	// resolved or spawned, then the root is resolved again at every call, without a
	// cache, so a choice written to the project file takes effect on the next call. A
	// project with none fails PROJECT_NO_GRADLE_ROOT, the candidates in its details.
	authorizeGradle := func(ctx context.Context, call *capability.Call) (any, error) {
		v, err := authorize(ctx, call)
		if err != nil {
			return nil, err
		}
		projectRoot := v.(string)
		// Without candidates: a failure lists them anyway, and a valid key skips the scan.
		r := workspace.ResolveGradleRoot(projectRoot, false)
		// The workspace's current root, also when there is none, so a crash of this root
		// degrades it and its close releases the connection (ADR-0075 §8). A root it
		// switched away from that no other workspace remembers is closed once no operation
		// holds it (ADR-0092 §2), each in a goroutine of its own: the wait has no deadline.
		// A workspace that began closing since the trust check refuses the call.
		forgotten, err := ws.UseGradleRoot(call.Workspace, r.Abs)
		if err != nil {
			return nil, err
		}
		for _, g := range forgotten {
			// Asked again once the root's lock is held: a build can hold it for long, and a
			// workspace may have taken the root meanwhile.
			go gradleClient.CloseWhenFree(g, func() bool { return ws.GradleRootInUse(g) })
		}
		if r.Abs == "" {
			return nil, r.Err
		}
		return gradle.Roots{Project: projectRoot, Gradle: r.Abs}, nil
	}
	// sdk.status resolves the host SDK in the daemon's own environment and reads what is
	// installed from its package.xml files: no JVM, no sdkmanager, no requirement, and a
	// missing SDK is answered with found: false (plan m1h, D11, D13). sdk.requirements
	// fetches the project model through the same client and carries the trust hook, so
	// only a trusted root is answered (D12).
	sdk.Register(reg, sdk.Options{Env: adbEnviron(), Canonical: env.Canonical, Client: gradleClient, Authorize: authorizeGradle})
	project.Register(reg, gradleClient, authorizeGradle)
	builddomain.Register(reg, builddomain.Options{Client: gradleClient, History: history, Authorize: authorize, AuthorizeGradle: authorizeGradle})
	// file.* runs on trusted and untrusted roots alike and needs no trust hook: its
	// only registry read is the workspace's canonical root (ADR-0015 §1, §5; plan m1j D2).
	file.Register(reg, file.Options{
		Root: func(id string) (string, bool) {
			info, err := ws.Describe(id)
			if err != nil {
				return "", false
			}
			return info.Root, true
		},
		Env:        env,
		Bus:        bus,
		Watchers:   watchers,
		WriteLocks: file.NewWriteLocks(),
		Tick:       watchTick,
		Trash:      trash,
	})
	// git.read is the diff's HEAD side and git.watch the status (ADR-0066): audience
	// human, never an MCP tool. Their git is started through the supervisor's spawner,
	// in its own group, in the daemon's environment minus GIT_*, and the root's trust is
	// read at every call and every tick (ADR-0015 §2).
	gitdomain.Register(reg, gitdomain.Options{
		Root: func(id string) (string, bool) {
			info, err := ws.Describe(id)
			if err != nil {
				return "", false
			}
			return info.Root, true
		},
		Env:      env,
		Trusted:  pol.Trusted,
		Starter:  spawn,
		Bus:      bus,
		Watchers: gitWatchers,
		Tick:     watchTick,
		// The writes (ADR-0071): gated on the same trust hook as project code, and started
		// recorded, as shell.exec's commands are, so a killed daemon's next start reaps
		// their groups.
		Authorize: authorize,
		Recorded:  gitWriteStarter(sup),
	})
	workspace.Register(reg, ws)
	event.Register(reg, ops.Active)
	// agent.recordCommand, agent.recordEdit and agent.checkWrite are the hook's channel
	// (ADR-0063, ADR-0095 §3): audience human, stateless; a record is the operation's
	// result. The check reads who holds a worktree from the session registry, and a
	// recorded edit makes the file watches walk at once.
	agent.Register(reg, agent.Options{Sessions: ops.Sessions(), Canonical: env.Canonical, Nudge: watchers.Nudge})
	// shell.exec runs one command with `/bin/sh -c` in a trusted root the shell policy
	// admits (plan m1k D4–D6). Its gate is the shell policy first, then the same trust
	// hook the project-code capabilities share; its command is spawned through the
	// supervisor's recorded starter and its group killed at every end. Its children
	// inherit the daemon's own environment: not adbEnviron, which a test replaces with
	// an empty PATH for adb's sake (the shell then failed to find sleep, a flake that
	// showed under one parent shell only; m1k §Changes, step 2).
	shell.Register(reg, shell.Options{
		RootOf: func(call *capability.Call) (string, bool) {
			info, err := ws.Describe(call.Workspace)
			if err != nil {
				return "", false
			}
			return info.Root, true
		},
		ShellAllowed: pol.ShellAllowed,
		Authorize:    authorize,
		ResolveDir: func(root, path string) (string, error) {
			return file.ResolveDir(env, root, path)
		},
		Recorded:  shellStarter(sup),
		Env:       os.Environ(),
		Artifacts: artifacts,
	})
	if testCapabilities != nil {
		testCapabilities(reg, sup)
	}
	return reg, ops, bus, artifacts, adb, watchers
}

// shellStarter adapts the supervisor's recorded spawner to shell.exec's starter: the
// command is recorded while it runs and its whole group is killed if the record cannot
// be written (plan m1k D5). Without a supervisor (caps, schema) it is nil, and a call
// that somehow reached the handler would fail SHELL_EXEC_FAILED.
func shellStarter(sup *proc.Supervisor) shell.Starter {
	if sup == nil {
		return nil
	}
	return func(cmd *exec.Cmd, kind string) (shell.Recorded, error) {
		return sup.SpawnRecorded(cmd, proc.Kind(kind))
	}
}

// gitWriteStarter adapts the supervisor's recorded spawner to the git writes' starter: the
// git is recorded while it runs, as its own session and group leader, and its whole group
// is killed if the record cannot be written (process.GitWrite). Without a supervisor (caps,
// schema) it is nil, and a write that somehow reached the handler would fail GIT_FAILED.
func gitWriteStarter(sup *proc.Supervisor) gitdomain.RecordedStarter {
	if sup == nil {
		return nil
	}
	return func(cmd *exec.Cmd, kind string) (gitdomain.Recorded, error) {
		r, err := sup.SpawnRecorded(cmd, proc.Kind(kind))
		if err != nil {
			return nil, err // a nil interface, not a typed nil
		}
		return r, nil
	}
}

func printCaps() error {
	reg, _, _, _, _, _ := registry(platform.Current(), nil, nil, nil)
	for _, d := range reg.All() {
		a := d.ResolveAnnotations()

		var hints []string
		for _, h := range []struct {
			on   bool
			name string
		}{
			{a.ReadOnly, "read-only"},
			{a.Destructive, "destructive"},
			{a.Idempotent, "idempotent"},
			{a.OpenWorld, "open-world"},
		} {
			if h.on {
				hints = append(hints, h.name)
			}
		}

		var attrs []string
		attrs = append(attrs, d.Kind.String(), d.Scope.String(), d.Statefulness.String())
		if d.LongRunning {
			attrs = append(attrs, "long-running")
		}
		if d.Audience == capability.AudienceHuman {
			attrs = append(attrs, "human-only")
		}
		sort.Strings(attrs)

		fmt.Printf("%-24s %s\n", d.Name, strings.Join(attrs, ", "))
		fmt.Printf("%-24s   %s\n", "", strings.Join(hints, ", "))
		fmt.Printf("%-24s   %s\n", "", d.Description)
	}
	return nil
}

func printSchema() error {
	reg, _, _, _, _, _ := registry(platform.Current(), nil, nil, nil)
	doc, err := schema.Generate(reg, system.Version)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "droidyard:", err)
	os.Exit(1)
}
