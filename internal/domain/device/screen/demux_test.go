package screen

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"testing"
)

// Decision: the video socket of step 1's recording is read to the packet and key frame
// counts the note recorded; the first packet is a config and then a key frame; and the
// SPS inside the config says the size the session packet said.
func TestDemuxRecordedStream(t *testing.T) {
	data := fixture(t, "scrcpy-api31-portrait.bin")
	pkts, sessions, err := collect(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var configs, keys, frames int
	for _, p := range pkts {
		switch {
		case p.Config:
			configs++
		case p.Key:
			keys++
			frames++
		default:
			frames++
		}
	}
	if configs != 1 || frames != 187 || keys != 4 || len(pkts) != 188 {
		t.Fatalf("configs %d frames %d keys %d packets %d, want 1 187 4 188", configs, frames, keys, len(pkts))
	}
	if !pkts[0].Config || pkts[0].Key {
		t.Fatalf("first packet %+v, want a config on its own", pkts[0])
	}
	if !pkts[1].Key || pkts[1].Config {
		t.Fatalf("second packet is not a plain key frame")
	}
	for i := 1; i < len(pkts); i++ {
		if pkts[i].PTS < pkts[i-1].PTS {
			t.Fatalf("PTS decreased at packet %d: %d after %d", i, pkts[i].PTS, pkts[i-1].PTS)
		}
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions %v, want one", sessions)
	}
	sps, err := ParseSPS(pkts[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	if sps.Width != sessions[0].Width || sps.Height != sessions[0].Height {
		t.Fatalf("SPS %dx%d, session packet %v", sps.Width, sps.Height, sessions[0])
	}
	if sps.Width != 590 || sps.Height != 1280 || sps.Codec != "avc1.42c029" {
		t.Fatalf("SPS %+v, want 590x1280 avc1.42c029", sps)
	}
}

// Decision: a rotation is a session packet, a config with the swapped size and then a
// key frame, in that order; Demux reports both session packets and yields neither as media.
func TestDemuxRotation(t *testing.T) {
	data := fixture(t, "scrcpy-api31-rotate.bin")
	var events []string
	var pkts []Packet
	var sessions []Session
	err := Demux(bytes.NewReader(data), func(p Packet) error {
		pkts = append(pkts, p)
		switch {
		case p.Config:
			events = append(events, "config")
		case p.Key:
			events = append(events, "key")
		}
		return nil
	}, OnSession(func(s Session) {
		sessions = append(sessions, s)
		events = append(events, "session")
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[0] != (Session{590, 1280}) || sessions[1] != (Session{1280, 590}) {
		t.Fatalf("sessions %v, want 590x1280 then 1280x590", sessions)
	}
	var configs []Packet
	var keys int
	for _, p := range pkts {
		if p.Config {
			configs = append(configs, p)
		}
		if p.Key {
			keys++
		}
	}
	if len(configs) != 2 || keys != 3 || len(pkts) != 2+73 {
		t.Fatalf("configs %d keys %d packets %d, want 2 3 75", len(configs), keys, len(pkts))
	}
	// Each session comes before its config, and a key frame follows the config.
	var shape []string
	for i, e := range events {
		if e == "session" || e == "config" || (e == "key" && (i == 0 || events[i-1] == "config")) {
			shape = append(shape, e)
		}
	}
	want := []string{"session", "config", "key", "session", "config", "key"}
	if len(shape) != len(want) {
		t.Fatalf("event shape %v, want %v", shape, want)
	}
	for i := range want {
		if shape[i] != want[i] {
			t.Fatalf("event shape %v, want %v", shape, want)
		}
	}
	for i, c := range configs {
		sps, err := ParseSPS(c.Data)
		if err != nil {
			t.Fatal(err)
		}
		if (Session{sps.Width, sps.Height}) != sessions[i] {
			t.Fatalf("config %d says %dx%d, its session packet %v", i, sps.Width, sps.Height, sessions[i])
		}
	}
}

// Decision: the packets are the device's bytes, not a re-chunking of them: each Data is
// the payload the stream carried, in order, and nothing else is yielded. The reference is
// the test's own parse of the file.
func TestPacketsAreTheDevicesBytes(t *testing.T) {
	for _, name := range []string{"scrcpy-api31-portrait.bin", "scrcpy-api31-rotate.bin"} {
		data := fixture(t, name)
		var want []refItem
		for _, it := range refParse(t, data) {
			if !it.session {
				want = append(want, it)
			}
		}
		pkts, _, err := collect(bytes.NewReader(data))
		if err != nil {
			t.Fatal(name, err)
		}
		if len(pkts) != len(want) {
			t.Fatalf("%s: %d packets, the stream has %d", name, len(pkts), len(want))
		}
		for i, p := range pkts {
			w := want[i]
			if !bytes.Equal(p.Data, w.payload) || len(p.Data) != len(w.payload) {
				t.Fatalf("%s: packet %d is %d bytes, the device sent %d (or different ones)", name, i, len(p.Data), len(w.payload))
			}
			if p.Config != w.config || p.Key != w.key || uint64(p.PTS) != w.pts {
				t.Fatalf("%s: packet %d flags/pts %+v, want config %v key %v pts %d", name, i, p, w.config, w.key, w.pts)
			}
		}
	}
}

// Decision: the 4 MiB cap is on the claim in the header, so an oversize packet is an
// error before any payload is read or allocated; a stream cut anywhere is
// io.ErrUnexpectedEOF, except between two packets, where it simply ended.
func TestDemuxBounds(t *testing.T) {
	yieldNone := func(Packet) error { t.Error("yielded a packet"); return nil }

	t.Run("oversize header", func(t *testing.T) {
		s := newStream().packet(keyBit, 0, nil)
		b := s.bytes()
		binary32(b[len(b)-4:], MaxPacket+1) // the header of the packet just written
		err := Demux(bytes.NewReader(b[:len(b)]), yieldNone)
		if !errors.Is(err, ErrPacketTooLarge) {
			t.Fatalf("err %v, want ErrPacketTooLarge", err)
		}
	})

	t.Run("exactly the cap", func(t *testing.T) {
		pkts, _, err := collect(bytes.NewReader(newStream().packet(keyBit, 7, make([]byte, MaxPacket)).bytes()))
		if err != nil || len(pkts) != 1 || len(pkts[0].Data) != MaxPacket {
			t.Fatalf("err %v packets %d", err, len(pkts))
		}
	})

	t.Run("a big claim on a short stream allocates what arrived", func(t *testing.T) {
		s := newStream().packet(keyBit, 0, make([]byte, 10))
		b := s.bytes()
		binary32(b[len(b)-10-4:], MaxPacket) // claims the cap, carries 10 bytes
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		err := Demux(bytes.NewReader(b), yieldNone)
		runtime.ReadMemStats(&after)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("err %v, want ErrUnexpectedEOF", err)
		}
		if got := after.TotalAlloc - before.TotalAlloc; got > 1<<20 {
			t.Fatalf("allocated %d bytes for a 10 byte stream", got)
		}
	})

	t.Run("cut at every offset", func(t *testing.T) {
		full := newStream().
			session(720, 1280).
			packet(configBit, 0, []byte{0, 0, 0, 1, 0x67, 1, 2, 3}).
			packet(keyBit, 5, []byte("KEY")).
			packet(0, 9, []byte("delta")).
			packet(0, 12, nil).
			bytes()
		boundary := map[int]bool{69: true}
		for _, it := range refParse(t, full) {
			boundary[it.end] = true
		}
		for n := 0; n <= len(full); n++ {
			err := Demux(bytes.NewReader(full[:n]), func(Packet) error { return nil })
			switch {
			case boundary[n] && err != nil:
				t.Fatalf("cut at %d, between two packets: %v, want nil", n, err)
			case !boundary[n] && !errors.Is(err, io.ErrUnexpectedEOF):
				t.Fatalf("cut at %d, inside a unit: %v, want io.ErrUnexpectedEOF", n, err)
			}
		}
	})

	t.Run("not the format", func(t *testing.T) {
		hevc := newStream().bytes()
		copy(hevc[65:], "h265")
		both := newStream().packet(configBit|keyBit, 0, []byte{1}).bytes()
		zero := newStream().session(0, 1280).bytes()
		huge := newStream().session(MaxDimension+1, 10).bytes()
		for name, b := range map[string][]byte{"codec": hevc, "config and key": both, "zero size": zero, "huge size": huge} {
			if err := Demux(bytes.NewReader(b), yieldNone); !errors.Is(err, ErrBadStream) {
				t.Errorf("%s: %v, want ErrBadStream", name, err)
			}
		}
	})

	t.Run("errors pass through", func(t *testing.T) {
		boom := errors.New("boom")
		b := newStream().packet(keyBit, 0, []byte("x")).bytes()
		if err := Demux(bytes.NewReader(b), func(Packet) error { return boom }); err != boom {
			t.Fatalf("yield's error: %v", err)
		}
		r := io.MultiReader(bytes.NewReader(b[:70]), errReader{boom})
		if err := Demux(r, yieldNone); err != boom {
			t.Fatalf("reader's error: %v", err)
		}
	})
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// binary32 writes v big endian into b[:4].
func binary32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

// Decision: a session packet is not media: it is never yielded, whatever its flags.
func TestSessionPacketIsNotMedia(t *testing.T) {
	b := newStream().session(10, 20).session(30, 40).packet(keyBit, 1, []byte("k")).bytes()
	pkts, sessions, err := collect(bytes.NewReader(b))
	if err != nil || len(pkts) != 1 || len(sessions) != 2 {
		t.Fatalf("err %v packets %d sessions %d", err, len(pkts), len(sessions))
	}
	// Without a listener the session packets are still skipped.
	var n int
	if err := Demux(bytes.NewReader(b), func(Packet) error { n++; return nil }); err != nil || n != 1 {
		t.Fatalf("err %v packets %d", err, n)
	}
}

func FuzzDemux(f *testing.F) {
	f.Add(newStream().bytes())
	f.Add(newStream().
		session(590, 1280).
		packet(configBit, 0, []byte{0, 0, 0, 1, 0x67, 0x42, 0xc0, 0x29}).
		packet(keyBit, 100, []byte{0, 0, 0, 1, 0x65, 1}).
		packet(0, 200, []byte{0, 0, 0, 1, 0x41, 2}).
		bytes())
	for _, name := range []string{"scrcpy-api31-portrait.bin", "scrcpy-api31-rotate.bin"} {
		data := fixture(f, name)
		// The preamble, the opening packets and the first key frame, as the fixtures
		// record them, are the realistic seeds; the rest is the fuzzer's.
		items := refParse(f, data)
		for _, n := range []int{2, 3, 4} {
			if n < len(items) {
				f.Add(data[:items[n-1].end])
			}
		}
		f.Add(data[:69+12+33])
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var yielded int
		err := Demux(bytes.NewReader(data), func(p Packet) error {
			yielded += len(p.Data)
			if len(p.Data) > MaxPacket {
				t.Fatalf("a packet of %d bytes passed the cap", len(p.Data))
			}
			if p.Config && p.Key {
				t.Fatal("a packet both config and key")
			}
			if p.PTS < 0 {
				t.Fatal("negative PTS")
			}
			return nil
		}, OnSession(func(s Session) {
			if s.Width < 1 || s.Height < 1 || s.Width > MaxDimension || s.Height > MaxDimension {
				t.Fatalf("session %v out of range", s)
			}
		}))
		// What came out cannot be more than what went in.
		if yielded > len(data) {
			t.Fatalf("yielded %d bytes from %d", yielded, len(data))
		}
		_ = err
	})
}
