package device

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build"
	"github.com/jtortosasen/droidyard/internal/hosting"
)

// device.install and device.uninstall (adb-device.md §Install, §Capabilities; plan m1g
// step 8; ADR-0023 §Decision 5; D18). The device subsystem does not find APKs, read
// build state or guess an application id: device.install takes a build reference, the
// build registry resolves it to one installable APK, and this package hands the path to
// adb and publishes only the applicationId, versionCode and the APK's file name.
//
// device.install is the one workspace-scoped device capability, because its only input
// is a build reference (adb-device.md §Install). It does not run project code, so it
// carries no workspace trust re-check (ADR-0015 §5 gates `project.*`, `build.*` and
// `sdk.requirements`); it runs the device preamble hook like every other device call.

// ErrInstallFailed is adb install's rejection, detected also on exit 0 (adb-device.md
// §Install, §Failure modes; plan m1g R3, R7).
const ErrInstallFailed capability.ErrorCode = "DEVICE_INSTALL_FAILED"

// ErrUninstallFailed is adb uninstall's rejection (adb-device.md §Failure modes; plan
// m1g D9, D18).
const ErrUninstallFailed capability.ErrorCode = "DEVICE_UNINSTALL_FAILED"

// moduleRE and variantRE are R6: module matches project.modules' project paths and
// variant matches project.variants' names, so nothing a caller sends reaches the build
// registry unchecked. applicationId reuses launch.go's pattern.
var (
	moduleRE  = regexp.MustCompile(`^(:[A-Za-z0-9_.-]+)+$`)
	variantRE = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
)

// installFailureRE is the "Failure [INSTALL_FAILED_…]" adb prints, which is a failure
// even on exit 0. The bracketed token is the whole cause, never the rest of the line
// (adb-device.md §Install; plan m1g R3).
// Modern adb prints the token followed by a colon and a message
// (`Failure [INSTALL_FAILED_UPDATE_INCOMPATIBLE: Package … ignoring!]`), older ones the
// bare token; only the token is kept either way (R3; step 8 review B2).
var installFailureRE = regexp.MustCompile(`Failure \[([A-Z][A-Z0-9_]*)[\]:]`)

// The install reference's bounds (plan m1g step 8: the install command gets 180 s).
// uninstall and pm path are bounded too; a variable so a test injects a scale.
var (
	installTimeout   = 180 * time.Second
	uninstallTimeout = 60 * time.Second
	pmPathTimeout    = 15 * time.Second
)

const (
	installOutputCap   = 64 << 10
	pmPathOutputCap    = 64 << 10
	uninstallOutputCap = 16 << 10
)

// BuildResolver resolves a build reference to an installable APK (the build registry;
// gradle.md §Build outputs "Install reference"). The device package declares it, so the
// device package depends on the build package's shape and the build package never
// depends on the device package.
type BuildResolver interface {
	ResolveBuild(wsid, buildID, module, variant string) (build.ResolvedOutput, error)
}

// BuildRef is device.install's build reference (gradle.md §Build outputs "Install
// reference").
type BuildRef struct {
	BuildID string `json:"buildId,omitempty" jsonschema:"pattern=^op-[0-9a-f]{16}$" jsonschema_description:"The build to install; absent means the newest finished build with an output for module and variant."`
	Module  string `json:"module" jsonschema:"required,pattern=^(:[A-Za-z0-9_.-]+)+$" jsonschema_description:"The Gradle project path of the module whose output to install, e.g. \":app\", matching project.modules."`
	Variant string `json:"variant" jsonschema:"required,pattern=^[a-z][A-Za-z0-9]*$" jsonschema_description:"The variant whose APK to install, e.g. \"debug\", matching project.variants."`
}

// InstallParams is device.install's parameters.
type InstallParams struct {
	Device string   `json:"device" jsonschema:"required" jsonschema_description:"The serial of the device, from device.list."`
	Build  BuildRef `json:"build" jsonschema:"required" jsonschema_description:"The build reference to install: a module and variant, and optionally a buildId."`
}

// InstallResult is device.install's result. It carries applicationId (which launch,
// uninstall and logs take), the output's versionCode and the installed APK's file name —
// never a path (principle 14).
type InstallResult struct {
	ApplicationID string `json:"applicationId" jsonschema:"required"`
	VersionCode   *int   `json:"versionCode,omitempty" jsonschema_description:"The output's versionCode, absent when the build's metadata named none."`
	APK           string `json:"apk" jsonschema:"required" jsonschema_description:"The installed APK's file name."`
}

// UninstallParams is device.uninstall's parameters.
type UninstallParams struct {
	Device string `json:"device" jsonschema:"required" jsonschema_description:"The serial of the device, from device.list."`
	// ApplicationID carries the same package-name rule validate enforces, published so a
	// client rejects the id before the call (launch.go; plan m1g R6).
	ApplicationID string `json:"applicationId" jsonschema:"required,pattern=^[A-Za-z][A-Za-z0-9_]*(\\.[A-Za-z][A-Za-z0-9_]*)+$" jsonschema_description:"The applicationId to uninstall: a package name, unmatched ids are rejected before the device is touched."`
}

// UninstallResult is device.uninstall's result. Uninstalled false with reason
// "notInstalled" is a completed call: the package was already absent (D18).
type UninstallResult struct {
	ApplicationID string `json:"applicationId" jsonschema:"required"`
	Uninstalled   bool   `json:"uninstalled" jsonschema:"required"`
	Reason        string `json:"reason,omitempty" jsonschema_description:"Why uninstalled is false; \"notInstalled\" when the package was already absent."`
}

// validate rejects a module or variant that is not a project path or variant name,
// before it reaches the build registry or anything else (plan m1g R6).
func (p InstallParams) validate() error {
	if !moduleRE.MatchString(p.Build.Module) {
		return capability.Errorf(capability.ErrInvalidParams, "module %q is not a Gradle project path: it must look like \":app\"", p.Build.Module)
	}
	if !variantRE.MatchString(p.Build.Variant) {
		return capability.Errorf(capability.ErrInvalidParams, "variant %q is not a variant name", p.Build.Variant)
	}
	return nil
}

// validate rejects an applicationId that is not a package name, before it can reach a
// device shell (plan m1g R6).
func (p UninstallParams) validate() error {
	if !applicationIDRE.MatchString(p.ApplicationID) {
		return capability.Errorf(capability.ErrInvalidParams, "applicationId %q is not a package name: use at least two dot-separated segments of letters, digits and underscores, starting with a letter", p.ApplicationID)
	}
	return nil
}

// installErrors are the codes device.install can return: the device preamble's, the
// build resolution's (the build subsystem's codes, plus BUILD_NOT_FOUND for an unknown
// buildId and WORKSPACE_STATE_UNREADABLE for a state root that will not read), the
// params check and its own. It is long-running, so it need not list
// OPERATION_QUEUE_TIMEOUT (registry.go).
var installErrors = append(append([]capability.ErrorCode{}, PreambleErrors...),
	build.ErrBuildOutputNotFound, build.ErrBuildOutputStale, build.ErrBuildOutputUnsupported,
	capability.ErrBuildNotFound, capability.ErrWorkspaceStateUnreadable,
	capability.ErrInvalidParams, ErrInstallFailed)

// uninstallErrors are the codes device.uninstall can return: the device preamble's, the
// params check, its own, and the queue bound the scheduler may return (it holds the
// device's write lock and is not long-running).
var uninstallErrors = append(append([]capability.ErrorCode{}, PreambleErrors...),
	capability.ErrInvalidParams, ErrUninstallFailed, capability.ErrOperationQueueTimeout)

// registerInstall adds device.install, resolving its build reference through builds.
// builds may be nil in a descriptor-only registry (the published schema), where the
// handler reports no build registry rather than panicking.
func registerInstall(reg *capability.Registry, adb *ADB, builds BuildResolver) {
	reg.MustRegister(capability.Descriptor{
		Name:         "device.install",
		Kind:         capability.Command,
		Scope:        capability.Workspace,
		LongRunning:  true,
		Since:        "0.6.0",
		Availability: []capability.Requirement{capability.RequiresADB},
		Params:       InstallParams{},
		Target: func(p any) *capability.OpTarget {
			ip := p.(InstallParams)
			return &capability.OpTarget{Device: ip.Device, Module: ip.Build.Module, Variant: ip.Build.Variant}
		},
		Result:      InstallResult{},
		Errors:      installErrors,
		Description: "Installs a build's APK on the device with adb install -r, which keeps the app's data, and returns its applicationId and versionCode. buildId absent means the newest finished build with an output for module and variant.",
		Resource: func(call *capability.Call) string {
			if s := call.Params.(InstallParams).Device; s != "" {
				return writeKey(s)
			}
			return ""
		},
		Annotations: capability.Annotations{
			Destructive: boolPtr(false),
			Reason:      "-r replaces the app and keeps its data (mcp.md §Annotations)",
		},
		Authorize: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(InstallParams)
			// Validate before the hook so a malformed module or variant runs no adb
			// command at all (plan m1g R6).
			if err := p.validate(); err != nil {
				return nil, err
			}
			// A device a host lends is named <remote>/<serial>: the client's own policy
			// decides, and the build is uploaded to the host (ADR-0100 §3).
			if rt, ok, err := adb.authorizeRemote(call); ok || err != nil {
				if err != nil {
					return nil, err
				}
				return rt, nil
			}
			return adb.authorize(ctx, call.Origin, p.Device, "device.install")
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(InstallParams)
			if err := p.validate(); err != nil {
				return nil, err
			}
			if builds == nil {
				return nil, capability.NewError(build.ErrBuildOutputNotFound, "this daemon has no build registry configured").
					WithCause("no build history was passed to the device package").
					WithNextStep("call build.assemble to build the workspace, then retry device.install")
			}
			out, err := builds.ResolveBuild(call.Workspace, p.Build.BuildID, p.Build.Module, p.Build.Variant)
			if err != nil {
				return nil, err
			}
			if rt, ok := call.Authorized.(*remoteTarget); ok {
				return adb.installRemote(ctx, call, rt, out)
			}
			t := call.Authorized.(*target)
			return adb.install(ctx, t, p, out)
		},
	})
}

// installUploadErrors are the codes device.installUpload can return: the device preamble's,
// the params check (which is also an upload this peer does not have) and its own. It is
// long-running, so it need not list OPERATION_QUEUE_TIMEOUT.
var installUploadErrors = append(append([]capability.ErrorCode{}, PreambleErrors...),
	capability.ErrInvalidParams, ErrInstallFailed)

// InstallUploadParams is device.installUpload's parameters.
type InstallUploadParams struct {
	Device string `json:"device" jsonschema:"required" jsonschema_description:"The serial of the device, from device.list."`
	Upload string `json:"upload" jsonschema:"required,pattern=^[0-9a-f]{32}$" jsonschema_description:"The id the host answered the peer's upload with: the peer's own pending upload."`
}

// InstallUploadResult is device.installUpload's result.
type InstallUploadResult struct {
	SHA256 string `json:"sha256" jsonschema:"required" jsonschema_description:"The SHA-256 of the installed file, as the host stored it."`
}

// validate rejects an upload id that is not the shape the host issues, before anything
// looks it up. The id is only ever a key in memory, never part of a path.
func (p InstallUploadParams) validate() error {
	if !hosting.ValidUploadID(p.Upload) {
		return capability.Errorf(capability.ErrInvalidParams, "upload %q is not an upload id", p.Upload)
	}
	return nil
}

// registerInstallUpload adds device.installUpload (ADR-0099 §3), the host's half of
// installing a build on a device it lends. It is global, human-only and lent to peers; the
// APK is one a peer uploaded, so for any other caller there is none.
func registerInstallUpload(reg *capability.Registry, adb *ADB) {
	reg.MustRegister(capability.Descriptor{
		Name:         "device.installUpload",
		Kind:         capability.Command,
		Scope:        capability.Global,
		LongRunning:  true,
		Audience:     capability.AudienceHuman,
		Lendable:     true,
		Since:        "0.29.0",
		Availability: []capability.Requirement{capability.RequiresADB},
		Params:       InstallUploadParams{},
		Target:       func(p any) *capability.OpTarget { return deviceTarget(p.(InstallUploadParams).Device) },
		Result:       InstallUploadResult{},
		Errors:       installUploadErrors,
		Description:  "Installs the APK a paired peer uploaded to this host, then removes it; an upload that is not the caller's, has expired or was replaced is rejected. The device must be one this host lends to the peer.",
		Resource: func(call *capability.Call) string {
			if s := call.Params.(InstallUploadParams).Device; s != "" {
				return writeKey(s)
			}
			return ""
		},
		Annotations: capability.Annotations{
			Destructive: boolPtr(false),
			Reason:      "-r replaces the app and keeps its data (mcp.md §Annotations)",
		},
		Authorize: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(InstallUploadParams)
			if err := p.validate(); err != nil {
				return nil, err
			}
			return adb.authorize(ctx, call.Origin, p.Device, "device.installUpload")
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(InstallUploadParams)
			if err := p.validate(); err != nil {
				return nil, err
			}
			t := call.Authorized.(*target)
			return adb.installUpload(ctx, t, call.Origin, p)
		},
	})
}

// registerUninstall adds device.uninstall (D18). It is global: its only input is an
// applicationId.
func registerUninstall(reg *capability.Registry, adb *ADB) {
	reg.MustRegister(adb.plain(capability.Descriptor{
		Name:         "device.uninstall",
		Kind:         capability.Command,
		Scope:        capability.Global,
		Since:        "0.6.0",
		Availability: []capability.Requirement{capability.RequiresADB},
		Params:       UninstallParams{},
		Target:       func(p any) *capability.OpTarget { return deviceTarget(p.(UninstallParams).Device) },
		Result:       UninstallResult{},
		Errors:       uninstallErrors,
		Description:  "Uninstalls an application and reports whether it removed one; an app that is not installed completes with uninstalled false. It removes the app's data.",
		Resource: func(call *capability.Call) string {
			if s := call.Params.(UninstallParams).Device; s != "" {
				return writeKey(s)
			}
			return ""
		},
		Authorize: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(UninstallParams)
			if err := p.validate(); err != nil {
				return nil, err
			}
			return adb.authorize(ctx, call.Origin, p.Device, "device.uninstall")
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(UninstallParams)
			if err := p.validate(); err != nil {
				return nil, err
			}
			t := call.Authorized.(*target)
			return adb.uninstall(ctx, t, p)
		},
	}))
}

// install runs `adb -s <serial> install -r <apk>` for a resolved output and maps its
// result (adb-device.md §Install). A "Failure [INSTALL_FAILED_…]" is detected on exit 0
// as well: the token alone is the cause (R3).
func (a *ADB) install(ctx context.Context, t *target, p InstallParams, out build.ResolvedOutput) (InstallResult, error) {
	if err := a.installAPK(ctx, t, out.Path, out.APK); err != nil {
		return InstallResult{}, err
	}
	return InstallResult{ApplicationID: out.ApplicationID, VersionCode: out.VersionCode, APK: out.APK}, nil
}

// installAPK is the install both a build and an upload go through: `adb install -r` of the
// file at path, which no error names (name stands in for it).
func (a *ADB) installAPK(ctx context.Context, t *target, path, name string) error {
	if err := a.ready(ctx, t); err != nil {
		return err
	}
	res, err := a.runShell(ctx, t, installTimeout, installOutputCap, "install", "-r", path)
	if err != nil {
		if errors.Is(err, ErrTimeout) {
			return installFailed("adb install did not answer in " + installTimeout.String())
		}
		return withoutPath(err, path, name)
	}
	if token := failureToken(res); token != "" {
		return installFailed(token)
	}
	if res.ExitCode != 0 {
		return installFailed("adb install exited with status " + strconv.Itoa(res.ExitCode))
	}
	return nil
}

// installUpload installs the APK the calling peer uploaded and removes it, whatever the
// install does: an upload is used once (ADR-0099 §3). Only a paired peer has uploads, and
// only its own are found.
func (a *ADB) installUpload(ctx context.Context, t *target, who capability.Origin, p InstallUploadParams) (InstallUploadResult, error) {
	peer := ""
	if who.Client == capability.ClientPeer {
		peer = who.Name
	}
	var up *hosting.Upload
	ok := false
	if a.uploads != nil {
		up, ok = a.uploads.Take(peer, p.Upload)
	}
	if !ok {
		cause := "the upload may have expired (an upload is kept 10 minutes), been replaced by a newer one, or already been installed"
		if a.uploads != nil {
			if why := a.uploads.Gone(peer, p.Upload); why != "" {
				cause = "the upload was " + why
			}
		}
		return InstallUploadResult{}, capability.NewError(capability.ErrInvalidParams, "this host has no such upload from this peer").
			WithCause(cause).
			WithNextStep("Repeat device.install, which uploads the build again.")
	}
	defer up.Remove()
	if err := a.installAPK(ctx, t, up.Path, "upload.apk"); err != nil {
		return InstallUploadResult{}, err
	}
	return InstallUploadResult{SHA256: up.SHA256}, nil
}

// uninstall runs D18: `pm path` first; an absent package is a completed call; otherwise
// `adb uninstall`, whose "Success" or "Failure […]" decides.
func (a *ADB) uninstall(ctx context.Context, t *target, p UninstallParams) (UninstallResult, error) {
	if err := a.ready(ctx, t); err != nil {
		return UninstallResult{}, err
	}
	installed, err := a.packageInstalled(ctx, t, p.ApplicationID)
	if err != nil {
		return UninstallResult{}, err
	}
	if !installed {
		return UninstallResult{ApplicationID: p.ApplicationID, Uninstalled: false, Reason: "notInstalled"}, nil
	}
	res, err := a.runShell(ctx, t, uninstallTimeout, uninstallOutputCap, "uninstall", p.ApplicationID)
	if err != nil {
		if errors.Is(err, ErrTimeout) {
			return UninstallResult{}, uninstallFailed("adb uninstall did not answer in " + uninstallTimeout.String())
		}
		return UninstallResult{}, err
	}
	if hasSuccess(res) {
		return UninstallResult{ApplicationID: p.ApplicationID, Uninstalled: true}, nil
	}
	if token := failureToken(res); token != "" {
		return UninstallResult{}, uninstallFailed(token)
	}
	return UninstallResult{}, uninstallFailed("adb uninstall exited with status " + strconv.Itoa(res.ExitCode))
}

// packageInstalled answers whether the package is present, with `adb -s <serial> shell
// pm path <id>`: a "package:" line means installed, nothing means absent (D18). A device
// that does not answer is DEVICE_NOT_READY, never read as "absent".
func (a *ADB) packageInstalled(ctx context.Context, t *target, id string) (bool, error) {
	res, err := a.runShell(ctx, t, pmPathTimeout, pmPathOutputCap, "shell", "pm", "path", id)
	if err != nil {
		if errors.Is(err, ErrTimeout) {
			return false, capability.NewError(ErrNotReady, "the device "+t.serial+" is not answering").
				WithCause("adb shell pm path " + id + " did not answer in " + pmPathTimeout.String()).
				WithNextStep("Retry in a few seconds; if it persists, restart the device.")
		}
		return false, err
	}
	for _, l := range strings.Split(string(res.Stdout), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "package:") {
			return true, nil
		}
	}
	// An absent package is exit 1 with no output (observed on API 36.1, step 8 level
	// 5). Anything printed instead is the package manager failing, which is not
	// "absent": the uninstall is refused rather than reported done (step 8 review N5).
	if text := strings.TrimSpace(string(res.Stdout) + string(res.Stderr)); text != "" {
		return false, uninstallFailed("pm path did not answer with a package")
	}
	return false, nil
}

// failureToken is the bracketed token of adb's "Failure [TOKEN]" line in either stream,
// "" when there is none. It is the whole cause an install or uninstall failure carries
// (R3): never the rest of the line, which could quote device text.
func failureToken(out Output) string {
	for _, b := range [][]byte{out.Stderr, out.Stdout} {
		if m := installFailureRE.FindSubmatch(b); m != nil {
			return string(m[1])
		}
	}
	return ""
}

// hasSuccess reports whether adb printed its own "Success" line, which is what decides
// a successful uninstall (D18).
func hasSuccess(out Output) bool {
	for _, b := range [][]byte{out.Stdout, out.Stderr} {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(l) == "Success" {
				return true
			}
		}
	}
	return false
}

// installFailed is DEVICE_INSTALL_FAILED with adb's reason as cause.
func installFailed(cause string) *capability.Error {
	return capability.NewError(ErrInstallFailed, "the application could not be installed").
		WithCause(cause).
		WithNextStep("Check the APK and the device's free space and signatures, then retry device.install.")
}

// uninstallFailed is DEVICE_UNINSTALL_FAILED with adb's reason as cause.
func uninstallFailed(cause string) *capability.Error {
	return capability.NewError(ErrUninstallFailed, "the application could not be uninstalled").
		WithCause(cause).
		WithNextStep("Check the applicationId and the device state, then retry device.uninstall.")
}

// withoutPath replaces the APK's absolute path with its file name in an error's text:
// an adb failure quotes its command line or output, and no result or error carries a
// path (gradle.md §Build outputs, principle 14; step 8 review N1).
func withoutPath(err error, path, name string) error {
	var e *capability.Error
	if !errors.As(err, &e) || path == "" {
		return err
	}
	c := *e
	c.Message = strings.ReplaceAll(c.Message, path, name)
	c.Cause = strings.ReplaceAll(c.Cause, path, name)
	return &c
}
