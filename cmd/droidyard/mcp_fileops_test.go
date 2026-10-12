package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// TestMCPBatchWithFailedItemsIsASuccessfulCall holds ADR-0053 §2 on the wire: file_move
// over a real MCP shim, bound to a project, with one item whose target exists and one that
// is fine. The call is not an MCP error (isError false, structured content present), the
// failed item carries FILE_EXISTS and the target keeps its bytes, the done item is done,
// and the text block opens with the failure, so an agent reading it cannot miss it. A
// call the request's own text refuses (a path outside the root) is an MCP tool error.
func TestMCPBatchWithFailedItemsIsASuccessfulCall(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	root, _ := projectWith(t, "settings.gradle")
	for name, content := range map[string]string{"a.kt": "A", "b.kt": "taken", "c.kt": "C"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := startShimIn(t, p.state, root, "ops", []string{"mcp", "--no-autostart"})
	c.initialize("ops")

	type reply struct {
		Result struct {
			Content           []struct{ Text string } `json:"content"`
			StructuredContent json.RawMessage         `json:"structuredContent"`
			IsError           bool                    `json:"isError"`
		} `json:"result"`
	}
	c.callTool(1, "file_move", `{"items":[{"from":"a.kt","to":"b.kt"},{"from":"c.kt","to":"d.kt"}]}`)
	var r reply
	if err := json.Unmarshal(c.recv(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Result.IsError || len(r.Result.StructuredContent) == 0 || len(r.Result.Content) != 1 {
		t.Fatalf("a batch with a failed item returned %+v; want a successful call with structured content", r.Result)
	}
	var res struct {
		Items []struct{ From, To, Code string }
		Done  int
	}
	if err := json.Unmarshal(r.Result.StructuredContent, &res); err != nil || res.Done != 1 || len(res.Items) != 2 ||
		res.Items[0].Code != "FILE_EXISTS" || res.Items[1].Code != "" {
		t.Fatalf("structured content %s (%v); want item 1 FILE_EXISTS and item 2 done", r.Result.StructuredContent, err)
	}
	if text := r.Result.Content[0].Text; !strings.HasPrefix(text, "failed 1 of 2 FILE_EXISTS\nFILE_EXISTS a.kt -> b.kt ") {
		t.Errorf("the text block is %q; want it to open with the failure", text)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "b.kt")); string(b) != "taken" {
		t.Errorf("the target holds %q after the refused item", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "d.kt")); string(b) != "C" {
		t.Errorf("the done item left %q at its target", b)
	}

	c.callTool(2, "file_move", `{"items":[{"from":"../outside","to":"x"}]}`)
	var bad reply
	if err := json.Unmarshal(c.recv(), &bad); err != nil {
		t.Fatal(err)
	}
	if !bad.Result.IsError || len(bad.Result.StructuredContent) != 0 || !strings.Contains(bad.Result.Content[0].Text, "FILE_OUTSIDE_ROOT") {
		t.Errorf("a path outside the root returned %+v; want an MCP tool error naming FILE_OUTSIDE_ROOT", bad.Result)
	}
	c.exit()
}

// TestCLIBatchWithFailedItemsCompletes holds ADR-0053 §2 over the CLI, the surface that
// keeps file.move once the file capabilities leave MCP (ADR-0060): a batch with one
// colliding item and one free item exits 0 with the operation completed, the colliding
// item carries FILE_EXISTS and its target keeps its bytes, the free item is done; a source
// outside the root exits non-zero with FILE_OUTSIDE_ROOT.
func TestCLIBatchWithFailedItemsCompletes(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	root, _ := projectWith(t, "settings.gradle")
	for name, content := range map[string]string{"a.kt": "A", "b.kt": "taken", "c.kt": "C"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if env, code := callIn(t, p.state, root, "workspace.open"); code != 0 {
		t.Fatalf("workspace.open exited %d: %+v", code, env.Error)
	}

	env, code := callIn(t, p.state, root, "file.move", "--params", `{"items":[{"from":"a.kt","to":"b.kt"},{"from":"c.kt","to":"d.kt"}]}`)
	if code != 0 || env.Error != nil || env.Operation == nil || env.Operation.State != capability.StateCompleted {
		t.Fatalf("a batch with a failed item: exit %d, error %+v, operation %+v; want exit 0 and a completed operation", code, env.Error, env.Operation)
	}
	var res struct {
		Items []struct{ From, To, Code string }
		Done  int
	}
	if err := json.Unmarshal(env.Result, &res); err != nil || res.Done != 1 || len(res.Items) != 2 ||
		res.Items[0].Code != "FILE_EXISTS" || res.Items[1].Code != "" {
		t.Fatalf("result %s (%v); want item 1 FILE_EXISTS and item 2 done", env.Result, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "b.kt")); string(b) != "taken" {
		t.Errorf("the target holds %q after the refused item", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "d.kt")); string(b) != "C" {
		t.Errorf("the done item left %q at its target", b)
	}

	bad, code := callIn(t, p.state, root, "file.move", "--params", `{"items":[{"from":"../outside","to":"x"}]}`)
	if code == 0 || bad.Error == nil || bad.Error.Code != "FILE_OUTSIDE_ROOT" {
		t.Errorf("a source outside the root: exit %d, %+v; want non-zero and FILE_OUTSIDE_ROOT", code, bad.Error)
	}
}
