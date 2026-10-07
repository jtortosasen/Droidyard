package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
)

// fakeClient is the sidecar client these handlers see: a fixed model and staleness.
type fakeClient struct {
	model gradle.Model
	stale bool
	err   error
}

func (f fakeClient) Model(context.Context, string) (gradle.Model, bool, error) {
	return f.model, f.stale, f.err
}

// handler returns the registered descriptor's handler, so the test exercises the
// capability as the scheduler invokes it (Authorize already ran, root in Authorized).
func handler(t *testing.T, reg *capability.Registry, name string) capability.Handler {
	t.Helper()
	d, ok := reg.Lookup(name)
	if !ok {
		t.Fatalf("%s is not registered", name)
	}
	return d.Handler
}

// recorded reads step 2's recorded projects and their task names, so the variants test
// runs over the real lists rather than a made-up model.
func recorded(t *testing.T) gradle.Model {
	t.Helper()
	base := filepath.Join("..", "build", "testdata", "s1run2")
	m := gradle.Model{Root: "/w/dc"}
	for _, path := range lines(t, filepath.Join(base, "projects.txt")) {
		name := path[strings.LastIndex(path, ":")+1:]
		if path == ":" {
			name = "root"
		}
		mod := gradle.Module{Path: path, Name: name, Dir: dir(path)}
		file := "tasks-" + strings.ReplaceAll(strings.TrimPrefix(path, ":"), ":", "-") + ".txt"
		for _, task := range lines(t, filepath.Join(base, file)) {
			mod.Tasks = append(mod.Tasks, gradle.Task{Name: task, Path: path + ":" + task, Group: "build", Public: true})
		}
		m.Projects = append(m.Projects, mod)
	}
	return m
}

// dir is Gradle's project directory for a path: ":" is ".", ":a:b" is "a/b".
func dir(path string) string {
	if path == ":" {
		return "."
	}
	return strings.ReplaceAll(strings.TrimPrefix(path, ":"), ":", "/")
}

func lines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func errCode(t *testing.T, err error) capability.ErrorCode {
	t.Helper()
	e, ok := err.(*capability.Error)
	if !ok {
		t.Fatalf("error %v is not a capability error", err)
	}
	return e.Code
}

// TestModulesListsEveryProjectAndPassesStaleThrough pins the shape of project.modules:
// path, name and dir for every project the model holds, buildDir dropped, and the
// client's staleness reported unchanged.
func TestModulesListsEveryProjectAndPassesStaleThrough(t *testing.T) {
	reg := capability.NewRegistry()
	Register(reg, fakeClient{model: recorded(t), stale: true}, nil)
	got, err := handler(t, reg, "project.modules")(context.Background(), &capability.Call{Authorized: "/w/dc", Params: None{}})
	if err != nil {
		t.Fatal(err)
	}
	res := got.(ModulesResult)
	if !res.Stale {
		t.Error("stale was not passed through")
	}
	if len(res.Modules) != 12 {
		t.Fatalf("project.modules returned %d modules, want the 12 recorded", len(res.Modules))
	}
	var paths []string
	for _, m := range res.Modules {
		paths = append(paths, m.Path)
		if m.Path == ":core:database" && (m.Name != "database" || m.Dir != "core/database") {
			t.Errorf(":core:database = %+v; want name database, dir core/database", m)
		}
	}
	for _, want := range []string{":", ":app", ":core", ":feature", ":feature:applist"} {
		if !strings.Contains(strings.Join(paths, ","), want) {
			t.Errorf("project.modules does not list %q: %v", want, paths)
		}
	}
}

// TestVariantsOverTheRecordedModel pins A3 and ADR-0023 §Decision 4 over step 2's real
// task lists: :app reports debug and release (lowercase, the AndroidTest variants
// excluded) with source task-names and exact false, and a library reports none.
func TestVariantsOverTheRecordedModel(t *testing.T) {
	reg := capability.NewRegistry()
	Register(reg, fakeClient{model: recorded(t)}, nil)
	got, err := handler(t, reg, "project.variants")(context.Background(), &capability.Call{Authorized: "/w/dc", Params: VariantsParams{}})
	if err != nil {
		t.Fatal(err)
	}
	byModule := map[string]ModuleVariants{}
	for _, m := range got.(VariantsResult).Modules {
		byModule[m.Module] = m
	}
	app := byModule[":app"]
	if strings.Join(app.Variants, ",") != "debug,release" {
		t.Errorf(":app variants = %v, want debug, release", app.Variants)
	}
	if app.Source != "task-names" || app.Exact {
		t.Errorf(":app = %+v; want source task-names, exact false", app)
	}
	if lib := byModule[":core:database"]; len(lib.Variants) != 0 {
		t.Errorf(":core:database variants = %v, want none (it packages no APK)", lib.Variants)
	}
}

// probedModel is a model whose probe read :app's Android variants and :lib as a
// non-Android project.
func probedModel() gradle.Model {
	app := gradle.Android{Variants: []gradle.Variant{
		{Name: "freeStagingDebug", BuildType: "debug", Flavors: []gradle.Flavor{{Dimension: "tier", Name: "free"}, {Dimension: "stage", Name: "staging"}}},
		{Name: "paidStagingDebug", BuildType: "debug", Flavors: []gradle.Flavor{{Dimension: "tier", Name: "paid"}, {Dimension: "stage", Name: "staging"}}},
	}}
	return gradle.Model{
		Root:    "/w/flavoured",
		Plugins: gradle.Plugins{AGP: "8.13.2", KGP: "2.0.21"},
		Probe:   gradle.Probe{Status: "ok"},
		Projects: []gradle.Module{
			{Path: ":app", Name: "app", Dir: "app", Android: &app},
			{Path: ":lib", Name: "lib", Dir: "lib"},
		},
	}
}

// Decision (plan m1h D8, A3): when the probe read a project, project.variants reports
// source probe, exact true and the details, in dimension order; a non-Android project
// the probe read is exactly empty. Seen failing with the details dropped.
func TestVariantsFromTheProbe(t *testing.T) {
	reg := capability.NewRegistry()
	Register(reg, fakeClient{model: probedModel()}, nil)
	got, err := handler(t, reg, "project.variants")(context.Background(), &capability.Call{Authorized: "/w/flavoured", Params: VariantsParams{}})
	if err != nil {
		t.Fatal(err)
	}
	byModule := map[string]ModuleVariants{}
	for _, m := range got.(VariantsResult).Modules {
		byModule[m.Module] = m
	}
	app := byModule[":app"]
	if app.Source != "probe" || !app.Exact {
		t.Errorf(":app = %+v; want source probe, exact true", app)
	}
	if strings.Join(app.Variants, ",") != "freeStagingDebug,paidStagingDebug" {
		t.Errorf(":app variants = %v, want the probe's two", app.Variants)
	}
	if len(app.Details) != 2 || app.Details[1].Name != "paidStagingDebug" || app.Details[1].BuildType != "debug" {
		t.Fatalf(":app details = %+v, want two with paidStagingDebug/debug", app.Details)
	}
	if fl := app.Details[1].Flavors; len(fl) != 2 || fl[0].Dimension != "tier" || fl[0].Name != "paid" || fl[1].Dimension != "stage" {
		t.Errorf("paidStagingDebug details flavours = %+v, want tier then stage", fl)
	}

	lib := byModule[":lib"]
	if lib.Source != "probe" || !lib.Exact || len(lib.Variants) != 0 || lib.Details != nil {
		t.Errorf(":lib = %+v; want probe, exact, empty and no details", lib)
	}
}

// Decision (plan m1h D8): a failed probe publishes no details and falls back to task
// names for every module.
func TestVariantsFallBackWhenTheProbeFailed(t *testing.T) {
	m := probedModel()
	m.Probe = gradle.Probe{Status: "failed"}
	m.Plugins = gradle.Plugins{}
	reg := capability.NewRegistry()
	Register(reg, fakeClient{model: m}, nil)
	got, err := handler(t, reg, "project.variants")(context.Background(), &capability.Call{Authorized: "/w/flavoured", Params: VariantsParams{Module: ":app"}})
	if err != nil {
		t.Fatal(err)
	}
	app := got.(VariantsResult).Modules[0]
	if app.Source != "task-names" || app.Exact || app.Details != nil {
		t.Errorf(":app under a failed probe = %+v; want task-names, not exact, no details", app)
	}
}

// TestVariantsForOneModuleAndAnUnknownOne pins the module filter: naming one project
// narrows the result to it, and an unknown path is REQUEST_INVALID_PARAMS carrying the
// known paths, not an empty answer.
func TestVariantsForOneModuleAndAnUnknownOne(t *testing.T) {
	reg := capability.NewRegistry()
	Register(reg, fakeClient{model: recorded(t)}, nil)
	h := handler(t, reg, "project.variants")

	got, err := h(context.Background(), &capability.Call{Authorized: "/w/dc", Params: VariantsParams{Module: ":app"}})
	if err != nil {
		t.Fatal(err)
	}
	res := got.(VariantsResult)
	if len(res.Modules) != 1 || res.Modules[0].Module != ":app" {
		t.Fatalf("with module :app the result is %+v; want only :app", res.Modules)
	}

	_, err = h(context.Background(), &capability.Call{Authorized: "/w/dc", Params: VariantsParams{Module: ":nope"}})
	if code := errCode(t, err); code != capability.ErrInvalidParams {
		t.Fatalf("an unknown module gave %s, want REQUEST_INVALID_PARAMS", code)
	}
	e := err.(*capability.Error)
	paths, _ := e.Details["modules"].([]string)
	if len(paths) != 12 {
		t.Errorf("the error's details list %d modules, want the 12 known: %v", len(paths), e.Details)
	}
}
