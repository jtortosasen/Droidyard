package screen

import (
	"bytes"
	"errors"
	"regexp"
	"testing"
)

// bitWriter builds an SPS for the tests, bit by bit, from the syntax in H.264 §7.3.2.1.1.
type bitWriter struct {
	b    []byte
	nbit int
}

func (w *bitWriter) bit(v int) {
	if w.nbit%8 == 0 {
		w.b = append(w.b, 0)
	}
	if v != 0 {
		w.b[len(w.b)-1] |= 0x80 >> (w.nbit % 8)
	}
	w.nbit++
}

func (w *bitWriter) ue(v uint32) {
	x := uint64(v) + 1
	n := 0
	for ; x>>n > 1; n++ {
	}
	for i := 0; i < n; i++ {
		w.bit(0)
	}
	for i := n; i >= 0; i-- {
		w.bit(int(x>>i) & 1)
	}
}

func (w *bitWriter) se(v int) {
	if v > 0 {
		w.ue(uint32(2*v - 1))
	} else {
		w.ue(uint32(-2 * v))
	}
}

// spsSpec is the part of an SPS the parser reads.
type spsSpec struct {
	profile, constraints, level byte
	chroma                      uint32 // high profiles only
	separatePlanes              bool
	scalingLists                map[int][]int // list index -> delta_scale values
	pocType                     uint32
	pocCycle                    []int
	widthMbs, heightUnits       uint32 // the _minus1 values
	interlaced                  bool   // frame_mbs_only_flag = 0
	crop                        *[4]uint32
	vui                         bool
}

var highProfile = map[byte]bool{100: true, 110: true, 122: true, 244: true, 44: true, 83: true, 86: true,
	118: true, 128: true, 138: true, 139: true, 134: true, 135: true}

// nal returns the SPS as a NAL unit (header byte included), with emulation prevention.
func (s spsSpec) nal() []byte {
	w := &bitWriter{}
	w.ue(0) // seq_parameter_set_id
	if highProfile[s.profile] {
		w.ue(s.chroma)
		if s.chroma == 3 {
			w.bit(b2i(s.separatePlanes))
		}
		w.ue(0)  // bit_depth_luma_minus8
		w.ue(0)  // bit_depth_chroma_minus8
		w.bit(0) // qpprime_y_zero_transform_bypass_flag
		w.bit(b2i(s.scalingLists != nil))
		if s.scalingLists != nil {
			n := 8
			if s.chroma == 3 {
				n = 12
			}
			for i := 0; i < n; i++ {
				deltas, ok := s.scalingLists[i]
				w.bit(b2i(ok))
				if ok {
					for _, d := range deltas {
						w.se(d)
					}
				}
			}
		}
	}
	w.ue(0) // log2_max_frame_num_minus4
	w.ue(s.pocType)
	switch s.pocType {
	case 0:
		w.ue(2)
	case 1:
		w.bit(0)
		w.se(-3)
		w.se(4)
		w.ue(uint32(len(s.pocCycle)))
		for _, o := range s.pocCycle {
			w.se(o)
		}
	}
	w.ue(4)  // max_num_ref_frames
	w.bit(0) // gaps_in_frame_num_value_allowed_flag
	w.ue(s.widthMbs)
	w.ue(s.heightUnits)
	w.bit(b2i(!s.interlaced))
	if s.interlaced {
		w.bit(1) // mb_adaptive_frame_field_flag
	}
	w.bit(1) // direct_8x8_inference_flag
	w.bit(b2i(s.crop != nil))
	if s.crop != nil {
		for _, c := range s.crop {
			w.ue(c)
		}
	}
	w.bit(b2i(s.vui))
	if s.vui {
		w.bit(0) // aspect_ratio_info_present_flag; the rest is not read
	}
	w.bit(1) // rbsp_stop_one_bit
	rbsp := append([]byte{s.profile, s.constraints, s.level}, w.b...)
	return append([]byte{0x67}, escape(rbsp)...)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// escape inserts the emulation prevention bytes.
func escape(b []byte) []byte {
	var out []byte
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

func annexB(nals ...[]byte) []byte {
	var out []byte
	for i, n := range nals {
		if i%2 == 0 {
			out = append(out, 0, 0, 0, 1)
		} else {
			out = append(out, 0, 0, 1)
		}
		out = append(out, n...)
	}
	return out
}

// Decision: the displayed size is the macroblock grid less the cropping, with the crop
// unit of the chroma format and of frame_mbs_only_flag, and the codec string is the three
// bytes after the NAL header in hex. Each case's expected size is computed by hand from
// H.264 §7.4.2.1.1, not by the code under test.
func TestParseSPSSize(t *testing.T) {
	cases := []struct {
		name string
		spec spsSpec
		w, h int
		code string
	}{
		{"baseline 1280x720, no crop", spsSpec{profile: 66, constraints: 0xc0, level: 0x29, widthMbs: 79, heightUnits: 44}, 1280, 720, "avc1.42c029"},
		{"1080p: 1088 rows less 4 units of 2", spsSpec{profile: 100, constraints: 0, level: 0x28, chroma: 1, widthMbs: 119, heightUnits: 67, crop: &[4]uint32{0, 0, 0, 4}}, 1920, 1080, "avc1.640028"},
		{"4:2:0 crops left and right in 2 pixels", spsSpec{profile: 100, level: 30, chroma: 1, widthMbs: 36, heightUnits: 79, crop: &[4]uint32{1, 0, 0, 0}}, 590, 1280, "avc1.64001e"},
		{"interlaced: unit Y is 4, rows are 2 per map unit", spsSpec{profile: 77, level: 40, widthMbs: 119, heightUnits: 33, interlaced: true, crop: &[4]uint32{0, 0, 0, 2}}, 1920, 1080, "avc1.4d0028"},
		{"4:4:4: units of 1", spsSpec{profile: 244, level: 40, chroma: 3, widthMbs: 3, heightUnits: 3, crop: &[4]uint32{0, 3, 0, 1}}, 61, 63, "avc1.f40028"},
		{"4:4:4 separate planes: units of 1 (interlaced: 2)", spsSpec{profile: 244, level: 40, chroma: 3, separatePlanes: true, widthMbs: 3, heightUnits: 1, interlaced: true, crop: &[4]uint32{2, 0, 0, 1}}, 62, 62, "avc1.f40028"},
		{"4:2:2: X by 2, Y by 1", spsSpec{profile: 122, level: 40, chroma: 2, widthMbs: 3, heightUnits: 3, crop: &[4]uint32{1, 1, 3, 0}}, 60, 61, "avc1.7a0028"},
		{"monochrome: units of 1", spsSpec{profile: 100, level: 40, chroma: 0, widthMbs: 3, heightUnits: 3, crop: &[4]uint32{3, 0, 0, 2}}, 61, 62, "avc1.640028"},
		{"scaling lists, POC type 1, VUI after the cropping", spsSpec{profile: 100, level: 41, chroma: 1,
			scalingLists: map[int][]int{0: make([]int, 16), 3: append([]int{5, -7}, make([]int, 14)...), 6: make([]int, 64), 7: {-8}},
			pocType:      1, pocCycle: []int{2, -2, 9}, widthMbs: 44, heightUnits: 44, vui: true}, 720, 720, "avc1.640029"},
		{"POC type 2", spsSpec{profile: 66, level: 20, pocType: 2, widthMbs: 19, heightUnits: 14}, 320, 240, "avc1.420014"},
	}
	for _, c := range cases {
		nal := c.spec.nal()
		for _, form := range []struct {
			name string
			data []byte
		}{{"bare NAL", nal}, {"Annex B", annexB(nal, []byte{0x68, 0xce, 0x3c, 0x80})}} {
			got, err := ParseSPS(form.data)
			if err != nil {
				t.Fatalf("%s (%s): %v", c.name, form.name, err)
			}
			if got.Width != c.w || got.Height != c.h || got.Codec != c.code {
				t.Errorf("%s (%s): %+v, want %dx%d %s", c.name, form.name, got, c.w, c.h, c.code)
			}
		}
	}
}

// Decision: a NAL unit with emulation prevention bytes is read as its RBSP. The case is
// searched for, not assumed: a scaling-list delta whose Exp-Golomb code holds a run of
// 24 zero bits makes the writer emit 00 00 03.
func TestParseSPSEmulationPrevention(t *testing.T) {
	// Which list holds the delta shifts the bit alignment of its zero run.
	for list := 0; list < 6; list++ {
		spec := spsSpec{profile: 100, level: 40, chroma: 1, widthMbs: 7, heightUnits: 7,
			scalingLists: map[int][]int{list: append([]int{1 << 23}, make([]int, 15)...)}}
		nal := spec.nal()
		if !bytes.Contains(nal, []byte{0, 0, 3}) {
			continue
		}
		got, err := ParseSPS(nal)
		if err != nil || got.Width != 128 || got.Height != 128 {
			t.Fatalf("list %d: %+v, %v", list, got, err)
		}
		return
	}
	t.Fatal("no case produced an emulation prevention byte")
}

func TestUnescape(t *testing.T) {
	for in, want := range map[string]string{
		"\x00\x00\x03\x01":         "\x00\x00\x01",
		"\x00\x00\x03\x00\x00\x03": "\x00\x00\x00\x00",
		"\x00\x03\x00":             "\x00\x03\x00",
		"\x00\x00\x03":             "\x00\x00",
		"\x03\x03":                 "\x03\x03",
	} {
		if got := unescape([]byte(in)); string(got) != want {
			t.Errorf("unescape(% x) = % x, want % x", in, got, want)
		}
	}
}

// Decision: ParseSPS finds the SPS among the NAL units of a config packet, and refuses
// everything that is not one, without panicking.
func TestParseSPSRefuses(t *testing.T) {
	ok := spsSpec{profile: 66, level: 40, widthMbs: 3, heightUnits: 3}.nal()
	pps := []byte{0x68, 0xce, 0x3c, 0x80}
	aud := []byte{0x09, 0xf0}

	if _, err := ParseSPS(annexB(aud, pps, ok)); err != nil {
		t.Fatalf("SPS after other NAL units: %v", err)
	}
	for name, data := range map[string][]byte{
		"empty":             nil,
		"a PPS only":        annexB(pps),
		"start code only":   {0, 0, 0, 1},
		"zeros":             make([]byte, 40),
		"forbidden bit set": annexB(append([]byte{0xe7}, ok[1:]...)),
		"a key frame":       annexB([]byte{0x65, 0x88, 0x84}),
		"only the header":   {0x67},
		"profile and level": {0x67, 66, 0, 40},
	} {
		if _, err := ParseSPS(data); !errors.Is(err, ErrBadSPS) {
			t.Errorf("%s: %v, want ErrBadSPS", name, err)
		}
	}

	bad := map[string]spsSpec{
		"chroma_format_idc 4":       {profile: 100, chroma: 4, widthMbs: 3, heightUnits: 3},
		"POC type 3":                {profile: 66, pocType: 3, widthMbs: 3, heightUnits: 3},
		"POC cycle of 256":          {profile: 66, pocType: 1, pocCycle: make([]int, 256), widthMbs: 3, heightUnits: 3},
		"crop takes the width":      {profile: 66, widthMbs: 3, heightUnits: 3, crop: &[4]uint32{16, 16, 0, 0}},
		"crop takes the height":     {profile: 66, widthMbs: 3, heightUnits: 3, crop: &[4]uint32{0, 0, 16, 16}},
		"crop beyond 32 bits":       {profile: 66, widthMbs: 3, heightUnits: 3, crop: &[4]uint32{0xfffffffe, 0xfffffffe, 0, 0}},
		"wider than a u16":          {profile: 66, widthMbs: 4096, heightUnits: 3},
		"taller than a u16, fields": {profile: 66, widthMbs: 3, heightUnits: 2047, interlaced: true},
		"width_in_mbs of 2^32-2":    {profile: 66, widthMbs: 0xfffffffe, heightUnits: 3},
	}
	for name, spec := range bad {
		if _, err := ParseSPS(spec.nal()); !errors.Is(err, ErrBadSPS) {
			t.Errorf("%s: %v, want ErrBadSPS", name, err)
		}
	}

	// An Exp-Golomb code of 32 zeros.
	long := []byte{0x67, 66, 0, 40, 0, 0, 0, 0, 0x80}
	if _, err := ParseSPS(long); !errors.Is(err, ErrBadSPS) {
		t.Errorf("a 33-bit Exp-Golomb code: %v", err)
	}
}

// Decision: every prefix of an SPS is bounds-checked: it parses or it errors, and a
// prefix that stops before the cropping flag never parses.
func TestParseSPSTruncated(t *testing.T) {
	for _, spec := range []spsSpec{
		{profile: 66, level: 40, widthMbs: 79, heightUnits: 44, crop: &[4]uint32{0, 0, 0, 4}},
		{profile: 100, level: 40, chroma: 3, widthMbs: 7, heightUnits: 7, pocType: 1, pocCycle: []int{1, 2},
			scalingLists: map[int][]int{0: make([]int, 16), 6: make([]int, 64)}, crop: &[4]uint32{1, 2, 3, 4}},
	} {
		full := spec.nal()
		if _, err := ParseSPS(full); err != nil {
			t.Fatal(err)
		}
		for n := 0; n < len(full)-1; n++ {
			_, err := ParseSPS(full[:n]) // must not panic
			if n < 5 && err == nil {
				t.Errorf("a %d byte prefix parsed", n)
			}
		}
		// The last byte holds the stop bit, so the cropping flag is in the byte before.
		// Cutting two bytes off always loses at least one field, with a crop present.
		if _, err := ParseSPS(full[:len(full)-2]); err == nil && spec.crop != nil {
			t.Errorf("a prefix without the cropping fields parsed")
		}
	}
}

var codecString = regexp.MustCompile(`^avc1\.[0-9a-f]{6}$`)

func FuzzParseSPS(f *testing.F) {
	f.Add(spsSpec{profile: 66, constraints: 0xc0, level: 0x29, widthMbs: 36, heightUnits: 79, crop: &[4]uint32{1, 0, 0, 0}}.nal())
	f.Add(annexB(spsSpec{profile: 100, level: 40, chroma: 3, widthMbs: 7, heightUnits: 7, pocType: 1, pocCycle: []int{1, 2},
		scalingLists: map[int][]int{0: make([]int, 16), 6: make([]int, 64)}, crop: &[4]uint32{1, 2, 3, 4}, vui: true}.nal(),
		[]byte{0x68, 0xce}))
	f.Add([]byte{0, 0, 0, 1, 0x67})
	for _, name := range []string{"scrcpy-api31-portrait.bin", "scrcpy-api31-rotate.bin"} {
		for _, it := range refParse(f, fixture(f, name)) {
			if it.config {
				f.Add(it.payload)
			}
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		sps, err := ParseSPS(data)
		if err != nil {
			if !errors.Is(err, ErrBadSPS) {
				t.Fatalf("error %v does not wrap ErrBadSPS", err)
			}
			return
		}
		if sps.Width < 1 || sps.Height < 1 || sps.Width > MaxDimension || sps.Height > MaxDimension {
			t.Fatalf("size %dx%d out of range", sps.Width, sps.Height)
		}
		if !codecString.MatchString(sps.Codec) {
			t.Fatalf("codec %q", sps.Codec)
		}
	})
}
