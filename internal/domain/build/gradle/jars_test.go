package gradle

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/sidecar"
)

// Decision (D5; step 5 review B2): a cached jar is checked by its bytes, so one altered
// in place with the same size is rewritten before the sidecar runs it. Seen failing
// with the size-only check.
func TestCachedJarAlteredInPlaceIsRewritten(t *testing.T) {
	dir := t.TempDir()
	jars, err := cacheJars(dir)
	if err != nil {
		t.Fatal(err)
	}
	garbage := bytes.Repeat([]byte{0xAA}, len(sidecar.Jar()))
	if err := os.WriteFile(jars.Class, garbage, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := cacheJars(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(jars.Class)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, sidecar.Jar()) {
		t.Fatal("a same-size altered jar was left in the cache")
	}
}

// Decision (plan m1h D4): the probe's jar and init script land together in
// <cache>/gradle/probe-<digest>/, because the init script finds the jar beside itself;
// each is checked by its own bytes, so a same-size altered one is rewritten. Seen failing
// with the init script written where the class jar is, and with a size-only check.
func TestProbeFilesLandBesideTheirDigestAndAreRewritten(t *testing.T) {
	dir := t.TempDir()
	jars, err := cacheJars(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "gradle", "probe-"+sidecar.ProbeDigest())
	if filepath.Dir(jars.Probe) != want || filepath.Dir(jars.Init) != want {
		t.Fatalf("the probe's files are not in %s: jar %s, init %s", want, jars.Probe, jars.Init)
	}
	if got, err := os.ReadFile(jars.Probe); err != nil || !bytes.Equal(got, sidecar.ProbeJar()) {
		t.Fatalf("the cached probe jar is not the embedded one: %v", err)
	}
	if got, err := os.ReadFile(jars.Init); err != nil || !bytes.Equal(got, sidecar.InitScript()) {
		t.Fatalf("the cached init script is not the embedded one: %v", err)
	}
	garbage := bytes.Repeat([]byte{'x'}, len(sidecar.InitScript()))
	if err := os.WriteFile(jars.Init, garbage, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := cacheJars(dir); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(jars.Init); err != nil || !bytes.Equal(got, sidecar.InitScript()) {
		t.Fatal("a same-size altered init script was left in the cache")
	}
}

// Decision (plan m1h D7): the launch line carries the probe jar on the classpath — the
// sidecar holds the BuildAction's class — and the init script's path as
// -Ddroidyard.probe.init, because only the cache knows the probe's directory. Seen
// failing with the probe jar off the classpath.
func TestLaunchCarriesTheProbeJarAndItsInitScript(t *testing.T) {
	dir := t.TempDir()
	c := &Client{opts: Options{CacheDir: dir}}
	_, args, _, err := c.launch(JVM{Found: true, Home: "/nonexistent-jvm"})
	if err != nil {
		t.Fatal(err)
	}
	jars, err := cacheJars(dir)
	if err != nil {
		t.Fatal(err)
	}
	var classpath string
	for i, a := range args {
		if a == "-cp" && i+1 < len(args) {
			classpath = args[i+1]
		}
	}
	if classpath == "" {
		t.Fatalf("the launch line has no -cp: %v", args)
	}
	for _, want := range []string{jars.Deps, jars.Class, jars.Probe} {
		if !strings.Contains(classpath, want) {
			t.Errorf("the classpath does not carry %s: %q", want, classpath)
		}
	}
	property := "-Ddroidyard.probe.init=" + jars.Init
	found := false
	for _, a := range args {
		if a == property {
			found = true
		}
	}
	if !found {
		t.Errorf("the launch line lacks %s: %v", property, args)
	}
}

// Decision (step 5 review N5): a relative PATH entry is never searched, so a daemon
// started inside a project never runs that project's own java.
func TestLookPathJavaSkipsRelativeEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/bin", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/bin/java", []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if got := lookPathJava("bin"); got != "" {
		t.Fatalf("relative PATH entry searched: %q", got)
	}
	if got := lookPathJava(dir + "/bin"); got != dir+"/bin/java" {
		t.Fatalf("absolute entry: %q", got)
	}
}
