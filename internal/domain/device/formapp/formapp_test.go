package formapp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFormAppBuilds pins D14: the fixture app is built from the checked-in sources by
// build.sh with the SDK's own tools, no Gradle. The check reads the built APK's
// badging: it must name the fixture package and a launchable activity, which is what
// the milestone's level-5 runs install and start. A manifest without the LAUNCHER
// category has no launchable-activity line and fails here. Level 2 where the SDK or
// the JDK is absent: the test then skips, and says so, as reader_test.go does.
func TestFormAppBuilds(t *testing.T) {
	sdk := sdkDir()
	if _, err := os.Stat(filepath.Join(sdk, "build-tools", "37.0.0", "aapt2")); err != nil {
		t.Skipf("level 2 only: no Android SDK build-tools 37.0.0 at %s", sdk)
	}
	if _, err := exec.LookPath("javac"); err != nil {
		t.Skip("level 2 only: no javac")
	}
	out := filepath.Join(t.TempDir(), "formapp.apk")
	build := exec.Command("sh", "build.sh")
	build.Env = append(os.Environ(), "OUT="+out, "ANDROID_HOME="+sdk)
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build.sh: %v\n%s", err, b)
	}
	badging, err := exec.Command(
		filepath.Join(sdk, "build-tools", "37.0.0", "aapt2"), "dump", "badging", out,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("aapt2 dump badging: %v\n%s", err, badging)
	}
	got := string(badging)
	// The package name is the literal, not ApplicationID: the test would pass for any
	// value of the constant, so it would not pin the package the level-5 runs install.
	if !strings.Contains(got, "package: name='dev.droidyard.formapp'") {
		t.Errorf("badging does not name package dev.droidyard.formapp:\n%s", got)
	}
	if !strings.Contains(got, "launchable-activity:") {
		t.Errorf("badging names no launchable activity:\n%s", got)
	}
}

// sdkDir mirrors reader_test.go's discovery: ANDROID_HOME, then ANDROID_SDK_ROOT, then
// the per-OS default.
func sdkDir() string {
	if sdk := os.Getenv("ANDROID_HOME"); sdk != "" {
		return sdk
	}
	if sdk := os.Getenv("ANDROID_SDK_ROOT"); sdk != "" {
		return sdk
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Android", "sdk")
}
