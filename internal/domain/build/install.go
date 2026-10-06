package build

import (
	"os"
	"strconv"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The install reference device.install resolves against (gradle.md §Build outputs
// "Install reference"; plan m1g step 8; ADR-0023 §Decision 5). The device subsystem
// does not find APKs, read build state or guess an application id: device.install takes
// a build reference, the build registry resolves it to one installable APK from the
// output-path index it persisted, and re-checks that the file is still the one the
// build wrote.

// The codes this resolution returns (gradle.md §Build outputs; plan m1g R7). They are
// the build subsystem's: device.install surfaces them unchanged.
const (
	// ErrBuildOutputNotFound: no finished build of the workspace produced an output for
	// the requested module and variant.
	ErrBuildOutputNotFound capability.ErrorCode = "BUILD_OUTPUT_NOT_FOUND"
	// ErrBuildOutputStale: the recorded APK is gone, or its size or modification time
	// changed since the build; nothing is installed (gradle.md §Build outputs).
	ErrBuildOutputStale capability.ErrorCode = "BUILD_OUTPUT_STALE"
	// ErrBuildOutputUnsupported: the output's shape is outside the MVP: not exactly one
	// unfiltered APK (gradle.md §Build outputs "MVP scope").
	ErrBuildOutputUnsupported capability.ErrorCode = "BUILD_OUTPUT_UNSUPPORTED"
)

// ResolvedOutput is one installable output: the record's published facts plus the APK's
// path and identity. Path stays inside the core (principle 14: no paths in results);
// device.install hands it to adb and publishes only ApplicationID, VersionCode and APK.
type ResolvedOutput struct {
	BuildID       string
	Module        string
	Variant       string
	ApplicationID string
	// VersionCode is the output's, nil when the build's metadata named none (field 0).
	VersionCode *int
	// APK is the APK's file name, never a path.
	APK string
	// Path is the APK's absolute path. Never published.
	Path      string
	SizeBytes int64
}

// ResolveBuild resolves a build reference to an installable APK, or the reason there is
// none (gradle.md §Build outputs "Install reference"). With buildID empty it walks the
// workspace's finished builds, newest first, and takes the first with an output for
// module and variant; a buildID names one. The chosen output must be exactly one
// unfiltered APK, and the file's size and mtime must match the build's record, or the
// resolution fails BUILD_OUTPUT_UNSUPPORTED or BUILD_OUTPUT_STALE.
func (r *Registry) ResolveBuild(wsid, buildID, module, variant string) (ResolvedOutput, error) {
	if !r.configured() {
		return ResolvedOutput{}, buildOutputNotFound(buildID, module, variant)
	}
	if buildID != "" {
		rec, err := r.load(wsid, buildID)
		if err != nil {
			// BUILD_NOT_FOUND for an unknown id, WORKSPACE_STATE_UNREADABLE for a
			// record this binary cannot read (D9).
			return ResolvedOutput{}, err
		}
		return resolveBuildOutput(rec, module, variant)
	}
	entries, err := r.entries(wsid)
	if err != nil {
		return ResolvedOutput{}, stateUnreadable(wsid, err)
	}
	for _, e := range entries {
		rec, err := r.load(wsid, e.BuildID)
		if err != nil {
			// The index names this build but its record will not read: it cannot say
			// it has the output, so the next older build is asked.
			continue
		}
		if _, _, ok := outputForVariant(rec, module, variant); ok {
			return resolveBuildOutput(rec, module, variant)
		}
	}
	return ResolvedOutput{}, buildOutputNotFound("", module, variant)
}

// outputForVariant finds a record's published output for module and variant and the
// APK paths behind it. ok is false when the record names no such output, or names one
// without an applicationId: both are "no usable output", never guessed at
// (gradle.md §Build outputs: missing is reported, not guessed).
func outputForVariant(rec record, module, variant string) (out Output, paths []OutputPath, ok bool) {
	for _, o := range rec.Outputs {
		if o.Module == module && o.Variant == variant {
			out = o
			break
		}
	}
	if out.ApplicationID == "" {
		return Output{}, nil, false
	}
	for _, p := range rec.OutputPaths {
		if p.Module == module && p.Variant == variant {
			paths = append(paths, p)
		}
	}
	return out, paths, true
}

// resolveBuildOutput applies the install reference's rules to one record's output:
// exactly one unfiltered APK, and the file unchanged since the build wrote it.
func resolveBuildOutput(rec record, module, variant string) (ResolvedOutput, error) {
	out, paths, ok := outputForVariant(rec, module, variant)
	if !ok {
		return ResolvedOutput{}, buildOutputNotFound(rec.BuildID, module, variant)
	}
	var apk *OutputPath
	for i := range paths {
		if len(paths[i].Filters) != 0 {
			continue
		}
		if apk != nil {
			return ResolvedOutput{}, buildOutputUnsupported(rec.BuildID, module, variant, "more than one unfiltered APK")
		}
		apk = &paths[i]
	}
	if apk == nil {
		return ResolvedOutput{}, buildOutputUnsupported(rec.BuildID, module, variant, "no unfiltered APK")
	}
	// Lstat, as the record was made (outputs.go): a symlink swapped in after the build is
	// not followed (step 8 review N3).
	fi, err := os.Lstat(apk.Path)
	if err != nil || !fi.Mode().IsRegular() {
		return ResolvedOutput{}, buildOutputStale(rec.BuildID, module, variant, "the APK it recorded is gone")
	}
	if fi.Size() != apk.SizeBytes || !fi.ModTime().Equal(apk.ModTime) {
		return ResolvedOutput{}, buildOutputStale(rec.BuildID, module, variant, "its size or modification time changed")
	}
	return ResolvedOutput{
		BuildID:       rec.BuildID,
		Module:        module,
		Variant:       variant,
		ApplicationID: out.ApplicationID,
		VersionCode:   out.VersionCode,
		APK:           apk.APK,
		Path:          apk.Path,
		SizeBytes:     apk.SizeBytes,
	}, nil
}

func buildOutputNotFound(buildID, module, variant string) *capability.Error {
	msg := "no finished build of this workspace has an output for module " + module + " variant " + variant
	if buildID != "" {
		msg = "build " + strconv.Quote(buildID) + " has no output for module " + module + " variant " + variant
	}
	return capability.NewError(ErrBuildOutputNotFound, msg).
		WithNextStep("call build.assemble {module: " + strconv.Quote(module) + ", variant: " + strconv.Quote(variant) + "} to build it, then retry device.install")
}

func buildOutputStale(buildID, module, variant, why string) *capability.Error {
	return capability.NewError(ErrBuildOutputStale, "the APK the build recorded is not the file on disk any more").
		WithCause("build " + buildID + "'s output for module " + module + " variant " + variant + ": " + why).
		WithNextStep("call build.assemble to rebuild it, then retry device.install")
}

func buildOutputUnsupported(buildID, module, variant, why string) *capability.Error {
	return capability.NewError(ErrBuildOutputUnsupported, "the output for module "+module+" variant "+variant+" is not one the MVP installs").
		WithCause("build " + buildID + " produced " + why + "; the MVP installs exactly one unfiltered APK per variant").
		WithNextStep("assemble a variant whose output is a single unfiltered APK, then retry device.install")
}
