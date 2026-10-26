// Package sdktest is a fake avdmanager for tests (plan m3f step 6): a shell script standing in
// for cmdline-tools' bin/avdmanager, so that no test finds or runs this machine's. A test
// installs it under an SDK root (InstallTools), gives the daemon a fake JDK (JavaHome) and a
// scenario directory (DirEnv), and reads what the fake logged (Calls). The script's header
// lists the scenario files it understands. Only tests import it.
package sdktest

import (
	_ "embed"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// DirEnv names the scenario directory the fake logs to and reads its scenario from. Tests
// put it in the environment of the daemon, which the tool passes on.
const DirEnv = "FAKESDK_DIR"

//go:embed testdata/fakesdk/avdmanager
var script []byte

//go:embed testdata/fakesdk/devices.out
var devices []byte

// DevicesOut is the sample `avdmanager list device` output the fake prints by default:
// seven of the real tool's profiles (spike m3f step 1 (d)), one with each shape.
func DevicesOut() []byte { return append([]byte(nil), devices...) }

// InstallTools writes the fake as <root>/cmdline-tools/<dir>/bin/avdmanager with a
// package.xml of revision rev (the form sdk.installedOf reads), and returns the script's path.
func InstallTools(t *testing.T, root, dir, rev string) string {
	t.Helper()
	bin := filepath.Join(root, "cmdline-tools", dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bin, "avdmanager")
	if err := os.WriteFile(path, script, 0o755); err != nil {
		t.Fatal(err)
	}
	major, minor, _ := strings.Cut(rev, ".")
	if minor == "" {
		minor = "0"
	}
	xml := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<ns2:repository xmlns:ns2="http://schemas.android.com/repository/android/common/02" xmlns:ns3="http://schemas.android.com/repository/android/common/01" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><license id="android-sdk-license" type="text"></license><localPackage path="cmdline-tools;` + dir + `" obsolete="false"><type-details xsi:type="ns3:genericDetailsType"/><revision><major>` + major + `</major><minor>` + minor + `</minor></revision><display-name>Android SDK Command-line Tools (` + dir + `)</display-name><uses-license ref="android-sdk-license"/></localPackage></ns2:repository>
`
	if err := os.WriteFile(filepath.Join(root, "cmdline-tools", dir, "package.xml"), []byte(xml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// JavaHome makes a directory a JVM discovery accepts as a JDK 17: a release file naming the
// version. Nothing in it runs.
func JavaHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "release"), []byte("JAVA_VERSION=\"17.0.11\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// Write puts a scenario file in dir.
func Write(t *testing.T, dir, name, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Has reports whether the scenario directory holds the file.
func Has(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// Call is one invocation of the fake.
type Call struct {
	Args  []string
	Env   map[string]string
	Stdin string
	// File is the log file's name, call.<n>.<pid>.
	File string
}

// Calls are the invocations the fake logged in dir, in order.
func Calls(t *testing.T, dir string) []Call {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "call.*"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(names, func(i, j int) bool { return order(names[i]) < order(names[j]) })
	var out []Call
	for _, n := range names {
		if strings.HasSuffix(n, ".tmp") {
			continue
		}
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		c := Call{Env: map[string]string{}, File: filepath.Base(n)}
		for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
			kind, rest, _ := strings.Cut(line, "\t")
			switch kind {
			case "arg":
				c.Args = append(c.Args, rest)
			case "env":
				k, v, _ := strings.Cut(rest, "\t")
				c.Env[k] = v
			case "stdin":
				c.Stdin = rest
			}
		}
		out = append(out, c)
	}
	return out
}

// order is the call number in call.<n>.<pid>.
func order(path string) int {
	parts := strings.Split(filepath.Base(path), ".")
	n := 0
	if len(parts) > 1 {
		for _, c := range parts[1] {
			n = n*10 + int(c-'0')
		}
	}
	return n
}
