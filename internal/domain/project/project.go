// Package project registers the capabilities that describe a project's shape:
// project.modules and project.variants (ADR-0023). Both answer from the sidecar's
// cached project model, passed into this package as the Client interface, and hold no
// logic of their own beyond shaping the model into the contract's results.
package project

import (
	"context"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
)

// Client is what these capabilities need of the sidecar client: the project model and
// whether the cached one is stale. *gradle.Client implements it.
type Client interface {
	Model(ctx context.Context, root string) (gradle.Model, bool, error)
}

// Authorize is the trust re-check the scheduler runs before each handler: it returns
// the canonical root as Call.Authorized (internal/policy; ADR-0015 §2).
type Authorize func(ctx context.Context, call *capability.Call) (any, error)

// None is the empty parameter object.
type None struct{}

// Module is one module in project.modules' result. buildDir stays core-internal and is
// never published (gradle.md §Project model).
type Module struct {
	Path string `json:"path" jsonschema:"required" jsonschema_description:"The Gradle project path, e.g. \":app\"; the root project is \":\"."`
	Name string `json:"name" jsonschema:"required" jsonschema_description:"The project's name as Gradle reports it."`
	Dir  string `json:"dir" jsonschema:"required" jsonschema_description:"The project directory, relative to the root; \".\" for the root project."`
}

// ModulesResult is project.modules' result.
type ModulesResult struct {
	Modules []Module `json:"modules" jsonschema:"required"`
	// Stale says the model came from the cache because a build holds the root, and the
	// build files changed since it was fetched (gradle.md §Concurrency; plan m1g D8).
	Stale bool `json:"stale"`
}

// VariantsParams is project.variants' parameters.
type VariantsParams struct {
	Module string `json:"module,omitempty" jsonschema_description:"The Gradle project path to report variants for, e.g. \":app\". Absent reports every module."`
}

// VariantsResult is project.variants' result: variants per module.
type VariantsResult struct {
	Modules []ModuleVariants `json:"modules" jsonschema:"required"`
	Stale   bool             `json:"stale"`
}

// ModuleVariants is one module's variants and how they were derived.
type ModuleVariants struct {
	Module   string   `json:"module" jsonschema:"required"`
	Variants []string `json:"variants" jsonschema:"required" jsonschema_description:"Variant names; empty for a module that produces no APK variants."`
	Source   string   `json:"source" jsonschema:"required,enum=probe,enum=task-names" jsonschema_description:"How the variants were derived: probe is exact (a later milestone); task-names is the labelled fallback of ADR-0023 §Decision 4."`
	Exact    bool     `json:"exact" jsonschema_description:"True only for source probe."`
}

// errors are the codes a handler here can return: the sidecar's (plan m1g D9), the
// trust hook's, and REQUEST_INVALID_PARAMS where a module argument is validated.
var baseErrors = []capability.ErrorCode{
	capability.ErrWorkspaceUntrusted,
	capability.ErrBuildSidecarUnavailable,
	capability.ErrBuildSidecarCrashed,
	capability.ErrBuildGradleUnsupported,
	capability.ErrProjectModelFailed,
}

// Models is the description project.modules and project.variants share: what the model
// is, where it comes from and what its staleness means.
const modelNote = "The answer comes from one Gradle configuration pass, cached per (root, build-files fingerprint) and consulted without a pass when a build holds the root. " +
	"When a build holds the root and the fingerprint changed since the model was fetched, the result is the cached one and stale is true. " +
	"Only a trusted root is answered: an untrusted one fails WORKSPACE_UNTRUSTED before any Gradle code runs. " +
	"Fails with BUILD_SIDECAR_UNAVAILABLE when no JVM 17 or newer was found or the sidecar could not start, BUILD_SIDECAR_CRASHED when it died or broke its protocol, BUILD_GRADLE_UNSUPPORTED when the project's Gradle is below the 8.0 floor, and PROJECT_MODEL_FAILED when the configuration pass failed."

// Register adds project.modules and project.variants to the registry.
func Register(reg *capability.Registry, client Client, authorize Authorize) {
	reg.MustRegister(capability.Descriptor{
		Name:  "project.modules",
		Kind:  capability.Query,
		Scope: capability.Workspace,
		// A cold call runs a configuration pass, so it may outlive an adapter's wait
		// budget (capability-api.md §Core capabilities).
		LongRunning:  true,
		Since:        "0.6.0",
		Availability: []capability.Requirement{capability.RequiresGradle},
		Authorize:    authorize,
		Params:       None{},
		Result:       ModulesResult{},
		Errors:       baseErrors,
		Description:  "Lists the Gradle projects of the workspace's root: every project including the root \":\" and the container projects, with the Gradle project path, the name and the directory relative to the root. " + modelNote,
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			model, stale, err := client.Model(ctx, root(call))
			if err != nil {
				return nil, err
			}
			out := ModulesResult{Modules: []Module{}, Stale: stale}
			for _, p := range model.Projects {
				out.Modules = append(out.Modules, Module{Path: p.Path, Name: p.Name, Dir: p.Dir})
			}
			return out, nil
		},
	})

	reg.MustRegister(capability.Descriptor{
		Name:         "project.variants",
		Kind:         capability.Query,
		Scope:        capability.Workspace,
		LongRunning:  true,
		Since:        "0.6.0",
		Availability: []capability.Requirement{capability.RequiresGradle},
		Authorize:    authorize,
		Params:       VariantsParams{},
		Result:       VariantsResult{},
		Errors:       append(append([]capability.ErrorCode{}, baseErrors...), capability.ErrInvalidParams),
		Description: "Reports the build variants of each module (or of one, with module), as { module, variants[], source, exact } per module. " +
			"Without the Android probe this milestone derives them from task names: the X with both assemble<X> and package<X> among the module's tasks, minus names ending AndroidTest, UnitTest or TestFixtures, so source is task-names and exact is false (ADR-0023 §Decision 4). " +
			"A module that produces no APK variants (a library, or the root project) reports an empty list. " +
			"A module argument that names no project is REQUEST_INVALID_PARAMS. " + modelNote,
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(VariantsParams)
			model, stale, err := client.Model(ctx, root(call))
			if err != nil {
				return nil, err
			}
			modules := model.Projects
			if p.Module != "" {
				m, ok := model.Module(p.Module)
				if !ok {
					return nil, capability.Errorf(capability.ErrInvalidParams, "no project %q in this workspace", p.Module).
						WithDetails(map[string]any{"modules": modulePaths(model)}).
						WithNextStep("call project.modules for the project paths")
				}
				modules = []gradle.Module{m}
			}
			out := VariantsResult{Modules: []ModuleVariants{}, Stale: stale}
			for _, m := range modules {
				variants := gradle.VariantNames(m)
				if variants == nil {
					variants = []string{}
				}
				out.Modules = append(out.Modules, ModuleVariants{Module: m.Path, Variants: variants, Source: "task-names", Exact: false})
			}
			return out, nil
		},
	})
}

// root is the canonical project root the Authorize hook decided.
func root(call *capability.Call) string { return call.Authorized.(string) }

func modulePaths(m gradle.Model) []string {
	paths := make([]string, 0, len(m.Projects))
	for _, p := range m.Projects {
		paths = append(paths, p.Path)
	}
	return paths
}
