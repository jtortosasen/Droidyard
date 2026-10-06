package main

import (
	"os"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/device"
)

// TestRequirementsAskTheDevicePackageAboutAdb pins that adb availability is the
// device package's resolution, not a constant, while the tools without a check yet are
// still reported missing. The environment is explicit: this machine's adb is never
// found.
func TestRequirementsAskTheDevicePackageAboutAdb(t *testing.T) {
	empty := t.TempDir()
	none := requirements{adb: device.NewADB([]string{"HOME=" + empty, "PATH=" + empty}, nil)}
	reason, missing := none.Missing(capability.RequiresADB)
	if !missing || !strings.Contains(reason, "ADB") || !strings.Contains(reason, "ANDROID_HOME") {
		t.Errorf("with no adb: missing %v, reason %q, want missing and naming what to set", missing, reason)
	}
	t.Setenv("PATH", "") // nothing in the process environment may matter
	exe := t.TempDir() + "/adb"
	if err := writeExecutable(exe); err != nil {
		t.Fatal(err)
	}
	have := requirements{adb: device.NewADB([]string{"ADB=" + exe}, nil)}
	if reason, missing := have.Missing(capability.RequiresADB); missing {
		t.Errorf("with ADB naming an executable: missing, %q", reason)
	}
}

// TestRequirementsAskTheSidecarClientAboutGradle pins that the gradle requirement is
// the sidecar client's JVM discovery (plan m1g D22), not a constant: no client is
// missing with its own reason, and a client whose discovery rejected every candidate
// reports that reason, which is what system.capabilities shows.
func TestRequirementsAskTheSidecarClientAboutGradle(t *testing.T) {
	if _, missing := (requirements{}).Missing(capability.RequiresGradle); !missing {
		t.Error("with no sidecar client, gradle must be reported missing")
	}

	jvm := gradle.JVM{Rejected: []gradle.Rejected{{Candidate: "/jdk11", Source: gradle.SourceJavaHome, Reason: "Java 11 is older than 17"}}}
	client := gradle.New(gradle.Options{JVM: &jvm})
	reason, missing := (requirements{gradle: client}).Missing(capability.RequiresGradle)
	if !missing {
		t.Fatalf("with no JVM 17 found, gradle must be reported missing")
	}
	if !strings.Contains(reason, "17") || !strings.Contains(reason, "/jdk11") || !strings.Contains(reason, "Java 11 is older than 17") {
		t.Errorf("the reason %q does not name the floor and the rejected candidates", reason)
	}

	found := gradle.JVM{Found: true, Home: "/jdk17", Version: "17.0.11", Source: gradle.SourceJavaHome}
	if reason, missing := (requirements{gradle: gradle.New(gradle.Options{JVM: &found})}).Missing(capability.RequiresGradle); missing {
		t.Errorf("with a JVM 17 found, gradle must be available, got missing %q", reason)
	}

	// A tool with no check yet is still reported missing rather than vanishing.
	if _, missing := (requirements{}).Missing(capability.RequiresGit); !missing {
		t.Error("git has no check yet and must still be reported missing")
	}
}

func writeExecutable(path string) error {
	return os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755)
}
