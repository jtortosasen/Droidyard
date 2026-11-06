package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// touched is what fixture.touch answers: the workspace, the project root and the Gradle
// root its call resolved to (ADR-0137 §3).
type touched struct {
	ID         string `json:"id"`
	Root       string `json:"root"`
	GradleRoot string `json:"gradleRoot"`
}

// registerTouchFixture adds fixture.touch, a project capability that does nothing but say
// where it ran. A call that names a project opens its root (Registry.Enter, at Start), so
// this is how a test opens one; there is no capability that does only that, since no
// capability opens or closes a workspace (ADR-0137 §1). Test-only, never shipped.
func registerTouchFixture(reg *capability.Registry) {
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.touch", Kind: capability.Command, Scope: capability.Workspace,
		Description: "Says which workspace it ran in; test-only.", Params: struct{}{}, Result: touched{},
		Handler: func(_ context.Context, call *capability.Call) (any, error) {
			return touched{ID: call.Workspace, Root: call.Root, GradleRoot: call.GradleRoot}, nil
		},
	})
}

// registerTrustedFixture adds fixture.trusted, a project capability gated by the policy the
// daemon reads, as the Gradle ones are: its handler runs only on a trusted root, which is
// the call's resolved root. Test-only, never shipped.
func registerTrustedFixture(reg *capability.Registry, env platform.Env) {
	pol := policy.New(env)
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.trusted", Kind: capability.Command, Scope: capability.Workspace,
		Description: "Runs project code, gated by trust; test-only.", Params: struct{}{}, Result: touched{},
		Authorize: pol.Authorize(func(call *capability.Call) (string, bool) { return call.Root, call.Root != "" }),
		Handler: func(_ context.Context, call *capability.Call) (any, error) {
			return touched{ID: call.Workspace, Root: call.Root, GradleRoot: call.GradleRoot}, nil
		},
	})
}

// touchedOf decodes a fixture.touch (or fixture.trusted) answer's envelope.
func touchedOf(t *testing.T, env capability.ResponseEnvelope) touched {
	t.Helper()
	var got touched
	if err := json.Unmarshal(env.Result, &got); err != nil || got.ID == "" {
		t.Fatalf("the answer %s (%+v) is not a touched workspace: %v", env.Result, env.Error, err)
	}
	return got
}

// withProject is the parameter object params ("" is {}) with `project` added, as an adapter
// sends a project capability's call (ADR-0137 §3).
func withProject(params, project string) json.RawMessage {
	fields := map[string]json.RawMessage{}
	if params != "" {
		if err := json.Unmarshal([]byte(params), &fields); err != nil {
			panic(err)
		}
	}
	fields[capability.ProjectParam], _ = json.Marshal(project)
	out, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return out
}
