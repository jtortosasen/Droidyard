package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/adapters/cli"
	"github.com/jtortosasen/droidyard/internal/capability"
)

// startedID waits for fixture.block to report its operation id.
func (p *inProcess) startedID(t *testing.T) string {
	t.Helper()
	select {
	case id := <-p.started:
		return id
	case <-time.After(10 * time.Second):
		t.Fatal("fixture.block never started")
	}
	return ""
}

// TestCLILongRunning holds `droidyard call` to the CLI projection of ADR-0020
// §Adapter projections: Start plus a wait with no deadline, Ctrl-C cancels, --detach
// prints the id and leaves the operation running; and to ADR-0019 §Sessions: a killed
// CLI does not stop the operation it started.
func TestCLILongRunning(t *testing.T) {
	t.Run("SIGKILL does not stop the operation", func(t *testing.T) {
		p := startInProcess(t)
		before := p.d.Sessions()
		c := droidyard(t, p.state, "call", "--no-autostart", "fixture.block")
		id := p.startedID(t)
		c.cmd.Process.Signal(syscall.SIGKILL)
		c.wait(t)
		eventually(t, "the killed CLI's session ends", func() bool { return p.d.Sessions() == before })

		released(t, p, id)
	})

	t.Run("SIGINT cancels the operation", func(t *testing.T) {
		p := startInProcess(t)
		c := droidyard(t, p.state, "call", "--no-autostart", "fixture.block")
		id := p.startedID(t)
		c.cmd.Process.Signal(syscall.SIGINT)
		line := c.expect(t, "interrupted; cancelling operation "+id)
		if code := c.wait(t); code != cli.ExitInterrupted {
			t.Errorf("the interrupted CLI exited %d; want %d", code, cli.ExitInterrupted)
		}
		if strings.Contains(line, "failed") {
			t.Errorf("the cancel was not accepted: %s", line)
		}

		env, code := waited(t, p, id, "")
		if code == 0 || env.Operation == nil || env.Operation.ID != id || env.Operation.State != capability.StateCancelled ||
			env.Error == nil || env.Error.Code != capability.ErrOperationCancelled {
			t.Fatalf("after Ctrl-C, operation.wait exited %d with %+v (error %v); want non-zero, %s cancelled, OPERATION_CANCELLED", code, env.Operation, env.Error, id)
		}
	})

	t.Run("--detach prints the id and leaves the operation running", func(t *testing.T) {
		p := startInProcess(t)
		c := droidyard(t, p.state, "call", "--no-autostart", "--detach", "fixture.block")
		id := p.startedID(t)
		if code := c.wait(t); code != 0 {
			t.Fatalf("a detached call exited %d while its operation runs; want 0", code)
		}
		if printed := strings.TrimSpace(c.stdout.String()); printed != id {
			t.Fatalf("a detached call printed %q; want the operation id %s", printed, id)
		}
		released(t, p, id)
	})
}

// TestCtrlCDuringAutoStart: while a call waits for a daemon it started, the first
// Ctrl-C ends the wait. The daemon here cannot bind (a file sits where run/ goes), so
// without the interrupt the call would retry for 10 s.
func TestCtrlCDuringAutoStart(t *testing.T) {
	state := shortDir(t)
	if err := os.WriteFile(filepath.Join(state, "run"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c := droidyard(t, state, "call", "system.health")
	time.Sleep(500 * time.Millisecond) // the handler is installed, the retries under way
	start := time.Now()
	c.cmd.Process.Signal(syscall.SIGINT)
	line := c.expect(t, "interrupted")
	if code := c.wait(t); code != cli.ExitInterrupted {
		t.Errorf("the interrupted call exited %d (%s); want %d", code, line, cli.ExitInterrupted)
	}
	if waited := time.Since(start); waited > 3*time.Second {
		t.Errorf("the call ended %s after Ctrl-C", waited)
	}
}
