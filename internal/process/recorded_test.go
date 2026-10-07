package process

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jtortosasen/droidyard/internal/process/processtest"
	"github.com/jtortosasen/droidyard/internal/store"
)

// spawnSleep starts processtest's Sleep mode through SpawnRecorded, the shape a follow's
// client has: a long-lived child the supervisor does not supervise but records (plan m1i
// D9).
func sleepCmd() *exec.Cmd {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), processtest.ModeEnv+"="+processtest.Sleep)
	return cmd
}

// Decision (plan m1i D9; ADR-0021 §Pid records): SpawnRecorded writes the client's pid
// record while it runs — kind device-stream, the pid and its group the child's own, the
// command as run — and removes it when the caller reaps the child. A record left behind
// would be reaped by the next daemon as an orphan, so it must not outlive the process.
// Seen failing reversed (a record kept after Wait): the next start would signal a pid it
// no longer owns.
func TestSpawnRecordedWritesAndRemovesTheRecord(t *testing.T) {
	s, st := newSupervisor(t)
	cmd := sleepCmd()
	r, err := s.SpawnRecorded(cmd, DeviceStream)
	if err != nil {
		t.Fatalf("SpawnRecorded: %v", err)
	}
	recs := onDisk(t, st)
	if len(recs) != 1 {
		t.Fatalf("processes.json holds %d records while the client runs, want 1: %+v", len(recs), recs)
	}
	rec := recs[0]
	if rec.Kind != DeviceStream || rec.ID != string(DeviceStream) {
		t.Errorf("record kind/id = %q/%q, want %q/%q", rec.Kind, rec.ID, DeviceStream, DeviceStream)
	}
	if rec.PID != r.Pid() || rec.PID != cmd.Process.Pid {
		t.Errorf("record pid %d, handle %d, cmd %d; want one pid", rec.PID, r.Pid(), cmd.Process.Pid)
	}
	if rec.PGID != rec.PID {
		t.Errorf("record pgid %d, want the client's own group %d", rec.PGID, rec.PID)
	}
	if rec.StartTime == 0 {
		t.Error("the record carries no start time, so a reused pid could be mistaken for the client")
	}
	if got, want := strings.Join(rec.Command, " "), strings.Join(cmd.Args, " "); got != want {
		t.Errorf("recorded command %q, want %q", got, want)
	}
	if dead(rec.PID) {
		t.Fatal("the recorded client is not running")
	}

	// Only the client's own pid is killed (it forked nothing), then reaped: the record
	// goes with it.
	syscall.Kill(r.Pid(), syscall.SIGKILL)
	if err := r.Wait(); err == nil {
		t.Error("Wait of a killed client returned nil, want its exit error")
	}
	if recs := onDisk(t, st); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after Wait, want none", recs)
	}
}

// Decision (plan m1i D9; plan m1c R10): when processes.json cannot be written, no client
// is started at all — SpawnRecorded refuses before the first fork, so the follow reports
// DEVICE_LOGS_FAILED with nothing left running. Seen failing reversed (a client started
// before the refusal): it would run unrecorded, unseen by the next daemon's Reconcile.
func TestSpawnRecordedRefusesWhenTheRecordCannotBeWritten(t *testing.T) {
	root := t.TempDir()
	// A processes.json of a newer version: Load refuses, and every later write is refused
	// too (store.ErrNewer).
	if err := os.WriteFile(filepath.Join(root, RecordsFile), []byte(`{"schemaVersion":99,"processes":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: st, Log: testLog{t}})
	if err := s.Load(); err == nil {
		t.Fatal("Load of a newer processes.json returned nil, want a refusal")
	}
	t.Cleanup(s.Shutdown)

	cmd := sleepCmd()
	if _, err := s.SpawnRecorded(cmd, DeviceStream); !errors.Is(err, ErrUnrecordable) {
		t.Fatalf("SpawnRecorded err = %v, want ErrUnrecordable", err)
	}
	if cmd.Process != nil {
		t.Errorf("the client was started (pid %d); a process it cannot record must not run", cmd.Process.Pid)
	}
}

// Decision (plan m1i D9; m1e R8): when the record cannot be written after the client has
// started, SpawnRecorded kills the client's pid — never its group, which a forked adb
// server would share — and returns an error the caller maps to DEVICE_LOGS_FAILED, so no
// unrecorded client is left behind. Seen failing reversed (the client left running): it
// would be a follow no daemon could find.
func TestSpawnRecordedKillsTheClientWhenTheRecordWriteFails(t *testing.T) {
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
	// A path the store's write cannot pass: its stale-tmp removal meets a directory that
	// is not empty. Unlike a read-only root this fails whatever the test's uid, so the
	// case is deterministic.
	tmp := filepath.Join(root, RecordsFile+".tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "held"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := sleepCmd()
	if _, err := s.SpawnRecorded(cmd, DeviceStream); !errors.Is(err, ErrUnrecordable) {
		t.Fatalf("SpawnRecorded err = %v, want ErrUnrecordable", err)
	}
	if cmd.Process == nil {
		t.Fatal("no client was started; this case must exercise the write failing after it starts")
	}
	if !dead(cmd.Process.Pid) {
		t.Errorf("pid %d survived a record that could not be written", cmd.Process.Pid)
	}
	if recs := onDisk(t, st); len(recs) != 0 {
		t.Errorf("processes.json holds %+v, want none: the write failed", recs)
	}
}
