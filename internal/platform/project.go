package platform

import (
	"os"
	"path/filepath"
)

// GradleRoot is the nearest ancestor of dir, dir included, that holds settings.gradle
// or settings.gradle.kts: the project root every entry point derives by one rule (the
// MCP binding, ADR-0019 §Sessions; `droidyard ui`, a folder the dialog chose, ADR-0045).
func GradleRoot(dir string) (string, bool) {
	for dir = filepath.Clean(dir); ; {
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
