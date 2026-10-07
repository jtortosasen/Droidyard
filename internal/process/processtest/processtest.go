// Package processtest is the processes that tests of reconciliation and of the
// supervisor start: the test binary re-executed in a mode named in its environment
// (plan m1c, R17). Only tests import it, so no mode is ever in the shipped binary.
package processtest

import (
	"bufio"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ModeEnv names the mode; FileEnv a file the mode appends what it observes to.
const (
	ModeEnv = "DROIDYARD_TEST_PROCESS"
	FileEnv = "DROIDYARD_TEST_PROCESS_FILE"
)

// The modes. Each prints "ready" on stdout, followed by what the test needs to know,
// once it is set up.
const (
	// Sleep runs until it is signalled.
	Sleep = "sleep"
	// TermExits dies on SIGTERM, having started a Sleep grandchild in its own process
	// group; its ready line ends with the grandchild's pid.
	TermExits = "term-exits"
	// TermExitsOwnGroup dies on SIGTERM, having started a Sleep helper in a process group
	// of its own, as `adb` forks the shared server; its ready line ends with the helper's
	// pid. Reaping the client's group must leave that helper running (plan m1i D9).
	TermExitsOwnGroup = "term-exits-own-group"
	// TermIgnored ignores SIGTERM.
	TermIgnored = "term-ignored"
	// TermRecorded appends "SIGTERM" to FileEnv's file on each SIGTERM and runs on.
	TermRecorded = "term-recorded"
)

// lifetime bounds a mode's life, so a helper a crashed test leaves behind still ends.
const lifetime = 2 * time.Minute

// Main runs the mode the environment names and exits. With no mode it returns at
// once: call it first in TestMain.
func Main() {
	mode := os.Getenv(ModeEnv)
	if mode == "" {
		return
	}
	if mode == Sidecar {
		runSidecar()
		os.Exit(0)
	}
	ready := "ready"
	switch mode {
	case Sleep:
	case TermExits:
		gc := exec.Command(os.Args[0])
		gc.Env = append(os.Environ(), ModeEnv+"="+Sleep)
		if err := gc.Start(); err != nil {
			os.Stderr.WriteString("processtest: " + err.Error() + "\n")
			os.Exit(2)
		}
		ready += " " + strconv.Itoa(gc.Process.Pid)
	case TermExitsOwnGroup:
		gc := exec.Command(os.Args[0])
		gc.Env = append(os.Environ(), ModeEnv+"="+Sleep)
		gc.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		// Detached from this process's stdio, as the adb server is from its client's: the
		// helper must not hold the client's output pipe open past the client's death.
		devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			os.Stderr.WriteString("processtest: " + err.Error() + "\n")
			os.Exit(2)
		}
		gc.Stdout, gc.Stderr = devnull, devnull
		if err := gc.Start(); err != nil {
			os.Stderr.WriteString("processtest: " + err.Error() + "\n")
			os.Exit(2)
		}
		ready += " " + strconv.Itoa(gc.Process.Pid)
	case TermIgnored:
		signal.Ignore(syscall.SIGTERM)
	case TermRecorded:
		ch := make(chan os.Signal, 4)
		signal.Notify(ch, syscall.SIGTERM)
		go func() {
			for range ch {
				f, err := os.OpenFile(os.Getenv(FileEnv), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
				if err == nil {
					f.WriteString("SIGTERM\n")
					f.Close()
				}
			}
		}()
	default:
		os.Stderr.WriteString("processtest: unknown mode " + mode + "\n")
		os.Exit(2)
	}
	os.Stdout.WriteString(ready + "\n")
	time.Sleep(lifetime)
	os.Exit(0)
}

// Proc is a running helper, in a process group of its own and reaped by the test as
// soon as it exits, so a dead helper never lingers as a zombie whose pid still
// answers kill -0 and still has a start time.
type Proc struct {
	Cmd *exec.Cmd
	Pid int
	// Ready is what followed "ready" on the helper's first line.
	Ready string

	done     chan struct{}
	mu       sync.Mutex
	exitedAt time.Time
}

// Start starts the test binary in mode with extra environment, waits for its ready
// line, and kills its group when the test ends.
func Start(t testing.TB, mode string, env ...string) *Proc {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(append(os.Environ(), ModeEnv+"="+mode), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &Proc{Cmd: cmd, Pid: cmd.Process.Pid, done: make(chan struct{})}
	t.Cleanup(func() {
		syscall.Kill(-p.Pid, syscall.SIGKILL)
		<-p.done
	})
	line := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(out)
		sc.Scan()
		line <- sc.Text()
		for sc.Scan() {
		}
		cmd.Wait()
		p.mu.Lock()
		p.exitedAt = time.Now()
		p.mu.Unlock()
		close(p.done)
	}()
	select {
	case l := <-line:
		rest, ok := strings.CutPrefix(l, "ready")
		if !ok {
			t.Fatalf("helper %s (pid %d) printed %q, not ready", mode, p.Pid, l)
		}
		p.Ready = strings.TrimSpace(rest)
	case <-time.After(10 * time.Second):
		t.Fatalf("helper %s (pid %d) was not ready within 10 s", mode, p.Pid)
	}
	return p
}

// Done is closed once the helper has exited and been reaped.
func (p *Proc) Done() <-chan struct{} { return p.done }

// ExitedAt is when the helper was reaped; zero while it runs.
func (p *Proc) ExitedAt() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitedAt
}
