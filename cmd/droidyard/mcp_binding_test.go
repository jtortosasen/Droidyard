package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/schema"
)

// boundCall is a tools/call answer as the wire has it: the structured result for a
// success, the error object for an isError result.
type boundCall struct {
	isError bool
	result  json.RawMessage
	err     capability.Error
	raw     []byte
}

// toolWith is the answer of any tool called with args.
func (s *shim) toolWith(id int, name, args string) boundCall {
	s.t.Helper()
	s.callTool(id, name, args)
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
	c := boundCall{isError: msg.Result.IsError, raw: line, result: msg.Result.StructuredContent}
	if c.isError {
		if len(msg.Result.StructuredContent) != 0 || len(msg.Result.Content) != 1 {
			s.t.Fatalf("%s: an error result with structured content or no text: %s", s.name, line)
		}
		text, _ := msg.Result.Content[0]["text"].(string)
		if err := json.Unmarshal([]byte(text), &c.err); err != nil {
			s.t.Fatalf("%s: the error text is not the error object: %s", s.name, line)
		}
	}
	return c
}

// touchedBy is a fixture_touch answer decoded.
func touchedBy(t *testing.T, c boundCall) touched {
	t.Helper()
	var got touched
	if c.isError || json.Unmarshal(c.result, &got) != nil || got.ID == "" {
		t.Fatalf("fixture_touch answered %s; want the workspace it opened", c.raw)
	}
	return got
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

// TestMCPSessionBindsNothing holds ADR-0137 §2, §3: an MCP session is the machine's. Started
// in one project's folder it lists the same tools it would anywhere, none of them a
// `workspace_` tool; each tool of a project capability has one required `project` property
// with one description, and no other tool has it; a project call names its project, which may
// be any folder, and is opened by that call, not by the session; two sessions started in
// different folders that name one project meet in one workspace; a call that names none, or a
// global tool that is sent one, is REQUEST_INVALID_PARAMS and runs nothing. Real shims over
// the real socket, under -race.
func TestMCPSessionBindsNothing(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	rootA, subA := projectWith(t, "settings.gradle")
	rootB, subB := projectWith(t, "settings.gradle.kts")

	a := startShimIn(t, p.state, subA, "a", []string{"mcp", "--no-autostart"})
	a.initialize("binding")
	a.send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var list struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Properties map[string]struct {
						Description string `json:"description"`
					} `json:"properties"`
					Required []string `json:"required"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	json.Unmarshal(a.recv(), &list)
	scoped := map[string]bool{}
	optional := map[string]bool{}
	for _, d := range p.reg.All() {
		if d.Scope == capability.Workspace {
			scoped[strings.Replace(d.Name, ".", "_", 1)] = true
			optional[strings.Replace(d.Name, ".", "_", 1)] = d.ProjectOptional
		}
	}
	withProject := 0
	for _, tool := range list.Result.Tools {
		if strings.HasPrefix(tool.Name, "workspace_") {
			t.Errorf("the session lists %s: the workspace tools left with the binding", tool.Name)
		}
		prop, has := tool.InputSchema.Properties["project"]
		switch {
		case scoped[tool.Name] && !has:
			t.Errorf("%s is a project tool and has no project property", tool.Name)
		case has && !scoped[tool.Name]:
			t.Errorf("%s takes a project and is no project tool", tool.Name)
		case has:
			withProject++
			// device_install alone takes project for a build only (ADR-0137 §4): its own
			// description, and not required.
			if optional[tool.Name] {
				if prop.Description != schema.ProjectOptionalDescription || slices.Contains(tool.InputSchema.Required, "project") {
					t.Errorf("%s: project is %+v, required %v; want the optional description and not required", tool.Name, prop, tool.InputSchema.Required)
				}
			} else if prop.Description != schema.ProjectDescription || len(tool.InputSchema.Required) == 0 || tool.InputSchema.Required[0] != "project" {
				t.Errorf("%s: project is %+v, required %v; want the one description and first among the required", tool.Name, prop, tool.InputSchema.Required)
			}
		}
	}
	if withProject == 0 {
		t.Fatalf("no tool takes a project: %d tools listed", len(list.Result.Tools))
	}

	// The session started in project A names project B, and the call opens B.
	inB := touchedBy(t, a.toolWith(2, "fixture_touch", `{"project":`+quote(subB)+`}`))
	idB, _ := platform.Current().WorkspaceID(rootB)
	if inB.ID != idB || inB.Root != rootB {
		t.Errorf("a call naming %s ran in %+v; want workspace %s at %s", subB, inB, idB, rootB)
	}
	// A second session, started in neither project, naming A and B: A and B are two workspaces,
	// and the one B is the same for both.
	c := startShimIn(t, p.state, nonProject(t), "c", []string{"mcp", "--no-autostart", "--root", rootB})
	c.initialize("binding")
	again := touchedBy(t, c.toolWith(1, "fixture_touch", `{"project":`+quote(rootB)+`}`))
	if again != inB {
		t.Errorf("a second session naming B ran in %+v; the first ran in %+v; want one workspace", again, inB)
	}
	idA, _ := platform.Current().WorkspaceID(rootA)
	if inA := touchedBy(t, c.toolWith(2, "fixture_touch", `{"project":`+quote(subA)+`}`)); inA.ID != idA || inA.ID == inB.ID {
		t.Errorf("a call naming %s ran in %+v; want its own workspace %s", subA, inA, idA)
	}

	// No project, and a project where none belongs.
	for _, tc := range []struct{ name, tool, args string }{
		{"a project tool with no project", "fixture_touch", `{}`},
		{"a project tool with an empty project", "fixture_touch", `{"project":""}`},
		{"a project tool with a relative project", "fixture_touch", `{"project":"app/src"}`},
		{"a global tool sent a project", "system_health", `{"project":` + quote(rootA) + `}`},
	} {
		got := a.toolWith(10, tc.tool, tc.args)
		if !got.isError || got.err.Code != capability.ErrInvalidParams || got.err.NextStep == "" {
			t.Errorf("%s: %s; want isError REQUEST_INVALID_PARAMS with a nextStep", tc.name, got.raw)
		}
	}

	for _, s := range []*shim{a, c} {
		if code := s.exit(); code != 0 {
			t.Errorf("%s exited %d", s.name, code)
		}
	}
}

// quote is s as a JSON string.
func quote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}
