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
//   - Split undoes the Hub's join form, a key frame carrying its config, for a Hub that
//     receives a stream another Hub already merged (a relayed remote device);
//   - Input and EncodeControl turn a tap, a swipe or a key into scrcpy's control
//     messages against the stream's frame size.
//
// Source is the seam the daemon uses: a remote device's relayed stream implements it too
// (ADR-0100), and Split gives its client-side Hub the config and key frame back apart.
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

// PartialError wraps an Inject failure that came after the input's first byte was written
// to the device: the device may have received part of the gesture (ADR-0079 §3). Err is the
// cause and is reachable with errors.Is and errors.As.
type PartialError struct{ Err error }

func (e *PartialError) Error() string { return "screen: input cut short: " + e.Err.Error() }
func (e *PartialError) Unwrap() error { return e.Err }

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
	// Inject sends one input to the device against Size. An error from before the first
	// byte reached the device is returned as it is; one from after it is wrapped in a
	// *PartialError, so the caller can tell a refused input from a cut one.
	Inject(ctx context.Context, in Input) error
	// Done is closed when the stream ended; Reason then says why.
	Done() <-chan struct{}
	Reason() error
	// WatchSecure registers fn to hear whether the focused window is secure (FLAG_SECURE,
	// ADR-0080): at once when the source already knows, then on each change. It is the
	// seam's, so a relayed source feeds it from its host (ADR-0100 §5). The returned stop
	// removes fn and, when it returns, no call of fn is running or will start.
	WatchSecure(fn func(secure bool)) (stop func())
}
