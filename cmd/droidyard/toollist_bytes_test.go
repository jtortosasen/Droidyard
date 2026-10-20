package main

import (
	"encoding/json"
	"github.com/jtortosasen/droidyard/internal/assistant"
	"strings"
	"testing"
)

// maxToolListBytes is the model-facing budget for tools/list (ADR-0036 Decision 1; plan
// m1o A2): what a client shows the model is each tool's name, description and
// inputSchema. 66.7 KB at 1e736e6, 34,786 B at 3f6eac0, 28,032 B once the file
// capabilities left MCP (ADR-0060 §4: the logged size rounded up to the next 1,000 B, so the
// saving is held); outputSchema is left out because the two clients measured (OpenCode,
// Cursor) do not show it to the model.
const maxToolListBytes = 29000

// TestToolListModelFacingBytes sends initialize and tools/list through a real shim to
// the shipped wiring and holds the compact JSON of the shipped tools, outputSchema
// dropped, to the budget. A descriptor that grows past it is caught here, not by a
// reader of the golden file.
func TestToolListModelFacingBytes(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	c := startShim(t, p.state, "client")
	c.initialize("toollist")

	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var list struct {
		Result struct {
			Tools []map[string]json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(c.recv(), &list); err != nil {
		t.Fatal(err)
	}
	var shipped []map[string]json.RawMessage
	for _, tool := range list.Result.Tools {
		var name string
		json.Unmarshal(tool["name"], &name)
		if strings.HasPrefix(name, "fixture_") { // the test daemon's own tools
			continue
		}
		if _, has := tool["outputSchema"]; !has {
			t.Errorf("%s has no outputSchema on the wire; the measurement drops it, so it must exist to be dropped", name)
		}
		delete(tool, "outputSchema")
		shipped = append(shipped, tool)
	}
	if len(shipped) != 32 {
		t.Fatalf("tools/list has %d shipped tools; want 32", len(shipped))
	}
	raw, err := json.Marshal(shipped)
	if err != nil {
		t.Fatal(err)
	}
	c.exit()
	t.Logf("tools/list without outputSchema: %d bytes (budget %d)", len(raw), maxToolListBytes)
	if len(raw) > maxToolListBytes {
		t.Errorf("tools/list without outputSchema is %d bytes; the budget is %d", len(raw), maxToolListBytes)
	}
}

// TestHintToolsAreExposed: ADR-0063 §2. Every tool the hook's hint can name is listed by
// tools/list under `all`, so the hint never sends an agent to a tool that does not exist.
func TestHintToolsAreExposed(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	c := startShim(t, p.state, "client", "--tools", "all")
	c.initialize("hinttools")
	names, _ := toolNames(t, c, 1)
	c.exit()
	listed := map[string]bool{}
	for _, n := range names {
		listed[n] = true
	}
	for _, tool := range assistant.HintTools() {
		if !listed[tool] {
			t.Errorf("the hint names %s, which tools/list under all does not list", tool)
		}
	}
}
