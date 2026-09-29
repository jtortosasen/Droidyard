package main

import (
	"encoding/json"
	"os"
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
	for _, d := range registry().All() {
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
