package sdk

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
)

// fakeClient is the project Client sdk.requirements' handler sees: a fixed model.
type fakeClient struct {
	model gradle.Model
	err   error
}

func (f fakeClient) Model(context.Context, string) (gradle.Model, bool, error) {
	return f.model, false, f.err
}

// runRequirements calls sdk.requirements' registered handler with Authorize already run
// (the root in Call.Authorized, as the scheduler leaves it) over the given model.
func runRequirements(t *testing.T, o Options, model gradle.Model, root string) RequirementsResult {
	t.Helper()
	o.Client = fakeClient{model: model}
	reg := capability.NewRegistry()
	Register(reg, o)
	d, ok := reg.Lookup("sdk.requirements")
	if !ok {
		t.Fatal("sdk.requirements is not registered")
	}
	got, err := d.Handler(context.Background(), &capability.Call{Authorized: root, Params: None{}})
	if err != nil {
		t.Fatalf("sdk.requirements: %v", err)
	}
	return got.(RequirementsResult)
}

// androidModel is a model whose probe read one application module's facts: a compile
// target, a build-tools version and the SDK directory AGP resolved. An empty value leaves
// that field unread (a nil pointer), which is D6's isolated-failure shape.
func androidModel(compileTarget, buildTools, sdkDir string) gradle.Model {
	a := gradle.Android{}
	if compileTarget != "" {
		a.CompileTarget = &compileTarget
	}
	if buildTools != "" {
		a.BuildTools = &buildTools
	}
	if sdkDir != "" {
		a.SDKDir = &sdkDir
	}
	return gradle.Model{
		Root:    "/w",
		Plugins: gradle.Plugins{AGP: "8.13.2", KGP: "2.0.21"},
		Probe:   gradle.Probe{Status: "ok"},
		Projects: []gradle.Module{
			{Path: ":", Name: "root", Dir: "."},
			{Path: ":app", Name: "app", Dir: "app", Android: &a},
		},
	}
}

// writeLocalProperties writes root/local.properties with the given text.
func writeLocalProperties(t *testing.T, root, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "local.properties"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func missing(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func anyNote(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// Decision (plan m1h D12, A6; sdk.md §Capabilities): when the project SDK has everything
// the probe's modules need, requirements is complete, lists nothing missing, and reports
// the project SDK as AGP's own directory (source agp, exact), the same as the host SDK.
// Seen failing when the host SDK is preferred over the probe's directory.
func TestRequirementsEverythingInstalled(t *testing.T) {
	sdkRoot := t.TempDir()
	writePackage(t, filepath.Join(sdkRoot, "platforms", "android-36", "package.xml"), "platforms;android-36", "2")
	writePackage(t, filepath.Join(sdkRoot, "build-tools", "35.0.0", "package.xml"), "build-tools;35.0.0", "35", "0", "0")

	res := runRequirements(t, Options{Env: []string{"ANDROID_HOME=" + sdkRoot}}, androidModel("android-36", "35.0.0", sdkRoot), "/w")
	if res.Source != SourceProbe || !res.Complete {
		t.Errorf("source/complete = %q/%v, want probe/true", res.Source, res.Complete)
	}
	if len(res.Missing) != 0 {
		t.Errorf("missing = %v, want none", res.Missing)
	}
	if !res.ProjectSDK.Found || res.ProjectSDK.Source != SourceAGP || !res.ProjectSDK.Exact || res.ProjectSDK.Root != sdkRoot {
		t.Errorf("projectSdk = %+v, want found, source agp, exact, root %q", res.ProjectSDK, sdkRoot)
	}
	if !res.ProjectSDK.SameAsHost {
		t.Errorf("sameAsHost is false; want true (the probe's directory is ANDROID_HOME)")
	}
	if len(res.Required) != 2 || res.Required[0].Package != "platforms;android-36" || res.Required[1].Package != "build-tools;35.0.0" {
		t.Fatalf("required = %+v, want the platform then build-tools", res.Required)
	}
	if !res.Required[0].Installed || !res.Required[1].Installed {
		t.Errorf("required = %+v, want both installed", res.Required)
	}
}

// Decision (plan m1h D12, A6): a required platform the project SDK lacks is in missing,
// and a note names the sdkmanager command that would install it into the project SDK's
// root; the build-tools that are installed are not.
func TestRequirementsNamesAMissingPlatform(t *testing.T) {
	sdkRoot := t.TempDir()
	writePackage(t, filepath.Join(sdkRoot, "platforms", "android-36", "package.xml"), "platforms;android-36", "2")
	writePackage(t, filepath.Join(sdkRoot, "build-tools", "35.0.0", "package.xml"), "build-tools;35.0.0", "35", "0", "0")

	res := runRequirements(t, Options{Env: []string{"ANDROID_HOME=" + sdkRoot}}, androidModel("android-33", "35.0.0", sdkRoot), "/w")
	if !res.Complete {
		t.Errorf("complete is false; want true (both fields were read)")
	}
	if !missing(res.Missing, "platforms;android-33") {
		t.Errorf("missing = %v, want platforms;android-33", res.Missing)
	}
	if missing(res.Missing, "build-tools;35.0.0") {
		t.Errorf("missing = %v, want the installed build-tools left out", res.Missing)
	}
	wantNote := `sdkmanager --sdk_root=` + sdkRoot + ` "platforms;android-33"`
	if !anyNote(res.Notes, wantNote) {
		t.Errorf("notes = %v, want one naming %q", res.Notes, wantNote)
	}
}

// Decision (plan m1h step 1 §Changes, D12; G4): the compile target android-37 is
// satisfied by an installed platforms;android-37.0 (a current SDK installs a minor
// version while AGP reports the target without it), and the required package keeps the
// compile target's spelling. Seen failing with the matcher removed.
func TestRequirementsMatchesTheCompileTargetToAMinorVersionedPlatform(t *testing.T) {
	sdkRoot := t.TempDir()
	writePackage(t, filepath.Join(sdkRoot, "platforms", "android-37.0", "package.xml"), "platforms;android-37.0", "2")
	writePackage(t, filepath.Join(sdkRoot, "build-tools", "37.0.0", "package.xml"), "build-tools;37.0.0", "37", "0", "0")

	res := runRequirements(t, Options{Env: []string{"ANDROID_HOME=" + sdkRoot}}, androidModel("android-37", "37.0.0", sdkRoot), "/w")
	if len(res.Missing) != 0 {
		t.Errorf("missing = %v, want none (android-37 is installed as android-37.0)", res.Missing)
	}
	if res.Required[0].Package != "platforms;android-37" {
		t.Errorf("required[0] = %q, want the compile target's spelling platforms;android-37", res.Required[0].Package)
	}
	if !res.Required[0].Installed {
		t.Errorf("required[0] is not installed; want android-37.0 to satisfy android-37")
	}
}

// Decision (plan m1h D12; sdk.md §Project SDK): with no probe directory, the project SDK
// is local.properties' sdk.dir — Java properties syntax (an escaped colon) and a relative
// path resolved against the root — before the environment.
func TestProjectSDKFromLocalProperties(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		root string // the expected project SDK root, computed from the project root
	}{
		{"a relative path", "sdk.dir=../shared-sdk\n", ""}, // set below
		{"an escaped colon", "sdk.dir=/opt/a\\:b/sdk\n", "/opt/a:b/sdk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := t.TempDir()
			want := tc.root
			if want == "" {
				want = filepath.Clean(filepath.Join(project, "../shared-sdk"))
			}
			writeLocalProperties(t, project, tc.text)

			res := runRequirements(t, Options{}, androidModel("android-36", "35.0.0", ""), project)
			if !res.ProjectSDK.Found || res.ProjectSDK.Source != SourceLocalProperties || res.ProjectSDK.Exact {
				t.Fatalf("projectSdk = %+v, want found, source local.properties, not exact", res.ProjectSDK)
			}
			if res.ProjectSDK.Root != want {
				t.Errorf("projectSdk.root = %q, want %q", res.ProjectSDK.Root, want)
			}
		})
	}
}

// Decision (plan m1h D12; sdk.md §Project SDK, §Failure modes): with no probe directory
// and no local.properties, ANDROID_HOME (then ANDROID_SDK_ROOT) decides, source env and
// inexact. Seen failing when the probe's directory is not preferred (the flip side).
func TestProjectSDKFromEnvWhenNoProbeDirectory(t *testing.T) {
	envSDK := t.TempDir()
	res := runRequirements(t, Options{Env: []string{"ANDROID_HOME=" + envSDK}}, androidModel("android-36", "35.0.0", ""), t.TempDir())
	if !res.ProjectSDK.Found || res.ProjectSDK.Source != SourceEnv || res.ProjectSDK.Root != envSDK || res.ProjectSDK.Exact {
		t.Errorf("projectSdk = %+v, want found, source env, root %q, not exact", res.ProjectSDK, envSDK)
	}
}

// Decision (plan m1h D12; sdk.md §Project SDK step 3, §Failure modes): nothing resolves
// the project SDK, so found is false and a note names the "SDK location not found"
// failure the build will hit; the requirements the probe read are still listed, and every
// one is missing because there is no SDK to hold them.
func TestProjectSDKNotFound(t *testing.T) {
	res := runRequirements(t, Options{}, androidModel("android-36", "35.0.0", ""), t.TempDir())
	if res.ProjectSDK.Found {
		t.Errorf("projectSdk = %+v, want found false", res.ProjectSDK)
	}
	if !anyNote(res.Notes, "SDK location not found") {
		t.Errorf("notes = %v, want one naming the SDK location failure", res.Notes)
	}
	if len(res.Required) != 2 || len(res.Missing) != 2 {
		t.Errorf("required/missing = %v/%v, want the probe's two, all missing with no SDK", res.Required, res.Missing)
	}
}

// Decision (plan m1h D12): sameAsHost canonicalises both roots, so a symlinked project
// directory that points at the host SDK is the same SDK, and two different directories are
// not. Seen failing when the symlink is compared unresolved.
func TestSameAsHostThroughASymlink(t *testing.T) {
	real := mkSDK(t, t.TempDir())
	link := filepath.Join(t.TempDir(), "sdk-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	same := runRequirements(t, Options{Env: []string{"ANDROID_HOME=" + real}}, androidModel("", "", link), "/w")
	if !same.ProjectSDK.SameAsHost {
		t.Errorf("sameAsHost is false for a symlink to the host SDK; want true (root %q, host %q)", same.ProjectSDK.Root, real)
	}

	other := mkSDK(t, t.TempDir())
	different := runRequirements(t, Options{Env: []string{"ANDROID_HOME=" + other}}, androidModel("", "", real), "/w")
	if different.ProjectSDK.SameAsHost {
		t.Errorf("sameAsHost is true for two different SDKs (%q and %q); want false", real, other)
	}
}

// Decision (plan m1h D12; gradle.md §SDK requirements (build side)): a probe that read
// nothing yields source failure-signatures, complete false and no requirements, rather
// than an empty check presented as a complete one; and the probe's own directory is not
// used when the probe failed.
func TestRequirementsProbeFailed(t *testing.T) {
	m := androidModel("android-36", "35.0.0", "/w/from-the-probe")
	m.Probe = gradle.Probe{Status: "failed"}
	res := runRequirements(t, Options{}, m, "/w")
	if res.Source != SourceFailures || res.Complete {
		t.Errorf("source/complete = %q/%v, want failure-signatures/false", res.Source, res.Complete)
	}
	if len(res.Required) != 0 || len(res.Missing) != 0 {
		t.Errorf("required/missing = %v/%v, want empty", res.Required, res.Missing)
	}
	if len(res.Notes) == 0 {
		t.Error("no note explains that the probe read nothing")
	}
	if res.ProjectSDK.Found {
		t.Errorf("projectSdk = %+v, want found false: a failed probe's directory is not used", res.ProjectSDK)
	}
}

// Decision (plan m1h D12, R2; capability-api.md §Classification): sdk.requirements is a
// read-only, idempotent, open-world workspace Query that runs a configuration pass
// (long-running, [gradle]) and carries the trust hook, and its declared error codes are
// the trust hook's plus the model fetch's.
func TestRequirementsDescriptorClassification(t *testing.T) {
	reg := capability.NewRegistry()
	Register(reg, Options{Authorize: func(context.Context, *capability.Call) (any, error) { return "/x", nil }})
	d, ok := reg.Lookup("sdk.requirements")
	if !ok {
		t.Fatal("sdk.requirements is not registered")
	}
	if d.Kind != capability.Query || d.Scope != capability.Workspace || !d.LongRunning {
		t.Errorf("sdk.requirements is %v/%v/longRunning=%v, want Query/Workspace/true", d.Kind, d.Scope, d.LongRunning)
	}
	if len(d.Availability) != 1 || d.Availability[0] != capability.RequiresGradle {
		t.Errorf("availability = %v, want [gradle]", d.Availability)
	}
	if d.Authorize == nil {
		t.Error("sdk.requirements has no trust hook")
	}
	a := d.ResolveAnnotations()
	if !a.ReadOnly || !a.Idempotent || a.Destructive || !a.OpenWorld {
		t.Errorf("annotations = %+v, want read-only, idempotent, not destructive, open-world", a)
	}
	for _, code := range []capability.ErrorCode{
		capability.ErrWorkspaceUntrusted, capability.ErrBuildSidecarUnavailable, capability.ErrBuildSidecarCrashed,
		capability.ErrBuildGradleUnsupported, capability.ErrProjectModelFailed,
	} {
		if !hasCode(d.Errors, code) {
			t.Errorf("errors %v lack %s", d.Errors, code)
		}
	}
}

func hasCode(codes []capability.ErrorCode, want capability.ErrorCode) bool {
	for _, c := range codes {
		if c == want {
			return true
		}
	}
	return false
}

// Decision (step 3 review B1, applied here): under a partial probe a module whose read
// failed has no Android facts and is skipped, so the answer is never complete. Seen
// failing when partial counted as complete.
func TestRequirementsPartialProbeIsNotComplete(t *testing.T) {
	m := androidModel("android-36", "35.0.0", "/w/from-the-probe")
	m.Projects = append(m.Projects, gradle.Module{Path: ":other", Name: "other", Dir: "other"})
	m.Probe = gradle.Probe{Status: "partial", Notes: []string{":other: probe: failed"}}
	r := runRequirements(t, Options{}, m, "/w")
	if r.Source != SourceProbe || r.Complete {
		t.Fatalf("a partial probe: source %q complete %v, want probe and incomplete", r.Source, r.Complete)
	}
}
