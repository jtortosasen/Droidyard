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
	"github.com/jtortosasen/droidyard/internal/testtime"
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
	t.Parallel()
	t.Run("SIGKILL does not stop the operation", func(t *testing.T) {
		p := startInProcess(t)
		before := p.d.Sessions()
		c := droidyard(t, p.state, "call", "--no-autostart", "fixture.block")
		id := p.startedID(t)
		c.cmd.Process.Signal(syscall.SIGKILL)
		c.wait(t)
		testtime.Eventually(t, testtime.Bound(10*time.Second), "the killed CLI's session ends", func() bool { return p.d.Sessions() == before })

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
// without the interrupt the call would retry for autostartTimeout, 10 s.
//
// The interrupt goes once the call has printed that its wait is under way
// (autostartWaiting, a seam), so no sleep guesses when the handler is installed. The bound
// on the call's end separates "ended by the interrupt" from "ended by its own 10 s": it is
// scaled like a hang guard but held below the 10 s it must stay apart from. Seen failing with
// the interrupt ending no wait (connectOrStart's case on interrupt removed): the call retried
// its 10 s and the bound passed first.
func TestCtrlCDuringAutoStart(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	if err := os.WriteFile(filepath.Join(state, "run"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c := droidyardEnv(t, state, "", []string{autostartWaitEnv + "=1"}, "call", "system.health")
	c.expect(t, autostartWaitLine)
	start := time.Now()
	c.cmd.Process.Signal(syscall.SIGINT)
	bound := min(testtime.Bound(3*time.Second), autostartTimeout-time.Second)
	var line string
	select {
	case l, ok := <-c.lines:
		if !ok {
			t.Fatalf("the interrupted call exited before printing \"interrupted\", %s after Ctrl-C", time.Since(start))
		}
		line = l
	case <-time.After(bound):
		t.Fatalf("the call printed nothing %s after Ctrl-C; want it to end its wait (its own limit is %s)", bound, autostartTimeout)
	}
	if !strings.Contains(line, "interrupted") {
		t.Fatalf("the call printed %q after Ctrl-C; want \"interrupted\"", line)
	}
	if code := c.wait(t); code != cli.ExitInterrupted {
		t.Errorf("the interrupted call exited %d (%s); want %d", code, line, cli.ExitInterrupted)
	}
}
