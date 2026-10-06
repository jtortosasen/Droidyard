package build

import (
	"context"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
)

type fakeClient struct {
	model gradle.Model
	stale bool
	err   error
}

func (f fakeClient) Model(context.Context, string) (gradle.Model, bool, error) {
	return f.model, f.stale, f.err
}

// fixture is a model with the task shapes build.tasks filters: two groups, a hidden
// task, and names that share a substring in different cases.
func fixture() gradle.Model {
	return gradle.Model{Root: "/w/dc", Projects: []gradle.Module{
		{Path: ":app", Name: "app", Tasks: []gradle.Task{
			{Name: "assembleDebug", Path: ":app:assembleDebug", Group: "build", Description: "Assembles debug.", Public: true},
			{Name: "assembleRelease", Path: ":app:assembleRelease", Group: "build", Public: true},
			{Name: "connectedDebugAndroidTest", Path: ":app:connectedDebugAndroidTest", Group: "verification", Public: true},
			{Name: "uninstallDebug", Path: ":app:uninstallDebug", Group: "install", Public: true},
			{Name: "internalSecret", Path: ":app:internalSecret", Group: "build", Public: false},
		}},
		{Path: ":core:database", Name: "database", Tasks: []gradle.Task{
			{Name: "assembleDebug", Path: ":core:database:assembleDebug", Group: "build", Public: true},
		}},
	}}
}

func handler(t *testing.T, client Client) capability.Handler {
	t.Helper()
	reg := capability.NewRegistry()
	Register(reg, client, nil)
	d, ok := reg.Lookup("build.tasks")
	if !ok {
		t.Fatal("build.tasks is not registered")
	}
	for _, want := range []capability.ErrorCode{
		capability.ErrWorkspaceUntrusted, capability.ErrBuildSidecarUnavailable,
		capability.ErrBuildSidecarCrashed, capability.ErrBuildGradleUnsupported,
		capability.ErrProjectModelFailed, capability.ErrInvalidParams,
	} {
		if !containsCode(d.Errors, want) {
			t.Errorf("build.tasks' descriptor omits the code %s its handler can return: %v", want, d.Errors)
		}
	}
	return d.Handler
}

func containsCode(codes []capability.ErrorCode, want capability.ErrorCode) bool {
	for _, c := range codes {
		if c == want {
			return true
		}
	}
	return false
}

func run(t *testing.T, h capability.Handler, p TasksParams) TasksResult {
	t.Helper()
	got, err := h(context.Background(), &capability.Call{Authorized: "/w/dc", Params: p})
	if err != nil {
		t.Fatal(err)
	}
	return got.(TasksResult)
}

func names(res TasksResult) string {
	var out []string
	for _, task := range res.Tasks {
		out = append(out, task.Path)
	}
	return strings.Join(out, ",")
}

// TestTasksFilters pins build.tasks' filters over the cached model: no filter lists
// every public task of every module; module, group and a case-insensitive name
// substring narrow it; hidden tasks appear only with includeHidden (gradle.md §Task
// listing).
func TestTasksFilters(t *testing.T) {
	h := handler(t, fakeClient{model: fixture(), stale: true})

	all := run(t, h, TasksParams{})
	if all.Stale != true {
		t.Error("stale was not passed through")
	}
	if want := ":app:assembleDebug,:app:assembleRelease,:app:connectedDebugAndroidTest,:app:uninstallDebug,:core:database:assembleDebug"; names(all) != want {
		t.Errorf("no filter = %s; want %s (no hidden task)", names(all), want)
	}

	if got := names(run(t, h, TasksParams{Module: ":core:database"})); got != ":core:database:assembleDebug" {
		t.Errorf("module :core:database = %s", got)
	}
	if got := names(run(t, h, TasksParams{Group: "verification"})); got != ":app:connectedDebugAndroidTest" {
		t.Errorf("group verification = %s", got)
	}
	if got := names(run(t, h, TasksParams{Contains: "DEBUG"})); strings.Contains(got, "assembleRelease") || !strings.Contains(got, ":app:assembleDebug") || !strings.Contains(got, ":app:connectedDebugAndroidTest") {
		t.Errorf("contains DEBUG = %s; want the debug tasks, case-insensitively", got)
	}
	hidden := run(t, h, TasksParams{IncludeHidden: true, Contains: "Secret"})
	if len(hidden.Tasks) != 1 || hidden.Tasks[0].Path != ":app:internalSecret" {
		t.Errorf("includeHidden with contains Secret = %+v; want the hidden task", hidden.Tasks)
	}
}

// TestTasksUnknownModuleIsInvalidParams pins that a module argument naming no project
// fails REQUEST_INVALID_PARAMS with the known paths, rather than returning no tasks.
func TestTasksUnknownModuleIsInvalidParams(t *testing.T) {
	h := handler(t, fakeClient{model: fixture()})
	_, err := h(context.Background(), &capability.Call{Authorized: "/w/dc", Params: TasksParams{Module: ":nope"}})
	e, ok := err.(*capability.Error)
	if !ok || e.Code != capability.ErrInvalidParams {
		t.Fatalf("an unknown module gave %v, want REQUEST_INVALID_PARAMS", err)
	}
	if got, _ := e.Details["modules"].([]string); len(got) != 2 {
		t.Errorf("the error's details list %v; want the two known modules", e.Details)
	}
}
