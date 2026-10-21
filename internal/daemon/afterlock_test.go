package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/system"
)

// Decision (Options.AfterLock; ADR-0113 §5: the lock holder finalises what a dead daemon left
// unfinished, before anything is served): the hook runs once, while the daemon holds the lock
// (a second Acquire is refused) and before the socket exists, so no call can reach the daemon
// first. Seen failing with the hook run at the end of Start (the socket was already bound).
func TestAfterLockRunsHoldingTheLockBeforeTheSocketExists(t *testing.T) {
	state := shortDir(t)
	socket := filepath.Join(state, "run", "droidyard.sock")
	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	system.Register(reg, none{}, nil, ops, nil, nil, nil, nil)
	ops.RegisterCapabilities()
	var calls int
	var locked bool
	var socketThen error
	opts := Options{
		State: state, Socket: socket, Version: system.Version, UID: os.Getuid(), Ops: ops,
		Handlers: map[capability.ClientKind]Handler{capability.ClientCLI: Native(reg, ops)},
		Log:      testLog{t},
		AfterLock: func() {
			calls++
			_, err := Acquire(state, LockInfo{PID: os.Getpid()})
			locked = errors.Is(err, ErrLocked)
			_, socketThen = os.Stat(socket)
			if c, err := net.DialTimeout("unix", socket, time.Second); err == nil {
				c.Close()
				socketThen = nil
			}
		},
	}
	d, err := Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d.Shutdown(ctx)
	})
	if calls != 1 {
		t.Errorf("AfterLock ran %d times, want once", calls)
	}
	if !locked {
		t.Error("AfterLock ran without the daemon holding the lock")
	}
	if !errors.Is(socketThen, os.ErrNotExist) {
		t.Errorf("the socket at AfterLock: %v, want it not to exist yet", socketThen)
	}
	if _, err := os.Stat(socket); err != nil {
		t.Errorf("the socket after Start: %v", err)
	}
}
