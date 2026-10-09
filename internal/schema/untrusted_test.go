package schema_test

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/schema"
)

type leaf struct {
	Text  string `json:"text" untrusted:"true"`
	Other string `json:"other"`
}
type emb struct {
	Hidden string `json:"hidden" untrusted:"true"`
}
type branch struct {
	emb                   // flattened into branch: its tagged field is branch's property
	Ptr   *leaf           `json:"ptr"`
	ByKey map[string]leaf `json:"byKey"`
	// Same type as Quiet, once tagged and once not: a marker that edits a shared
	// schema would mark both.
	Loud  leaf   `json:"loud" untrusted:"true"`
	Quiet leaf   `json:"quiet"`
	Items []leaf `json:"items"`
	Name  string `json:"name"`
}
type tree struct {
	Title string `json:"title"`
	Deep  branch `json:"deep"`
}

func untrustedFixture(t *testing.T, longRunning bool) map[string]any {
	t.Helper()
	reg := capability.NewRegistry()
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.untrusted", Kind: capability.Query, Scope: capability.Global, LongRunning: longRunning,
		Params: struct{}{}, Result: tree{}, Description: "Returns device-derived text.",
		Handler: func(context.Context, *capability.Call) (any, error) { return tree{}, nil },
	})
	doc, err := schema.Generate(reg, "test")
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(doc.Capabilities[0].Output, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func prop(t *testing.T, s map[string]any, path ...string) map[string]any {
	t.Helper()
	for _, name := range path {
		props, _ := s["properties"].(map[string]any)
		next, ok := props[name].(map[string]any)
		if !ok {
			if items, isArr := s["items"].(map[string]any); isArr && name == "[]" {
				s = items
				continue
			}
			if ap, isMap := s["additionalProperties"].(map[string]any); isMap && name == "{}" {
				s = ap
				continue
			}
			t.Fatalf("no property %q in %v", name, s)
		}
		s = next
	}
	return s
}

// TestUntrustedKeywordIsABooleanOnTheTaggedFieldsOnly: the published schema carries
// "x-droidyard-untrusted": true, the JSON boolean, on each field tagged untrusted at every
// depth and on nothing else (ADR-0015 §1), including inside the operation view of a
// long-running capability. A keyword written as the string "true" fails the boolean
// assertion; one that spills to a sibling of the same type fails the absence rows.
func TestUntrustedKeywordIsABooleanOnTheTaggedFieldsOnly(t *testing.T) {
	for _, long := range []bool{false, true} {
		root := untrustedFixture(t, long)
		if long {
			root = prop(t, root, "result")
		}
		has := func(s map[string]any) (bool, bool) {
			v, ok := s[capability.UntrustedKeyword]
			b, isBool := v.(bool)
			return ok, isBool && b
		}
		for _, c := range []struct {
			path []string
			want bool
		}{
			{[]string{"deep", "loud"}, true},          // the field itself is tagged
			{[]string{"deep", "loud", "text"}, true},  // and the field inside it, a level further
			{[]string{"deep", "quiet", "text"}, true}, // the tag is on the field of the type, wherever the type is used
			{[]string{"deep", "items", "[]", "text"}, true},
			{[]string{"deep", "ptr", "text"}, true},         // through a pointer
			{[]string{"deep", "byKey", "{}", "text"}, true}, // through a map value
			{[]string{"deep", "hidden"}, true},              // through an embedded struct
			{[]string{"deep", "ptr", "other"}, false},
			{[]string{"deep", "byKey"}, false},
			{[]string{"title"}, false},
			{[]string{"deep"}, false},
			{[]string{"deep", "quiet"}, false},
			{[]string{"deep", "loud", "other"}, false},
			{[]string{"deep", "quiet", "other"}, false},
			{[]string{"deep", "items"}, false},
			{[]string{"deep", "name"}, false},
		} {
			present, isTrue := has(prop(t, root, c.path...))
			if c.want && !isTrue {
				t.Errorf("long=%v %v: keyword missing or not the boolean true (present=%v)", long, c.path, present)
			}
			if !c.want && present {
				t.Errorf("long=%v %v: carries the keyword, but is not tagged", long, c.path)
			}
		}
	}
}

// TestOnlyTaggedFieldsPublishTheKeyword: the keyword appears in the published schema of
// a capability exactly when its result has a tagged field. Of the shipped capabilities
// that is device.uiTree (exactly nodes, foreground and display: the snapshot id,
// truncated, hint and emptyReason are the daemon's), device.screenshot (its reference
// field, R9), device.logs (each line's tag and message and each crash's exception and
// message), artifact.read (its data) and shell.exec (its stdout and stderr, which a
// command fills with whatever it likes, device text included); system.health and
// device.list report the host's own state and the list of serials, not device text (R6).
func TestOnlyTaggedFieldsPublishTheKeyword(t *testing.T) {
	doc, err := schema.Generate(registry(), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Capabilities {
		has := bytes.Contains(c.Output, []byte(capability.UntrustedKeyword))
		tagged := map[string]bool{"device.uiTree": true, "device.screenshot": true, "device.tapElement": true, "device.inputText": true, "device.logs": true, "artifact.read": true, "shell.exec": true}
		if want := tagged[c.Name]; has != want {
			t.Errorf("%s: output carries %s = %v, want %v", c.Name, capability.UntrustedKeyword, has, want)
		}
		d, _ := registry().Lookup(c.Name)
		if d.Untrusted() != tagged[c.Name] {
			t.Errorf("%s: descriptor flag = %v", c.Name, d.Untrusted())
		}
		var out map[string]any
		if err := json.Unmarshal(c.Output, &out); err != nil {
			t.Fatal(err)
		}
		// The artifact results tag one field each, and only it.
		for name, only := range map[string]string{"device.screenshot": "ref", "artifact.read": "data"} {
			if c.Name != name {
				continue
			}
			props, _ := out["properties"].(map[string]any)
			for f := range props {
				v, _ := prop(t, out, f)[capability.UntrustedKeyword].(bool)
				isTrue := v
				if isTrue != (f == only) {
					t.Errorf("%s.%s: marked untrusted = %v, want %v", name, f, isTrue, f == only)
				}
			}
		}
		if c.Name == "device.logs" {
			// R3: a line's tag and message and a crash's exception and message are the
			// device's text; the times, ids and priorities are the daemon's own reading.
			for _, f := range []string{"tag", "message"} {
				if v, _ := prop(t, out, "result", "lines", "[]", f)[capability.UntrustedKeyword].(bool); !v {
					t.Errorf("device.logs lines[].%s: not marked untrusted", f)
				}
			}
			for _, f := range []string{"exception", "message"} {
				if v, _ := prop(t, out, "result", "crashes", "[]", f)[capability.UntrustedKeyword].(bool); !v {
					t.Errorf("device.logs crashes[].%s: not marked untrusted", f)
				}
			}
			for _, f := range []string{"time", "pid", "tid", "priority", "crash"} {
				if v, _ := prop(t, out, "result", "lines", "[]", f)[capability.UntrustedKeyword].(bool); v {
					t.Errorf("device.logs lines[].%s: marked untrusted, but it is the daemon's own", f)
				}
			}
		}
		if c.Name == "device.tapElement" {
			// R4: tapped, foreground, tree and expect.unmet quote device text, and so can
			// warnings (a moved tap point names the covering node, D22); the daemon's
			// expect bookkeeping does not.
			for _, f := range []string{"tapped", "foreground", "tree", "appCrash", "systemDialog", "expect", "warnings"} {
				v, _ := prop(t, out, f)[capability.UntrustedKeyword].(bool)
				if want := f == "tapped" || f == "foreground" || f == "tree" || f == "warnings"; v != want {
					t.Errorf("device.tapElement.%s: marked untrusted = %v, want %v", f, v, want)
				}
			}
			for _, f := range []string{"unmet", "vacuous", "matched", "polls"} {
				v, _ := prop(t, out, "expect", f)[capability.UntrustedKeyword].(bool)
				if want := f == "unmet"; v != want {
					t.Errorf("device.tapElement.expect.%s: marked untrusted = %v, want %v", f, v, want)
				}
			}
		}
		if c.Name == "device.inputText" {
			// R4: tapped and expect.unmet quote device text, and so can warnings (D22);
			// the bookkeeping does not.
			for _, f := range []string{"typed", "characters", "method", "tapped", "appCrash", "systemDialog", "expect", "warnings"} {
				v, _ := prop(t, out, f)[capability.UntrustedKeyword].(bool)
				if want := f == "tapped" || f == "warnings"; v != want {
					t.Errorf("device.inputText.%s: marked untrusted = %v, want %v", f, v, want)
				}
			}
			for _, f := range []string{"unmet", "vacuous", "matched", "polls"} {
				v, _ := prop(t, out, "expect", f)[capability.UntrustedKeyword].(bool)
				if want := f == "unmet"; v != want {
					t.Errorf("device.inputText.expect.%s: marked untrusted = %v, want %v", f, v, want)
				}
			}
		}
		if c.Name != "device.uiTree" {
			continue
		}
		for _, f := range []string{"nodes", "foreground", "display", "systemDialog", "snapshot", "truncated", "hint", "emptyReason"} {
			v, _ := prop(t, out, f)[capability.UntrustedKeyword].(bool)
			isTrue := v
			if want := f == "nodes" || f == "foreground" || f == "display"; isTrue != want {
				t.Errorf("device.uiTree.%s: marked untrusted = %v, want %v", f, isTrue, want)
			}
		}
	}
	raw, _ := json.Marshal(untrustedFixture(t, false))
	if !bytes.Contains(raw, []byte(capability.UntrustedKeyword)) {
		t.Errorf("a result with tagged fields publishes no %s", capability.UntrustedKeyword)
	}
}

// TestCrashFieldsAreUntrusted: the crash and dialog fields (ADR-0034 §2–4) carry the
// keyword exactly where the text is device-derived. An appCrash's package, exception,
// message, frames and reason, and a systemDialog's package, are marked on device.tapElement
// and device.inputText, and a systemDialog's package on device.uiTree; an appCrash's pid,
// time and source and a systemDialog's kind are the daemon's own reading and carry none.
// Seen failing with any mark dropped and with the keyword added to pid, time, source or kind.
func TestCrashFieldsAreUntrusted(t *testing.T) {
	doc, err := schema.Generate(registry(), "test")
	if err != nil {
		t.Fatal(err)
	}
	output := func(name string) map[string]any {
		for _, c := range doc.Capabilities {
			if c.Name != name {
				continue
			}
			var out map[string]any
			if err := json.Unmarshal(c.Output, &out); err != nil {
				t.Fatal(err)
			}
			return out
		}
		t.Fatalf("%s is not in the schema", name)
		return nil
	}
	tagged := func(out map[string]any, obj, field string) bool {
		v, _ := prop(t, out, obj, field)[capability.UntrustedKeyword].(bool)
		return v
	}
	for _, name := range []string{"device.tapElement", "device.inputText"} {
		out := output(name)
		for _, f := range []string{"package", "exception", "message", "frames", "reason"} {
			if !tagged(out, "appCrash", f) {
				t.Errorf("%s.appCrash.%s: not marked untrusted", name, f)
			}
		}
		for _, f := range []string{"pid", "time", "source"} {
			if tagged(out, "appCrash", f) {
				t.Errorf("%s.appCrash.%s: marked untrusted, but it is the daemon's own", name, f)
			}
		}
	}
	for _, name := range []string{"device.tapElement", "device.inputText", "device.uiTree"} {
		out := output(name)
		if !tagged(out, "systemDialog", "package") {
			t.Errorf("%s.systemDialog.package: not marked untrusted", name)
		}
		if tagged(out, "systemDialog", "kind") {
			t.Errorf("%s.systemDialog.kind: marked untrusted, but it is the daemon's own", name)
		}
	}
}

// TestTapElementResultRequiresTappedAndNamesItsSnapshots: the published contract requires
// tapped (a completed call always has one) and carries the snapshot id beside every ref
// the result returns: tapped.snapshot for tapped.ref, a top-level snapshot for the unmet
// candidates and tree. The snapshot ids are the daemon's, not device text, so neither is
// marked untrusted. Seen failing with tapped optional and with either snapshot id
// dropped (plan m1f step 6, fix round).
func TestTapElementResultRequiresTappedAndNamesItsSnapshots(t *testing.T) {
	doc, err := schema.Generate(registry(), "test")
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	for _, c := range doc.Capabilities {
		if c.Name != "device.tapElement" {
			continue
		}
		if err := json.Unmarshal(c.Output, &out); err != nil {
			t.Fatal(err)
		}
	}
	if out == nil {
		t.Fatal("device.tapElement is not in the schema")
	}
	var required []string
	if rs, ok := out["required"].([]any); ok {
		for _, r := range rs {
			required = append(required, r.(string))
		}
	}
	if !slices.Contains(required, "tapped") {
		t.Errorf("required = %v, want tapped required in a completed result", required)
	}
	if _, ok := prop(t, out, "tapped")["properties"].(map[string]any)["snapshot"]; !ok {
		t.Errorf("tapped has no snapshot property: %v", prop(t, out, "tapped"))
	}
	if v, _ := prop(t, out, "tapped", "snapshot")[capability.UntrustedKeyword].(bool); v {
		t.Error("tapped.snapshot is the daemon's id, not device text: it must not be marked untrusted")
	}
	if _, ok := out["properties"].(map[string]any)["snapshot"]; !ok {
		t.Errorf("the result has no top-level snapshot property: %v", out)
	}
	if v, _ := prop(t, out, "snapshot")[capability.UntrustedKeyword].(bool); v {
		t.Error("the top-level snapshot is the daemon's id, not device text: it must not be marked untrusted")
	}
}

type byInt struct {
	M map[int]leaf `json:"m"`
}

// TestGenerateFailsWhereATaggedFieldCannotBePlaced: a tagged field the walk cannot find
// in the schema (invopop puts an int-keyed map's values under patternProperties) is an
// error naming the type and field, not a silently unmarked field.
func TestGenerateFailsWhereATaggedFieldCannotBePlaced(t *testing.T) {
	reg := capability.NewRegistry()
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.byint", Kind: capability.Query, Scope: capability.Global,
		Params: struct{}{}, Result: byInt{}, Description: "Returns device-derived text.",
		Handler: func(context.Context, *capability.Call) (any, error) { return byInt{}, nil },
	})
	_, err := schema.Generate(reg, "test")
	if err == nil {
		t.Fatal("a tagged field the schema cannot place was accepted")
	}
	if !strings.Contains(err.Error(), "byInt") || !strings.Contains(err.Error(), "M") {
		t.Errorf("error %q names neither the type nor the field", err)
	}
}

// streamingFixture registers a streaming capability whose events are the given types.
func streamingFixture(types map[string]any) *capability.Registry {
	reg := capability.NewRegistry()
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.stream", Kind: capability.Command, Scope: capability.Global,
		Statefulness: capability.Streaming,
		Params:       struct{}{}, Result: struct{}{}, Description: "Streams device-derived text.",
		Events:  &capability.Events{Topic: "fixture.text", Types: types},
		Handler: func(context.Context, *capability.Call) (any, error) { return struct{}{}, nil },
	})
	return reg
}

// TestEventPayloadsPublishTheUntrustedKeyword walks the events of every capability of the
// shipped registry: the keyword is on a payload field exactly where the payload's Go type
// tags it, at every depth (ADR-0015 §1; ADR-0041 §4), so a consumer reading the schema
// alone knows which event fields are the device's text. For device.watchLogs that is each
// line's tag and message and nothing else; a stopped event quotes no device text. Seen
// failing with the walk skipped for events (no mark on tag and message) and with the
// keyword added to the line's time, pid or priority.
func TestEventPayloadsPublishTheUntrustedKeyword(t *testing.T) {
	reg := registry()
	doc, err := schema.Generate(reg, "test")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, c := range doc.Capabilities {
		if c.Events == nil {
			continue
		}
		d, _ := reg.Lookup(c.Name)
		for typ, raw := range c.Events.Types {
			seen++
			has := bytes.Contains(raw, []byte(capability.UntrustedKeyword))
			tagged, err := capability.HasUntrusted(reflect.TypeOf(d.Events.Types[typ]))
			if err != nil {
				t.Fatal(err)
			}
			if has != tagged {
				t.Errorf("%s event %s: the schema carries %s = %v, but the payload type's tags say %v", c.Name, typ, capability.UntrustedKeyword, has, tagged)
			}
		}
		if c.Name != "device.watchLogs" {
			continue
		}
		var lines map[string]any
		if err := json.Unmarshal(c.Events.Types["lines"], &lines); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"tag", "message"} {
			if v, _ := prop(t, lines, "lines", "[]", f)[capability.UntrustedKeyword].(bool); !v {
				t.Errorf("device.watchLogs lines[].%s: not marked untrusted", f)
			}
		}
		for _, f := range []string{"time", "pid", "tid", "priority"} {
			if _, present := prop(t, lines, "lines", "[]", f)[capability.UntrustedKeyword]; present {
				t.Errorf("device.watchLogs lines[].%s: marked untrusted, but it is the daemon's own", f)
			}
		}
		for _, f := range []string{"device", "dropped", "untrusted"} {
			if _, present := prop(t, lines, f)[capability.UntrustedKeyword]; present {
				t.Errorf("device.watchLogs lines.%s: marked untrusted, but it is the daemon's own", f)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no capability of the shipped registry publishes events")
	}
}

// TestGenerateFailsWhereATaggedEventFieldCannotBePlaced: the placement error of a result
// holds for an event payload too, so a stream's device text is never published unmarked.
func TestGenerateFailsWhereATaggedEventFieldCannotBePlaced(t *testing.T) {
	_, err := schema.Generate(streamingFixture(map[string]any{"text": byInt{}}), "test")
	if err == nil {
		t.Fatal("an event payload with a tagged field the schema cannot place was accepted")
	}
	if !strings.Contains(err.Error(), "fixture.stream") || !strings.Contains(err.Error(), "text") || !strings.Contains(err.Error(), "byInt") {
		t.Errorf("error %q names neither the capability, the event type nor the payload type", err)
	}
}
