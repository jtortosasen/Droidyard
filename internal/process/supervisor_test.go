package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/process/processtest"
	"github.com/jtortosasen/droidyard/internal/store"
)

func TestMain(m *testing.M) {
	processtest.Main()
	os.Exit(m.Run())
}

// dead: no process holds pid, as the kernel sees it.
func dead(pid int) bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }

func newSupervisor(t *testing.T) (*Supervisor, *store.Store) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Store: st, Log: testLog{t}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Shutdown)
	return s, st
}

type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// dummy describes processtest's sidecar with opts, and returns its event file.
func dummy(t *testing.T, id string, scope Scope, opts ...string) (Descriptor, string) {
	t.Helper()
	events := filepath.Join(t.TempDir(), "events")
	cmd, env := processtest.SidecarCommand(events, opts...)
	kind := SharedSidecar
	if scope != Global {
		kind = WorkspaceSidecar
	}
	return Descriptor{
		ID: id, Kind: kind, Command: cmd, Env: env, Scope: scope,
		Ready:    func(line string) bool { return line == "ready" },
		Shutdown: ShutdownPolicy{Signal: syscall.SIGTERM, Timeout: 300 * time.Millisecond},
		LogLines: 50,
	}, events
}

func events(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Fields(strings.ReplaceAll(string(raw), " ", "_"))
}

func onDisk(t *testing.T, st *store.Store) []Record {
	t.Helper()
	var recs Records
	if _, err := st.Read(RecordsFile, store.Version, &recs); err != nil {
		t.Fatal(err)
	}
	return recs.Processes
}

func use(t *testing.T, s *Supervisor, d Descriptor) *Lease {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	l, err := s.Use(ctx, d)
	if err != nil {
		t.Fatalf("Use(%s): %v", d.ID, err)
	}
	return l
}

func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(within); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("not within %s: %s", within, what)
		}
	}
}

// helperOf is the pid of the helper the dummy forked into its group.
func helperOf(t *testing.T, path string) int {
	t.Helper()
	for _, e := range events(path) {
		if pid, ok := strings.CutPrefix(e, "helper_"); ok {
			n, _ := strconv.Atoi(pid)
			return n
		}
	}
	t.Fatalf("the dummy forked no helper: %v", events(path))
	return 0
}

// TestUseStartsLazilyAndRecordsAtOnce: nothing runs and nothing is recorded before
// the first Use; the pid record is written as soon as the pid is known, before the
// process is ready, with the kernel's start time of that pid; Use returns after the
// ready line (process-lifecycle.md §Startup; ADR-0021 §Pid records).
func TestUseStartsLazilyAndRecordsAtOnce(t *testing.T) {
	s, st := newSupervisor(t)
	gate := filepath.Join(t.TempDir(), "go")
	d, ev := dummy(t, "lazy", Global, "ready-after="+gate)
	if s.State(d.ID, d.Scope) != "" || len(onDisk(t, st)) != 0 || events(ev) != nil {
		t.Fatal("something ran or was recorded before the first Use")
	}

	used := make(chan *Lease, 1)
	go func() {
		l, err := s.Use(context.Background(), d)
		if err != nil {
			t.Error(err)
		}
		used <- l
	}()
	eventually(t, 5*time.Second, "a record while the process starts", func() bool { return len(onDisk(t, st)) == 1 })
	rec := onDisk(t, st)[0]
	if s.State(d.ID, d.Scope) != Starting {
		t.Errorf("state %s when the record appeared; want starting", s.State(d.ID, d.Scope))
	}
	select {
	case <-used:
		t.Fatal("Use returned before the ready line")
	default:
	}
	started, err := platform.StartTime(rec.PID)
	if err != nil || started != rec.StartTime || rec.PGID != rec.PID || rec.ID != "lazy" || rec.Kind != SharedSidecar {
		t.Errorf("record %+v; the live pid's start time is %d (%v)", rec, started, err)
	}

	os.WriteFile(gate, nil, 0o600)
	l := <-used
	if l == nil || l.Pid() != rec.PID || s.State(d.ID, d.Scope) != Busy {
		t.Fatalf("lease %v, state %s; want pid %d, busy", l, s.State(d.ID, d.Scope), rec.PID)
	}
	eventually(t, 5*time.Second, "the dummy's ready event", func() bool {
		e := events(ev)
		return len(e) > 0 && e[0] == "ready"
	})
}

// TestIdleShutdownRespectsLeases: a held lease keeps the process past its idle
// timeout; released, it stops within the timeout plus a margin, its record removed;
// the next Use starts a new process (process-lifecycle.md §Idle shutdown).
func TestIdleShutdownRespectsLeases(t *testing.T) {
	s, st := newSupervisor(t)
	d, _ := dummy(t, "idle", Global)
	d.IdleTimeout = 300 * time.Millisecond
	l := use(t, s, d)
	pid := l.Pid()

	time.Sleep(3 * d.IdleTimeout)
	if dead(pid) || s.State(d.ID, d.Scope) != Busy || len(onDisk(t, st)) != 1 {
		t.Fatalf("with a lease held past the idle timeout: dead %v, state %s", dead(pid), s.State(d.ID, d.Scope))
	}
	released := time.Now()
	l.Release()
	eventually(t, d.IdleTimeout+time.Second, "an idle stop", func() bool { return s.State(d.ID, d.Scope) == Stopped })
	if took := time.Since(released); took < d.IdleTimeout {
		t.Errorf("stopped %s after the release, before the idle timeout", took)
	}
	if !dead(pid) || len(onDisk(t, st)) != 0 {
		t.Errorf("after the idle stop: dead %v, records %v", dead(pid), onDisk(t, st))
	}

	again := use(t, s, d)
	if again.Pid() == pid || dead(again.Pid()) {
		t.Errorf("the next Use got pid %d; want a new live one, not %d", again.Pid(), pid)
	}
}

// TestStopKillsTheWholeGroup: an explicit stop ignores leases; a dummy that ignores
// SIGTERM and stdin EOF, with a helper that ignores SIGTERM in its group, is gone with
// its helper once the shutdown timeout has passed, its record removed
// (process-lifecycle.md §Shutdown, cancellation and no-orphans).
func TestStopKillsTheWholeGroup(t *testing.T) {
	s, st := newSupervisor(t)
	d, ev := dummy(t, "stubborn", WorkspaceScope("a"), "ignore-term", "ignore-eof", "fork-helper")
	l := use(t, s, d)
	pid, helper := l.Pid(), helperOf(t, ev)

	began := time.Now()
	s.Stop(WorkspaceScope("a"))
	took := time.Since(began)
	if !dead(pid) || !dead(helper) {
		t.Fatalf("after Stop: dummy dead %v, helper dead %v", dead(pid), dead(helper))
	}
	if took < d.Shutdown.Timeout {
		t.Errorf("Stop returned after %s, before the shutdown timeout", took)
	}
	if s.State(d.ID, d.Scope) != Stopped || len(onDisk(t, st)) != 0 {
		t.Errorf("state %s, records %v", s.State(d.ID, d.Scope), onDisk(t, st))
	}
	if !strings.Contains(strings.Join(events(ev), " "), "SIGTERM") {
		t.Errorf("the graceful signal never reached it: %v", events(ev))
	}
	l.Release() // after the stop: does nothing
}

// TestStopIsScoped: Stop(workspace:a) stops a's process only; workspace b's and the
// global one keep their pids and records.
func TestStopIsScoped(t *testing.T) {
	s, st := newSupervisor(t)
	da, _ := dummy(t, "lsp", WorkspaceScope("a"))
	db, _ := dummy(t, "lsp", WorkspaceScope("b"))
	dg, _ := dummy(t, "gradle", Global)
	a, b, g := use(t, s, da), use(t, s, db), use(t, s, dg)

	s.Stop(WorkspaceScope("a"))
	if !dead(a.Pid()) || s.State(da.ID, da.Scope) != Stopped {
		t.Errorf("workspace a's process: dead %v, state %s", dead(a.Pid()), s.State(da.ID, da.Scope))
	}
	for name, l := range map[string]*Lease{"workspace b": b, "global": g} {
		if dead(l.Pid()) {
			t.Errorf("%s's process was stopped", name)
		}
	}
	if s.Pid(db.ID, db.Scope) != b.Pid() || s.Pid(dg.ID, dg.Scope) != g.Pid() {
		t.Error("a survivor's pid changed")
	}
	kept := map[int]bool{}
	for _, r := range onDisk(t, st) {
		kept[r.PID] = true
	}
	if len(kept) != 2 || !kept[b.Pid()] || !kept[g.Pid()] {
		t.Errorf("records %v; want b's and the global one", onDisk(t, st))
	}
	if r := onDisk(t, st); len(r) == 2 && (r[0].Workspace == "" && r[1].Workspace == "") {
		t.Errorf("no record names its workspace: %v", r)
	}
}

// TestUseWithADoneContextSpawnsNothing: an operation cancelled by a close that runs
// on past the drain cannot start a sidecar again (ADR-0027).
func TestUseWithADoneContextSpawnsNothing(t *testing.T) {
	s, st := newSupervisor(t)
	d, ev := dummy(t, "late", WorkspaceScope("a"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if l, err := s.Use(ctx, d); err == nil || l != nil {
		t.Fatalf("Use with a done context = %v, %v; want an error", l, err)
	}
	time.Sleep(300 * time.Millisecond)
	if events(ev) != nil || len(onDisk(t, st)) != 0 || s.State(d.ID, d.Scope) != "" {
		t.Errorf("something was spawned: events %v, records %v, state %q", events(ev), onDisk(t, st), s.State(d.ID, d.Scope))
	}
}

// TestUnexpectedExitFails: a process killed from outside becomes failed with its
// record removed, and the next Use starts it afresh.
func TestUnexpectedExitFails(t *testing.T) {
	s, st := newSupervisor(t)
	d, _ := dummy(t, "crashy", Global)
	l := use(t, s, d)
	syscall.Kill(l.Pid(), syscall.SIGKILL)
	eventually(t, 5*time.Second, "failed", func() bool { return s.State(d.ID, d.Scope) == Failed })
	eventually(t, 5*time.Second, "its record removed", func() bool { return len(onDisk(t, st)) == 0 })
	l.Release()

	again := use(t, s, d)
	if again.Pid() == l.Pid() || s.State(d.ID, d.Scope) != Busy || len(onDisk(t, st)) != 1 {
		t.Errorf("after a crash, Use got pid %d (was %d), state %s", again.Pid(), l.Pid(), s.State(d.ID, d.Scope))
	}
}

// TestNeverReadyFails: a process that exits before its ready line fails Use and
// leaves no record.
func TestNeverReadyFails(t *testing.T) {
	s, st := newSupervisor(t)
	d := Descriptor{ID: "false", Kind: SharedSidecar, Command: "/usr/bin/false", Scope: Global,
		Ready: func(string) bool { return true }, Shutdown: ShutdownPolicy{Timeout: time.Second}}
	if _, err := s.Use(context.Background(), d); err == nil {
		t.Fatal("Use of a process that exits at once succeeded")
	}
	if s.State(d.ID, d.Scope) != Failed || len(onDisk(t, st)) != 0 {
		t.Errorf("state %s, records %v", s.State(d.ID, d.Scope), onDisk(t, st))
	}
}

// TestShutdownStopsEverything: every process, leased or not, and no Use after.
func TestShutdownStopsEverything(t *testing.T) {
	s, st := newSupervisor(t)
	d1, _ := dummy(t, "one", Global)
	d2, _ := dummy(t, "two", WorkspaceScope("a"), "ignore-term")
	l1, l2 := use(t, s, d1), use(t, s, d2)
	l1.Release()
	s.Shutdown()
	if !dead(l1.Pid()) || !dead(l2.Pid()) || len(onDisk(t, st)) != 0 {
		t.Errorf("after Shutdown: dead %v %v, records %v", dead(l1.Pid()), dead(l2.Pid()), onDisk(t, st))
	}
	if _, err := s.Use(context.Background(), d1); !errors.Is(err, ErrClosed) {
		t.Errorf("Use after Shutdown: %v; want ErrClosed", err)
	}
}

// TestOutputIsDrainedAndBounded: a process writing megabytes is never blocked on its
// pipe, and its log sink keeps no more than its bound (process-lifecycle.md §Output
// handling).
func TestOutputIsDrainedAndBounded(t *testing.T) {
	s, _ := newSupervisor(t)
	d, ev := dummy(t, "noisy", Global, "flood=8000000")
	use(t, s, d)
	eventually(t, 10*time.Second, "the dummy wrote 8 MB", func() bool {
		return strings.Contains(strings.Join(events(ev), " "), "flooded")
	})
	out := s.Output(d.ID, d.Scope)
	if len(out) == 0 || out[len(out)-1] != strings.Repeat("x", 99) {
		t.Fatalf("the sink's last line is not the dummy's output: %d lines", len(out))
	}
	s.mu.Lock()
	sk := s.procs[key(d.ID, d.Scope)].sink
	s.mu.Unlock()
	sk.mu.Lock()
	held := len(sk.lines)
	sk.mu.Unlock()
	if held > d.LogLines {
		t.Errorf("the sink holds %d lines; its bound is %d", held, d.LogLines)
	}
}

// TestNewerRecordsFileSpawnsNothing: a processes.json of a newer version cannot be
// written, so no process may be started: Use fails and the file is untouched (plan
// m1c, R10).
func TestNewerRecordsFileSpawnsNothing(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	newer := []byte(`{"schemaVersion":2,"processes":[]}`)
	path := filepath.Join(st.Root(), RecordsFile)
	os.WriteFile(path, newer, 0o600)
	s, err := New(Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Shutdown)
	d, ev := dummy(t, "blocked", Global)
	if _, err := s.Use(context.Background(), d); !errors.Is(err, ErrUnrecordable) {
		t.Fatalf("Use with a newer processes.json: %v; want ErrUnrecordable", err)
	}
	time.Sleep(300 * time.Millisecond)
	if raw, _ := os.ReadFile(path); string(raw) != string(newer) || events(ev) != nil || s.State(d.ID, d.Scope) != "" {
		t.Errorf("file %q, events %v, state %q: something was spawned", raw, events(ev), s.State(d.ID, d.Scope))
	}
}

// TestConcurrentUsesShareOneProcess: uses that race for a process not yet running
// start it once.
func TestConcurrentUsesShareOneProcess(t *testing.T) {
	s, st := newSupervisor(t)
	d, _ := dummy(t, "shared", Global)
	var pids [8]atomic.Int64
	done := make(chan struct{})
	for i := range pids {
		go func() {
			defer func() { done <- struct{}{} }()
			l, err := s.Use(context.Background(), d)
			if err == nil {
				pids[i].Store(int64(l.Pid()))
			}
		}()
	}
	for range pids {
		<-done
	}
	for i := range pids {
		if pids[i].Load() == 0 || pids[i].Load() != pids[0].Load() {
			t.Fatalf("use %d got pid %d, use 0 pid %d; want one process", i, pids[i].Load(), pids[0].Load())
		}
	}
	if len(onDisk(t, st)) != 1 {
		t.Errorf("records %v; want one", onDisk(t, st))
	}
}
