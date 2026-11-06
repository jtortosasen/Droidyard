// Command droidyard is the Droidyard binary.
//
// `caps` and `schema` read the registry and the generated schema. `serve` runs the
// daemon on its socket; `call`, `mcp`, `status` and `stop` talk to it, and
// `trust` writes the user configuration directly, with no daemon involved, and so do
// `host` and `remote` (the peer pairing and lending, ADR-0098 §7; `id` asks the daemon
// for its fingerprint) and `init`, which writes the assistant integration into a project (ADR-0037). `hook`
// passes an assistant's hook event and does nothing else: it stays one minor release for
// the entries an earlier init wrote (ADR-0137, plan m3j Q4).
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
	artifactcap "github.com/jtortosasen/droidyard/internal/domain/artifacts"
	builddomain "github.com/jtortosasen/droidyard/internal/domain/build"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen/scrcpyserver"
	"github.com/jtortosasen/droidyard/internal/domain/emulator"
	"github.com/jtortosasen/droidyard/internal/domain/project"
	"github.com/jtortosasen/droidyard/internal/domain/sdk"
	"github.com/jtortosasen/droidyard/internal/domain/sharing"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/hosting"
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

// main resolves the command line through the command tree (commands.go, ADR-0122): a hand
// command, a capability as `<domain> <verb>`, or help. No subcommand is the help.
func main() {
	ct, err := newCmdTree()
	if err != nil {
		fail(err)
	}
	if len(os.Args) < 2 {
		os.Exit(ct.help(nil, os.Stdout, os.Stderr))
	}
	if a := os.Args[1]; a == "-h" || a == "--help" {
		os.Exit(ct.help(nil, os.Stdout, os.Stderr))
	}
	os.Exit(ct.dispatch(os.Args[1:], os.Stdout, os.Stderr))
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
	default:
		return string(req) + " was not found on this machine", true
	}
}

// Seams for this package's tests only, set by TestMain in a re-executed test binary
// before it calls main (plan m1c step 7, R17). The shipped binary never sets them.
var (
	// testCapabilities registers test-only capabilities beside the shipped ones.
	testCapabilities func(reg *capability.Registry, sup *proc.Supervisor)
	// testHosting registers test-only capabilities that reach the peer link, which the
	// shipped binary reaches through no capability until a later step relays a device.
	testHosting func(reg *capability.Registry, host *hosting.Host)
	// joinSeam, when set, stands in for the host a hosting.join dials, so a test can have a host
	// whose answer is hostile. The shipped binary never sets it.
	joinSeam sharing.JoinFunc
	// testDrain replaces the drain bound an idle release waits for its workspace's operations;
	// zero keeps the default.
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
	// adbFloor, when positive, is the shortest limit any adb command of the daemon gets
	// (device.ADB.LengthenLimits): a test whose claim is not one of those limits, and whose
	// fake adb a loaded host starts late. The shipped binary never sets it.
	adbFloor time.Duration
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
	// sessionClock, when set, is the clock the session registry reads (a session's last
	// request and the idle sweep): a test advances it to make a holder idle. The shipped
	// binary never sets it.
	sessionClock func() time.Time
	// sessionSweep, when > 0, is how often the idle sweep runs, in place of the minute
	// (capability.SweepInterval). The shipped binary never sets it.
	sessionSweep time.Duration
	// workspaceClock, when set, is the idle clock of the workspace registry, and
	// workspaceSweep, when > 0, the interval of its idle sweep: a test moves the clock past
	// workspace.IdleAfter to release a root (ADR-0137 §3). The shipped binary never sets them.
	workspaceClock func() time.Time
	workspaceSweep time.Duration
)

// registry builds the shipped registry, the operation store that dispatches from it
// and the daemon's event bus, over the state store st and the supervisor sup, logging
// to log. internal/schema's test builds the same set for the golden file. caps and
// schema, which run no capability, pass nil for all three. The device ADB is returned
// so the daemon's shutdown can stop the UI readers and remove their forwards
// (plan m1f step 5); the peer link (ADR-0098) so serve can start it once it holds the lock.
//
// env is the daemon's environment: it decides where the state root and, with it, the
// user configuration live, so an in-process test daemon reads its own configuration
// and not this machine's (plan m1g step 3).
func registry(env platform.Env, st *store.Store, sup *proc.Supervisor, log io.Writer) (*capability.Registry, *capability.Operations, *event.Bus, *artifact.Store, *device.ADB, *hosting.Host, *workspace.Registry) {
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
	// The daemon's one policy: it decides trust and physical devices, reports
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
	adb.LengthenLimits(adbFloor)
	// device.watchLogs publishes its streams to the daemon's bus (ADR-0041).
	adb.SetBus(bus)
	// The live view's gestures are recorded as operations of the capability that replays
	// them (ADR-0117 §6).
	adb.SetRecorder(ops)
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
		InUse:      func(root string) bool { return ws.GradleRootInUse(root) },
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
		// The sockets the reader and the screen server are forwarded to (ADR-0102): in the
		// state root's run directory, within sun_path as the daemon's socket is.
		adb.SetForwardDir(env.ForwardDir(stateRoot))
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
	// avdmanager starts in one place (plan m3f step 6; ADR-0130 §5): the runner sdk.profiles,
	// emulator.create and emulator.delete share. It runs on the sidecar's JVM and through the
	// supervisor's recorded spawner, so a run is pid-recorded as kind sdk-tool and a daemon
	// killed during one has its group ended by the next start's reconcile.
	sdkTool := sdk.NewTool(sdk.ToolOptions{Env: adbEnviron(), JVM: gradleClient.JVM, Start: sdkToolStarter(sup)})
	emulatorClient.SetTool(sdkTool)
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
	// hosting.lend, read live as the policy is, says which of this machine's devices a
	// paired peer may use (ADR-0099 §1).
	adb.SetLending(pol)
	// Who occupies which device (ADR-0094): the one registry of MCP sessions, so a session that
	// ends frees the devices it held.
	adb.SetOccupation(ops.Sessions())
	// The peer link (ADR-0098): the daemon's identity, the links it dials and what its
	// listener reports. Nothing is read or written until serve starts it, once the daemon
	// holds the lock, since the identity has one writer. Without a state store (caps,
	// schema) there is none.
	var host *hosting.Host
	var hostHealth system.Hosting
	if st != nil {
		host = hosting.New(pol, stateRoot, system.Version, log)
		hostHealth = host
		// What the host lends is hosting.lend and its foreground shares, which live in the
		// host (ADR-0108 §3); both are read live at each call.
		adb.SetLending(host)
		// A source a peer marked fast starts gets the lighter preset (ADR-0118 §4).
		adb.SetPeerFast(host.PeerFast)
		// A foreground share belongs to the native connection that made it: when that connection
		// ends, however it ends, the share does, the listeners are matched to what is left
		// and the streams its peers held are stopped at once, not at the next sweep.
		ops.OnDisconnect(func(originID string) {
			if len(host.EndForegroundOwnedBy(originID, hosting.ShareConnection)) > 0 {
				host.Reconcile()
				adb.SweepLeases()
			}
		})
		// The hosts this daemon borrows devices from: <remote>/<serial> is relayed over
		// their links (ADR-0100). The links exist once serve starts them.
		adb.SetRemotes(host)
		// The UI recordings (ADR-0113) live in the state directory; their recovery waits for
		// the lock (daemon.Options.AfterLock runs adb.RecoverRecordings, before anything is
		// served), and a recorder's start runs it first.
		adb.SetRecordings(device.NewUIRecordings(st))
		// The APKs the daemon's peers upload, installed by device.installUpload (ADR-0099 §3).
		adb.SetUploads(host.Uploads())
		base := problems
		problems = func() []store.Problem { return append(base(), host.Problems()...) }
	}
	system.Register(reg, requirements{adb: adb, emulator: emulatorClient, gradle: gradleClient}, adb, ops, problems, gradleClient, pol, hostHealth)
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
	// hosting.share, hosting.unshare and hosting.join: the human's commands that pair two
	// daemons and write the configuration for it (ADR-0107, ADR-0108). Without a state store
	// (caps, schema) there is no host and they answer HOSTING_UNAVAILABLE.
	sharing.Register(reg, sharing.Options{Host: host, Devices: adb, API: system.Version, Join: joinSeam, Log: log, Sweep: adb.SweepLeases})
	artifactcap.Register(reg, artifacts)
	ops.RegisterCapabilities()
	var stopSidecars func(id string)
	if sup != nil {
		// A released workspace's sidecars are stopped after its drain and before it is
		// marked closed (ADR-0027 §Decision).
		stopSidecars = func(id string) { sup.Stop(proc.WorkspaceScope(id)) }
	}
	// A released workspace gives up its root's Gradle connection, so Gradle's daemon
	// connection is not left to the sidecar's idle timeout (gradle.md §Protocol). Any
	// failure here is not the release's business: the sidecar is stopped with the daemon.
	closeRoot := func(root string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		gradleClient.Close(ctx, root)
	}
	ws = workspace.New(workspace.Options{Store: st, Ops: ops, Log: log, Drain: testDrain,
		StopSidecars: stopSidecars, CloseRoot: closeRoot, Publisher: bus,
		OnOpen: func(id, root string) { history.Sweep(id) }})
	if workspaceClock != nil {
		ws.UseClock(workspaceClock)
	}
	if workspaceSweep > 0 {
		ws.UseIdleEvery(workspaceSweep)
	}
	// The trust hook: a call's root is the project root its `project` resolved to at Start
	// (Registry.Enter, workspace.ResolveProject), never the path the caller passed: a folder
	// inside a linked worktree is judged by the worktree's root, and so inherits its
	// project's trust (ADR-0081). An untrusted root fails WORKSPACE_UNTRUSTED before the
	// handler runs (plan m1g step 3, R4).
	authorize := pol.Authorize(func(call *capability.Call) (string, bool) {
		return call.Root, call.Root != ""
	})
	// The Gradle capabilities run on the Gradle root, a directory inside the project root
	// (ADR-0075 §3), resolved at Start with the project root (call.GradleRoot, the key the
	// build lock is held on): trust is decided on the resolved project root before anything
	// is spawned. A project with no Gradle root, or several, was refused at Start
	// (PROJECT_NO_GRADLE_ROOT, the candidates in its details).
	authorizeGradle := func(ctx context.Context, call *capability.Call) (any, error) {
		v, err := authorize(ctx, call)
		if err != nil {
			return nil, err
		}
		projectRoot := v.(string)
		// The workspace's current root, so a crash of this root degrades it and its release
		// closes the connection (ADR-0075 §8). A root it switched away from that no other
		// workspace remembers is closed once no operation holds it (ADR-0092 §2), each in a
		// goroutine of its own: the wait has no deadline. A workspace that began closing since
		// the trust check refuses the call.
		forgotten, err := ws.UseGradleRoot(call.Workspace, call.GradleRoot)
		if err != nil {
			return nil, err
		}
		for _, g := range forgotten {
			// Asked again once the root's lock is held: a build can hold it for long, and a
			// workspace may have taken the root meanwhile.
			go gradleClient.CloseWhenFree(g, func() bool { return ws.GradleRootInUse(g) })
		}
		return gradle.Roots{Project: projectRoot, Gradle: call.GradleRoot}, nil
	}
	// sdk.status resolves the host SDK in the daemon's own environment and reads what is
	// installed from its package.xml files: no JVM, no sdkmanager, no requirement, and a
	// missing SDK is answered with found: false (plan m1h, D11, D13). sdk.requirements
	// fetches the project model through the same client and carries the trust hook, so
	// only a trusted root is answered (D12).
	sdk.Register(reg, sdk.Options{Env: adbEnviron(), Canonical: env.Canonical, Client: gradleClient, Authorize: authorizeGradle, Log: log, Tool: sdkTool,
		// Before an update or an uninstall replaces a package: A10's rule over this host's
		// processes, lsof and the operations in flight (plan m3f step 5).
		InUse: sdk.SystemInUse(ops.Active)})
	project.Register(reg, gradleClient, authorizeGradle)
	builddomain.Register(reg, builddomain.Options{Client: gradleClient, History: history, Authorize: authorize, AuthorizeGradle: authorizeGradle})
	event.Register(reg, ops.Active)
	if testCapabilities != nil {
		testCapabilities(reg, sup)
	}
	if testHosting != nil && host != nil {
		testHosting(reg, host)
	}
	return reg, ops, bus, artifacts, adb, host, ws
}

// sdkToolStarter adapts the supervisor's recorded spawner to the avdmanager runner's
// starter: the run is recorded while it lasts, as its own session and group leader, and its
// whole group is killed if the record cannot be written (process.SDKTool). Without a
// supervisor (caps, schema) it is nil, and a run that somehow reached the handler would fail
// SDK_TOOL_FAILED.
func sdkToolStarter(sup *proc.Supervisor) sdk.RecordedStarter {
	if sup == nil {
		return nil
	}
	return func(cmd *exec.Cmd, kind string) (sdk.Recorded, error) {
		r, err := sup.SpawnRecorded(cmd, proc.Kind(kind))
		if err != nil {
			return nil, err // a nil interface, not a typed nil
		}
		return r, nil
	}
}

func printCaps() error {
	reg, _, _, _, _, _, _ := registry(platform.Current(), nil, nil, nil)
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
	reg, _, _, _, _, _, _ := registry(platform.Current(), nil, nil, nil)
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
