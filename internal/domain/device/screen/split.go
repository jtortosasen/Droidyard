package screen

import "bytes"

// Split undoes the Hub's join form (ADR-0100 §5): a packet flagged both Key and Config is
// the current config followed by a key frame, in one Data, and comes back as the config
// packet and the key frame, the bytes unchanged and in order. The config is the leading run
// of SPS and PPS NAL units; the key frame is what follows. Both pieces keep the packet's PTS.
//
// Any other packet is returned as it is. A packet flagged Key and Config whose Data does
// not start with a parameter set comes back as a key frame alone, and one that is nothing
// but parameter sets as a config alone: the flags then say what the bytes are, and no
// byte is dropped or invented. If the device repeats its SPS and PPS in front of a key
// frame, the repeat joins the config; the decoder reads the same parameter sets either way.
//
// The key frame shares Data's memory; the config is copied out, because a Hub keeps the
// config for as long as the stream runs and would otherwise keep the whole message's
// backing array (a key frame is megabytes, a config tens of bytes) alive with it. Split
// never panics; a caller checking MaxPacket does so on each piece.
func Split(p Packet) []Packet {
	if !p.Key || !p.Config {
		return []Packet{p}
	}
	n := configLen(p.Data)
	switch {
	case n == 0:
		p.Config = false
		return []Packet{p}
	case n == len(p.Data):
		p.Key = false
		return []Packet{p}
	}
	return []Packet{
		{Config: true, PTS: p.PTS, Data: bytes.Clone(p.Data[:n])},
		{Key: true, PTS: p.PTS, Data: p.Data[n:len(p.Data):len(p.Data)]},
	}
}

// configLen is the length of the leading run of SPS (type 7) and PPS (type 8) NAL units of
// an Annex B buffer: 0 when it does not start with a start code and one of them. A NAL unit
// runs to the zero byte of the next four-byte start code, or to its three-byte one.
func configLen(data []byte) int {
	end := 0
	for end < len(data) {
		start := startCodeLen(data[end:])
		hdr := end + start
		if start == 0 || hdr >= len(data) || data[hdr]&0x80 != 0 {
			return end
		}
		if t := data[hdr] & 0x1f; t != 7 && t != 8 {
			return end
		}
		next := bytes.Index(data[hdr+1:], startCode3)
		if next < 0 {
			return len(data)
		}
		end = hdr + 1 + next
		if end > hdr+1 && data[end-1] == 0 {
			end-- // the next start code is the four-byte form: its zero byte starts the next unit
		}
	}
	return end
}

// startCodeLen is 3 or 4 when b starts with an Annex B start code, 0 when it does not.
func startCodeLen(b []byte) int {
	switch {
	case bytes.HasPrefix(b, startCode3):
		return 3
	case bytes.HasPrefix(b, []byte{0, 0, 0, 1}):
		return 4
	}
	return 0
}
