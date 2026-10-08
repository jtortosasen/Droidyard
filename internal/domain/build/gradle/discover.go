// Package gradle is the core's Gradle sidecar client (ADR-0017): it discovers the JVM
// the sidecar runs on, extracts its jars, supervises one sidecar for every root, and
// speaks the line-JSON protocol of docs/subsystems/gradle.md §Protocol to it. It holds
// the per-root TAPI lock and the project-model cache; it defines no capability itself
// (cmd/droidyard registers project.* and build.tasks over it).
package gradle

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/jtortosasen/droidyard/internal/policy"
)

// Where a JVM came from, in the order discovery tries them (gradle.md §Sidecar JVM
// discovery; ADR-0032 §3; plan m1k D8): the configured `gradle.sidecarJavaHome` first,
// then JAVA_HOME.
const (
	SourceSidecarJavaHome = "gradle.sidecarJavaHome"
	SourceJavaHome        = "JAVA_HOME"
	SourcePath            = "PATH"
	SourceJavaHomeTool    = "/usr/libexec/java_home"
	SourceStudio          = "Android Studio JBR"
)

// Rejected is a JVM candidate discovery looked at and did not use.
type Rejected struct {
	Candidate string `json:"candidate" jsonschema:"required"`
	Source    string `json:"source" jsonschema:"required"`
	Reason    string `json:"reason" jsonschema:"required"`
}

// JVM is the sidecar's JVM: where it was found and its version, or every candidate that
// was rejected. system.health reports it (plan m1g D22).
type JVM struct {
	Found    bool       `json:"found" jsonschema:"required"`
	Home     string     `json:"home,omitempty"`
	Version  string     `json:"version,omitempty"`
	Source   string     `json:"source,omitempty"`
	Rejected []Rejected `json:"rejected" jsonschema:"required"`

	// configPath is the configuration file the `gradle.sidecarJavaHome` key was read
	// from, named in the "Tried:" text beside the key (plan m1k D8; plan m1n A10). Empty
	// when no file was read; never published.
	configPath string
}

// NotFound explains, for a person or an agent, that no JVM ≥ 17 was found, where it was
// looked for and what to set. Each candidate names its source, the configured key naming
// the file it came from (plan m1k D8; plan m1n A10).
func (j JVM) NotFound() string {
	tried := make([]string, 0, len(j.Rejected))
	for _, c := range j.Rejected {
		tried = append(tried, c.Candidate+" ["+j.sourceName(c.Source)+"] ("+c.Reason+")")
	}
	if len(tried) == 0 {
		return "no JVM 17 or newer was found"
	}
	return "no JVM 17 or newer was found. Tried: " + strings.Join(tried, "; ") + ". Set `gradle.sidecarJavaHome` in the droidyard configuration (or JAVA_HOME) to a JDK 17 or newer."
}

// sourceName spells a rejected candidate's source for the human "Tried:" text (plan m1k
// D8; plan m1n A10); the structured Source keeps its constant.
func (j JVM) sourceName(src string) string {
	switch src {
	case SourceSidecarJavaHome:
		if j.configPath != "" {
			return SourceSidecarJavaHome + " in " + j.configPath
		}
		return SourceSidecarJavaHome
	case SourceJavaHome:
		return "$JAVA_HOME"
	case SourcePath:
		return "PATH"
	}
	return src
}

// Finder is where discovery looks and how it validates: the seams of the probes are
// interfaces so a test uses fake directories. The daemon builds it from its environment.
type Finder struct {
	// GOOS overrides runtime.GOOS; "" means runtime.GOOS.
	GOOS string
	// Env is the environment discovery reads (KEY=value), the daemon's own.
	Env []string
	// ReadRelease reads <home>/release; nil reads the real file.
	ReadRelease func(home string) ([]byte, error)
	// JavaVersion runs <home>/bin/java -version and returns its version; nil runs it.
	JavaVersion func(home string) (string, bool)
	// MacJavaHome runs /usr/libexec/java_home -v 17+; nil runs it (macOS only).
	MacJavaHome func() (string, bool)
	// StudioHomes lists Android Studio JBR homes; nil uses the real globs.
	StudioHomes func() []string
	// JavaHome is `gradle.sidecarJavaHome` as read at daemon start: the first candidate
	// (ADR-0032 §3; gradle.md §Sidecar JVM discovery, step 1), or the file that could
	// not be read, when it could not (plan m1k D8). Empty means the file set no key.
	JavaHome policy.ToolKey
}

// Discover returns the first candidate that is a runnable JVM ≥ 17, validated by its
// release file or `java -version`, never assumed (gradle.md §Sidecar JVM discovery;
// ADR-0018). It touches only the file system and, for a candidate without a release
// file, `java -version`.
func Discover(f Finder) JVM {
	if f.GOOS == "" {
		f.GOOS = runtime.GOOS
	}
	if f.ReadRelease == nil {
		f.ReadRelease = readReleaseFile
	}
	if f.JavaVersion == nil {
		f.JavaVersion = runJavaVersion
	}
	if f.MacJavaHome == nil {
		f.MacJavaHome = macJavaHome
	}
	if f.StudioHomes == nil {
		f.StudioHomes = studioHomes
	}
	res := JVM{Rejected: []Rejected{}, configPath: f.JavaHome.ConfigPath}
	reject := func(candidate, source, reason string) {
		res.Rejected = append(res.Rejected, Rejected{Candidate: candidate, Source: source, Reason: reason})
	}
	win := func(home, source string) bool {
		version, reason := validate(f, home)
		if reason != "" {
			reject(home, source, reason)
			return false
		}
		res.Found, res.Home, res.Version, res.Source = true, home, version, source
		return true
	}

	// The file's tool keys were not read at start: no key is used, the file is listed,
	// and discovery goes on to JAVA_HOME and the rest (ADR-0032 §4; plan m1k D8).
	if f.JavaHome.Invalid != "" {
		reject(f.JavaHome.ConfigPath, SourceSidecarJavaHome, "the configuration file is invalid ("+f.JavaHome.Invalid+"); its tool keys were not read")
	}

	// The configured gradle.sidecarJavaHome is the first candidate (ADR-0032 §3;
	// gradle.md §Sidecar JVM discovery, step 1). Validated like every candidate: a path
	// that is not a runnable JVM ≥ 17 is rejected and the next is tried.
	if f.JavaHome.Invalid == "" && f.JavaHome.Path != "" {
		if win(f.JavaHome.Path, SourceSidecarJavaHome) {
			return res
		}
	}

	env := envMap(f.Env)
	if home := env["JAVA_HOME"]; home != "" && !filepath.IsAbs(home) {
		reject(home, SourceJavaHome, "not an absolute path")
	} else if home != "" {
		if win(home, SourceJavaHome) {
			return res
		}
	}
	if java := lookPathJava(env["PATH"]); java != "" {
		real, err := filepath.EvalSymlinks(java)
		if err != nil {
			real = java
		}
		// macOS's /usr/bin/java is a launcher stub, not a JDK: its home would read /usr.
		// It counts only through what it resolves to, which is java_home's answer below
		// (gradle.md §Sidecar JVM discovery, step 3; step 5 review N5).
		if f.GOOS == "darwin" && real == "/usr/bin/java" {
			reject(real, SourcePath, "the macOS java launcher stub, resolved through java_home instead")
		} else if win(filepath.Dir(filepath.Dir(real)), SourcePath) {
			return res
		}
	} else {
		reject("java on PATH", SourcePath, "no java in any PATH entry")
	}
	if f.GOOS == "darwin" {
		if home, ok := f.MacJavaHome(); ok {
			if win(home, SourceJavaHomeTool) {
				return res
			}
		} else {
			reject("/usr/libexec/java_home -v 17+", SourceJavaHomeTool, "no JVM 17 or newer")
		}
	}
	for _, home := range f.StudioHomes() {
		if win(home, SourceStudio) {
			return res
		}
	}
	return res
}

// validate says whether home is a runnable JVM ≥ 17, and its version: the `release`
// file first, `java -version` second.
func validate(f Finder, home string) (version, reason string) {
	fi, err := os.Stat(home)
	switch {
	case err != nil:
		return "", "no such directory"
	case !fi.IsDir():
		return "", "is not a directory"
	}
	if data, err := f.ReadRelease(home); err == nil {
		v := parseRelease(data)
		if v == "" {
			return "", "its release file names no JAVA_VERSION"
		}
		if !atLeast17(v) {
			return "", "Java " + v + " is older than 17"
		}
		return v, ""
	}
	v, ok := f.JavaVersion(home)
	if !ok {
		return "", "not a runnable JVM: no release file and `java -version` failed"
	}
	if !atLeast17(v) {
		return "", "Java " + v + " is older than 17"
	}
	return v, ""
}

// parseRelease reads JAVA_VERSION from a JDK's release file: JAVA_VERSION="17.0.11".
func parseRelease(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), "JAVA_VERSION=")
		if !ok {
			continue
		}
		return strings.Trim(strings.TrimSpace(v), `"`)
	}
	return ""
}

// atLeast17 reports whether a Java version string names 17 or newer.
func atLeast17(v string) bool {
	major := v
	if i := strings.IndexAny(major, "."); i >= 0 {
		major = major[:i]
	}
	n, err := strconv.Atoi(strings.TrimSpace(major))
	return err == nil && n >= 17
}

func readReleaseFile(home string) ([]byte, error) {
	return os.ReadFile(filepath.Join(home, "release"))
}

// javaVersionRe matches `openjdk version "17.0.11"` and `java version "1.8.0_292"`.
var javaVersionPrefix = `version "`

// runJavaVersion runs <home>/bin/java -version and returns its version, from stderr
// (where `java -version` writes it).
func runJavaVersion(home string) (string, bool) {
	out, err := exec.Command(filepath.Join(home, "bin", "java"), "-version").CombinedOutput()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.Index(line, javaVersionPrefix); i >= 0 {
			rest := line[i+len(javaVersionPrefix):]
			if j := strings.Index(rest, `"`); j >= 0 {
				return rest[:j], true
			}
		}
	}
	return "", false
}

// macJavaHome runs /usr/libexec/java_home -v 17+, the macOS JDK registry.
func macJavaHome() (string, bool) {
	out, err := exec.Command("/usr/libexec/java_home", "-v", "17+").Output()
	if err != nil {
		return "", false
	}
	home := strings.TrimSpace(string(out))
	if home == "" {
		return "", false
	}
	return home, true
}

// studioHomes lists Android Studio's bundled JBR homes, macOS and Linux (gradle.md
// §Sidecar JVM discovery, step 5), the JetBrains Toolbox installs included.
func studioHomes() []string {
	home, _ := os.UserHomeDir()
	var patterns []string
	if runtime.GOOS == "darwin" {
		patterns = append(patterns, "/Applications/Android Studio*.app/Contents/jbr/Contents/Home")
		if home != "" {
			patterns = append(patterns,
				filepath.Join(home, "Applications", "Android Studio*.app", "Contents", "jbr", "Contents", "Home"),
				filepath.Join(home, "Library", "Application Support", "JetBrains", "Toolbox", "apps", "AndroidStudio", "ch-*", "*", "Android Studio.app", "Contents", "jbr", "Contents", "Home"),
			)
		}
	} else {
		patterns = append(patterns, "/opt/android-studio/jbr")
		if home != "" {
			patterns = append(patterns, filepath.Join(home, "android-studio", "jbr"),
				filepath.Join(home, ".local", "share", "JetBrains", "Toolbox", "apps", "AndroidStudio", "ch-*", "*", "android-studio", "jbr"))
		}
	}
	var homes []string
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		homes = append(homes, matches...)
	}
	return homes
}

// lookPathJava finds `java` in the PATH value path, as a shell would, without reading
// the process's own PATH.
func lookPathJava(path string) string {
	if path == "" {
		return ""
	}
	for _, d := range filepath.SplitList(path) {
		// A relative entry would resolve against the daemon's working directory, which
		// may be a project's: never run a project's own java (step 5 review N5).
		if d == "" || !filepath.IsAbs(d) {
			continue
		}
		p := filepath.Join(d, "java")
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
