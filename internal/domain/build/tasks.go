// Package build holds the capabilities that run and describe Gradle work. This
// milestone registers build.tasks, the filter over the cached project model
// (gradle.md §Task listing); builds arrive in a later step.
package build

import (
	"context"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
)

// Client is what build.tasks needs of the sidecar client: the project model and whether
// the cached one is stale. *gradle.Client implements it.
type Client interface {
	Model(ctx context.Context, root string) (gradle.Model, bool, error)
}

// Authorize is the trust re-check the scheduler runs before each handler: it returns
// the canonical root as Call.Authorized (internal/policy; ADR-0015 §2).
type Authorize func(ctx context.Context, call *capability.Call) (any, error)

// TasksParams is build.tasks' parameters.
type TasksParams struct {
	Module        string `json:"module,omitempty" jsonschema_description:"Only tasks of this Gradle project path, e.g. \":app\". Absent lists every module's."`
	Group         string `json:"group,omitempty" jsonschema_description:"Only tasks in this Gradle group, e.g. \"build\", exactly as Gradle reports it."`
	Contains      string `json:"contains,omitempty" jsonschema_description:"Only tasks whose name contains this text, case-insensitively."`
	IncludeHidden bool   `json:"includeHidden,omitempty" jsonschema_description:"Include hidden tasks (Gradle's public = false). Hidden tasks are noise by default."`
}

// Task is one available task.
type Task struct {
	Module      string `json:"module" jsonschema:"required" jsonschema_description:"The Gradle project path the task belongs to."`
	Path        string `json:"path" jsonschema:"required" jsonschema_description:"The task's full Gradle path, e.g. \":app:assembleDebug\"."`
	Name        string `json:"name" jsonschema:"required"`
	Group       string `json:"group,omitempty"`
	Description string `json:"description,omitempty"`
}

// TasksResult is build.tasks' result: the tasks the filters selected, in the model's
// order.
type TasksResult struct {
	Tasks []Task `json:"tasks" jsonschema:"required"`
	// Stale is true when the answer came from the cache while a build held the root and
	// the build files had changed (gradle.md §Concurrency; plan m1g D8).
	Stale bool `json:"stale"`
}

// registerTasks adds build.tasks to the registry. Register calls it, so build.tasks is
// registered with the same client and authorizer as the build capabilities.
func registerTasks(reg *capability.Registry, client Client, authorize Authorize) {
	reg.MustRegister(capability.Descriptor{
		Name:         "build.tasks",
		Kind:         capability.Query,
		Scope:        capability.Workspace,
		LongRunning:  true,
		Since:        "0.6.0",
		Availability: []capability.Requirement{capability.RequiresGradle},
		Authorize:    authorize,
		Params:       TasksParams{},
		Result:       TasksResult{},
		Errors: []capability.ErrorCode{
			capability.ErrWorkspaceUntrusted,
			capability.ErrBuildSidecarUnavailable,
			capability.ErrBuildSidecarCrashed,
			capability.ErrBuildGradleUnsupported,
			capability.ErrProjectModelFailed,
			capability.ErrInvalidParams,
		},
		Description: "Lists the tasks that exist in the project, filtered by module, group or a name substring, from the cached project model (it never runs Gradle). Hidden tasks are left out unless includeHidden is true.",
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(TasksParams)
			model, stale, err := client.Model(ctx, call.Authorized.(string))
			if err != nil {
				return nil, err
			}
			modules := model.Projects
			if p.Module != "" {
				m, ok := model.Module(p.Module)
				if !ok {
					paths := make([]string, 0, len(model.Projects))
					for _, mp := range model.Projects {
						paths = append(paths, mp.Path)
					}
					return nil, capability.Errorf(capability.ErrInvalidParams, "no project %q in this workspace", p.Module).
						WithDetails(map[string]any{"modules": paths}).
						WithNextStep("call project.modules for the project paths")
				}
				modules = []gradle.Module{m}
			}
			contains := strings.ToLower(p.Contains)
			out := TasksResult{Tasks: []Task{}, Stale: stale}
			for _, m := range modules {
				for _, t := range m.Tasks {
					if !p.IncludeHidden && !t.Public {
						continue
					}
					if p.Group != "" && t.Group != p.Group {
						continue
					}
					if contains != "" && !strings.Contains(strings.ToLower(t.Name), contains) {
						continue
					}
					out.Tasks = append(out.Tasks, Task{Module: m.Path, Path: t.Path, Name: t.Name, Group: t.Group, Description: t.Description})
				}
			}
			return out, nil
		},
	})
}
