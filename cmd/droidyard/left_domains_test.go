package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/system"
)

// TestSystemCapabilitiesListsNoLeftDomain holds ADR-0137 §1 on the shipped wiring: the Git,
// file, shell and session capabilities went to a separate project, and a daemon started
// from this binary lists none of them. The list is read through system.capabilities, the
// call a consumer discovers the surface with, not from the registry directly, so a
// capability registered anywhere in the shipped wiring is seen. The control is the same
// list holding the device domain, so an empty answer cannot pass for the absence.
func TestSystemCapabilitiesListsNoLeftDomain(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	env, code := p.call(t, "system.capabilities")
	if code != 0 {
		t.Fatalf("system.capabilities exited %d: %+v", code, env.Error)
	}
	var list system.Capabilities
	if err := json.Unmarshal(env.Result, &list); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, c := range list.Capabilities {
		listed[c.Name] = true
		for _, left := range []string{"git.", "file.", "shell.", "session."} {
			if strings.HasPrefix(c.Name, left) {
				t.Errorf("system.capabilities lists %s: its domain left with ADR-0137 §1", c.Name)
			}
		}
	}
	if !listed["device.list"] || !listed["build.assemble"] || !listed["policy.get"] {
		t.Errorf("system.capabilities lists %d capabilities without device.list, build.assemble and policy.get; the control failed", len(list.Capabilities))
	}
}
