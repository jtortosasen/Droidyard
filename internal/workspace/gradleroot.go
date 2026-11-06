package workspace

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// The Gradle root of a project (ADR-0075 §1, §2): the directory inside the project root
// that holds settings.gradle(.kts), where the Gradle sidecar connects and the build
// capabilities run. Nothing is cached: a call resolves again (a stat and two directory
// reads), so a settings file that appears or goes takes effect on the next call. The
// project file's gradleRoot key (ADR-0075 §2 (a)) left with ADR-0137.

// maxCandidates caps the folders listed.
const maxCandidates = 50

// maxScanDirs bounds rule (c): the directories examined, read or checked for a settings
// file, before the scan stops (ADR-0075 §2). A variable so a test can lower it.
var maxScanDirs = 1000

// examineHook, when set, sees every directory rule (c) examines. A test seam that
// counts the reads a call made; nil in the daemon.
var examineHook func(dir string)

// GradleRoot is the outcome of ResolveGradleRoot.
type GradleRoot struct {
	// Abs is the canonical path of the Gradle root; empty when there is none.
	Abs string
	// Rel is Abs relative to the project root, `/`-separated; "." is the project root
	// itself. Empty when there is none.
	Rel string
	// Candidates are the folders that hold a settings file, relative to the project
	// root, sorted and at most 50, so a failure can say what to pass. Never nil.
	Candidates []string
	// Err, set exactly when Abs is empty, is PROJECT_NO_GRADLE_ROOT with the cause.
	Err *capability.Error
}

// ResolveGradleRoot finds the Gradle root of the project at projectRoot, in ADR-0075 §2's
// order: the project root when it holds a settings file, else the one folder one or two
// levels below that does. A project root that is the home directory or `/` is never
// searched: a Gradle root only by a settings file of its own.
func ResolveGradleRoot(projectRoot string) GradleRoot {
	env := platform.Current()
	root := env.Canonical(projectRoot)

	if isHomeOrFilesystemRoot(env, root) {
		if hasSettings(root) {
			return GradleRoot{Abs: root, Rel: ".", Candidates: []string{"."}}
		}
		return noRoot(root, "the project root is the home directory or the filesystem root, so the folders below it are not searched", nil)
	}

	var candidates []string
	var stopped bool
	if hasSettings(root) {
		candidates = []string{"."}
	} else {
		candidates, stopped = scan(root)
	}

	switch {
	case len(candidates) == 1 && candidates[0] == ".":
		return GradleRoot{Abs: root, Rel: ".", Candidates: candidates}
	case stopped:
		return noRoot(root, fmt.Sprintf("the search stopped after examining %d folders without deciding", maxScanDirs), candidates)
	case len(candidates) == 1:
		return GradleRoot{Abs: filepath.Join(root, filepath.FromSlash(candidates[0])), Rel: candidates[0], Candidates: candidates}
	case len(candidates) == 0:
		return noRoot(root, "no settings.gradle or settings.gradle.kts in the project root or one or two levels below it", candidates)
	default:
		return noRoot(root, fmt.Sprintf("several folders (%d) below the project root hold a settings.gradle or settings.gradle.kts and none is chosen", len(candidates)), candidates)
	}
}

func noRoot(projectRoot, cause string, candidates []string) GradleRoot {
	if candidates == nil {
		candidates = []string{}
	}
	return GradleRoot{Candidates: candidates, Err: noGradleRoot(projectRoot, cause, candidates)}
}

// noGradleRoot is PROJECT_NO_GRADLE_ROOT (ADR-0075 §4): the cause says none, several or
// what is wrong with the key; the candidates are in the details.
func noGradleRoot(projectRoot, cause string, candidates []string) *capability.Error {
	if candidates == nil {
		candidates = []string{}
	}
	return capability.NewError(capability.ErrProjectNoGradleRoot, fmt.Sprintf("the project at %s has no Gradle root to run in", projectRoot)).
		WithCause(cause).
		WithNextStep(noGradleRootNextStep(candidates)).
		WithDetails(map[string]any{"candidates": candidates})
}

// The nextStep of PROJECT_NO_GRADLE_ROOT: the project is the folder of the build, passed
// by its absolute path (ADR-0137 §3).
const (
	nextStepPickCandidate = "pass the absolute path of one of the candidates (details.candidates, relative to the project) as the project"
	nextStepNoCandidate   = "pass the absolute path of a folder that holds settings.gradle or settings.gradle.kts, or of a folder inside one, as the project"
)

func noGradleRootNextStep(candidates []string) string {
	if len(candidates) == 0 {
		return nextStepNoCandidate
	}
	return nextStepPickCandidate
}

// isHomeOrFilesystemRoot reports whether root, canonical, is the home directory
// (canonicalised as the project root is) or `/`.
func isHomeOrFilesystemRoot(env platform.Env, root string) bool {
	if root == string(filepath.Separator) {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	return env.Canonical(home) == root
}

// hasSettings reports whether dir holds a regular settings.gradle or settings.gradle.kts.
func hasSettings(dir string) bool {
	for _, f := range []string{"settings.gradle", "settings.gradle.kts"} {
		if fi, err := os.Stat(filepath.Join(dir, f)); err == nil && fi.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// scan is rule (c): the root's entries and each subdirectory's, skipping hidden
// directories, build, node_modules and every symlink; a directory that holds a settings
// file is a candidate and is not descended into. It stops when it has examined
// maxScanDirs directories, reporting that; the candidates are sorted and capped.
func scan(root string) (candidates []string, stopped bool) {
	n := 0
	examine := func(dir string) bool {
		if n >= maxScanDirs {
			stopped = true
			return false
		}
		n++
		if examineHook != nil {
			examineHook(dir)
		}
		return true
	}
	candidates = []string{}
	defer func() {
		sort.Strings(candidates)
		if len(candidates) > maxCandidates {
			candidates = candidates[:maxCandidates]
		}
	}()

	if !examine(root) {
		return
	}
	for _, e1 := range subdirs(root) {
		d1 := filepath.Join(root, e1)
		if !examine(d1) {
			return
		}
		if hasSettings(d1) {
			candidates = append(candidates, e1)
			continue
		}
		for _, e2 := range subdirs(d1) {
			d2 := filepath.Join(d1, e2)
			if !examine(d2) {
				return
			}
			if hasSettings(d2) {
				candidates = append(candidates, e1+"/"+e2)
			}
		}
	}
	return
}

// subdirs lists the names of dir's subdirectories that the scan may look in: not hidden,
// not build or node_modules, and not a symlink (a link may lead outside the root or
// loop). Errors read as an empty directory.
func subdirs(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.Type()&fs.ModeSymlink != 0 || !e.IsDir() {
			continue
		}
		if strings.HasPrefix(name, ".") || name == "build" || name == "node_modules" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// quoteKey quotes the value for a message, bounded: it is caller or repository content.
func quoteKey(v string) string {
	const max = 100
	if len(v) > max {
		v = v[:max] + "..."
	}
	return fmt.Sprintf("%q", v)
}
