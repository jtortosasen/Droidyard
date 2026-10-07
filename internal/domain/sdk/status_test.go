package sdk

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// TestStatusWithoutSDK pins D11's answer for no SDK: found false, no requirement and no
// new error code, the warning naming the variable that would fix it (ANDROID_HOME), and
// the empty slices non-nil so the result marshals cleanly.
func TestStatusWithoutSDK(t *testing.T) {
	st := Status([]string{"HOME=" + filepath.Join(t.TempDir(), "user")})
	if st.Found || st.Root != "" || st.Source != "" {
		t.Errorf("Status = %+v, want found false with no root or source", st)
	}
	if !strings.Contains(strings.Join(st.Warnings, " "), "ANDROID_HOME") {
		t.Errorf("warnings = %v, want one naming ANDROID_HOME", st.Warnings)
	}
	if len(st.Rejected) == 0 {
		t.Error("no candidate was reported as tried")
	}
	if st.Installed.Platforms == nil || st.Installed.BuildTools == nil || st.Installed.CmdlineTools == nil || st.Installed.SystemImages == nil {
		t.Errorf("installed = %+v, want non-nil empty slices", st.Installed)
	}
}

// TestStatusWithSDK pins that a found host SDK is reported with its root and source and
// what is installed, read from its package.xml files.
func TestStatusWithSDK(t *testing.T) {
	root := t.TempDir()
	writePackage(t, filepath.Join(root, "platforms", "android-36", "package.xml"), "platforms;android-36", "2")
	writePackage(t, filepath.Join(root, "build-tools", "35.0.0", "package.xml"), "build-tools;35.0.0", "35", "0", "0")

	st := Status([]string{"ANDROID_HOME=" + root})
	if !st.Found || st.Root != root || st.Source != SourceAndroidHome {
		t.Fatalf("Status = %+v, want %q from ANDROID_HOME", st, root)
	}
	if got := ids(st.Installed.Platforms); !equal(got, []string{"platforms;android-36"}) {
		t.Errorf("platforms = %v, want platforms;android-36", got)
	}
	if got := ids(st.Installed.BuildTools); !equal(got, []string{"build-tools;35.0.0"}) {
		t.Errorf("buildTools = %v, want build-tools;35.0.0", got)
	}
}

// TestStatusDescriptorIsAGlobalQueryWithNoRequirement pins the classification D11
// rests on: sdk.status is a Global Query that needs no external tool, so its annotation
// derivation gives read-only and idempotent, and no requirement means open-world is
// false (registry.go ResolveAnnotations).
func TestStatusDescriptorIsAGlobalQueryWithNoRequirement(t *testing.T) {
	reg := capability.NewRegistry()
	Register(reg, Options{})
	d, ok := reg.Lookup("sdk.status")
	if !ok {
		t.Fatal("sdk.status was not registered")
	}
	if d.Kind != capability.Query || d.Scope != capability.Global || d.Statefulness != capability.Stateless {
		t.Errorf("sdk.status is %v/%v/%v, want Query/Global/Stateless", d.Kind, d.Scope, d.Statefulness)
	}
	if len(d.Availability) != 0 {
		t.Errorf("availability = %v, want none: a missing SDK is found: false, not a requirement", d.Availability)
	}
	a := d.ResolveAnnotations()
	if !a.ReadOnly || !a.Idempotent || a.Destructive || a.OpenWorld {
		t.Errorf("annotations = %+v, want read-only and idempotent, not destructive or open-world", a)
	}
}
