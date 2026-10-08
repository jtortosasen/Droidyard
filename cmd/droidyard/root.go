package main

import (
	"os"
	"path/filepath"
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
	if dir, ok := gradleRoot(cwd); ok {
		return dir
	}
	return filepath.Clean(cwd)
}

// gradleRoot is the nearest ancestor of cwd, cwd included, that holds settings.gradle or
// settings.gradle.kts.
func gradleRoot(cwd string) (string, bool) {
	for dir := filepath.Clean(cwd); ; {
		for _, f := range []string{"settings.gradle", "settings.gradle.kts"} {
			if fi, err := os.Stat(filepath.Join(dir, f)); err == nil && !fi.IsDir() {
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
