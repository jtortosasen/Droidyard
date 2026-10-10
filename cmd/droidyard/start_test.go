package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/workspace"
)

// launched runs a re-executed droidyard in dir with the launcher recording the URL it
// would have opened, and returns that URL and the process.
func launched(t *testing.T, state, dir string, args ...string) (string, *process) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "url")
	p := droidyardEnv(t, state, dir, []string{launcherEnv + "=" + file}, args...)
	if code := p.wait(t); code != 0 {
		t.Fatalf("droidyard %v exited %d; stderr: %s", args, code, p.stderr.String())
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("droidyard %v did not open the browser: %v", args, err)
	}
	return string(raw), p
}

// TestBareDroidyardOpensTheProject: `droidyard` with no subcommand is `ui`, and `ui` in a
// project folder, or under one, opens that project (workspace.open as client cli) and
// the browser on its id; the open grants no trust (ADR-0045 §1; ADR-0015 §5).
func TestBareDroidyardOpensTheProject(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	srv, addr := serveOn(t, state)
	token, _ := daemon.ReadToken(state)
	root, sub := gradleProject(t)
	id, _ := platform.Current().WorkspaceID(root)

	for _, args := range [][]string{nil, {"ui"}} {
		url, p := launched(t, state, sub, args...)
		if want := "http://" + addr + "/#token=" + token + "&w=" + id; url != want {
			t.Errorf("droidyard %v opened %q; want the tokenised URL with w=%s", args, strings.ReplaceAll(url, token, "<token>"), id)
		}
		srv.expect(t, "client=cli capability=workspace.open")
		if out := p.stdout.String() + p.stderr.String(); strings.Contains(out, token) || strings.Contains(out, "usage") {
			t.Errorf("droidyard %v printed %q: the token or the usage", args, out)
		}
	}

	env, code := callIn(t, state, "", "--workspace", root, "workspace.describe")
	if info := workspaceOf(t, env); code != 0 || info.State != workspace.Ready || info.Trusted || info.ID != id {
		t.Errorf("the opened workspace is %+v (exit %d); want %s ready and untrusted", info, code, id)
	}
	stopped(t, state)
	srv.wait(t)
}

// TestBareDroidyardElsewhereOpensThePicker: outside any project the browser opens on the
// page without `w`, and no workspace is opened (ADR-0045 §1).
func TestBareDroidyardElsewhereOpensThePicker(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	srv, addr := serveOn(t, state)
	token, _ := daemon.ReadToken(state)
	bare := platform.Current().Canonical(t.TempDir())

	url, _ := launched(t, state, bare)
	if want := "http://" + addr + "/#token=" + token; url != want {
		t.Errorf("droidyard in a folder with no project opened %q; want the URL with no w", strings.ReplaceAll(url, token, "<token>"))
	}
	if env, _ := callIn(t, state, "", "--workspace", bare, "workspace.describe"); env.Error == nil || env.Error.Code != capability.ErrWorkspaceNotOpen {
		t.Errorf("describing the folder gave %+v; want WORKSPACE_NOT_OPEN: nothing opened it", env.Error)
	}
	stopped(t, state)
	srv.wait(t)
}

// TestBareDroidyardStartsTheDaemon: with no daemon running, `droidyard` alone starts one
// as `ui` does (ADR-0019 §4) and opens the project on it.
func TestBareDroidyardStartsTheDaemon(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	killDaemonAtEnd(t, state)
	root, sub := gradleProject(t)
	id, _ := platform.Current().WorkspaceID(root)
	url, _ := launched(t, state, sub)
	if !strings.HasSuffix(url, "&w="+id) {
		t.Errorf("the URL does not end with &w=%s", id)
	}
	stopped(t, state)
}

// TestHelpPrintsTheUsage: the usage is `help`'s: it says what a bare `droidyard` does and
// exits 0.
func TestHelpPrintsTheUsage(t *testing.T) {
	t.Parallel()
	for _, arg := range []string{"help", "--help", "-h"} {
		p := droidyard(t, shortDir(t), arg)
		if code := p.wait(t); code != 0 {
			t.Errorf("droidyard %s exited %d", arg, code)
		}
		if out := p.stdout.String(); !strings.Contains(out, "Usage:") || !strings.Contains(out, "With no command, droidyard runs ui.") {
			t.Errorf("droidyard %s printed %q; want the usage, naming what a bare droidyard does", arg, out)
		}
	}
}

// fakeOsascript writes a script that stands for osascript and returns its path.
func fakeOsascript(t *testing.T, body string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "osascript")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func pidFromFile(t *testing.T, path string) int {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if raw, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(raw), "\n") {
			pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatal(err)
			}
			return pid
		}
	}
	t.Fatalf("%s was never written", path)
	return 0
}

func waitDead(t *testing.T, what string, pid int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return
		}
	}
	t.Errorf("%s (pid %d) is still running", what, pid)
}

// TestChooseRootThroughTheDaemon: workspace.chooseRoot end to end on a real daemon, its
// helper started by the supervisor's spawner (a fake osascript): the project root of the
// chosen folder; the call cancelled, and the daemon stopped, each take the helper down,
// its whole group (ADR-0045 §4).
func TestChooseRootThroughTheDaemon(t *testing.T) {
	t.Parallel()
	root, sub := gradleProject(t)

	t.Run("a chosen folder answers its project", func(t *testing.T) {
		state := shortDir(t)
		bin := fakeOsascript(t, "echo '"+sub+"/'")
		srv := droidyardEnv(t, state, "", []string{dialogFakeEnv + "=" + bin}, "serve")
		srv.expect(t, "listening")
		env, code := callIn(t, state, "", "workspace.chooseRoot")
		var res workspace.ChooseRootResult
		if err := json.Unmarshal(env.Result, &res); err != nil || code != 0 || res != (workspace.ChooseRootResult{Root: root}) {
			t.Errorf("chooseRoot = %s (exit %d, %+v); want root %s", env.Result, code, env.Error, root)
		}
		stopped(t, state)
		srv.wait(t)
	})

	t.Run("a cancel and a stop take the helper down", func(t *testing.T) {
		state := shortDir(t)
		dir := t.TempDir()
		bin := fakeOsascript(t, `echo $$ > '`+dir+`/pid.tmp'; mv '`+dir+`/pid.tmp' '`+dir+`/pid'; sleep 60 & echo $! > '`+dir+`/child.tmp'; mv '`+dir+`/child.tmp' '`+dir+`/child'; wait`)
		srv := droidyardEnv(t, state, "", []string{dialogFakeEnv + "=" + bin}, "serve")
		srv.expect(t, "listening")

		start := droidyard(t, state, "call", "--no-autostart", "--detach", "workspace.chooseRoot")
		if code := start.wait(t); code != 0 {
			t.Fatalf("chooseRoot --detach exited %d: %s", code, start.stderr.String())
		}
		op := strings.TrimSpace(start.stdout.String())
		pid := pidFromFile(t, filepath.Join(dir, "pid"))
		child := pidFromFile(t, filepath.Join(dir, "child"))
		cancelled, code := callIn(t, state, "", "operation.cancel", "--params", `{"operationId":"`+op+`"}`)
		if code != 0 {
			t.Fatalf("operation.cancel exited %d: %+v", code, cancelled.Error)
		}
		waitDead(t, "the dialog after the cancel", pid)
		waitDead(t, "what the dialog forked after the cancel", child)
		waited, _ := callIn(t, state, "", "operation.wait", "--params", `{"operationId":"`+op+`"}`)
		if waited.Operation == nil || waited.Operation.State != capability.StateCancelled {
			t.Errorf("the cancelled dialog's operation is %+v; want cancelled", waited.Operation)
		}

		// A second dialog, left up when the daemon stops: the helper does not outlive it.
		os.Remove(filepath.Join(dir, "pid"))
		os.Remove(filepath.Join(dir, "child"))
		if code := droidyard(t, state, "call", "--no-autostart", "--detach", "workspace.chooseRoot").wait(t); code != 0 {
			t.Fatalf("the second chooseRoot exited %d", code)
		}
		pid2 := pidFromFile(t, filepath.Join(dir, "pid"))
		child2 := pidFromFile(t, filepath.Join(dir, "child"))
		stopped(t, state)
		srv.wait(t)
		waitDead(t, "the dialog after the daemon stopped", pid2)
		waitDead(t, "what the dialog forked after the daemon stopped", child2)
	})
}

// TestFolderDialogRequirement: the folderDialog requirement is the dialog's own answer
// (ADR-0045 §4): missing, with the reason, off macOS and without osascript; present with
// both. The zero requirements, a test's, report it missing.
func TestFolderDialogRequirement(t *testing.T) {
	if _, missing := (requirements{}).Missing(capability.RequiresFolderDialog); !missing {
		t.Error("with no dialog configured the folderDialog requirement must be missing")
	}
	bin := fakeOsascript(t, "true")
	have := requirements{dialog: workspace.Dialog{GOOS: "darwin", Binary: bin, Spawn: func(*exec.Cmd) error { return nil }}}
	if reason, missing := have.Missing(capability.RequiresFolderDialog); missing {
		t.Errorf("darwin with an osascript and a spawner: missing, %q", reason)
	}
	linux := requirements{dialog: workspace.Dialog{GOOS: "linux", Binary: bin, Spawn: func(*exec.Cmd) error { return nil }}}
	if reason, missing := linux.Missing(capability.RequiresFolderDialog); !missing || !strings.Contains(reason, "macOS") {
		t.Errorf("linux: missing %v, %q; want missing, naming macOS", missing, reason)
	}
	none := requirements{dialog: workspace.Dialog{GOOS: "darwin", Binary: filepath.Join(t.TempDir(), "osascript"), Spawn: func(*exec.Cmd) error { return nil }}}
	if reason, missing := none.Missing(capability.RequiresFolderDialog); !missing || !strings.Contains(reason, "osascript") {
		t.Errorf("no osascript: missing %v, %q; want missing, naming osascript", missing, reason)
	}
}
