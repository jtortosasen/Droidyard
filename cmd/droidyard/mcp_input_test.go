package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
)

// TestInputToolsOverMCP: ADR-0133 §1. device.tap, device.swipe and device.pressKey are agent
// tools, listed by the MCP adapter under the default profile, and pressKey's key enum is the
// key table the three deliveries share; device.scroll, which only a live view's stream
// carries, is not a tool, and a call to it is "tool not found" at the transport. Reversing the
// decision (the three left at audience human) drops them from the list; a key enum that
// drifted from screen.KeyNames fails the third assertion.
func TestInputToolsOverMCP(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	c := startShim(t, p.state, "input")
	c.initialize("input")
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var list struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Properties map[string]struct {
						Enum []string `json:"enum"`
					} `json:"properties"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(c.recv(), &list); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	var keyEnum []string
	for _, tool := range list.Result.Tools {
		listed[tool.Name] = true
		if tool.Name == "device_pressKey" {
			keyEnum = tool.InputSchema.Properties["key"].Enum
		}
	}
	for _, name := range []string{"device_tap", "device_swipe", "device_pressKey"} {
		if !listed[name] {
			t.Errorf("tools/list lacks %s: it is an agent tool", name)
		}
	}
	if listed["device_scroll"] {
		t.Error("tools/list has device_scroll: the wheel is the view's, for humans")
	}
	slices.Sort(keyEnum)
	if want := screen.KeyNames(); !slices.Equal(keyEnum, want) {
		t.Errorf("device_pressKey's key enum is %v; want the key table %v", keyEnum, want)
	}

	c.callTool(2, "device_scroll", `{"device":"emulator-5554","x":0.5,"y":0.5,"dx":0,"dy":1}`)
	var msg struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(c.recv(), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Error == nil || msg.Error.Code != -32602 || !strings.Contains(msg.Error.Message, "tool 'device_scroll' not found") {
		t.Errorf("tools/call device_scroll answered %+v; want -32602 tool 'device_scroll' not found", msg.Error)
	}
	c.exit()
}
