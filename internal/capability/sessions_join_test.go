package capability

import (
	"context"
	"testing"
	"time"
)

// TestJoinedIsTheChatsMostRecentlySeenSession holds ADR-0095 §1: a chat joins its live
// MCP session, the most recently seen when two share it; a chat nobody has, an empty
// chat, and a session that ended join no one. The mutations that prove it: choosing the
// first-connected session instead of the most recently seen fails the second check; not
// matching on the chat fails the third.
func TestJoinedIsTheChatsMostRecentlySeenSession(t *testing.T) {
	s, _, clk := occupied(OccupationRefuse)
	connectAt(s, "a1", "chat-a", "claude-code")
	clk.advance(time.Second)
	connectAt(s, "a2", "chat-a", "claude-code")
	connectAt(s, "b1", "chat-b", "other")

	if j, ok := s.Joined("chat-a"); !ok || j.ID != "a2" || j.Name != "claude-code" {
		t.Errorf("Joined(chat-a) = %+v, %v; want a2, the later connection, with its name", j, ok)
	}
	clk.advance(time.Second)
	s.Seen("a1")
	if j, _ := s.Joined("chat-a"); j.ID != "a1" {
		t.Errorf("Joined(chat-a) = %q after a1 was seen last; want a1, the most recently seen", j.ID)
	}
	if j, ok := s.Joined("chat-b"); !ok || j.ID != "b1" {
		t.Errorf("Joined(chat-b) = %+v, %v; want b1", j, ok)
	}
	for _, chat := range []string{"", "chat-none"} {
		if j, ok := s.Joined(chat); ok {
			t.Errorf("Joined(%q) = %+v; want no session", chat, j)
		}
	}
	s.End("a1")
	s.End("a2")
	if j, ok := s.Joined("chat-a"); ok {
		t.Errorf("Joined(chat-a) = %+v after both sessions ended; want none", j)
	}
}

// TestJoinedCallIsAttributed holds ADR-0095 §2 in the core: a call whose Origin is joined to
// a session records that session's ref (its id, its name), not the connection's own. The
// mutation that proves it: with the Joined field ignored by sessionRef the operation
// carries the connection's id.
func TestJoinedCallIsAttributed(t *testing.T) {
	ops := NewOperations(NewRegistry())
	ops.reg.MustRegister(Descriptor{
		Name: "fixture.read", Kind: Query, Scope: Global, Idempotent: true,
		Description: "Reads.", Params: struct{}{}, Result: struct{}{},
		Handler: func(_ context.Context, call *Call) (any, error) { return struct{}{}, nil },
	})
	ops.Sessions().Connect("mcp-1", "chat-a")
	ops.Sessions().Named("mcp-1", "claude-code")

	out, err := ops.Invoke(context.Background(), Request{Capability: "fixture.read", Client: ClientCLI,
		Origin: Origin{ID: "cli-9", Client: ClientCLI, Chat: "chat-a", Joined: "mcp-1", Name: "claude-code"}})
	if err != nil {
		t.Fatal(err)
	}
	ops.mu.Lock()
	op := ops.ops[out.OperationID]
	ops.mu.Unlock()
	if op == nil || op.session == nil || op.session.ID != "mcp-1" || op.session.Name != "claude-code" {
		t.Fatalf("the joined call's operation carries the session %+v; want mcp-1, claude-code", op)
	}
}
