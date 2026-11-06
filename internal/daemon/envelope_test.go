package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// TestEnvelopeHasNoWorkspace holds ADR-0137 §3 on the native socket: the request envelope has
// no workspace field, so a client of the old contract that still sends one is refused as an
// unknown field before anything is recorded, whether the capability takes a project or not
// (the connection's hello names none either: the project is the call's own parameter). The
// same line with the project in params is served. Seen failing with the field still in the
// envelope: the project call ran with its workspace and no `project`.
func TestEnvelopeHasNoWorkspace(t *testing.T) {
	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	type none struct{}
	ran := 0
	for _, scope := range []capability.Scope{capability.Global, capability.Workspace} {
		name := "fixture.global"
		if scope == capability.Workspace {
			name = "fixture.scoped"
		}
		reg.MustRegister(capability.Descriptor{
			Name: name, Kind: capability.Query, Scope: scope, Description: "A fixture.",
			Params: none{}, Result: none{},
			Handler: func(context.Context, *capability.Call) (any, error) { ran++; return none{}, nil },
		})
	}
	ops.RegisterCapabilities()
	hello := Hello{Client: capability.ClientCLI}

	for _, name := range []string{"fixture.global", "fixture.scoped"} {
		line := `{"id":"w","capability":"` + name + `","workspace":"/some/project"}`
		env, ok := handle(context.Background(), reg, ops, nil, hello, "s-1", []byte(line))
		if !ok || env.Error == nil || env.Error.Code != capability.ErrInvalidParams || env.Operation != nil || env.ID != "w" ||
			!strings.Contains(env.Error.Cause, `"workspace"`) {
			t.Errorf("%s with a workspace field: %+v (error %+v); want REQUEST_INVALID_PARAMS naming the unknown field, no operation", name, env, env.Error)
		}
	}
	if ran != 0 {
		t.Fatalf("a request with a workspace field ran a handler %d times", ran)
	}

	// A global capability with the project in its params is refused as the unknown parameter
	// it is; the daemon takes no project from the hello.
	env, _ := handle(context.Background(), reg, ops, nil, hello, "s-1", []byte(`{"id":"p","capability":"fixture.global","params":{"project":"/some/project"}}`))
	if env.Error == nil || env.Error.Code != capability.ErrInvalidParams || env.Operation != nil {
		t.Errorf("a project sent to a global capability: %+v; want REQUEST_INVALID_PARAMS and no operation", env.Error)
	}
	// A project capability with none is refused too, with the nextStep that says what to pass.
	env, _ = handle(context.Background(), reg, ops, nil, hello, "s-1", []byte(`{"id":"n","capability":"fixture.scoped"}`))
	if env.Error == nil || env.Error.Code != capability.ErrInvalidParams || env.Operation != nil || !strings.Contains(env.Error.NextStep, "project") {
		t.Errorf("a project capability with no project: %+v; want REQUEST_INVALID_PARAMS naming project", env.Error)
	}
}
