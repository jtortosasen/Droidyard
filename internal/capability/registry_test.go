package capability

import (
	"context"
	"errors"
	"testing"
)

func noop(ctx context.Context, call *Call) (any, error) { return nil, nil }

type params struct{ Name string }
type result struct{ Count int }

func valid() Descriptor {
	return Descriptor{
		Name:        "system.health",
		Kind:        Query,
		Scope:       Global,
		Params:      params{},
		Result:      result{},
		Description: "Reports whether the core is alive.",
		Handler:     noop,
	}
}

func TestRegisterRejectsBadNames(t *testing.T) {
	// The name rule exists so the MCP mapping (dot to underscore) is reversible.
	for _, name := range []string{"system_health", "System.Health", "system", "system.", ".health", "system.health_now"} {
		d := valid()
		d.Name = name
		if err := NewRegistry().Register(d); err == nil {
			t.Errorf("name %q was accepted; it must be rejected", name)
		}
	}
}

func TestRegisterAcceptsCanonicalName(t *testing.T) {
	d := valid()
	if err := NewRegistry().Register(d); err != nil {
		t.Fatalf("canonical name rejected: %v", err)
	}
}

func TestRegisterRequiresWhatTheSchemaNeeds(t *testing.T) {
	cases := map[string]func(*Descriptor){
		"no handler":     func(d *Descriptor) { d.Handler = nil },
		"no params":      func(d *Descriptor) { d.Params = nil },
		"no result":      func(d *Descriptor) { d.Result = nil },
		"no description": func(d *Descriptor) { d.Description = "" },
	}
	for name, mutate := range cases {
		d := valid()
		mutate(&d)
		if err := NewRegistry().Register(d); err == nil {
			t.Errorf("%s: was accepted; it must be rejected", name)
		}
	}
}

func TestRegisterRefusesDuplicates(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(valid()); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(valid()); err == nil {
		t.Error("a duplicate capability was accepted")
	}
}

func TestAnnotationOverridesNeedAReason(t *testing.T) {
	no := false
	d := valid()
	d.Annotations = Annotations{Destructive: &no}
	if err := NewRegistry().Register(d); err == nil {
		t.Error("an override without a reason was accepted")
	}
	d.Annotations.Reason = "a query that reports on other sessions' work"
	if err := NewRegistry().Register(d); err != nil {
		t.Errorf("a justified override was rejected: %v", err)
	}
}

func TestAnnotationDerivation(t *testing.T) {
	// Read-only and idempotent come from Kind and Idempotent and cannot be overridden.
	q := valid().ResolveAnnotations()
	if !q.ReadOnly || !q.Idempotent || q.Destructive || q.OpenWorld {
		t.Errorf("a plain query derived %+v; expected read-only, idempotent, neither destructive nor open-world", q)
	}

	// A command defaults to destructive, because MCP's own default for an
	// unannotated tool is more permissive than ours.
	c := valid()
	c.Name = "device.uninstall"
	c.Kind = Command
	c.Availability = []Requirement{RequiresADB}
	got := c.ResolveAnnotations()
	if got.ReadOnly {
		t.Error("a command resolved as read-only")
	}
	if !got.Destructive {
		t.Error("a command did not default to destructive")
	}
	if !got.OpenWorld {
		t.Error("a capability that needs adb did not default to open-world")
	}
	if got.Idempotent {
		t.Error("a command that is not marked idempotent resolved as idempotent")
	}

	// An explicit override wins.
	noDestructive := false
	c.Annotations = Annotations{Destructive: &noDestructive, Reason: "the build output is regenerated on demand"}
	if c.ResolveAnnotations().Destructive {
		t.Error("an explicit override did not win")
	}
}

func TestRegistryAllIsSorted(t *testing.T) {
	r := NewRegistry()
	for _, n := range []string{"system.health", "build.tasks", "device.list"} {
		d := valid()
		d.Name = n
		if err := r.Register(d); err != nil {
			t.Fatal(err)
		}
	}
	all := r.All()
	for i := 1; i < len(all); i++ {
		if all[i-1].Name > all[i].Name {
			t.Fatalf("All() is not sorted: %s before %s", all[i-1].Name, all[i].Name)
		}
	}
}

func TestAsErrorKeepsTypedErrorsAndWrapsTheRest(t *testing.T) {
	typed := Errorf(ErrWorkspaceNotOpen, "workspace %s is not open", "x")
	if got := AsError(typed); got != typed {
		t.Error("a typed error was not passed through")
	}

	wrapped := AsError(errors.New("connection reset"))
	if wrapped.Code != ErrInvalidParams {
		t.Errorf("an untyped error became %s; expected it to be wrapped as %s", wrapped.Code, ErrInvalidParams)
	}
	if wrapped.Cause != "connection reset" {
		t.Errorf("the original text was lost: %q", wrapped.Cause)
	}
}

// TestResourceLockWithoutQueueTimeoutIsRejected: a descriptor that takes a resource
// lock and is not long-running can fail OPERATION_QUEUE_TIMEOUT, so Register refuses
// it unless Errors lists the code (ADR-0020 §Queue bound); a long-running one has no
// bound and is not required to.
func TestResourceLockWithoutQueueTimeoutIsRejected(t *testing.T) {
	d := valid()
	d.Resource = func(*Call) string { return "k" }
	if err := NewRegistry().Register(d); err == nil {
		t.Error("a non-long-running descriptor with a Resource and no OPERATION_QUEUE_TIMEOUT was accepted")
	}
	d.Errors = []ErrorCode{ErrOperationQueueTimeout}
	if err := NewRegistry().Register(d); err != nil {
		t.Errorf("with OPERATION_QUEUE_TIMEOUT listed: %v", err)
	}
	long := valid()
	long.Resource = func(*Call) string { return "k" }
	long.LongRunning = true
	if err := NewRegistry().Register(long); err != nil {
		t.Errorf("a long-running descriptor needs no queue-timeout code: %v", err)
	}
}

// TestUnrecordedCapabilityCannotTakeALockOrAHook: Invoke runs a non-recorded handler
// without the scheduler, so a Resource or Authorize on it would be silently ignored;
// Register refuses it instead.
func TestUnrecordedCapabilityCannotTakeALockOrAHook(t *testing.T) {
	d := valid()
	d.unrecorded = true
	d.Authorize = func(context.Context, *Call) (any, error) { return nil, nil }
	if err := NewRegistry().Register(d); err == nil {
		t.Error("a non-recorded descriptor with Authorize was accepted")
	}
	d.Authorize = nil
	d.LongRunning = true
	d.Resource = func(*Call) string { return "k" }
	if err := NewRegistry().Register(d); err == nil {
		t.Error("a non-recorded descriptor with Resource was accepted")
	}
}

type streamPayload struct {
	Text string `json:"text" untrusted:"true"`
}

type badTag struct {
	Text string `json:"text" untrusted:"yes"`
}

func streaming() Descriptor {
	d := valid()
	d.Name = "device.watchLogs"
	d.Statefulness = Streaming
	d.Events = &Events{Topic: "device.logcat", Types: map[string]any{"lines": streamPayload{}}}
	return d
}

// TestRegisterRejectsAStreamTheSchemaCannotState: a bad Events declaration is a startup
// failure, never a schema that fails at the first call (ADR-0041 §4). Each row names the
// decision it keeps: only a streaming capability owns a stream; the topic is well formed
// and is not one events.subscribe serves, which would deliver the stream to subscribers
// it was never meant for; a type is a struct value with valid untrusted tags; and no two
// capabilities share a topic. Seen failing with the check removed, one row at a time.
func TestRegisterRejectsAStreamTheSchemaCannotState(t *testing.T) {
	if err := NewRegistry().Register(streaming()); err != nil {
		t.Fatalf("a valid stream was rejected: %v", err)
	}
	cases := map[string]func(*Descriptor){
		"a stateless capability":      func(d *Descriptor) { d.Statefulness = Stateless },
		"a stateful capability":       func(d *Descriptor) { d.Statefulness = Stateful },
		"a topic without a dot":       func(d *Descriptor) { d.Events.Topic = "logcat" },
		"an underscore in a topic":    func(d *Descriptor) { d.Events.Topic = "device.log_cat" },
		"workspace.lifecycle":         func(d *Descriptor) { d.Events.Topic = "workspace.lifecycle" },
		"operation.progress":          func(d *Descriptor) { d.Events.Topic = ProgressTopic },
		"events.gap":                  func(d *Descriptor) { d.Events.Topic = "events.gap" },
		"no types":                    func(d *Descriptor) { d.Events.Types = nil },
		"a type name with a dot":      func(d *Descriptor) { d.Events.Types = map[string]any{"a.b": streamPayload{}} },
		"a nil payload":               func(d *Descriptor) { d.Events.Types = map[string]any{"lines": nil} },
		"a payload that is no struct": func(d *Descriptor) { d.Events.Types = map[string]any{"lines": "text"} },
		"a bad untrusted tag":         func(d *Descriptor) { d.Events.Types = map[string]any{"lines": badTag{}} },
	}
	for name, mutate := range cases {
		d := streaming()
		mutate(&d)
		if err := NewRegistry().Register(d); err == nil {
			t.Errorf("%s: was accepted; it must be rejected", name)
		}
	}

	r := NewRegistry()
	if err := r.Register(streaming()); err != nil {
		t.Fatal(err)
	}
	other := streaming()
	other.Name = "device.watchMore"
	if err := r.Register(other); err == nil {
		t.Error("a second capability on the same topic was accepted")
	}
	other.Events = &Events{Topic: "device.more", Types: map[string]any{"lines": streamPayload{}}}
	if err := r.Register(other); err != nil {
		t.Errorf("a second stream on its own topic was rejected: %v", err)
	}
}
