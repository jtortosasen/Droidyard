package gradle_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle/gradletest"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// TestMain runs the fake sidecar when this binary is re-executed in its mode, so the
// client tests spawn a real process without a JVM or Gradle (plan m1g step 5).
func TestMain(m *testing.M) {
	gradletest.Main()
	os.Exit(m.Run())
}

// jdk makes a fake JDK directory whose release file names version; version "" makes a
// directory with no release file at all.
func jdk(t *testing.T, parent, name, version string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if version != "" {
		release := "IMPLEMENTOR=\"Test\"\nJAVA_VERSION=\"" + version + "\"\n"
		if err := os.WriteFile(filepath.Join(dir, "release"), []byte(release), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestDiscoverPrefersJavaHomeThenPath pins the discovery order of gradle.md §Sidecar
// JVM discovery (plan m1g D22): JAVA_HOME first, then the java on PATH, then the
// macOS registry, then Android Studio's JBR. Seen failing with the order reversed.
func TestDiscoverPrefersJavaHomeThenPath(t *testing.T) {
	dir := t.TempDir()
	home := jdk(t, dir, "java-home", "21.0.1")
	pathJDK := jdk(t, dir, "path-jdk", "17.0.11")
	studio := jdk(t, dir, "studio", "17.0.9")
	if err := os.MkdirAll(filepath.Join(pathJDK, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pathJDK, "bin", "java"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	macTool := jdk(t, dir, "mac-tool", "17.0.5")

	noProbes := func() []string { return []string{studio} }
	base := gradle.Finder{
		GOOS:        "linux",
		Env:         []string{"JAVA_HOME=" + home, "PATH=" + filepath.Join(pathJDK, "bin")},
		StudioHomes: noProbes,
	}
	if got := gradle.Discover(base); !got.Found || got.Home != home || got.Version != "21.0.1" || got.Source != gradle.SourceJavaHome {
		t.Fatalf("with JAVA_HOME set: %+v, want the JAVA_HOME candidate", got)
	}

	// Without JAVA_HOME the PATH's java wins, its home taken from its parent's parent
	// (symlinks resolved, so /var becomes /private/var on macOS).
	pathOnly := base
	pathOnly.Env = []string{"PATH=" + filepath.Join(pathJDK, "bin")}
	resolved, err := filepath.EvalSymlinks(pathJDK)
	if err != nil {
		t.Fatal(err)
	}
	if got := gradle.Discover(pathOnly); !got.Found || got.Home != resolved || got.Version != "17.0.11" || got.Source != gradle.SourcePath {
		t.Fatalf("with only PATH: %+v, want the PATH candidate", got)
	}

	// On macOS the registry comes before Android Studio.
	mac := gradle.Finder{
		GOOS: "darwin", Env: []string{"PATH="},
		MacJavaHome: func() (string, bool) { return macTool, true },
		StudioHomes: noProbes,
	}
	if got := gradle.Discover(mac); !got.Found || got.Home != macTool || got.Source != gradle.SourceJavaHomeTool {
		t.Fatalf("on macOS: %+v, want /usr/libexec/java_home's candidate", got)
	}

	// With neither, the JBR is the last resort.
	base.Env = []string{"PATH="}
	if got := gradle.Discover(base); !got.Found || got.Home != studio || got.Source != gradle.SourceStudio {
		t.Fatalf("with only Android Studio: %+v, want the studio candidate", got)
	}
}

// TestDiscoverRejectsOlderAndBrokenCandidates pins that each candidate is validated by
// its release file or `java -version`, never assumed (gradle.md §Sidecar JVM
// discovery; ADR-0018): an 11 is too old and a broken one is unusable, both rejected
// with a reason, and a machine with nothing usable reports every candidate it rejected.
func TestDiscoverRejectsOlderAndBrokenCandidates(t *testing.T) {
	dir := t.TempDir()
	eleven := jdk(t, dir, "jdk11", "11.0.2")
	bad := jdk(t, dir, "broken", "")
	badBin := filepath.Join(bad, "bin")
	if err := os.MkdirAll(badBin, 0o755); err != nil {
		t.Fatal(err)
	}
	// A java that exists and runs but answers nothing: neither a release file nor a
	// runnable JVM.
	if err := os.WriteFile(filepath.Join(badBin, "java"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := gradle.Discover(gradle.Finder{
		GOOS:        "linux",
		Env:         []string{"JAVA_HOME=" + eleven, "PATH=" + badBin},
		StudioHomes: func() []string { return nil },
	})
	if got.Found {
		t.Fatalf("with only an 11 and a broken JVM, discovery found %+v", got)
	}
	var reasons []string
	for _, r := range got.Rejected {
		reasons = append(reasons, r.Source+": "+r.Reason)
	}
	joined := strings.Join(reasons, " | ")
	if !strings.Contains(joined, gradle.SourceJavaHome) || !strings.Contains(joined, "older than 17") {
		t.Errorf("the JDK 11 is not rejected with its reason: %s", joined)
	}
	if !strings.Contains(joined, gradle.SourcePath) || !strings.Contains(joined, "not a runnable JVM") {
		t.Errorf("the broken JVM is not rejected with its reason: %s", joined)
	}
	if n := got.NotFound(); !strings.Contains(n, "17") || !strings.Contains(n, "JAVA_HOME") {
		t.Errorf("NotFound() = %q, want the floor and what to set", n)
	}
}

// TestDiscoverPrefersTheConfiguredJavaHome pins ADR-0032 §3 and D8: `gradle.sidecarJavaHome`
// is the first JVM candidate, ahead of JAVA_HOME (gradle.md §Sidecar JVM discovery, step
// 1); a configured home that is a JVM below the floor is rejected naming the key, and
// JAVA_HOME wins. Seen failing with JAVA_HOME before the key.
func TestDiscoverPrefersTheConfiguredJavaHome(t *testing.T) {
	dir := t.TempDir()
	configured := jdk(t, dir, "configured", "17.0.11")
	javaHome := jdk(t, dir, "java-home", "21.0.1")
	base := gradle.Finder{
		GOOS:        "linux",
		Env:         []string{"JAVA_HOME=" + javaHome, "PATH="},
		StudioHomes: func() []string { return nil },
		JavaHome:    policy.ToolKey{Path: configured},
	}
	if got := gradle.Discover(base); !got.Found || got.Home != configured || got.Version != "17.0.11" || got.Source != gradle.SourceSidecarJavaHome {
		t.Fatalf("with the key set: %+v, want the configured candidate", got)
	}

	// A configured home below the floor is rejected naming the key, and JAVA_HOME wins.
	eleven := jdk(t, dir, "eleven", "11.0.2")
	below := base
	below.JavaHome = policy.ToolKey{Path: eleven}
	got := gradle.Discover(below)
	if !got.Found || got.Home != javaHome || got.Source != gradle.SourceJavaHome {
		t.Fatalf("with an 11 configured: %+v, want JAVA_HOME after the rejected key", got)
	}
	var rejected string
	for _, r := range got.Rejected {
		if r.Candidate == eleven && r.Source == gradle.SourceSidecarJavaHome {
			rejected = r.Reason
		}
	}
	if !strings.Contains(rejected, "older than 17") {
		t.Errorf("the key's rejection = %q; want it recorded with its reason", rejected)
	}
}

// TestDiscoverInvalidConfigurationNamesTheFile pins D8's invalid-file entry: with an
// unusable configuration no key is used and the file is listed as rejected with
// `gradle.sidecarJavaHome` as its source, while a later candidate still wins.
func TestDiscoverInvalidConfigurationNamesTheFile(t *testing.T) {
	dir := t.TempDir()
	javaHome := jdk(t, dir, "java-home", "17.0.11")
	configPath := filepath.Join(dir, "config.json")
	got := gradle.Discover(gradle.Finder{
		GOOS:        "linux",
		Env:         []string{"JAVA_HOME=" + javaHome, "PATH="},
		StudioHomes: func() []string { return nil },
		JavaHome:    policy.ToolKey{ConfigPath: configPath, Invalid: "gradle.sidecarJavaHome: not an absolute path"},
	})
	if !got.Found || got.Source != gradle.SourceJavaHome {
		t.Fatalf("Discover = %+v; want JAVA_HOME despite the bad file", got)
	}
	var named bool
	for _, r := range got.Rejected {
		if r.Candidate == configPath && r.Source == gradle.SourceSidecarJavaHome && strings.Contains(r.Reason, "the configuration file is invalid") {
			named = true
		}
	}
	if !named {
		t.Errorf("rejected = %+v; want the invalid file with source %s", got.Rejected, gradle.SourceSidecarJavaHome)
	}
}

// TestDiscoverValidatesByJavaVersionWhenThereIsNoReleaseFile pins the second
// validation path: a candidate with no release file is accepted when `java -version`
// answers 17 or newer, and refused when it answers older.
func TestDiscoverValidatesByJavaVersionWhenThereIsNoReleaseFile(t *testing.T) {
	dir := t.TempDir()
	home := jdk(t, dir, "no-release", "")
	f := gradle.Finder{
		GOOS:        "linux",
		Env:         []string{"JAVA_HOME=" + home, "PATH="},
		JavaVersion: func(string) (string, bool) { return "17.0.15", true },
		StudioHomes: func() []string { return nil },
	}
	if got := gradle.Discover(f); !got.Found || got.Version != "17.0.15" {
		t.Fatalf("a JVM whose `java -version` answers 17 is not accepted: %+v", got)
	}
	f.JavaVersion = func(string) (string, bool) { return "1.8.0_292", true }
	got := gradle.Discover(f)
	if got.Found || !strings.Contains(got.NotFound(), "older than 17") {
		t.Fatalf("a JVM whose `java -version` answers 8 is accepted: %+v", got)
	}
}
