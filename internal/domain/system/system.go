// Package system holds the capabilities that describe the core itself.
package system

import (
	"context"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// Version is the API version reported by system.capabilities. It is the one semantic
// version of the capability API, not a per-capability one
// (docs/architecture/capability-api.md §Discovery and versioning).
const Version = "0.2.0"

var started = time.Now()

// Health is the result of system.health.
type Health struct {
	UptimeSeconds int    `json:"uptimeSeconds"`
	Version       string `json:"version"`
	Operations    int    `json:"operations"`
}

// CapabilityInfo is one capability as system.capabilities reports it.
type CapabilityInfo struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Scope        string `json:"scope"`
	Statefulness string `json:"statefulness"`
	LongRunning  bool   `json:"longRunning,omitempty"`
	Audience     string `json:"audience"`
	Description  string `json:"description"`
	Available    bool   `json:"available"`
	Unavailable  string `json:"unavailableReason,omitempty"`
}

// Capabilities is the result of system.capabilities.
type Capabilities struct {
	Version      string           `json:"version"`
	Capabilities []CapabilityInfo `json:"capabilities"`
}

// None is the empty parameter object, for capabilities that take no arguments.
type None struct{}

// Requirements reports which external tools are present. The first milestone has no
// integrations, so everything that needs one is unavailable, and it says why.
type Requirements interface {
	Missing(r capability.Requirement) (reason string, missing bool)
}

// Operations reports how many operations are in flight.
type Operations interface {
	Running() int
}

// Register adds the system capabilities to the registry.
func Register(reg *capability.Registry, req Requirements, ops Operations) {
	reg.MustRegister(capability.Descriptor{
		Name:        "system.health",
		Kind:        capability.Query,
		Scope:       capability.Global,
		Since:       "0.1.0", // the version that introduced it, pinned
		Description: "Reports whether the core is alive, its version and how long it has been running.",
		Params:      None{},
		Result:      Health{},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			// In-flight work is the signal; the calling operation is not part of it.
			running := ops.Running()
			if call.OperationID != "" {
				running--
			}
			return Health{
				UptimeSeconds: int(time.Since(started).Seconds()),
				Version:       Version,
				Operations:    running,
			}, nil
		},
	})

	reg.MustRegister(capability.Descriptor{
		Name:        "system.capabilities",
		Kind:        capability.Query,
		Scope:       capability.Global,
		Since:       "0.1.0", // the version that introduced it, pinned
		Description: "Lists every capability the core exposes, with its scope, statefulness, annotations and availability. This is the discovery entry point: it reports what is available rather than assuming a fixed surface.",
		Params:      None{},
		Result:      Capabilities{},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			out := Capabilities{Version: Version}
			for _, d := range reg.All() {
				info := CapabilityInfo{
					Name:         d.Name,
					Kind:         d.Kind.String(),
					Scope:        d.Scope.String(),
					Statefulness: d.Statefulness.String(),
					LongRunning:  d.LongRunning,
					Audience:     d.Audience.String(),
					Description:  d.Description,
					Available:    true,
				}
				for _, r := range d.Availability {
					if reason, missing := req.Missing(r); missing {
						info.Available = false
						info.Unavailable = reason
						break
					}
				}
				out.Capabilities = append(out.Capabilities, info)
			}
			return out, nil
		},
	})
}
