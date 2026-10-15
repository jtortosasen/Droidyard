package workspace

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// The Gradle root of a project (ADR-0075 §1, §2): the directory inside the project root
// that holds settings.gradle(.kts), where the Gradle sidecar connects and the build
// capabilities run. Nothing is cached: a call resolves again (a stat and two directory
// reads), so a choice written to the project file takes effect on the next call.

const (
	// projectFile is the UI's project file (ADR-0046 §1), read for its gradleRoot key.
	projectFile = ".droidyard/project.json"
	// maxProjectFile bounds what is read of the project file, which is repository content.
	maxProjectFile = 256 << 10
	// maxCandidates caps the folders listed.
	maxCandidates = 50
)

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
	// root, sorted and at most 50: listed even when a key decided or failed, so a choice
	// can be changed or repaired. Never nil.
	Candidates []string
	// Err, set exactly when Abs is empty, is PROJECT_NO_GRADLE_ROOT with the cause.
	Err *capability.Error
}

// ResolveGradleRoot finds the Gradle root of the project at projectRoot, in ADR-0075
// §2's order: the gradleRoot key of the project file, else the project root when it
// holds a settings file, else the one folder one or two levels below that does. A key
// that is not valid never falls back to the others. withCandidates false lets a valid
// key answer without listing the candidates; a call that ends in an error lists them
// whatever it says, as the error's details need them.
//
// The project file is repository content: its value only ever selects a folder inside
// the canonical project root (the trust the project root already has), and nothing
// outside it is read. A project root that is the home directory or `/` is never
// searched or consulted for a key: an MCP binding may fall back to either
// (ADR-0019 §Sessions).
func ResolveGradleRoot(projectRoot string, withCandidates bool) GradleRoot {
	env := platform.Current()
	root := env.Canonical(projectRoot)

	if isHomeOrFilesystemRoot(env, root) {
		if hasSettings(root) {
			return GradleRoot{Abs: root, Rel: ".", Candidates: []string{"."}}
		}
		return noRoot(root, "the project root is the home directory or the filesystem root, so the folders below it are not searched", nil)
	}

	var key *keyResult
	if raw, ok := readGradleRootKey(root); ok {
		k := validateKey(env, root, raw)
		key = &k
		if k.reason == "" && !withCandidates {
			return GradleRoot{Abs: k.abs, Rel: k.rel, Candidates: []string{k.rel}}
		}
	}

	var candidates []string
	var stopped bool
	if hasSettings(root) {
		candidates = []string{"."}
	} else {
		candidates, stopped = scan(root)
	}

	if key != nil {
		if key.reason != "" {
			return noRoot(root, key.reason, candidates)
		}
		return GradleRoot{Abs: key.abs, Rel: key.rel, Candidates: withKey(candidates, key.rel)}
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
		WithNextStep(fmt.Sprintf(`Choose the Gradle build in the Droidyard UI, or set "gradleRoot" in %s to the folder, relative to the project root, that holds settings.gradle(.kts).`, filepath.Join(projectRoot, projectFile))).
		WithDetails(map[string]any{"candidates": candidates})
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

// withKey adds the key's folder to the candidates when it is not among them, sorted; at
// the cap, it takes the last place, so the folder in use is always listed.
func withKey(candidates []string, rel string) []string {
	for _, c := range candidates {
		if c == rel {
			return candidates
		}
	}
	out := append([]string{}, candidates...)
	if len(out) >= maxCandidates {
		out = out[:maxCandidates-1]
	}
	out = append(out, rel)
	sort.Strings(out)
	return out
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

// readGradleRootKey returns the gradleRoot value of the project file. It is absent
// (false) when the file is missing, leaves the root, is not a regular file, is over
// 256 KiB, does not parse, is not schemaVersion 1 (ADR-0046 §1: newer is absent) or when
// the value is not a string. The read goes through os.Root, so a .droidyard or file that
// is a symlink out of the root is not followed.
func readGradleRootKey(root string) (string, bool) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return "", false
	}
	defer r.Close()
	// Opened without blocking, so a FIFO swapped in cannot hang the call, and then checked
	// to be a regular file.
	f, err := r.OpenFile(projectFile, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	data, err := io.ReadAll(io.LimitReader(f, maxProjectFile+1))
	if err != nil || len(data) > maxProjectFile {
		return "", false
	}
	var doc struct {
		SchemaVersion json.RawMessage `json:"schemaVersion"`
		GradleRoot    json.RawMessage `json:"gradleRoot"`
	}
	if json.Unmarshal(data, &doc) != nil || string(doc.SchemaVersion) != "1" {
		return "", false
	}
	var value string
	if string(doc.GradleRoot) == "null" || json.Unmarshal(doc.GradleRoot, &value) != nil {
		return "", false
	}
	return value, true
}

// keyResult is a gradleRoot value checked: the folder it names, or why it is not usable.
type keyResult struct {
	abs, rel string
	reason   string
}

// validateKey checks a gradleRoot value against the canonical project root: relative,
// no `..` segment, and, once joined and its symlinks resolved, a directory inside the
// root, at a path-component boundary, holding a regular settings file.
func validateKey(env platform.Env, root, value string) keyResult {
	bad := func(why string) keyResult {
		return keyResult{reason: fmt.Sprintf("gradleRoot %s in .droidyard/project.json %s", quoteKey(value), why)}
	}
	switch {
	case value == "":
		return bad("is empty")
	case strings.HasPrefix(value, "/") || filepath.IsAbs(value):
		return bad("is not a relative path")
	}
	for _, seg := range strings.Split(value, "/") {
		if seg == ".." {
			return bad(`has a ".." segment`)
		}
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(value)))
	if err != nil {
		return bad("does not name an existing folder")
	}
	abs := env.Canonical(resolved)
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return bad("leads outside the project root")
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return bad("is not a folder")
	}
	if !hasSettings(abs) {
		return bad("holds no settings.gradle or settings.gradle.kts")
	}
	return keyResult{abs: abs, rel: filepath.ToSlash(rel)}
}

// quoteKey quotes the value for a message, bounded: it is repository content.
func quoteKey(v string) string {
	const max = 100
	if len(v) > max {
		v = v[:max] + "..."
	}
	return fmt.Sprintf("%q", v)
}
