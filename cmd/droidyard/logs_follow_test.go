package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	proc "github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// The daemon-level half of step 3's check (plan m1i D9): a follow's client is a real
// process the supervisor records, so only a re-executed daemon — whose adb is the fake
// (devicetest.Main), a process per call — reaches the paths this file pins. The
// in-process fake of device_test.go returns its output at once and cannot be recorded.

// fakeFollowDaemon is a state root with a scenario directory the fake adb answers from:
// a device, a device clock, and a log whose one line is due long after any window, so a
// follow streams nothing and blocks until it is killed (a line cannot end it by SIGPIPE).
type fakeFollowDaemon struct {
	state string
	dir   string
	env   []string
}

func newFakeFollowDaemon(t *testing.T) *fakeFollowDaemon {
	t.Helper()
	d := &fakeFollowDaemon{state: shortDir(t), dir: t.TempDir()}
	const serial = "emulator-5554"
	devicetest.Write(t, d.dir, "devices.out", []byte("List of devices attached\n"+serial+" device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 device:emu64a16k transport_id:1\n"))
	devicetest.Write(t, d.dir, serial+".bootcompleted.out", []byte("1\n"))
	devicetest.Write(t, d.dir, serial+".date.out", []byte("1791337000.500000\n"))
	devicetest.Write(t, d.dir, serial+".log", []byte("--------- beginning of main\n"+
		"1791339999.000000 10146 6406 6406 I App: far_future\n"))
	d.env = []string{adbFakeEnv + "=" + d.dir, adbPortEnv + "=" + unusedPort(t), adbFloorVar()}
	return d
}

func (d *fakeFollowDaemon) serve(t *testing.T) *process {
	t.Helper()
	srv := droidyardEnv(t, d.state, "", d.env, "serve")
	srv.expect(t, "listening")
	return srv
}

// records are the pid records processes.json holds, read straight from disk so a test can
// see them while a follow runs (the daemon is a process of its own).
func (d *fakeFollowDaemon) records(t *testing.T) []proc.Record {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(d.state, proc.RecordsFile))
	if err != nil {
		return nil
	}
	var recs proc.Records
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatalf("processes.json: %v", err)
	}
	return recs.Processes
}

// waitFollowRecord polls for the one device-stream record a running follow wrote.
func (d *fakeFollowDaemon) waitFollowRecord(t *testing.T) proc.Record {
	t.Helper()
	for deadline := time.Now().Add(testtime.Bound(processGuard)); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if recs := d.records(t); len(recs) == 1 {
			return recs[0]
		}
	}
	t.Fatalf("no follow's pid record appeared in processes.json")
	return proc.Record{}
}

// TestGracefulStopCancelsABoundedFollow pins plan m1i step 3's check: a `droidyard stop`
// while a bounded follow runs ends the call cancelled — not completed, not failed — with
// no client left and its record removed, so a graceful stop leaves nothing behind
// (ADR-0020; ADR-0021 §Pid records). Seen failing reversed (a completed or failed call, a
// live client, or a record left on disk): a stopped daemon would leak a logcat process
// the next start reaps as an orphan it never owned.
func TestGracefulStopCancelsABoundedFollow(t *testing.T) {
	t.Parallel()
	d := newFakeFollowDaemon(t)
	srv := d.serve(t)

	// The call blocks for its whole window; the daemon's stop is what ends it.
	call := droidyardEnv(t, d.state, "", d.env, "call", "--no-autostart", "device.logs",
		"--params", `{"device":"emulator-5554","followMs":60000}`)
	rec := d.waitFollowRecord(t)
	if rec.Kind != proc.DeviceStream {
		t.Errorf("record kind = %q, want %q: a follow's client is recorded as a device stream", rec.Kind, proc.DeviceStream)
	}
	if !alive(rec.PID) {
		t.Fatalf("the recorded client (pid %d) is not running while the follow does", rec.PID)
	}

	if code := droidyardEnv(t, d.state, "", d.env, "stop").wait(t); code != 0 {
		t.Fatalf("stop exited %d", code)
	}
	if code := srv.wait(t); code != 0 {
		t.Errorf("serve exited %d after stop; want 0", code)
	}

	env, _ := call.envelope(t)
	if env.Operation == nil || env.Operation.State != capability.StateCancelled {
		t.Fatalf("the stopped follow ended %+v (error %+v); want it cancelled", env.Operation, env.Error)
	}
	if alive(rec.PID) {
		t.Errorf("client pid %d survived the graceful stop", rec.PID)
	}
	if recs := d.records(t); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after the stop, want none", recs)
	}
}

// TestKilledDaemonLeavesTheFollowForTheNextStart pins the other half (plan m1i step 3;
// ADR-0021 §Pid records): a daemon killed mid-follow cannot stop its client, so the
// orphan lives on until the next daemon's Reconcile reaps it by its recorded group. The
// fake writes nothing after the kill (its line is due long past the window), so SIGPIPE
// cannot have ended the orphan by itself. Seen failing reversed (the client gone after
// the kill, or alive after the next start): either the record was not written, or the
// next daemon never reconciled.
func TestKilledDaemonLeavesTheFollowForTheNextStart(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("linux: Pdeathsig (process-lifecycle.md §Shutdown, layer 2) ends the follow client when the daemon is killed, so the orphan this test reaps never exists; the reap-by-record half runs on macOS only")
	}
	t.Parallel()
	d := newFakeFollowDaemon(t)
	srv := d.serve(t)

	// Detached: nothing waits on the operation, so the daemon's death is the only event.
	detached := droidyardEnv(t, d.state, "", d.env, "call", "--no-autostart", "--detach",
		"device.logs", "--params", `{"device":"emulator-5554","followMs":60000}`)
	if code := detached.wait(t); code != 0 {
		t.Fatalf("the detached call exited %d", code)
	}
	rec := d.waitFollowRecord(t)
	if !alive(rec.PID) {
		t.Fatalf("the recorded client (pid %d) is not running while the follow does", rec.PID)
	}

	// Killed outright: no shutdown runs, nothing removes the record.
	srv.cmd.Process.Signal(syscall.SIGKILL)
	srv.wait(t)
	if !alive(rec.PID) {
		t.Fatalf("the client (pid %d) died with the daemon; it must outlive a kill -9", rec.PID)
	}
	if recs := d.records(t); len(recs) != 1 || recs[0].PID != rec.PID {
		t.Fatalf("processes.json holds %+v after the kill, want the client's record %d", recs, rec.PID)
	}

	// The next start reconciles before it listens, so by "listening" the orphan is reaped.
	second := d.serve(t)
	if alive(rec.PID) {
		t.Errorf("client pid %d survived the next daemon's reconciliation", rec.PID)
	}
	if recs := d.records(t); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after reconciling, want none", recs)
	}
	if code := droidyardEnv(t, d.state, "", d.env, "stop").wait(t); code != 0 {
		t.Fatalf("the second stop exited %d", code)
	}
	second.wait(t)
}
