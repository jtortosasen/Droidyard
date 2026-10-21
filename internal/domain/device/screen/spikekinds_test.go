package screen

import (
	"bytes"
	"testing"
	"time"
)

// Decision (plan m2y step 1): the spike tool's new control kinds put scrcpy v4.1's
// layouts on the wire (the source lines are cited in spike_test.go). A scroll of +1 notch
// is 1/16 as an i16 fixed point (0x0800), +16 saturates at 0x7fff, -16 is 0x8000; a cancel
// is the touch message with action 3; a reset is the single byte 17. A wrong width or
// offset here would make the S1/S4 runs measure a malformed message.
func TestSpikeControlKinds(t *testing.T) {
	steps, err := parseSpikeScript("1s:scroll:100:200:0:1;2s:scroll:1:2:16:-16;3s:cancel:5:6;4s:reset")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 4 || steps[3].at != 4*time.Second {
		t.Fatalf("steps = %d", len(steps))
	}
	want := []byte{3, 0, 0, 0, 100, 0, 0, 0, 200, 0x04, 0x38, 0x08, 0x00, 0, 0, 0x08, 0, 0, 0, 0, 0}
	if got := steps[0].encode(1080, 2048); !bytes.Equal(got, want) {
		t.Errorf("scroll = % x, want % x", got, want)
	}
	sat := steps[1].encode(1080, 2048)
	if !bytes.Equal(sat[13:17], []byte{0x7f, 0xff, 0x80, 0x00}) {
		t.Errorf("saturated scroll axes = % x", sat[13:17])
	}
	c := steps[2].encode(1080, 2048)
	if len(c) != 32 || c[0] != 2 || c[1] != 3 {
		t.Errorf("cancel = % x", c)
	}
	if got := steps[3].encode(1080, 2048); !bytes.Equal(got, []byte{17}) {
		t.Errorf("reset = % x", got)
	}
}
