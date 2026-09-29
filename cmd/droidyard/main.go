// Command droidyard is the Droidyard binary.
//
// Milestone 1 is the contract, not the daemon: `caps` and `schema` read the registry
// and the generated schema, and prove the wiring end to end. The transport
// subcommands are recognised so the CLI surface is honest about what exists, and they
// say plainly that they are not implemented yet.
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

Not implemented yet (the daemon and its adapters):
  serve      Run the core daemon.
  mcp        Attach an MCP stdio session to the daemon.
  call       Call one capability and print its result.
  ui         Open the web UI.
  trust      Grant a project root trust.
  status     Report on the running daemon.
  stop       Stop the running daemon.
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
	case "serve", "mcp", "call", "ui", "trust", "status", "stop":
		fmt.Fprintf(os.Stderr, "droidyard %s: not implemented in this milestone.\n", cmd)
		fmt.Fprintf(os.Stderr, "This build carries the capability contract only. See docs/roadmap/mvp.md.\n")
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

func registry() *capability.Registry {
	reg := capability.NewRegistry()
	system.Register(reg, notInstalled{})
	return reg
}

func printCaps() error {
	for _, d := range registry().All() {
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
	doc, err := schema.Generate(registry(), system.Version)
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
