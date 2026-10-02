// Package daemon is the core daemon's process-level machinery: the single-instance
// lock now, the socket and sessions next (ADR-0019).
package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// LockFile is the lock's name under the state root (ADR-0021 §Layout).
const LockFile = "daemon.lock"

// LockInfo identifies the holder, for humans and for `droidyard stop`. It is never
// what excludes a second daemon: the flock is (ADR-0021 §Locking).
type LockInfo struct {
	PID int `json:"pid"`
	// StartTime is the daemon's own clock at start. A held flock already rules out
	// pid reuse; kernel start times are for pid records.
	StartTime time.Time `json:"startTime"`
	Version   string    `json:"version"`
	Socket    string    `json:"socket"`
	// HTTP is the UI's loopback address; null until the HTTP server exists.
	HTTP *string `json:"http"`
}

// ErrLocked means another process holds the lock.
var ErrLocked = errors.New("another droidyard daemon holds the lock")

// Lock is a held daemon.lock. The kernel releases it when the process dies, so a
// crashed daemon never blocks the next one (ADR-0019 §5).
type Lock struct {
	f *os.File
}

// Acquire takes an exclusive, non-blocking flock on <state>/daemon.lock and writes
// info into it. When another process holds it, the error wraps ErrLocked and names
// the holder if the file says who it is.
func Acquire(state string, info LockInfo) (*Lock, error) {
	if err := os.MkdirAll(state, 0o700); err != nil {
		return nil, fmt.Errorf("creating the state directory: %w", err)
	}
	path := filepath.Join(state, LockFile)
	// Never truncate on open: the file may be the holder's, and its contents name it.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if err := lockExclusive(f); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			if holder, rerr := ReadLock(state); rerr == nil {
				return nil, fmt.Errorf("%w: pid %d, version %s, started %s (%s)", ErrLocked, holder.PID, holder.Version, holder.StartTime.Format(time.RFC3339), path)
			}
			return nil, fmt.Errorf("%w (%s)", ErrLocked, path)
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}

	// Written in place: a rename would put the contents on a new inode while the
	// flock stays on the old one, and the next daemon would lock the new file freely.
	data, err := json.Marshal(info)
	if err == nil {
		err = f.Truncate(0)
	}
	if err == nil {
		_, err = f.WriteAt(append(data, '\n'), 0)
	}
	if err == nil {
		err = f.Sync()
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// probeGrace is how long Acquire keeps trying while the lock is busy. Holder's probe
// holds a shared lock for microseconds; a daemon holds the exclusive one for its
// life. Without the grace, a daemon starting during a `status` or `stop` probe would
// conclude another daemon holds the lock when none does.
const probeGrace = 100 * time.Millisecond

// lockExclusive takes the exclusive flock without blocking, retrying through
// probeGrace while it is busy.
func lockExclusive(f *os.File) error {
	deadline := time.Now().Add(probeGrace)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Release empties the file, so it no longer names a holder, and releases the lock.
func (l *Lock) Release() error {
	terr := l.f.Truncate(0)
	if err := l.f.Close(); err != nil {
		return err
	}
	return terr
}

// Holder reports whether a daemon holds <state>/daemon.lock, and what the file says
// about it. It probes with a shared, non-blocking flock that it drops at once, so the
// answer does not rest on a pid that may have been reused.
func Holder(state string) (info LockInfo, held bool, err error) {
	f, err := os.Open(filepath.Join(state, LockFile))
	if errors.Is(err, os.ErrNotExist) {
		return info, false, nil
	}
	if err != nil {
		return info, false, err
	}
	defer f.Close()
	switch err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); {
	case err == nil:
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return info, false, nil
	case !errors.Is(err, syscall.EWOULDBLOCK):
		return info, false, err
	}
	info, err = ReadLock(state)
	return info, true, err
}

// ReadLock reads what <state>/daemon.lock says about its holder. The contents say
// nothing about whether the lock is held.
func ReadLock(state string) (LockInfo, error) {
	var info LockInfo
	data, err := os.ReadFile(filepath.Join(state, LockFile))
	if err != nil {
		return info, err
	}
	if len(data) == 0 {
		return info, errors.New("daemon.lock names no holder")
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return info, fmt.Errorf("daemon.lock: %w", err)
	}
	return info, nil
}
