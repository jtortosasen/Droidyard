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
	"github.com/jtortosasen/droidyard/internal/policy"
)

// Where adb came from, in the order Resolve tries them (ADR-0032 §3; plan m1k D8). The
// `tools.adb` key comes first, the reverse of adb-device.md's older ADB-then-key order:
// the accepted ADR binds (R3). The adb.path and sdk.root configuration keys have no
// specification and are skipped (plan m1e, R5).
const (
	SourceToolKey = "tools.adb"
	SourceADBEnv  = "ADB"
	SourcePath    = "PATH"
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

	// configPath is the configuration file the tool key was read from. NotFound names it
	// beside the key, so a "Tried:" list says which file set `tools.adb` (plan m1k D8;
	// plan m1n A10). It is empty when no file was read, and is never published: only the
	// human "Tried:" text uses it.
	configPath string
}

// Resolve finds adb with no configured tool key: it is ResolveWith over an empty key,
// the order every caller that predates the configuration file gets.
func Resolve(env []string) Resolution { return ResolveWith(env, policy.ToolKey{}) }

// ResolveWith finds adb in ADR-0032 §3's order: the `tools.adb` key, the ADB variable,
// the host SDK's platform-tools/adb (sdk.md §Host SDK), adb on PATH. It reads only env
// ("KEY=value" entries, as os.Environ gives them), never the process's own environment,
// and touches nothing but the file system. key is `tools.adb` read at daemon start
// (plan m1k D8); an empty key is the same as Resolve.
//
// A configured `tools.adb` that names no executable file, and an ADB that names none,
// both fail closed: nothing is tried after them (plan m1e, Q6; plan m1k D8), because
// falling through could pick an adb whose version differs from the running server's and
// restart the shared server. When the configured key wins while ADB is set, ADB's path
// is listed as rejected ("overridden by tools.adb"), so system.health shows the override
// (R3).
func ResolveWith(env []string, key policy.ToolKey) Resolution {
	e := envMap(env)
	host := sdkpkg.Host(env)
	var res Resolution
	res.Warnings = append([]string{}, host.Warnings...)
	res.Rejected = []Rejected{}
	res.configPath = key.ConfigPath
	reject := func(cand, src, reason string) {
		res.Rejected = append(res.Rejected, Rejected{Candidate: cand, Source: src, Reason: reason})
	}
	win := func(path, src string) Resolution {
		res.Found, res.Path, res.Source = true, path, src
		return res
	}

	// The file's tool keys were not read at start: no key is used, the file is listed,
	// and resolution falls through to ADB, the SDK and PATH (ADR-0032 §4; plan m1k D8).
	if key.Invalid != "" {
		reject(key.ConfigPath, SourceToolKey, "the configuration file is invalid ("+key.Invalid+"); its tool keys were not read")
	}

	// The configured tools.adb is the first candidate (ADR-0032 §3; R3). Not executable
	// → fail closed, as ADB does; when it wins with ADB set, ADB is shown as overridden.
	if key.Invalid == "" && key.Path != "" {
		if reason := notExecutable(key.Path); reason != "" {
			reject(key.Path, SourceToolKey, reason+"; tools.adb is set, so no other adb is tried")
			return res
		}
		if v := e["ADB"]; v != "" {
			reject(v, SourceADBEnv, "overridden by tools.adb")
		}
		return win(key.Path, SourceToolKey)
	}

	if v := e["ADB"]; v != "" {
		if reason := notExecutable(v); reason != "" {
			reject(v, SourceADBEnv, reason+"; ADB is set, so no other adb is tried")
			return res
		}
		return win(v, SourceADBEnv)
	}

	// The host SDK, as sdk.Host found it (sdk.md §Host SDK); its platform-tools/adb is
	// the third candidate. Its rejected candidates come before the adb on PATH.
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
// and what to set. Each tried candidate names its source, so the reader knows which
// key, variable or directory named it; the configured key names the file it came from
// (plan m1k D8; plan m1n A10). It is the one text system.capabilities' reason and a
// device call's DEVICE_ADB_NOT_FOUND both carry.
func (r Resolution) NotFound() string {
	tried := make([]string, 0, len(r.Rejected))
	for _, c := range r.Rejected {
		tried = append(tried, c.Candidate+" ["+r.sourceName(c.Source)+"] ("+c.Reason+")")
	}
	return "adb was not found: set `tools.adb` in the droidyard configuration to the adb binary, or ADB in the environment, or ANDROID_HOME to an Android SDK with platform-tools/adb, or put adb on PATH. Tried: " + strings.Join(tried, "; ")
}

// sourceName spells a rejected candidate's source for the human "Tried:" text (plan m1k
// D8; plan m1n A10). The structured Source field keeps its constant — the same value
// system.health shows — and only this rendering adds the configuration file, the `$`
// sigil of an environment variable and the SDK's own name.
func (r Resolution) sourceName(src string) string {
	switch src {
	case SourceToolKey:
		if r.configPath != "" {
			return SourceToolKey + " in " + r.configPath
		}
		return SourceToolKey
	case SourceADBEnv:
		return "$ADB"
	case SourcePath:
		return "PATH"
	case SourceAndroidHome:
		return "SDK (ANDROID_HOME)"
	case SourceSDKRoot:
		return "SDK (ANDROID_SDK_ROOT)"
	case SourceSDKFromPath:
		return "SDK (derived from adb on PATH)"
	case SourceSDKDefault:
		return "SDK (per-OS default)"
	}
	return src
}
