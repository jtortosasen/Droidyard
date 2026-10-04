// Package schema turns the capability registry into the published JSON Schema.
//
// The Go descriptors and parameter structs are the source of truth; this package
// generates the schema that MCP tool definitions and (in Phase 2) the TypeScript
// client are derived from, and the golden file that is committed and reviewed
// (ADR-0016).
//
// Two rules from the MCP surface shape the output: everything is **inlined** (no
// "$ref" and no "$defs"), and there is **no top-level union** — a capability that
// would need one is split instead.
package schema

import (
	"encoding/json"
	"fmt"
	goreflect "reflect"

	"github.com/invopop/jsonschema"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// Document is the published schema: every capability, in name order, with the input
// and output shape a consumer needs.
type Document struct {
	Version      string             `json:"version"`
	Capabilities []CapabilitySchema `json:"capabilities"`
}

// CapabilitySchema is one capability's published contract.
type CapabilitySchema struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Scope        string   `json:"scope"`
	Statefulness string   `json:"statefulness"`
	LongRunning  bool     `json:"longRunning,omitempty"`
	Audience     string   `json:"audience"`
	Since        string   `json:"since,omitempty"`
	Description  string   `json:"description"`
	Availability []string `json:"availability,omitempty"`

	Annotations AnnotationsSchema `json:"annotations"`

	// Errors are the codes this capability returns itself, so a consumer can handle
	// them without guessing; a projecting capability passes the waited operation's code
	// through as well (docs/architecture/capability-api.md §Error model).
	Errors []string `json:"errors,omitempty"`

	// Input is the MCP input schema: the parameter object, and nothing else. The
	// workspace is an envelope field that the adapter fills, so it never appears
	// here.
	Input json.RawMessage `json:"input"`
	// Output is the MCP output schema: the result, or the operation view for a
	// long-running capability.
	Output json.RawMessage `json:"output"`
}

// AnnotationsSchema is the four hints as a client receives them, already resolved.
type AnnotationsSchema struct {
	ReadOnly    bool   `json:"readOnlyHint"`
	Destructive bool   `json:"destructiveHint"`
	Idempotent  bool   `json:"idempotentHint"`
	OpenWorld   bool   `json:"openWorldHint"`
	Reason      string `json:"-"`
}

// reflector is configured once: inlining everywhere, no additional properties, and
// no "$schema" key in the output, which several clients object to.
func reflector() *jsonschema.Reflector {
	return &jsonschema.Reflector{
		DoNotReference:             true,
		AllowAdditionalProperties:  false,
		RequiredFromJSONSchemaTags: true,
	}
}

// reflect produces a JSON Schema object for one Go value, inlined.
func reflect(v any) (json.RawMessage, error) {
	sch, err := inlined(v)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(sch)
	if err != nil {
		return nil, fmt.Errorf("reflecting %T: %w", v, err)
	}
	return raw, nil
}

func inlined(v any) (*jsonschema.Schema, error) {
	s := reflector().Reflect(v)
	s.Version = ""
	s.ID = ""
	if err := markUntrusted(goreflect.TypeOf(v), s, map[visit]bool{}); err != nil {
		return nil, fmt.Errorf("%T: %w", v, err)
	}
	return s, nil
}

// markUntrusted puts the boolean "x-droidyard-untrusted": true on the property of each
// field tagged untrusted, at every depth (ADR-0015 §1), by walking the Go type and its
// inlined schema together. The keyword is set here, from the one tag the registry also
// derives the descriptor's flag from, so the two cannot disagree. A tagged field it
// cannot place (a type with its own JSONSchema(), a map whose values the reflector put
// under patternProperties) is an error naming the type and field, never a silent miss.
type visit struct {
	t goreflect.Type
	s *jsonschema.Schema
}

func markUntrusted(t goreflect.Type, s *jsonschema.Schema, seen map[visit]bool) error {
	if t == nil {
		return nil
	}
	if s != nil {
		// A (type, schema node) pair is walked once. No type can reach this today:
		// with DoNotReference the reflector itself overflows its stack on a
		// self-referential type (observed on invopop/jsonschema v0.14.0), so such a
		// result never gets here. The guard keeps this walk from being the thing that
		// loops if that changes.
		if seen[visit{t, s}] {
			return nil
		}
		seen[visit{t, s}] = true
	}
	if s == nil {
		has, err := capability.HasUntrusted(t)
		if err != nil {
			return err
		}
		if has {
			return fmt.Errorf("%s has a field tagged %s, but its schema position cannot be found", t, capability.UntrustedTag)
		}
		return nil
	}
	switch t.Kind() {
	case goreflect.Pointer:
		return markUntrusted(t.Elem(), s, seen)
	case goreflect.Slice, goreflect.Array:
		return markUntrusted(t.Elem(), s.Items, seen)
	case goreflect.Map:
		return markUntrusted(t.Elem(), s.AdditionalProperties, seen)
	case goreflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, omitted := capability.JSONName(f)
			if (!f.IsExported() && !f.Anonymous) || omitted {
				continue
			}
			if name == "" { // an embedded struct is flattened into this one
				if err := markUntrusted(f.Type, s, seen); err != nil {
					return err
				}
				continue
			}
			tagged, err := capability.FieldUntrusted(f)
			if err != nil {
				return fmt.Errorf("%s: %w", t, err)
			}
			var prop *jsonschema.Schema
			if s.Properties != nil {
				prop, _ = s.Properties.Get(name)
			}
			if prop == nil {
				inner, err := capability.HasUntrusted(f.Type)
				if err != nil {
					return err
				}
				if tagged || inner {
					return fmt.Errorf("%s.%s: tagged %s (or holding a tagged field), but its property %q is not in the schema", t, f.Name, capability.UntrustedTag, name)
				}
				continue
			}
			if tagged {
				if prop.Extras == nil {
					prop.Extras = map[string]any{}
				}
				prop.Extras[capability.UntrustedKeyword] = true
			}
			if err := markUntrusted(f.Type, prop, seen); err != nil {
				return fmt.Errorf("%s.%s: %w", t, f.Name, err)
			}
		}
	}
	return nil
}

// operationView is the output of a long-running capability: the operation view, with
// the capability's own result schema, inlined, as its result
// (ADR-0020 §Adapter projections). operation.wait keeps the view's open object, since
// the schema that applies depends on the operation waited on.
func operationView(result any) (json.RawMessage, error) {
	view, err := inlined(capability.OperationView{})
	if err != nil {
		return nil, err
	}
	res, err := inlined(result)
	if err != nil {
		return nil, err
	}
	view.Properties.Set("result", res)
	raw, err := json.Marshal(view)
	if err != nil {
		return nil, fmt.Errorf("reflecting the operation view of %T: %w", result, err)
	}
	return raw, nil
}

// Generate builds the published schema for every registered capability.
func Generate(reg *capability.Registry, version string) (*Document, error) {
	doc := &Document{Version: version}

	for _, d := range reg.All() {
		input, err := reflect(d.Params)
		if err != nil {
			return nil, fmt.Errorf("capability %s: %w", d.Name, err)
		}

		var output json.RawMessage
		if d.LongRunning {
			output, err = operationView(d.Result)
		} else {
			output, err = reflect(d.Result)
		}
		if err != nil {
			return nil, fmt.Errorf("capability %s: %w", d.Name, err)
		}

		a := d.ResolveAnnotations()
		codes := make([]string, 0, len(d.Errors))
		for _, c := range d.Errors {
			codes = append(codes, string(c))
		}
		requires := make([]string, 0, len(d.Availability))
		for _, r := range d.Availability {
			requires = append(requires, string(r))
		}

		doc.Capabilities = append(doc.Capabilities, CapabilitySchema{
			Name:         d.Name,
			Kind:         d.Kind.String(),
			Scope:        d.Scope.String(),
			Statefulness: d.Statefulness.String(),
			LongRunning:  d.LongRunning,
			Audience:     d.Audience.String(),
			Since:        d.Since,
			Description:  d.Description,
			Availability: requires,
			Annotations: AnnotationsSchema{
				ReadOnly:    a.ReadOnly,
				Destructive: a.Destructive,
				Idempotent:  a.Idempotent,
				OpenWorld:   a.OpenWorld,
				Reason:      a.Reason,
			},
			Errors: codes,
			Input:  input,
			Output: output,
		})
	}

	return doc, nil
}
