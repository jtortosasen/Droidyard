// Package screen is the device's live view as the daemon sees it (plan m2p, ADR-0077,
// ADR-0078): H.264 packets from a source, fanned out to viewers, and the neutral input
// that goes back.
//
// Nothing here touches adb or a device. The package holds the pieces that parse or move
// bytes a device sent, so none of them may panic or exceed a cap on any input:
//
//   - Demux reads the video socket of scrcpy's server (the wire is in
//     docs/research/spike-screen-stream-2026-10.md §(a)) into Packets, each the device's
//     own bytes;
//   - ParseSPS reads the frame size and codec string out of a config packet;
//   - Hub keeps the current config apart from the current GOP and gives every viewer a
//     stream that starts at a key frame with that config;
//   - Input and EncodeControl turn a tap, a swipe or a key into scrcpy's control
//     messages against the stream's frame size.
//
// Source is the seam the daemon uses: a relayed remote device would implement it too.
package screen

import (
	"context"
	"errors"
)

// Limits. Every one is a bound on bytes that a device or a client controls.
const (
	// MaxPacket is the largest packet Demux accepts; a header claiming more is an error
	// and nothing is allocated for it.
	MaxPacket = 4 << 20
	// MaxGOP is the most device bytes the Hub keeps for a join (the config is not counted).
	// Past it the GOP is emptied until the next key frame.
	MaxGOP = 16 << 20
	// MaxQueue is the most bytes one viewer's queue holds: the GOP bound plus one packet
	// cap (the config is a packet, so it fits in that cap). A join never overflows it.
	MaxQueue = MaxGOP + MaxPacket
	// MaxPackets bounds how many packets the Hub's GOP and a viewer's queue hold, whatever
	// their size: the byte bounds alone would let a stream of tiny packets cost tens of
	// bytes of bookkeeping each. A real GOP is a few hundred packets. Past it the GOP is
	// emptied, and a queue overflows, as for the byte bounds.
	MaxPackets = 1 << 14
	// MaxDimension is the largest frame side the package accepts: scrcpy's control
	// messages carry the frame size as u16.
	MaxDimension = 1<<16 - 1
)

// Packet is one unit of the device's stream. Data is the device's Annex B bytes and is
// shared read-only by every holder: nobody modifies it.
//
// From Demux a packet is a config (SPS/PPS, Config), a key frame (Key) or a delta frame.
// From a Hub's viewer, a Config packet is always also a Key packet: the current config
// followed by the key frame it belongs to, in one Data.
type Packet struct {
	Key, Config bool
	PTS         int64 // microseconds, never negative
	Data        []byte
}

// Size is a frame size in pixels.
type Size struct{ Width, Height int }

// The errors a viewer, the Hub and the source report. A Source's own failure is a
// different error, passed to Hub.Close.
var (
	// ErrClosed ends a hub that was closed with no reason, and a viewer's Next after the
	// hub closed without one.
	ErrClosed = errors.New("screen: closed")
	// ErrViewerClosed is what Next returns after the viewer's own Close.
	ErrViewerClosed = errors.New("screen: viewer closed")
)

// Viewer is one reader of a stream. Next blocks until a packet, the end of the stream,
// Close or ctx. It returns the stream's end reason (Source.Reason) once the stream ended,
// ErrViewerClosed after Close, and ctx.Err() when ctx ends first.
type Viewer interface {
	Next(ctx context.Context) (Packet, error)
	Close()
}

// Source is what the daemon sees of one device's screen stream.
type Source interface {
	// Join returns a viewer whose first packet is a key frame carrying the current config.
	Join() Viewer
	// Size is the newest frame size the stream announced; the zero Size before the first.
	Size() Size
	// Inject sends one input to the device against Size.
	Inject(ctx context.Context, in Input) error
	// Done is closed when the stream ended; Reason then says why.
	Done() <-chan struct{}
	Reason() error
}
