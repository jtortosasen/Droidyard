// Package device is the adb-backed device subsystem (docs/subsystems/adb-device.md).
package device

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Where adb came from, in the order Resolve tries them. The adb.path and sdk.root
// configuration keys have no specification and are skipped (plan m1e, R5).
const (
	SourceADBEnv        = "ADB"
	SourceAndroidHome   = "sdk:ANDROID_HOME"
	SourceSDKRoot       = "sdk:ANDROID_SDK_ROOT"
	SourceSDKFromPath   = "sdk:derived-from-PATH"
	SourceSDKDefault    = "sdk:per-OS-default"
	SourcePath          = "PATH"
	warnHomeRootDiffers = "ANDROID_HOME and ANDROID_SDK_ROOT are both set and differ; ANDROID_HOME wins, as AGP does. Unset ANDROID_SDK_ROOT (deprecated)."
)

// Rejected is a candidate Resolve looked at and did not use.
type Rejected struct {
	Candidate string `json:"candidate" jsonschema:"required"`
	Source    string `json:"source" jsonschema:"required"`
	Reason    string `json:"reason" jsonschema:"required"`
}

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
	var res Resolution
	res.Warnings = []string{}
	res.Rejected = []Rejected{}
	reject := func(cand, src, reason string) {
		res.Rejected = append(res.Rejected, Rejected{Candidate: cand, Source: src, Reason: reason})
	}
	win := func(path, src string) Resolution {
		res.Found, res.Path, res.Source = true, path, src
		return res
	}

	home, root := e["ANDROID_HOME"], e["ANDROID_SDK_ROOT"]
	if home != "" && root != "" && filepath.Clean(home) != filepath.Clean(root) {
		res.Warnings = append(res.Warnings, warnHomeRootDiffers)
	}

	if v := e["ADB"]; v != "" {
		if reason := notExecutable(v); reason != "" {
			reject(v, SourceADBEnv, reason+"; ADB is set, so no other adb is tried")
			return res
		}
		return win(v, SourceADBEnv)
	}

	// The host SDK: the first candidate that looks like an SDK wins.
	pathAdb, pathReason := lookPath(e["PATH"])
	type root_ struct{ dir, src string }
	var cands []root_
	if home != "" {
		cands = append(cands, root_{home, SourceAndroidHome})
	}
	if root != "" {
		cands = append(cands, root_{root, SourceSDKRoot})
	}
	if pathAdb != "" {
		if real, err := filepath.EvalSymlinks(pathAdb); err == nil {
			cands = append(cands, root_{filepath.Dir(filepath.Dir(real)), SourceSDKFromPath})
		}
	}
	if h := e["HOME"]; h != "" {
		cands = append(cands, root_{defaultSDK(h), SourceSDKDefault})
	}
	for _, c := range cands {
		if !looksLikeSDK(c.dir) {
			reject(c.dir, c.src, "not an Android SDK (none of platform-tools, platforms, emulator, cmdline-tools)")
			continue
		}
		adb := filepath.Join(c.dir, "platform-tools", "adb")
		if reason := notExecutable(adb); reason != "" {
			reject(adb, c.src, reason)
		} else {
			return win(adb, c.src)
		}
		break // the first SDK that looks like one is the host SDK; its adb is absent
	}

	if pathAdb == "" {
		reject("adb on PATH", SourcePath, pathReason)
		return res
	}
	return win(pathAdb, SourcePath)
}

// defaultSDK is the per-OS default host SDK (sdk.md §Host SDK, step 5).
func defaultSDK(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Android", "sdk")
	}
	return filepath.Join(home, "Android", "Sdk")
}

func looksLikeSDK(dir string) bool {
	for _, d := range []string{"platform-tools", "platforms", "emulator", "cmdline-tools"} {
		if fi, err := os.Stat(filepath.Join(dir, d)); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

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
