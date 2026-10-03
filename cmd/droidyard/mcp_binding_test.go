package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/workspace"
)

// boundCall is a tools/call answer as the wire has it: the workspace object for a
// success, the error object for an isError result.
type boundCall struct {
	isError bool
	info    workspace.Info
	err     capability.Error
	raw     []byte
}

// workspaceTool calls a workspace tool with {} and decodes its answer.
func (s *shim) workspaceTool(id int, name string) boundCall {
	s.t.Helper()
	s.callTool(id, name, `{}`)
	line := s.recv()
	var msg struct {
		ID     int `json:"id"`
		Result struct {
			IsError           bool             `json:"isError"`
			StructuredContent json.RawMessage  `json:"structuredContent"`
			Content           []map[string]any `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line, &msg); err != nil || msg.ID != id {
		s.t.Fatalf("%s: expected the answer to %d, got %s", s.name, id, line)
	}
	c := boundCall{isError: msg.Result.IsError, raw: line}
	if c.isError {
		if len(msg.Result.StructuredContent) != 0 || len(msg.Result.Content) != 1 {
			s.t.Fatalf("%s: an error result with structured content or no text: %s", s.name, line)
		}
		text, _ := msg.Result.Content[0]["text"].(string)
		if err := json.Unmarshal([]byte(text), &c.err); err != nil {
			s.t.Fatalf("%s: the error text is not the error object: %s", s.name, line)
		}
		return c
	}
	if len(msg.Result.StructuredContent) > 0 {
		if err := json.Unmarshal(msg.Result.StructuredContent, &c.info); err != nil {
			s.t.Fatalf("%s: structuredContent is not a workspace: %s", s.name, line)
		}
	}
	return c
}

// project makes a project root holding settingsFile, by its canonical path, and a
// subdirectory of it.
func projectWith(t *testing.T, settingsFile string) (root, sub string) {
	t.Helper()
	root = platform.Current().Canonical(t.TempDir())
	sub = filepath.Join(root, "app", "src", "main")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, settingsFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return root, sub
}

// boundReady asserts a workspace_describe answer is the bound root, ready, untrusted.
func boundReady(t *testing.T, what string, c boundCall, root string) {
	t.Helper()
	id, _ := platform.Current().WorkspaceID(root)
	if c.isError || c.info.Root != root || c.info.ID != id || c.info.State != workspace.Ready || c.info.Trusted {
		t.Errorf("%s: %s; want root %s, ready, trusted false", what, c.raw, root)
	}
}

// TestMCPSessionBindsAndOpensItsRoot: an MCP session is bound to --root, else to the
// nearest ancestor of the shim's cwd holding settings.gradle(.kts), else to the cwd;
// it opens that root before it answers anything; workspace tools take no workspace
// argument; the binding grants no trust
// (ADR-0019 §Sessions; ADR-0020 §Adapter projections; ADR-0015 §5).
// Real shims over the real socket, under -race.
func TestMCPSessionBindsAndOpensItsRoot(t *testing.T) {
	p := startInProcess(t)

	// (a) The cwd is a subdirectory of a project with settings.gradle: describe, the
	// first call after initialize, answers ready.
	groovy, sub := projectWith(t, "settings.gradle")
	a := startShimIn(t, p.state, sub, "a", []string{"mcp", "--no-autostart"})
	a.initialize("binding")
	boundReady(t, "(a) describe right after initialize", a.workspaceTool(1, "workspace_describe"), groovy)
	a.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var list struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Properties map[string]any `json:"properties"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	json.Unmarshal(a.recv(), &list)
	seen := 0
	for _, tool := range list.Result.Tools {
		if !strings.HasPrefix(tool.Name, "workspace_") {
			continue
		}
		seen++
		for prop := range tool.InputSchema.Properties {
			if strings.Contains(strings.ToLower(prop), "workspace") || prop == "root" {
				t.Errorf("(a) %s takes %q: agents never pass a workspace", tool.Name, prop)
			}
		}
	}
	if seen != 3 {
		t.Errorf("(a) tools/list has %d workspace tools; want 3", seen)
	}

	// (b) --root wins over the cwd.
	kts, _ := projectWith(t, "settings.gradle.kts")
	b := startShimIn(t, p.state, sub, "b", []string{"mcp", "--no-autostart", "--root", kts})
	b.initialize("binding")
	boundReady(t, "(b) --root over the cwd", b.workspaceTool(1, "workspace_describe"), kts)

	// (c) No ancestor holds a settings file: the cwd is the root.
	bare := platform.Current().Canonical(t.TempDir())
	c := startShimIn(t, p.state, bare, "c", []string{"mcp", "--no-autostart"})
	c.initialize("binding")
	boundReady(t, "(c) the cwd", c.workspaceTool(1, "workspace_describe"), bare)

	// (e) A second session bound to the same root shares the workspace: its open is a
	// no-op, so workspaces.json is not rewritten and createdAt is the same.
	index := filepath.Join(p.state, "workspaces.json")
	before, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	first := a.workspaceTool(3, "workspace_describe")
	a2 := startShimIn(t, p.state, groovy, "a2", []string{"mcp", "--no-autostart"})
	a2.initialize("binding")
	second := a2.workspaceTool(1, "workspace_describe")
	boundReady(t, "(e) the second session", second, groovy)
	if second.info.CreatedAt == nil || first.info.CreatedAt == nil || !second.info.CreatedAt.Equal(*first.info.CreatedAt) {
		t.Errorf("(e) the second session sees createdAt %v; the first %v", second.info.CreatedAt, first.info.CreatedAt)
	}
	if after, _ := os.Stat(index); !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("(e) workspaces.json was rewritten by the second session's open: it was not a no-op")
	}

	// (d) Another client closes the workspace: both sessions' tools get
	// WORKSPACE_NOT_OPEN, again on a second call (nothing reopens it), until
	// workspace_open reopens it.
	if env, code := p.call(t, "--workspace", groovy, "workspace.close"); code != 0 {
		t.Fatalf("(d) the CLI's close exited %d: %+v", code, env.Error)
	}
	for i, s := range []*shim{a, a, a2} {
		got := s.workspaceTool(10+i, "workspace_describe")
		if !got.isError || got.err.Code != capability.ErrWorkspaceNotOpen {
			t.Errorf("(d) %s, call %d after the close: %s; want isError WORKSPACE_NOT_OPEN", s.name, i, got.raw)
		}
	}
	boundReady(t, "(d) workspace_open", a.workspaceTool(20, "workspace_open"), groovy)
	boundReady(t, "(d) describe after the reopen", a2.workspaceTool(21, "workspace_describe"), groovy)

	for _, s := range []*shim{a, b, c, a2} {
		if code := s.exit(); code != 0 {
			t.Errorf("%s exited %d", s.name, code)
		}
	}
}

// TestMCPBadBoundRootDegrades: a bound root that cannot be opened does not fail the
// session: initialize and tools/list succeed, workspace tools get WORKSPACE_NOT_OPEN,
// and workspace_open shows the cause, WORKSPACE_ROOT_INVALID with the bound root as
// its target; the failure is in the daemon log (ADR-0026 §Decision; plan m1c, R13).
func TestMCPBadBoundRootDegrades(t *testing.T) {
	p := startInProcess(t)
	s := startShim(t, p.state, "bad", "--root", "/nonexistent")
	s.initialize("binding")
	s.send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if res := response(t, s.recv(), 1); len(res["tools"].([]any)) == 0 {
		t.Fatal("tools/list is empty")
	}
	if got := s.workspaceTool(2, "workspace_describe"); !got.isError || got.err.Code != capability.ErrWorkspaceNotOpen {
		t.Errorf("describe: %s; want isError WORKSPACE_NOT_OPEN", got.raw)
	}
	got := s.workspaceTool(3, "workspace_open")
	if !got.isError || got.err.Code != workspace.ErrRootInvalid || got.err.Details["target"] != "/nonexistent" {
		t.Errorf("open: %s; want isError WORKSPACE_ROOT_INVALID, target /nonexistent", got.raw)
	}
	if log := p.log.String(); !strings.Contains(log, "session bound to /nonexistent") || !strings.Contains(log, string(workspace.ErrRootInvalid)) {
		t.Errorf("the daemon log does not record the failed bound open:\n%s", log)
	}
	if code := s.exit(); code != 0 {
		t.Errorf("the shim exited %d", code)
	}
}

// TestMCPBoundOpenPrecedesTheFirstAnswer: the adapter opens the bound root before it
// reads the first MCP message, so no tool call can precede the open (plan m1c, R4).
// The open is held on the test hook: initialize gets no answer meanwhile; released,
// initialize is answered, and the first tool call finds the workspace ready.
func TestMCPBoundOpenPrecedesTheFirstAnswer(t *testing.T) {
	p := startInProcess(t)
	root, _ := projectWith(t, "settings.gradle")
	held, release := make(chan struct{}), make(chan struct{})
	p.mcp.SetBoundOpenHook(func() {
		close(held)
		<-release
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	s := startShim(t, p.state, "held", "--root", root)
	s.send(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"held","version":"0"}}}`)
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the bound open never reached the hook")
	}
	s.silent(300 * time.Millisecond)
	p.mcp.SetBoundOpenHook(nil)
	close(release)
	response(t, s.recv(), 0)
	s.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	boundReady(t, "describe after the held open", s.workspaceTool(1, "workspace_describe"), root)
	if code := s.exit(); code != 0 {
		t.Errorf("the shim exited %d", code)
	}
}
