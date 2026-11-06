package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/daemon"
)

// TestHelpPrintsTheUsage: the usage is `help`'s, and it exits 0. A bare `droidyard` prints
// the same help (TestBareDroidyardPrintsTheHelp).
func TestHelpPrintsTheUsage(t *testing.T) {
	t.Parallel()
	for _, arg := range []string{"help", "--help", "-h"} {
		p := droidyard(t, shortDir(t), arg)
		if code := p.wait(t); code != 0 {
			t.Errorf("droidyard %s exited %d", arg, code)
		}
		if out := p.stdout.String(); !strings.Contains(out, "Usage:") {
			t.Errorf("droidyard %s printed %q; want the usage", arg, out)
		}
	}
}

// Decision (ADR-0137 §1): a bare `droidyard` is the help, not a browser: it exits 0,
// prints the usage, starts no daemon and opens no project. Seen failing while bare
// droidyard still ran `ui`.
func TestBareDroidyardPrintsTheHelp(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	p := droidyard(t, state)
	if code := p.wait(t); code != 0 {
		t.Fatalf("bare droidyard exited %d; stderr: %s", code, p.stderr.String())
	}
	if out := p.stdout.String(); !strings.Contains(out, "Usage:") || !strings.Contains(out, "droidyard help <command>") {
		t.Errorf("bare droidyard printed %q; want the help", out)
	}
	if _, err := os.Stat(filepath.Join(state, daemon.LockFile)); !os.IsNotExist(err) {
		t.Errorf("bare droidyard started a daemon: %v", err)
	}
}
