package screen

import (
	"bytes"
	"errors"
	"fmt"
)

// ErrBadSPS: the bytes hold no sequence parameter set this package can read.
var ErrBadSPS = errors.New("screen: no readable SPS")

// SPS is what the live view needs from an H.264 sequence parameter set.
type SPS struct {
	// Width and Height are the displayed size: the coded size less the frame cropping.
	Width, Height int
	// Codec is the WebCodecs codec string, "avc1." and the profile_idc, the constraint
	// flags byte and the level_idc as three lower-case hex pairs.
	Codec string
}

var (
	startCode3 = []byte{0, 0, 1}
)

// ParseSPS reads the first sequence parameter set in data, which is either Annex B (a
// config packet: start codes, then NAL units) or one bare NAL unit, and returns its
// displayed size and codec string. Everything it reads is bounds-checked: any input
// that is not a complete, plausible SPS is an error wrapping ErrBadSPS, never a panic.
//
// The size is the macroblock grid less the cropping (H.264 §7.4.2.1.1), with the crop
// unit of the chroma format and of frame_mbs_only_flag; a size outside 1..MaxDimension
// is an error. The VUI is not read.
func ParseSPS(data []byte) (SPS, error) {
	nal := findSPS(data)
	if nal == nil {
		return SPS{}, fmt.Errorf("%w: no SPS NAL unit", ErrBadSPS)
	}
	sps, err := parseSPSNAL(nal)
	if err != nil {
		return SPS{}, fmt.Errorf("%w: %v", ErrBadSPS, err)
	}
	return sps, nil
}

// findSPS returns the first NAL unit of type 7 (forbidden bit clear), or nil.
func findSPS(data []byte) []byte {
	isSPS := func(n []byte) bool { return len(n) > 0 && n[0]&0x80 == 0 && n[0]&0x1f == 7 }
	if !bytes.HasPrefix(data, startCode3) && !bytes.HasPrefix(data, []byte{0, 0, 0, 1}) {
		if isSPS(data) { // one bare NAL unit
			return data
		}
		return nil
	}
	for i := 0; i < len(data); {
		at := bytes.Index(data[i:], startCode3)
		if at < 0 {
			return nil
		}
		start := i + at + len(startCode3)
		end := len(data)
		if next := bytes.Index(data[start:], startCode3); next >= 0 {
			end = start + next
		}
		nal := bytes.TrimRight(data[start:end], "\x00")
		if isSPS(nal) {
			return nal
		}
		i = end
	}
	return nil
}

func parseSPSNAL(nal []byte) (SPS, error) {
	if len(nal) < 4 {
		return SPS{}, errors.New("shorter than the profile and level bytes")
	}
	profile, constraints, level := nal[1], nal[2], nal[3]
	r := &bitReader{b: unescape(nal[4:])}

	if _, err := r.ue(); err != nil { // seq_parameter_set_id
		return SPS{}, err
	}
	chroma, separatePlanes := uint32(1), false
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		var err error
		if chroma, err = r.ue(); err != nil {
			return SPS{}, err
		}
		if chroma > 3 {
			return SPS{}, fmt.Errorf("chroma_format_idc %d", chroma)
		}
		if chroma == 3 {
			b, err := r.bit()
			if err != nil {
				return SPS{}, err
			}
			separatePlanes = b == 1
		}
		for range 2 { // bit_depth_luma_minus8, bit_depth_chroma_minus8
			d, err := r.ue()
			if err != nil {
				return SPS{}, err
			}
			if d > 6 {
				return SPS{}, fmt.Errorf("bit depth minus 8 is %d", d)
			}
		}
		if _, err := r.bit(); err != nil { // qpprime_y_zero_transform_bypass_flag
			return SPS{}, err
		}
		present, err := r.bit()
		if err != nil {
			return SPS{}, err
		}
		if present == 1 {
			lists := 8
			if chroma == 3 {
				lists = 12
			}
			for i := range lists {
				p, err := r.bit()
				if err != nil {
					return SPS{}, err
				}
				if p == 0 {
					continue
				}
				size := 16
				if i >= 6 {
					size = 64
				}
				if err := r.skipScalingList(size); err != nil {
					return SPS{}, err
				}
			}
		}
	}

	if _, err := r.ue(); err != nil { // log2_max_frame_num_minus4
		return SPS{}, err
	}
	pocType, err := r.ue()
	if err != nil {
		return SPS{}, err
	}
	switch pocType {
	case 0:
		if _, err := r.ue(); err != nil { // log2_max_pic_order_cnt_lsb_minus4
			return SPS{}, err
		}
	case 1:
		if _, err := r.bit(); err != nil { // delta_pic_order_always_zero_flag
			return SPS{}, err
		}
		if _, err := r.se(); err != nil { // offset_for_non_ref_pic
			return SPS{}, err
		}
		if _, err := r.se(); err != nil { // offset_for_top_to_bottom_field
			return SPS{}, err
		}
		cycle, err := r.ue()
		if err != nil {
			return SPS{}, err
		}
		if cycle > 255 {
			return SPS{}, fmt.Errorf("num_ref_frames_in_pic_order_cnt_cycle %d", cycle)
		}
		for range cycle {
			if _, err := r.se(); err != nil {
				return SPS{}, err
			}
		}
	case 2:
	default:
		return SPS{}, fmt.Errorf("pic_order_cnt_type %d", pocType)
	}
	if _, err := r.ue(); err != nil { // max_num_ref_frames
		return SPS{}, err
	}
	if _, err := r.bit(); err != nil { // gaps_in_frame_num_value_allowed_flag
		return SPS{}, err
	}
	widthMbs, err := r.ue() // pic_width_in_mbs_minus1
	if err != nil {
		return SPS{}, err
	}
	heightUnits, err := r.ue() // pic_height_in_map_units_minus1
	if err != nil {
		return SPS{}, err
	}
	frameMbsOnly, err := r.bit()
	if err != nil {
		return SPS{}, err
	}
	if frameMbsOnly == 0 {
		if _, err := r.bit(); err != nil { // mb_adaptive_frame_field_flag
			return SPS{}, err
		}
	}
	if _, err := r.bit(); err != nil { // direct_8x8_inference_flag
		return SPS{}, err
	}
	cropping, err := r.bit()
	if err != nil {
		return SPS{}, err
	}
	var left, right, top, bottom uint32
	if cropping == 1 {
		for _, p := range []*uint32{&left, &right, &top, &bottom} {
			if *p, err = r.ue(); err != nil {
				return SPS{}, err
			}
		}
	}

	// The coded size, in uint64 so no product of two 32-bit values overflows.
	fullW := (uint64(widthMbs) + 1) * 16
	fullH := uint64(2-frameMbsOnly) * (uint64(heightUnits) + 1) * 16

	// CropUnitX and CropUnitY (H.264 eq. 7-19 to 7-22). ChromaArrayType 0 (monochrome, or
	// separate colour planes) crops in luma samples; otherwise by the chroma subsampling.
	cropX, cropY := uint64(1), uint64(2-frameMbsOnly)
	if !separatePlanes {
		switch chroma {
		case 1: // 4:2:0
			cropX, cropY = 2, 2*uint64(2-frameMbsOnly)
		case 2: // 4:2:2
			cropX = 2
		}
	}
	cx := (uint64(left) + uint64(right)) * cropX
	cy := (uint64(top) + uint64(bottom)) * cropY
	if cx >= fullW || cy >= fullH {
		return SPS{}, fmt.Errorf("cropping %dx%d leaves nothing of %dx%d", cx, cy, fullW, fullH)
	}
	w, h := fullW-cx, fullH-cy
	if w > MaxDimension || h > MaxDimension {
		return SPS{}, fmt.Errorf("frame size %dx%d", w, h)
	}
	return SPS{
		Width:  int(w),
		Height: int(h),
		Codec:  fmt.Sprintf("avc1.%02x%02x%02x", profile, constraints, level),
	}, nil
}

// unescape removes the emulation prevention bytes (00 00 03 -> 00 00) of a NAL unit.
func unescape(b []byte) []byte {
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
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

// bitReader reads an RBSP most significant bit first. Every read is bounds-checked.
type bitReader struct {
	b   []byte
	pos int // in bits
}

var errShort = errors.New("ran out of bits")

func (r *bitReader) bit() (uint32, error) {
	if r.pos >= len(r.b)*8 {
		return 0, errShort
	}
	v := uint32(r.b[r.pos>>3]>>(7-uint(r.pos&7))) & 1
	r.pos++
	return v, nil
}

// ue reads an unsigned Exp-Golomb code. More than 31 leading zeros would not fit the
// 32-bit range the syntax allows, so it is an error.
func (r *bitReader) ue() (uint32, error) {
	zeros := 0
	for {
		b, err := r.bit()
		if err != nil {
			return 0, err
		}
		if b == 1 {
			break
		}
		if zeros++; zeros > 31 {
			return 0, errors.New("Exp-Golomb code longer than 32 bits")
		}
	}
	var rest uint64
	for range zeros {
		b, err := r.bit()
		if err != nil {
			return 0, err
		}
		rest = rest<<1 | uint64(b)
	}
	return uint32(uint64(1)<<zeros - 1 + rest), nil
}

// se reads a signed Exp-Golomb code.
func (r *bitReader) se() (int64, error) {
	k, err := r.ue()
	if err != nil {
		return 0, err
	}
	if k&1 == 1 {
		return int64(k+1) / 2, nil
	}
	return -int64(k / 2), nil
}

// skipScalingList reads past one scaling_list() of the given size (H.264 §7.3.2.1.1.1).
func (r *bitReader) skipScalingList(size int) error {
	last, next := int64(8), int64(8)
	for range size {
		if next != 0 {
			delta, err := r.se()
			if err != nil {
				return err
			}
			next = (last + delta + 256) % 256
			if next < 0 {
				next += 256
			}
		}
		if next != 0 {
			last = next
		}
	}
	return nil
}
