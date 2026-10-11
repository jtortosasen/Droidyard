package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// maxToolListBytes is the model-facing budget for tools/list (ADR-0036 Decision 1; plan
// m1o A2): what a client shows the model is each tool's name, description and
// inputSchema. 66.7 KB at cc0c27b; outputSchema is left out because the two clients
// measured (OpenCode, Cursor) do not show it to the model.
const maxToolListBytes = 35000

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
	if len(shipped) != 39 {
		t.Fatalf("tools/list has %d shipped tools; want 39", len(shipped))
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
