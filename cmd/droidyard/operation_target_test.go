package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// TestOperationStateCarriesTarget is the promise of ADR-0076: the operation.state events
// and operation.watch's rows name what a call acts on, as its descriptor declares from the
// call's decoded parameters, cleaned of control and format characters, so a mirror can
// tell which device a tap was for and which module a build was of. Real capabilities over a WebSocket on a daemon with no adb and no
// JDK, which fail at once: the target is recorded when the operation starts, so the failure
// changes nothing, and the running event and the terminal event agree. device.tapElement
// gives {device}, emulator.start {avd}, build.assemble {module, variant}, device.install all
// of device, module and variant, and build.run none (its tasks are not a target). A
// fixture blocked mid-run gives operation.watch's snapshot a row to agree with its event;
// a fixture with an over-long target, holding a bidirectional override and a newline, shows the
// cleaning and the 200-byte bound on a character boundary ("a" and 150 "é" is 301 bytes and
// is cut at 199, not in the middle of a character), and one whose target is
// empty shows that no empty object is sent.
func TestOperationStateCarriesTarget(t *testing.T) {
	t.Parallel()
	p := startInProcessWith(t, func(reg *capability.Registry) {
		reg.MustRegister(capability.Descriptor{
			Name: "fixture.targeted", Kind: capability.Command, Scope: capability.Global,
			Description: "Blocks until released; declares a target.", Params: struct{}{}, Result: struct{}{},
			Target: func(any) *capability.OpTarget { return &capability.OpTarget{Device: "emulator-5554", AVD: "Pixel_8"} },
			Handler: func(ctx context.Context, call *capability.Call) (any, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		})
		reg.MustRegister(capability.Descriptor{
			Name: "fixture.longTarget", Kind: capability.Command, Scope: capability.Global,
			Description: "Declares a target longer than the bound.", Params: struct{}{}, Result: struct{}{},
			Target: func(any) *capability.OpTarget {
				return &capability.OpTarget{Device: strings.Repeat("d", 300), Module: "a\u202e\n" + strings.Repeat("é", 150), Variant: "de\u202ebug\n"}
			},
			Handler: func(context.Context, *capability.Call) (any, error) { return struct{}{}, nil },
		})
		reg.MustRegister(capability.Descriptor{
			Name: "fixture.emptyTarget", Kind: capability.Command, Scope: capability.Global,
			Description: "Declares a target with every field empty.", Params: struct{}{}, Result: struct{}{},
			Target:  func(any) *capability.OpTarget { return &capability.OpTarget{} },
			Handler: func(context.Context, *capability.Call) (any, error) { return struct{}{}, nil },
		})
	})
	root, _ := projectWith(t, "settings.gradle")
	wid, _ := platform.Current().WorkspaceID(root)

	w := wsConnect(t, p)
	w.send("open", "workspace.open", root, "{}", nil)
	if f := w.must("workspace.open"); f.ID != "open" || f.Error != nil {
		t.Fatalf("workspace.open: %+v", f)
	}
	w.send("watch", "operation.watch", "", `{}`, nil)
	f := w.must("the operation.watch response")
	var snap struct {
		SubscriptionID string `json:"subscriptionId"`
	}
	if err := json.Unmarshal(f.Result, &snap); err != nil || snap.SubscriptionID == "" {
		t.Fatalf("operation.watch answered %s: %v", f.Result, err)
	}

	calls := []struct{ id, name, workspace, params string }{
		{"tap", "device.tapElement", "", `{"device":"emulator-5554","target":{"text":"OK"}}`},
		{"start", "emulator.start", "", `{"avd":"Pixel_8"}`},
		{"assemble", "build.assemble", wid, `{"module":":app","variant":"debug"}`},
		{"install", "device.install", wid, `{"device":"emulator-5554","build":{"module":":app","variant":"release"}}`},
		{"run", "build.run", wid, `{"tasks":["assemble"]}`},
		{"long", "fixture.longTarget", "", ``},
		{"empty", "fixture.emptyTarget", "", ``},
		{"blocked", "fixture.targeted", "", ``},
	}
	for _, c := range calls {
		w.send(c.id, c.name, c.workspace, c.params, nil)
	}

	// The events by capability, until every call but the blocked one has ended and the
	// blocked one is running.
	// Only the calls above count: operation.watch is an operation too (ADR-0043 §4), and its
	// own terminal event may reach the subscription.
	type seen struct{ states []capability.OperationState }
	byCap := map[string]*seen{}
	blockedOp := ""
	ended := map[string]bool{}
	called := map[string]bool{}
	for _, c := range calls {
		called[c.name] = true
	}
	for len(ended) < len(calls)-1 || blockedOp == "" {
		fr := w.must("the operation.state events")
		if fr.Event == nil || fr.SubscriptionID != snap.SubscriptionID {
			continue
		}
		var pl capability.OperationState
		if err := json.Unmarshal(fr.Event.Payload, &pl); err != nil {
			t.Fatal(err)
		}
		if byCap[pl.Capability] == nil {
			byCap[pl.Capability] = &seen{}
		}
		byCap[pl.Capability].states = append(byCap[pl.Capability].states, pl)
		if pl.Capability == "fixture.targeted" {
			blockedOp = fr.Event.Operation
		} else if pl.State.Terminal() && called[pl.Capability] {
			ended[pl.Capability] = true
		}
	}

	want := map[string]*capability.OpTarget{
		"device.tapElement":   {Device: "emulator-5554"},
		"emulator.start":      {AVD: "Pixel_8"},
		"build.assemble":      {Module: ":app", Variant: "debug"},
		"device.install":      {Device: "emulator-5554", Module: ":app", Variant: "release"},
		"build.run":           nil,
		"fixture.emptyTarget": nil,
		"fixture.targeted":    {Device: "emulator-5554", AVD: "Pixel_8"},
		"fixture.longTarget":  {Device: strings.Repeat("d", 200), Module: "a" + strings.Repeat("é", 99), Variant: "debug"},
	}
	for name, wantTarget := range want {
		s := byCap[name]
		if s == nil || len(s.states) == 0 {
			t.Errorf("%s: no operation.state event", name)
			continue
		}
		for _, st := range s.states {
			switch {
			case wantTarget == nil && st.Target != nil:
				t.Errorf("%s %s event carries the target %+v; want none", name, st.State, st.Target)
			case wantTarget != nil && (st.Target == nil || *st.Target != *wantTarget):
				t.Errorf("%s %s event carries the target %+v; want %+v", name, st.State, st.Target, wantTarget)
			}
		}
	}

	// A second watch's snapshot lists the blocked operation, and its row agrees with the
	// event.
	w.send("watch2", "operation.watch", "", `{}`, nil)
	var rows struct {
		Operations []struct {
			OperationID string               `json:"operationId"`
			Capability  string               `json:"capability"`
			Target      *capability.OpTarget `json:"target"`
		} `json:"operations"`
	}
	for {
		fr := w.must("the second operation.watch response")
		if fr.ID != "watch2" {
			continue
		}
		if err := json.Unmarshal(fr.Result, &rows); err != nil {
			t.Fatal(err)
		}
		break
	}
	found := false
	for _, r := range rows.Operations {
		if r.OperationID != blockedOp {
			continue
		}
		found = true
		if r.Target == nil || *r.Target != *want["fixture.targeted"] {
			t.Errorf("the snapshot row of the blocked operation carries the target %+v; want %+v", r.Target, want["fixture.targeted"])
		}
	}
	if !found {
		t.Errorf("the snapshot does not list the blocked operation %s: %+v", blockedOp, rows.Operations)
	}

	// The wire form of an absent target is no key at all, never {} or null.
	raw, _ := json.Marshal(capability.OperationState{Capability: "x", State: capability.StateRunning})
	if strings.Contains(string(raw), "target") {
		t.Errorf("an untargeted payload marshals as %s; want no target key", raw)
	}
}
