package emulator

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/sdk"
)

// Where the launcher came from, in the order Resolve tries them (emulator.md §Binary
// resolution).
const (
	SourceHostSDK     = "host SDK"
	SourceADBLocation = "adb location"
	SourcePath        = "PATH"
)

// Rejected is sdk.Rejected, the host SDK discovery's own type, so the two cannot drift.
type Rejected = sdk.Rejected

// Resolution is where the emulator launcher is, or why it is nowhere. Rejected lists
// every candidate tried before the winner, or all of them when none won; Warnings are
// notes that do not stop resolution.
type Resolution struct {
	Found    bool
	Path     string
	Source   string
	Rejected []Rejected
	Warnings []string
}

// Resolve finds the launcher in emulator.md §Binary resolution's order: the host SDK's
// emulator/emulator (sdk.md §Host SDK), then the one derived from adb's location
// (`<sdk>/platform-tools/adb` → `<sdk>/emulator/emulator`), then `emulator` on PATH. It
// reads only env ("KEY=value" entries, as os.Environ gives them), never the process's
// own environment, and touches nothing but the file system. Its absence is normal and
// affects only this subsystem (emulator.md §Binary resolution).
func Resolve(env []string) Resolution {
	host := sdk.Host(env)
	adb := device.Resolve(env)

	var res Resolution
	// The adb resolution's warnings already carry the host SDK's: device.Resolve copies
	// sdk.Host's onto its own (resolve.go), so appending both would duplicate them.
	res.Warnings = append([]string{}, adb.Warnings...)
	res.Rejected = []Rejected{}
	seen := map[string]bool{}
	reject := func(cand, src, reason string) {
		if cand == "" || seen[cand] {
			return
		}
		seen[cand] = true
		res.Rejected = append(res.Rejected, Rejected{Candidate: cand, Source: src, Reason: reason})
	}
	win := func(path, src string) Resolution {
		res.Found, res.Path, res.Source = true, path, src
		return res
	}

	// The SDK locations themselves, as sdk.Host rejected them, come first: when nothing
	// is found they are half of what a reader needs, and the launcher paths are the rest.
	for _, r := range host.Rejected {
		reject(r.Candidate, r.Source, r.Reason)
	}

	if host.Found {
		cand := filepath.Join(host.Root, "emulator", "emulator")
		if reason := notExecutable(cand); reason == "" {
			return win(cand, SourceHostSDK)
		} else {
			reject(cand, SourceHostSDK, reason)
		}
	}

	if cand, ok := derivedFromADB(adb); ok {
		if reason := notExecutable(cand); reason == "" {
			return win(cand, SourceADBLocation)
		} else {
			reject(cand, SourceADBLocation, reason)
		}
	}

	path, reason := lookPathEmulator(envMap(env)["PATH"])
	if path == "" {
		reject("emulator on PATH", SourcePath, reason)
		return res
	}
	return win(path, SourcePath)
}

// derivedFromADB is the launcher `<sdk>/emulator/emulator` derived from adb's location
// (emulator.md §Binary resolution), when adb's own directory is a platform-tools one.
// adb elsewhere — a bare PATH adb, an ADB variable outside an SDK — names no SDK and
// derives nothing.
func derivedFromADB(adb device.Resolution) (string, bool) {
	if !adb.Found || filepath.Base(filepath.Dir(adb.Path)) != "platform-tools" {
		return "", false
	}
	return filepath.Join(filepath.Dir(filepath.Dir(adb.Path)), "emulator", "emulator"), true
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

// lookPathEmulator finds emulator in the PATH value path, as a shell would, without
// reading the process's own PATH.
func lookPathEmulator(path string) (found, reason string) {
	if path == "" {
		return "", "PATH is empty or unset"
	}
	for _, d := range filepath.SplitList(path) {
		if d == "" {
			continue
		}
		p := filepath.Join(d, "emulator")
		if notExecutable(p) == "" {
			return p, ""
		}
	}
	return "", "no executable emulator in any PATH entry"
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

// NotFound explains, for a person or an agent, that the launcher was not found, what was
// tried and what to set. It is the one text system.capabilities' unavailableReason and a
// call's EMULATOR_LAUNCHER_NOT_FOUND both carry.
func (r Resolution) NotFound() string {
	tried := make([]string, 0, len(r.Rejected))
	for _, c := range r.Rejected {
		tried = append(tried, c.Candidate+" ("+c.Reason+")")
	}
	return "the emulator launcher was not found: set ANDROID_HOME to an Android SDK with emulator/emulator, or put emulator on PATH. Tried: " + strings.Join(tried, "; ")
}
