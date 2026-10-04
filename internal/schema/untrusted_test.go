package schema_test

import (
	"bytes"
	"context"
	"encoding/json"
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
// that is device.uiTree alone (system.health and device.list report the host's own state
// and the list of serials, not device text, R6), and in it exactly nodes, foreground and
// display: the snapshot id, truncated, hint and emptyReason are the daemon's.
func TestOnlyTaggedFieldsPublishTheKeyword(t *testing.T) {
	doc, err := schema.Generate(registry(), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Capabilities {
		has := bytes.Contains(c.Output, []byte(capability.UntrustedKeyword))
		if want := c.Name == "device.uiTree"; has != want {
			t.Errorf("%s: output carries %s = %v, want %v", c.Name, capability.UntrustedKeyword, has, want)
		}
		d, _ := registry().Lookup(c.Name)
		if d.Untrusted() != (c.Name == "device.uiTree") {
			t.Errorf("%s: descriptor flag = %v", c.Name, d.Untrusted())
		}
		if c.Name != "device.uiTree" {
			continue
		}
		var out map[string]any
		if err := json.Unmarshal(c.Output, &out); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"nodes", "foreground", "display", "snapshot", "truncated", "hint", "emptyReason"} {
			_, isTrue := prop(t, out, f)[capability.UntrustedKeyword].(bool)
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
