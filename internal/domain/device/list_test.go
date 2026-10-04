package device

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// scenario builds a fake adb scenario directory and the ADB over it. Nothing here runs
// this machine's adb: ADB names the test binary, PATH is empty, and the server port is
// one nothing listens on.
func scenario(t *testing.T, files map[string]string) (*ADB, string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		devicetest.Write(t, dir, name, []byte(content))
	}
	return newRunner(t, devicetest.Env(t, dir, closedPort(t))), dir
}

func asCapError(t *testing.T, err error) *capability.Error {
	t.Helper()
	var ce *capability.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a *capability.Error", err)
	}
	return ce
}

// TestListFromRecordedFiles: the fake answers from step 1's recorded bytes, and
// device.list returns each device with its serial, state, kind and the details.
func TestListFromRecordedFiles(t *testing.T) {
	a, dir := scenario(t, map[string]string{
		"devices.out":           fixture(t, "devices-l-two-emulators-pixel9a-pixeltablet.txt"),
		"emulator-5554.getprop": fixture(t, "pixel9a-getprop.txt"),
		"emulator-5554.avd":     fixture(t, "pixel9a-emu-avd-name.txt"),
		"emulator-5556.getprop": fixture(t, "pixeltablet-getprop.txt"),
		"emulator-5556.avd":     fixture(t, "pixeltablet-emu-avd-name.txt"),
	})
	got, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The version check runs once, before the first device call (the decision that
	// makes a mismatch visible before adb can restart the shared server).
	requireVersionBeforeDevices(t, dir)
	want := []Device{
		{Device: "emulator-5554", State: "device", Kind: "emulator", Online: true, Product: "sdk_gphone16k_arm64", Model: "sdk_gphone16k_arm64", TransportID: "1", AndroidVersion: "16", SDK: 36, AvdName: "Pixel_9a"},
		{Device: "emulator-5556", State: "device", Kind: "emulator", Online: true, Product: "sdk_gtablet_arm64", Model: "Pixel_Tablet", TransportID: "2", AndroidVersion: "15", SDK: 35, AvdName: "Pixel_Tablet"},
	}
	if len(got) != len(want) {
		t.Fatalf("listed %d devices, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		g := got[i]
		g.usb = false
		if g != want[i] {
			t.Errorf("device %d = %+v, want %+v", i, g, want[i])
		}
	}
}

// TestListPolicyAndStates: an unauthorized and an offline emulator are listed with
// their state (and never asked for details: nothing answers them); a physical device is
// hidden and never touched (ADR-0015 §4; plan R5).
func TestListPolicyAndStates(t *testing.T) {
	a, dir := scenario(t, map[string]string{
		"devices.out": "List of devices attached\n" +
			"emulator-5554 unauthorized transport_id:1\n" +
			"R58M12ABCDE device usb:1-1.2 product:raven model:Pixel_6_Pro device:raven transport_id:2\n" +
			"emulator-5556 offline transport_id:3\n" +
			"192.168.1.20:5555 device product:raven model:Pixel_6_Pro transport_id:4\n",
	})
	got, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Device != "emulator-5554" || got[0].State != "unauthorized" || got[0].Online ||
		got[1].Device != "emulator-5556" || got[1].State != "offline" {
		t.Fatalf("listed %+v, want the unauthorized and the offline emulator and no physical device", got)
	}
	for _, c := range devicetest.Calls(t, dir) {
		if strings.Contains(c, "R58M12ABCDE") || strings.Contains(c, "192.168") || strings.Contains(c, "-s ") {
			t.Errorf("adb was run as %q: a hidden or not-online device must not be asked anything", c)
		}
	}
}

// TestListSurvivesHangingDetails: when getprop and the console never answer, every
// device is still listed, with empty details, and the call ends within the plan's
// 12 s (5 s listing + 3 s details + the version check).
func TestListSurvivesHangingDetails(t *testing.T) {
	files := map[string]string{
		"devices.out": "List of devices attached\n" +
			"emulator-5554 device product:sdk_gphone16k_arm64 transport_id:1\n" +
			"emulator-5556 device product:sdk_gphone16k_arm64 transport_id:2\n" +
			"emulator-5558 device product:sdk_gtablet_arm64 transport_id:3\n",
	}
	for _, s := range []string{"emulator-5554", "emulator-5556", "emulator-5558"} {
		files[s+".getprop.hang"], files[s+".avd.hang"] = "", ""
	}
	a, _ := scenario(t, files)
	start := time.Now()
	got, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 12*time.Second {
		t.Errorf("took %v, want under 12 s", d)
	}
	if len(got) != 3 {
		t.Fatalf("listed %d devices, want all 3: %+v", len(got), got)
	}
	for _, d := range got {
		if d.AndroidVersion != "" || d.SDK != 0 || d.AvdName != "" || !d.Online {
			t.Errorf("%+v: details should be empty and the device still online", d)
		}
	}
}

// TestListNoticesOnStderrAreNotDevices: the run that starts a server prints its
// notices to stderr; the listing is empty, not an error. A device-looking line on
// stderr must not be listed either: only stdout is the listing.
func TestListNoticesOnStderrAreNotDevices(t *testing.T) {
	a, _ := scenario(t, map[string]string{
		"devices.out": fixture(t, "fork-5199-stdout.txt"),
		"devices.err": fixture(t, "fork-5199-stderr.txt") + "emulator-5554 device transport_id:1\n",
	})
	got, err := a.List(context.Background())
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("List = %+v, %v; want an empty, non-nil list and no error", got, err)
	}
}

// TestListHostFailuresAreDeviceADBFailed (Q3): a non-zero exit, a timeout and an output
// this version cannot read are DEVICE_ADB_FAILED with a cause naming the command and
// what happened, never an empty device list.
func TestListHostFailuresAreDeviceADBFailed(t *testing.T) {
	old := devicesTimeout
	devicesTimeout = 300 * time.Millisecond
	t.Cleanup(func() { devicesTimeout = old })

	cases := []struct {
		name  string
		files map[string]string
		in    []string // what the cause must contain
	}{
		{"exit status", map[string]string{"devices.out": "", "devices.err": "adb server version (41) doesn't match this client (39); killing...\nerror: could not install *smartsocket* listener: Address already in use\n", "devices.exit": "1"},
			[]string{"devices -l", "status 1", "Address already in use"}},
		{"timeout", map[string]string{"devices.hang": ""}, []string{"devices -l", "timed out"}},
		{"unreadable", map[string]string{"devices.out": "something else entirely\n"}, []string{"something else entirely"}},
	}
	for _, c := range cases {
		a, _ := scenario(t, c.files)
		got, err := a.List(context.Background())
		if err == nil {
			t.Errorf("%s: listed %+v without error", c.name, got)
			continue
		}
		ce := asCapError(t, err)
		if ce.Code != ErrADBFailed || ce.NextStep == "" {
			t.Errorf("%s: %+v, want %s with a next step", c.name, ce, ErrADBFailed)
		}
		for _, w := range c.in {
			if !strings.Contains(ce.Cause, w) {
				t.Errorf("%s: cause %q does not contain %q", c.name, ce.Cause, w)
			}
		}
	}
}

// TestListCancelledIsNotAnADBFailure: a caller that gives up gets its own cancellation,
// and only the client is killed.
func TestListCancelledIsNotAnADBFailure(t *testing.T) {
	a, _ := scenario(t, map[string]string{"devices.hang": ""})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	_, err := a.List(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// TestListWithoutADB: with nothing resolvable device.list fails with
// DEVICE_ADB_NOT_FOUND, a cause listing every candidate and a next step; an ADB naming
// nothing fails closed and the cause names the value (Q6). Nothing is run.
func TestListWithoutADB(t *testing.T) {
	empty := t.TempDir()
	for name, env := range map[string][]string{
		"nothing":            {"HOME=" + empty, "PATH=" + empty},
		"ADB naming no file": {"ADB=" + empty + "/nope/adb", "HOME=" + empty, "PATH=" + empty},
	} {
		a := NewADB(env, nil) // no starter: running anything would be ErrNoStarter, a different failure
		_, err := a.List(context.Background())
		ce := asCapError(t, err)
		if ce.Code != ErrADBNotFound || ce.NextStep == "" || !strings.Contains(ce.Cause, "Tried:") || !strings.Contains(ce.NextStep, "ADB") {
			t.Errorf("%s: %+v, want %s, a next step naming what to set and the candidates tried", name, ce, ErrADBNotFound)
		}
		if name != "nothing" && !strings.Contains(ce.Cause, empty+"/nope/adb") {
			t.Errorf("%s: cause %q does not name the bad ADB value", name, ce.Cause)
		}
	}
}

// TestListDescriptor pins what the registry publishes for device.list: a global,
// stateless query that needs adb (so open-world and read-only are derived), whose
// errors are the two codes the handler returns itself.
func TestListDescriptor(t *testing.T) {
	reg := capability.NewRegistry()
	Register(reg, NewADB(nil, nil))
	for _, d := range reg.All() {
		if d.Name != "device.list" {
			continue
		}
		an := d.ResolveAnnotations()
		if d.Kind != capability.Query || d.Scope != capability.Global || d.Statefulness != capability.Stateless ||
			len(d.Availability) != 1 || d.Availability[0] != capability.RequiresADB || !an.ReadOnly || !an.OpenWorld || d.Since != "0.4.0" {
			t.Errorf("device.list descriptor = %+v, annotations %+v", d, an)
		}
		var codes []string
		for _, c := range d.Errors {
			codes = append(codes, string(c))
		}
		if strings.Join(codes, ",") != "DEVICE_ADB_NOT_FOUND,DEVICE_ADB_FAILED" {
			t.Errorf("errors = %v", codes)
		}
		return
	}
	t.Error("device.list is not registered")
}

// requireVersionBeforeDevices: `adb version` is in the fake's call log, ahead of
// `devices -l`.
func requireVersionBeforeDevices(t *testing.T, dir string) {
	t.Helper()
	v, d := -1, -1
	for i, c := range devicetest.Calls(t, dir) {
		if c == "version" && v < 0 {
			v = i
		}
		if c == "devices -l" && d < 0 {
			d = i
		}
	}
	if v < 0 || d < 0 || v > d {
		t.Errorf("calls %q: want `version` before `devices -l`", devicetest.Calls(t, dir))
	}
}

// TestListReportsAVersionMismatch: the warning system.health carries is also on the
// device call (adb-device.md §Failure modes), and the check runs before `devices -l`.
// The fake client is 1.0.41; the loopback server says 42.
func TestListReportsAVersionMismatch(t *testing.T) {
	port, _ := fakeServer(t, []byte("OKAY0004002a"))
	dir := t.TempDir()
	devicetest.Write(t, dir, "devices.out", []byte("List of devices attached\n"))
	a := newRunner(t, devicetest.Env(t, dir, port))
	_, warnings, err := a.list(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "version 42") || !strings.Contains(warnings[0], "1.0.41") {
		t.Errorf("warnings = %q, want the mismatch between client 1.0.41 and server 42", warnings)
	}
	requireVersionBeforeDevices(t, dir)

	// Through the capability: the result carries it.
	reg := capability.NewRegistry()
	Register(reg, a)
	for _, d := range reg.All() {
		if d.Name != "device.list" {
			continue
		}
		res, err := d.Handler(context.Background(), &capability.Call{})
		if err != nil {
			t.Fatal(err)
		}
		if lr, ok := res.(ListResult); !ok || len(lr.Warnings) != 1 {
			t.Errorf("result = %+v, want one warning", res)
		}
	}
}

// TestListCancelledDuringDetails: a caller that gives up while the details run gets
// its cancellation, not a completed list of sparse devices.
func TestListCancelledDuringDetails(t *testing.T) {
	a, _ := scenario(t, map[string]string{
		"devices.out":                "List of devices attached\nemulator-5554 device transport_id:1\n",
		"emulator-5554.getprop.hang": "",
		"emulator-5554.avd.hang":     "",
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(700 * time.Millisecond); cancel() }()
	got, err := a.List(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("List = %+v, %v; want context.Canceled", got, err)
	}
}

// TestListDeadlineIsNotAnADBFailure: a caller whose deadline passes during `devices -l`
// gets its own context.DeadlineExceeded, not DEVICE_ADB_FAILED (the caller's limit is
// not an adb fault; only adb's own timeout is).
func TestListDeadlineIsNotAnADBFailure(t *testing.T) {
	a, _ := scenario(t, map[string]string{"devices.hang": ""})
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	_, err := a.List(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	var ce *capability.Error
	if errors.As(err, &ce) {
		t.Errorf("err is %s: a caller's deadline must not become a capability error", ce.Code)
	}
}

// TestListFailureNamesTheConfiguredPort: the next step for a failed adb names the port
// the environment configures, not a hardcoded 5037.
func TestListFailureNamesTheConfiguredPort(t *testing.T) {
	dir := t.TempDir()
	devicetest.Write(t, dir, "devices.exit", []byte("1"))
	port := closedPort(t)
	a := newRunner(t, devicetest.Env(t, dir, port))
	_, err := a.List(context.Background())
	ce := asCapError(t, err)
	if !strings.Contains(ce.NextStep, "port "+port+" ") || strings.Contains(ce.NextStep, "5037") {
		t.Errorf("next step %q should name port %s", ce.NextStep, port)
	}
}
