package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jtortosasen/droidyard/adapters/cli"
	"github.com/jtortosasen/droidyard/internal/assistant"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/file"
)

const trashUsage = "usage: droidyard trash [--state-dir <absolute dir>] [--root <absolute project root>] [-r|-R|-f ...] [--] <path>..."

// trashMaxPaths is file.delete's batch bound (it refuses more).
const trashMaxPaths = 1000

// trashArgs is a parsed `droidyard trash` command line.
type trashArgs struct {
	stateDir, root string
	recursive      bool
	force          bool
	operands       []string
}

// parseTrashArgs reads the command line as `rm` does (ADR-0062 §1): options end at `--`
// or at the first operand (a later `-r` is a file name), and combined flags such as
// `-rf` are taken. --state-dir and --root, each with a separate value, are among the
// options.
func parseTrashArgs(args []string) (trashArgs, error) {
	var a trashArgs
	i := 0
options:
	for ; i < len(args); i++ {
		s := args[i]
		switch {
		case s == "--":
			i++
			break options
		case s == "--state-dir" || s == "--root":
			if i+1 >= len(args) || args[i+1] == "" {
				return a, fmt.Errorf("%s needs a value", s)
			}
			i++
			if !filepath.IsAbs(args[i]) {
				return a, fmt.Errorf("%s needs an absolute path, not %q", s, args[i])
			}
			if s == "--root" {
				a.root = filepath.Clean(args[i])
			} else {
				a.stateDir = filepath.Clean(args[i])
			}
		case strings.HasPrefix(s, "-") && len(s) > 1:
			if strings.HasPrefix(s, "--") {
				return a, fmt.Errorf("unknown option %s", s)
			}
			for _, r := range s[1:] {
				switch r {
				case 'r', 'R':
					a.recursive = true
				case 'f':
					a.force = true
				default:
					return a, fmt.Errorf("unknown option -%c", r)
				}
			}
		default:
			break options
		}
	}
	a.operands = args[i:]
	if len(a.operands) == 0 {
		return a, errors.New("no path to delete")
	}
	return a, nil
}

// flags is the rm flags as one word, "" for none.
func (a trashArgs) flags() string {
	f := ""
	if a.recursive {
		f += "r"
	}
	if a.force {
		f += "f"
	}
	if f == "" {
		return ""
	}
	return "-" + f
}

// quoteArg single-quotes s for a POSIX shell, leaving a plain word as it is.
func quoteArg(s string) string {
	plain := s != ""
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_@%+=:,./-", c)) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// plainRm is the command that deletes the operands without droidyard's trash, which a
// refusal hands back (ADR-0062 §2); `command` keeps a hook from routing it again.
func (a trashArgs) plainRm() string {
	parts := []string{"command rm"}
	if f := a.flags(); f != "" {
		parts = append(parts, f)
	}
	parts = append(parts, "--")
	for _, o := range a.operands {
		parts = append(parts, quoteArg(o))
	}
	return strings.Join(parts, " ")
}

// trashTarget is one operand that will be deleted: its root-relative path.
type trashTarget struct {
	operand, rel string
}

// trashCmd runs `droidyard trash` (ADR-0062): it moves its operands, resolved against
// the working directory, to the project's trash through the running daemon. It checks
// every operand before moving any, never starts a daemon, and exits 0 only when every
// operand is trashed, 1 when anything is refused or fails, 2 for a usage error.
func trashCmd(args []string, stdout, stderr io.Writer) int {
	a, err := parseTrashArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "droidyard trash: %v\n%s\n", err, trashUsage)
		return 2
	}
	// The state root is read by platform.Current in this process, as everywhere.
	if a.stateDir != "" {
		os.Setenv("DROIDYARD_STATE_DIR", a.stateDir)
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "droidyard trash: no working directory: %v\n", err)
		return 1
	}
	root := a.root
	if root == "" {
		root = resolveRoot("", cwd)
	}

	targets, problems := a.check(cwd, root)
	if len(problems) > 0 {
		return a.refuse(stderr, problems)
	}
	if len(targets) == 0 {
		return 0 // every operand was missing under -f: nothing to do, as rm -f
	}
	if len(targets) > trashMaxPaths {
		return a.refuse(stderr, []string{fmt.Sprintf("%d paths; droidyard trashes at most %d at once", len(targets), trashMaxPaths)})
	}

	// Never auto-starts: with no daemon nothing is moved (ADR-0062 §1). Opening the
	// workspace and deleting take a connection each: a cli.Call reads its connection
	// through a goroutine that outlives it, so a second call cannot share one.
	hello := daemon.Hello{Client: capability.ClientCLI}
	conn, r, socket, err := connectOrStart(hello, false, nil)
	if err != nil {
		return a.refuse(stderr, []string{dialProblem(socket, err)})
	}
	id, err := openProject(conn, r, root)
	conn.Close()
	if err != nil {
		return a.refuse(stderr, []string{fmt.Sprintf("opening %s: %v", root, err)})
	}
	conn, r, socket, err = connectOrStart(hello, false, nil)
	if err != nil {
		return a.refuse(stderr, []string{dialProblem(socket, err)})
	}
	defer conn.Close()
	paths := make([]string, len(targets))
	for i, t := range targets {
		paths[i] = t.rel
	}
	params, _ := json.Marshal(file.DeleteParams{Paths: paths})
	var out bytes.Buffer
	if _, err := (cli.Call{Capability: "file.delete", Workspace: id, Params: params}).Run(r, conn, cli.Streams{Out: &out, Err: stderr}, nil); err != nil {
		fmt.Fprintf(stderr, "droidyard trash: %v\n", err)
		return 1
	}
	var env capability.ResponseEnvelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		fmt.Fprintf(stderr, "droidyard trash: the daemon's answer is not an envelope: %v\n", err)
		return 1
	}
	if env.Error != nil {
		return a.refuse(stderr, []string{fmt.Sprintf("%s: %s", env.Error.Code, env.Error.Message)})
	}
	var res file.DeleteResult
	if err := json.Unmarshal(env.Result, &res); err != nil || len(res.Items) != len(targets) {
		fmt.Fprintf(stderr, "droidyard trash: file.delete answered %s, not one item per path\n", env.Result)
		return 1
	}
	code := 0
	for i, it := range res.Items {
		if it.Code != "" {
			fmt.Fprintf(stdout, "%s: %s\n", targets[i].operand, it.Code)
			if it.Message != "" {
				fmt.Fprintf(stderr, "%s: %s\n", targets[i].operand, it.Message)
			}
			code = 1
			continue
		}
		fmt.Fprintf(stdout, "trashed %s\n", targets[i].operand)
	}
	return code
}

// check places every operand against the root, before anything moves (ADR-0062 §2): the
// hook's target rules, plus -r for a directory and -f for a missing operand. A path
// two operands name is one target, listed under the first operand.
func (a trashArgs) check(cwd, root string) (targets []trashTarget, problems []string) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, []string{fmt.Sprintf("the project root %s cannot be resolved: %v", root, err)}
	}
	seen := map[string]bool{}
	for _, o := range a.operands {
		if o == "" {
			problems = append(problems, "'': an empty path")
			continue
		}
		abs := o
		if !filepath.IsAbs(o) {
			abs = strings.TrimRight(cwd, "/") + "/" + o
		}
		rel, isDir, err := assistant.PlaceTarget(abs, realRoot)
		switch {
		case errors.Is(err, assistant.ErrTargetMissing):
			if !a.force {
				problems = append(problems, fmt.Sprintf("%s: no such file or directory", o))
			}
		case err != nil:
			problems = append(problems, fmt.Sprintf("%s: %v", o, err))
		case isDir && !a.recursive:
			problems = append(problems, fmt.Sprintf("%s: is a directory (use -r)", o))
		case !seen[rel]:
			seen[rel] = true
			targets = append(targets, trashTarget{operand: o, rel: rel})
		}
	}
	return targets, problems
}

// refuse prints why nothing was moved, and the command that deletes without the trash.
func (a trashArgs) refuse(stderr io.Writer, problems []string) int {
	for _, p := range problems {
		fmt.Fprintf(stderr, "droidyard trash: %s\n", p)
	}
	fmt.Fprintln(stderr, "droidyard trash: nothing was moved.")
	fmt.Fprintf(stderr, "To delete without droidyard's trash: %s\n", a.plainRm())
	return 1
}

// dialProblem says why no connection to the daemon was made.
func dialProblem(socket string, err error) string {
	var e *capability.Error
	if errors.As(err, &e) {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("no daemon answers at %s: %v (start one with `droidyard serve`)", socket, err)
}
