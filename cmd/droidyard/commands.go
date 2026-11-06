package main

// The command tree of the binary (plan m2z, ADR-0122): every registered capability is a
// derived command, `droidyard <domain> <verb>`, and the commands that are not capabilities
// are hand-written ones registered here with their own help. adapters/cli holds the tree;
// this file is what the binary adds to it: the specs from its registry, the hand commands,
// the index's sections.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/jtortosasen/droidyard/adapters/cli"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
	"github.com/jtortosasen/droidyard/internal/schema"
)

// specs is one cli.Spec per descriptor of reg. Input is left empty: reflecting every
// parameter struct at each start is work the listings do not need, so a command's input
// schema is read when that command is the one invoked (cmdTree.prepare).
func specs(reg *capability.Registry) []cli.Spec {
	all := reg.All()
	out := make([]cli.Spec, 0, len(all))
	for _, d := range all {
		out = append(out, cli.Spec{
			Name:            d.Name,
			Kind:            d.Kind,
			Scope:           d.Scope,
			Statefulness:    d.Statefulness,
			LongRunning:     d.LongRunning,
			Audience:        d.Audience,
			Annotations:     d.ResolveAnnotations(),
			Description:     d.Description,
			Errors:          errorCodes(d),
			UntrustedEvents: untrustedEvents(d),
			Text:            textAdapter(d),
		})
	}
	return out
}

func errorCodes(d capability.Descriptor) []string {
	codes := make([]string, 0, len(d.Errors))
	for _, c := range d.Errors {
		codes = append(codes, string(c))
	}
	return codes
}

// untrustedEvents says whether any event payload the capability declares has a field
// marked untrusted: its stream then carries device text (ADR-0015 §1).
func untrustedEvents(d capability.Descriptor) bool {
	if d.Events == nil {
		return false
	}
	for _, payload := range d.Events.Types {
		if ok, err := capability.HasUntrusted(reflect.TypeOf(payload)); err != nil || ok {
			return true // a payload that cannot be walked is treated as marked
		}
	}
	return false
}

// textAdapter renders a result's JSON through the descriptor's Text renderer, by decoding
// it into the descriptor's Result type. It returns "" where there is no renderer or the
// result does not decode, and the caller falls back to its generic rendering.
func textAdapter(d capability.Descriptor) func(json.RawMessage) string {
	if d.Text == nil || d.Result == nil {
		return nil
	}
	t := reflect.TypeOf(d.Result)
	return func(raw json.RawMessage) string {
		v := reflect.New(t)
		if err := json.Unmarshal(raw, v.Interface()); err != nil {
			return ""
		}
		return d.Text(v.Elem().Interface())
	}
}

// cmdTree is the command tree with the registry it was built from, so a command's input
// schema can be read when it is invoked.
type cmdTree struct {
	*cli.Tree
	reg *capability.Registry
}

// newCmdTree builds the tree from the binary's own registry, as caps does, so help works
// with no daemon (plan m2z D14).
func newCmdTree() (*cmdTree, error) {
	reg, _, _, _, _, _, _ := registry(platform.Current(), nil, nil, nil)
	return buildTree(reg)
}

func buildTree(reg *capability.Registry) (*cmdTree, error) {
	ct := &cmdTree{reg: reg}
	tree, err := cli.NewTree(specs(reg), commands(ct))
	if err != nil {
		return nil, err
	}
	tree.Before, tree.After = sections()
	ct.Tree = tree
	return ct, nil
}

// prepare reads the input schema of the capability args name, if they name one, before
// the tree parses or describes it.
func (ct *cmdTree) prepare(args []string) {
	r, err := ct.Resolve(args)
	if err != nil || r.Spec == nil || r.Spec.Input != nil {
		return
	}
	d, ok := ct.reg.Lookup(r.Spec.Name)
	if !ok {
		return
	}
	if in, err := schema.Input(d); err == nil {
		r.Spec.Input = in
	}
}

// dispatch runs a command line.
func (ct *cmdTree) dispatch(args []string, out, errOut io.Writer) int {
	ct.prepare(args)
	return ct.Dispatch(args, os.Getenv, out, errOut, ct.runDerived)
}

// runDerived makes the call a derived command parsed. A derived command prints for a
// person unless it was given --json (plan m2z D7, D8): the descriptor's Text renderer,
// else the generic one. `droidyard call` stays the envelope.
func (ct *cmdTree) runDerived(d cli.Derived) int {
	c := d.Call
	ct.defaultProject(&c)
	if !d.JSON {
		c.Human = true
		c.Who = strings.ReplaceAll(c.Capability, ".", " ")
		if desc, ok := ct.reg.Lookup(c.Capability); ok {
			c.Text = textAdapter(desc)
			c.UntrustedEvents = untrustedEvents(desc)
		}
	}
	return runCapability(strings.ReplaceAll(d.Call.Capability, ".", " "), c)
}

// absoluteAPK makes a relative `apk` of a device.install call absolute against this
// process's current directory, as a person writes it (`--apk app.apk`); the daemon has no
// cwd of this process and refuses a relative path, as MCP's calls still do.
func absoluteAPK(c *cli.Call) {
	if c.Capability != "device.install" {
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(c.Params, &fields) != nil {
		return
	}
	var apk string
	if json.Unmarshal(fields["apk"], &apk) != nil || apk == "" || filepath.IsAbs(apk) {
		return
	}
	abs, err := filepath.Abs(apk)
	if err != nil {
		return
	}
	fields["apk"], _ = json.Marshal(abs)
	if out, err := json.Marshal(fields); err == nil {
		c.Params = out
	}
}

// defaultProject gives a call that takes a project the one the command line names, as an
// absolute path, or the current directory when it names none (ADR-0137 §5): the daemon has
// no cwd of this process, so the path goes in the call. A capability that takes none, and
// one the registry does not know (the daemon answers it), are left as they are.
func (ct *cmdTree) defaultProject(c *cli.Call) {
	if ct == nil || ct.reg == nil {
		return
	}
	absoluteAPK(c)
	d, ok := ct.reg.Lookup(c.Capability)
	if !ok || d.Scope != capability.Workspace {
		return
	}
	if c.Project == "" && d.ProjectOptional && !callNames(c.Params, "build") {
		// device.install takes a project for a build only (ADR-0137 §4): an apk install is
		// not given the current directory, which need not be a project.
		return
	}
	if c.Project == "" {
		// A project in --params is the call's project: the current directory does not stand in
		// beside it, and a --project given too is refused by the adapter.
		var fields map[string]json.RawMessage
		if json.Unmarshal(c.Params, &fields) == nil {
			if _, ok := fields[capability.ProjectParam]; ok {
				return
			}
		}
		if cwd, err := os.Getwd(); err == nil {
			c.Project = cwd
		}
		return
	}
	if abs, err := filepath.Abs(c.Project); err == nil {
		c.Project = abs
	}
}

// help prints the help for a path: the index, a domain, a verb or a hand command.
func (ct *cmdTree) help(path []string, out, errOut io.Writer) int {
	ct.prepare(path)
	text, err := ct.Help(path)
	if err != nil {
		fmt.Fprintf(errOut, "droidyard: %v\n", err)
		return cli.ExitUsage
	}
	fmt.Fprint(out, text)
	return 0
}

// handCommand is a hand-written command: its summary (a line of the index), its help, and
// what it runs.
type handCommand struct {
	name    string
	summary string
	help    string
	ownHelp bool
	run     func(args []string) int
}

func stdio(run func(args []string, out, errOut io.Writer) int) func([]string) int {
	return func(args []string) int { return run(args, os.Stdout, os.Stderr) }
}

// handCommands is every command that is not a capability. None is renamed from the
// binary's first form (plan m2z D12); `device allow|disallow` and `artifact cat` are verbs
// of the domains they belong to (D13).
func handCommands(ct *cmdTree) []handCommand {
	return []handCommand{
		{name: "caps", summary: "List the capabilities this build exposes, with their annotations.", help: capsHelp,
			run: func([]string) int {
				if err := printCaps(); err != nil {
					fail(err)
				}
				return 0
			}},
		{name: "schema", summary: "Print the generated JSON Schema that consumers are contracted to.", help: schemaHelp,
			run: func([]string) int {
				if err := printSchema(); err != nil {
					fail(err)
				}
				return 0
			}},
		{name: "version", summary: "Print the build version and the API version.", help: versionHelp,
			run: func(args []string) int { return versionCommand(os.Stdout, args) }},
		{name: "help", summary: "Show this index, or the help of a command: droidyard help <command>.", help: helpHelp,
			run: func(args []string) int { return ct.help(args, os.Stdout, os.Stderr) }},
		{name: "serve", summary: "Run the core daemon in the foreground, until SIGTERM or SIGINT.", help: serveHelp,
			run: serve},
		{name: "call", summary: "Call one capability by its name and print its response envelope.", help: callHelp,
			run: func(args []string) int { return call(ct, args) }},
		{name: "status", summary: "Report on the running daemon: its socket and the HTTP API's address.", help: statusHelp,
			run: func([]string) int { return status() }},
		{name: "stop", summary: "Stop the running daemon.", help: stopHelp,
			run: func([]string) int { return stop() }},
		{name: "mcp", summary: "Attach an MCP stdio session to the daemon.", help: mcpHelp,
			run: mcpShim},
		{name: "trust", summary: "Grant a project root trust, so capabilities that run its code may.", help: trustHelp,
			run: trust},
		{name: "id", summary: "Print this daemon's fingerprint, what a host pins to admit it.", help: idHelp,
			run: stdio(idCmd)},
		{name: "host", summary: "Lend physical devices to paired Droidyards, the host's side.", help: hostHelp,
			run: stdio(hostCmd)},
		{name: "remote", summary: "Use the devices another Droidyard lends, the client's side.", help: remoteHelp,
			run: stdio(remoteCmd)},
		{name: "device allow", summary: "Allow a physical device (or <remote>/<serial>) without editing the configuration.", help: deviceAllowHelp,
			run: func(args []string) int {
				return deviceWith(policy.New(platform.Current()), append([]string{"allow"}, args...), os.Stdout, os.Stderr)
			}},
		{name: "device disallow", summary: "Stop allowing a physical device.", help: deviceDisallowHelp,
			run: func(args []string) int {
				return deviceWith(policy.New(platform.Current()), append([]string{"disallow"}, args...), os.Stdout, os.Stderr)
			}},
		{name: "share", summary: "Share a physical device with another Droidyard, the host's side.", help: shareHelp,
			run: stdio(shareCmd)},
		{name: "unshare", summary: "Stop sharing a device, or every device.", help: unshareHelp,
			run: stdio(unshareCmd)},
		{name: "join", summary: "Use the device another Droidyard shares, the client's side.", help: joinHelp,
			run: stdio(joinCmd)},
		{name: "init", summary: "Set a project up for coding assistants (optional).", help: initHelp,
			run: stdio(initCmd)},
		{name: "hook", summary: "Pass one coding assistant's hook event (kept one minor for old init entries).", help: hookHelp, ownHelp: true,
			run: func(args []string) int { return hook(args, os.Stdin, os.Stdout, os.Stderr) }},
		{name: "artifact cat", summary: "Write a text artifact's bytes to standard output, unchanged.", help: artifactCatHelp,
			run: func(args []string) int {
				return artifactCmd(append([]string{"cat"}, args...), os.Stdout, os.Stderr)
			}},
	}
}

func commands(ct *cmdTree) []cli.Hand {
	var out []cli.Hand
	for _, h := range handCommands(ct) {
		out = append(out, cli.Hand{Name: h.name, Summary: h.summary, Help: h.help, OwnHelp: h.ownHelp, Run: h.run})
	}
	return out
}

// sections are the index's hand-written parts: where to start, then (after the generated
// list of domains and verbs) the hand commands by section and the raw door. A command's
// line is its summary, so the summary lives once.
func sections() (before, after []cli.Section) {
	summaries := map[string]string{}
	for _, h := range handCommands(&cmdTree{}) {
		summaries[h.name] = h.summary
	}
	list := func(names ...string) string {
		var b strings.Builder
		width := 0
		for _, n := range names {
			width = max(width, len(n))
		}
		for _, n := range names {
			fmt.Fprintf(&b, "  %-*s  %s\n", width, n, summaries[n])
		}
		return b.String()
	}
	before = []cli.Section{{Body: `Droidyard — a lean Android development runtime for humans and agents.

Usage:
  droidyard [<command>]

With no command, droidyard prints this help.
droidyard help <command> shows a command's own help; droidyard <domain> --help lists
a domain's verbs.`}}
	after = []cli.Section{
		{Title: "Project", Body: list("trust", "init")},
		{Title: "The daemon", Body: list("serve", "status", "stop", "id", "version")},
		{Title: "Assistants", Body: list("mcp", "hook")},
		{Title: "Sharing devices", Body: list("share", "unshare", "join", "host", "remote", "device allow", "device disallow")},
		{Title: "The raw door", Body: list("call", "caps", "schema", "help", "artifact cat")},
	}
	return before, after
}

// callNames reports whether the parameter object params carries a field called name.
func callNames(params json.RawMessage, name string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(params, &fields) != nil {
		return false
	}
	_, ok := fields[name]
	return ok
}
