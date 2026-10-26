package sdk

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// Tests of sdk.uninstall (plan m3f step 4): a person's call that deletes SDK packages.

// uninstallFixture is an SDK with platform-tools (known file sizes) and the emulator.
func uninstallFixture(t *testing.T, tweak ...func(*installer)) (*instFixture, int64) {
	t.Helper()
	f := newInst(t, stdRepo(t).build(), filepath.Join(t.TempDir(), "sdk"), tweak...)
	var size int64
	pt := filepath.Join(f.root, "platform-tools")
	writePackage(t, filepath.Join(pt, "package.xml"), "platform-tools", "37", "0", "1")
	for path, n := range map[string]int{"package.xml": 0, "adb": 1000, "sub/fastboot": 234} {
		full := filepath.Join(pt, path)
		if n > 0 {
			os.MkdirAll(filepath.Dir(full), 0o755)
			if err := os.WriteFile(full, make([]byte, n), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		fi, err := os.Stat(full)
		if err != nil {
			t.Fatal(err)
		}
		size += fi.Size()
	}
	writePackage(t, filepath.Join(f.root, "emulator", "package.xml"), "emulator", "37", "2", "12")
	return f, size
}

func uninstall(f *instFixture, client capability.ClientKind, ids ...string) capability.Outcome {
	return f.call(client, "sdk.uninstall", `{"packages":`+jsonList(ids)+`}`)
}

// TestUninstall: the package is gone, nothing is left in .temp, the others stay, and freed
// is what its files took, measured before. An id that is not installed is refused.
func TestUninstall(t *testing.T) {
	f, size := uninstallFixture(t)
	out := uninstall(f, capability.ClientUI, "platform-tools")
	if out.State != capability.StateCompleted {
		t.Fatalf("outcome = %s %+v", out.State, out.Error)
	}
	res := out.Result.(UninstallResult)
	if len(res.Removed) != 1 || res.Removed[0] != "platform-tools" || res.Freed != size || len(res.Warnings) != 0 {
		t.Errorf("result = %+v, want platform-tools removed and %d freed", res, size)
	}
	if present(t, filepath.Join(f.root, "platform-tools")) {
		t.Error("the directory is still there")
	}
	if left := tempLeftovers(t, f.root); len(left) != 0 {
		t.Errorf("temporary entries left: %v", left)
	}
	if !present(t, filepath.Join(f.root, "emulator", "package.xml")) {
		t.Error("another package was touched")
	}
	again := uninstall(f, capability.ClientUI, "platform-tools")
	wantCode(t, again, ErrPackageNotFound)
}

// TestUninstallRefusedInUse: a package in use, or not checked yet (the shipped seam until
// step 5), is not removed: SDK_PACKAGE_IN_USE naming it, the directory intact.
func TestUninstallRefusedInUse(t *testing.T) {
	check := func(t *testing.T, f *instFixture, by string) {
		out := uninstall(f, capability.ClientUI, "platform-tools", "emulator")
		wantCode(t, out, ErrPackageInUse)
		pkgs := out.Error.Details["packages"].([]map[string]any)
		if len(pkgs) != 1 || pkgs[0]["id"] != "platform-tools" || !strings.Contains(strings.Join(pkgs[0]["usedBy"].([]string), ","), by) {
			t.Errorf("details.packages = %v", pkgs)
		}
		for _, id := range []string{"platform-tools", "emulator"} {
			if !present(t, filepath.Join(f.root, id, "package.xml")) {
				t.Errorf("%s was removed", id)
			}
		}
	}
	t.Run("in use", func(t *testing.T) {
		f, _ := uninstallFixture(t)
		f.inUse = func(ids []string) map[string][]string {
			return map[string][]string{"platform-tools": {"adb server (pid 7)"}}
		}
		check(t, f, "adb server")
	})
	t.Run("not checked yet", func(t *testing.T) {
		f, _ := uninstallFixture(t, func(in *installer) { in.inUse = notChecked })
		out := uninstall(f, capability.ClientUI, "platform-tools")
		wantCode(t, out, ErrPackageInUse)
		if !present(t, filepath.Join(f.root, "platform-tools", "package.xml")) {
			t.Error("the package was removed unchecked")
		}
	})
}

// TestUninstallRefusedOverMCP pins ADR-0129 §1: sdk.uninstall is a person's call. An
// agent's session is refused before anything runs and nothing is removed. Seen failing
// with the audience set to all.
func TestUninstallRefusedOverMCP(t *testing.T) {
	f, _ := uninstallFixture(t)
	out := uninstall(f, capability.ClientMCP, "platform-tools")
	wantCode(t, out, capability.ErrNotAllowedHere)
	if !present(t, filepath.Join(f.root, "platform-tools", "package.xml")) {
		t.Error("the package was removed for an agent")
	}
}

// TestUninstallPutsBackWhenARenameFails: a call that stops part way leaves the SDK as it
// found it, the directories already moved aside going back.
func TestUninstallPutsBackWhenARenameFails(t *testing.T) {
	n := 0
	f, _ := uninstallFixture(t, func(in *installer) {
		in.rename = func(oldpath, newpath string) error {
			n++
			if n == 2 {
				return errors.New("the disk said no")
			}
			return os.Rename(oldpath, newpath)
		}
	})
	out := uninstall(f, capability.ClientUI, "platform-tools", "emulator")
	wantCode(t, out, ErrNotWritable)
	for _, id := range []string{"platform-tools", "emulator"} {
		if !present(t, filepath.Join(f.root, id, "package.xml")) {
			t.Errorf("%s was not put back", id)
		}
	}
	if left := tempLeftovers(t, f.root); len(left) != 0 {
		t.Errorf("temporary entries left: %v", left)
	}
}
