package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// configPath is where the in-process daemon's configuration lives: the test's state
// root (startInProcessFull sets DROIDYARD_STATE_DIR) and the config subdirectory
// (platform.Env.ConfigDir; plan m1g step 3).
func configPath(p *inProcess) string {
	return filepath.Join(p.state, "config", policy.FileName)
}

func writeConfig(t *testing.T, p *inProcess, body string) {
	t.Helper()
	dir := filepath.Dir(configPath(p))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath(p), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestPolicyGetAnswersTheHumanChannels: policy.get is answered for a human client —
// `droidyard call` and POST /v1/call — in D2's shape, in every file state (missing,
// ok, invalid), and it never fails on a bad file (ADR-0032 §5, §6; plan m1k D1, D2).
// system.health reports the same state, and its toolKeys stay at what was read when the
// daemon started.
func TestPolicyGetAnswersTheHumanChannels(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	httpAddr, port := p.d.HTTPAddr(), portOf(t, p.d.HTTPAddr())

	getCLI := func() policy.GetResult {
		t.Helper()
		env, code := p.call(t, "policy.get")
		if code != 0 || env.Error != nil || env.Operation == nil || env.Operation.State != capability.StateCompleted {
			t.Fatalf("droidyard call policy.get exited %d with %+v; want completed, no error", code, env)
		}
		var got policy.GetResult
		if err := json.Unmarshal(env.Result, &got); err != nil {
			t.Fatalf("policy.get result %s: %v", env.Result, err)
		}
		return got
	}
	getHTTP := func() policy.GetResult {
		t.Helper()
		resp := callRequest(port, p.token, `{"id":"g","capability":"policy.get","params":{}}`).send(t, httpAddr)
		if resp.StatusCode != 200 {
			t.Fatalf("POST /v1/call policy.get: %d; want 200", resp.StatusCode)
		}
		env := envelopeOf(t, resp)
		if env.Error != nil || env.Result == nil {
			t.Fatalf("POST /v1/call policy.get: %+v; want a result", env)
		}
		var got policy.GetResult
		if err := json.Unmarshal(env.Result, &got); err != nil {
			t.Fatalf("policy.get result %s: %v", env.Result, err)
		}
		return got
	}
	healthConfig := func() policy.ConfigStatus {
		t.Helper()
		env, _ := p.call(t, "system.health")
		h := health(t, env)
		if h.Config == nil {
			t.Fatal("system.health carries no config")
		}
		return *h.Config
	}

	// Nothing written yet: missing, and the same from both channels.
	if got := getCLI(); got.State != policy.StateMissing || got.Path != configPath(p) {
		t.Errorf("missing file: CLI policy.get = %+v; want state missing, path %s", got, configPath(p))
	}
	if got := getHTTP(); got.State != policy.StateMissing {
		t.Errorf("missing file: HTTP policy.get = %+v; want state missing", got)
	}
	if hc := healthConfig(); hc.State != policy.StateMissing || hc.Path != configPath(p) {
		t.Errorf("missing file: health config = %+v; want state missing", hc)
	}

	// A valid file: the sections as written, from both channels, and health agrees.
	writeConfig(t, p, `{"schemaVersion":1,"trust":{"roots":["/w/root"],"prefixes":["/w/pre"]},"devices":{"allowPhysical":true,"allowlist":["SER"]},"shell":{"enabled":true,"roots":["/w/sh"]}}`)
	for _, c := range []struct {
		name string
		got  policy.GetResult
	}{{"CLI", getCLI()}, {"HTTP", getHTTP()}} {
		if c.got.State != policy.StateOK || len(c.got.Trust.Roots) != 1 || c.got.Trust.Roots[0] != "/w/root" ||
			len(c.got.Trust.Prefixes) != 1 || !c.got.Devices.AllowPhysical || len(c.got.Devices.Allowlist) != 1 ||
			!c.got.Shell.Enabled || len(c.got.Shell.Roots) != 1 {
			t.Errorf("%s policy.get = %+v; want the file's sections", c.name, c.got)
		}
	}
	if hc := healthConfig(); hc.State != policy.StateOK {
		t.Errorf("valid file: health config = %+v; want state ok", hc)
	}
	// The tool keys are read once, at start: the daemon started with no file, so they
	// stay missing however the file is edited afterwards (ADR-0032 §6).
	if hc := healthConfig(); hc.ToolKeys != policy.StateMissing {
		t.Errorf("health config toolKeys = %q; want missing (read once at start)", hc.ToolKeys)
	}

	// A file that does not parse: completed, state invalid, and the error names the
	// parse problem — the UI can show it (plan m1k D2).
	writeConfig(t, p, `{"schemaVersion":1,"tools":{"adb":"adb"}}`)
	for _, c := range []struct {
		name string
		got  policy.GetResult
	}{{"CLI", getCLI()}, {"HTTP", getHTTP()}} {
		if c.got.State != policy.StateInvalid || c.got.Error == "" || !strings.Contains(c.got.Error, "not an absolute path") {
			t.Errorf("%s policy.get on a bad file = %+v; want state invalid and an error naming the parse problem", c.name, c.got)
		}
	}
	if hc := healthConfig(); hc.State != policy.StateInvalid || hc.Error == "" {
		t.Errorf("bad file: health config = %+v; want state invalid with an error", hc)
	}
}

// TestPolicyGetIsRefusedFromTheCoreOnMCP: the core refuses policy.get from an MCP
// client with REQUEST_NOT_ALLOWED_HERE — enforcement, not just the adapter's omission
// (ADR-0015 §6; plan m1k A1). The adapter's tools/list omission is asserted by
// TestMCPTranscript, which holds tools/list to the golden file.
func TestPolicyGetIsRefusedFromTheCoreOnMCP(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)

	_, err := p.ops.Invoke(context.Background(), capability.Request{Capability: "policy.get", Client: capability.ClientMCP})
	var e *capability.Error
	if !errors.As(err, &e) || e.Code != capability.ErrNotAllowedHere {
		t.Fatalf("policy.get as client mcp: %v; want REQUEST_NOT_ALLOWED_HERE", err)
	}
	if _, err := p.ops.Invoke(context.Background(), capability.Request{Capability: "policy.get", Client: capability.ClientCLI}); err != nil {
		t.Errorf("policy.get as client cli: %v; want it answered", err)
	}
}
