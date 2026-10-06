package sdk

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePackage writes a real-shaped package.xml at path declaring the package id with
// the given revision parts (empty parts are omitted).
func writePackage(t *testing.T, path, id string, parts ...string) {
	t.Helper()
	rev := ""
	for i, name := range []string{"major", "minor", "micro"} {
		if i < len(parts) && parts[i] != "" {
			rev += "<" + name + ">" + parts[i] + "</" + name + ">"
		}
	}
	body := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><ns2:repository xmlns:ns2="http://schemas.android.com/repository/android/common/02"><localPackage path="` + id + `" obsolete="false"><revision>` + rev + `</revision><display-name>` + id + `</display-name></localPackage></ns2:repository>`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReadInstalledListsTheFixedLocations pins D14: the walk reads the package.xml at
// each of the fixed locations and reports each package's id and revision. A directory
// with no package.xml contributes nothing and is not a note.
func TestReadInstalledListsTheFixedLocations(t *testing.T) {
	root := t.TempDir()
	writePackage(t, filepath.Join(root, "platforms", "android-36", "package.xml"), "platforms;android-36", "2")
	writePackage(t, filepath.Join(root, "platforms", "android-37.0", "package.xml"), "platforms;android-37.0", "2")
	writePackage(t, filepath.Join(root, "build-tools", "35.0.0", "package.xml"), "build-tools;35.0.0", "35", "0", "0")
	writePackage(t, filepath.Join(root, "platform-tools", "package.xml"), "platform-tools", "36", "0", "0")
	writePackage(t, filepath.Join(root, "emulator", "package.xml"), "emulator", "36", "6", "11")
	writePackage(t, filepath.Join(root, "system-images", "android-35", "google_apis", "arm64-v8a", "package.xml"), "system-images;android-35;google_apis;arm64-v8a", "9")
	// A directory in a walked location with no package.xml: nothing is reported for it.
	if err := os.MkdirAll(filepath.Join(root, "platforms", "android-empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	inst, warnings := ReadInstalled(root)
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if got := ids(inst.Platforms); !equal(got, []string{"platforms;android-36", "platforms;android-37.0"}) {
		t.Errorf("platforms = %v, want android-36 and android-37.0", got)
	}
	if inst.Platforms[0].Version != "2" || inst.Platforms[1].Version != "2" {
		t.Errorf("platform versions = %q, %q, want 2", inst.Platforms[0].Version, inst.Platforms[1].Version)
	}
	if got := ids(inst.BuildTools); !equal(got, []string{"build-tools;35.0.0"}) {
		t.Errorf("buildTools = %v, want build-tools;35.0.0", got)
	}
	if inst.BuildTools[0].Version != "35.0.0" {
		t.Errorf("build-tools version = %q, want 35.0.0", inst.BuildTools[0].Version)
	}
	if inst.PlatformTools == nil || inst.PlatformTools.ID != "platform-tools" {
		t.Errorf("platformTools = %+v, want platform-tools", inst.PlatformTools)
	}
	if inst.Emulator == nil || inst.Emulator.ID != "emulator" || inst.Emulator.Version != "36.6.11" {
		t.Errorf("emulator = %+v, want emulator 36.6.11", inst.Emulator)
	}
	if got := ids(inst.SystemImages); !equal(got, []string{"system-images;android-35;google_apis;arm64-v8a"}) {
		t.Errorf("systemImages = %v, want the android-35 image", got)
	}
}

// TestReadInstalledSkipsAMalformedFileWithANote pins D14's "skipped, with a note": a
// package.xml that does not parse is left out of the result and reported, and the rest
// of the walk still runs.
func TestReadInstalledSkipsAMalformedFileWithANote(t *testing.T) {
	root := t.TempDir()
	writePackage(t, filepath.Join(root, "platforms", "android-36", "package.xml"), "platforms;android-36", "2")
	bad := filepath.Join(root, "platforms", "android-bad", "package.xml")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("<not xml"), 0o644); err != nil {
		t.Fatal(err)
	}
	writePackage(t, filepath.Join(root, "platforms", "android-37.0", "package.xml"), "platforms;android-37.0", "2")

	inst, warnings := ReadInstalled(root)
	if got := ids(inst.Platforms); !equal(got, []string{"platforms;android-36", "platforms;android-37.0"}) {
		t.Errorf("platforms = %v, want the two good ones and not the malformed", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "android-bad") {
		t.Errorf("warnings = %v, want one naming the malformed file", warnings)
	}
}

// TestReadInstalledIsBounded pins D14's bound: three thousand package.xml files are read
// at most maxPackages deep, with a note, so the walk is bounded. Removing the bound
// makes this fail on the unread tail.
func TestReadInstalledIsBounded(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 3000; i++ {
		writePackage(t, filepath.Join(root, "platforms", fmt.Sprintf("android-%04d", i), "package.xml"), fmt.Sprintf("platforms;android-%04d", i), "1")
	}
	inst, warnings := ReadInstalled(root)
	if len(inst.Platforms) != maxPackages {
		t.Errorf("read %d packages, want the bound %d", len(inst.Platforms), maxPackages)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], fmt.Sprintf("more than %d", maxPackages)) {
		t.Errorf("warnings = %v, want one noting the bound", warnings)
	}
}

// TestReadInstalledEmptySlicesAreNotNil pins that a host SDK with nothing at a fixed
// location still reports an empty list there, so its result marshals as [] and not null.
func TestReadInstalledEmptySlicesAreNotNil(t *testing.T) {
	inst, _ := ReadInstalled(t.TempDir())
	if inst.Platforms == nil || inst.BuildTools == nil || inst.CmdlineTools == nil || inst.SystemImages == nil {
		t.Errorf("Installed = %+v, want non-nil empty slices", inst)
	}
	if inst.PlatformTools != nil || inst.Emulator != nil {
		t.Errorf("Installed = %+v, want absent singletons", inst)
	}
}

func ids(ps []Package) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
