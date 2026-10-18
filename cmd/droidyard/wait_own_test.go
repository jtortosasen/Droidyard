package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// configAt writes body as the configuration file of the daemon whose state root is state.
func configAt(t *testing.T, state, body string) {
	t.Helper()
	dir := filepath.Join(state, "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, policy.FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// clientHost is the client daemon's peer state, in this process, reading the
// configuration file in its own state root: what `serve` builds, without the rest of the
// daemon. It is not started.
func clientHost(t *testing.T, state string) *hosting.Host {
	t.Helper()
	env := platform.Env{GOOS: platform.Current().GOOS, UID: os.Getuid(), Getenv: func(k string) string {
		if k == "DROIDYARD_STATE_DIR" {
			return state
		}
		return os.Getenv(k)
	}}
	h := hosting.New(policy.New(env), state, system.Version, os.Stderr)
	if err := h.Init(); err != nil {
		t.Fatal(err)
	}
	return h
}

// pairedWith makes a client that the host p lends to as `anna`, and returns the client's
// link to it once it is up. p must have been started with its configuration naming the
// client's fingerprint: see peerConfigFor.
func pairedWith(t *testing.T, p *inProcess, clientState string) *hosting.Link {
	t.Helper()
	stop := startPeers(p.host, p.reg, p.ops, p.bus, os.Stderr)
	t.Cleanup(stop)
	addr := ""
	eventually(t, "the host's peer listener", func() bool {
		addr = p.host.Health().Hosting.Listen
		return addr != ""
	})
	configAt(t, clientState, `{"remotes":[{"name":"qa-lab","address":"`+addr+`","fingerprint":"`+p.host.Identity().Fingerprint()+`"}]}`)
	c := clientHost(t, clientState)
	c.Start()
	t.Cleanup(c.Stop)
	eventually(t, "the client's link to come up", func() bool { return c.Remote("qa-lab").Connected() })
	return c.Remote("qa-lab")
}

// peerConfigFor is the host configuration that pairs the client whose state root is
// clientState, as `anna`, with a listener on an ephemeral loopback port; it makes the
// client's identity, which the client's daemon then loads.
func peerConfigFor(t *testing.T, clientState string) string {
	t.Helper()
	id, _, err := hosting.LoadIdentity(clientState)
	if err != nil {
		t.Fatal(err)
	}
	return `{"hosting":{"listen":"127.0.0.1:0","peers":[{"name":"anna","fingerprint":"` + id.Fingerprint() + `"}]}}`
}

// idArgs are the arguments of operation_wait (a status poll) or operation_cancel (MCP's
// spelling or the capability's) naming id.
func idArgs(tool, id string) string {
	if tool == "operation_wait" || tool == "operation.wait" {
		return `{"operationId":"` + id + `","timeoutMs":0}`
	}
	return `{"operationId":"` + id + `"}`
}

// earlierOp starts capability as an operation of an MCP session of the given chat that is
// no longer there, and returns its id: what a session resumed in that chat waits on
// (ADR-0101). With wait it returns once the operation has ended. A test that drives the
// MCP adapter on an operation it did not start through a tool starts it this way, since
// an MCP session reaches no other session's operations but its chat's.
func earlierOp(t *testing.T, p *inProcess, chat, capName string, wait bool) string {
	t.Helper()
	session := "earlier-" + capName
	p.ops.Sessions().Connect(session, chat)
	id, err := p.ops.Start(context.Background(), capability.Request{Capability: capName, Client: capability.ClientMCP, Origin: capability.Origin{ID: session, Client: capability.ClientMCP}})
	if err != nil {
		t.Fatal(err)
	}
	if wait {
		if _, err := p.ops.Wait(context.Background(), id, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	p.ops.Sessions().End(session)
	return id
}

// shimInChat is startShim for a session of the given chat.
func shimInChat(t *testing.T, p *inProcess, name, chat string, args ...string) *shim {
	t.Helper()
	return startShimEnv(t, p.state, "", name, []string{chatEnv + "=" + chat}, append([]string{"mcp", "--no-autostart"}, args...))
}

// viewOf decodes the answer to an operation_wait or a long-running tool: the operation
// view of a success, or the error of an isError result.
func viewOf(t *testing.T, c boundCall) (v capability.OperationView, e *capability.Error) {
	t.Helper()
	if c.isError {
		return v, &c.err
	}
	var msg struct {
		Result struct {
			StructuredContent capability.OperationView `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(c.raw, &msg); err != nil {
		t.Fatalf("not a tool result: %s", c.raw)
	}
	return msg.Result.StructuredContent, nil
}

// TestWaitOwnOperations is ADR-0101 through every real adapter: operation_wait and
// operation_cancel of an MCP session reach the operations of that session and of its chat
// and no other's, including a session of the chat that connects after the starting one has
// gone; the UI's WebSocket and the CLI reach any; a peer reaches its own and not the host
// UI's, whose id it learns from the queue its own call waits in. An id that is not
// reachable is answered as an unknown one, and the operation runs on.
func TestWaitOwnOperations(t *testing.T) {
	t.Parallel()
	clientState := shortDir(t)
	hold := make(chan struct{})
	p := startInProcessConfig(t, "127.0.0.1:0", func(reg *capability.Registry, _ platform.Env) {
		// A lent capability that holds one key until released: a second call queues behind
		// the first and learns its operation id from blockedBy.
		reg.MustRegister(capability.Descriptor{
			Name: "fixture.keyed", Kind: capability.Command, Scope: capability.Global, LongRunning: true, Lendable: true,
			Description: "Holds one key until released.", Params: struct{}{}, Result: struct{}{},
			Resource: func(*capability.Call) string { return "keyed" },
			Handler: func(ctx context.Context, _ *capability.Call) (any, error) {
				select {
				case <-hold:
					return struct{}{}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			},
		})
	}, nil, 0, peerConfigFor(t, clientState))
	t.Cleanup(func() { close(hold) })
	link := pairedWith(t, p, clientState)

	// Two agents of two chats start a long-running tool each. fixture_slow holds until
	// the test releases it, and answers its tool call with the operation still running.
	shimOf := func(name, chat string) *shim {
		s := startShimEnv(t, p.state, "", name, []string{chatEnv + "=" + chat}, []string{"mcp", "--no-autostart", "--wait", "300ms"})
		s.initialize(name)
		return s
	}
	a, b := shimOf("a", "chat-a"), shimOf("b", "chat-b")
	started := func() string {
		select {
		case id := <-p.started:
			return id
		case <-time.After(10 * time.Second):
			t.Fatal("fixture_slow never started")
			return ""
		}
	}
	slow := func(s *shim, call int) string {
		t.Helper()
		v, e := viewOf(t, s.toolWith(call, "fixture_slow", `{}`))
		id := started()
		if e != nil || v.OperationID != id || v.State != capability.StateRunning {
			t.Fatalf("%s: fixture_slow answered %+v / %v; want its operation %s running", s.name, v, e, id)
		}
		return id
	}
	opA, opB := slow(a, 1), slow(b, 1)
	stillRunning := func(what, id string) {
		t.Helper()
		if env, _ := waited(t, p, id, "0"); env.Operation == nil || env.Operation.State != capability.StateRunning {
			t.Fatalf("%s: the operation is %+v; want still running", what, env.Operation)
		}
	}

	// B reaches neither wait nor cancel of A's operation, and the answer is an unknown id's.
	_, unknown := viewOf(t, b.toolWith(2, "operation_wait", `{"operationId":"op-0000000000000000","timeoutMs":0}`))
	for i, tool := range []string{"operation_wait", "operation_cancel"} {
		_, e := viewOf(t, b.toolWith(3+i, tool, idArgs(tool, opA)))
		if e == nil || e.Code != capability.ErrOperationNotFound {
			t.Errorf("B's %s on A's operation: %v; want OPERATION_NOT_FOUND", tool, e)
		}
		if unknown == nil || e == nil || e.Message[:len("no operation")] != unknown.Message[:len("no operation")] {
			t.Errorf("B's %s on A's operation answers %v, not like an unknown id (%v)", tool, e, unknown)
		}
	}
	stillRunning("after B's wait and cancel", opA)

	// A reaches its own.
	if v, e := viewOf(t, a.toolWith(5, "operation_wait", `{"operationId":"`+opA+`","timeoutMs":0}`)); e != nil || v.State != capability.StateRunning {
		t.Errorf("A's operation_wait on its own: %+v / %v; want running", v, e)
	}

	// A's session ends. A new session of A's chat reaches the operation its predecessor
	// started: the owner is recorded when the operation starts, not read from a session
	// that is gone. A new session of B's chat does not.
	sessionOf := func(opID string) string {
		for _, op := range p.ops.Active() {
			if op.OperationID == opID && op.Session != nil {
				return op.Session.ID
			}
		}
		t.Fatalf("operation %s lists no session", opID)
		return ""
	}
	sessionA := sessionOf(opA)
	if code := a.exit(); code != 0 {
		t.Fatalf("A's shim exited %d", code)
	}
	eventually(t, "A's session to leave the registry", func() bool { return !p.ops.Sessions().Registered(sessionA) })
	a2 := shimOf("a2", "chat-a")
	if v, e := viewOf(t, a2.toolWith(1, "operation_wait", `{"operationId":"`+opA+`","timeoutMs":0}`)); e != nil || v.OperationID != opA || v.State != capability.StateRunning {
		t.Errorf("a later session of A's chat: operation_wait %+v / %v; want A's operation running", v, e)
	}
	if _, e := viewOf(t, a2.toolWith(2, "operation_wait", `{"operationId":"`+opB+`","timeoutMs":0}`)); e == nil || e.Code != capability.ErrOperationNotFound {
		t.Errorf("a session of A's chat on B's operation: %v; want OPERATION_NOT_FOUND", e)
	}

	// The UI's WebSocket and the CLI reach any, an MCP session's included.
	ui := wsConnect(t, p)
	zero := int64(0)
	for _, id := range []string{opA, opB} {
		ui.send("w-"+id, "operation.wait", "", `{"operationId":"`+id+`","timeoutMs":0}`, nil)
		if f := ui.must("the UI's wait"); f.Error != nil || f.Operation == nil || f.Operation.ID != id || f.Operation.State != capability.StateRunning {
			t.Errorf("the UI waits on %s: %+v (error %v); want it running", id, f.Operation, f.Error)
		}
		stillRunning("the CLI waits", id)
	}

	// The host's UI starts a keyed operation. The peer's own call queues behind it and sees
	// its id in blockedBy; with that id the peer reaches nothing, and its own queued
	// operation is its to wait on.
	ui.send("k", "fixture.keyed", "", "", &zero)
	uiOp := ui.must("the UI's keyed call")
	if uiOp.Operation == nil || uiOp.Operation.State != capability.StateRunning {
		t.Fatalf("the UI's keyed call: %+v; want running", uiOp)
	}
	queued, err := link.Call(context.Background(), capability.RequestEnvelope{ID: "q", Capability: "fixture.keyed", WaitMs: &zero})
	if err != nil || queued.Operation == nil || queued.Operation.State != capability.StateQueued || queued.Operation.BlockedBy == nil || queued.Operation.BlockedBy.OperationID != uiOp.Operation.ID {
		t.Fatalf("the peer's keyed call: %+v / %v; want queued behind the UI's %s", queued.Operation, err, uiOp.Operation.ID)
	}
	for _, name := range []string{"operation.wait", "operation.cancel"} {
		env, err := link.Call(context.Background(), capability.RequestEnvelope{ID: "x", Capability: name, Params: json.RawMessage(idArgs(name, uiOp.Operation.ID))})
		if err != nil || env.Error == nil || env.Error.Code != capability.ErrOperationNotFound {
			t.Errorf("the peer's %s on the UI's operation: %+v / %v; want OPERATION_NOT_FOUND", name, env.Error, err)
		}
	}
	stillRunning("after the peer's wait and cancel", uiOp.Operation.ID)
	if env, err := link.Call(context.Background(), capability.RequestEnvelope{ID: "y", Capability: "operation.wait", Params: json.RawMessage(`{"operationId":"` + queued.Operation.ID + `","timeoutMs":0}`)}); err != nil || env.Error != nil || env.Operation == nil || env.Operation.State != capability.StateQueued {
		t.Errorf("the peer waits on its own queued operation: %+v / %v; want it queued", env.Operation, err)
	}
	// And the host's agents do not reach the peer's, nor the peer the agents'.
	if _, e := viewOf(t, a2.toolWith(3, "operation_wait", `{"operationId":"`+queued.Operation.ID+`","timeoutMs":0}`)); e == nil || e.Code != capability.ErrOperationNotFound {
		t.Errorf("an MCP session on the peer's operation: %v; want OPERATION_NOT_FOUND", e)
	}
	if env, err := link.Call(context.Background(), capability.RequestEnvelope{ID: "z", Capability: "operation.wait", Params: json.RawMessage(`{"operationId":"` + opB + `","timeoutMs":0}`)}); err != nil || env.Error == nil || env.Error.Code != capability.ErrOperationNotFound {
		t.Errorf("the peer on an MCP session's operation: %+v / %v; want OPERATION_NOT_FOUND", env.Error, err)
	}

	// An owner cancels its own: A's chat cancels A's operation.
	if _, e := viewOf(t, a2.toolWith(4, "operation_cancel", `{"operationId":"`+opA+`"}`)); e != nil {
		t.Errorf("A's chat cancels A's operation: %v", e)
	}
	if env, _ := waited(t, p, opA, "5000"); env.Operation == nil || env.Operation.State != capability.StateCancelled {
		t.Errorf("after the cancel A's operation is %+v; want cancelled", env.Operation)
	}
	close(p.release)
}
