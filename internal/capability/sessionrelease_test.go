package capability

import (
	"context"
	"testing"
)

// releaseOps is the operations with the registry of sessions the capability acts on.
func releaseOps() *Operations {
	ops := NewOperations(NewRegistry())
	ops.RegisterCapabilities()
	return ops
}

func releaseAs(ops *Operations, client ClientKind, params string) (Outcome, error) {
	return ops.Invoke(context.Background(), Request{Capability: SessionRelease, Params: []byte(params), Client: client})
}

// TestSessionReleaseFreesOutright is the decision of ADR-0094 §3: the owner's release frees
// the worktree, and the handover that a session's own release makes to another live session
// of its chat (ADR-0094 §1) is not applied, so the second session of the chat is not left
// holding what the owner just took away; and it never changes a binding. A device is freed
// the same way, and what nobody holds answers as done.
func TestSessionReleaseFreesOutright(t *testing.T) {
	ops := releaseOps()
	s := ops.Sessions()
	for _, id := range []string{"a1", "a2"} {
		s.Connect(id, "chat-a")
		s.Opened(id, "/main", "ws-main", "/main")
	}
	s.Occupy("a1", "ws-wt1", "/wt1", true)
	s.Occupy("a2", "ws-wt1", "/wt1", true) // the same chat: bound, not holding
	s.Connect("b", "chat-b")
	s.Opened("b", "/main", "ws-main", "/main")
	s.OccupyDevice("a1", "emulator-5554")

	if out, err := releaseAs(ops, ClientUI, `{"workspace":"ws-wt1"}`); err != nil || out.State != StateCompleted {
		t.Fatalf("session.release {workspace} = (%+v, %v)", out, err)
	}
	rows := map[string]SessionListed{}
	for _, r := range s.listed() {
		rows[r.ID] = r
	}
	if rows["a1"].Occupies != "" || rows["a2"].Occupies != "" {
		t.Errorf("after the owner's release a1 occupies %q and a2 %q; want neither: the worktree is free outright, no handover to the chat's other session", rows["a1"].Occupies, rows["a2"].Occupies)
	}
	if rows["a1"].Workspace != "ws-wt1" {
		t.Errorf("a1 is bound to %q after the release; want ws-wt1: the owner's release never moves a session", rows["a1"].Workspace)
	}
	if h, refused := s.Occupy("b", "ws-wt1", "/wt1", true); refused {
		t.Fatalf("b entering the released worktree was refused (%+v)", h)
	}

	if _, err := releaseAs(ops, ClientUI, `{"device":"emulator-5554"}`); err != nil {
		t.Fatal(err)
	}
	if _, refused := s.OccupyDevice("b", "emulator-5554"); refused {
		t.Error("b was refused the released device")
	}
	for _, p := range []string{`{"workspace":"ws-none"}`, `{"device":"emulator-9"}`} {
		if out, err := releaseAs(ops, ClientCLI, p); err != nil || out.State != StateCompleted {
			t.Errorf("session.release %s of what nobody holds = (%+v, %v); want done", p, out, err)
		}
	}
}

// TestSessionReleaseTakesExactlyOne is the shape of ADR-0094 §3: both fields or neither is
// REQUEST_INVALID_PARAMS and releases nothing, and the core refuses the call to an MCP
// session as a person's action (ADR-0015 §6).
func TestSessionReleaseTakesExactlyOne(t *testing.T) {
	ops := releaseOps()
	s := ops.Sessions()
	s.Connect("a", "chat-a")
	s.Opened("a", "/main", "ws-main", "/main")
	s.Occupy("a", "ws-wt1", "/wt1", true)
	s.OccupyDevice("a", "emulator-5554")

	for _, p := range []string{`{}`, `{"workspace":"ws-wt1","device":"emulator-5554"}`} {
		out, err := releaseAs(ops, ClientUI, p)
		if err == nil {
			err = out.Error
		}
		if codeOf(err) != ErrInvalidParams {
			t.Errorf("session.release %s = %v; want %s", p, err, ErrInvalidParams)
		}
	}
	if _, err := releaseAs(ops, ClientMCP, `{"device":"emulator-5554"}`); codeOf(err) != ErrNotAllowedHere {
		t.Errorf("session.release over MCP = %v; want %s", err, ErrNotAllowedHere)
	}
	rows := s.listed()
	if rows[0].Occupies != "ws-wt1" || len(rows[0].Devices) != 1 {
		t.Errorf("the refused calls left the row %+v; want both holds kept", rows[0])
	}
}
