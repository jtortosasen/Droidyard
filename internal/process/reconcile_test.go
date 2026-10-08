package process

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/process/processtest"
	"github.com/jtortosasen/droidyard/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// TestReconcileLeavesANewerFileUntouched: a processes.json this daemon cannot read is
// never overwritten, nothing is reaped, the problem is recorded and the daemon goes on
// (ADR-0021 §Format; plan m1c, R10).
func TestReconcileLeavesANewerFileUntouched(t *testing.T) {
	st := newStore(t)
	path := filepath.Join(st.Root(), RecordsFile)
	newer := []byte(`{"schemaVersion":3,"processes":[{"id":"x","pid":1,"pgid":1}]}`)
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	if err := Reconcile(st, &log, nil); err != nil {
		t.Fatalf("Reconcile = %v; a newer file must not stop the daemon", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, newer) {
		t.Errorf("the newer file changed: %q", got)
	}
	if p := st.Problems(); len(p) != 1 || p[0].File != RecordsFile || p[0].Problem != store.ProblemNewerVersion {
		t.Errorf("problems = %+v", p)
	}
	if !strings.Contains(log.String(), "newer schemaVersion") {
		t.Errorf("log: %q", log.String())
	}
}

// TestReconcileSurvivesACorruptFile: a corrupt processes.json is moved aside by the
// store and reconciling goes on with no records.
func TestReconcileSurvivesACorruptFile(t *testing.T) {
	st := newStore(t)
	if err := os.WriteFile(filepath.Join(st.Root(), RecordsFile), []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	if err := Reconcile(st, &log, nil); err != nil {
		t.Fatal(err)
	}
	if p := st.Problems(); len(p) != 1 || p[0].Problem != store.ProblemCorrupt || p[0].MovedTo == "" {
		t.Errorf("problems = %+v", p)
	}
}

// TestReconcileNeverSignalsGroupZeroOrOne: kill(-0) is this daemon's own group and
// kill(-1) every process of the user, so a record naming either is dropped even when
// its pid and start time match. The record here is this test process itself.
func TestReconcileNeverSignalsGroupZeroOrOne(t *testing.T) {
	st := newStore(t)
	self, err := platform.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	recs := Records{Processes: []Record{
		{ID: "zero", PID: os.Getpid(), PGID: 0, StartTime: self},
		{ID: "one", PID: os.Getpid(), PGID: 1, StartTime: self},
	}}
	if err := st.Write(RecordsFile, recs); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	if err := Reconcile(st, &log, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log.String(), "SIGTERM") || strings.Count(log.String(), "not a valid pid and process group") != 2 {
		t.Errorf("log:\n%s", log.String())
	}
	var left Records
	if out, err := st.Read(RecordsFile, RecordsVersion, &left); out != store.Loaded || err != nil || len(left.Processes) != 0 {
		t.Errorf("after reconciling: %v, %v, %+v", out, err, left)
	}
}

// TestReconcileSparesAChildInItsOwnGroup pins the other half of "the group is signalled,
// not the pid" (plan m1i D9; m1e R8): a recorded follow client — a device-stream record —
// is reaped by signalling its own process group, so a child it forked into a group of its
// own, as `adb` forks the shared server, is never reached. Real processes: the client dies
// on SIGTERM, the helper in its own group lives. It pins what must never be signalled
// (a child in a group of its own, like adb's forked server); signalling the client's pid
// instead of its group also passes it (step 3 review 4), since the client dies either way.
func TestReconcileSparesAChildInItsOwnGroup(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	client := processtest.Start(t, processtest.TermExitsOwnGroup)
	helper, err := strconv.Atoi(client.Ready)
	if err != nil {
		t.Fatalf("the client's ready line named no helper: %q", client.Ready)
	}
	// In its own group, the helper is not reached by the client's cleanup: kill it here,
	// before the client's, so the client's stdout pipe closes and it is reaped.
	t.Cleanup(func() { syscall.Kill(helper, syscall.SIGKILL) })

	started, err := platform.StartTime(client.Pid)
	if err != nil {
		t.Fatal(err)
	}
	recs := Records{Processes: []Record{{
		ID: string(DeviceStream), Kind: DeviceStream, PID: client.Pid, PGID: client.Pid,
		StartTime: started, Command: []string{"adb", "-s", "emulator-5554", "logcat", "-v", "epoch"},
	}}}
	if err := st.Write(RecordsFile, recs); err != nil {
		t.Fatal(err)
	}

	var log bytes.Buffer
	if err := Reconcile(st, &log, nil); err != nil {
		t.Fatal(err)
	}
	<-client.Done()
	if !dead(client.Pid) {
		t.Errorf("the recorded client (pid %d) survived reconcling", client.Pid)
	}
	if dead(helper) {
		t.Error("the helper in its own process group died with the client: the group was not the one signalled")
	}
	if !strings.Contains(log.String(), "reaped") {
		t.Errorf("Reconcile's log does not name the client reaped:\n%s", log.String())
	}
}

// TestReconcileWithoutAFileWritesNothing: a first start finds no records and creates
// no file.
func TestReconcileWithoutAFileWritesNothing(t *testing.T) {
	st := newStore(t)
	if err := Reconcile(st, &bytes.Buffer{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(st.Root(), RecordsFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("processes.json: %v", err)
	}
}
