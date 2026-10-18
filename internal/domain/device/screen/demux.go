package screen

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Errors of Demux. A cut stream is io.ErrUnexpectedEOF, not one of these.
var (
	// ErrPacketTooLarge: a packet header claims more than MaxPacket bytes.
	ErrPacketTooLarge = errors.New("screen: packet larger than the cap")
	// ErrBadStream: the bytes are not the video socket's format.
	ErrBadStream = errors.New("screen: malformed video stream")
)

const (
	preambleLen = 1 + 64 + 4 // dummy byte, device name, codec id
	headerLen   = 12

	sessionBit = 1 << 63 // the first word's bit 31
	configBit  = 1 << 62
	keyBit     = 1 << 61
	ptsMask    = keyBit - 1
)

// Session is a session packet: the frame size the encoder starts with. The server sends
// one before each encoder start's config, at start and at a rotation.
type Session struct{ Width, Height int }

// DemuxOption customises Demux.
type DemuxOption func(*demuxConfig)

type demuxConfig struct{ onSession func(Session) }

// OnSession has Demux call fn for each session packet, in stream order, before the
// packets that follow it. A session packet is never yielded as a Packet.
func OnSession(fn func(Session)) DemuxOption {
	return func(c *demuxConfig) { c.onSession = fn }
}

// Demux reads the video socket of scrcpy's server (docs/research/spike-screen-stream-2026-10.md
// §(a)) and yields each media packet with the bytes the device sent, in order. A config
// packet is yielded on its own (Config true, Key false); merging it into a key frame is
// the Hub's job.
//
// The preamble is the dummy byte, the device name and the codec id, which must be h264.
// The name is device text and is dropped here. Nothing the stream says allocates more
// than MaxPacket bytes, and Data is allocated as the bytes arrive, not from the header's
// claim alone.
//
// Demux returns nil when r ends cleanly between two packets, io.ErrUnexpectedEOF when it
// ends anywhere else (the preamble included), ErrPacketTooLarge or an error wrapping
// ErrBadStream for bytes that are not the format, yield's error as is, and r's own read
// error as is. It never panics.
func Demux(r io.Reader, yield func(Packet) error, opts ...DemuxOption) error {
	var cfg demuxConfig
	for _, o := range opts {
		o(&cfg)
	}

	var pre [preambleLen]byte
	if _, err := io.ReadFull(r, pre[:]); err != nil {
		return cut(err)
	}
	if string(pre[65:]) != "h264" {
		return fmt.Errorf("%w: codec id % x, want h264", ErrBadStream, pre[65:])
	}

	var h [headerLen]byte
	for {
		if _, err := io.ReadFull(r, h[:]); err != nil {
			if err == io.EOF { // no byte of a header: the stream ended between packets
				return nil
			}
			return cut(err)
		}
		v := binary.BigEndian.Uint64(h[:8])
		if v&sessionBit != 0 {
			w, hh := binary.BigEndian.Uint32(h[4:8]), binary.BigEndian.Uint32(h[8:12])
			if w == 0 || hh == 0 || w > MaxDimension || hh > MaxDimension {
				return fmt.Errorf("%w: session packet size %dx%d", ErrBadStream, w, hh)
			}
			if cfg.onSession != nil {
				cfg.onSession(Session{Width: int(w), Height: int(hh)})
			}
			continue
		}
		if v&configBit != 0 && v&keyBit != 0 {
			return fmt.Errorf("%w: a packet flagged both config and key frame", ErrBadStream)
		}
		size := binary.BigEndian.Uint32(h[8:12])
		if size > MaxPacket {
			return fmt.Errorf("%w: %d bytes", ErrPacketTooLarge, size)
		}
		data, err := readN(r, int(size))
		if err != nil {
			return cut(err)
		}
		p := Packet{
			Config: v&configBit != 0,
			Key:    v&keyBit != 0,
			PTS:    int64(v & ptsMask),
			Data:   data,
		}
		if err := yield(p); err != nil {
			return err
		}
	}
}

// cut turns a bare EOF, which means the stream ended inside a unit, into
// io.ErrUnexpectedEOF and leaves every other error alone.
func cut(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// readN reads exactly n bytes into a slice of exactly n, growing it as bytes arrive so
// that a header claiming a large packet on a short stream costs only what arrived.
func readN(r io.Reader, n int) ([]byte, error) {
	const first = 64 << 10
	buf := make([]byte, 0, min(n, first))
	for len(buf) < n {
		if len(buf) == cap(buf) {
			grown := make([]byte, len(buf), min(n, 2*cap(buf)))
			copy(grown, buf)
			buf = grown
		}
		m, err := io.ReadFull(r, buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+m]
		if err != nil {
			return nil, err
		}
	}
	return buf, nil
}
