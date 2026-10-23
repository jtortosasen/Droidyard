package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	mcpadapter "github.com/jtortosasen/droidyard/adapters/mcp"
	"github.com/jtortosasen/droidyard/internal/assistant"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/git"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/workspace"
	"github.com/jtortosasen/droidyard/skills"
)

const initUsage = "usage: droidyard init --client cursor|claude|opencode|all [--user | [--root <path>] [--exclude | --gitignore]] [--dry-run] [--uninstall]"

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
	user := fs.Bool("user", false, "")
	exclude := fs.Bool("exclude", false, "")
	gitignore := fs.Bool("gitignore", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintf(errw, "droidyard init: %v\n%s\n", firstErr(err, fmt.Errorf("unexpected argument %q", fs.Arg(0))), initUsage)
		return 2
	}
	// The flags that cannot be combined are refused before anything is read or written
	// (ADR-0124 §1, §4).
	if clash := flagClash(*user, *rootFlag != "", *exclude, *gitignore); clash != "" {
		fmt.Fprintf(errw, "droidyard init: %s\n%s\n", clash, initUsage)
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
	if *user {
		return initUser(cwd, clients, *dryRun, *uninstall, out, errw)
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
			cause := "no Gradle root"
			if g := workspace.ResolveGradleRoot(cwd, false); g.Err != nil {
				cause = g.Err.Cause
			}
			fmt.Fprintf(errw, "droidyard init: no project at %s: no settings.gradle or settings.gradle.kts in it or above it, and %s; choose one with \"gradleRoot\" in its .droidyard/project.json, or name the project with --root\n%s\n", cwd, cause, initUsage)
			return 2
		}
		root = p
	}
	if home, err := os.UserHomeDir(); err == nil && sameFile(root, home) {
		fmt.Fprintf(errw, "droidyard init: %s is your home directory, not a project: its .cursor and .claude hold your global configuration; name the project with --root\n", root)
		return 2
	}
	command, stateDir, tools, code := initInputs(errw)
	if code != 0 {
		return code
	}

	res, err := assistant.Run(assistant.Options{
		Root: root, Command: command, StateDir: stateDir, Clients: clients, Tools: tools,
		Skill: skills.DroidyardAndroid(), DryRun: *dryRun, Uninstall: *uninstall,
	})
	if err != nil {
		fmt.Fprintln(errw, "droidyard init:", err)
		return 1
	}
	// The ignore block of the files git does not track (ADR-0124 §4): written by an
	// install with --exclude or --gitignore, and removed by every uninstall.
	var ignoreErr error
	if *exclude || *gitignore || *uninstall {
		var targets []assistant.IgnoreTarget
		switch {
		case *uninstall:
			targets = []assistant.IgnoreTarget{assistant.ExcludeFile, assistant.GitignoreFile}
		case *exclude:
			targets = []assistant.IgnoreTarget{assistant.ExcludeFile}
		default:
			targets = []assistant.IgnoreTarget{assistant.GitignoreFile}
		}
		var changes []assistant.Change
		changes, ignoreErr = ignoreStep(root, res.Changes, targets, *dryRun, *uninstall)
		for _, c := range changes {
			// An uninstall with no flag mentions the block only when it removed one.
			if *uninstall && !*exclude && !*gitignore && c.Status == assistant.Unchanged {
				continue
			}
			res.Changes = append(res.Changes, c)
		}
	}
	printInit(out, "for "+root, command, res, tools, *dryRun, *uninstall)
	if ignoreErr != nil {
		fmt.Fprintln(errw, "droidyard init: the ignore block:", ignoreErr)
		return 1
	}
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

// flagClash says which pair of flags cannot be combined, or "" when none: --user installs
// for every project, so it names no root and no project file; --exclude and --gitignore
// are two places for the same block.
func flagClash(user, root, exclude, gitignore bool) string {
	switch {
	case user && root:
		return "--user and --root exclude each other: --user installs for every project, --root names one"
	case user && (exclude || gitignore):
		return "--user and --exclude or --gitignore exclude each other: those keep a project's files out of version control, and --user writes none"
	case exclude && gitignore:
		return "--exclude and --gitignore exclude each other: choose where the block goes"
	}
	return ""
}

// initInputs are what every run of init writes: the binary entries run, the state root a
// client's hook environment lacks, and the read-only tools. A non-zero code is the exit.
func initInputs(errw io.Writer) (command, stateDir string, tools []string, code int) {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(errw, "droidyard init: cannot find this binary:", err)
		return "", "", nil, 1
	}
	command, err = binaryCommand(exe, exec.LookPath)
	if err != nil {
		fmt.Fprintln(errw, "droidyard init:", err)
		return "", "", nil, 1
	}
	// A client's hook environment lacks DROIDYARD_STATE_DIR, so a custom state root is
	// written into the hook command (ADR-0061 §3).
	if env := platform.Current(); env.Getenv("DROIDYARD_STATE_DIR") != "" {
		if stateDir, err = env.StateDir(); err != nil {
			fmt.Fprintln(errw, "droidyard init:", err)
			return "", "", nil, 1
		}
	}
	reg, _, _, _, _, _, _ := registry(platform.Current(), nil, nil, nil)
	return command, stateDir, readOnlyTools(mcpTools(reg)), 0
}

// initUser is `init --user` (ADR-0124 §1): the integration written once into the user's
// own configuration, with no project, no root in any entry and no daemon.
func initUser(cwd string, clients []assistant.Client, dryRun, uninstall bool, out, errw io.Writer) int {
	env := platform.Current()
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(errw, "droidyard init: cannot find your home directory:", err)
		return 1
	}
	dirs := assistant.ResolveUserDirs(home, env.Getenv)
	command, stateDir, tools, code := initInputs(errw)
	if code != 0 {
		return code
	}
	opts := assistant.Options{
		User: true, Dirs: dirs, Command: command, StateDir: stateDir, Clients: clients, Tools: tools,
		Skill: skills.DroidyardAndroid(), DryRun: dryRun, Uninstall: uninstall,
	}
	// The runner stays a nil interface when there is no claude: the run then refuses the
	// entry and prints the command (a typed nil would look like a runner).
	if p, err := exec.LookPath("claude"); err == nil {
		opts.ClaudeMCP = claudeCLI{path: p}
	}
	res, err := assistant.Run(opts)
	if err != nil {
		fmt.Fprintln(errw, "droidyard init:", err)
		return 1
	}
	printInit(out, "--user", command, res, tools, dryRun, uninstall)
	if !uninstall {
		reportProjectEntries(out, cwd, clients)
	}
	if res.Refused() {
		return 1
	}
	return 0
}

// reportProjectEntries says, per client, that the project cwd lies in has its own
// droidyard entry, which takes precedence over the user's there (ADR-0124 §1), and the
// command that removes it.
func reportProjectEntries(w io.Writer, cwd string, clients []assistant.Client) {
	root, ok := projectOf(cwd)
	if !ok {
		return
	}
	for _, c := range clients {
		if f := assistant.ProjectEntry(root, c); f != "" {
			fmt.Fprintf(w, "%s: %s holds a droidyard entry, and the project's entries take precedence over the user's here; remove them with: droidyard init --uninstall --client %s\n", c, filepath.Join(root, filepath.FromSlash(f)), c)
		}
	}
}

// claudeCLI is Claude's own command line, which changes the user-scope MCP entry
// (ADR-0124 §1): .claude.json is Claude's live state and init never edits it.
type claudeCLI struct{ path string }

func (c claudeCLI) Add(entry json.RawMessage) error {
	return c.run("mcp", "add-json", "--scope", "user", assistant.ServerName, string(entry))
}

func (c claudeCLI) Remove() error {
	return c.run("mcp", "remove", "--scope", "user", assistant.ServerName)
}

func (c claudeCLI) run(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.path, args...)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		if line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n"); line != "" {
			return fmt.Errorf("%w: %s", err, line)
		}
		return err
	}
	return nil
}

// startInGroup is the repository reader's starter outside a daemon: the child in a
// process group of its own, so the runner can end all of it on a timeout.
func startInGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}

// ignoreStep keeps (or on uninstall takes out) the block of the run's untracked files in
// each target (ADR-0124 §4). The repository is read through the git package's runner,
// untrusted. A root outside a repository, or no git, is not an error: an install is
// refused by Ignore with its reason, an uninstall has no block to remove.
func ignoreStep(root string, changes []assistant.Change, targets []assistant.IgnoreTarget, dry, uninstall bool) ([]assistant.Change, error) {
	var paths []string
	if !uninstall {
		paths = assistant.IgnoreCandidates(changes)
	}
	gitPath, _ := exec.LookPath("git")
	repo, err := git.Tracked(context.Background(), startInGroup, gitPath, os.Environ(), root, paths)
	inRepo, noGit := err == nil, errors.Is(err, git.ErrNotFound)
	if err != nil && !noGit && !errors.Is(err, git.ErrNotRepository) {
		return nil, err
	}
	var out []assistant.Change
	for _, target := range targets {
		c, err := assistant.Ignore(assistant.IgnoreOptions{
			Root: root, Target: target, Changes: changes, InRepo: inRepo,
			Top: repo.Top, CommonDir: repo.CommonDir, Tracked: repo.Tracked, DryRun: dry, Uninstall: uninstall,
		})
		if err != nil {
			return out, err
		}
		if noGit && c.Status == assistant.Refused {
			c.Notes = append(c.Notes, "git was not found on PATH")
		}
		if !uninstall && !dry && c.Status != assistant.Refused {
			file := c.Path
			if !filepath.IsAbs(file) {
				file = filepath.Join(root, filepath.FromSlash(file))
			}
			for _, l := range assistant.IgnoreBlock(file) {
				c.Notes = append(c.Notes, "ignores "+l)
			}
		}
		out = append(out, c)
	}
	return out, nil
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

// printInit prints a run's changes. target is "for <root>" or "--user".
func printInit(w io.Writer, target, command string, res assistant.Result, tools []string, dry, uninstall bool) {
	verb := "init"
	if uninstall {
		verb = "uninstall"
	}
	if dry {
		fmt.Fprintf(w, "droidyard %s %s (dry run, nothing is written)\n", verb, target)
	} else {
		fmt.Fprintf(w, "droidyard %s %s\n", verb, target)
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
