package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestShippedRegistryIsThePublishedOne checks that the registry this binary builds
// names the same capabilities as the committed schema. The golden file is generated
// from the registry that internal/schema's test assembles, and main assembles its own;
// a domain registered in one and not the other would ship a surface the published
// contract does not describe (ADR-0016).
func TestShippedRegistryIsThePublishedOne(t *testing.T) {
	raw, err := os.ReadFile("../../schema/capabilities.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Capabilities []struct {
			Name string `json:"name"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	published := map[string]bool{}
	for _, c := range doc.Capabilities {
		published[c.Name] = true
	}
	shipped := map[string]bool{}
	reg, _, _, _, _, _ := registry(isolatedEnv(t), nil, nil, nil)
	for _, d := range reg.All() {
		shipped[d.Name] = true
		if !published[d.Name] {
			t.Errorf("cmd/droidyard registers %s, but schema/capabilities.json does not publish it. Register its domain in registry() in internal/schema/schema_test.go as well, then run: go test ./internal/schema -update", d.Name)
		}
	}
	for n := range published {
		if !shipped[n] {
			t.Errorf("schema/capabilities.json publishes %s, but registry() in cmd/droidyard/main.go does not register it. Register its domain there", n)
		}
	}
}

// TestAdaptersImportOnlyTheCore: adapters translate; they may import capability, event
// and artifact from this module, never domain/*, schema, daemon or platform
// (docs/architecture/core-runtime.md §Adapter boundary; ADR-0025 §Consequences).
func TestAdaptersImportOnlyTheCore(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-deps", "../../adapters/...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	const module = "github.com/jtortosasen/droidyard/"
	allowed := map[string]bool{"internal/capability": true, "internal/event": true, "internal/artifact": true}
	listed := map[string]bool{}
	for _, pkg := range strings.Fields(string(out)) {
		rel, ok := strings.CutPrefix(pkg, module)
		if !ok {
			continue
		}
		listed[rel] = true
		if !strings.HasPrefix(rel, "adapters/") && !allowed[rel] {
			t.Errorf("an adapter depends on %s; adapters may import only internal/capability, internal/event and internal/artifact", rel)
		}
	}
	// The list is the adapters' real graph, not an empty or failed listing.
	for _, want := range []string{"adapters/cli", "adapters/httpapi", "adapters/mcp", "internal/capability"} {
		if !listed[want] {
			t.Errorf("go list -deps ./adapters/... does not list %s: %s", want, out)
		}
	}
}

// TestTheDaemonImportsNoAdapter: internal/daemon is given the HTTP handler and the
// session handlers; it never imports an adapter (core-runtime.md §Adapter boundary;
// plan m1d, step 4).
func TestTheDaemonImportsNoAdapter(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-deps", "../../internal/daemon").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	if !strings.Contains(string(out), "internal/daemon") {
		t.Fatalf("go list -deps did not list internal/daemon: %s", out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.Contains(pkg, "/droidyard/adapters/") {
			t.Errorf("internal/daemon depends on %s", pkg)
		}
	}
}
