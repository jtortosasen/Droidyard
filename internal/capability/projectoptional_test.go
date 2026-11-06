package capability

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// countingAdmit admits every project as one workspace and counts the projects it was asked
// to resolve.
type countingAdmit struct {
	mu sync.Mutex
	n  int
}

func (c *countingAdmit) Enter(project string, record func(Entered)) error {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	record(Entered{ID: "ws-counted", Root: "/root", GradleRoot: "/gradle"})
	return nil
}
func (*countingAdmit) Ended(string) {}

// Decision (ADR-0137 §4; plan m3j step 8): a ProjectOptional capability resolves and enters a
// project only when the call names one, and runs with no workspace on its Call otherwise;
// a `project` that is sent but empty or not a string is still refused, and an ordinary
// workspace-scoped capability still refuses a call without one. Reversing the decision
// (optional taken as "ignored", or as "never refused") fails the second and third parts.
func TestProjectOptionalResolvesOnlyWhenNamed(t *testing.T) {
	reg := NewRegistry()
	var seen string
	handler := func(ctx context.Context, call *Call) (any, error) {
		seen = call.Workspace
		return result{}, nil
	}
	reg.MustRegister(Descriptor{Name: "fixture.either", Kind: Command, Scope: Workspace, ProjectOptional: true,
		Description: "Takes a project for some calls.", Params: params{}, Result: result{}, Handler: handler})
	reg.MustRegister(Descriptor{Name: "fixture.always", Kind: Command, Scope: Workspace,
		Description: "Takes a project always.", Params: params{}, Result: result{}, Handler: handler})
	ops := NewOperations(reg)
	admit := &countingAdmit{}
	ops.UseWorkspaces(admit)
	run := func(name, raw string) (Outcome, error) {
		id, err := ops.Start(context.Background(), Request{Capability: name, Params: json.RawMessage(raw)})
		if err != nil {
			return Outcome{}, err
		}
		return ops.Wait(context.Background(), id, time.Now().Add(5*time.Second))
	}

	if out, err := run("fixture.either", `{"name":"x"}`); err != nil || out.State != StateCompleted || seen != "" || admit.n != 0 {
		t.Errorf("a call with no project: %+v, %v, workspace %q, %d projects entered; want it completed in none", out, err, seen, admit.n)
	}
	if out, err := run("fixture.either", `{"project":"/p","name":"x"}`); err != nil || out.State != StateCompleted || seen != "ws-counted" || admit.n != 1 {
		t.Errorf("a call naming a project: %+v, %v, workspace %q, %d projects entered; want it in the workspace, entered once", out, err, seen, admit.n)
	}
	for _, bad := range []string{`{"project":"","name":"x"}`, `{"project":7,"name":"x"}`} {
		if _, err := run("fixture.either", bad); codeOf(err) != ErrInvalidParams {
			t.Errorf("%s: %v; want %s", bad, err, ErrInvalidParams)
		}
	}
	if _, err := run("fixture.always", `{"name":"x"}`); codeOf(err) != ErrInvalidParams {
		t.Errorf("a capability that is not ProjectOptional, called with no project: %v; want %s", err, ErrInvalidParams)
	}
	if admit.n != 1 {
		t.Errorf("%d projects entered in all; a refused call enters none", admit.n)
	}

	// Register refuses the flag where it means nothing.
	if err := NewRegistry().Register(Descriptor{Name: "fixture.global", Kind: Command, Scope: Global, ProjectOptional: true,
		Description: "A global capability.", Params: params{}, Result: result{}, Handler: handler}); err == nil {
		t.Error("ProjectOptional on a global capability was registered")
	}
}
