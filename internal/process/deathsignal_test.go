package process

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/store"
)

// TestDeathSignalPerKind: on Linux a child is asked for SIGTERM when the thread that
// forked it exits (Pdeathsig), for every kind but shell-command. A shell.exec command
// must survive the daemon so Reconcile, which keys on a live leader, can reap its whole
// group; a device-stream client or a sidecar keeps the signal, as before (plan m1k D5;
// process-lifecycle.md §Shutdown, layer 2). The choice is a portable bool so it is
// tested here on any OS, not only under the Linux build tag. Seen failing with
// shell-command given the death signal.
func TestDeathSignalPerKind(t *testing.T) {
	if deathSignal(ShellCommand) {
		t.Error("shell-command asks for the death signal; its leader must outlive the daemon")
	}
	for _, kind := range []Kind{"", SharedSidecar, WorkspaceSidecar, DeviceStream} {
		if !deathSignal(kind) {
			t.Errorf("kind %q does not ask for the death signal", kind)
		}
	}
}

// TestSpawnRecordedKillsTheShellCommandsGroupWhenTheRecordWriteFails: a shell.exec
// command whose record write fails has its group killed — not the pid alone, which
// would leave a background child the command had already forked (plan m1k D5) — and no
// record is left. The beforeRecord hook holds the write until the command has forked
// and named its child, so the group-versus-pid decision is observed here.
func TestSpawnRecordedKillsTheShellCommandsGroupWhenTheRecordWriteFails(t *testing.T) {
	root := t.TempDir()
	st, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: st, Log: testLog{t}})
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Shutdown)
	// A path the store's write cannot pass: a non-empty directory where its tmp file
	// would go.
	tmp := filepath.Join(root, RecordsFile+".tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "held"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The command forks a background sleep and names it; the hook holds the record write
	// until it has, so the failed write meets a group with a member besides the leader.
	// Seen failing with the leader killed by pid: the background sleep survives.
	pidFile := filepath.Join(t.TempDir(), "bg.pid")
	cmd := exec.Command("/bin/sh", "-c", "sleep 60 & echo $! > "+pidFile+"; sleep 60")
	bgPid := 0
	beforeRecord = func(*exec.Cmd) {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if b, err := os.ReadFile(pidFile); err == nil {
				if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 0 {
					bgPid = n
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Cleanup(func() {
		beforeRecord = nil
		if bgPid > 0 {
			syscall.Kill(bgPid, syscall.SIGKILL)
		}
	})
	if _, err := s.SpawnRecorded(cmd, ShellCommand); !errors.Is(err, ErrUnrecordable) {
		t.Fatalf("SpawnRecorded err = %v, want ErrUnrecordable", err)
	}
	if bgPid == 0 {
		t.Fatal("the command never named its background child")
	}
	// SIGKILL to the group is delivered asynchronously and the orphaned child is reaped
	// by init: poll briefly rather than read a zombie as alive.
	eventually(t, 2*time.Second, fmt.Sprintf("the background child (pid %d) gone after a record that could not be written", bgPid),
		func() bool { return dead(bgPid) })
	if cmd.Process == nil {
		t.Fatal("no command was started; this case must exercise the write failing after it starts")
	}
	if !dead(cmd.Process.Pid) {
		t.Errorf("the command (pid %d) survived a record that could not be written", cmd.Process.Pid)
	}
	eventually(t, 2*time.Second, fmt.Sprintf("the process group %d gone after the record write failed", cmd.Process.Pid),
		func() bool { return errors.Is(syscall.Kill(-cmd.Process.Pid, 0), syscall.ESRCH) })
	if recs := onDisk(t, st); len(recs) != 0 {
		t.Errorf("processes.json holds %+v, want none: the write failed", recs)
	}
}

// TestSpawnRecordedSparesTheGroupOfOtherKindsWhenTheRecordWriteFails: for every kind but
// shell-command a failed record write kills the client by its pid, never its group — a
// forked adb server shares that group and must survive (m1e R8). The command forks a
// child, as adb forks its server, and the hook holds the write until it has. Seen
// failing with every kind's group killed (m1k close review N2).
func TestSpawnRecordedSparesTheGroupOfOtherKindsWhenTheRecordWriteFails(t *testing.T) {
	root := t.TempDir()
	st, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: st, Log: testLog{t}})
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Shutdown)
	tmp := filepath.Join(root, RecordsFile+".tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "held"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.Command("/bin/sh", "-c", "sleep 60 & echo $! > "+pidFile+"; sleep 60")
	childPid := 0
	beforeRecord = func(*exec.Cmd) {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if b, err := os.ReadFile(pidFile); err == nil {
				if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 0 {
					childPid = n
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Cleanup(func() {
		beforeRecord = nil
		// The spared group still holds the child and the leader's foreground sleep.
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		if childPid > 0 {
			syscall.Kill(childPid, syscall.SIGKILL)
		}
	})
	if _, err := s.SpawnRecorded(cmd, DeviceStream); !errors.Is(err, ErrUnrecordable) {
		t.Fatalf("SpawnRecorded err = %v, want ErrUnrecordable", err)
	}
	if childPid == 0 {
		t.Fatal("the command never named its child")
	}
	eventually(t, 2*time.Second, "the client gone", func() bool { return dead(cmd.Process.Pid) })
	time.Sleep(100 * time.Millisecond)
	if dead(childPid) {
		t.Errorf("the forked child (pid %d) was killed: a device-stream client's group must be spared", childPid)
	}
}
