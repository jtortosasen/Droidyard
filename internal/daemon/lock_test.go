package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestAcquireOutlastsAHolderProbe: Holder's probe (a shared flock held for an
// instant, by `status` and `stop`) must not make a starting daemon conclude that
// another daemon holds the lock (ADR-0019 §5). Acquire keeps trying through
// probeGrace; a real holder still wins.
func TestAcquireOutlastsAHolderProbe(t *testing.T) {
	state := shortDir(t)
	probe, err := os.OpenFile(filepath.Join(state, LockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	fd := int(probe.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(20 * time.Millisecond)
		syscall.Flock(fd, syscall.LOCK_UN)
	}()
	defer func() { <-released }()

	lock, err := Acquire(state, LockInfo{PID: os.Getpid()})
	if err != nil {
		t.Fatalf("a shared lock held for 20 ms made Acquire fail: %v", err)
	}
	defer lock.Release()

	// A daemon's exclusive lock still wins: a second Acquire gives up after the grace.
	start := time.Now()
	if _, err := Acquire(state, LockInfo{PID: os.Getpid()}); !errors.Is(err, ErrLocked) {
		t.Fatalf("a second Acquire returned %v; want ErrLocked", err)
	}
	if waited := time.Since(start); waited < probeGrace || waited > 10*probeGrace {
		t.Errorf("the loser gave up after %s; want about %s", waited, probeGrace)
	}
}
