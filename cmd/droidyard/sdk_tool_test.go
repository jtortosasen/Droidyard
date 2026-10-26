package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/sdk/sdktest"
	proc "github.com/jtortosasen/droidyard/internal/process"
)

// The seam that gives a re-executed daemon an SDK with the fake avdmanager (sdktest) and a
// JDK: three variables the test sets, read in the daemon by installTestSeams. Like the other
// seams the daemon's environment is replaced whole, so nothing it starts finds this
// machine's adb, SDK or JDK.
const (
	sdkToolRootEnv = "DROIDYARD_TEST_SDK_ROOT"
	sdkToolJVMEnv  = "DROIDYARD_TEST_SDK_JVM"
	sdkToolDirEnv  = "DROIDYARD_TEST_SDK_SCENARIO"
)

func installSDKToolSeam() {
	root := os.Getenv(sdkToolRootEnv)
	if root == "" {
		return
	}
	scenario, jvm := os.Getenv(sdkToolDirEnv), os.Getenv(sdkToolJVMEnv)
	adbEnviron = func() []string {
		return []string{"HOME=/nonexistent-droidyard-test", "PATH=", "ANDROID_HOME=" + root, sdktest.DirEnv + "=" + scenario}
	}
	gradleFinder = func([]string) *gradle.Finder {
		return &gradle.Finder{GOOS: "linux", Env: []string{"JAVA_HOME=" + jvm}, StudioHomes: func() []string { return nil }}
	}
}

// TestKilledDaemonsSDKToolIsReapedByTheNext pins ADR-0129 §6 and ADR-0130 §5 end to end: an
// avdmanager run is a real process the supervisor records as kind sdk-tool, so a daemon
// SIGKILLed while it runs leaves its whole group alive (no death signal: the leader
// survives) and the pid record on disk, and the next start's Reconcile ends the group, the
// JVM its script started included, before it listens. The tool is the fake avdmanager
// (sdktest) under a temporary SDK; its JVM is a temporary directory with a release file;
// nothing here touches a device, an emulator or this machine's SDK. The Linux variant, the
// kind given the death signal, is not run on a macOS host. Seen failing with the run
// started unrecorded (no record: the group outlives the second daemon).
func TestKilledDaemonsSDKToolIsReapedByTheNext(t *testing.T) {
	state := shortDir(t)
	cfg := t.TempDir()
	sdkRoot, toolDir := t.TempDir(), t.TempDir()
	sdktest.InstallTools(t, sdkRoot, "latest", "12.0")
	sdktest.Write(t, toolDir, "devices.out", string(sdktest.DevicesOut()))
	sdktest.Write(t, toolDir, "hang", "")
	sdktest.Write(t, toolDir, "child", "")
	home := t.TempDir()
	// No adb, emulator or Gradle is reachable: the daemon's environment is the seam's.
	daemonEnv := []string{
		"HOME=" + home, "PATH=/usr/bin:/bin", "TMPDIR=" + os.TempDir(),
		sdkToolRootEnv + "=" + sdkRoot, sdkToolJVMEnv + "=" + sdktest.JavaHome(t), sdkToolDirEnv + "=" + toolDir,
		"DROIDYARD_CONFIG_DIR=" + cfg, "DROIDYARD_STATE_DIR=" + state, reexec + "=1",
	}

	srv := droidyardProc(t, daemonEnv, "", "serve")
	srv.expect(t, "listening")
	detached := droidyardProc(t, daemonEnv, "", "call", "--no-autostart", "--detach", "sdk.profiles")
	if code := detached.wait(t); code != 0 {
		t.Fatalf("the detached call exited %d", code)
	}
	rec := waitShellRecord(t, state)
	if rec.Kind != proc.SDKTool {
		t.Fatalf("record kind = %q, want %q", rec.Kind, proc.SDKTool)
	}
	leader, child := waitPid(t, filepath.Join(toolDir, "leader.pid")), waitPid(t, filepath.Join(toolDir, "child.pid"))
	t.Cleanup(func() { syscall.Kill(-rec.PGID, syscall.SIGKILL) })
	if leader != rec.PID {
		t.Fatalf("the fake avdmanager is pid %d, the record's is %d", leader, rec.PID)
	}
	if g, _ := syscall.Getpgid(child); g != rec.PGID {
		t.Fatalf("the tool's child is in group %d, the record's is %d", g, rec.PGID)
	}

	// Killed outright: nothing runs a shutdown.
	srv.cmd.Process.Signal(syscall.SIGKILL)
	srv.wait(t)
	if !alive(rec.PID) || groupGone(rec.PGID) {
		t.Fatalf("the tool (pid %d, group %d) died with the daemon; it must outlive a kill -9", rec.PID, rec.PGID)
	}
	if recs := shellRecords(t, state); len(recs) != 1 || recs[0].PID != rec.PID {
		t.Fatalf("processes.json holds %+v after the kill, want the tool's record %d", recs, rec.PID)
	}

	second := droidyardProc(t, daemonEnv, "", "serve")
	second.expect(t, "listening")
	if !groupGone(rec.PGID) {
		t.Errorf("the tool's group %d survived the next daemon's reconciliation", rec.PGID)
	}
	if alive(child) {
		t.Errorf("the tool's child %d survived the next daemon's reconciliation", child)
	}
	if recs := shellRecords(t, state); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after reconciling, want none", recs)
	}
	if code := droidyardProc(t, daemonEnv, "", "stop").wait(t); code != 0 {
		t.Fatalf("stop exited %d", code)
	}
	second.wait(t)
}

// TestSDKToolEndsWithADaemonShutdown pins the third end of ADR-0129 §6: a daemon that is
// stopped while an avdmanager run is in flight ends the run's whole group, the JVM its script
// started included, and removes the record, before it exits. A SIGKILLed daemon is the other
// test's. Seen failing with the group left to the next start (the run not tied to its call's
// context).
func TestSDKToolEndsWithADaemonShutdown(t *testing.T) {
	state := shortDir(t)
	sdkRoot, toolDir := t.TempDir(), t.TempDir()
	sdktest.InstallTools(t, sdkRoot, "latest", "12.0")
	sdktest.Write(t, toolDir, "hang", "")
	sdktest.Write(t, toolDir, "child", "")
	daemonEnv := []string{
		"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin", "TMPDIR=" + os.TempDir(),
		sdkToolRootEnv + "=" + sdkRoot, sdkToolJVMEnv + "=" + sdktest.JavaHome(t), sdkToolDirEnv + "=" + toolDir,
		"DROIDYARD_CONFIG_DIR=" + t.TempDir(), "DROIDYARD_STATE_DIR=" + state, reexec + "=1",
		drainEnv + "=200ms",
	}
	srv := droidyardProc(t, daemonEnv, "", "serve")
	srv.expect(t, "listening")
	if code := droidyardProc(t, daemonEnv, "", "call", "--no-autostart", "--detach", "sdk.profiles").wait(t); code != 0 {
		t.Fatalf("the detached call exited %d", code)
	}
	rec := waitShellRecord(t, state)
	child := waitPid(t, filepath.Join(toolDir, "child.pid"))
	t.Cleanup(func() { syscall.Kill(-rec.PGID, syscall.SIGKILL) })
	if code := droidyardProc(t, daemonEnv, "", "stop").wait(t); code != 0 {
		t.Fatalf("stop exited %d", code)
	}
	srv.wait(t)
	if !groupGone(rec.PGID) || alive(child) {
		t.Errorf("the tool's group %d (child %d) outlived the daemon's shutdown", rec.PGID, child)
	}
	if recs := shellRecords(t, state); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after the shutdown, want none", recs)
	}
}
