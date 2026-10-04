package device

import (
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestParseRecordedListings pins the parser against what real adb printed (step 1's
// files): the fields come from the line, and adb's "daemon not running" notices, which
// it prints to stderr, never become devices.
func TestParseRecordedListings(t *testing.T) {
	two, err := ParseDevices(fixture(t, "devices-l-two-emulators-pixel9a-pixeltablet.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(two) != 2 {
		t.Fatalf("parsed %d devices from the two-emulator listing, want 2: %+v", len(two), two)
	}
	want := []Device{
		{Device: "emulator-5554", State: "device", Kind: "emulator", Online: true, Product: "sdk_gphone16k_arm64", Model: "sdk_gphone16k_arm64", TransportID: "1"},
		{Device: "emulator-5556", State: "device", Kind: "emulator", Online: true, Product: "sdk_gtablet_arm64", Model: "Pixel_Tablet", TransportID: "2"},
	}
	for i := range want {
		got, w := two[i], want[i]
		got.usb = false
		if got != w {
			t.Errorf("device %d = %+v, want %+v", i, got, w)
		}
	}

	// The no-server run: stdout is the header alone, the notices are on stderr.
	none, err := ParseDevices(fixture(t, "fork-5199-stdout.txt"))
	if err != nil || len(none) != 0 {
		t.Errorf("the empty listing parsed to %+v, %v; want no devices", none, err)
	}
	// ... and the same notices on stdout, ahead of the header, are skipped, not devices.
	mixed, err := ParseDevices("* daemon not running; starting now at tcp:5199\n* daemon started successfully\nList of devices attached\n")
	if err != nil || len(mixed) != 0 {
		t.Errorf("notices before the header gave %+v, %v; want no devices and no error", mixed, err)
	}
}

// TestClassifier decides which devices are emulators. The decision it pins is the
// fail-safe one in adb-device.md §Device model: usb: wins over everything, and what
// cannot be recognised is physical, so the emulators-only default never admits it.
// Lines marked synthetic were written for this test: no phone was recorded
// (plan m1e, Unverified).
func TestClassifier(t *testing.T) {
	cases := []struct {
		name, line, serial, state, kind string
	}{
		{"recorded phone emulator", "emulator-5554 device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 device:emu64a16k transport_id:1", "emulator-5554", "device", "emulator"},
		{"recorded tablet emulator", "emulator-5556 device product:sdk_gtablet_arm64 model:Pixel_Tablet device:emu64a transport_id:2", "emulator-5556", "device", "emulator"},
		{"synthetic: offline emulator", "emulator-5554 offline transport_id:4", "emulator-5554", "offline", "emulator"},
		{"synthetic: unauthorized emulator, no product fields", "emulator-5554 unauthorized", "emulator-5554", "unauthorized", "emulator"},
		{"synthetic: recovery", "emulator-5554 recovery product:sdk_gphone64_arm64", "emulator-5554", "recovery", "emulator"},
		{"synthetic: usb phone", "R58M12ABCDE device usb:1-1.2 product:raven model:Pixel_6_Pro device:raven transport_id:7", "R58M12ABCDE", "device", "physical"},
		{"synthetic: usb device whose product looks like an emulator's", "R58M12ABCDE device usb:1-1.2 product:sdk_gphone64_arm64 model:x transport_id:7", "R58M12ABCDE", "device", "physical"},
		{"synthetic: tcp emulator by product", "localhost:5555 device product:sdk_gphone64_x86_64 model:sdk_gphone64_x86_64 transport_id:9", "localhost:5555", "device", "emulator"},
		{"synthetic: wifi phone", "192.168.1.20:5555 device product:raven model:Pixel_6_Pro transport_id:5", "192.168.1.20:5555", "device", "physical"},
		{"synthetic: unrecognisable, no usb", "mystery01 device product:foo", "mystery01", "device", "physical"},
		{"synthetic: unknown state", "ABC123 no permissions (user in plugdev group) usb:1-2", "ABC123", "unknown", "physical"},
	}
	for _, c := range cases {
		got, err := ParseDevices("List of devices attached\n" + c.line + "\n")
		if err != nil || len(got) != 1 {
			t.Errorf("%s: %+v, %v", c.name, got, err)
			continue
		}
		d := got[0]
		if d.Device != c.serial || d.State != c.state || d.Kind != c.kind || d.Online != (c.state == "device") {
			t.Errorf("%s: parsed %+v, want serial %s, state %s, kind %s", c.name, d, c.serial, c.state, c.kind)
		}
	}
}

// TestParseRejectsWhatItCannotRead: a listing with no header, or an entry that is not
// "<serial> <state> ...", is an error, so the caller reports adb failing instead of an
// empty device list (adb-device.md §Failure modes).
func TestParseRejectsWhatItCannotRead(t *testing.T) {
	for name, in := range map[string]string{
		"no header":      "emulator-5554 device\n",
		"empty":          "",
		"entry too thin": "List of devices attached\nlonely\n",
		"text first":     "adb: error: something\nList of devices attached\n",
	} {
		if devs, err := ParseDevices(in); err == nil {
			t.Errorf("%s: parsed %+v without error", name, devs)
		}
	}
}

func TestParseDetails(t *testing.T) {
	v, sdk := parseProps(fixture(t, "pixel9a-getprop.txt"))
	if v != "16" || sdk != 36 {
		t.Errorf("pixel9a: androidVersion %q sdk %d, want 16 and 36 (ro.build.version.*, not the ro.system.* copies)", v, sdk)
	}
	if v, sdk := parseProps(fixture(t, "pixeltablet-getprop.txt")); v != "15" || sdk != 35 {
		t.Errorf("pixeltablet: %q %d, want 15 and 35 (a CRLF dump)", v, sdk)
	}
	if n := parseAvdName(fixture(t, "pixeltablet-emu-avd-name.txt")); n != "Pixel_Tablet" {
		t.Errorf("avd name %q, want Pixel_Tablet from the CRLF reply", n)
	}
	if n := parseAvdName("KO: unknown command\r\n"); n != "" {
		t.Errorf("avd name %q from an error reply, want none", n)
	}
	if strings.Contains(parseAvdName(fixture(t, "pixel9a-emu-avd-name.txt")), "OK") {
		t.Error("the console's OK line leaked into the avd name")
	}
}
