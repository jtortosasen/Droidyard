package gradle

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/jtortosasen/droidyard/sidecar"
)

// Jars is the extracted classpath: where the sidecar's two jars were written.
type Jars struct {
	Deps  string `json:"deps"`
	Class string `json:"class"`
}

// cacheJars writes the embedded sidecar jars under dir, named by their digest, and
// returns their paths (plan m1g D5). A file that is already there with the right name
// and the same bytes is left alone, so a second daemon over the same cache writes nothing; a
// different digest is a different name, so an upgrade never races the old jar. Each
// file is written to a temporary name and renamed, so a reader never sees a partial
// jar.
func cacheJars(dir string) (Jars, error) {
	sub := filepath.Join(dir, "gradle")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		return Jars{}, err
	}
	deps, err := writeJar(sub, "deps-"+sidecar.DepsDigest()+".jar", sidecar.DepsJar())
	if err != nil {
		return Jars{}, err
	}
	class, err := writeJar(sub, "sidecar-"+sidecar.Digest()+".jar", sidecar.Jar())
	if err != nil {
		return Jars{}, err
	}
	return Jars{Deps: deps, Class: class}, nil
}

func writeJar(dir, name string, data []byte) (string, error) {
	path := filepath.Join(dir, name)
	// The bytes, not the size: a cached jar altered in place is rewritten, never run
	// (D5; step 5 review B2). Reading 5 MB costs milliseconds, on the fetch path only.
	if have, err := os.ReadFile(path); err == nil && bytes.Equal(have, data) {
		return path, nil
	}
	tmp, err := os.CreateTemp(dir, name+".*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}
