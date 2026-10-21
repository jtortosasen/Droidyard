package httpapi

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The window of a peer screen socket (ADR-0118 §3): the client acknowledges the video bytes it
// received, and the host keeps at most peerWindow sent and not acknowledged, so a slow link's
// backlog forms in the Hub, where the age bound acts, not in the kernel's buffers. The local
// /v1/screen has none: its browser is on the same machine.

// peerWindow is the most bytes the host keeps sent and unacknowledged per peer socket
// (working default, ADR-0118 §3).
const peerWindow = 256 << 10

// maxAcksPerSecond is how many acknowledgements a client may send in a second before its
// socket closes 1008. A client sends about ten.
const maxAcksPerSecond = 100

var errAckRefused = errors.New("httpapi: the acknowledgement is refused")

// ackWindow counts the bytes sent on one peer socket and the client's acknowledgement of them.
// It holds nothing until the first acknowledgement: a client that never acknowledges (an older
// one) is sent as before.
type ackWindow struct {
	mu     sync.Mutex
	sent   uint32 // bytes sent, mod 2^32
	acked  uint32 // the client's last acknowledgement
	on     bool   // an acknowledgement has arrived
	wake   chan struct{}
	stamps []time.Time // the last acknowledgements' arrival times, for the rate bound
	now    func() time.Time
}

func newAckWindow() *ackWindow {
	return &ackWindow{wake: make(chan struct{}, 1), now: time.Now}
}

// sentBytes records n bytes written to the socket.
func (w *ackWindow) sentBytes(n int) {
	w.mu.Lock()
	w.sent += uint32(n)
	w.mu.Unlock()
}

// ack takes one acknowledgement frame. It refuses a frame that is not a well-formed
// acknowledgement, one that acknowledges more than was sent, and one that comes over the rate
// bound.
func (w *ackWindow) ack(frame []byte) error {
	if len(frame) != 5 || frame[0] != capability.PeerScreenAck {
		return errAckRefused
	}
	n := binary.BigEndian.Uint32(frame[1:])
	now := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stamps = append(w.stamps, now)
	if len(w.stamps) > maxAcksPerSecond+1 {
		w.stamps = w.stamps[1:]
	}
	if len(w.stamps) == maxAcksPerSecond+1 {
		// The 101st ack inside a second is more than 100 in it.
		if now.Sub(w.stamps[0]) < time.Second {
			return errAckRefused
		}
	}
	if w.sent-n > w.sent-w.acked && w.on || w.sent-n > 1<<31 {
		return errAckRefused // behind the last one, or ahead of what was sent
	}
	w.acked, w.on = n, true
	select {
	case w.wake <- struct{}{}:
	default:
	}
	return nil
}

// wait blocks while the window is full, until an acknowledgement frees it or ctx ends.
func (w *ackWindow) wait(ctx context.Context) error {
	for {
		w.mu.Lock()
		full := w.on && w.sent-w.acked >= peerWindow
		w.mu.Unlock()
		if !full {
			return nil
		}
		select {
		case <-w.wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
