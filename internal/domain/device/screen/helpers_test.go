package screen

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// fixture reads one of step 1's recordings of the video socket.
func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// refItem is one unit of a video socket as the reference parser below sees it: a
// session packet or a media packet with its payload. The reference parser is written
// from the format note, with different arithmetic from Demux, so that a test comparing
// the two checks Demux against the format and not against itself.
type refItem struct {
	session       bool
	width, height uint32
	config, key   bool
	pts           uint64
	payload       []byte
	end           int // offset just after this unit in the stream
}

func refParse(t testing.TB, data []byte) []refItem {
	t.Helper()
	if len(data) < 69 || string(data[65:69]) != "h264" {
		t.Fatalf("reference parser: bad preamble")
	}
	var items []refItem
	for off := 69; off < len(data); {
		h := data[off : off+12]
		if h[0]&0x80 != 0 {
			off += 12
			items = append(items, refItem{session: true,
				width:  binary.BigEndian.Uint32(h[4:]),
				height: binary.BigEndian.Uint32(h[8:]), end: off})
			continue
		}
		size := int(binary.BigEndian.Uint32(h[8:]))
		it := refItem{
			config: h[0]&0x40 != 0,
			key:    h[0]&0x20 != 0,
			pts:    binary.BigEndian.Uint64(h[:8]) & (1<<61 - 1),
		}
		it.payload = data[off+12 : off+12+size]
		off += 12 + size
		it.end = off
		items = append(items, it)
	}
	return items
}

// streamBuilder writes a small synthetic video socket.
type streamBuilder struct{ buf bytes.Buffer }

func newStream() *streamBuilder {
	s := &streamBuilder{}
	s.buf.WriteByte(0)
	name := make([]byte, 64)
	copy(name, "device")
	s.buf.Write(name)
	s.buf.WriteString("h264")
	return s
}

func (s *streamBuilder) session(w, h uint32) *streamBuilder {
	var b [12]byte
	b[0] = 0x80
	binary.BigEndian.PutUint32(b[4:], w)
	binary.BigEndian.PutUint32(b[8:], h)
	s.buf.Write(b[:])
	return s
}

// packet appends a media packet whose first word is flagBits|pts.
func (s *streamBuilder) packet(flagBits, pts uint64, payload []byte) *streamBuilder {
	var b [12]byte
	binary.BigEndian.PutUint64(b[:8], flagBits|pts)
	binary.BigEndian.PutUint32(b[8:], uint32(len(payload)))
	s.buf.Write(b[:])
	s.buf.Write(payload)
	return s
}

func (s *streamBuilder) bytes() []byte { return s.buf.Bytes() }

// collect demuxes data and returns what it yielded and the sessions it reported.
func collect(r *bytes.Reader) (pkts []Packet, sessions []Session, err error) {
	err = Demux(r, func(p Packet) error {
		pkts = append(pkts, p)
		return nil
	}, OnSession(func(s Session) { sessions = append(sessions, s) }))
	return
}
