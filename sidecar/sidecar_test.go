package sidecar

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Decision (ADR-0029: a checked-in build artefact is pinned by its digest, D4): the
// embedded jars — the probe jar and the init script included (m1h D4) — are the ones
// sidecar.go's digests name, so a rebuilt jar without its digest, or a digest edited
// without its jar, fails here.
func TestEmbeddedJarsMatchThePinnedDigests(t *testing.T) {
	if got := DepsDigest(); got != DepsSHA256 {
		t.Fatalf("embedded gradle-sidecar-deps.jar has SHA-256 %s, sidecar.go pins %s", got, DepsSHA256)
	}
	if got := Digest(); got != SHA256 {
		t.Fatalf("embedded gradle-sidecar.jar has SHA-256 %s, sidecar.go pins %s", got, SHA256)
	}
	if got := ProbeDigest(); got != ProbeSHA256 {
		t.Fatalf("embedded droidyard-probe.jar has SHA-256 %s, sidecar.go pins %s", got, ProbeSHA256)
	}
	if got := InitDigest(); got != InitSHA256 {
		t.Fatalf("embedded droidyard-probe.init.gradle has SHA-256 %s, sidecar.go pins %s", got, InitSHA256)
	}
	// A jar is a zip: PK\x03\x04 at the front. A guard against an empty embed that
	// somehow also hashed to the pinned value.
	if !bytes.HasPrefix(depsJar, []byte("PK\x03\x04")) || !bytes.HasPrefix(classJar, []byte("PK\x03\x04")) ||
		!bytes.HasPrefix(probeJar, []byte("PK\x03\x04")) {
		t.Errorf("embedded files are not jars (%d, %d and %d bytes)", len(depsJar), len(classJar), len(probeJar))
	}
	if len(initScript) == 0 {
		t.Error("the embedded init script is empty")
	}
}

// Decision (ADR-0023 §Decision 2, 3; plan m1h D4): the probe jar is Java 8 bytecode and
// references no Kotlin, so it loads in an 8.0 daemon on Java 8, and the probe keeps the
// Action overload of onVariants rather than pulling kotlin's Function1 into its constant
// pool. Seen failing with --release 17, and with the Function1 overload of onVariants.
func TestProbeJarIsJava8WithoutKotlin(t *testing.T) {
	zr, err := zip.NewReader(bytes.NewReader(probeJar), int64(len(probeJar)))
	if err != nil {
		t.Fatal(err)
	}
	classes := 0
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		// "References kotlin/" is a byte scan of the entry: the probe's classes carry no
		// Kotlin type, so the substring never appears in a constant pool.
		if bytes.Contains(data, []byte("kotlin/")) {
			t.Errorf("%s references kotlin/", f.Name)
		}
		if !strings.HasSuffix(f.Name, ".class") {
			continue
		}
		classes++
		if len(data) < 8 {
			t.Fatalf("%s is too short to be a class file", f.Name)
		}
		if major := binary.BigEndian.Uint16(data[6:8]); major != 52 {
			t.Errorf("%s has class file major version %d, want 52 (Java 8)", f.Name, major)
		}
	}
	if classes == 0 {
		t.Fatal("the probe jar carries no class files")
	}
}

// Decision (plan m1h D17): the init script logs its marker, the only positive control
// that it ran at all — a Gradle that dropped init-script lifecycle lines would make the
// "never in a build" check vacuous. Seen failing when the marker line is dropped.
func TestProbeInitScriptLogsItsMarker(t *testing.T) {
	if !strings.Contains(string(initScript), "droidyard-probe: applied") {
		t.Fatalf("the init script does not log its marker:\n%s", initScript)
	}
}

// Decision (D4, reproducible build): build.sh, run on the checked-in source with the
// pinned Tooling API and slf4j, gives the embedded jars byte for byte, the probe jar
// included. It skips, naming both versions, when the host's kotlinc or javac is absent or
// differs from the recorded one (D14) — a different compiler may emit different class
// files, which is not a defect.
func TestBuildScriptReproducesTheJars(t *testing.T) {
	kotlinc := findKotlinc()
	if kotlinc == "" {
		t.Skipf("level 2 only: no kotlinc (the jars were built with %s)", Kotlinc)
	}
	host := kotlincVersion(kotlinc)
	if host != Kotlinc {
		t.Skipf("level 2 only: host kotlinc %s, the jars were built with %s", host, Kotlinc)
	}
	javac := findJavac()
	if javac == "" {
		t.Skipf("level 2 only: no javac (the probe jar was built with javac %s)", Javac)
	}
	if host := javacVersion(javac); host != Javac {
		t.Skipf("level 2 only: host javac %s, the probe jar was built with %s", host, Javac)
	}
	out := t.TempDir()
	cmd := exec.Command("sh", "build.sh")
	cmd.Env = append(os.Environ(), "KOTLINC="+kotlinc, "JAVAC="+javac, "OUTDIR="+out)
	if jdk := findJDK(); jdk != "" {
		cmd.Env = append(cmd.Env, "JAVA_HOME="+jdk)
	}
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build.sh: %v\n%s", err, b)
	}
	checkReproduced(t, filepath.Join(out, "gradle-sidecar-deps.jar"), depsJar, "DepsSHA256")
	checkReproduced(t, filepath.Join(out, "gradle-sidecar.jar"), classJar, "SHA256")
	checkReproduced(t, filepath.Join(out, "droidyard-probe.jar"), probeJar, "ProbeSHA256")
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

// findJavac mirrors build.sh's search: $JAVAC, $JAVA_HOME's bin, the discovered JDK, then
// PATH.
func findJavac() string {
	if j := os.Getenv("JAVAC"); j != "" && isExecutable(j) {
		return j
	}
	if h := os.Getenv("JAVA_HOME"); h != "" && isExecutable(filepath.Join(h, "bin", "javac")) {
		return filepath.Join(h, "bin", "javac")
	}
	if h := findJDK(); h != "" && isExecutable(filepath.Join(h, "bin", "javac")) {
		return filepath.Join(h, "bin", "javac")
	}
	if p, err := exec.LookPath("javac"); err == nil {
		return p
	}
	return ""
}

// javacVersion reads `javac -version`'s "javac <version>", as build.sh does. The
// version is the first whitespace-delimited token: `javac -version` ends its line with a
// newline, which a bare [^ ]* would swallow.
func javacVersion(javac string) string {
	out, _ := exec.Command(javac, "-version").CombinedOutput()
	m := regexp.MustCompile(`javac ([0-9][^ \t\r\n]*)`).FindSubmatch(out)
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
