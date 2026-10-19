package device

import (
	"context"
	"reflect"
	"testing"
)

// TestShareableSeesWhatTheListingHides: hosting.share is what admits a physical device, so
// the device it must see is exactly the one device.list hides (ADR-0108 §1). With the
// default policy (no physical device admitted) the listing is empty of the phones, and
// Shareable still holds every entry with its state, kind and model; a serial that two entries
// report is marked on both (ADR-0103 §1).
func TestShareableSeesWhatTheListingHides(t *testing.T) {
	a, _ := scenario(t, map[string]string{
		"devices.out": "List of devices attached\n" +
			"emulator-5554 device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:1\n" +
			"R58M001 device usb:1-1.1 product:raven model:Pixel_6_Pro transport_id:2\n" +
			"R58M002 unauthorized usb:1-1.2 transport_id:3\n" +
			"R58M003 offline usb:1-1.3 transport_id:4\n" +
			"DUP1 device usb:1-1.4 product:raven model:Pixel_6_Pro transport_id:5\n" +
			"DUP1 device usb:1-1.5 product:raven model:Pixel_6_Pro transport_id:6\n",
	})
	got, err := a.Shareable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []ShareableDevice{
		{Device: "emulator-5554", State: "device", Kind: "emulator", Model: "sdk_gphone16k_arm64"},
		{Device: "R58M001", State: "device", Kind: "physical", Model: "Pixel_6_Pro"},
		{Device: "R58M002", State: "unauthorized", Kind: "physical"},
		{Device: "R58M003", State: "offline", Kind: "physical"},
		{Device: "DUP1", State: "device", Kind: "physical", Model: "Pixel_6_Pro", Duplicate: true},
		{Device: "DUP1", State: "device", Kind: "physical", Model: "Pixel_6_Pro", Duplicate: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Shareable = %+v\nwant %+v", got, want)
	}
	// The same listing through device.list shows the emulator and nothing physical.
	listed, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range listed {
		if d.Kind == KindPhysical {
			t.Errorf("device.list shows the physical device %s with the default policy; Shareable must be what sees it", d.Device)
		}
	}
}

// TestShareableWithoutADB: no adb is DEVICE_ADB_NOT_FOUND, the contract's code.
func TestShareableWithoutADB(t *testing.T) {
	a := NewADB([]string{"PATH="}, nil)
	_, err := a.Shareable(context.Background())
	if ce := asCapError(t, err); ce.Code != ErrADBNotFound {
		t.Errorf("code %s, want %s", ce.Code, ErrADBNotFound)
	}
}
