// Package device is the adb-backed device subsystem (docs/subsystems/adb-device.md).
package device

import (
	"os"
	"path/filepath"
	"strings"

	// sdkpkg aliases internal/domain/sdk: this package's tests declare a helper named
	// sdk, and a file-block import name may not collide with a package-block declaration
	// (plan m1h D13 keeps m1e's TestResolveOrder unchanged, so the import moves out of
	// the way, not the test helper).
	sdkpkg "github.com/jtortosasen/droidyard/internal/domain/sdk"
)

// Where adb came from, in the order Resolve tries them. The adb.path and sdk.root
// configuration keys have no specification and are skipped (plan m1e, R5).
const (
	SourceADBEnv = "ADB"
	SourcePath   = "PATH"
)

// The host SDK's sources, re-exported from internal/domain/sdk: host SDK discovery moved
// there (plan m1h, D13), and these are its candidate names, so adb resolution and its
// tests keep naming them.
const (
	SourceAndroidHome = sdkpkg.SourceAndroidHome
	SourceSDKRoot     = sdkpkg.SourceSDKRoot
	SourceSDKFromPath = sdkpkg.SourceSDKFromPath
	SourceSDKDefault  = sdkpkg.SourceSDKDefault
)

// Rejected is a candidate Resolve looked at and did not use. It is sdkpkg.Rejected, the
// host SDK discovery's own type, so the two cannot drift.
type Rejected = sdkpkg.Rejected

// Resolution is where adb is, or why it is nowhere. Rejected lists every candidate
// tried before the winner, or all of them when none won; Warnings are notes that do
// not stop resolution.
type Resolution struct {
	Found    bool
	Path     string
	Source   string
	Rejected []Rejected
	Warnings []string
}

// Resolve finds adb in adb-device.md §adb resolution and degradation's order: the
// ADB variable, the host SDK's platform-tools/adb (sdk.md §Host SDK), adb on PATH. It
// reads only env ("KEY=value" entries, as os.Environ gives them), never the process's
// own environment, and touches nothing but the file system.
//
// An ADB that names no executable file fails closed: nothing is tried after it
// (plan m1e, Q6), because falling through could pick an adb whose version differs from
// the running server's and restart the shared server.
func Resolve(env []string) Resolution {
	e := envMap(env)
	host := sdkpkg.Host(env)
	var res Resolution
	res.Warnings = append([]string{}, host.Warnings...)
	res.Rejected = []Rejected{}
	reject := func(cand, src, reason string) {
		res.Rejected = append(res.Rejected, Rejected{Candidate: cand, Source: src, Reason: reason})
	}
	win := func(path, src string) Resolution {
		res.Found, res.Path, res.Source = true, path, src
		return res
	}

	if v := e["ADB"]; v != "" {
		if reason := notExecutable(v); reason != "" {
			reject(v, SourceADBEnv, reason+"; ADB is set, so no other adb is tried")
			return res
		}
		return win(v, SourceADBEnv)
	}

	// The host SDK, as sdk.Host found it (sdk.md §Host SDK); its platform-tools/adb is
	// the second candidate. Its rejected candidates come before the adb on PATH.
	res.Rejected = append(res.Rejected, host.Rejected...)
	if host.Found {
		adb := filepath.Join(host.Root, "platform-tools", "adb")
		if reason := notExecutable(adb); reason != "" {
			reject(adb, host.Source, reason)
		} else {
			return win(adb, host.Source)
		}
	}

	pathAdb, pathReason := lookPath(e["PATH"])
	if pathAdb == "" {
		reject("adb on PATH", SourcePath, pathReason)
		return res
	}
	return win(pathAdb, SourcePath)
}

// defaultSDK is the per-OS default host SDK (sdk.md §Host SDK, step 5).
func defaultSDK(home string) string { return sdkpkg.DefaultSDK(home) }

// notExecutable says why path is not a runnable file, "" if it is.
func notExecutable(path string) string {
	fi, err := os.Stat(path)
	switch {
	case err != nil:
		return "no such file"
	case fi.IsDir():
		return "is a directory"
	case fi.Mode().Perm()&0o111 == 0:
		return "not executable"
	}
	return ""
}

// lookPath finds adb in the PATH value path, as a shell would, without reading the
// process's own PATH.
func lookPath(path string) (found, reason string) {
	if path == "" {
		return "", "PATH is empty or unset"
	}
	for _, d := range filepath.SplitList(path) {
		if d == "" {
			continue
		}
		p := filepath.Join(d, "adb")
		if notExecutable(p) == "" {
			return p, ""
		}
	}
	return "", "no executable adb in any PATH entry"
}

func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// NotFound explains, for a person or an agent, that adb was not found, what was tried
// and what to set. It is the one text system.capabilities' reason and a device call's
// DEVICE_ADB_NOT_FOUND both carry.
func (r Resolution) NotFound() string {
	tried := make([]string, 0, len(r.Rejected))
	for _, c := range r.Rejected {
		tried = append(tried, c.Candidate+" ("+c.Reason+")")
	}
	return "adb was not found: set ADB to the adb binary, or ANDROID_HOME to an Android SDK with platform-tools/adb, or put adb on PATH. Tried: " + strings.Join(tried, "; ")
}
