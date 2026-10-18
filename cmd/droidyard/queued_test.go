package main

import (
	"context"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// A queued operation, seen through every adapter (plan m1e, step 5): `operation.wait`
// with timeoutMs 0 on a call behind another shows state queued and blockedBy naming the
// holder, on the CLI, POST /v1/call and MCP operation_wait. Each adapter builds its
// own projection of the Outcome, so one passing says nothing of another.
func TestQueuedOperationOnEveryAdapter(t *testing.T) {
	t.Parallel()
	release, started := make(chan struct{}), make(chan string, 4)
	p := startInProcessWith(t, func(reg *capability.Registry) {
		reg.MustRegister(capability.Descriptor{
			Name: "fixture.keyed", Kind: capability.Command, Scope: capability.Global,
			Description: "Holds the key k until released.", Params: struct{}{}, Result: struct{}{},
			Resource: func(*capability.Call) string { return "k" },
			Errors:   []capability.ErrorCode{capability.ErrOperationQueueTimeout},
			Handler: func(ctx context.Context, call *capability.Call) (any, error) {
				started <- call.OperationID
				select {
				case <-release:
					return struct{}{}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			},
		})
	})
	t.Cleanup(func() { close(release) })

	// The operations are an MCP session's, of a chat the MCP subtest's shim resumes: an
	// MCP session reaches the operations of its own chat only (ADR-0101).
	p.ops.Sessions().Connect("earlier", "chat-queued")
	earlier := capability.Request{Capability: "fixture.keyed", Client: capability.ClientMCP, Origin: capability.Origin{ID: "earlier"}}
	first, err := p.ops.Start(context.Background(), earlier)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	second, err := p.ops.Start(context.Background(), earlier)
	if err != nil {
		t.Fatal(err)
	}
	params := `{"operationId":"` + second + `","timeoutMs":0}`

	t.Run("CLI", func(t *testing.T) {
		env, _ := p.call(t, "operation.wait", "--params", params)
		if env.Operation == nil || env.Operation.State != capability.StateQueued || env.Operation.BlockedBy == nil ||
			env.Operation.BlockedBy.OperationID != first || env.Operation.BlockedBy.Capability != "fixture.keyed" {
			t.Errorf("CLI operation.wait: %+v (blockedBy %+v); want queued behind %s", env.Operation, blockedOf(env), first)
		}
	})
	t.Run("HTTP", func(t *testing.T) {
		addr := p.d.HTTPAddr()
		env := envelopeOf(t, callRequest(portOf(t, addr), p.token, `{"id":"w","capability":"operation.wait","params":`+params+`}`).send(t, addr))
		if env.Operation == nil || env.Operation.State != capability.StateQueued || env.Operation.BlockedBy == nil ||
			env.Operation.BlockedBy.OperationID != first {
			t.Errorf("POST /v1/call operation.wait: %+v (blockedBy %+v); want queued behind %s", env.Operation, blockedOf(env), first)
		}
	})
	t.Run("MCP", func(t *testing.T) {
		c := startShimEnv(t, p.state, "", "client", []string{chatEnv + "=chat-queued"}, []string{"mcp", "--no-autostart", "--wait", "200ms"})
		c.initialize("queued")
		c.callTool(1, "operation_wait", params)
		r := decodeTool(t, c.recv(), 1)
		if r.isError || r.view.State != capability.StateQueued || r.view.BlockedBy == nil ||
			r.view.BlockedBy.OperationID != first || r.view.BlockedBy.Capability != "fixture.keyed" {
			t.Errorf("MCP operation_wait: %v (view %+v); want queued behind %s", r.raw, r.view, first)
		}
	})
}

func blockedOf(env capability.ResponseEnvelope) *capability.BlockedBy {
	if env.Operation == nil {
		return nil
	}
	return env.Operation.BlockedBy
}
