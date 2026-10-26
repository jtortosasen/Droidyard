package sdk

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The codes of the SDK provisioning calls (plan m3f; ADR-0129 §2, §5; ADR-0130).
const (
	// ErrRepositoryUnavailable: Google's repository could not be read, so the call has
	// nothing to act on. Its cause says why.
	ErrRepositoryUnavailable capability.ErrorCode = "SDK_REPOSITORY_UNAVAILABLE"
	// ErrLicenseUnknown: the licence id is not one the repository serves, or the hash is not
	// the hash of a text it serves for that id now. Nothing was written.
	ErrLicenseUnknown capability.ErrorCode = "SDK_LICENSE_UNKNOWN"
	// ErrNotWritable: the directory the SDK would live in cannot be written, or is not one
	// to write an SDK into. Its cause names what stopped the call.
	ErrNotWritable capability.ErrorCode = "SDK_NOT_WRITABLE"
)

// Target is the directory Droidyard writes into: the host SDK when there is one, else
// ANDROID_HOME, else ANDROID_SDK_ROOT, else the per-OS default under HOME (ADR-0129 Q5;
// ADR-0131 §3). It reads env only and touches nothing.
//
// A target that exists must be a directory that is empty, or an SDK (sdk.md §Host SDK's
// test), or holds nothing but licenses/ and .temp/: what sdk.acceptLicense leaves on a host
// with no SDK before the first install, which must not lock that host out of installing
// (ADR-0131 §3). Any other non-empty directory is refused: Droidyard does not install into
// a folder that holds somebody's files. A target that does not exist is the answer; the
// caller creates it.
func Target(env []string) (string, error) {
	if h := Host(env); h.Found {
		return h.Root, nil
	}
	e := envMap(env)
	var root string
	switch {
	case e["ANDROID_HOME"] != "":
		root = e["ANDROID_HOME"]
	case e["ANDROID_SDK_ROOT"] != "":
		root = e["ANDROID_SDK_ROOT"]
	case e["HOME"] != "":
		root = DefaultSDK(e["HOME"])
	default:
		return "", capability.NewError(ErrNotWritable, "there is no place to set up an Android SDK").
			WithCause("no host SDK was found and neither ANDROID_HOME, ANDROID_SDK_ROOT nor HOME is set").
			WithNextStep("set ANDROID_HOME to the directory the SDK should live in")
	}
	if !filepath.IsAbs(root) {
		return "", capability.NewError(ErrNotWritable, "the SDK directory must be an absolute path").
			WithCause("the configured SDK directory is " + capability.Quote(root)).
			WithNextStep("set ANDROID_HOME to an absolute path")
	}
	root = filepath.Clean(root)
	if err := checkTarget(root); err != nil {
		return "", err
	}
	return root, nil
}

// checkTarget applies Target's rule to a directory that is not an SDK by Host's test.
func checkTarget(root string) error {
	fi, err := os.Stat(root) // the root itself may be a link to the real directory
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return capability.NewError(ErrNotWritable, "the SDK directory cannot be read").
			WithCause(capability.Quote(root) + ": " + describeFS(err))
	}
	if !fi.IsDir() {
		return capability.NewError(ErrNotWritable, "the SDK directory is not a directory").
			WithCause(capability.Quote(root) + " exists and is not a directory").
			WithNextStep("point ANDROID_HOME at a directory that is empty or does not exist")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return capability.NewError(ErrNotWritable, "the SDK directory cannot be read").
			WithCause(capability.Quote(root) + ": " + describeFS(err))
	}
	for _, en := range entries {
		if n := en.Name(); n != "licenses" && n != ".temp" {
			if looksLikeSDK(root) {
				return nil
			}
			return capability.NewError(ErrNotWritable, "the directory is not empty and is not an Android SDK").
				WithCause(capability.Quote(root) + " holds files that are not an SDK's; Droidyard does not install into it").
				WithNextStep("point ANDROID_HOME at an empty directory, or at an existing Android SDK")
		}
	}
	return nil
}

// describeFS is a file system error as a short sentence without the path the error repeats.
func describeFS(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}
