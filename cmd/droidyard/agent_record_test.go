package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// TestRecordCommandIsTheHooksChannelOnly: ADR-0063 §5. agent.recordCommand has audience
// human, so no MCP profile lists agent_recordCommand (no flag, all, core) and the core
// refuses it from an MCP client (REQUEST_NOT_ALLOWED_HERE, with no operation id). The CLI,
// which is what the hook calls through, runs it after workspace.open, and a UI session
// watching operations sees the call under client cli, with the result the record, which
// is what Activity reads. Setting the descriptor's audience to all lists the tool and
// lets the MCP client in, so the first two assertions fail.
func TestRecordCommandIsTheHooksChannelOnly(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)

	for _, args := range [][]string{nil, {"--tools", "all"}, {"--tools", "core"}} {
		c := startShim(t, p.state, "agent", args...)
		c.initialize("agent")
		names, _ := toolNames(t, c, 1)
		if len(names) == 0 {
			t.Errorf("%v lists no tools; the check below would pass vacuously", args)
		}
		for _, n := range names {
			if n == "agent_recordCommand" {
				t.Errorf("%v lists agent_recordCommand; it is the hook's channel, never an MCP tool (ADR-0063 §5)", args)
			}
		}
		c.exit()
	}

	root, _ := projectWith(t, "settings.gradle")
	openEnv, code := callIn(t, p.state, root, "workspace.open")
	if code != 0 {
		t.Fatalf("workspace.open exited %d: %+v", code, openEnv.Error)
	}
	ws := workspaceOf(t, openEnv).ID
	params := `{"client":"cursor","commands":[{"program":"gradlew","words":[":app:installDebug"],"suggested":"build_run"}]}`

	id, err := p.ops.Start(context.Background(), capability.Request{Capability: "agent.recordCommand", Workspace: ws,
		Params: json.RawMessage(params), Client: capability.ClientMCP})
	var e *capability.Error
	if !errors.As(err, &e) || e.Code != capability.ErrNotAllowedHere || id != "" {
		t.Errorf("agent.recordCommand as client mcp: (%q, %v); want REQUEST_NOT_ALLOWED_HERE and no operation", id, err)
	}

	w := wsConnect(t, p)
	w.send("watch", "operation.watch", "", `{}`, nil)
	var res struct {
		SubscriptionID string `json:"subscriptionId"`
	}
	f := w.must("the operation.watch response")
	if err := json.Unmarshal(f.Result, &res); err != nil || res.SubscriptionID == "" {
		t.Fatalf("operation.watch answered %s (error %+v): %v", f.Result, f.Error, err)
	}

	env, code := callIn(t, p.state, root, "agent.recordCommand", "--params", params)
	if code != 0 || env.Error != nil || env.Operation == nil || env.Operation.State != capability.StateCompleted {
		t.Fatalf("agent.recordCommand from the CLI: exit %d, error %+v, operation %+v; want it completed", code, env.Error, env.Operation)
	}
	var rec struct {
		Client   string `json:"client"`
		Commands []struct {
			Program   string   `json:"program"`
			Words     []string `json:"words"`
			Suggested string   `json:"suggested"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(env.Result, &rec); err != nil || rec.Client != "cursor" || len(rec.Commands) != 1 ||
		rec.Commands[0].Program != "gradlew" || strings.Join(rec.Commands[0].Words, " ") != ":app:installDebug" || rec.Commands[0].Suggested != "build_run" {
		t.Errorf("the result %s (%v); want the record as sent", env.Result, err)
	}

	var states []capability.OperationState
	for len(states) < 2 {
		fr := w.must("the events of the CLI call")
		if fr.Event == nil || fr.SubscriptionID != res.SubscriptionID {
			continue
		}
		var pl capability.OperationState
		if err := json.Unmarshal(fr.Event.Payload, &pl); err != nil {
			t.Fatal(err)
		}
		if pl.Capability == "agent.recordCommand" {
			states = append(states, pl)
		}
	}
	if states[0].State != capability.StateRunning || states[1].State != capability.StateCompleted ||
		states[0].Client != capability.ClientCLI || states[1].Client != capability.ClientCLI {
		t.Errorf("operation.watch saw %+v then %+v; want running then completed, client cli", states[0], states[1])
	}

	bad, code := callIn(t, p.state, root, "agent.recordCommand", "--params", `{"client":"cursor","commands":[{"program":"rm","words":[]}]}`)
	if code == 0 || bad.Error == nil || bad.Error.Code != capability.ErrInvalidParams {
		t.Errorf("a record with another program: exit %d, error %+v; want REQUEST_INVALID_PARAMS", code, bad.Error)
	}
}
