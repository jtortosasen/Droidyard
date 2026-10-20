package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	proc "github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// The daemon-level half of step 2's process-lifetime check (plan m1k D5): a shell.exec
// command is a real process the supervisor records, so only a re-executed daemon reaches
// the paths this file pins. Real processes, a temp state root and a temp project root;
// nothing here touches a device or the shared adb server.

// shellRecords reads the pid records processes.json holds, straight from disk, so a test
// can see them while the daemon runs them (the daemon is a process of its own).
func shellRecords(t *testing.T, state string) []proc.Record {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(state, proc.RecordsFile))
	if err != nil {
		return nil
	}
	var recs proc.Records
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatalf("processes.json: %v", err)
	}
	return recs.Processes
}

// waitShellRecord polls for the one shell-command record a running command wrote.
func waitShellRecord(t *testing.T, state string) proc.Record {
	t.Helper()
	for deadline := time.Now().Add(testtime.Bound(processGuard)); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if recs := shellRecords(t, state); len(recs) == 1 {
			return recs[0]
		}
	}
	t.Fatalf("no shell-command record appeared in processes.json")
	return proc.Record{}
}

// groupGone reports whether no process remains in the group: kill(-pgid, 0) is ESRCH.
func groupGone(pgid int) bool {
	return syscall.Kill(-pgid, 0) == syscall.ESRCH
}

// TestShellCommandOutlivesAKilledDaemonAndTheNextStartReapsIt: a shell.exec command is
// spawned without the death signal, so a SIGKILLed daemon leaves it running; the next
// start's Reconcile reaps its whole group by the record (plan m1k D5;
// ADR-0021 §Pid records; process-lifecycle.md §Shutdown, cancellation and no-orphans).
// Seen failing with the shell-command kind given the death signal (Linux) or with a
// record the next start cannot use.
func TestShellCommandOutlivesAKilledDaemonAndTheNextStartReapsIt(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	cfg := t.TempDir()
	root, _ := gradleProject(t)
	body := `{"trust":{"roots":[` + strconv.Quote(root) + `]},"shell":{"enabled":true}}`
	if err := os.WriteFile(filepath.Join(cfg, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"DROIDYARD_CONFIG_DIR=" + cfg}

	srv := droidyardEnv(t, state, "", env, "serve")
	srv.expect(t, "listening")
	if opened, code := callIn(t, state, root, "workspace.open"); code != 0 {
		t.Fatalf("workspace.open exited %d: %+v", code, opened.Error)
	}

	// Detached: nothing waits on the operation, so the daemon's death is the only event.
	detached := droidyardEnv(t, state, "", env, "call", "--no-autostart", "--detach",
		"--workspace", root, "shell.exec", "--params", `{"command":"sleep 300 & sleep 300"}`)
	if code := detached.wait(t); code != 0 {
		t.Fatalf("the detached call exited %d", code)
	}
	rec := waitShellRecord(t, state)
	if rec.Kind != proc.ShellCommand {
		t.Fatalf("record kind = %q, want %q", rec.Kind, proc.ShellCommand)
	}
	if !alive(rec.PID) {
		t.Fatalf("the recorded command (pid %d) is not running while the call does", rec.PID)
	}

	// Killed outright: no shutdown runs, so nothing kills the group or removes the record.
	srv.cmd.Process.Signal(syscall.SIGKILL)
	srv.wait(t)
	if !alive(rec.PID) {
		t.Fatalf("the command (pid %d) died with the daemon; it must outlive a kill -9", rec.PID)
	}
	if groupGone(rec.PGID) {
		t.Fatalf("the command's group %d died with the daemon", rec.PGID)
	}
	if recs := shellRecords(t, state); len(recs) != 1 || recs[0].PID != rec.PID {
		t.Fatalf("processes.json holds %+v after the kill, want the command's record %d", recs, rec.PID)
	}

	// The next start reconciles before it listens, so by "listening" the group is reaped.
	second := droidyardEnv(t, state, "", env, "serve")
	second.expect(t, "listening")
	if !groupGone(rec.PGID) {
		t.Errorf("the command's group %d survived the next daemon's reconciliation", rec.PGID)
	}
	if recs := shellRecords(t, state); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after reconciling, want none", recs)
	}

	if code := droidyardEnv(t, state, "", env, "stop").wait(t); code != 0 {
		t.Fatalf("the second stop exited %d", code)
	}
	second.wait(t)
}

// TestGracefulStopEndsAShellCommand: a `droidyard stop` while a shell.exec command runs
// ends the call cancelled — not completed — with no process group left and its record
// removed, so a graceful stop leaves nothing behind (ADR-0020; ADR-0021 §Pid records;
// plan m1k D5).
func TestGracefulStopEndsAShellCommand(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	cfg := t.TempDir()
	root, _ := gradleProject(t)
	body := `{"trust":{"roots":[` + strconv.Quote(root) + `]},"shell":{"enabled":true}}`
	if err := os.WriteFile(filepath.Join(cfg, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"DROIDYARD_CONFIG_DIR=" + cfg}

	srv := droidyardEnv(t, state, "", env, "serve")
	srv.expect(t, "listening")
	if opened, code := callIn(t, state, root, "workspace.open"); code != 0 {
		t.Fatalf("workspace.open exited %d: %+v", code, opened.Error)
	}
	detached := droidyardEnv(t, state, "", env, "call", "--no-autostart", "--detach",
		"--workspace", root, "shell.exec", "--params", `{"command":"sleep 60"}`)
	if code := detached.wait(t); code != 0 {
		t.Fatalf("the detached call exited %d", code)
	}
	rec := waitShellRecord(t, state)
	if !alive(rec.PID) {
		t.Fatalf("the recorded command (pid %d) is not running while the call does", rec.PID)
	}

	if code := droidyardEnv(t, state, "", env, "stop").wait(t); code != 0 {
		t.Fatalf("stop exited %d", code)
	}
	srv.wait(t)
	if !groupGone(rec.PGID) {
		t.Errorf("the command's group %d survived the graceful stop", rec.PGID)
	}
	if recs := shellRecords(t, state); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after the stop, want none", recs)
	}
}
