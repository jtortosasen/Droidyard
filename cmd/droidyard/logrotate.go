package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// logMaxBytes is the size past which a detached daemon's log rotates (plan m2c, D18:
// 16 MiB, one daemon.log.1 kept; ADR-0021 §Layout says "size-rotated"). A variable
// only so that this package's tests can lower it in a re-executed binary.
var logMaxBytes int64 = 16 << 20

// rotateRetry is how long a writer whose rotation failed waits before it tries again:
// the failure is reported once, not at every write.
const rotateRetry = 30 * time.Second

// rotatingLog is the detached daemon's log writer. Its output is standard error, which
// startDetached pointed at <state>/daemon.log; before a write that would take the file
// past max it renames the file to <name>.1 (replacing the previous one), opens a new
// one and points descriptors 1 and 2 at it, so a write to os.Stderr that bypasses this
// writer (a panic, a library's print) lands in the new file as well and nothing keeps
// the old one open. The size is read from descriptor 2 at each write, so such strays
// count toward the bound. Descriptor 2 that is no longer the file at path (already
// moved, deleted by hand, replaced) is not renamed over: a fresh file is opened at path
// and the descriptors pointed at it.
type rotatingLog struct {
	mu   sync.Mutex
	path string
	max  int64

	// out and repoint are standard error and the descriptors a rotation repoints; open
	// and now are the file open and the clock. Tests replace them.
	out     *os.File
	repoint []int
	open    func(path string) (*os.File, error)
	now     func() time.Time

	// failed is set while a rotation keeps failing (it was reported once); retryAt is
	// when the next attempt is allowed.
	failed  bool
	retryAt time.Time
}

func newRotatingLog(path string, max int64) *rotatingLog {
	return &rotatingLog{path: path, max: max, out: os.Stderr, repoint: []int{1, 2}, now: time.Now,
		open: func(path string) (*os.File, error) {
			return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		}}
}

func (l *rotatingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.now().Before(l.retryAt) {
		if info, err := l.out.Stat(); err == nil && info.Size() > 0 && info.Size()+int64(len(p)) > l.max {
			if err := l.rotate(); err != nil {
				// Keep logging to the file there is: a rotation that failed must not lose
				// lines. Say so once, and try again after a pause.
				l.retryAt = l.now().Add(rotateRetry)
				if !l.failed {
					fmt.Fprintf(l.out, "droidyard serve: rotating %s: %v (retrying every %s)\n", l.path, err, rotateRetry)
				}
				l.failed = true
			} else {
				l.failed = false
			}
		}
	}
	return l.out.Write(p)
}

func (l *rotatingLog) rotate() error {
	cur, err := os.Stat(l.path)
	info, ierr := l.out.Stat()
	if err == nil && ierr == nil && os.SameFile(cur, info) {
		// A rename that finds nothing to move is a file that has gone already.
		if err := os.Rename(l.path, l.path+".1"); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	f, err := l.open(l.path)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, fd := range l.repoint {
		if err := dupOnto(int(f.Fd()), fd); err != nil {
			return err
		}
	}
	return nil
}

// daemonLog is where the daemon writes its log lines: for a detached daemon, a writer
// that rotates <state>/daemon.log; otherwise standard error, unchanged.
func daemonLog(state string, detached bool) io.Writer {
	if !detached {
		return os.Stderr
	}
	return newRotatingLog(filepath.Join(state, logFile), logMaxBytes)
}
