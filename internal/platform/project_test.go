package platform

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGradleRoot: the project root is the nearest ancestor, the directory itself
// included, that holds a settings.gradle or settings.gradle.kts file (ADR-0019 §Sessions);
// a directory of that name is not one, and no ancestor is no root.
func TestGradleRoot(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	nested := filepath.Join(proj, "included")
	deep := filepath.Join(nested, "lib", "src")
	bare := filepath.Join(base, "bare", "settings.gradle") // a directory named like the file
	for _, d := range []string{deep, bare} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(proj, "settings.gradle"), nil, 0o600)
	os.WriteFile(filepath.Join(nested, "settings.gradle.kts"), nil, 0o600)
	for _, c := range []struct {
		dir, want string
		ok        bool
	}{
		{deep, nested, true},
		{nested, nested, true},
		{proj, proj, true},
		{bare, "", false},
	} {
		if got, ok := GradleRoot(c.dir); ok != c.ok || (ok && got != c.want) {
			t.Errorf("GradleRoot(%q) = %q, %v; want %q, %v", c.dir, got, ok, c.want, c.ok)
		}
	}
}
