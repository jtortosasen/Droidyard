// Command droidyard is the Droidyard binary.
//
// `caps` and `schema` read the registry and the generated schema. `serve` runs the
// daemon on its socket; `call`, `mcp`, `status` and `stop` talk to it. The subcommands still
// missing are recognised so the CLI surface is honest about what exists, and they say
// plainly that they are not implemented yet.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/schema"
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
             droidyard call <capability> [--params <json>] [--detach] [--no-autostart]
             Ctrl-C cancels the operation; --detach prints its id and returns.
             call and mcp start a daemon when none answers, unless --no-autostart.
  mcp        Attach an MCP stdio session to the daemon:
             droidyard mcp [--wait <duration>] [--no-autostart]
             --wait bounds how long a long-running tool waits (default 20s).
  status     Report on the running daemon.
  stop       Stop the running daemon.

Not implemented yet:
  ui         Open the web UI.
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
	case "ui", "trust":
		fmt.Fprintf(os.Stderr, "droidyard %s: not implemented in this milestone.\n", cmd)
		fmt.Fprintf(os.Stderr, "See docs/roadmap/mvp.md.\n")
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

// notInstalled stands in until the requirement checks exist: every external tool is
// reported missing, with the reason, rather than the capability vanishing.
type notInstalled struct{}

func (notInstalled) Missing(r capability.Requirement) (string, bool) {
	return string(r) + " was not found on this machine", true
}

// registry builds the shipped registry and the operation store that dispatches from
// it. internal/schema's test builds the same set for the golden file.
func registry() (*capability.Registry, *capability.Operations) {
	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	system.Register(reg, notInstalled{}, ops)
	ops.RegisterCapabilities()
	return reg, ops
}

func printCaps() error {
	reg, _ := registry()
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
	reg, _ := registry()
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
