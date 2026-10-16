package main

import (
	"path/filepath"

	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/workspace"
)

// resolveRoot is the project root a shim names to the daemon (ADR-0019 §Sessions): an
// explicit root, made absolute against cwd; else the nearest ancestor of cwd, cwd
// included, that holds settings.gradle or settings.gradle.kts; else cwd. It is sent as
// resolved: only the daemon canonicalises it, so the CLI, MCP and the core derive one
// workspace id (plan m1c, step 4).
func resolveRoot(explicit, cwd string) string {
	if explicit != "" {
		if filepath.IsAbs(explicit) {
			return filepath.Clean(explicit)
		}
		return filepath.Join(cwd, explicit)
	}
	if dir, ok := platform.GradleRoot(cwd); ok {
		return dir
	}
	return filepath.Clean(cwd)
}

// projectOf is the project `ui` opens and `init` writes into when the user names none
// (ADR-0075 §6): the nearest ancestor of cwd, cwd included, with a settings.gradle(.kts)
// (unchanged); else cwd itself when it is the top of a repository, that is when its
// Gradle root resolves (a valid gradleRoot key, or exactly one build one or two levels
// below). Otherwise there is none. The home directory and `/` never resolve, so they
// are never a project. resolveRoot is not this: it never fails, and `mcp`, `call` and
// `trash` name the daemon's workspace by it.
func projectOf(cwd string) (string, bool) {
	if dir, ok := platform.GradleRoot(cwd); ok {
		return dir, true
	}
	if workspace.ResolveGradleRoot(cwd, false).Err == nil {
		return filepath.Clean(cwd), true
	}
	return "", false
}
