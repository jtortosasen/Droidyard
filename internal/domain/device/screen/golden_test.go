package screen

import (
	"bytes"
	"testing"
)

// Decision: testdata/frame-golden.bin is one /v1/screen message as ADR-0078 §4 lays it
// out: a flags byte (1 key frame, 2 config), the PTS as a big-endian u64, then the Annex B
// bytes. Step 4's Go test and step 5's Vitest read this file, so the two encoders of the
// header meet one set of bytes; this test pins the file itself.
func TestFrameGolden(t *testing.T) {
	b := fixture(t, "frame-golden.bin")
	if len(b) < 9 {
		t.Fatalf("%d bytes", len(b))
	}
	if b[0] != 3 {
		t.Errorf("flags %d, want 3 (key frame and config: a merged packet)", b[0])
	}
	var pts uint64
	for _, c := range b[1:9] {
		pts = pts<<8 | uint64(c)
	}
	if pts != 1234567 {
		t.Errorf("pts %d, want 1234567", pts)
	}
	payload := b[9:]
	sps, err := ParseSPS(payload)
	if err != nil || sps.Width != 590 || sps.Height != 1280 {
		t.Errorf("the payload's SPS: %+v, %v", sps, err)
	}
	if !bytes.HasSuffix(payload, []byte{0, 0, 0, 1, 0x65, 0x88, 0x84, 0x21, 0xa0}) {
		t.Errorf("the payload does not end with the key frame NAL unit: % x", payload)
	}
	if len(payload) != 33+9 {
		t.Errorf("payload %d bytes, want 42", len(payload))
	}
}
