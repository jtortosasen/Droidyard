package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	proc "github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/testtime"
	"github.com/jtortosasen/droidyard/internal/workspace"
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

// fakeOsascript writes a script that stands for osascript and returns its path.
func fakeOsascript(t *testing.T, body string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "osascript")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// pidFromFile reads the pid a fake script wrote at path. The script is a file the test just
// wrote, whose first run a loaded host takes seconds over, so the wait is a hang guard.
func pidFromFile(t *testing.T, path string) int {
	t.Helper()
	pid := 0
	testtime.Eventually(t, testtime.Bound(30*time.Second), path+" to be written", func() bool {
		raw, err := os.ReadFile(path)
		if err != nil || !strings.HasSuffix(string(raw), "\n") {
			return false
		}
		var perr error
		if pid, perr = strconv.Atoi(strings.TrimSpace(string(raw))); perr != nil {
			t.Fatal(perr)
		}
		return true
	})
	return pid
}

func waitDead(t *testing.T, what string, pid int) {
	t.Helper()
	testtime.Eventually(t, testtime.Bound(30*time.Second), what+" (pid "+strconv.Itoa(pid)+") to be gone", func() bool {
		return syscall.Kill(pid, 0) == syscall.ESRCH
	})
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
	have := requirements{dialog: workspace.Dialog{GOOS: "darwin", Binary: bin, Spawn: func(*exec.Cmd) (func() error, error) { return nil, nil }}}
	if reason, missing := have.Missing(capability.RequiresFolderDialog); missing {
		t.Errorf("darwin with an osascript and a spawner: missing, %q", reason)
	}
	linux := requirements{dialog: workspace.Dialog{GOOS: "linux", Binary: bin, Spawn: func(*exec.Cmd) (func() error, error) { return nil, nil }}}
	if reason, missing := linux.Missing(capability.RequiresFolderDialog); !missing || !strings.Contains(reason, "macOS") {
		t.Errorf("linux: missing %v, %q; want missing, naming macOS", missing, reason)
	}
	none := requirements{dialog: workspace.Dialog{GOOS: "darwin", Binary: filepath.Join(t.TempDir(), "osascript"), Spawn: func(*exec.Cmd) (func() error, error) { return nil, nil }}}
	if reason, missing := none.Missing(capability.RequiresFolderDialog); !missing || !strings.Contains(reason, "osascript") {
		t.Errorf("no osascript: missing %v, %q; want missing, naming osascript", missing, reason)
	}
}

// dialogRecords are the folder-dialog records processes.json holds, read from disk (the
// daemon is a process of its own).
func dialogRecords(t *testing.T, state string) []proc.Record {
	t.Helper()
	var out []proc.Record
	for _, r := range (&fakeFollowDaemon{state: state}).records(t) {
		if r.Kind == proc.FolderDialog {
			out = append(out, r)
		}
	}
	return out
}

// waitDialogRecords polls until the folder-dialog records number want, within a hang guard.
func waitDialogRecords(t *testing.T, state string, want int) []proc.Record {
	t.Helper()
	var recs []proc.Record
	testtime.Eventually(t, testtime.Bound(30*time.Second), "processes.json to hold "+strconv.Itoa(want)+" folder-dialog records", func() bool {
		recs = dialogRecords(t, state)
		return len(recs) == want
	})
	return recs
}

// TestFolderDialogRecordAndReap: the folder dialog is pid-recorded for as long as it is up
// (plan m2r step 2; ADR-0021 §Pid records: every long-running child), on a real daemon
// whose osascript is a script that writes its pid, forks a child and sleeps. The record
// is there during the call and gone once the call is cancelled; a daemon killed with
// SIGKILL during a dialog leaves the record, and the next daemon's reconcile reaps the
// dialog's group, the forked child included. Seen failing on main, where the dialog is
// spawned unrecorded: no record, and both survive the kill.
func TestFolderDialogRecordAndReap(t *testing.T) {
	t.Parallel()
	script := func(dir string) string {
		return fakeOsascript(t, `echo $$ > '`+dir+`/pid.tmp'; mv '`+dir+`/pid.tmp' '`+dir+`/pid'; sleep 60 & echo $! > '`+dir+`/child.tmp'; mv '`+dir+`/child.tmp' '`+dir+`/child'; wait`)
	}
	detach := func(t *testing.T, state string) string {
		t.Helper()
		start := droidyard(t, state, "call", "--no-autostart", "--detach", "workspace.chooseRoot")
		if code := start.wait(t); code != 0 {
			t.Fatalf("chooseRoot --detach exited %d: %s", code, start.stderr.String())
		}
		return strings.TrimSpace(start.stdout.String())
	}

	t.Run("recorded while the call runs, gone after its cancel", func(t *testing.T) {
		state, dir := shortDir(t), t.TempDir()
		srv := droidyardEnv(t, state, "", []string{dialogFakeEnv + "=" + script(dir)}, "serve")
		srv.expect(t, "listening")
		op := detach(t, state)
		pid := pidFromFile(t, filepath.Join(dir, "pid"))
		recs := waitDialogRecords(t, state, 1)
		if recs[0].PID != pid || recs[0].PGID != pid {
			t.Errorf("the dialog record is %+v; want pid and group %d, the script's", recs[0], pid)
		}
		if _, code := callIn(t, state, "", "operation.cancel", "--params", `{"operationId":"`+op+`"}`); code != 0 {
			t.Fatalf("operation.cancel exited %d", code)
		}
		waitDead(t, "the dialog after the cancel", pid)
		waitDialogRecords(t, state, 0)
		stopped(t, state)
		srv.wait(t)
	})

	t.Run("a killed daemon's dialog is reaped by the next start", func(t *testing.T) {
		if runtime.GOOS == "linux" {
			t.Skip("linux: Pdeathsig (process-lifecycle.md §Shutdown, layer 2) ends the dialog's leader when the daemon is killed, so the orphan this half reaps never exists and Reconcile drops the record as no such process (known-limits row 268); the real dialog exists on macOS only (Dialog.Missing)")
		}
		state, dir := shortDir(t), t.TempDir()
		env := []string{dialogFakeEnv + "=" + script(dir)}
		srv := droidyardEnv(t, state, "", env, "serve")
		srv.expect(t, "listening")
		detach(t, state)
		pid := pidFromFile(t, filepath.Join(dir, "pid"))
		child := pidFromFile(t, filepath.Join(dir, "child"))
		waitDialogRecords(t, state, 1)

		// Killed outright: no shutdown runs, nothing removes the record or the dialog.
		srv.cmd.Process.Signal(syscall.SIGKILL)
		srv.wait(t)
		if !alive(pid) || !alive(child) {
			t.Fatalf("the dialog (pid %d) or its child (pid %d) died with the daemon; both must outlive a kill -9", pid, child)
		}
		if recs := dialogRecords(t, state); len(recs) != 1 || recs[0].PID != pid {
			t.Fatalf("processes.json holds %+v after the kill; want the dialog's record %d", recs, pid)
		}

		// The next start reconciles before it listens.
		second := droidyardEnv(t, state, "", env, "serve")
		second.expect(t, "listening")
		reaped := false
		for _, l := range strings.Split(second.stderr.String(), "\n") {
			if strings.Contains(l, `reconcile: record "folder-dialog" pid `+strconv.Itoa(pid)) && strings.HasSuffix(l, ": reaped") {
				reaped = true
			}
		}
		if !reaped {
			t.Errorf("the next daemon's reconcile does not name the dialog reaped:\n%s", second.stderr.String())
		}
		waitDead(t, "the dialog after the next start", pid)
		waitDead(t, "what the dialog forked after the next start", child)
		if recs := dialogRecords(t, state); len(recs) != 0 {
			t.Errorf("processes.json holds %+v after reconciling; want none", recs)
		}
		stopped(t, state)
		second.wait(t)
	})
}
