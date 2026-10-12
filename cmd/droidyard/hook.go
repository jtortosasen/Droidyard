package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/jtortosasen/droidyard/adapters/cli"
	"github.com/jtortosasen/droidyard/internal/assistant"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/agent"
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
// writes one answer on stdout: before a shell command, the client's pass-through or a
// deny that hands the agent `droidyard trash`, which moves the files to the trash from
// the agent's own shell (ADR-0062); after one, a hint naming the droidyard tool that
// does the same and a record of the Android command in Activity, or the pass-through
// (ADR-0063). It never starts a daemon, deletes nothing, denies nothing after a
// command, and never exits 2, which Claude Code reads as a deny: it exits 0 with an
// answer, or 1 with a note on stderr and nothing on stdout when it is misused. Anything
// that goes wrong after the arguments are read (no daemon, an error, the deadline,
// malformed input, a panic) is the client's pass-through, or after a command the hint
// once it is computed (fail open).
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
// that never answers or a panic cannot keep the answer past deadline. Past it, or after
// a panic, the answer is the fallback the decision had reached by then: after a shell
// command, the hint, which does not wait on the daemon (ADR-0063 §2, §3); else the
// client's pass-through.
func hookAnswer(client assistant.Client, root string, stdin io.Reader, stderr io.Writer, deadline time.Time) []byte {
	var fallback atomic.Pointer[[]byte]
	failOpen := func() []byte {
		if f := fallback.Load(); f != nil {
			return *f
		}
		return assistant.PassOutput(client)
	}
	answer := make(chan []byte, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(stderr, "droidyard hook: recovered from a panic, the command passes: %v\n", r)
				answer <- failOpen()
			}
		}()
		answer <- hookDecide(client, root, stdin, deadline, &fallback)
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case a := <-answer:
		return a
	case <-timer.C:
		return failOpen()
	}
}

// hookDecide is the answer for one event: the deny for a routed `rm`, handing the agent
// `droidyard trash`, when a daemon answers and opens the workspace, else the
// pass-through; after a shell command, hookObserve's answer. fallback is where the
// decision leaves the answer to give if the deadline comes first.
func hookDecide(client assistant.Client, root string, stdin io.Reader, deadline time.Time, fallback *atomic.Pointer[[]byte]) []byte {
	pass := assistant.PassOutput(client)
	if hookSeam != nil {
		hookSeam()
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, hookMaxStdin+1))
	if err != nil || len(raw) > hookMaxStdin {
		return pass
	}
	if after, ok := assistant.DecodeAfterEvent(client, raw); ok {
		return hookObserve(client, root, after, deadline, fallback)
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

// hookObserve is the answer after a shell command (ADR-0063): the hint for the Android
// commands it ran, computed before anything is dialled so that the deadline still
// answers with it, and, when a daemon answers within the deadline, a record of them in
// Activity. It never denies. A command with no Android program dials nothing.
func hookObserve(client assistant.Client, root string, ev assistant.AfterEvent, deadline time.Time, fallback *atomic.Pointer[[]byte]) []byte {
	obs := assistant.ObserveCommands(ev.Command)
	if len(obs) == 0 {
		return assistant.PassOutput(client)
	}
	out := assistant.AfterOutput(client, ev.Name, assistant.Hint(obs))
	fallback.Store(&out)
	recordCommands(client, root, obs, deadline)
	return out
}

// commandRecord is the parameters of agent.recordCommand for obs: what the reader kept,
// and nothing else (ADR-0063 §4).
func commandRecord(client assistant.Client, obs []assistant.Observed) agent.Record {
	rec := agent.Record{Client: string(client)}
	for _, o := range obs {
		words := o.Words
		if words == nil {
			words = []string{} // the schema requires the list, never null
		}
		rec.Commands = append(rec.Commands, agent.Command{Program: o.Program, Words: words, Suggested: o.Suggested})
	}
	return rec
}

// recordCommands calls agent.recordCommand for obs, and gives up quietly: with no daemon
// (never started here), a refusal or the deadline, nothing is recorded and the caller
// answers all the same (ADR-0063 §3). Opening the workspace and recording take a
// connection each, as in `droidyard trash`: a cli.Call reads its connection through a
// goroutine that outlives it. The open runs no project code and grants no trust
// (ADR-0015 §5).
func recordCommands(client assistant.Client, root string, obs []assistant.Observed, deadline time.Time) {
	params, err := json.Marshal(commandRecord(client, obs))
	if err != nil {
		return
	}
	hello := daemon.Hello{Client: capability.ClientCLI}
	conn, r, _, err := connectOrStart(hello, false, nil)
	if err != nil {
		return
	}
	conn.SetDeadline(deadline)
	id, err := openProject(conn, r, root)
	conn.Close()
	if err != nil {
		return
	}
	conn, r, _, err = connectOrStart(hello, false, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetDeadline(deadline)
	var out bytes.Buffer
	// The result is ignored: the hint does not depend on the record.
	_, _ = (cli.Call{Capability: "agent.recordCommand", Workspace: id, Params: params}).Run(r, conn, cli.Streams{Out: &out, Err: io.Discard}, nil)
}
