package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jtortosasen/droidyard/internal/assistant"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/platform"
)

const hookUsage = "usage: droidyard hook --client claude|cursor --root <absolute project root>"

// hookDeadline is how long the hook may take to answer, from its start: a client waits
// on it before running the command, so past this it lets the command through
// (ADR-0061 §1).
const hookDeadline = 2 * time.Second

// hookMaxStdin bounds the event the hook reads; a larger one is not a client's event.
const hookMaxStdin = 1 << 20

// hookSeam is for this package's tests only: called first in the decision, so a test
// can force a panic there. The shipped binary never sets it.
var hookSeam func()

// hook runs `droidyard hook` (ADR-0061): it reads one assistant hook event on stdin and
// writes one answer on stdout, the client's pass-through or a deny that hands the agent
// `droidyard trash`, which moves the files to the trash from the agent's own shell
// (ADR-0062). It never starts a daemon, deletes
// nothing, and never exits 2, which Claude Code reads as a deny: it exits 0 with an
// answer, or 1 with a note on stderr and nothing on stdout when it is misused. Anything
// that goes wrong after the arguments are read (no daemon, an error, the deadline,
// malformed input, a panic) is the client's pass-through (fail open).
func hook(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	client, root, err := parseHookArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "droidyard hook: %v\n%s\n", err, hookUsage)
		return 1
	}
	// One write, after the decision: stdout carries the answer and nothing else.
	stdout.Write(hookAnswer(client, root, stdin, stderr, time.Now().Add(hookDeadline)))
	return 0
}

func parseHookArgs(args []string) (client assistant.Client, root string, err error) {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	c := fs.String("client", "", "")
	r := fs.String("root", "", "")
	if err := fs.Parse(args); err != nil {
		return "", "", err
	}
	if fs.NArg() > 0 {
		return "", "", fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	switch client = assistant.Client(*c); client {
	case assistant.Claude, assistant.Cursor:
	default:
		return "", "", fmt.Errorf("--client needs claude or cursor, not %q", *c)
	}
	if !filepath.IsAbs(*r) {
		return "", "", fmt.Errorf("--root needs an absolute project root, not %q", *r)
	}
	return client, filepath.Clean(*r), nil
}

// hookAnswer decides in a goroutine of its own, so a stdin that never ends, a daemon
// that never answers or a panic cannot keep the answer past deadline.
func hookAnswer(client assistant.Client, root string, stdin io.Reader, stderr io.Writer, deadline time.Time) []byte {
	answer := make(chan []byte, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(stderr, "droidyard hook: recovered from a panic, the command passes: %v\n", r)
				answer <- assistant.PassOutput(client)
			}
		}()
		answer <- hookDecide(client, root, stdin, deadline)
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case a := <-answer:
		return a
	case <-timer.C:
		return assistant.PassOutput(client)
	}
}

// hookDecide is the answer for one event: the deny for a routed `rm`, handing the agent
// `droidyard trash`, when a daemon answers and opens the workspace, else the
// pass-through.
func hookDecide(client assistant.Client, root string, stdin io.Reader, deadline time.Time) []byte {
	pass := assistant.PassOutput(client)
	if hookSeam != nil {
		hookSeam()
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, hookMaxStdin+1))
	if err != nil || len(raw) > hookMaxStdin {
		return pass
	}
	ev, ok := assistant.DecodeHookEvent(client, raw)
	if !ok {
		return pass
	}
	route, ok := assistant.RouteRm(ev, root)
	if !ok {
		return pass
	}
	// The replacement is `droidyard trash`, naming this binary, the project and, when the
	// hook's state root came from the environment, that state root, so it reaches the
	// daemon dialled below and never starts another (ADR-0062 §4).
	exe, err := os.Executable()
	if err != nil {
		return pass
	}
	env := platform.Current()
	redirect := assistant.Redirect{Binary: exe, Root: root}
	if env.Getenv("DROIDYARD_STATE_DIR") != "" {
		if redirect.StateDir, err = env.StateDir(); err != nil {
			return pass
		}
	}
	msg, ok := redirect.Message(route)
	if !ok {
		return pass
	}
	// Dial without auto-start: with no daemon the command passes and none is started.
	conn, r, _, err := connectOrStart(daemon.Hello{Client: capability.ClientCLI}, false, nil)
	if err != nil {
		return pass
	}
	defer conn.Close()
	conn.SetDeadline(deadline)
	// The workspace is opened so the replacement finds it open. That runs no project
	// code and grants no trust (ADR-0015 §5).
	if _, err := openProject(conn, r, root); err != nil {
		return pass
	}
	return assistant.DenyOutput(client, msg)
}
