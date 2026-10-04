package main

import (
	"os"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
)

// TestRequirementsAskTheDevicePackageAboutAdb pins that adb availability is the
// device package's resolution, not a constant, while the tools without a check yet are
// still reported missing. The environment is explicit: this machine's adb is never
// found.
func TestRequirementsAskTheDevicePackageAboutAdb(t *testing.T) {
	empty := t.TempDir()
	none := requirements{device.NewADB([]string{"HOME=" + empty, "PATH=" + empty}, nil)}
	reason, missing := none.Missing(capability.RequiresADB)
	if !missing || !strings.Contains(reason, "ADB") || !strings.Contains(reason, "ANDROID_HOME") {
		t.Errorf("with no adb: missing %v, reason %q, want missing and naming what to set", missing, reason)
	}
	t.Setenv("PATH", "") // nothing in the process environment may matter
	exe := t.TempDir() + "/adb"
	if err := writeExecutable(exe); err != nil {
		t.Fatal(err)
	}
	have := requirements{device.NewADB([]string{"ADB=" + exe}, nil)}
	if reason, missing := have.Missing(capability.RequiresADB); missing {
		t.Errorf("with ADB naming an executable: missing, %q", reason)
	}
	if _, missing := have.Missing(capability.RequiresGradle); !missing {
		t.Error("gradle has no check yet and must still be reported missing")
	}
}

func writeExecutable(path string) error {
	return os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755)
}
