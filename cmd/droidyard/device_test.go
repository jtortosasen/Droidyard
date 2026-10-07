package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/system"
)

func unusedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, p, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	return p
}

func fixtureFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../internal/domain/device/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fakeADBDaemon starts the shipped wiring in this process with the in-process fake adb,
// answering from files in a scenario directory, as the daemon's adb; the server port is one
// nothing listens on, so 5037 is never reached.
func fakeADBDaemon(t *testing.T, files map[string]string) *inProcess {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		devicetest.Write(t, dir, name, []byte(content))
	}
	// The daemon runs in this process, so its adb clients can too: the fake answers
	// from the scenario files without starting a process per call.
	return startInProcessADB(t, "127.0.0.1:0", nil, &adbSeam{env: devicetest.Env(t, dir, unusedPort(t)), exec: devicetest.Exec(dir)})
}

func listOf(t *testing.T, env capability.ResponseEnvelope) []device.Device {
	t.Helper()
	var r device.ListResult
	if err := json.Unmarshal(env.Result, &r); err != nil {
		t.Fatalf("device.list result %s: %v (error %+v)", env.Result, err, env.Error)
	}
	return r.Devices
}

// writeDaemonConfig writes the daemon's configuration file under its state directory, as
// a hand edit would; the daemon's policy reads it live, at each device check (plan m1k
// D7). The in-process daemon places its configuration at <state>/config, since only
// DROIDYARD_STATE_DIR is set for it (platform.ConfigDir).
func writeDaemonConfig(t *testing.T, p *inProcess, body string) {
	t.Helper()
	dir := filepath.Join(p.state, "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// failedCode is the code of a call's error, failing the test when the call succeeded.
func failedCode(t *testing.T, env capability.ResponseEnvelope) string {
	t.Helper()
	if env.Error == nil {
		t.Fatalf("call succeeded, want an error (operation %+v)", env.Operation)
	}
	return string(env.Error.Code)
}

// Decision (plan m1k step 5, check): the physical-device policy through the shipped
// wiring, driven by the configuration file the daemon reads live. With no file a physical
// device is hidden and every call for it is DEVICE_NOT_ALLOWED; admitting it in the file
// makes device.list show it as physical, a device call completes, and emulator.stop
// reaches its not-an-emulator refusal (ADR-0015 §4; ADR-0032 §2, §6; plan m1k D7).
func TestPhysicalDevicePolicyThroughTheDaemon(t *testing.T) {
	const phys = "R58M123"
	p := fakeADBDaemon(t, map[string]string{
		"devices.out": "List of devices attached\n" +
			"emulator-5554 device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:1\n" +
			"R58M123 device usb:1-1.2 product:raven model:Pixel_6_Pro transport_id:2\n",
		"emulator-5554.getprop":           fixtureFile(t, "pixel9a-getprop.txt"),
		"emulator-5554.avd":               fixtureFile(t, "pixel9a-emu-avd-name.txt"),
		"emulator-5554.bootcompleted.out": "1\n",
		"emulator-5554.screencap.out":     fixtureFile(t, "pixel9a-screencap.png"),
		phys + ".bootcompleted.out":       "1\n",
		phys + ".screencap.out":           fixtureFile(t, "pixel9a-screencap.png"),
	})

	// No configuration file: the physical device is hidden, and every call for it is
	// DEVICE_NOT_ALLOWED (device.list, device.screenshot, emulator.stop).
	env, code := p.call(t, "device.list")
	if code != 0 || env.Operation == nil || env.Operation.State != capability.StateCompleted {
		t.Fatalf("device.list exited %d with %+v (error %+v)", code, env.Operation, env.Error)
	}
	if got := listOf(t, env); len(got) != 1 || got[0].Device != "emulator-5554" {
		t.Errorf("listed %+v, want the emulator only with no configuration", got)
	}
	shot, code := p.call(t, "device.screenshot", "--params", `{"device":"`+phys+`"}`)
	if code == 0 || shot.Operation == nil || shot.Operation.State != capability.StateFailed {
		t.Errorf("device.screenshot exited %d with %+v, want a failed operation", code, shot.Operation)
	}
	if c := failedCode(t, shot); c != "DEVICE_NOT_ALLOWED" {
		t.Errorf("device.screenshot error %s, want DEVICE_NOT_ALLOWED", c)
	}
	stop, _ := p.call(t, "emulator.stop", "--params", `{"device":"`+phys+`"}`)
	if c := failedCode(t, stop); c != "DEVICE_NOT_ALLOWED" {
		t.Errorf("emulator.stop error %s, want DEVICE_NOT_ALLOWED while unadmitted", c)
	}

	// Admit the exact serial, with no restart: the edit decides the next calls.
	writeDaemonConfig(t, p, `{"schemaVersion":1,"devices":{"allowPhysical":true,"allowlist":["`+phys+`"]}}`)
	env, code = p.call(t, "device.list")
	if code != 0 || env.Operation == nil || env.Operation.State != capability.StateCompleted {
		t.Fatalf("device.list (admitted) exited %d with %+v (error %+v)", code, env.Operation, env.Error)
	}
	var found bool
	for _, d := range listOf(t, env) {
		if d.Device == phys {
			found = true
			if d.Kind != "physical" {
				t.Errorf("admitted device kind = %q, want physical", d.Kind)
			}
		}
	}
	if !found {
		t.Errorf("listed %+v, want the admitted physical device %s", listOf(t, env), phys)
	}
	shot, code = p.call(t, "device.screenshot", "--params", `{"device":"`+phys+`"}`)
	if code != 0 || shot.Operation == nil || shot.Operation.State != capability.StateCompleted {
		t.Errorf("device.screenshot (admitted) exited %d with %+v (error %+v), want completed", code, shot.Operation, shot.Error)
	}
	stop, _ = p.call(t, "emulator.stop", "--params", `{"device":"`+phys+`"}`)
	if c := failedCode(t, stop); c != "EMULATOR_NOT_AN_EMULATOR" {
		t.Errorf("emulator.stop error %s, want EMULATOR_NOT_AN_EMULATOR once admitted", c)
	}
}

// TestDeviceListThroughCLI: `droidyard call device.list` against a daemon whose adb is
// the fake, answering from step 1's recorded files. A running emulator is listed with
// its kind and the details; an unauthorized emulator with its state; a physical device
// not at all (ADR-0015 §4).
func TestDeviceListThroughCLI(t *testing.T) {
	t.Parallel()
	p := fakeADBDaemon(t, map[string]string{
		"devices.out": "List of devices attached\n" +
			"emulator-5554 device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 device:emu64a16k transport_id:1\n" +
			"emulator-5556 unauthorized transport_id:2\n" +
			"R58M12ABCDE device usb:1-1.2 product:raven model:Pixel_6_Pro device:raven transport_id:3\n",
		"emulator-5554.getprop": fixtureFile(t, "pixel9a-getprop.txt"),
		"emulator-5554.avd":     fixtureFile(t, "pixel9a-emu-avd-name.txt"),
	})
	env, code := p.call(t, "device.list")
	if code != 0 || env.Operation == nil || env.Operation.State != capability.StateCompleted {
		t.Fatalf("call device.list exited %d with %+v (error %+v)", code, env.Operation, env.Error)
	}
	got := listOf(t, env)
	if len(got) != 2 {
		t.Fatalf("listed %+v, want the emulator and the unauthorized emulator only", got)
	}
	if d := got[0]; d.Device != "emulator-5554" || d.Kind != "emulator" || d.State != "device" || !d.Online ||
		d.AvdName != "Pixel_9a" || d.AndroidVersion != "16" || d.SDK != 36 {
		t.Errorf("first device = %+v, want emulator-5554, kind emulator, avdName Pixel_9a, Android 16, sdk 36", d)
	}
	if d := got[1]; d.Device != "emulator-5556" || d.State != "unauthorized" || d.Online {
		t.Errorf("second device = %+v, want emulator-5556 listed as unauthorized", d)
	}
	if strings.Contains(string(env.Result), "R58M12ABCDE") {
		t.Error("the physical device is in the result")
	}
}

// TestDeviceListWithHangingDetails: three emulators whose getprop and console never
// answer are all listed, with empty details, within the plan's 12 s.
func TestDeviceListWithHangingDetails(t *testing.T) {
	t.Parallel()
	files := map[string]string{"devices.out": "List of devices attached\n"}
	for i, s := range []string{"emulator-5554", "emulator-5556", "emulator-5558"} {
		files["devices.out"] += s + " device product:sdk_gphone16k_arm64 transport_id:" + string(rune('1'+i)) + "\n"
		files[s+".getprop.hang"], files[s+".avd.hang"] = "", ""
	}
	p := fakeADBDaemon(t, files)
	start := time.Now()
	env, code := p.call(t, "device.list")
	if d := time.Since(start); code != 0 || d > 12*time.Second {
		t.Fatalf("exited %d after %v (error %+v), want success within 12 s", code, d, env.Error)
	}
	got := listOf(t, env)
	if len(got) != 3 {
		t.Fatalf("listed %d devices, want 3: %+v", len(got), got)
	}
	for _, d := range got {
		if d.AvdName != "" || d.AndroidVersion != "" || d.SDK != 0 {
			t.Errorf("%+v has details although nothing answered", d)
		}
	}
}

// TestDegradedWithoutADB: a daemon with no adb anywhere (no ADB, no SDK variable, a
// PATH without adb, a HOME with no SDK) stays up and registers every capability.
// device.list is listed unavailable with a reason, its call is a failed operation with
// DEVICE_ADB_NOT_FOUND and a next step, system.health says adb was not found, and a
// workspace opened in the same daemon is ready (plan m1e, R1; adb-device.md §adb
// resolution and degradation).
func TestDegradedWithoutADB(t *testing.T) {
	t.Parallel()
	p := startInProcessADB(t, "127.0.0.1:0", nil, &adbSeam{env: []string{"HOME=" + t.TempDir(), "PATH=" + t.TempDir()}})

	caps, code := p.call(t, "system.capabilities")
	if code != 0 {
		t.Fatalf("system.capabilities exited %d: %+v", code, caps.Error)
	}
	var list system.Capabilities
	if err := json.Unmarshal(caps.Result, &list); err != nil {
		t.Fatal(err)
	}
	found := false
	emulatorListed := false
	for _, c := range list.Capabilities {
		if c.Name == "device.list" {
			found = true
			if c.Available || !strings.Contains(c.Unavailable, "ADB") || !strings.Contains(c.Unavailable, "Tried:") {
				t.Errorf("device.list = %+v, want unavailable with a reason naming what to set", c)
			}
		}
		if c.Name == "emulator.list" {
			emulatorListed = true
			// plan m1i step 4, check: no launcher is listed unavailable, naming what was
			// tried (the emulator requirement is asked before adb's).
			if c.Available || !strings.Contains(c.Unavailable, "ANDROID_HOME") || !strings.Contains(c.Unavailable, "Tried:") {
				t.Errorf("emulator.list = %+v, want unavailable with a reason naming what to set", c)
			}
		}
		if c.Name == "system.health" && !c.Available {
			t.Errorf("system.health must stay available: %+v", c)
		}
	}
	if !found {
		t.Fatal("device.list is not listed: degraded must keep every capability registered")
	}
	if !emulatorListed {
		t.Fatal("emulator.list is not listed: degraded must keep every capability registered")
	}

	elist, code := p.call(t, "emulator.list")
	if code == 0 || elist.Error == nil || elist.Error.Code != "EMULATOR_LAUNCHER_NOT_FOUND" {
		t.Errorf("call emulator.list exited %d with error %+v; want EMULATOR_LAUNCHER_NOT_FOUND", code, elist.Error)
	}

	env, code := p.call(t, "device.list")
	if code == 0 || env.Operation == nil || env.Operation.State != capability.StateFailed ||
		env.Error == nil || env.Error.Code != "DEVICE_ADB_NOT_FOUND" || env.Error.NextStep == "" {
		t.Errorf("call device.list exited %d with %+v, error %+v; want a failed operation with DEVICE_ADB_NOT_FOUND and a next step", code, env.Operation, env.Error)
	}

	h, _ := p.call(t, "system.health")
	if hh := health(t, h); hh.ADB == nil || hh.ADB.Found || len(hh.ADB.Rejected) == 0 {
		t.Errorf("system.health adb = %+v, want found false with the candidates tried", hh.ADB)
	}

	root, _ := gradleProject(t)
	ws, code := p.call(t, "--workspace", root, "workspace.open")
	if code != 0 || workspaceOf(t, ws).State != "ready" {
		t.Errorf("workspace.open exited %d, %+v: an open workspace stays ready without adb", code, ws.Error)
	}
}
