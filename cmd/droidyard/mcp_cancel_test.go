package main

import (
	"encoding/json"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// startBlockingCall opens an MCP session, calls fixture_block as request 5 and returns
// the shim, the operation id the fixture reported, and the session count before the
// shim connected.
func startBlockingCall(t *testing.T, p *inProcess) (*shim, string, int) {
	t.Helper()
	before := p.d.Sessions()
	c := startShim(t, p.state, "client")
	c.initialize("cancellation")
	c.callTool(5, "fixture_block", `{}`)
	select {
	case id := <-p.started:
		return c, id, before
	case <-time.After(10 * time.Second):
		t.Fatal("fixture_block never started")
	}
	return nil, "", 0
}

// waited reads an operation's outcome through `droidyard call operation.wait`.
func waited(t *testing.T, p *inProcess, id, timeout string) (capability.ResponseEnvelope, int) {
	t.Helper()
	params := `{"operationId":"` + id + `"`
	if timeout != "" {
		params += `,"timeoutMs":` + timeout
	}
	return p.call(t, "operation.wait", "--params", params+"}")
}

// TestMCPCancellation is ADR-0025 criterion 5 (the acceptance gate): a
// notifications/cancelled for the call that started an operation cancels it; a
// disconnect, by a killed shim or by the client ending its input, never does
// (ADR-0020 §Adapter projections; ADR-0019 §Sessions).
func TestMCPCancellation(t *testing.T) {
	t.Run("a: notifications/cancelled cancels the operation", func(t *testing.T) {
		p := startInProcess(t)
		c, id, _ := startBlockingCall(t, p)
		c.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":5,"reason":"the user interrupted"}}`)

		env, code := waited(t, p, id, "")
		if code == 0 || env.Operation == nil || env.Operation.ID != id || env.Operation.State != capability.StateCancelled ||
			env.Error == nil || env.Error.Code != capability.ErrOperationCancelled {
			t.Fatalf("after notifications/cancelled, operation.wait exited %d with %+v (error %v); want non-zero, %s cancelled, OPERATION_CANCELLED", code, env.Operation, env.Error, id)
		}
		if code := c.exit(); code != 0 {
			t.Errorf("the shim exited %d; want 0", code)
		}
	})

	t.Run("b: killing the shim does not cancel", func(t *testing.T) {
		p := startInProcess(t)
		c, id, before := startBlockingCall(t, p)
		c.cmd.Process.Signal(syscall.SIGKILL)
		c.wait()
		eventually(t, "the daemon ends the killed shim's session", func() bool { return p.d.Sessions() == before })
		released(t, p, id)
	})

	t.Run("c: ending the shim's input does not cancel", func(t *testing.T) {
		p := startInProcess(t)
		c, id, before := startBlockingCall(t, p)
		if code := c.exit(); code != 0 {
			t.Fatalf("the shim exited %d when its stdin closed; want 0. stderr: %s", code, c.stderr)
		}
		// The session has ended: nothing is written to it, not even the answer to
		// the call it abandoned (owner decision, 2026-09-30).
		if len(c.trailing) != 0 {
			t.Errorf("the ended session was written %d line(s): %s", len(c.trailing), c.trailing[0])
		}
		eventually(t, "the daemon ends the session", func() bool { return p.d.Sessions() == before })
		released(t, p, id)
	})
}

// released checks, after a disconnect, that the operation is still running, then
// releases the fixture and checks that it completes and stays waitable from other
// sessions: the CLI's, and a new MCP session's.
func released(t *testing.T, p *inProcess, id string) {
	t.Helper()
	if env, _ := waited(t, p, id, "0"); env.Operation == nil || env.Operation.State != capability.StateRunning {
		t.Fatalf("after the disconnect the operation is %+v; want still running", env.Operation)
	}
	close(p.release)
	env, code := waited(t, p, id, "")
	if code != 0 || env.Operation == nil || env.Operation.ID != id || env.Operation.State != capability.StateCompleted {
		t.Fatalf("after the release, operation.wait exited %d with %+v (error %v); want 0, %s completed", code, env.Operation, env.Error, id)
	}

	other := startShim(t, p.state, "another session")
	other.initialize("another")
	other.callTool(1, "operation_wait", `{"operationId":"`+id+`"}`)
	var view struct {
		Result struct {
			StructuredContent capability.OperationView `json:"structuredContent"`
			IsError           bool                     `json:"isError"`
		} `json:"result"`
	}
	json.Unmarshal(other.recv(), &view)
	if v := view.Result.StructuredContent; view.Result.IsError || v.OperationID != id || v.State != capability.StateCompleted {
		t.Errorf("operation_wait from another MCP session answered %+v; want %s completed", view.Result, id)
	}
	other.exit()
}
