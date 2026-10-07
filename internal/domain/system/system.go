// Package system holds the capabilities that describe the core itself.
package system

import (
	"context"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/store"
)

// Version is the API version reported by system.capabilities. It is the one semantic
// version of the capability API, not a per-capability one
// (docs/architecture/capability-api.md §Discovery and versioning).
const Version = "0.7.0"

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

// Register adds the system capabilities to the registry. gr, when non-nil, adds the
// sidecar's JVM to system.health.
func Register(reg *capability.Registry, req Requirements, adb ADB, ops Operations, problems StateProblems, gr Gradle) {
	reg.MustRegister(capability.Descriptor{
		Name:        "system.health",
		Kind:        capability.Query,
		Scope:       capability.Global,
		Since:       "0.1.0", // the version that introduced it, pinned
		Description: "Reports whether the core is alive, its version, how long it has been running, how many operations are in flight, any state file it could not use (stateProblems: corrupt files moved aside, or files of a newer version left untouched), where adb was found, its version and any warning (adb), and the JVM the Gradle sidecar runs on: where it was found and its version, or every candidate that was rejected (jvm).",
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
			return h, nil
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
