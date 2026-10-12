package emulator

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// recorder stands in for the supervisor's launch records (plan m1p Q1): it keeps the set
// of recorded launches, can fail either write, and tells a test what was true at the
// moment of each removal.
type recorder struct {
	mu        sync.Mutex
	live      map[int]recEntry
	failWrite error
	failDrop  error
	onDrop    func(pid int) // runs at the moment of a removal, before it takes effect
	drops     int
	pids      []int // every pid a record was attempted for
}

type recEntry struct {
	avd     string
	port    int
	command []string
}

func (r *recorder) hooks() LaunchRecorder {
	return LaunchRecorder{
		Record: func(avd string, port, pid int, command []string) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.pids = append(r.pids, pid)
			if r.failWrite != nil {
				return r.failWrite
			}
			if r.live == nil {
				r.live = map[int]recEntry{}
			}
			r.live[pid] = recEntry{avd, port, append([]string(nil), command...)}
			return nil
		},
		Drop: func(pid int) error {
			if r.onDrop != nil {
				r.onDrop(pid)
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			r.drops++
			if r.failDrop != nil {
				return r.failDrop
			}
			delete(r.live, pid)
			return nil
		},
	}
}

func (r *recorder) has(pid int) (recEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.live[pid]
	return e, ok
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.live)
}

func (r *recorder) firstPid(t *testing.T) int {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pids) == 0 {
		t.Fatal("no launch was recorded")
	}
	return r.pids[0]
}

// Decision (plan m1p Q1 ¶1): a launch is pid-recorded from its spawn until its serial is
// resolved, and the record is removed BEFORE the serial is returned (and before any work
// that follows the resolution, so a daemon dying after it can never have its successor
// reach a device); a launch ended before its serial is removed once its group is empty,
// not before. The first case asserts the record is present while the fake has no serial,
// names the AVD, the adb port and the command, and that at the moment of the removal no
// readiness poll of the new serial has run (the fake counts them) and at the moment boot
// returns the serial the record is gone. The second asserts the removal comes after `end`
// emptied the group. Seen failing with no record, with the removal after the serial is
// returned (polls > 0 at the removal), and with the removal before `end` (the group is
// still there).
func TestLaunchIsRecordedUntilResolution(t *testing.T) {
	t.Run("resolved", func(t *testing.T) {
		scaleBoot(t, 10*time.Second, 10*time.Millisecond)
		f := newFixture(t, map[string]string{"devices.out": listHead}, "ANDROID_ADB_SERVER_PORT=5599")
		f.launcher(t, loadedAVD+"\n")
		f.emuWrite(t, loadedAVD+".launch.delay", "600\n")
		rec := &recorder{}
		var pollsAtDrop = -1
		rec.onDrop = func(int) { pollsAtDrop = pollCount(f.adbDir, serial) }
		f.em.SetLaunchRecorder(rec.hooks())

		done := make(chan capability.Outcome, 1)
		go func() { done <- f.boot(t, `{"avd":"`+loadedAVD+`"}`) }()
		l := f.awaitLaunch(t, loadedAVD)
		// The fake has not registered a serial yet; the record is already there.
		if pid := f.launcherPid(t, serial); pid != 0 {
			t.Fatalf("the fake registered %s before the test looked; lengthen the launch delay", serial)
		}
		// The launch is visible to launchFor a moment before the record hook runs, so the
		// record is awaited, but only while the fake still has no serial.
		e, ok := rec.has(l.pid)
		for end := time.Now().Add(300 * time.Millisecond); !ok && time.Now().Before(end); e, ok = rec.has(l.pid) {
			time.Sleep(time.Millisecond)
		}
		if !ok {
			t.Fatal("no record while the launch has no serial")
		}
		if e.avd != loadedAVD || e.port != 5599 || len(e.command) < 3 || e.command[1] != "-avd" || e.command[2] != loadedAVD {
			t.Errorf("record = %+v; want the AVD, port 5599 and the launcher command", e)
		}

		r := mustStart(t, <-done)
		if r.Device != serial {
			t.Fatalf("device = %q", r.Device)
		}
		if _, ok := rec.has(l.pid); ok || rec.count() != 0 {
			t.Errorf("the record is still present when boot returned the serial")
		}
		if pollsAtDrop != 0 {
			t.Errorf("%d readiness polls had run when the record was removed; it must go at the resolution, before any of them", pollsAtDrop)
		}
	})

	t.Run("ended", func(t *testing.T) {
		scaleBoot(t, 30*time.Second, 20*time.Millisecond)
		scaleSerial(t, 300*time.Millisecond)
		f := newFixture(t, map[string]string{"devices.out": listHead})
		f.launcher(t, loadedAVD+"\n")
		f.emuWrite(t, loadedAVD+".launch.delay", "100000\n")
		f.emuWrite(t, loadedAVD+".launch.child", "")
		rec := &recorder{}
		groupGoneAtDrop := false
		rec.onDrop = func(pid int) { groupGoneAtDrop = groupGone(pid) }
		f.em.SetLaunchRecorder(rec.hooks())

		mustFail(t, f.boot(t, `{"avd":"`+loadedAVD+`"}`), ErrBootTimeout)
		pid := rec.firstPid(t)
		if rec.count() != 0 || rec.drops != 1 {
			t.Errorf("after the launch was ended: %d records, %d removals; want 0 and 1", rec.count(), rec.drops)
		}
		if !groupGoneAtDrop {
			t.Error("the record was removed while the launch's group was still present")
		}
		awaitDead(t, pid)
	})
}

// Decision (plan m1p Q1 ¶1): a launch whose record cannot be written is ended and fails
// EMULATOR_LAUNCH_FAILED, as a stream that cannot be recorded is not started
// (ErrStreamUnrecorded): a daemon that died now could not be cleaned up after. The
// launcher is a real process that would otherwise run to the serial deadline. Seen failing
// with the write error ignored (the boot goes on, the launcher lives).
func TestFailedRecordWriteFailsTheLaunch(t *testing.T) {
	scaleBoot(t, 10*time.Second, 10*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.launcher(t, loadedAVD+"\n")
	f.emuWrite(t, loadedAVD+".launch.delay", "100000\n")
	rec := &recorder{failWrite: errors.New("disk full")}
	f.em.SetLaunchRecorder(rec.hooks())

	started := time.Now()
	e := mustFail(t, f.boot(t, `{"avd":"`+loadedAVD+`"}`), ErrLaunchFailed)
	if d := time.Since(started); d > 5*time.Second {
		t.Errorf("the call took %s; the launch should have been ended at once", d)
	}
	if !strings.Contains(e.Cause, "pid record could not be written") || !strings.Contains(e.Cause, "disk full") || !strings.Contains(e.Cause, "was stopped") {
		t.Errorf("cause = %q; want the record, the reason and that the launcher was stopped", e.Cause)
	}
	awaitDead(t, rec.firstPid(t))
	if f.em.launchFor(loadedAVD) != nil {
		t.Error("the launch that was never recorded is still in the table")
	}
}

// Decision (plan m1p Q1 ¶1): when the serial is resolved but its record cannot be removed,
// the launch is ended and the boot fails: no serial is handed out, because a record that
// stays would let the next daemon end a running device. Seen failing with the removal
// error ignored (the boot returns the serial of a device whose record remains).
func TestFailedRecordRemovalFailsTheBoot(t *testing.T) {
	scaleBoot(t, 10*time.Second, 10*time.Millisecond)
	f := newFixture(t, map[string]string{"devices.out": listHead})
	f.launcher(t, loadedAVD+"\n")
	rec := &recorder{failDrop: errors.New("read-only file system")}
	f.em.SetLaunchRecorder(rec.hooks())

	out := f.boot(t, `{"avd":"`+loadedAVD+`"}`)
	e := mustFail(t, out, ErrLaunchFailed)
	if !strings.Contains(e.Cause, "pid record could not be removed") || !strings.Contains(e.Cause, "read-only file system") {
		t.Errorf("cause = %q", e.Cause)
	}
	if !strings.Contains(e.Cause, "was stopped") {
		t.Errorf("cause = %q; want the launch ended", e.Cause)
	}
	awaitDead(t, rec.firstPid(t))
}

// Decision (plan m1p Q1, verifier N6): ANDROID_ADB_SERVER_PORT set but empty means adb's
// default 5037, as the daemon's host:version read treats it; an invalid value records 0 so
// crash recovery proves nothing from it. Seen failing with the empty value read as 0.
func TestAdbServerPortOfTheLauncherEnvironment(t *testing.T) {
	for env, want := range map[string]int{"": 5037, "ANDROID_ADB_SERVER_PORT=": 5037, "ANDROID_ADB_SERVER_PORT=5599": 5599, "ANDROID_ADB_SERVER_PORT=x": 0} {
		var e []string
		if env != "" {
			e = []string{env}
		}
		if got := adbServerPort(e); got != want {
			t.Errorf("adbServerPort(%q) = %d, want %d", env, got, want)
		}
	}
}
