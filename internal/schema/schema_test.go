package schema_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/schema"
)

// update rewrites the golden file instead of comparing against it.
var update = flag.Bool("update", false, "rewrite the committed schema")

// goldenPath is the published schema. It is committed and reviewed: a change in the
// Go descriptors that alters what a consumer sees shows up as a diff here (ADR-0016).
const goldenPath = "../../schema/capabilities.json"

// nothingInstalled stands in for the as-yet-unwritten requirement checks.
type nothingInstalled struct{}

func (nothingInstalled) Missing(r capability.Requirement) (string, bool) {
	return string(r) + " was not found on this machine", true
}

func registry() *capability.Registry {
	reg := capability.NewRegistry()
	system.Register(reg, nothingInstalled{})
	return reg
}

func generate(t *testing.T) []byte {
	t.Helper()
	doc, err := schema.Generate(registry(), system.Version)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return append(raw, '\n')
}

func TestGeneratedSchemaMatchesTheGoldenFile(t *testing.T) {
	got := generate(t)

	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading the golden file: %v (run: go test ./internal/schema -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the published schema changed.\nIf the change is intended, run: go test ./internal/schema -update\n\n--- got ---\n%s", got)
	}
}

// TestSchemaObeysTheClientRules checks the two rules that exist because of how real
// MCP clients behave: everything is inlined, and nothing is a top-level union.
func TestSchemaObeysTheClientRules(t *testing.T) {
	var doc struct {
		Capabilities []struct {
			Name   string          `json:"name"`
			Input  json.RawMessage `json:"input"`
			Output json.RawMessage `json:"output"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(generate(t), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Capabilities) == 0 {
		t.Fatal("the schema has no capabilities")
	}

	for _, c := range doc.Capabilities {
		for _, s := range []json.RawMessage{c.Input, c.Output} {
			var obj map[string]any
			if err := json.Unmarshal(s, &obj); err != nil {
				t.Fatalf("%s: a schema is not an object: %v", c.Name, err)
			}
			if _, bad := obj["$ref"]; bad {
				t.Errorf("%s: a schema uses $ref; references must be inlined", c.Name)
			}
			if _, bad := obj["$defs"]; bad {
				t.Errorf("%s: a schema has $defs; definitions must be inlined", c.Name)
			}
			for _, union := range []string{"oneOf", "anyOf", "allOf"} {
				if _, bad := obj[union]; bad {
					t.Errorf("%s: a schema has a top-level %s; unions are split into separate capabilities", c.Name, union)
				}
			}
			if obj["type"] != "object" {
				t.Errorf("%s: a schema is %v, not an object", c.Name, obj["type"])
			}
		}
	}
}

// TestLongRunningCapabilitiesAdvertiseTheOperationView checks the decision that
// resolved the output-schema problem: a long-running tool's output schema is the
// operation view, not its result.
func TestLongRunningCapabilitiesAdvertiseTheOperationView(t *testing.T) {
	reg := capability.NewRegistry()
	reg.MustRegister(capability.Descriptor{
		Name:        "build.assemble",
		Kind:        capability.Command,
		Scope:       capability.Workspace,
		LongRunning: true,
		Since:       "0.1.0",
		Description: "Assembles a variant.",
		Params:      struct{}{},
		Result:      struct{}{},
		Handler:     func(_ context.Context, _ *capability.Call) (any, error) { return nil, nil },
	})

	doc, err := schema.Generate(reg, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(doc.Capabilities[0].Output, &out); err != nil {
		t.Fatal(err)
	}
	props, _ := out["properties"].(map[string]any)
	if _, ok := props["operationId"]; !ok {
		t.Errorf("a long-running capability did not advertise the operation view: %s", doc.Capabilities[0].Output)
	}
	if _, ok := props["result"]; !ok {
		t.Errorf("the operation view has no result field: %s", doc.Capabilities[0].Output)
	}
}
