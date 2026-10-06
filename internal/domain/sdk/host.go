// Package sdk is the Android SDK as a host resource (docs/subsystems/sdk.md): where
// the host SDK is, what is installed in it, and (later in this milestone) what a
// project's build requires of it. Host SDK discovery lives here so that adb resolution
// (internal/domain/device) and sdk.status answer from one place, and so the package.xml
// walk and the local.properties reader have one home (plan m1h, D13, D14).
package sdk

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Where the host SDK came from, in the order Host tries them (sdk.md §Host SDK). The
// configured sdk.root key has no specification and is skipped (plan m1e, R5).
const (
	SourceAndroidHome = "sdk:ANDROID_HOME"
	SourceSDKRoot     = "sdk:ANDROID_SDK_ROOT"
	SourceSDKFromPath = "sdk:derived-from-PATH"
	SourceSDKDefault  = "sdk:per-OS-default"

	warnHomeRootDiffers = "ANDROID_HOME and ANDROID_SDK_ROOT are both set and differ; ANDROID_HOME wins, as AGP does. Unset ANDROID_SDK_ROOT (deprecated)."
)

// Rejected is a candidate Host looked at and did not use. It is also the type adb
// resolution reports its rejected candidates with (device.Rejected aliases it), so the
// two cannot drift.
type Rejected struct {
	Candidate string `json:"candidate" jsonschema:"required"`
	Source    string `json:"source" jsonschema:"required"`
	Reason    string `json:"reason" jsonschema:"required"`
}

// HostResolution is where the host SDK is, or why it is nowhere. Rejected lists every
// candidate tried before the winner, or all of them when none won; Warnings are notes
// that do not stop resolution.
type HostResolution struct {
	Found    bool
	Root     string
	Source   string
	Rejected []Rejected
	Warnings []string
}

// Host finds the host SDK in sdk.md §Host SDK's order: ANDROID_HOME, ANDROID_SDK_ROOT
// (deprecated), an SDK derived from adb on PATH, then the per-OS default. The first
// candidate that exists and looks like an SDK (it contains platform-tools/, platforms/,
// emulator/ or cmdline-tools/) wins. It reads only env ("KEY=value" entries, as
// os.Environ gives them), never the process's own environment, and touches nothing but
// the file system.
//
// ANDROID_HOME and ANDROID_SDK_ROOT both set and different is one warning;
// ANDROID_HOME still wins, as AGP does.
func Host(env []string) HostResolution {
	e := envMap(env)
	res := HostResolution{Rejected: []Rejected{}, Warnings: []string{}}
	home, root := e["ANDROID_HOME"], e["ANDROID_SDK_ROOT"]
	if home != "" && root != "" && filepath.Clean(home) != filepath.Clean(root) {
		res.Warnings = append(res.Warnings, warnHomeRootDiffers)
	}

	type cand struct{ dir, src string }
	var cands []cand
	if home != "" {
		cands = append(cands, cand{home, SourceAndroidHome})
	}
	if root != "" {
		cands = append(cands, cand{root, SourceSDKRoot})
	}
	if adb := lookPathADB(e["PATH"]); adb != "" {
		if real, err := filepath.EvalSymlinks(adb); err == nil {
			cands = append(cands, cand{filepath.Dir(filepath.Dir(real)), SourceSDKFromPath})
		}
	}
	if h := e["HOME"]; h != "" {
		cands = append(cands, cand{DefaultSDK(h), SourceSDKDefault})
	}
	for _, c := range cands {
		if !looksLikeSDK(c.dir) {
			res.Rejected = append(res.Rejected, Rejected{
				Candidate: c.dir,
				Source:    c.src,
				Reason:    "not an Android SDK (none of platform-tools, platforms, emulator, cmdline-tools)",
			})
			continue
		}
		// The first candidate that looks like an SDK is the host SDK, whatever its
		// state: a caller that needs one of its tools decides whether it is usable.
		res.Found, res.Root, res.Source = true, c.dir, c.src
		return res
	}
	return res
}

// NotFound explains, for a person or an agent, that the host SDK was not found, what
// was tried and what to set. sdk.status carries it as a warning when found is false.
func (h HostResolution) NotFound() string {
	tried := make([]string, 0, len(h.Rejected))
	for _, c := range h.Rejected {
		tried = append(tried, c.Candidate+" ("+c.Reason+")")
	}
	return "no Android SDK was found: set ANDROID_HOME to an Android SDK directory (or ANDROID_SDK_ROOT); tried: " + strings.Join(tried, "; ")
}

// DefaultSDK is the per-OS default host SDK under home (sdk.md §Host SDK). Windows is
// out of scope (ADR-0002); its default belongs to the discovery seam when Windows is
// added.
func DefaultSDK(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Android", "sdk")
	}
	return filepath.Join(home, "Android", "Sdk")
}

// looksLikeSDK reports whether dir contains at least one thing an SDK contains.
func looksLikeSDK(dir string) bool {
	for _, d := range []string{"platform-tools", "platforms", "emulator", "cmdline-tools"} {
		if fi, err := os.Stat(filepath.Join(dir, d)); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// lookPathADB finds an executable adb in the PATH value path, as a shell would, without
// reading the process's own PATH. It is the first step of deriving a host SDK from adb
// on PATH (sdk.md §Host SDK, step 4).
func lookPathADB(path string) string {
	for _, d := range filepath.SplitList(path) {
		if d == "" {
			continue
		}
		p := filepath.Join(d, "adb")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode().Perm()&0o111 != 0 {
			return p
		}
	}
	return ""
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
