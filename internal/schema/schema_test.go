package schema_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/agent"
	"github.com/jtortosasen/droidyard/internal/domain/artifacts"
	"github.com/jtortosasen/droidyard/internal/domain/build"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/emulator"
	"github.com/jtortosasen/droidyard/internal/domain/file"
	"github.com/jtortosasen/droidyard/internal/domain/git"
	"github.com/jtortosasen/droidyard/internal/domain/project"
	"github.com/jtortosasen/droidyard/internal/domain/sdk"
	"github.com/jtortosasen/droidyard/internal/domain/shell"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
	"github.com/jtortosasen/droidyard/internal/schema"
	"github.com/jtortosasen/droidyard/internal/workspace"
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

// noSidecar is the Gradle client the golden registry is built with: the schema needs
// the descriptors, and no handler of the golden registry is ever run.
type noSidecar struct{}

func (noSidecar) Model(context.Context, string) (gradle.Model, bool, error) {
	return gradle.Model{}, false, nil
}

func (noSidecar) BeginBuild(context.Context, string) (*gradle.BuildRun, error) {
	return nil, errors.New("the golden registry has no sidecar")
}

// trustNothing is the trust hook the golden registry is built with: only the
// descriptor matters here.
func trustNothing(context.Context, *capability.Call) (any, error) { return nil, nil }

func registry() *capability.Registry {
	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	// A hermetic policy: no configuration directory resolves, so it reads no file. Only
	// its descriptors matter here — no handler of the golden registry is ever run.
	pol := policy.New(platform.Env{Getenv: func(string) string { return "" }})
	policy.Register(reg, pol)
	system.Register(reg, nothingInstalled{}, nil, ops, nil, nil, pol)
	store, err := artifact.New(artifact.Options{Epoch: "schema"})
	if err != nil {
		panic(err)
	}
	device.Register(reg, device.NewADB(nil, nil), store, nil)
	emulator.Register(reg, emulator.New(nil, device.NewADB(nil, nil), nil, ""))
	artifacts.Register(reg, store)
	// The golden registry only needs the sdk descriptors: no handler is run, so an empty
	// environment (which resolves no SDK) and no client are enough.
	sdk.Register(reg, sdk.Options{})
	ops.RegisterCapabilities()
	project.Register(reg, noSidecar{}, trustNothing)
	build.Register(reg, build.Options{Client: noSidecar{}, Authorize: trustNothing, AuthorizeGradle: trustNothing})
	file.Register(reg, file.Options{})
	git.Register(reg, git.Options{})
	agent.Register(reg, agent.Options{})
	// The shell descriptors only: no handler of the golden registry is ever run, so the
	// policy, trust, starter and store are left nil.
	shell.Register(reg, shell.Options{})
	workspace.Register(reg, workspace.New(workspace.Options{Ops: ops}))
	event.Register(reg, ops.Active)
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
			Events *struct {
				Topic string                     `json:"topic"`
				Types map[string]json.RawMessage `json:"types"`
			} `json:"events"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(generate(t), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Capabilities) == 0 {
		t.Fatal("the schema has no capabilities")
	}

	for _, c := range doc.Capabilities {
		// A stream's event payloads obey the same rules: they are what the generated
		// client types, and a $ref or a union there would not survive its generator.
		schemas := []json.RawMessage{c.Input, c.Output}
		if c.Events != nil {
			if c.Events.Topic == "" || len(c.Events.Types) == 0 {
				t.Errorf("%s: events has topic %q and %d types; both are required", c.Name, c.Events.Topic, len(c.Events.Types))
			}
			for _, raw := range c.Events.Types {
				schemas = append(schemas, raw)
			}
		}
		for _, s := range schemas {
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
// operation view, not its result, and the view's result is that capability's own
// result schema, inlined (ADR-0020 §Adapter projections). The reference is the same
// result type reflected as a capability that is not long-running.
func TestLongRunningCapabilitiesAdvertiseTheOperationView(t *testing.T) {
	type assembled struct {
		Outcome string   `json:"outcome"`
		APKs    []string `json:"apks,omitempty"`
	}
	reg := capability.NewRegistry()
	for _, longRunning := range []bool{true, false} {
		name := "build.assemble"
		if !longRunning {
			name = "build.twin"
		}
		reg.MustRegister(capability.Descriptor{
			Name:        name,
			Kind:        capability.Command,
			Scope:       capability.Workspace,
			LongRunning: longRunning,
			Since:       "0.1.0",
			Description: "Assembles a variant.",
			Params:      struct{}{},
			Result:      assembled{},
			Handler:     func(_ context.Context, _ *capability.Call) (any, error) { return nil, nil },
		})
	}

	doc, err := schema.Generate(reg, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	outputs := map[string]map[string]any{}
	for _, c := range doc.Capabilities {
		var out map[string]any
		if err := json.Unmarshal(c.Output, &out); err != nil {
			t.Fatal(err)
		}
		outputs[c.Name] = out
	}
	props, _ := outputs["build.assemble"]["properties"].(map[string]any)
	for _, field := range []string{"operationId", "capability", "state", "progress", "blockedBy", "result"} {
		if _, ok := props[field]; !ok {
			t.Errorf("the long-running output lacks the view's %s: %v", field, outputs["build.assemble"])
		}
	}
	if !reflect.DeepEqual(props["result"], outputs["build.twin"]) {
		r, _ := json.Marshal(props["result"])
		w, _ := json.Marshal(outputs["build.twin"])
		t.Errorf("the view's result is %s; want the capability's own result schema %s", r, w)
	}
}

// TestNoCapabilityTakesASessionID is the decision of ADR-0048 §2: a session id is an
// identity the events show, never a handle a caller can present, so no capability's
// parameters, at any depth, name a session. If one did, the id the events publish would
// become a credential for whatever that capability acts on.
func TestNoCapabilityTakesASessionID(t *testing.T) {
	var doc struct {
		Capabilities []struct {
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(generate(t), &doc); err != nil {
		t.Fatal(err)
	}
	var walk func(name, path string, node any)
	walk = func(name, path string, node any) {
		switch n := node.(type) {
		case map[string]any:
			if props, ok := n["properties"].(map[string]any); ok {
				for prop, sub := range props {
					if strings.Contains(strings.ToLower(prop), "session") {
						t.Errorf("%s takes %s.%s: no capability takes a session id (ADR-0048 §2)", name, path, prop)
					}
					walk(name, path+"."+prop, sub)
				}
			}
			for k, v := range n {
				if k != "properties" {
					walk(name, path, v)
				}
			}
		case []any:
			for _, v := range n {
				walk(name, path, v)
			}
		}
	}
	for _, c := range doc.Capabilities {
		walk(c.Name, "input", c.Input)
	}
	if len(doc.Capabilities) == 0 {
		t.Fatal("the schema has no capabilities")
	}
}

// TestTargetsFollowTheParameters is the decision of ADR-0076 §2: a capability that takes a
// device declares a target naming it, so its operations mirror per device, and one that
// does not take one declares none; the AVD and module capabilities likewise. A new
// capability with a `device` parameter and no Target fails here, which is the point: it
// would otherwise mirror as untargeted and reload every device's panels (ADR-0076
// Consequences). The expected set is stated, so build.run (its tasks are not a target)
// and the capabilities whose parameters name nothing are pinned too.
func TestTargetsFollowTheParameters(t *testing.T) {
	declared := map[string]bool{}
	for _, d := range registry().All() {
		pt := reflect.TypeOf(d.Params)
		fieldOf := func(json string) (reflect.StructField, bool) {
			if pt == nil || pt.Kind() != reflect.Struct {
				return reflect.StructField{}, false
			}
			for i := 0; i < pt.NumField(); i++ {
				if strings.Split(pt.Field(i).Tag.Get("json"), ",")[0] == json && pt.Field(i).Type.Kind() == reflect.String {
					return pt.Field(i), true
				}
			}
			return reflect.StructField{}, false
		}
		dev, hasDevice := fieldOf("device")
		avd, hasAVD := fieldOf("avd")
		if d.Target == nil {
			if hasDevice || hasAVD {
				t.Errorf("%s takes a device or an AVD and declares no Target (ADR-0076 §2)", d.Name)
			}
			continue
		}
		declared[d.Name] = true
		v := reflect.New(pt).Elem()
		if hasDevice {
			v.FieldByName(dev.Name).SetString("serial-1")
		}
		if hasAVD {
			v.FieldByName(avd.Name).SetString("avd-1")
		}
		got := d.Target(v.Interface())
		switch {
		case hasDevice && (got == nil || got.Device != "serial-1"):
			t.Errorf("%s: Target = %+v; want the device parameter, serial-1", d.Name, got)
		case hasAVD && (got == nil || got.AVD != "avd-1"):
			t.Errorf("%s: Target = %+v; want the avd parameter, avd-1", d.Name, got)
		}
	}
	want := []string{"build.assemble", "device.inputText", "device.install", "device.launch", "device.logs",
		"device.pressKey", "device.screenshot", "device.swipe", "device.tap", "device.tapElement", "device.uiTree",
		"device.uninstall", "device.watchLogs", "device.watchScreen", "emulator.start", "emulator.stop", "emulator.wipe"}
	var got []string
	for name := range declared {
		got = append(got, name)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("the capabilities that declare a Target are\n%v\nwant (ADR-0076 §2)\n%v", got, want)
	}
}

// TestOccupiesFollowsTheDeviceOccupationRule is the decision of ADR-0094 §2: the capabilities
// that hold a device for the calling session are stated, not inferred, and the schema says
// which of them can be refused. Seven acting capabilities list DEVICE_OCCUPIED in their
// errors; emulator.start holds the serial it boots and is never refused, so it does not;
// and no capability lists the code without being one that occupies. A new acting
// capability, or a code listed on a read, fails here.
func TestOccupiesFollowsTheDeviceOccupationRule(t *testing.T) {
	acting := []string{"device.inputText", "device.install", "device.launch", "device.tapElement",
		"device.uninstall", "emulator.stop", "emulator.wipe"}
	var gotActing, gotBoot []string
	for _, d := range registry().All() {
		lists := slices.Contains(d.Errors, device.ErrOccupied)
		switch d.Occupies {
		case capability.OccupiesAct:
			gotActing = append(gotActing, d.Name)
			if !lists {
				t.Errorf("%s can be refused a held device and does not list %s", d.Name, device.ErrOccupied)
			}
		case capability.OccupiesBoot:
			gotBoot = append(gotBoot, d.Name)
			if lists {
				t.Errorf("%s is never refused, yet lists %s", d.Name, device.ErrOccupied)
			}
		default:
			if lists {
				t.Errorf("%s lists %s and occupies nothing", d.Name, device.ErrOccupied)
			}
		}
	}
	slices.Sort(gotActing)
	if !slices.Equal(gotActing, acting) {
		t.Errorf("capabilities that act on a held device = %v; want %v", gotActing, acting)
	}
	if !slices.Equal(gotBoot, []string{"emulator.start"}) {
		t.Errorf("capabilities that boot a held device = %v; want [emulator.start]", gotBoot)
	}
}
