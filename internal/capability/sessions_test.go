package capability

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
)

// bindingFixture registers a Binding capability that records that its handler ran.
func bindingFixture(t *testing.T) (*Operations, *atomic.Int32) {
	t.Helper()
	var ran atomic.Int32
	reg := NewRegistry()
	reg.MustRegister(Descriptor{
		Name: "fixture.enter", Kind: Command, Scope: Global, Binding: true, Idempotent: true,
		Description: "Moves the calling session.", Params: struct{}{}, Result: struct{}{},
		Handler: func(_ context.Context, call *Call) (any, error) {
			ran.Add(1)
			return struct{}{}, nil
		},
	})
	ops := NewOperations(reg)
	ops.RegisterCapabilities()
	return ops, &ran
}

// TestBindingCapabilityNeedsAnMCPSession holds ADR-0082 §4: Start refuses a Binding
// capability, with REQUEST_NOT_ALLOWED_HERE, before the handler can run and before any
// operation is recorded, unless the call's Origin.ID is a session registered in the core's
// Sessions; so a CLI or WebSocket call (an Origin that was never connected, or none) cannot
// move a binding, whichever client it names, while the same call from a connected session
// reaches the handler. The mutation that proves it: with the check removed, the first
// three calls run the handler.
func TestBindingCapabilityNeedsAnMCPSession(t *testing.T) {
	ops, ran := bindingFixture(t)
	records := func() int {
		ops.mu.Lock()
		defer ops.mu.Unlock()
		return len(ops.ops)
	}
	for name, req := range map[string]Request{
		"no origin":                               {Capability: "fixture.enter", Client: ClientCLI},
		"an origin never connected":               {Capability: "fixture.enter", Client: ClientUI, Origin: Origin{ID: "ws-1"}},
		"an MCP call naming no connected session": {Capability: "fixture.enter", Client: ClientMCP, Origin: Origin{ID: "gone"}},
	} {
		for _, call := range []func() error{
			func() error { _, err := ops.Start(context.Background(), req); return err },
			func() error { _, err := ops.Invoke(context.Background(), req); return err },
		} {
			var e *Error
			if err := call(); !errors.As(err, &e) || e.Code != ErrNotAllowedHere {
				t.Errorf("%s: err = %v; want REQUEST_NOT_ALLOWED_HERE", name, err)
			} else if e.NextStep == "" {
				t.Errorf("%s: the refusal carries no nextStep", name)
			}
		}
	}
	if ran.Load() != 0 || records() != 0 {
		t.Fatalf("a refused call ran the handler %d times and recorded %d operations; want none", ran.Load(), records())
	}

	ops.Sessions().Connect("mcp-1", "")
	if _, err := ops.Invoke(context.Background(), Request{Capability: "fixture.enter", Client: ClientMCP, Origin: Origin{ID: "mcp-1"}}); err != nil {
		t.Fatalf("a registered session's call: %v", err)
	}
	if ran.Load() != 1 {
		t.Errorf("the handler ran %d times for the registered session; want 1", ran.Load())
	}
	ops.Sessions().End("mcp-1")
	if _, err := ops.Invoke(context.Background(), Request{Capability: "fixture.enter", Client: ClientMCP, Origin: Origin{ID: "mcp-1"}}); err == nil {
		t.Errorf("a call from a session that has ended was admitted")
	}
}

// TestBindingNeedsAStatelessCapabilityOfEveryAudience: the flag promises an MCP tool that
// moves the calling session, so a descriptor that could never be one (stateful, or reserved
// to humans) is refused at registration, not discovered at the first call.
func TestBindingNeedsAStatelessCapabilityOfEveryAudience(t *testing.T) {
	for name, d := range map[string]Descriptor{
		"stateful":  {Name: "fixture.a", Statefulness: Stateful},
		"streaming": {Name: "fixture.b", Statefulness: Streaming},
		"human":     {Name: "fixture.c", Audience: AudienceHuman},
	} {
		d.Binding, d.Kind, d.Scope = true, Command, Global
		d.Description, d.Params, d.Result = "x", struct{}{}, struct{}{}
		d.Handler = func(context.Context, *Call) (any, error) { return struct{}{}, nil }
		if err := NewRegistry().Register(d); err == nil {
			t.Errorf("%s: a Binding capability was registered", name)
		}
	}
}

// TestSessionsRemembersEachChatsLastMove holds ADR-0088 §2: a move (Bound) is remembered
// under the session's chat; the hello's own binding (Opened) is not, so a new connection
// can read what the chat's last move was; a session with no chat remembers nothing; the
// memory outlives the session; it holds MaxChats chats, dropping the one moved longest
// ago; and Forget drops one. Each assertion fails if its rule is reversed: remembering
// from Opened would overwrite the move before the new connection reads it.
func TestSessionsRemembersEachChatsLastMove(t *testing.T) {
	s := NewSessions()
	s.Connect("a", "chat-x")
	s.Opened("a", "/main", "ws-main", "/main")
	if _, ok := s.Remembered("chat-x"); ok {
		t.Fatalf("the hello's binding was remembered; only a move is")
	}
	s.Bound("a", "ws-wt", "/wt")
	if root, ok := s.Binding("a"); !ok || root != "/wt" {
		t.Fatalf("Binding = (%q, %v) after a move to /wt", root, ok)
	}
	if got := s.Project("a"); got != "/main" {
		t.Errorf("Project = %q after a move; want /main: a move stays within the project", got)
	}
	s.End("a")
	if root, ok := s.Remembered("chat-x"); !ok || root != "/wt" {
		t.Fatalf("Remembered after the session ended = (%q, %v); want /wt", root, ok)
	}
	if s.Registered("a") {
		t.Errorf("an ended session is still registered")
	}

	s.Connect("b", "")
	s.Opened("b", "/main", "ws-main", "/main")
	s.Bound("b", "ws-wt", "/wt")
	if _, ok := s.Remembered(""); ok {
		t.Errorf("a session with no chat left a memory")
	}

	s.Forget("chat-x")
	if _, ok := s.Remembered("chat-x"); ok {
		t.Errorf("Forget left the memory")
	}

	// The bound: MaxChats chats, the one moved longest ago dropped first, and a chat that
	// moves again becomes the newest.
	for i := 0; i < MaxChats; i++ {
		id := fmt.Sprintf("s%d", i)
		s.Connect(id, fmt.Sprintf("chat-%d", i))
		s.Bound(id, "w", fmt.Sprintf("/r%d", i))
	}
	s.Bound("s0", "w", "/again") // chat-0 is the newest now, chat-1 the oldest
	s.Connect("extra", "chat-extra")
	s.Bound("extra", "w", "/extra")
	if _, ok := s.Remembered("chat-1"); ok {
		t.Errorf("chat-1, moved longest ago, survived the %dth chat", MaxChats+1)
	}
	for _, c := range []string{"chat-0", "chat-2", "chat-extra"} {
		if _, ok := s.Remembered(c); !ok {
			t.Errorf("%s was dropped; only the oldest goes", c)
		}
	}
	if root, _ := s.Remembered("chat-0"); root != "/again" {
		t.Errorf("chat-0 remembers %q; want its last move, /again", root)
	}
	if n := len(s.chats); n != MaxChats {
		t.Errorf("%d chats held; want %d", n, MaxChats)
	}
}
