package capability

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The decisions of ADR-0101 (operation.wait and operation.cancel reach one's own
// operations) and ADR-0098 §3 (a peer reaches only what a host lends), at the core: the
// adapters pass the origin, and the core is the fence (ADR-0015 §6).

// ownerStore is a store whose one capability, own.block, waits until it is cancelled and
// may be started by every kind of client, a peer's included.
func ownerStore(t *testing.T) (*Operations, chan struct{}) {
	t.Helper()
	started := make(chan struct{}, 16)
	reg := NewRegistry()
	reg.MustRegister(Descriptor{
		Name: "own.block", Kind: Command, Scope: Global, Lendable: true, Description: "Blocks until cancelled.",
		Params: traceParams{}, Result: traceResult{},
		Handler: func(ctx context.Context, call *Call) (any, error) {
			started <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	ops := NewOperations(reg)
	ops.RegisterCapabilities()
	return ops, started
}

// reach calls operation.wait (timeoutMs 0, a status poll) or operation.cancel on id as the
// caller, through Invoke, as every adapter does.
func reach(t *testing.T, ops *Operations, name string, client ClientKind, origin Origin, id string) (Outcome, error) {
	t.Helper()
	params := json.RawMessage(`{"operationId":"` + id + `"}`)
	if name == OperationWait {
		params = json.RawMessage(`{"operationId":"` + id + `","timeoutMs":0}`)
	}
	return ops.Invoke(context.Background(), Request{Capability: name, Params: params, Client: client, Origin: origin})
}

func wantNotFound(t *testing.T, what string, err error) {
	t.Helper()
	if e, ok := err.(*Error); !ok || e.Code != ErrOperationNotFound {
		t.Errorf("%s: %v; want OPERATION_NOT_FOUND", what, err)
	}
}

// TestWaitAndCancelReachOwnOperations is ADR-0101 whole. An MCP session reaches the
// operations of its own session and of any session of its chat, including one that
// connected after the operation's session ended; another session, with another chat or
// none, is told OPERATION_NOT_FOUND in the very words an unknown id gets, and its cancel
// leaves the operation running. The UI and the CLI reach any. A peer reaches those of its
// own name, over any of its links, and no other's, nor a host session's.
func TestWaitAndCancelReachOwnOperations(t *testing.T) {
	ops, started := ownerStore(t)
	ctx := context.Background()
	ops.Sessions().Connect("a", "chat-1")
	ops.Sessions().Connect("b", "")
	ops.Sessions().Connect("d", "chat-2")

	startAs := func(client ClientKind, origin Origin) string {
		t.Helper()
		id, err := ops.Start(ctx, Request{Capability: "own.block", Client: client, Origin: origin})
		if err != nil {
			t.Fatal(err)
		}
		<-started
		t.Cleanup(func() { ops.Cancel(id) })
		return id
	}
	mcpOf := func(id string) Origin { return Origin{ID: id, Client: ClientMCP} }
	peerOf := func(name, id string) Origin { return Origin{ID: id, Name: name, Client: ClientPeer} }

	opA := startAs(ClientMCP, mcpOf("a"))
	opUI := startAs(ClientUI, Origin{ID: "ws-1"})
	opAnna := startAs(ClientPeer, peerOf("anna", "link-1"))

	state := func(id string) State {
		t.Helper()
		out, err := ops.Wait(ctx, id, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return out.State
	}

	// The unknown id's answer, which every refusal below must equal.
	_, unknown := reach(t, ops, OperationWait, ClientCLI, Origin{}, "op-0000000000000000")
	wantNotFound(t, "an unknown id", unknown)
	sameWords := func(what string, id string, err error) {
		t.Helper()
		wantNotFound(t, what, err)
		if e, ok := err.(*Error); ok && unknown != nil && strings.ReplaceAll(e.Message, id, "X") != strings.ReplaceAll(unknown.(*Error).Message, "op-0000000000000000", "X") {
			t.Errorf("%s: %q differs from an unknown id's %q: the answer tells whose the operation is", what, e.Message, unknown.(*Error).Message)
		}
	}

	// Another MCP session: neither wait nor cancel, and the operation runs on.
	_, err := reach(t, ops, OperationWait, ClientMCP, mcpOf("b"), opA)
	sameWords("B waits on A's operation", opA, err)
	_, err = reach(t, ops, OperationCancel, ClientMCP, mcpOf("b"), opA)
	sameWords("B cancels A's operation", opA, err)
	_, err = reach(t, ops, OperationWait, ClientMCP, mcpOf("d"), opA)
	sameWords("a session of another chat waits on A's operation", opA, err)
	if got := state(opA); got != StateRunning {
		t.Fatalf("A's operation is %s after B's cancel; want running", got)
	}

	// The session itself, then a session of its chat after it has gone.
	if out, err := reach(t, ops, OperationWait, ClientMCP, mcpOf("a"), opA); err != nil || out.State != StateRunning {
		t.Errorf("A waits on its own operation: %+v, %v; want running", out, err)
	}
	ops.Sessions().End("a")
	ops.Sessions().Connect("a2", "chat-1")
	if out, err := reach(t, ops, OperationWait, ClientMCP, mcpOf("a2"), opA); err != nil || out.State != StateRunning {
		t.Errorf("a later session of A's chat waits on its operation: %+v, %v; want running", out, err)
	}

	// The humans' channels reach any operation, an MCP session's and a peer's included.
	for _, c := range []ClientKind{ClientUI, ClientCLI} {
		for _, id := range []string{opA, opUI, opAnna} {
			if _, err := reach(t, ops, OperationWait, c, Origin{ID: "h"}, id); err != nil {
				t.Errorf("%s waits on %s: %v; want the outcome", c, id, err)
			}
		}
	}

	// MCP and a peer do not reach a UI operation, nor each other's.
	_, err = reach(t, ops, OperationWait, ClientMCP, mcpOf("a2"), opUI)
	wantNotFound(t, "an MCP session waits on a UI operation", err)
	_, err = reach(t, ops, OperationWait, ClientMCP, mcpOf("a2"), opAnna)
	wantNotFound(t, "an MCP session waits on a peer's operation", err)
	_, err = reach(t, ops, OperationWait, ClientPeer, peerOf("anna", "link-1"), opUI)
	wantNotFound(t, "a peer waits on a UI operation", err)
	_, err = reach(t, ops, OperationWait, ClientPeer, peerOf("anna", "link-1"), opA)
	wantNotFound(t, "a peer waits on an MCP session's operation", err)

	// A peer reaches its own, on a link other than the one that started it; another peer
	// does not, and neither does a peer with no name.
	if out, err := reach(t, ops, OperationWait, ClientPeer, peerOf("anna", "link-2"), opAnna); err != nil || out.State != StateRunning {
		t.Errorf("anna waits on her operation over a second link: %+v, %v; want running", out, err)
	}
	_, err = reach(t, ops, OperationCancel, ClientPeer, peerOf("bob", "link-3"), opAnna)
	wantNotFound(t, "bob cancels anna's operation", err)
	_, err = reach(t, ops, OperationWait, ClientPeer, peerOf("", "link-4"), opAnna)
	wantNotFound(t, "a peer with no name waits on anna's operation", err)
	if got := state(opAnna); got != StateRunning {
		t.Fatalf("anna's operation is %s after bob's cancel; want running", got)
	}

	// An MCP call that came without a session owns nothing, and reaches nothing.
	_, err = reach(t, ops, OperationWait, ClientMCP, Origin{}, opA)
	wantNotFound(t, "an MCP call with no session waits on A's operation", err)

	// Each cancels its own.
	if _, err := reach(t, ops, OperationCancel, ClientPeer, peerOf("anna", "link-2"), opAnna); err != nil {
		t.Errorf("anna cancels her operation: %v", err)
	}
	if out, _ := ops.Wait(ctx, opAnna, time.Time{}); out.State != StateCancelled {
		t.Errorf("anna's operation is %s after her cancel; want cancelled", out.State)
	}
	if _, err := reach(t, ops, OperationCancel, ClientMCP, mcpOf("a2"), opA); err != nil {
		t.Errorf("a session of A's chat cancels A's operation: %v", err)
	}
	if out, _ := ops.Wait(ctx, opA, time.Time{}); out.State != StateCancelled {
		t.Errorf("A's operation is %s after its chat's cancel; want cancelled", out.State)
	}
}

// TestPeerReachesOnlyWhatIsLendable is ADR-0098 §3 at the core: a peer's call to a
// capability that is not Lendable is REQUEST_NOT_ALLOWED_HERE before anything is
// recorded, whatever its audience, scope or parameters; a Lendable one runs as client
// peer, and the peer's configured name is the session name its operation carries
// (ADR-0098 §5). The same capability stays callable by every other kind.
func TestPeerReachesOnlyWhatIsLendable(t *testing.T) {
	reg := NewRegistry()
	ops := NewOperations(reg)
	ops.RegisterCapabilities()
	ran := 0
	handler := func(context.Context, *Call) (any, error) { ran++; return traceResult{}, nil }
	reg.MustRegister(Descriptor{Name: "lend.ok", Kind: Query, Scope: Global, Lendable: true, Description: "Lent.",
		Params: traceParams{}, Result: traceResult{}, Handler: handler})
	reg.MustRegister(Descriptor{Name: "lend.no", Kind: Query, Scope: Global, Description: "Not lent.",
		Params: traceParams{}, Result: traceResult{}, Handler: handler})
	reg.MustRegister(Descriptor{Name: "lend.human", Kind: Command, Scope: Global, Audience: AudienceHuman, Description: "Human only.",
		Params: traceParams{}, Result: traceResult{}, Handler: handler})
	reg.MustRegister(Descriptor{Name: "lend.scoped", Kind: Query, Scope: Workspace, Description: "In a workspace.",
		Params: traceParams{}, Result: traceResult{}, Handler: handler})
	rec := &traceRecorder{ops: ops}
	ops.UseTrace(rec.logf)
	ctx := context.Background()
	peer := Origin{ID: "link-1", Name: "anna", Client: ClientPeer}

	for _, name := range []string{"lend.no", "lend.human", "lend.scoped", "no.such"} {
		req := Request{Capability: name, Client: ClientPeer, Origin: peer}
		_, err := ops.Start(ctx, req)
		want := ErrNotAllowedHere
		if name == "no.such" {
			want = ErrCapabilityNotFound
		}
		if e, ok := err.(*Error); !ok || e.Code != want {
			t.Errorf("Start %s as a peer: %v; want %s", name, err, want)
		}
		if _, err := ops.Invoke(ctx, req); err == nil {
			t.Errorf("Invoke %s as a peer: no error", name)
		}
	}
	if ran != 0 || ops.Running() != 0 || len(ops.Active()) != 0 || len(rec.snapshot()) != 0 {
		t.Errorf("a refused peer call ran %d handlers, left %d operations running and %d trace lines; want none of them", ran, ops.Running(), len(rec.snapshot()))
	}

	out, err := ops.Invoke(ctx, Request{Capability: "lend.ok", Client: ClientPeer, Origin: peer})
	if err != nil || out.State != StateCompleted {
		t.Fatalf("a peer calls a lendable capability: %+v, %v", out, err)
	}
	var active []ActiveOperation
	hold := make(chan struct{})
	reg.MustRegister(Descriptor{Name: "lend.hold", Kind: Query, Scope: Global, Lendable: true, Description: "Lent, held.",
		Params: traceParams{}, Result: traceResult{},
		Handler: func(ctx context.Context, _ *Call) (any, error) { <-hold; return traceResult{}, nil }})
	id, err := ops.Start(ctx, Request{Capability: "lend.hold", Client: ClientPeer, Origin: peer})
	if err != nil {
		t.Fatal(err)
	}
	active = ops.Active()
	close(hold)
	if len(active) != 1 || active[0].OperationID != id || active[0].Client != ClientPeer || active[0].Session == nil || active[0].Session.Name != "anna" {
		t.Errorf("a peer's operation is listed as %+v; want client peer and session name anna", active)
	}

	// The same capability, called by the other kinds, is unchanged.
	for _, c := range []ClientKind{ClientCLI, ClientMCP, ClientUI} {
		if _, err := ops.Invoke(ctx, Request{Capability: "lend.no", Client: c}); err != nil {
			t.Errorf("%s calls a capability that is not lent: %v", c, err)
		}
	}
}

// TestRegisterRefusesLendableThatIsNotGlobal: a peer is another person's daemon, so a
// Lendable capability that takes a workspace would let it name the host's, and one that
// moves a session's binding has no peer session to move. Registration refuses both, and
// RawTarget, which is the same door.
func TestRegisterRefusesLendableThatIsNotGlobal(t *testing.T) {
	ok := func(context.Context, *Call) (any, error) { return traceResult{}, nil }
	base := Descriptor{Name: "lend.x", Kind: Query, Scope: Global, Description: "x", Params: traceParams{}, Result: traceResult{}, Handler: ok, Lendable: true}
	if err := NewRegistry().Register(base); err != nil {
		t.Fatalf("a global Lendable capability is refused: %v", err)
	}
	scoped := base
	scoped.Scope = Workspace
	bound := base
	bound.Binding = true
	raw := base
	raw.RawTarget = true
	for name, d := range map[string]Descriptor{"workspace-scoped": scoped, "binding": bound, "raw target": raw} {
		if err := NewRegistry().Register(d); err == nil || !strings.Contains(err.Error(), "Lendable") {
			t.Errorf("a Lendable %s capability: %v; want a registration error naming Lendable", name, err)
		}
	}
}
