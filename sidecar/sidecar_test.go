package sidecar

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Decision (ADR-0029: a checked-in build artefact is pinned by its digest, D4): the
// embedded jars are the ones sidecar.go's digests name, so a rebuilt jar without its
// digest, or a digest edited without its jar, fails here.
func TestEmbeddedJarsMatchThePinnedDigests(t *testing.T) {
	if got := DepsDigest(); got != DepsSHA256 {
		t.Fatalf("embedded gradle-sidecar-deps.jar has SHA-256 %s, sidecar.go pins %s", got, DepsSHA256)
	}
	if got := Digest(); got != SHA256 {
		t.Fatalf("embedded gradle-sidecar.jar has SHA-256 %s, sidecar.go pins %s", got, SHA256)
	}
	// A jar is a zip: PK\x03\x04 at the front. A guard against an empty embed that
	// somehow also hashed to the pinned value.
	if !bytes.HasPrefix(depsJar, []byte("PK\x03\x04")) || !bytes.HasPrefix(classJar, []byte("PK\x03\x04")) {
		t.Errorf("embedded files are not jars (%d and %d bytes)", len(depsJar), len(classJar))
	}
}

// Decision (D4, reproducible build): build.sh, run on the checked-in source with the
// pinned Tooling API and slf4j, gives the embedded jars byte for byte. It skips, naming
// both versions, when the host's kotlinc is absent or differs from the recorded one
// (D14) — a different compiler may emit different class files, which is not a defect.
func TestBuildScriptReproducesTheJars(t *testing.T) {
	kotlinc := findKotlinc()
	if kotlinc == "" {
		t.Skipf("level 2 only: no kotlinc (the jars were built with %s)", Kotlinc)
	}
	host := kotlincVersion(kotlinc)
	if host != Kotlinc {
		t.Skipf("level 2 only: host kotlinc %s, the jars were built with %s", host, Kotlinc)
	}
	out := t.TempDir()
	cmd := exec.Command("sh", "build.sh")
	cmd.Env = append(os.Environ(), "KOTLINC="+kotlinc, "OUTDIR="+out)
	if jdk := findJDK(); jdk != "" {
		cmd.Env = append(cmd.Env, "JAVA_HOME="+jdk)
	}
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build.sh: %v\n%s", err, b)
	}
	checkReproduced(t, filepath.Join(out, "gradle-sidecar-deps.jar"), depsJar, "DepsSHA256")
	checkReproduced(t, filepath.Join(out, "gradle-sidecar.jar"), classJar, "SHA256")
}

func checkReproduced(t *testing.T, path string, want []byte, pin string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("build.sh's %s differs from the embedded one (%d against %d bytes): rebuild the jars and update %s",
			filepath.Base(path), len(got), len(want), pin)
	}
}

// findKotlinc mirrors build.sh's search: $KOTLINC, Android Studio's bundle, then PATH.
func findKotlinc() string {
	if k := os.Getenv("KOTLINC"); k != "" && isExecutable(k) {
		return k
	}
	home, _ := os.UserHomeDir()
	for _, c := range []string{
		"/Applications/Android Studio.app/Contents/plugins/Kotlin/kotlinc/bin/kotlinc",
		filepath.Join(home, "Applications/Android Studio.app/Contents/plugins/Kotlin/kotlinc/bin/kotlinc"),
		filepath.Join(home, "Library/Application Support/JetBrains/Toolbox/apps/AndroidStudio/ch-0/plugins/Kotlin/kotlinc/bin/kotlinc"),
	} {
		if isExecutable(c) {
			return c
		}
	}
	if p, err := exec.LookPath("kotlinc"); err == nil {
		return p
	}
	return ""
}

// kotlincVersion reads `kotlinc -version`'s "kotlinc-jvm <version>", as build.sh does.
func kotlincVersion(kotlinc string) string {
	out, _ := exec.Command(kotlinc, "-version").CombinedOutput()
	m := regexp.MustCompile(`kotlinc-jvm ([0-9][^ ]*)`).FindSubmatch(out)
	if m == nil {
		return "unknown"
	}
	return string(m[1])
}

// findJDK returns a JDK ≥ 17's home, preferring exactly 17 (the target the jars were
// built for and the version the opt-in test names), or "" when none is found.
func findJDK() string {
	if h := os.Getenv("JAVA_HOME"); h != "" && isExecutable(filepath.Join(h, "bin", "java")) {
		return h
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("/usr/libexec/java_home", "-v", "17").Output(); err == nil {
			if h := strings.TrimSpace(string(out)); h != "" {
				return h
			}
		}
	}
	if jp, err := exec.LookPath("java"); err == nil {
		if real, err := filepath.EvalSymlinks(jp); err == nil {
			if home := filepath.Dir(filepath.Dir(real)); isExecutable(filepath.Join(home, "bin", "java")) {
				return home
			}
		}
	}
	return ""
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}
