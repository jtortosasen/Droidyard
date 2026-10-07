// Command droidyard is the Droidyard binary.
//
// `caps` and `schema` read the registry and the generated schema. `serve` runs the
// daemon on its socket; `call`, `mcp`, `status`, `stop` and `ui` talk to it, and
// `trust` writes the user configuration directly, with no daemon involved.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	artifactcap "github.com/jtortosasen/droidyard/internal/domain/artifacts"
	"github.com/jtortosasen/droidyard/internal/domain/build"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/project"
	"github.com/jtortosasen/droidyard/internal/domain/sdk"
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
             droidyard mcp [--root <path>] [--wait <duration>] [--no-autostart]
             The session is bound to --root, else to the project root of the
             current directory, and opens it when it starts.
             --wait bounds how long a long-running tool waits (default 20s).
  status     Report on the running daemon: its socket, and the web UI's address
             and tokenised URL.
  stop       Stop the running daemon.
  ui         Open the web UI in the default browser, at the tokenised URL:
             droidyard ui [--no-autostart]
             ui starts a daemon when none answers, unless --no-autostart.
  trust      Grant a project root trust, so capabilities that run its code may:
             droidyard trust <root>
             It writes the user configuration; no daemon is involved or started.
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
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

// requirements reports what is missing: adb through the device package, which
// resolves it afresh on every question, gradle through the sidecar client, which
// answers from the JVM discovery result it cached at startup (plan m1g D22), and every
// other external tool as missing until its own check exists, with the reason, rather
// than the capability vanishing.
type requirements struct {
	adb    *device.ADB
	gradle *gradle.Client
}

func (r requirements) Missing(req capability.Requirement) (string, bool) {
	switch req {
	case capability.RequiresADB:
		res := r.adb.Resolve()
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
func registry(env platform.Env, st *store.Store, sup *proc.Supervisor, log io.Writer) (*capability.Registry, *capability.Operations, *event.Bus, *artifact.Store, *device.ADB) {
	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	// The daemon's one event bus (core-runtime.md §Events).
	bus := event.New(event.Options{})
	ops.UsePublisher(bus)
	var problems system.StateProblems
	if st != nil {
		problems = st.Problems
	}
	// Every adb client is started through the supervisor's locked spawner and given
	// the daemon's whole environment (plan m1e, R8). Without a supervisor (caps,
	// schema) adb can be resolved and reported but not run.
	var spawn device.Starter
	if sup != nil {
		spawn = sup.Spawn
	}
	var adb *device.ADB
	if adbExec != nil {
		adb = device.NewADBWithExec(adbEnviron(), adbExec)
	} else {
		adb = device.NewADB(adbEnviron(), spawn)
	}
	// The one Gradle sidecar client (ADR-0019): it discovers the JVM in the daemon's
	// environment, extracts the jars to the cache directory and supervises the shared
	// sidecar lazily (plan m1g step 5). It holds no process until a call needs one.
	gradleOpts := gradle.Options{Supervisor: sup, Log: log, Env: gradleEnviron(), Launch: gradleLaunch}
	if sup != nil {
		if cache, err := env.CacheDir(); err == nil {
			gradleOpts.CacheDir = cache
		}
	}
	if gradleFinder != nil {
		gradleOpts.Finder = gradleFinder(gradleEnviron())
	}
	gradleClient := gradle.New(gradleOpts)
	system.Register(reg, requirements{adb: adb, gradle: gradleClient}, adb, ops, problems, gradleClient)
	// The daemon's one artifact store, for the bus's epoch: a reference from an earlier
	// daemon run is ARTIFACT_EXPIRED (ADR-0012). Its state root is the daemon's, so a
	// durable build console survives a restart; without one (caps, schema) it serves
	// ephemeral references only. The caller closes it on shutdown.
	var stateRoot string
	if st != nil {
		stateRoot = st.Root()
	}
	artifacts, err := artifact.New(artifact.Options{Epoch: bus.Position().Epoch, StateRoot: stateRoot})
	if err != nil {
		panic(err) // the bus mints a well-formed epoch
	}
	// The daemon's one build history (plan m1g step 7): the records, their index and
	// the consoles they own. Without a state store it registers capabilities whose
	// handlers report that nothing is configured.
	history := build.NewRegistry(build.RegistryOptions{Store: st, Artifacts: artifacts, Log: log})
	device.Register(reg, adb, artifacts, history)
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
	ws := workspace.New(workspace.Options{Store: st, Ops: ops, Policy: policy.New(env), Log: log, Drain: testDrain,
		StopSidecars: stopSidecars, CloseRoot: closeRoot, Publisher: bus,
		OnOpen: func(id, _ string) { history.Sweep(id) }})
	// The trust hook: a call's root comes from its workspace, and an untrusted root
	// fails WORKSPACE_UNTRUSTED before the handler runs (plan m1g step 3, R4).
	authorize := policy.New(env).Authorize(func(call *capability.Call) (string, bool) {
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
	workspace.Register(reg, ws)
	event.Register(reg)
	if testCapabilities != nil {
		testCapabilities(reg, sup)
	}
	return reg, ops, bus, artifacts, adb
}

func printCaps() error {
	reg, _, _, _, _ := registry(platform.Current(), nil, nil, nil)
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
	reg, _, _, _, _ := registry(platform.Current(), nil, nil, nil)
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
