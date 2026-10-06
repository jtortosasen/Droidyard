package main

import (
	"bufio"
	"bytes"
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

	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/process/processtest"
)

// reexec makes the test binary run main instead of the tests, so a test can start
// real droidyard processes without building the binary first.
const reexec = "DROIDYARD_TEST_REEXEC"

func TestMain(m *testing.M) {
	processtest.Main()
	devicetest.Main() // a fake adb, when a test names a scenario directory
	// No test of this package may find or run the machine's adb, or reach the shared
	// adb server (plan m1e, step 2): an environment with no ADB, no SDK, an empty PATH.
	adbEnviron = func() []string { return []string{"HOME=/nonexistent-droidyard-test", "PATH="} }
	if os.Getenv(reexec) == "1" {
		installTestSeams()
		dieWithTheTest()
		main()
		os.Exit(0)
	}
	// Every process a test re-executes inherits this, detached daemons included.
	os.Setenv(testParentEnv, strconv.Itoa(os.Getpid()))
	os.Exit(m.Run())
}

// testParentEnv names the test binary's pid for the processes it re-executes.
const testParentEnv = "DROIDYARD_TEST_PARENT"

// dieWithTheTest ends a re-executed droidyard once the test binary that started it is
// gone. A test whose binary is killed (a -timeout, a stopped run) never reaches the
// cleanup that stops its daemon, and a detached daemon would otherwise outlive it
// indefinitely. SIGTERM, so a daemon shuts down as it would on a stop.
func dieWithTheTest() {
	pid, err := strconv.Atoi(os.Getenv(testParentEnv))
	if err != nil || pid <= 1 {
		return
	}
	go func() {
		for {
			time.Sleep(500 * time.Millisecond)
			if syscall.Kill(pid, 0) == syscall.ESRCH {
				syscall.Kill(os.Getpid(), syscall.SIGTERM)
				return
			}
		}
	}()
}

// shortDir is a state root under /tmp, short enough that the socket stays inside it
// rather than moving to the sun_path fallback under /tmp/droidyard-<uid>/.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dyc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// process is a re-executed droidyard: its stdout, and its stderr line by line.
type process struct {
	cmd    *exec.Cmd
	stdout *bytes.Buffer // read it only after wait
	lines  chan string
	exited chan error
	stderr lockedBuffer // every stderr line, whether or not a test read it
}

func droidyard(t *testing.T, state string, args ...string) *process {
	t.Helper()
	return droidyardIn(t, state, "", args...)
}

// droidyardIn is droidyard run with dir as its working directory; "" keeps the
// test's.
func droidyardIn(t *testing.T, state, dir string, args ...string) *process {
	t.Helper()
	return droidyardEnv(t, state, dir, nil, args...)
}

// droidyardEnv is droidyardIn with more environment, such as the test seams.
func droidyardEnv(t *testing.T, state, dir string, env []string, args ...string) *process {
	t.Helper()
	return droidyardProc(t, append(append(os.Environ(), reexec+"=1", "DROIDYARD_STATE_DIR="+state), env...), dir, args...)
}

// droidyardProc is droidyard started with exactly env, for a test that must control
// variables (HOME, say) the inherited environment already sets, since a duplicate in
// an environment is resolved to its first occurrence.
func droidyardProc(t *testing.T, env []string, dir string, args ...string) *process {
	t.Helper()
	cmd := &exec.Cmd{
		Path: os.Args[0],
		Args: append([]string{"droidyard"}, args...),
		Env:  env,
		Dir:  dir,
	}
	stdout := new(bytes.Buffer)
	cmd.Stdout = stdout
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &process{cmd: cmd, stdout: stdout, lines: make(chan string, 64), exited: make(chan error, 1)}
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			p.stderr.Write([]byte(sc.Text() + "\n"))
			p.lines <- sc.Text()
		}
		close(p.lines)
		p.exited <- cmd.Wait()
	}()
	t.Cleanup(func() { cmd.Process.Kill() }) // after Wait, a no-op
	return p
}

// expect waits for a stderr line containing want.
func (p *process) expect(t *testing.T, want string) string {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case l, ok := <-p.lines:
			if !ok {
				t.Fatalf("pid %d exited before printing %q", p.cmd.Process.Pid, want)
			}
			if strings.Contains(l, want) {
				return l
			}
		case <-timeout:
			t.Fatalf("pid %d printed no %q within 10 s", p.cmd.Process.Pid, want)
		}
	}
}

// wait returns the exit code once the process ends, failing after 10 s.
func (p *process) wait(t *testing.T) int {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for lines := p.lines; lines != nil; {
		select {
		case _, ok := <-lines:
			if !ok {
				lines = nil
			}
		case <-timeout:
			t.Fatalf("pid %d did not exit within 10 s", p.cmd.Process.Pid)
		}
	}
	select {
	case err := <-p.exited:
		var exit *exec.ExitError
		if err == nil {
			return 0
		}
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		t.Fatalf("waiting for pid %d: %v", p.cmd.Process.Pid, err)
	case <-timeout:
		t.Fatalf("pid %d did not exit within 10 s", p.cmd.Process.Pid)
	}
	return -1
}

// TestSecondServeLosesTheLock: one daemon per state root, enforced by an exclusive
// flock on daemon.lock whose contents name the holder (ADR-0019 §5, ADR-0021
// §Locking). Real processes and a real flock: a second serve on the same state exits
// non-zero without touching the holder's record; the kernel releases the lock when
// the holder is killed; a signalled daemon releases it and exits 0.
func TestSecondServeLosesTheLock(t *testing.T) {
	t.Parallel()
	state := shortDir(t)

	first := droidyard(t, state, "serve")
	first.expect(t, "holding")

	raw, err := os.ReadFile(filepath.Join(state, daemon.LockFile))
	if err != nil {
		t.Fatal(err)
	}
	info, err := daemon.ReadLock(state)
	if err != nil {
		t.Fatal(err)
	}
	env := platform.Env{GOOS: platform.Current().GOOS, UID: os.Getuid(), Getenv: func(k string) string {
		if k == "DROIDYARD_STATE_DIR" {
			return state
		}
		return os.Getenv(k)
	}}
	if info.PID != first.cmd.Process.Pid || info.Version != system.Version || info.Socket != env.SocketPath(state) || info.StartTime.IsZero() {
		t.Fatalf("daemon.lock says %+v; want pid %d, version %s, socket %s and a start time", info, first.cmd.Process.Pid, system.Version, env.SocketPath(state))
	}
	// The lock names the UI's address by the time serve listens (plan m1d, step 5).
	if info.HTTP == nil || !regexp.MustCompile(`^127\.0\.0\.1:\d+$`).MatchString(*info.HTTP) || !bytes.Contains(raw, []byte(`"http":"127.0.0.1:`)) {
		t.Errorf("daemon.lock names no loopback HTTP address once serve listens: %s", raw)
	}

	second := droidyard(t, state, "serve")
	lost := second.expect(t, "holds the lock")
	if code := second.wait(t); code == 0 {
		t.Fatal("a second serve on the same state exited 0; it must lose the lock and exit non-zero")
	}
	if !strings.Contains(lost, "pid "+strconv.Itoa(first.cmd.Process.Pid)) {
		t.Errorf("the losing serve did not name the holder: %q", lost)
	}
	if again, err := daemon.ReadLock(state); err != nil || again.PID != first.cmd.Process.Pid {
		t.Fatalf("after the loser exited, daemon.lock says (%+v, %v); want the first pid, %d", again, err, first.cmd.Process.Pid)
	}

	// A killed holder releases the lock: the kernel does it, no cleanup runs.
	first.cmd.Process.Signal(syscall.SIGKILL)
	first.wait(t)
	third := droidyard(t, state, "serve")
	third.expect(t, "holding")
	if info, err := daemon.ReadLock(state); err != nil || info.PID != third.cmd.Process.Pid {
		t.Fatalf("after the holder was killed, daemon.lock says (%+v, %v); want pid %d", info, err, third.cmd.Process.Pid)
	}

	// SIGTERM: the daemon releases the lock and exits 0.
	third.cmd.Process.Signal(syscall.SIGTERM)
	if code := third.wait(t); code != 0 {
		t.Fatalf("serve exited %d on SIGTERM; want 0", code)
	}
	lock, err := daemon.Acquire(state, daemon.LockInfo{PID: os.Getpid()})
	if err != nil {
		t.Fatalf("the lock was not released after SIGTERM: %v", err)
	}
	lock.Release()
}

// TestServeStartsThroughAProbe: a `serve` that starts while `status` or `stop` holds
// their shared probe lock on daemon.lock must still take the lock, since no daemon
// exists (ADR-0019 §5). Real processes: the test holds LOCK_SH, starts serve, watches
// serve open daemon.lock, holds 20 ms more, and releases. It skips without lsof;
// TestAcquireOutlastsAHolderProbe covers the fix in process everywhere.
func TestServeStartsThroughAProbe(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof is not installed: the test cannot see serve open daemon.lock")
	}
	state := shortDir(t)
	probe, err := os.OpenFile(filepath.Join(state, daemon.LockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	fd := int(probe.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	s := droidyard(t, state, "serve")
	pid := strconv.Itoa(s.cmd.Process.Pid)
	for deadline := time.Now().Add(10 * time.Second); ; {
		out, _ := exec.Command("lsof", "-a", "-p", pid, "-Fn").Output()
		if strings.Contains(string(out), "/"+daemon.LockFile+"\n") {
			break
		}
		select {
		case line, ok := <-s.lines:
			// Serve prints nothing until it holds the lock, which the probe forbids.
			syscall.Flock(fd, syscall.LOCK_UN)
			t.Fatalf("serve gave up while the probe held the lock (exited: %v): %s", !ok, line)
		default:
		}
		if time.Now().After(deadline) {
			syscall.Flock(fd, syscall.LOCK_UN)
			t.Fatal("serve never opened daemon.lock while the probe held it")
		}
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	syscall.Flock(fd, syscall.LOCK_UN)

	s.expect(t, "holding")
	s.cmd.Process.Signal(syscall.SIGTERM)
	if code := s.wait(t); code != 0 {
		t.Errorf("serve exited %d on SIGTERM", code)
	}
}
