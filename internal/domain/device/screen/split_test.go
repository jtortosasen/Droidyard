package screen

import (
	"bytes"
	"context"
	"testing"
)

// Decision (ADR-0100 §5): Split undoes the Hub's join form exactly. The recording's config
// and first key frame go through a Hub; its viewer's first packet is the merged form, and
// Split gives back the two packets the device sent, byte for byte and in order. Seen
// failing with Split returning the merged packet whole: one packet of the config and the
// key frame, not two.
func TestSplitUndoesTheHubsJoinForm(t *testing.T) {
	pkts, _, err := collect(bytes.NewReader(fixture(t, "scrcpy-api31-portrait.bin")))
	if err != nil {
		t.Fatal(err)
	}
	h := NewHub()
	h.Push(pkts[0])
	h.Push(pkts[1])
	v := h.Join()
	merged, err := v.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !merged.Key || !merged.Config {
		t.Fatalf("the Hub's first packet %+v is not the join form", merged)
	}
	got := Split(merged)
	if len(got) != 2 {
		t.Fatalf("Split gave %d packets, want the config and the key frame", len(got))
	}
	if !got[0].Config || got[0].Key || !bytes.Equal(got[0].Data, pkts[0].Data) {
		t.Errorf("first piece %+v, want the device's config packet", got[0])
	}
	if !got[1].Key || got[1].Config || !bytes.Equal(got[1].Data, pkts[1].Data) || got[1].PTS != pkts[1].PTS {
		t.Errorf("second piece (key %v config %v, %d bytes, pts %d), want the device's key frame", got[1].Key, got[1].Config, len(got[1].Data), got[1].PTS)
	}

	// A Hub fed the pieces joins a viewer with the same merged form: the client's Hub keeps
	// ADR-0078 §5 over a stream another Hub already merged.
	h2 := NewHub()
	for _, p := range got {
		h2.Push(p)
	}
	again, err := h2.Join().Next(context.Background())
	if err != nil || !again.Key || !again.Config || !bytes.Equal(again.Data, merged.Data) {
		t.Errorf("a second Hub joined %+v (%v), want the same merged packet", again, err)
	}
}

// Decision: where the config ends is read from the NAL units, so both start code forms and
// a device that repeats its parameter sets in front of a key frame split without losing or
// inventing a byte; what is not the join form is returned as it is.
func TestSplitBoundaries(t *testing.T) {
	sps4 := []byte{0, 0, 0, 1, 0x67, 0x42, 0xc0, 0x29}
	pps3 := []byte{0, 0, 1, 0x68, 0xce, 0x3c}
	idr4 := []byte{0, 0, 0, 1, 0x65, 0x88, 0x84}
	idr3 := []byte{0, 0, 1, 0x65, 0x88}
	join := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

	for _, c := range []struct {
		name string
		data []byte
		cut  int // where the config ends; -1: not split
		key  bool
	}{
		{"four-byte start codes", join(sps4, pps3, idr4), len(sps4) + len(pps3), true},
		{"three-byte key frame", join(sps4, pps3, idr3), len(sps4) + len(pps3), true},
		{"repeated parameter sets join the config", join(sps4, pps3, sps4, pps3, idr4), len(sps4) + len(pps3)*2 + len(sps4), true},
		{"a key frame with no parameter set before it", idr4, 0, true},
		{"parameter sets alone", join(sps4, pps3), len(sps4) + len(pps3), false},
		{"not Annex B", []byte{1, 2, 3, 4, 5}, 0, true},
		{"truncated after a start code", []byte{0, 0, 0, 1}, 0, true},
		{"a forbidden bit set", join(sps4, []byte{0, 0, 1, 0xe8, 1}, idr4), len(sps4), true},
		{"empty", nil, 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := Packet{Key: true, Config: true, PTS: 77, Data: c.data}
			got := Split(in)
			var all []byte
			for _, p := range got {
				all = append(all, p.Data...)
				if p.PTS != 77 {
					t.Errorf("a piece lost its PTS: %+v", p)
				}
				if p.Config && p.Key {
					t.Errorf("a piece is both config and key frame: %+v", p)
				}
			}
			if !bytes.Equal(all, c.data) {
				t.Fatalf("the pieces hold %x, want exactly %x", all, c.data)
			}
			switch {
			case c.cut > 0 && c.key:
				if len(got) != 2 || len(got[0].Data) != c.cut || !got[0].Config || !got[1].Key {
					t.Fatalf("pieces %+v, want a config of %d bytes and a key frame", got, c.cut)
				}
			case c.cut > 0:
				if len(got) != 1 || !got[0].Config || got[0].Key {
					t.Fatalf("pieces %+v, want a config alone", got)
				}
			default:
				if len(got) != 1 || !got[0].Key || got[0].Config {
					t.Fatalf("pieces %+v, want a key frame alone", got)
				}
			}
		})
	}

	// Anything but the join form is returned as it is.
	for _, p := range []Packet{{Key: true, Data: []byte{1}}, {Config: true, Data: []byte{2}}, {Data: []byte{3}}} {
		if got := Split(p); len(got) != 1 || got[0].Key != p.Key || got[0].Config != p.Config || !bytes.Equal(got[0].Data, p.Data) {
			t.Errorf("Split(%+v) = %+v, want it unchanged", p, got)
		}
	}
}

// Decision: the config is a copy, so the Hub, which keeps it for the stream's life, does not
// keep the message's whole backing array (a key frame's megabytes) alive. Seen failing with the config a slice of the message: changing the
// message changed the config.
func TestSplitCopiesTheConfig(t *testing.T) {
	data := []byte{0, 0, 0, 1, 0x67, 1, 0, 0, 0, 1, 0x68, 2, 0, 0, 0, 1, 0x65, 3}
	got := Split(Packet{Key: true, Config: true, Data: data})
	if len(got) != 2 {
		t.Fatalf("pieces: %+v", got)
	}
	data[5] = 0xee
	if got[0].Data[5] != 1 {
		t.Fatalf("the config follows the message's bytes: %x", got[0].Data)
	}
}

func FuzzSplit(f *testing.F) {
	f.Add([]byte{0, 0, 0, 1, 0x67, 0x42, 0, 0, 1, 0x68, 1, 0, 0, 0, 1, 0x65, 2})
	f.Add([]byte{0, 0, 1, 0x67, 0, 0, 1})
	f.Add([]byte{0, 0, 0, 1})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		got := Split(Packet{Key: true, Config: true, PTS: 5, Data: data})
		if len(got) < 1 || len(got) > 2 {
			t.Fatalf("%d pieces", len(got))
		}
		var all []byte
		for _, p := range got {
			if p.Config && p.Key {
				t.Fatal("a piece both config and key")
			}
			all = append(all, p.Data...)
		}
		if !bytes.Equal(all, data) {
			t.Fatalf("pieces %x differ from the packet %x", all, data)
		}
	})
}
