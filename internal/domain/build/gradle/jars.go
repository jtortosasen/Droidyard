package gradle

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/jtortosasen/droidyard/sidecar"
)

// Jars is the extracted classpath: where the sidecar's jars and the probe were written
// (plan m1g D5; m1h D4).
type Jars struct {
	// Deps, Class and Probe are the three jars of the sidecar's launch classpath.
	Deps  string `json:"deps"`
	Class string `json:"class"`
	Probe string `json:"probe"`
	// Init is the probe's init script, which finds the probe jar beside itself.
	Init string `json:"init"`
}

// cacheJars writes the embedded sidecar jars and the probe under dir, named by their
// digest, and returns their paths (plan m1g D5; m1h D4). A file that is already there
// with the same bytes is left alone, so a second daemon over the same cache writes
// nothing; a different digest is a different name, so an upgrade never races the old
// file. Each file is written to a temporary name and renamed, so a reader never sees a
// partial jar.
//
// The probe's two files share one directory, `probe-<probe jar digest>`: the init script
// resolves the jar beside itself (m1h D4), so the two have to move together. Each is
// still checked by its own bytes, so an altered file is rewritten even when the
// directory it lives in already exists.
func cacheJars(dir string) (Jars, error) {
	sub := filepath.Join(dir, "gradle")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		return Jars{}, err
	}
	deps, err := writeCached(sub, "deps-"+sidecar.DepsDigest()+".jar", sidecar.DepsJar())
	if err != nil {
		return Jars{}, err
	}
	class, err := writeCached(sub, "sidecar-"+sidecar.Digest()+".jar", sidecar.Jar())
	if err != nil {
		return Jars{}, err
	}
	probeDir := filepath.Join(sub, "probe-"+sidecar.ProbeDigest())
	if err := os.MkdirAll(probeDir, 0o755); err != nil {
		return Jars{}, err
	}
	probe, err := writeCached(probeDir, "droidyard-probe.jar", sidecar.ProbeJar())
	if err != nil {
		return Jars{}, err
	}
	init, err := writeCached(probeDir, "droidyard-probe.init.gradle", sidecar.InitScript())
	if err != nil {
		return Jars{}, err
	}
	return Jars{Deps: deps, Class: class, Probe: probe, Init: init}, nil
}

func writeCached(dir, name string, data []byte) (string, error) {
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
