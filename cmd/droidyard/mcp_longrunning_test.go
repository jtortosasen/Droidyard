package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// toolResult is a tools/call response as it is on the wire.
type toolResult struct {
	raw     map[string]json.RawMessage
	text    string
	view    capability.OperationView
	isError bool
}

func decodeTool(t *testing.T, line []byte, id int) toolResult {
	t.Helper()
	var msg struct {
		ID     int                        `json:"id"`
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(line, &msg); err != nil || msg.ID != id || msg.Result == nil {
		t.Fatalf("expected the result of request %d, got %s", id, line)
	}
	r := toolResult{raw: msg.Result}
	var content []struct {
		Text string `json:"text"`
	}
	json.Unmarshal(msg.Result["content"], &content)
	if len(content) == 1 {
		r.text = content[0].Text
	}
	json.Unmarshal(msg.Result["isError"], &r.isError)
	if sc, ok := msg.Result["structuredContent"]; ok {
		if err := json.Unmarshal(sc, &r.view); err != nil {
			t.Fatalf("structuredContent is not an operation view: %s", sc)
		}
	}
	return r
}

// TestMCPLongRunning holds long-running tools and operation_wait to their contract
// (ADR-0020 §Adapter projections; docs/interfaces/mcp-tool-surface.md §Long-running work):
// the bounded wait, the operation view as structured content, operation_wait capped at the budget
// and rendering a finished operation as the original tool would, errors without
// structured content, and notifications/cancelled on operation_wait abandoning only
// the wait.
func TestMCPLongRunning(t *testing.T) {
	t.Parallel()
	t.Run("the budget, then operation_wait", func(t *testing.T) {
		p := startInProcess(t)
		c := startShim(t, p.state, "client", "--wait", "200ms")
		c.initialize("long-running")

		start := time.Now()
		c.callTool(1, "fixture_slow", `{}`)
		id := p.startedID(t)
		first := decodeTool(t, c.recv(), 1)
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("the first call answered after %s; the budget is 200ms", elapsed)
		}
		if first.isError || first.view.OperationID != id || first.view.State != capability.StateRunning || first.view.Result != nil {
			t.Fatalf("the first call answered %v; want a successful view of %s, running, no result", first.raw, id)
		}
		if !strings.Contains(first.text, "operation_wait") || !strings.Contains(first.text, id) {
			t.Errorf("a still-running call's text %q does not say to call operation_wait with its id", first.text)
		}

		// timeoutMs above the budget is capped at it.
		start = time.Now()
		c.callTool(2, "operation_wait", `{"operationId":"`+id+`","timeoutMs":60000}`)
		capped := decodeTool(t, c.recv(), 2)
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("operation_wait with timeoutMs 60000 answered after %s; want within the 200ms budget", elapsed)
		}
		if capped.isError || capped.view.State != capability.StateRunning || !strings.Contains(capped.text, "operation_wait") {
			t.Fatalf("a capped operation_wait answered %v; want the view, still running", capped.raw)
		}

		close(p.release)
		c.callTool(3, "operation_wait", `{"operationId":"`+id+`"}`)
		done := decodeTool(t, c.recv(), 3)
		if done.isError || done.view.OperationID != id || done.view.Capability != "fixture.slow" ||
			done.view.State != capability.StateCompleted || string(done.view.Result) != `{"answer":42}` {
			t.Fatalf("operation_wait after the release answered %v; want %s completed with result {\"answer\":42}", done.raw, id)
		}

		// What the original tool renders for a finished call: call it again, now
		// that the fixture is released and finishes at once.
		c.callTool(4, "fixture_slow", `{}`)
		p.startedID(t)
		original := decodeTool(t, c.recv(), 4)
		if original.view.State != capability.StateCompleted {
			t.Fatalf("the second call answered %v; want it completed", original.raw)
		}
		if done.text != original.text {
			t.Errorf("operation_wait rendered %q; the original tool renders %q", done.text, original.text)
		}
		// And that rendering is the plain result (ADR-0020 §Adapter projections: a
		// completed operation renders as the plain result).
		if original.text != `{"answer":42}` {
			t.Errorf("a finished long-running call rendered %q; want the plain result", original.text)
		}

		// The views sent conform to the outputSchema advertised, the nested result
		// included (the step's Rests on).
		c.send(`{"jsonrpc":"2.0","id":5,"method":"tools/list"}`)
		var list struct {
			Result struct {
				Tools []struct {
					Name         string         `json:"name"`
					OutputSchema map[string]any `json:"outputSchema"`
				} `json:"tools"`
			} `json:"result"`
		}
		json.Unmarshal(c.recv(), &list)
		var slowSchema map[string]any
		for _, tool := range list.Result.Tools {
			if tool.Name == "fixture_slow" {
				slowSchema = tool.OutputSchema
			}
		}
		if slowSchema == nil {
			t.Fatal("tools/list has no fixture_slow")
		}
		for _, r := range []toolResult{first, done, original} {
			var v any
			json.Unmarshal(r.raw["structuredContent"], &v)
			conforms(t, slowSchema, v, "structuredContent")
		}
		c.exit()
	})

	t.Run("operation_wait on a cancelled operation", func(t *testing.T) {
		p := startInProcess(t)
		c := startShim(t, p.state, "client", "--wait", "200ms")
		c.initialize("cancelled")
		c.callTool(1, "fixture_slow", `{}`)
		id := p.startedID(t)
		decodeTool(t, c.recv(), 1)

		c.callTool(2, "operation_cancel", `{"operationId":"`+id+`"}`)
		if r := decodeTool(t, c.recv(), 2); r.isError {
			t.Fatalf("operation_cancel answered %v", r.raw)
		}
		c.callTool(3, "operation_wait", `{"operationId":"`+id+`"}`)
		r := decodeTool(t, c.recv(), 3)
		var e capability.Error
		if !r.isError || json.Unmarshal([]byte(r.text), &e) != nil || e.Code != capability.ErrOperationCancelled {
			t.Errorf("operation_wait on a cancelled operation answered %v; want isError with %s", r.raw, capability.ErrOperationCancelled)
		}
		if _, ok := r.raw["structuredContent"]; ok {
			t.Errorf("an error result carries structuredContent: %v", r.raw)
		}
		c.exit()
	})

	t.Run("notifications/cancelled on operation_wait abandons only the wait", func(t *testing.T) {
		p := startInProcess(t)
		detached := droidyard(t, p.state, "call", "--no-autostart", "--detach", "fixture.slow")
		id := p.startedID(t)
		if code := detached.wait(t); code != 0 {
			t.Fatalf("the detached start exited %d", code)
		}

		// A budget far above the test's patience: only the notification can end
		// this wait early.
		c := startShim(t, p.state, "client", "--wait", "60s")
		c.initialize("abandon")
		c.callTool(1, "operation_wait", `{"operationId":"`+id+`"}`)
		time.Sleep(300 * time.Millisecond)
		start := time.Now()
		c.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1,"reason":"the user interrupted"}}`)
		abandoned := decodeTool(t, c.recv(), 1)
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("the abandoned wait answered after %s", elapsed)
		}
		if abandoned.view.State != capability.StateRunning {
			t.Errorf("the abandoned wait answered %v; want the view, still running", abandoned.raw)
		}
		c.exit()

		released(t, p, id)
	})
}

// conforms checks value against the subset of JSON Schema that internal/schema emits:
// type, properties, required and additionalProperties false. It is enough to catch a
// view whose shape, or whose nested result, is not the one advertised.
func conforms(t *testing.T, schema map[string]any, value any, at string) {
	t.Helper()
	typ, _ := schema["type"].(string)
	switch typ {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			t.Errorf("%s: %v is not an object", at, value)
			return
		}
		props, _ := schema["properties"].(map[string]any)
		for k, v := range obj {
			sub, ok := props[k].(map[string]any)
			if !ok {
				if schema["additionalProperties"] == false {
					t.Errorf("%s: property %q is not in the schema", at, k)
				}
				continue
			}
			conforms(t, sub, v, at+"."+k)
		}
		req, _ := schema["required"].([]any)
		for _, k := range req {
			if _, ok := obj[k.(string)]; !ok {
				t.Errorf("%s: required property %q is missing", at, k)
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			t.Errorf("%s: %v is not a string", at, value)
		}
	case "integer":
		if f, ok := value.(float64); !ok || f != float64(int64(f)) {
			t.Errorf("%s: %v is not an integer", at, value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			t.Errorf("%s: %v is not a boolean", at, value)
		}
	case "array":
		arr, ok := value.([]any)
		if !ok {
			t.Errorf("%s: %v is not an array", at, value)
			return
		}
		if items, ok := schema["items"].(map[string]any); ok {
			for i, v := range arr {
				conforms(t, items, v, fmt.Sprintf("%s[%d]", at, i))
			}
		}
	}
}
