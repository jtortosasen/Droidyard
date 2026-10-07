package sdk

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
)

// sdk.requirements (plan m1h D12; sdk.md §Project SDK, §Capabilities): what each Android
// module's build needs of the SDK, compared against the SDK that build would use. The
// project's needs come from the probe (gradle.md §SDK requirements (build side)); the
// project SDK is the directory AGP itself resolved, or AGP's order emulated from
// local.properties and the environment.

// Project SDK sources, in resolution order (sdk.md §Project SDK; D12). agp is exact: the
// probe reported the directory AGP resolved. The other two are the emulated order and
// inexact.
const (
	SourceAGP             = "agp"
	SourceLocalProperties = "local.properties"
	SourceEnv             = "env"
)

// Requirements sources (D12, gradle.md §SDK requirements (build side)): probe when the
// probe read the project's facts, failure-signatures when there was nothing to read.
const (
	SourceProbe     = "probe"
	SourceFailures  = "failure-signatures"
	androidSDKDir   = "sdk.dir"
	sdkLocationNote = `no project SDK could be resolved: the build will fail with "SDK location not found"; set sdk.dir in local.properties or ANDROID_HOME`
)

// requirementErrors are the codes sdk.requirements' handler can return (D12, schema): the
// trust hook's and the model fetch's, as in project.* (plan m1h step 4).
var requirementErrors = []capability.ErrorCode{
	capability.ErrWorkspaceUntrusted,
	capability.ErrBuildSidecarUnavailable,
	capability.ErrBuildSidecarCrashed,
	capability.ErrBuildGradleUnsupported,
	capability.ErrProjectModelFailed,
}

// ProjectSDK is which SDK the project's build would compile against (D12; sdk.md §Project
// SDK). Its root is published so a missing package's note can name the sdkmanager
// --sdk_root target (the only place this core-internal directory leaves the core).
type ProjectSDK struct {
	Found      bool   `json:"found" jsonschema:"required" jsonschema_description:"Whether a project SDK location was resolved."`
	Root       string `json:"root,omitempty" jsonschema_description:"The project SDK directory: the directory a missing package would be installed into (sdkmanager --sdk_root)."`
	Source     string `json:"source,omitempty" jsonschema:"enum=agp,enum=local.properties,enum=env" jsonschema_description:"How the directory was resolved: agp (the probe's own sdkDir, exact), local.properties (its sdk.dir) or env (ANDROID_HOME or ANDROID_SDK_ROOT, inexact)."`
	Exact      bool   `json:"exact" jsonschema_description:"True only for source agp: the directory AGP itself resolved."`
	SameAsHost bool   `json:"sameAsHost" jsonschema_description:"Whether the project SDK and the host SDK are the same directory, canonicalised (symlinks resolved)."`
}

// HostSDK is the host SDK, reported beside the project SDK (D12): the reason the emulator
// and the build can disagree about what is installed. Its own detail is sdk.status'.
type HostSDK struct {
	Found bool   `json:"found" jsonschema:"required" jsonschema_description:"Whether a host SDK was found (see sdk.status)."`
	Root  string `json:"root,omitempty" jsonschema_description:"The host SDK directory."`
}

// Required is one SDK package an Android module requires, and whether the project SDK has
// it (D12). A platform's package is platforms;<compile target>; build-tools' is
// build-tools;<version>.
type Required struct {
	Module    string `json:"module" jsonschema:"required" jsonschema_description:"The Gradle project path requiring the package, e.g. \":app\"."`
	Package   string `json:"package" jsonschema:"required" jsonschema_description:"The SDK package id, e.g. \"platforms;android-36\" or \"build-tools;35.0.0\"."`
	Installed bool   `json:"installed" jsonschema:"required" jsonschema_description:"Whether the project SDK has this package."`
}

// RequirementsResult is sdk.requirements' result (D12).
type RequirementsResult struct {
	Source   string `json:"source" jsonschema:"required,enum=probe,enum=failure-signatures" jsonschema_description:"Where the requirements came from: probe when the Android probe read the project's facts, failure-signatures when it read nothing and nothing could be checked."`
	Complete bool   `json:"complete" jsonschema_description:"True only when the probe read every Android module's compile target and build-tools."`
	// ProjectSDK is the SDK the build would use.
	ProjectSDK ProjectSDK `json:"projectSdk" jsonschema:"required"`
	// HostSDK is the SDK Droidyard's own tools use.
	HostSDK HostSDK `json:"hostSdk" jsonschema:"required"`
	// Required is what the Android modules need, one entry per package per module.
	Required []Required `json:"required" jsonschema:"required"`
	// Missing is the package ids the project SDK does not have, in Required's order.
	Missing []string `json:"missing" jsonschema:"required" jsonschema_description:"The required package ids the project SDK does not have, in required's order."`
	// Notes name an sdkmanager command per missing package, the SDK-location failure when
	// no project SDK resolves, a project SDK directory that does not exist, and why
	// nothing could be checked when the probe read nothing.
	Notes []string `json:"notes" jsonschema:"required"`
}

// registerRequirements adds sdk.requirements to the registry.
func registerRequirements(reg *capability.Registry, o Options) {
	reg.MustRegister(capability.Descriptor{
		Name:         "sdk.requirements",
		Kind:         capability.Query,
		Scope:        capability.Workspace,
		LongRunning:  true,
		Since:        "0.7.0",
		Availability: []capability.Requirement{capability.RequiresGradle},
		Authorize:    o.Authorize,
		Params:       None{},
		Result:       RequirementsResult{},
		Errors:       requirementErrors,
		Description: "Reports what this project's build requires of the SDK, compared against the SDK that build would use, without changing anything. " +
			"For each Android module the Android probe read, required lists platforms;<compile target> and build-tools;<version> and whether the project SDK has each; missing lists the package ids it lacks, and a note names `sdkmanager --sdk_root=<projectSdk.root> \"<package>\"` for each. " +
			"A compile target android-N is satisfied by an installed platforms;android-N or platforms;android-N.0. " +
			"projectSdk is the directory AGP resolved (source agp, exact), else local.properties sdk.dir (relative to the root, Java properties escapes; source local.properties), else ANDROID_HOME or ANDROID_SDK_ROOT (source env); sameAsHost says whether it is the host SDK, canonicalised. With none, projectSdk.found is false and a note names the \"SDK location not found\" failure the build will hit. " +
			"source is probe when the probe read the project's facts and failure-signatures otherwise (complete false, required empty, and a note), so an incomplete answer is never presented as a complete one. " +
			"Only a trusted root is answered: an untrusted one fails WORKSPACE_UNTRUSTED before any Gradle code runs. " +
			"Fails with BUILD_SIDECAR_UNAVAILABLE, BUILD_SIDECAR_CRASHED, BUILD_GRADLE_UNSUPPORTED and PROJECT_MODEL_FAILED as project.modules does.",
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			root := call.Authorized.(string)
			model, _, err := o.Client.Model(ctx, root)
			if err != nil {
				return nil, err
			}
			return Requirements(o, root, model), nil
		},
	})
}

// Requirements answers sdk.requirements for root and the model the probe read (D12). The
// needs come from the probe; the SDK is the probe's own directory, or AGP's order
// emulated; the comparison is against the SDK's package.xml files, never a JVM or
// sdkmanager. It reads the environment for the emulated order and the host SDK, and
// touches nothing but the file system.
func Requirements(o Options, root string, model gradle.Model) RequirementsResult {
	host := Host(o.Env)
	out := RequirementsResult{
		ProjectSDK: ProjectSDK{},
		HostSDK:    HostSDK{Found: host.Found, Root: host.Root},
		Required:   []Required{},
		Missing:    []string{},
		Notes:      []string{},
	}

	projectRoot, projectSource, found := resolveProjectSDK(o, root, model)
	var installed Installed
	if found {
		installed, _ = ReadInstalled(projectRoot)
		if !exists(projectRoot) {
			out.Notes = append(out.Notes, "the project SDK directory does not exist: "+projectRoot)
		}
	}
	out.ProjectSDK = ProjectSDK{
		Found:      found,
		Root:       projectRoot,
		Source:     projectSource,
		Exact:      projectSource == SourceAGP,
		SameAsHost: found && host.Found && canonical(o)(projectRoot) == canonical(o)(host.Root),
	}

	if model.Probe.Read() {
		out.Source = SourceProbe
		out.Complete = true
		for _, m := range model.Projects {
			if m.Android == nil {
				continue
			}
			if m.Android.CompileTarget != nil {
				pkg := "platforms;" + *m.Android.CompileTarget
				out.Required = append(out.Required, Required{
					Module: m.Path, Package: pkg,
					Installed: installedPlatform(installed.Platforms, *m.Android.CompileTarget),
				})
			} else {
				out.Complete = false
			}
			if m.Android.BuildTools != nil {
				pkg := "build-tools;" + *m.Android.BuildTools
				out.Required = append(out.Required, Required{
					Module: m.Path, Package: pkg,
					Installed: installedPackage(installed.BuildTools, pkg),
				})
			} else {
				out.Complete = false
			}
		}
	} else {
		// D12: without the probe's facts there is nothing to compare, so the answer says
		// so rather than presenting an empty check as a complete one (gradle.md §SDK
		// requirements (build side)).
		out.Source = SourceFailures
		out.Complete = false
		out.Notes = append(out.Notes, "the Android probe read no facts for this project (probe status: "+probeStatus(model.Probe)+"); the required SDK packages could not be determined")
	}

	for _, r := range out.Required {
		if r.Installed {
			continue
		}
		out.Missing = append(out.Missing, r.Package)
		if found {
			out.Notes = append(out.Notes, fmt.Sprintf("install %s with: sdkmanager --sdk_root=%s %q", r.Package, projectRoot, r.Package))
		}
	}
	if !found {
		out.Notes = append(out.Notes, sdkLocationNote)
	}
	return out
}

// resolveProjectSDK is sdk.md §Project SDK's order (D12): the probe's directory (agp,
// exact), else local.properties sdk.dir (relative to the root), else ANDROID_HOME, else
// ANDROID_SDK_ROOT from the daemon's environment (env, inexact). found is false when none
// resolves.
func resolveProjectSDK(o Options, root string, model gradle.Model) (dir, source string, found bool) {
	// The probe's own directory, from the first project in path order that reported one.
	// It may not exist (a wrong sdk.dir): it is still what AGP resolved, so it is reported
	// as such. Only a probe that read the facts is trusted for it.
	if model.Probe.Read() {
		for _, m := range model.Projects {
			if m.Android != nil && m.Android.SDKDir != nil && *m.Android.SDKDir != "" {
				return *m.Android.SDKDir, SourceAGP, true
			}
		}
	}
	if props, err := ReadLocalProperties(root); err == nil {
		if v := props[androidSDKDir]; v != "" {
			if !filepath.IsAbs(v) {
				v = filepath.Join(root, v)
			}
			return filepath.Clean(v), SourceLocalProperties, true
		}
	}
	e := envMap(o.Env)
	if v := e["ANDROID_HOME"]; v != "" {
		return filepath.Clean(v), SourceEnv, true
	}
	if v := e["ANDROID_SDK_ROOT"]; v != "" {
		return filepath.Clean(v), SourceEnv, true
	}
	return "", "", false
}

// installedPlatform reports whether the SDK has the platform a compile target names: a
// compile target android-N is satisfied by platforms;android-N or platforms;android-N.0,
// because a current SDK installs a minor-versioned platform directory while AGP reports
// the compile target without the minor (plan m1h step 1 §Changes: compileSdk 37 →
// android-37, installed as android-37.0).
func installedPlatform(platforms []Package, compileTarget string) bool {
	want := "platforms;" + compileTarget
	for _, p := range platforms {
		if p.ID == want || p.ID == want+".0" {
			return true
		}
	}
	return false
}

// installedPackage reports whether the SDK has the given package id exactly.
func installedPackage(pkgs []Package, id string) bool {
	for _, p := range pkgs {
		if p.ID == id {
			return true
		}
	}
	return false
}

// canonical is the canonicaliser sameAsHost compares through: the environment's when one
// is configured (so it agrees with how a workspace root is canonicalised), else the
// filesystem's, with a path that does not exist kept as cleaned.
func canonical(o Options) func(string) string {
	if o.Canonical != nil {
		return o.Canonical
	}
	return func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
}

// exists reports whether path is present on disk.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// probeStatus is the probe's status for a note, naming "none" when the reply carried no
// probe block at all.
func probeStatus(p gradle.Probe) string {
	if p.Status == "" {
		return "none"
	}
	return p.Status
}
