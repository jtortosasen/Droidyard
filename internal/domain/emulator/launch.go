package emulator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// How much of a failed launch's log a failure quotes (plan m1i, D15): its last lines,
// at most this many bytes. The launcher's own words, host text like `emulator
// -list-avds`'s chatter, never device output.
const (
	launchLogLines = 20
	launchLogTail  = 2 << 10
	// launchLogRead is how much of the log's end is read before the tail is taken; a
	// running emulator's log can grow without bound.
	launchLogRead = 64 << 10
)

// launch is one launcher we started (plan m1i, D15): its pid and log, and the goroutine
// that reaps it. It lives in the in-memory table only — no pid record — because the
// emulator must outlive the daemon (emulator.md §Detached launch).
type launch struct {
	avd     string
	pid     int
	logPath string
	done    chan struct{}

	mu     sync.Mutex
	serial string // the serial the boot resolved; "" until then
	state  *os.ProcessState
	werr   error
}

// exited reports whether the launcher has exited (its Wait goroutine returned).
func (l *launch) exited() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

// exitStatus is the launcher's exit code and whether it has exited.
func (l *launch) exitStatus() (int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state == nil {
		return -1, false
	}
	return l.state.ExitCode(), true
}

// setSerial records the serial this launch booted, once the boot resolved it.
func (l *launch) setSerial(serial string) {
	l.mu.Lock()
	l.serial = serial
	l.mu.Unlock()
}

// getSerial is the serial this launch booted, "" until the boot resolved it.
func (l *launch) getSerial() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.serial
}

// end ends a launch whose serial this daemon has not resolved (ADR-0033 ¶1; plan m1m,
// Q1 ¶1): SIGTERM to the process group step 1 found (pgid = the launch's pid under
// Setsid, the pid the qemu itself), SIGKILL if the group is not empty after endGrace, and
// it waits until the group is empty — not only until the launcher's reaper returned,
// which says nothing about a process the launcher left behind. It reports whether the
// group emptied. It takes no context: it must run even when the call's context is already
// cancelled (a cancel or the daemon's shutdown), and its own two graces bound it, so it
// cannot outlast the daemon's shutdown bound. A launch whose group is already empty is a
// no-op. It is never called once a serial is resolved: a resolved emulator is a device and
// is not ours to signal (ADR-0033 ¶2).
func (l *launch) end() bool {
	if l == nil || groupGone(l.pid) {
		return true
	}
	_ = syscall.Kill(-l.pid, syscall.SIGTERM)
	if waitGroupGone(l.pid, endGrace) {
		return true
	}
	_ = syscall.Kill(-l.pid, syscall.SIGKILL)
	return waitGroupGone(l.pid, endKillGrace)
}

// groupGone is whether the process group pgid holds no process we could signal:
// `kill(-pgid, 0)` answering ESRCH. Any other answer — success, or a permission error that
// means a process is there — is "not empty".
func groupGone(pgid int) bool {
	return errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
}

// waitGroupGone polls groupGone until it answers true or d passes, and returns the last
// answer.
func waitGroupGone(pgid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		gone := groupGone(pgid)
		if gone || !time.Now().Before(deadline) {
			return gone
		}
		time.Sleep(endPollInterval)
	}
}

// logPath is where an AVD's launcher output goes: `<state>/emulator/<avd>.log`
// (emulator.md §Detached launch; ADR-0021 §Layout). It refuses a name that is not a
// bare AVD name, so no caller string can reach a path, and confirms the result is
// inside the emulator directory (plan m1i step 5 check: the log path built from an
// unchecked name).
func logPath(root, avd string) (string, bool) {
	if !avdName.MatchString(avd) {
		return "", false
	}
	dir := filepath.Join(root, "emulator")
	p := filepath.Join(dir, avd+".log")
	if filepath.Dir(p) != dir {
		return "", false
	}
	return p, true
}

// spawn starts the launcher detached (emulator.md §Detached launch; plan m1i, D15):
// exec.Cmd with SysProcAttr{Setsid: true}, stdin from /dev/null, stdout and stderr to
// the AVD's log truncated now, the working directory the state root, the daemon's
// environment. It never goes through the supervisor: no pid record, no Pdeathsig — the
// one exception to process-lifecycle.md's "every child" rule, because the emulator is
// not ours. One goroutine calls Wait, so the launcher is reaped while the daemon lives
// and its exit status is available to the boot loop and to `stop`.
func (e *Emulator) spawn(path, avd string, flags []string) (*launch, error) {
	log, ok := logPath(e.state, avd)
	if !ok {
		return nil, invalidAVDName(avd)
	}
	if err := os.MkdirAll(filepath.Dir(log), 0o700); err != nil {
		return nil, launchNotStarted(avd, err)
	}
	f, err := os.OpenFile(log, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, launchNotStarted(avd, err)
	}
	defer f.Close()
	devnull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return nil, launchNotStarted(avd, err)
	}
	defer devnull.Close()

	cmd := exec.Command(path, append([]string{"-avd", avd}, flags...)...)
	cmd.Env = e.env
	cmd.Dir = e.state
	cmd.Stdin = devnull
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, launcherStartFailed(path, avd, err)
	}
	l := &launch{avd: avd, pid: cmd.Process.Pid, logPath: log, done: make(chan struct{})}
	e.mu.Lock()
	e.launches[avd] = l
	e.mu.Unlock()
	go func() {
		werr := cmd.Wait()
		l.mu.Lock()
		l.state, l.werr = cmd.ProcessState, werr
		l.mu.Unlock()
		close(l.done)
	}()
	return l, nil
}

// launchFor is the launcher we started for avd, nil when the table has none.
func (e *Emulator) launchFor(avd string) *launch {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.launches[avd]
}

// launchForSerial is the launcher we started that boots serial, nil when the table has
// none: the lookup `emulator.stop` uses to wait for our launcher's own exit
// (emulator.md §Stop).
func (e *Emulator) launchForSerial(serial string) *launch {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, l := range e.launches {
		if l.getSerial() == serial {
			return l
		}
	}
	return nil
}

// awaitExit waits until the launcher has exited or the deadline passes, and reports
// whether it exited; ctx ending returns its error. A nil launcher (one we did not
// start, an adoption) counts as gone.
func awaitExit(ctx context.Context, l *launch, deadline time.Time) (bool, error) {
	if l == nil {
		return true, nil
	}
	if l.exited() {
		return true, nil
	}
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	select {
	case <-l.done:
		return true, nil
	case <-t.C:
		return l.exited(), nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// logTail is the log's last non-empty lines, at most launchLogTail bytes: what a launch
// failure quotes so a caller can see the launcher's own words without opening the file.
func logTail(path string) string {
	b, err := tailOf(path, launchLogRead)
	if err != nil {
		return ""
	}
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if s := strings.TrimSpace(l); s != "" {
			lines = append(lines, s)
		}
	}
	if len(lines) > launchLogLines {
		lines = lines[len(lines)-launchLogLines:]
	}
	return truncate(strings.Join(lines, " | "), launchLogTail)
}

// tailOf reads at most n bytes from the end of path.
func tailOf(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	off := fi.Size() - n
	if off < 0 {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

// lockedMarker is the line the launcher prints when it refuses a second instance of a
// running AVD (step 1's G10 recording, second-launch.txt), which maps to
// EMULATOR_AVD_LOCKED.
const lockedMarker = "Running multiple emulators with the same AVD"

// launchExited is EMULATOR_AVD_LOCKED when the launcher's log holds the second-instance
// refusal and EMULATOR_LAUNCH_FAILED otherwise (emulator.md §Failure modes; plan m1i,
// D15): the exit status and the log's tail. It is read on every readiness poll, so a
// launcher that dies during a boot ends it at once, never at the boot timeout.
func launchExited(l *launch, during string) *capability.Error {
	tail := logTail(l.logPath)
	if strings.Contains(tail, lockedMarker) {
		cause := lockedMarker
		if line := lineContaining(tail, lockedMarker); line != "" {
			cause = line
		}
		return capability.NewError(ErrAVDLocked, "the AVD "+l.avd+" is held by another instance").
			WithCause(cause).
			WithNextStep("No emulator in adb devices answers as this AVD, so the instance holding it has no console Droidyard can address: it was started outside this daemon (by hand, another tool or an earlier daemon), or hung before its console opened. Droidyard does not stop processes it did not launch: close that emulator's window or end its process, then call emulator.start again, or boot another AVD.")
	}
	status, _ := l.exitStatus()
	cause := fmt.Sprintf("the emulator launcher %s (exit status %d)", during, status)
	if tail != "" {
		cause += ": " + tail
	}
	return capability.NewError(ErrLaunchFailed, "the emulator launcher exited before the device finished booting").
		WithCause(cause).
		WithNextStep("Run the emulator by hand once to see why; its output is under emulator/ in the state directory.")
}

// lineContaining is the first " | "-separated piece of s that holds the marker, "" when
// none does.
func lineContaining(s, marker string) string {
	for _, l := range strings.Split(s, " | ") {
		if l = strings.TrimSpace(l); strings.Contains(l, marker) {
			return truncate(l, 300)
		}
	}
	return ""
}

// invalidAVDName is REQUEST_INVALID_PARAMS for a name that is not a bare AVD name; it
// runs before any path is built or any launcher starts (plan m1i step 5 check).
func invalidAVDName(avd string) *capability.Error {
	return capability.NewError(capability.ErrInvalidParams, "the AVD name is not valid").
		WithCause(fmt.Sprintf("%q is not a bare AVD name", truncate(avd, 80))).
		WithNextStep("Pass an AVD name as emulator.list reports it: letters, digits, dot, dash and underscore, starting with a letter or digit.")
}

// launchNotStarted is EMULATOR_LAUNCH_FAILED when the launcher's log cannot be prepared,
// so nothing was booted.
func launchNotStarted(avd string, err error) *capability.Error {
	return capability.NewError(ErrLaunchFailed, "the emulator could not be started").
		WithCause(avd + ": " + err.Error()).
		WithNextStep("Check that the state directory is writable, then retry.")
}

// launcherStartFailed is EMULATOR_LAUNCH_FAILED for a launcher the OS refused to start.
func launcherStartFailed(path, avd string, err error) *capability.Error {
	return capability.NewError(ErrLaunchFailed, "the emulator launcher could not be started").
		WithCause(path + " -avd " + avd + ": " + err.Error()).
		WithNextStep("Run the launcher by hand to see why, and put a working emulator on PATH or under ANDROID_HOME.")
}
