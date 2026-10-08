package process

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/process/processtest"
	"github.com/jtortosasen/droidyard/internal/store"
)

// launchRecord is the record of a live processtest Sleep process standing in for an
// emulator launcher: a leader of a group of its own, with its real start time.
func launchRecord(t *testing.T, p *processtest.Proc) Record {
	t.Helper()
	started, err := platform.StartTime(p.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return Record{ID: string(EmulatorLaunch), Kind: EmulatorLaunch, PID: p.Pid, PGID: p.Pid, StartTime: started,
		Command: []string{"emulator", "-avd", "A"}, AVD: "A", AdbPort: 5599}
}

func writeRecords(t *testing.T, st *store.Store, recs ...Record) {
	t.Helper()
	if err := st.WriteVersion(RecordsFile, RecordsVersion, Records{Processes: recs}); err != nil {
		t.Fatal(err)
	}
}

// Decision (plan m1p Q1 ¶1; ADR-0021 §Format): RecordLaunch writes an emulator-launch record
// naming the AVD and the adb port, in a processes.json of schemaVersion 2, and DropLaunch
// removes it. Version 2 is what makes a daemon that reads only version 1 leave the file
// alone instead of reaping a launch it does not know to judge: the last assertion is that
// reader's, a store.Read at the old version and an old Write, both refused with the file
// byte for byte unchanged. Seen failing with the version left at 1 (the old reader loads
// the file and its Write replaces it).
func TestRecordLaunchWritesAVersion2RecordAnOlderDaemonLeavesAlone(t *testing.T) {
	s, st := newSupervisor(t)
	p := processtest.Start(t, processtest.Sleep)
	if err := s.RecordLaunch("Pixel_8", 5599, p.Pid, []string{"emulator", "-avd", "Pixel_8"}); err != nil {
		t.Fatal(err)
	}
	recs := onDisk(t, st)
	if len(recs) != 1 {
		t.Fatalf("processes.json holds %d records, want 1: %+v", len(recs), recs)
	}
	r := recs[0]
	if r.Kind != EmulatorLaunch || r.AVD != "Pixel_8" || r.AdbPort != 5599 || r.PID != p.Pid || r.PGID != p.Pid || r.StartTime == 0 {
		t.Errorf("record = %+v", r)
	}
	path := filepath.Join(st.Root(), RecordsFile)
	raw, _ := os.ReadFile(path)
	if !bytes.Contains(raw, []byte(`"schemaVersion": 2`)) {
		t.Errorf("processes.json is not schemaVersion 2:\n%s", raw)
	}

	// An older daemon: it reads and writes at store.Version (1).
	old, err := store.New(st.Root())
	if err != nil {
		t.Fatal(err)
	}
	var seen Records
	if out, err := old.Read(RecordsFile, store.Version, &seen); out != store.Newer || !errors.Is(err, store.ErrNewer) {
		t.Errorf("an older reader got %v, %v; want Newer", out, err)
	}
	if err := old.Write(RecordsFile, Records{}); !errors.Is(err, store.ErrNewer) {
		t.Errorf("an older writer got %v; want ErrNewer", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, raw) {
		t.Errorf("an older daemon changed processes.json:\n%s", after)
	}

	if err := s.DropLaunch(p.Pid); err != nil {
		t.Fatal(err)
	}
	if recs := onDisk(t, st); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after DropLaunch", recs)
	}
}

// Decision (plan m1p Q1 ¶1): both failures reach the caller. A record that cannot be
// written is ErrUnrecordable, so the launch is ended; a record that cannot be removed is an
// error, so the boot fails. Seen failing with RecordLaunch ignoring addRecord's error.
func TestRecordLaunchAndDropLaunchReportAFailedWrite(t *testing.T) {
	s, st := newSupervisor(t)
	p := processtest.Start(t, processtest.Sleep)
	tmp := filepath.Join(st.Root(), RecordsFile+".tmp")
	block := func() {
		if err := os.Mkdir(tmp, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmp, "held"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	unblock := func() {
		if err := os.RemoveAll(tmp); err != nil {
			t.Fatal(err)
		}
	}

	block()
	if err := s.RecordLaunch("A", 5037, p.Pid, nil); !errors.Is(err, ErrUnrecordable) {
		t.Errorf("RecordLaunch with an unwritable file = %v, want ErrUnrecordable", err)
	}
	unblock()
	if err := s.RecordLaunch("A", 5037, p.Pid, nil); err != nil {
		t.Fatal(err)
	}
	block()
	if err := s.DropLaunch(p.Pid); err == nil {
		t.Error("DropLaunch with an unwritable file returned nil; the caller must learn the record may remain")
	}
}

// Decision (plan m1p Q1 ¶1): a launcher that has already exited has no start time to
// record and nothing to find later, so recording it is not an error: an immediate exit is
// reported by the launch's own exit status (EMULATOR_AVD_LOCKED, for one), not as a failed
// record. Seen failing with the missing process turned into ErrUnrecordable.
func TestRecordLaunchOfAnExitedLauncherIsNotAnError(t *testing.T) {
	s, st := newSupervisor(t)
	p := processtest.Start(t, processtest.Sleep)
	syscall.Kill(p.Pid, syscall.SIGKILL)
	<-p.Done()
	if err := s.RecordLaunch("A", 5037, p.Pid, nil); err != nil {
		t.Errorf("RecordLaunch of an exited process = %v", err)
	}
	if recs := onDisk(t, st); len(recs) != 0 {
		t.Errorf("an exited launcher was recorded: %+v", recs)
	}
}

// Decision (plan m1p Q1 ¶2): the judge is asked only for an emulator-launch record whose
// pid and start time match, so a normal start (no such record) runs no adb at all, and a
// stale record is dropped without a question. It is asked once per such record under one
// deadline of JudgeTimeout; true signals the group, false (doubt) signals nothing and logs
// the reason; a daemon with no judge treats every such record as doubt. Seen failing with
// the judge called for a dead pid (the calls count), with "signal unless the judge says
// no" (the doubt case would kill), and with a deadline per record instead of one.
func TestReconcileAsksTheJudgeOnlyForAMatchingLaunchRecord(t *testing.T) {
	st := newStore(t)
	yes := processtest.Start(t, processtest.Sleep)
	no := processtest.Start(t, processtest.Sleep)
	stale := processtest.Start(t, processtest.Sleep)
	other := processtest.Start(t, processtest.Sleep)
	ry, rn, rs, ro := launchRecord(t, yes), launchRecord(t, no), launchRecord(t, stale), launchRecord(t, other)
	ry.AVD, rn.AVD, rs.AVD, ro.AVD = "yes", "no", "stale", "other"
	rs.StartTime += 1_000_000 // another process reused the pid
	ro.Kind = SharedSidecar   // not a launch: never judged, reaped as before
	writeRecords(t, st, ry, rn, rs, ro)

	var asked []string
	var deadlines []time.Time
	judge := func(ctx context.Context, r Record) (bool, string) {
		asked = append(asked, r.AVD)
		d, ok := ctx.Deadline()
		if !ok {
			t.Error("the judge's context has no deadline")
		}
		deadlines = append(deadlines, d)
		if r.AVD == "yes" {
			return true, "adb proved it"
		}
		return false, "the console of emulator-5554 did not answer"
	}
	var log bytes.Buffer
	if err := Reconcile(st, &log, judge); err != nil {
		t.Fatal(err)
	}
	<-yes.Done()
	<-other.Done()
	if strings.Join(asked, ",") != "yes,no" {
		t.Errorf("the judge was asked about %v, want [yes no]", asked)
	}
	if len(deadlines) == 2 && !deadlines[0].Equal(deadlines[1]) {
		t.Errorf("each record got its own deadline: %v", deadlines)
	}
	if dead(no.Pid) {
		t.Errorf("a launch the judge doubted was signalled (pid %d)", no.Pid)
	}
	if dead(stale.Pid) {
		t.Errorf("a record with another start time was signalled (pid %d)", stale.Pid)
	}
	if !strings.Contains(log.String(), "did not answer; nothing signalled") {
		t.Errorf("the log does not carry the doubt's reason:\n%s", log.String())
	}
	var left Records
	if out, err := st.Read(RecordsFile, RecordsVersion, &left); out != store.Loaded || err != nil || len(left.Processes) != 0 {
		t.Errorf("processes.json after reconciling: %v, %v, %+v", out, err, left)
	}
	syscall.Kill(no.Pid, syscall.SIGKILL)
	syscall.Kill(stale.Pid, syscall.SIGKILL)
}

// Decision (plan m1p Q1 ¶2): with no judge, an emulator-launch record is dropped and
// nothing is signalled. Seen failing with a nil judge treated as "signal".
func TestReconcileWithoutAJudgeSignalsNoLaunch(t *testing.T) {
	st := newStore(t)
	p := processtest.Start(t, processtest.Sleep)
	writeRecords(t, st, launchRecord(t, p))
	if err := Reconcile(st, &bytes.Buffer{}, nil); err != nil {
		t.Fatal(err)
	}
	if dead(p.Pid) {
		t.Error("a launch was signalled with no judge to prove it is not a device")
	}
	syscall.Kill(p.Pid, syscall.SIGKILL)
}

// Decision (plan m1p Q1, verifier N9): a judge that panics is doubt, logged: the record is
// dropped, nothing is signalled, and the daemon still starts. Seen failing without the
// recover (Reconcile panics).
func TestReconcileTreatsAPanickingJudgeAsDoubt(t *testing.T) {
	st := newStore(t)
	p := processtest.Start(t, processtest.Sleep)
	writeRecords(t, st, launchRecord(t, p))
	var log bytes.Buffer
	if err := Reconcile(st, &log, func(context.Context, Record) (bool, string) { panic("boom") }); err != nil {
		t.Fatal(err)
	}
	if dead(p.Pid) {
		t.Error("a launch was signalled although the judge panicked")
	}
	if !strings.Contains(log.String(), "panicked: boom") {
		t.Errorf("the log does not say the judge panicked:\n%s", log.String())
	}
	syscall.Kill(p.Pid, syscall.SIGKILL)
}
