package gradle_test

import (
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
)

// flavoured is :app's probed variants in the order AGP reports them, with the flavours
// in dimension order (tier then stage), and a :lib module with debug and release
// (spike S3 G3).
func flavouredAndroid() *gradle.Android {
	names := []string{
		"freeProdDebug", "freeProdRelease", "freeStagingDebug", "freeStagingRelease",
		"paidProdDebug", "paidProdRelease", "paidStagingDebug", "paidStagingRelease",
	}
	var variants []gradle.Variant
	for _, name := range names {
		buildType := "debug"
		if strings.HasSuffix(name, "Release") {
			buildType = "release"
		}
		tier, stage := "free", "prod"
		if strings.HasPrefix(name, "paid") {
			tier = "paid"
		}
		if strings.Contains(name, "Staging") {
			stage = "staging"
		}
		variants = append(variants, gradle.Variant{
			Name: name, BuildType: buildType,
			Flavors: []gradle.Flavor{{Dimension: "tier", Name: tier}, {Dimension: "stage", Name: stage}},
		})
	}
	return &gradle.Android{CompileTarget: strptr("android-36"), BuildTools: strptr("35.0.0"), Variants: variants}
}

func strptr(s string) *string { return &s }

// appModule is a module the task-name fallback would also resolve, so a test can tell
// which source produced the names.
func appModule(android *gradle.Android) gradle.Module {
	m := gradle.Module{Path: ":app", Name: "app", Dir: "app", Android: android}
	for _, name := range []string{"Debug", "Release"} {
		m.Tasks = append(m.Tasks,
			gradle.Task{Name: "assemble" + name, Path: ":app:assemble" + name},
			gradle.Task{Name: "package" + name, Path: ":app:package" + name})
	}
	return m
}

// Decision (plan m1h D8): the one variant source is the probe's when it read the
// project, and the task-name fallback otherwise. A module the probe read carries
// source probe and its details; a project the probe could not read, a failed probe and
// an absent probe all fall back to task names. Seen failing with the fallback used
// despite the probe.
func TestVariantsSourcePerModule(t *testing.T) {
	probe := gradle.Probe{Status: "ok"}

	// The probe read :app's variants: exact names and details, not the fallback's.
	names, details, source := gradle.Variants(appModule(flavouredAndroid()), probe)
	if source != "probe" {
		t.Errorf("source is %q, want probe", source)
	}
	if len(names) != 8 || names[6] != "paidStagingDebug" {
		t.Errorf("names are %v, want the probe's eight in order", names)
	}
	if len(details) != 8 || details[6].BuildType != "debug" {
		t.Fatalf("details are %+v, want eight with paidStagingDebug/debug", details)
	}
	if fl := details[6].Flavors; len(fl) != 2 || fl[0] != (gradle.Flavor{Dimension: "tier", Name: "paid"}) || fl[1] != (gradle.Flavor{Dimension: "stage", Name: "staging"}) {
		t.Errorf("paidStagingDebug flavours are %+v, want tier:paid then stage:staging", fl)
	}

	// A non-Android project the probe read: no variants, still exact.
	names, details, source = gradle.Variants(gradle.Module{Path: ":"}, probe)
	if source != "probe" || names == nil || len(names) != 0 || details != nil {
		t.Errorf("a non-Android project is %v/%v/%v, want probe with an empty list", names, details, source)
	}

	// An Android project whose variants field the probe could not read: fallback for
	// that module alone (D8).
	unread := &gradle.Android{CompileTarget: strptr("android-36")}
	names, details, source = gradle.Variants(appModule(unread), gradle.Probe{Status: "partial"})
	if source != "task-names" || details != nil {
		t.Errorf("an Android project the probe could not read is %v/%v, want task-names with no details", source, details)
	}
	if strings.Join(names, ",") != "debug,release" {
		t.Errorf("fallback names are %v, want debug,release", names)
	}

	// A failed probe: every module falls back, even one whose reply carries android
	// facts (the failed reply has none, and the fallback must not read a stale one).
	names, _, source = gradle.Variants(appModule(flavouredAndroid()), gradle.Probe{Status: "failed"})
	if source != "task-names" {
		t.Errorf("a failed probe is %q, want task-names", source)
	}
	if strings.Join(names, ",") != "debug,release" {
		t.Errorf("names under a failed probe are %v, want the fallback's debug,release", names)
	}

	// No probe at all (a plain model): the fallback, as before.
	if _, _, source = gradle.Variants(appModule(nil), gradle.Probe{}); source != "task-names" {
		t.Errorf("an absent probe is %q, want task-names", source)
	}

	// A module with no variants at all still reports an empty, non-nil list.
	if names, _, _ = gradle.Variants(gradle.Module{Path: ":empty"}, probe); names == nil {
		t.Error("names is nil for a variantless module; the contract wants an empty list")
	}
}

// Decision (plan m1h D10): an unknown or below-2.4 KGP is below the structured-error
// threshold, and only 2.4 and newer is at or above it.
func TestKGPAtLeastThreshold(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"2.4.20", true},
		{"2.4", true},
		{"2.5.0", true},
		{"3.0.0", true},
		{"2.3.10", false},
		{"2.0.21", false},
		{"unknown", false},
		{"", false},
	} {
		if got := gradle.KGPAtLeast(tc.version, 2, 4); got != tc.want {
			t.Errorf("KGPAtLeast(%q, 2, 4) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

// Decision (plan m1h D8; step 3 review B1): under a partial probe, an Android project
// with no Android facts may be one the probe could not read, so it falls back to task
// names instead of being published as an exact, empty list. Seen failing when partial
// was treated as ok.
func TestPartialProbeFallsBackForAProjectItDidNotRead(t *testing.T) {
	names, _, source := gradle.Variants(appModule(nil), gradle.Probe{Status: "partial"})
	if source != "task-names" || len(names) == 0 {
		t.Fatalf("an Android project omitted from a partial probe is %v/%q, want its task-name variants", names, source)
	}
}
