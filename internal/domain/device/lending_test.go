package device

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// usbLine is a physical phone's line in `adb devices -l`.
func usbLine(serial string) string {
	return serial + " device usb:1-1." + serial[len(serial)-1:] + " product:raven model:Pixel_6_Pro transport_id:" + serial[len(serial)-1:] + "\n"
}

// hostFixture is a host's device capabilities over the fake adb, with the real policy
// (the physical policy and hosting.lend) read live from a configuration directory the
// test rewrites. Calls are made as a local caller or as a paired peer.
type hostFixture struct {
	adb   *ADB
	ops   *capability.Operations
	cfg   string
	dir   string // the fake adb's scenario directory
	store *artifact.Store
}

const (
	anna = "anna"
	bob  = "bob"
	fp1  = "1111111111111111111111111111111111111111111111111111111111111111"
	fp2  = "2222222222222222222222222222222222222222222222222222222222222222"
)

func newHostFixture(t *testing.T, listing string, serials []string, config string) *hostFixture {
	t.Helper()
	files := map[string]string{"devices.out": listing, uiSerial + ".bootcompleted.out": "1\n"}
	for _, s := range append([]string{uiSerial}, serials...) {
		files[s+".bootcompleted.out"] = "1\n"
		files[s+".dumptty.out"] = fixture(t, "pixel9a-uidump-devtty.xml")
		files[s+".push.exit"] = "1"
	}
	dir := t.TempDir()
	for name, content := range files {
		devicetest.Write(t, dir, name, []byte(content))
	}
	adb := newFake(t, dir, closedPort(t))
	cfg := t.TempDir()
	writePolicyConfig(t, cfg, config)
	pol := policyAt(t, cfg)
	adb.SetPolicy(pol)
	adb.SetLending(pol)
	reg := capability.NewRegistry()
	store := testStore(t)
	Register(reg, adb, store, nil)
	ops := capability.NewOperations(reg)
	ops.RegisterCapabilities()
	return &hostFixture{adb: adb, ops: ops, cfg: cfg, dir: dir, store: store}
}

// call runs capName as who: "" is a local caller, a name a paired peer of that name.
func (f *hostFixture) call(t *testing.T, who, capName, params string) capability.Outcome {
	t.Helper()
	req := capability.Request{Capability: capName, Params: []byte(params), Client: capability.ClientCLI}
	if who != "" {
		req.Client = capability.ClientPeer
		req.Origin = capability.Origin{ID: "peer-" + who, Name: who}
	}
	id, err := f.ops.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *hostFixture) listed(t *testing.T, who string) ([]string, []string) {
	t.Helper()
	out := f.call(t, who, "device.list", `{}`)
	if out.State != capability.StateCompleted {
		t.Fatalf("device.list as %q: %s, %+v", who, out.State, out.Error)
	}
	r := out.Result.(ListResult)
	return listedSerials(r.Devices), r.Warnings
}

func (f *hostFixture) tree(t *testing.T, who, serial string) capability.Outcome {
	t.Helper()
	return f.call(t, who, "device.uiTree", `{"device":"`+serial+`"}`)
}

func online(t *testing.T, ce *capability.Error) []string {
	t.Helper()
	o, _ := ce.Details["online"].([]string)
	return o
}

const lendingConfig = `{"schemaVersion":1,
 "devices":{"allowPhysical":true,"allowlist":["R58M001","R58M002","R58M004","emulator-5554"]},
 "hosting":{"peers":[{"name":"anna","fingerprint":"` + fp1 + `"},{"name":"bob","fingerprint":"` + fp2 + `"}],
  "lend":[{"device":"R58M001","to":["anna"]},{"device":"R58M002","to":["bob"]},
          {"device":"R58M003","to":["anna"]},{"device":"emulator-5554","to":["anna"]},
          {"device":"R58M004","to":["anna","bob"]}]}}`

// Decision (ADR-0099 §1; plan m2t Q3 §1): a paired peer sees and reaches only the devices
// that are lent to it AND that the host's own physical policy admits AND that are physical.
// Everything else is as if it were not there: absent from its listing, DEVICE_NOT_FOUND
// when named, and absent from that error's online candidates. Lending never widens the
// policy (R58M003 is lent but not allowlisted) and never lends an emulator. The host's
// own callers keep the devices they had.
func TestLendingToPeers(t *testing.T) {
	listing := uiList(phoneLine, usbLine("R58M001"), usbLine("R58M002"), usbLine("R58M003"), usbLine("R58M004"))
	f := newHostFixture(t, listing, []string{"R58M001", "R58M002", "R58M003", "R58M004"}, lendingConfig)

	if got, _ := f.listed(t, anna); !slices.Equal(got, []string{"R58M001", "R58M004"}) {
		t.Errorf("anna's list = %v; want exactly the physical, admitted devices lent to her", got)
	}
	if got, warnings := f.listed(t, bob); !slices.Equal(got, []string{"R58M002", "R58M004"}) || len(warnings) != 0 {
		t.Errorf("bob's list = %v %v; want what is lent to bob, and none of the host's warnings", got, warnings)
	}
	// The host's own list is unchanged by the lending.
	if got, _ := f.listed(t, ""); !slices.Equal(got, []string{uiSerial, "R58M001", "R58M002", "R58M004"}) {
		t.Errorf("the host's own list = %v; want its emulator and the allowlisted phones", got)
	}

	if out := f.tree(t, anna, "R58M001"); out.State != capability.StateCompleted {
		t.Fatalf("anna's uiTree of her device: %s, %+v", out.State, out.Error)
	}
	for _, c := range []struct{ serial, why string }{
		{"R58M002", "lent to another peer"},
		{"R58M003", "lent but not in the host's allowlist"},
		{uiSerial, "an emulator named in lend"},
		{"R58M999", "not connected"},
	} {
		ce := mustFail(t, f.tree(t, anna, c.serial), ErrNotFound)
		if got := online(t, ce); !slices.Equal(got, []string{"R58M001", "R58M004"}) {
			t.Errorf("%s (%s): online = %v; want only what is lent to anna", c.serial, c.why, got)
		}
	}
	// The host's own caller reaches the emulator and R58M002 as before.
	if out := f.tree(t, "", "R58M002"); out.State != capability.StateCompleted {
		t.Errorf("the host's own uiTree of R58M002: %s, %+v", out.State, out.Error)
	}

	// hosting.lend is read at each call: un-lending takes the device away at the next one.
	writePolicyConfig(t, f.cfg, strings.Replace(lendingConfig, `{"device":"R58M001","to":["anna"]},`, "", 1))
	mustFail(t, f.tree(t, anna, "R58M001"), ErrNotFound)
	if got, _ := f.listed(t, anna); !slices.Equal(got, []string{"R58M004"}) {
		t.Errorf("anna's list after R58M001 is no longer lent = %v", got)
	}
}

// Decision (ADR-0099 §2): a peer calls only what is Lendable. The core refuses the rest
// with REQUEST_NOT_ALLOWED_HERE, before an operation exists. This pins which device
// capabilities are lent so far; a capability added to the set must be added here. The three
// UI reads joined it with ADR-0115 Decision 1, and the five of the UI recording with Decision 2.
func TestDeviceLendableSet(t *testing.T) {
	reg := capability.NewRegistry()
	Register(reg, newFake(t, t.TempDir(), closedPort(t)), testStore(t), nil)
	var got []string
	for _, d := range reg.All() {
		if strings.HasPrefix(d.Name, "device.") && d.Lendable {
			got = append(got, d.Name)
		}
	}
	slices.Sort(got)
	want := []string{"device.launch", "device.list", "device.pressKey", "device.scroll", "device.swipe", "device.tap", "device.tapElement", "device.uiTree", "device.uninstall", "device.inputText", "device.watchScreen", "device.installUpload", "device.screenshot", "device.logs", "device.watchLogs", "device.uiDump", "device.uiEvents", "device.watchUiEvents", "device.recordUi", "device.stopUiRecording", "device.uiRecordings", "device.watchUiRecordings", "device.deleteUiRecording", "device.systemPanel", "device.forceStop", "device.rotate"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("lendable device capabilities = %v; want %v", got, want)
	}
}

// Decision (ADR-0103 §1): a serial that two devices report is refused for both, with its
// reason, in the listing (a warning) and in a call (DEVICE_NOT_ALLOWED, whose cause names
// the duplicate), because the allowlist names serials and one entry would admit both. A
// device the policy hides is not disclosed through the warning, and a duplicate is never
// lent.
func TestDuplicateSerial(t *testing.T) {
	twin := "R58M001 device usb:1-1.2 product:raven model:Pixel_6_Pro transport_id:2\n" +
		"R58M001 device usb:1-1.3 product:raven model:Pixel_6_Pro transport_id:3\n"
	admit := `{"schemaVersion":1,"devices":{"allowPhysical":true,"allowlist":["R58M001"]},
	  "hosting":{"peers":[{"name":"anna","fingerprint":"` + fp1 + `"}],"lend":[{"device":"R58M001","to":["anna"]}]}}`
	f := newHostFixture(t, uiList(phoneLine, twin), []string{"R58M001"}, admit)

	got, warnings := f.listed(t, "")
	if !slices.Equal(got, []string{uiSerial}) {
		t.Errorf("list = %v; want the emulator only: neither twin is listed", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "2 devices report the serial R58M001; droidyard cannot tell them apart") {
		t.Errorf("warnings = %q; want the duplicate named", warnings)
	}
	ce := mustFail(t, f.tree(t, "", "R58M001"), ErrNotAllowed)
	if !strings.Contains(ce.Cause, "2 devices report the serial R58M001") {
		t.Errorf("cause %q; want it to name the duplicate", ce.Cause)
	}
	// Never lent, though lend names it and the policy admits the serial.
	mustFail(t, f.tree(t, anna, "R58M001"), ErrNotFound)
	if got, _ := f.listed(t, anna); len(got) != 0 {
		t.Errorf("anna's list = %v; a duplicated serial is never lent", got)
	}

	// The policy does not admit the serial: nothing is said about it.
	hidden := newHostFixture(t, uiList(phoneLine, twin), []string{"R58M001"}, `{"schemaVersion":1}`)
	if _, warnings := hidden.listed(t, ""); len(warnings) != 0 {
		t.Errorf("warnings = %q; a device the policy hides is not disclosed by a duplicate warning", warnings)
	}
	if ce := mustFail(t, hidden.tree(t, "", "R58M001"), ErrNotAllowed); strings.Contains(ce.Cause, "report the serial") {
		t.Errorf("cause %q; the policy's reason comes first and discloses no duplicate", ce.Cause)
	}
}

// Decision (ADR-0103 §2): a usb: field makes a device physical, first; otherwise a listed
// emulator product makes an emulator only on emulator-<port> or a loopback serial, so a
// device on the network that reports an emulator's product is physical and under the
// policy.
func TestEmulatorClassification(t *testing.T) {
	cases := []struct{ line, kind string }{
		{"10.0.0.5:5555 device product:sdk_gphone64_arm64 model:x transport_id:1", KindPhysical},
		{"127.0.0.1:5555 device product:sdk_gphone64_arm64 model:x transport_id:1", KindEmulator},
		{"localhost:5555 device product:sdk_gtablet_arm64 model:x transport_id:1", KindEmulator},
		{"emulator-5554 device product:sdk_gphone64_arm64 model:x transport_id:1", KindEmulator},
		{"emulator-5554 device usb:1-1.2 product:sdk_gphone64_arm64 model:x transport_id:1", KindPhysical},
		{"127.0.0.1:5555 device product:raven model:x transport_id:1", KindPhysical},
		{"10.0.0.5:5555 device usb:1-1 product:sdk_gphone64_arm64 model:x transport_id:1", KindPhysical},
	}
	for _, c := range cases {
		got, err := ParseDevices("List of devices attached\n" + c.line + "\n")
		if err != nil || len(got) != 1 {
			t.Fatalf("%q: %+v, %v", c.line, got, err)
		}
		if got[0].Kind != c.kind {
			t.Errorf("%q: kind %s; want %s", c.line, got[0].Kind, c.kind)
		}
	}
}
