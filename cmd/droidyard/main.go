// Command droidyard is the Droidyard binary.
//
// `caps` and `schema` read the registry and the generated schema. `serve` runs the
// daemon on its socket; `call`, `mcp`, `status`, `stop` and `ui` talk to it. The subcommands still
// missing are recognised so the CLI surface is honest about what exists, and they say
// plainly that they are not implemented yet.
package main

import (
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
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/event"
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

Not implemented yet:
  trust      Grant a project root trust.
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
		fmt.Fprintf(os.Stderr, "droidyard %s: not implemented in this milestone.\n", cmd)
		fmt.Fprintf(os.Stderr, "See docs/roadmap/mvp.md.\n")
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

// requirements reports what is missing: adb through the device package, which
// resolves it afresh on every question, and every other external tool as missing
// until its own check exists, with the reason, rather than the capability vanishing.
type requirements struct{ adb *device.ADB }

func (r requirements) Missing(req capability.Requirement) (string, bool) {
	if req != capability.RequiresADB {
		return string(req) + " was not found on this machine", true
	}
	res := r.adb.Resolve()
	if res.Found {
		return "", false
	}
	return res.NotFound(), true
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
)

// registry builds the shipped registry, the operation store that dispatches from it
// and the daemon's event bus, over the state store st and the supervisor sup, logging
// to log. internal/schema's test builds the same set for the golden file. caps and
// schema, which run no capability, pass nil for all three.
func registry(st *store.Store, sup *proc.Supervisor, log io.Writer) (*capability.Registry, *capability.Operations, *event.Bus, *artifact.Store) {
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
	system.Register(reg, requirements{adb}, adb, ops, problems)
	// The daemon's one artifact store, for the bus's epoch: a reference from an earlier
	// daemon run is ARTIFACT_EXPIRED (ADR-0012). The caller closes it on shutdown.
	artifacts, err := artifact.New(artifact.Options{Epoch: bus.Position().Epoch})
	if err != nil {
		panic(err) // the bus mints a well-formed epoch
	}
	device.Register(reg, adb, artifacts)
	artifactcap.Register(reg, artifacts)
	ops.RegisterCapabilities()
	var stopSidecars func(id string)
	if sup != nil {
		// workspace.close stops the workspace's sidecars after its drain and before it
		// marks the workspace closed (ADR-0027 §Decision).
		stopSidecars = func(id string) { sup.Stop(proc.WorkspaceScope(id)) }
	}
	workspace.Register(reg, workspace.New(workspace.Options{Store: st, Ops: ops, Log: log, Drain: testDrain, StopSidecars: stopSidecars, Publisher: bus}))
	event.Register(reg)
	if testCapabilities != nil {
		testCapabilities(reg, sup)
	}
	return reg, ops, bus, artifacts
}

func printCaps() error {
	reg, _, _, _ := registry(nil, nil, nil)
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
	reg, _, _, _ := registry(nil, nil, nil)
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
