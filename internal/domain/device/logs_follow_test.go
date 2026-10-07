package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/store"
)

// device.logs' bounded follow (plan m1i step 3): the fake adb re-executed as a real
// client, so the supervisor records its pid and the window's end can kill it. The
// in-process fake of logs_test.go cannot reach this path.

// followFixture is the device capabilities over a real process supervisor and the
// re-executed fake adb: a follow's client is a process the supervisor records, so only
// this fixture exercises one (plan m1i step 3's check).
type followFixture struct {
	dir string // the fake's scenario directory
	// root is the state root: processes.json lives here, so a test can watch the record.
	root string
	adb  *ADB
	ops  *capability.Operations
	sup  *process.Supervisor
}

func newFollowFixture(t *testing.T, files map[string]string) *followFixture {
	t.Helper()
	base := map[string]string{
		"devices.out":                   uiList(phoneLine),
		uiSerial + ".bootcompleted.out": "1\n",
	}
	for k, v := range files {
		base[k] = v
	}
	dir := t.TempDir()
	for name, content := range base {
		devicetest.Write(t, dir, name, []byte(content))
	}
	root := t.TempDir()
	st, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	sup := process.New(process.Options{Store: st, Log: io.Discard})
	t.Cleanup(sup.Shutdown)
	if err := sup.Load(); err != nil {
		t.Fatal(err)
	}
	adb := NewADB(devicetest.Env(t, dir, closedPort(t)), sup.Spawn)
	adb.SetStreamStarter(recordedStarter(sup))
	reg := capability.NewRegistry()
	Register(reg, adb, testStore(t), nil)
	ops := capability.NewOperations(reg)
	ops.RegisterCapabilities()
	return &followFixture{dir: dir, root: root, adb: adb, ops: ops, sup: sup}
}

// recordedStarter is the daemon's adapter (cmd/droidyard/main.go): the supervisor's
// SpawnRecorded with its error mapped to the device's, so a follow can tell a record it
// could not write from a client it could not start.
func recordedStarter(sup *process.Supervisor) StreamStarter {
	return func(cmd *exec.Cmd, kind string) (Stream, error) {
		r, err := sup.SpawnRecorded(cmd, process.Kind(kind))
		if errors.Is(err, process.ErrUnrecordable) {
			return nil, fmt.Errorf("%w: %v", ErrStreamUnrecorded, err)
		}
		return r, err
	}
}

func (f *followFixture) startLogs(t *testing.T, params string) string {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: "device.logs", Params: []byte(params)})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return id
}

func (f *followFixture) logsCall(t *testing.T, params string) capability.Outcome {
	t.Helper()
	return f.wait(t, f.startLogs(t, params))
}

func (f *followFixture) wait(t *testing.T, id string) capability.Outcome {
	t.Helper()
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// records are the pid records the daemon has written, read from processes.json.
func (f *followFixture) records(t *testing.T) []process.Record {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, process.RecordsFile))
	if err != nil {
		return nil
	}
	var recs process.Records
	if err := json.Unmarshal(b, &recs); err != nil {
		t.Fatalf("processes.json: %v", err)
	}
	return recs.Processes
}

// waitRecord polls until the follow's one pid record is on disk, and returns it.
func (f *followFixture) waitRecord(t *testing.T) process.Record {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if recs := f.records(t); len(recs) == 1 {
			return recs[0]
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("the follow's pid record never appeared in processes.json")
	return process.Record{}
}

// followLine is one line of the device log in the recorded format (plan m1i step 1):
// "<epoch>.<usec> <uid> <pid> <tid> <lvl> <tag>: <message>". ts is written verbatim, so
// a test's epochs are exact.
func followLine(ts string, uid int, tag, msg string) string {
	return fmt.Sprintf("%s %d 6406 6406 I %s: %s", ts, uid, tag, msg)
}

// Decision (plan m1i D9; check (a)): a follow returns exactly the lines the window
// covers — a line due before it ends is returned, one due after it is not — the window
// runs its whole followMs, and the client is a recorded process while it runs and gone,
// with its record, when the call returns. Seen failing reversed (the third line kept, or
// the record left behind): the window would not be bounded, or the next daemon would reap
// a pid it no longer owns.
func TestFollowWindowKeepsOnlyItsLinesAndRecordsTheClient(t *testing.T) {
	f := newFollowFixture(t, map[string]string{
		uiSerial + ".date.out": "1791337000.500000\n",
		uiSerial + ".log": "--------- beginning of main\n" +
			followLine("1791337001.000000", 10146, "App", "first") + "\n" +
			followLine("1791337002.000000", 10146, "App", "second") + "\n" +
			followLine("1791337003.500000", 10146, "App", "third") + "\n",
	})
	started := time.Now()
	id := f.startLogs(t, fmt.Sprintf(`{"device":%q,"followMs":2000}`, uiSerial))
	rec := f.waitRecord(t)
	if rec.Kind != process.DeviceStream || rec.ID != string(process.DeviceStream) {
		t.Errorf("record kind/id = %q/%q, want %q", rec.Kind, rec.ID, process.DeviceStream)
	}
	pid := rec.PID
	out := f.wait(t, id)
	took := time.Since(started)
	if out.State != capability.StateCompleted {
		t.Fatalf("state = %s with error %+v, want completed: the window's end is a normal end", out.State, out.Error)
	}
	r := mustLogs(t, out)
	if got := messages(r.Lines); strings.Join(got, ",") != "first,second" {
		t.Errorf("lines = %v, want [first second]: the third is due after the %s window", got, "2s")
	}
	if took < 2*time.Second || took > 3*time.Second {
		t.Errorf("the follow took %s, want its 2 s window and no more than 1 s over", took)
	}
	if !gone(pid) {
		t.Errorf("client pid %d survived the window", pid)
	}
	if recs := f.records(t); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after the window, want none", recs)
	}
}

// Decision (plan m1i D9; ADR-0020): cancelling a follow ends it as cancelled — not
// completed, not failed — with no client and no record left. Seen failing reversed (a
// cancelled state or a live client): a caller could not tell a cancelled window from a
// finished one, and the client would linger.
func TestFollowCancelEndsWithNoClientOrRecord(t *testing.T) {
	f := newFollowFixture(t, map[string]string{
		uiSerial + ".date.out": "1791337000.500000\n",
		// A line due far past the window: the client would stream nothing this call, so
		// every sign of it comes from the process and the record.
		uiSerial + ".log": "--------- beginning of main\n" +
			followLine("1791337060.000000", 10146, "App", "later") + "\n",
	})
	id := f.startLogs(t, fmt.Sprintf(`{"device":%q,"followMs":60000}`, uiSerial))
	rec := f.waitRecord(t)
	if err := f.ops.Cancel(id); err != nil {
		t.Fatal(err)
	}
	out := f.wait(t, id)
	if out.State != capability.StateCancelled {
		t.Fatalf("state = %s with error %+v, want cancelled", out.State, out.Error)
	}
	if !gone(rec.PID) {
		t.Errorf("client pid %d survived the cancellation", rec.PID)
	}
	if recs := f.records(t); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after the cancellation, want none", recs)
	}
}

// Decision (plan m1i D9; check (a)): when the client's pid record cannot be written, the
// follow fails with DEVICE_LOGS_FAILED, names the state store in its cause, and leaves no
// client running — the supervisor killed the pid it could not record. Seen failing
// reversed (a recorded result, or a live client): a follow no daemon could find would run
// unseen.
func TestFollowFailsWhenTheRecordCannotBeWritten(t *testing.T) {
	f := newFollowFixture(t, map[string]string{
		uiSerial + ".date.out": "1791337000.500000\n",
		uiSerial + ".log": "--------- beginning of main\n" +
			followLine("1791337001.000000", 10146, "App", "first") + "\n",
	})
	// The store's write cannot pass: its stale-tmp removal meets a directory that is not
	// empty (the same uid-independent trick internal/process uses).
	tmp := filepath.Join(f.root, process.RecordsFile+".tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "held"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := f.logsCall(t, fmt.Sprintf(`{"device":%q,"followMs":1000}`, uiSerial))
	if out.State != capability.StateFailed {
		t.Fatalf("state = %s, want failed", out.State)
	}
	e := out.Error
	if e == nil || e.Code != ErrLogsFailed {
		t.Fatalf("error %+v, want DEVICE_LOGS_FAILED", out.Error)
	}
	if !strings.Contains(e.Cause, "record") || !strings.Contains(e.Cause, "state store") {
		t.Errorf("cause %q, want it to name the pid record and the state store", e.Cause)
	}
	// No record survived, and no client it started outlived the failure.
	if recs := f.records(t); len(recs) != 0 {
		t.Errorf("processes.json holds %+v, want none", recs)
	}
	if pid, ok := readFollowPid(t, f.dir, uiSerial); ok && !gone(pid) {
		t.Errorf("client pid %d survived a record that could not be written", pid)
	}
}

// Decision (plan m1i D9; D10): a device that says it is gone on a follow's stderr is
// DEVICE_DISCONNECTED, not a logs failure. Seen failing reversed (DEVICE_LOGS_FAILED):
// the caller would not know the device vanished.
func TestFollowDeviceGoneIsDisconnected(t *testing.T) {
	f := newFollowFixture(t, map[string]string{
		uiSerial + ".date.out":    "1791337000.500000\n",
		uiSerial + ".logcat.err":  "error: device 'emulator-5554' not found\n",
		uiSerial + ".logcat.exit": "1",
	})
	out := f.logsCall(t, fmt.Sprintf(`{"device":%q,"followMs":1000}`, uiSerial))
	if e := out.Error; e == nil || e.Code != ErrDisconnected {
		t.Fatalf("error %+v, want DEVICE_DISCONNECTED", out.Error)
	}
}

// Decision (plan m1i D9): an explicit since reads its backlog at once — the follow's
// window is not the only source of lines — and a follow without one starts at the device
// clock, so a recorded launch is not used as its lower bound. The pair pins that a follow
// has no "launch" default. Seen failing reversed (the launch used as the bound): a bare
// follow would silently re-read from a launch and repeat lines.
func TestFollowSinceBacklogAndTheNoLaunchDefault(t *testing.T) {
	// The device log: a line just after the launch (backlog for an explicit since) and a
	// line due a moment after the device clock the follow reads.
	log := "--------- beginning of main\n" +
		followLine("1791337000.600000", 10146, "App", "backlog") + "\n" +
		followLine("1791337011.000000", 10146, "App", "live") + "\n"
	// The device clock is ten seconds after the launch, so 1791337011.0 is half a second
	// into a one-second window.
	const date = "1791337010.500000\n"
	files := map[string]string{
		uiSerial + ".date.out":     date,
		uiSerial + ".log":          log,
		uiSerial + ".pm.out":       "package:" + appID + " uid:10146\n",
		uiSerial + ".apppidof.out": "",
	}
	const launchRFC = "2026-10-07T01:36:40.000000Z"

	t.Run("explicit since keeps the backlog", func(t *testing.T) {
		f := newFollowFixture(t, files)
		f.adb.appStore().noteLaunch(uiSerial, appID, time.Unix(1791337000, 0), launchRFC, nil)
		// since the launch instant, a device-clock RFC 3339 timestamp before the backlog line.
		out := f.logsCall(t, fmt.Sprintf(
			`{"device":%q,"applicationId":%q,"since":%q,"followMs":1000}`,
			uiSerial, appID, launchRFC))
		r := mustLogs(t, out)
		if got := strings.Join(messages(r.Lines), ","); got != "backlog,live" {
			t.Errorf("lines = %q, want the backlog line and the live one", got)
		}
	})

	t.Run("without since a launch adds no backlog", func(t *testing.T) {
		f := newFollowFixture(t, files)
		f.adb.appStore().noteLaunch(uiSerial, appID, time.Unix(1791337000, 0), launchRFC, nil)
		out := f.logsCall(t, fmt.Sprintf(
			`{"device":%q,"applicationId":%q,"followMs":1000}`, uiSerial, appID))
		r := mustLogs(t, out)
		if got := strings.Join(messages(r.Lines), ","); got != "live" {
			t.Errorf("lines = %q, want only the live line: a follow without since starts at the device clock, not the launch", got)
		}
	})

	t.Run("since launch reads from the launch", func(t *testing.T) {
		f := newFollowFixture(t, files)
		f.adb.appStore().noteLaunch(uiSerial, appID, time.Unix(1791337000, 0), launchRFC, nil)
		out := f.logsCall(t, fmt.Sprintf(
			`{"device":%q,"applicationId":%q,"since":"launch","followMs":1000}`, uiSerial, appID))
		r := mustLogs(t, out)
		if got := strings.Join(messages(r.Lines), ","); got != "backlog,live" {
			t.Errorf("lines = %q, want the launch's backlog and the live line", got)
		}
	})
}

// messages is the lines' messages in order.
func messages(lines []LogLine) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.Message)
	}
	return out
}

// waitInfo polls until the forked fake has written its pid file, and returns it.
func waitInfo(t *testing.T, path string) forked {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return readInfo(t, path)
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("the fake client never forked")
	return forked{}
}

// newRecordedRunner is an ADB over env whose followers are recorded through a real
// supervisor (the follow's path), with the state store so a test can read the record.
func newRecordedRunner(t *testing.T, env []string) (*ADB, *process.Supervisor) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sup := process.New(process.Options{Store: st})
	if err := sup.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sup.Shutdown)
	a := NewADB(env, sup.Spawn)
	a.SetStreamStarter(recordedStarter(sup))
	return a, sup
}

// Decision (plan m1i D9; m1e R8): a follow ends the window and a cancellation alike by
// killing only the client's pid, never its process group, so a child the client forked
// into the same group — as adb forks its server — survives both. Seen failing reversed
// (the group signalled): ending a follow would kill the user's shared adb server.
func TestRunLinesRecordedSparesTheForkedServer(t *testing.T) {
	args := []string{"-s", uiSerial, "logcat", "-v", logcatFormat, "-b", "main,system,crash", "-T", "1791337000.500000"}

	t.Run("at the window's end", func(t *testing.T) {
		info := filepath.Join(t.TempDir(), "info")
		a, _ := newRecordedRunner(t, fakeEnvFor(t, modeForkHang, "1", fakeInfo+"="+info))
		ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
		defer cancel()
		out, err := a.RunLinesRecorded(ctx, a.Resolve().Path, 10*time.Second, 1<<10, func([]byte) error { return nil }, args...)
		f := waitInfo(t, info)
		t.Cleanup(func() { syscall.Kill(f.child, syscall.SIGKILL) })
		if !errors.Is(err, ErrTimeout) {
			t.Errorf("err = %v, want ErrTimeout: the window's end is a normal end", err)
		}
		if out.ExitCode != -1 {
			t.Errorf("exit code %d, want -1: the client was killed", out.ExitCode)
		}
		if !gone(f.client) {
			t.Error("the client survived its window")
		}
		if gone(f.child) {
			t.Error("the forked child died with the client: the group was signalled, not the pid")
		}
	})

	t.Run("on cancellation", func(t *testing.T) {
		info := filepath.Join(t.TempDir(), "info")
		a, _ := newRecordedRunner(t, fakeEnvFor(t, modeForkHang, "1", fakeInfo+"="+info))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// Cancel once the child exists, so this measures the kill, not the race to fork.
		go func() {
			waitInfo(t, info)
			cancel()
		}()
		out, err := a.RunLinesRecorded(ctx, a.Resolve().Path, 10*time.Second, 1<<10, func([]byte) error { return nil }, args...)
		f := waitInfo(t, info)
		t.Cleanup(func() { syscall.Kill(f.child, syscall.SIGKILL) })
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if out.ExitCode != -1 {
			t.Errorf("exit code %d, want -1: the client was killed", out.ExitCode)
		}
		if !gone(f.client) {
			t.Error("the client survived the cancellation")
		}
		if gone(f.child) {
			t.Error("the forked child died with the client: the group was signalled, not the pid")
		}
	})
}

// readFollowPid reads the pid the fake follow client wrote to <serial>.follow.pid, if it
// got that far.
func readFollowPid(t *testing.T, dir, serial string) (int, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, serial+".follow.pid"))
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, false
	}
	return n, true
}
