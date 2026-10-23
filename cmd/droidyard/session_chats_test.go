package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/jtortosasen/droidyard/adapters/cli"
	"github.com/jtortosasen/droidyard/internal/assistant"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/chats"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// Decision (ADR-0127 §1, §2): the daemon wires session.chats to its own parts. The projects it
// reads are those its workspace index knows (and their live git worktrees), the stores are
// those of the daemon's own HOME, and a chat whose id a live MCP session announced carries
// that session's id. This runs a real `serve` with a fake HOME holding two synthetic
// Claude Code transcripts, one per tree, and an MCP session of the first chat. Reversed (no
// Known, the wrong HOME, no join) the chat is not listed, or has no session.
func TestSessionChatsEndToEnd(t *testing.T) {
	home := platform.Current().Canonical(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	w := newOccupationWorld(t)
	a := w.agent(t, "agent-a", "chat-x", w.main)
	_ = a
	transcript := func(tree, id string) {
		dir := filepath.Join(home, ".claude", "projects", chatDir(tree))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		body := `{"type":"user","cwd":` + quoteJSON(tree) + `,"entrypoint":"cli","message":{"content":"hi"}}` + "\n" +
			`{"type":"ai-title","aiTitle":"Title of ` + id + `"}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	transcript(w.main, "chat-x")
	transcript(w.wt1, "chat-w")

	conn, r, _, err := daemon.Dial(sockFor(w.state), daemon.Hello{Protocol: daemon.Protocol, Version: system.Version,
		Client: capability.ClientCLI, Root: w.main})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	env, err := runCall(conn, r, cli.Call{Capability: "session.chats"})
	if err != nil {
		t.Fatal(err)
	}
	if env.Error != nil {
		t.Fatalf("session.chats: %+v", env.Error)
	}
	var res chats.Result
	if err := json.Unmarshal(env.Result, &res); err != nil {
		t.Fatal(err)
	}
	var x, wt *chats.Chat
	for i := range res.Chats {
		switch res.Chats[i].ID {
		case "chat-x":
			x = &res.Chats[i]
		case "chat-w":
			wt = &res.Chats[i]
		}
	}
	row, _ := w.row(t, "agent-a")
	if x == nil || x.Project != w.main || x.Worktree != "" || x.Title != "Title of chat-x" || x.Session != row.ID || row.ID == "" {
		t.Errorf("chat-x: %+v (live session %q); want the main tree's chat, titled, joined to the live session", x, row.ID)
	}
	if wt == nil || wt.Project != w.main || wt.Worktree != w.wt1 || wt.Session != "" {
		t.Errorf("chat-w: %+v; want wt1's chat of the main project, joined to no session", wt)
	}
	if len(res.Sources) != 1 || res.Sources[0].State != chats.StateRead {
		t.Errorf("sources %+v; want claude-code read", res.Sources)
	}
}

// chatDir is Claude Code's directory name for a working directory, spelt here on its own
// (every character but a letter or digit becomes a dash) so the test does not borrow the
// reader's own function as its oracle.
func chatDir(tree string) string {
	return regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(tree, "-")
}

// Decision (ADR-0127 §2): the daemon reads the stores `init --user` writes in, from its own
// environment: CLAUDE_CONFIG_DIR replaces ~/.claude. Reversed the reader looks in ~/.claude
// of a user who keeps Claude Code elsewhere.
func TestClientDirsFollowTheDaemonsEnvironment(t *testing.T) {
	home := platform.Current().Canonical(t.TempDir())
	t.Setenv("HOME", home)
	env := func(custom string) platform.Env {
		return platform.Env{GOOS: platform.Current().GOOS, Getenv: func(k string) string {
			if k == "CLAUDE_CONFIG_DIR" {
				return custom
			}
			return ""
		}}
	}
	if d := clientDirs(env("")); d != assistant.ResolveUserDirs(home, func(string) string { return "" }) {
		t.Errorf("default dirs %+v; want the home's", d)
	}
	custom := filepath.Join(home, "elsewhere")
	if d := clientDirs(env(custom)); d.Claude != custom {
		t.Errorf("with CLAUDE_CONFIG_DIR the Claude directory is %q; want %q", d.Claude, custom)
	}
}
