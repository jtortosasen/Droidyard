package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	mcpadapter "github.com/jtortosasen/droidyard/adapters/mcp"
	"github.com/jtortosasen/droidyard/internal/assistant"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/workspace"
	"github.com/jtortosasen/droidyard/skills"
)

const initUsage = "usage: droidyard init --client cursor|claude|opencode|all [--root <path>] [--dry-run] [--uninstall]"

// mcpTool is one tool an MCP session lists, as `init` and the skill's tool map need it.
type mcpTool struct {
	Name        string // the MCP name, build_assemble
	ReadOnly    bool
	Core        bool
	Description string
}

// mcpTools lists the registry's MCP tools through the adapter's own rule for what is
// exposed, so the allowlist and the skill's map cannot name a tool the server does not
// list, or miss one it does (ADR-0037 Decision 4).
func mcpTools(reg *capability.Registry) []mcpTool {
	core := map[string]bool{}
	for _, n := range mcpadapter.CoreTools {
		core[n] = true
	}
	var out []mcpTool
	for _, d := range reg.All() {
		if !mcpadapter.Exposed(d) {
			continue
		}
		name := mcpadapter.ToolName(d.Name)
		out = append(out, mcpTool{Name: name, ReadOnly: d.ResolveAnnotations().ReadOnly, Core: core[name], Description: d.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func readOnlyTools(tools []mcpTool) []string {
	var names []string
	for _, t := range tools {
		if t.ReadOnly {
			names = append(names, t.Name)
		}
	}
	return names
}

// binaryCommand is the absolute path every entry init writes runs (ADR-0037 Decision 2;
// ADR-0019 §Processes 8): a client started from the GUI has no shell PATH. It is the
// `droidyard` on PATH when that is this very binary, named as PATH names it and never
// resolved, so a package manager's upgrade, which repoints the link, keeps the entry
// valid; otherwise the running binary's own path, likewise unresolved.
func binaryCommand(exe string, lookPath func(string) (string, error)) (string, error) {
	exe, err := filepath.Abs(exe)
	if err != nil {
		return "", err
	}
	if p, err := lookPath("droidyard"); err == nil {
		if abs, err := filepath.Abs(p); err == nil && sameFile(abs, exe) {
			return abs, nil
		}
	}
	return exe, nil
}

func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

// initCmd implements `droidyard init`: it writes, merges or (--uninstall) removes the
// assistant integration in a project (ADR-0037). It talks to no daemon and starts none.
func initCmd(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	rootFlag := fs.String("root", "", "")
	clientFlag := fs.String("client", "", "")
	dryRun := fs.Bool("dry-run", false, "")
	uninstall := fs.Bool("uninstall", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintf(errw, "droidyard init: %v\n%s\n", firstErr(err, fmt.Errorf("unexpected argument %q", fs.Arg(0))), initUsage)
		return 2
	}
	if *clientFlag == "" {
		fmt.Fprintf(errw, "droidyard init: --client is required; it writes into your project, so say for which assistant\n%s\n", initUsage)
		return 2
	}
	clients, err := assistant.ParseClients(*clientFlag)
	if err != nil {
		fmt.Fprintf(errw, "droidyard init: %v\n%s\n", err, initUsage)
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(errw, "droidyard init:", err)
		return 1
	}
	// init writes into the user's project, so it does not guess one: without --root the
	// project is the one `droidyard ui` opens (ADR-0075 §6): found by a settings.gradle
	// at or above the working directory, or the top of a repository whose Gradle build
	// is one or two folders below. The home directory is never a project (its .cursor
	// and .claude are the user's global configuration).
	root := resolveRoot(*rootFlag, cwd)
	if *rootFlag == "" {
		p, ok := projectOf(cwd)
		if !ok {
			fmt.Fprintf(errw, "droidyard init: no project at %s: no settings.gradle or settings.gradle.kts in it or above it, none in the folders one or two levels below it, and no gradleRoot in its .droidyard/project.json; name the project with --root\n%s\n", cwd, initUsage)
			return 2
		}
		root = p
	}
	if home, err := os.UserHomeDir(); err == nil && sameFile(root, home) {
		fmt.Fprintf(errw, "droidyard init: %s is your home directory, not a project: its .cursor and .claude hold your global configuration; name the project with --root\n", root)
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(errw, "droidyard init: cannot find this binary:", err)
		return 1
	}
	command, err := binaryCommand(exe, exec.LookPath)
	if err != nil {
		fmt.Fprintln(errw, "droidyard init:", err)
		return 1
	}
	// A client's hook environment lacks DROIDYARD_STATE_DIR, so a custom state root is
	// written into the hook command (ADR-0061 §3).
	stateDir := ""
	if env := platform.Current(); env.Getenv("DROIDYARD_STATE_DIR") != "" {
		if stateDir, err = env.StateDir(); err != nil {
			fmt.Fprintln(errw, "droidyard init:", err)
			return 1
		}
	}
	reg, _, _, _, _, _ := registry(platform.Current(), nil, nil, nil)
	tools := readOnlyTools(mcpTools(reg))

	res, err := assistant.Run(assistant.Options{
		Root: root, Command: command, StateDir: stateDir, Clients: clients, Tools: tools,
		Skill: skills.DroidyardAndroid(), DryRun: *dryRun, Uninstall: *uninstall,
	})
	if err != nil {
		fmt.Fprintln(errw, "droidyard init:", err)
		return 1
	}
	printInit(out, root, command, res, tools, *dryRun, *uninstall)
	if g := workspace.ResolveGradleRoot(root, true); g.Err == nil {
		fmt.Fprintf(out, "Gradle root: %s\n", g.Abs)
	} else {
		fmt.Fprintf(out, "Gradle root: none (%s). %s\n", g.Err.Cause, g.Err.NextStep)
	}
	if !res.Refused() && !*dryRun && !*uninstall {
		for _, c := range clients {
			if n := assistant.Note(c); n != "" {
				fmt.Fprintln(out, n)
			}
		}
	}
	if res.Refused() {
		return 1
	}
	return 0
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// wouldVerb is how a dry run says what a real one did.
var wouldVerb = map[assistant.Status]string{
	assistant.Created: "would create", assistant.Updated: "would update",
	assistant.Removed: "would remove", assistant.Deleted: "would delete",
}

func printInit(w io.Writer, root, command string, res assistant.Result, tools []string, dry, uninstall bool) {
	verb := "init"
	if uninstall {
		verb = "uninstall"
	}
	if dry {
		fmt.Fprintf(w, "droidyard %s for %s (dry run, nothing is written)\n", verb, root)
	} else {
		fmt.Fprintf(w, "droidyard %s for %s\n", verb, root)
	}
	for _, c := range res.Changes {
		status := string(c.Status)
		if w, ok := wouldVerb[c.Status]; ok && dry {
			status = w
		}
		fmt.Fprintf(w, "  %-12s %s\n", status, c.Path)
		for _, n := range c.Notes {
			fmt.Fprintf(w, "               %s\n", n)
		}
		if c.Entry != "" {
			fmt.Fprintf(w, "               add this yourself:\n%s\n", indent(c.Entry, "                 "))
		}
	}
	if uninstall {
		return
	}
	for _, c := range assistant.Clients() {
		if cmd, ok := res.Hooks[c]; ok {
			fmt.Fprintf(w, "The %s hook, which, when a daemon runs, sends an agent's simple rm inside the project to droidyard's trash and records the agent's Android commands in Activity, and hints at the droidyard tool for them, runs: %s\n", c, cmd)
		}
	}
	if len(res.Allowed) == 0 {
		return
	}
	fmt.Fprintf(w, "The servers run %s.\n", command)
	fmt.Fprintf(w, "Allowlisted without asking, the %d read-only tools: %s\n", len(tools), strings.Join(tools, ", "))
	fmt.Fprintln(w, "These include queries that run the project's own code on a trusted root (project_*, build_tasks, sdk_requirements); droidyard trusts nothing here.")
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
