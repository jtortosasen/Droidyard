package device

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// The physical device the policy rows turn on: a usb device, so classify calls it
// physical (device/parse.go), beside the online emulator uiSerial.
const (
	physSerial = "R58M123"
	physLine   = "R58M123 device usb:1-1.2 product:raven model:Pixel_6_Pro transport_id:2\n"
)

// policyAt is a real *policy.Policy reading the configuration under dir: it never reads
// this machine's configuration, and HOME is nowhere.
func policyAt(t *testing.T, dir string) *policy.Policy {
	t.Helper()
	base := platform.Current()
	env := base
	env.Getenv = func(k string) string {
		switch k {
		case "DROIDYARD_CONFIG_DIR":
			return dir
		case "HOME":
			return "/nonexistent-droidyard-home"
		}
		return base.Getenv(k)
	}
	return policy.New(env)
}

// writePolicyConfig writes body at the policy directory dir, as the user's hand edit
// would. A test rewrites it between calls to prove the policy is read live.
func writePolicyConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, policy.FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// devicePolicyFixture is a device capability over the fake adb with a real configuration
// policy set on it. dir is the fake scenario directory; cfg is the configuration
// directory, so a test can rewrite the file between calls.
type devicePolicyFixture struct {
	adb *ADB
	ops *capability.Operations
	dir string
	cfg string
}

// newDevicePolicyFixture is the fake adb listing an online emulator and the physical
// device physSerial, with a real policy over a fresh configuration directory holding
// config (absent when ""). The physical device's screenshot and readiness answer.
func newDevicePolicyFixture(t *testing.T, files map[string]string, config string) *devicePolicyFixture {
	t.Helper()
	base := map[string]string{
		"devices.out":                     uiList(phoneLine, physLine),
		uiSerial + ".bootcompleted.out":   "1\n",
		uiSerial + ".dumptty.out":         fixture(t, "pixel9a-uidump-devtty.xml"),
		uiSerial + ".push.exit":           "1",
		uiSerial + ".screencap.out":       fixture(t, "pixel9a-screencap.png"),
		physSerial + ".bootcompleted.out": "1\n",
		physSerial + ".screencap.out":     fixture(t, "pixel9a-screencap.png"),
	}
	for k, v := range files {
		base[k] = v
	}
	dir := t.TempDir()
	for name, content := range base {
		devicetest.Write(t, dir, name, []byte(content))
	}
	adb := newFake(t, dir, closedPort(t))
	cfg := t.TempDir()
	if config != "" {
		writePolicyConfig(t, cfg, config)
	}
	adb.SetPolicy(policyAt(t, cfg))
	reg := capability.NewRegistry()
	store := testStore(t)
	Register(reg, adb, store, nil)
	ops := capability.NewOperations(reg)
	ops.RegisterCapabilities()
	return &devicePolicyFixture{adb: adb, ops: ops, dir: dir, cfg: cfg}
}

func (f *devicePolicyFixture) run(t *testing.T, cap, params string) capability.Outcome {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: cap, Params: []byte(params)})
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *devicePolicyFixture) list(t *testing.T) []Device {
	t.Helper()
	out := f.run(t, "device.list", `{}`)
	if out.State != capability.StateCompleted {
		t.Fatalf("device.list: %s, %+v", out.State, out.Error)
	}
	return out.Result.(ListResult).Devices
}

func (f *devicePolicyFixture) shoot(t *testing.T, serial string) capability.Outcome {
	t.Helper()
	return f.run(t, "device.screenshot", `{"device":"`+serial+`"}`)
}

func listedSerials(devices []Device) []string {
	out := make([]string, 0, len(devices))
	for _, d := range devices {
		out = append(out, d.Device)
	}
	return out
}

// Decision (plan m1k step 5, check): a physical device is listed and usable only when
// `devices.allowPhysical` is true *and* its exact serial is in `devices.allowlist`, both
// read from the configuration at the call; emulators need no entry. The refusal's cause
// names the condition that failed (ADR-0015 §4; ADR-0032 §2; plan m1k D7).
func TestPhysicalDeviceAdmission(t *testing.T) {
	admit := `{"schemaVersion":1,"devices":{"allowPhysical":true,"allowlist":["R58M123"]}}`
	cases := []struct {
		name       string
		config     string // the file's body; "" leaves it absent
		wantListed bool
		wantCause  string // the DEVICE_NOT_ALLOWED cause when refused
	}{
		{"no file", "", false, "allowPhysical is false"},
		{"allowPhysical true, empty allowlist", `{"schemaVersion":1,"devices":{"allowPhysical":true,"allowlist":[]}}`, false, "not in devices.allowlist"},
		{"a prefix of the serial, not the exact serial", `{"schemaVersion":1,"devices":{"allowPhysical":true,"allowlist":["R58M12"]}}`, false, "not in devices.allowlist"},
		{"the serial in the allowlist but allowPhysical false", `{"schemaVersion":1,"devices":{"allowPhysical":false,"allowlist":["R58M123"]}}`, false, "allowPhysical is false"},
		{"both conditions admit it", admit, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newDevicePolicyFixture(t, nil, c.config)
			listed := f.list(t)
			if !slices.Contains(listedSerials(listed), uiSerial) {
				t.Errorf("listed %v, want the emulator %s: an emulator needs no allowlist entry", listedSerials(listed), uiSerial)
			}
			hasPhysical := slices.Contains(listedSerials(listed), physSerial)
			if hasPhysical != c.wantListed {
				t.Errorf("physical device listed=%v, want %v: %v", hasPhysical, c.wantListed, listedSerials(listed))
			}

			out := f.shoot(t, physSerial)
			if c.wantListed {
				if out.State != capability.StateCompleted {
					t.Fatalf("screenshot %s: %s, %+v; an admitted device reaches the readiness check and completes", physSerial, out.State, out.Error)
				}
				return
			}
			ce := mustFail(t, out, ErrNotAllowed)
			if !strings.Contains(ce.Cause, c.wantCause) {
				t.Errorf("cause %q, want it to name %q", ce.Cause, c.wantCause)
			}
			if !strings.Contains(ce.NextStep, physSerial) {
				t.Errorf("nextStep %q, want it to name %s", ce.NextStep, physSerial)
			}
		})
	}
}

// Decision (plan m1k step 5, check): an admitted physical device is listed with
// kind physical, and a device call for it runs (the fake's readiness check and the
// capture both happen). A policy that admits without classifying is a bug.
func TestAdmittedPhysicalDeviceIsListedAsPhysicalAndUsed(t *testing.T) {
	f := newDevicePolicyFixture(t, nil, `{"schemaVersion":1,"devices":{"allowPhysical":true,"allowlist":["R58M123"]}}`)
	listed := f.list(t)
	var phys *Device
	for i := range listed {
		if listed[i].Device == physSerial {
			phys = &listed[i]
		}
	}
	if phys == nil {
		t.Fatalf("listed %v, want the physical device %s", listedSerials(listed), physSerial)
	}
	if phys.Kind != KindPhysical || phys.State != StateDevice || !phys.Online {
		t.Errorf("physical device = %+v, want kind physical, state device, online", *phys)
	}
	if out := f.shoot(t, physSerial); out.State != capability.StateCompleted {
		t.Fatalf("screenshot %s: %s, %+v", physSerial, out.State, out.Error)
	}
	// The call reached the fake: readiness then the capture both ran for the device.
	var readiness, capture bool
	for _, c := range devicetest.Calls(t, f.dir) {
		if c == "-s "+physSerial+" shell getprop sys.boot_completed" {
			readiness = true
		}
		if c == "-s "+physSerial+" exec-out screencap -p" {
			capture = true
		}
	}
	if !readiness || !capture {
		t.Errorf("readiness=%v capture=%v, want both for the admitted device", readiness, capture)
	}
}

// Decision (plan m1k step 5, check): DEVICE_NOT_FOUND's online candidates name a
// physical device only when its serial is admitted, so an unrelated serial's error does
// not disclose an unadmitted device.
func TestOnlineCandidatesFollowAdmission(t *testing.T) {
	unknown := func(t *testing.T, f *devicePolicyFixture) []string {
		t.Helper()
		ce := mustFail(t, f.shoot(t, "emulator-5999"), ErrNotFound)
		online, _ := ce.Details["online"].([]string)
		return online
	}

	unadmitted := newDevicePolicyFixture(t, nil, `{"schemaVersion":1,"devices":{"allowPhysical":true,"allowlist":[]}}`)
	if online := unknown(t, unadmitted); slices.Contains(online, physSerial) {
		t.Errorf("online %v names the unadmitted physical device %s", online, physSerial)
	}
	if online := unknown(t, unadmitted); !slices.Contains(online, uiSerial) {
		t.Errorf("online %v does not name the emulator %s", online, uiSerial)
	}

	admitted := newDevicePolicyFixture(t, nil, `{"schemaVersion":1,"devices":{"allowPhysical":true,"allowlist":["R58M123"]}}`)
	if online := unknown(t, admitted); !slices.Contains(online, physSerial) {
		t.Errorf("online %v does not name the admitted physical device %s", online, physSerial)
	}
}

// Decision (plan m1k step 5, check): an invalid configuration refuses a physical device
// with a cause naming the file, while an emulator call completes unaffected (ADR-0032
// §4: the bad file's keys fail closed, and it is reported by the capability it affects).
func TestInvalidConfigRefusesPhysicalOnly(t *testing.T) {
	f := newDevicePolicyFixture(t, nil, `{"schemaVersion":1,"devices":{"allowPhysical":"yes"}}`)
	ce := mustFail(t, f.shoot(t, physSerial), ErrNotAllowed)
	if !strings.Contains(ce.Cause, policy.FileName) {
		t.Errorf("cause %q, want it to name the configuration file %s", ce.Cause, policy.FileName)
	}
	if out := f.shoot(t, uiSerial); out.State != capability.StateCompleted {
		t.Fatalf("emulator screenshot: %s, %+v; an invalid file must not affect an emulator", out.State, out.Error)
	}
}

// Decision (plan m1k step 5, check): the policy is read live from the configuration, so
// an edit between two calls decides the second, in both directions, without a restart
// (ADR-0032 §6). A policy read once at start fails the second call.
func TestPolicyEditBetweenCallsDecidesTheSecond(t *testing.T) {
	f := newDevicePolicyFixture(t, nil, `{"schemaVersion":1,"devices":{"allowPhysical":true,"allowlist":["R58M123"]}}`)
	if out := f.shoot(t, physSerial); out.State != capability.StateCompleted {
		t.Fatalf("first call (admitted): %s, %+v", out.State, out.Error)
	}
	// Revoke: rewrite the file with a different length, so no cache could serve it.
	writePolicyConfig(t, f.cfg, `{"schemaVersion":1,"devices":{"allowPhysical":false}}`)
	mustFail(t, f.shoot(t, physSerial), ErrNotAllowed)
	// Admit again, and it is usable once more.
	writePolicyConfig(t, f.cfg, `{"schemaVersion":1,"devices":{"allowPhysical":true,"allowlist":["R58M123"]}}`)
	if out := f.shoot(t, physSerial); out.State != capability.StateCompleted {
		t.Fatalf("third call (readmitted): %s, %+v", out.State, out.Error)
	}
}

// Decision (plan m1k step 5, check): a serial that is not the exact allowlist entry is
// refused, so a prefix (a shorter serial) never admits a longer one (ADR-0032 §2: an
// exact serial per call).
func TestAllowlistMatchIsExact(t *testing.T) {
	f := newDevicePolicyFixture(t, nil, `{"schemaVersion":1,"devices":{"allowPhysical":true,"allowlist":["R58M12"]}}`)
	ce := mustFail(t, f.shoot(t, physSerial), ErrNotAllowed)
	if !strings.Contains(ce.Cause, "not in devices.allowlist") {
		t.Errorf("cause %q, want the exact-match refusal", ce.Cause)
	}
}

// Decision (m1k §Changes, step 5 B1): a device string that is not a listed serial is
// refused by the authorization hook, whatever the capability. adb's -s also accepts
// selectors (usb:<path>, model:, product:, device:) that name a device under another
// name; device.inputText reached the unadmitted physical device through them, since it
// runs no readiness check without a target. Seen failing with the hook leaving an
// unlisted string to the handler.
func TestSelectorsNeverReachADevice(t *testing.T) {
	f := newDevicePolicyFixture(t, nil, "")
	for _, sel := range []string{"usb:1-1.2", "model:Pixel_6_Pro", "product:raven", "device:raven"} {
		out := f.run(t, "device.inputText", `{"device":"`+sel+`","text":"hi"}`)
		if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != ErrNotFound {
			t.Errorf("device.inputText {%s}: %s %+v; want DEVICE_NOT_FOUND", sel, out.State, out.Error)
		}
	}
	for _, call := range devicetest.Calls(t, f.dir) {
		if strings.HasPrefix(call, "-s ") {
			t.Errorf("a device command ran: %q", call)
		}
	}
}

// Decision (register S3): under an invalid configuration a connected physical device is
// hidden, and device.list says so in its warnings, naming the file, once, without the
// serial; with a valid configuration that simply does not admit the device, or with no
// physical device connected, there is no warning (the file said no, or there is nothing
// to hide). A listing that hid the device silently looked like an unplugged phone.
func TestListWarnsWhenAnInvalidConfigurationHidesADevice(t *testing.T) {
	warningsOf := func(t *testing.T, f *devicePolicyFixture) []string {
		t.Helper()
		out := f.run(t, "device.list", `{}`)
		if out.State != capability.StateCompleted {
			t.Fatalf("device.list: %s, %+v", out.State, out.Error)
		}
		res := out.Result.(ListResult)
		if slices.Contains(listedSerials(res.Devices), physSerial) {
			t.Fatalf("listed %v: the physical device must stay hidden", listedSerials(res.Devices))
		}
		return res.Warnings
	}

	f := newDevicePolicyFixture(t, nil, `{"schemaVersion":1,"devices":{"allowPhysical":"yes"}}`)
	got := warningsOf(t, f)
	if len(got) != 1 || !strings.Contains(got[0], policy.FileName) || strings.Contains(got[0], physSerial) {
		t.Errorf("warnings = %q; want one warning that names the configuration file and not the serial", got)
	}

	for name, config := range map[string]string{
		"no file":                         "",
		"a valid file that says no":       `{"schemaVersion":1,"devices":{"allowPhysical":false}}`,
		"a valid file, serial not listed": `{"schemaVersion":1,"devices":{"allowPhysical":true,"allowlist":["OTHER"]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if got := warningsOf(t, newDevicePolicyFixture(t, nil, config)); len(got) != 0 {
				t.Errorf("warnings = %q; want none: the file is usable and said no", got)
			}
		})
	}

	t.Run("an invalid file and no physical device connected", func(t *testing.T) {
		f := newDevicePolicyFixture(t, map[string]string{"devices.out": uiList(phoneLine)}, `{"schemaVersion":1,"devices":{"allowPhysical":"yes"}}`)
		out := f.run(t, "device.list", `{}`)
		if out.State != capability.StateCompleted {
			t.Fatalf("device.list: %s, %+v", out.State, out.Error)
		}
		if w := out.Result.(ListResult).Warnings; len(w) != 0 {
			t.Errorf("warnings = %q; want none: nothing is hidden", w)
		}
	})

	t.Run("a duplicate key is an invalid file", func(t *testing.T) {
		f := newDevicePolicyFixture(t, nil, `{"schemaVersion":1,"devices":{"allowPhysical":true,"allowPhysical":true,"allowlist":["R58M123"]}}`)
		if got := warningsOf(t, f); len(got) != 1 {
			t.Errorf("warnings = %q; want the one warning", got)
		}
	})
}
