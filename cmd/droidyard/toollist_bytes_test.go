package main

import (
	"encoding/json"
	"github.com/jtortosasen/droidyard/internal/assistant"
	"strings"
	"testing"
)

// maxToolListBytes is the model-facing budget for tools/list (ADR-0036 Decision 1; plan
// m1o A2): what a client shows the model is each tool's name, description and
// inputSchema. 66.7 KB at ee06eb7, 34,786 B at b5e8dd9, 28,032 B once the file
// capabilities left MCP (ADR-0060 §4: the logged size rounded up to the next 1,000 B, so the
// saving is held), 29,697 B with device_uiDump (plan m2w step 4: the same rule, 30,000 B), 31,187 B with
// device_uiEvents (step 6: the same rule, 32,000 B), 32,690 B with device_recordUi and
// device_stopUiRecording (plan m2x step 5: the same rule, 33,000 B), 33,372 B with device_uiRecordings
// (step 6: the same rule, 34,000 B), 34,598 B with sdk_packages (plan m3f step 2: the same
// rule, 35,000 B), 35,418 B with sdk_install (step 4: the same rule, 36,000 B), 36,748 B with sdk_profiles and emulator_create (step 6: the same rule, 37,000 B), 38,249 B with emulator_create's settings (step 7: the same rule, 39,000 B), 40,727 B with device_systemPanel, device_forceStop, device_rotate and device_launch's url (plan m3g step 3: the same rule, 41,000 B), 43,792 B with device_tap, device_swipe and device_pressKey for agents (step 4: the same rule, 44,000 B), 50,950 B with expect and timeoutMs on those three (step 5: the same rule, 51,000 B), 51,897 B with device_logs's line filter and processes (plan m3i step 3: the same rule, 52,000 B), 54,324 B with artifact_search and build_errors's filters (steps 6, 7: the same rule, 55,000 B), 55,284 B with device_logs's read shape (step 4: the same rule, 56,000 B); outputSchema is left out because the two
// clients measured (OpenCode, Cursor) do not show it to the model.
const maxToolListBytes = 56000

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
	if len(shipped) != 48 { // artifact_search, plan m3i step 6
		t.Fatalf("tools/list has %d shipped tools; want 48", len(shipped))
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
