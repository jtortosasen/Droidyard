package capability

import "testing"

// Decision (ADR-0101, kept for a job the daemon owns beyond an operation: a UI recording,
// ADR-0113 §4): OwnerStamp stores what an operation's owner would be, and OwnerReaches is the
// rule reaches applies. The UI and the CLI reach any; an MCP session reaches an MCP owner's by
// session or by chat and nothing a UI, CLI or peer started; a peer reaches its own name's.
// Seen failing with the chat compared when it is empty (a session with no chat reached the
// job of another session with none).
func TestOwnerReachesIsADR0101(t *testing.T) {
	chats := map[string]string{"s1": "chat-1", "s2": "chat-1", "s3": "chat-2", "s4": ""}
	chatOf := func(id string) string { return chats[id] }

	session, chat, peer := OwnerStamp(ClientMCP, Origin{ID: "s1", Name: "agent"}, chatOf)
	if session != "s1" || chat != "chat-1" || peer != "" {
		t.Errorf("OwnerStamp(mcp) = %q %q %q, want s1, chat-1 and no peer", session, chat, peer)
	}
	if _, chat, peer := OwnerStamp(ClientPeer, Origin{ID: "link", Name: "anna"}, chatOf); chat != "" || peer != "anna" {
		t.Errorf("OwnerStamp(peer) chat %q peer %q, want no chat and anna", chat, peer)
	}
	if _, chat, peer := OwnerStamp(ClientUI, Origin{ID: "ui-1"}, chatOf); chat != "" || peer != "" {
		t.Errorf("OwnerStamp(ui) chat %q peer %q, want none", chat, peer)
	}

	mcpOwner := func(who Origin) bool { return OwnerReaches(ClientMCP, "s1", "chat-1", "", who, chatOf) }
	for _, c := range []struct {
		name string
		who  Origin
		want bool
	}{
		{"the UI", Origin{Client: ClientUI}, true},
		{"the CLI", Origin{Client: ClientCLI, ID: "c"}, true},
		{"its own session", Origin{Client: ClientMCP, ID: "s1"}, true},
		{"another session of its chat", Origin{Client: ClientMCP, ID: "s2"}, true},
		{"a session of another chat", Origin{Client: ClientMCP, ID: "s3"}, false},
		{"a session with no chat", Origin{Client: ClientMCP, ID: "s4"}, false},
		{"an MCP call with no session", Origin{Client: ClientMCP}, false},
		{"a peer", Origin{Client: ClientPeer, ID: "link", Name: "anna"}, false},
	} {
		if got := mcpOwner(c.who); got != c.want {
			t.Errorf("an MCP owner is reached by %s = %v, want %v", c.name, got, c.want)
		}
	}
	// An owner with no chat is matched by an MCP caller with none only through the session.
	if OwnerReaches(ClientMCP, "s4", "", "", Origin{Client: ClientMCP, ID: "s9"}, func(string) string { return "" }) {
		t.Errorf("a session with no chat reached a recording of another session that had none")
	}
	uiOwner := func(who Origin) bool { return OwnerReaches(ClientUI, "ui-1", "", "", who, chatOf) }
	if uiOwner(Origin{Client: ClientMCP, ID: "s1"}) || !uiOwner(Origin{Client: ClientCLI}) {
		t.Errorf("a UI owner: want an MCP session refused and the CLI let in")
	}
	peerOwner := func(who Origin) bool { return OwnerReaches(ClientPeer, "link", "", "anna", who, chatOf) }
	if !peerOwner(Origin{Client: ClientPeer, ID: "link-2", Name: "anna"}) || peerOwner(Origin{Client: ClientPeer, ID: "link", Name: "bob"}) || peerOwner(Origin{Client: ClientMCP, ID: "link"}) {
		t.Errorf("a peer owner: want its own name on another link let in, another name and an MCP session refused")
	}
}
