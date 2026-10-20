package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

var startLine = regexp.MustCompile(`droidyard serve \(pid (\d+)\): starting`)

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// TestRacingAutoStarts: with no daemon running, shims start one detached, and racing
// starts leave exactly one (ADR-0019 §4, §5). Real processes and sessions: two
// concurrent `call system.health` without --no-autostart in one process group, on an
// empty state; the winner leads its own session, logs to daemon.log from cwd /, and
// survives the callers' group being killed; every loser logged its loss and is gone.
func TestRacingAutoStarts(t *testing.T) {
	state := shortDir(t)
	env := append(os.Environ(), reexec+"=1", "DROIDYARD_STATE_DIR="+state)
	t.Cleanup(func() {
		if info, err := daemon.ReadLock(state); err == nil && info.PID > 0 && alive(info.PID) {
			syscall.Kill(info.PID, syscall.SIGKILL)
		}
	})

	// The callers' process group, led by a process that outlives them.
	leader := exec.Command("/bin/sleep", "60")
	leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := leader.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { leader.Process.Kill(); leader.Wait() })

	type caller struct {
		cmd      *exec.Cmd
		out, err bytes.Buffer
		done     chan error
	}
	var callers []*caller
	for range 2 {
		c := &caller{done: make(chan error, 1)}
		c.cmd = &exec.Cmd{
			Path:        os.Args[0],
			Args:        []string{"droidyard", "call", "system.health"},
			Env:         env,
			Stdout:      &c.out,
			Stderr:      &c.err,
			SysProcAttr: &syscall.SysProcAttr{Setpgid: true, Pgid: leader.Process.Pid},
		}
		callers = append(callers, c)
	}
	for _, c := range callers {
		if err := c.cmd.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { c.done <- c.cmd.Wait() }()
	}
	for i, c := range callers {
		select {
		case err := <-c.done:
			if err != nil {
				t.Fatalf("caller %d: %v; stderr: %s", i, err, c.err.String())
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("caller %d did not finish within 20 s", i)
		}
		var env capability.ResponseEnvelope
		if err := json.Unmarshal(c.out.Bytes(), &env); err != nil || env.Operation == nil || env.Operation.State != capability.StateCompleted {
			t.Fatalf("caller %d printed %q; want a completed system.health envelope", i, c.out.String())
		}
		var h system.Health
		if json.Unmarshal(env.Result, &h) != nil || h.Version != system.Version {
			t.Fatalf("caller %d's result is %s", i, env.Result)
		}
	}

	// One winner, leading a session of its own, logging to daemon.log, in /.
	info, err := daemon.ReadLock(state)
	if err != nil {
		t.Fatal(err)
	}
	winner := info.PID
	if !alive(winner) {
		t.Fatalf("daemon.lock names pid %d, which is not alive", winner)
	}
	if sid, err := unix.Getsid(winner); err != nil || sid != winner {
		t.Errorf("the daemon %d is in session %d (%v); want a session of its own", winner, sid, err)
	}

	// Every other serve that started loses, after its grace for a probe, then exits.
	logPath := filepath.Join(state, logFile)
	losers := func() (out []int) {
		logged, _ := os.ReadFile(logPath)
		for _, m := range startLine.FindAllStringSubmatch(string(logged), -1) {
			if pid, _ := strconv.Atoi(m[1]); pid != winner {
				out = append(out, pid)
			}
		}
		return out
	}
	testtime.Eventually(t, testtime.Bound(10*time.Second), "every losing serve has exited", func() bool {
		for _, pid := range losers() {
			if alive(pid) {
				return false
			}
		}
		return true
	})
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("daemon.log has %d start line(s):\n%s", len(startLine.FindAllString(string(logged), -1)), logged)
	if !strings.Contains(string(logged), "droidyard serve (pid "+strconv.Itoa(winner)+"): starting") {
		t.Errorf("the winner %d has no start line in daemon.log", winner)
	}
	for _, pid := range losers() {
		if !strings.Contains(string(logged), "droidyard serve (pid "+strconv.Itoa(pid)+"): lost the lock") {
			t.Errorf("pid %d started but logged no loss of the lock", pid)
		}
	}

	// Only these checks need lsof; without it they skip, visibly, and the rest runs.
	t.Run("lsof", func(t *testing.T) {
		if _, err := exec.LookPath("lsof"); err != nil {
			t.Skip("lsof is not installed: the lock holder, cwd and stdio checks are not run")
		}
		lsof := func(args ...string) string {
			out, _ := exec.Command("lsof", args...).Output()
			return string(out)
		}
		realState, _ := filepath.EvalSymlinks(state)
		if held := strings.Fields(lsof("-t", filepath.Join(state, daemon.LockFile))); len(held) != 1 || held[0] != strconv.Itoa(winner) {
			t.Errorf("lsof -t daemon.lock lists %v; want only the winner %d", held, winner)
		}
		if cwd := lsof("-a", "-p", strconv.Itoa(winner), "-d", "cwd", "-Fn"); !strings.Contains(cwd, "\nn/\n") {
			t.Errorf("the daemon's cwd is not /: %q", cwd)
		}
		if out := lsof("-a", "-p", strconv.Itoa(winner), "-d", "1,2", "-Fn"); strings.Count(out, "n"+filepath.Join(realState, logFile)) != 2 {
			t.Errorf("the daemon's stdout and stderr are not daemon.log: %q", out)
		}
	})

	// The callers' process group dies; the daemon, in its own session, does not.
	if err := syscall.Kill(-leader.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	leader.Wait()
	after := droidyard(t, state, "call", "--no-autostart", "system.health")
	if env, code := after.envelope(t); code != 0 || env.Operation == nil || env.Operation.State != capability.StateCompleted {
		t.Fatalf("after the callers' group was killed, call --no-autostart exited %d with %+v", code, env)
	}

	if code := droidyard(t, state, "stop").wait(t); code != 0 {
		t.Fatalf("stop exited %d", code)
	}
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the daemon exits after stop", func() bool { return !alive(winner) })
	if _, err := os.Lstat(filepath.Join(state, "run", "droidyard.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the socket is still there after stop: %v", err)
	}
}

// TestNoAutostartStartsNothing: with --no-autostart, a shim that finds no daemon fails
// and starts none (ADR-0019 §4).
func TestNoAutostartStartsNothing(t *testing.T) {
	state := shortDir(t)
	for _, args := range [][]string{{"call", "--no-autostart", "system.health"}, {"mcp", "--no-autostart"}} {
		p := droidyard(t, state, args...)
		if code := p.wait(t); code == 0 {
			t.Errorf("droidyard %s exited 0 with no daemon", strings.Join(args, " "))
		}
	}
	if _, err := os.Stat(filepath.Join(state, daemon.LockFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("--no-autostart started a daemon: %v", err)
	}
}

// TestMCPShimAutoStarts: `droidyard mcp` without --no-autostart starts the daemon it
// finds missing, and its session works (ADR-0019 §4).
func TestMCPShimAutoStarts(t *testing.T) {
	state := shortDir(t)
	t.Cleanup(func() {
		if info, err := daemon.ReadLock(state); err == nil && info.PID > 0 && alive(info.PID) {
			syscall.Kill(info.PID, syscall.SIGKILL)
		}
	})
	c := startShimArgs(t, state, "client", []string{"mcp"})
	if res := c.initialize("autostart"); res["serverInfo"] == nil {
		t.Fatalf("initialize answered %v", res)
	}
	if code := c.exit(); code != 0 {
		t.Errorf("the shim exited %d; want 0", code)
	}
	if info, held, err := daemon.Holder(state); !held || err != nil || !alive(info.PID) {
		t.Fatalf("no daemon holds the lock after the shim ended: (%+v, %v, %v)", info, held, err)
	}
	if code := droidyard(t, state, "stop").wait(t); code != 0 {
		t.Fatalf("stop exited %d", code)
	}
}
