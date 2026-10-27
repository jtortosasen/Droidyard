package device

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// Decision (ADR-0135 §6, plan m3i step 3 (iii)): a new parameter is omitempty, so a call
// that uses none of them reaches an older host unchanged. device.logs of 0.40.0 is frozen
// here as that host's struct.
type logsParams040 struct {
	Device        string   `json:"device" jsonschema:"required"`
	ApplicationID string   `json:"applicationId,omitempty"`
	Since         string   `json:"since,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	MinPriority   string   `json:"minPriority,omitempty"`
	Buffers       []string `json:"buffers,omitempty"`
	Contains      string   `json:"contains,omitempty"`
	MaxLines      int      `json:"maxLines,omitempty"`
	FollowMs      int      `json:"followMs,omitempty"`
}

// Decision (ADR-0135 §6, plan m3i step 3 (i)): a host older than this daemon refuses a
// parameter it was not built with, by strict decoding, with a cause that begins
// `json: unknown field `: the refusal the client turns into "the host runs an older
// droidyard". The registry here has device.logs as 0.40.0 declared it.
func TestAnOlderDecoderRefusesNewFilters(t *testing.T) {
	reg := capability.NewRegistry()
	reg.MustRegister(capability.Descriptor{
		Name: "device.logs", Kind: capability.Command, Scope: capability.Global, Description: "the 0.40.0 device.logs.",
		Params: logsParams040{}, Result: LogsResult{},
		Handler: func(context.Context, *capability.Call) (any, error) { return LogsResult{}, nil },
	})
	ops := capability.NewOperations(reg)
	ops.RegisterCapabilities()

	for _, field := range []string{"exclude", "match", "excludeTags", "tagPriorities", "processes",
		"until", "keep", "context", "maxMessageBytes", "collapse", "topTags"} {
		_, err := ops.Start(context.Background(), capability.Request{Capability: "device.logs", Params: []byte(`{"device":"s","` + field + `":["x"]}`)})
		e := capability.AsError(err)
		if e.Code != capability.ErrInvalidParams || e.Cause != `json: unknown field "`+field+`"` {
			t.Errorf("%s on an older decoder: %+v; want REQUEST_INVALID_PARAMS with the unknown-field cause", field, e)
		}
	}
	// What 0.40.0 took still decodes.
	if _, err := ops.Start(context.Background(), capability.Request{Capability: "device.logs", Params: []byte(`{"device":"s","tags":["a"],"minPriority":"info","contains":"x"}`)}); err != nil {
		t.Errorf("the older decoder refused a 0.40.0 call: %v", err)
	}
}

// Decision (ADR-0135 §6, plan m3i step 3 (iii)): every parameter device.logs gained is
// omitempty, so a call that sets none of them sends an older host exactly the keys it
// knows. Read by reflection over LogsParams, with the embedded LineFilter flattened.
func TestNewParamsAreOmitEmpty(t *testing.T) {
	old := map[string]bool{}
	frozen := reflect.TypeOf(logsParams040{})
	for i := 0; i < frozen.NumField(); i++ {
		name, _, _ := strings.Cut(frozen.Field(i).Tag.Get("json"), ",")
		old[name] = true
	}
	visited := map[string]bool{}
	var walk func(reflect.Type)
	walk = func(rt reflect.Type) {
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			if f.Anonymous && name == "" {
				walk(f.Type)
				continue
			}
			visited[name] = true
			if !old[name] && !strings.Contains(opts, "omitempty") {
				t.Errorf("LogsParams.%s (json %q) is new since 0.40.0 and not omitempty: a call that does not use it would send an older host an unknown key", f.Name, name)
			}
			delete(old, name)
		}
	}
	walk(reflect.TypeOf(LogsParams{}))
	if len(old) != 0 {
		t.Errorf("0.40.0 parameters %v are gone from LogsParams: that is a breaking change", old)
	}
	if !visited["exclude"] || !visited["tags"] {
		t.Errorf("walked %v; the embedded LineFilter was not flattened", visited)
	}

	// And on the wire: a call that sets only 0.40.0's fields sends exactly their keys.
	raw, err := hostParams(LogsParams{Device: "qa/S1", ApplicationID: appID, Since: "launch", MaxLines: 5,
		LineFilter: LineFilter{Tags: []string{"T"}, MinPriority: "info", Contains: "x"}}, "S1")
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	var got []string
	for k := range keys {
		got = append(got, k)
	}
	slices.Sort(got)
	if want := []string{"applicationId", "contains", "device", "maxLines", "minPriority", "since", "tags"}; !slices.Equal(got, want) {
		t.Errorf("hostParams keys %v; want exactly %v", got, want)
	}
}

// Decision (plan m3i step 3, N2): the unknown-parameter hint lists the parameters the
// caller can send, and an embedded struct is flattened into them as the schema flattens
// it: `exclude`, never `LineFilter`.
func TestUnknownParamHintFlattensEmbedded(t *testing.T) {
	f := logsFixture(t, nil)
	_, err := f.ops.Start(context.Background(), capability.Request{Capability: "device.logs", Params: []byte(`{"device":"emulator-5554","exlude":["x"]}`)})
	e := capability.AsError(err)
	if e.Code != capability.ErrInvalidParams {
		t.Fatalf("error = %+v; want REQUEST_INVALID_PARAMS", e)
	}
	if !strings.Contains(e.NextStep, "exclude") || !strings.Contains(e.NextStep, "tagPriorities") || strings.Contains(e.NextStep, "LineFilter") {
		t.Errorf("nextStep = %q; want the flattened parameters, exclude among them, and no LineFilter", e.NextStep)
	}
}
