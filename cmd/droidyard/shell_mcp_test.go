package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// TestShellExecOverMCP is the surface row of step 2's check (plan m1k D6): shell_exec is
// listed whether or not the shell is enabled; with the shell off it is refused
// POLICY_SHELL_DISABLED, the nextStep naming the configuration file; with the shell
// enabled for the bound root and the root trusted, it runs, and its result carries
// _meta {"droidyard/untrusted": true} and opens with the one fixed line, since a command
// prints whatever it likes (ADR-0015 §1; mcp-tool-surface.md §Untrusted data).
func TestShellExecOverMCP(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	root, _ := projectWith(t, "settings.gradle")
	c := startShimIn(t, p.state, root, "client", []string{"mcp", "--no-autostart", "--root", root})
	c.initialize("shell")

	// The shell is off by default (no file): the tool is listed, and a call is refused.
	c.send(`{"jsonrpc":"2.0","id":0,"method":"tools/list"}`)
	var list struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	json.Unmarshal(c.recv(), &list)
	hasShell := false
	for _, tool := range list.Result.Tools {
		if tool.Name == "shell_exec" {
			hasShell = true
		}
	}
	if !hasShell {
		t.Fatal("tools/list has no shell_exec; it is always listed (plan m1k D4)")
	}

	c.callTool(1, "shell_exec", `{"command":"echo hi"}`)
	r := decodeTool(t, c.recv(), 1)
	if !r.isError {
		t.Fatalf("shell_exec with the shell disabled: %s; want isError", r.raw)
	}
	var e capability.Error
	if err := json.Unmarshal([]byte(r.text), &e); err != nil || e.Code != capability.ErrPolicyShellDisabled {
		t.Fatalf("the error is %q (%v); want %s", r.text, err, capability.ErrPolicyShellDisabled)
	}
	if !strings.Contains(e.NextStep, policy.FileName) {
		t.Errorf("nextStep %q does not name the configuration file", e.NextStep)
	}

	// Enable it for the bound root and trust it (no restart): the policy is re-read per
	// check, so the next call runs.
	writeConfig(t, p, `{"trust":{"roots":[`+strconv.Quote(root)+`]},"shell":{"enabled":true}}`)
	c.callTool(2, "shell_exec", `{"command":"echo hi"}`)
	r = decodeTool(t, c.recv(), 2)
	if r.isError {
		t.Fatalf("shell_exec with the shell enabled: %s; want a result", r.raw)
	}
	if m := metaOf(t, r); m["droidyard/untrusted"] != true {
		t.Errorf("_meta = %v; want droidyard/untrusted true", m)
	}
	line, _, ok := strings.Cut(r.text, "\n")
	if !ok || line != untrustedLine {
		t.Errorf("the text does not open with the fixed line: %q", r.text)
	}
}
