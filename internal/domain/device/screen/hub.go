package screen

import (
	"context"
	"sync"
)

// Hub fans one producer's packets out to any number of viewers (plan m2p Q3, ADR-0078
// §5). It exists because a decoder needs the SPS/PPS and a key frame before it can show
// anything, and the device sends the config only when its encoder starts: a viewer that
// joins later must be given it.
//
// The Hub therefore keeps two things apart: the current config (the last config packet)
// and the GOP (the latest key frame and the packets after it, at most MaxGOP bytes of
// device data and MaxPackets packets). It is the only place a config is ever merged into
// a key frame:
//
//   - live, a config packet is held back and merged into the key frame that follows it;
//   - a viewer's first packet is the GOP's key frame with the current config in front of
//     it, once; with no GOP (nothing yet, a config just arrived, or the bound emptied
//     it) the viewer waits for the next key frame and gets it with the config;
//   - a viewer whose queue would pass MaxQueue bytes or MaxPackets packets is emptied
//     and rejoined at the next key frame, never at the current GOP, so a stalled viewer
//     cannot loop; it counts as one rejoin.
//
// A merged packet has Key and Config set and Data = config followed by the key frame.
// Every other packet is the device's, unchanged. The Hub is safe for concurrent use.
type Hub struct {
	mu       sync.Mutex
	config   []byte   // the last config packet's Data
	pending  bool     // a config arrived and no key frame since
	gop      []Packet // gop[0] is the key frame as the device sent it; nil when empty
	gopBytes int
	size     Size
	viewers  map[*hubViewer]struct{}
	rejoins  int

	closed bool
	reason error
	done   chan struct{}
}

// NewHub returns an empty Hub.
func NewHub() *Hub {
	return &Hub{viewers: make(map[*hubViewer]struct{}), done: make(chan struct{})}
}

// Push gives the Hub the next packet from the device. Only one goroutine pushes. The Hub
// keeps p.Data, so the caller must not reuse it. A push after Close is ignored.
func (h *Hub) Push(p Packet) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}

	if p.Config {
		h.config = p.Data
		if sps, err := ParseSPS(p.Data); err == nil {
			h.size = Size{sps.Width, sps.Height}
		}
		h.pending = true
		// The encoder restarted: what the GOP holds belongs to the old stream.
		h.gop, h.gopBytes = nil, 0
		return // never on its own
	}

	if p.Key {
		h.gop, h.gopBytes = []Packet{p}, len(p.Data)
	} else if len(h.gop) > 0 {
		if h.gopBytes+len(p.Data) > MaxGOP || len(h.gop) >= MaxPackets {
			h.gop, h.gopBytes = nil, 0 // until the next key frame
		} else {
			h.gop = append(h.gop, p)
			h.gopBytes += len(p.Data)
		}
	}

	var merged *Packet
	merge := func() Packet {
		if merged == nil {
			m := h.withConfig(p)
			merged = &m
		}
		return *merged
	}
	for v := range h.viewers {
		if !v.waiting {
			out := p
			if p.Key && h.pending {
				out = merge()
			}
			if v.bytes+len(out.Data) <= MaxQueue && len(v.queue)-v.head < MaxPackets {
				v.push(out)
				continue
			}
			v.reset()
			h.rejoins++
		}
		// v waits for a key frame: this one, if it is one.
		if p.Key {
			v.push(merge())
			v.waiting = false
		}
	}
	if p.Key {
		h.pending = false
	}
}

// withConfig returns the key frame k with the current config in front of it, flagged
// Config as well as Key. With no config known it is k as the device sent it.
func (h *Hub) withConfig(k Packet) Packet {
	if len(h.config) == 0 {
		return k
	}
	data := make([]byte, 0, len(h.config)+len(k.Data))
	data = append(append(data, h.config...), k.Data...)
	return Packet{Key: true, Config: true, PTS: k.PTS, Data: data}
}

// Join returns a viewer of the stream from now (see Hub). On a closed Hub the viewer's
// first Next returns the reason.
func (h *Hub) Join() Viewer {
	v := &hubViewer{h: h, wake: make(chan struct{}, 1)}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return v
	}
	h.viewers[v] = struct{}{}
	if len(h.gop) == 0 {
		v.waiting = true
		return v
	}
	// GOP <= MaxGOP and the config is one packet, <= MaxPacket: this fits MaxQueue.
	first := h.withConfig(h.gop[0])
	v.queue = make([]Packet, 0, len(h.gop))
	v.push(first)
	for _, p := range h.gop[1:] {
		v.push(p)
	}
	return v
}

// Size is the frame size of the newest SPS the Hub has seen; zero before the first.
func (h *Hub) Size() Size {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.size
}

// Rejoins is how many times a viewer's queue overflowed and the viewer was emptied to
// wait for the next key frame, over all viewers.
func (h *Hub) Rejoins() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rejoins
}

// Close ends the stream: every viewer's Next returns reason (ErrClosed for nil), and
// the Hub drops its packets. Only the first Close counts.
func (h *Hub) Close(reason error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	if reason == nil {
		reason = ErrClosed
	}
	h.closed, h.reason = true, reason
	h.gop, h.gopBytes, h.config = nil, 0, nil
	for v := range h.viewers {
		v.queue, v.head, v.bytes = nil, 0, 0
	}
	h.viewers = nil
	close(h.done)
}

// Done is closed by Close.
func (h *Hub) Done() <-chan struct{} { return h.done }

// Reason is what Close was given; nil until then.
func (h *Hub) Reason() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reason
}

// hubViewer is one viewer's queue. All fields but wake are guarded by Hub.mu.
type hubViewer struct {
	h       *Hub
	wake    chan struct{}
	queue   []Packet
	head    int
	bytes   int  // Data bytes in queue[head:]
	waiting bool // dropping packets until the next key frame
	closed  bool
}

func (v *hubViewer) push(p Packet) {
	v.queue = append(v.queue, p)
	v.bytes += len(p.Data)
	select {
	case v.wake <- struct{}{}:
	default:
	}
}

// reset empties the queue and has the viewer wait for a key frame.
func (v *hubViewer) reset() {
	v.queue, v.head, v.bytes = nil, 0, 0
	v.waiting = true
}

// Next implements Viewer.
func (v *hubViewer) Next(ctx context.Context) (Packet, error) {
	h := v.h
	for {
		if err := ctx.Err(); err != nil {
			return Packet{}, err
		}
		h.mu.Lock()
		switch {
		case v.closed:
			h.mu.Unlock()
			return Packet{}, ErrViewerClosed
		case h.closed:
			err := h.reason
			h.mu.Unlock()
			return Packet{}, err
		case v.head < len(v.queue):
			p := v.queue[v.head]
			v.queue[v.head] = Packet{}
			v.head++
			v.bytes -= len(p.Data)
			if v.head == len(v.queue) {
				v.queue, v.head = v.queue[:0], 0
			} else if v.head >= 64 && v.head*2 >= len(v.queue) {
				n := copy(v.queue, v.queue[v.head:])
				clear(v.queue[n:])
				v.queue, v.head = v.queue[:n], 0
			}
			h.mu.Unlock()
			return p, nil
		}
		h.mu.Unlock()
		select {
		case <-v.wake:
		case <-ctx.Done():
			return Packet{}, ctx.Err()
		case <-h.done:
		}
	}
}

// Close implements Viewer. It is safe to call more than once.
func (v *hubViewer) Close() {
	h := v.h
	h.mu.Lock()
	if !v.closed {
		v.closed = true
		delete(h.viewers, v) // a no-op on a nil map
		v.queue, v.head, v.bytes = nil, 0, 0
	}
	h.mu.Unlock()
	select {
	case v.wake <- struct{}{}:
	default:
	}
}
