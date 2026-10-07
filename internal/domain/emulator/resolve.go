package emulator

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/sdk"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// Where the launcher came from, in the order Resolve tries them (emulator.md §Binary
// resolution; ADR-0032 §3; plan m1k D8): the configured `tools.emulator` first, then
// the host SDK's emulator/emulator.
const (
	SourceToolKey     = "tools.emulator"
	SourceHostSDK     = "host SDK"
	SourceADBLocation = "adb location"
	SourcePath        = "PATH"
)

// Keys is the configuration's tool keys as the launcher resolution reads them, read once
// at daemon start (ADR-0032 §3, §6; plan m1k D8). Launcher is `tools.emulator`; ADB is
// `tools.adb`, which the launcher derived from adb's location uses as adb was resolved.
type Keys struct {
	Launcher policy.ToolKey
	ADB      policy.ToolKey
}

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

// Resolve finds the launcher with no configured tool keys: it is ResolveWith over empty
// keys, the order every caller that predates the configuration file gets.
func Resolve(env []string) Resolution { return ResolveWith(env, Keys{}) }

// ResolveWith finds the launcher in ADR-0032 §3's order: the `tools.emulator` key, the
// host SDK's emulator/emulator (sdk.md §Host SDK), then the one derived from adb's
// location (`<sdk>/platform-tools/adb` → `<sdk>/emulator/emulator`), then `emulator` on
// PATH. It reads only env ("KEY=value" entries, as os.Environ gives them), never the
// process's own environment, and touches nothing but the file system. keys are the tool
// keys read at daemon start (plan m1k D8); an empty Keys is the same as Resolve. The
// launcher's absence is normal and affects only this subsystem (emulator.md §Binary
// resolution).
func ResolveWith(env []string, keys Keys) Resolution {
	host := sdk.Host(env)
	adb := device.ResolveWith(env, keys.ADB)

	var res Resolution
	// The adb resolution's warnings already carry the host SDK's: device.ResolveWith
	// copies sdk.Host's onto its own (resolve.go), so appending both would duplicate them.
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

	// The file's tool keys were not read at start: no key is used, the file is listed,
	// and resolution goes on to the host SDK and the rest (ADR-0032 §4; plan m1k D8).
	if keys.Launcher.Invalid != "" {
		reject(keys.Launcher.ConfigPath, SourceToolKey, "the configuration file is invalid ("+keys.Launcher.Invalid+"); its tool keys were not read")
	}

	// The configured tools.emulator is the first candidate (ADR-0032 §3; D8). Not
	// executable → rejected and the next is tried: the launcher's absence affects only
	// this subsystem (emulator.md §Binary resolution).
	if keys.Launcher.Invalid == "" && keys.Launcher.Path != "" {
		if reason := notExecutable(keys.Launcher.Path); reason == "" {
			return win(keys.Launcher.Path, SourceToolKey)
		} else {
			reject(keys.Launcher.Path, SourceToolKey, reason)
		}
	}

	// The SDK locations themselves, as sdk.Host rejected them, come next: when nothing
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
	return "the emulator launcher was not found: set `tools.emulator` in the droidyard configuration to the launcher, or ANDROID_HOME to an Android SDK with emulator/emulator, or put emulator on PATH. Tried: " + strings.Join(tried, "; ")
}
