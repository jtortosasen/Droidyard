package workspace

import (
	"fmt"
	"path/filepath"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// projectNextStep is what a project that cannot be resolved is told to pass.
const projectNextStep = "pass the absolute path of the project's folder, or of a folder inside it"

// ResolveProject is the project root a call's project names (ADR-0137 §3; plan m3j Q2),
// canonical: the nearest ancestor of project, itself included, that holds a
// settings.gradle(.kts); else project itself when exactly one Gradle build lies one or two
// levels below it (ADR-0075 §2 (c)). The home directory and `/` are a project only by a
// settings file of their own, and are never searched below. Trust, the workspace id and
// every published path are that root's.
//
// It reads a stat and a directory listing per ancestor and per folder the search
// examines, at most maxScanDirs of them; it runs nothing. It does not consult the project
// file, which names a Gradle root inside a project root the caller has already chosen.
//
// project must be absolute and an existing, readable directory, else the error is
// REQUEST_INVALID_PARAMS; a project with no Gradle root, or several, is
// PROJECT_NO_GRADLE_ROOT with the candidates in its details.
func ResolveProject(env platform.Env, project string) (string, *capability.Error) {
	if !filepath.IsAbs(project) {
		return "", invalidProject(project, "is not an absolute path")
	}
	// Symlinks are resolved first, so the walk up follows the project's real ancestors and
	// every spelling of one folder reaches one root.
	root := env.Canonical(filepath.Clean(project))
	if err := readableDir(root); err != nil {
		return "", invalidProject(project, err.Error())
	}
	for dir := root; ; {
		if hasSettings(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if isHomeOrFilesystemRoot(env, root) {
		return "", noGradleRoot(root, "the folder is the home directory or the filesystem root, so the folders below it are not searched", nil)
	}
	candidates, stopped := scan(root)
	switch {
	case stopped:
		return "", noGradleRoot(root, fmt.Sprintf("the search stopped after examining %d folders without deciding", maxScanDirs), candidates)
	case len(candidates) == 1:
		return root, nil
	case len(candidates) == 0:
		return "", noGradleRoot(root, "no settings.gradle or settings.gradle.kts in the folder, above it, or one or two levels below it", candidates)
	default:
		return "", noGradleRoot(root, fmt.Sprintf("several folders (%d) below the folder hold a settings.gradle or settings.gradle.kts and none contains it", len(candidates)), candidates)
	}
}

// invalidProject is REQUEST_INVALID_PARAMS for a project that cannot name a folder. The
// text is project content, bounded.
func invalidProject(project, cause string) *capability.Error {
	return capability.Errorf(capability.ErrInvalidParams, "the project %s cannot be used: %s", quoteKey(project), cause).
		WithNextStep(projectNextStep).
		WithDetails(map[string]any{"project": project})
}
