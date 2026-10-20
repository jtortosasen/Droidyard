package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/testtime"
	"golang.org/x/sys/unix"
)

// TestDetachedLogRotates is plan m2c D18 / S5: a detached daemon's log is size-rotated
// (ADR-0021 §Layout). Past the bound, daemon.log.1 holds the old lines, new lines land
// in a fresh daemon.log, and so does a write that bypasses the daemon's log writer
// (a plain fmt.Fprintln(os.Stderr), as a panic or a library would): the descriptors the
// daemon writes to were pointed at the new file, not only the writer's. Without that
// repointing the stray line below is found in daemon.log.1.
//
// A rotation is a rename and then a dup2, and the line that triggered it is written after the
// dup2 and after the call that caused it has been answered (the trace is flushed once the
// operation's lock is released). So a daemon.log.1 on disk does not say the rotation is done,
// and a call's trace line is not there when the call returns: the test waits for the line that
// rotated the log to reach the new daemon.log before it makes the stray write, and for the
// stray call's own trace line before it reads it. Seen failing, with a pause between the
// rename and the dup2 put in the daemon, as the reading before the wait: the stray line was
// in daemon.log.1.
func TestDetachedLogRotates(t *testing.T) {
	state := shortDir(t)
	env := []string{logMaxEnv + "=1500"}
	t.Cleanup(func() {
		if info, err := daemon.ReadLock(state); err == nil && info.PID > 0 && alive(info.PID) {
			syscall.Kill(info.PID, syscall.SIGKILL)
		}
	})
	if code := droidyardEnv(t, state, "", env, "serve", "--detach").wait(t); code != 0 {
		t.Fatalf("serve --detach exited %d", code)
	}
	call := func(name string) {
		t.Helper()
		c := droidyardEnv(t, state, "", env, "call", name)
		if out, code := c.envelope(t); code != 0 {
			t.Fatalf("call %s exited %d with %+v", name, code, out)
		}
	}
	logPath := filepath.Join(state, logFile)
	rotated := logPath + ".1"
	read := func(path string) string {
		b, _ := os.ReadFile(path)
		return string(b)
	}
	call("system.health") // the daemon is up: its first start line is in daemon.log
	if !strings.Contains(read(logPath), "): starting") {
		t.Fatalf("daemon.log has no start line before any rotation:\n%s", read(logPath))
	}
	for i := 0; i < 400; i++ {
		if _, err := os.Stat(rotated); err == nil {
			break
		}
		call("system.health")
	}
	if _, err := os.Stat(rotated); err != nil {
		t.Fatalf("400 calls (%d bytes of log) never rotated daemon.log: %v", len(read(logPath)), err)
	}
	if !strings.Contains(read(rotated), "): starting") {
		t.Errorf("daemon.log.1 does not hold the old lines (no start line):\n%s", read(rotated))
	}
	// The rotation is done when the line that caused it is in the new file: it is written
	// to standard error, which the dup2 repoints.
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the line that caused the rotation to reach the new daemon.log (it goes to standard error, so only a dup2 onto the new file puts it there)", func() bool {
		return strings.Contains(read(logPath), "droidyard: op ")
	})
	if strings.Contains(read(logPath), "): starting") {
		t.Errorf("daemon.log still holds the old lines after the rotation")
	}

	call("fixture.stray")
	if got := read(logPath); !strings.Contains(got, "STRAY-LINE") {
		t.Errorf("a stray write to standard error is not in the new daemon.log (it went to the old file?):\n%s", got)
	}
	if strings.Contains(read(rotated), "STRAY-LINE") {
		t.Errorf("a stray write to standard error landed in daemon.log.1, which was renamed away")
	}
	// New trace lines land in the new file as well; the call's own is written after its answer.
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the stray call's trace line in the new daemon.log", func() bool {
		return strings.Contains(read(logPath), "fixture.stray")
	})

	// Nothing keeps the old file open: dup2 left no descriptor on it.
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Log("lsof is not installed: the open-descriptor check is not run")
		return
	}
	info, err := daemon.ReadLock(state)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command("lsof", "-p", strconv.Itoa(info.PID), "-Fn").Output()
	if strings.Contains(string(out), "daemon.log.1") {
		t.Errorf("the daemon still holds daemon.log.1 open:\n%s", out)
	}
	if !strings.Contains(string(out), "daemon.log") {
		t.Errorf("the daemon holds no daemon.log descriptor:\n%s", out)
	}
}

// logHarness is a rotatingLog over a temporary daemon.log whose "standard error" is a
// private descriptor, so the test never repoints this process's own.
type logHarness struct {
	l     *rotatingLog
	path  string
	now   time.Time
	fail  bool // the next opens fail
	opens int
}

func newLogHarness(t *testing.T, max int64) *logHarness {
	t.Helper()
	h := &logHarness{path: filepath.Join(t.TempDir(), "daemon.log"), now: time.Unix(1_000_000, 0)}
	f, err := os.OpenFile(h.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fd, err := unix.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	out := os.NewFile(uintptr(fd), "stderr")
	t.Cleanup(func() { out.Close() })
	h.l = newRotatingLog(h.path, max)
	h.l.out, h.l.repoint = out, []int{fd}
	h.l.now = func() time.Time { return h.now }
	real := h.l.open
	h.l.open = func(path string) (*os.File, error) {
		h.opens++
		if h.fail {
			return nil, syscall.EMFILE
		}
		return real(path)
	}
	return h
}

func (h *logHarness) write(t *testing.T, s string) {
	t.Helper()
	if _, err := h.l.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
}

func fileText(path string) string { b, _ := os.ReadFile(path); return string(b) }

// TestLogRotationThatFailsReportsOnceAndRecovers: a rotation whose reopen fails (the
// descriptors stay on the renamed file) is reported once, not before every later line;
// writes during the pause neither retry nor spam; and once the pause is over and the open
// works, the log rotates into a fresh daemon.log. A file deleted by hand is not renamed
// over: a fresh one is opened. Seen failing without the pause and the report-once flag
// (a "rotating" line before every write, and an open attempt at each).
func TestLogRotationThatFailsReportsOnceAndRecovers(t *testing.T) {
	h := newLogHarness(t, 100)
	h.write(t, strings.Repeat("a", 120)+"\n")
	h.fail = true
	for i := 0; i < 10; i++ {
		h.write(t, fmt.Sprintf("line %d\n", i))
	}
	rotated := fileText(h.path + ".1")
	if n := strings.Count(rotated, "rotating"); n != 1 {
		t.Errorf("the failure was reported %d times in:\n%s; want once", n, rotated)
	}
	if h.opens != 1 {
		t.Errorf("%d open attempts during the pause; want the one that failed", h.opens)
	}
	if !strings.Contains(rotated, "line 9") {
		t.Errorf("lines written while rotation failed were lost:\n%s", rotated)
	}

	// The pause is over and the open works: rotate into a fresh daemon.log.
	h.fail = false
	h.now = h.now.Add(rotateRetry + time.Second)
	h.write(t, "after recovery\n")
	if got := fileText(h.path); !strings.Contains(got, "after recovery") || strings.Contains(got, "line 9") {
		t.Errorf("after recovery daemon.log is %q; want only the new line", got)
	}
	if got := fileText(h.path + ".1"); !strings.Contains(got, "line 9") {
		t.Errorf("daemon.log.1 lost the old lines: %q", got)
	}

	// daemon.log deleted by hand: the descriptors are on a file that is no longer at path.
	h2 := newLogHarness(t, 100)
	h2.write(t, strings.Repeat("b", 120)+"\n")
	os.Remove(h2.path)
	h2.write(t, "later\n")
	if got := fileText(h2.path); !strings.Contains(got, "later") {
		t.Errorf("after the log was deleted, the next write did not reach a new daemon.log: %q", got)
	}
	if _, err := os.Stat(h2.path + ".1"); err == nil {
		t.Errorf("a rename was attempted over a file that was already gone")
	}
}
