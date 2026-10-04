package capability

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

type deepest struct {
	Text  string `json:"text" untrusted:"true"`
	Plain string `json:"plain"`
}
type middle struct {
	Inner deepest `json:"inner"`
}
type nested struct {
	Top    string `json:"top"`
	Middle middle `json:"middle"`
}

type embedded struct {
	deepest
}

type plainLeaf struct {
	V string `json:"v"`
}
type onlyTrusted struct {
	A string      `json:"a"`
	B []plainLeaf `json:"b"`
}
type viaPointer struct {
	P *deepest `json:"p"`
}
type viaSlice struct {
	S []deepest `json:"s"`
}
type viaMap struct {
	M map[string]deepest `json:"m"`
}
type viaEmbedded struct {
	deepest
	Other string `json:"other"`
}
type ignoredTag struct {
	Hidden string `json:"-" untrusted:"true"`
}
type recursive struct {
	Next *recursive `json:"next"`
	Name string     `json:"name" untrusted:"true"`
}

// TestUntrustedFlagIsDerivedFromTheWalk: the descriptor's flag is true iff some field of
// the result type, however deep (struct, pointer, slice, map value, embedded struct), is
// tagged `untrusted:"true"` (ADR-0015 §1). Nothing sets it by hand: a result with no
// tag is false, whatever else it holds. A reversal (a flag that looks only at the
// top-level fields) fails the nested rows.
func TestUntrustedFlagIsDerivedFromTheWalk(t *testing.T) {
	for _, c := range []struct {
		name   string
		result any
		want   bool
	}{
		{"two levels down", nested{}, true},
		{"pointer", viaPointer{}, true},
		{"slice element", viaSlice{}, true},
		{"map value", viaMap{}, true},
		{"embedded struct", viaEmbedded{}, true},
		{"a recursive type", recursive{}, true},
		{"no tag anywhere", onlyTrusted{}, false},
		{"a field JSON omits", ignoredTag{}, false},
		{"an empty result", struct{}{}, false},
	} {
		d := valid()
		d.Name = "fixture.untrusted"
		d.Result = c.result
		reg := NewRegistry()
		if err := reg.Register(d); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got, _ := reg.Lookup(d.Name)
		if got.Untrusted() != c.want {
			t.Errorf("%s: derived flag = %v, want %v", c.name, got.Untrusted(), c.want)
		}
	}
}

// TestRegisterRejectsAnUntrustedTagItCannotHonour: a tag whose value is not "true", or on
// an embedded struct (flattened, so no property can carry the keyword), is a startup
// failure, not a field that is quietly trusted (ADR-0015 §1; Register's job).
func TestRegisterRejectsAnUntrustedTagItCannotHonour(t *testing.T) {
	type badValue struct {
		X string `json:"x" untrusted:"yes"`
	}
	type badEmbed struct {
		deepest `untrusted:"true"`
	}
	for _, c := range []struct {
		name   string
		result any
		names  string // what the error must name, so the author can find the field
	}{
		{"a value other than true", badValue{}, "X"},
		{"an embedded field", badEmbed{}, "deepest"},
	} {
		d := valid()
		d.Name = "fixture.bad"
		d.Result = c.result
		err := NewRegistry().Register(d)
		if err == nil {
			t.Errorf("%s was accepted", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.names) || !strings.Contains(err.Error(), UntrustedTag) {
			t.Errorf("%s: error %q names neither the field %q nor the tag", c.name, err, c.names)
		}
	}
}

// TestOutcomeMarkFollowsTheDescriptorUnlessTheHandlerSetsItsOwn: a completed result is
// marked with the descriptor's derived flag; a handler that sets Call.Untrusted
// overrides it either way (artifact.read, ADR-0015 §1); a failed call has no result to
// mark; and operation.wait reports the waited operation's mark.
func TestOutcomeMarkFollowsTheDescriptorUnlessTheHandlerSetsItsOwn(t *testing.T) {
	reg := NewRegistry()
	ops := NewOperations(reg)
	ops.RegisterCapabilities()
	add := func(name string, result any, h Handler) {
		reg.MustRegister(Descriptor{Name: name, Kind: Query, Scope: Global, Params: struct{}{}, Result: result, Description: "A fixture.", Handler: h})
	}
	add("fixture.tagged", nested{}, func(context.Context, *Call) (any, error) { return nested{}, nil })
	add("fixture.plain", onlyTrusted{}, func(context.Context, *Call) (any, error) { return onlyTrusted{}, nil })
	add("fixture.optout", nested{}, func(_ context.Context, c *Call) (any, error) {
		c.Untrusted = boolPtr(false)
		return nested{}, nil
	})
	add("fixture.optin", onlyTrusted{}, func(_ context.Context, c *Call) (any, error) {
		c.Untrusted = boolPtr(true)
		return onlyTrusted{}, nil
	})
	add("fixture.failing", nested{}, func(context.Context, *Call) (any, error) { return nil, NewError(ErrInvalidParams, "no") })

	for _, c := range []struct {
		name string
		want bool
	}{
		{"fixture.tagged", true}, {"fixture.plain", false}, {"fixture.optout", false}, {"fixture.optin", true}, {"fixture.failing", false},
	} {
		out, err := ops.Invoke(context.Background(), Request{Capability: c.name})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if out.Untrusted != c.want {
			t.Errorf("%s: outcome mark = %v, want %v", c.name, out.Untrusted, c.want)
		}
		if (out.Meta() != nil) != c.want || (c.want && !out.Meta().Untrusted) {
			t.Errorf("%s: meta = %+v, want untrusted iff %v", c.name, out.Meta(), c.want)
		}
	}

	id, err := ops.Start(context.Background(), Request{Capability: "fixture.tagged"})
	if err != nil {
		t.Fatal(err)
	}
	waited, err := ops.Invoke(context.Background(), Request{Capability: OperationWait, Params: []byte(`{"operationId":"` + id + `"}`)})
	if err != nil || !waited.Untrusted {
		t.Errorf("operation.wait on a marked operation = %+v, %v; want its mark", waited, err)
	}
}

// TestJSONNameSaysOmittedExplicitly: only `json:"-"` omits a field; `json:"-,"` is a
// field named "-" (as encoding/json writes it), so a tag on it is walked and derives
// the flag. Overloading "-" as the omitted marker lets such a field escape.
func TestJSONNameSaysOmittedExplicitly(t *testing.T) {
	type dash struct {
		Omitted string `json:"-" untrusted:"true"`
		Dashed  string `json:"-," untrusted:"true"`
		Plain   string
	}
	typ := reflect.TypeOf(dash{})
	for i, want := range []struct {
		name    string
		omitted bool
	}{{"", true}, {"-", false}, {"Plain", false}} {
		name, omitted := JSONName(typ.Field(i))
		if name != want.name || omitted != want.omitted {
			t.Errorf("field %d: JSONName = (%q, %v), want (%q, %v)", i, name, omitted, want.name, want.omitted)
		}
	}
	type onlyOmitted struct {
		Omitted string `json:"-" untrusted:"true"`
	}
	type onlyDashed struct {
		Dashed string `json:"-," untrusted:"true"`
	}
	for _, c := range []struct {
		result any
		want   bool
	}{{onlyOmitted{}, false}, {onlyDashed{}, true}} {
		d := valid()
		d.Name = "fixture.dash"
		d.Result = c.result
		reg := NewRegistry()
		if err := reg.Register(d); err != nil {
			t.Fatal(err)
		}
		if got, _ := reg.Lookup(d.Name); got.Untrusted() != c.want {
			t.Errorf("%T: derived flag = %v, want %v", c.result, got.Untrusted(), c.want)
		}
	}
}

// TestUnrecordedCallsAreMarkedLikeRecordedOnes: a call run without a record (the path
// operation.cancel takes) honours the descriptor's derived flag and the handler's own
// Call.Untrusted for a non-Outcome result, so a device result could not slip through
// unmarked if one ever ran there.
func TestUnrecordedCallsAreMarkedLikeRecordedOnes(t *testing.T) {
	reg := NewRegistry()
	ops := NewOperations(reg)
	add := func(name string, result any, h Handler) {
		reg.MustRegister(Descriptor{Name: name, Kind: Query, Scope: Global, Params: struct{}{}, Result: result, Description: "A fixture.", Handler: h, unrecorded: true})
	}
	add("fixture.utagged", nested{}, func(context.Context, *Call) (any, error) { return nested{}, nil })
	add("fixture.uplain", onlyTrusted{}, func(context.Context, *Call) (any, error) { return onlyTrusted{}, nil })
	add("fixture.uoptout", nested{}, func(_ context.Context, c *Call) (any, error) {
		c.Untrusted = boolPtr(false)
		return nested{}, nil
	})
	add("fixture.uoptin", onlyTrusted{}, func(_ context.Context, c *Call) (any, error) {
		c.Untrusted = boolPtr(true)
		return onlyTrusted{}, nil
	})
	for name, want := range map[string]bool{"fixture.utagged": true, "fixture.uplain": false, "fixture.uoptout": false, "fixture.uoptin": true} {
		out, err := ops.Invoke(context.Background(), Request{Capability: name})
		if err != nil || out.Untrusted != want {
			t.Errorf("%s: mark = %v, %v; want %v", name, out.Untrusted, err, want)
		}
	}
}
