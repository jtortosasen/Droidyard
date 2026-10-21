package hosting

import (
	"context"
	"encoding/binary"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The acknowledgements of a peer screen socket (ADR-0118 §3): the client tells the host how
// many video bytes it has received, so the host keeps a bounded amount in flight.
const (
	// ackEvery is the bytes received between two acknowledgements, at most.
	ackEvery = 64 << 10
	// ackInterval is the time between two while bytes arrive, at most.
	ackInterval = 100 * time.Millisecond
	ackBound    = time.Second
)

// ackSender counts the bytes the socket's reader received and writes [4][u32 total mod 2^32]
// at least every ackEvery bytes and every ackInterval while they arrive. The reader calls
// received; a timer flushes what a pause in the stream leaves unacknowledged.
type ackSender struct {
	c *websocket.Conn

	mu      sync.Mutex
	total   uint32 // bytes received, mod 2^32
	sent    uint32 // the total last acknowledged
	last    time.Time
	timer   *time.Timer
	stopped bool
}

func (a *ackSender) received(n int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.total += uint32(n)
	if a.stopped || a.total == a.sent {
		return
	}
	if a.total-a.sent >= ackEvery || time.Since(a.last) >= ackInterval {
		a.flushLocked()
		return
	}
	if a.timer == nil {
		a.timer = time.AfterFunc(ackInterval, func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.timer = nil
			if !a.stopped && a.total != a.sent {
				a.flushLocked()
			}
		})
	}
}

// flushLocked writes the acknowledgement. A write that fails is the socket's end, which the
// reader hears itself.
func (a *ackSender) flushLocked() {
	frame := binary.BigEndian.AppendUint32([]byte{capability.PeerScreenAck}, a.total)
	a.sent, a.last = a.total, time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), ackBound)
	defer cancel()
	_ = a.c.Write(ctx, websocket.MessageBinary, frame)
}

func (a *ackSender) stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopped = true
	if a.timer != nil {
		a.timer.Stop()
	}
}

// APIAtLeast says whether the API version have (major.minor.patch) is want or later; a
// version that is not three numbers is not at least anything.
func APIAtLeast(have, want string) bool {
	h, w := apiParts(have), apiParts(want)
	if h == nil || w == nil {
		return false
	}
	for i := range h {
		if h[i] != w[i] {
			return h[i] > w[i]
		}
	}
	return true
}

func apiParts(v string) []int {
	f := strings.Split(v, ".")
	if len(f) != 3 {
		return nil
	}
	out := make([]int, 3)
	for i, p := range f {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil
		}
		out[i] = n
	}
	return out
}
