package build

import (
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle/gradletest"
)

// The step 3 checks (plan m1h step 3, (a)) over the fake sidecar (D16): the flavoured
// model — :app's eight variants from the probe, :lib's debug and release — drives
// build.assemble's validation (D9), the output search (D9) and the AGP/KGP and
// expectedSources in the record (D10).

// eightVariants is :app's probed variants, flavours in dimension order (spike S3 G3).
func eightVariants() []gradletest.Variant {
	var out []gradletest.Variant
	for _, tier := range []string{"free", "paid"} {
		for _, stage := range []string{"prod", "staging"} {
			for _, bt := range []string{"debug", "release"} {
				out = append(out, gradletest.Variant{
					Name: tier + gradle.ExportVariant(stage) + gradle.ExportVariant(bt), BuildType: bt,
					Flavors: []gradletest.Flavor{{Dimension: "tier", Name: tier}, {Dimension: "stage", Name: stage}},
				})
			}
		}
	}
	return out
}

// flavouredModel is the module shape the fixture builds: :app (application tasks per
// variant), :lib (a library with no package task), and a root project.
func flavouredModel(appBuildDir string) gradle.Model {
	app := gradle.Module{Path: ":app", Name: "app", Dir: "app", BuildDir: appBuildDir}
	for _, v := range eightVariants() {
		stem := gradle.ExportVariant(v.Name)
		app.Tasks = append(app.Tasks,
			gradle.Task{Name: "assemble" + stem, Path: ":app:assemble" + stem},
			gradle.Task{Name: "package" + stem, Path: ":app:package" + stem})
	}
	lib := gradle.Module{Path: ":lib", Name: "lib", Dir: "lib", Tasks: []gradle.Task{
		{Name: "assembleDebug", Path: ":lib:assembleDebug"},
		{Name: "assembleRelease", Path: ":lib:assembleRelease"},
	}}
	return gradle.Model{
		Root:     "/w",
		Projects: []gradle.Module{{Path: ":", Name: "flavoured", Dir: "."}, app, lib},
	}
}

// flavouredConfig is the fake's script: the model above, the probe's per-module facts
// and, when build is non-nil, a build script.
func flavouredConfig(appBuildDir string, build *gradletest.Build) gradletest.Config {
	return gradletest.Config{
		Model: gradletest.MustModel(flavouredModel(appBuildDir)),
		Probe: &gradletest.Probe{
			Status: "ok", AGP: "8.13.2", KGP: "2.0.21",
			Android: map[string]gradletest.Android{
				":app": {CompileTarget: "android-36", BuildTools: "35.0.0", Variants: eightVariants()},
				":lib": {CompileTarget: "android-36", BuildTools: "35.0.0", Variants: []gradletest.Variant{
					{Name: "debug", BuildType: "debug"}, {Name: "release", BuildType: "release"},
				}},
			},
		},
		Build: build,
	}
}

// packagedBuild is a build that ran :app:packagePaidStagingDebug and ended successfully.
func packagedBuild() *gradletest.Build {
	return &gradletest.Build{Steps: []gradletest.Step{
		{Kind: "output", Stream: "stdout", Lines: []string{"> Task :app:packagePaidStagingDebug", "BUILD SUCCESSFUL in 1s"}},
		{Kind: "taskFinished", Path: ":app:packagePaidStagingDebug", Outcome: "success", DurationMs: 9},
	}}
}

// Decision (plan m1h D8, D9, A3, A4): a flavoured variant the probe named is a valid
// build.assemble target; the build produces its output from step 1's G8 directory
// (outputs/apk/<flavours>/<buildType>/); and the record carries the probe's AGP and KGP.
// Seen failing with the fallback used despite the probe and with outputs mapped by task
// names.
func TestAssembleFlavouredVariantFromTheProbe(t *testing.T) {
	buildDir := t.TempDir()
	writeAPK(t, buildDir, "paidStaging/debug", `{"version":3,"applicationId":"dev.droidyard.flavoured.paid.staging","variantName":"paidStagingDebug","elements":[{"outputFile":"app-paid-staging-debug.apk","filters":[],"versionCode":1,"versionName":"1.0"}]}`)
	f := newFixture(t, flavouredConfig(buildDir, packagedBuild()))

	res := mustResult(t, f.wait(t, f.start(t, "build.assemble", `{"module":":app","variant":"paidStagingDebug"}`)))
	if res.Outcome != "success" {
		t.Fatalf("outcome is %q, want success (%+v)", res.Outcome, res.Errors)
	}
	if len(res.Outputs) != 1 || res.Outputs[0].Variant != "paidStagingDebug" {
		t.Fatalf("outputs are %+v, want one for variant paidStagingDebug", res.Outputs)
	}
	if got := res.Outputs[0].ApplicationID; got != "dev.droidyard.flavoured.paid.staging" {
		t.Errorf("applicationId is %q, want dev.droidyard.flavoured.paid.staging", got)
	}
	if res.AGP != "8.13.2" || res.KGP != "2.0.21" {
		t.Errorf("agp/kgp are %q/%q, want 8.13.2/2.0.21 from the probe", res.AGP, res.KGP)
	}
	if res.ExpectedSources.Kotlin != "console" || res.ExpectedSources.Javac != "console" {
		t.Errorf("expectedSources are %+v, want console/console for KGP 2.0.21 without problemLocations", res.ExpectedSources)
	}
}

// Decision (plan m1h D9, A3): an aggregate name the fallback cannot see is not a
// variant; build.assemble refuses it listing the probe's eight. Seen failing when the
// probe's exact set is not used for validation.
func TestAssembleRejectsAnAggregateName(t *testing.T) {
	buildDir := t.TempDir()
	f := newFixture(t, flavouredConfig(buildDir, packagedBuild()))
	out := f.call(t, "build.assemble", `{"module":":app","variant":"paidStaging"}`)
	if code := codeOf(t, out.Error); code != capability.ErrInvalidParams {
		t.Fatalf("code is %s, want REQUEST_INVALID_PARAMS", code)
	}
	variants, ok := out.Error.Details["variants"].([]string)
	if !ok || len(variants) != 8 {
		t.Fatalf("details are %+v, want the probe's eight", out.Error.Details)
	}
	if !containsString(variants, "paidStagingDebug") || containsString(variants, "paidStaging") {
		t.Errorf("candidates are %v, want the eight exact names", variants)
	}
}

// Decision (plan m1h D9, A3): the output search follows the probe's variant set, not
// task names. A probe variant whose assemble task the fallback cannot pair still
// yields its output. Seen failing when resolveOutputs maps by task names.
func TestOutputFollowsTheProbeVariantNotTheTaskNames(t *testing.T) {
	buildDir := t.TempDir()
	// :app has the package task but no assemble counterpart, so the task-name heuristic
	// sees no variants at all; the probe names paidStagingDebug.
	model := gradle.Model{Root: "/w", Projects: []gradle.Module{
		{Path: ":app", Name: "app", Dir: "app", BuildDir: buildDir, Tasks: []gradle.Task{
			{Name: "packagePaidStagingDebug", Path: ":app:packagePaidStagingDebug"},
		}},
	}}
	cfg := gradletest.Config{
		Model: gradletest.MustModel(model),
		Probe: &gradletest.Probe{Status: "ok", AGP: "8.13.2", KGP: "2.0.21", Android: map[string]gradletest.Android{
			":app": {Variants: []gradletest.Variant{{Name: "paidStagingDebug", BuildType: "debug",
				Flavors: []gradletest.Flavor{{Dimension: "tier", Name: "paid"}, {Dimension: "stage", Name: "staging"}}}}},
		}},
		Build: &gradletest.Build{Steps: []gradletest.Step{
			{Kind: "taskFinished", Path: ":app:packagePaidStagingDebug", Outcome: "success"},
		}},
	}
	writeAPK(t, buildDir, "paidStaging/debug", `{"version":3,"applicationId":"dev.droidyard.flavoured.paid.staging","variantName":"paidStagingDebug","elements":[{"outputFile":"app-paid-staging-debug.apk","filters":[]}]}`)
	f := newFixture(t, cfg)

	res := mustResult(t, f.wait(t, f.start(t, "build.assemble", `{"module":":app","variant":"paidStagingDebug"}`)))
	if len(res.Outputs) != 1 || res.Outputs[0].Variant != "paidStagingDebug" {
		t.Fatalf("outputs are %+v, want one for the probe variant paidStagingDebug", res.Outputs)
	}
}

// Decision (plan m1h D9, A3): the probe's library variants make build.assemble of a
// library valid; it builds the AAR and reports no outputs (it has no package task).
func TestAssembleLibraryVariantFromTheProbe(t *testing.T) {
	buildDir := t.TempDir()
	f := newFixture(t, flavouredConfig(buildDir, &gradletest.Build{Steps: []gradletest.Step{
		{Kind: "output", Stream: "stdout", Lines: []string{"> Task :lib:assembleDebug", "BUILD SUCCESSFUL in 1s"}},
	}}))
	res := mustResult(t, f.wait(t, f.start(t, "build.assemble", `{"module":":lib","variant":"debug"}`)))
	if res.Outcome != "success" {
		t.Fatalf("outcome is %q, want success", res.Outcome)
	}
	if len(res.Outputs) != 0 {
		t.Errorf("outputs are %+v, want none for a library", res.Outputs)
	}
}

// Decision (plan m1h D10, A4): with a failed probe the record's agp and kgp are
// "unknown", and they survive a restart in build.lastResult. Seen failing with the
// values not persisted.
func TestPluginVersionsUnknownWhenTheProbeFailedAndPersisted(t *testing.T) {
	buildDir := t.TempDir()
	state := t.TempDir()
	cfg := flavouredConfig(buildDir, &gradletest.Build{Steps: []gradletest.Step{
		{Kind: "output", Stream: "stdout", Lines: []string{"> Task :app:assembleDebug", "BUILD SUCCESSFUL in 1s"}},
	}})
	cfg.Probe = &gradletest.Probe{Status: "failed"}

	f := newFixtureAt(t, state, cfg)
	res := mustResult(t, f.wait(t, f.start(t, "build.assemble", `{"module":":app","variant":"freeProdDebug"}`)))
	if res.AGP != "unknown" || res.KGP != "unknown" {
		t.Fatalf("agp/kgp are %q/%q, want unknown with the probe failed", res.AGP, res.KGP)
	}

	// A second daemon over the same state reads the persisted record (A4).
	second := newFixtureAt(t, state, cfg)
	last := second.call(t, "build.lastResult", "")
	got, ok := last.Result.(Result)
	if !ok {
		t.Fatalf("lastResult is %T, want Result", last.Result)
	}
	if got.AGP != "unknown" || got.KGP != "unknown" {
		t.Errorf("after a restart agp/kgp are %q/%q, want unknown", got.AGP, got.KGP)
	}
	if got.ExpectedSources == nil || res.ExpectedSources == nil || *got.ExpectedSources != *res.ExpectedSources {
		t.Errorf("after a restart expectedSources are %+v, want the recorded %+v", got.ExpectedSources, res.ExpectedSources)
	}
}

// Decision (plan m1h D10, A4; gradle.md §Version detection item 4): expectedSources is
// a pure function of the connection's problemLocations capability and the KGP version —
// Kotlin is structured only with problemLocations and KGP ≥ 2.4, an unknown KGP is
// below, and the other four tools are console. Seen failing with KGP ignored.
func TestExpectedSourcesTable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		locations bool
		kgp       string
		kotlin    string
		javac     string
	}{
		{"KGP 2.0.21 with locations", true, "2.0.21", "console", "problems"},
		{"KGP 2.4.20 with locations", true, "2.4.20", "problems", "problems"},
		{"KGP 2.4.20 without locations", false, "2.4.20", "console", "console"},
		{"unknown KGP with locations", true, "unknown", "console", "problems"},
		{"KGP 2.0.21 without locations", false, "2.0.21", "console", "console"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := gradle.Connection{Capabilities: map[string]bool{"problemLocations": tc.locations}}
			es := expectedSources(conn, gradle.Plugins{KGP: tc.kgp})
			if es.Kotlin != tc.kotlin {
				t.Errorf("kotlin is %q, want %q", es.Kotlin, tc.kotlin)
			}
			if es.Javac != tc.javac {
				t.Errorf("javac is %q, want %q", es.Javac, tc.javac)
			}
			for tool, got := range map[string]string{"aapt2": es.AAPT2, "manifestMerger": es.ManifestMerger, "ksp": es.KSP, "kapt": es.Kapt} {
				if got != "console" {
					t.Errorf("%s is %q, want console", tool, got)
				}
			}
		})
	}
}

// Decision (plan m1h D10): javac and Kotlin follow problemLocations and the KGP
// threshold end to end — a probed build at KGP 2.4.20 on a connection with
// problemLocations records kotlin: problems.
func TestExpectedSourcesInABuildResult(t *testing.T) {
	buildDir := t.TempDir()
	cfg := flavouredConfig(buildDir, &gradletest.Build{Steps: []gradletest.Step{
		{Kind: "output", Stream: "stdout", Lines: []string{"> Task :app:assembleDebug", "BUILD SUCCESSFUL in 1s"}},
	}})
	cfg.Capabilities = map[string]bool{"problemEvents": true, "problemLocations": true}
	cfg.Probe.KGP = "2.4.20"
	f := newFixture(t, cfg)

	res := mustResult(t, f.wait(t, f.start(t, "build.assemble", `{"module":":app","variant":"paidStagingDebug"}`)))
	if res.ExpectedSources.Kotlin != "problems" || res.ExpectedSources.Javac != "problems" {
		t.Errorf("expectedSources are %+v, want problems/problems for KGP 2.4.20 with problemLocations", res.ExpectedSources)
	}
	if res.KGP != "2.4.20" {
		t.Errorf("kgp is %q, want 2.4.20", res.KGP)
	}
}
