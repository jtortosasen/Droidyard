package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/jtortosasen/droidyard/internal/assistant"
)

const hookUsage = "usage: droidyard hook --client claude|cursor [--root <absolute project root>]"

// hook runs `droidyard hook`, which droidyard no longer needs and keeps for one minor
// release (plan m3j Q4, ADR-0137 §Consequences). Every `droidyard init` before it wrote an
// entry into the client's settings that runs this command on each tool call; removed
// outright, every call of every chat would run a failing hook until the user reran init.
// So it answers each event with the client's pass-through (exit 0, assistant.PassOutput)
// and does nothing else: it drains the event unread (drainEvent), dials no socket and writes nothing. It never
// exits 2, which Claude Code reads as a deny; only a misused command line exits 1, with a
// note on stderr and nothing on stdout. `droidyard init` removes the entries it wrote.
func hook(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	client, err := parseHookArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "droidyard hook: %v\n%s\n", err, hookUsage)
		return 1
	}
	drainEvent(stdin, hookDrainLimit)
	stdout.Write(assistant.PassOutput(client))
	return 0
}

// hookDrainLimit bounds how long the hook waits for its event to end: a client writes one
// small JSON object and closes stdin, so a stdin still open after that is not waited on.
const hookDrainLimit = time.Second

// drainEvent reads r to its end and discards it, at most 4 MiB and limit long, so the
// client's write of the event never meets a closed pipe.
func drainEvent(r io.Reader, limit time.Duration) {
	if r == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		io.Copy(io.Discard, io.LimitReader(r, 4<<20))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
	}
}

// parseHookArgs reads the arguments an old init entry carries: --client, and --root when
// the entry is a project's. The root is checked as before and then unused.
func parseHookArgs(args []string) (client assistant.Client, err error) {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	c := fs.String("client", "", "")
	r := fs.String("root", "", "")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() > 0 {
		return "", fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	switch client = assistant.Client(*c); client {
	case assistant.Claude, assistant.Cursor:
	default:
		return "", fmt.Errorf("--client needs claude or cursor, not %q", *c)
	}
	given := false
	fs.Visit(func(f *flag.Flag) { given = given || f.Name == "root" })
	if given && !filepath.IsAbs(*r) {
		return "", fmt.Errorf("--root needs an absolute project root, not %q", *r)
	}
	return client, nil
}
