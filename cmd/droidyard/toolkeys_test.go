package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/emulator"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// startInProcessConfigFile starts the shipped wiring in this process with the
// configuration file already written, so the tool keys are read from it at start
// (ADR-0032 §3, §6; plan m1k D8). The adb seal TestMain put on the package is kept: no
// adb is found, and the shared server on 5037 is never reached.
func startInProcessConfigFile(t *testing.T, configBody string) *inProcess {
	t.Helper()
	return startInProcessConfig(t, "127.0.0.1:0", nil, nil, 0, configBody)
}

// startInProcessConfigADB is startInProcessConfigFile with a fake adb the daemon is
// given too, so a configured `tools.adb` can be resolved and reported.
func startInProcessConfigADB(t *testing.T, configBody string, adb *adbSeam) *inProcess {
	t.Helper()
	return startInProcessConfig(t, "127.0.0.1:0", nil, adb, 0, configBody)
}

// adbRejected reports whether rejected lists candidate with source and a reason
// containing want.
func adbRejected(rejected []device.Rejected, candidate, source, want string) bool {
	for _, r := range rejected {
		if r.Candidate == candidate && r.Source == source && strings.Contains(r.Reason, want) {
			return true
		}
	}
	return false
}

// jvmRejected reports whether rejected lists candidate with source and a reason
// containing want.
func jvmRejected(rejected []gradle.Rejected, candidate, source, want string) bool {
	for _, r := range rejected {
		if r.Candidate == candidate && r.Source == source && strings.Contains(r.Reason, want) {
			return true
		}
	}
	return false
}

// TestAnInvalidConfigurationAtStartIsReportedByEveryResolution is D8's invalid-file row
// (plan m1k step 6, check (a); ADR-0032 §4; review B4): with a file that does not parse
// when the daemon reads it, no tool key is used, `system.health` reports the tool keys as
// `invalid`, and each of the three resolutions carries the file as a rejected candidate
// under its own key's source — in `system.health`'s `adb` and `jvm`, in `device.list`'s
// and `emulator.list`'s not-found cause, and in a `build.*` call's
// BUILD_SIDECAR_UNAVAILABLE. Seen failing when a resolution omits the entry.
func TestAnInvalidConfigurationAtStartIsReportedByEveryResolution(t *testing.T) {
	t.Parallel()
	// A relative `tools.adb` makes the whole file invalid (ADR-0032 §1, §4).
	p := startInProcessConfigFile(t, `{"schemaVersion":1,"tools":{"adb":"adb"}}`)
	cfg := configPath(p)

	env, code := p.call(t, "system.health")
	if code != 0 {
		t.Fatalf("system.health exited %d: %+v", code, env.Error)
	}
	h := health(t, env)
	if h.Config == nil || h.Config.ToolKeys != policy.StateInvalid {
		t.Fatalf("config = %+v; want toolKeys invalid", h.Config)
	}
	if h.ADB == nil || !adbRejected(h.ADB.Rejected, cfg, device.SourceToolKey, "the configuration file is invalid") {
		t.Errorf("health.adb rejected = %+v; want %s from %s", adbRejectedList(h.ADB), cfg, device.SourceToolKey)
	}
	if h.JVM == nil || !jvmRejected(h.JVM.Rejected, cfg, gradle.SourceSidecarJavaHome, "the configuration file is invalid") {
		t.Errorf("health.jvm rejected = %+v; want %s from %s", jvmRejectedList(h.JVM), cfg, gradle.SourceSidecarJavaHome)
	}

	// device.list: adb is otherwise absent, so the file is why it is not found.
	env, code = p.call(t, "device.list")
	if code == 0 || env.Error == nil || env.Error.Code != device.ErrADBNotFound {
		t.Fatalf("device.list exited %d with %+v; want DEVICE_ADB_NOT_FOUND", code, env)
	}
	if !strings.Contains(env.Error.Cause, cfg) {
		t.Errorf("device.list cause = %q; want it to name %s", env.Error.Cause, cfg)
	}

	// emulator.list: no launcher is found, and the file is named as a rejected candidate.
	env, code = p.call(t, "emulator.list")
	if code == 0 || env.Error == nil || env.Error.Code != emulator.ErrLauncherNotFound {
		t.Fatalf("emulator.list exited %d with %+v; want EMULATOR_LAUNCHER_NOT_FOUND", code, env)
	}
	if !strings.Contains(env.Error.Cause, cfg) {
		t.Errorf("emulator.list cause = %q; want it to name %s", env.Error.Cause, cfg)
	}

	// build.* on a trusted root. The file is edited valid again so trust passes at the
	// call (the live snapshot), but the tool keys were read invalid at start, so the
	// sidecar has no JVM and the reason names the file the keys were not read from
	// (ADR-0032 §6).
	root, _ := gradleProject(t)
	writeConfig(t, p, `{"schemaVersion":1,"trust":{"roots":["`+root+`"]}}`)
	if env, code := callIn(t, p.state, root, "workspace.open"); code != 0 {
		t.Fatalf("workspace.open exited %d: %+v", code, env.Error)
	}
	env, code = callIn(t, p.state, root, "build.tasks")
	if code == 0 || env.Error == nil || env.Error.Code != capability.ErrBuildSidecarUnavailable {
		t.Fatalf("build.tasks exited %d with %+v; want BUILD_SIDECAR_UNAVAILABLE", code, env)
	}
	if !strings.Contains(env.Error.Cause, cfg) {
		t.Errorf("build.tasks cause = %q; want it to name %s", env.Error.Cause, cfg)
	}
}

// adbRejectedList, jvmRejectedList render a rejected list for a failure message.
func adbRejectedList(s *device.Status) []device.Rejected {
	if s == nil {
		return nil
	}
	return s.Rejected
}

func jvmRejectedList(j *gradle.JVM) []gradle.Rejected {
	if j == nil {
		return nil
	}
	return j.Rejected
}

// TestTheToolKeysAreReadOnceAtStartup pins ADR-0032 §6 and D8: a tool key is read once,
// at daemon start, so an edit of the configuration while the daemon runs does not change
// the running resolution — the key it read at start is the one it keeps, and a new
// daemon is what picks the edit up. Seen failing when the keys are read per call.
func TestTheToolKeysAreReadOnceAtStartup(t *testing.T) {
	t.Parallel()
	keyAtStart := filepath.Join(t.TempDir(), "adb")
	keyAfterEdit := filepath.Join(t.TempDir(), "adb")
	if keyAtStart == keyAfterEdit {
		t.Fatal("the two temporary roots collided")
	}
	p := startInProcessConfigFile(t, `{"schemaVersion":1,"tools":{"adb":"`+keyAtStart+`"}}`)

	env, code := p.call(t, "system.health")
	if code != 0 {
		t.Fatalf("system.health exited %d: %+v", code, env.Error)
	}
	h := health(t, env)
	if h.Config == nil || h.Config.ToolKeys != policy.StateOK {
		t.Fatalf("config = %+v; want toolKeys ok", h.Config)
	}
	// The configured key names no file, so adb fails closed and the key it read is the
	// whole story (m1e Q6; D8).
	if h.ADB == nil || h.ADB.Found || !adbRejected(h.ADB.Rejected, keyAtStart, device.SourceToolKey, "tools.adb is set") {
		t.Fatalf("health.adb = %+v; want not found with %s rejected", h.ADB, keyAtStart)
	}

	// The file is edited while the daemon runs: the running resolution keeps the key it
	// read at start, and never sees the new one (ADR-0032 §6).
	writeConfig(t, p, `{"schemaVersion":1,"tools":{"adb":"`+keyAfterEdit+`"}}`)
	env, code = p.call(t, "system.health")
	if code != 0 {
		t.Fatalf("system.health after the edit exited %d: %+v", code, env.Error)
	}
	h = health(t, env)
	if !adbRejected(h.ADB.Rejected, keyAtStart, device.SourceToolKey, "tools.adb is set") {
		t.Errorf("after the edit, health.adb rejected = %+v; want the key read at start (%s)", h.ADB.Rejected, keyAtStart)
	}
	if adbRejected(h.ADB.Rejected, keyAfterEdit, device.SourceToolKey, "") {
		t.Errorf("after the edit, health.adb rejected = %+v; the running daemon used the edited key without a restart", h.ADB.Rejected)
	}
}

// TestTheConfiguredADBIsNamedAsHealthsSource is A6's adb half (plan m1k step 6;
// ADR-0032 §3, §6): a configured `tools.adb` is the source of the resolved adb that
// `system.health` names, and the `ADB` variable's path is listed as overridden (R3).
func TestTheConfiguredADBIsNamedAsHealthsSource(t *testing.T) {
	t.Parallel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// The fake adb answers in process, and its server port is one nothing listens on, so
	// 5037 is never reached.
	seam := &adbSeam{env: devicetest.Env(t, dir, unusedPort(t)), exec: devicetest.Exec(dir)}
	p := startInProcessConfigADB(t, `{"schemaVersion":1,"tools":{"adb":"`+exe+`"}}`, seam)

	env, code := p.call(t, "system.health")
	if code != 0 {
		t.Fatalf("system.health exited %d: %+v", code, env.Error)
	}
	h := health(t, env)
	if h.ADB == nil || !h.ADB.Found || h.ADB.Source != device.SourceToolKey || h.ADB.Path != exe {
		t.Fatalf("health.adb = %+v; want found at %s from %s", h.ADB, exe, device.SourceToolKey)
	}
	// devicetest.Env sets ADB to the same path; it is still reported as overridden by the key.
	if !adbRejected(h.ADB.Rejected, exe, device.SourceADBEnv, "overridden by tools.adb") {
		t.Errorf("health.adb rejected = %+v; want the ADB variable's path listed as overridden", h.ADB.Rejected)
	}
}
