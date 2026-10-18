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
	"sync/atomic"
	"time"

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

// quoteArg single-quotes s for a POSIX shell, leaving a plain word as it is. `=` is not
// plain: zsh replaces a word starting with it by a command's path.
func quoteArg(s string) string {
	plain := s != ""
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_@%+:,./-", c)) {
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

	// Never auto-starts: with no daemon nothing is moved (ADR-0062 §1). Everything
	// before the wait on the delete runs in a goroutine raced against trashDeadline
	// (plan m2r step 3): a daemon that accepts and never answers must not hang the
	// agent's shell. Only the wait itself, once its connection is up, is unbounded.
	paths := make([]string, len(targets))
	for i, t := range targets {
		paths[i] = t.rel
	}
	gate := &trashGate{}
	ready := make(chan struct{})
	done := make(chan trashResult, 1)
	go func() { done <- trashDelete(gate, ready, trashChat(), root, paths) }()
	timer := time.NewTimer(trashDeadline)
	defer timer.Stop()
	var res trashResult
	select {
	case <-ready:
		res = <-done
	case res = <-done:
	case <-timer.C:
		select {
		case <-ready: // the wait connection came up as the timer fired: wait for the delete
			res = <-done
		case res = <-done:
		default:
			if gate.giveUp() {
				return a.refuse(stderr, []string{fmt.Sprintf("the daemon did not answer within %s", trashDeadline)})
			}
			return a.unknown(stderr, fmt.Sprintf("the daemon did not finish starting the delete within %s", trashDeadline))
		}
	}
	if len(res.problems) > 0 {
		return a.refuse(stderr, res.problems)
	}
	if res.unknown != "" {
		return a.unknown(stderr, res.unknown)
	}
	var env capability.ResponseEnvelope
	if err := json.Unmarshal(res.out, &env); err != nil {
		fmt.Fprintf(stderr, "droidyard trash: the daemon's answer is not an envelope: %v\n", err)
		return 1
	}
	if env.Error != nil {
		return a.refuse(stderr, []string{fmt.Sprintf("%s: %s", env.Error.Code, env.Error.Message)})
	}
	var deleted file.DeleteResult
	if err := json.Unmarshal(env.Result, &deleted); err != nil || len(deleted.Items) != len(targets) {
		fmt.Fprintf(stderr, "droidyard trash: file.delete answered %s, not one item per path\n", env.Result)
		return 1
	}
	code := 0
	for i, it := range deleted.Items {
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

// unknown says that the delete was sent and its outcome is not known, and hands back the
// command that deletes without the trash. An operand already trashed is then reported
// missing by that rm; one not trashed is deleted as the agent first asked.
func (a trashArgs) unknown(stderr io.Writer, why string) int {
	fmt.Fprintf(stderr, "droidyard trash: %s\n", why)
	fmt.Fprintln(stderr, "droidyard trash: the outcome is unknown: some paths may already be in the trash.")
	fmt.Fprintf(stderr, "To delete without droidyard's trash (rm reports an operand already trashed as missing): %s\n", a.plainRm())
	return 1
}

// trashDeadline bounds everything before the wait on the delete: the dials and hellos, the
// workspace opening, the delete's start and the wait connection's hello. A live daemon
// answers each at once (plan m2r step 3). A test lowers it through installTestSeams.
var trashDeadline = 10 * time.Second

// The states of a trashGate.
const (
	trashIdle   int32 = iota // nothing decided
	trashSent                // the goroutine is writing, or has written, the delete
	trashGaveUp              // the main goroutine has reported that nothing was moved
)

// trashGate decides, once, whether the delete request was sent. The goroutine marks it
// sent before writing the request, only if the main goroutine has not given up; the main
// goroutine gives up only if it is not marked. So "nothing was moved" is printed only
// when no request was written, and the abandoned goroutine never writes one afterwards.
type trashGate struct{ state atomic.Int32 }

// send marks the delete sent, and reports whether the request may be written.
func (g *trashGate) send() bool {
	return g.state.CompareAndSwap(trashIdle, trashSent) || g.state.Load() == trashSent
}

// giveUp reports whether the main goroutine may say nothing was moved.
func (g *trashGate) giveUp() bool { return g.state.CompareAndSwap(trashIdle, trashGaveUp) }

var errTrashGaveUp = errors.New("the deadline passed before the delete was sent")

// gatedWriter writes to w only when its gate lets the delete go.
type gatedWriter struct {
	w    io.Writer
	gate *trashGate
}

func (g gatedWriter) Write(p []byte) (int, error) {
	if !g.gate.send() {
		return 0, errTrashGaveUp
	}
	return g.w.Write(p)
}

// trashChat is the chat the delete is joined to (ADR-0095 §1): the id Claude Code sets in
// its Bash tool's environment (plan m2s step 1 (d)), so Activity shows the delete under
// the agent. It is empty when the variable is not set or is not a chat id, and when
// CURSOR_AGENT=1 marks the shell as cursor-agent's: a client started from a Claude Code
// shell inherits that variable, which then names another client's chat.
func trashChat() string {
	if os.Getenv("CURSOR_AGENT") == "1" {
		return ""
	}
	if v := os.Getenv(chatEnv); daemon.ChatPattern.MatchString(v) {
		return v
	}
	return ""
}

// trashResult is how trashDelete ended: problems when nothing was moved, unknown when the
// delete was sent and its outcome is not known, else the final response envelope.
type trashResult struct {
	problems []string
	unknown  string
	out      []byte
}

// trashDelete opens the workspace and deletes paths through the daemon. It closes ready
// when only the wait on the delete is left. It prints nothing: the main goroutine reports
// what it returns, and one that has given up never reads it.
func trashDelete(gate *trashGate, ready chan<- struct{}, chat, root string, paths []string) trashResult {
	// Opening the workspace, the delete's start and its wait take a connection each: a
	// cli.Call reads its connection through a goroutine that outlives it, so a second
	// call cannot share one.
	hello := daemon.Hello{Client: capability.ClientCLI, Chat: chat}
	conn, r, socket, err := connectOrStart(hello, false, nil)
	if err != nil {
		return trashResult{problems: []string{dialProblem(socket, err)}}
	}
	id, err := openProject(conn, r, root)
	conn.Close()
	if err != nil {
		return trashResult{problems: []string{fmt.Sprintf("opening %s: %v", root, err)}}
	}
	conn, r, socket, err = connectOrStart(hello, false, nil)
	if err != nil {
		return trashResult{problems: []string{dialProblem(socket, err)}}
	}
	params, _ := json.Marshal(file.DeleteParams{Paths: paths})
	var started bytes.Buffer
	code, err := (cli.Call{Capability: "file.delete", Workspace: id, Params: params, Detach: true}).
		Run(r, gatedWriter{conn, gate}, cli.Streams{Out: &started, Err: io.Discard}, nil)
	conn.Close()
	switch {
	case errors.Is(err, errTrashGaveUp):
		return trashResult{} // nobody reads it
	case err != nil:
		return trashResult{unknown: fmt.Sprintf("the call to file.delete failed: %v", err)}
	case code != 0:
		return trashResult{out: started.Bytes()} // rejected: an error envelope, nothing started
	}
	opID := strings.TrimSpace(started.String())

	conn, r, socket, err = connectOrStart(hello, false, nil)
	if err != nil {
		return trashResult{unknown: fmt.Sprintf("the delete was started, but the wait for it could not connect: %s", dialProblem(socket, err))}
	}
	defer conn.Close()
	close(ready)
	wait, _ := json.Marshal(capability.WaitParams{OperationID: opID})
	var out bytes.Buffer
	if _, err := (cli.Call{Capability: capability.OperationWait, Params: wait}).Run(r, conn, cli.Streams{Out: &out, Err: io.Discard}, nil); err != nil {
		return trashResult{unknown: fmt.Sprintf("waiting for the delete failed: %v", err)}
	}
	// A daemon that replaced the one that took the delete knows no such operation: some
	// paths may already be in the old daemon's trash. Any other error envelope is the
	// delete's own answer (a whole-batch refusal), which moved nothing.
	var env capability.ResponseEnvelope
	if json.Unmarshal(out.Bytes(), &env) == nil && env.Error != nil && env.Error.Code == capability.ErrOperationNotFound {
		return trashResult{unknown: fmt.Sprintf("the daemon no longer knows the delete (%s: %s)", env.Error.Code, env.Error.Message)}
	}
	return trashResult{out: out.Bytes()}
}
