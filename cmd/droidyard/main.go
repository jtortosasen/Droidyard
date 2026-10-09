// Command droidyard is the Droidyard binary.
//
// `caps` and `schema` read the registry and the generated schema. `serve` runs the
// daemon on its socket; `call`, `mcp`, `status`, `stop` and `ui` talk to it, and
// `trust` writes the user configuration directly, with no daemon involved, and so does
// `init`, which writes the assistant integration into a project (ADR-0037).
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
	"github.com/jtortosasen/droidyard/internal/domain/build"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/emulator"
	"github.com/jtortosasen/droidyard/internal/domain/file"
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

const usage = `Droidyard — a lean Android development runtime for humans and agents.

Usage:
  droidyard <command>

Commands:
  caps       List the capabilities this build exposes, with their annotations.
  schema     Print the generated JSON Schema that consumers are contracted to.
  version    Print the API version.
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
             ui starts a daemon when none answers, unless --no-autostart.
  trust      Grant a project root trust, so capabilities that run its code may:
             droidyard trust <root>
             It writes the user configuration; no daemon is involved or started.
  init       Set a project up for coding assistants (optional; every tool works without it):
             droidyard init --client cursor|claude|opencode|all [--root <path>] [--dry-run] [--uninstall]
             Writes the droidyard-android skill and, per client, the MCP server entry and
             an allowlist of the read-only tools, naming this binary by its absolute path.
             It merges into existing files, keeps an existing droidyard entry, and refuses
             a file it cannot parse. --uninstall removes what init would write; --dry-run
             prints the changes and writes none.
             The root is --root, else the project root of the current directory.
             No daemon is involved or started.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
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
		fmt.Println(system.Version)
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
	// watchTick, when > 0, is the file.watch poller's interval for the daemons this
	// process starts: the in-process WebSocket test shortens it to 50 ms so a change is
	// seen at once (plan m1j step 4). Set under adbSeamMu, like adbEnviron.
	watchTick time.Duration
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
	gradleOpts := gradle.Options{Supervisor: sup, Log: log, Env: gradleEnviron(), Launch: gradleLaunch, JavaHome: startup.JavaHomeKey(), AllowBelowFloor: startup.BelowFloorKey()}
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
	system.Register(reg, requirements{adb: adb, emulator: emulatorClient, gradle: gradleClient}, adb, ops, problems, gradleClient, pol)
	artifacts, err := artifact.New(artifact.Options{Epoch: bus.Position().Epoch, StateRoot: stateRoot})
	if err != nil {
		panic(err) // the bus mints a well-formed epoch
	}
	// The daemon's one build history (plan m1g step 7): the records, their index and
	// the consoles they own. Without a state store it registers capabilities whose
	// handlers report that nothing is configured.
	history := build.NewRegistry(build.RegistryOptions{Store: st, Artifacts: artifacts, Log: log})
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
	// The daemon's live file.watch pollers: a workspace close ends that workspace's
	// watches, each publishing its stopped event (plan m1j, D9).
	watchers := file.NewWatchers()
	ws := workspace.New(workspace.Options{Store: st, Ops: ops, Policy: pol, Log: log, Drain: testDrain,
		StopSidecars: stopSidecars, StopPollers: watchers.Stop, CloseRoot: closeRoot, Publisher: bus,
		OnOpen: func(id, _ string) { history.Sweep(id) }})
	// The trust hook: a call's root comes from its workspace, and an untrusted root
	// fails WORKSPACE_UNTRUSTED before the handler runs (plan m1g step 3, R4).
	authorize := pol.Authorize(func(call *capability.Call) (string, bool) {
		info, err := ws.Describe(call.Workspace)
		if err != nil {
			return "", false
		}
		return info.Root, true
	})
	// sdk.status resolves the host SDK in the daemon's own environment and reads what is
	// installed from its package.xml files: no JVM, no sdkmanager, no requirement, and a
	// missing SDK is answered with found: false (plan m1h, D11, D13). sdk.requirements
	// fetches the project model through the same client and carries the trust hook, so
	// only a trusted root is answered (D12).
	sdk.Register(reg, sdk.Options{Env: adbEnviron(), Canonical: env.Canonical, Client: gradleClient, Authorize: authorize})
	project.Register(reg, gradleClient, authorize)
	build.Register(reg, build.Options{Client: gradleClient, History: history, Authorize: authorize})
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
		Env:      env,
		Bus:      bus,
		Watchers: watchers,
		Tick:     watchTick,
	})
	workspace.Register(reg, ws)
	event.Register(reg, ops.Active)
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
