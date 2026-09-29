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

	// Errors are the codes this capability may return, so a consumer can handle them
	// without guessing (ADR-0020 §Error model).
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

// OperationView is what a long-running capability returns when the call has not
// finished. It is one object with no top-level union: a finished call sets Result.
type OperationView struct {
	OperationID string          `json:"operationId"`
	Capability  string          `json:"capability"`
	State       string          `json:"state"`
	Progress    *Progress       `json:"progress,omitempty"`
	BlockedBy   *BlockedBy      `json:"blockedBy,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

// Progress reports how far a long-running call has got.
type Progress struct {
	Phase   string `json:"phase,omitempty"`
	Done    int    `json:"done"`
	Total   int    `json:"total,omitempty"`
	Message string `json:"message,omitempty"`
}

// BlockedBy names what a queued operation is waiting behind.
type BlockedBy struct {
	OperationID string `json:"operationId,omitempty"`
	Capability  string `json:"capability,omitempty"`
	Reason      string `json:"reason,omitempty"`
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
	s := reflector().Reflect(v)
	s.Version = ""
	s.ID = ""
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("reflecting %T: %w", v, err)
	}
	return raw, nil
}

// Generate builds the published schema for every registered capability.
func Generate(reg *capability.Registry, version string) (*Document, error) {
	doc := &Document{Version: version}

	// The operation view is reflected once and reused.
	viewSchema, err := reflect(OperationView{})
	if err != nil {
		return nil, err
	}

	for _, d := range reg.All() {
		input, err := reflect(d.Params)
		if err != nil {
			return nil, fmt.Errorf("capability %s: %w", d.Name, err)
		}

		output := viewSchema
		if !d.LongRunning {
			output, err = reflect(d.Result)
			if err != nil {
				return nil, fmt.Errorf("capability %s: %w", d.Name, err)
			}
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
