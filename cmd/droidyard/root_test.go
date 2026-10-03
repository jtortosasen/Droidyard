package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveRoot: a shim names an explicit root, else the nearest ancestor of its cwd
// holding settings.gradle or settings.gradle.kts, else its cwd (ADR-0019 §Sessions).
func TestResolveRoot(t *testing.T) {
	base := t.TempDir()
	groovy := filepath.Join(base, "groovy")
	kts := filepath.Join(base, "kts")
	nested := filepath.Join(kts, "included")
	bare := filepath.Join(base, "bare")
	for _, d := range []string{filepath.Join(groovy, "app", "src"), filepath.Join(nested, "lib"), bare} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(groovy, "settings.gradle"), nil, 0o600)
	os.WriteFile(filepath.Join(kts, "settings.gradle.kts"), nil, 0o600)
	os.WriteFile(filepath.Join(nested, "settings.gradle.kts"), nil, 0o600)
	// A directory named like the file does not make a root.
	os.MkdirAll(filepath.Join(bare, "settings.gradle"), 0o700)

	cases := []struct {
		name, explicit, cwd, want string
	}{
		{"settings.gradle two levels up", "", filepath.Join(groovy, "app", "src"), groovy},
		{"settings.gradle.kts in the cwd", "", kts, kts},
		{"the nearest ancestor wins", "", filepath.Join(nested, "lib"), nested},
		{"no ancestor holds one: the cwd", "", bare, bare},
		{"an explicit root wins", groovy, filepath.Join(nested, "lib"), groovy},
		{"a relative explicit root is made absolute against the cwd", "../groovy", bare, groovy},
	}
	for _, c := range cases {
		if got := resolveRoot(c.explicit, c.cwd); got != c.want {
			t.Errorf("%s: resolveRoot(%q, %q) = %q; want %q", c.name, c.explicit, c.cwd, got, c.want)
		}
	}
}
