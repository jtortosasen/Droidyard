// Package system holds the capabilities that describe the core itself.
package system

import (
	"context"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/policy"
	"github.com/jtortosasen/droidyard/internal/store"
)

// Version is the API version reported by system.capabilities. It is the one semantic
// version of the capability API, not a per-capability one
// (docs/architecture/capability-api.md §Discovery and versioning).
const Version = "0.22.0"

var started = time.Now()

// Health is the result of system.health.
type Health struct {
	UptimeSeconds int    `json:"uptimeSeconds"`
	Version       string `json:"version"`
	Operations    int    `json:"operations"`
	// StateProblems are the state files the daemon could not use: corrupt ones,
	// moved aside, and ones of a newer version, left untouched (ADR-0021 §Format;
	// plan m1c, R9).
	StateProblems []StateProblem `json:"stateProblems,omitempty"`
	// ADB is where adb was found (or every place it was not), its version and the
	// running server's, and warnings such as a client/server mismatch
	// (adb-device.md §adb resolution and degradation). Absent when the daemon has
	// no adb subsystem.
	ADB *device.Status `json:"adb,omitempty"`
	// JVM is the JVM the Gradle sidecar runs on: where it was found and its version, or
	// every candidate rejected (gradle.md §Sidecar JVM discovery; plan m1g D22).
	// Absent when the daemon has no Gradle subsystem.
	JVM *gradle.JVM `json:"jvm,omitempty"`
	// Config is the configuration file's state: its path, whether it parsed, and the
	// state the tool keys were read in at daemon start (ADR-0032 §4, §6; plan m1k D1).
	// Absent when the daemon has no policy.
	Config *policy.ConfigStatus `json:"config,omitempty"`
}

// StateProblem is one state file the daemon could not use. File and MovedTo are
// relative to the state root.
type StateProblem struct {
	File    string `json:"file" jsonschema:"required"`
	Problem string `json:"problem" jsonschema:"required,enum=corrupt,enum=newerVersion"`
	MovedTo string `json:"movedTo,omitempty"`
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

// capabilitiesText is the rendering of system.capabilities (ADR-0036 Decision 2): the
// version, then one line per capability, <name> <kind> <scope> [stateful|streaming]
// [long] [human] [unavailable "<reason>"]. The descriptions are in the structured content
// only; an agent has them in the tool list already.
func capabilitiesText(c Capabilities) string {
	var t capability.Text
	t.W("version", c.Version).Nl()
	for _, i := range c.Capabilities {
		t.W(i.Name, i.Kind, i.Scope)
		if i.Statefulness != "stateless" {
			t.W(i.Statefulness)
		}
		if i.LongRunning {
			t.W("long")
		}
		if i.Audience != "all" {
			t.W(i.Audience)
		}
		if !i.Available {
			t.W("unavailable").Q(i.Unavailable)
		}
		t.Nl()
	}
	return t.String()
}

// None is the empty parameter object, for capabilities that take no arguments.
type None struct{}

// Requirements reports which external tools are present. The first milestone has no
// integrations, so everything that needs one is unavailable, and it says why.
type Requirements interface {
	Missing(r capability.Requirement) (reason string, missing bool)
}

// ADB reports adb's resolution and versions for system.health: device.ADB.Check.
type ADB interface {
	Check(ctx context.Context) device.Status
}

// Gradle reports the JVM the Gradle sidecar runs on, for system.health (plan m1g
// D22). *gradle.Client implements it.
type Gradle interface {
	JVM() gradle.JVM
}

// Operations reports how many operations are in flight.
type Operations interface {
	Running() int
}

// StateProblems lists the state files the daemon could not use; the store's
// Problems. Nil reports none.
type StateProblems func() []store.Problem

// Config reports the configuration file's state for system.health (plan m1k D1).
// *policy.Policy implements it.
type Config interface {
	ConfigStatus() policy.ConfigStatus
}

// Register adds the system capabilities to the registry. gr, when non-nil, adds the
// sidecar's JVM to system.health; cfg, when non-nil, the configuration file's state.
func Register(reg *capability.Registry, req Requirements, adb ADB, ops Operations, problems StateProblems, gr Gradle, cfg Config) {
	reg.MustRegister(capability.Descriptor{
		Name:        "system.health",
		Kind:        capability.Query,
		Scope:       capability.Global,
		Since:       "0.1.0", // the version that introduced it, pinned
		Description: "Reports whether the core is alive, its version, uptime and operations in flight, where adb and the JVM were found, any state file it could not use, and the configuration file's state.",
		Params:      None{},
		Result:      Health{},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			// In-flight work is the signal; the calling operation is not part of it.
			running := ops.Running()
			if call.OperationID != "" {
				running--
			}
			h := Health{
				UptimeSeconds: int(time.Since(started).Seconds()),
				Version:       Version,
				Operations:    running,
			}
			if problems != nil {
				for _, p := range problems() {
					h.StateProblems = append(h.StateProblems, StateProblem{File: p.File, Problem: p.Problem, MovedTo: p.MovedTo})
				}
			}
			if adb != nil {
				st := adb.Check(ctx)
				h.ADB = &st
			}
			if gr != nil {
				j := gr.JVM()
				h.JVM = &j
			}
			if cfg != nil {
				cs := cfg.ConfigStatus()
				h.Config = &cs
			}
			return h, nil
		},
	})

	reg.MustRegister(capability.Descriptor{
		Name:        "system.capabilities",
		Kind:        capability.Query,
		Scope:       capability.Global,
		Since:       "0.1.0", // the version that introduced it, pinned
		Description: "Lists every capability the core exposes, with its scope, statefulness, annotations and availability: the discovery entry point.",
		Params:      None{},
		Result:      Capabilities{},
		Text:        capability.TextOf(capabilitiesText),
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
