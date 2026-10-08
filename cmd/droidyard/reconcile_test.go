package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/platform"
	proc "github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/process/processtest"
	"github.com/jtortosasen/droidyard/internal/store"
)

// gone: the pid is dead and reaped, as the kernel sees it.
func gone(pid int) bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }

// TestServeReapsOrphansBeforeItAnswers: a daemon starting on a previous one's pid
// records stops that daemon's orphans, and only them, before its socket answers
// (ADR-0021 §Pid records; plan m1c step 2, R11). Real processes, each in its own
// group and reaped by the test as soon as it exits:
//   - A dies on SIGTERM and has a grandchild in its group: the group is signalled, not
//     the pid;
//   - C1 and C2 ignore SIGTERM: one wait of 5 s for both, then SIGKILL to each group;
//   - B is alive but recorded with a start time one second off, standing in for a
//     process that reused a recorded pid: never signalled;
//   - a record of a pid already reaped: dropped.
func TestServeReapsOrphansBeforeItAnswers(t *testing.T) {
	// It waits out proc.TermGrace (5 s) and shares nothing with other tests: its state
	// directory, helpers and daemon are its own.
	t.Parallel()
	state := shortDir(t)
	bTerm := filepath.Join(t.TempDir(), "b-sigterm")

	a := processtest.Start(t, processtest.TermExits)
	grandchild, err := strconv.Atoi(a.Ready)
	if err != nil {
		t.Fatalf("A's ready line named no grandchild: %q", a.Ready)
	}
	c1 := processtest.Start(t, processtest.TermIgnored)
	c2 := processtest.Start(t, processtest.TermIgnored)
	b := processtest.Start(t, processtest.TermRecorded, processtest.FileEnv+"="+bTerm)
	reaped := processtest.Start(t, processtest.Sleep)

	record := func(id string, p *processtest.Proc, offset int64) proc.Record {
		started, err := platform.StartTime(p.Pid)
		if err != nil {
			t.Fatalf("start time of %s: %v", id, err)
		}
		return proc.Record{ID: id, Kind: proc.SharedSidecar, PID: p.Pid, PGID: p.Pid,
			StartTime: started + offset, Command: []string{"helper", id}}
	}
	const oneSecond = 1_000_000 // microseconds, macOS p_starttime
	recs := []proc.Record{
		record("a", a, 0), record("c1", c1, 0), record("c2", c2, 0),
		record("b", b, oneSecond), record("reaped", reaped, 0),
	}
	syscall.Kill(reaped.Pid, syscall.SIGKILL)
	<-reaped.Done()
	st, err := store.New(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write(proc.RecordsFile, proc.Records{Processes: recs}); err != nil {
		t.Fatal(err)
	}

	env := platform.Env{GOOS: platform.Current().GOOS, UID: os.Getuid(), Getenv: func(k string) string {
		if k == "DROIDYARD_STATE_DIR" {
			return state
		}
		return os.Getenv(k)
	}}
	sock := env.SocketPath(state)

	started := time.Now()
	srv := droidyard(t, state, "serve")
	var answered time.Time
	for deadline := started.Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if conn, err := net.Dial("unix", sock); err == nil {
			answered = time.Now()
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the socket did not answer within 15 s")
		}
	}
	// What was alive at the moment the socket first answered.
	for name, pid := range map[string]int{"A": a.Pid, "A's grandchild": grandchild, "C1": c1.Pid, "C2": c2.Pid} {
		if !gone(pid) {
			t.Errorf("%s (pid %d) was alive when the socket first answered", name, pid)
		}
	}
	if gone(b.Pid) {
		t.Errorf("B (pid %d), a process that is not ours, was killed", b.Pid)
	}

	if after := answered.Sub(started); after >= 7*time.Second {
		t.Errorf("the socket first answered %s after serve started; one wait for every orphan answers within 7 s", after)
	}
	for name, p := range map[string]*processtest.Proc{"C1": c1, "C2": c2} {
		select {
		case <-p.Done():
			if died := p.ExitedAt().Sub(started); died < proc.TermGrace {
				t.Errorf("%s died %s after serve started, before the %s grace", name, died, proc.TermGrace)
			}
		default:
			t.Errorf("%s is still running", name)
		}
	}
	if got, err := os.ReadFile(bTerm); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("B received SIGTERM: %q (%v)", got, err)
	}

	var left proc.Records
	if out, err := st.Read(proc.RecordsFile, proc.RecordsVersion, &left); out != store.Loaded || err != nil || left.Processes == nil || len(left.Processes) != 0 {
		t.Errorf("processes.json after reconciling: %v, %v, %+v; want an empty list", out, err, left)
	}

	var log []string
	for {
		l := srv.expect(t, "droidyard")
		log = append(log, l)
		if strings.Contains(l, "holding") {
			break
		}
	}
	logged := func(id, what string) bool {
		for _, l := range log {
			if strings.Contains(l, `record "`+id+`" `) && strings.Contains(l, what) {
				return true
			}
		}
		return false
	}
	for _, id := range []string{"a", "c1", "c2"} {
		if !logged(id, ": reaped") {
			t.Errorf("serve's log does not name %s as reaped:\n%s", id, strings.Join(log, "\n"))
		}
	}
	for _, id := range []string{"c1", "c2"} {
		if !logged(id, ": killed") {
			t.Errorf("serve's log does not name %s as killed:\n%s", id, strings.Join(log, "\n"))
		}
	}
	if logged("a", ": killed") {
		t.Error("A honoured SIGTERM, yet the log says it was killed")
	}
	if !logged("b", "dropped, the pid is another process") || !logged("reaped", "dropped, no such process") {
		t.Errorf("serve's log does not say why B and the reaped record were dropped:\n%s", strings.Join(log, "\n"))
	}

	srv.cmd.Process.Signal(syscall.SIGTERM)
	if code := srv.wait(t); code != 0 {
		t.Errorf("serve exited %d on SIGTERM", code)
	}
	t.Logf("socket first answered %s after serve started; C1 reaped at %s, C2 at %s; serve's log:\n%s",
		answered.Sub(started), c1.ExitedAt().Sub(started), c2.ExitedAt().Sub(started), strings.Join(log, "\n"))
}
